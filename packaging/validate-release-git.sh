#!/usr/bin/env sh
set -e

tag=${RELEASE_TAG:?RELEASE_TAG is required}
case "$tag" in
  v*) packaging/package-release.sh --validate-version "${tag#v}" >/dev/null ;;
  *) printf 'error: release tag must start with v\n' >&2; exit 1 ;;
esac

status=$(git status --porcelain)
if [ -n "$status" ]; then
  printf 'error: release requires a clean Git worktree\n' >&2
  exit 1
fi
git describe --exact-match --tags --match "$tag" HEAD >/dev/null
