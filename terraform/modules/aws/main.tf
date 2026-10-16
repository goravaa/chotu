# AWS implementation of the Chotu module contract: one EC2 instance in the
# default VPC, SSH + status page open, IAM role scoped to the workspace
# bucket, cloud-init rendering /etc/chotu/config.yaml and running the
# release bootstrap.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
    local = {
      source  = "hashicorp/local"
      version = "~> 2.5"
    }
  }
}

# ---------------------------------------------------------------------------
# Image: explicit AMI wins, otherwise latest Ubuntu 26.04 from Canonical
# ---------------------------------------------------------------------------

data "aws_ami" "ubuntu" {
  count       = var.image_id == "" ? 1 : 0
  most_recent = true
  owners      = ["099720109477"] # Canonical

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-*-26.04-amd64-server-*"]
  }
}

locals {
  ami_id = var.image_id != "" ? var.image_id : data.aws_ami.ubuntu[0].id
}

# ---------------------------------------------------------------------------
# Networking: default VPC, SSH + status page in, everything out
# ---------------------------------------------------------------------------

data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
}

resource "aws_security_group" "chotu" {
  name        = "${var.name_prefix}-sg"
  description = "Chotu single machine: SSH only. The status page and inference are reached over the SSH tunnel, never publicly."
  vpc_id      = data.aws_vpc.default.id

  tags = {
    Name = "${var.name_prefix}-sg"
  }
}

# One ingress rule per CIDR block keeps the intent readable in the console.
resource "aws_vpc_security_group_ingress_rule" "ssh" {
  for_each          = toset(var.allowed_cidr_blocks)
  security_group_id = aws_security_group.chotu.id
  description       = "SSH from ${each.value}"
  ip_protocol       = "tcp"
  from_port         = 22
  to_port           = 22
  cidr_ipv4         = each.value
}

resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.chotu.id
  description       = "Outbound for package installs, image pulls and release downloads."
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

# ---------------------------------------------------------------------------
# SSH access: terraform generates the key pair, registers the public half
# with EC2, and writes the private half plus an ssh config entry (with
# tunnels for the status page and inference) next to the terraform files.
#
# Note: the private key lives in terraform state. State files are local
# and gitignored here; do not push them anywhere.
# ---------------------------------------------------------------------------

resource "tls_private_key" "chotu" {
  algorithm = "ED25519"
}

resource "aws_key_pair" "chotu" {
  key_name   = "${var.name_prefix}-key"
  public_key = tls_private_key.chotu.public_key_openssh

  tags = {
    Name = "${var.name_prefix}-key"
  }
}

locals {
  ssh_key_file = abspath("${path.root}/${var.name_prefix}-key.pem")
}

resource "local_file" "ssh_private_key" {
  filename        = local.ssh_key_file
  content         = tls_private_key.chotu.private_key_openssh
  file_permission = "0600"
}

# ---------------------------------------------------------------------------
# Workspace bucket: created when asked, otherwise adopted.
#
# Terraform has no probe-then-create: a resource errors if the bucket
# already exists, a data source errors if it does not. The s3_create_bucket
# toggle picks one path; locals below resolve whichever one is active so
# the IAM policy never cares.
# ---------------------------------------------------------------------------

resource "aws_s3_bucket" "workspace" {
  count = var.s3_enabled && var.s3_create_bucket ? 1 : 0

  bucket = var.s3_bucket_name

  tags = {
    Name = var.s3_bucket_name
  }
}

resource "aws_s3_bucket_versioning" "workspace" {
  count = var.s3_enabled && var.s3_create_bucket ? 1 : 0

  bucket = aws_s3_bucket.workspace[0].id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "workspace" {
  count = var.s3_enabled && var.s3_create_bucket ? 1 : 0

  bucket = aws_s3_bucket.workspace[0].id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Adopt mode reads the existing bucket at plan time. A missing bucket fails
# the plan with the AWS NoSuchBucket error, which is the fast feedback for
# a typo in s3_bucket_name.
data "aws_s3_bucket" "workspace" {
  count = var.s3_enabled && !var.s3_create_bucket ? 1 : 0

  bucket = var.s3_bucket_name
}

locals {
  # Exactly one of the two sources above has an element whenever sync is
  # enabled. try/one cannot express this: one([]) is null, not an error,
  # so try would stop at the null instead of falling through. concat plus
  # one resolves the active branch with no null path.
  workspace_bucket_arn = one(concat(
    aws_s3_bucket.workspace[*].arn,
    data.aws_s3_bucket.workspace[*].arn,
  ))
}

# ---------------------------------------------------------------------------
# IAM: instance role scoped to the workspace bucket so sync works without
# long-lived credentials on the machine
# ---------------------------------------------------------------------------

resource "aws_iam_role" "chotu" {
  name = "${var.name_prefix}-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
    }]
  })

  tags = {
    Name = "${var.name_prefix}-role"
  }
}

