#!/usr/bin/env python3
"""Discover allocatable CloudLab node types from the CloudLab Portal or GENI.

The default output is the legacy JSON array consumed by
``cloudlab_all_availability``.  The fast path reads the Portal's central
aggregate status over HTTPS; GENI advertisement RSpecs remain available as a
strict fallback.  Switch, Internet, interconnect, and infrastructure-only
pseudo-node types are excluded by default.

All human-readable warnings go to stderr so stdout remains pure JSON.
"""

import argparse
import json
import os
import sys
import threading
import time
import urllib.parse
import urllib.request
import warnings
import xml.etree.ElementTree as ET
from collections import defaultdict
from queue import Empty, Queue

# urllib3 emits this on the system Python shipped with older macOS releases.
# It is unrelated to discovery success and would otherwise be mistaken for a
# partial-discovery warning by the Terraform data source.
warnings.filterwarnings(
    "ignore",
    message=r"urllib3 v2 only supports OpenSSL 1\.1\.1\+.*",
)

DEFAULT_CERT = os.path.expanduser("~/Downloads/cloudlab (2).pem")
DEFAULT_PORTAL_URL = "https://www.cloudlab.us/server-ajax.php"
PORTAL_AGGREGATES = {
    "Utah": "urn:publicid:IDN+utah.cloudlab.us+authority+cm",
    "Wisconsin": "urn:publicid:IDN+wisc.cloudlab.us+authority+cm",
    "Clemson": "urn:publicid:IDN+clemson.cloudlab.us+authority+cm",
    "Apt": "urn:publicid:IDN+apt.emulab.net+authority+cm",
    "Emulab": "urn:publicid:IDN+emulab.net+authority+cm",
    "Mass": "urn:publicid:IDN+cloudlab.umass.edu+authority+cm",
}
DEFAULT_PORTAL_AGGREGATES = tuple(PORTAL_AGGREGATES)
DEFAULT_GENI_AGGREGATES = ("Utah", "Wisconsin", "Clemson", "Apt")
GENI_AGGREGATE_NAMES = DEFAULT_GENI_AGGREGATES + ("UtahDDC",)
USER_URN = "urn:publicid:IDN+emulab.net+user+lzhou247"
USER_NAME = "lzhou247"

NS = {"r": "http://www.geni.net/resources/rspec/3"}


def normalize_urn(cm_id):
    """Normalize a component-manager ID for the Portal reservation API."""
    if cm_id and cm_id.endswith("+cm") and "+authority+cm" not in cm_id:
        return cm_id[: -len("+cm")] + "+authority+cm"
    return cm_id


def is_compute_node(node):
    """Return true when an RSpec node can be allocated as a physical host."""
    return any(
        sliver.get("name") == "raw-pc"
        for sliver in node.findall("r:sliver_type", NS)
    )


def collect(rspec_text, include_noncompute=False):
    """Return ``{(urn, node_type): [free, total]}`` for one RSpec.

    ``include_noncompute`` exists for diagnostics and backwards compatibility.
    Normal availability surveys should leave it disabled.
    """
    acc = defaultdict(lambda: [0, 0])
    root = ET.fromstring(rspec_text)
    for node in root.findall(".//r:node", NS):
        if not include_noncompute and not is_compute_node(node):
            continue

        hardware_types = [
            hardware.get("name")
            for hardware in node.findall("r:hardware_type", NS)
            if hardware.get("name")
        ]
        if not hardware_types:
            continue

        cm_id = normalize_urn(node.get("component_manager_id") or "")
        if not cm_id:
            continue

        key = (cm_id, hardware_types[0])
        acc[key][1] += 1
        available = node.find("r:available", NS)
        if available is not None and available.get("now") == "true":
            acc[key][0] += 1
    return acc


def is_portal_compute_type(node_type, attributes):
    """Return true for Portal node types that represent compute hosts.

    Portal ``typeinfo`` contains a small number of switch and infrastructure
    types in addition to the raw-PC inventory.  Real compute types have CPU
    architecture/core attributes; the one extra type observed at Wisconsin is
    explicitly named ``*-infra`` and is not advertised as a GENI raw PC.
    """
    if not isinstance(attributes, dict) or node_type.endswith("-infra"):
        return False
    try:
        return bool(attributes.get("architecture")) and int(
            attributes.get("hw_cpu_cores", 0)
        ) > 0
    except (TypeError, ValueError):
        return False


