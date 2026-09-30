BINARY  := chotu-status
PKG     := github.com/goravaa/chotu/status-service
GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
IMAGE   ?= chotu-status
PORT    ?= 8081

GOOS        ?= linux
GOARCH      ?= amd64
RELEASE_ARCHS := amd64 arm64

.PHONY: all build test vet fmt check release run stop clean

all: check build

build:
	CGO_ENABLED=0 $(GO) build -ldflags '$(LDFLAGS)' -o $(BINARY) .

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

check: fmt vet test

# The Dockerfile copies the binary in rather than compiling, which keeps the
# image a static Alpine layer with no Go toolchain in it. Building it first is
# the makefile's job, so docker cannot be run against a stale or missing binary.
docker: build
	docker build -t $(IMAGE) .

# release cross-compiles the static binaries for every target architecture,
# which is the only way to build an arm64 image on an amd64 host without a
# cross-emulator.
release:
	rm -rf dist
	mkdir -p dist
	@for arch in $(RELEASE_ARCHS); do \
		echo "building $(BINARY)-$(GOOS)-$$arch"; \
		GOOS=$(GOOS) GOARCH=$$arch CGO_ENABLED=0 \
			$(GO) build -ldflags '$(LDFLAGS)' \
			-o dist/$(BINARY)-$(GOOS)-$$arch . || exit 1; \
	done
	cd dist && sha256sum $(BINARY)-* > SHA256SUMS

run: docker
	docker rm -f $(BINARY) 2>/dev/null || true
	docker run -d --name $(BINARY) -p $(PORT):$(PORT) $(IMAGE)

stop:
	docker rm -f $(BINARY) 2>/dev/null || true

clean:
	rm -f $(BINARY)
	rm -rf dist
	$(GO) clean
