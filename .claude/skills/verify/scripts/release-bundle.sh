#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: release-bundle.sh <release-binaries run id>" >&2
  exit 2
fi
run=$1
work=$(cd "$(mktemp -d "${TMPDIR:-/tmp}/oap-release-bundle.XXXXXX")" && pwd -P)
trap 'rm -rf "$work"' EXIT

gh run view "$run" --repo lsm/oxap --json headSha,event,conclusion --jq '"run \(.event) \(.headSha) \(.conclusion)"'
gh run download "$run" --repo lsm/oxap --name release-bundle --dir "$work"
cd "$work"
shasum -a 256 -c SHA256SUMS.txt > checked.txt
echo "digests checked: $(wc -l < checked.txt | tr -d ' ')"
echo "npm tarballs: $(find npm -name '*.tgz' | wc -l | tr -d ' ')"
echo "crates: $(find crates -name '*.crate' | wc -l | tr -d ' ')"
echo "python: $(find python -type f | wc -l | tr -d ' ')"
echo "archives: $(find binaries -name 'oxap-*.tar.gz' -o -name 'oxap-*.zip' | wc -l | tr -d ' ')"
find npm crates python binaries -type f | sort
test "$(find npm -name '*.tgz' | wc -l)" -eq 9
test "$(find binaries -name 'oxap-*.tar.gz' -o -name 'oxap-*.zip' | wc -l)" -eq 6
test -n "$(find python -name '*.whl')"
test -n "$(find crates -name '*.crate')"
grep -c '^npm publish ' PUBLISH-BY-HAND.md
grep -c '^cargo publish ' PUBLISH-BY-HAND.md
echo ok