def parse_portal_inventory(payload, names, include_noncompute=False):
    """Parse ``GetHealthStatusExtended`` into discovery rows."""
    if not isinstance(payload, dict) or payload.get("code") not in (0, "0"):
        raise ValueError("Portal status response did not report success")
    value = payload.get("value")
    if not isinstance(value, list) or len(value) < 2 or not isinstance(value[1], dict):
        raise ValueError("Portal status response has no aggregate inventory")

    aggregate_info = value[1]
    rows = []
    errors = {}
    for name in names:
        urn = PORTAL_AGGREGATES.get(name)
        if urn is None:
            errors[name] = "not available from the Portal inventory"
            continue
        info = aggregate_info.get(urn)
        if not isinstance(info, dict):
            errors[name] = "aggregate missing from the Portal inventory"
            continue
        typeinfo = info.get("typeinfo")
        typelist = info.get("typelist")
        if not isinstance(typeinfo, dict) or not isinstance(typelist, dict):
            errors[name] = "aggregate has no node-type inventory"
            continue

        aggregate_rows = []
        for node_type, counts in typeinfo.items():
            if not isinstance(node_type, str) or not isinstance(counts, dict):
                raise ValueError(f"invalid node-type entry for {name}")
            if not include_noncompute and not is_portal_compute_type(
                node_type, typelist.get(node_type)
            ):
                continue
            try:
                free = int(counts["free"])
                total = int(counts["count"])
            except (KeyError, TypeError, ValueError) as exc:
                raise ValueError(f"invalid counts for {name}/{node_type}") from exc
            if free < 0 or total < 0 or free > total:
                raise ValueError(
                    f"invalid counts for {name}/{node_type}: free={free}, total={total}"
                )
            aggregate_rows.append(
                {
                    "cluster": name,
                    "urn": urn,
                    "node_type": node_type,
                    "free": free,
                    "total": total,
                }
            )
        if not aggregate_rows:
            errors[name] = "aggregate has no allocatable node types"
            continue
        rows.extend(aggregate_rows)

    rows.sort(key=lambda row: (row["cluster"], row["urn"], row["node_type"]))
    return rows, errors


