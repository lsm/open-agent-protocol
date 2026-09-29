import test from "node:test";
import assert from "node:assert/strict";
import { join } from "node:path";
import { createOapClient } from "../src";

const fixture = join(process.cwd(), "sdk/typescript/test/fixtures/oap-server.js");

test("a model entry carries the facts the provider published", async () => {
  const client = await createOapClient({ command: process.execPath, args: [fixture] });
  try {
    const listed = await client.models.list();
    const model = listed.models[0];
    assert.ok(model, "the listing returned no models");

    assert.deepEqual(model.cost, { input: 3, output: 15, cache_read: 0.3, cache_write: 3.75 });
    assert.deepEqual(model.input_modalities, ["text", "image"]);
    assert.deepEqual(model.reasoning_levels, ["off", "medium", "high"]);
    assert.equal(model.release_date, "2025-09-29");
    assert.equal(model.family, "mock-family");
    assert.deepEqual(listed.catalog, { observed_at_ms: 1_759_100_000_000, complete: true });
  } finally {
    await client.close();
  }
});

test("an absent fact stays absent rather than becoming an empty value", async () => {
  const client = await createOapClient({ command: process.execPath, args: [fixture] });
  try {
    const listed = await client.models.list();
    const model = listed.models[0];
    assert.ok(model, "the listing returned no models");

    assert.equal(
      Object.hasOwn(model, "output_modalities"),
      false,
      "output_modalities is present on a model whose provider published none: an absent list is unknown, and an empty one reads as a claim that the model takes no output",
    );
    assert.ok(Object.hasOwn(model, "cost"), "cost is missing from an entry that published one, which would mean the mapping dropped it");
  } finally {
    await client.close();
  }
});
