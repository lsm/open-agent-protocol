import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { uint64RuntimeConsumer, uint64TypeCases } from './fixtures/uint64-consumer.js';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');

test('installed public client retains uniform exact uint64 values', async (t) => {
  const temporary = mkdtempSync(join(tmpdir(), 'oap-ts-uint64-'));
  t.after(() => rmSync(temporary, { recursive: true, force: true }));
  const consumer = join(temporary, 'consumer');
  mkdirSync(consumer);
  writeFileSync(join(consumer, 'package.json'), JSON.stringify({ private: true, type: 'module' }));
  const evidence = process.env.OAP_TS_UINT64_OUTPUT;
  if (evidence) mkdirSync(evidence, { recursive: true });
  const run = (command: string, args: string[], cwd = consumer) => {
    const result = spawnSync(command, args, { cwd, encoding: 'utf8', timeout: 60000 });
    assert.ifError(result.error);
    return result;
  };
  const pack = run('npm', ['pack', '--ignore-scripts', '--json', '--pack-destination', temporary], root);
  assert.equal(pack.status, 0, pack.stdout + pack.stderr);
  const archive = (JSON.parse(pack.stdout) as { filename: string }[])[0]?.filename;
  assert.ok(archive);
  const installed = run('npm', ['install', '--offline', '--ignore-scripts', '--no-audit', '--no-fund', '--package-lock=false', join(temporary, archive)]);
  assert.equal(installed.status, 0, installed.stdout + installed.stderr);
  await t.test('runtime values and round-trips', () => {
    const path = join(consumer, 'runtime.mjs');
    writeFileSync(path, uint64RuntimeConsumer);
    const result = run(process.execPath, [path]);
    if (evidence) {
      writeFileSync(join(evidence, 'runtime.mjs'), uint64RuntimeConsumer);
      writeFileSync(join(evidence, 'runtime.log'), result.stdout + result.stderr);
    }
    assert.equal(result.status, 0, result.stdout + result.stderr);
  });
  for (const sample of uint64TypeCases) await t.test(sample.name, () => {
    const path = join(consumer, 'consumer.ts');
    writeFileSync(path, sample.source);
    const result = run(process.execPath, [join(root, 'node_modules/typescript/bin/tsc'), '--noEmit', '--strict', '--skipLibCheck',
      '--module', 'NodeNext', '--moduleResolution', 'NodeNext', '--target', 'ES2022', '--types', 'node', '--typeRoots', join(root, 'node_modules/@types'), path]);
    if (evidence) {
      writeFileSync(join(evidence, sample.name + '.ts'), sample.source);
      writeFileSync(join(evidence, sample.name + '.log'), result.stdout + result.stderr);
    }
    if (sample.accepts) assert.equal(result.status, 0, result.stdout + result.stderr);
    else {
      assert.equal(result.status, 2, result.stdout + result.stderr);
      assert.match(result.stdout, /error TS(2322|2345):/);
    }
  });
});
