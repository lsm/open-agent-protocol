# OAP Python SDK

`oap-sdk` (imported as `oap_sdk`) uses one combined OAP 0.1 stdio endpoint by default:

```text
oapx serve agent,provider --stdio
```

It multiplexes `agent-control-core` and `model-provider-core` profiles over that connection. The public `MakaiClient` name remains for source compatibility; the old Makai V1 wire is used only when `legacy_wire=True` or `OAP_SDK_LEGACY_WIRE=1` is set. There is no silent fallback.

The package requires Python 3.11+. Install from a checkout with `pip install ./sdk/python` (it is not yet published to PyPI).

## Connect and call a provider

```python
import asyncio
import oap_sdk

async def main() -> None:
    async with oap_sdk.connect() as client:
        model = await client.models.resolve(
            provider_id="anthropic", model_id="claude-sonnet-4-5"
        )
        response = await client.provider.complete(
            model_ref=model.model_ref,
            messages=[{"role": "user", "content": "Write a haiku about streams."}],
            options=oap_sdk.RunOptions(max_tokens=128),
        )
        print(response.text)

asyncio.run(main())
```

`client.models` reads the direct provider profile's catalog. Treat returned `model_ref` values as opaque. `client.provider.stream(...)` yields `MessageStart`, content deltas, tool-call events, and `MessageEnd` or `StreamError`. If leaving a stream early, close its async generator so the SDK sends a best-effort cancellation. Direct provider tool definitions map to OAP `input_schema`; tool calls are returned to the caller, not executed in the SDK.

## Agent sessions

`client.agent` uses the agent-control profile. A session has a default model, which can be changed between runs. A per-call `model_ref` overrides that default for one submit. `available_models` reads the session's effective catalog, distinct from direct provider discovery.

```python
async with oap_sdk.connect() as client:
    session = await client.agent.open_session("my-session")
    session_id = session["session_id"]
    catalog = await client.agent.available_models(session_id)
    await client.agent.switch_model(session_id, catalog["models"][0]["id"])
    response = await client.agent.run(
        messages=[{"role": "user", "content": "Hello"}],
        options=oap_sdk.RunOptions(session_id=session_id),
    )
```

The optional `client.agent.attach_provider(session_id, {"id": ..., "provider_id": ..., "service_id": ...})` operation attaches an operator-configured OAP provider service to one session. A host without the extension returns typed `unsupported_feature`; there is no implicit attachment. Remote provider services are follow-up work. `switch_model` affects future runs, not an active run.

The current OAP agent host does not advertise client-executed `+control-tools`. Supplying `tools=` to `client.agent.run` or `.stream` raises `MakaiProtocolError(code="unsupported_feature")`; callbacks are never silently discarded. Agent `RunOptions.max_tokens`, `.temperature`, and `.reasoning_effort` also have no OAP 0.1 submit projection and are rejected explicitly. `.metadata` maps to submit metadata.

## Authentication

`client.auth.list_providers()` and `.login()` use agent-profile `+auth` on the trusted local stdio connection. The runtime owns credentials; the SDK receives no token. A prompt answer may be a short-lived OAuth code, so applications must not log it.

```python
handlers = oap_sdk.AuthFlowHandlers(
    on_event=lambda event: print(event.url) if isinstance(event, oap_sdk.AuthUrlEvent) else None,
    on_prompt=lambda prompt: input(f"{prompt.message} "),
)
await client.auth.login("anthropic", handlers)
```

A missing prompt handler cancels the flow. `AuthOptions(auth_retry_policy="auto_once", handlers=handlers)` enables one login and retry after a typed `auth_required` or `credential_*` failure; arbitrary provider-error text never triggers login. For a stream that already emitted user-visible output, the SDK does not replay the call.

## Configuration and errors

`connect()` accepts `command`, `args`, `cwd`, `env`, a `BinaryResolverOptions`, `auth`, and timeouts in seconds. The default args are `("serve", "agent,provider", "--stdio")`. Binary resolution honors `OAP_SDK_BINARY_PATH`, an explicit path or checksum-verified URL, local `zig-out/bin/oapx` or `zig/zig-out/bin/oapx`, then `PATH`.

`oap_sdk.connect_sync()` supplies the same API through a blocking wrapper for scripts; do not call it inside a running event loop. It also accepts `legacy_wire=True` for an old Makai V1 runtime. The async and sync clients own their child process and should be closed.

Failures are typed as `MakaiStreamError`, `MakaiAuthRequiredError`, `MakaiProtocolError`, or `MakaiAuthError`. Unsupported operations carry code `unsupported_feature`. Legacy class names remain for compatibility, even when the wire is OAP.

Run `uv run --with pytest --with pytest-asyncio pytest -q` from `sdk/python` to run the suite. OAP fake-host tests require no credentials.
