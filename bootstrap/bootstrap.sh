#!/usr/bin/env bash
#
# Chotu bootstrap.
#
# Downloads and starts the two Chotu daemons:
#   - status-service
#   - chotu-setup
#
# status-service must be running before chotu-setup starts.
#
# Bootstrap does not build binaries.
#

set -Eeuo pipefail

readonly CHOTU_BOOTSTRAP_VERSION="0.1.0"
readonly CHOTU_LOG_FILE="/var/log/chotu-bootstrap.log"

readonly STATUS_BINARY="/usr/local/bin/status-service"
readonly SETUP_BINARY="/usr/local/bin/chotu-setup"

# Binaries come from the chotu GitHub release page. The artifact names match
# what `make release` publishes in chotu-setup and status-service, and the
# architecture is detected from the machine itself.
#
# Nothing is published there yet. Until the first release, override
# STATUS_BINARY_URL and SETUP_BINARY_URL in the environment to point at a dev
# build; the download failure otherwise names the exact URL that is missing.
readonly CHOTU_RELEASE_VERSION="${CHOTU_RELEASE_VERSION:-v0.1.0}"
readonly CHOTU_RELEASE_BASE="${CHOTU_RELEASE_BASE:-https://github.com/goravaa/chotu/releases/download}"

release_arch() {
    case "$(uname -m)" in
        x86_64) echo "amd64" ;;
        aarch64 | arm64) echo "arm64" ;;
        *) die "Unsupported architecture $(uname -m): chotu publishes amd64 and arm64 binaries" ;;
    esac
}

readonly STATUS_BINARY_URL="${STATUS_BINARY_URL:-${CHOTU_RELEASE_BASE}/${CHOTU_RELEASE_VERSION}/chotu-status-linux-$(release_arch)}"
readonly SETUP_BINARY_URL="${SETUP_BINARY_URL:-${CHOTU_RELEASE_BASE}/${CHOTU_RELEASE_VERSION}/chotu-setup-linux-$(release_arch)}"

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
After=network.target chotu-status.service
Requires=chotu-status.service

[Service]
Type=simple
ExecStart=${SETUP_BINARY} -config /etc/chotu/config.yaml
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

    # Download both binaries before starting either service.
    install_status_service
    install_chotu_setup

    # status-service must be running before chotu-setup.
    install_status_service_daemon
    install_setup_daemon

    log "Bootstrap complete"
}

main "$@"