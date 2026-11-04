# OXAP documentation site

This first slice adds the publication pipeline and six navigation sections. It
keeps source documents in their original directories. Build-time staging copies
selected prose into `.content/source/`, links unstaged material to GitHub, adds
open-conflict notices, and generates schema pages and manifest inventory counts. Numeric schema tokens retain their original precision, including
the full uint64 boundary.
The final export step gives LLM Markdown links the repository base prefix,
including links between exported pages. Generated files are ignored. The conflict register preserves the audit IDs and
both claims; this site does not choose a winning side.

```sh
npm --prefix site ci
npm --prefix site run build
npm --prefix site run check
npm --prefix site run preview -- --port 4173
```

Visit `http://127.0.0.1:4173/open-agent-protocol/`. Node 22 is used in CI.
Browser verification is documented in the repository's docs-site verify journey.

A repository administrator must select GitHub Actions as the Pages source and
allow the github-pages deployment environment. The workflow builds on PRs and
main; only main pushes or a main workflow dispatch deploy. It does not configure
Pages, a domain, or DNS. The first hosted smoke check remains pending until the
administrator enables deployment. Local preview checks cover the same base path.

The lockfile pins VitePress 1.6.4 and the LLM plugin. Compatible Vite and
minimatch overrides patch transitive advisories; Mermaid is pinned separately
for rendering the existing source diagrams. Dependency changes must rerun the
artifact and browser checks. Some development-tool advisories remain in this
stable VitePress line; preview binds only to loopback and Pages publishes static
output.
