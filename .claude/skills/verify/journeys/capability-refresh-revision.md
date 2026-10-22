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

In `capability-refresh-late-response-after-refresh`, the old response arrives
at index 6 after the matching refresh at 5. Only index 6 is stale; the following
request and response citing the active revision at 7 and 8 pass. An old response
must never roll the active descriptor back, even after the window closes.

The descriptor-member pack's `region-on-stale-descriptor` case must still
report `unavailable_capability` for the packed member at index 4, alongside
`stale_capability_revision` at indices 4 and 5. Go and Zig compare this case
with the pack explicitly loaded in tolerant mode. Zig's existing strict CLI
does not widen core payload schemas for packed members and stops at
`schema_invalid` before these semantic checks; that limitation is unchanged.
Go's strict manifest suite also pins the pack case. Rust's existing lack of
pack loading is unchanged. Together the helper checks 12 core judgments across all three trees
and two pack judgments across Go and Zig, with zero disagreements.

Also observe acceptance of the permission and user-input traces that settle
under the previous revision while the window remains open. Require zero
validator disagreements. This journey checks wire validation only; it does not
make a provider call or exercise a live endpoint's refresh behavior.
