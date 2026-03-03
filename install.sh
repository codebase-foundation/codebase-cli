#!/bin/sh
set -e

# Codebase CLI installer (Linux / macOS)
# Usage: curl -sSL https://raw.githubusercontent.com/codebase-foundation/codebase-cli/main/install.sh | sh

REPO="codebase-foundation/codebase-cli"
BINARY="codebase"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# Detect OS
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux)  OS="linux" ;;
  darwin) OS="darwin" ;;
  *)      echo "Unsupported OS: $OS (use install.ps1 for Windows)"; exit 1 ;;
esac

# Detect architecture
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64)  ARCH="arm64" ;;
  *)              echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

# Get latest release tag
echo "Finding latest release..."
TAG=$(curl -sSf "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | cut -d'"' -f4)
if [ -z "$TAG" ]; then
  echo "Error: Could not find latest release. Check https://github.com/${REPO}/releases"
  exit 1
fi
echo "Latest version: $TAG"

# Download
ARCHIVE="${BINARY}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${TAG}/${ARCHIVE}"

echo "Downloading ${URL}..."
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

curl -sSfL "$URL" -o "${TMP}/${ARCHIVE}"
tar -xzf "${TMP}/${ARCHIVE}" -C "$TMP"

# Install
if [ -w "$INSTALL_DIR" ]; then
  mv "${TMP}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mv "${TMP}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
fi

chmod +x "${INSTALL_DIR}/${BINARY}"

echo ""
echo "Installed ${BINARY} ${TAG} to ${INSTALL_DIR}/${BINARY}"

# Check if INSTALL_DIR is in PATH
case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    echo ""
    echo "WARNING: ${INSTALL_DIR} is not in your PATH."
    echo ""
    SHELL_NAME="$(basename "${SHELL:-/bin/sh}")"
    case "$SHELL_NAME" in
      zsh)  RC="~/.zshrc" ;;
      bash) RC="~/.bashrc" ;;
      fish) RC="~/.config/fish/config.fish" ;;
      *)    RC="your shell rc file" ;;
    esac
    if [ "$SHELL_NAME" = "fish" ]; then
      echo "Add this to ${RC}:"
      echo "  fish_add_path ${INSTALL_DIR}"
    else
      echo "Add this to ${RC}:"
      echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    fi
    echo ""
    echo "Then restart your terminal or run: source ${RC}"
    ;;
esac

echo ""
echo "Run 'codebase' in any project directory to get started."
