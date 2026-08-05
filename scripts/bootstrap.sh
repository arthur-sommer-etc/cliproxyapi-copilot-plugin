#!/bin/sh
set -eu
umask 077

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/common.sh"

require_layout
mkdir -p "$REPO_DIR/.runtime"

random_hex() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c 'import secrets; print(secrets.token_hex(32))'
  else
    die "openssl or python3 is required to generate secrets"
  fi
}

if [ ! -f "$ENV_FILE" ]; then
  {
    printf 'MANAGEMENT_PASSWORD=%s\n' "$(random_hex)"
    printf 'CLIPROXYAPI_API_KEY=%s\n' "$(random_hex)"
  } >"$ENV_FILE"
  chmod 600 "$ENV_FILE"
fi

load_secrets

sed "s/__CLIPROXYAPI_API_KEY__/$CLIPROXYAPI_API_KEY/g" \
  "$REPO_DIR/config/config.yaml" >"$REPO_DIR/.runtime/config.yaml"
chmod 600 "$REPO_DIR/.runtime/config.yaml"

printf 'Runtime configuration created under %s/.runtime\n' "$REPO_DIR"
printf 'Secrets were generated locally and were not printed.\n'
