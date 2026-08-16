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
[ "$("${GO}" env GOOS)" = "windows" ] && EXT=".exe"
"${GO}" build -o "${OUT}/restreamd${EXT}" .
cp "${ROOT}/install.sh" "${OUT}/install.sh"
chmod +x "${OUT}/restreamd${EXT}" "${OUT}/install.sh"

echo "==> ${OUT}/restreamd${EXT}"
echo "==> ${OUT}/install.sh"
echo "Copy the whole build/ directory to the server and run ./install.sh there."