resource "aws_iam_policy" "workspace_sync" {
  count = var.s3_enabled ? 1 : 0

  name        = "${var.name_prefix}-workspace-sync"
  description = "Workspace sync access to the Chotu bucket."

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        # ListBucket plus GetBucketLocation: rclone resolves the bucket's
        # region on first contact, and without the location call it falls
        # back to us-east-1 and dies on a 301 redirect.
        Action   = ["s3:ListBucket", "s3:GetBucketLocation"]
        Effect   = "Allow"
        Resource = local.workspace_bucket_arn
      },
      {
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
        Effect   = "Allow"
        Resource = "${local.workspace_bucket_arn}/${var.s3_sync_prefix}/*"
      }
    ]
  })
}

resource "aws_iam_role_policy_attachment" "workspace_sync" {
  count      = var.s3_enabled ? 1 : 0
  role       = aws_iam_role.chotu.name
  policy_arn = aws_iam_policy.workspace_sync[0].arn
}

resource "aws_iam_instance_profile" "chotu" {
  name = "${var.name_prefix}-profile"
  role = aws_iam_role.chotu.name

  tags = {
    Name = "${var.name_prefix}-profile"
  }
}

# ---------------------------------------------------------------------------
# The machine
# ---------------------------------------------------------------------------

resource "aws_instance" "chotu" {
  ami                    = local.ami_id
  instance_type          = var.instance_type
  subnet_id              = data.aws_subnets.default.ids[0]
  vpc_security_group_ids = [aws_security_group.chotu.id]
  key_name               = aws_key_pair.chotu.key_name
  iam_instance_profile   = aws_iam_instance_profile.chotu.name

  root_block_device {
    volume_size           = var.root_disk_size_gb
    volume_type           = var.root_disk_type
    encrypted             = true
    delete_on_termination = true
  }

  # Spot only when asked; on-demand is the default.
  dynamic "instance_market_options" {
    for_each = var.use_spot ? [1] : []

    content {
      market_type = "spot"
    }
  }

  user_data = templatefile("${path.module}/templates/user_data.sh.tftpl", {
    release_version                  = var.chotu_release_version
    instance_type                    = var.instance_type
    image_id                         = local.ami_id
    region                           = var.region
    use_spot                         = var.use_spot
    persist                          = var.persist
    root_disk_size_gb                = var.root_disk_size_gb
    root_disk_type                   = var.root_disk_type
    s3_enabled                       = var.s3_enabled
    s3_bucket_name                   = var.s3_bucket_name
    s3_sync_prefix                   = var.s3_sync_prefix
    s3_sync_interval                 = var.s3_sync_interval
    s3_exclusions                    = var.s3_exclusions
    inference_runtime                = var.inference_runtime
    inference_model                  = var.inference_model
    inference_quantization           = var.inference_quantization
    inference_access                 = var.inference_access
    inference_image                  = var.inference_image
    inference_port                   = var.inference_port
    inference_max_model_len          = var.inference_max_model_len
    inference_gpu_memory_utilization = var.inference_gpu_memory_utilization
    inference_max_output_tokens      = var.inference_max_output_tokens
    opencode_enabled                 = var.opencode_enabled
    tooling_packages                 = var.tooling_packages
    status_port                      = var.status_port
  })

  # A new release or config change converges by replacement: the setup
  # daemon is idempotent, so a fresh machine always reaches the desired state.
  user_data_replace_on_change = true

  tags = {
    Name = var.name_prefix
  }

  # These mirror chotu-setup's config loader. Preconditions on a managed
  # resource evaluate at plan time (variable validation blocks can only see
  # their own variable), so bad combinations fail before anything is built.
  lifecycle {
    precondition {
      condition     = !(var.use_spot && var.persist)
      error_message = "use_spot=true requires persist=false: a spot instance cannot keep state on a root disk that outlives it."
    }

    precondition {
      condition     = !(var.s3_enabled && var.s3_bucket_name == "")
      error_message = "s3_enabled=true requires s3_bucket_name to be set."
    }
  }
}
