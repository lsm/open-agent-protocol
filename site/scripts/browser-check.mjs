import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'

const site = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const dist = path.join(site, '.vitepress/dist')
const base = '/open-agent-protocol/'
const output = process.env.OAP_DOCS_ARTIFACTS
if (output) await mkdir(output, { recursive: true })
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml', '.md': 'text/plain', '.txt': 'text/plain' }
const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost')
  if (!url.pathname.startsWith(base)) { res.writeHead(404).end(); return }
  const relative = decodeURIComponent(url.pathname.slice(base.length)) || 'index.html'
  const file = path.resolve(dist, relative.endsWith('/') ? `${relative}index.html` : relative)
  if (!file.startsWith(`${dist}${path.sep}`)) { res.writeHead(404).end(); return }
  try {
    const bytes = await readFile(file)
    res.writeHead(200, { 'Content-Type': mime[path.extname(file)] || 'application/octet-stream' }).end(bytes)
  } catch { res.writeHead(404).end() }
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const url = `http://127.0.0.1:${server.address().port}${base}`
const browser = await chromium.launch({ headless: true })
const failures = []
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
  page.on('pageerror', error => failures.push(error.message))
  page.on('response', response => { if (response.status() >= 400 && response.url().startsWith(url)) failures.push(`${response.status()} ${response.url()}`) })
  await page.goto(url)
  await page.getByRole('heading', { name: /One protocol/ }).waitFor()
  assert.ok(!(await page.locator('body').innerText()).includes('data-nosnippet'))
  await page.getByRole('switch').first().click()
  assert.equal(await page.locator('html').evaluate(el => el.classList.contains('dark')), true)
  await page.getByRole('switch').first().click()
  await page.getByRole('link', { name: 'Start reading' }).click()
  await page.getByRole('heading', { name: /^Start with OXAP/ }).waitFor()
  await page.goto(`${url}source/README.html`)
  await page.locator('.mermaid svg').first().waitFor()
  assert.ok(await page.locator('main img').evaluateAll(images => images.every(image => image.complete && image.naturalWidth > 0)))
  await page.goto(`${url}reference/schema/session.schema.html`)
  await page.getByRole('heading', { name: /^Declared examples/ }).first().waitFor()
  await page.goto(`${url}source/docs/go-library.html`)
  await page.getByText('Open conflict —').first().waitFor()
  await page.getByRole('button', { name: /Search/ }).first().click()
  const input = page.getByRole('searchbox')
  await input.fill('recovery')
  await page.locator('.VPLocalSearchBox .result').first().waitFor()
  await input.fill('zxqvnomatch713')
  await page.getByText('No results for').waitFor()
  await page.keyboard.press('Escape')
  await page.goto(`${url}conflicts.html#c41`)
  await page.getByRole('heading', { name: /C41:/ }).waitFor()
  if (output) await page.screenshot({ path: path.join(output, 'conflicts.png'), fullPage: false })
  await page.goto(url)
  if (output) await page.screenshot({ path: path.join(output, 'desktop.png') })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.getByRole('button', { name: /mobile navigation/i }).click()
  await page.getByRole('link', { name: 'SDKs', exact: true }).first().click()
  await page.getByRole('heading', { name: /^SDKs and clients/ }).waitFor()
  await page.locator('.VPNavScreen').waitFor({ state: 'hidden' })
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false)
  if (output) await page.screenshot({ path: path.join(output, 'mobile.png') })
  assert.equal((await page.request.get(`${url}missing-page.html`)).status(), 404)
  const nojs = await browser.newPage({ javaScriptEnabled: false })
  await nojs.goto(`${url}reference/schema/session.schema.html`)
  assert.ok((await nojs.locator('main').innerText()).includes('Complete definition'))
  for (const file of ['llms.txt', 'llms-full.txt', 'start.md', 'schemas/session.schema.json']) {
    const response = await page.request.get(`${url}${file}`)
    assert.equal(response.status(), 200, file)
  }
  assert.deepEqual(failures, [])
  const result = 'PASS: desktop/mobile navigation, deep links, local search, Open conflict C41, schema + raw JSON, LLM URLs and no-JS body under repository base'
  console.log(result)
  if (output) await writeFile(path.join(output, 'browser-result.txt'), `${result}\n`)
} finally {
  await browser.close()
  await new Promise(resolve => server.close(resolve))
}
