#!/usr/bin/env bash
#
# Готує реліз у build/: бінарі обох платформ, інсталятори і два архіви.
# internal/scripts/VERSION тримає номер ЦЬОГО релізу; після збірки patch
# росте. BUMP=0 -- зібрати, не витрачаючи номер.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO="${GO:-/usr/local/go/bin/go}"
OUT="${ROOT}/build"
VERSION_FILE="${ROOT}/internal/scripts/VERSION"

cd "${ROOT}"
mkdir -p "${OUT}"

VERSION="$(tr -d '[:space:]' < "${VERSION_FILE}")"
case "${VERSION}" in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "==> ERROR: ${VERSION_FILE} must hold MAJOR.MINOR.PATCH, got '${VERSION}'" >&2; exit 1 ;;
esac
echo "==> Version ${VERSION}"

GOOS=linux GOARCH=amd64 "${GO}" build -o "${OUT}/restreamd" .
cp "${ROOT}/internal/scripts/install.sh.template" "${OUT}/install.sh"
chmod +x "${OUT}/restreamd" "${OUT}/install.sh"

# Іконку в PE вносить .syso; лінкер шукає його в каталозі main-пакета.
"${GO}" run internal/assets/mkicon.go
GOOS=windows GOARCH=amd64 "${GO}" build -o "${OUT}/restreamd.exe" .
cp "${ROOT}/internal/scripts/install.ps1.template" "${OUT}/install.ps1"

LINUX_ARCHIVE="build_${VERSION}_linux64.tar.gz"
WIN_ARCHIVE="build_${VERSION}_win64.tar.gz"
tar -czf "${OUT}/${LINUX_ARCHIVE}" -C "${OUT}" restreamd install.sh
tar -czf "${OUT}/${WIN_ARCHIVE}" -C "${OUT}" restreamd.exe install.ps1

if [ "${BUMP:-1}" != "0" ]; then
  NEXT="$(awk -F. '{printf "%d.%d.%d\n", $1, $2, $3 + 1}' "${VERSION_FILE}")"
  echo "${NEXT}" > "${VERSION_FILE}"
fi

echo "==> ${OUT}/${LINUX_ARCHIVE}"
echo "==> ${OUT}/${WIN_ARCHIVE}"
echo "Upload both archives as the release assets; each unpacks to a binary and its installer."
