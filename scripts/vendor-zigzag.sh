#!/usr/bin/env bash
# Re-vendor zigzag into zig/vendor/zigzag from a checkout of the fork
# (github.com/lsm/zigzag), then strip comments for the zero-comments policy.
#
#   scripts/vendor-zigzag.sh <zigzag-checkout> <ref>
#
# Copies exactly the paths the fork's build.zig.zon declares for its package,
# drops the standalone tests/ loop from build.zig (the package has no tests/),
# and leaves the result staged. Fix vendored code in the fork, never here.
set -euo pipefail

usage="usage: scripts/vendor-zigzag.sh <zigzag-checkout> <ref>"
src="${1:?$usage}"
ref="${2:?$usage}"
root="$(cd "$(dirname "$0")/.." && pwd)"
dest="zig/vendor/zigzag"

sha="$(git -C "$src" rev-parse --verify "$ref^{commit}")"
paths=()
while IFS= read -r path; do
  paths+=("$path")
done < <(git -C "$src" show "$sha:build.zig.zon" |
  awk '/\.paths = \.\{/ { inside = 1; next } inside && /\}/ { exit } inside' |
  sed -n 's/^[[:space:]]*"\([^"]*\)",*[[:space:]]*$/\1/p')
if [ "${#paths[@]}" -eq 0 ]; then
  echo "vendor-zigzag: no .paths in build.zig.zon at $sha" >&2
  exit 1
fi

cd "$root"
rm -rf "$dest"
mkdir -p "$dest"
git -C "$src" archive "$sha" -- "${paths[@]}" | tar -x -C "$dest"

node -e '
const fs = require("fs");
const file = process.argv[1];
const text = fs.readFileSync(file, "utf8");
const start = text.indexOf("    for (test_files) |test_file| {\n");
if (start < 0) {
  console.error("vendor-zigzag: test loop not found in " + file);
  process.exit(1);
}
const end = text.indexOf("\n    }\n", start);
fs.writeFileSync(file, text.slice(0, start) + "    _ = test_files;" + text.slice(end + "\n    }".length));
' "$dest/build.zig"

git add -A "$dest"
files=()
while IFS= read -r -d '' file; do
  files+=("$file")
done < <(git ls-files -z -- "$dest/*.zig" "$dest/*.ts")
node scripts/check-no-comments.mjs --write --files "${files[@]}"
git add -A "$dest"
echo "vendored lsm/zigzag $sha (${paths[*]})"
