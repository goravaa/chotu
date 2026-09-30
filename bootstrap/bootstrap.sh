#!/usr/bin/env bash
#
# Chotu bootstrap.
#
# This step makes the chotu-setup daemon available and runs it. It deliberately
# does nothing else: installing Docker, the NVIDIA stack, rclone, OpenCode, the
# inference container, the status page and the workspace sync all belong to the
# daemon, which owns the task graph in chotu-setup/internals/setup/tasks.go and
# re-runs it on every cycle.
#
# Keeping setup in one place matters more than keeping bootstrap short: two
# implementations of the same machine drift apart, and the one that is not
# tested is the one that breaks.

set -Eeuo pipefail

readonly CHOTU_BOOTSTRAP_VERSION="0.1.0"
readonly CHOTU_SCRIPT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
readonly CHOTU_REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

readonly CHOTU_SETUP_DIR="${CHOTU_REPO_ROOT}/chotu-setup"
readonly CHOTU_CONFIG="${CHOTU_CONFIG_FILE:-/etc/chotu/config.yaml}"
readonly CHOTU_STATE_DIR="${CHOTU_STATE_DIR:-/var/lib/chotu}"
readonly CHOTU_REBOOT_MARKER="${CHOTU_STATE_DIR}/reboot-required"
readonly CHOTU_RESUME_UNIT="/etc/systemd/system/chotu-bootstrap-resume.service"
readonly CHOTU_SETUP_UNIT="chotu-setup.service"
readonly CHOTU_LOG_FILE="/var/log/chotu-bootstrap.log"

# How long to wait for the first setup cycle to settle before deciding whether
# the machine needs a reboot. A cold cycle pulls container images, so this is
# generous on purpose.
readonly CHOTU_FIRST_CYCLE_TIMEOUT="${CHOTU_FIRST_CYCLE_TIMEOUT:-900}"

CHOTU_REBOOTED="${CHOTU_REBOOTED:-false}"

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

# install_config places the user-facing config where the daemon expects it. An
# existing file is never overwritten: the config is the user's, and this script
# may run again on every boot.
install_config() {
  mkdir -p "$(dirname "${CHOTU_CONFIG}")"

  if [[ -f "${CHOTU_CONFIG}" ]]; then
    log "Using the existing ${CHOTU_CONFIG}"
    return
  fi

  local source="${CHOTU_REPO_ROOT}/config.yaml"

  [[ -f "${source}" ]] || die "No config to install and no ${source} to take it from"

  install -m 0644 "${source}" "${CHOTU_CONFIG}"
  log "Installed ${CHOTU_CONFIG}"
}

# build_daemon delegates to the makefile, which owns the build, the unit file
# and the install layout. bootstrap.sh does not know how the binary is built.
build_daemon() {
  [[ -f "${CHOTU_SETUP_DIR}/makefile" ]] ||
    die "No makefile at ${CHOTU_SETUP_DIR}/makefile"

  command -v go >/dev/null ||
    die "The go toolchain is required to build ${CHOTU_SETUP_DIR}"

  log "Building and installing chotu-setup"

  make -C "${CHOTU_SETUP_DIR}" install \
    CONFIG_SRC="${CHOTU_REPO_ROOT}/config.yaml" \
    STATE_DIR="${CHOTU_STATE_DIR}"
}

start_daemon() {
  log "Starting ${CHOTU_SETUP_UNIT}"

  systemctl daemon-reload
  systemctl enable --now "${CHOTU_SETUP_UNIT}"
  systemctl is-active --quiet "${CHOTU_SETUP_UNIT}" ||
    die "${CHOTU_SETUP_UNIT} did not start; see journalctl -u ${CHOTU_SETUP_UNIT}"
}

install_resume_unit() {
  cat > "${CHOTU_RESUME_UNIT}" <<EOF
[Unit]
Description=Resume Chotu bootstrap after a reboot
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
Environment=CHOTU_REBOOTED=true
ExecStart=${CHOTU_SCRIPT_PATH}

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable chotu-bootstrap-resume.service
}

remove_resume_unit() {
  systemctl disable --now chotu-bootstrap-resume.service >/dev/null 2>&1 || true
  rm -f "${CHOTU_RESUME_UNIT}"
  systemctl daemon-reload
}

# wait_for_settle blocks until the daemon has finished a cycle or asks for a
# reboot. The daemon is the only component that can answer either question, so
# bootstrap.sh waits on the marker it writes rather than re-checking the machine
# itself.
wait_for_settle() {
  local deadline=$(( SECONDS + CHOTU_FIRST_CYCLE_TIMEOUT ))

  log "Waiting up to ${CHOTU_FIRST_CYCLE_TIMEOUT}s for the first setup cycle"

  while (( SECONDS < deadline )); do
    if [[ -f "${CHOTU_REBOOT_MARKER}" ]]; then
      return 0
    fi

    sleep 5
  done

  log "The first cycle is still running; leaving the daemon to finish it"

  return 1
}

# reboot_if_required restarts the machine when the daemon installed something
# that cannot take effect until it does, such as the NVIDIA driver. This only
# runs while the machine is still coming up: once a user is on it, rebooting
# without asking is not this script's decision to make.
reboot_if_required() {
  [[ -f "${CHOTU_REBOOT_MARKER}" ]] || return 0
  [[ "${CHOTU_REBOOTED}" != "true" ]] || {
    log "This boot is already the resume boot and the machine still needs a reboot; not rebooting again"
    return 1
  }

  log "The setup daemon reported that a reboot is required; rebooting now"

  install_resume_unit
  systemctl reboot

  exit 0
}

main() {
  need_root "$@"

  mkdir -p "${CHOTU_STATE_DIR}"
  touch "${CHOTU_LOG_FILE}"

  log "Chotu bootstrap ${CHOTU_BOOTSTRAP_VERSION} (daemon delivery only)"

  install_config
  build_daemon
  start_daemon

  if ! wait_for_settle; then
    log "Bootstrap handed over to ${CHOTU_SETUP_UNIT}"
    exit 0
  fi

  reboot_if_required
}

main "$@"
