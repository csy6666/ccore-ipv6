#!/usr/bin/env bash
set -euo pipefail

fail=0
need() {
  command -v "$1" >/dev/null 2>&1 || { echo "missing: $1"; fail=1; }
}
need ip
need systemctl

if [ "$(uname -s)" != Linux ]; then
  echo "unsupported OS: $(uname -s)"
  exit 1
fi

echo "== kernel =="
uname -a
echo "== interfaces =="
ip -6 addr show || fail=1
if ip -o -6 addr show scope global | grep -q .; then
  echo "global IPv6 address: present"
else
  echo "global IPv6 address: missing"
  fail=1
fi
echo "== default routes =="
if ip -6 route show default; then
  if ! ip -6 route show default | grep -q .; then
    echo "IPv6 default route: missing"
    fail=1
  fi
else
  fail=1
fi
echo "== capabilities =="
if [ "$(id -u)" -ne 0 ] && ! command -v capsh >/dev/null 2>&1; then
  echo "warning: not root and capsh unavailable; capability check is incomplete"
fi
echo "== ccore-ipv6 =="
if command -v ccore-ipv6 >/dev/null 2>&1; then
  ccore-ipv6 version || fail=1
  ccore-ipv6 list || fail=1
  if systemctl is-active --quiet ccore-ipv6.service; then
    echo "ccore-ipv6.service: active"
  else
    echo "ccore-ipv6.service: inactive"
    fail=1
  fi
else
  echo "missing: ccore-ipv6"
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo "preflight: FAIL"
  exit 1
fi
echo "preflight: PASS"
