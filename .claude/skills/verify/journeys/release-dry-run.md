# Journey: a release dry run packs every channel and publishes nothing

`release-binaries.yml` run by hand builds, signs, packs and smoke-tests a
release exactly as a tag does, and its publish jobs are skipped. Its
`release-bundle` artifact is what a human publishes a package's first version
from, so check it before anyone does.

Start the run on the branch or commit to check, then wait for it to finish:

```sh
gh workflow run release-binaries.yml --repo lsm/oxap --ref <branch>
gh run list --repo lsm/oxap --workflow release-binaries.yml --limit 1
bash .claude/skills/verify/scripts/release-bundle.sh <run id>; echo "exit=$?"
```

The script downloads the bundle, checks every file against its
`SHA256SUMS.txt`, and counts what is in it: nine npm tarballs (six
`@oxap/cli-<target>`, `@oxap/cli`, `@oxap/sdk`, `@oxap/client`), the publishable
`.crate` files, the wheel and sdist, and six `oxap` archives. It ends with `ok`
and exits 0. The two counts before it are the `npm publish` and
`cargo publish` lines in `PUBLISH-BY-HAND.md`.

In the run itself, `bundle-smoke` must be green: it installs the packed npm
packages and wheel and runs `oxap` and its `oapx` alias from the Linux archive.
`release`, `publish-npm`, `publish-crates`, `publish-pypi` and `smoke` must show
as skipped. A publish job that ran on a hand-started run is a failure of this
journey, whatever it printed.
