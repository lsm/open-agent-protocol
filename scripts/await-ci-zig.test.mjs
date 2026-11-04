import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
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
    total=$(wc -l < "$ANSWERS")
    [ "$n" -ge "$total" ] && n=$((total - 1))
    answer=$(sed -n "$((n + 1))p" "$ANSWERS")
    [ "$answer" = unreadable ] && exit 1
    echo "$answer"
    ;;
esac
`;

const own = (state) => ownRun(state, 1);

function ownRun(state, number) {
  const [status, conclusion] = state.split(" ");
  return {
    status,
    conclusion: conclusion === "null" ? null : conclusion,
    head_repository: { full_name: "owner/repo" },
    pull_requests: [{ number }],
  };
}

function forkRun(state, repository) {
  return { ...own(state), head_repository: { full_name: repository }, pull_requests: [] };
}

function run(t, files, answers) {
  const root = mkdtempSync(join(tmpdir(), "await-ci-zig-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const gh = join(root, "gh");
  writeFileSync(gh, fakeGh);
  chmodSync(gh, 0o755);
  const lines = answers.map((runs) =>
    runs === "unreadable" ? runs : JSON.stringify({ workflow_runs: runs }),
  );
  writeFileSync(join(root, "answers"), `${lines.join("\n")}\n`);
  const result = spawnSync(script, [], {
    encoding: "utf8",
    env: {
      ...process.env,
      PATH: `${root}${delimiter}${process.env.PATH}`,
      COUNT: join(root, "count"),
      ANSWERS: join(root, "answers"),
      FILES: files.map((file) => `${file}\\n`).join(""),
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
  assert.equal(run(t, ["docs/a.md", "drafts/b.md", "README.md"], [[]]), 0);
});

test("a nested README is not prose, so a missing run fails", (t) => {
  assert.equal(run(t, ["zig/README.md"], [[]]), 1);
});

test("a code change with no ci-zig run fails rather than passing on nothing", (t) => {
  assert.equal(run(t, ["zig/a.zig"], [[]]), 1);
});

test("the wait lasts until the run passes", (t) => {
  const answers = [[own("queued null")], [own("in_progress null")], [own("completed success")]];
  assert.equal(run(t, ["zig/a.zig"], answers), 0);
});

test("an unreadable answer is retried", (t) => {
  assert.equal(run(t, ["zig/a.zig"], ["unreadable", [own("completed success")]]), 0);
});

test("a failed run fails, even when prose changed beside the code", (t) => {
  const answers = [[own("in_progress null")], [own("completed failure")]];
  assert.equal(run(t, ["zig/a.zig", "docs/a.md"], answers), 1);
});

test("a cancelled run fails", (t) => {
  assert.equal(run(t, ["zig/a.zig"], [[own("completed cancelled")]]), 1);
});

test("another pull request's passing run on the same head does not count", (t) => {
  assert.equal(run(t, ["zig/a.zig"], [[ownRun("completed success", 2)]]), 1);
});

test("this pull request's run is found behind another's newer one", (t) => {
  const answers = [[ownRun("completed success", 2), own("completed failure")]];
  assert.equal(run(t, ["zig/a.zig"], answers), 1);
});

test("a fork's run, which names no pull request, counts", (t) => {
  assert.equal(run(t, ["zig/a.zig"], [[forkRun("completed success", "fork/repo")]]), 0);
});

test("a run that names no pull request from this repository does not count", (t) => {
  assert.equal(run(t, ["zig/a.zig"], [[forkRun("completed success", "owner/repo")]]), 1);
});

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");

test("All Tests Pass needs every other job of ci.yml", () => {
  const workflow = read("../.github/workflows/ci.yml");
  const jobs = workflow
    .slice(workflow.indexOf("\njobs:\n"))
    .split("\n")
    .flatMap((line) => line.match(/^  ([A-Za-z0-9_-]+):$/)?.[1] ?? []);
  const needs = workflow.match(/name: All Tests Pass[\s\S]*?needs: \[(.*)\]/)[1].split(", ");
  assert.deepEqual(needs.toSorted(), jobs.filter((job) => job !== "all-tests-pass").toSorted());
});

test("the script's prose filter is ci-zig.yml's paths-ignore", () => {
  const ignored = read("../.github/workflows/ci-zig.yml")
    .match(/paths-ignore:\n((?: +- .*\n)+)/)[1]
    .split("\n")
    .flatMap((line) => line.match(/- "(.*)"/)?.[1] ?? []);
  const directories = ignored.filter((path) => path.endsWith("/**")).map((path) => path.slice(0, -3));
  assert.deepEqual(ignored.filter((path) => !path.endsWith("/**")), ["*.md"]);
  const filter = `^(${directories.join("|")})/|^[^/]+\\.md$`;
  assert.ok(read("./await-ci-zig.sh").includes(`'${filter}'`), filter);
});
