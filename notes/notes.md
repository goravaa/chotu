I don't want to invent the Docker/NVIDIA Toolkit installation steps. Your notes establish that Docker and the NVIDIA runtime were working, but they **don't contain the commands used to install Docker or `nvidia-container-toolkit`**.

So I'll document those as verified prerequisites rather than fabricate an installation procedure. Everything else below is based on your notes and corrections.

# EC2 G6 + Docker + vLLM + OpenCode Setup

## 1. Overview

This document records the working setup used to run **OpenCode against a local vLLM server** on an AWS EC2 G6 instance.

### Final stack

```text
AWS EC2 g6.xlarge
        │
        ├── NVIDIA L4 (~24 GB VRAM)
        │
        ├── Ubuntu
        │
        ├── Docker
        │     └── containerd
        │
        ├── vLLM
        │     └── Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit
        │
        └── OpenCode
              └── OpenAI-compatible API
                    │
                    └── localhost:8000
```

### AWS configuration

* **Instance:** `g6.xlarge`
* **Region:** `ap-south-1`
* **GPU:** NVIDIA L4
* **GPU memory:** ~24 GB
* **Root disk:** ~80 GB EBS
* **Additional disk:** ~233 GB instance-store NVMe

---

# 2. Machine / GPU

The instance was an:

```text
AWS EC2 G6.xlarge
Region: ap-south-1
GPU: NVIDIA L4
VRAM: ~24 GB
```

The initial GPU check was:

```bash
lspci | grep -i nvidia
```

Output:

```text
NVIDIA Corporation AD104GL [L4]
```

Initially, `nvidia-smi` was unavailable.

NVIDIA utilities/drivers were then installed and configured. After that:

```bash
nvidia-smi
```

showed:

```text
NVIDIA L4
Driver Version: 580.173.02
CUDA Version: 13.0
Memory: 23034 MiB
```

This confirmed that the **host GPU and NVIDIA driver stack were working**.

---

# 3. Disk Layout

The machine initially appeared to have two NVMe devices:

```bash
lsblk
```

Approximately:

```text
nvme0n1       80G
├─nvme0n1p1   6.9G   /
├─nvme0n1p13  1G     /boot
├─nvme0n1p14  4M
└─nvme0n1p15  106M   /boot/efi

nvme1n1       232.8G
```

`nvme1n1` initially had no filesystem.

The important distinction was:

```text
nvme0n1 → EBS root volume
nvme1n1 → EC2 instance-store NVMe
```

The instance-store NVMe does **not** appear as a normal EBS volume because it is physically local storage attached to the EC2 host.

## Important: instance-store is ephemeral

The ~233 GB NVMe is **not persistent storage**.

It can be mounted and configured in `/etc/fstab`, which makes it automatically mount again **if the same instance and its instance-store device remain available**.

However, the data does **not** survive replacement/recreation of the EC2 instance.

This was confirmed during the setup: after the instance was stopped/recreated, the instance-store NVMe was no longer persistent and the `/data` setup had to be recreated.

Therefore:

```text
/data on instance-store
        ↓
Fast + large
        ↓
Ephemeral
        ↓
Do NOT store the only copy of important data
```

The instance-store disk was suitable for disposable data such as:

* Docker images
* containerd data
* Hugging Face model cache
* benchmark scratch space
* temporary datasets

---

# 4. Format and Mount the Instance-Store NVMe

The 233 GB NVMe was formatted with ext4:

```bash
sudo mkfs.ext4 /dev/nvme1n1
```

A mount point was created:

```bash
sudo mkdir -p /data
sudo mount /dev/nvme1n1 /data
```

The mount was verified with:

```bash
df -h /data
```

Result:

```text
/dev/nvme1n1    229G    2.1M    217G    1%    /data
```

---

# 5. Configure Automatic Mounting

The NVMe filesystem UUID was obtained using:

```bash
sudo blkid /dev/nvme1n1
```

The filesystem was then added to:

```bash
sudo nano /etc/fstab
```

The mount configuration was tested with:

```bash
sudo umount /data
sudo mount -a
```

A systemd warning appeared:

```text
fstab has been modified, but systemd still uses the old version
```

This was resolved with:

```bash
sudo systemctl daemon-reload
```

The mount itself was successful.

### Important clarification

This configuration **does not make the instance-store persistent**.

It only means:

> When `/dev/nvme1n1` exists on this instance, Linux can automatically mount it at `/data` during boot.

It does **not** turn instance-store into EBS storage.

---

# 6. Expand the Root EBS Volume

The root EBS volume was approximately 80 GB, but the root partition initially occupied only about 7.4 GB.

