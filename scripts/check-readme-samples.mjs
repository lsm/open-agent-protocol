#!/usr/bin/env node
/**
 * Type-checks every ```ts block in sdk/typescript/README.md against the SDK
 * source (#182).
 *
 * Nothing compiled the README, so its samples rotted silently: the package
 * rename left twelve `from "makai"` import lines that no longer resolve, and
 * the suite stayed green. Each block is written to a temp file, `oap-sdk` is
 * mapped to the SDK entrypoint, and `tsc --noEmit` judges the set. Errors are
 * reported at their README line, not the temp file's.
 *
 * Samples are type-checked, never run: several spawn a runtime.
 */

import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const README = join(ROOT, "sdk", "typescript", "README.md");
const ENTRY = join(ROOT, "sdk", "typescript", "src", "index.ts");
const FENCE = "```ts";

function extract(markdown) {
  const lines = markdown.split("\n");
  const blocks = [];
  let open = null;
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index].trim();
    if (open === null && line === FENCE) {
      open = { start: index + 2, body: [] };
    } else if (open !== null && line === "```") {
      blocks.push(open);
      open = null;
    } else if (open !== null) {
      open.body.push(lines[index]);
    }
  }
  if (open !== null) {
    console.error(`check-readme-samples: FAIL: unterminated ${FENCE} block at line ${open.start - 1}`);
    process.exit(1);
  }
  return blocks;
}

const blocks = extract(readFileSync(README, "utf8"));
if (blocks.length === 0) {
  console.error(`check-readme-samples: FAIL: no ${FENCE} blocks found — did the fence marker change?`);
  process.exit(1);
}

const workDir = mkdtempSync(join(tmpdir(), "oap-readme-samples-"));
const samples = join(workDir, "samples");
mkdirSync(samples);

// One file per block, so a failure names one sample, and `export {}` so a
// block with no import is still a module rather than a script sharing globals.
const byFile = new Map();
for (const block of blocks) {
  const name = `sample-${block.start}.ts`;
  byFile.set(name, block.start);
  writeFileSync(join(samples, name), `${block.body.join("\n")}\nexport {};\n`);
}

writeFileSync(
  join(workDir, "tsconfig.json"),
  JSON.stringify(
    {
      compilerOptions: {
        target: "ES2022",
        module: "Node16",
        moduleResolution: "Node16",
        strict: true,
        noEmit: true,
        esModuleInterop: true,
        skipLibCheck: true,
        types: ["node"],
        typeRoots: [join(ROOT, "node_modules", "@types")],
        baseUrl: ".",
        paths: { "oap-sdk": [ENTRY] },
      },
      include: ["samples/**/*.ts"],
    },
    null,
    2
  )
);

console.log(`type-checking ${blocks.length} README samples against ${ENTRY}...`);
try {
  execFileSync(process.execPath, [join(ROOT, "node_modules", "typescript", "bin", "tsc"), "-p", workDir], {
    cwd: ROOT,
    encoding: "utf8",
    stdio: "pipe",
  });
} catch (error) {
  const output = `${error.stdout ?? ""}${error.stderr ?? ""}`;
  const located = output.replace(/\S*sample-(\d+)\.ts\((\d+),(\d+)\)/g, (whole, start, line, column) => {
    const readmeLine = Number(start) + Number(line) - 1;
    return `sdk/typescript/README.md:${readmeLine}:${column} (sample at line ${start})`;
  });
  console.error(located.trim());
  console.error("check-readme-samples: FAIL: a README sample does not type-check");
  rmSync(workDir, { recursive: true, force: true });
  process.exit(1);
}

rmSync(workDir, { recursive: true, force: true });
console.log("check-readme-samples: OK");
