import { createHash } from 'node:crypto';
import { existsSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const record = join(root, 'dist/.build-hash');
const preparationError = 'oap-client build is missing, stale or incomplete; restore a complete checkout and run npm install in clients/ts before installing it as a file dependency.';

function sources(directory) {
  return readdirSync(join(root, directory), { withFileTypes: true }).flatMap((entry) => {
    const path = `${directory}/${entry.name}`;
    return entry.isDirectory() ? sources(path) : path.endsWith('.ts') ? [path] : [];
  }).sort();
}

function digest(paths) {
  const hash = createHash('sha256');
  for (const path of paths) hash.update(path).update('\0').update(readFileSync(join(root, path))).update('\0');
  return hash.digest('hex');
}

function inputState() {
  try {
    const inputs = [...sources('src'), ...sources('test')];
    return {
      outputs: inputs.filter((path) => !path.endsWith('.d.ts')).flatMap((path) => [
        `dist/${path.slice(0, -3)}.js`,
        `dist/${path.slice(0, -3)}.d.ts`,
      ]),
      inputHash: digest([...inputs, 'tsconfig.json', 'package.json', 'package-lock.json', 'scripts/build.mjs']),
    };
  } catch (error) {
    if (error.code === 'ENOENT') throw new Error(preparationError);
    throw error;
  }
}

const { outputs, inputHash } = inputState();

function fresh() {
  try {
    return readFileSync(record, 'utf8') === `${inputHash}\n${digest(outputs)}\n`;
  } catch (error) {
    if (error.code === 'ENOENT') return false;
    throw error;
  }
}

if (process.argv[2] !== '--prepare' || !fresh()) {
  const compiler = join(root, 'node_modules/typescript/bin/tsc');
  if (!existsSync(compiler)) {
    throw new Error(preparationError);
  }
  rmSync(join(root, 'dist'), { recursive: true, force: true });
  const compiled = spawnSync(process.execPath, [compiler, '-p', 'tsconfig.json'], { cwd: root, stdio: 'inherit' });
  if (compiled.error) throw compiled.error;
  if (compiled.status !== 0) process.exit(compiled.status ?? 1);
  writeFileSync(record, `${inputHash}\n${digest(outputs)}\n`);
}
