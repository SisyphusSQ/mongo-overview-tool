.PHONY: all release release-clean release-assets release-archives release-checksums release-verify require-release-version require-release-tools build linux test test_x86 darwin deploy run clean harness-check harness-verify harness-review-gate

VERSION ?= dev

VARS_PKG = github.com/SisyphusSQ/mongo-overview-tool/vars

BUILD_FLAGS  = -X '${VARS_PKG}.AppName=mot'
BUILD_FLAGS += -X '${VARS_PKG}.AppVersion=${VERSION}'
BUILD_FLAGS += -X '${VARS_PKG}.GoVersion=$(shell go version)'
BUILD_FLAGS += -X '${VARS_PKG}.BuildTime=$(shell date +"%Y-%m-%d %H:%M:%S")'
BUILD_FLAGS += -X '${VARS_PKG}.GitCommit=$(shell git rev-parse HEAD)'
BUILD_FLAGS += -X '${VARS_PKG}.GitRemote=$(shell git config --get remote.origin.url)'

RELEASE_BINARIES = \
	bin/mot.linux.amd64 \
	bin/mot.linux.arm64 \
	bin/mot.darwin.amd64 \
	bin/mot.darwin.arm64 \
	bin/mot.windows.amd64.exe \
	bin/mot.windows.arm64.exe

.PHONY: ${RELEASE_BINARIES}

all: clean build deploy run

release:
	@set -eu; \
	status=1; \
	cleanup_on_error() { \
		if [ "$$status" -ne 0 ]; then rm -rf bin/release; fi; \
	}; \
	trap cleanup_on_error 0; \
	$(MAKE) require-release-version VERSION=$(VERSION); \
	$(MAKE) require-release-tools; \
	$(MAKE) release-clean; \
	$(MAKE) release-assets VERSION=$(VERSION); \
	$(MAKE) release-archives; \
	$(MAKE) release-checksums; \
	$(MAKE) release-verify VERSION=$(VERSION); \
	status=0

release-clean:
	@rm -rf bin/release
	@mkdir -p bin/release

release-assets:
	@$(MAKE) $(RELEASE_BINARIES) VERSION=$(VERSION)

require-release-version:
	@printf '%s\n' "$(VERSION)" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$$' || { echo "usage: make release VERSION=vX.Y.Z" >&2; exit 2; }

require-release-tools:
	@command -v tar >/dev/null 2>&1 || { echo "release requires tar" >&2; exit 2; }
	@command -v zip >/dev/null 2>&1 || { echo "release requires zip" >&2; exit 2; }
	@command -v unzip >/dev/null 2>&1 || { echo "release requires unzip" >&2; exit 2; }
	@{ command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1; } || { echo "release requires sha256sum or shasum" >&2; exit 2; }
	@command -v file >/dev/null 2>&1 || { echo "release requires file" >&2; exit 2; }
	@command -v mktemp >/dev/null 2>&1 || { echo "release requires mktemp" >&2; exit 2; }

