#!/usr/bin/env bash
# Builds and installs the Chotu setup service, then enables it on boot.
set -Eeuo pipefail

readonly CHOTU_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly CHOTU_REPO="$(cd "${CHOTU_ROOT}/.." && pwd)"
readonly CHOTU_BIN="/usr/local/bin/chotu-setup"
readonly CHOTU_CONFIG="/etc/chotu/config.yaml"
readonly CHOTU_STATE_DIR="/var/lib/chotu"
readonly CHOTU_UNIT_SRC="${CHOTU_ROOT}/deploy/chotu-setup.service"
readonly CHOTU_UNIT_DST="/etc/systemd/system/chotu-setup.service"

log() { printf '[install] %s\n' "$*"; }
die() { log "ERROR: $*"; exit 1; }

[[ "${EUID}" -ne 0 ]] && { exec sudo -E bash "$0" "$@"; }

command -v go >/dev/null || die "go toolchain not found"
[[ -f "${CHOTU_UNIT_SRC}" ]] || die "missing ${CHOTU_UNIT_SRC}"

log "building chotu-setup"
(cd "${CHOTU_ROOT}" && go build -o "${CHOTU_BIN}" .)

mkdir -p "$(dirname "${CHOTU_CONFIG}")" "${CHOTU_STATE_DIR}"

if [[ ! -f "${CHOTU_CONFIG}" ]]; then
  [[ -f "${CHOTU_REPO}/config.yaml" ]] || die "no config to install"
  install -m 0644 "${CHOTU_REPO}/config.yaml" "${CHOTU_CONFIG}"
  log "installed ${CHOTU_CONFIG} from the repository copy"
else
  log "keeping the existing ${CHOTU_CONFIG}"
fi

log "installing ${CHOTU_UNIT_DST}"
install -m 0644 "${CHOTU_UNIT_SRC}" "${CHOTU_UNIT_DST}"

systemctl daemon-reload
systemctl enable --now chotu-setup.service

sleep 2
systemctl --no-pager --lines=15 status chotu-setup.service || true

log "installed and running: chotu-setup"
log "follow it with: journalctl -u chotu-setup -f"
