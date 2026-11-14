# Cutting a release

A release is cut by pushing a `v*` tag: `.github/workflows/release-binaries.yml`
builds the six `oxap` binaries, signs and notarizes the macOS ones, packs every
channel, smoke-tests the packed files, and only then publishes: the GitHub
Release, npm (the six `@oxap/cli-<target>` packages, then `@oxap/cli`,
`@oxap/sdk` and `@oxap/client`), crates.io (every crate whose manifest is
publishable, dependencies first) and PyPI (`oxap-sdk`). Every registry is
reached through OIDC trusted publishing; the workflow holds no registry token.
After publishing, `release-smoke.yml` installs each channel from its registry.

Nothing in it writes `CHANGELOG.md`, because a pull request does not either —
**the release notes for a version are generated and committed before the tag is
pushed**, by `scripts/changelog-release.mjs`. A tag with no section fails the
`changelog` job, and every publish job needs it.

`scripts/release-channels.mjs` is the one list of what ships. `versions`
refuses a tag that the root and client `package.json`, `pyproject.toml`,
`build.zig.zon` and each publishable crate do not all carry; `crates` prints the
crates in publish order; `registry --version V` says which packages each
registry already holds.

## A dry run

Run the workflow by hand on the commit you mean to tag:

```sh
gh workflow run release-binaries.yml --repo lsm/oxap --ref main
```

It builds, signs, packs and smoke-tests exactly as a tag does and publishes
nothing, whatever ref it is started on: only a tag push publishes. Its `release-bundle` artifact holds the npm tarballs, the `.crate`
files, the wheel and sdist, the six archives, one `SHA256SUMS.txt` over all of
them, and `PUBLISH-BY-HAND.md`, which the run's summary also shows.

## The first release of a package

npm and crates.io only accept a trusted publisher for a package that already
exists, so each new package's first version is published by hand from a dry
run's bundle. `PUBLISH-BY-HAND.md` is the ordered list of commands for that, in
one sitting: npm, then crates.io, then the trusted publishers on all three
registries (PyPI takes a pending publisher, so `oxap-sdk` needs no hand
upload), then the tag. It sets `RELEASE_BOOTSTRAP=true` before tagging: with
it, the npm and crates jobs only check that the hand-published versions exist,
and fail if one is missing. Delete the variable once the tag run is green.

## A run that fails part-way

Each publish job publishes only what its registry lacks, so **Re-run failed
jobs** on the same tag run fills in the missing channels without touching what
is already out. Use that, not **Re-run all jobs**: a full re-run rebuilds and
re-signs every binary, and although the GitHub Release keeps the assets it
already has, the rebuilt files in that run's bundle no longer match them. Never
move a tag or bump the version to recover.

## Before the first tag

The npm, crates and PyPI jobs run in the `release` environment, and each
registry's trusted publisher names it. Create it by hand, with required
reviewers, before the first tag: a job that names an environment the
repository lacks creates it on the spot, unprotected.

## The steps

Do these on `main`, in order. The boundary in step 1 is the **exact commit time
of the last release tag**, which is where the notes start.

**1. Collect the pull requests merged since the last tag.**

```sh
gh api repos/lsm/oxap/commits/v0.1.0-alpha.4 \
  --jq .commit.committer.date
# 2026-09-27T13:02:38Z

gh pr list --repo lsm/oxap --state merged \
  --search "merged:>2026-09-27T13:02:38Z" \
  --json number,title,body,mergedAt,url > prs.json
```

**Use the full timestamp, not a date.** A calendar day is not a boundary: this
release merges its changelog pull request and tags on the same day, so a bare
`merged:>=2026-09-27` would re-collect everything merged earlier that day,
including the pull requests the section you are about to write already listed.
Use `>` and the tag's commit time to the second.

**The changelog pull request from step 3 will be collected by the next release,
and that is correct.** It is a real change to the repository. Do not try to
exclude it, and do not copy this section's entries forward by hand — the next
release re-collects everything since the tag you are about to push, which is the
point.

**2. Write the section.**

```sh
node scripts/changelog-release.mjs --version 0.1.0-alpha.5 --date 2026-09-29 \
  --input prs.json --write
```

`--version` is the version you are about to tag, without the `v`. `--date` is
the release date. The script carries `## Unreleased` forward into the new
section, so nothing written by hand is lost, and leaves `## Unreleased` empty on
top for the next cycle. The version must sort **above** the newest section in the
file, or the script refuses — which is the check that catches a tag line that has
drifted behind the changelog.

Two things it refuses, both worth knowing before you tag:

- a version **older** than the newest section already in the file, which is what
  happens if the tag line and the changelog have drifted apart;
- a version that already has a section, so the same release cannot be cut twice.

### Resolved: the tag line and the changelog now agree

**The alpha line continues, and the two untagged sections are gone.** This was
the owner's call (2026-09-29), and it was the second of the two ways out; the
first — tag forward to `v0.3.0` and retire `v0.1.0-alpha.N` as a naming
accident — was not taken.

