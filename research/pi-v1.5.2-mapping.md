# Pi coding agent v1.5.2 mapping ledger

Status: re-pinned from v1.5.0. The [v1.5.0 ledger](pi-v1.5.0-mapping.md)
and its predecessors remain applicable except as described here.

## Provenance

- Repository: https://github.com/earendil-works/pi
- Tag `v1.5.2` (lightweight): commit
  `b4afdddf44e059c67ccf5097e5317d913521b470`, tree
  `ed418d79cb44d3690f110e1077c4612db033f2c9`.
- Release metadata SHA-256: `pi-linux-x64.tar.gz`
  `811c6f2c9e7aaee252b5404252e556fb713aa209c1f7f924226aa77d4ef70cb5`;
  `pi-darwin-arm64.tar.gz`
  `56b5e194d485ced555667b8ba3276157a8272e9ecde9e001e428af80985bdd9b`.
  Artifacts were not downloaded or executed for this pin.
- The window v1.5.0…v1.5.2 covers 395 commits, most of them the new `apps/p2`
  web client, which is not on the adapter's surface.

## Wire comparison

Comparing the two tag trees on the adapter's pinned surface,
`packages/coding-agent/src/modes/rpc/rpc-types.ts`,
`packages/coding-agent/src/modes/json-event.ts`,
`packages/coding-agent/src/core/session-manager.ts`,
`packages/agent/src/types.ts` and `packages/coding-agent/src/rpc-entry.ts`
are byte-identical (blob `c0c04fde0d7305bb8c07151c17498df8a25fb1d8` for
`json-event.ts` at both tags). The changed files:

- `packages/coding-agent/src/modes/rpc/rpc-mode.ts`
  (`afd8267a1078e86dba06ab6e98aa4432c8217b16`): shutdown and signal handling
  only. An extension shutdown request now waits until input is attached, no
  command is being handled and the session is idle; signal handlers stay
  registered for the whole shutdown and only a repeated signal forces exit;
  the SIGHUP/SIGTERM exit codes 129/143 are unchanged. No RPC method,
  notification or event member changed.
- `packages/coding-agent/src/core/agent-session.ts`
  (`f76247d5647a6ab17923425ebb0cee5b4ca6495b`): 192 changed lines around
  queue-update emission call sites; no new emitted event kind. As at v1.5.0,
  a source comparison does not prove all runtime behavior is unchanged.
- `packages/coding-agent/src/cli/args.ts`
  (`8de8441c43185db446cdb1290c1d4f0f9a1f5cb0`): one help-text line for
  `auth`. Argument parsing, including `--mode` and `--no-extensions`, is
  unchanged.

This is source evidence only, not a v1.5.2 process check.

## Release notes

v1.5.1 adds `pi auth login/logout` CLI commands, idle prompt-cache warming
and Anthropic message-thread requests; v1.5.2 adds forking a session whose
folder is gone. None of these change the RPC mode the adapter drives. The
auth commands are interactive CLI surfaces the adapter does not use.

## Corpus

`fixtures/adapters/pi-v1.5.2` carries the prior corpus forward with updated
provenance and capability revision; the retired v1.5.0 corpus is removed.
No case was re-recorded, since no surface the adapter uses changed. No real
account, credentials, model call or new process recording was used for this
upgrade.
