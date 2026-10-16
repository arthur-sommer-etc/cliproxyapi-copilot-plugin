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

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_DIR=$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)
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
mkdir -p "$DIST_DIR"
rm -f "$DIST_DIR/$ARCHIVE" "$DIST_DIR/checksums.txt"
python3 - "$PLUGIN" "$DIST_DIR/$ARCHIVE" "$SCRIPT_DIR" "$REPO_DIR" "$PLUGIN_ID" "$VERSION" <<'PY'
import json
import pathlib
import sys
import zipfile

sys.path.insert(0, sys.argv[3])
from host_requirements import selected_host_requirements

plugin = pathlib.Path(sys.argv[1])
archive = pathlib.Path(sys.argv[2])
repository = pathlib.Path(sys.argv[4])
plugin_id = sys.argv[5]
plugin_version = sys.argv[6]
requirements = selected_host_requirements(plugin_id, plugin_version, repository)
metadata = json.dumps(requirements, indent=2, sort_keys=True) + "\n"

with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as output:
    output.write(plugin, plugin.name)
    output.writestr(f"{plugin_id}.host-requirements.json", metadata)
PY
(
  cd "$DIST_DIR"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$ARCHIVE" >checksums.txt
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$ARCHIVE" >checksums.txt
  else
    printf 'error: sha256sum or shasum is required\n' >&2
    exit 1
  fi
)

printf 'Created %s and checksums.txt\n' "$DIST_DIR/$ARCHIVE"
