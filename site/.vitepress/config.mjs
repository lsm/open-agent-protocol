import { defineConfig } from 'vitepress'
import llmstxt from 'vitepress-plugin-llms'
import { withMermaid } from 'vitepress-plugin-mermaid'

export default withMermaid(defineConfig({
  title: 'OXAP',
  description: 'One protocol across agent sessions, tools, and model inference.',
  base: '/oxap/',
  srcDir: '.content',
  cleanUrls: false,
  lastUpdated: false,
  markdown: { config(md) { md.set({ html: false }) } },
  vite: { plugins: [llmstxt({ injectLLMHint: false })] },
  themeConfig: {
    siteTitle: 'OXAP',
    nav: [
      { text: 'Start', link: '/start.html' },
      { text: 'Concepts', link: '/concepts.html' },
      { text: 'Protocol reference', link: '/reference.html' },
      { text: 'SDKs', link: '/sdks.html' },
      { text: 'Decisions', link: '/decisions.html' },
      { text: 'Drafts', link: '/drafts.html' }
    ],
    sidebar: [
      { text: 'Read OXAP', items: [
        { text: 'Start', link: '/start.html' },
        { text: 'Concepts', link: '/concepts.html' },
        { text: 'Protocol reference', link: '/reference.html' },
        { text: 'SDKs', link: '/sdks.html' },
        { text: 'Decisions', link: '/decisions.html' },
        { text: 'Drafts', link: '/drafts.html' },
        { text: 'Open conflicts', link: '/conflicts' }
      ] }
    ],
    search: { provider: 'local' },
    socialLinks: [{ icon: 'github', link: 'https://github.com/lsm/oxap' }],
    footer: { message: 'OXAP, formerly Open Agent Protocol · the v0.1 wire identities are unchanged.' }
  }
}))
