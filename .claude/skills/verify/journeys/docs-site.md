# OXAP documentation site

Use Node 22. The site has its own lockfile and build-time staging; do not move
or edit source documents to satisfy the renderer.

```sh
npm --prefix site ci
npm --prefix site run build
npm --prefix site run check
npm --prefix site exec -- playwright install chromium
npm --prefix site run browser-check
```

The browser journey serves the built site on an OS-assigned loopback port,
visits it under `/open-agent-protocol/`, checks deep links, local search,
conflict notices, schema/raw JSON and LLM outputs, and reads content with
JavaScript disabled. It checks desktop/mobile layouts, records screenshots
under the supplied artifact directory, and closes its browser and server.
Use `OAP_DOCS_ARTIFACTS=/tmp/oxap-docs-<unique>` to retain the browser evidence.

The structural check verifies every generated page, source conflict ID, schema
copy, manifest inventory, base-prefixed LLM URL and links inside exported
Markdown. Search and visible layout
must also pass in the browser: a successful build alone is insufficient.

Hosted Pages verification is pending until an administrator enables GitHub
Actions as the repository's Pages source and permits the deployment environment.
No local build or preview claims that deployment, domains or DNS were verified.
