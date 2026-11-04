import assert from 'node:assert/strict'
import { readFile, stat, readdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const site = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const root = path.dirname(site)
const dist = path.join(site, '.vitepress/dist')
const base = '/open-agent-protocol/'
const read = name => readFile(path.join(dist, name), 'utf8')
const staged = JSON.parse(await read('staging.json'))
const audit = JSON.parse(await readFile(path.join(site, 'conflicts.json'), 'utf8'))
for (const source of staged.sources) {
  const html = await read(`source/${source.replace(/\.md$/, '.html')}`)
  const conflicts = audit.items.filter(item => item.document.includes(`[${source}:`))
  if (conflicts.length) {
    assert.match(html, /Open conflict/)
    for (const conflict of conflicts) assert.ok(html.includes(conflict.id), `${source}: ${conflict.id}`)
  }
}
for (const name of staged.schemas) {
  assert.equal(await read(`schemas/${name}`), await readFile(path.join(root, 'schema/v0.1', name), 'utf8'))
  const html = await read(`reference/schema/${name.replace(/\.json$/, '.html')}`)
  assert.match(html, /Complete definition/)
  assert.match(html, /Declared examples/)
}
assert.ok((await read('reference/schema/common.schema.md')).includes('\"maximum\": 18446744073709551615'), 'schema reference must retain the exact uint64 maximum')
const manifest = JSON.parse(await read('contracts/manifest.json'))
assert.equal(staged.manifestEntries, manifest.fixtures.length)
assert.ok((await read('reference.html')).includes(String(manifest.fixtures.length)))
const htmlFiles = (await readdir(dist, { recursive: true })).filter(file => file.endsWith('.html'))
for (const file of htmlFiles) {
  const html = await read(file)
  for (const match of html.matchAll(/(?:href|src)="([^"]+)"/g)) {
    const target = match[1]
    if (/^(?:[a-z]+:|#|\/\/)/i.test(target)) continue
    const url = new URL(target, `http://docs.test${base}${file}`)
    assert.ok(url.pathname.startsWith(base), `${file}: ${target}`)
    let local = decodeURIComponent(url.pathname.slice(base.length))
    if (!local || local.endsWith('/')) local += 'index.html'
    assert.ok((await stat(path.join(dist, local))).isFile(), `${file}: ${target}`)
  }
}
const index = await read('llms.txt')
const full = await read('llms-full.txt')
for (const id of ['C01', 'C16', 'C17', 'C18', 'C41']) assert.ok(full.includes(id))
const links = [...index.matchAll(/\]\(([^)]+\.md)\)/g)].map(match => match[1])
assert.ok(links.length > staged.sources.length)
for (const link of links) {
  assert.ok(link.startsWith(base), link)
  assert.ok((await stat(path.join(dist, link.slice(base.length)))).isFile(), link)
}
for (const link of links) {
  const markdown = await read(link.slice(base.length))
  const prose = markdown.replace(/```[\s\S]*?```/g, '').replace(/`[^`]*`/g, '')
  for (const match of prose.matchAll(/\]\(([^\s)]+)\)/g)) {
    if (/^(?:[a-z]+:|#|\/\/)/i.test(match[1])) continue
    const target = new URL(match[1], `http://docs.test${link}`)
    assert.ok(target.pathname.startsWith(base), `${link}: ${match[1]}`)
    assert.ok((await stat(path.join(dist, decodeURIComponent(target.pathname.slice(base.length))))).isFile(), `${link}: ${match[1]}`)
  }
}
for (const section of ['start', 'concepts', 'reference', 'sdks', 'decisions', 'drafts']) {
  assert.match(await read(`${section}.html`), /OXAP/)
}
assert.match(await read('conflicts.html'), /Open conflict/)
console.log(`PASS: ${staged.sources.length} staged source pages, ${staged.schemas.length} exact schemas, ${links.length} base-prefixed LLM pages; conflicts and live inventory retained`)
