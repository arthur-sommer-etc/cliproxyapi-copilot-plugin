GO_IMAGE ?= docker.io/library/golang:1.26-bookworm
CONTAINER_RUNTIME ?= docker
CONTAINER_USER_ARGS ?= --user $$(id -u):$$(id -g)
VERSION ?= 0.3.3
CACHE_DIR := .cache
VERSION_LDFLAG := -X main.pluginVersion=$(VERSION)
LINUX_PLUGIN_DIR := build/plugins/linux/amd64
LINUX_PLUGIN := $(LINUX_PLUGIN_DIR)/cliproxyapi-copilot.so
LINUX_ARM64_PLUGIN_DIR := build/plugins/linux/arm64
LINUX_ARM64_PLUGIN := $(LINUX_ARM64_PLUGIN_DIR)/cliproxyapi-copilot.so
DARWIN_AMD64_PLUGIN_DIR := build/plugins/darwin/amd64
DARWIN_AMD64_PLUGIN := $(DARWIN_AMD64_PLUGIN_DIR)/cliproxyapi-copilot.dylib
DARWIN_PLUGIN_DIR := build/plugins/darwin/arm64
DARWIN_PLUGIN := $(DARWIN_PLUGIN_DIR)/cliproxyapi-copilot.dylib

.PHONY: test build build-local build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 package package-linux-amd64 package-linux-arm64 package-darwin-amd64 package-darwin-arm64 clean

test:
	go test ./...

build: build-linux-amd64

build-linux-amd64:
	mkdir -p $(LINUX_PLUGIN_DIR) $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod $(CACHE_DIR)/home
	$(CONTAINER_RUNTIME) run --rm \
		--platform linux/amd64 \
		$(CONTAINER_USER_ARGS) \
		-e HOME=/src/$(CACHE_DIR)/home \
		-e GOCACHE=/src/$(CACHE_DIR)/go-build \
		-e GOMODCACHE=/src/$(CACHE_DIR)/go-mod \
		-v "$(CURDIR):/src" \
		-w /src \
		$(GO_IMAGE) \
		sh -ec 'CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags "$(VERSION_LDFLAG)" -buildmode=c-shared -o $(LINUX_PLUGIN) ./cmd/cliproxyapi-copilot'

build-linux-arm64:
	mkdir -p $(LINUX_ARM64_PLUGIN_DIR) $(CACHE_DIR)/go-build-arm64 $(CACHE_DIR)/go-mod $(CACHE_DIR)/home
	$(CONTAINER_RUNTIME) run --rm \
		--platform linux/arm64 \
		$(CONTAINER_USER_ARGS) \
		-e HOME=/src/$(CACHE_DIR)/home \
		-e GOCACHE=/src/$(CACHE_DIR)/go-build-arm64 \
		-e GOMODCACHE=/src/$(CACHE_DIR)/go-mod \
		-v "$(CURDIR):/src" \
		-w /src \
		$(GO_IMAGE) \
		sh -ec 'CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build -buildvcs=false -trimpath -ldflags "$(VERSION_LDFLAG)" -buildmode=c-shared -o $(LINUX_ARM64_PLUGIN) ./cmd/cliproxyapi-copilot'

build-local:
	mkdir -p build/plugins/$$(go env GOOS)/$$(go env GOARCH)
	case "$$(go env GOOS)" in darwin) extension=dylib ;; *) extension=so ;; esac; \
	CGO_ENABLED=1 go build -buildvcs=false -trimpath -ldflags "$(VERSION_LDFLAG)" -buildmode=c-shared \
		-o build/plugins/$$(go env GOOS)/$$(go env GOARCH)/cliproxyapi-copilot.$$extension ./cmd/cliproxyapi-copilot

build-darwin-amd64:
	@test "$$(go env GOOS)" = "darwin" || \
		{ echo "error: build-darwin-amd64 requires a Darwin host" >&2; exit 1; }
	mkdir -p $(DARWIN_AMD64_PLUGIN_DIR)
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags "$(VERSION_LDFLAG)" -buildmode=c-shared -o $(DARWIN_AMD64_PLUGIN) ./cmd/cliproxyapi-copilot

build-darwin-arm64:
	@test "$$(go env GOOS)" = "darwin" || \
		{ echo "error: build-darwin-arm64 requires a Darwin host" >&2; exit 1; }
	mkdir -p $(DARWIN_PLUGIN_DIR)
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -buildvcs=false -trimpath -ldflags "$(VERSION_LDFLAG)" -buildmode=c-shared -o $(DARWIN_PLUGIN) ./cmd/cliproxyapi-copilot

package: package-linux-amd64

package-linux-amd64: build-linux-amd64
	scripts/package-release.sh "$(VERSION)" linux amd64

package-linux-arm64: build-linux-arm64
	scripts/package-release.sh "$(VERSION)" linux arm64

package-darwin-amd64: build-darwin-amd64
	scripts/package-release.sh "$(VERSION)" darwin amd64

package-darwin-arm64: build-darwin-arm64
	scripts/package-release.sh "$(VERSION)" darwin arm64

clean:
	rm -rf build dist $(CACHE_DIR)
