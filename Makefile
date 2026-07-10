.PHONY: all release release-assets require-release-version build linux test test_x86 darwin deploy run clean harness-check harness-verify harness-review-gate

BINARY_NAME = mot
BIN_DIR = bin
VERSION ?= dev

VARS_PKG = github.com/SisyphusSQ/mongo-overview-tool/vars

BUILD_FLAGS  = -X '${VARS_PKG}.AppName=${BINARY_NAME}'
BUILD_FLAGS += -X '${VARS_PKG}.AppVersion=${VERSION}'
BUILD_FLAGS += -X '${VARS_PKG}.GoVersion=$(shell go version)'
BUILD_FLAGS += -X '${VARS_PKG}.BuildTime=$(shell date +"%Y-%m-%d %H:%M:%S")'
BUILD_FLAGS += -X '${VARS_PKG}.GitCommit=$(shell git rev-parse HEAD)'
BUILD_FLAGS += -X '${VARS_PKG}.GitRemote=$(shell git config --get remote.origin.url)'

RELEASE_ASSETS = \
	${BIN_DIR}/${BINARY_NAME}.linux.amd64 \
	${BIN_DIR}/${BINARY_NAME}.linux.arm64 \
	${BIN_DIR}/${BINARY_NAME}.darwin.amd64 \
	${BIN_DIR}/${BINARY_NAME}.darwin.arm64 \
	${BIN_DIR}/${BINARY_NAME}.windows.amd64.exe \
	${BIN_DIR}/${BINARY_NAME}.windows.arm64.exe

.PHONY: ${RELEASE_ASSETS}

all: clean build deploy run

release: release-assets

release-assets:
	@$(MAKE) require-release-version VERSION=$(VERSION)
	@$(MAKE) $(RELEASE_ASSETS) VERSION=$(VERSION)

require-release-version:
	@printf '%s\n' "$(VERSION)" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$$' || { echo "usage: make release VERSION=vX.Y.Z" >&2; exit 2; }

build:
	@mkdir -p $(BIN_DIR)
	GOARCH=amd64 GOOS=linux go build -ldflags="${BUILD_FLAGS}" -o $(BIN_DIR)/${BINARY_NAME} main.go

linux:
	@mv $(BIN_DIR)/${BINARY_NAME} $(BIN_DIR)/${BINARY_NAME}.linux.amd64

test:
	@mkdir -p $(BIN_DIR)
	go build -ldflags="${BUILD_FLAGS}" -o $(BIN_DIR)/${BINARY_NAME} main.go

test_x86:
	@mkdir -p $(BIN_DIR)
	GOARCH=amd64 go build -ldflags="${BUILD_FLAGS}" -o $(BIN_DIR)/${BINARY_NAME} main.go

darwin:
	@mv $(BIN_DIR)/${BINARY_NAME} $(BIN_DIR)/${BINARY_NAME}.darwin.arm64

$(BIN_DIR)/$(BINARY_NAME).linux.amd64:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(BUILD_FLAGS)" -o "$@" main.go

$(BIN_DIR)/$(BINARY_NAME).linux.arm64:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(BUILD_FLAGS)" -o "$@" main.go

$(BIN_DIR)/$(BINARY_NAME).darwin.amd64:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="$(BUILD_FLAGS)" -o "$@" main.go

$(BIN_DIR)/$(BINARY_NAME).darwin.arm64:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$(BUILD_FLAGS)" -o "$@" main.go

$(BIN_DIR)/$(BINARY_NAME).windows.amd64.exe:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(BUILD_FLAGS)" -o "$@" main.go

$(BIN_DIR)/$(BINARY_NAME).windows.arm64.exe:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath -ldflags="$(BUILD_FLAGS)" -o "$@" main.go

deploy:
	@mv -f ${BINARY_NAME} /usr/local/bin/

run:
	@./${BINARY_NAME} version

clean:
	@go clean
	@rm -f $(BIN_DIR)/${BINARY_NAME} $(RELEASE_ASSETS)

harness-check:
	bash scripts/harness/check.sh

harness-verify: harness-check

harness-review-gate:
	@if [ -z "$(PLAN)" ]; then echo "usage: make harness-review-gate PLAN=path/to/plan.md" >&2; exit 2; fi
	bash scripts/harness/review_gate.sh --plan "$(PLAN)"
