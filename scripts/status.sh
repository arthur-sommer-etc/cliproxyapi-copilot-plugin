#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/common.sh"

require_layout
require_docker
[ -f "$ENV_FILE" ] || die "run scripts/bootstrap.sh first"

compose ps

if command -v curl >/dev/null 2>&1; then
  load_secrets
  code=$(
    curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
      --max-time 5 \
      -H "Authorization: Bearer $CLIPROXYAPI_API_KEY" \
      "http://127.0.0.1:$HOST_PORT/v1/models" || true
  )
  if [ "$code" = "200" ]; then
    printf 'API health: healthy (authenticated /v1/models returned 200)\n'
  else
    printf 'API health: unavailable (HTTP %s)\n' "${code:-none}"
  fi
fi