def discover_portal(
    names,
    portal_url=DEFAULT_PORTAL_URL,
    timeout=5.0,
    include_noncompute=False,
):
    """Fetch the central Portal node-type inventory over HTTPS."""
    supported = [name for name in names if name in PORTAL_AGGREGATES]
    errors = {
        name: "not available from the Portal inventory"
        for name in names
        if name not in PORTAL_AGGREGATES
    }
    if not supported:
        return [], errors

    form = urllib.parse.urlencode(
        {
            "ajax_route": "frontpage",
            "ajax_method": "GetHealthStatusExtended",
            "ajax_args[noargs]": "noargs",
        }
    ).encode("ascii")
    request = urllib.request.Request(
        portal_url,
        data=form,
        headers={
            "Accept": "application/json",
            "Content-Type": "application/x-www-form-urlencoded",
            "User-Agent": "terraform-provider-cloudlab/node-discovery",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            payload = json.load(response)
        rows, parse_errors = parse_portal_inventory(
            payload, supported, include_noncompute=include_noncompute
        )
        errors.update(parse_errors)
        return rows, errors
    except Exception as exc:  # noqa: BLE001 - auto mode falls back to GENI
        reason = f"{type(exc).__name__}: {str(exc)[:200]}"
        errors.update({name: reason for name in supported})
        return [], errors


def build_context(cert, passphrase):
    """Build the geni-lib context lazily so parser tests need no geni-lib."""
    import geni.aggregate.frameworks as fw
    from geni.aggregate.context import Context
    from geni.aggregate.user import User

    context = Context()
    framework = fw.ProtoGENI()
    framework.cert = cert
    framework.setKey(cert, passphrase.encode())
    framework._sa = "https://www.emulab.net:12369/protogeni/xmlrpc/sa"

    # geni-lib writes the user credential in binary mode, while pgch1 can
    # return str. Coerce it without modifying the installed dependency.
    original_get_credentials = framework.getUserCredentials

    def get_credentials(urn=None):
        result = original_get_credentials(urn)
        return result.encode("utf-8") if isinstance(result, str) else result

    framework.getUserCredentials = get_credentials
    context.cf = framework
    user = User()
    user.name = USER_NAME
    user.urn = USER_URN
    context.addUser(user)
    return context


def aggregate_catalog():
    """Return the supported aggregate name to geni-lib object mapping."""
    import geni.aggregate.cloudlab as cloudlab

    return {
        "Utah": cloudlab.Utah,
        "Wisconsin": cloudlab.Wisconsin,
        "Clemson": cloudlab.Clemson,
        "Apt": cloudlab.Apt,
        # This legacy endpoint is no longer queried by default, but remains
        # available for installations where it is still reachable.
        "UtahDDC": cloudlab.UtahDDC,
    }


def parse_args(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--source",
        choices=("auto", "portal", "geni"),
        default="auto",
        help="node inventory source: fast Portal inventory with GENI fallback "
        "(auto, default), Portal only, or strict GENI RSpec discovery",
    )
    parser.add_argument(
        "--aggregates",
        nargs="+",
        default=None,
        metavar="NAME",
        help="aggregates to query (default: all six CloudLab aggregates for "
        "auto/portal; Utah, Wisconsin, Clemson, and Apt for geni)",
    )
    parser.add_argument(
        "--include-noncompute",
        action="store_true",
        help="include switch and other pseudo-node types for diagnostics",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=30.0,
        metavar="SECONDS",
        help="deadline for all aggregate queries, which run in parallel "
        "(default: %(default)s). Aggregates still pending at the deadline "
        "are reported as failed; an unreachable one (e.g. a hanging DNS "
        "lookup) therefore cannot stall the whole survey.",
    )
    parser.add_argument(
        "--portal-url",
        default=DEFAULT_PORTAL_URL,
        metavar="URL",
        help="CloudLab Portal AJAX endpoint used for central inventory "
        "(default: %(default)s)",
    )
    parser.add_argument(
        "--portal-timeout",
        type=float,
        default=5.0,
        metavar="SECONDS",
        help="Portal inventory request timeout (default: %(default)s)",
    )
    parser.add_argument(
        "--connect-timeout",
        type=float,
        default=5.0,
        metavar="SECONDS",
        help="TCP connect timeout per aggregate (default: %(default)s). A "
        "cluster whose AM endpoint drops packets fails this fast instead of "
        "riding out the full --timeout deadline.",
    )
    parser.add_argument(
        "--stream",
        action="store_true",
        help="emit rows as newline-delimited JSON objects instead of one array "
        "at the end. In strict GENI mode, each aggregate is flushed when it "
        "answers so the provider can overlap discovery and searches.",
    )
    parser.add_argument("--verbose", action="store_true")
    return parser.parse_args(argv)


def discover_aggregates(names, catalog, context, timeout, on_rspec=None):
    """Fetch advertisement RSpecs from all aggregates in parallel.

    Returns ``(rspecs, errors)``: ``{name: rspec_text}`` for aggregates that
    answered within ``timeout`` seconds, ``{name: reason}`` for the rest.
    ``on_rspec(name, text)``, when given, is invoked by the coordinating thread
    as soon as that aggregate answers; if it raises, the aggregate is counted
    as failed. Workers only publish into a queue, so a late daemon worker cannot
    mutate returned dictionaries or emit output after the deadline.
    """
    rspecs = {}
    errors = {}
    completions = Queue()

    def worker(name):
        try:
            manifest = catalog[name].listresources(context)
            text = manifest.text if hasattr(manifest, "text") else str(manifest)
            completions.put((name, text, None))
        except Exception as exc:  # noqa: BLE001 - best-effort multi-site query
            completions.put((name, None, repr(exc)[:240]))

    threads = [
        threading.Thread(target=worker, args=(name,), daemon=True,
                         name=f"discover-{name}")
        for name in names
    ]
    for thread in threads:
        thread.start()
    deadline = time.monotonic() + timeout
    pending = set(names)
    while pending:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        try:
            name, text, error = completions.get(timeout=remaining)
        except Empty:
            break
        if name not in pending:
            continue
        pending.remove(name)
        if error is not None:
            errors[name] = error
            continue
        try:
            if on_rspec is not None:
                on_rspec(name, text)
            rspecs[name] = text
        except Exception as exc:  # noqa: BLE001 - callback is part of discovery
            errors[name] = repr(exc)[:240]
    for name in pending:
        errors[name] = f"timed out after {timeout:g}s"
    return rspecs, errors


def rows_for_rspec(name, rspec_text, include_noncompute=False):
    """Convert one GENI advertisement RSpec to discovery rows."""
    rows = [
        {
            "cluster": name,
            "urn": urn,
            "node_type": node_type,
            "free": free,
            "total": total,
        }
        for (urn, node_type), (free, total) in collect(
            rspec_text, include_noncompute=include_noncompute
        ).items()
    ]
    rows.sort(key=lambda row: (row["cluster"], row["urn"], row["node_type"]))
    return rows


def main(argv=None):
    args = parse_args(argv)
    names = args.aggregates
    if names is None:
        names = list(
            DEFAULT_GENI_AGGREGATES
            if args.source == "geni"
            else DEFAULT_PORTAL_AGGREGATES
        )
    unknown = sorted(set(names) - set(PORTAL_AGGREGATES) - set(GENI_AGGREGATE_NAMES))
    if unknown:
        print(
            "unknown aggregate(s): " + ", ".join(unknown),
            file=sys.stderr,
        )
        return 1
    output = []
    emitted_count = 0
    successful = set()
    errors = {}

    def accept_rows(rows):
        nonlocal emitted_count
        if args.stream:
            for row in rows:
                print(json.dumps(row))
            sys.stdout.flush()
        else:
            output.extend(rows)
        emitted_count += len(rows)

    fallback_names = list(names) if args.source == "geni" else []
    if args.source in ("auto", "portal"):
        portal_rows, portal_errors = discover_portal(
            names,
            portal_url=args.portal_url,
            timeout=args.portal_timeout,
            include_noncompute=args.include_noncompute,
        )
        accept_rows(portal_rows)
        errors.update(portal_errors)
        successful.update(set(names) - set(portal_errors))
        if args.source == "auto":
            fallback_names = [name for name in names if name in portal_errors]

    if fallback_names:
        catalog = aggregate_catalog()
        geni_names = [name for name in fallback_names if name in catalog]
        for name in fallback_names:
            if name not in catalog:
                previous = errors.get(name)
                suffix = "not supported by geni-lib"
                errors[name] = f"Portal: {previous}; GENI: {suffix}" if previous else suffix

        if geni_names:
            passphrase = os.environ.get("CLOUDLAB_PASS")
            if not passphrase:
                for name in geni_names:
                    previous = errors.get(name)
                    suffix = "CLOUDLAB_PASS is not set"
                    errors[name] = (
                        f"Portal: {previous}; GENI: {suffix}" if previous else suffix
                    )
            else:
                cert = os.environ.get("CLOUDLAB_CERT", DEFAULT_CERT)
                context = build_context(cert, passphrase)

                # geni-lib forwards HTTP.TIMEOUT directly to requests, which
                # accepts a (connect, read) tuple.
                from geni.minigcf import config as minigcf_config

                minigcf_config.HTTP.TIMEOUT = (args.connect_timeout, args.timeout)

                try:
                    # Fetch/cache once; concurrent first touches race on disk.
                    context.usercred_path  # noqa: B018 - side-effectful property
                except Exception as exc:  # noqa: BLE001
                    for name in geni_names:
                        previous = errors.get(name)
                        suffix = f"user credential: {repr(exc)[:200]}"
                        errors[name] = (
                            f"Portal: {previous}; GENI: {suffix}"
                            if previous
                            else suffix
                        )
                else:
                    def on_rspec(name, rspec_text):
                        accept_rows(
                            rows_for_rspec(
                                name,
                                rspec_text,
                                include_noncompute=args.include_noncompute,
                            )
                        )

                    rspecs, geni_errors = discover_aggregates(
                        geni_names,
                        catalog,
                        context,
                        args.timeout,
                        on_rspec=on_rspec if args.stream else None,
                    )
                    for name in geni_names:
                        if name in geni_errors:
                            previous = errors.get(name)
                            suffix = geni_errors[name]
                            errors[name] = (
                                f"Portal: {previous}; GENI: {suffix}"
                                if previous
                                else suffix
                            )
                            continue
                        if not args.stream:
                            accept_rows(
                                rows_for_rspec(
                                    name,
                                    rspecs[name],
                                    include_noncompute=args.include_noncompute,
                                )
                            )
                        successful.add(name)
                        errors.pop(name, None)

    failed_aggregates = [name for name in names if name not in successful]
    for name in failed_aggregates:
        print(f"WARN: {name}: {errors.get(name, 'discovery failed')}", file=sys.stderr)

    if not successful:
        print("all aggregate discovery requests failed", file=sys.stderr)
        return 1

    if not args.stream:
        output.sort(key=lambda row: (row["cluster"], row["urn"], row["node_type"]))
        json.dump(output, sys.stdout)
    if args.verbose:
        print(
            f"discovered {emitted_count} node-type pairs from "
            f"{len(successful)} aggregate(s); source={args.source}; "
            f"failed={','.join(failed_aggregates) or 'none'}",
            file=sys.stderr,
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
