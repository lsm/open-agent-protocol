import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import {
  createMakaiModelsApi,
  createOapClient,
  MakaiStdioClient,
  type MakaiModelsApi,
} from "../src";

const sourceFixturesDir = path.resolve(__dirname, "../../sdk/typescript/test/fixtures");
const oapFixtureScript = path.join(sourceFixturesDir, "oap-server.js");
const legacyFixtureScript = path.join(sourceFixturesDir, "models-server.js");

function descriptor(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    model_ref: "anthropic/anthropic-messages@claude-sonnet-4-5",
    model_id: "claude-sonnet-4-5",
    display_name: "Claude Sonnet 4.5",
    provider_id: "anthropic",
    api: "anthropic-messages",
    auth_status: "authenticated",
    lifecycle: "stable",
    capabilities: ["chat"],
    source: "static_fallback",
    ...overrides,
  };
}

async function withLegacyModels<T>(
  model: Record<string, unknown>,
  use: (api: MakaiModelsApi) => Promise<T>,
): Promise<T> {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "oap-source-test-"));
  const responsePath = path.join(tmpDir, "response.json");
  fs.writeFileSync(
    responsePath,
    JSON.stringify({
      models: [model],
      fetched_at_ms: 1_760_000_000_198,
      cache_max_age_ms: 300_000,
    }),
  );
  const client = new MakaiStdioClient({
    command: process.execPath,
    args: [legacyFixtureScript],
    env: { ...process.env, OAP_SDK_TEST_RESPONSE_PATH: responsePath },
    handshakeTimeoutMs: 5000,
  });
  try {
    await client.connect();
    return await use(createMakaiModelsApi(client));
  } finally {
    await client.close();
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
}

async function withOapModels<T>(
  stating: string,
  use: (api: MakaiModelsApi) => Promise<T>,
): Promise<T> {
  const client = await createOapClient({
    command: process.execPath,
    args: [oapFixtureScript],
    env: { ...process.env, OAP_FIXTURE_SOURCE: stating },
  });
  try {
    return await use(client.models);
  } finally {
    await client.close();
  }
}

test("the shared reader records an absent source as unknown", async () => {
  const model = descriptor();
  delete model.source;
  const models = await withLegacyModels(model, async (api) => (await api.list()).models);
  assert.equal(models.length, 1);
  assert.equal(
    models[0].source,
    undefined,
    "an omitted source must stay unknown, not become a chosen value",
  );
  assert.ok(
    !("source" in models[0]),
    "an unknown source must not be written into the descriptor at all",
  );
});

test("the shared reader keeps a stated source and its mapping", async () => {
  for (const [stated, want] of [
    ["static_fallback", "static_fallback"],
    ["dynamic", "dynamic"],
  ] as const) {
    const models = await withLegacyModels(
      descriptor({ source: stated }),
      async (api) => (await api.list()).models,
    );
    assert.equal(models[0].source, want, `a stated ${stated} must keep its mapping`);
  }
});

test("the shared reader refuses a present null or wrong-typed source", async () => {
  for (const [value, what] of [
    [null, "an explicit null"],
    [7, "a number"],
    ["invented-source", "an invented literal"],
  ] as const) {
    await assert.rejects(
      () =>
        withLegacyModels(descriptor({ source: value }), async (api) => {
          await api.list();
          return null;
        }),
      (error: unknown) => {
        assert.ok(
          error instanceof Error && /models\[0\]\.source/.test(error.message),
          `${what} must be refused naming the field, got: ${String(error)}`,
        );
        return true;
      },
      `${what} must be a malformed response, not an unknown source`,
    );
  }
});

test("the OAP reader records an absent source as unknown", async () => {
  const models = await withOapModels("absent", async (api) => (await api.list()).models);
  assert.equal(models.length, 1);
  assert.equal(models[0].source, undefined, "an unstated source must stay unknown");
  assert.ok(!("source" in models[0]), "no value may be invented for the descriptor");
});

test("the OAP reader maps a stated source to its shared vocabulary", async () => {
  for (const [stated, want] of [
    ["discovered", "dynamic"],
    ["fallback", "static_fallback"],
  ] as const) {
    const models = await withOapModels(stated, async (api) => (await api.list()).models);
    assert.equal(models[0].source, want, `${stated} must read as ${want}`);
  }
});

test("the OAP reader refuses a present null or unknown source", async () => {
  for (const [stating, what] of [
    ["null", "an explicit null"],
    ["number", "a number"],
    ["invented", "an invented literal"],
  ] as const) {
    await assert.rejects(
      () =>
        withOapModels(stating, async (api) => {
          await api.list();
          return null;
        }),
      (error: unknown) => {
        assert.ok(
          error instanceof Error && /source/.test(error.message),
          `${what} must be refused naming source, got: ${String(error)}`,
        );
        return true;
      },
      `${what} must be a malformed response on the OAP path too`,
    );
  }
});

test("the default fixture still states a source, so no reader regressed to unknown", async () => {
  const models = await withOapModels("", async (api) => (await api.list()).models);
  assert.equal(models[0].source, "dynamic", "a stated discovered source still reads as dynamic");
});

test("the OAP reader refuses the shared aliases, which are not wire values", async () => {
  for (const [stating, alias] of [
    ["alias-dynamic", "dynamic"],
    ["alias-static-fallback", "static_fallback"],
  ] as const) {
    await assert.rejects(
      () =>
        withOapModels(stating, async (api) => {
          await api.list();
          return null;
        }),
      (error: unknown) => {
        assert.ok(
          error instanceof Error && /source/.test(error.message),
          `${alias} must be refused naming source on the wire, got: ${String(error)}`,
        );
        return true;
      },
      `${alias} is the shared vocabulary, not a wire value: the modelSource enum permits discovered and fallback only`,
    );
  }
});

test("the shared reader still accepts the native aliases", async () => {
  for (const [alias, want] of [
    ["dynamic", "dynamic"],
    ["static_fallback", "static_fallback"],
  ] as const) {
    const models = await withLegacyModels(
      descriptor({ source: alias }),
      async (api) => (await api.list()).models,
    );
    assert.equal(
      models[0].source,
      want,
      `${alias} is the native stated vocabulary and must keep decoding on the shared path`,
    );
  }
});
