import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { breakingSection, compareVersions, firstLine, fold } from "./changelog-release.mjs";

const SCRIPT = new URL("./changelog-release.mjs", import.meta.url).pathname;
const HEAD = "# Changelog\n\nAll notable changes to this project will be documented in this file.\n\n";

function cli(args, input, script) {
  try {
    const stdout = execFileSync("node", [script ?? SCRIPT, ...args], { input, encoding: "utf8", stdio: ["pipe", "pipe", "pipe"] });
    return { code: 0, stdout, stderr: "" };
  } catch (error) {
    return { code: error.status, stdout: error.stdout ?? "", stderr: error.stderr ?? "" };
  }
}

function changelogFile(text) {
  const directory = mkdtempSync(join(tmpdir(), "changelog-release-"));
  const path = join(directory, "CHANGELOG.md");
  writeFileSync(path, text);
  return path;
}

test("a prerelease sorts below the release it precedes", () => {
  assert.equal(compareVersions("0.1.0-alpha.4", "0.1.0"), -1);
  assert.equal(compareVersions("0.1.0", "0.1.0-alpha.4"), 1);
  assert.equal(compareVersions("0.1.0-alpha.4", "0.1.0-alpha.4"), 0);
  assert.equal(compareVersions("0.1.0-alpha.4", "0.2.0"), -1);
  assert.equal(compareVersions("0.2.0", "0.10.0"), -1);
  assert.equal(compareVersions("0.2.0-alpha.1", "0.2.0-alpha.2"), -1);
});

test("a two-digit prerelease counter orders above a single digit", () => {
  // The tags here run v0.1.0-alpha.N, so this is the reachable crossover. As
  // strings "alpha.10" < "alpha.9", which is the opposite of semver 11.4.
  assert.equal(compareVersions("0.1.0-alpha.9", "0.1.0-alpha.10"), -1);
  assert.equal(compareVersions("0.1.0-alpha.10", "0.1.0-alpha.9"), 1);
  assert.equal(compareVersions("0.1.0-alpha.10", "0.1.0-alpha.10"), 0);
  assert.equal(compareVersions("0.1.0-alpha.4", "0.1.0-alpha.10"), -1);
  assert.equal(compareVersions("0.1.0-alpha.19", "0.1.0-alpha.20"), -1);
});

test("a numeric prerelease field has lower precedence than an alphanumeric one", () => {
  assert.equal(compareVersions("0.1.0-1", "0.1.0-alpha"), -1);
  assert.equal(compareVersions("0.1.0-alpha", "0.1.0-1"), 1);
  assert.equal(compareVersions("0.1.0-alpha.1", "0.1.0-beta"), -1);
  assert.equal(compareVersions("0.1.0-rc.1", "0.1.0-rc.2"), -1);
});

