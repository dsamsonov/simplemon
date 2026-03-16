#!/usr/bin/env bash
# =============================================================================
# build-tar.sh — build .tar.gz tarballs for simplemon (amd64, arm64, armhf)
#
# Each archive contains:
#   simplemon-<version>-linux-<arch>/
#   ├── bin/
#   │   └── simplemon              # compiled binary
#   ├── etc/
#   │   └── simplemon.yaml         # default config
#   ├── systemd/
#   │   └── simplemon.service      # systemd unit
#   ├── html/
#   │   ├── simplemon.html         # web frontend
#   │   └── simplemon.config.js    # frontend hosts config (preserved on update)
#   ├── install.sh                 # one-shot install script
#   ├── uninstall.sh               # uninstall script
#   └── README.txt                 # quick-start instructions
#
# Usage:
#   ./build-tar.sh [arch...]
#
# Examples:
#   ./build-tar.sh                  # build all architectures
#   ./build-tar.sh amd64            # amd64 only
#   ./build-tar.sh amd64 arm64      # amd64 + arm64
#
# Supported architectures: amd64, arm64, armhf
# =============================================================================

set -euo pipefail

# ---------- configuration ----------------------------------------------------
PACKAGE_NAME="simplemon"
_raw_ver="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo "0.0.0")}"
if [[ ! "$_raw_ver" =~ ^[0-9] ]]; then
    VERSION="1.0.0~${_raw_ver}"
else
    VERSION="$_raw_ver"
fi
unset _raw_ver

BINARY_NAME="simplemon"

BUILD_DIR="$(pwd)/build"
DIST_DIR="$(pwd)/dist"

# Map: arch label -> GOARCH -> GOARM
declare -A GOARCH_MAP=(
    [amd64]="amd64"
    [arm64]="arm64"
    [armhf]="arm"
)
declare -A GOARM_MAP=(
    [amd64]=""
    [arm64]=""
    [armhf]="7"
)

# ---------- colors -----------------------------------------------------------
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; NC='\033[0m'; BOLD='\033[1m'

log()  { echo -e "${CYAN}[build]${NC} $*"; }
ok()   { echo -e "${GREEN}[  ok ]${NC} $*"; }
warn() { echo -e "${YELLOW}[ warn]${NC} $*"; }
err()  { echo -e "${RED}[error]${NC} $*" >&2; exit 1; }

