import { readFile, readdir, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const dist = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../.vitepress/dist')
const base = '/oxap/'
const files = (await readdir(dist, { recursive: true })).filter(file => file.endsWith('.md') || file === 'llms-full.txt')
for (const file of files) {
  let fence = false
  const content = (await readFile(path.join(dist, file), 'utf8')).split('\n').map(line => {
    if (/^\s*(```|~~~)/.test(line)) fence = !fence
    if (fence) return line
    return line.replace(/(\]\()\/(?!\/|oxap\/)([^)]+\))/g, `$1${base}$2`)
  }).join('\n')
  await writeFile(path.join(dist, file), content)
}
