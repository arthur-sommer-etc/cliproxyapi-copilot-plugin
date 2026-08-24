#!/bin/sh
set -eu

VERSION=${1:-}
GOOS=${2:-linux}
GOARCH=${3:-amd64}
PLUGIN_ID="cliproxyapi-copilot"

case "$VERSION" in
  "" | *[!0-9.]* | .* | *. | *..*)
    printf 'error: version must be dotted numeric without a leading v\n' >&2
    exit 1
    ;;
esac

case "$GOOS/$GOARCH" in
  linux/amd64) EXTENSION="so" ;;
  linux/arm64) EXTENSION="so" ;;
  darwin/amd64) EXTENSION="dylib" ;;
  darwin/arm64) EXTENSION="dylib" ;;
  *)
    printf 'error: unsupported release platform: %s/%s\n' "$GOOS" "$GOARCH" >&2
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

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_DIR=$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)
PLUGIN="$REPO_DIR/build/plugins/$GOOS/$GOARCH/$PLUGIN_ID.$EXTENSION"
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
mkdir -p "$DIST_DIR"
rm -f "$DIST_DIR/$ARCHIVE" "$DIST_DIR/checksums.txt"
python3 - "$PLUGIN" "$DIST_DIR/$ARCHIVE" "$DIST_DIR/checksums.txt" <<'PY'
import hashlib
import pathlib
import sys
import zipfile

plugin = pathlib.Path(sys.argv[1])
archive = pathlib.Path(sys.argv[2])
checksums = pathlib.Path(sys.argv[3])
with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as output:
    output.write(plugin, plugin.name)
digest = hashlib.sha256(archive.read_bytes()).hexdigest()
checksums.write_text(f"{digest}  {archive.name}\n", encoding="utf-8")
PY

printf 'Created %s and checksums.txt\n' "$DIST_DIR/$ARCHIVE"
