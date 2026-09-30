#!/usr/bin/env bash
set -Eeuo pipefail

readonly CHOTU_BOOTSTRAP_VERSION="0.1.0"
readonly CHOTU_SCRIPT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
readonly CHOTU_CONFIG_RESOLVER="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/resolve_config.py"
readonly CHOTU_DEFAULT_CONFIG="/etc/chotu/config.yaml"
readonly CHOTU_REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly CHOTU_REPO_CONFIG="${CHOTU_REPO_ROOT}/config.yaml"
readonly CHOTU_BOOTSTRAP_DIR="/var/lib/chotu/bootstrap"
readonly CHOTU_RESOLVED_CONFIG="${CHOTU_BOOTSTRAP_DIR}/config.resolved"
readonly CHOTU_STATE_DIR="${CHOTU_BOOTSTRAP_DIR}/stages"
readonly CHOTU_LOG_FILE="/var/log/chotu-bootstrap.log"

CHOTU_CONFIG_FILE="${CHOTU_CONFIG_FILE:-}"
CHOTU_ASSUME_REBOOTED="${CHOTU_ASSUME_REBOOTED:-false}"

log() {
  printf '[%s] %s\n' "$(date -Is)" "$*" | tee -a "${CHOTU_LOG_FILE}"
}

die() {
  log "ERROR: $*"
  exit 1
}

need_root() {
  if [[ "${EUID}" -ne 0 ]]; then
    exec sudo -E bash "$0" "$@"
  fi
}

stage_done() {
  [[ -f "${CHOTU_STATE_DIR}/$1.done" ]]
}

mark_stage_done() {
  mkdir -p "${CHOTU_STATE_DIR}"
  date -Is > "${CHOTU_STATE_DIR}/$1.done"
}

have_command() {
  command -v "$1" >/dev/null 2>&1
}

retry() {
  local attempts="$1"
  local delay="$2"
  shift 2

  local n=1
  until "$@"; do
    if (( n >= attempts )); then
      return 1
    fi
    sleep "${delay}"
    n=$((n + 1))
  done
}

apt_install() {
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@"
}

ensure_python3() {
  if have_command python3; then
    return
  fi

  log "python3 not found; installing python3 before resolving config"
  apt-get update
  apt_install python3
}

install_resume_service() {
  cat > /etc/systemd/system/chotu-bootstrap-resume.service <<EOF
[Unit]
Description=Resume Chotu bootstrap after reboot
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
Environment=CHOTU_CONFIG_FILE=${CHOTU_CONFIG_FILE}
ExecStart=/bin/bash ${CHOTU_SCRIPT_PATH}

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable chotu-bootstrap-resume.service
}

clear_resume_service() {
  systemctl disable --now chotu-bootstrap-resume.service >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/chotu-bootstrap-resume.service
  systemctl daemon-reload
}

resolve_config_path() {
  if [[ -n "${CHOTU_CONFIG_FILE}" ]]; then
    [[ -f "${CHOTU_CONFIG_FILE}" ]] || die "Config file not found: ${CHOTU_CONFIG_FILE}"
    return
  fi

  if [[ -f "${CHOTU_DEFAULT_CONFIG}" ]]; then
    CHOTU_CONFIG_FILE="${CHOTU_DEFAULT_CONFIG}"
  elif [[ -f "${CHOTU_REPO_CONFIG}" ]]; then
    CHOTU_CONFIG_FILE="${CHOTU_REPO_CONFIG}"
  else
    die "No config found. Set CHOTU_CONFIG_FILE or create ${CHOTU_DEFAULT_CONFIG}."
  fi
}

