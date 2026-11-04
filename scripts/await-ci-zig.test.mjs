import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import { test } from "node:test";

const script = new URL("./await-ci-zig.sh", import.meta.url).pathname;

const fakeGh = `#!/usr/bin/env bash
case "$*" in
  *pulls*) printf '%b' "$FILES" ;;
  *)
    n=$(cat "$COUNT" 2>/dev/null || echo 0)
    echo $((n + 1)) > "$COUNT"
    IFS=, read -ra states <<<"$STATES"
    i=$n
    [ "$i" -ge "\${#states[@]}" ] && i=$((\${#states[@]} - 1))
    [ "\${states[$i]}" = unreadable ] && exit 1
    echo "\${states[$i]}"
    ;;
esac
`;

function run(t, files, states) {
  const root = mkdtempSync(join(tmpdir(), "await-ci-zig-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const gh = join(root, "gh");
  writeFileSync(gh, fakeGh);
  chmodSync(gh, 0o755);
  const result = spawnSync(script, [], {
    encoding: "utf8",
    env: {
      ...process.env,
      PATH: `${root}${delimiter}${process.env.PATH}`,
      COUNT: join(root, "count"),
      FILES: files.map((file) => `${file}\\n`).join(""),
      STATES: states.join(","),
      REPO: "owner/repo",
      PR: "1",
      HEAD_SHA: "abc",
      POLL_SECONDS: "0",
      ABSENT_LIMIT: "3",
    },
  });
  return result.status;
}

test("a prose-only pull request needs no ci-zig run", (t) => {
  assert.equal(run(t, ["docs/a.md", "drafts/b.md", "README.md"], ["absent"]), 0);
});

test("a nested README is not prose, so a missing run fails", (t) => {
  assert.equal(run(t, ["zig/README.md"], ["absent"]), 1);
});

test("a code change with no ci-zig run fails rather than passing on nothing", (t) => {
  assert.equal(run(t, ["zig/a.zig"], ["absent"]), 1);
});

test("the wait lasts until the run passes", (t) => {
  assert.equal(run(t, ["zig/a.zig"], ["queued null", "in_progress null", "completed success"]), 0);
});

test("an unreadable answer is retried", (t) => {
  assert.equal(run(t, ["zig/a.zig"], ["unreadable", "completed success"]), 0);
});

test("a failed run fails, even when prose changed beside the code", (t) => {
  assert.equal(run(t, ["zig/a.zig", "docs/a.md"], ["in_progress null", "completed failure"]), 1);
});

test("a cancelled run fails", (t) => {
  assert.equal(run(t, ["zig/a.zig"], ["completed cancelled"]), 1);
});
