BINARY      := chotu-setup
PKG         := github.com/goravaa/chotu/chotu-setup
GO          ?= go
DIST        := dist
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)

PREFIX      ?= /usr/local
BINDIR      := $(PREFIX)/bin
UNIT_SRC    := deploy/chotu-setup.service
UNIT_DST    := /etc/systemd/system/chotu-setup.service
CONFIG_SRC  ?= ../dev.config.yaml
CONFIG_DST  := /etc/chotu/config.yaml
STATE_DIR   ?= /var/lib/chotu

GOOS        ?= linux
GOARCH      ?= amd64
RELEASE_ARCHS := amd64 arm64

.PHONY: all build test vet fmt check install uninstall clean release run

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

# install puts the binary, the systemd unit and the config in place and leaves
# the service enabled. It does not start it, so a caller can edit the config
# first. The daemon owns everything after this point: bootstrap.sh only has to
# make the binary available and start the unit.
install: build
	install -D -m 0755 $(BINARY) $(BINDIR)/$(BINARY)
	install -D -m 0644 $(UNIT_SRC) $(UNIT_DST)
	@if [ -f "$(CONFIG_SRC)" ] && [ ! -f "$(CONFIG_DST)" ]; then \
		install -D -m 0644 $(CONFIG_SRC) $(CONFIG_DST); \
		echo "installed $(CONFIG_DST)"; \
	else \
		echo "keeping existing $(CONFIG_DST)"; \
	fi
	install -d -m 0755 $(STATE_DIR)
	systemctl daemon-reload
	systemctl enable chotu-setup.service

uninstall:
	systemctl disable --now chotu-setup.service 2>/dev/null || true
	rm -f $(UNIT_DST) $(BINDIR)/$(BINARY)
	systemctl daemon-reload

# release cross-compiles the static binaries the bootstrap step downloads from
# the GitHub release page, plus the checksums it verifies them with.
release:
	rm -rf $(DIST)
	mkdir -p $(DIST)
	@for arch in $(RELEASE_ARCHS); do \
		echo "building $(BINARY)-$(GOOS)-$$arch"; \
		GOOS=$(GOOS) GOARCH=$$arch CGO_ENABLED=0 \
			$(GO) build -ldflags '$(LDFLAGS)' \
			-o $(DIST)/$(BINARY)-$(GOOS)-$$arch . || exit 1; \
	done
	cd $(DIST) && sha256sum $(BINARY)-* > SHA256SUMS

run: build
	./$(BINARY) -config $(CONFIG_SRC) -db $(STATE_DIR)/chotu.db -once

clean:
	rm -f $(BINARY)
	rm -rf $(DIST)
	$(GO) clean