resolve_configuration() {
  if stage_done configuration && [[ -f "${CHOTU_RESOLVED_CONFIG}" ]]; then
    # shellcheck disable=SC1090
    source "${CHOTU_RESOLVED_CONFIG}"
    return
  fi

  resolve_config_path
  mkdir -p "${CHOTU_BOOTSTRAP_DIR}" "${CHOTU_STATE_DIR}"

  ensure_python3
  python3 "${CHOTU_CONFIG_RESOLVER}" "${CHOTU_CONFIG_FILE}" "${CHOTU_RESOLVED_CONFIG}"

  # shellcheck disable=SC1090
  source "${CHOTU_RESOLVED_CONFIG}"
  mark_stage_done configuration
  log "Resolved config: mode=${CHOTU_STORAGE_MODE}, workspace=${CHOTU_WORKSPACE}, model=${CHOTU_MODEL}"
}

machine_prep() {
  if stage_done machine_prep; then
    return
  fi

  [[ -r /etc/os-release ]] || die "Cannot read /etc/os-release"
  # shellcheck disable=SC1091
  source /etc/os-release
  [[ "${ID:-}" == "ubuntu" ]] || die "Unsupported OS: ${ID:-unknown}. Expected Ubuntu."
  [[ "${VERSION_ID:-}" == "22.04" ]] || die "Unsupported Ubuntu version: ${VERSION_ID:-unknown}. Expected 22.04."
  [[ "$(uname -m)" == "x86_64" ]] || die "Unsupported architecture: $(uname -m). Expected x86_64."

  if [[ "${CHOTU_STORAGE_MODE}" == "ephemeral" ]]; then
    [[ -d /data ]] || die "Ephemeral mode requires /data to exist"
    mountpoint -q /data || die "Ephemeral mode requires /data to be mounted"
  fi

  if ! lspci | grep -qi nvidia; then
    die "No NVIDIA PCI device detected"
  fi

  if have_command curl; then
    curl -fsS --connect-timeout 2 http://169.254.169.254/latest/meta-data/iam/security-credentials/ >/dev/null 2>&1 || true
  fi

  mark_stage_done machine_prep
  log "Machine prep passed for Ubuntu ${VERSION_ID}, $(uname -m)"
}

install_base_packages() {
  if stage_done base_packages; then
    return
  fi

  apt-get update
  apt_install ca-certificates curl gnupg lsb-release jq unzip pciutils python3
  if [[ -n "${CHOTU_TOOLING_PACKAGES:-}" ]]; then
    # shellcheck disable=SC2086
    apt_install ${CHOTU_TOOLING_PACKAGES}
  fi
  if ! have_command aws; then
    apt_install awscli
  fi

  mark_stage_done base_packages
  log "Base packages installed"
}

install_nvidia_stack() {
  if stage_done nvidia_stack; then
    return
  fi

  if ! dpkg -s nvidia-driver-580-server >/dev/null 2>&1; then
    apt-get update
    apt_install linux-headers-"$(uname -r)" || true
    apt_install nvidia-driver-580-server nvidia-utils-580-server
    touch "${CHOTU_BOOTSTRAP_DIR}/nvidia-reboot-required"
    log "NVIDIA driver installed. Reboot is required before verification."
    if [[ "${CHOTU_ASSUME_REBOOTED}" != "true" ]]; then
      install_resume_service
      systemctl reboot
      exit 0
    fi
  fi

  if [[ -f "${CHOTU_BOOTSTRAP_DIR}/nvidia-reboot-required" && "${CHOTU_ASSUME_REBOOTED}" != "true" ]]; then
    if ! nvidia-smi >/dev/null 2>&1; then
      log "NVIDIA stack still needs a reboot before continuing."
      install_resume_service
      systemctl reboot
      exit 0
    fi
  fi

  curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey \
    | gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
  curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
    | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' \
    > /etc/apt/sources.list.d/nvidia-container-toolkit.list

  apt-get update
  apt_install nvidia-container-toolkit

  nvidia-smi >/dev/null || die "nvidia-smi failed after NVIDIA installation"
  nvidia-ctk --version >/dev/null || die "nvidia-ctk not available"
  systemctl start nvidia-cdi-refresh.service >/dev/null 2>&1 || true
  nvidia-ctk cdi list | grep -q 'nvidia.com/gpu' || die "NVIDIA CDI did not expose any GPUs"

  rm -f "${CHOTU_BOOTSTRAP_DIR}/nvidia-reboot-required"
  mark_stage_done nvidia_stack
  log "NVIDIA driver/toolkit verified"
}

