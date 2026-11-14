#!/usr/bin/env sh
set -e

version=$(packaging/package-release.sh --validate-version "${RELEASE_VERSION:-}")
checksums=$(mktemp)
trap 'rm -f "$checksums"' EXIT HUP INT TERM
if [ "${RELEASE_BUILD_PACKAGES:-0}" = 1 ]; then
  task package VERSION="$version"
  cp dist/checksums.txt "$checksums"
  task package-darwin VERSION="$version"
  cat dist/checksums.txt >> "$checksums"
  cp "$checksums" dist/checksums.txt
fi

(
  cd dist
  shasum -a 256 \
    "cliproxyapi-copilot_${version}_linux_amd64.zip" \
    "cliproxyapi-copilot_${version}_darwin_arm64.zip" > "$checksums"
  cmp "$checksums" checksums.txt
)
