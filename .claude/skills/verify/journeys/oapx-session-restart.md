## Served oapx session restart journey

Build ReleaseSafe with the shared setup in `SKILL.md`, then run:

```sh
python3 .claude/skills/verify/scripts/oapx-session-restart.py --output-dir /tmp/oap-session-restart-<unique-name>
python3 .claude/skills/verify/scripts/oapx-session-restart.py --storage-recovery --output-dir /tmp/oap-session-storage-recovery-<unique-name>
python3 .claude/skills/verify/scripts/oapx-session-restart.py --partial-write-recovery --output-dir /tmp/oap-session-partial-recovery-<unique-name>
```

First it checks that `--no-save-oapx-sessions` omits reopen support and writes
no transcript after a completed synthetic turn. Then it starts the real `oapx serve --stdio` with an oapx adapter and default
session saving, submits a synthetic prompt to a loopback HTTP provider, and
observes both prompt and reply through `work.read`. It exits the hub, starts
a fresh process with the same temporary HOME, reads the stored turns before
opening, reopens the session, and submits a second prompt. The provider's
captured request must contain the original prompt, original reply, and new
prompt, proving that the restored loop uses its history.

All provider replies are synthetic. The provider binds an OS-assigned free
loopback port. The child environment is cleared, HOME/data and working directory
are temporary, and on macOS the sandbox denies reads under the real HOME,
Security Mach services and outbound network except loopback. Do not remove the
sandbox if it fails. Children are stopped and temporary state removed on exit.
Require exit 0 and `PASS`; inspect `opt-out.jsonl`, `before.jsonl`, `after.jsonl`, `restored.json`
and `provider-requests.json`. No credential file may be written. Live providers
and recovery of an interrupted active run are not exercised by this journey.

The storage-recovery invocation first obstructs the synthetic session's
transcript with a directory, completes a turn, removes the obstruction, and
submits another turn. A `work.read` barrier drains saving before removing the
obstruction. Require the recovered transcript to include the selected
model and provider metadata in `storage-recovered.jsonl`, then require the same
restart and restored-request checks. A failed batch must allow later saves to
write their metadata again.

The partial-write invocation limits only the child Hub's regular files to
128 KiB and ignores its file-size signal. A large synthetic reply must exceed
that limit. The next small turn must still persist, survive restart and enter
the next provider request. Inspect `partial-before.jsonl` alongside the normal
restart artifacts, captured after a `work.read` barrier. A fresh helper process
sets the file-size limit and execs the Hub. A failed append must truncate back to its previous length
before a later batch appends. The provider emits its large reply only for the
special current prompt.

After changing the served-session writer, also run:

```sh
zig build --build-file zig/build.zig test-unit-makai-cli -Doptimize=ReleaseSafe -j4
zig build --build-file zig/build.zig test-unit-tui -Doptimize=ReleaseSafe -j4
```

The storage test holds the writer while another thread enqueues more events
than its bounded queue can hold. It then reads all 80 user/reply pairs in
order, checks a live read barrier, and reopens the store after shutdown to
observe the last queued conversation. Capture allocation failures must release
every owned metadata field and event payload. The real-process journey above
remains the product proof for read-back, shutdown and reopening.
The store tests also reopen a batched conversation after compaction, check its
offset and latest index, and exercise batch allocation failures.
