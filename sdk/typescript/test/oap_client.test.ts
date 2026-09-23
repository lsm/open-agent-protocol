import assert from "node:assert/strict";
import test from "node:test";
import { join } from "node:path";
import { createOapClient, OapUnsupportedFeatureError } from "../src/oap_client";
import { MakaiAuthRequiredError } from "../src/execution_types";

const fixture = join(process.cwd(), "sdk/typescript/test/fixtures/oap-server.js");

test("combined OAP stdio exposes provider models, inference, and agent runs", async () => {
  const client = await createOapClient({ command: process.execPath, args: [fixture] });
  try {
    const models = await client.models.list();
    assert.equal(models.models[0]?.model_ref, "fixture/openai-responses@mock");

    const request = { model_ref: "fixture/openai-responses@mock", messages: [{ role: "user" as const, content: "hello" }] };
    const inference = await client.provider.complete(request);
    assert.equal(inference.message.content, "provider works");
    assert.equal(inference.usage?.output, 2);

    const run = await client.agent.run(request);
    assert.equal(run.message.content, "agent works");
    assert.equal(run.usage?.output, 3);
    await client.agent.switchModel("existing-session", "fixture/openai-responses@switched");
    const selected = await client.agent.runSelected("existing-session", request.messages);
    assert.equal(selected.message.content, "agent works");
    assert.equal(selected.model_id, "switched");
    const streamed = [];
    for await (const event of client.agent.streamSelected("existing-session", request.messages)) streamed.push(event);
    assert.deepEqual(streamed.filter((event) => event.type === "text_delta").map((event) => event.delta), ["agent works"]);
  } finally {
    await client.close();
  }
});

test("unrepresented client tools fail explicitly before a legacy frame is sent", async () => {
  const client = await createOapClient({ command: process.execPath, args: [fixture] });
  try {
    await assert.rejects(() => client.agent.run({
      model_ref: "fixture/openai-responses@mock",
      messages: [{ role: "user", content: "hello" }],
      tools: [{ name: "local", description: "local", parameters_schema_json: "{}", execute: () => "ok" }],
    }), (error: unknown) => error instanceof OapUnsupportedFeatureError && error.code === "unsupported_feature");
    await assert.rejects(() => client.provider.complete({
      model_ref: "fixture/openai-responses@mock",
      messages: [{ role: "user", content: "hello" }],
      tools: [{ name: "local", description: "local", parameters_schema_json: "{}", execute: () => "ok" }],
    }), (error: unknown) => error instanceof OapUnsupportedFeatureError);
    await assert.rejects(() => client.agent.run({
      model_ref: "fixture/openai-responses@mock",
      messages: [{ role: "user", content: "hello" }],
      options: { max_tokens: 32 },
    }), (error: unknown) => error instanceof OapUnsupportedFeatureError);
  } finally {
    await client.close();
  }
});

test("OAP auth discovery, URL, progress, and terminal completion", async () => {
  const client = await createOapClient({ command: process.execPath, args: [fixture] });
  try {
    assert.equal((await client.auth.listProviders())[0]?.auth_status, "login_required");
    const seen: string[] = [];
    await client.auth.login("fixture", {
      onEvent: (event) => seen.push(event.type),
      onPrompt: () => { throw new Error("unexpected OAP prompt"); },
    });
    assert.deepEqual(seen, ["auth_url", "progress", "success"]);
    assert.equal((await client.auth.listProviders())[0]?.auth_status, "authenticated");
  } finally {
    await client.close();
  }
});

test("OAP provider and agent auto-once login retry only after auth rejection", async () => {
  for (const channel of ["provider", "agent"] as const) {
    const client = await createOapClient({ command: process.execPath, args: [fixture], auth: {
      auth_retry_policy: "auto_once", handlers: { onPrompt: () => "code" },
    } });
    try {
      const request = { model_ref: "fixture/openai-responses@needs-login", messages: [{ role: "user" as const, content: "hello" }] };
      const result = channel === "provider" ? await client.provider.complete(request) : await client.agent.run(request);
      assert.equal(result.message.content, channel === "provider" ? "provider works" : "agent works");
      assert.equal((await client.auth.listProviders())[0]?.auth_status, "authenticated");
    } finally {
      await client.close();
    }
  }
});

test("OAP browser login needs no prompt handler", async () => {
  const client = await createOapClient({ command: process.execPath, args: [fixture] });
  try {
    await client.auth.login("fixture");
  } finally {
    await client.close();
  }
});

