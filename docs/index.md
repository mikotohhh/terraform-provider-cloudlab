---
page_title: "CloudLab Provider"
description: |-
  The CloudLab provider manages resources on CloudLab (cloudlab.us), the academic cloud and network testbed.
---

# CloudLab Provider

The **CloudLab provider** allows [Terraform](https://terraform.io) to manage resources on [CloudLab](https://www.cloudlab.us/) — the academic cloud and network testbed operated by the University of Utah, Clemson University, and the University of Wisconsin.

CloudLab provides bare-metal and virtualized compute resources for systems research. This provider exposes the full [CloudLab Portal API](https://gitlab.flux.utah.edu/emulab/portal-api) as Terraform resources and data sources.

## Authentication

The provider authenticates to the CloudLab Portal API using an API token.

To obtain a token:
1. Log in to [cloudlab.us](https://www.cloudlab.us/)
2. Navigate to your profile page
3. Generate an API token under **API Tokens**

The token can be provided via:
- The `token` provider attribute (recommended: use a variable or secret)
- The `CLOUDLAB_TOKEN` environment variable

## Example Usage

```terraform
terraform {
  required_providers {
    cloudlab = {
      source  = "srmanda-cs/cloudlab"
      version = "~> 0.2"
    }
  }
}

provider "cloudlab" {
  token = var.cloudlab_token
}

variable "cloudlab_token" {
  type      = string
  sensitive = true
}
```

### Quick-start: profile + experiment

```terraform
# Create a profile from a geni-lib Python script
resource "cloudlab_profile" "topology" {
  name    = "my-cluster"
  project = "MyProject"
  script  = file("${path.module}/profile.py")
}

# Instantiate the profile as an experiment (provisions real hardware)
resource "cloudlab_experiment" "run" {
  name            = "my-run"
  project         = "MyProject"
  profile_name    = cloudlab_profile.topology.name
  profile_project = cloudlab_profile.topology.project
  duration        = 24
}

# Get the node hostnames once the experiment is ready
data "cloudlab_manifest" "nodes" {
  experiment_id = cloudlab_experiment.run.id
}

output "node_hostnames" {
  value = flatten([
    for m in data.cloudlab_manifest.nodes.manifests :
    [for n in m.nodes : n.hostname]
  ])
}
```

## Schema

### Required

- `token` (String, Sensitive) — CloudLab Portal API token. Can also be set via the `CLOUDLAB_TOKEN` environment variable.

### Optional

- `portal_url` (String) — CloudLab portal REST API base URL. Defaults to `https://boss.emulab.net:43794` (the CloudLab/Emulab Portal API server). Can also be set via the `CLOUDLAB_PORTAL_URL` environment variable. Override this to target a different portal instance.

## Resources

| Resource | Description |
|----------|-------------|
| [cloudlab_experiment](resources/cloudlab_experiment.md) | Provisions an experiment (allocates hardware) using a profile |
| [cloudlab_profile](resources/cloudlab_profile.md) | Manages experiment profiles (topology templates) |
| [cloudlab_resgroup](resources/cloudlab_resgroup.md) | Manages hardware reservation groups |
| [cloudlab_vlan_connection](resources/cloudlab_vlan_connection.md) | Connects shared VLANs between two running experiments |
| [cloudlab_snapshot](resources/cloudlab_snapshot.md) | Takes a disk image snapshot of a node in an experiment |

## Data Sources

| Data Source | Description |
|-------------|-------------|
| [cloudlab_experiment](data-sources/cloudlab_experiment.md) | Queries a running experiment by UUID |
| [cloudlab_manifest](data-sources/cloudlab_manifest.md) | Retrieves all node hostnames and IPs from a running experiment |
| [cloudlab_profile](data-sources/cloudlab_profile.md) | Queries an existing profile by UUID or `project,name` |
| [cloudlab_resgroup](data-sources/cloudlab_resgroup.md) | Queries an existing reservation group by UUID |
| [cloudlab_node](data-sources/cloudlab_node.md) | Queries a specific node in a running experiment |
| [cloudlab_availability](data-sources/cloudlab_availability.md) | Finds the earliest reservable window for a requested set of node types |
| [cloudlab_all_availability](data-sources/cloudlab_all_availability.md) | Surveys per-type availability across allocatable physical nodes |
