import assert from "node:assert/strict";
import test from "node:test";
import { flattenAuthEvent } from "../src";

test("flattenAuthEvent normalizes every Zig union wire variant", () => {
  assert.deepEqual(flattenAuthEvent({
    auth_url: {
      flow_id: "00000000000000000000000000",
      provider_id: "fixture",
      url: "https://example.test/auth",
      instructions: "open browser",
    },
  }), {
    type: "auth_url",
    flow_id: "00000000000000000000000000",
    provider_id: "fixture",
    url: "https://example.test/auth",
    instructions: "open browser",
  });

  assert.deepEqual(flattenAuthEvent({
    prompt: {
      flow_id: "00000000000000000000000001",
      prompt_id: "device_code",
      provider_id: "fixture",
      message: "enter code",
      allow_empty: false,
    },
  }), {
    type: "prompt",
    flow_id: "00000000000000000000000001",
    prompt_id: "device_code",
    provider_id: "fixture",
    message: "enter code",
    allow_empty: false,
  });

  assert.deepEqual(flattenAuthEvent({
    progress: {
      flow_id: "00000000000000000000000002",
      provider_id: "fixture",
      message: "waiting",
    },
  }), {
    type: "progress",
    flow_id: "00000000000000000000000002",
    provider_id: "fixture",
    message: "waiting",
  });

  assert.deepEqual(flattenAuthEvent({
    success: {
      flow_id: "00000000000000000000000003",
      provider_id: "fixture",
    },
  }), {
    type: "success",
    flow_id: "00000000000000000000000003",
    provider_id: "fixture",
  });

  assert.deepEqual(flattenAuthEvent({
    error: {
      flow_id: "00000000000000000000000004",
      provider_id: "fixture",
      code: "auth_failed",
      message: "boom",
    },
  }), {
    type: "error",
    flow_id: "00000000000000000000000004",
    provider_id: "fixture",
    code: "auth_failed",
    message: "boom",
  });
});
