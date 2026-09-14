#!/usr/bin/env sh
# Installs the latest mirror-gha release for your OS/arch.
#
#   curl -fsSL https://raw.githubusercontent.com/pvishnuprasaath/mirror-gha/main/install.sh | sh
#
# Override the install directory (default: $HOME/.local/bin):
#   curl -fsSL .../install.sh | INSTALL_DIR=/usr/local/bin sh
#
# No jq dependency on purpose — keeps this runnable on the widest range of
# machines (bare CI images, minimal containers) without extra setup.

set -eu

REPO="pvishnuprasaath/mirror-gha"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

log() { printf '%s\n' "$*" >&2; }
die() {
	log "error: $*"
	exit 1
}

require_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "'$1' is required but not installed"
}

require_cmd curl
require_cmd tar

detect_os() {
	case "$(uname -s)" in
	Linux) echo "linux" ;;
	Darwin) echo "darwin" ;;
	*) die "unsupported OS: $(uname -s) (mirror-gha ships Linux and macOS binaries only)" ;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) echo "amd64" ;;
	arm64 | aarch64) echo "arm64" ;;
	*) die "unsupported architecture: $(uname -m)" ;;
	esac
}

os=$(detect_os)
arch=$(detect_arch)

log "Fetching latest release metadata..."
release_json=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest") ||
	die "could not reach the GitHub Releases API for ${REPO}"

# Extract the tag without jq: grab the first "tag_name": "..." line's value.
tag=$(printf '%s' "$release_json" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
[ -n "$tag" ] || die "could not determine the latest release tag"
version="${tag#v}"

archive="mirror-gha_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/${REPO}/releases/download/${tag}"

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

log "Downloading ${archive} (${tag})..."
curl -fsSL -o "${tmp_dir}/${archive}" "${base_url}/${archive}" ||
	die "download failed: ${base_url}/${archive}"

log "Verifying checksum..."
curl -fsSL -o "${tmp_dir}/checksums.txt" "${base_url}/checksums.txt" ||
	die "could not download checksums.txt"

expected=$(grep "  ${archive}\$" "${tmp_dir}/checksums.txt" | awk '{print $1}')
[ -n "$expected" ] || die "no checksum entry found for ${archive}"

if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "${tmp_dir}/${archive}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "${tmp_dir}/${archive}" | awk '{print $1}')
else
	die "need either sha256sum or shasum to verify the download"
fi

[ "$expected" = "$actual" ] || die "checksum mismatch for ${archive}: expected ${expected}, got ${actual}"

log "Installing to ${INSTALL_DIR}..."
mkdir -p "$INSTALL_DIR"
tar -xzf "${tmp_dir}/${archive}" -C "$tmp_dir" mirror
mv "${tmp_dir}/mirror" "${INSTALL_DIR}/mirror"
chmod +x "${INSTALL_DIR}/mirror"

log "Installed mirror ${version} to ${INSTALL_DIR}/mirror"
case ":$PATH:" in
*":${INSTALL_DIR}:"*) ;;
*) log "Note: ${INSTALL_DIR} is not on your PATH. Add it, e.g.: export PATH=\"${INSTALL_DIR}:\$PATH\"" ;;
esac
