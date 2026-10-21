import assert from 'node:assert/strict';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');

function fixture(t, compiler) {
  const temporary = mkdtempSync(join(tmpdir(), 'oap-client-prepare-'));
  t.after(() => rmSync(temporary, { recursive: true, force: true }));
  const source = join(temporary, 'client checkout');
  const consumer = join(temporary, 'consumer');
  mkdirSync(source);
  mkdirSync(consumer);
  for (const name of ['src', 'test', 'scripts', 'package.json', 'package-lock.json', 'tsconfig.json', 'README.md']) {
    if (existsSync(join(root, name))) cpSync(join(root, name), join(source, name), { recursive: true });
  }
  writeFileSync(join(source, 'src/index.ts'), readFileSync(join(source, 'src/index.ts'), 'utf8') + "\nexport const prepareProbe = 'before';\n");
  symlinkSync(join(root, 'node_modules'), join(source, 'node_modules'), 'junction');
  const home = join(temporary, 'home');
  mkdirSync(home);
  writeFileSync(join(home, 'npmrc'), '');
  const env = {
    PATH: process.env.PATH,
    HOME: home,
    npm_config_userconfig: join(home, 'npmrc'),
    npm_config_globalconfig: join(home, 'global-npmrc'),
    npm_config_cache: join(temporary, 'cache'),
    npm_config_audit: 'false',
    npm_config_fund: 'false',
  };
  function run(command, args, cwd = source) {
    if (command === 'npm' && process.env.npm_execpath) {
      args = [process.env.npm_execpath, ...args];
      command = process.execPath;
    }
    const result = spawnSync(command, args, { cwd, env, encoding: 'utf8', timeout: 60000 });
    assert.ifError(result.error);
    return result;
  }
  const build = run('npm', ['run', 'build']);
  assert.equal(build.status, 0, build.stdout + build.stderr);
  if (!compiler) rmSync(join(source, 'node_modules'));
  writeFileSync(join(consumer, 'package.json'), JSON.stringify({ private: true, type: 'module' }));
  return {
    source,
    install() {
      const result = run('npm', ['install', '--offline', '--foreground-scripts', '--package-lock=false', source], consumer);
      t.diagnostic(`npm install file dependency: exit ${result.status}`);
      return result;
    },
    observe() {
      const result = run(process.execPath, ['--input-type=module', '-e', "import { prepareProbe } from 'oap-client'; process.stdout.write(prepareProbe);"], consumer);
      assert.equal(result.status, 0, result.stdout + result.stderr);
      t.diagnostic(`consumer import: ${result.stdout}`);
      return result.stdout;
    },
  };
}

test('a prepared file dependency installs and imports without a compiler', (t) => {
  const f = fixture(t, false);
  const installed = f.install();
  assert.equal(installed.status, 0, installed.stdout + installed.stderr);
  assert.equal(f.observe(), 'before');
  assert.equal(existsSync(join(f.source, 'node_modules')), false);
});

test('a stale file dependency cannot install without a compiler', (t) => {
  const f = fixture(t, false);
  const source = join(f.source, 'src/index.ts');
  writeFileSync(source, readFileSync(source, 'utf8').replace("'before'", "'after'"));
  const installed = f.install();
  if (installed.status === 0) assert.fail('stale install succeeded and exposed ' + f.observe());
  assert.match(installed.stdout + installed.stderr, /oap-client build is missing, stale or incomplete/);
});

for (const output of ['session.js', 'session.d.ts']) {
  test(`a file dependency missing ${output} cannot install without a compiler`, (t) => {
    const f = fixture(t, false);
    rmSync(join(f.source, 'dist/src', output));
    const installed = f.install();
    assert.notEqual(installed.status, 0, 'partial install succeeded: ' + output);
    assert.match(installed.stdout + installed.stderr, /oap-client build is missing, stale or incomplete/);
  });
}

test('a file dependency with modified compiled output cannot install without a compiler', (t) => {
  const f = fixture(t, false);
  const output = join(f.source, 'dist/src/index.js');
  writeFileSync(output, readFileSync(output, 'utf8').replace("'before'", "'tampered'"));
  const installed = f.install();
  assert.notEqual(installed.status, 0, 'modified output installed successfully');
  assert.match(installed.stdout + installed.stderr, /oap-client build is missing, stale or incomplete/);
});

test('a stale file dependency rebuilds and exposes the current source with a compiler', (t) => {
  const f = fixture(t, true);
  const source = join(f.source, 'src/index.ts');
  writeFileSync(source, readFileSync(source, 'utf8').replace("'before'", "'after'"));
  const installed = f.install();
  assert.equal(installed.status, 0, installed.stdout + installed.stderr);
  assert.equal(f.observe(), 'after');
});

test('a partial file dependency rebuilds missing JavaScript and declarations with a compiler', (t) => {
  const f = fixture(t, true);
  for (const output of ['session.js', 'session.d.ts']) rmSync(join(f.source, 'dist/src', output));
  const installed = f.install();
  assert.equal(installed.status, 0, installed.stdout + installed.stderr);
  assert.equal(f.observe(), 'before');
  assert.equal(existsSync(join(f.source, 'dist/src/session.d.ts')), true);
});
