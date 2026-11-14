# Journey: the compatibility gate on a pull request

`compatibility.yml` runs `go/cmd/apidiffcheck` from the base tip, comparing
the pull request's head commit against its merge base, and fails unless the
description's `## Breaking changes` section names every package that breaks.
Each side's packages load under the module path its own `go.mod` declares,
so a pull request that renames the module still gets a verdict.

Run from the repository root, with the pull request's head fetched and its
description saved to a file:

```sh
git fetch origin pull/<N>/head:refs/remotes/origin/pr-<N>
gh pr view <N> --json body --jq .body > /tmp/pr-<N>-body.md
bash .claude/skills/verify/scripts/compatibility.sh . origin/pr-<N> origin/main /tmp/pr-<N>-body.md; echo "exit=$?"
```

The first argument is the checker's tree: `.` tests the checker on this branch,
and a checkout of `origin/main` reproduces what CI runs today. The script
prints the head and merge base it compared, then the checker's verdict:
`PASS compatibility: ...` with exit 0, or the packages still missing from the
section with exit 1. An `export data for the base of ...` error means a side
did not load, which is not a verdict.
