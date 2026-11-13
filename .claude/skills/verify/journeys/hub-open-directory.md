# Host-named hub open directory placement

Use shared setup from `SKILL.md`, then rebuild both hubs and run the public
stdio differential without the Go test cache.

The Python journey needs POSIX pipes and process groups. Its Go wrapper skips
on Windows or when `python3` is absent from PATH, before building or starting a
hub. Check the missing-Python skip with the isolated subprocess regression;
then run the journey normally to ensure the supported surface still executes.

```sh
go test ./go/cmd/goap -run '^TestHubOpenDirectorySkipsWithoutPython$' -count=1 -v
zig build --build-file zig/build.zig -Doptimize=ReleaseSafe -j4
OAP_OAPX_BIN="$PWD/zig/zig-out/bin/oxap" go test ./go/cmd/goap -run '^TestHubOpenDirectory$' -count=1 -v
go test ./go/serve/servehttp -run '^TestAReopenDirectoryConflictIsABadRequest$' -count=1 -v
zig build --build-file zig/build.zig test-unit-hub -Doptimize=ReleaseSafe -j4
```

For retained wire, native-request and history artifacts, build `goap`, then
run the same script once with each binary and a fresh output directory.

```sh
go build -o /tmp/hub-directory-goap ./go/cmd/goap
python3 .claude/skills/verify/scripts/hub-open-directory.py --binary /tmp/hub-directory-goap --output-dir /tmp/hub-directory-go-<unique-name>
python3 .claude/skills/verify/scripts/hub-open-directory.py --binary zig/zig-out/bin/oxap --output-dir /tmp/hub-directory-zig-<unique-name>
```

Require PASS from both. The script observes host-named sessions in separate
workspaces, a compound open carrying its first message, a subscribing open,
and ordinary opens without a directory election. The synthetic Codex peers
also exercise an adapter without a configured working directory while the
hub runs from another directory. They
record their real cwd and the native thread/start cwd. Both must match the
selected workspace. thread/resume must keep the recorded native id and cwd
after close and after a fresh hub process starts with a different default.

Empty, relative, missing and file paths, fixed-adapter elections and conflicting
reopen directories must be refused. Refusals must leave no sessions. A directory
on another op is refused by presence, including null. A non-string directory
is a fatal framing error. History must retain the original selected directory.
Turning off any_directory after restart must refuse placement rather than use
the new default. The HTTP check requires the same invalid_request refusal when
a stored session's directory can no longer be placed by its fixed adapter.

All replies are synthetic. HOME/data/config and workspaces are temporary.
The script reaps its process groups, uses stdio and retains wire logs, stderr,
native requests and history under the requested output directory. Run the full
baseline in `SKILL.md` too.
