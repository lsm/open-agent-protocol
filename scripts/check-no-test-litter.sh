#!/usr/bin/env bash
set -euo pipefail

root="${1:-.}"
leaked="$(find "$root" \( -name .git -o -name node_modules -o -name .zig-cache -o -name zig-out -o -name target -o -name .venv \) -prune -o \( -path '*/.oapx/auth.json*' -o -path '*/.makai/auth.json*' \) -print)"
if [[ -n "$leaked" ]]; then
  echo "[litter] a test left a credential store inside the checkout:" >&2
  echo "$leaked" >&2
  exit 1
fi
echo "[litter] no credential store inside the checkout"
