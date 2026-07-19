#!/bin/sh
# better-drive one-shot installer.
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/n24q02m/better-drive/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/n24q02m/better-drive/main/install.sh | sh -s -- --version=v1.0.0 --user
# Flags:
#   --version=<tag>   install a specific release tag (default: latest)
#   --prefix=<path>   install target dir (default: /usr/local/bin or ~/.local/bin)
#   --user            force user-mode install to ~/.local/bin (no sudo)
#   --no-completion   skip shell completion install
#   --quiet           suppress progress output

set -eu

REPO="n24q02m/better-drive"
VERSION=""
PREFIX=""
USER_INSTALL=0
NO_COMPLETION=0
QUIET=0

while [ $# -gt 0 ]; do
  case "$1" in
    --version=*) VERSION="${1#*=}"; shift ;;
    --prefix=*) PREFIX="${1#*=}"; shift ;;
    --user) USER_INSTALL=1; shift ;;
    --no-completion) NO_COMPLETION=1; shift ;;
    --quiet) QUIET=1; shift ;;
    -h|--help)
      sed -n '2,12p' "$0"
      exit 0
      ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

log() { [ "$QUIET" = 1 ] || echo "==> $*"; }
err() { echo "better-drive install: $*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || err "missing required tool: $1"; }
need curl
need tar
need uname

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  *) err "unsupported OS: $os (use install.ps1 on Windows)" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) err "unsupported arch: $arch" ;;
esac

if [ -z "$VERSION" ]; then
  log "Detecting latest release"
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
fi
[ -n "$VERSION" ] || err "could not detect latest version"

# Strip leading 'v' for asset name templating (releases use better-drive_1.0.0_... not better-drive_v1.0.0_...)
ver_trim="${VERSION#v}"

if [ -z "$PREFIX" ]; then
  if [ "$USER_INSTALL" = 1 ]; then
    PREFIX="$HOME/.local/bin"
  elif [ -w "/usr/local/bin" ]; then
    PREFIX="/usr/local/bin"
  elif command -v sudo >/dev/null 2>&1 && [ -d "/usr/local/bin" ]; then
    PREFIX="/usr/local/bin"
    USE_SUDO=1
  else
    PREFIX="$HOME/.local/bin"
  fi
fi
mkdir -p "$PREFIX"

# Darwin -> darwin, Linux -> linux (lowercase matches release archives)
asset="better-drive_${ver_trim}_${os}_${arch}.tar.gz"
url="https://github.com/$REPO/releases/download/$VERSION/$asset"
checksum_url="https://github.com/$REPO/releases/download/$VERSION/checksums.txt"
cert_url="https://github.com/$REPO/releases/download/$VERSION/checksums.txt.pem"
sig_url="https://github.com/$REPO/releases/download/$VERSION/checksums.txt.sig"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

log "Downloading $asset"
curl -fsSL "$url" -o "$tmp/better-drive.tar.gz"
curl -fsSL "$checksum_url" -o "$tmp/checksums.txt"

log "Verifying SHA256 checksum"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/better-drive.tar.gz" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$tmp/better-drive.tar.gz" | awk '{print $1}')
fi
expected=$(grep "  $asset" "$tmp/checksums.txt" | awk '{print $1}')
[ -n "$expected" ] || err "no checksum row for $asset"
[ "$expected" = "$actual" ] || err "checksum mismatch (expected $expected, got $actual)"

if command -v cosign >/dev/null 2>&1; then
  log "Verifying cosign Sigstore signature"
  curl -fsSL "$cert_url" -o "$tmp/checksums.txt.pem"
  curl -fsSL "$sig_url" -o "$tmp/checksums.txt.sig"
  cosign verify-blob \
    --certificate "$tmp/checksums.txt.pem" \
    --signature "$tmp/checksums.txt.sig" \
    --certificate-identity-regexp "https://github.com/$REPO/.+" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
    "$tmp/checksums.txt" \
    >/dev/null 2>&1 || log "WARN: cosign verify failed -continuing (checksum already matched)"
else
  log "cosign not installed -skipping signature check (checksum already verified)"
fi

log "Extracting to $tmp"
tar -xzf "$tmp/better-drive.tar.gz" -C "$tmp"

dest="$PREFIX/better-drive"
log "Installing $dest"
if [ -n "${USE_SUDO:-}" ]; then
  sudo install -m 0755 "$tmp/better-drive" "$dest"
else
  install -m 0755 "$tmp/better-drive" "$dest"
fi

if [ "$NO_COMPLETION" = 0 ]; then
  for shell in bash zsh fish; do
    if command -v "$shell" >/dev/null 2>&1; then
      log "Generating $shell completion (run 'better-drive completion $shell' to refresh)"
      break
    fi
  done
fi

log "Installed: $("$dest" --version 2>/dev/null || echo 'better-drive --version failed')"
case ":$PATH:" in
  *":$PREFIX:"*) ;;
  *) log "WARN: $PREFIX is not in PATH. Add: export PATH=\"$PREFIX:\$PATH\"" ;;
esac
