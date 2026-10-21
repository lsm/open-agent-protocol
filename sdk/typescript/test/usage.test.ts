import assert from "node:assert/strict";
import test from "node:test";
import { join } from "node:path";
import { createOapClient, MakaiProtocolError, type MakaiClient } from "../src/index";

const fixture = join(process.cwd(), "sdk/typescript/test/fixtures/oap-server.js");
const fields = {
  input_tokens: "input", output_tokens: "output", total_tokens: "total",
  cache_read_input_tokens: "cache_read", cache_write_input_tokens: "cache_write", reasoning_tokens: "reasoning",
} as const;

async function observe(client: MakaiClient, agent: boolean, stream: boolean): Promise<unknown> {
  const request = { model_ref: "fixture/openai-responses@mock", messages: [{ role: "user" as const, content: "usage" }] };
  if (agent) {
    if (!stream) return (await client.agent.run(request)).usage;
    for await (const event of client.agent.stream(request)) if (event.type === "agent_end") return event.usage;
  } else {
    if (!stream) return (await client.provider.complete(request)).usage;
    for await (const event of client.provider.stream(request)) if (event.type === "message_end") return event.usage;
  }
  throw new Error("missing terminal");
}

async function consumer(raw: string, expected: unknown): Promise<void> {
  const client = await createOapClient({ command: process.execPath, args: [fixture], env: { OAP_FIXTURE_USAGE: raw } });
  try {
    for (const agent of [false, true]) for (const stream of [false, true]) {
      assert.deepEqual(await observe(client, agent, stream), expected, `agent=${agent} stream=${stream} usage=${raw}`);
    }
  } finally { await client.close(); }
}

test("public APIs preserve absent usage, partial counts and independent measured zeros", async () => {
  await consumer("absent", undefined);
  await consumer("{}", {});
  for (const [wire, field] of Object.entries(fields)) await consumer(`{"${wire}":0}`, { [field]: 0 });
  await consumer('{"input_tokens":0,"total_tokens":17}', { input: 0, total: 17 });
});

test("public APIs retain the safe boundary and signal larger counts without failing the response", async () => {
  for (const [wire, field] of Object.entries(fields)) {
    await consumer(`{"${wire}":9007199254740991}`, { [field]: Number.MAX_SAFE_INTEGER });
    for (const value of ["9007199254740992", "18446744073709551615"]) {
      await consumer(`{"${wire}":${value}}`, { unrepresentable_counts: [{ code: "unrepresentable_count", field: wire, value }] });
    }
  }
  await consumer('{"input_tokens":18446744073709551615,"output_tokens":0}', {
    output: 0, unrepresentable_counts: [{ code: "unrepresentable_count", field: "input_tokens", value: "18446744073709551615" }],
  });
});

test("public APIs refuse null, invalid and non-integer-spelling counts with typed errors", async () => {
  const cases = ["null", "[]", "true", '"usage"', "3"];
  for (const field of Object.keys(fields)) for (const value of ["null", "-1", "1.5", "1.0", "1e3", "true", '"3"', "[]", "{}", "18446744073709551616"]) {
    cases.push(`{"${field}":${value}}`);
  }
  for (const raw of cases) {
    const client = await createOapClient({ command: process.execPath, args: [fixture], env: { OAP_FIXTURE_USAGE: raw } });
    try {
      for (const agent of [false, true]) for (const stream of [false, true]) {
        await assert.rejects(() => observe(client, agent, stream), (error: unknown) =>
          error instanceof MakaiProtocolError && error.code === "malformed_response", `usage=${raw}`);
      }
    } finally { await client.close(); }
  }
});
