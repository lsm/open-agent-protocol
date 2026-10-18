# Choosing a backend

`oapx serve agent --stdio` serves oapx's built-in loop. `--backend <name>`
serves a third-party harness behind the same envelopes instead, and
`oapx tui --backend <name>` runs the terminal UI over it. This page says what
each backend needs before it starts. The pins come from `harnesses/*.json`,
the only place a pin is written; what each harness cannot express in OAP is in
the ledger linked from its row.

```sh
oapx serve agent --stdio --backend claude
oapx serve agent --stdio --backend acp --config oap-serve.json
```

## The backends

| `--backend` | Catalog entry | Current pin | Runs without `--config` | Ledger |
| --- | --- | --- | --- | --- |
| `oapx` | — | — | yes | — |
| `memory` | — | — | yes | — |
| `claude` | `claude-code` | `2.1.302` | yes: `claude` from `PATH` | [mapping](../research/claude-code-agent-sdk-2.1.302-mapping.md) |
| `codex` | `codex-app-server` | `rust-v0.163.0` | yes: `codex` from `PATH` | [mapping](../research/codex-app-server-0.163.0-mapping.md) |
| `pi` | `pi` | `v1.3.0` | yes: `pi` from `PATH` | [mapping](../research/pi-v1.3.0-mapping.md) |
| `opencode` | `opencode` | `v2.0.32` | yes: the installed build, else `opencode` from `PATH` | [mapping](../research/opencode-v2.0.32-mapping.md) |
| `acp` | `acp` | `v1.12.0` | no: needs `executable` | [mapping](../research/acp-v1.12.0-mapping.md) |
| `hermes` | `hermes` | `v0.22.2` | no: needs `executable` | [mapping](../research/hermes-v0.22.2-mapping.md) |
| `deepseek` | `deepseek-harness` | `dsh-v0.2.1-rc.2` | no: needs `executable`, `provider` and `model` | [mapping](../research/deepseek-harness-dsh-v0.2.1-rc.2-mapping.md) |

`--config <path>` takes the entry of that name from a registry shaped like
[`examples/oap-serve.json`](../examples/oap-serve.json). Unknown members are
refused.

## How each one authenticates

A harness child never inherits the caller's environment. A built-in entry
passes `HOME` and `PATH` and nothing else; a `--config` entry passes exactly
the variables its `environment` list names, either by name or as a literal
`NAME=value`. So a harness reaches a credential in one of three ways:

- **Its own sign-in under `HOME`.** `claude`, `codex`, `pi` and `opencode` use
  whatever login their own CLI stored, because `HOME` is passed through. Sign
  in with the harness's CLI first; oapx never reads that login.
- **A key the entry names.** List the variable in `environment`, such as
  `ANTHROPIC_API_KEY` for `claude`. The value is read from oapx's own
  environment when the backend starts.
- **A catalog provider.** A `claude` or `codex` entry may set `model_provider`
  to a provider in [`providers/catalog.json`](../providers/catalog.json). oapx
  then points the harness at that provider's endpoint and hands it the
  provider's API key, taken from the provider's environment variable or from
  `oapx auth login`. `claude` needs a provider serving `anthropic-messages`;
  `codex` needs one serving `openai-responses` at a fixed address. oapx refuses
  an OAuth login here, since it does not hand a login to a harness, and
  refuses an entry that also sets a variable `model_provider` sets.

The rest:

- `oapx` uses oapx's own credentials (`oapx auth login`).
- `memory` needs none.
- `acp` and `hermes` use whatever the agent or gateway reads, which on this
  path is its own config plus the entry's `environment`.
- `deepseek` names a `provider` the harness already knows, and the harness
  finds that provider's key the way its own config says: a variable the entry
  must then list, or its credential store under `DSH_HOME`, which the
  [dsh-v0.1.7-rc.2 ledger](../research/deepseek-harness-dsh-v0.1.7-rc.2-mapping.md)
  describes.
- `opencode` with an `endpoint` attaches to a running server, so the entry
  names the server's password variable (`OPENCODE_PASSWORD`); without one,
  oapx starts the server itself under a fresh password.

## From the TypeScript SDK

The SDK spawns `oapx` with the `args` it is given, so the same flag selects a
backend there:

```ts
const client = await createMakaiClient({
  args: ["serve", "agent", "--stdio", "--backend", "claude"],
});
```

Such an endpoint serves the agent profile alone. Agent runs work as usual;
`client.provider` and `client.models` fail with `OapUnsupportedFeatureError`
before sending anything. The SDK does not answer permission requests yet, so a
run on a backend that asks one waits until a timeout or an abort ends it.

## Hermes gates and a `copilot` on `PATH`

The Hermes live gates pass `PATH` through. When that `PATH` held GitHub's
`copilot` CLI, a process wrote into the gate's isolated `HOME` after the
gateway exited and outlived it, and could reach credentials kept outside
`HOME`. Which gateway path starts it was not established. Run the Hermes gates
with a `PATH` that does not contain `copilot`; the
[v0.22.2 ledger](../research/hermes-v0.22.2-mapping.md#the-live-gates-start-the-users-copilot-cli)
has the runs.