What it cost, so nobody has to re-derive it: the tags ran
`v0.1.0-alpha.1` through `v0.1.0-alpha.4` (2026-09-27) while the changelog's
newest released sections were `## [0.2.0] - 2026-09-11` and
`## [0.1.0] - 2026-09-05`, **neither of which was ever tagged**. There is no
`v0.1.0` and no `v0.2.0` in the repository, so `[0.2.0]` described no release
that anyone could install, and `v0.1.0-alpha.5` was a version the script
**refused** to cut because it sorts below `[0.2.0]`.

Those two sections are now deleted. They were the most complete release notes in
the file, and deleting them is the price of the decision: keeping them would mean
either tagging `v0.2.0` after four alphas already shipped past it, or leaving
`[0.2.0]` above every version that exists, which is the disagreement itself.

**What is actually lost: the prose, permanently.** The 37 pull requests those
sections cite are `#184` through `#243`, all merged 2026-09-05 to 2026-09-11 —
before `v0.1.0-alpha.4` (2026-09-27). The next `--write` collects everything
merged *since the last real tag*, so **none of those 37 is in the list it will
build**; checked, zero of the 37 appear in the 142 pull requests a
`0.1.0-alpha.5` release actually collects. The re-collection is not a recovery
path for this content. What survives is the work itself, on `main` and in the
history — and the notes verbatim, in the last real tag:

```sh
git show v0.1.0-alpha.4:CHANGELOG.md
```

That tag predates the deletion, so the `[0.2.0]` and `[0.1.0]` sections are
still in its copy of the file — byte-identical to the text this section removed,
verified by diffing the two. It is a good pointer to leave behind because it
cannot rot: a tag is not rewritten by a squash, and reading a file *at* a tag
needs no history walk, so it works in a shallow clone and in any future state of
this branch. A `git log -S` search for the heading would be the obvious
alternative and is the wrong tool here — the string's count changed in three
commits, not one, and which of them a given clone can see depends on its depth.

If those notes are ever wanted back into the file, that is where they are — a
deliberate revert of one commit, not a regeneration.

Every package carries the release version, and the `versions` job refuses a
run where they disagree: the root and `clients/ts` `package.json`,
`sdk/python/pyproject.toml` in its PEP 440 spelling (`0.1.0a33` for
`0.1.0-alpha.33`), `zig/build.zig.zon` (the default for `-Dversion`, so what a
local `oxap --version` prints) and every publishable crate. A dispatch builds
and names its artifacts under that version, and a tag must equal it, so bump
them all in one pull request before tagging. A version containing `-`
publishes to npm under the `next` dist-tag, `@oxap/client` included.

**3. Commit the changelog through a pull request.** This is an ordinary change to
`CHANGELOG.md` on a branch, merged the usual way. It has to be on `main` *before*
the tag: the release job checks out the tagged tree and reads the file from
there.

**4. Tag.**

```sh
git tag v0.1.0-alpha.5 && git push origin v0.1.0-alpha.5
```

To check step 2 landed before you tag:

```sh
node scripts/changelog-release.mjs --version 0.1.0-alpha.5 --check
```

That is the same check the release workflow runs, and it is what holds back
every publish job when the section is missing.

## The generated list

The script writes the pull requests under a `### Merged pull requests` heading
rather than under Keep a Changelog's type headings — `### Added`, `### Changed`,
`### Fixed` and so on. It does not categorise them because the repository has no
labels on its pull requests to categorise by, and a guessed category is worse
than an honest one.

**That means the list is meant to be re-sorted by hand.** Move the entries that
belong under `### Added` or `### Fixed` as you see fit, keeping the
`## [version] - date` heading and the pull request links. The script's only hard
requirement is that the section exists and carries the version; what sits under
which subheading is yours. Entries that were hand-written under a type heading
come along in the carried-forward block and stay where they are.

### Breaking changes come out on their own

A pull request that changed a public Go package incompatibly carries a
`## Breaking changes` section in its description — that is where `apidiffcheck`
reads the record from, and what a release note needs to say. The script lifts
each one into the release under a `### Breaking changes` heading of its own,
above the generated list, so the breaks are not buried in it. The release form
is level three on purpose: a `##` heading inside the `## [version]` section
would end that section early, so the level-two spelling is what a pull request
description must contain and the level-three one is what the notes carry.

Both spellings are case sensitive, so `## breaking changes` is not recognised.
Write it exactly.

## What the workflow does with all this

| job | needs | what it does |
| --- | --- | --- |
| `changelog` | — | on a tag, asserts `CHANGELOG.md` carries a section for it |
| `package-npm` | `versions`, `build-binaries`, `changelog` | packs the nine npm tarballs |
| `bundle-smoke` | `versions`, `bundle` | installs and runs the packed release |
| `release` | `checksums`, `changelog`, `bundle-smoke` | publishes the GitHub Release |
| `publish-npm`, `publish-crates`, `publish-pypi` | `versions`, `changelog`, `bundle-smoke` | publish to their registries |

Every publish job names `changelog` in its `needs` itself rather than through
another job, so a tag with no notes reaches no registry and no GitHub Release.