configure_storage_layout() {
  if stage_done storage_layout; then
    return
  fi

  mkdir -p "${CHOTU_STATE}" "${CHOTU_MODEL_CACHE}" "${CHOTU_WORKSPACE}"
  chown -R "${CHOTU_RUN_USER}:${CHOTU_RUN_USER}" "${CHOTU_STATE}" "${CHOTU_MODEL_CACHE}" "${CHOTU_WORKSPACE}"

  if [[ "${CHOTU_STORAGE_MODE}" == "ephemeral" ]]; then
    mkdir -p "${CHOTU_DOCKER_ROOT}" /data/containerd
  fi

  mark_stage_done storage_layout
  log "Storage layout ready at ${CHOTU_STORAGE_ROOT}"
}

install_docker() {
  if stage_done docker; then
    return
  fi

  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
    -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc

  # shellcheck disable=SC1091
  source /etc/os-release
  cat > /etc/apt/sources.list.d/docker.list <<EOF
deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${VERSION_CODENAME} stable
EOF

  apt-get update
  apt_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

  mkdir -p /etc/docker
  if [[ "${CHOTU_STORAGE_MODE}" == "ephemeral" ]]; then
    cat > /etc/docker/daemon.json <<EOF
{
  "data-root": "${CHOTU_DOCKER_ROOT}",
  "runtimes": {
    "nvidia": {
      "args": [],
      "path": "nvidia-container-runtime"
    }
  }
}
EOF
  else
    cat > /etc/docker/daemon.json <<'EOF'
{
  "runtimes": {
    "nvidia": {
      "args": [],
      "path": "nvidia-container-runtime"
    }
  }
}
EOF
  fi

  nvidia-ctk runtime configure --runtime=docker >/dev/null
  usermod -aG docker "${CHOTU_RUN_USER}" || true
  systemctl enable --now docker
  systemctl restart docker

  retry 12 5 docker info >/dev/null || die "Docker daemon did not become ready"
  docker run --rm --runtime=nvidia --gpus all ubuntu nvidia-smi >/dev/null || die "Docker GPU smoke test failed"

  mark_stage_done docker
  log "Docker and GPU runtime verified"
}

install_rclone_and_s3() {
  if stage_done rclone_s3; then
    return
  fi

  if ! have_command rclone; then
    apt-get update
    apt_install rclone
  fi

  local user_config_dir="${CHOTU_RUN_HOME}/.config/rclone"
  mkdir -p "${user_config_dir}" "${CHOTU_RUN_HOME}/.config/chotu"
  cat > "${user_config_dir}/rclone.conf" <<'EOF'
[s3]
type = s3
provider = AWS
env_auth = true
EOF

  if [[ -n "${CHOTU_SYNC_EXCLUSIONS:-}" ]]; then
    printf '%s\n' "${CHOTU_SYNC_EXCLUSIONS}" > "${CHOTU_RUN_HOME}/.config/chotu/workspace-filter"
  else
    : > "${CHOTU_RUN_HOME}/.config/chotu/workspace-filter"
  fi
  chown -R "${CHOTU_RUN_USER}:${CHOTU_RUN_USER}" "${CHOTU_RUN_HOME}/.config/rclone" "${CHOTU_RUN_HOME}/.config/chotu"

  sudo -u "${CHOTU_RUN_USER}" rclone listremotes | grep -q '^s3:$' || die "rclone S3 remote was not configured"
  if [[ -n "${CHOTU_S3_BUCKET}" ]]; then
    sudo -u "${CHOTU_RUN_USER}" rclone lsd "s3:${CHOTU_S3_BUCKET}" >/dev/null || die "Cannot access S3 bucket ${CHOTU_S3_BUCKET}"
  else
    log "sync.bucket is empty; configured rclone but skipped bucket verification"
  fi

  mark_stage_done rclone_s3
  log "rclone configured for IAM-backed S3"
}

