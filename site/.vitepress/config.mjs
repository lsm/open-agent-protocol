import { defineConfig } from 'vitepress'
import llmstxt from 'vitepress-plugin-llms'
import { withMermaid } from 'vitepress-plugin-mermaid'

export default withMermaid(defineConfig({
  title: 'OXAP',
  description: 'One protocol across agent sessions, tools, and model inference.',
  base: '/open-agent-protocol/',
  srcDir: '.content',
  cleanUrls: false,
  lastUpdated: false,
  markdown: { config(md) { md.set({ html: false }) } },
  vite: { plugins: [llmstxt({ injectLLMHint: false })] },
  themeConfig: {
    siteTitle: 'OXAP',
    nav: [
      { text: 'Start', link: '/start/' },
      { text: 'Concepts', link: '/concepts/' },
      { text: 'Protocol reference', link: '/reference/' },
      { text: 'SDKs', link: '/sdks/' },
      { text: 'Decisions', link: '/decisions/' },
      { text: 'Drafts', link: '/drafts/' }
    ],
    sidebar: [
      { text: 'Read OXAP', items: [
        { text: 'Start', link: '/start/' },
        { text: 'Concepts', link: '/concepts/' },
        { text: 'Protocol reference', link: '/reference/' },
        { text: 'SDKs', link: '/sdks/' },
        { text: 'Decisions', link: '/decisions/' },
        { text: 'Drafts', link: '/drafts/' },
        { text: 'Open conflicts', link: '/conflicts' }
      ] }
    ],
    search: { provider: 'local' },
    socialLinks: [{ icon: 'github', link: 'https://github.com/lsm/open-agent-protocol' }],
    footer: { message: 'OXAP · existing v0.1 wire identities and package names remain unchanged.' }
  }
}))
