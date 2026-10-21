# Rust open-time session settings

From the repository root, run:

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test open_settings
python3 .claude/skills/verify/scripts/rust-open-settings.py --output-dir /tmp/oap-open-settings-<unique>
```

The helper builds Go's public validator, Rust's public `validate` example and
ReleaseSafe `oapx`, then replays synthetic opens, their repairs and controls.
Inspect `results.json` and retained inputs/stdout/stderr: exact code sets,
semantic phases, exits and diagnostic indices must agree across all three.
Repeated diagnostics on one response remain observable. Direct `oapx validate`
avoids Zig fixture-test cache reuse. No endpoint or real data is read.

Supplying reasoning or a compaction policy requires affirmative disclosure of
that feature's `session_open` mode. An undisclosed key, missing mode or live-only
mode must draw `unavailable_capability` at an improperly admitted/refused open.
A degraded feature needs its own key in the opt-in, or draws
`degraded_without_optin`. A conforming refusal names the feature, with the
unadvertised reason where required. Open-level refusals remain valid.

Refusal attribution includes every pending setting alongside other open
controls. A refusal satisfying any real defect discharges the open. A wrong
refusal has one owner under existing rank rules. A conforming settings refusal
must not invent a provided-tool limit violation. Gate defects suppress value
fidelity checks on that response.

When both requested and reported values are known, a changed reasoning level,
policy kind, share or token count draws `session_state_mismatch` on that open
response. Exact token values above 2^53 remain distinct. Omitted reports mean
unknown; unrequested settings do not elect a feature. Reversed replies retain
their own requested values. Matching native/emulated policies, explicit opt-in,
correct refusals and repaired traces remain valid.

With no concurrent edit or build in this worktree, run:

```sh
python3 .claude/skills/verify/scripts/rust-open-settings.py --mutation-check --output-dir /tmp/oap-open-settings-mutations-<unique>
```

Every mutation must compile and produce its exact changed public result,
including diagnostic locations; compiler failures are not catches. Source is
restored in `finally`, Rust rebuilt, then the complete corpus replayed through
all three validators after restoration. Inspect `mutations.json`; check
restoration if the helper is forcibly killed.

Integrate current main before final Go validation, Rust manifest and Zig
`test-unit-validation` gates and public replay, with zero unexpected
disagreements. Run the complete Verify baseline and observe its isolated PTY
fixture reply and `/quit` exit 0, temporary state removal and child cleanup.

This ports only `0045#where-the-settings-go/1–2`'s `session.open` arm and
`0045#what-the-state-reports/1–2`. Live-update rows and all deferred
snapshot/admission/lead/active/model-capture lanes stay outside this slice.
Credentials, authentication, live data, provider calls and real GUI tests are
not exercised.
