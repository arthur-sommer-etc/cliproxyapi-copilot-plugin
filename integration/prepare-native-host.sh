#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_DIR=$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)
TARGET_GOOS=${NATIVE_HOST_GOOS:-}
TARGET_GOARCH=${NATIVE_HOST_GOARCH:-}
NATIVE_HOST_SOURCE=${NATIVE_HOST_SOURCE:-}
HOST_MODULE=github.com/router-for-me/CLIProxyAPI/v8
TEST_LIST_PATTERN='^(TestNativeHost|TestFilteredChildEnvironment)'
REQUIRED_TESTS='TestNativeHostProtocolRoundTrips TestNativeHostOAuthContinuityPersistsAcrossRestart TestNativeHostOAuthExcludedModelsFilterPluginModels TestNativeHostOAuthSettingsOverrideCopilotModelContext TestFilteredChildEnvironment TestNativeHostCanonicalResponsesRouting TestNativeHostConfiguredCanonicalResponsesCompaction TestNativeHostPluginResponsesWebsocket TestNativeHostResponseInterrupt TestNativeHostPayloadFinalization TestNativeHostPayloadFinalizationPreservesCodexReservedSchemas'

if [ -z "$TARGET_GOOS" ] || [ -z "$TARGET_GOARCH" ]; then
  case "$(uname -s)" in
    Linux) DETECTED_GOOS=linux ;;
    Darwin) DETECTED_GOOS=darwin ;;
    *)
      printf 'error: native host preparation requires Linux or macOS\n' >&2
      exit 1
      ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) DETECTED_GOARCH=amd64 ;;
    aarch64 | arm64) DETECTED_GOARCH=arm64 ;;
    *)
      printf 'error: unsupported native host architecture: %s\n' "$(uname -m)" >&2
      exit 1
      ;;
  esac
  TARGET_GOOS=${TARGET_GOOS:-$DETECTED_GOOS}
  TARGET_GOARCH=${TARGET_GOARCH:-$DETECTED_GOARCH}
fi

case "$TARGET_GOOS/$TARGET_GOARCH" in
  linux/amd64 | linux/arm64 | darwin/amd64 | darwin/arm64) ;;
  *)
    printf 'error: unsupported native host target: %s/%s\n' "$TARGET_GOOS" "$TARGET_GOARCH" >&2
    exit 1
    ;;
esac

case "$TARGET_GOOS" in
  linux) PLUGIN_EXT=so ;;
  darwin) PLUGIN_EXT=dylib ;;
esac

PLUGIN="$REPO_DIR/build/plugins/$TARGET_GOOS/$TARGET_GOARCH/cliproxyapi-copilot.$PLUGIN_EXT"
if [ ! -f "$PLUGIN" ]; then
  printf 'error: native plugin artifact is missing: %s\n' "$PLUGIN" >&2
  printf 'Build the plugin before preparing native host tests.\n' >&2
  exit 1
fi

GO_SHIM=$(command -v go || true)
if [ -z "$GO_SHIM" ]; then
  printf 'error: Go is required to prepare native host test binaries\n' >&2
  exit 1
fi
CALLER_HOME=${HOME:-}
CALLER_PATH=${PATH:-}
if [ -z "$CALLER_HOME" ] || [ -z "$CALLER_PATH" ]; then
  printf 'error: HOME and PATH are required to resolve the selected Go toolchain\n' >&2
  exit 1
fi
if ! GO_ROOT=$(
  cd "$REPO_DIR" && env -i \
    HOME="$CALLER_HOME" \
    PATH="$CALLER_PATH" \
    GOENV=off \
    GOWORK=off \
    GOFLAGS= \
    "$GO_SHIM" env GOROOT
); then
  printf 'error: could not resolve the selected Go toolchain GOROOT\n' >&2
  exit 1
fi
GO_BIN="$GO_ROOT/bin/go"
if [ ! -x "$GO_BIN" ]; then
  printf 'error: selected Go toolchain executable is missing: %s\n' "$GO_BIN" >&2
  exit 1
fi
SAFE_PATH="$GO_ROOT/bin:/usr/local/bin:/usr/local/sbin:/usr/bin:/bin:/usr/sbin:/sbin"
PREP_HOME="$REPO_DIR/.cache/home"
GO_BUILD_CACHE="$REPO_DIR/.cache/go-build"
GO_MODULE_CACHE="$REPO_DIR/.cache/go-mod"
PREPARED_DIR="$REPO_DIR/.cache/native-host/$TARGET_GOOS/$TARGET_GOARCH"
mkdir -p "$PREP_HOME" "$GO_BUILD_CACHE" "$GO_MODULE_CACHE" "$PREPARED_DIR"

