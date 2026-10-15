GO_IMAGE ?= golang:1-bookworm
export GO_IMAGE NATIVE_HOST_SOURCE
VERSION ?= 0.3.3
PLUGIN_DIR := build/plugins/linux/amd64
PLUGIN_SO := $(PLUGIN_DIR)/cliproxyapi-copilot.so
CACHE_DIR := .cache
VERSION_LDFLAG := -X main.pluginVersion=$(VERSION)

.PHONY: test build build-local package prepare-runtime-host test-native clean

define PREPARE_NATIVE_HOST_CONTAINER
@set -eu; \
	REPO_DIR=$$(pwd -P); \
	HOST_SOURCE_DIR=; \
	REQUESTED_HOST_SOURCE=$${NATIVE_HOST_SOURCE:-}; \
	if [ -n "$$REQUESTED_HOST_SOURCE" ]; then \
		case "$$REQUESTED_HOST_SOURCE" in \
			/*) ;; \
			*) REQUESTED_HOST_SOURCE="$$REPO_DIR/$$REQUESTED_HOST_SOURCE" ;; \
		esac; \
		if [ ! -d "$$REQUESTED_HOST_SOURCE" ] || [ ! -r "$$REQUESTED_HOST_SOURCE" ]; then \
			printf 'error: NATIVE_HOST_SOURCE is not a readable directory: %s\n' "$$REQUESTED_HOST_SOURCE" >&2; \
			exit 1; \
		fi; \
		if ! HOST_SOURCE_DIR=$$(CDPATH='' cd -- "$$REQUESTED_HOST_SOURCE" && pwd -P); then \
			printf 'error: could not resolve NATIVE_HOST_SOURCE: %s\n' "$$REQUESTED_HOST_SOURCE" >&2; \
			exit 1; \
		fi; \
		if [ ! -r "$$HOST_SOURCE_DIR/go.mod" ] || [ ! -f "$$HOST_SOURCE_DIR/go.mod" ] || \
			[ ! -r "$$HOST_SOURCE_DIR/cmd/server/main.go" ] || [ ! -f "$$HOST_SOURCE_DIR/cmd/server/main.go" ]; then \
			printf 'error: NATIVE_HOST_SOURCE must be a readable CLIProxyAPI checkout: %s\n' "$$HOST_SOURCE_DIR" >&2; \
			exit 1; \
		fi; \
	fi; \
	if [ "$(1)" = validate ]; then exit 0; fi; \
	set -- run --rm --platform=linux/amd64 \
		--user "$$(id -u):$$(id -g)" \
		-e HOME=/src/.cache/home \
		-e GOENV=off \
		-e GOWORK=off \
		-e GOTOOLCHAIN=auto \
		-e GOCACHE=/src/.cache/go-build \
		-e GOMODCACHE=/src/.cache/go-mod \
		-e NATIVE_HOST_GOOS=linux \
		-e NATIVE_HOST_GOARCH=amd64; \
	if [ -n "$$HOST_SOURCE_DIR" ]; then \
		set -- "$$@" \
			-e NATIVE_HOST_SOURCE=/native-host-source \
			-v "$$HOST_SOURCE_DIR:/native-host-source:ro"; \
	fi; \
	set -- "$$@" \
		-v "$$REPO_DIR:/src" \
		-w /src \
		"$$GO_IMAGE" integration/prepare-native-host.sh; \
	docker "$$@"
endef

test:
	go test ./...
	python3 -m unittest discover -s packaging -p 'test_*.py'
	python3 -m unittest discover -s integration -p 'test_*.py'

build:
	mkdir -p $(PLUGIN_DIR) $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod $(CACHE_DIR)/home
	docker run --rm --platform=linux/amd64 \
		--user "$$(id -u):$$(id -g)" \
		-e HOME=/src/$(CACHE_DIR)/home \
		-e GOCACHE=/src/$(CACHE_DIR)/go-build \
		-e GOMODCACHE=/src/$(CACHE_DIR)/go-mod \
		-v "$(CURDIR):/src" \
		-w /src \
		$(GO_IMAGE) \
		sh -ec 'CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags "$(VERSION_LDFLAG)" -buildmode=c-shared -o $(PLUGIN_SO) ./cmd/cliproxyapi-copilot'

build-local:
	mkdir -p $(PLUGIN_DIR)
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags "$(VERSION_LDFLAG)" -buildmode=c-shared -o $(PLUGIN_SO) ./cmd/cliproxyapi-copilot

package: build
	packaging/package-release.sh "$(VERSION)"

prepare-runtime-host:
	$(call PREPARE_NATIVE_HOST_CONTAINER,validate)
	$(MAKE) -C "$(CURDIR)" build
	$(call PREPARE_NATIVE_HOST_CONTAINER,run)

ifeq ($(shell uname -s),Linux)
test-native:
	mkdir -p $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod $(CACHE_DIR)/home
	$(call PREPARE_NATIVE_HOST_CONTAINER,run)
	NATIVE_HOST_GOOS=linux NATIVE_HOST_GOARCH=amd64 NATIVE_HOST_IMAGE="$(GO_IMAGE)" integration/test-native-host.sh
else
test-native:
	integration/prepare-native-host.sh
	NATIVE_HOST_IMAGE="$(GO_IMAGE)" integration/test-native-host.sh
endif

clean:
	rm -rf build dist $(CACHE_DIR)
