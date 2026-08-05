#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/common.sh"

require_layout
require_docker
[ -f "$ENV_FILE" ] || die "run scripts/bootstrap.sh first"
[ -f "$REPO_DIR/.runtime/config.yaml" ] || die "run scripts/bootstrap.sh first"
[ -f "$REPO_DIR/build/plugins/linux/amd64/cliproxyapi-copilot.so" ] ||
  die "plugin artifact missing; run make build first"
ensure_runtime_image

# Only query this repository's exact Compose project. Do not enumerate or
# inspect unrelated containers.
if ! compose ps --status running --services 2>/dev/null | grep -qx 'cliproxyapi'; then
  command -v python3 >/dev/null 2>&1 ||
    die "python3 is required to verify that host port 8417 is unused"
  python3 - "$HOST_PORT" <<'PY' || die "host port 8417 is already in use"
import socket
import sys

sock = socket.socket()
try:
    sock.bind(("127.0.0.1", int(sys.argv[1])))
finally:
    sock.close()
PY
fi

compose up -d
printf 'Started %s on http://127.0.0.1:%s\n' "$CONTAINER_NAME" "$HOST_PORT"
