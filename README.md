# chotu

Rent a private GPU machine for coding work. Use it. Give it back.

## Why this exists

I don't own a GPU machine. A few times a month I hit the ceiling of what I can run locally, and some of that work involves private client data I don't want to paste into someone else's chat box. So instead of buying hardware or uploading data anywhere, chotu spins up a single cloud GPU box, sets itself up, and tears itself down when I'm done. The data stays on my machine. Nothing to clean up afterwards.

## What it does

One `terraform apply` gets you a working box with:

- A local LLM endpoint (vLLM serving Qwen3-Coder-30B) reachable only over SSH — no public ports
- OpenCode wired to it, so it behaves like a normal coding setup
- Your workspace synced from S3, so the box starts with your code and your data never leaves it
- A status page that shows setup progress while the box converges, then system health after
- Spot support, so a disposable run costs noticeably less than on-demand

When you're done, `terraform destroy` and it's gone: no lingering VM, no data left behind anywhere except your bucket.

## How to use

You need three things installed and working: the AWS CLI (configured — `aws sts get-caller-identity` should print your account), terraform, and this repo cloned locally. Then:

```
cd terraform
terraform init
terraform plan    # check what gets built
terraform apply   # wait ~5 minutes for setup to converge
```

Apply prints an `ssh_config_snippet`. Paste it into `~/.ssh/config`, then:

```
ssh chotu
```

With that session open: `http://localhost:8081` is the status page (setup progress, then system health), `localhost:8000` is the model endpoint. When you're done, keep the bucket and destroy everything else:

```
# Forget the bucket resources so destroy leaves the data alone
terraform state rm 'module.aws[0].aws_s3_bucket.workspace[0]' 'module.aws[0].aws_s3_bucket_versioning.workspace[0]' 'module.aws[0].aws_s3_bucket_public_access_block.workspace[0]'
terraform destroy
```

The bucket stays in AWS. On the next deploy, set `s3_create_bucket = false` to adopt it. A plain `terraform destroy` without the `state rm` fails on purpose (`prevent_destroy` on the bucket) rather than deleting workspace data.

Tweaks live in `terraform/variables.tf` — instance type, region, spot vs on-demand, bucket, model. No other files to touch for a normal deploy.

## What it won't do (yet)

Honest version: a 30B quantized model is not going to architect your system or untangle your hardest bug. For that kind of work it's mediocre. What it's genuinely good at is routine work over private data — linting, writing and running tests, churning through data analysis scripts — the stuff that's boring but can't leave your machine. If that's your bottleneck, this removes it for a few dollars.

## How it works

Terraform provisions one EC2 GPU instance (g5/g6) and cloud-init writes the machine config, then a bootstrap script installs two small Go daemons: one that converges the box (drivers, Docker, model pull, sync) and one that serves the status page. Access is SSH-only with generated keys; the status page and the model endpoint come through tunnels. The setup daemon is idempotent, so a fresh box always lands in the same state.

## Where it's going

- More clouds: Vast.ai and GCP modules, so I can chase cheaper or faster GPUs per job instead of being tied to one provider
- Bigger models (70B+) for when the task actually needs the reasoning, priced per-run via spot
- Generally: whatever removes the next bottleneck in my own development speed

Built for my own freelance work — mostly client tasks where the data can't leave my machine.
