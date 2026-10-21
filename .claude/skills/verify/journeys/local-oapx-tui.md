## Local oapx/TUI journey (PTY, no real credentials)

Run the bundled script, supplying a new artifact directory outside the worktree:

```sh
python3 .claude/skills/verify/scripts/mac-smoke.py --output-dir /tmp/oap-verify-smoke-<unique-name>
```

It copies the built binary into a temporary directory, clears the child
environment, supplies a fresh HOME and unique `OAPX_KEYCHAIN_SERVICE`, and
uses macOS `sandbox-exec` to deny network access, Security Mach services, and
reads under the real HOME. It reuses the existing PTY driver's terminal decoder
and bounded waits. It checks `oapx --help`, then observes welcome -> typed prompt
-> fixture-provider reply -> `/quit` with exit 0, saves help/transcript/screen
artifacts, kills/reaps the child on failure, and deletes temporary state.
Inspect `screen.txt` and the transcript for the actual rendered reply. The journey
also asserts real cursor coordinates and caret-column escapes for ASCII, CJK
wide characters and wrapped CJK input, then moves left over CJK, widens the PTY,
checks the new caret, types CJK in Zen mode, and checks cursor restoration on exit. `caret-*.txt` and
`resized.txt` retain the visible rows and real cursor coordinates. Run vendored
renderer escape/redraw/reflow tests with:

```sh
zig build --build-file zig/vendor/zigzag/build.zig test
```

A PTY cannot generate a terminal emulator's IME preedit. Real IME behaviour is
**unverified** until a human checks it in their own terminal:

1. Start the rebuilt `oapx`, enable a Chinese IME, and type a syllable without
   committing it; preedit should appear at the composer caret without scrolling.
2. Commit characters, fill/wrap an input row, move the caret left, and compose
   again; the frame should stay still and preedit should follow the caret.
3. Resize while a draft is present, compose and commit again, then `/quit`;
   check that the prompt and terminal cursor are restored correctly.

Do not remove the sandbox if it fails. HOME alone does not isolate macOS
Keychain; a unique Keychain service alone still permits file fallback and
`loadDefault` can import the fixed `Codex Auth` service. The general
`scripts/tui-pty-driver.py` rejects macOS for this reason; keep its guard intact.
Do not use login/logout, `/login`, `/model`, provider discovery or real SDK auth
smokes as substitutes. Live providers and real-binary SDK credential persistence
remain unverified on this Mac; the latter belongs on Linux CI.