test("OAP manual prompt never calls an answer handler", async () => {
  const client = await createOapClient({ command: process.execPath, args: [fixture] });
  try {
    let called = false;
    await assert.rejects(() => client.auth.login("manual", {
      onPrompt: () => { called = true; return "SENSITIVE_TEST_CODE"; },
    }), (error: unknown) => error instanceof Error && "code" in error && error.code === "auth_input_unavailable");
    assert.equal(called, false);
  } finally {
    await client.close();
  }
});

test("direct OAP completion preserves structured assistant parts and hides partial tool arguments", async () => {
  const client = await createOapClient({ command: process.execPath, args: [fixture] });
  const request = { model_ref: "fixture/openai-responses@structured", messages: [{ role: "user" as const, content: "hello" }] };
  try {
    const completed = await client.provider.complete(request);
    assert.deepEqual(completed.message.content, [
      { type: "thinking", thinking: "thinking", thinking_signature: "reasoning-carry" },
      { type: "tool_call", tool_call_id: "call-1", name: "lookup", arguments_json: '{"city":"Paris"}', carry: "tool-carry" },
      { type: "tool_result", tool_call_id: "call-0", tool_name: "", content: "prior result", is_error: false },
      { type: "image", data: "aGVsbG8=", mime_type: "image/png" },
      { type: "text", text: "answer" },
    ]);
    const events = [];
    for await (const event of client.provider.stream(request)) events.push(event);
    assert.deepEqual(events.filter((event) => event.type === "text_delta").map((event) => event.delta), ["provider works"]);
    assert.deepEqual(events.filter((event) => event.type === "tool_call").map((event) => event.arguments_json), ['{"city":"Paris"}']);
  } finally { await client.close(); }
});

test("direct OAP request uses JSON input_schema and metadata, and correlates tool result follow-up", async () => {
  const client = await createOapClient({ command: process.execPath, args: [fixture] });
  try {
    const completed = await client.provider.complete({
      model_ref: "fixture/openai-responses@echo",
      messages: [
        { role: "assistant", content: [{ type: "tool_call", tool_call_id: "call-1", name: "lookup", arguments_json: '{"city":"Paris"}', carry: "tool-carry" }] },
        { role: "tool", tool_call_id: "call-1", content: "sunny" },
      ],
      tools: [{ name: "lookup", description: "City lookup", parameters_schema_json: '{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}' }],
      options: { metadata: { trace: "test" } },
    });
    assert.equal(typeof completed.message.content, "string");
    const sent = JSON.parse(completed.message.content as string);
    assert.deepEqual(sent.tools, [{ name: "lookup", description: "City lookup", input_schema: { type: "object", properties: { city: { type: "string" } }, required: ["city"] } }]);
    assert.deepEqual(sent.metadata, { trace: "test" });
    assert.equal(sent.metadata_json, undefined);
    assert.deepEqual(sent.messages[0].content, [{ type: "tool_call", tool_call_id: "call-1", name: "lookup", arguments_json: { city: "Paris" }, carry: "tool-carry" }]);
    assert.deepEqual(sent.messages[1], { role: "tool", content: [{ type: "tool_result", tool_call_id: "call-1", result: "sunny" }] });
  } finally { await client.close(); }
});

test("OAP credential_rejected is a typed auth-required error", async () => {
  const client = await createOapClient({ command: process.execPath, args: [fixture] });
  try {
    await assert.rejects(() => client.provider.complete({
      model_ref: "fixture/openai-responses@credential-rejected", messages: [{ role: "user", content: "hello" }],
    }), (error: unknown) => error instanceof MakaiAuthRequiredError && error.provider_id === "fixture");
  } finally { await client.close(); }
});

test("malformed OAP frames do not disclose their contents in diagnostics", async () => {
  const client = await createOapClient({ command: process.execPath, args: [fixture] });
  try {
    await assert.rejects(() => client.provider.complete({
      model_ref: "fixture/openai-responses@malformed-frame", messages: [{ role: "user", content: "hello" }],
    }), (error: unknown) => error instanceof Error && error.message === "invalid OAP frame" && !error.message.includes("secret-code"));
  } finally { await client.close(); }
});

test("real combined oapx exposes both profiles and auth status without login or inference", {
  skip: !process.env.OAP_SDK_OAP_BINARY_PATH,
}, async () => {
  const client = await createOapClient({ command: process.env.OAP_SDK_OAP_BINARY_PATH });
  try {
    assert.ok((await client.auth.listProviders()).length > 0);
    assert.ok((await client.models.list({ include_login_required: true })).models.length > 0);
  } finally {
    await client.close();
  }
});
