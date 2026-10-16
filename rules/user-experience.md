# Chotu Intended Bootstrap Experience

## Goal

After `terraform apply`, Chotu should take care of machine setup with minimal user intervention and provide continuous visibility into what is happening.

## Expected Experience

1. Terraform creates the machine from `terraform/variables.tf` (the control panel) with `terraform.tfvars` overrides.
2. The status service becomes available as early as possible, reachable only over the SSH tunnel — nothing is publicly exposed.
3. The Chotu setup daemon connects to the status service and begins reporting progress.
4. Setup and preflight tasks execute through the task graph.
5. The status page continuously shows setup progress, task state, and important errors.
6. If a major problem occurs, the status page clearly explains:

   * what failed,
   * why it failed when known,
   * and what the user needs to change or fix.
7. The user corrects a terraform variable and re-applies; the machine is replaced and converges on its own. No manual reconstruction, no SSH surgery.
8. Under normal conditions, setup completes in approximately five minutes.
9. Once complete, the machine has the expected working environment:

   * workspace available,
   * OpenCode running,
   * S3 synchronization running,
   * vLLM/GPU environment running,
   * and usage/system information visible on the status page.

## Design Principle

The status page is the user's primary view of the bootstrap process. Setup failures should be visible and actionable rather than requiring the user to inspect daemon logs or SSH into the machine.

The setup system should be idempotent and safe to re-run so that retries, restarts, and redeployments converge toward the intended final state.

## Operating Agreements

Settled decisions. Follow them unless a new ADR overturns one:

- Machines are cattle: config changes replace the instance, never patched over SSH.
- Private by default: SSH-only security group; status page and inference ride the tunnel.
- `dev.config.yaml` is the local-run default and format reference; deployed config is rendered by terraform.
- Status is a runtime monitor: in-memory only, no persistence, timings included.
- Cloud modules share one contract (same inputs, `instance_id` / `public_ip` / `status_url` / `ssh` out); root picks exactly one.
