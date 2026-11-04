# Package release artifacts

The release workflows package two targets:

| Target | Plugin library | Archive |
| --- | --- | --- |
| `linux/amd64` | `cliproxyapi-copilot.so` | `cliproxyapi-copilot_<version>_linux_amd64.zip` |
| `darwin/arm64` | `cliproxyapi-copilot.dylib` | `cliproxyapi-copilot_<version>_darwin_arm64.zip` |

The release workflow builds the macOS ARM64 package on the `macos-15` runner
and the Linux AMD64 package in Docker.
Each archive contains its plugin library and `cliproxyapi-copilot.host-requirements.json`
at the ZIP root. The metadata records the required host callback and the selected
published host module. A package command
writes the SHA-256 digest of its exact ZIP to `dist/checksums.txt`. The release
workflow combines both target lines in the release's `checksums.txt`.
GoReleaser verifies the packages and publishes them with the checksum file.
It uses the existing package script for the ZIP contents and metadata.

## Run a release snapshot

On an ARM64 Mac with Docker, the Go version in `tools/go.mod`, and Xcode Command Line Tools,
install the pinned Task and GoReleaser executables and run the snapshot from the repository root.
Ensure the Go binary directory is on `PATH` so the preparation hook can invoke Task:

```sh
(cd tools && go install github.com/go-task/task/v3/cmd/task)
mkdir -p .cache/tools
CC=/usr/bin/clang go -C tools build -o ../.cache/tools/goreleaser github.com/goreleaser/goreleaser/v2
RELEASE_VERSION=0.0.0 RELEASE_BUILD_PACKAGES=1 CC=/usr/bin/clang \
.cache/tools/goreleaser release --snapshot --clean
```

The preparation hook runs `task package` and `task package-darwin`.
It combines and verifies both SHA-256 digests before GoReleaser continues.
The ZIPs and `checksums.txt` remain in `dist/`.
GoReleaser writes its own snapshot metadata under `.cache/goreleaser-dist/`.
Snapshot mode skips publishing and does not test GitHub release creation.
Use a dotted numeric `RELEASE_VERSION` without a leading `v`.

The tagged GitHub workflow runs the same verification hook on packages built by its two jobs.
It sets `RELEASE_VERSION` from the validated tag and leaves `RELEASE_BUILD_PACKAGES` unset.
GoReleaser then publishes the ZIPs and checksums with GitHub-generated release notes.
The workflow accepts dotted numeric tags with more than three components.
GoReleaser's semver validation does not accept those tags, so the workflow uses
`--skip=validate` after `packaging/validate-release-git.sh` checks the numeric tag,
clean worktree, and exact tag at HEAD. These replace its skipped Git checks.

## Run the package tests offline

The package tests use temporary fixture libraries. They do not build or load a
CLIProxyAPI plugin and do not need Go modules for the archive tests.

Run:

```sh
PYTHONDONTWRITEBYTECODE=1 GOPROXY=off GOSUMDB=off python3 -m unittest discover -s packaging -p 'test_package_release.py' -v
```

The tests create the Linux and macOS archive names, check each archive's
top-level library, host requirements, and contents, and compare the recorded checksum with the
SHA-256 of the complete ZIP file. They also require an unsupported target to
fail before artifact lookup.

## Build a release package

Use Python 3.8 or newer and the Go version in `go.mod` to package either target.
Build the macOS archive on an ARM64 macOS host with Xcode Command Line Tools for
cgo. Build the Linux archive with Docker and the `golang:1-bookworm` image.

To build the macOS package for version `0.3.3`, set `CC=/usr/bin/clang` so cgo
uses Xcode Command Line Tools' compiler when another `clang` appears earlier on
`PATH`:

```sh
GOMODCACHE="$PWD/.cache/go-mod" GOCACHE="$PWD/.cache/go-build" \
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local CC=/usr/bin/clang \
task package-darwin VERSION=0.3.3
```

The command creates
`dist/cliproxyapi-copilot_0.3.3_darwin_arm64.zip`. The ZIP contains
`cliproxyapi-copilot.dylib` and the host requirements metadata, and
`dist/checksums.txt` contains its SHA-256 digest.

To build the Linux package for the same version, run:

```sh
GOPROXY=off GOSUMDB=off task package VERSION=0.3.3
```

The command creates
`dist/cliproxyapi-copilot_0.3.3_linux_amd64.zip`. The ZIP contains
`cliproxyapi-copilot.so` and the host requirements metadata.

## Provision Go dependencies

The package tests need no Go modules. Go tests, vet, and builds need every
module in the checked-in `go.mod` and `go.sum`.

Use the Go version in `go.mod` and provision the module cache before disabling network access.
Keep checksum verification enabled during setup:

```sh
GOMODCACHE="$PWD/.cache/go-mod" GOTOOLCHAIN=local GOSUMDB=sum.golang.org go mod download
```

Run the uncached tests and vet offline with the same module cache:

```sh
GOMODCACHE="$PWD/.cache/go-mod" GOCACHE="$PWD/.cache/go-build" \
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local CC=/usr/bin/clang \
go test -count=1 ./...
GOMODCACHE="$PWD/.cache/go-mod" GOCACHE="$PWD/.cache/go-build" \
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local CC=/usr/bin/clang \
go vet ./...
```

Tests exit successfully with `ok` for packages with tests and `[no test files]`
for packages without tests. `go vet` exits successfully without diagnostics.
The macOS `CC` setting selects Xcode Command Line Tools' compiler when a
third-party `clang` appears earlier on `PATH`.

For an offline Linux build, provision the module cache mounted into Docker and
pull the Go image before disabling network access:

```sh
GOMODCACHE="$PWD/.cache/go-mod" GOTOOLCHAIN=local GOSUMDB=sum.golang.org go mod download
docker pull golang:1-bookworm
```

Then rerun the matching package command with `GOPROXY=off GOSUMDB=off`. The
Linux build passes those settings into Docker. A build fails rather than
fetching modules if its cache is incomplete.
