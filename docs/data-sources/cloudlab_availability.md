---
page_title: "cloudlab_availability Data Source - terraform-provider-cloudlab"
description: |-
  Finds the earliest time slot where a set of nodes can be reserved for a given duration.
---

# cloudlab_availability (Data Source)

Finds the **earliest time slot** where a set of nodes can be reserved for a given
duration. Wraps the CloudLab Portal reservation search (`POST /resgroups/search`):
given one or more node types and a duration in hours, it returns the single
earliest window (`start_at`..`expires_at`) where the **entire requested group**
can be scheduled together.

Use this to answer *"I want N nodes of type X for 7 days — when is the earliest I
can start?"*. An error is returned if no window can accommodate the request.

This data source only needs the provider's API `token` — no certificate or
external tooling. To survey **all** node types at once instead of a specific set,
see [`cloudlab_all_availability`](cloudlab_all_availability.md).

## Example Usage

### Earliest 7-day window for one GPU node type

```terraform
data "cloudlab_availability" "gpu_7d" {
  project        = "YourProject"
  duration_hours = 168 # 7 days

  node_types = [{
    urn       = "urn:publicid:IDN+wisc.cloudlab.us+authority+cm"
    node_type = "d8545"
    count     = 1
  }]
}

output "earliest_start" {
  value = data.cloudlab_availability.gpu_7d.start_at
}

output "would_expire" {
  value = data.cloudlab_availability.gpu_7d.expires_at
}
```

### A heterogeneous group scheduled together

```terraform
data "cloudlab_availability" "cluster_2d" {
  project        = "YourProject"
  duration_hours = 48

  node_types = [
    {
      urn       = "urn:publicid:IDN+clemson.cloudlab.us+authority+cm"
      node_type = "c4130"
      count     = 2
    },
    {
      urn       = "urn:publicid:IDN+wisc.cloudlab.us+authority+cm"
      node_type = "d8545"
      count     = 1
    },
  ]
}
```

## Schema

### Required

- `project` (String) — The CloudLab project the reservation would belong to.
- `duration_hours` (Number) — How long the reservation is needed, in hours (e.g. `168` for 7 days).
- `node_types` (List of Object) — The node types to reserve. The returned window fits all of them together. Each entry:
  - `urn` (String, Required) — The aggregate URN, e.g. `urn:publicid:IDN+wisc.cloudlab.us+authority+cm`.
  - `node_type` (String, Required) — The hardware node type, e.g. `d8545`.
  - `count` (Number, Optional) — Number of nodes of this type (default `1`).

### Optional

- `group` (String) — The project subgroup.

### Read-Only

- `start_at` (String) — The earliest time the reservation can start (RFC3339).
- `expires_at` (String) — When the reservation would expire (`start_at` + duration, RFC3339).