BUILD_DIR=$(mktemp -d "$PREPARED_DIR/.prepare.XXXXXX")
cleanup() {
  rm -rf "$BUILD_DIR"
}
trap cleanup 0
trap 'exit 1' HUP INT TERM

run_go() {
  env -i \
    PATH="$SAFE_PATH" \
    HOME="$PREP_HOME" \
    TMPDIR="$BUILD_DIR" \
    GOENV=off \
    GOWORK=off \
    GOTOOLCHAIN=local \
    GOFLAGS= \
    GOCACHE="$GO_BUILD_CACHE" \
    GOMODCACHE="$GO_MODULE_CACHE" \
    GOPROXY=https://proxy.golang.org \
    GOOS="$TARGET_GOOS" \
    GOARCH="$TARGET_GOARCH" \
    CGO_ENABLED=1 \
    "$GO_BIN" "$@"
}

if [ -n "$NATIVE_HOST_SOURCE" ]; then
  case "$NATIVE_HOST_SOURCE" in
    /*) HOST_MODULE_DIR=$(CDPATH='' cd -- "$NATIVE_HOST_SOURCE" && pwd) ;;
    *) HOST_MODULE_DIR=$(CDPATH='' cd -- "$REPO_DIR/$NATIVE_HOST_SOURCE" && pwd) ;;
  esac
  HOST_SOURCE_LABEL="explicit local source"
else
  (
    cd "$REPO_DIR"
    run_go mod download "$HOST_MODULE"
  )
  MODULE_IDENTITY=$(cd "$REPO_DIR" && run_go list -mod=readonly -m -f '{{.Version}}|{{if .Replace}}{{.Replace.Path}}{{end}}|{{if .Replace}}{{.Replace.Version}}{{end}}|{{.Dir}}' "$HOST_MODULE")
  IFS='|' read -r HOST_VERSION HOST_REPLACEMENT_PATH HOST_REPLACEMENT_VERSION HOST_MODULE_DIR <<EOF
$MODULE_IDENTITY
EOF
  if [ -z "$HOST_VERSION" ]; then
    printf 'error: expected a versioned %s module, got %s\n' "$HOST_MODULE" "$MODULE_IDENTITY" >&2
    exit 1
  fi
  case "$HOST_REPLACEMENT_PATH" in
    '')
      HOST_SOURCE_LABEL="official module $HOST_VERSION"
      ;;
    github.com/ririnto/CLIProxyAPI/v8)
      if [ -z "$HOST_REPLACEMENT_VERSION" ]; then
        printf 'error: the maintained CLIProxyAPI replacement must use a published module version\n' >&2
        exit 1
      fi
      HOST_SOURCE_LABEL="maintained fork module $HOST_REPLACEMENT_PATH@$HOST_REPLACEMENT_VERSION"
      ;;
    *)
      printf 'error: unsupported CLIProxyAPI replacement source: %s\n' "$HOST_REPLACEMENT_PATH" >&2
      exit 1
      ;;
  esac
fi

if [ ! -f "$HOST_MODULE_DIR/go.mod" ] || [ ! -f "$HOST_MODULE_DIR/cmd/server/main.go" ]; then
  printf 'error: selected CLIProxyAPI module does not contain the server source\n' >&2
  exit 1
fi
if ! sed -n '1p' "$HOST_MODULE_DIR/go.mod" | grep -Fqx -- "module $HOST_MODULE"; then
  printf 'error: selected server source has an unexpected module path\n' >&2
  exit 1
fi

printf 'Building CLIProxyAPI server from %s\n' "$HOST_SOURCE_LABEL"
(
  cd "$HOST_MODULE_DIR"
  run_go build -mod=readonly -buildvcs=false -trimpath -o "$BUILD_DIR/cli-proxy-api" ./cmd/server
)

printf 'Compiling native host integration tests for %s/%s\n' "$TARGET_GOOS" "$TARGET_GOARCH"
(
  cd "$REPO_DIR"
  run_go test -mod=readonly -c -o "$BUILD_DIR/integration.test" ./integration
)

"$BUILD_DIR/integration.test" -test.list "$TEST_LIST_PATTERN" >"$BUILD_DIR/test-list"
for test_name in $REQUIRED_TESTS; do
  if ! grep -Fqx -- "$test_name" "$BUILD_DIR/test-list"; then
    printf 'error: integration binary is missing required test: %s\n' "$test_name" >&2
    exit 1
  fi
done

chmod 0755 "$BUILD_DIR/cli-proxy-api" "$BUILD_DIR/integration.test"
mv "$BUILD_DIR/cli-proxy-api" "$PREPARED_DIR/cli-proxy-api"
mv "$BUILD_DIR/integration.test" "$PREPARED_DIR/integration.test"
printf 'Prepared native host binaries in .cache/native-host/%s/%s\n' "$TARGET_GOOS" "$TARGET_GOARCH"
