# Survey the earliest 7-day availability across every node type.
#
# Live discovery (run_discovery.sh -> cloudlab_nodetypes.py) enumerates all
# (cluster, node type) pairs with current free/total counts via geni-lib.
# It needs Python + geni-lib + an Emulab cert; the cert-key passphrase is
# read from the macOS Keychain (see run_discovery.sh for the one-time setup).
data "cloudlab_all_availability" "survey" {
  project        = "YourProject"
  duration_hours = 168 # 7 days

  discover_command = ["bash", "${path.module}/run_discovery.sh"]

  only_with_free = true # skip types with nothing currently free

  # The advertisement RSpec does not flag GPUs, so restrict to GPU hardware
  # with an allow-list (the same types listed in gpu_node_types.json):
  # only_node_types = ["c240g5", "c4130", "d7525", "d8545", "ibm8335", "r7525", "nvidiagh"]
}

# No geni-lib? A static node-type list works too: discovery only needs the
# JSON shape, so `cat` of the curated GPU inventory in this directory is a
# dependency-free alternative (free/total are placeholders — leave
# only_with_free off).
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