The situation was approximately:

```text
Disk:             ~85.9 GB
Root partition:   ~7.4 GB
Unallocated:      ~77.3 GB
```

The partition table was inspected using:

```bash
sudo parted /dev/nvme0n1 print free
```

This produced a warning similar to:

```text
Warning: Not all of the space available ... appears to be used
Fix/Ignore?
```

`Fix` was selected.

After fixing the GPT information, the layout showed approximately:

```text
/dev/nvme0n1
    p1       7.4G
    free     77.3G
```

The root partition was then expanded:

```bash
sudo growpart /dev/nvme0n1 1
```

The ext4 filesystem was expanded:

```bash
sudo resize2fs /dev/nvme0n1p1
```

The root EBS filesystem could then use the previously unallocated capacity.

---

# 7. Docker

Docker was installed on the Ubuntu host and configured to use the NVIDIA runtime.

The NVIDIA container tooling was also configured so Docker containers could access the L4 GPU.

The important verification was that Docker could use the NVIDIA runtime and that the existing Docker configuration contained:

```text
nvidia-container-runtime
```

The exact installation commands for Docker and `nvidia-container-toolkit` are intentionally not reproduced here because they were not captured in the original notes.

---

# 8. The Docker Storage Problem

The first major issue after installing Docker was not the GPU or Docker itself.

Pulling the vLLM image failed with:

```text
write /var/lib/containerd/...: no space left on device
```

This was confusing because:

```bash
df -h /data
```

showed approximately:

```text
217 GB free
```

The actual storage situation was:

```text
/                         → small root EBS filesystem
/data                     → ~229 GB instance-store NVMe

/var/lib/containerd      → root filesystem
```

Therefore Docker/containerd was attempting to unpack the very large vLLM image onto the small root filesystem.

The free space on `/data` was irrelevant until Docker and containerd were configured to use it.

---

# 9. Configure Docker to Use `/data`

The existing Docker configuration was located at:

```bash
/etc/docker/daemon.json
```

It already contained the NVIDIA runtime configuration:

```json
{
  "runtimes": {
    "nvidia": {
      "args": [],
      "path": "nvidia-container-runtime"
    }
  }
}
```

That configuration was preserved while Docker's data directory was moved to `/data`.

The resulting configuration was:

```json
{
  "data-root": "/data/docker",
  "runtimes": {
    "nvidia": {
      "args": [],
      "path": "nvidia-container-runtime"
    }
  }
}
```

The directory was created:

```bash
sudo mkdir -p /data/docker
```

Docker was restarted:

```bash
sudo systemctl restart docker
```

The Docker root directory was verified:

```bash
docker info | grep "Docker Root Dir"
```

Result:

```text
Docker Root Dir: /data/docker
```

---

# 10. Move containerd Storage to `/data`

Changing Docker's `data-root` was **not sufficient**.

The containerd image/snapshot storage was still using:

```text
/var/lib/containerd
```

on the root filesystem.

Therefore containerd also had to be moved to the large NVMe disk.

## Stop Docker and containerd

```bash
sudo systemctl stop docker.socket
sudo systemctl stop docker
sudo systemctl stop containerd
```

## Generate containerd configuration

```bash
sudo mkdir -p /etc/containerd
containerd config default | sudo tee /etc/containerd/config.toml > /dev/null
```

The generated configuration used:

```text
root = '/var/lib/containerd'
```

This was changed to:

```text
root = '/data/containerd'
```

using:

```bash
sudo sed -i "s#root = '/var/lib/containerd'#root = '/data/containerd'#" /etc/containerd/config.toml
```

Verify:

```bash
grep '^root' /etc/containerd/config.toml
```

Expected:

```text
root = '/data/containerd'
```

Create the new directory:

```bash
sudo mkdir -p /data/containerd
```

Reload systemd:

```bash
sudo systemctl daemon-reload
```

Restart containerd:

```bash
sudo systemctl restart containerd
```

Start Docker:

```bash
sudo systemctl start docker
```

Verify containerd:

```bash
systemctl is-active containerd
```

Expected:

```text
active
```

Verify Docker:

```bash
docker info | grep "Docker Root Dir"
```

Expected:

```text
Docker Root Dir: /data/docker
```

---

# 11. Final Storage Layout

The resulting storage arrangement was:

```text
EBS
└── nvme0n1
    └── /
        ├── Ubuntu
        ├── system packages
        └── system files


EC2 Instance-Store NVMe
└── nvme1n1
    └── /data
        ├── docker/
        ├── containerd/
        └── huggingface/
```

