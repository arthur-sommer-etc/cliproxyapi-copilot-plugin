#!/bin/sh
set -eu

VERSION=${1:-}
PLUGIN_ID="cliproxyapi-copilot"
GOOS="linux"
GOARCH="amd64"

case "$VERSION" in
  "" | *[!0-9.]* | .* | *. | *..*)
    printf 'error: version must be dotted numeric without a leading v\n' >&2
    exit 1
    ;;
esac
case "$VERSION" in
  *.*) ;;
  *)
    printf 'error: version must contain at least two numeric components\n' >&2
    exit 1
    ;;
esac

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
PLUGIN="$REPO_DIR/build/plugins/$GOOS/$GOARCH/$PLUGIN_ID.so"
DIST_DIR="$REPO_DIR/dist"
ARCHIVE="$PLUGIN_ID"_"$VERSION"_"$GOOS"_"$GOARCH".zip

[ -f "$PLUGIN" ] || {
  printf 'error: plugin artifact is missing; run make build first\n' >&2
  exit 1
}
command -v python3 >/dev/null 2>&1 || {
  printf 'error: python3 is required\n' >&2
  exit 1
}
command -v sha256sum >/dev/null 2>&1 || {
  printf 'error: sha256sum is required\n' >&2
  exit 1
}

mkdir -p "$DIST_DIR"
rm -f "$DIST_DIR/$ARCHIVE" "$DIST_DIR/checksums.txt"
python3 - "$PLUGIN" "$DIST_DIR/$ARCHIVE" <<'PY'
import pathlib
import sys
import zipfile

plugin = pathlib.Path(sys.argv[1])
archive = pathlib.Path(sys.argv[2])
with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as output:
    output.write(plugin, plugin.name)
PY
(
  cd "$DIST_DIR"
  sha256sum "$ARCHIVE" >checksums.txt
)

printf 'Created %s and checksums.txt\n' "$DIST_DIR/$ARCHIVE"
