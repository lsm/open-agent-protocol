# OAP Command Line: One Verb Set, Two Binaries

Status: proposed design
Governs: the `oapx` (Zig) and `goap` (Go) command lines
Follows: [Decision 0032](../decisions/0032-go-and-zig-are-peers.md)

Two binaries implement OAP: `oapx`, the product, and `goap`, its Go peer. Until
now each grew its own verbs, and the same word meant different things: `oap
serve` started a multi-session daemon, while `oapx serve agent` served one
agent loop. This draft fixes one verb set. Where both binaries carry a verb, it
means the same thing in both.

## Verbs

| verb | meaning | `oapx` | `goap` |
|---|---|---|---|
| `serve agent [--backend B] [--config F] [--stdio]` | one agent loop over `agent-control-core`, raw envelopes per [endpoint-stdio](endpoint-stdio.md); no `--backend` means the binary's own loop | native loop; harness backends as they are wired | the Go adapters (today `goap endpoint --adapter`) |
| `serve provider [--stdio \| --http ADDR] [--specimens]` | `model-provider-core` | yes | answers `unavailable` |
| `serve agent,provider --stdio` | both profiles on one pipe (Decision 0027) | yes | answers `unavailable` |
| `hub [--config F] [--addr ADDR \| --stdio]` | the multi-session daemon: an adapter registry, fan-out, cursor replay, HTTP+SSE or the stdio transport-object wire | — | yes (today `goap serve`) |
| `validate [--format human\|json] [--mode strict\|tolerant] [--pack DIR]... [--provider] TRACE...` | judge traces: decode, schema, semantic | yes (packs, modes and some semantic rules still porting) | yes |
| `conformance [--command CMD] [--format text\|json]` | drive an endpoint and judge what crossed the pipe | not yet | yes |
| `check` | the repository's own schemas, fixtures and reference path | not yet | yes |
| `run`, `auth`, the TUI (bare invocation) | the product's own loop and credentials | yes | — |

The hub keeps its name only in Go. It is a layer above an endpoint, not a
different spelling of one, and giving it its own verb is what lets `serve agent`
mean one thing.

## Rules both binaries follow

- **A backend or verb a binary does not carry answers `unavailable`**, naming
  it, and exits non-zero. It never falls back to something else silently.
- **`--config` is one schema** (`examples/oap-serve.json`), decoded with the same
  strictness in both: unknown members refused, the `environment` list an
  allowlist, and an optional harness `version` resolved against the harness
  catalog (Decision 0033).
- **Exit codes:** 0 when the verb succeeded and everything judged passed; 1 when
  something judged failed; 2 for a usage error; 3 when the binary could not
  judge (an unreadable input, a rule it has not ported that the input needs).
- **`--format json` output is shared.** `validate` prints an array of
  `{file, valid, diagnostics}`, each diagnostic carrying at least `phase`,
  `code` and `index`, and `line` when the input had lines. `oapx` adds
  `"complete": false` while its semantic port is partial, and drops it once it
  is not. A differential job compares the two outputs field by field.

## Migration

- `goap serve` becomes `goap hub`. There is no alias. An alias would keep the
  word meaning two things for another release, which is the confusion this
  draft removes.
- `goap endpoint --adapter A` becomes `goap serve agent --backend A`. `endpoint`
  stays as an alias until the conformance runner and its callers move.
- `oapx`'s superseded flags (`--tui`, `-p`, `--oap`, `--oap-provider`) keep
  working, as they do today.
