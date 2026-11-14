# Journey: the TypeScript SDK installs as @oxap/sdk

Decision 0067 publishes the SDK as `@oxap/sdk`, keeping every public symbol,
and makes the CLI its optional same-version runtime dependency
(`@oxap/cli`). Nothing is published yet: this journey packs the tarball and
installs it, which is what the registry install will do.

Install dependencies once, from the repository root:

```sh
npm ci --ignore-scripts
```

## The package identity, and no source pin on the CLI

```sh
node -p 'const p=require("./package.json"); `${p.name} ${p.version} ${(p.optionalDependencies||{})["@oxap/cli"]}`'
node -p 'const l=require("./package-lock.json"); `${l.name} ${l.packages[""].name} ${l.packages["node_modules/@oxap/cli"]}`'
```

Both lines must name `@oxap/sdk` at the same version and end in `undefined`.
The `@oxap/cli` pin lives only in `scripts/package-npm.ts`, which writes it into
the packed manifest (see the tagged-release step below): npm cannot write or
repair a lock entry for a package the registry does not have, so a source pin
leaves `npm ci` out of sync on the next version bump. `npm ci` above must exit 0.

## The packed tarball installs and works as a consumer

```sh
cked=$(mktemp -d)
npm pack --json --pack-destination "$cked" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const r=JSON.parse(s)[0];console.log(r.name,r.version,r.filename)})'
npm run check:declarations
```

Require `@oxap/sdk` and the packed file name `oxap-sdk-<version>.tgz`, then
`check-npm-declarations: OK`: it packs the tarball again, installs it into a
fresh consumer project, type-checks under strict TS with `skipLibCheck`
disabled, and requires the installed package to expose `createMakaiClient`.

## The README's own samples import the new specifier

```sh
npm run check:readme
```

Require `check-readme-samples: OK`. Every `ts` block is type-checked against
`@oxap/sdk`, so a sample still importing the old name fails here.

## The CLI never depends back on the SDK

```sh
node -e 'const fs=require("fs"),d="dist/npm";const m=(fs.existsSync(d)?fs.readdirSync(d):[]).filter(n=>n.startsWith("cli")).map(n=>`${d}/${n}/package.json`).filter(f=>fs.existsSync(f));if(!m.length){console.log("no CLI packages built yet");process.exit(0)}const bad=m.filter(f=>{const p=JSON.parse(fs.readFileSync(f,"utf8"));return [p.dependencies,p.optionalDependencies].some(x=>x&&"@oxap/sdk" in x)});console.log(`${m.length} CLI manifests, ${bad.length} name @oxap/sdk`,...bad);process.exit(bad.length?1:0)'
```

Before any packaging run it prints `no CLI packages built yet`. Once
`dist/npm/cli*` exists, require `<n> CLI manifests, 0 name @oxap/sdk` and exit
0: none of them may name `@oxap/sdk` in `dependencies` or
`optionalDependencies`, and any that does is listed and the step exits 1. This
is the only check of that rule.

## A tagged release pins the CLI at the tag's version

```sh
repo=$(pwd); cked=$(mktemp -d); mkdir -p "$cked/dist/bin" "$cked/dist/src" "$cked/sdk/typescript"
touch "$cked/dist/src/index.js" "$cked/sdk/typescript/README.md"; cp package.json "$cked/"
(cd "$cked" && npx --yes tsx "$repo/scripts/package-npm.ts" --version 9.9.9-tag.1 \
  --withhold darwin-arm64,darwin-x64,linux-arm64,linux-x64,win32-x64,win32-arm64 >/dev/null)
node -p "const p=require('$cked/dist/npm/oxap-sdk/package.json'); p.version+' '+p.optionalDependencies['@oxap/cli']"
```

This is the release workflow's own `npx tsx` call with a tag version that
differs from `package.json`. Require `9.9.9-tag.1 9.9.9-tag.1`: the published
SDK pins `@oxap/cli` at the tag, not at the manifest's version.

## An installed SDK finds the runtime in the @oxap/cli platform package

```sh
c=$(mktemp -d); npm pack --pack-destination "$c" >/dev/null
cd "$c" && echo '{"name":"c","version":"1.0.0"}' > package.json
npm install --no-audit --no-fund --omit=optional ./oxap-sdk-*.tgz >/dev/null; echo "install=$?"
key=$(node -p 'process.platform+"-"+process.arch'); mkdir -p "node_modules/@oxap/cli-$key/bin"
echo "{\"name\":\"@oxap/cli-$key\",\"version\":\"0.0.0\"}" > "node_modules/@oxap/cli-$key/package.json"
printf '#!/bin/sh\n' > "node_modules/@oxap/cli-$key/bin/oxap"; chmod +x "node_modules/@oxap/cli-$key/bin/oxap"
unset OXAP_SDK_BINARY_PATH OAP_SDK_BINARY_PATH
node -e 'require("@oxap/sdk").resolveMakaiBinary({}).then(p=>console.log("resolved:",p))'
```

The stub stands in for the platform package `@oxap/cli` will install. Require
`install=0` and a `resolved:` path ending in
`node_modules/@oxap/cli-<platform>-<arch>/bin/oxap`; a resolver that knew only
`@oap-sdk/cli-*` falls through to `oxap` on `PATH` here.