# ---------- dependency check -------------------------------------------------
check_deps() {
    local missing=()
    for cmd in go tar; do
        command -v "$cmd" &>/dev/null || missing+=("$cmd")
    done
    if [[ ${#missing[@]} -gt 0 ]]; then
        err "Missing dependencies: ${missing[*]}\n  Install with: sudo apt-get install -y golang-go tar"
    fi

    local go_ver
    go_ver=$(go version | grep -oP 'go\K[0-9]+\.[0-9]+' | head -1)
    log "Go version: ${go_ver}"
}

# ---------- binary compilation -----------------------------------------------
build_binary() {
    local arch="$1"
    local goarch="${GOARCH_MAP[$arch]}"
    local goarm="${GOARM_MAP[$arch]}"

    local bin_path="${BUILD_DIR}/bin/${arch}/${BINARY_NAME}"
    mkdir -p "$(dirname "$bin_path")"

    log "Compiling for ${arch} (GOARCH=${goarch}${goarm:+, GOARM=${goarm}})..."

    local ldflags="-s -w -X main.Version=${VERSION}"

    env CGO_ENABLED=0 \
        GOOS=linux \
        GOARCH="$goarch" \
        ${goarm:+GOARM=$goarm} \
        go build \
            -trimpath \
            -ldflags "$ldflags" \
            -o "$bin_path" \
            .

    ok "Binary: ${bin_path} ($(du -sh "$bin_path" | cut -f1))"
}

# ---------- generate install.sh ----------------------------------------------
write_install_script() {
    local dest="$1"

    cat > "$dest" << 'EOF'
#!/bin/sh
# =============================================================================
# install.sh — install simplemon from tarball
# Run as root: sudo ./install.sh
# =============================================================================
set -e

BINARY_NAME="simplemon"
BINDIR="/usr/local/bin"
CONFDIR="/etc/simplemon"
HTMLDIR="/var/www/simplemon"
SYSTEMDDIR="/lib/systemd/system"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Check root
if [ "$(id -u)" -ne 0 ]; then
    echo "Error: this script must be run as root (sudo ./install.sh)" >&2
    exit 1
fi

echo "Installing SimpleMon..."

# Create system user
if ! id simplemon >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin \
        --comment "SimpleMon monitoring daemon" simplemon
    echo "  [ok] Created system user: simplemon"
fi

# Install binary
install -Dm 0755 "${SCRIPT_DIR}/bin/${BINARY_NAME}" "${BINDIR}/${BINARY_NAME}"
echo "  [ok] Binary installed: ${BINDIR}/${BINARY_NAME}"

# Install backend config (skip if already exists to preserve user changes)
if [ ! -f "${CONFDIR}/${BINARY_NAME}.yaml" ]; then
    install -d "${CONFDIR}"
    install -m 0640 "${SCRIPT_DIR}/etc/${BINARY_NAME}.yaml" \
        "${CONFDIR}/${BINARY_NAME}.yaml"
    chown root:simplemon "${CONFDIR}/${BINARY_NAME}.yaml"
    echo "  [ok] Config installed: ${CONFDIR}/${BINARY_NAME}.yaml"
else
    echo "  [--] Config already exists, skipping: ${CONFDIR}/${BINARY_NAME}.yaml"
fi

# Install systemd unit
install -Dm 0644 "${SCRIPT_DIR}/systemd/${BINARY_NAME}.service" \
    "${SYSTEMDDIR}/${BINARY_NAME}.service"
echo "  [ok] Systemd unit installed: ${SYSTEMDDIR}/${BINARY_NAME}.service"

# Install HTML frontend (always updated)
install -d "${HTMLDIR}"
install -m 0644 "${SCRIPT_DIR}/html/${BINARY_NAME}.html" \
    "${HTMLDIR}/${BINARY_NAME}.html"
echo "  [ok] Frontend installed: ${HTMLDIR}/${BINARY_NAME}.html"

# Install frontend hosts config (skip if already exists to preserve user settings)
if [ ! -f "${HTMLDIR}/${BINARY_NAME}.config.js" ]; then
    install -m 0644 "${SCRIPT_DIR}/html/${BINARY_NAME}.config.js" \
        "${HTMLDIR}/${BINARY_NAME}.config.js"
    echo "  [ok] Frontend config installed: ${HTMLDIR}/${BINARY_NAME}.config.js"
else
    echo "  [--] Frontend config already exists, skipping: ${HTMLDIR}/${BINARY_NAME}.config.js"
fi

# Enable and start service
systemctl daemon-reload
systemctl enable "${BINARY_NAME}"
systemctl restart "${BINARY_NAME}"

echo ""
echo "  SimpleMon installed and running."
echo "  Frontend: ${HTMLDIR}/${BINARY_NAME}.html"
echo "  API:      http://127.0.0.1:8095/health"
echo "  Config:   ${CONFDIR}/${BINARY_NAME}.yaml"
echo "  Hosts:    ${HTMLDIR}/${BINARY_NAME}.config.js"
echo "  Logs:     journalctl -u ${BINARY_NAME} -f"
echo ""
EOF

    chmod 0755 "$dest"
}

# ---------- generate uninstall.sh --------------------------------------------
write_uninstall_script() {
    local dest="$1"

    cat > "$dest" << 'EOF'
#!/bin/sh
# =============================================================================
# uninstall.sh — remove simplemon installed from tarball
# Run as root: sudo ./uninstall.sh
# =============================================================================
set -e

BINARY_NAME="simplemon"
BINDIR="/usr/local/bin"
CONFDIR="/etc/simplemon"
HTMLDIR="/var/www/simplemon"
SYSTEMDDIR="/lib/systemd/system"

if [ "$(id -u)" -ne 0 ]; then
    echo "Error: this script must be run as root (sudo ./uninstall.sh)" >&2
    exit 1
fi

echo "Removing SimpleMon..."

systemctl stop "${BINARY_NAME}"    2>/dev/null || true
systemctl disable "${BINARY_NAME}" 2>/dev/null || true

rm -f  "${BINDIR}/${BINARY_NAME}"
rm -f  "${SYSTEMDDIR}/${BINARY_NAME}.service"
rm -rf "${HTMLDIR}"

systemctl daemon-reload

echo ""
echo "  Config directory preserved: ${CONFDIR}"
echo "  Remove manually if no longer needed: sudo rm -rf ${CONFDIR}"
echo ""
echo "  SimpleMon removed."
EOF

    chmod 0755 "$dest"
}

# ---------- generate README.txt ----------------------------------------------
write_readme() {
    local dest="$1"
    local arch="$2"

    cat > "$dest" << EOF
SimpleMon ${VERSION} — linux/${arch}
=====================================

Quick install
-------------
  sudo ./install.sh

Quick uninstall
---------------
  sudo ./uninstall.sh

After install, edit the frontend hosts config if needed:
  /var/www/simplemon/simplemon.config.js
This file is NOT overwritten on update.

Manual install
--------------
  # 1. Create system user
  sudo useradd --system --no-create-home --shell /usr/sbin/nologin \\
       --comment "SimpleMon monitoring daemon" simplemon

  # 2. Install binary
  sudo install -Dm 0755 bin/simplemon /usr/local/bin/simplemon

  # 3. Install backend config
  sudo mkdir -p /etc/simplemon
  sudo install -m 0640 etc/simplemon.yaml /etc/simplemon/simplemon.yaml
  sudo chown root:simplemon /etc/simplemon/simplemon.yaml

  # 4. Install systemd unit
  sudo install -m 0644 systemd/simplemon.service /lib/systemd/system/simplemon.service
  sudo systemctl daemon-reload

  # 5. Install frontend
  sudo mkdir -p /var/www/simplemon
  sudo install -m 0644 html/simplemon.html /var/www/simplemon/simplemon.html
  sudo install -m 0644 html/simplemon.config.js /var/www/simplemon/simplemon.config.js

  # 6. Start
  sudo systemctl enable --now simplemon

Verify
------
  systemctl status simplemon
  journalctl -u simplemon -f
  curl http://127.0.0.1:8095/health

Config
------
  /etc/simplemon/simplemon.yaml       — backend config
  /var/www/simplemon/simplemon.config.js — frontend hosts (edit to add servers)
  sudo systemctl restart simplemon   # apply backend config changes

Homepage
--------
  https://github.com/dsamsonov/simplemon
EOF
}

# ---------- assemble tarball -------------------------------------------------
build_tar() {
    local arch="$1"
    local dir_name="${PACKAGE_NAME}-${VERSION}-linux-${arch}"
    local stage_dir="${BUILD_DIR}/tar/${arch}/${dir_name}"
    local bin_path="${BUILD_DIR}/bin/${arch}/${BINARY_NAME}"

    log "Assembling tarball for ${arch}..."

    rm -rf "${BUILD_DIR}/tar/${arch}"
    mkdir -p \
        "${stage_dir}/bin" \
        "${stage_dir}/etc" \
        "${stage_dir}/systemd" \
        "${stage_dir}/html"

    # Binary
    install -m 0755 "$bin_path"              "${stage_dir}/bin/${BINARY_NAME}"

    # Backend config
    install -m 0644 "etc/simplemon.yaml"     "${stage_dir}/etc/simplemon.yaml"

    # Systemd unit
    install -m 0644 "systemd/simplemon.service" "${stage_dir}/systemd/simplemon.service"

    # HTML frontend
    install -m 0644 "html/simplemon.html"    "${stage_dir}/html/simplemon.html"

    # Frontend hosts config
    install -m 0644 "html/simplemon.config.js" "${stage_dir}/html/simplemon.config.js"

    # Helper scripts
    write_install_script   "${stage_dir}/install.sh"
    write_uninstall_script "${stage_dir}/uninstall.sh"
    write_readme           "${stage_dir}/README.txt" "$arch"

    # Pack
    mkdir -p "$DIST_DIR"
    local tar_file="${DIST_DIR}/${PACKAGE_NAME}-${VERSION}-linux-${arch}.tar.gz"

    tar -czf "$tar_file" \
        -C "${BUILD_DIR}/tar/${arch}" \
        "$dir_name"

    ok "Tarball ready: ${tar_file} ($(du -sh "$tar_file" | cut -f1))"

    # Show contents
    echo ""
    log "Tarball contents (${arch}):"
    tar -tzf "$tar_file" | awk '{print "  "$0}'
}

# ---------- main -------------------------------------------------------------
main() {
    echo -e "\n${BOLD}=== SimpleMon TAR Builder v${VERSION} ===${NC}\n"

    check_deps

    # Determine target architectures
    local archs=()
    if [[ $# -gt 0 ]]; then
        archs=("$@")
    else
        archs=("amd64" "arm64" "armhf")
    fi

    # Validate architectures
    for arch in "${archs[@]}"; do
        [[ -v GOARCH_MAP[$arch] ]] || \
            err "Unknown architecture: ${arch}. Supported: amd64, arm64, armhf"
    done

    log "Target architectures: ${archs[*]}"
    log "Package version:      ${VERSION}"
    log "Output directory:     ${DIST_DIR}"
    echo ""

    # Fetch dependencies
    log "Running go mod tidy..."
    go mod tidy

    # Build for each architecture
    for arch in "${archs[@]}"; do
        echo -e "\n${BOLD}--- ${arch} ---${NC}"
        build_binary "$arch"
        build_tar    "$arch"
    done

    # Summary
    echo ""
    echo -e "${BOLD}=== Done ===${NC}"
    echo ""
    ls -lh "${DIST_DIR}"/*.tar.gz 2>/dev/null || true
    echo ""
    echo -e "Extract and install:"
    echo -e "  ${CYAN}tar -xzf dist/${PACKAGE_NAME}-${VERSION}-linux-amd64.tar.gz${NC}"
    echo -e "  ${CYAN}sudo ./${PACKAGE_NAME}-${VERSION}-linux-amd64/install.sh${NC}"
    echo ""
}

main "$@"
