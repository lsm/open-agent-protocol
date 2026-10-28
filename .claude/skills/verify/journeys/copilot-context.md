## Copilot context tiers in the TUI

Use the shared macOS build and credential isolation setup from `SKILL.md`.
After building ReleaseSafe, run:

```sh
python3 .claude/skills/verify/scripts/copilot-context.py --output-dir /tmp/oap-copilot-context-<unique-name>
```

For a release archive, pass `--binary /absolute/path/to/extracted/oapx` to
exercise the shipped binary with the same isolation and assertions.

The real TUI loads a synthetic Copilot catalog with a default 200K prompt tier
and an 872K long tier under the same Claude model id. It reports 200K, accepts
`/context 872k`, refuses 900K without changing the persisted selection, restores
200K with `/context default`, and accepts an explicit 200K selection. Require
exit 0 and `PASS`; inspect the `screen-*.txt` terminal frames and transcript.

The synthetic numbers exercise metadata parsing, not a claim about a user's
Copilot subscription. The child has a temporary HOME, synthetic login placeholder
and cached model listing. Its environment is cleared and macOS isolation denies
network, Security services and reads under the real HOME. No real credential is
read; no provider request or login occurs. Do not remove isolation if it fails.
The child is stopped and temporary HOME removed even on failure. Live service
acceptance, actual long-prompt billing, and the user's account limits remain
unverified.

Run `zig build --build-file zig/build.zig test-unit-tui -Doptimize=ReleaseSafe`
for field-version, per-model limit and cloning regression checks as well.
