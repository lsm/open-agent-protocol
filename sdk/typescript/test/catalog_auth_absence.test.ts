import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";
import {
  createOapClient,
  MakaiProtocolError,
  type MakaiModelsApi,
} from "../src";

const fixturesDir = path.resolve(__dirname, "../../sdk/typescript/test/fixtures");
const oapFixtureScript = path.join(fixturesDir, "oap-server.js");

async function withOapModels<T>(
  auth: string,
  use: (api: MakaiModelsApi) => Promise<T>,
  lifecycle?: string,
): Promise<T> {
  const client = await createOapClient({
    command: process.execPath,
    args: [oapFixtureScript],
    env: { ...process.env, OAP_FIXTURE_AUTH: auth, OAP_FIXTURE_LIFECYCLE: lifecycle ?? "" },
  });
  try {
    return await use(client.models);
  } finally {
    await client.close();
  }
}

async function refusesMalformedAuthStatus(
  auth: string,
  use: (api: MakaiModelsApi) => Promise<unknown>,
): Promise<void> {
  let caught: unknown;
  try {
    await withOapModels(auth, use);
  } catch (error) {
    caught = error;
  }
  assert.ok(
    caught instanceof MakaiProtocolError,
    `a present but invalid auth_status must be refused, got ${String(caught)}`,
  );
  assert.equal(
    (caught as MakaiProtocolError).code,
    "malformed_response",
    "a refusal must carry the malformed_response code",
  );
  assert.match(
    (caught as MakaiProtocolError).message,
    /auth_status must be one of/,
  );
}

test("an absent OAP auth_status reads as the existing unknown value", async () => {
  const models = await withOapModels("absent", async (api) => (await api.list()).models);
  assert.equal(models.length, 1);
  assert.equal(models[0].auth_status, "unknown");
});

test("every schema auth_status literal reaches the OAP reader as itself", async () => {
  for (const literal of [
    "authenticated",
    "login_required",
    "expired",
    "refreshing",
    "login_in_progress",
    "failed",
    "unknown",
  ] as const) {
    const models = await withOapModels(
      literal,
      async (api) => (await api.list({ include_login_required: true })).models,
    );
    assert.equal(models.length, 1, `${literal} must survive the reader`);
    assert.equal(models[0].auth_status, literal);
  }
});

test("the model_id filter really does skip the valid fixture row", async () => {
  const models = await withOapModels(
    "authenticated",
    async (api) => (await api.list({ model_id: "no-such-model" })).models,
  );
  assert.equal(
    models.length,
    0,
    "a local model_id filter matching nothing must yield no models",
  );
});

test("a present but invalid OAP auth_status is refused", async () => {
  for (const shape of ["null", "number", "empty", "invented"]) {
    await refusesMalformedAuthStatus(shape, (api) => api.list());
  }
});

test("a malformed auth_status is refused even when a local filter would skip the row", async () => {
  await refusesMalformedAuthStatus("invented", (api) =>
    api.list({ model_id: "no-such-model" }),
  );
  await refusesMalformedAuthStatus("null", (api) =>
    api.list({ model_id: "no-such-model" }),
  );
});

test("the include_deprecated filter really does skip a deprecated row", async () => {
  const kept = await withOapModels(
    "authenticated",
    async (api) => (await api.list({ include_deprecated: true })).models,
    "deprecated",
  );
  assert.equal(kept.length, 1, "a deprecated row survives when the filter allows it");
  assert.equal(kept[0].lifecycle, "deprecated");

  const dropped = await withOapModels(
    "authenticated",
    async (api) => (await api.list({ include_deprecated: false })).models,
    "deprecated",
  );
  assert.equal(
    dropped.length,
    0,
    "include_deprecated: false must skip the deprecated row",
  );
});

test("an unrecognized OAP_FIXTURE_AUTH selector fails the fixture, not the reader", async () => {
  for (const selector of ["stated:expired", "no-such-selector", "expired "]) {
    await assert.rejects(
      () => withOapModels(selector, (api) => api.list()),
      /OAP process exited/,
      `selector ${selector} must be reported by the fixture, never silently defaulted`,
    );
  }
});

test("a malformed auth_status is refused even when include_deprecated would skip it", async () => {
  let caught: unknown;
  try {
    await withOapModels(
      "invented",
      (api) => api.list({ include_deprecated: false }),
      "deprecated",
    );
  } catch (error) {
    caught = error;
  }
  assert.ok(
    caught instanceof MakaiProtocolError,
    `a malformed auth_status must be refused under an excluding filter, got ${String(caught)}`,
  );
  assert.equal((caught as MakaiProtocolError).code, "malformed_response");
});
