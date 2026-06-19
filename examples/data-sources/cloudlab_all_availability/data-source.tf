# Survey the earliest 7-day availability across every node type.
#
# Requires the discover_command (see cloudlab_nodetypes.py in this directory) to
# be runnable wherever Terraform executes: Python + geni-lib + an Emulab cert,
# with the CLOUDLAB_PASS env var set to the cert key passphrase.
data "cloudlab_all_availability" "survey" {
  project        = "YourProject"
  duration_hours = 168 # 7 days

  discover_command = ["python3", "${path.module}/cloudlab_nodetypes.py"]

  only_with_free = true # skip types with nothing currently free

  # only_node_types = ["d8545", "c4130", "r7525"]
}

# Earliest start per cluster/node type.
output "earliest_by_type" {
  value = {
    for r in data.cloudlab_all_availability.survey.results :
    "${r.cluster}/${r.node_type}" => r.start_at if r.error == null
  }
}
