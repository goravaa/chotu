#!/usr/bin/env bash
#
# Chotu bootstrap.
#
# Downloads and starts the two Chotu daemons:
#   - status-service
#   - chotu-setup
#
# Bootstrap does not build binaries.
#

set -Eeuo pipefail

readonly CHOTU_BOOTSTRAP_VERSION="0.1.0"
readonly CHOTU_LOG_FILE="/var/log/chotu-bootstrap.log"

readonly STATUS_BINARY="/usr/local/bin/status-service"
readonly SETUP_BINARY="/usr/local/bin/chotu-setup"

readonly STATUS_BINARY_URL="${STATUS_BINARY_URL:-https://example.com/status-service}"
readonly SETUP_BINARY_URL="${SETUP_BINARY_URL:-https://example.com/chotu-setup}"

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

download_binary() {
    local url="$1"
    local destination="$2"
    local name="$3"

    log "Downloading ${name}"

    curl \
        --fail \
        --silent \
        --show-error \
        --location \
        --output "${destination}" \
        "${url}" ||
        die "Failed to download ${name} from ${url}"

    chmod +x "${destination}"

    log "${name} installed at ${destination}"
}

install_status_service() {
    download_binary \
        "${STATUS_BINARY_URL}" \
        "${STATUS_BINARY}" \
        "status-service"
}

install_chotu_setup() {
    download_binary \
        "${SETUP_BINARY_URL}" \
        "${SETUP_BINARY}" \
        "chotu-setup"
}

install_status_service_daemon() {
    log "Installing status-service systemd service"

    cat > /etc/systemd/system/chotu-status.service <<EOF
[Unit]
Description=Chotu Status Service
After=network.target

[Service]
Type=simple
ExecStart=${STATUS_BINARY}
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable --now chotu-status.service

    systemctl is-active --quiet chotu-status.service ||
        die "status-service failed to start"

    log "status-service daemon is running"
}

install_setup_daemon() {
    log "Installing chotu-setup systemd service"

    cat > /etc/systemd/system/chotu-setup.service <<EOF
[Unit]
Description=Chotu Setup Daemon
After=network.target

[Service]
Type=simple
ExecStart=${SETUP_BINARY}
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable --now chotu-setup.service

    systemctl is-active --quiet chotu-setup.service ||
        die "chotu-setup failed to start"

    log "chotu-setup daemon is running"
}

main() {
    need_root "$@"

    mkdir -p "$(dirname "${CHOTU_LOG_FILE}")"
    touch "${CHOTU_LOG_FILE}"

    log "Chotu bootstrap ${CHOTU_BOOTSTRAP_VERSION}"

    install_status_service
    install_chotu_setup

    install_status_service_daemon
    install_setup_daemon

    log "Bootstrap complete"
}

main "$@"