release-archives:
	@set -eu; \
	release_dir="$(CURDIR)/bin/release"; \
	staging="$$release_dir/.staging"; \
	success=false; \
	cleanup() { \
		rm -rf "$$staging"; \
		if [ "$$success" != "true" ]; then \
			rm -f \
				"$$release_dir/mot.linux.amd64.tar.gz" \
				"$$release_dir/mot.linux.arm64.tar.gz" \
				"$$release_dir/mot.darwin.amd64.tar.gz" \
				"$$release_dir/mot.darwin.arm64.tar.gz" \
				"$$release_dir/mot.windows.amd64.zip" \
				"$$release_dir/mot.windows.arm64.zip"; \
		fi; \
	}; \
	trap cleanup 0; \
	mkdir -p \
		"$$staging/linux-amd64" \
		"$$staging/linux-arm64" \
		"$$staging/darwin-amd64" \
		"$$staging/darwin-arm64" \
		"$$staging/windows-amd64" \
		"$$staging/windows-arm64"; \
	cp bin/mot.linux.amd64 "$$staging/linux-amd64/mot"; \
	cp bin/mot.linux.arm64 "$$staging/linux-arm64/mot"; \
	cp bin/mot.darwin.amd64 "$$staging/darwin-amd64/mot"; \
	cp bin/mot.darwin.arm64 "$$staging/darwin-arm64/mot"; \
	cp bin/mot.windows.amd64.exe "$$staging/windows-amd64/mot.exe"; \
	cp bin/mot.windows.arm64.exe "$$staging/windows-arm64/mot.exe"; \
	chmod 0755 \
		"$$staging/linux-amd64/mot" \
		"$$staging/linux-arm64/mot" \
		"$$staging/darwin-amd64/mot" \
		"$$staging/darwin-arm64/mot" \
		"$$staging/windows-amd64/mot.exe" \
		"$$staging/windows-arm64/mot.exe"; \
	tar -C "$$staging/linux-amd64" -czf "$$release_dir/mot.linux.amd64.tar.gz" mot; \
	tar -C "$$staging/linux-arm64" -czf "$$release_dir/mot.linux.arm64.tar.gz" mot; \
	tar -C "$$staging/darwin-amd64" -czf "$$release_dir/mot.darwin.amd64.tar.gz" mot; \
	tar -C "$$staging/darwin-arm64" -czf "$$release_dir/mot.darwin.arm64.tar.gz" mot; \
	(cd "$$staging/windows-amd64" && zip -q -X "$$release_dir/mot.windows.amd64.zip" mot.exe); \
	(cd "$$staging/windows-arm64" && zip -q -X "$$release_dir/mot.windows.arm64.zip" mot.exe); \
	success=true

release-checksums:
	@set -eu; \
	cd bin/release; \
	tmp="SHA256SUMS.tmp"; \
	trap 'rm -f "$$tmp"' 0; \
	if command -v sha256sum >/dev/null 2>&1; then \
		sha256sum \
			mot.linux.amd64.tar.gz \
			mot.linux.arm64.tar.gz \
			mot.darwin.amd64.tar.gz \
			mot.darwin.arm64.tar.gz \
			mot.windows.amd64.zip \
			mot.windows.arm64.zip > "$$tmp"; \
	else \
		shasum -a 256 \
			mot.linux.amd64.tar.gz \
			mot.linux.arm64.tar.gz \
			mot.darwin.amd64.tar.gz \
			mot.darwin.arm64.tar.gz \
			mot.windows.amd64.zip \
			mot.windows.arm64.zip > "$$tmp"; \
	fi; \
	mv "$$tmp" SHA256SUMS

