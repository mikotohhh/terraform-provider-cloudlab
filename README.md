# Terraform Provider for CloudLab

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

The **CloudLab Terraform Provider** allows [Terraform](https://terraform.io) to manage resources on [CloudLab](https://www.cloudlab.us/) — the academic cloud and network testbed operated by the University of Utah, Clemson University, and the University of Wisconsin.

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.0
- [Go](https://golang.org/doc/install) >= 1.21 (to build from source)
- A CloudLab account with a valid API token

## Getting a CloudLab API Token

1. Log in to [cloudlab.us](https://www.cloudlab.us/)
2. Navigate to your profile settings
3. Generate an API token under **API Tokens**

## Usage

```hcl
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
  # portal_url defaults to https://boss.emulab.net:43794 (the CloudLab Portal REST API)
}

# Create a profile (topology template)
resource "cloudlab_profile" "small_cluster" {
  name    = "small-cluster"
  project = "MyProject"
  script  = file("profile.py")
}

# Spin up an experiment (provisions actual machines)
resource "cloudlab_experiment" "cluster" {
  name            = "my-cluster"
  project         = "MyProject"
  profile_name    = cloudlab_profile.small_cluster.name
  profile_project = "MyProject"
  duration        = 24
}

output "experiment_id" {
  value = cloudlab_experiment.cluster.id
}

output "experiment_status" {
  value = cloudlab_experiment.cluster.status
}

# Look up hostnames/IPs via manifest
data "cloudlab_manifest" "nodes" {
  experiment_id = cloudlab_experiment.cluster.id
}

# Or look up a specific node directly
data "cloudlab_node" "node1" {
  experiment_id = cloudlab_experiment.cluster.id
  client_id     = "node1"
}

output "node1_hostname" {
  value = data.cloudlab_node.node1.hostname
}

output "node1_ipv4" {
  value = data.cloudlab_node.node1.ipv4
}
```

## Resources

| Resource | Description |
|----------|-------------|
| `cloudlab_experiment` | Provisions a set of machines on CloudLab using a profile |
| `cloudlab_profile` | Manages experiment profiles (topology templates) |
| `cloudlab_resgroup` | Manages hardware reservation groups (pre-reserve nodes) |
| `cloudlab_vlan_connection` | Connects shared VLANs between two running experiments |
| `cloudlab_snapshot` | Takes a disk image snapshot of a node in a running experiment |

## Data Sources

| Data Source | Description |
|-------------|-------------|
| `cloudlab_experiment` | Queries a running experiment by ID |
| `cloudlab_manifest` | Retrieves node hostnames and IPs from a running experiment |
| `cloudlab_profile` | Queries an existing profile by UUID or `project,name` |
| `cloudlab_resgroup` | Queries an existing reservation group by UUID |
| `cloudlab_node` | Queries a specific node in a running experiment |
| `cloudlab_availability` | Finds the earliest reservable window for a given set of node types and duration (e.g. "node X for 7 days — earliest start?") |
| `cloudlab_all_availability` | Surveys earliest availability across **all** node types, using an external `discover_command` (geni-lib) to enumerate types then searching each |

## Notable Features

### Experiment Management
- Create experiments from any profile with optional `group`, `paramset_name`/`paramset_owner`, `bindings` (JSON), `refspec`, and `sshpubkey`
- **Extend lifetime** in-place by updating `expires_at` (no destroy/recreate needed)
- **Modify bindings** in-place via PATCH
- `wait_for_ready` (default `true`) polls until the experiment reaches `ready` status

### Profile Management
- Create profiles from inline geni-lib Python `script` or a `repository_url`
- **Update `script`, `public`, `project_writable`** in-place without replacement
- Trigger repository pull for repo-backed profiles

### Reservation Groups
- Reserve hardware nodes by aggregate URN and node type
- Support for frequency range reservations (Powder/WIRELESS testbed)
- Support for named route reservations
- Powder zone selection (`Outdoor`, `Indoor OTA Lab`, `Flux`)
- **Update** reason, timing, and reservations in-place

### Node Operations
The client supports all node-level operations exposed by the API:
- Bulk: reboot, reload, start, stop, power cycle all nodes
- Per-node: reboot, reload, start, stop, power cycle individual nodes

## Development

```bash
git clone https://github.com/srmanda-cs/terraform-provider-cloudlab.git
cd terraform-provider-cloudlab
go build ./...
go vet ./...
golangci-lint run ./...
```

### Local Provider Override

Add to `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "srmanda-cs/cloudlab" = "/path/to/cloudlab-terraform-provider"
  }
  direct {}
}
```

Then build and use:

```bash
go build -o terraform-provider-cloudlab
```

## License

[MIT License](LICENSE)
