#!/usr/bin/env node
// Build a release's CHANGELOG section from the pull requests merged since the
// last tag, and fold it into CHANGELOG.md in the Keep a Changelog shape.
//
// The pull request list is read from a file or stdin as a JSON array of
// {number, title, body, mergedAt, url}. Nothing here reaches the network, so the
// same command runs on a maintainer's machine and on a runner. Collect the list
// with the last tag's exact commit time as the boundary -- docs/releasing.md is
// the procedure, and the timestamp matters:
//
//   gh pr list --repo lsm/open-agent-protocol --state merged \
//     --search "merged:>2026-09-27T13:02:38Z" \
//     --json number,title,body,mergedAt,url > prs.json
//   node scripts/changelog-release.mjs --version 0.2.1 --input prs.json --write
//
// --write folds the section in. --check and --verify-tag ask whether a version
// section is already there and exit non-zero if not, which is what the release
// workflow runs so a tag cannot ship without its notes.

import { readFileSync, realpathSync, writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const UNRELEASED = "## Unreleased";
const GENERATED_HEADING = "### Merged pull requests";
const LIFTED_BREAKING_HEADING = "### Breaking changes";
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
// release it precedes: 0.1.0-alpha.4 < 0.1.0 < 0.2.0. Prerelease identifiers
// follow semver 11.4: dot-separated fields, all-numeric ones compared as
// numbers, so alpha.10 is above alpha.9 rather than below it as the strings
// would sort. Tags here run v0.1.0-alpha.N, so the crossover is reachable.
export function compareVersions(left, right) {
  const parse = (version) => {
    const [core, ...rest] = String(version).split("-");
    return { numbers: core.split(".").map((part) => Number.parseInt(part, 10) || 0), prerelease: rest.join("-") };
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
  const aFields = a.prerelease.split(".");
  const bFields = b.prerelease.split(".");
  const fields = Math.max(aFields.length, bFields.length);
  for (let i = 0; i < fields; i += 1) {
    const x = aFields[i];
    const y = bFields[i];
    if (x === undefined) return -1;
    if (y === undefined) return 1;
    if (x === y) continue;
    const xNumeric = /^\d+$/.test(x);
    const yNumeric = /^\d+$/.test(y);
    if (xNumeric && yNumeric) {
      const difference = Number.parseInt(x, 10) - Number.parseInt(y, 10);
      if (difference !== 0) return difference < 0 ? -1 : 1;
      continue;
    }
    // A numeric identifier always has lower precedence than an alphanumeric one.
    if (xNumeric !== yNumeric) return xNumeric ? -1 : 1;
    return x < y ? -1 : 1;
  }
  return 0;
}

// The body is markdown, hard-wrapped, and carries the template's HTML comment
// block. The line is the first prose paragraph's first sentence, unwrapped,
// because a physical line of a wrapped body is half a sentence.
export const BREAKING_HEADING = /^##[ \t]+Breaking changes[ \t]*$/m;

export function breakingSection(body) {
  const text = String(body ?? "");
  const match = BREAKING_HEADING.exec(text);
  if (match === null) return "";
  const rest = text.slice(match.index + match[0].length);
  const end = rest.search(/^##[ \t]/m);
  return (end < 0 ? rest : rest.slice(0, end)).trim();
}

export function firstLine(body) {
  const withoutComments = String(body ?? "").replace(/<!--[\s\S]*?-->/g, "");
  const blocks = withoutComments.split(/\n\s*\n/);
  for (const block of blocks) {
    const text = block
      .split("\n")
      .map((line) => line.trim())
      .filter((line) => line.length > 0)
      // A markdown heading is "#" then a space. Plain startsWith("#") also ate a
      // wrapped line beginning with an issue reference, which dropped the rest of
      // the sentence mid-clause.
      .filter((line) => !/^#{1,6}\s/.test(line) && !line.startsWith("<!--"))
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
  // A pull request that made an incompatible change to a public Go package
  // carries a "## Breaking changes" section, and that section is the record a
  // reader needs. Lift it into the release under its own heading rather than
  // leaving it in the description where the release notes cannot reach it.
  const breaking = options.pullRequests
    .filter((pr) => typeof pr.number === "number" && pr.title && breakingSection(pr.body))
    .map((pr) => `- **${pr.title}**${pr.url ? ` ([#${pr.number}](${pr.url}))` : ` (#${pr.number})`}\n\n${breakingSection(pr.body)}`);
  const carried = pending.trim();
  const parts = [`${UNRELEASED}\n`, `## [${options.version}] - ${options.date}\n`];
  if (carried) parts.push(`${carried.replace(/\n+$/, "")}\n`);
  if (breaking.length > 0) {
    parts.push(`${LIFTED_BREAKING_HEADING}\n\n${breaking.join("\n\n")}\n`);
  }
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

// pathToFileURL of the realpath, as check-no-comments.mjs does: comparing against
// `file://${process.argv[1]}` mismatches whenever the checkout path holds a space
// or another character that percent-encodes, and then main() never runs and the
// script exits 0 without writing or checking anything.
if (process.argv[1] && import.meta.url === pathToFileURL(realpathSync(process.argv[1])).href) {
  main();
}
