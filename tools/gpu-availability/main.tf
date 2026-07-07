terraform {
  required_providers {
    cloudlab = {
      source = "srmanda-cs/cloudlab"
    }
  }
}

variable "duration_hours" {
  description = "How long the reservation is needed, in hours."
  type        = number
  default     = 168 # 7 days
}

provider "cloudlab" {}

# Live survey of every GPU node type: run_discovery.sh enumerates all node
# types with current free/total via geni-lib (passphrase from the macOS
# Keychain), then each type is searched for its earliest reservation window.
data "cloudlab_all_availability" "gpus" {
  project        = "uw-mad-dash"
  duration_hours = var.duration_hours

  discover_command = ["bash", "${path.module}/../../examples/data-sources/cloudlab_all_availability/run_discovery.sh"]

  # GPU-equipped hardware types (see examples/.../gpu_node_types.json).
  only_node_types = ["c240g5", "c4130", "d7525", "d8545", "ibm8335", "r7525", "nvidiagh"]
}

output "gpu_availability" {
  value = {
    for r in data.cloudlab_all_availability.gpus.results :
    "${r.cluster}/${r.node_type}" => {
      free     = r.free
      total    = r.total
      start_at = r.start_at
      error    = r.error
    }
  }
}
