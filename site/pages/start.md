# Start with OXAP

OXAP is the new name of Open Agent Protocol (OAP). The command is `oxap`, and `oapx` still works as an alias; the v0.1 protocol ID remains `open-agent-protocol`. The [install and migration guide](./source/docs/install.md) lists the new package names, and [Decision 0067](./source/decisions/0067-the-project-is-named-oxap.md) records them.

Choose the boundary before choosing a client:

- An **agent endpoint** carries raw envelopes over stdio.
- A **Hub** manages multiple sessions over stdio operations or HTTP/SSE.
- A **provider endpoint** carries model inference rather than agent runs.

Read the [source README](./source/README.md), [backend guide](./source/docs/choosing-a-backend.md), and [SDK guide](./sdks.md). The copied source pages retain their original wording and disclose audited conflicts.

::: warning Open conflict — C01, C05, C06, C40, C42
The README examples, command inventory, decision index, operation count and handshake diagram have documented disagreements with implementation. Consult [both sides](./conflicts.md) before using them as an integration recipe. This site does not adjudicate them.
:::

## Hosting this documentation

This site uses GitHub Pages under `/oxap/`. A repository administrator must choose **Settings → Pages → Build and deployment → Source: GitHub Actions** and permit the `github-pages` environment deployment if approval is required. The workflow can then be dispatched after merge. Domain, repository and DNS settings are outside this change.
