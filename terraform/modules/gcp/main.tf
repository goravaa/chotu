# Stub for the GCP module. Not implemented: the root module's
# cloud_provider validation rejects "gcp" until this directory honors
# the module contract documented at the top of terraform/main.tf
# (same inputs, same four outputs: instance_id, public_ip, status_url, ssh).
#
# Implementation path: hashicorp/google provider, one GCE instance with a
# guest accelerator (the equivalent of instance_type, e.g. g2-standard-8
# with an L4), firewall rules for SSH + status_port instead of a security
# group, a service account scoped to the workspace bucket instead of an
# instance profile, and the same first-boot sequence as the AWS module:
# write /etc/chotu/config.yaml via startup-script and execute the release
# bootstrap.sh.
