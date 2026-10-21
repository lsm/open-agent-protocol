const assert = require('node:assert/strict');
const { createOapClient } = require(require('node:path').join(process.cwd(), 'dist/src/oap_client.js'));
(async () => {
  const other = process.argv[3] === '--non-codex';
  const args = other ? ['sdk/typescript/test/fixtures/oap-server.js'] : ['serve', 'agent', '--backend', 'codex', '--config', process.argv[3]];
  const client = await createOapClient({ command: process.argv[2], args });
  const events = [];
  try {
    for await (const event of client.agent.stream({ model_ref: other ? 'fixture/openai-responses@bash-no-exit' : 'fixture/openai-responses@glm-test', messages: [{ role: 'user', content: 'read the code' }] })) {
      if (event.type === 'tool_execution_end') events.push(event);
    }
  } finally {
    await client.close();
  }
  console.log(JSON.stringify(events));
  if (other) {
    assert.equal(events.length, 2);
    assert.equal(events[0].is_error, false, 'non-Codex Bash without exitCode changed outcome');
    assert.equal(events[1].is_error, false);
    assert.equal(events[1].result, null);
    return;
  }
  assert.equal(events.length, 8);
  for (const [i, event] of events.entries()) {
    assert.equal(event.is_error, i !== 0 && i !== 5, `command outcome ${i}`);
    if (i < 6) assert.equal(event.result?.exitCode, [0, 1, -7, null, null, 0][i], `exit evidence ${i}`);
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
