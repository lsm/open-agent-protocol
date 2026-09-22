# OAP Rust SDK

Rust SDK published as `oap-sdk`. By default it starts `oapx serve agent,provider --stdio` and speaks OAP v0.1 on both profiles. Provider inference and catalog discovery use `model-provider-core`; agent sessions and authentication use `agent-control-core`.

It mirrors the [TypeScript SDK](../typescript/README.md): same protocol, same namespaces, same error taxonomy.

The old Makai v1 wire is available only with the explicit `Client::builder().legacy_wire()` opt-in. There is no automatic fallback.

## Installation

The crate lives in this repository at `sdk/rust/`. Until it is published, depend on it by path or by git:

```toml
[dependencies]
oap-sdk = { git = "https://github.com/lsm/open-agent-protocol" }
tokio = { version = "1", features = ["rt-multi-thread", "macros"] }
futures = "0.3"
```

The crate is imported as `oap_sdk`.

You also need the runtime binary. By default the SDK looks for a local build under `zig-out/bin/oapx` or `zig/zig-out/bin/oapx`, then falls back to `oapx` on `PATH`. See [Configuration](#configuration) for explicit options.

```bash
zig build install --prefix /tmp/oapx
export OAP_SDK_BINARY_PATH=/tmp/oapx/bin/oapx
```

## Quick start

Create a client, resolve a model, send one message, print the reply.

```rust,no_run
use oap_sdk::{Client, ExecutionRequest};

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let client = Client::connect().await?;

    let model = client
        .models()
        .resolve("anthropic", Some("anthropic-messages"), "claude-sonnet-4-5")
        .await?;

    let response = client
        .provider()
        .complete(
            ExecutionRequest::prompt(&model.model_ref, "Write a haiku about streams.")
                .with_max_tokens(128),
        )
        .await?;

    println!("{}", response.text());

    client.close().await;
    Ok(())
}
```

## Streaming completions

`provider().stream(...)` is the streaming form of `complete`. Each `TextDelta` carries newly generated text; deltas concatenate.

```rust
use futures::StreamExt;
use oap_sdk::{Client, ExecutionRequest, ProviderEvent};

# async fn run(client: &oap_sdk::Client, model_ref: &str) -> oap_sdk::Result<()> {
let mut events = Box::pin(
    client
        .provider()
        .stream(ExecutionRequest::prompt(model_ref, "Explain lock-free queues.")),
);

while let Some(event) = events.next().await {
    match event? {
        ProviderEvent::TextDelta { delta } => print!("{delta}"),
        ProviderEvent::ToolCall { name, arguments_json, .. } => {
            eprintln!("tool call: {name}({arguments_json})");
        }
        ProviderEvent::MessageEnd { stop_reason, .. } => {
            eprintln!("stop reason: {stop_reason:?}");
        }
        _ => {}
    }
}
# Ok(())
# }
```

Exactly one terminal event ends the stream — `MessageEnd` or `Error`. Failures that never reach the event plane (a rejected request, a dead runtime, a timeout) arrive as an `Err` item.

**Cancellation.** Dropping the stream cancels the work: the SDK sends `abort_request` for a provider stream and `agent_stop` for an agent run, so the runtime stops rather than finishing into a queue nobody is reading. Dropping the whole `Client` terminates and reaps the child process.

## Agent runs and model switching

`agent().run(...)` and `agent().stream(...)` drive an OAP agent session. `switch_model(session_id, model_ref)` changes the selected model mid-session; `run_selected(session_id, messages)` uses that selection. Client-executed tools (`Tool::on_call`) are not yet exposed by the OAP endpoint: agent requests containing tools and direct provider requests containing executable callbacks fail explicitly with `unsupported_feature`. Direct provider calls can still pass declaration-only tool schemas. The old callback path is available only under `legacy_wire()`.

```rust
use oap_sdk::{Client, ExecutionRequest};

# async fn run(client: &oap_sdk::Client, model_ref: &str) -> oap_sdk::Result<()> {
let response = client
    .agent()
    .run(
        ExecutionRequest::prompt(model_ref, "Say hello."),
    )
    .await?;

println!("{}", response.text());
# Ok(())
# }
```

For streaming, iterate `agent().stream(request)` and handle `AgentStart`, wrapped provider deltas, and terminal `AgentEnd`. Per-run agent sampling controls (`max_tokens`, `temperature`, `reasoning_effort`) also fail explicitly until represented on the OAP agent profile; these controls remain supported for direct provider inference.

### Agent model discovery

`client.agent().models()` is a compatibility alias for the provider model catalog on the same OAP connection.

## Auth

`auth().list_providers()` inspects auth state; `auth().login(...)` runs an interactive flow. Token material is owned by the runtime and is never exposed by the SDK.

```rust
use oap_sdk::{AuthEvent, AuthHandlers, AuthStatus, Client};

# async fn run(client: &oap_sdk::Client) -> oap_sdk::Result<()> {
let providers = client.auth().list_providers().await?;
let needs_login = providers
    .iter()
    .find(|provider| provider.id == "anthropic")
    .is_none_or(|provider| provider.auth_status != AuthStatus::Authenticated);

if needs_login {
    let handlers = AuthHandlers::new()
        .on_event(|event| {
            if let AuthEvent::AuthUrl { url, .. } = event {
                println!("Open {url}");
            }
        })
        .on_prompt(|prompt| async move {
            println!("{}", prompt.message);
            let mut answer = String::new();
            std::io::stdin().read_line(&mut answer).map_err(|e| e.to_string())?;
            Ok(answer.trim().to_owned())
        });

    client.auth().login("anthropic", Some(&handlers)).await?;
}
# Ok(())
# }
```

A flow that reaches a prompt with no `on_prompt` handler is cancelled rather than left hanging. Dropping the login future sends `auth.login.cancel.request`, so the runtime does not leave an OAuth listener running.

You can also configure one-shot automatic retry for `provider` and `agent` calls:

```rust
use oap_sdk::{AuthHandlers, AuthRetryPolicy, Client};

# async fn run() -> oap_sdk::Result<()> {
let client = Client::builder()
    .auth_retry_policy(AuthRetryPolicy::AutoOnce)
    .auth_handlers(AuthHandlers::new().on_prompt(|_| async move {
        Ok(std::env::var("OAPX_AUTH_CODE").unwrap_or_default())
    }))
    .connect()
    .await?;
# let _ = client;
# Ok(())
# }
```

With `AutoOnce`, a call that hits `auth_required` logs in once and retries. With no handlers configured and interactive auth required, it fails fast with the typed error rather than hanging.

## Models

Models are discovered through `client.models()`. Use `model_ref` from the returned descriptor in provider and agent requests. **Treat `model_ref` as opaque**: do not parse or construct it.

```rust
use oap_sdk::{Client, ListModelsRequest};

# async fn run(client: &oap_sdk::Client) -> oap_sdk::Result<()> {
let response = client
    .models()
    .list(ListModelsRequest {
        provider_id: Some("anthropic".into()),
        include_login_required: Some(true),
        ..Default::default()
    })
    .await?;

for model in &response.models {
    println!("{}: {} [{:?}]", model.display_name, model.model_ref, model.auth_status);
}
# Ok(())
# }
```

`resolve(provider_id, api, model_id)` is the deterministic single-model lookup. Zero or multiple matches are an `invalid_request` failure.

## Sessions

`RunOptions::session_id` identifies an OAP session. Sessions can retain their selected model across runs; use `switch_model` then `run_selected` to change it. Legacy-wire sessions retain their earlier correlation-only behavior.

## Configuration

`Client::builder()` configures the transport and binary resolution.

```rust
use std::time::Duration;
use oap_sdk::Client;

# async fn run() -> oap_sdk::Result<()> {
let client = Client::builder()
    .binary_path("/opt/oapx/bin/oapx")
    .env("OAPX_LOG", "info")
    .handshake_timeout(Duration::from_secs(2))
    .response_timeout(Duration::from_secs(30))
    .frame_timeout(Duration::from_secs(30))
    .connect()
    .await?;
# let _ = client;
# Ok(())
# }
```

### Binary resolution

Mirroring the TypeScript resolver, in order:

1. `OAP_SDK_BINARY_PATH`, then `ClientBuilder::binary_path` / `BinaryResolver::binary_path`;
2. `OAP_SDK_BINARY_URL` / `binary_url` with a **required** SHA-256 checksum (`OAP_SDK_BINARY_SHA256` / `checksum_sha256`), cached under `~/.cache/makai/bin`;
3. *(TypeScript only)* the `@oap-sdk/cli-<platform>-<arch>` npm package — **not implemented in Rust**, see below;
4. `./zig-out/bin/oapx`, then `./zig/zig-out/bin/oapx`;
5. `oapx` on `PATH`.

On Windows the executable name is `oapx.exe`.

The environment variable outranks the builder option, as in TypeScript, so an operator can redirect an application that hardcoded a path. `ClientBuilder::command(path)` bypasses resolution entirely when that override is not wanted.

**The npm platform-package step is deliberately omitted.** It resolves through Node's module resolution against an optional npm dependency; Rust has no equivalent channel that ships a platform-specific executable alongside a library crate, and inventing one (a `build.rs` download, say) would put a network fetch in the build with none of the checksum guarantees step 2 insists on. Install the binary however you like and point `OAP_SDK_BINARY_PATH` at it, or let step 5 find it on `PATH`.

Downloading from a URL needs the `download` feature (off by default, to keep an HTTP stack out of the default dependency tree):

```toml
oap-sdk = { version = "0.1", features = ["download"] }
```

Without it, an already-cached file is still checksum-verified and used, and a cache miss is a clear error rather than a silent fallback to a different binary than you pinned.

## Error handling

All failures are one `Error` enum, mirroring the TypeScript error classes:

| TypeScript | Rust |
| --- | --- |
| `MakaiStreamError` | `Error::Stream { kind, code, provider_id, message }` |
| `MakaiAuthRequiredError` | `Error::AuthRequired { provider_id, message }` |
| `MakaiProtocolError` | `Error::Protocol { code, message }` |
| `MakaiAuthError` | `Error::Auth { kind, code, message }` |
| *(untyped in TS)* | `Error::Transport { message }` — spawn, handshake, binary resolution |
| *(untyped in TS)* | `Error::InvalidRequest { message }` — rejected before anything is sent |

```rust
use oap_sdk::Error;

# fn handle(error: oap_sdk::Error) {
match error {
    Error::AuthRequired { provider_id, .. } => eprintln!("login required for {provider_id}"),
    Error::Stream { kind, code, .. } => eprintln!("stream failed ({kind}/{code:?})"),
    Error::Protocol { code, message } => eprintln!("protocol failed ({code:?}): {message}"),
    Error::Auth { kind, message, .. } => eprintln!("auth failed ({kind}): {message}"),
    other => eprintln!("{other}"),
}
# }
```

`Error::code()`, `Error::provider_id()`, `Error::is_auth_required()`, and `Error::is_cancelled()` are available on every variant.

## Examples

```bash
cargo run --example complete
cargo run --example stream
cargo run --example agent_tools  # explicitly uses legacy_wire() for callbacks
cargo run --example login -- anthropic
```

## Development

```bash
cargo fmt --check
cargo clippy --all-targets --all-features -- -D warnings
cargo test
```

`cargo test` needs no credentials and no runtime binary: the OAP integration tests drive `oap-protocol-fake`, and explicit legacy tests drive `makai-protocol-fake`. You can point `ClientBuilder::command` at either built binary to test your own code:

```rust
# fn build(fake: std::path::PathBuf) -> oap_sdk::ClientBuilder {
oap_sdk::ClientBuilder::new()
    .command(fake)
    .args(Vec::<String>::new())
    .env_clear()
    // Add `.legacy_wire()` when targeting makai-protocol-fake.
# }
```

Inside this crate, `tests/common/mod.rs` gets that path from
`env!("CARGO_BIN_EXE_oap-protocol-fake")`. Cargo defines that variable only for
a crate's own integration tests, so from another crate it does not exist: build
the binary with `cargo build --bin oap-protocol-fake` and pass the path
yourself.

To also exercise a real runtime:

```bash
zig build install --prefix /tmp/oapx-rs
OAP_SDK_BINARY_PATH=/tmp/oapx-rs/bin/oapx cargo test
```

Without `OAP_SDK_BINARY_PATH` the `real_binary` tests skip, mirroring `sdk/typescript/test/makai_binary_smoke.test.ts`.

**On macOS**, the two tests that make the runtime *persist* credentials skip by default: `saveToPreferredStorage` writes to the login Keychain, and creating that item from an unsigned local build blocks in `AuthorizationCopyRights` waiting on a UI prompt no test runner can answer. Everything else runs. Set `OAP_SDK_RUST_SDK_ALLOW_KEYCHAIN=1` to run them on a Mac where the item's ACL is already approved. CI runs on Linux, where the file store is used and the write is unattended.
