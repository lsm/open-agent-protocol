# Host-named hub open directory placement

Use shared setup from `SKILL.md`, then rebuild both hubs and run the public
stdio differential without the Go test cache.

```sh
zig build --build-file zig/build.zig -Doptimize=ReleaseSafe -j4
OAP_OAPX_BIN="$PWD/zig/zig-out/bin/oapx" go test ./go/cmd/goap -run '^TestHubOpenDirectory$' -count=1 -v
zig build --build-file zig/build.zig test-unit-hub -Doptimize=ReleaseSafe -j4
```

For retained wire, native-request and history artifacts, build `goap`, then
run the same script once with each binary and a fresh output directory.

```sh
go build -o /tmp/hub-directory-goap ./go/cmd/goap
python3 .claude/skills/verify/scripts/hub-open-directory.py --binary /tmp/hub-directory-goap --output-dir /tmp/hub-directory-go-<unique-name>
python3 .claude/skills/verify/scripts/hub-open-directory.py --binary zig/zig-out/bin/oapx --output-dir /tmp/hub-directory-zig-<unique-name>
```

Require PASS from both. The script observes host-named sessions in separate
workspaces, a compound open carrying its first message, a subscribing open,
and ordinary opens without a directory election. The synthetic Codex peers
record their real cwd and the native thread/start cwd. Both must match the
selected workspace. thread/resume must keep the recorded native id and cwd
after close and after a fresh hub process starts with a different default.

Empty, relative, missing and file paths, fixed-adapter elections and conflicting
reopen directories must be refused. Refusals must leave no sessions. A directory
on another op is refused by presence, including null. A non-string directory
is a fatal framing error. History must retain the original selected directory.
Turning off any_directory after restart must refuse placement rather than use
the new default.

All replies are synthetic. HOME/data/config and workspaces are temporary.
The script reaps its process groups, uses stdio and retains wire logs, stderr,
native requests and history under the requested output directory. Run the full
baseline in `SKILL.md` too.
