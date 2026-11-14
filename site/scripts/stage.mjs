import { readFile, writeFile, mkdir, readdir, rm, cp } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'

const site = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const root = path.dirname(site)
const out = path.join(site, '.content')
const base = '/oxap/'
const revision = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim()
const blob = `https://github.com/lsm/oxap/blob/${revision}/`
const audit = JSON.parse(await readFile(path.join(site, 'conflicts.json'), 'utf8'))
const selectedDocs = ['choosing-a-backend.md', 'go-library.md', 'storage.md', 'sdk-command-results.md', 'conformance-matrix.md', 'validation-coverage.md', 'releasing.md', 'zig-stream-memory-ownership.md', 'zig-0.17.0-downstream-migration.md']
const sources = ['README.md', ...selectedDocs.map(name => `docs/${name}`), 'sdk/typescript/README.md', 'sdk/python/README.md', 'rust/sdk/README.md', 'clients/ts/README.md']
async function walk(dir) {
  const entries = await readdir(path.join(root, dir), { withFileTypes: true })
  for (const entry of entries) {
    const name = `${dir}/${entry.name}`
    if (entry.isDirectory()) await walk(name)
    else if (name.endsWith('.md')) sources.push(name)
  }
}
await walk('decisions')
await walk('drafts')
const assets = (await readdir(path.join(root, 'docs'))).filter(name => /\.(svg|png|jpe?g)$/.test(name)).map(name => `docs/${name}`)
const chosen = new Set([...sources, ...assets])
await rm(out, { recursive: true, force: true })
async function put(name, content) {
  const dest = path.join(out, name)
  await mkdir(path.dirname(dest), { recursive: true })
  if (name.endsWith('.md')) {
    let fence = false
    content = content.split('\n').map(line => {
      if (/^\s*(```|~~~)/.test(line)) fence = !fence
      if (fence) return line
      return line.replace(/(!?\[[^\]]*\]\()([^\s)]+)(\))/g, (match, open, target, close) => {
        if (open.startsWith('!') || /^(?:[a-z]+:|#|\/\/)/i.test(target)) return match
        const [file, fragment] = target.split('#')
        let resolved = file.startsWith('/') ? file.replace(base, '/').slice(1) : path.posix.normalize(path.posix.join(path.posix.dirname(name), file))
        if (!path.posix.extname(resolved)) resolved = resolved.replace(/\/$/, '') + '.md'
        return `${open}/${resolved}${fragment ? `#${fragment}` : ''}${close}`
      })
    }).join('\n')
  }
  await writeFile(dest, content)
}
function rewrite(text, source) {
  let fence = false
  return text.split('\n').map(line => {
    if (/^\s*(```|~~~)/.test(line)) fence = !fence
    if (fence) return line
    return line.replace(/(!?\[[^\]]*\]\()([^\s)]+)(\))/g, (match, open, target, close) => {
      if (/^(?:[a-z]+:|#|\/)/i.test(target)) return match
      const [file, fragment] = target.split('#')
      const resolved = path.posix.normalize(path.posix.join(path.posix.dirname(source), file))
      if (chosen.has(resolved)) return match
      return `${open}${blob}${resolved}${fragment ? `#${fragment}` : ''}${close}`
    })
  }).join('\n')
}
for (const source of sources) {
  let text = await readFile(path.join(root, source), 'utf8')
  text = rewrite(text, source)
  const conflicts = audit.items.filter(item => item.document.includes(`[${source}:`))
  const note = conflicts.length ? `\n::: warning Open conflict — ${conflicts.map(c => c.id).join(', ')}\nThis source has audited documentation/implementation disagreements. [Read both claims](/conflicts). The audit is a snapshot at ${audit.auditCommit.slice(0, 12)}; publishing this source does not resolve them.\n:::\n` : ''
  const status = source.startsWith('drafts/') ? '\n::: info Draft source\nThis is a design source. Its own status and amending decisions determine its standing; publication does not graduate a proposal.\n:::\n' : ''
  await put(`source/${source}`, `${note}${status}\n${text}\n\n---\n[Original source at ${revision.slice(0, 12)}](${blob}${source})\n`)
}
for (const asset of assets) {
  await mkdir(path.dirname(path.join(out, 'source', asset)), { recursive: true })
  await cp(path.join(root, asset), path.join(out, 'source', asset))
  await mkdir(path.dirname(path.join(out, 'public/source', asset)), { recursive: true })
  await cp(path.join(root, asset), path.join(out, 'public/source', asset))
}
await mkdir(path.join(out, 'public/schemas'), { recursive: true })
const schemas = (await readdir(path.join(root, 'schema/v0.1'))).filter(n => n.endsWith('.json')).sort()
const cell = value => String(value).replaceAll('|', '&#124;').replaceAll('\n', ' ')
const slug = value => value.replaceAll(/[^a-zA-Z0-9_-]/g, '-')
function refLink(value, current) {
  if (!value || /^https?:/.test(value)) return value ? `\`${cell(value)}\`` : '—'
  const [file, pointer] = value.split('#')
  const anchor = pointer?.startsWith('/$defs/') ? `#def-${slug(pointer.slice(7))}` : '#root'
  return `[${cell(value)}](./${(file || current).replace(/\.json$/, '.md')}${anchor})`
}
const index = []
for (const name of schemas) {
  const raw = await readFile(path.join(root, 'schema/v0.1', name), 'utf8')
  const schema = JSON.parse(raw, (key, value, context) =>
    typeof value === 'number' && !Number.isSafeInteger(value) ? JSON.rawJSON(context.source) : value)
  await put(`public/schemas/${name}`, raw)
  const sections = [['Root', schema], ...Object.entries(schema.$defs || {})]
  const lines = [`# ${schema.title || name}`, '', `Generated from [${name}](/schemas/${name}). This describes the schema, not runtime support.`, '', '::: warning Open conflict', 'Schema and semantic/runtime behavior are separate. See [C16–C18 and C41](/conflicts) before relying on subscription or transcript-page behavior.', ':::', '']
  for (const [key, node] of sections) {
    lines.push(`## ${key} {#${key === 'Root' ? 'root' : `def-${slug(key)}`}}`, '', node.description || '', '', '| Field | Type | Required | Reference / constraints |', '| --- | --- | --- | --- |')
    const properties = node.properties || {}
    for (const [field, value] of Object.entries(properties)) {
      const constraints = Object.fromEntries(Object.entries(value).filter(([k]) => !['type', '$ref', 'description'].includes(k)))
      lines.push(`| \`${field}\` | ${cell(value.type || (value.$ref ? 'reference' : 'composite'))} | ${(node.required || []).includes(field) ? 'yes' : 'no'} | ${refLink(value.$ref, name)} ${value.description ? cell(value.description) : ''} ${Object.keys(constraints).length ? `\`${cell(JSON.stringify(constraints))}\`` : ''} |`)
    }
    if (!Object.keys(properties).length) lines.push('| — | See schema below | — | This definition has no direct properties. |')
    lines.push('', '### Complete definition', '', '```json', JSON.stringify(node, null, 2), '```', '', '### Declared examples', '', node.examples?.length ? `\`\`\`json\n${JSON.stringify(node.examples, null, 2)}\n\`\`\`` : 'No examples are declared in this definition.', '')
  }
  await put(`reference/schema/${name.replace(/\.json$/, '.md')}`, lines.join('\n'))
  index.push(`- [${name}](./reference/schema/${name.replace(/\.json$/, '.md')}) · [raw JSON](/schemas/${name})`)
}
const manifest = JSON.parse(await readFile(path.join(root, 'fixtures/manifest.json'), 'utf8'))
const gaps = JSON.parse(await readFile(path.join(root, 'fixtures/known-gaps.json'), 'utf8'))
const counts = `| Inventory | Count |\n| --- | --- |\n| Manifest entries (including load-invalid) | ${manifest.fixtures.length} |\n| Provider-profile entries | ${manifest.fixtures.filter(f => f.profile === 'model-provider-core').length} |\n${['go', 'zig', 'rust'].map(language => `| ${language} declared known-gap fixtures | ${gaps[language].length} |`).join('\n')}\n\nSource revision: \`${revision}\`. These counts are generated from [manifest](/contracts/manifest.json) and [known gaps](/contracts/known-gaps.json).`
for (const name of ['manifest.json', 'known-gaps.json']) await put(`public/contracts/${name}`, await readFile(path.join(root, 'fixtures', name)))
for (const name of ['index', 'start', 'concepts', 'reference', 'sdks']) {
  const source = await readFile(path.join(site, `pages/${name}.md`), 'utf8')
  await put(`${name}.md`, source.replace('<!-- schema-index -->', index.join('\n')).replace('<!-- manifest-summary -->', counts))
}
for (const dir of ['decisions', 'drafts']) {
  const entries = sources.filter(s => s.startsWith(`${dir}/`))
  const list = entries.map(s => `- [${s.slice(dir.length + 1).replace(/\.md$/, '')}](./source/${s})`).join('\n')
  await put(`${dir}.md`, `# ${dir === 'decisions' ? 'Decisions' : 'Drafts'}\n\nOriginal records are staged without changing their status. Read each record's own Status and amendment chain.\n\n${list}\n`)
}
const conflictLines = ['# Open conflicts', '', `Audit snapshot: \`${audit.auditCommit}\`. Each ID comes from the reviewed conflicts.md audit. These are **open conflicts**, not resolved corrections. Source documents and implementation observations are retained separately.`, '', ...audit.items.flatMap(item => [`## ${item.id}: ${item.area} {#${item.id.toLowerCase()}}`, '', '::: warning Open conflict', 'This site does not choose a side. Consult both sources before relying on this behavior.', ':::', '', `**Document claim:** ${item.claim}`, '', item.document, '', `**Implementation observation:** ${item.observation}`, '', item.implementation, ''])]
await put('conflicts.md', conflictLines.join('\n'))
await put('public/staging.json', JSON.stringify({ revision, sources, schemas, manifestEntries: manifest.fixtures.length }, null, 2))
console.log(`Staged ${sources.length} source pages and ${schemas.length} schemas at ${revision}`)
