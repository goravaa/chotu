# Stub for the Vast.ai module. Not implemented: the root module's
# cloud_provider validation rejects "vastai" until this directory honors
# the module contract documented at the top of terraform/main.tf
# (same inputs, same four outputs: instance_id, public_ip, status_url, ssh).
#
# Implementation path: there is no first-party Vast.ai terraform provider,
# so this module will most likely be a thin wrapper that picks a GPU offer
# and creates the instance through the Vast.ai API (community provider or
# a local-exec provisioner), then runs the same first-boot sequence as the
# AWS module: write /etc/chotu/config.yaml and execute the release
# bootstrap.sh. Vast.ai machines have no security groups, so status_port
# exposure is whatever the offer's port mapping provides.
