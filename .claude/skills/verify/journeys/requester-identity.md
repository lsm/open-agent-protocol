# Control-owned tool requester identity

Run the public validator consumer from the repository root:

```sh
python3 .claude/skills/verify/scripts/requester-identity.py --output-dir /tmp/oap-requester-identity-check
```

It builds and calls all three validator CLIs with synthetic traces. A declared
agent requester passes; consistently using the control participant or an
undeclared requester fails with `scope_mismatch`. Renaming the initialized
endpoint invalidates the old requester and renaming both repairs the trace.
A prefix without initialization retains its existing `unmatched_interaction`
outcome without inventing an endpoint identity. Go and Rust point
the error at `/payload/requested_by`. The script retains each exact input,
stdout, stderr, exit code and diagnostic comparison in its output directory.

The shared negative fixtures are
`fixtures/semantic-invalid/control-call-requester-is-control.json` and
`fixtures/semantic-invalid/control-call-requester-is-undeclared.json`. Run the Go
validation suite, Rust manifest test and Zig `test-unit-validation` alongside
this journey; they must report no unexpected disagreements or stale gaps.
