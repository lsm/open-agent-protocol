import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { schemaPrecisionCases } from './fixtures/schema-precision-consumer.js';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');

test('public client declarations enforce transport and shutdown cardinality', async (t) => {
  const temporary = mkdtempSync(join(tmpdir(), 'oap-ts-schema-precision-'));
  t.after(() => rmSync(temporary, { recursive: true, force: true }));
  const evidence = process.env.OAP_TS_SCHEMA_PRECISION_OUTPUT;
  const consumer = join(temporary, 'consumer');
  mkdirSync(consumer);
  writeFileSync(join(consumer, 'package.json'), JSON.stringify({ private: true, type: 'module' }));
  const run = (command: string, args: string[], cwd: string) => {
    const result = spawnSync(command, args, { cwd, encoding: 'utf8', timeout: 60000 });
    assert.ifError(result.error);
    return result;
  };
  const packed = run('npm', ['pack', '--ignore-scripts', '--json', '--pack-destination', temporary], root);
  assert.equal(packed.status, 0, packed.stdout + packed.stderr);
  const filename = (JSON.parse(packed.stdout) as { filename: string }[])[0]?.filename;
  assert.ok(filename);
  const installed = run('npm', ['install', '--offline', '--ignore-scripts', '--no-audit', '--no-fund', '--package-lock=false', join(temporary, filename)], consumer);
  assert.equal(installed.status, 0, installed.stdout + installed.stderr);
  const results: { name: string; expected: boolean; exit: number | null; diagnostics: string }[] = [];
  for (const sample of schemaPrecisionCases) {
    await t.test(sample.name, () => {
      const source = join(consumer, 'consumer.ts');
      writeFileSync(source, `import type { FeatureSupport, EndpointShutdownResponse, ShutdownRun, ShutdownQueuedRun } from 'oap-client';\n${sample.source}`);
      const compiled = run(process.execPath, [join(root, 'node_modules/typescript/bin/tsc'),
        '--noEmit', '--strict', '--noUncheckedIndexedAccess', '--skipLibCheck',
        '--module', 'NodeNext', '--moduleResolution', 'NodeNext', '--target', 'ES2022',
        '--types', 'node', '--typeRoots', join(root, 'node_modules/@types'), source], consumer);
      const diagnostics = compiled.stdout + compiled.stderr;
      results.push({ name: sample.name, expected: sample.accepts, exit: compiled.status, diagnostics });
      if (evidence) {
        mkdirSync(evidence, { recursive: true });
        copyFileSync(source, join(evidence, `${sample.name}.ts`));
        writeFileSync(join(evidence, `${sample.name}.txt`), diagnostics);
        writeFileSync(join(evidence, 'results.json'), JSON.stringify(results, null, 2) + '\n');
      }
      t.diagnostic(`${sample.name}: public tsc exit ${compiled.status}`);
      if (sample.accepts) {
        assert.equal(compiled.status, 0, diagnostics);
      } else {
        assert.equal(compiled.status, 2, `invalid consumer compiled or failed unexpectedly: ${diagnostics}`);
        const errors = diagnostics.split('\n').filter((line) => line.includes('error TS'));
        assert.equal(errors.length, 1, diagnostics);
        assert.match(errors[0] ?? '', /consumer\.ts\(\d+,\d+\): error TS2322:/);
      }
    });
  }
  const declarations = readFileSync(join(consumer, 'node_modules/oap-client/dist/src/protocol.d.ts'), 'utf8');
  assert.ok(declarations.includes('EndpointShutdownResponse'));
});