test("the ordering guard accepts the next counter and refuses a regression", () => {
  const withAlpha9 = `${HEAD}## Unreleased\n\n## [0.1.0-alpha.9] - 2026-09-20\n\n## [0.1.0-alpha.8] - 2026-09-19\n`;
  const accepted = fold(withAlpha9, {
    version: "0.1.0-alpha.10",
    date: "2026-09-29",
    changelog: "CHANGELOG.md",
    pullRequests: [],
  });
  assert.match(accepted.text, /## \[0\.1\.0-alpha\.10\] - 2026-09-29/);
  assert.throws(
    () => fold(accepted.text, {
      version: "0.1.0-alpha.9",
      date: "2026-09-30",
      changelog: "CHANGELOG.md",
      pullRequests: [],
    }),
    /is older than 0\.1\.0-alpha\.10/,
    "a regressed counter must be refused once a two-digit one exists",
  );
});

test("the line is the first sentence of the first prose paragraph, unwrapped", () => {
  const body = [
    "## What changed and why",
    "",
    "The group is one per pull request, so a newer",
    "push cancels the run the previous push started. Nothing else is touched.",
    "",
    "## Notes for reviewers",
    "",
    "Never read this part.",
  ].join("\n");
  assert.equal(
    firstLine(body),
    "The group is one per pull request, so a newer push cancels the run the previous push started.",
  );
});

test("the template's HTML comment block is not the first line", () => {
  const body = [
    "## What changed and why",
    "",
    "<!-- 2-4 sentences, then bullets. Skip the design essay. -->",
    "",
    "A real sentence about the change.",
  ].join("\n");
  assert.equal(firstLine(body), "A real sentence about the change.");
});

test("a bullet paragraph is skipped for the prose that follows it", () => {
  const body = "\n- one\n- two\n\nThe prose.\n";
  assert.equal(firstLine(body), "The prose.");
});

test("a long sentence is clipped at a word boundary with an ellipsis", () => {
  const line = firstLine(`${"word ".repeat(80)}end.`);
  assert.ok(line.length <= 241, `clipped to ${line.length} characters`);
  assert.ok(line.endsWith("…"), "clipped lines end in an ellipsis");
  assert.ok(!line.includes("  "), "no double space is left where the cut fell");
});

test("an empty body yields no line, so the entry is still emitted", () => {
  const { text } = fold(`${HEAD}## Unreleased\n\n`, {
    version: "0.3.0",
    date: "2026-09-29",
    changelog: "CHANGELOG.md",
    pullRequests: [{ number: 7, title: "fix: a thing", body: "", url: "https://example/7" }],
  });
  assert.match(text, /- \*\*fix: a thing\*\* \(\[#7\]\(https:\/\/example\/7\)\)/);
});

test("a release carries the pending section forward and leaves Unreleased empty", () => {
  const before = `${HEAD}## Unreleased\n\n### Added\n\n- hand written work\n\n## [0.2.0] - 2026-09-11\n\n### Fixed\n\n- older\n`;
  const { text, count } = fold(before, {
    version: "0.3.0",
    date: "2026-09-29",
    changelog: "CHANGELOG.md",
    pullRequests: [{ number: 9, title: "zig: a thing", body: "Why it changed.", url: "https://example/9" }],
  });
  assert.equal(count, 1);
  assert.match(text, /## \[0\.3\.0\] - 2026-09-29/);
  assert.ok(text.includes("- hand written work"), "the pending section is carried forward");
  assert.match(text, /### Merged pull requests\n\n- \*\*zig: a thing\*\* \(\[#9\]\(https:\/\/example\/9\)\): Why it changed\./);
  assert.ok(text.includes("## [0.2.0] - 2026-09-11"), "the earlier release is untouched");
  assert.ok(text.includes("- older"), "the earlier release keeps its own entries");
  const afterUnreleased = text.slice(text.indexOf("## Unreleased") + "## Unreleased".length);
  const body = afterUnreleased.split("\n## ")[0];
  assert.equal(body.trim(), "", `Unreleased left as ${JSON.stringify(body)}`);
  assert.ok(
    text.indexOf("## Unreleased") < text.indexOf("## [0.3.0]"),
    "Unreleased stays on top, above the release it was cut into",
  );
  assert.ok(
    text.indexOf("## [0.3.0]") < text.indexOf("## [0.2.0]"),
    "the new release sits below Unreleased and above the previous one",
  );
});

test("folding twice is refused rather than duplicating a version", () => {
  const once = fold(`${HEAD}## Unreleased\n\n`, {
    version: "0.3.0",
    date: "2026-09-29",
    changelog: "CHANGELOG.md",
    pullRequests: [],
  }).text;
  assert.throws(
    () => fold(once, { version: "0.3.0", date: "2026-09-29", changelog: "CHANGELOG.md", pullRequests: [] }),
    /already has a 0\.3\.0 section/,
  );
});

test("a version older than the newest release is refused", () => {
  assert.throws(
    () => fold(`${HEAD}## Unreleased\n\n## [0.2.0] - 2026-09-11\n`, {
      version: "0.1.0-alpha.5",
      date: "2026-09-29",
      changelog: "CHANGELOG.md",
      pullRequests: [],
    }),
    /is older than 0\.2\.0/,
  );
});

test("the refusal names the newest release, not the oldest heading in the file", () => {
  // Keep a Changelog orders newest first, so the last heading is the oldest
  // version. Comparing against it would let 0.1.6 through as a release, between
  // two versions the file already records.
  const threeReleases = `${HEAD}## Unreleased\n\n## [0.2.0] - 2026-09-11\n\n- newer\n\n## [0.1.0] - 2026-09-05\n\n- oldest\n`;
  assert.throws(
    () => fold(threeReleases, {
      version: "0.1.6",
      date: "2026-09-29",
      changelog: "CHANGELOG.md",
      pullRequests: [],
    }),
    /is older than 0\.2\.0/,
    "0.1.6 is older than 0.2.0 even though it is newer than the last heading",
  );
  const { text } = fold(threeReleases, {
    version: "0.3.0",
    date: "2026-09-29",
    changelog: "CHANGELOG.md",
    pullRequests: [],
  });
  assert.ok(text.indexOf("## [0.3.0]") < text.indexOf("## [0.2.0]"), "the new release goes on top");
  assert.ok(text.includes("- oldest"), "the oldest section is still at the bottom");
});

test("a release with nothing pending gets the Keep a Changelog skeleton", () => {
  const { text } = fold(`${HEAD}## Unreleased\n\n## [0.2.0] - 2026-09-11\n`, {
    version: "0.3.0",
    date: "2026-09-29",
    changelog: "CHANGELOG.md",
    pullRequests: [],
  });
  for (const name of ["Added", "Changed", "Fixed"]) {
    assert.ok(text.includes(`### ${name}`), `the skeleton has a ${name} heading`);
  }
});

test("a flag with an empty value is refused rather than read as absent", () => {
  const path = changelogFile(`${HEAD}## Unreleased\n\n## [0.2.0] - 2026-09-11\n`);
  const result = cli(["--verify-tag", "", "--changelog", path]);
  assert.notEqual(result.code, 0, "an empty --verify-tag must not exit 0");
  assert.match(result.stderr, /--verify-tag needs a value/);
  assert.doesNotMatch(result.stderr, /--version is required/, "it must not report the wrong problem");
});

test("an unknown flag is refused", () => {
  const result = cli(["--changelog-note", "x"]);
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /unknown argument --changelog-note/);
});

test("--check asks whether a release is already recorded, and needs no pull request list", () => {
  const path = changelogFile(`${HEAD}## Unreleased\n\n## [0.2.0] - 2026-09-11\n\n- older\n`);
  const input = JSON.stringify([{ number: 11, title: "go: a thing", body: "Because of a reason.", url: "https://example/11" }]);

  const before = cli(["--version", "0.3.0", "--date", "2026-09-29", "--changelog", path, "--check"]);
  assert.notEqual(before.code, 0, "an unrecorded release does not pass the check");
  assert.match(before.stderr, /no \[0\.3\.0\] section/);

  const written = cli(["--version", "0.3.0", "--date", "2026-09-29", "--changelog", path, "--write"], input);
  assert.equal(written.code, 0, written.stderr);
  assert.match(written.stdout, /wrote \[0\.3\.0\] - 2026-09-29 with 1 merged pull request\(s\)/);

  const after = cli(["--version", "0.3.0", "--date", "2026-09-29", "--changelog", path, "--check"]);
  assert.equal(after.code, 0, after.stderr);
  assert.match(after.stdout, /PASS changelog: \[0\.3\.0\] has a section/);

  const again = cli(["--version", "0.3.0", "--date", "2026-09-29", "--changelog", path, "--write"], input);
  assert.notEqual(again.code, 0, "cutting the same version twice is refused");
  assert.match(again.stderr, /already has a 0\.3\.0 section/);
});

test("--verify-tag accepts the tag whose section exists and refuses one that does not", () => {
  const path = changelogFile(`${HEAD}## Unreleased\n\n## [0.2.0] - 2026-09-11\n\n## [0.1.0] - 2026-09-05\n`);
  const good = cli(["--verify-tag", "v0.2.0", "--changelog", path]);
  assert.equal(good.code, 0, good.stderr);
  assert.match(good.stdout, /PASS changelog: \[0\.2\.0\] has a section/);
  const bad = cli(["--verify-tag", "v0.1.0-alpha.5", "--changelog", path]);
  assert.notEqual(bad.code, 0);
  assert.match(bad.stderr, /no \[0\.1\.0-alpha\.5\] section/);
});

test("a Breaking changes section is lifted into the release", () => {
  const base = `${HEAD}## Unreleased\n\n## [0.2.0] - 2026-09-11\n\n- old\n`;
  const { text } = fold(base, {
    version: "0.3.0",
    date: "2026-09-29",
    changelog: "CHANGELOG.md",
    pullRequests: [
      { number: 1, title: "go: ordinary", body: "Just prose.", url: "https://example/1" },
      {
        number: 2,
        title: "go/providercatalog: drops ModelsURL",
        body: "## What changed and why\n\nWhy.\n\n## Breaking changes\n\n- `go/providercatalog`.ModelsURL is gone; use Models().\n\n## Notes for reviewers\n\n- unrelated",
        url: "https://example/2",
      },
    ],
  });
  const section = text.slice(text.indexOf("## [0.3.0]"), text.indexOf("## [0.2.0]"));
  assert.match(section, /^## Breaking changes$/m, "the release carries its own Breaking changes heading");
  // The description's first sentence still belongs in the generated list, so
  // scope the "not lifted wholesale" assertions to the Breaking changes block.
  const lifted = section.slice(
    section.indexOf("## Breaking changes"),
    section.indexOf("### Merged pull requests"),
  );
  assert.ok(lifted.includes("ModelsURL is gone"), "the recorded break is carried over");
  assert.ok(!lifted.includes("unrelated"), "the section after it is not swept in");
  assert.ok(!lifted.includes("Why."), "only the Breaking changes body is lifted, not the whole description");
  assert.ok(
    section.indexOf("## Breaking changes") < section.indexOf("### Merged pull requests"),
    "the breaking changes come before the generated list",
  );
});

test("a description with no Breaking changes section contributes no heading", () => {
  const base = `${HEAD}## Unreleased\n\n## [0.2.0] - 2026-09-11\n`;
  const { text } = fold(base, {
    version: "0.3.0",
    date: "2026-09-29",
    changelog: "CHANGELOG.md",
    pullRequests: [{ number: 3, title: "go: ordinary", body: "## What changed and why\n\n- `go/serve` in a bullet\n", url: "https://example/3" }],
  });
  assert.doesNotMatch(text, /^## Breaking changes$/m, "a package named in ordinary prose is not a recorded break");
  assert.match(text, /### Merged pull requests/);
});

test("the Breaking changes heading is case sensitive, matching the Go gate", () => {
  assert.equal(breakingSection("## breaking changes\n\n- `go/serve`\n"), "");
  assert.equal(breakingSection("## Breaking changes\n\n- `go/serve`\n").trim(), "- `go/serve`");
  assert.equal(breakingSection("##BREAKING CHANGES\n\n- x\n"), "");
  assert.equal(breakingSection("## Breaking changes  \n\n- x\n").trim(), "- x");
  assert.equal(breakingSection("### Breaking changes\n\n- x\n"), "", "a level-three heading is not the section");
});

test("a wrapped line beginning with an issue reference is kept", () => {
  // A markdown heading is "#" then a space. Filtering every line that starts
  // with "#" ate a wrapped continuation like this one and cut the sentence.
  const body = "This closes the gap that\n#210 and #215 left\nopen in the client.";
  const line = firstLine(body);
  assert.ok(line.includes("#210"), `kept the reference, got ${JSON.stringify(line)}`);
  assert.ok(line.includes("open in the client"), "kept the rest of the paragraph");
});

test("a real heading is still skipped", () => {
  const body = "## What changed and why\n\nA real sentence, with #210 inside it.";
  assert.equal(firstLine(body), "A real sentence, with #210 inside it.");
  assert.equal(firstLine("# Heading\n\nBody."), "Body.");
  assert.equal(firstLine("###### Deep heading\n\nBody."), "Body.");
});

test("the entry-point guard survives a checkout path holding a space", () => {
  // `file://${process.argv[1]}` mismatches on a percent-encoded path, which made
  // main() never run and the script exit 0 having done nothing.
  const directory = mkdtempSync(join(tmpdir(), "changelog-release-"));
  const spaced = join(directory, "a dir with a space");
  const path = join(spaced, "CHANGELOG.md");
  const script = join(spaced, "changelog-release.mjs");
  mkdirSync(spaced, { recursive: true });
  writeFileSync(path, `${HEAD}## Unreleased\n\n## [0.2.0] - 2026-09-11\n`);
  copyFileSync(SCRIPT, script);
  const input = JSON.stringify([{ number: 3, title: "go: a thing", body: "A reason.", url: "https://example/3" }]);
  const result = cli(["--version", "0.3.0", "--date", "2026-09-29", "--changelog", path, "--write"], input, script);
  assert.equal(result.code, 0, result.stderr);
  assert.match(result.stdout, /wrote \[0\.3\.0\]/, "the script ran from a path with a space");
  assert.match(readFileSync(path, "utf8"), /go: a thing/);
});
