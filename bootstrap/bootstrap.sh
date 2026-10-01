#!/usr/bin/env bash
#
# Chotu bootstrap.
#
# This step only prepares Docker and starts the status-service.
# The chotu-setup daemon is built and installed separately with its Makefile.
#

set -Eeuo pipefail

readonly CHOTU_BOOTSTRAP_VERSION="0.1.0"

readonly STATUS_IMAGE="${STATUS_IMAGE:-garvw/chotu-status-service:2026-10-01}"
readonly STATUS_CONTAINER="${STATUS_CONTAINER:-chotu-status}"
readonly STATUS_PORT="${STATUS_PORT:-8081}"
readonly CHOTU_LOG_FILE="/var/log/chotu-bootstrap.log"

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

install_docker() {
    if command -v docker >/dev/null 2>&1; then
        log "Docker is already installed"
        return
    fi

    log "Installing Docker"

    apt-get update
    apt-get install -y docker.io

    systemctl enable --now docker

    command -v docker >/dev/null 2>&1 ||
        die "Docker installation failed"

    log "Docker installed successfully"
}

start_docker() {
    log "Starting Docker"

    systemctl enable --now docker

    systemctl is-active --quiet docker ||
        die "Docker service is not running"
}

run_status_service() {
    log "Pulling status service image: ${STATUS_IMAGE}"

    docker pull "${STATUS_IMAGE}"

    if docker container inspect "${STATUS_CONTAINER}" >/dev/null 2>&1; then
        log "Removing existing ${STATUS_CONTAINER} container"
        docker rm -f "${STATUS_CONTAINER}" >/dev/null
    fi

    log "Starting status service on port ${STATUS_PORT}"

    docker run -d \
        --name "${STATUS_CONTAINER}" \
        --restart unless-stopped \
        -p "${STATUS_PORT}:${STATUS_PORT}" \
        "${STATUS_IMAGE}" >/dev/null

    sleep 2

    if ! docker ps --filter "name=^/${STATUS_CONTAINER}$" --filter "status=running" --format '{{.Names}}' |
        grep -qx "${STATUS_CONTAINER}"; then
        log "Status service failed to start"
        docker logs "${STATUS_CONTAINER}" || true
        die "Status service container is not running"
    fi

    log "Status service is running"
}

main() {
    need_root "$@"

    mkdir -p "$(dirname "${CHOTU_LOG_FILE}")"
    touch "${CHOTU_LOG_FILE}"

    log "Chotu bootstrap ${CHOTU_BOOTSTRAP_VERSION}"

    install_docker
    start_docker
    run_status_service

    log "Bootstrap complete"
    log "Status service: ${STATUS_IMAGE}"
    log "Status service port: ${STATUS_PORT}"
    log "Chotu setup can now be installed separately with its Makefile"
}

main "$@"