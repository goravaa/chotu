# 1. Configure the Local Provider
terraform {
  required_providers {
    local = {
      source  = "hashicorp/local"
      version = "~> 2.5"
    }
  }
}

provider "local" {
  # Configuration options (none needed for this basic example)
}

# 2. Create a local text file with "Hello, World!"
resource "local_file" "hello_world" {
  filename = "${path.module}/hello.txt"
  content  = "Hello, World!"
}

# 3. Output a message directly to your terminal screen
output "greeting" {
  value       = "Hello, World! Terraform has successfully executed."
  description = "A simple confirmation message"
}
