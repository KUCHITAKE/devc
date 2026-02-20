#!/usr/bin/env bash
set -euo pipefail

INSTALL_DIR="${HOME}/.local/bin"
mkdir -p "$INSTALL_DIR"
cp devc "$INSTALL_DIR/devc"
chmod +x "$INSTALL_DIR/devc"
echo "Installed devc to ${INSTALL_DIR}/devc"