release-verify:
	@$(MAKE) require-release-version VERSION=$(VERSION)
	@$(MAKE) require-release-tools
	@set -eu; \
	expected=$$(printf '%s\n' \
		mot.darwin.amd64.tar.gz \
		mot.darwin.arm64.tar.gz \
		mot.linux.amd64.tar.gz \
		mot.linux.arm64.tar.gz \
		mot.windows.amd64.zip \
		mot.windows.arm64.zip \
		SHA256SUMS | LC_ALL=C sort); \
	actual=$$(find bin/release -mindepth 1 -maxdepth 1 -exec basename {} \; | LC_ALL=C sort); \
	if [ "$$actual" != "$$expected" ]; then \
		echo "unexpected release assets" >&2; \
		printf 'expected:\n%s\nactual:\n%s\n' "$$expected" "$$actual" >&2; \
		exit 1; \
	fi; \
	verify_root=$$(mktemp -d "$${TMPDIR:-/tmp}/mot-release-verify.XXXXXX"); \
	trap 'rm -rf "$$verify_root"' 0; \
	mkdir -p \
		"$$verify_root/linux-amd64" \
		"$$verify_root/linux-arm64" \
		"$$verify_root/darwin-amd64" \
		"$$verify_root/darwin-arm64" \
		"$$verify_root/windows-amd64" \
		"$$verify_root/windows-arm64"; \
	for platform in linux.amd64 linux.arm64 darwin.amd64 darwin.arm64; do \
		archive="bin/release/mot.$$platform.tar.gz"; \
		destination="$$verify_root/$$(printf '%s' "$$platform" | tr . -)"; \
		[ "$$(tar -tzf "$$archive")" = "mot" ]; \
		tar -xzf "$$archive" -C "$$destination"; \
		[ -f "$$destination/mot" ]; \
		[ -x "$$destination/mot" ]; \
		[ -n "$$(find "$$destination/mot" -type f -perm 0755 -print)" ]; \
	done; \
	file "$$verify_root/linux-amd64/mot" | grep -Eq 'ELF 64-bit .*x86-64'; \
	file "$$verify_root/linux-arm64/mot" | grep -Eq 'ELF 64-bit .*ARM aarch64'; \
	file "$$verify_root/darwin-amd64/mot" | grep -Fq 'Mach-O 64-bit executable x86_64'; \
	file "$$verify_root/darwin-arm64/mot" | grep -Fq 'Mach-O 64-bit executable arm64'; \
	[ "$$(unzip -Z1 bin/release/mot.windows.amd64.zip)" = "mot.exe" ]; \
	[ "$$(unzip -Z1 bin/release/mot.windows.arm64.zip)" = "mot.exe" ]; \
	unzip -p bin/release/mot.windows.amd64.zip mot.exe > "$$verify_root/windows-amd64/mot.exe"; \
	unzip -p bin/release/mot.windows.arm64.zip mot.exe > "$$verify_root/windows-arm64/mot.exe"; \
	file "$$verify_root/windows-amd64/mot.exe" | grep -Eq 'PE32\+ executable .*x86-64'; \
	file "$$verify_root/windows-arm64/mot.exe" | grep -Eq 'PE32\+ executable .*Aarch64'; \
	if command -v sha256sum >/dev/null 2>&1; then \
		(cd bin/release && sha256sum -c SHA256SUMS); \
	else \
		(cd bin/release && shasum -a 256 -c SHA256SUMS); \
	fi; \
	if [ "$$(uname -s)" = "Darwin" ] && [ "$$(uname -m)" = "arm64" ]; then \
		"$$verify_root/darwin-arm64/mot" version | grep -Fq "AppVersion:  $(VERSION)"; \
		"$$verify_root/darwin-arm64/mot" -h >/dev/null; \
	fi

build:
	@mkdir -p bin
	GOARCH=amd64 GOOS=linux go build -ldflags="${BUILD_FLAGS}" -o bin/mot main.go

linux:
	@mv bin/mot bin/mot.linux.amd64

test:
	@mkdir -p bin
	go build -ldflags="${BUILD_FLAGS}" -o bin/mot main.go

test_x86:
	@mkdir -p bin
	GOARCH=amd64 go build -ldflags="${BUILD_FLAGS}" -o bin/mot main.go

darwin:
	@mv bin/mot bin/mot.darwin.arm64

bin/mot.linux.amd64:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(BUILD_FLAGS)" -o bin/mot.linux.amd64 main.go

bin/mot.linux.arm64:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(BUILD_FLAGS)" -o bin/mot.linux.arm64 main.go

bin/mot.darwin.amd64:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="$(BUILD_FLAGS)" -o bin/mot.darwin.amd64 main.go

bin/mot.darwin.arm64:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$(BUILD_FLAGS)" -o bin/mot.darwin.arm64 main.go

bin/mot.windows.amd64.exe:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(BUILD_FLAGS)" -o bin/mot.windows.amd64.exe main.go

bin/mot.windows.arm64.exe:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath -ldflags="$(BUILD_FLAGS)" -o bin/mot.windows.arm64.exe main.go

deploy:
	@mv -f mot /usr/local/bin/

run:
	@./mot version

clean:
	@go clean
	@rm -f bin/mot $(RELEASE_BINARIES)
	@rm -rf bin/release

harness-check:
	bash scripts/harness/check.sh

harness-verify: harness-check

harness-review-gate:
	@if [ -z "$(PLAN)" ]; then echo "usage: make harness-review-gate PLAN=path/to/plan.md" >&2; exit 2; fi
	bash scripts/harness/review_gate.sh --plan "$(PLAN)"
