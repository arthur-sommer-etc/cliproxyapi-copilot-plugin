#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/common.sh"

require_layout
require_docker
[ -f "$ENV_FILE" ] || die "missing isolated environment file; refusing to guess"

# This exact project scope cannot select containers from another deployment.
compose down
printf 'Stopped only Compose project %s; its named auth volume was retained.\n' "$PROJECT_NAME"
