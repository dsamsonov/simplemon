# =============================================================================
# Makefile — SimpleMon
# =============================================================================

BINARY      := simplemon
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo "0.0.0")
LDFLAGS     := -s -w -X main.Version=$(VERSION)
BUILD_FLAGS := -trimpath -ldflags "$(LDFLAGS)"

PREFIX      ?= /usr/local
BINDIR      := $(PREFIX)/bin
CONFDIR     := /etc/$(BINARY)
HTMLDIR     := /var/www/$(BINARY)
SYSTEMDDIR  := /lib/systemd/system

DIST_DIR    := dist

.DEFAULT_GOAL := build

# ---------- build ------------------------------------------------------------

.PHONY: build
build:  ## Build binary for the current platform
	@mkdir -p bin
	CGO_ENABLED=0 go build $(BUILD_FLAGS) -o bin/$(BINARY) .
	@echo "Done: bin/$(BINARY)"

.PHONY: build-all
build-all: build-amd64 build-arm64 build-armhf  ## Build binaries for all architectures

.PHONY: build-amd64
build-amd64:  ## Build for amd64
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -o bin/$(BINARY)-amd64 .

.PHONY: build-arm64
build-arm64:  ## Build for arm64
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(BUILD_FLAGS) -o bin/$(BINARY)-arm64 .

.PHONY: build-armhf
build-armhf:  ## Build for armhf (ARM 32-bit v7)
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build $(BUILD_FLAGS) -o bin/$(BINARY)-armhf .

# ---------- deb packages -----------------------------------------------------

.PHONY: deb
deb:  ## Build .deb packages for all architectures (amd64, arm64, armhf)
	@chmod +x build-deb.sh
	VERSION=$(VERSION) ./build-deb.sh

.PHONY: deb-amd64
deb-amd64:  ## Build .deb for amd64 only
	@chmod +x build-deb.sh
	VERSION=$(VERSION) ./build-deb.sh amd64

.PHONY: deb-arm64
deb-arm64:  ## Build .deb for arm64 only
	@chmod +x build-deb.sh
	VERSION=$(VERSION) ./build-deb.sh arm64

.PHONY: deb-armhf
deb-armhf:  ## Build .deb for armhf only
	@chmod +x build-deb.sh
	VERSION=$(VERSION) ./build-deb.sh armhf

# ---------- tar.gz archives --------------------------------------------------

.PHONY: tar
tar:  ## Build .tar.gz archives for all architectures (amd64, arm64, armhf)
	@chmod +x build-tar.sh
	VERSION=$(VERSION) ./build-tar.sh

.PHONY: tar-amd64
tar-amd64:  ## Build .tar.gz for amd64 only
	@chmod +x build-tar.sh
	VERSION=$(VERSION) ./build-tar.sh amd64

.PHONY: tar-arm64
tar-arm64:  ## Build .tar.gz for arm64 only
	@chmod +x build-tar.sh
	VERSION=$(VERSION) ./build-tar.sh arm64

.PHONY: tar-armhf
tar-armhf:  ## Build .tar.gz for armhf only
	@chmod +x build-tar.sh
	VERSION=$(VERSION) ./build-tar.sh armhf

.PHONY: dist
dist: deb tar  ## Build all release artifacts (.deb + .tar.gz) for all architectures

# ---------- install (from source) --------------------------------------------

.PHONY: install
install: build  ## Install from source (requires sudo)
	install -Dm 0755 bin/$(BINARY) $(DESTDIR)$(BINDIR)/$(BINARY)
	install -d $(DESTDIR)$(CONFDIR)
	install -Dm 0640 etc/$(BINARY).yaml $(DESTDIR)$(CONFDIR)/$(BINARY).yaml
	install -Dm 0644 systemd/$(BINARY).service $(DESTDIR)$(SYSTEMDDIR)/$(BINARY).service
	install -d $(DESTDIR)$(HTMLDIR)
	install -Dm 0644 html/$(BINARY).html $(DESTDIR)$(HTMLDIR)/$(BINARY).html
	@# Install frontend config only if it does not already exist (preserve user settings)
	@if [ ! -f $(DESTDIR)$(HTMLDIR)/$(BINARY).config.js ]; then \
		install -Dm 0644 html/$(BINARY).config.js $(DESTDIR)$(HTMLDIR)/$(BINARY).config.js; \
		echo "Installed frontend config: $(DESTDIR)$(HTMLDIR)/$(BINARY).config.js"; \
	else \
		echo "Frontend config already exists, skipping: $(DESTDIR)$(HTMLDIR)/$(BINARY).config.js"; \
	fi
	@if ! id $(BINARY) >/dev/null 2>&1; then \
		useradd --system --no-create-home --shell /usr/sbin/nologin \
			--comment "SimpleMon monitoring daemon" $(BINARY); \
	fi
	chown root:$(BINARY) $(DESTDIR)$(CONFDIR)/$(BINARY).yaml
	systemctl daemon-reload
	systemctl enable --now $(BINARY)
	@echo ""
	@echo "Installed. API: http://127.0.0.1:8095/health"

.PHONY: uninstall
uninstall:  ## Uninstall (requires sudo)
	-systemctl stop $(BINARY)
	-systemctl disable $(BINARY)
	rm -f $(BINDIR)/$(BINARY)
	rm -f $(SYSTEMDDIR)/$(BINARY).service
	rm -rf $(CONFDIR)
	rm -rf $(HTMLDIR)
	-userdel $(BINARY) 2>/dev/null
	systemctl daemon-reload
	@echo "Uninstalled."

# ---------- development ------------------------------------------------------

.PHONY: run
run:  ## Run locally as the current user
	go run . -config etc/$(BINARY).yaml

.PHONY: test
test:  ## Run tests
	go test ./...

.PHONY: vet
vet:  ## Run static analysis
	go vet ./...

.PHONY: tidy
tidy:  ## Run go mod tidy
	go mod tidy

# ---------- clean ------------------------------------------------------------

.PHONY: clean
clean:  ## Remove build artifacts
	rm -rf bin/ build/ $(DIST_DIR)/

.PHONY: help
help:  ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.PHONY: version
version:  ## Print current version
	@echo $(VERSION)
