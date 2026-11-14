#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: compatibility.sh <checker-tree> <head-commit> <base-ref> <description-file>" >&2
  exit 2
fi
checker=$(cd "$1" && pwd)
head=$2
base_ref=$3
body=$(cd "$(dirname "$4")" && pwd)/$(basename "$4")

work=$(cd "$(mktemp -d "${TMPDIR:-/tmp}/oap-compat.XXXXXX")" && pwd -P)
trap 'rm -rf "$work"' EXIT
merge_base=$(git merge-base "$base_ref" "$head")
mkdir "$work/head" "$work/base"
git archive "$head" | tar -x -C "$work/head"
git archive "$merge_base" | tar -x -C "$work/base"
echo "head $(git rev-parse "$head")"
echo "merge base $merge_base"
cd "$checker"
PR_BASE_ROOT="$work/base" go run ./go/cmd/apidiffcheck -root "$work/head" -description "$body"
