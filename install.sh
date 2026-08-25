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
need sort
need uniq
need awk

compute_file_sha256() {
  target_file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$target_file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$target_file" | awk '{print $1}'
  else
    err "missing SHA-256 tool (sha256sum or shasum required)"
  fi
}

verify_no_symlink_ancestors() {
  check_path="$1"
  curr="$check_path"
  while [ -n "$curr" ] && [ "$curr" != "/" ] && [ "$curr" != "." ]; do
    if [ -L "$curr" ]; then
      err "ancestor component is a forbidden symlink: $curr"
    fi
    next_curr=$(dirname "$curr")
    if [ "$next_curr" = "$curr" ]; then
      break
    fi
    curr="$next_curr"
  done
}

ensure_dir_secure() {
  target_dir="$1"
  if [ -e "$target_dir" ]; then
    [ -d "$target_dir" ] && [ ! -L "$target_dir" ] || err "directory is not a safe regular directory: $target_dir"
    verify_no_symlink_ancestors "$target_dir"
    return
  fi
  mkdir -p "$target_dir"
  chmod 0700 "$target_dir" 2>/dev/null || true
  verify_no_symlink_ancestors "$target_dir"
}

safe_extract_tar() {
  archive="$1"
  destination="$2"
  expected_member="better-drive"

  ensure_dir_secure "$destination"

  # Exact root-level allowlist and entry budget preflight. Restricting the
  # archive to at most one binary and one of each documentation class bounds
  # aggregate extraction independently of platform-specific tar listing
  # columns.
  count=0
  binary_count=0
  license_count=0
  readme_count=0
  changelog_count=0
  raw_list="$destination/.raw-list"
  tar -tzf "$archive" > "$raw_list" || err "could not list archive"

  while IFS= read -r entry; do
    [ -n "$entry" ] || continue
    count=$((count + 1))
    [ "$count" -le 4 ] || err "archive contains too many entries"

    case "$entry" in
      /*|../*|*/../*|*:/|*:/\*|*\\*|*"\0"*) err "unsafe archive path: $entry" ;;
    esac
    case "$entry" in
      ./*) clean_entry="${entry#./}" ;;
      *) clean_entry="$entry" ;;
    esac
    case "$clean_entry" in
      CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9]|CON.*|PRN.*|AUX.*|NUL.*|COM[1-9].*|LPT[1-9].*)
        err "archive contains forbidden device name: $entry"
        ;;
      "$expected_member")
        binary_count=$((binary_count + 1))
        ;;
      LICENSE|LICENSE.*)
        license_count=$((license_count + 1))
        ;;
      README|README.*)
        readme_count=$((readme_count + 1))
        ;;
      CHANGELOG|CHANGELOG.*)
        changelog_count=$((changelog_count + 1))
        ;;
      *)
        err "archive contains unexpected non-allowlisted entry: $entry"
        ;;
    esac
  done < "$raw_list"

  [ "$binary_count" -eq 1 ] || err "archive must contain exactly one $expected_member"
  [ "$license_count" -le 1 ] || err "archive contains multiple LICENSE entries"
  [ "$readme_count" -le 1 ] || err "archive contains multiple README entries"
  [ "$changelog_count" -le 1 ] || err "archive contains multiple CHANGELOG entries"

  duplicates=$(sed 's#^\./##' "$raw_list" | sort | uniq -d)
  if [ -n "$duplicates" ]; then
    rm -f "$raw_list"
    err "archive contains duplicate entries: $duplicates"
  fi
  rm -f "$raw_list"

  # Entry type is the first portable tar -tv column. Size column positions
  # differ between GNU tar and BSD tar, so size is enforced by a child file
  # limit plus exact post-extraction byte readback instead.
  archive_details="$destination/.archive-details"
  tar -tvzf "$archive" > "$archive_details" || err "could not inspect archive entry types"
  while IFS= read -r listing; do
    [ -n "$listing" ] || continue
    first_char=$(printf '%s' "$listing" | cut -c1)
    case "$first_char" in
      l|h) rm -f "$archive_details"; err "archive symlink/hardlink is forbidden: $listing" ;;
      b|c|p|s|d) rm -f "$archive_details"; err "archive non-regular entry is forbidden: $listing" ;;
      -) ;;
      *) rm -f "$archive_details"; err "unrecognized archive entry type: $listing" ;;
    esac
  done < "$archive_details"
  rm -f "$archive_details"

  # Bash/sh file-size limit is expressed in KiB. Each allowlisted member is
  # capped at 100 MiB before tar can materialize an oversized file.
  (ulimit -f 102400; tar -xzf "$archive" -C "$destination") ||
    err "archive extraction failed or exceeded the 100MB per-entry limit"

  # Verify allowlist and structure: only regular files/directories allowed
  extracted_bin="$destination/$expected_member"
  if [ ! -f "$extracted_bin" ]; then
    err "archive does not contain expected regular file $expected_member at root"
  fi
  if [ -L "$extracted_bin" ]; then
    err "extracted binary is a symlink"
  fi

  # Exact allowlist validation: ensure no stray files or unexpected members
  total_bytes=0
  for item in "$destination"/*; do
    [ -e "$item" ] || continue
    base_item=$(basename "$item")
    [ -f "$item" ] && [ ! -L "$item" ] || err "extracted member is not a regular non-symlink file: $base_item"
    item_size=$(wc -c < "$item" | tr -d '[:space:]')
    case "$item_size" in
      ''|*[!0-9]*) err "could not read extracted member size: $base_item" ;;
    esac
    [ "$item_size" -le 104857600 ] || err "extracted member exceeds 100MB: $base_item"
    total_bytes=$((total_bytes + item_size))
    [ "$total_bytes" -le 524288000 ] || err "extracted archive exceeds 500MB aggregate limit"
    case "$base_item" in
      "$expected_member"|LICENSE|LICENSE.*|README|README.*|CHANGELOG|CHANGELOG.*)
        ;;
      *)
        err "archive contains unexpected non-allowlisted file: $base_item"
        ;;
    esac
  done
}

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

# Strip leading 'v' for asset name templating
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

asset="better-drive_${ver_trim}_${os}_${arch}.tar.gz"
url="https://github.com/$REPO/releases/download/$VERSION/$asset"
checksum_url="https://github.com/$REPO/releases/download/$VERSION/checksums.txt"
sigstore_bundle_url="https://github.com/$REPO/releases/download/$VERSION/checksums.txt.sigstore.json"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
ensure_dir_secure "$tmp"

log "Downloading $asset"
curl -fsSL "$url" -o "$tmp/better-drive.tar.gz"
curl -fsSL "$checksum_url" -o "$tmp/checksums.txt"
curl -fsSL "$sigstore_bundle_url" -o "$tmp/checksums.txt.sigstore.json"

log "Verifying SHA256 checksum"
actual=$(compute_file_sha256 "$tmp/better-drive.tar.gz")
expected=$(grep "  $asset" "$tmp/checksums.txt" | awk '{print $1}')
[ -n "$expected" ] || err "no checksum row for $asset in checksums.txt"
[ "$expected" = "$actual" ] || err "checksum mismatch (expected $expected, got $actual)"

need cosign
log "Verifying cosign Sigstore bundle signature"
cosign verify-blob \
  --bundle "$tmp/checksums.txt.sigstore.json" \
  --certificate-identity-regexp "https://github.com/$REPO/.+" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  "$tmp/checksums.txt" >/dev/null 2>&1 || err "cosign Sigstore bundle verification failed"

extract="$tmp/extract"
log "Extracting through SAFE-ARCHIVE-V1 preflight"
safe_extract_tar "$tmp/better-drive.tar.gz" "$extract"
[ -f "$extract/better-drive" ] || err "archive does not contain better-drive at its root"

# Staged atomic replacement with ancestor and hash verification
dest="$PREFIX/better-drive"
ensure_dir_secure "$PREFIX"
verify_no_symlink_ancestors "$PREFIX"

src_bin="$extract/better-drive"
src_hash=$(compute_file_sha256 "$src_bin")

log "Installing $dest"
[ ! -L "$dest" ] || err "installed binary path is a symlink: $dest"
backup_file=""
if [ -n "${USE_SUDO:-}" ]; then
  stage_file=$(sudo mktemp "$PREFIX/.better-drive.install.XXXXXX") || err "could not create staged install file"
  sudo cp "$src_bin" "$stage_file"
  sudo chmod 0755 "$stage_file"
  stage_hash=$(compute_file_sha256 "$stage_file")
  [ "$src_hash" = "$stage_hash" ] || { sudo rm -f "$stage_file"; err "staged binary hash mismatch"; }
  if [ -e "$dest" ]; then
    backup_file=$(sudo mktemp "$PREFIX/.better-drive.backup.XXXXXX") || { sudo rm -f "$stage_file"; err "could not create install backup"; }
    sudo cp -p "$dest" "$backup_file" || { sudo rm -f "$stage_file" "$backup_file"; err "could not preserve installed binary"; }
  fi
  sudo mv "$stage_file" "$dest" || { sudo rm -f "$stage_file"; err "atomic install replacement failed"; }
else
  stage_file=$(mktemp "$PREFIX/.better-drive.install.XXXXXX") || err "could not create staged install file"
  cp "$src_bin" "$stage_file"
  chmod 0755 "$stage_file"
  stage_hash=$(compute_file_sha256 "$stage_file")
  [ "$src_hash" = "$stage_hash" ] || { rm -f "$stage_file"; err "staged binary hash mismatch"; }
  if [ -e "$dest" ]; then
    backup_file=$(mktemp "$PREFIX/.better-drive.backup.XXXXXX") || { rm -f "$stage_file"; err "could not create install backup"; }
    cp -p "$dest" "$backup_file" || { rm -f "$stage_file" "$backup_file"; err "could not preserve installed binary"; }
  fi
  mv "$stage_file" "$dest" || { rm -f "$stage_file"; err "atomic install replacement failed"; }
fi

installed_hash=$(compute_file_sha256 "$dest")
if [ "$src_hash" != "$installed_hash" ]; then
  if [ -n "$backup_file" ]; then
    if [ -n "${USE_SUDO:-}" ]; then
      sudo mv "$backup_file" "$dest" || err "installed hash mismatch and previous binary restore failed"
    else
      mv "$backup_file" "$dest" || err "installed hash mismatch and previous binary restore failed"
    fi
  elif [ -n "${USE_SUDO:-}" ]; then
    sudo rm -f "$dest"
  else
    rm -f "$dest"
  fi
  err "installed binary hash mismatch; previous state restored"
fi
if [ -n "$backup_file" ]; then
  if [ -n "${USE_SUDO:-}" ]; then sudo rm -f "$backup_file"; else rm -f "$backup_file"; fi
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
