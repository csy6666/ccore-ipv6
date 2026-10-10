#!/usr/bin/env bash
set -euo pipefail

PREFIX=/usr/local
BIN="$PREFIX/bin/ccore-ipv6"
UNIT=/etc/systemd/system/ccore-ipv6.service
STATE_DIR=/var/lib/ccore-ipv6
USER_NAME=ccore-ipv6
BACKUP_ROOT=/var/backups/ccore-ipv6

die() { echo "error: $*" >&2; exit 1; }
backup_existing() {
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  backup="$BACKUP_ROOT/$stamp"
  install -d -m 700 "$backup"
  if [ -f "$BIN" ]; then cp -a "$BIN" "$backup/ccore-ipv6.bin"; fi
  if [ -f "$UNIT" ]; then cp -a "$UNIT" "$backup/ccore-ipv6.service"; fi
  if [ -d "$STATE_DIR" ]; then cp -a "$STATE_DIR" "$backup/state"; fi
  echo "backup: $backup"
}
[ "$(id -u)" -eq 0 ] || die "run as root"
[ "$(uname -s)" = Linux ] || die "Linux is required"

case "${1:-install}" in
install|update)
  test -x ./ccore-ipv6 || die "build ccore-ipv6 first and run this script from the project directory"
  backup_existing
  install -m 755 ./ccore-ipv6 "$BIN"
  getent group "$USER_NAME" >/dev/null || groupadd --system "$USER_NAME"
  id "$USER_NAME" >/dev/null 2>&1 || useradd --system --gid "$USER_NAME" --no-create-home --shell /usr/sbin/nologin "$USER_NAME"
  install -d -o "$USER_NAME" -g "$USER_NAME" -m 2750 "$STATE_DIR"
  if [ -f "$STATE_DIR/state.json" ]; then chmod 644 "$STATE_DIR/state.json"; fi
  if [ -f "$STATE_DIR/state.json.lock" ]; then chgrp "$USER_NAME" "$STATE_DIR/state.json.lock"; chmod 660 "$STATE_DIR/state.json.lock"; fi
  install -m 644 systemd/ccore-ipv6.service "$UNIT"
  systemctl daemon-reload
  systemctl enable --now ccore-ipv6.service
  systemctl --no-pager --full status ccore-ipv6.service
  ;;
uninstall)
  systemctl disable --now ccore-ipv6.service || true
  rm -f "$UNIT" "$BIN"
  systemctl daemon-reload
  echo "binary and service removed; state kept at $STATE_DIR"
  ;;
purge)
  systemctl disable --now ccore-ipv6.service || true
  rm -f "$UNIT" "$BIN"
  rm -rf "$STATE_DIR"
  systemctl daemon-reload
  echo "binary, service and state removed"
  ;;
*) die "usage: $0 [install|update|uninstall|purge]" ;;
esac
