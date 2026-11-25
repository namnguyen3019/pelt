#!/bin/sh
# Install pelt from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.sh | sh -s -- v0.1.0
#
# The script picks the archive for this OS and CPU, checks it against the
# release's checksums.txt (SHA-256) and installs the pelt binary.
#
# Settings (environment variables):
#   PELT_VERSION   release to install, e.g. v0.1.0 (default: latest).
#                  A version passed as the first argument wins over this.
#   INSTALL_DIR    where to put the binary (default: /usr/local/bin).
#                  sudo is used only when this directory is not writable.
#   PELT_BASE_URL  download assets from here instead of GitHub, for mirrors
#                  and testing; it must hold the release's files directly.

set -eu

REPO="namnguyen3019/pelt"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
VERSION="${1:-${PELT_VERSION:-latest}}"

say() { printf 'pelt-install: %s\n' "$*"; }
fail() { printf 'pelt-install: error: %s\n' "$*" >&2; exit 1; }

detect_os() {
  case "$(uname -s)" in
    Linux) echo linux ;;
    Darwin) echo darwin ;;
    MINGW* | MSYS* | CYGWIN* | Windows_NT)
      fail "Windows is not supported by this script. Download pelt_windows_amd64.zip or pelt_windows_arm64.zip from https://github.com/$REPO/releases instead." ;;
    *) fail "unsupported operating system: $(uname -s) (supported: Linux, macOS)" ;;
  esac
}

detect_arch() {
  arch=$(uname -m)
  # A shell running under Rosetta on Apple silicon reports x86_64.
  if [ "$1" = darwin ] && [ "$arch" = x86_64 ] &&
    [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
    arch=arm64
  fi
  case "$arch" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *) fail "unsupported CPU architecture: $arch (supported: amd64, arm64)" ;;
  esac
}

download() { # url dest
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 -o "$2" "$1" || return 1
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1" || return 1
  else
    fail "curl or wget is required"
  fi
}

sha256() { # file
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" | awk '{print $NF}'
  else
    fail "sha256sum, shasum or openssl is required to verify the download"
  fi
}

main() {
  os=$(detect_os)
  arch=$(detect_arch "$os")
  asset="pelt_${os}_${arch}.tar.gz"

  case "$VERSION" in
    latest) ;;
    v*) ;;
    *) VERSION="v$VERSION" ;;
  esac

  if [ -n "${PELT_BASE_URL:-}" ]; then
    base="${PELT_BASE_URL%/}"
  elif [ "$VERSION" = latest ]; then
    base="https://github.com/$REPO/releases/latest/download"
  else
    base="https://github.com/$REPO/releases/download/$VERSION"
  fi

  tmp=$(mktemp -d 2>/dev/null || mktemp -d -t pelt)
  trap 'rm -rf "$tmp"' EXIT
  trap 'exit 1' INT TERM

  say "downloading $asset ($VERSION)"
  if [ "$VERSION" = latest ]; then what="the latest release"; else what="release $VERSION"; fi
  download "$base/$asset" "$tmp/$asset" ||
    fail "could not download $base/$asset (check that $what exists and has this file)"
  download "$base/checksums.txt" "$tmp/checksums.txt" ||
    fail "could not download $base/checksums.txt"

  expected=$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1 }' "$tmp/checksums.txt")
  [ -n "$expected" ] || fail "checksums.txt has no entry for $asset"
  actual=$(sha256 "$tmp/$asset")
  [ "$expected" = "$actual" ] ||
    fail "checksum mismatch for $asset (expected $expected, got $actual)"
  say "checksum OK"

  tar -xzf "$tmp/$asset" -C "$tmp" pelt || fail "could not extract pelt from $asset"
  chmod 755 "$tmp/pelt"

  sudo=""
  if ! mkdir -p "$INSTALL_DIR" 2>/dev/null || [ ! -w "$INSTALL_DIR" ]; then
    if [ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1; then
      say "$INSTALL_DIR is not writable, using sudo"
      sudo="sudo"
      $sudo mkdir -p "$INSTALL_DIR" ||
        fail "sudo failed; rerun with INSTALL_DIR set to a directory you own, e.g. INSTALL_DIR=\$HOME/.local/bin"
    else
      fail "cannot write to $INSTALL_DIR; set INSTALL_DIR to a directory you own, e.g. INSTALL_DIR=\$HOME/.local/bin"
    fi
  fi
  $sudo cp "$tmp/pelt" "$INSTALL_DIR/pelt"
  $sudo chmod 755 "$INSTALL_DIR/pelt"

  say "installed $INSTALL_DIR/pelt"
  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) say "note: $INSTALL_DIR is not on your PATH; add it to run pelt by name" ;;
  esac
}

main