The important distinction is:

```text
/       → EBS → persistent with the EBS volume
/data   → instance-store → ephemeral
```

Verification:

```bash
df -h / /data
```

Approximately:

```text
/       6.7G   4.3G   2.3G
/data   229G   2.3M   217G
```

The old containerd directory was checked:

```bash
sudo du -sh /data/containerd /data/docker
sudo du -sh /var/lib/containerd
```

At that point the result was approximately:

```text
/data/containerd     92K
/data/docker         184K
/var/lib/containerd  154M
```

The `154 MB` under `/var/lib/containerd` was stale data left over from the failed image pull.

Since containerd had been confirmed to use `/data/containerd`, the stale root-disk data could be removed:

```bash
sudo rm -rf /var/lib/containerd/*
```

---

# 12. vLLM

The vLLM server was run using:

```text
vllm/vllm-openai:latest
```

The server exposed an OpenAI-compatible API on:

```text
http://127.0.0.1:8000/v1
```

The selected model was:

```text
cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit
```

The Hugging Face model cache was placed on the large NVMe:

```text
/data/huggingface
```

and mounted into the container as:

```text
/root/.cache/huggingface
```

This avoids repeatedly downloading the model while the instance-store data remains available.

---

# 13. vLLM Docker Command

The working container configuration was:

```bash
docker run -d \
  --name qwen \
  --gpus all \
  --ipc=host \
  -p 8000:8000 \
  -v /data/huggingface:/root/.cache/huggingface \
  vllm/vllm-openai:latest \
  --model cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit \
  --enable-auto-tool-choice \
  --tool-call-parser qwen3_coder \
  --max-model-len 14336 \
  --gpu-memory-utilization 0.97
```

## Flag explanation

| Flag                                            | Purpose                                                           |
| ----------------------------------------------- | ----------------------------------------------------------------- |
| `--gpus all`                                    | Gives the container access to the NVIDIA L4                       |
| `--ipc=host`                                    | Provides appropriate shared-memory configuration for the workload |
| `-p 8000:8000`                                  | Exposes the vLLM API on port 8000                                 |
| `-v /data/huggingface:/root/.cache/huggingface` | Stores the Hugging Face model cache on the NVMe                   |
| `--enable-auto-tool-choice`                     | Enables automatic OpenAI-style tool calling                       |
| `--tool-call-parser qwen3_coder`                | Parses Qwen3-Coder's native tool-call format                      |
| `--max-model-len 14336`                         | Keeps the model within available L4 memory                        |
| `--gpu-memory-utilization 0.97`                 | Allows vLLM to use most of the available GPU memory               |

---

# 14. Why `qwen3_coder` Tool Parser Matters

The model emits its native tool-call format.

The generic `hermes` parser was tested and did not correctly extract the model's tool calls.

The resulting behavior was misleading:

```text
Model:
"I'll use a tool..."
```

but OpenCode did not actually execute the tool.

The correct parser is:

```bash
--tool-call-parser qwen3_coder
```

Combined with:

```bash
--enable-auto-tool-choice
```

this allows vLLM to expose the model's tool calls correctly through the OpenAI-compatible API.

---

# 15. Why `--max-model-len 14336`

The L4 has approximately 24 GB of VRAM.

The AWQ model weights consume roughly:

```text
~17 GB
```

Additional GPU memory is required for:

* CUDA graphs
* runtime overhead
* KV cache

A 32,768-token context was too large for the available memory.

The selected value was:

```text
--max-model-len 14336
```

This fit within the available GPU memory while leaving room for the KV cache.

---

# 16. Why the AWQ Model Was Selected

Several models were considered for a single 24 GB L4:

| Model                                            | Result                                                |
| ------------------------------------------------ | ----------------------------------------------------- |
| `Qwen/Qwen3-0.6B`                                | Runs, but too weak for useful agentic coding          |
| `Qwen/Qwen3-Coder-30B-A3B-Instruct` BF16         | ~62 GB; does not fit on one L4                        |
| `cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit` | **Selected; ~17 GB and works with tools**             |
| Other AWQ repositories                           | Available, but some carried significant-loss warnings |

The selected model was:

```text
cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit
```

---

# 17. Verify vLLM

After starting the container:

```bash
docker ps
```

The server needs several minutes to load the model.

The API can be checked with:

```bash
curl -s http://127.0.0.1:8000/v1/models | head -c 200
```

A successful response confirms that the OpenAI-compatible endpoint is available.

---

# 18. OpenCode Configuration

The OpenCode configuration was placed in:

```text
opencode.json
```

at the project root.

