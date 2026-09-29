#!/usr/bin/env node
// Build a release's CHANGELOG section from the pull requests merged since the
// last tag, and fold it into CHANGELOG.md in the Keep a Changelog shape.
//
// The pull request list is read from a file or stdin as a JSON array of
// {number, title, body, mergedAt, url}. Nothing here reaches the network, so the
// same command runs on a maintainer's machine and on a runner. Collect the list
// with:
//
//   gh pr list --repo lsm/open-agent-protocol --state merged \
//     --search "merged:>=<date of the last tag>" \
//     --json number,title,body,mergedAt,url > prs.json
//   node scripts/changelog-release.mjs --version 0.2.1 --input prs.json --write
//
// --write folds the section in. --check and --verify-tag ask whether a version
// section is already there and exit non-zero if not, which is what the release
// workflow runs so a tag cannot ship without its notes.

import { readFileSync, writeFileSync } from "node:fs";

const UNRELEASED = "## Unreleased";
const GENERATED_HEADING = "### Merged pull requests";
const RELEASED = /^## \[([^\]]+)\] - (\S+)\s*$/;
const KEEP_A_CHANGELOG_SECTIONS = ["Added", "Changed", "Deprecated", "Removed", "Fixed", "Security"];
const MAX_LINE = 240;

function fail(message) {
  process.stderr.write(`changelog-release: ${message}\n`);
  process.exit(1);
}

export class ChangelogError extends Error {}

function reject(message) {
  throw new ChangelogError(message);
}

const VALUE_FLAGS = ["--version", "--date", "--input", "--changelog", "--verify-tag"];

function parseArgs(argv) {
  const options = { changelog: "CHANGELOG.md" };
  for (let i = 0; i < argv.length; i += 1) {
    const flag = argv[i];
    const value = argv[i + 1];
    // An empty value is a mistake, not a way to unset the flag: `--verify-tag ""`
    // would otherwise read as no flag at all and report the wrong problem.
    if (VALUE_FLAGS.includes(flag) && (value === undefined || value === "")) {
      fail(`${flag} needs a value`);
    }
    switch (flag) {
      case "--version": options.version = value; i += 1; break;
      case "--date": options.date = value; i += 1; break;
      case "--input": options.input = value; i += 1; break;
      case "--changelog": options.changelog = value; i += 1; break;
      case "--verify-tag": options.verifyTag = value; i += 1; break;
      case "--write": options.write = true; break;
      case "--check": options.check = true; break;
      default: fail(`unknown argument ${flag}`);
    }
  }
  return options;
}

// A version orders by its numeric parts, then a prerelease sorts below the
// release it precedes: 0.1.0-alpha.4 < 0.1.0 < 0.2.0.
export function compareVersions(left, right) {
  const parse = (version) => {
    const [core, ...rest] = String(version).split("-");
    const prerelease = rest.join("-");
    return { numbers: core.split(".").map((part) => Number.parseInt(part, 10) || 0), prerelease };
  };
  const a = parse(left);
  const b = parse(right);
  const length = Math.max(a.numbers.length, b.numbers.length);
  for (let i = 0; i < length; i += 1) {
    const difference = (a.numbers[i] ?? 0) - (b.numbers[i] ?? 0);
    if (difference !== 0) return difference < 0 ? -1 : 1;
  }
  if (a.prerelease === b.prerelease) return 0;
  if (a.prerelease === "") return 1;
  if (b.prerelease === "") return -1;
  return a.prerelease < b.prerelease ? -1 : 1;
}

// The body is markdown, hard-wrapped, and carries the template's HTML comment
// block. The line is the first prose paragraph's first sentence, unwrapped,
// because a physical line of a wrapped body is half a sentence.
export function firstLine(body) {
  const withoutComments = String(body ?? "").replace(/<!--[\s\S]*?-->/g, "");
  const blocks = withoutComments.split(/\n\s*\n/);
  for (const block of blocks) {
    const text = block
      .split("\n")
      .map((line) => line.trim())
      .filter((line) => line.length > 0)
      .filter((line) => !line.startsWith("#") && !line.startsWith("<!--"))
      .join(" ")
      .replace(/\s+/g, " ")
      .trim();
    if (text.length === 0) continue;
    if (/^[-*+]\s/.test(text) || /^\d+\.\s/.test(text)) continue;
    return clip(sentence(text));
  }
  return "";
}

