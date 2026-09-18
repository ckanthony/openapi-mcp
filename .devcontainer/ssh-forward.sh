#!/usr/bin/env bash
# Copies the host's SSH config + keys and git identity into the container so
# git can push over SSH (git@github.com:...) exactly like on the host.
#
# The host's ~/.ssh and ~/.gitconfig are bind-mounted (read-only) into the
# container's home by devcontainer.json; this script runs as the container
# user (remoteUser "vscode") after the container is created:
#   1. copies ~/.ssh-host -> ~/.ssh (and fixes permissions),
#   2. copies ~/.gitconfig-host -> ~/.gitconfig unless the container already
#      has a credential config (e.g. the VS Code HTTPS bridge).
set -euo pipefail

HOST_SSH=/home/vscode/.ssh-host
HOST_GIT=/home/vscode/.gitconfig-host
SSH=/home/vscode/.ssh
GIT=/home/vscode/.gitconfig

# --- SSH: host config, keys and known_hosts --------------------------------
if [ -d "$HOST_SSH" ] && [ -n "$(find "$HOST_SSH" -mindepth 1 -maxdepth 1 2>/dev/null)" ]; then
  mkdir -p "$SSH"
  cp -a "$HOST_SSH"/. "$SSH/"
  chmod 700 "$SSH"
  chmod 600 "$SSH"/* 2>/dev/null || true
fi

# Make sure GitHub's host key is known so ssh does not prompt.
if [ -n "$(find "$SSH" -name known_hosts 2>/dev/null)" ] && ! grep -q "github.com" "$SSH/known_hosts" 2>/dev/null; then
  command -v ssh-keyscan >/dev/null 2>&1 && (ssh-keyscan github.com >>"$SSH/known_hosts" 2>/dev/null || true)
fi

# --- Git identity: host name/e-mail ----------------------------------------
# Only overwrite the container's config when it has no credential setup of
# its own (the VS Code HTTPS credential bridge is managed by VS Code and
# overrides this file each attach).
if [ -f "$HOST_GIT" ]; then
  if ! grep -q "credential" "$GIT" 2>/dev/null; then
    cp -f "$HOST_GIT" "$GIT"
    # Host config may include paths that do not exist in the container.
    sed -i '/^\[include/d;/path[[:space:]]*=/d' "$GIT" 2>/dev/null || true
  fi
fi

git config --global --add safe.directory '*'