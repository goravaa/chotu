Chotu — Manual Target Environment Notes
0. Objective

The purpose of this manual run was to establish and verify the complete target Chotu environment on a real AWS g6.xlarge before writing the bootstrap automation.

Target:

AWS g6.xlarge
├── Ubuntu 22.04
├── NVIDIA L4 24 GB
├── NVIDIA host driver
├── NVIDIA Container Toolkit
├── Docker
├── vLLM
├── Qwen3-Coder 30B AWQ
├── OpenCode
├── workspace/
├── rclone
├── S3 persistence
├── IAM instance role
└── scheduled workspace → S3 sync

Important principle:

Manual environment first → verify every component → only then write bootstrap.sh/Terraform.

1. EC2 Machine

Machine:

Instance: g6.xlarge
GPU: NVIDIA L4
VRAM: ~24 GB
vCPU: 4
RAM: 16 GiB

Region used:

ap-south-1

Current machine kernel observed:

6.8.0-1063-aws
2. Storage Architecture

This run deliberately tested the persistent/stoppable mode.

/
└── EBS
    └── persistent

/data
└── EC2 instance-store NVMe
    └── ephemeral

Root disk:

~80 GB EBS
~72 GB free during testing

Instance store:

~229 GB filesystem
mounted at /data

Filesystem label:

chotu-data

/etc/fstab:

LABEL=chotu-data /data ext4 defaults,nofail 0 2

Unmount/remount of /data was successfully tested.

Directories created:

/data/docker
/data/containerd
/data/huggingface
Important design decision

For persistent mode, Docker remains on EBS:

Docker Root Dir: /var/lib/docker

We deliberately did not move Docker/containerd/model cache to /data.

The older notes represented an ephemeral mode where:

/data/docker
/data/containerd
/data/huggingface

were used.

Chotu needs to support both:

Persistent mode
EBS
├── Docker
├── model cache
├── workspace
└── persistent local state

/data
└── optional ephemeral workloads
Ephemeral mode
small EBS root
/data
├── Docker
├── containerd
├── models
└── workspace
3. NVIDIA Host Driver

The live machine ended up using:

nvidia-driver-580-server
Version: 580.178.04

and:

nvidia-utils-580-server
Version: 580.178.04

nvidia-smi successfully showed:

Driver Version: 580.178.04
CUDA Version: 13.0
GPU: NVIDIA L4
VRAM: 23034 MiB

Example:

+-----------------------------------------------------------------------------+
| NVIDIA-SMI 580.178.04
| Driver Version: 580.178.04
| CUDA Version: 13.0
| NVIDIA L4
| 23034 MiB
+-----------------------------------------------------------------------------+

GPU was idle after setup.

Important discrepancy

The original project specification had:

NVIDIA 535
CUDA 12.2.2

but the actual working machine used:

NVIDIA 580.178.04
CUDA 13.0

Therefore bootstrap should not blindly hardcode the old documented 535 stack without resolving the compatibility decision.

The live machine is evidence of a working newer stack.

4. NVIDIA Container Toolkit

Installed:

nvidia-container-toolkit 1.20.1-1
nvidia-container-toolkit-base 1.20.1-1
libnvidia-container-tools 1.20.1-1
libnvidia-container1 1.20.1-1

Version:

nvidia-ctk --version
1.20.1

NVIDIA official repository was configured.

Then:

sudo nvidia-ctk runtime configure --runtime=docker

Generated:

{
    "runtimes": {
        "nvidia": {
            "args": [],
            "path": "nvidia-container-runtime"
        }
    }
}

in:

/etc/docker/daemon.json
5. NVIDIA CDI

There was initially a problem where CDI detected zero GPUs because NVIDIA userspace/NVML was incomplete.

We restored the NVIDIA userspace packages:

nvidia-driver-580-server
nvidia-utils-580-server

rebooted, then started:

sudo systemctl start nvidia-cdi-refresh.service

After that:

sudo nvidia-ctk cdi list

returned:

