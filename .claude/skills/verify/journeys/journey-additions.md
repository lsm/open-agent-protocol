## Concurrent journey additions

After changing journey discovery or the skill layout, run from the repository root:

```sh
python3 .claude/skills/verify/scripts/journey-merges.py --output-dir /tmp/oap-journey-merges-<unique-name>
```

The script copies the current skill and journey files into a temporary Git
repository with isolated HOME and Git configuration. Two synthetic branches
independently add a journey using the current layout. It runs `git merge-tree
--write-tree` in both orders; both must exit 0, produce the same tree, retain
every original and added journey, and leave `SKILL.md` unchanged. It retains
the merge output, then removes the temporary repository and reaps its children.
The former layout, with both additions appended to `SKILL.md`, conflicts in
both orders and exits 1.

Check that Python optimization cannot disable these checks:

```sh
python3 .claude/skills/verify/scripts/journey-merges-mutations.py --output-dir /tmp/oap-journey-mutations-<unique-name>
```

The script supplies real Git merge trees with one journey removed, shared skill
content changed, or differing file modes between merge orders. The proof runs
under `python -O` and must reject each corruption with its corresponding failure;
an unrelated process or Git error does not count. Logs are retained, and temporary
Git wrappers and repositories are removed.