function sentence(text) {
  const boundary = text.search(/[.!?](?=\s+[A-Z`([]|$)/);
  if (boundary < 0) return text;
  return text.slice(0, boundary + 1);
}

function clip(text) {
  if (text.length <= MAX_LINE) return text;
  const cut = text.slice(0, MAX_LINE);
  const space = cut.lastIndexOf(" ");
  return `${(space > MAX_LINE * 0.6 ? cut.slice(0, space) : cut).replace(/[,;:\s]+$/, "")}…`;
}

function entry(pr) {
  const number = pr.number;
  if (typeof number !== "number" || !pr.title) return null;
  const link = pr.url ? `[#${number}](${pr.url})` : `#${number}`;
  const detail = firstLine(pr.body);
  return detail ? `- **${pr.title}** (${link}): ${detail}` : `- **${pr.title}** (${link})`;
}

export function fold(changelog, options) {
  const unreleasedAt = changelog.indexOf(UNRELEASED);
  if (unreleasedAt < 0) reject(`${options.changelog} has no ${UNRELEASED} section`);
  const afterUnreleased = changelog.slice(unreleasedAt + UNRELEASED.length);
  const unreleasedEnd = afterUnreleased.search(/\n## /);
  const pending = unreleasedEnd < 0 ? afterUnreleased : afterUnreleased.slice(0, unreleasedEnd);
  const rest = unreleasedEnd < 0 ? "" : afterUnreleased.slice(unreleasedEnd + 1);

  const releasedHeadings = [...changelog.matchAll(new RegExp(RELEASED.source, "gm"))];
  // Keep a Changelog orders sections newest first, so the highest version wins
  // rather than whichever heading happens to come first or last in the file.
  const newest = releasedHeadings.reduce(
    (highest, match) => (highest === null || compareVersions(match[1], highest[1]) > 0 ? match : highest),
    null,
  );
  if (newest && compareVersions(options.version, newest[1]) < 0) {
    reject(`version ${options.version} is older than ${newest[1]}, which ${options.changelog} already has; reconcile the tag line and the changelog before cutting a release`);
  }
  if (releasedHeadings.some((match) => match[1] === options.version)) {
    reject(`${options.changelog} already has a ${options.version} section`);
  }

  const entries = options.pullRequests.map(entry).filter((line) => line !== null);
  const carried = pending.trim();
  const parts = [`${UNRELEASED}\n`, `## [${options.version}] - ${options.date}\n`];
  if (carried) parts.push(`${carried.replace(/\n+$/, "")}\n`);
  if (entries.length > 0) {
    parts.push(`${GENERATED_HEADING}\n\n${entries.join("\n")}\n`);
  }
  if (entries.length === 0 && !carried) {
    parts.push(`${KEEP_A_CHANGELOG_SECTIONS.map((name) => `### ${name}`).join("\n\n")}\n`);
  }

  return {
    text: changelog.slice(0, unreleasedAt) + parts.join("\n") + (rest ? `\n${rest.replace(/^\n+/, "")}` : ""),
    count: entries.length,
  };
}

function recorded(changelog, version) {
  return [...changelog.matchAll(new RegExp(RELEASED.source, "gm"))].some((match) => match[1] === version);
}

function verifyTag(changelog, tag) {
  const version = tag.replace(/^v/, "");
  if (!recorded(changelog, version)) {
    reject(`CHANGELOG.md has no [${version}] section, so this tag would ship without release notes; run this script with --write and commit the result before tagging`);
  }
  process.stdout.write(`PASS changelog: [${version}] has a section\n`);
}

function readPullRequests(options) {
  const raw = options.input && options.input !== "-"
    ? readFileSync(options.input, "utf8")
    : readFileSync(0, "utf8");
  let parsed;
  try {
    parsed = JSON.parse(raw);
  } catch (error) {
    fail(`pull request list is not JSON: ${error.message}`);
  }
  if (!Array.isArray(parsed)) fail("pull request list must be a JSON array");
  return parsed;
}

function main() {
  try {
    run();
  } catch (error) {
    if (error instanceof ChangelogError) fail(error.message);
    throw error;
  }
}

function run() {
  const options = parseArgs(process.argv.slice(2));
  if (options.verifyTag) {
    verifyTag(readFileSync(options.changelog, "utf8"), options.verifyTag);
    return;
  }
  if (!options.version) fail("--version is required");
  if (!options.write && !options.check) fail("one of --write or --check is required");

  const changelog = readFileSync(options.changelog, "utf8");
  // --check asks whether this release is already recorded, so it needs neither a
  // date nor a pull request list. It is the same predicate --verify-tag applies
  // to a tag, which is why the release workflow runs that one.
  if (options.check) {
    verifyTag(changelog, options.version);
    return;
  }

  if (!options.date) fail("--date is required for --write, as YYYY-MM-DD");
  options.pullRequests = readPullRequests(options);
  const folded = fold(changelog, options);
  writeFileSync(options.changelog, folded.text);
  process.stdout.write(`wrote [${options.version}] - ${options.date} with ${folded.count} merged pull request(s)\n`);
}

if (process.argv[1] && import.meta.url === `file://${process.argv[1]}`) main();
