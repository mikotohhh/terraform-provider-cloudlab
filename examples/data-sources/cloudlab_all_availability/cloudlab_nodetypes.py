#!/usr/bin/env python3
"""Enumerate CloudLab/Emulab node types via GENI AM listresources and emit JSON.

This is the discovery backend for the Terraform `cloudlab_all_availability`
data source. It prints a JSON array to stdout, one object per (aggregate,
hardware_type):

    [{"cluster": "...", "urn": "urn:publicid:IDN+...+authority+cm",
      "node_type": "d8545", "free": 3, "total": 10}, ...]

The `urn` is taken straight from each node's component_manager_id in the
advertisement RSpec, so it is exactly the aggregate URN the Portal reservation
search expects (no hand-maintained cluster->URN table).

Passphrase comes from env CLOUDLAB_PASS (your emulab login passphrase for the
cert key). All human-readable logging goes to stderr so stdout stays pure JSON.
"""
import json
import os
import sys
import xml.etree.ElementTree as ET
from collections import defaultdict

import geni.aggregate.cloudlab as cl
import geni.aggregate.frameworks as fw
from geni.aggregate.context import Context
from geni.aggregate.user import User

CERT = os.environ.get("CLOUDLAB_CERT", "/Users/mikoto/Downloads/cloudlab (1).pem")
PASS = os.environ.get("CLOUDLAB_PASS")
if not PASS:
    sys.exit("set CLOUDLAB_PASS env var (your emulab login passphrase for the cert key)")

USER_URN = "urn:publicid:IDN+emulab.net+user+lzhou247"
USER_NAME = "lzhou247"

# emulab.net-issued cert -> ProtoGENI framework (https://www.emulab.net:12369)
ctx = Context()
cf = fw.ProtoGENI()
cf.cert = CERT
cf.setKey(CERT, PASS.encode())
cf._sa = "https://www.emulab.net:12369/protogeni/xmlrpc/sa"  # ProtoGENI leaves _sa=None
# geni-lib writes the user cred in binary mode but pgch1 returns str -> coerce to bytes
_orig_getuc = cf.getUserCredentials
def _getuc(urn=None, _o=_orig_getuc):
    r = _o(urn)
    return r.encode("utf-8") if isinstance(r, str) else r
cf.getUserCredentials = _getuc
ctx.cf = cf
u = User(); u.name = USER_NAME; u.urn = USER_URN
ctx.addUser(u)

# CloudLab aggregates to probe.
AGGS = {
    "Utah": cl.Utah, "Wisconsin": cl.Wisconsin, "Clemson": cl.Clemson,
    "Apt": cl.Apt, "UtahDDC": cl.UtahDDC,
}

NS = {"r": "http://www.geni.net/resources/rspec/3"}


def normalize_urn(cm_id):
    """Some RSpecs use '...+cm'; the reservation API wants '...+authority+cm'."""
    if cm_id and cm_id.endswith("+cm") and "+authority+cm" not in cm_id:
        return cm_id[: -len("+cm")] + "+authority+cm"
    return cm_id


def collect(rspec_text, fallback_cluster):
    """Return {(urn, node_type): [free, total]} for one advertisement RSpec."""
    acc = defaultdict(lambda: [0, 0])  # key -> [free, total]
    root = ET.fromstring(rspec_text)
    for node in root.findall(".//r:node", NS):
        hts = [h.get("name") for h in node.findall("r:hardware_type", NS)]
        ht = hts[0] if hts else "(unknown)"
        cm = normalize_urn(node.get("component_manager_id") or "")
        key = (cm, ht)
        acc[key][1] += 1  # total
        av = node.find("r:available", NS)
        if av is not None and av.get("now") == "true":
            acc[key][0] += 1  # free
    return acc


out = []
for name, agg in AGGS.items():
    try:
        m = agg.listresources(ctx)  # no slice -> advertisement rspec
        text = m.text if hasattr(m, "text") else str(m)
        for (urn, ht), (free, total) in collect(text, name).items():
            out.append({
                "cluster": name,
                "urn": urn,
                "node_type": ht,
                "free": free,
                "total": total,
            })
    except Exception as e:  # noqa: BLE001 - report and continue to next aggregate
        print(f"WARN: {name}: {repr(e)[:160]}", file=sys.stderr)

json.dump(out, sys.stdout)
print(file=sys.stderr)
print(f"discovered {len(out)} (aggregate, node_type) pairs", file=sys.stderr)
