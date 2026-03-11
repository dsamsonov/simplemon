BINARY     := simplemon
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "1.0.0")
BUILD_DIR  := ./bin
MAIN       := .
LDFLAGS    := -ldflags="-s -w -X main.version=$(VERSION)"

INSTALL_BIN  := /usr/local/bin/$(BINARY)
INSTALL_CFG  := /etc/simplemon/simplemon.yaml
SYSTEMD_UNIT := /etc/systemd/system/simplemon.service
SYSUSER      := simplemon

.PHONY: all build clean vet test install uninstall

all: build

build:
	mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) $(MAIN)
	@echo "Built: $(BUILD_DIR)/$(BINARY)  (version: $(VERSION))"

clean:
	rm -rf $(BUILD_DIR)

vet:
	go vet ./...

test:
	go test -race ./...

# --------------------------------------------------------------------------
# install – requires root (sudo make install)
# --------------------------------------------------------------------------
install: build
	@echo "==> Creating system user '$(SYSUSER)'..."
	id -u $(SYSUSER) >/dev/null 2>&1 || \
	  useradd --system --no-create-home --shell /usr/sbin/nologin \
	          --comment "SimpleMon monitoring daemon" $(SYSUSER)

	@echo "==> Installing binary -> $(INSTALL_BIN)"
	install -m 0755 $(BUILD_DIR)/$(BINARY) $(INSTALL_BIN)

	@echo "==> Installing config -> $(INSTALL_CFG)"
	mkdir -p /etc/simplemon
	@if [ ! -f $(INSTALL_CFG) ]; then \
	  install -m 0640 -o root -g $(SYSUSER) etc/simplemon.yaml $(INSTALL_CFG); \
	  echo "    Config installed. Review $(INSTALL_CFG) before starting."; \
	else \
	  echo "    Config already exists, skipping."; \
	fi

	@echo "==> Installing systemd unit -> $(SYSTEMD_UNIT)"
	install -m 0644 systemd/simplemon.service $(SYSTEMD_UNIT)
	systemctl daemon-reload

	@echo ""
	@echo "Done. Next steps:"
	@echo "  sudo systemctl enable --now simplemon"
	@echo "  systemctl status simplemon"

# --------------------------------------------------------------------------
# uninstall – requires root (sudo make uninstall)
# --------------------------------------------------------------------------
uninstall:
	systemctl stop simplemon    2>/dev/null || true
	systemctl disable simplemon 2>/dev/null || true
	rm -f $(SYSTEMD_UNIT)
	systemctl daemon-reload
	rm -f $(INSTALL_BIN)
	rm -rf /etc/simplemon
	userdel $(SYSUSER) 2>/dev/null || true
	@echo "Uninstalled."