nvidia.com/gpu=0
nvidia.com/gpu=GPU-e87227ff-f15e-44b6-17fd-381050d5fdae
nvidia.com/gpu=all

Also verified:

libnvidia-ml.so
libnvidia-ml.so.1

were available through ldconfig.

6. Docker

Docker was installed from the official Docker APT repository, not Ubuntu docker.io and not Snap.

Live version:

Docker Engine 29.8.1

Docker info showed:

Storage Driver: overlayfs
Driver Type: io.containerd.snapshotter.v1

Cgroup Driver: systemd
Cgroup Version: 2

Runtimes:
io.containerd.runc.v2
nvidia
runc

Default Runtime:
runc

Docker Root Dir:
/var/lib/docker

System:

Ubuntu 22.04.5 LTS
x86_64
4 CPUs
15.01 GiB RAM

Docker and containerd were active.

7. First GPU Container Test

We verified the complete host → container GPU path:

sudo docker run --rm \
  --runtime=nvidia \
  --gpus all \
  ubuntu \
  nvidia-smi

This successfully showed the L4 inside the container.

Therefore:

Host L4
 ↓
NVIDIA driver
 ↓
NVML
 ↓
NVIDIA Container Toolkit
 ↓
Docker
 ↓
Container
 ↓
nvidia-smi

was verified.

8. vLLM Image GPU Test

We pulled:

vllm/vllm-openai:latest

Initially this command was attempted:

sudo docker run --rm \
  --gpus all \
  vllm/vllm-openai:latest \
  nvidia-smi

It didn't execute nvidia-smi because the vLLM image's entrypoint is already the vLLM server.

It interpreted:

nvidia-smi

as the model name and attempted to retrieve a Hugging Face repository called nvidia-smi.

Correct test:

sudo docker run --rm \
  --gpus all \
  --entrypoint nvidia-smi \
  vllm/vllm-openai:latest

Result:

NVIDIA L4
Driver 580.178.04
CUDA 13.0
23034 MiB

So the actual vLLM container image could access the GPU.

9. Persistent Hugging Face Cache

Because this machine is the persistent/EBS mode, we did not use:

/data/huggingface

for the live model cache.

Created:

/var/lib/chotu/huggingface

and:

sudo chown -R $USER:$USER /var/lib/chotu

This keeps model cache on the EBS-backed root filesystem.

10. vLLM Model

Model:

cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit

vLLM command:

sudo docker run -d \
  --name qwen \
  --gpus all \
  --ipc=host \
  -p 8000:8000 \
  -v /var/lib/chotu/huggingface:/root/.cache/huggingface \
  vllm/vllm-openai:latest \
  --model cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit \
  --enable-auto-tool-choice \
  --tool-call-parser qwen3_coder \
  --max-model-len 14336 \
  --gpu-memory-utilization 0.97

Model loaded successfully.

11. Why 14336 Context

The model weights are approximately:

~17 GB

The L4 has approximately:

24 GB VRAM

Additional VRAM is required for:

CUDA graphs
runtime overhead
KV cache

32k context was too large in the previous setup.

Working value:

--max-model-len 14336

GPU utilization:

--gpu-memory-utilization 0.97
12. vLLM API Verification

After the model loaded:

curl http://localhost:8000/v1/models

returned:

{
  "object": "list",
  "data": [
    {
      "id": "cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit",
      "object": "model",
      "owned_by": "vllm",
      "max_model_len": 14336
    }
  ]
}

Therefore:

vLLM server
      ↓
OpenAI-compatible API
      ↓
localhost:8000/v1

was verified.

13. OpenCode

OpenCode was installed using:

curl -fsSL https://opencode.ai/install | bash

Installed version:

1.18.31

The installer added OpenCode to:

/home/ubuntu/.bashrc

The first shell did not immediately recognize it because the current shell hadn't reloaded .bashrc.

Fix:

source ~/.bashrc

Then:

opencode --version

worked.

The second installation attempt using sudo was unnecessary; OpenCode is installed under the user's home environment.