start_vllm() {
  if stage_done vllm; then
    return
  fi

  docker rm -f chotu-vllm >/dev/null 2>&1 || true
  docker pull "${CHOTU_INFERENCE_IMAGE}"
  docker run -d \
    --name chotu-vllm \
    --restart unless-stopped \
    --gpus all \
    --ipc=host \
    -p "${CHOTU_INFERENCE_PORT}:8000" \
    -v "${CHOTU_MODEL_CACHE}:/root/.cache/huggingface" \
    "${CHOTU_INFERENCE_IMAGE}" \
    --model "${CHOTU_MODEL}" \
    --enable-auto-tool-choice \
    --tool-call-parser qwen3_coder \
    --max-model-len "${CHOTU_MAX_MODEL_LEN}" \
    --gpu-memory-utilization "${CHOTU_GPU_MEMORY_UTILIZATION}"

  retry 120 10 curl -fsS "http://127.0.0.1:${CHOTU_INFERENCE_PORT}/v1/models" >/dev/null || {
    docker logs --tail=200 chotu-vllm >&2 || true
    die "vLLM API did not become ready"
  }

  mark_stage_done vllm
  log "vLLM is ready on port ${CHOTU_INFERENCE_PORT}"
}

install_opencode() {
  if stage_done opencode; then
    return
  fi

  if ! sudo -u "${CHOTU_RUN_USER}" bash -lc 'command -v opencode >/dev/null 2>&1'; then
    sudo -u "${CHOTU_RUN_USER}" bash -lc 'curl -fsSL https://opencode.ai/install | bash'
  fi

  cat > "${CHOTU_WORKSPACE}/opencode.json" <<EOF
{
  "\$schema": "https://opencode.ai/config.json",
  "model": "vllm/${CHOTU_MODEL}",
  "provider": {
    "vllm": {
      "name": "Local vLLM",
      "npm": "@ai-sdk/openai-compatible",
      "options": {
        "baseURL": "http://127.0.0.1:${CHOTU_INFERENCE_PORT}/v1"
      },
      "models": {
        "${CHOTU_MODEL}": {
          "name": "Qwen3-Coder-30B-A3B (AWQ)",
          "limit": {
            "context": ${CHOTU_MAX_MODEL_LEN},
            "output": ${CHOTU_OUTPUT_LIMIT}
          }
        }
      }
    }
  }
}
EOF

  mkdir -p "${CHOTU_WORKSPACE}/project"
  chown -R "${CHOTU_RUN_USER}:${CHOTU_RUN_USER}" "${CHOTU_WORKSPACE}"

  mark_stage_done opencode
  log "OpenCode configured in ${CHOTU_WORKSPACE}"
}

configure_sync_services() {
  if stage_done sync_services; then
    return
  fi

  if [[ -z "${CHOTU_S3_BUCKET}" ]]; then
    log "sync.bucket is empty; skipping workspace sync service"
    mark_stage_done sync_services
    return
  fi

  local destination="s3:${CHOTU_S3_BUCKET}"
  if [[ -n "${CHOTU_S3_PREFIX}" ]]; then
    destination="${destination}/${CHOTU_S3_PREFIX}"
  fi

  cat > /etc/systemd/system/chotu-workspace-sync.service <<EOF
[Unit]
Description=Sync Chotu workspace to S3
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=${CHOTU_RUN_USER}
Environment=HOME=${CHOTU_RUN_HOME}
ExecStart=/usr/bin/rclone sync ${CHOTU_WORKSPACE} ${destination} --filter-from ${CHOTU_RUN_HOME}/.config/chotu/workspace-filter --create-empty-src-dirs
EOF

  cat > /etc/systemd/system/chotu-workspace-sync.timer <<EOF
[Unit]
Description=Run Chotu workspace sync every ${CHOTU_SYNC_INTERVAL}

[Timer]
OnBootSec=1min
OnUnitActiveSec=${CHOTU_SYNC_INTERVAL}
Unit=chotu-workspace-sync.service

[Install]
WantedBy=timers.target
EOF

  systemctl daemon-reload
  systemctl enable --now chotu-workspace-sync.timer

  mark_stage_done sync_services
  log "Workspace sync timer enabled"
}

