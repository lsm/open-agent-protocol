# Journey: the TypeScript SDK installs as @oxap/sdk

Decision 0067 publishes the SDK as `@oxap/sdk`, keeping every public symbol,
and makes the CLI its optional same-version runtime dependency
(`@oxap/cli`). Nothing is published yet: this journey packs the tarball and
installs it, which is what the registry install will do.

Install dependencies once, from the repository root:

```sh
npm ci --ignore-scripts
```

## The package identity and its runtime dependency

```sh
node -p 'const p=require("./package.json"); `${p.name} ${p.version} ${p.optionalDependencies["@oxap/cli"]}`'
node -p 'const l=require("./package-lock.json"); `${l.name} ${l.packages[""].name} ${l.packages["node_modules/@oxap/cli"].version} ${l.packages["node_modules/@oxap/cli"].optional}`'
```

Both lines must name `@oxap/sdk` at the same version, and the lock must carry
`@oxap/cli` at that version marked optional. `npm ci` above must exit 0, which
it does only when the lock agrees with the manifest.

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
grep -c '"@oxap/sdk"' dist/npm/cli-*/package.json 2>/dev/null || echo "no CLI packages built yet"
```

While `@oxap/cli` is still packaged under its old name (the separate CLI PR),
there is no `dist/npm/cli-*`; once it lands, none of those files may name
`@oxap/sdk` in `dependencies` or `optionalDependencies`. The unit test
`the CLI is the only runtime dependency, and it never depends back on the SDK`
holds the same rule for the lockfile.
