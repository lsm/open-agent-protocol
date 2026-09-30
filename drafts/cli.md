# OAP Command Line: One Verb Set

Status: proposed design
Governs: the `oapx` command line
Follows: [Decision 0032](../decisions/0032-go-and-zig-are-peers.md)
Amended by: [Decision 0038](../decisions/0038-one-released-binary-and-a-library-for-every-language.md),
which is folded in here: **one released binary, and a library for every
language.** `oapx` is the released Zig binary. The Go tree carries the same
verb set — as `goap`, matching `oapx` — and is run from the repository with
`go run ./go/cmd/goap`; it is not installed, and nothing a user reads should
suggest installing it. So a rule below binds `oapx`, and where the Go tree
carries the verb it means the same thing there.

The two trees once grew different verbs, and the same word meant different
things: `goap serve` started a multi-session daemon, while `oapx serve agent`
served one agent loop. This draft fixes one verb set. The `goap` column records
what the repository's Go tool carries, so the two trees can be compared; it is
not a second product.

## Verbs

| verb | meaning | `oapx` (released) | `goap` (repository tool) |
|---|---|---|---|
| `serve agent [--backend B] [--config F] [--stdio]` | one agent loop over `agent-control-core`, raw envelopes per [endpoint-stdio](endpoint-stdio.md); no `--backend` means the binary's own loop | native loop; harness backends as they are wired | the Go adapters (today `goap endpoint --adapter`) |
| `serve provider [--stdio \| --http ADDR] [--specimens]` | `model-provider-core` | yes | answers `unavailable` |
| `serve agent,provider --stdio` | both profiles on one pipe (Decision 0027) | yes | answers `unavailable` |
| `hub [--config F] [--addr ADDR \| --stdio]` | the multi-session daemon: an adapter registry, fan-out, cursor replay, HTTP+SSE or the stdio transport-object wire, per [hub](hub.md) | yes (stdio) | yes |
| `validate [--format human\|json] [--mode strict\|tolerant] [--pack DIR]... [--provider] TRACE...` | judge traces: decode, schema, semantic | yes (packs, modes and some semantic rules still porting) | yes |
| `conformance [--command CMD] [--format text\|json]` | drive an endpoint and judge what crossed the pipe | yes, first slice: the handshake and one submitted run; the interaction, tool, queue, model and auth groups are still to come | yes |
| `check` | the repository's own schemas, fixtures and reference path | not yet | yes |
| `run`, `auth`, the TUI (bare invocation) | the product's own loop and credentials | yes | — |

`conformance` gives each wait one budget for the whole wait, not one per line: the
line deadline bounds a single response, event or control answer from the moment the
runner starts looking for it, and a frame the runner did not ask for spends that
budget rather than renewing it. An endpoint that keeps talking but never answers is
therefore judged on the deadline instead of being waited on forever. A match already
buffered is still returned, and a control frame the endpoint does not implement still
skips rather than failing, because neither is the endpoint answering late.

The hub is its own verb in both binaries: `goap hub` and `oapx hub`. It is a layer
above an endpoint, not a different spelling of one, and giving it its own verb is
what lets `serve agent` mean one thing.

`oapx hub` carries the stdio transport today and answers `unavailable` for the
other two, naming which: `--addr` with the HTTP and SSE transport, and `--config`
with the hub's registry. Nothing silently falls back — a host that asked for a
transport this build does not serve is told so rather than handed a pipe, because
a pipe answers the requests that fit it and is silent about the rest.

## Rules the verb set follows

- **A backend or verb an implementation does not carry answers `unavailable`**,
  naming it, and exits non-zero. It never falls back to something else
  silently.
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
