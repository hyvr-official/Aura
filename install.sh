#!/usr/bin/env bash
set -euo pipefail

REPO="hyvr-official/Aura"
INSTALL_DIR="/opt/aura"
SERVICE_NAME="aura"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
PORT="8445"
WEBHOOK_PORT="8600"

info() {
	printf '\033[36m==>\033[0m %s\n' "$1"
}

fail() {
	printf '\033[31mError:\033[0m %s\n' "$1" >&2
	exit 1
}

if [ "${EUID}" -ne 0 ]; then
	fail "This script must be run as root. Try: sudo bash install.sh"
fi

if [ "$(uname -s)" != "Linux" ]; then
	fail "Aura only supports Linux."
fi

UPDATE_MODE="false"
if [ -f "${INSTALL_DIR}/aura" ]; then
	UPDATE_MODE="true"
	info "Existing installation found in ${INSTALL_DIR}, updating to the latest version"
fi

command -v systemctl >/dev/null 2>&1 || fail "systemd is required but systemctl was not found."
command -v tar >/dev/null 2>&1 || fail "tar is required but was not found."

if command -v curl >/dev/null 2>&1; then
	DOWNLOADER="curl"
elif command -v wget >/dev/null 2>&1; then
	DOWNLOADER="wget"
else
	fail "curl or wget is required but neither was found."
fi

case "$(uname -m)" in
	x86_64|amd64)
		ARCH="x86_64"
		;;
	aarch64|arm64)
		ARCH="arm64"
		;;
	*)
		fail "Unsupported architecture: $(uname -m)"
		;;
esac

ASSET="aura_linux_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

info "Downloading ${ASSET} from the latest release"
if [ "${DOWNLOADER}" = "curl" ]; then
	curl -fSL --progress-bar "${URL}" -o "${TMP_DIR}/${ASSET}" || fail "Download failed: ${URL}"
else
	wget -q --show-progress -O "${TMP_DIR}/${ASSET}" "${URL}" || fail "Download failed: ${URL}"
fi

info "Extracting archive"
tar -xzf "${TMP_DIR}/${ASSET}" -C "${TMP_DIR}"
[ -f "${TMP_DIR}/aura" ] || fail "The archive does not contain the aura binary."

if systemctl is-active --quiet "${SERVICE_NAME}"; then
	info "Stopping running ${SERVICE_NAME} service"
	systemctl stop "${SERVICE_NAME}"
fi

if [ "${UPDATE_MODE}" = "true" ]; then
	info "Updating executable ${INSTALL_DIR}/aura"
else
	info "Installing to ${INSTALL_DIR}"
	mkdir -p "${INSTALL_DIR}"
fi
install -m 0755 "${TMP_DIR}/aura" "${INSTALL_DIR}/aura"

if [ "${UPDATE_MODE}" = "false" ] || [ ! -f "${SERVICE_FILE}" ]; then
	info "Creating service file ${SERVICE_FILE}"
	cat > "${SERVICE_FILE}" <<EOF
[Unit]
Description=Aura Server Control Panel
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=${INSTALL_DIR}
ExecStart=${INSTALL_DIR}/aura
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
fi

info "Starting ${SERVICE_NAME} service"
systemctl daemon-reload
systemctl enable "${SERVICE_NAME}" >/dev/null 2>&1
systemctl restart "${SERVICE_NAME}"

sleep 2

if ! systemctl is-active --quiet "${SERVICE_NAME}"; then
	journalctl -u "${SERVICE_NAME}" -n 20 --no-pager || true
	fail "The ${SERVICE_NAME} service failed to start."
fi

if [ "${UPDATE_MODE}" = "true" ]; then
	printf '\n\033[32mAura has been updated to the latest version and is running.\033[0m\n\n'
	printf 'Check status:systemctl status %s\n' "${SERVICE_NAME}"
	printf 'View logs:    journalctl -u %s -f\n' "${SERVICE_NAME}"
	exit 0
fi

HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
if [ -z "${HOST_IP}" ]; then
	HOST_IP="localhost"
fi

printf '\n\033[32mAura has been installed and is running.\033[0m\n\n'
printf 'Open \033[1mhttp://%s:%s/setup\033[0m in your browser to complete the setup.\n\n' "${HOST_IP}" "${PORT}"
printf 'Repository webhooks are received on port \033[1m%s\033[0m. Open this port in your firewall to use them.\n\n' "${WEBHOOK_PORT}"
printf 'Check status:systemctl status %s\n' "${SERVICE_NAME}"
printf 'View logs:    journalctl -u %s -f\n' "${SERVICE_NAME}"
