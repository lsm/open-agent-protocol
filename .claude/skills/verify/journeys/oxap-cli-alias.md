# Journey: the oxap binary and its oapx alias

Decision 0067 renames the product binary to `oxap` and keeps `oapx` as a
deprecated alias of the same artifact: identical arguments, exit codes and
stdout, with the deprecation note on stderr only. Both land in
`zig/zig-out/bin` from a plain `zig build`, so no `install --prefix` is
needed. Run from the repository root after `zig build --build-file
zig/build.zig -Doptimize=ReleaseSafe`.

## Both names install and print the release name

```sh
test -x zig/zig-out/bin/oxap
test -x zig/zig-out/bin/oapx
zig/zig-out/bin/oxap --version
zig/zig-out/bin/oapx --version
```

`oxap --version` prints `oxap-<version>-<os>-<arch>`, the same name the
GitHub Release archives carry, with the version from `zig/build.zig.zon`
(or `-Dversion`). `oapx --version` prints the identical line to stdout and
its deprecation note to stderr, never to stdout.

## The alias matches on stdout and exit code

```sh
for args in "--version" "--help" "serve ghost"; do
    zig/zig-out/bin/oxap $args </dev/null >/tmp/oxap.out 2>/tmp/oxap.err; oxap_rc=$?
    zig/zig-out/bin/oapx $args </dev/null >/tmp/oapx.out 2>/tmp/oapx.err; oapx_rc=$?
    test "$oxap_rc" = "$oapx_rc" || echo "exit codes differ for $args"
    cmp -s /tmp/oxap.out /tmp/oapx.out || echo "stdout differs for $args"
done
grep deprecated /tmp/oapx.err
! grep -q deprecated /tmp/oxap.err
```

Every leg must print nothing. `serve ghost` exits non-zero under both
names; its refusal and usage go to stderr, so its stdout is empty in both
and equal. `grep deprecated` on the alias's stderr must print the note;
the oxap leg carries none.

## The alias keeps working as a host surface

```sh
zig/zig-out/bin/oapx validate fixtures/valid/core-completed.json
zig/zig-out/bin/oapx validate fixtures/schema-invalid/missing-envelope-id.json; echo "exit=$?"
```

The valid trace exits 0, the schema-invalid trace exits 1 naming
`schema_invalid`, each with the deprecation note on stderr. Everything in
the repository that locates the built binary prefers `zig/zig-out/bin/oxap`;
the alias exists for outside scripts that still invoke `oapx`.
