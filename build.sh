#!/usr/bin/env bash
#
# Збирає дистрибутив у build/: самодостатній бінар і install.sh поруч із ним.
# build/ і є коренем інсталяції -- туди ж лягають bin/, config.json, tmp/, logs/, media/.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO="${GO:-/usr/local/go/bin/go}"
OUT="${ROOT}/build"

cd "${ROOT}"
mkdir -p "${OUT}"
# При явному -o Go не додає .exe сам.
EXT=""
if [ "$("${GO}" env GOOS)" = "windows" ]; then
  EXT=".exe"
  # Іконку в PE вносить .syso; лінкер шукає його в каталозі main-пакета.
  "${GO}" run internal/assets/mkicon.go
fi
"${GO}" build -o "${OUT}/restreamd${EXT}" .
cp "${ROOT}/internal/scripts/install.sh.template" "${OUT}/install.sh"
cp "${ROOT}/internal/scripts/install.ps1.template" "${OUT}/install.ps1"
chmod +x "${OUT}/restreamd${EXT}" "${OUT}/install.sh"

echo "==> ${OUT}/restreamd${EXT}"
echo "==> ${OUT}/install.sh"
echo "==> ${OUT}/install.ps1"
echo "Copy the whole build/ directory to the server and run ./install.sh there."
