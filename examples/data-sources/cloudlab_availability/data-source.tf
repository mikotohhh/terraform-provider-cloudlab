# Earliest 7-day window for a single GPU node type.
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
