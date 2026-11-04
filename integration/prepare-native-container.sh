#!/bin/sh
set -eu

repo_dir=$(pwd -P)
host_source_dir=
requested_host_source=${NATIVE_HOST_SOURCE:-}
if [ -n "$requested_host_source" ]; then
  case "$requested_host_source" in
    /*) ;;
    *) requested_host_source="$repo_dir/$requested_host_source" ;;
  esac
  if [ ! -d "$requested_host_source" ] || [ ! -r "$requested_host_source" ]; then
    printf 'error: NATIVE_HOST_SOURCE is not a readable directory: %s\n' "$requested_host_source" >&2
    exit 1
  fi
  if ! host_source_dir=$(CDPATH='' cd -- "$requested_host_source" && pwd -P); then
    printf 'error: could not resolve NATIVE_HOST_SOURCE: %s\n' "$requested_host_source" >&2
    exit 1
  fi
  if [ ! -r "$host_source_dir/go.mod" ] || [ ! -f "$host_source_dir/go.mod" ] || \
    [ ! -r "$host_source_dir/cmd/server/main.go" ] || [ ! -f "$host_source_dir/cmd/server/main.go" ]; then
    printf 'error: NATIVE_HOST_SOURCE must be a readable CLIProxyAPI checkout: %s\n' "$host_source_dir" >&2
    exit 1
  fi
fi
if [ "${1:-}" = validate ]; then
  exit 0
fi
if [ "${1:-}" != run ]; then
  printf 'error: expected validate or run\n' >&2
  exit 1
fi
set -- run --rm --platform=linux/amd64 \
  --user "$(id -u):$(id -g)" \
  -e HOME=/src/.cache/home \
  -e GOENV=off -e GOWORK=off -e GOTOOLCHAIN=auto \
  -e GOCACHE=/src/.cache/go-build -e GOMODCACHE=/src/.cache/go-mod \
  -e NATIVE_HOST_GOOS=linux -e NATIVE_HOST_GOARCH=amd64
if [ -n "$host_source_dir" ]; then
  set -- "$@" -e NATIVE_HOST_SOURCE=/native-host-source \
    -v "$host_source_dir:/native-host-source:ro"
fi
set -- "$@" -v "$repo_dir:/src" -w /src "$COPILOT_TASK_GO_IMAGE" integration/prepare-native-host.sh
docker "$@"
