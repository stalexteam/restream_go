#!/usr/bin/env bash
#
# Встановлення restream-controller: apt-пакети, MediaMTX, systemd-юніт і первинний сетап.
#
# Розрахований на Debian/Ubuntu (apt). Усі шляхи -- відносно каталогу самого скрипта.
# Конфіг, OBS-файли і tmp/mediamtx.yml генерує сам ./restreamd (шаблони вшиті в нього).

set -euo pipefail

MEDIAMTX_VERSION="v1.19.3"
BASE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESTREAMD_BIN="${BASE_DIR}/restreamd"
SERVICE_NAME="restreamd"
UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}.service"

echo "==> Project directory: ${BASE_DIR}"

if [ ! -x "${RESTREAMD_BIN}" ]; then
  echo "==> ERROR: ${RESTREAMD_BIN} not found or not executable"
  echo "    Build it first with ./build.sh in the source tree, then copy build/ here."
  exit 1
fi

echo "==> Installing system packages (ffmpeg, srt-tools, curl)"
sudo apt-get update -qq
sudo apt-get install -y -qq ffmpeg srt-tools curl ca-certificates

mkdir -p "${BASE_DIR}/bin" "${BASE_DIR}/media" "${BASE_DIR}/logs" "${BASE_DIR}/tmp"

if [ ! -x "${BASE_DIR}/bin/mediamtx" ]; then
  echo "==> Downloading MediaMTX ${MEDIAMTX_VERSION}"
  tmp_tar="$(mktemp)"
  curl -sL -o "${tmp_tar}" \
    "https://github.com/bluenviron/mediamtx/releases/download/${MEDIAMTX_VERSION}/mediamtx_${MEDIAMTX_VERSION}_linux_amd64.tar.gz"
  tar -xzf "${tmp_tar}" -C "${BASE_DIR}/bin" mediamtx
  rm -f "${tmp_tar}"
  chmod +x "${BASE_DIR}/bin/mediamtx"
else
  echo "==> MediaMTX already installed, skipping download"
fi

# Бінар systemctl є і там, де init інший (WSL, контейнер); /run/systemd/system -- ознака реального systemd.
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  echo "==> Installing the systemd unit ${UNIT_PATH}"
  sudo tee "${UNIT_PATH}" >/dev/null <<UNIT
[Unit]
Description=restream controller
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$(id -un)
WorkingDirectory=${BASE_DIR}
ExecStart=${RESTREAMD_BIN}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
  sudo systemctl daemon-reload
  sudo systemctl enable "${SERVICE_NAME}" >/dev/null
else
  echo "==> WARNING: systemd is not running here -- skipping the service unit"
  echo "    Start the controller manually: cd ${BASE_DIR} && ./restreamd &"
fi

# Конфіг, OBS-файли й фінальну інструкцію робить сам бінар.
echo "==> Configuration"
"${RESTREAMD_BIN}" --config

# Конфіг і OBS-файли несуть паролі й токен, tmp/ і logs/ -- ffmpeg-команди з ними.
# `|| true`: chmod не має обривати установку.
chmod 700 "${BASE_DIR}/tmp" "${BASE_DIR}/logs" 2>/dev/null || true
chmod 600 \
  "${BASE_DIR}/config.json" \
  "${BASE_DIR}/obs-dock.html" \
  "${BASE_DIR}/obs-source.html" 2>/dev/null || true
