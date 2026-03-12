#!/usr/bin/env bash
# =============================================================================
# build-deb.sh — build .deb packages for simplemon (amd64, arm64, armhf)
# Compatible with Debian 11/12 and Ubuntu 20.04/22.04/24.04
#
# Usage:
#   ./build-deb.sh [arch...]
#
# Examples:
#   ./build-deb.sh                  # build all architectures
#   ./build-deb.sh amd64            # amd64 only
#   ./build-deb.sh amd64 arm64      # amd64 + arm64
#
# Supported architectures: amd64, arm64, armhf
# =============================================================================

set -euo pipefail

# ---------- configuration ----------------------------------------------------
PACKAGE_NAME="simplemon"
# Normalize version: dpkg requires it to start with a digit.
# If there are no tags yet, git describe returns a bare commit hash
# (e.g. "cae91f2-dirty"). In that case we prepend "0.0.0~" so dpkg
# accepts it and the version sorts below any real release.
_raw_ver="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo "0.0.0")}"
if [[ ! "$_raw_ver" =~ ^[0-9] ]]; then
    VERSION="1.0.0~${_raw_ver}"
else
    VERSION="$_raw_ver"
fi
unset _raw_ver
MAINTAINER="${MAINTAINER:-SimpleMon Developer <noreply@example.com>}"
DESCRIPTION="Lightweight Linux server monitoring daemon"
HOMEPAGE="https://github.com/dsamsonov/simplemon"
LICENSE="MIT"
SECTION="admin"
PRIORITY="optional"

BINARY_NAME="simplemon"
GO_MODULE="github.com/dsamsonov/simplemon"

BUILD_DIR="$(pwd)/build"
DIST_DIR="$(pwd)/dist"

