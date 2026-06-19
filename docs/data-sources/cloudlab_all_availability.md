---
page_title: "cloudlab_all_availability Data Source - terraform-provider-cloudlab"
description: |-
  Surveys the earliest reservation availability across every node type CloudLab offers.
---

# cloudlab_all_availability (Data Source)

Surveys the **earliest reservation availability across every node type** CloudLab
offers, for a requested duration.

Because the Portal REST API cannot *enumerate* node types, this data source runs
an external `discover_command` (typically a geni-lib script that reads the GENI AM
advertisement RSpec) to obtain the full list of `(cluster, node type)` pairs, then
performs a Portal reservation search (`POST /resgroups/search`) for the requested
duration **once per type**.

For a single known set of node types, prefer
[`cloudlab_availability`](cloudlab_availability.md), which needs only the API token.

~> **Dependencies.** This data source shells out to `discover_command`. Wherever
Terraform runs, that command and its dependencies must be available — for the
reference script that means Python, `geni-lib`, an Emulab certificate, and the
`CLOUDLAB_PASS` environment variable (the cert key passphrase).

## Discovery contract

`discover_command` must print a JSON array to **stdout**, one object per
`(aggregate, node type)`:

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
inventory tool, etc.).

## Example Usage

```terraform
data "cloudlab_all_availability" "survey" {
  project        = "YourProject"
  duration_hours = 168 # 7 days

  discover_command = ["python3", "/Users/me/cloudlab_nodetypes.py"]

  # Optional: skip types with nothing currently free.
  only_with_free = true

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
- `discover_command` (List of String) — Command (program + args) to execute for node-type discovery. Must print the JSON array described above to stdout. Example: `["python3", "/Users/me/cloudlab_nodetypes.py"]`.

### Optional

- `group` (String) — The project subgroup.
- `only_node_types` (List of String) — Allow-list of node types to search; others are skipped.
- `only_with_free` (Boolean) — If `true`, only search node types reporting at least one free node (`free > 0`). Default `false`.

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
