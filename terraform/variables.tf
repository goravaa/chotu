# Root inputs: provider selection plus the common values passed straight
# through to the active cloud module. This file is the control panel:
# tweak here (or override via terraform.tfvars) instead of editing main.tf.
# Defaults mirror the dev config (dev.config.yaml).

# ---------------------------------------------------------------------------
# Provider selection
# ---------------------------------------------------------------------------

variable "cloud_provider" {
  type        = string
  default     = "aws"
  description = "Cloud to provision on. Only \"aws\" is implemented; vastai and gcp have stub modules waiting for an implementation."

  validation {
    condition     = contains(["aws"], var.cloud_provider)
    error_message = "Only \"aws\" is implemented yet. To add a provider, implement modules/<name>/ against the contract at the top of main.tf and extend this list."
  }
}

variable "gcp_project" {
  type        = string
  default     = ""
  description = "GCP project ID. Only used by the gcp module."
}

# ---------------------------------------------------------------------------
# Machine
# ---------------------------------------------------------------------------

variable "name_prefix" {
  type        = string
  default     = "chotu"
  description = "Prefix for all resource names and tags."
}

variable "region" {
  type        = string
  default     = "us-west-2"
  description = "Cloud region. Each module maps it to its own geography."
}

variable "instance_type" {
  type        = string
  default     = "g5.xlarge"
  description = "Machine shape. Each module maps it to its own machine types."
}

variable "image_id" {
  type        = string
  default     = ""
  description = "Machine image to boot. Empty means the module's default (Ubuntu 26.04)."
}

variable "use_spot" {
  type        = bool
  default     = false
  description = "Use spare/preemptible capacity. Must pair with persist=false."
}

variable "persist" {
  type        = bool
  default     = true
  description = "Persistent topology: workspace and Docker root live on the root disk. False keeps everything on ephemeral storage (spot only)."
}

variable "root_disk_size_gb" {
  type        = number
  default     = 80
  description = "Root disk size in GiB."
}

variable "root_disk_type" {
  type        = string
  default     = "gp2"
  description = "Root disk type. Each module maps it to its own disk types."
}

# ---------------------------------------------------------------------------
# Access: SSH in, everything else over the tunnel
# ---------------------------------------------------------------------------

variable "status_port" {
  type        = number
  default     = 8081
  description = "Port of the status page. Must match status_page.port in the rendered config."
}

variable "allowed_cidr_blocks" {
  type        = list(string)
  default     = ["0.0.0.0/0"]
  description = "CIDR blocks allowed to reach SSH. The status page and inference are tunnel-only. Narrow this to your IP for a non-demo machine."
}

# ---------------------------------------------------------------------------
# Workspace sync (S3)
# ---------------------------------------------------------------------------

variable "s3_enabled" {
  type        = bool
  default     = true
  description = "Enable workspace sync to S3."
}

variable "s3_bucket_name" {
  type        = string
  default     = "chotu-workspace-324330563475-us-west-2"
  description = "Workspace bucket. Lowercase, globally unique; the instance role is scoped to it."
}

variable "s3_create_bucket" {
  type        = bool
  default     = true
  description = "Create the workspace bucket. False adopts the existing bucket. Flip to false when pointing at a bucket that already holds data."
}

variable "s3_sync_prefix" {
  type        = string
  default     = "workspace"
  description = "Key prefix for workspace sync."
}

variable "s3_sync_interval" {
  type        = string
  default     = "1m"
  description = "Sync interval."
}

variable "s3_exclusions" {
  type        = list(string)
  default     = [".git/**", "node_modules/**", ".venv/**", "__pycache__/**", ".cache/**"]
  description = "Additional sync exclusions."
}

# ---------------------------------------------------------------------------
# Inference, coding, tooling, release
# ---------------------------------------------------------------------------

variable "inference_runtime" {
  type        = string
  default     = "vllm"
  description = "Inference runtime."
}

variable "inference_model" {
  type        = string
  default     = "cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit"
  description = "Model to serve."
}

variable "inference_quantization" {
  type        = string
  default     = "awq"
  description = "Model quantization."
}

variable "inference_access" {
  type        = string
  default     = "local"
  description = "Endpoint exposure. Local means SSH tunnel, not public."
}

variable "inference_image" {
  type        = string
  default     = "vllm/vllm-openai:v0.30.0-x86_64@sha256:5f5e535216848d0c52159c8c13a0af04be5f6fe1a84e79914300610796f76d40"
  description = "Pinned inference image (tag + digest)."
}

variable "inference_port" {
  type        = number
  default     = 8000
  description = "Local inference endpoint port."
}

variable "inference_max_model_len" {
  type        = number
  default     = 14336
  description = "Mirrors inference.resources.max_model_len."
}

variable "inference_gpu_memory_utilization" {
  type        = number
  default     = 0.97
  description = "Mirrors inference.resources.gpu_memory_utilization."
}

variable "inference_max_output_tokens" {
  type        = number
  default     = 4096
  description = "Mirrors inference.resources.max_output_tokens."
}

variable "opencode_enabled" {
  type        = bool
  default     = true
  description = "Run OpenCode on the machine."
}

variable "tooling_packages" {
  type        = list(string)
  default     = ["git", "tmux", "htop", "nvtop", "python3.14", "golang-go"]
  description = "Extra packages installed on the machine."
}

variable "chotu_release_version" {
  type        = string
  default     = "v0.1.0"
  description = "Chotu GitHub release tag. The machine fetches bootstrap.sh from this tag, and bootstrap downloads the daemon binaries from the same release."
}
