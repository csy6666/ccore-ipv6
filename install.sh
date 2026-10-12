#!/usr/bin/env bash
set -euo pipefail

PREFIX=/usr/local
BIN="$PREFIX/bin/ccore-ipv6"
UNIT=/etc/systemd/system/ccore-ipv6.service
HEALTH_UNIT=/etc/systemd/system/ccore-ipv6-health.service
HEALTH_TIMER=/etc/systemd/system/ccore-ipv6-health.timer
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
  if [ -f "$HEALTH_UNIT" ]; then cp -a "$HEALTH_UNIT" "$backup/ccore-ipv6-health.service"; fi
  if [ -f "$HEALTH_TIMER" ]; then cp -a "$HEALTH_TIMER" "$backup/ccore-ipv6-health.timer"; fi
  if [ -d "$STATE_DIR" ]; then cp -a "$STATE_DIR" "$backup/state"; fi
  echo "backup: $backup"
}
rollback_backup() {
  [ -n "${backup:-}" ] || die "no rollback backup was created"
  [ -f "$backup/ccore-ipv6.bin" ] || die "no previous binary is available for rollback"
  install -m 755 "$backup/ccore-ipv6.bin" "$BIN"
  if [ -f "$backup/ccore-ipv6.service" ]; then install -m 644 "$backup/ccore-ipv6.service" "$UNIT"; fi
  if [ -f "$backup/ccore-ipv6-health.service" ]; then install -m 644 "$backup/ccore-ipv6-health.service" "$HEALTH_UNIT"; fi
  if [ -f "$backup/ccore-ipv6-health.timer" ]; then install -m 644 "$backup/ccore-ipv6-health.timer" "$HEALTH_TIMER"; fi
  systemctl daemon-reload
  systemctl restart ccore-ipv6.service || true
  systemctl restart ccore-ipv6-health.timer || true
  die "new version failed to start; rolled back to $backup"
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
  install -d -m 750 /etc/ccore-ipv6
  if [ -f /etc/ccore-ipv6/cloudflare.env ]; then
    chown root:"$USER_NAME" /etc/ccore-ipv6/cloudflare.env
    chmod 640 /etc/ccore-ipv6/cloudflare.env
  fi
  install -d -o "$USER_NAME" -g "$USER_NAME" -m 2750 "$STATE_DIR"
  if [ -f "$STATE_DIR/state.json" ]; then chmod 644 "$STATE_DIR/state.json"; fi
  if [ -f "$STATE_DIR/state.json.lock" ]; then chgrp "$USER_NAME" "$STATE_DIR/state.json.lock"; chmod 660 "$STATE_DIR/state.json.lock"; fi
  install -m 644 systemd/ccore-ipv6.service "$UNIT"
  install -m 644 systemd/ccore-ipv6-health.service "$HEALTH_UNIT"
  install -m 644 systemd/ccore-ipv6-health.timer "$HEALTH_TIMER"
  systemctl daemon-reload
  if ! systemctl enable --now ccore-ipv6.service; then
    rollback_backup
  fi
  if ! systemctl enable --now ccore-ipv6-health.timer; then
    rollback_backup
  fi
  systemctl --no-pager --full status ccore-ipv6.service
  ;;
uninstall)
  systemctl disable --now ccore-ipv6.service || true
  systemctl disable --now ccore-ipv6-health.timer || true
  rm -f "$UNIT" "$HEALTH_UNIT" "$HEALTH_TIMER" "$BIN"
  systemctl daemon-reload
  echo "binary and service removed; state kept at $STATE_DIR"
  ;;
purge)
  systemctl disable --now ccore-ipv6.service || true
  systemctl disable --now ccore-ipv6-health.timer || true
  rm -f "$UNIT" "$HEALTH_UNIT" "$HEALTH_TIMER" "$BIN"
  rm -rf "$STATE_DIR"
  systemctl daemon-reload
  echo "binary, service and state removed"
  ;;
*) die "usage: $0 [install|update|uninstall|purge]" ;;
esac