The working configuration was:

```json
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
```

## Important OpenCode configuration details

The provider field is:

```json
"provider"
```

not:

```json
"providers"
```

The package field is:

```json
"npm"
```

not:

```json
"package"
```

The API URL belongs under:

```json
"options": {
  "baseURL": "..."
}
```

The model identifier follows:

```text
provider-id/model-id
```

Therefore:

```text
vllm/cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit
```

The OpenCode context limit was matched to vLLM's:

```text
--max-model-len 14336
```

### Restart OpenCode after changing the configuration

OpenCode loads its configuration at startup.

After modifying `opencode.json`, fully quit and restart OpenCode.

---

# 19. End-to-End Tool-Calling Test

The final smoke test was:

```bash
opencode run "List the files in the current directory using a tool and report what you find."
```

Expected behavior:

```text
OpenCode
   ↓
vLLM API
   ↓
Qwen3-Coder
   ↓
tool call
   ↓
OpenCode executes bash/ls
   ↓
tool result returned to model
   ↓
model summarizes result
```

The important thing to verify is **actual tool execution**, not merely text such as:

```text
"I'll use bash..."
```

A successful test should show the model invoking the tool, receiving its output, and then producing the final response.

---

# 20. Troubleshooting

## `no space left on device`

If the vLLM image pull fails with:

```text
no space left on device
```

check:

```bash
df -h / /data
```

Then check Docker:

```bash
docker info | grep "Docker Root Dir"
```

Expected:

```text
Docker Root Dir: /data/docker
```

Check containerd:

```bash
grep '^root' /etc/containerd/config.toml
```

Expected:

```text
root = '/data/containerd'
```

Both Docker **and** containerd need to use the large NVMe storage.

---

## `auto tool choice requires --enable-auto-tool-choice`

Add:

```bash
--enable-auto-tool-choice
```

to the vLLM command.

---

## Model says it will use a tool but no tool executes

Check:

```bash
--tool-call-parser qwen3_coder
```

Do not use the generic `hermes` parser for this Qwen3-Coder setup.

A useful diagnostic is to make a direct vLLM API request with a tool and:

```text
tool_choice: "required"
```

If the response contains the raw:

```text
<tool_call>...
```

XML rather than a structured tool call, the parser configuration is incorrect.

---

## vLLM refuses to serve the model because of max model length

An error such as:

```text
ValueError: To serve at least one request...
max model length
```

means the model weights plus runtime allocations leave insufficient GPU memory for the requested context.

Reduce:

```bash
--max-model-len
```

or adjust:

```bash
--gpu-memory-utilization
```

The working configuration used:

```text
--max-model-len 14336
--gpu-memory-utilization 0.97
```

---

# 21. Quick Restart

To recreate the vLLM container:

```bash
docker rm -f qwen 2>/dev/null
```

Then start it again:

```bash
docker run -d \
  --name qwen \
  --gpus all \
  --ipc=host \
  -p 8000:8000 \
  -v /data/huggingface:/root/.cache/huggingface \
  vllm/vllm-openai:latest \
  --model cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit \
  --enable-auto-tool-choice \
  --tool-call-parser qwen3_coder \
  --max-model-len 14336 \
  --gpu-memory-utilization 0.97
```

Then wait for model loading to complete and check:

```bash
curl -s http://127.0.0.1:8000/v1/models | head -c 200
```

---

# 22. Final Working Configuration

At the point where the setup was working, the important pieces were:

```text
AWS
├── Region: ap-south-1
├── Instance: g6.xlarge
└── GPU: NVIDIA L4 (~24 GB)
```

```text
Storage
├── nvme0n1
│   └── EBS
│       └── /
│
└── nvme1n1
    └── Instance-store NVMe
        └── /data
            ├── docker
            ├── containerd
            └── huggingface
```

```text
Docker
├── Docker Root Dir: /data/docker
└── NVIDIA runtime: nvidia-container-runtime
```

```text
containerd
└── root: /data/containerd
```

```text
vLLM
├── Image: vllm/vllm-openai:latest
├── Port: 8000
├── Model: cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit
├── Context: 14336
├── GPU utilization: 0.97
└── Tool parser: qwen3_coder
```

```text
OpenCode
└── vllm/cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit
        ↓
    http://127.0.0.1:8000/v1
```

### One critical operational fact

The entire `/data` layer is backed by **EC2 instance-store**, not EBS.

So although the configuration successfully moved Docker, containerd, and the Hugging Face cache off the tiny root filesystem, **none of that `/data` content should be treated as persistent storage across EC2 instance replacement/recreation**.
