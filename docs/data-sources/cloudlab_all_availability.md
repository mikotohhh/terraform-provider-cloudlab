---
page_title: "cloudlab_all_availability Data Source - terraform-provider-cloudlab"
description: |-
  Surveys the earliest reservation availability across every node type CloudLab offers.
---

# cloudlab_all_availability (Data Source)

Surveys the **earliest reservation availability across every allocatable physical
node type** CloudLab offers, for a requested duration.

This data source runs an external `discover_command` to obtain the full list of
`(cluster, node type)` pairs, then performs a Portal reservation search
(`POST /resgroups/search`) for the requested duration **once per type**. The
included discovery script uses CloudLab's central Portal inventory as its fast
path and strict GENI AM advertisement RSpecs as a fallback.

For a single known set of node types, prefer
[`cloudlab_availability`](cloudlab_availability.md), which needs only the API token.

Searches are independent and therefore require one Portal API request per
selected type. Searches run concurrently (default `search_concurrency = 16`).
The Portal handles overlapping in-flight predictions, but requests using the
same token and launched too close together race in its session layer and can
fail with a transient `401 "No such user"`. The provider therefore spaces
search starts by 200 ms by default and retries that specific 401 with
exponential backoff plus random jitter. Read-only searches also retry transient
Portal 5xx responses; `406 No fit` remains a normal per-type result.

~> **Dependencies.** This data source shells out to `discover_command`. The
reference script's Portal fast path needs only Python. Strict GENI mode and the
automatic fallback additionally need `geni-lib`, an Emulab certificate, and
the `CLOUDLAB_PASS` certificate-key passphrase.

## Discovery contract

`discover_command` must print `(aggregate, node type)` objects to **stdout**,
in either of two formats:

- **one JSON array** (legacy), emitted when discovery finishes, or
- **a stream of newline-delimited JSON objects**. The provider starts each
  row's Portal reservation search as soon as it arrives. The reference script
  enables this format with `--stream`; in strict GENI mode, aggregates can then
  overlap their downloads with reservation searches.

```json
[
  {
    "cluster": "Wisconsin",
    "urn": "urn:publicid:IDN+wisc.cloudlab.us+authority+cm",
    "node_type": "d8545",
    "free": 3,
    "total": 10
  }
]
```

A reference implementation is provided in the repository at
`examples/data-sources/cloudlab_all_availability/cloudlab_nodetypes.py`. Anything
that emits the same JSON shape works (a cache file via `cat`, a different
inventory tool, etc.). The reference script only emits nodes advertising the
GENI `raw-pc` sliver type, which excludes switch, Internet, and interconnect
pseudo-nodes that the reservation API cannot schedule. Pass
`--include-noncompute` only for discovery diagnostics.

The default `--source auto` mode queries the central Portal once and returns
Utah, Wisconsin, Clemson, Apt, Emulab, and Mass. This normally completes in
about two seconds and avoids direct access to the Utah GENI port. If the Portal
request fails or omits an aggregate, the script falls back to GENI for sites
supported by geni-lib. `--source portal` disables fallback; `--source geni`
uses strict raw-PC RSpec discovery and defaults to Utah, Wisconsin, Clemson,
and Apt. GENI aggregates run in parallel under a shared deadline (`--timeout`,
default 30 s) and per-aggregate TCP connect timeout (`--connect-timeout`,
default 5 s).
If some aggregates fail but at least one succeeds, Terraform returns the
successful results together with a warning; if all fail, discovery fails
instead of reporting an apparently valid empty survey.

Two ready-made options ship next to the script:

- `run_discovery.sh` — wraps the discovery script. The Portal fast path is
  token- and certificate-free; the wrapper pulls a GENI fallback passphrase
  from the macOS Keychain when one is available.
- `gpu_node_types.json` — a curated static inventory of every GPU node type
  (P100/V100/V100S/A30/A100/GH200 across Utah, Wisconsin and Clemson). Using it
  via `["cat", ".../gpu_node_types.json"]` needs nothing but the API token; its
  `free`/`total` values are placeholders, so leave `only_with_free` unset.

## Example Usage

```terraform
data "cloudlab_all_availability" "survey" {
  project        = "YourProject"
  duration_hours = 168 # 7 days

  discover_command = ["python3", "/Users/me/cloudlab_nodetypes.py", "--stream"]

  # Optional: skip types with nothing currently free.
  only_with_free = true

  # Optional: lower to 1 to run searches strictly serially (default 16).
  # search_concurrency = 1

  # Optional: conservative launch spacing; valid range is 200..2000.
  # search_launch_interval_ms = 200

  # Optional: restrict to specific hardware types.
  # only_node_types = ["d8545", "c4130", "r7525"]
}

# Earliest start per node type
output "availability" {
  value = {
    for r in data.cloudlab_all_availability.survey.results :
    "${r.cluster}/${r.node_type}" => r.start_at
  }
}
```

## Schema

### Required

- `project` (String) — The CloudLab project the reservations would belong to.
- `duration_hours` (Number) — How long each reservation is needed, in hours.
- `discover_command` (List of String) — Command (program + args) to execute for node-type discovery. Must print the JSON array or object stream described above to stdout. Example: `["python3", "/Users/me/cloudlab_nodetypes.py", "--stream"]`.

### Optional

- `group` (String) — The project subgroup.
- `only_node_types` (List of String) — Allow-list of node types to search; others are skipped.
- `only_with_free` (Boolean) — If `true`, only search node types reporting at least one free node (`free > 0`). Default `false`.
- `search_concurrency` (Number) — Maximum reservation searches in flight at once. Valid range `1..16`; default `16`.
- `search_launch_interval_ms` (Number) — Minimum spacing between search starts. Valid range `200..2000`; default `200`. Values below 200 are rejected because live tests triggered the Portal token-session race.

### Read-Only

- `results` (List of Object) — One entry per searched node type:
  - `cluster` (String) — Cluster name from discovery.
  - `urn` (String) — Aggregate URN.
  - `node_type` (String) — Hardware node type.
  - `free` (Number) — Nodes free right now (from discovery).
  - `total` (Number) — Total nodes of this type (from discovery).
  - `start_at` (String) — Earliest reservable start (RFC3339); empty if the search failed.
  - `expires_at` (String) — When that reservation would expire; empty if the search failed.
  - `error` (String) — Search error for this type, if any (e.g. no window found).