14. OpenCode Configuration

The working configuration:

{
  "$schema": "https://opencode.ai/config.json",
  "model": "vllm/cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit",
  "provider": {
    "vllm": {
      "name": "Local vLLM",
      "npm": "@ai-sdk/openai-compatible",
      "options": {
        "baseURL": "http://127.0.0.1:8000/v1"
      },
      "models": {
        "cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit": {
          "name": "Qwen3-Coder-30B-A3B (AWQ)",
          "limit": {
            "context": 14336,
            "output": 4096
          }
        }
      }
    }
  }
}

Located at:

~/workspace/opencode.json

Important configuration details:

provider

not:

providers

and:

npm

not:

package

API endpoint:

http://127.0.0.1:8000/v1

Model identifier:

vllm/cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit
15. Workspace-Level OpenCode Configuration

Workspace structure:

~/workspace/
├── opencode.json
└── project/

OpenCode was launched from:

~/workspace/project

and successfully discovered/used the parent:

~/workspace/opencode.json

This was actually tested, not assumed.

Therefore the desired Chotu structure works:

workspace/
├── opencode.json
├── project1/
├── project2/
└── project3/

with a shared OpenCode configuration at workspace level.

This created a functional MVP-like architecture:

workspace/
   ↓
OpenCode
   ↓
shared config
   ↓
local vLLM
   ↓
Qwen3-Coder

OpenCode was observed writing code smoothly.

16. OpenCode Tool Calling

We did not perform the final explicit tool-call smoke test.

The documented test is:

opencode run \
"List the files in the current directory using a tool and report what you find."

The reason this matters is that previous testing showed the model could say:

"I'll use a tool..."

without actually executing it when the wrong parser was used.

The working vLLM flags are:

--enable-auto-tool-choice
--tool-call-parser qwen3_coder

We stopped before this final explicit test because the environment was already successfully functioning as an MVP.

17. rclone

Installed using:

curl https://rclone.org/install.sh | sudo bash

The S3 bucket:

chotu-324330563475-us-west-2-an

ARN:

arn:aws:s3:::chotu-324330563475-us-west-2-an
18. IAM Architecture

We deliberately did not use AWS access keys on the machine.

Architecture:

EC2
 ↓
IAM Instance Profile
 ↓
ChotuWorkspaceS3Role
 ↓
chotu-workspace-management
 ↓
S3 bucket

Role:

ChotuWorkspaceS3Role

Policy:

chotu-workspace-management

The role was attached to the current g6.xlarge.

AWS CLI was installed:

sudo apt update
sudo apt install -y awscli

Then:

aws sts get-caller-identity

successfully returned the assumed role identity.

This verified the EC2 instance role.

19. IAM Policy

Initial policy had a mistake.

Bucket-level actions correctly used:

"Resource": "arn:aws:s3:::chotu-324330563475-us-west-2-an"

But object actions initially also used the bucket ARN.

That caused:

not authorized to perform: s3:PutObject

Correct policy:

{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "WorkspaceBucketAccess",
      "Effect": "Allow",
      "Action": [
        "s3:ListBucket",
        "s3:GetBucketLocation"
      ],
      "Resource": "arn:aws:s3:::chotu-324330563475-us-west-2-an"
    },
    {
      "Sid": "WorkspaceObjectAccess",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject"
      ],
      "Resource": "arn:aws:s3:::chotu-324330563475-us-west-2-an/*"
    }
  ]
}

Important distinction:

Bucket actions
arn:aws:s3:::bucket

