const assert = require('node:assert/strict');
const { createOapClient } = require(require('node:path').join(process.cwd(), 'dist/src/oap_client.js'));
(async () => {
  const client = await createOapClient({ command: process.argv[2], args: ['serve', 'agent', '--backend', 'codex', '--config', process.argv[3]] });
  const events = [];
  try {
    for await (const event of client.agent.stream({ model_ref: 'fixture/openai-responses@glm-test', messages: [{ role: 'user', content: 'read the code' }] })) {
      if (event.type === 'tool_execution_end') events.push(event);
    }
  } finally {
    await client.close();
  }
  console.log(JSON.stringify(events));
  assert.equal(events.length, 8);
  for (const [i, event] of events.entries()) {
    assert.equal(event.is_error, i !== 0 && i !== 5, `command outcome ${i}`);
    if (i < 6) assert.equal(event.result?.exitCode, [0, 1, -7, null, null, 0][i], `exit evidence ${i}`);
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
