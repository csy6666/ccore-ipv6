#!/usr/bin/env bash
set -euo pipefail

REPO="csy6666/ccore-ipv6"
VERSION="${1:-latest}"
BASE="https://github.com/$REPO"
TMP_DIR=""

die() { echo "error: $*" >&2; exit 1; }
cleanup() { if [ -n "$TMP_DIR" ]; then rm -rf "$TMP_DIR"; fi; }
trap cleanup EXIT

[ "$(id -u)" -eq 0 ] || die "run as root"
[ "$(uname -s)" = Linux ] || die "Linux is required"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

if [ "$VERSION" = latest ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | awk -F'"' '/"tag_name"[[:space:]]*:/ {print $4; exit}')
  [ -n "$VERSION" ] || die "could not determine the latest release"
fi
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "version must look like v0.1.0 (got $VERSION)"

TMP_DIR=$(mktemp -d)
ARCHIVE="ccore-ipv6-${VERSION}-linux-${ARCH}.tar.gz"
URL="$BASE/releases/download/$VERSION/$ARCHIVE"
curl -fL --retry 3 --retry-delay 1 -o "$TMP_DIR/$ARCHIVE" "$URL"
curl -fL --retry 3 --retry-delay 1 -o "$TMP_DIR/SHA256SUMS" "$BASE/releases/download/$VERSION/SHA256SUMS"
awk -v name="$ARCHIVE" '$2 == name { print }' "$TMP_DIR/SHA256SUMS" > "$TMP_DIR/SHA256SUMS.selected"
test -s "$TMP_DIR/SHA256SUMS.selected" || die "release checksum entry is missing"
(cd "$TMP_DIR" && sha256sum -c SHA256SUMS.selected)
tar -xzf "$TMP_DIR/$ARCHIVE" -C "$TMP_DIR"
test -x "$TMP_DIR/ccore-ipv6" || die "release archive has no executable"
test -f "$TMP_DIR/install.sh" || die "release archive has no install script"
(cd "$TMP_DIR" && ./install.sh install)
