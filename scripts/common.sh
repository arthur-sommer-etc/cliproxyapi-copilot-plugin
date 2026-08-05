#!/bin/sh
set -eu

PROJECT_NAME="cliproxyapi-official-copilot-dev"
CONTAINER_NAME="cliproxyapi-official-copilot-dev"
VOLUME_NAME="cliproxyapi_official_copilot_dev_home"
HOST_PORT="8417"
RUNTIME_IMAGE="eceasy/cli-proxy-api:7.2.118"
PUBLISHED_RUNTIME_IMAGE="eceasy/cli-proxy-api:v7.2.118"

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
COMPOSE_FILE="$REPO_DIR/docker-compose.yml"
ENV_FILE="$REPO_DIR/.runtime/secrets.env"

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_layout() {
  [ -f "$COMPOSE_FILE" ] || die "missing repository-local docker-compose.yml"
  [ "$PROJECT_NAME" = "cliproxyapi-official-copilot-dev" ] || die "unsafe project name"
  [ "$CONTAINER_NAME" = "cliproxyapi-official-copilot-dev" ] || die "unsafe container name"
  [ "$VOLUME_NAME" = "cliproxyapi_official_copilot_dev_home" ] || die "unsafe volume name"
  [ "$HOST_PORT" = "8417" ] || die "unsafe host port"

  # Container port 8317 is required by the pinned image; reject it only when it
  # appears in the host-port position.
  grep -Eq "^[[:space:]]*-[[:space:]]*[\"']?((127\\.0\\.0\\.1|0\\.0\\.0\\.0|\\[::\\]):)?(8317|3458):" "$COMPOSE_FILE" &&
    die "compose file maps a forbidden host port"
  grep -q 'ccs_home' "$COMPOSE_FILE" &&
    die "compose file references a forbidden volume"
  grep -q '127.0.0.1:8417:8317' "$COMPOSE_FILE" ||
    die "compose file must publish only 127.0.0.1:8417"
  grep -q 'cliproxyapi_official_copilot_dev_home' "$COMPOSE_FILE" ||
    die "compose file does not use the isolated volume"
}

require_docker() {
  command -v docker >/dev/null 2>&1 || die "docker is required"
  docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required"
}

ensure_runtime_image() {
  if docker image inspect "$RUNTIME_IMAGE" >/dev/null 2>&1; then
    return
  fi
  if docker pull "$RUNTIME_IMAGE"; then
    return
  fi
  printf 'Registry tag %s is unavailable; using official published tag %s.\n' \
    "$RUNTIME_IMAGE" "$PUBLISHED_RUNTIME_IMAGE" >&2
  docker pull "$PUBLISHED_RUNTIME_IMAGE"
  docker tag "$PUBLISHED_RUNTIME_IMAGE" "$RUNTIME_IMAGE"
}

compose() {
  docker compose --project-name "$PROJECT_NAME" --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
}

load_secrets() {
  [ -f "$ENV_FILE" ] || die "run scripts/bootstrap.sh first"
  MANAGEMENT_PASSWORD=$(sed -n 's/^MANAGEMENT_PASSWORD=//p' "$ENV_FILE")
  CLIPROXYAPI_API_KEY=$(sed -n 's/^CLIPROXYAPI_API_KEY=//p' "$ENV_FILE")
  case "$MANAGEMENT_PASSWORD:$CLIPROXYAPI_API_KEY" in
    *[!0-9a-f:]* | :* | *:) die "isolated secrets file has an invalid format" ;;
  esac
  export MANAGEMENT_PASSWORD CLIPROXYAPI_API_KEY
}
