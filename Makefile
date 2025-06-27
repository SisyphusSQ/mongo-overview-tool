BINARY_NAME = mot

VARS_PKG = github.com/SisyphusSQ/mongo-overview-tool/vars

BUILD_FLAGS  = -X '${VARS_PKG}.AppName=${BINARY_NAME}'
#BUILD_FLAGS += -X '${VARS_PKG}.AppVersion=$(shell git describe)'
BUILD_FLAGS += -X '${VARS_PKG}.GoVersion=$(shell go version)'
BUILD_FLAGS += -X '${VARS_PKG}.BuildTime=$(shell date +"%Y-%m-%d %H:%M:%S")'
BUILD_FLAGS += -X '${VARS_PKG}.GitCommit=$(shell git rev-parse HEAD)'
BUILD_FLAGS += -X '${VARS_PKG}.GitRemote=$(shell git config --get remote.origin.url)'

all: clean build deploy run

release: build linux test darwin

build:
	GOARCH=amd64 GOOS=linux go build -ldflags="${BUILD_FLAGS}" -o bin/${BINARY_NAME} main.go

linux:
	@mv bin/${BINARY_NAME} bin/${BINARY_NAME}.linux.amd64

test:
	go build -ldflags="${BUILD_FLAGS}" -o bin/${BINARY_NAME} main.go

darwin:
	@mv bin/${BINARY_NAME} bin/${BINARY_NAME}.darwin.arm64

deploy:
	@mv -f ${BINARY_NAME} /usr/local/bin/

run:
	@./${BINARY_NAME} version

clean:
	@go clean
	@rm -f bin/${BINARY_NAME}
	@rm -f bin/${BINARY_NAME}.linux.amd64
	@rm -f bin/${BINARY_NAME}.darwin.arm64