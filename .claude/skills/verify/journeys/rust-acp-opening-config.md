# Rust ACP confirmed opening configuration and reasoning

Follow shared setup and the mkdir mutex in SKILL.md. Run from the repository
root after the baseline builds ReleaseSafe `oapx`:

```sh
python3 .claude/skills/verify/scripts/rust-acp-opening-config.py --output-dir "$EVIDENCE/opening-config"
python3 .claude/skills/verify/scripts/rust-acp-opening-config-ownership.py --output-dir "$EVIDENCE/opening-config-ownership"
(cd rust && cargo test -p oap-adapter-acp --test opening_config)
```

The normal public Adapter/Session consumer opens a synthetic native ACP process
with an empty environment and a one-entry inbound queue. It applies the private
Config.session_config map in sorted order, confirms every returned option, and
checks the whole required set before and after the portable reasoning request.
Unadvertised values, ambiguous/non-select configured options and missing,
rejected, reset or unconfirmed settings must refuse open and reap the process.
The reasoning value or label is matched before writing; the final snapshot must
confirm the chosen id/value. Explicit requested reasoning remains in returned
state even when the confirmed snapshot omits its category. Fresh unconfigured
reasoning stays unspecified; concrete model ids still follow the model option.
Configured/reopened metadata is derived from returned options.

Twenty-eight Go/Rust cases compare successful state/catalog snapshots and
structured refusal metadata, normalizing only clocks and the staged revision.
Full error/parser wording is retained separately. Parsed native frames in both
directions must match exactly, including ordered setting ids/values. The agent
sends 320 notifications and an unknown reverse RPC before every setting reply,
proving the owned opening collector drains before Session dispatch starts.
All successful wrapped Rust exchanges must pass Go, Rust and Zig validators.

Four ownership cases cover quiet new/reloaded settings and actual partial
writes of a 2 MiB request or reverse reply. Retain 64 native bytes, PIDs,
phase/timing data and OS thread inventories. After that native boundary, a
waiting poll must remain below 50 ms; dropping the future must reap the child
and return consumer threads to baseline before allowing consumer exit. Large
snapshot preparation is measured separately and can exceed 50 ms; this journey
does not claim that CPU preparation bound. Retain the first failing setup-time
measurement and the corrected waiting-boundary evidence.

Run existing reload/deadline ownership, live-config, model-switch, session,
submit, permission and cancellation journeys, the full baseline and Rust
all-features under the same mutex. Read the baseline's actual PTY reply and
`/quit` exit. The current revision is `-rust-attachments`; session.reasoning is
emulated in session_open mode. Full byte corpus and C8 served parity remain
later gates of #1439. No live provider/harness, credential or port is used by
the new journeys. Shared process-helper grandchild-held-pipe limits apply.
