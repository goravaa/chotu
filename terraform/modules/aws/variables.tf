# Inputs for the AWS module. This is the root module contract: every cloud
# module declares these same inputs so the root can pass them through
# unchanged.
#
# Cross-field rules (spot pairs with persist=false, sync needs a bucket)
# cannot live in variable validation blocks, which only see their own
# variable. They are preconditions on aws_instance.chotu in main.tf instead,
# so they still fail at plan time.

variable "name_prefix" {
  type        = string
  description = "Prefix for all resource names and tags."
}

variable "region" {
  type        = string
  description = "AWS region for the machine."
}

variable "instance_type" {
  type        = string
  description = "EC2 instance type."
}

variable "image_id" {
  type        = string
  description = "AMI to boot. Empty means the latest Ubuntu 26.04 AMI from Canonical."
}

variable "use_spot" {
  type        = bool
  description = "Use a spot instance. Must pair with persist=false."
}

variable "persist" {
  type        = bool
  description = "Persistent topology: workspace and Docker root live on the root disk."
}

variable "root_disk_size_gb" {
  type        = number
  description = "Root EBS volume size in GiB."
}

variable "root_disk_type" {
  type        = string
  description = "Root EBS volume type."
}

variable "status_port" {
  type        = number
  description = "Port of the status page."
}

variable "allowed_cidr_blocks" {
  type        = list(string)
  description = "CIDR blocks allowed to reach SSH and the status page."
}

variable "s3_enabled" {
  type        = bool
  description = "Enable workspace sync to S3."
}

variable "s3_bucket_name" {
  type        = string
  description = "Workspace bucket. The instance role is scoped to this bucket only."
}

variable "s3_create_bucket" {
  type        = bool
  default     = true
  description = "Create the workspace bucket. False adopts the existing bucket via a data source instead. Terraform cannot probe-then-create, so this toggle is the decision: flip to false when pointing at a bucket that already holds data."
}

variable "s3_sync_prefix" {
  type        = string
  description = "Key prefix for workspace sync."
}

variable "s3_sync_interval" {
  type        = string
  description = "Sync interval."
}

variable "s3_exclusions" {
  type        = list(string)
  description = "Additional sync exclusions."
}

variable "inference_runtime" {
  type        = string
  description = "Inference runtime."
}

variable "inference_model" {
  type        = string
  description = "Model to serve."
}

variable "inference_quantization" {
  type        = string
  description = "Model quantization."
}

variable "inference_access" {
  type        = string
  description = "Endpoint exposure. Local means SSH tunnel, not public."
}

variable "inference_image" {
  type        = string
  description = "Pinned inference image (tag + digest)."
}

variable "inference_port" {
  type        = number
  description = "Local inference endpoint port."
}

variable "inference_max_model_len" {
  type        = number
  description = "Mirrors inference.resources.max_model_len."
}

variable "inference_gpu_memory_utilization" {
  type        = number
  description = "Mirrors inference.resources.gpu_memory_utilization."
}

variable "inference_max_output_tokens" {
  type        = number
  description = "Mirrors inference.resources.max_output_tokens."
}

variable "opencode_enabled" {
  type        = bool
  description = "Run OpenCode on the machine."
}

variable "tooling_packages" {
  type        = list(string)
  description = "Extra packages installed on the machine."
}

variable "chotu_release_version" {
  type        = string
  description = "Chotu GitHub release tag the machine bootstraps from."
}