Object actions
arn:aws:s3:::bucket/*
20. rclone Configuration

We directly injected:

~/.config/rclone/rclone.conf

with:

[chotu-s3]
type = s3
provider = AWS
env_auth = true
region = us-west-2

Important:

env_auth = true

means rclone obtains AWS credentials through the EC2 IAM role rather than stored access keys.

Verified:

rclone listremotes

returned:

chotu-s3:
21. S3 Connectivity Test

Initial test accidentally omitted part of the bucket name.

Correct bucket:

chotu-324330563475-us-west-2-an

Then:

rclone lsd chotu-s3:chotu-324330563475-us-west-2-an

worked.

And:

rclone size chotu-s3:chotu-324330563475-us-west-2-an

returned:

Total objects: 0
Total size: 0 B

So:

IAM → rclone → S3

was verified.

22. Workspace → S3 Sync

Manual sync command:

rclone sync ~/workspace \
  chotu-s3:chotu-324330563475-us-west-2-an/workspace \
  -P

After fixing the object ARN, it successfully uploaded the workspace.

Verified with:

rclone ls chotu-s3:chotu-324330563475-us-west-2-an/workspace

Example resulting objects:

opencode.json
project/README.md
project/cache/cache_manager.py
project/data_ingestion/__init__.py
project/data_ingestion/__pycache__/...
project/knowledge_graph/__init__.py
project/knowledge_graph/__pycache__/...
project/lineage/lineage_tracker.py
project/main.py
project/question_answering/__init__.py
project/question_answering/__pycache__/...
sync-test.txt

Therefore actual:

~/workspace
      ↓
    rclone
      ↓
S3 workspace prefix

was verified.

23. rclone Filter

We noticed that the first sync uploaded things we don't want persisted, especially:

__pycache__
*.pyc

The intended filter was:

- opencode.json
- .git/**
- .venv/**
- __pycache__/**
- *.pyc
- .pytest_cache/**
- .mypy_cache/**
- .ruff_cache/**
- node_modules/**
- .DS_Store

The filter was kept outside the workspace:

~/.config/chotu/workspace-filter

rather than:

~/workspace/.chotu-rclone-filter

so Chotu's operational configuration isn't part of the user's project data.

The sync script uses:

--filter-from "$HOME/.config/chotu/workspace-filter"
24. Sync Script

Created:

~/.local/bin/chotu-workspace-sync

Contents:

#!/bin/bash
set -euo pipefail

rclone sync "$HOME/workspace" \
  "chotu-s3:chotu-324330563475-us-west-2-an/workspace" \
  --filter-from "$HOME/.config/chotu/workspace-filter" \
  --log-file "$HOME/.config/chotu/rclone-sync.log" \
  --log-level INFO

Executable permission:

chmod +x ~/.local/bin/chotu-workspace-sync
25. Log Rotation

We wanted rclone logs to automatically expire/rotate.

Created:

/etc/logrotate.d/chotu-rclone

with:

/home/ubuntu/.config/chotu/rclone-sync.log {
    daily
    rotate 7
    maxsize 10M
    missingok
    notifempty
    compress
    delaycompress
    copytruncate
}

Meaning:

daily rotation
7 retained rotations
10 MB maximum trigger
compressed old logs
copytruncate so rclone keeps working
26. Scheduled Sync

We chose systemd user timer rather than writing a custom sync daemon.

Service:

~/.config/systemd/user/chotu-workspace-sync.service

Contents:

[Unit]
Description=Chotu workspace S3 sync

[Service]
Type=oneshot
ExecStart=%h/.local/bin/chotu-workspace-sync

Timer:

~/.config/systemd/user/chotu-workspace-sync.timer

Contents:

[Unit]
Description=Run Chotu workspace S3 sync

[Timer]
OnBootSec=30s
OnUnitActiveSec=1min
Unit=chotu-workspace-sync.service

[Install]
WantedBy=timers.target

The 1-minute interval was intentionally used for testing.

27. Scheduler Verification

Initially the timer files hadn't actually been created.

We created:

mkdir -p ~/.config/systemd/user

then the service and timer.

Activated:

systemctl --user daemon-reload
systemctl --user enable --now chotu-workspace-sync.timer

Verified:

systemctl --user list-timers --all

The timer triggered the service.

28. Scheduler Failure and Fix

First scheduled execution failed:

status=203/EXEC

with:

Failed to locate executable
/home/ubuntu/.local/bin/chotu-workspace-sync:
Permission denied

This proved the timer itself was firing.

The issue was simply the executable bit.

Fixed:

chmod +x ~/.local/bin/chotu-workspace-sync

Then manually triggered the systemd service:

systemctl --user start chotu-workspace-sync.service

It succeeded.

This verified:

systemd timer
      ↓
systemd service
      ↓
sync script
      ↓
rclone
      ↓
IAM
      ↓
S3
29. Scheduler Test File

Created:

echo "scheduler test $(date)" > ~/workspace/scheduler-test.txt

The scheduler successfully synchronized it to S3.

The timer was therefore tested as an actual scheduled mechanism rather than simply manually executing rclone.

30. Final Verified Architecture

At the end of the manual build:

                       AWS
                        │
                 g6.xlarge / L4
                        │
              ┌─────────┴─────────┐
              │                   │
            Ubuntu              Storage
              │                   │
       NVIDIA Driver              ├── EBS /
              │                   │
      NVIDIA Container            └── NVMe /data
          Toolkit
              │
            Docker
              │
           vLLM
              │
     Qwen3-Coder 30B AWQ
              │
       OpenAI API :8000
              │
          OpenCode
              │
         ~/workspace
              │
        systemd timer
              │
           rclone
              │
        IAM Instance Role
              │
        S3 workspace
31. Things We Have NOT Fully Verified

These should not be falsely marked as complete in the bootstrap requirements:

OpenCode explicit tool execution

We verified OpenCode coding/inference, but didn't run the final explicit tool-call smoke test.

Automatic startup restoration

We verified workspace → S3 but did not implement/test:

S3 → workspace

on boot.

Shutdown/final sync

Not implemented/tested.

Conflict handling

No policy yet for:

local changed
S3 changed

simultaneously.

Bootstrap reproducibility

The environment is manually verified, but bootstrap.sh has not yet recreated it from scratch.

Terraform recreation

Not yet tested from a clean machine.

32. Important Bootstrap Inputs We Learned

Bootstrap needs to account for the actual observed environment rather than copying old assumptions.

GPU

Observed working:

NVIDIA driver 580.178.04
CUDA 13.0
NVIDIA Container Toolkit 1.20.1
Docker

Observed:

Docker 29.8.1
Docker Root Dir: /var/lib/docker

for persistent mode.

vLLM
vllm/vllm-openai:latest
Model
cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit
vLLM configuration
--enable-auto-tool-choice
--tool-call-parser qwen3_coder
--max-model-len 14336
--gpu-memory-utilization 0.97
Model cache persistent location
/var/lib/chotu/huggingface
OpenCode
1.18.31
Workspace
~/workspace
Shared OpenCode configuration
~/workspace/opencode.json
rclone
~/.config/rclone/rclone.conf
Chotu sync configuration
~/.config/chotu/workspace-filter
~/.config/chotu/rclone-sync.log
~/.local/bin/chotu-workspace-sync
~/.config/systemd/user/chotu-workspace-sync.service
~/.config/systemd/user/chotu-workspace-sync.timer
33. Final State Before Destruction

The machine has successfully demonstrated:

[✓] Real g6.xlarge
[✓] Ubuntu
[✓] L4 detected
[✓] NVIDIA driver
[✓] NVIDIA Container Toolkit
[✓] CDI
[✓] Docker GPU access
[✓] vLLM container
[✓] Qwen3-Coder model
[✓] vLLM API
[✓] OpenCode
[✓] OpenCode → vLLM
[✓] Shared workspace OpenCode config
[✓] IAM role
[✓] rclone
[✓] rclone → S3
[✓] Workspace → S3
[✓] Filtering
[✓] systemd scheduled sync
[✓] scheduled sync actually executed
[✓] log rotation configuration

The remaining work is now automation, not discovering how the environment works.

And before terminating the instance, the ~/chotu-machine-snapshot/ collection we just created should be preserved. That snapshot is the useful bridge between “we know it works” and “now let's make Chotu build it automatically.”