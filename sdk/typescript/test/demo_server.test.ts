import assert from "node:assert/strict";
import { promises as fs } from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { startDemoServer } from "../demo/server";

const sourceFixturesDir = path.resolve(__dirname, "../../sdk/typescript/test/fixtures");
const oapFixture = path.join(sourceFixturesDir, "oap-server.js");

test("demo: serves UI and metadata", async () => {
  const tempHome = await fs.mkdtemp(path.join(os.tmpdir(), "makai-demo-home-"));
  const running = await startDemoServer({
    port: 0,
    command: process.execPath,
    args: [oapFixture],
    homeDir: tempHome,
  });
  try {
    const indexRes = await fetch(`${running.url}/`);
    assert.equal(indexRes.status, 200);
    const html = await indexRes.text();
    assert.equal(html.includes("Makai TS SDK Demo"), true);

    const metaRes = await fetch(`${running.url}/api/meta`);
    assert.equal(metaRes.status, 200);
    const meta = (await metaRes.json()) as {
      oauthProviders: Array<{ id: string }>;
      chatProviders: Array<{ id: string; authenticated: boolean }>;
    };
    assert.equal(meta.oauthProviders.some((provider) => provider.id === "fixture"), true);
    assert.equal(meta.chatProviders.some((provider) => provider.id === "test-fixture"), true);
  } finally {
    await running.close();
    await fs.rm(tempHome, { recursive: true, force: true });
  }
});

test("demo: fixture chat works without configured Makai runtime", async () => {
  const tempHome = await fs.mkdtemp(path.join(os.tmpdir(), "makai-demo-home-"));
  const running = await startDemoServer({
    port: 0,
    homeDir: tempHome,
    binaryPath: "",
    env: { OAP_SDK_BINARY_PATH: undefined },
  });
  try {
    const response = await fetch(`${running.url}/api/chat`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        provider: "test-fixture",
        model: "fixture-echo-v1",
        message: "binary free",
      }),
    });
    assert.equal(response.status, 200);
    const payload = (await response.json()) as { reply: string };
    assert.equal(payload.reply, "[fixture-echo-v1] eerf yranib");
  } finally {
    await running.close();
    await fs.rm(tempHome, { recursive: true, force: true });
  }
});
