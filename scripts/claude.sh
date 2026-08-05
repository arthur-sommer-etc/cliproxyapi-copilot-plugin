#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/common.sh"

load_secrets

export ANTHROPIC_BASE_URL="http://127.0.0.1:$HOST_PORT"
export ANTHROPIC_AUTH_TOKEN="$CLIPROXY_API_KEY"
export ANTHROPIC_API_KEY="$CLIPROXY_API_KEY"
export ANTHROPIC_MODEL="gpt-5.6-sol"
export ANTHROPIC_DEFAULT_FABLE_MODEL="claude-fable-5"
export ANTHROPIC_DEFAULT_OPUS_MODEL="gpt-5.6-sol"
export ANTHROPIC_DEFAULT_HAIKU_MODEL="gpt-5.6-terra"
export CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY="1"

# Exclude global settings so this launcher cannot inherit the existing CCR deployment.
exec claude --setting-sources "" "$@"
