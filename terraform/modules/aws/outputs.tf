# Outputs of the AWS module. This is the root module contract: every cloud
# module exposes these same four outputs.

output "instance_id" {
  value       = aws_instance.chotu.id
  description = "EC2 instance ID of the Chotu machine."
}

output "public_ip" {
  value       = aws_instance.chotu.public_ip
  description = "Public IP of the Chotu machine."
}

output "status_url" {
  value       = "http://localhost:${var.status_port}"
  description = "Status page over the SSH tunnel (see ssh output). Not exposed publicly."
}

output "ssh" {
  value       = "ssh -i ${local.ssh_key_file} -L ${var.status_port}:localhost:${var.status_port} -L ${var.inference_port}:localhost:${var.inference_port} ubuntu@${aws_instance.chotu.public_ip}"
  description = "SSH command with the generated key and tunnels for the status page and inference."
}

output "ssh_config_snippet" {
  value       = <<-EOT
  Host ${var.name_prefix}
    HostName ${aws_instance.chotu.public_ip}
    User ubuntu
    IdentityFile ${local.ssh_key_file}
    LocalForward ${var.status_port} localhost:${var.status_port}
    LocalForward ${var.inference_port} localhost:${var.inference_port}
  EOT
  description = "Paste into ~/.ssh/config for plain `ssh chotu` with tunnels."
}

output "ssh_key_file" {
  value       = local.ssh_key_file
  description = "Generated SSH private key (0600)."
}

output "s3_bucket" {
  value       = var.s3_enabled ? var.s3_bucket_name : null
  description = "Workspace bucket in use (created or adopted)."
}
