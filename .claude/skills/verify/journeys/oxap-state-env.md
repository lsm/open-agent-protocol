# Journey: OXAP state directories and environment names

Decision 0067 moves state to `~/.oxap` and a workspace's `.oxap/`. A home or
workspace that holds only the old `.oapx` directory keeps using it, read and
written in place; nothing is moved or deleted, and when both exist `.oxap`
wins. Every `OXAP_<name>` variable is read before `OAPX_<name>`, which still
works on its own. The Keychain service stays `ai.hyperneo.oap`.

Run on the designated test host, from the repository root, after
`zig build --build-file zig/build.zig -Doptimize=ReleaseSafe`:

```sh
python3 .claude/skills/verify/scripts/oxap-state-env.py zig/zig-out/bin/oxap; echo "exit=$?"
python3 .claude/skills/verify/scripts/shell-job-wake.py zig/zig-out/bin/oxap; echo "exit=$?"
```

The first script runs one turn through `oxap serve --stdio` against a local
scripted model, with `HOME` and both keychain service names pointed at
throwaway values and no provider key. Each line names the case, how the run
settled, which state directory's provider config the request came through,
what the home holds afterwards, and which payload dump was written:

- `new home`: config only under `.oxap`; the request goes through it and no
  `.oapx` appears.
- `old home`: config only under `.oapx`; the request goes through it and no
  `.oxap` appears.
- `both`: `.oxap` and `.oapx` point at different models; only the `.oxap` one
  is asked.
- `both env names`: `OXAP_DEBUG_PROVIDER_PAYLOAD` and
  `OAPX_DEBUG_PROVIDER_PAYLOAD` name different files; only the OXAP one is
  written.
- `old env name`: only `OAPX_DEBUG_PROVIDER_PAYLOAD` is set, and its file is
  written.

It ends with `ok` and exits 0. The second script runs from an old-style home
in a fresh workspace, so a background job's `output_path` must be under the
workspace's `.oxap/jobs/`; it fails if the job log lands anywhere else.

The unit tests in `test-unit-core`, `test-unit-utils`, `test-unit-tools` and
`test-unit-protocol` pin the same rules for the provider config, tool
artifacts, the keychain service override and the global base URL.
