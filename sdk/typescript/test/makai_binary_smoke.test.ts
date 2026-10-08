import assert from "node:assert/strict";
import { promises as fs } from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { createMakaiClient, MakaiAuthError } from "../src";

const binaryPath = process.env.OAP_SDK_BINARY_PATH;

test("e2e: connect to oapx serve agent,provider over stdio", async (t) => {
  if (!binaryPath) {
    t.skip("OAP_SDK_BINARY_PATH is not set");
    return;
  }

  const client = await createMakaiClient({
    resolver: { binaryPath },
    handshakeTimeoutMs: 5000,
  });
  await client.close();
});

test("e2e: a manual login fails closed and persists no credentials", async (t) => {
  if (!binaryPath) {
    t.skip("OAP_SDK_BINARY_PATH is not set");
    return;
  }

  const tempHome = await fs.mkdtemp(path.join(os.tmpdir(), "oapx-auth-home-"));
  const client = await createMakaiClient({
    resolver: { binaryPath },
    env: { ...process.env, HOME: tempHome, OAPX_TEST_FIXTURE_PROVIDER: "1" },
    handshakeTimeoutMs: 5000,
  });
  try {
    await assert.rejects(
      () => client.auth.login("test-fixture"),
      (error: unknown) => error instanceof MakaiAuthError && error.code === "auth_input_unavailable",
    );
    await assert.rejects(() => fs.access(path.join(tempHome, ".oapx", "auth.json")));
  } finally {
    await client.close();
    await fs.rm(tempHome, { recursive: true, force: true });
  }
});

test("e2e: a runtime that was not asked for the fixture does not serve it", async (t) => {
  if (!binaryPath) {
    t.skip("OAP_SDK_BINARY_PATH is not set");
    return;
  }

  const client = await createMakaiClient({
    resolver: { binaryPath },
    handshakeTimeoutMs: 5000,
  });
  try {
    const providers = await client.auth.listProviders();
    assert.ok(providers.length > 0);
    assert.ok(
      !providers.some((provider) => provider.id === "test-fixture"),
      `a user who did not set the opt-in was offered the CI fixture: ${JSON.stringify(providers)}`,
    );
  } finally {
    await client.close();
  }
});
