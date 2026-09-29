# Cutting a release

A release is cut by pushing a `v*` tag: `.github/workflows/release-binaries.yml`
builds every release target, signs and notarizes the macOS ones, publishes a
GitHub Release and the `@oap-sdk` npm packages. Nothing in it writes
`CHANGELOG.md`, because a pull request does not either — **the release notes for
a version are generated and committed before the tag is pushed**, by
`scripts/changelog-release.mjs`.

The ordering matters and is not recoverable afterwards: a tag with no section
fails the `release` and `publish-npm` jobs, so the version never reaches npm and
no GitHub Release is published. Those jobs run *after* the binaries are built, so
you find out at the end of a long run rather than at the start.

## The steps

Do these on `main`, in order. The boundary in step 1 is the **exact commit time
of the last release tag**, which is where the notes start.

**1. Collect the pull requests merged since the last tag.**

```sh
gh api repos/lsm/open-agent-protocol/commits/v0.1.0-alpha.4 \
  --jq .commit.committer.date
# 2026-09-27T13:02:38Z

gh pr list --repo lsm/open-agent-protocol --state merged \
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
node scripts/changelog-release.mjs --version 0.3.0 --date 2026-09-29 \
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

**3. Commit the changelog through a pull request.** This is an ordinary change to
`CHANGELOG.md` on a branch, merged the usual way. It has to be on `main` *before*
the tag: the release job checks out the tagged tree and reads the file from
there.

**4. Tag.**

```sh
git tag v0.3.0 && git push origin v0.3.0
```

To check step 2 landed before you tag:

```sh
node scripts/changelog-release.mjs --version 0.3.0 --check
```

That is the same check the release workflow runs, and it is what fails the
`release` and `publish-npm` jobs when the section is missing.

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

## What the workflow does with all this

| job | needs | what it does with the changelog |
| --- | --- | --- |
| `changelog` | — | asserts `CHANGELOG.md` carries a section for `$GITHUB_REF_NAME` |
| `release` | `checksums`, `changelog` | publishes the GitHub Release |
| `package-npm` | `build-binaries`, `changelog` | builds the npm packages |
| `publish-npm` | `package-npm`, `changelog` | publishes to npm |

`changelog` is its own job and all three publish paths name it in `needs`,
because the two publish paths are otherwise independent — `release` needs
`checksums` and `publish-npm` needs `package-npm`, and neither waits for the
other. Without that, a tag with no notes would fail the GitHub Release while the
npm packages published anyway, which is the irreversible half.
