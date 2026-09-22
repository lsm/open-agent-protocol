# OAP TypeScript SDK

TypeScript SDK published as `oap-sdk`. The default client spawns `oapx serve agent,provider --stdio` and speaks OAP v0.1 on both profiles: agent sessions and authentication on `agent-control-core`, direct inference and provider model discovery on `model-provider-core`.

`createOapClient()` is the preferred entry point. `createMakaiClient()` remains an alias that now defaults to OAP; it never silently falls back to the old wire. Pass `{ wireProtocol: "legacy" }` only when you explicitly need a still-unmigrated Makai feature. The low-level `createMakaiStdioClient`, `createMakaiAuthClient`, and related factories are legacy-wire APIs and deprecated for new integrations.

## Installation

```bash
npm install oap-sdk
```

No release has been published yet; until one is, install from a checkout of this repository.

The package is a library and ships no executable, so you also need the runtime binary. By default the SDK prefers an installed `@oap-sdk/cli-<platform>-<arch>` package, then a local build under `zig-out/bin/oapx` or `zig/zig-out/bin/oapx`, then `oapx` on `PATH`. See [Configuration](#configuration) for explicit binary resolver options.

## Quick start

Create a client, resolve a model, send one chat message, and print the assistant response.

```ts
import { createMakaiClient } from "oap-sdk";

async function main(): Promise<void> {
  const client = await createMakaiClient();

  try {
    const { model } = await client.models.resolve({
      provider_id: "anthropic",
      api: "anthropic-messages",
      model_id: "claude-sonnet-4-5",
    });

    const response = await client.provider.complete({
      model_ref: model.model_ref,
      messages: [{ role: "user", content: "Write a haiku about streams." }],
      options: { max_tokens: 128 },
    });

    const content = response.message.content;
    console.log(typeof content === "string" ? content : JSON.stringify(content, null, 2));
  } finally {
    await client.close();
  }
}

main().catch((error: unknown) => {
  console.error(error);
  process.exitCode = 1;
});
```

## Streaming completions

Use `client.provider.stream(...)` for provider-level streaming. It is the SDK's streaming form of `complete`; each `text_delta` contains newly generated text.

```ts
import { createMakaiClient } from "oap-sdk";

async function main(): Promise<void> {
  const client = await createMakaiClient();

  try {
    const { model } = await client.models.resolve({
      provider_id: "anthropic",
      api: "anthropic-messages",
      model_id: "claude-sonnet-4-5",
    });

    for await (const event of client.provider.stream({
      model_ref: model.model_ref,
      messages: [{ role: "user", content: "Explain lock-free queues in one paragraph." }],
      options: { max_tokens: 256 },
    })) {
      switch (event.type) {
        case "message_start":
          console.error(`Streaming ${event.provider_id ?? "provider"}/${event.model_id ?? "model"}`);
          break;
        case "text_delta":
          process.stdout.write(event.delta);
          break;
        case "thinking_delta":
          // Reasoning output is surfaced separately from normal text.
          break;
        case "tool_call":
          console.error(`\nTool call requested: ${event.name}(${event.arguments_json})`);
          break;
        case "message_end":
          process.stdout.write("\n");
          console.error("Stop reason:", event.stop_reason);
          break;
        case "error":
          throw new Error(event.message);
      }
    }
  } finally {
    await client.close();
  }
}

void main();
```

If you are migrating from an API that used `client.complete({ stream: true })`, the equivalent call is `client.provider.stream(request)`; non-streaming calls use `client.provider.complete(request)`.

## Agent runs and model switching

Use `client.agent.run(...)` for an agent session, or `client.agent.stream(...)` for lifecycle and content events. `switchModel(sessionId, modelRef)` changes the selected model of an open session; `runSelected(sessionId, messages)` uses that selection on the next run. The OAP server currently does not expose client-executed agent tools, so an OAP call containing `tools` fails with `OapUnsupportedFeatureError` rather than dropping callbacks. Direct provider calls may carry declaration-only tools, but client-side `execute` callbacks are unsupported there too.

```ts
import { createOapClient } from "oap-sdk";

async function main(): Promise<void> {
  const client = await createOapClient();

  try {
    const { model } = await client.models.resolve({
      provider_id: "anthropic",
      api: "anthropic-messages",
      model_id: "claude-sonnet-4-5",
    });

    const response = await client.agent.run({
      model_ref: model.model_ref,
      messages: [{ role: "user", content: "Say hello." }],
    });

    console.log(response.message.content);
  } finally {
    await client.close();
  }
}

void main();
```

For agent streaming, iterate over `client.agent.stream(request)` and handle `agent_start`, content deltas, and the terminal `agent_end` event. Client-side tool callbacks remain available only under explicit `{ wireProtocol: "legacy" }` until `+control-tools` is implemented.

### Agent model discovery

`client.agent.models` is a compatibility alias for the provider model catalog exposed as `client.models` on the same OAP connection.

```ts
import { createMakaiClient } from "oap-sdk";

async function main(): Promise<void> {
  const client = await createMakaiClient();

  // Both read the provider model catalog on the same OAP connection:
  const { models } = await client.models.list();
  const { models: agentModels } = await client.agent.models.list();

  void models;
  void agentModels;
}

void main;
```

Prefer `client.models` when you only need model discovery. Use `client.agent.models` when chaining discovery with an agent call on the same namespace.

## Auth

Use `client.auth.listProviders()` to inspect auth state, and `client.auth.login(providerId, handlers)` to start an interactive login flow. Token material is owned by the runtime and is not exposed by the SDK.

```ts
import { createMakaiClient, type MakaiAuthEvent } from "oap-sdk";
import { createInterface } from "node:readline/promises";
import { stdin as input, stdout as output } from "node:process";

async function main(): Promise<void> {
  const client = await createMakaiClient();
  const rl = createInterface({ input, output });

  try {
    const providers = await client.auth.listProviders();
    const anthropic = providers.find((provider) => provider.id === "anthropic");

    if (anthropic?.auth_status !== "authenticated") {
      await client.auth.login("anthropic", {
        onEvent(event: MakaiAuthEvent): void {
          if (event.type === "auth_url") {
            console.log(`Open ${event.url}`);
            if (event.instructions) console.log(event.instructions);
          } else if (event.type === "progress") {
            console.log(event.message);
          }
        },
        async onPrompt(prompt): Promise<string> {
          return rl.question(`${prompt.message} `);
        },
      });
    }
  } finally {
    rl.close();
    await client.close();
  }
}

void main();
```

You can also configure automatic one-shot auth retry for `provider` and `agent` calls:

```ts
import { createMakaiClient } from "oap-sdk";

async function main(): Promise<void> {
  const client = await createMakaiClient({
    auth: {
      auth_retry_policy: "auto_once",
      handlers: {
        onEvent: (event) => {
          if (event.type === "auth_url") console.log(`Open ${event.url}`);
        },
        onPrompt: async (prompt) => prompt.allow_empty ? "" : process.env.OAPX_AUTH_CODE ?? "",
      },
    },
  });

  try {
    // Calls that hit auth_required can now trigger one login attempt automatically.
  } finally {
    await client.close();
  }
}

void main();
```

## Models

Models are discovered through `client.models`. Use `model_ref` from the returned descriptor in completion and agent requests. Treat `model_ref` as opaque; do not parse or construct it in application code.

```ts
import { createMakaiClient } from "oap-sdk";

async function main(): Promise<void> {
  const client = await createMakaiClient();

  try {
    const { models, fetched_at_ms, cache_max_age_ms } = await client.models.list({
      provider_id: "anthropic",
      include_login_required: true,
    });

    console.log(`Fetched ${models.length} models at ${new Date(fetched_at_ms).toISOString()}`);
    console.log(`Cache max age: ${cache_max_age_ms}ms`);

    for (const model of models) {
      console.log(`${model.display_name}: ${model.model_ref} [${model.auth_status}]`);
    }

    const resolved = await client.models.resolve({
      provider_id: "anthropic",
      api: "anthropic-messages",
      model_id: "claude-sonnet-4-5",
    });

    console.log("Use this model_ref:", resolved.model.model_ref);
  } finally {
    await client.close();
  }
}

void main();
```

## Configuration

`createOapClient(...)` and `createMakaiClient(...)` accept stdio transport and binary resolver options. The low-level `createMakaiStdioClient(...)` and `createMakaiAuthClient(...)` remain legacy-wire only.

### Explicit binary path

```ts
import { createMakaiClient } from "oap-sdk";

async function main(): Promise<void> {
  const client = await createMakaiClient({
    resolver: {
      binaryPath: "/opt/oapx/bin/oapx",
    },
  });

  try {
    // Use client.provider, client.agent, client.auth, or client.models here.
  } finally {
    await client.close();
  }
}

void main();
```

You can also set `OAP_SDK_BINARY_PATH=/opt/oapx/bin/oapx`.

### Download from URL with checksum

```ts
import { createMakaiClient } from "oap-sdk";

async function main(): Promise<void> {
  const client = await createMakaiClient({
    resolver: {
      binaryUrl: "https://example.com/releases/oapx-darwin-arm64",
      checksumSha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
      cacheDir: "/tmp/oapx-bin-cache",
    },
  });

  try {
    // Use client.provider, client.agent, client.auth, or client.models here.
  } finally {
    await client.close();
  }
}

void main();
```

Environment variable equivalents are `OAP_SDK_BINARY_URL` and `OAP_SDK_BINARY_SHA256`.

### PATH lookup and local builds

With no resolver options, the SDK checks, in order:

1. The `@oap-sdk/cli-<platform>-<arch>` package, when it is installed
2. `./zig-out/bin/oapx`, then `./zig/zig-out/bin/oapx`
3. `oapx` on `PATH`

On Windows the executable name is `oapx.exe`.

Step 1 outranks both local build paths, so an installed platform package wins over a fresh `zig build`. It is no longer an optional dependency of `oap-sdk`, so it is only consulted when you install it yourself. Set `OAP_SDK_BINARY_PATH` (or `resolver.binaryPath`) to pin an exact binary.

There is no legacy `ready` frame on OAP. The client initializes the agent profile and describes the provider profile. `responseTimeoutMs` bounds provider and agent replies; `frameTimeoutMs` bounds auth event waits. A failed connection closes the spawned process.

```ts
import { createMakaiClient } from "oap-sdk";

async function main(): Promise<void> {
  const client = await createMakaiClient({
    // Optional transport settings:
    args: ["serve", "agent,provider", "--stdio"],
    cwd: process.cwd(),
    env: { ...process.env, OAPX_LOG: "info" },
    handshakeTimeoutMs: 2_000,
    responseTimeoutMs: 30_000,
    frameTimeoutMs: 30_000,
  });

  try {
    // Use client.provider, client.agent, client.auth, or client.models here.
  } finally {
    await client.close();
  }
}

void main();
```

Close clients when done:

```ts
async function closeClient(client: { close(): Promise<void> }): Promise<void> {
  await client.close();
}
```

## Cancellation

Pass an `AbortSignal` as `options.signal` to cancel a `provider` or `agent` call; these reject with an `Error` whose `name` is `"AbortError"`. `client.auth.login(providerId, handlers, { signal })` also accepts a signal and reports cancellation as `MakaiAuthError` with `kind: "cancelled"`.

```ts
import { createMakaiClient, isAbortError } from "oap-sdk";

async function main(): Promise<void> {
  const client = await createMakaiClient();
  const { model } = await client.models.resolve({
    provider_id: "anthropic",
    api: "anthropic-messages",
    model_id: "claude-sonnet-4-5",
  });

  const controller = new AbortController();
  setTimeout(() => controller.abort(), 5_000);

  try {
    for await (const event of client.provider.stream({
      model_ref: model.model_ref,
      messages: [{ role: "user", content: "Explain lock-free queues." }],
      options: { signal: controller.signal },
    })) {
      if (event.type === "text_delta") process.stdout.write(event.delta);
    }
  } catch (error: unknown) {
    if (!isAbortError(error)) throw error;
  }
}

void main;
```

Leaving an OAP stream early also cancels it: the SDK sends a best-effort `inference.cancel.request` for direct inference or `run.cancel.request` for an agent run. Explicit legacy mode uses its original `abort_request`/`agent_stop` frames.

## Error handling

The SDK exports error classes for common failure surfaces:

- `MakaiStreamError` — provider, stream, transport, or unknown failures while running `provider` or `agent` calls, including a call made on a closed transport (`kind: "transport_error"`). Aborts do **not** use this class; see [Cancellation](#cancellation).
- `MakaiAuthRequiredError` — specialized `MakaiStreamError` for `auth_required` failures. It includes `provider_id`.
- `MakaiProtocolError` — models API protocol failures such as `invalid_request`, malformed responses, or request `nack`s. A `client.models` call made on a closed transport rejects with a plain `Error` instead.
- `MakaiAuthError` — auth provider listing and login failures. The `kind` can be `provider_error`, `cancelled`, `transport_error`, or `unknown`.
- `StdioProtocolError` — handshake failures from `connect()`, such as a protocol `version_mismatch`. A handshake timeout rejects with a plain `Error`.

```ts
import {
  MakaiAuthError,
  MakaiAuthRequiredError,
  MakaiProtocolError,
  MakaiStreamError,
  createMakaiClient,
} from "oap-sdk";

async function run(): Promise<void> {
  const client = await createMakaiClient();

  try {
    const { model } = await client.models.resolve({
      provider_id: "anthropic",
      api: "anthropic-messages",
      model_id: "claude-sonnet-4-5",
    });

    await client.provider.complete({
      model_ref: model.model_ref,
      messages: [{ role: "user", content: "Hello" }],
    });
  } catch (error: unknown) {
    if (error instanceof MakaiAuthRequiredError) {
      console.error(`Login required for ${error.provider_id}`);
    } else if (error instanceof MakaiStreamError) {
      console.error(`Stream failed (${error.kind}/${error.code ?? "no-code"}): ${error.message}`);
    } else if (error instanceof MakaiProtocolError) {
      console.error(`Protocol failed (${error.code ?? "no-code"}): ${error.message}`);
    } else if (error instanceof MakaiAuthError) {
      console.error(`Auth failed (${error.kind}/${error.code ?? "no-code"}): ${error.message}`);
    } else {
      throw error;
    }
  } finally {
    await client.close();
  }
}

void run();
```

## TypeScript types

Public types are exported from the package root and generated in `dist/src/index.d.ts` when you run `npm run build:sdk`. Key API types include:

```ts
import type {
  AgentRunRequest,
  AgentRunResponse,
  AgentStreamEvent,
  AuthFlowHandlers,
  AuthStatus,
  BinaryResolverOptions,
  ChatMessage,
  CompletionResponse,
  ContentPart,
  CreateMakaiClientOptions,
  ListModelsRequest,
  ListModelsResponse,
  MakaiAgentApi,
  MakaiAgentModelsApi,
  MakaiAuthApi,
  MakaiClient,
  MakaiModelsApi,
  MakaiProviderApi,
  ModelDescriptor,
  ProviderCompleteRequest,
  ProviderCompleteResponse,
  ProviderStreamEvent,
  RunOptions,
  TextContentPart,
  ToolDefinition,
  UsageSummary,
} from "oap-sdk";
```

Most requests share this shape:

```ts
import type { ContentPart, TextContentPart } from "oap-sdk";

type ChatMessage = {
  role: "system" | "developer" | "user" | "assistant" | "tool";
  content: string | ContentPart[];
  name?: string;
  tool_call_id?: string;
};

type ToolDefinition = {
  name: string;
  description: string;
  parameters_schema_json: string;
  execute?: (
    args: Record<string, unknown>,
    context: { tool_call_id: string; tool_name: string; args_json: string },
  ) => Promise<string | TextContentPart[]> | string | TextContentPart[];
};

type RunOptions = {
  temperature?: number;
  max_tokens?: number;
  reasoning_effort?: "off" | "minimal" | "low" | "medium" | "high" | "xhigh";
  auth_retry_policy?: "manual" | "auto_once";
  /**
   * OAP session identity. Use `agent.switchModel(session_id, model_ref)` to
   * select a new model mid-session, then `agent.runSelected(session_id, messages)`.
   */
  session_id?: string;
  metadata?: Record<string, string>;
  signal?: AbortSignal;
};
```

Core namespaces:

```ts
import type {
  MakaiAuthApi,
  MakaiModelsApi,
  MakaiAgentModelsApi,
  MakaiProviderApi,
} from "oap-sdk";

type MakaiClient = {
  auth: MakaiAuthApi;
  models: MakaiModelsApi;
  agent: MakaiAgentModelsApi; // extends MakaiAgentApi with a `models` convenience alias
  provider: MakaiProviderApi;
  close(): Promise<void>;
};
```
