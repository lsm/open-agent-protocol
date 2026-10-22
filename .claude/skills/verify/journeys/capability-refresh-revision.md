# Only the announced descriptor completes a capability refresh

From the repository root, run:

```sh
python3 .claude/skills/verify/scripts/capability-refresh-revision.py --output-dir /tmp/oap-capability-refresh-<unique>
```

The helper builds and invokes the Go, Rust and Zig validator CLIs. It retains
stdout, stderr and a `results.json` summary, checking exact diagnostic positions
rather than only code sets: a mismatched descriptor is already stale, which can
hide a missing judgment on the next request in a code-set-only test.

In `capability-refresh-requires-announced-revision`, observe
`stale_capability_revision` at the delayed, unannounced and superseded responses
(indices 4, 13 and 18) and the following ordinary requests without a revision
(indices 5, 14 and 19). Matching responses at 8 and 22 close the window, so the
following requests at 9 and 23 pass. A second update supersedes the first
announced revision. The fixture cites Decision 0064's existing register row.

Also observe acceptance of the permission and user-input traces that settle
under the previous revision while the window remains open. Require zero
validator disagreements. This journey checks wire validation only; it does not
make a provider call or exercise a live endpoint's refresh behavior.