# Map: deb architecture -> GOARCH -> GOARM
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
    for cmd in go dpkg-deb fakeroot; do
        command -v "$cmd" &>/dev/null || missing+=("$cmd")
    done
    if [[ ${#missing[@]} -gt 0 ]]; then
        err "Missing dependencies: ${missing[*]}\n  Install with: sudo apt-get install -y golang-go fakeroot dpkg"
    fi

    local go_ver
    go_ver=$(go version | grep -oP 'go\K[0-9]+\.[0-9]+' | head -1)
    log "Go version: ${go_ver}"
}

# ---------- binary compilation -----------------------------------------------
build_binary() {
    local deb_arch="$1"
    local goarch="${GOARCH_MAP[$deb_arch]}"
    local goarm="${GOARM_MAP[$deb_arch]}"

    local bin_path="${BUILD_DIR}/bin/${deb_arch}/${BINARY_NAME}"
    mkdir -p "$(dirname "$bin_path")"

    log "Compiling for ${deb_arch} (GOARCH=${goarch}${goarm:+, GOARM=${goarm}})..."

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

# ---------- deb package assembly ---------------------------------------------
build_deb() {
    local deb_arch="$1"
    local pkg_dir="${BUILD_DIR}/pkg/${deb_arch}"
    local bin_path="${BUILD_DIR}/bin/${deb_arch}/${BINARY_NAME}"

    log "Assembling deb package for ${deb_arch}..."

    # Clean and create package tree
    rm -rf "$pkg_dir"

    # Binary
    install -Dm 0755 "$bin_path" "${pkg_dir}/usr/local/bin/${BINARY_NAME}"

    # Config (marked as conffile - apt will not overwrite it on upgrade)
    install -Dm 0640 "etc/simplemon.yaml" \
        "${pkg_dir}/etc/simplemon/simplemon.yaml"

    # Systemd unit
    install -Dm 0644 "systemd/simplemon.service" \
        "${pkg_dir}/lib/systemd/system/simplemon.service"

    # HTML frontend
    install -Dm 0644 "html/simplemon.html" \
        "${pkg_dir}/var/www/simplemon/simplemon.html"

    # Installed size (for control file)
    local installed_size
    installed_size=$(du -sk "$pkg_dir" | cut -f1)

    # ---------- DEBIAN/control -----------------------------------------------
    mkdir -p "${pkg_dir}/DEBIAN"

    cat > "${pkg_dir}/DEBIAN/control" <<EOF
Package: ${PACKAGE_NAME}
Version: ${VERSION}
Architecture: ${deb_arch}
Maintainer: ${MAINTAINER}
Installed-Size: ${installed_size}
Depends: systemd
Recommends: nginx
Section: ${SECTION}
Priority: ${PRIORITY}
Homepage: ${HOMEPAGE}
Description: ${DESCRIPTION}
 SimpleMon is a lightweight Linux server monitoring daemon written in Go.
 No databases, message brokers, or external dependencies required.
 Collects CPU, RAM, Swap, network interfaces, and custom shell widgets.
 Exposes a JSON API and a single-file HTML frontend.
EOF

    # ---------- conffiles -----------------------------------------------------
    cat > "${pkg_dir}/DEBIAN/conffiles" <<EOF
/etc/simplemon/simplemon.yaml
EOF

    # ---------- preinst -------------------------------------------------------
    cat > "${pkg_dir}/DEBIAN/preinst" <<'EOF'
#!/bin/sh
set -e
# Create system user if it does not exist
if ! id simplemon >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin \
        --comment "SimpleMon monitoring daemon" simplemon
fi
EOF

    # ---------- postinst ------------------------------------------------------
    cat > "${pkg_dir}/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e

# Set config file permissions
chown root:simplemon /etc/simplemon/simplemon.yaml || true
chmod 640 /etc/simplemon/simplemon.yaml || true

# Enable and start the service
if command -v systemctl >/dev/null 2>&1 && systemctl is-system-running --quiet 2>/dev/null; then
    systemctl daemon-reload || true
    systemctl enable simplemon || true
    systemctl start simplemon || true
fi

echo ""
echo "  SimpleMon installed and running."
echo "  Frontend: /var/www/simplemon/simplemon.html"
echo "  API:      http://127.0.0.1:8095/health"
echo "  Config:   /etc/simplemon/simplemon.yaml"
echo "  Logs:     journalctl -u simplemon -f"
echo ""
EOF

    # ---------- prerm ---------------------------------------------------------
    cat > "${pkg_dir}/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e
if command -v systemctl >/dev/null 2>&1; then
    systemctl stop simplemon || true
    systemctl disable simplemon || true
fi
EOF

    # ---------- postrm --------------------------------------------------------
    cat > "${pkg_dir}/DEBIAN/postrm" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = "purge" ]; then
    rm -rf /etc/simplemon
    userdel simplemon 2>/dev/null || true
    if command -v systemctl >/dev/null 2>&1; then
        systemctl daemon-reload || true
    fi
fi
EOF

    chmod 0755 \
        "${pkg_dir}/DEBIAN/preinst" \
        "${pkg_dir}/DEBIAN/postinst" \
        "${pkg_dir}/DEBIAN/prerm" \
        "${pkg_dir}/DEBIAN/postrm"

    # ---------- build .deb ---------------------------------------------------
    mkdir -p "$DIST_DIR"
    local deb_file="${DIST_DIR}/${PACKAGE_NAME}_${VERSION}_${deb_arch}.deb"

    fakeroot dpkg-deb --build --root-owner-group "$pkg_dir" "$deb_file"

    ok "Package ready: ${deb_file} ($(du -sh "$deb_file" | cut -f1))"

    # Show package contents
    if command -v dpkg-deb &>/dev/null; then
        echo ""
        log "Package contents (${deb_arch}):"
        dpkg-deb --contents "$deb_file" | awk '{print "  "$NF}' | head -20
    fi
}

# ---------- main -------------------------------------------------------------
main() {
    echo -e "\n${BOLD}=== SimpleMon DEB Builder v${VERSION} ===${NC}\n"

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
        build_deb "$arch"
    done

    # Summary
    echo ""
    echo -e "${BOLD}=== Done ===${NC}"
    echo ""
    ls -lh "${DIST_DIR}"/*.deb 2>/dev/null || true
    echo ""
    echo -e "Install the package:"
    echo -e "  ${CYAN}sudo dpkg -i dist/${PACKAGE_NAME}_${VERSION}_amd64.deb${NC}"
    echo ""
}

main "$@"