configure_chotu_status() {
  if stage_done chotu_status; then
    return
  fi

  mkdir -p /usr/local/lib/chotu
  cat > /usr/local/lib/chotu/status-page.py <<'PY'
#!/usr/bin/env python3
import http.server
import json
import os
import subprocess

PORT = int(os.environ.get("CHOTU_STATUS_PORT", "8080"))


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path not in ("/", "/healthz"):
            self.send_response(404)
            self.end_headers()
            return
        payload = {
            "ok": True,
            "service": "chotu",
            "model": os.environ.get("CHOTU_MODEL", ""),
            "workspace": os.environ.get("CHOTU_WORKSPACE", ""),
            "vllm": self._curl_ok(),
        }
        body = json.dumps(payload, indent=2).encode()
        self.send_response(200 if payload["vllm"] else 503)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return

    def _curl_ok(self):
        port = os.environ.get("CHOTU_INFERENCE_PORT", "8000")
        try:
            subprocess.run(
                ["curl", "-fsS", f"http://127.0.0.1:{port}/v1/models"],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=2,
                check=True,
            )
            return True
        except Exception:
            return False


http.server.HTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
PY
  chmod +x /usr/local/lib/chotu/status-page.py

  cat > /etc/systemd/system/chotu-status.service <<EOF
[Unit]
Description=Chotu status page
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/bin/bash -lc 'set -a; source "${CHOTU_RESOLVED_CONFIG}"; exec /usr/bin/python3 /usr/local/lib/chotu/status-page.py'
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable --now chotu-status.service

  mark_stage_done chotu_status
  log "Chotu status service enabled on port ${CHOTU_STATUS_PORT}"
}

install_chotu_setup() {
  local installer="${CHOTU_REPO_ROOT}/chotu-setup/deploy/install.sh"

  if [[ ! -f "${installer}" ]]; then
    die "chotu-setup installer not found at ${installer}"
  fi

  log "Installing and starting the Chotu setup service"
  "${installer}"
  systemctl is-active --quiet chotu-setup.service || die "chotu-setup.service is not active"
}

final_verification() {
  nvidia-smi >/dev/null || die "Final verification failed: nvidia-smi"
  docker info >/dev/null || die "Final verification failed: docker"
  docker ps --format '{{.Names}}' | grep -q '^chotu-vllm$' || die "Final verification failed: chotu-vllm container missing"
  curl -fsS "http://127.0.0.1:${CHOTU_INFERENCE_PORT}/v1/models" >/dev/null || die "Final verification failed: vLLM API"
  [[ -f "${CHOTU_WORKSPACE}/opencode.json" ]] || die "Final verification failed: opencode config missing"
  systemctl is-active --quiet chotu-status.service || die "Final verification failed: status service"
  systemctl is-active --quiet chotu-setup.service || die "Final verification failed: chotu-setup service"
  clear_resume_service
  log "Chotu bootstrap complete"
}

main() {
  need_root "$@"
  mkdir -p "${CHOTU_BOOTSTRAP_DIR}" "${CHOTU_STATE_DIR}"
  touch "${CHOTU_LOG_FILE}"

  log "Starting Chotu bootstrap ${CHOTU_BOOTSTRAP_VERSION}"
  resolve_configuration
  machine_prep
  install_base_packages
  configure_storage_layout
  install_nvidia_stack
  install_docker
  install_rclone_and_s3
  start_vllm
  install_opencode
  configure_sync_services
  configure_chotu_status
  install_chotu_setup
  final_verification
}

main "$@"
