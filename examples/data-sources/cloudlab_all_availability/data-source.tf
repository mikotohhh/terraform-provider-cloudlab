# Survey the earliest 7-day availability across every node type.
#
# Live discovery (run_discovery.sh -> cloudlab_nodetypes.py) enumerates all six
# CloudLab aggregates from the central Portal inventory. It needs only Python;
# geni-lib and the Emulab certificate are used if the Portal fast path fails.
data "cloudlab_all_availability" "survey" {
  project        = "YourProject"
  duration_hours = 168 # 7 days

  discover_command = ["bash", "${path.module}/run_discovery.sh", "--stream"]

  only_with_free = true # skip types with nothing currently free

  # The advertisement RSpec does not flag GPUs, so restrict to GPU hardware
  # with an allow-list (the same types listed in gpu_node_types.json):
  # only_node_types = ["c240g5", "c4130", "d7525", "d8545", "ibm8335", "r7525", "nvidiagh"]
}

# A static node-type list also works: discovery only needs the JSON shape, so
# `cat` of the curated GPU inventory in this directory is an offline fallback
# (free/total are placeholders — leave only_with_free off).
data "cloudlab_all_availability" "gpus_static" {
  project        = "YourProject"
  duration_hours = 168

  discover_command = ["cat", "${path.module}/gpu_node_types.json"]
}

# Earliest start per cluster/node type.
output "earliest_by_type" {
  value = {
    for r in data.cloudlab_all_availability.survey.results :
    "${r.cluster}/${r.node_type}" => r.start_at if r.error == null
  }
}
