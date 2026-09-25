# Claude Code / Claude Agent SDK 2.1.282 mapping ledger

Status: the Claude Code adapter's pin. This ledger moves the adapter from
2.1.280 to 2.1.282 and records only what the move changed or settled.
[The 2.1.280 ledger](claude-code-agent-sdk-2.1.280-mapping.md) and, beneath
it, [the 2.1.263 ledger](claude-code-agent-sdk-2.1.263-mapping.md) remain the
mapping of record for every surface not restated here. The adapter's corpus is
`fixtures/adapters/claude-code-2.1.282`. 2.1.280 is retired: its corpus is
removed, because its expectations name a capability revision the adapter no
longer advertises. Every hash below was computed on 2026-09-24 from the
artifacts named, and the live checks ran the 2.1.282 darwin-arm64 binary.

## Provenance

### Claude Code CLI 2.1.282

- npm artifact: `@anthropic-ai/claude-code@2.1.282`
  - tarball sha256: `12f908bd7bf3bc3a7c1edfcb50d017d526ebe76b4f59847c3334e70ac5d84893`
  - tarball sha1 (npm `dist.shasum`): `41946997391ddddbd8969f2d8e01082653b71856`
  - npm `dist.integrity`:
    `sha512-nKh6bevzeUQm7yJzA6WdyvQ92k6/M5muwTUJtI1CbRWzl9drmdGdh61N9y4CIUVXAZhaVam0migsHuqpt8Kvlw==`
  - the wrapper tarball is 27,580 bytes; `cli-wrapper.cjs` is byte-identical
    to 2.1.280's (sha256
    `61ad63033d9c8155d5e60a29f45dc4665afa07631c0b108e62cc83bf45ba490e`)
- native linux-x64 artifact: `@anthropic-ai/claude-code-linux-x64@2.1.282`
  - tarball sha256: `1499a6947b466c2048a678b3291adf1e3f9cc3e3abd8b822f9e03ac85a2bb44b`
  - tarball sha1: `04bfb45ef5533050f9e00aeddb481f5236ca8f72`
  - npm `dist.integrity`:
    `sha512-iuegTPhk/134MJiuyNDD6gW2SNDRkHoPDK6XK+lrhzyISLyNpKCuFUpIHKTfxkp4RbWQgH2Dtnp1rYxmJTzEOQ==`
  - binary sha256: `3afe8535c0cc33f0e24f7b25dab7a1727b8b592196f8496a8bc302ba2161eed3`
  - binary size: 238,767,288 bytes
- native darwin-arm64 artifact, the binary the live checks below ran:
  `@anthropic-ai/claude-code-darwin-arm64@2.1.282`
  - tarball sha256: `738ce2deba0060eef3cdc55b7aaa6c4c45ecaacdd371bc81ac9088d638204efe`
  - tarball sha1: `d0f1bcb5934081033442fd1bb0536d09c5bbead3`
  - npm `dist.integrity`:
    `sha512-THkAiXsMlYu5Bv6cFZmdgWWwxl39a7i793O+VPFPjMySKkeKJY4JQcO9IjJhy63cPWPiXn5tGauw1NDS8ZBF7w==`
  - binary sha256: `fcfd837103965c64de34a6b9b94370d77a347ea71819715a27d5f0ef01775ea4`
  - binary size: 222,245,312 bytes
  - `claude --version` self-reports `2.1.282 (Claude Code)`
- build manifest (carried by the Agent SDK artifact, `manifest.json`):
  - CLI build commit: `88e628ac87357ab077f78e21f78aee6156f01ab3`
  - build date: `2026-09-24T04:11:54Z`
  - its linux-x64 and darwin-arm64 checksums match the hashes computed above

Reproduce:

```sh
curl -fsSL 'https://registry.npmjs.org/@anthropic-ai%2fclaude-code/-/claude-code-2.1.282.tgz' -o claude-code-2.1.282.tgz
curl -fsSL 'https://registry.npmjs.org/@anthropic-ai%2fclaude-code-darwin-arm64/-/claude-code-darwin-arm64-2.1.282.tgz' -o claude-code-darwin-arm64-2.1.282.tgz
shasum -a 256 claude-code-2.1.282.tgz claude-code-darwin-arm64-2.1.282.tgz
tar -xzf claude-code-darwin-arm64-2.1.282.tgz
shasum -a 256 package/claude # fcfd837103965c64de34a6b9b94370d77a347ea71819715a27d5f0ef01775ea4
./package/claude --version   # 2.1.282 (Claude Code)
```

### TypeScript Agent SDK 0.3.282

- public repository: `https://github.com/anthropics/claude-agent-sdk-typescript`
  - tag `v0.3.282`, commit `9e477a178c370991ed87ca65b4c8631d390aea35`, tree
    `e3d3e4ee1d41271c26581176b7246b7c17693cf1`; provenance, not runtime
    evidence
- npm artifact: `@anthropic-ai/claude-agent-sdk@0.3.282`
  - tarball sha256: `cdb51a8df94bfa7f3d4367220b04d7f0e1adfc22799c3a22acf2250da53af366`
  - tarball sha1 (npm `dist.shasum`): `90351a7f48a1e47692e38a58588f8279a2e54d4d`
  - npm `dist.integrity`:
    `sha512-6UAerS1udzndLEx+0XW3gQWiICgfu/a+2fx/aLY3gUy+1JUQESbwYkhR40+D6d+yjueCslkLmvkPlZQFZDph6A==`
  - `package.json` declares `claudeCodeVersion: "2.1.282"`
  - file hashes inside the artifact:
    - `sdk.mjs`: `a69f9f736b5f8f4913fde6cbfc581c52612f13b4f5267d2f46aba6c62e1277bb`
    - `sdk.d.ts`: `efa0faa1630c09d3ca8f219869401cf0468e3bb85a7224947d4d93ddfdc7a32f`
    - `bridge.mjs`: `4469b2157143af8cb267af5b9583af2c0ac72fc88e3905371674882f22873862`
    - `bridge.d.ts`: `1ee98f40a605ef53c50c31aab8998a3df986298c398e8c95faacefc34fc7bac6`
    - `core.mjs` (new): `29a8108b2b93f3be612bcea596510ec1639063cd1415df2ca17df31a98588c17`
    - `manifest.json`: `041abb14aba47e7dd31f8ba83d8102e54b6350d099382cb1def7ab10add415ed`
    - `manifest.zst.json`: `817eb937c59b7283bf4b5f314aff24f7edc085e71a0c3443bb5d11a3cefc11ee`

### Python Agent SDK 0.2.159

No Python release bundles 2.1.282. The newest, `v0.2.159`, bundles 2.1.281
(`_cli_version.py` blob `b7ccc1ad93b04fc835653ac4f1977e8c7f58b114`). It is
pinned because it is the closest; nothing in it differs from 0.2.158 but the
version files, CI and changelog.

- repository: `https://github.com/anthropics/claude-agent-sdk-python`
  - tag `v0.2.159`, commit `2b87034f571b75797b976b3f32a6dbe7a03f20eb`, tree
    `f52d6ba8a97805ec31384af3580ff17628cf11c4`
  - `pyproject.toml` blob: `f8ec1d023cb020355afcde5fcb48c7aee919c053`
- files changed from `v0.2.158`: `.github/workflows/test.yml`, `CHANGELOG.md`,
  `e2e-tests/conftest.py`, `pyproject.toml`, `_cli_version.py`, `_version.py`.
  Every normative source blob the 2.1.280 ledger lists (`client.py`,
  `types.py`, `_internal/query.py`, `message_parser.py`, `subprocess_cli.py`,
  `session_resume.py`, `session_store.py`, `session_store_validation.py`) is
  unchanged.

## Wire delta from 2.1.280

`sdk.d.ts` between 0.3.280 and 0.3.282, for the frames this adapter decodes:

- `system/init`: optional `view_mode` (`focus` or `default`);
- `conversation_reset`: optional `trigger`, `user_message_uuid`, `timestamp`;
- origin `subkind` gains `session-inbox`;
- new `prewarm()`/`SpareProcess` API over a `--await-claim` flag, which this
  adapter does not pass.

The rest of the diff is settings documentation. None of it names a member
the native decoder requires or projects.

## Live verification at 2.1.282

`TestClaudeProcessRecordsCorpusProbes` ran all seventeen probes against the
2.1.282 binary and, the same hour with the same method, against the 2.1.280
binary, so the two captures differ only by the release. Method as in the
2.1.280 ledger's *Capture*: a `security` stand-in first on `PATH` (every
lookup it logged was `find-generic-password` for `Claude Code-<hash>` of the
temporary `CLAUDE_CONFIG_DIR`, and it answered not-found), `sandbox-exec`
denying the keychain daemons and every non-loopback connection, and the proxy
sink. The sink saw no connection in either run, and every probe passed.

Compared frame by frame, after removing ids, paths, timings and costs:

- Every probe emitted the same frames in the same order. Three probes placed
  the host's `{}` answer to `hook_callback` one stream frame earlier or later;
  that is the host's write racing the stream, not the CLI.
- The only CLI frame whose shape or harness-written values changed is
  `system/init`: `claude_code_version` is `2.1.282`; it adds
  `per_turn_effort_active` (`false`) and `view_mode` (`default`); `plugins` now
  lists a built-in `agents-md` plugin; `slash_commands` gains `focus` (between
  `fast` and `heapdump`) and `terminal_slash_commands` gains it too.
- `capabilities` (the same five), `tools`, `apiKeySource`, the `initialize`
  response, every `result`, `can_use_tool`, `hook_callback`, task frame,
  error text and the interrupt marker are unchanged.
- The OAP the adapter emitted is identical between the two runs apart from ids,
  timestamps and the capability revision.

## What changed in the adapter

Nothing in the adapter code of either tree. The catalog moves the pin, and
with it the endpoint version `v2.1.282` and the capability revision
`claude-code-2.1.282-oap-v1`. The advertised features are the 2.1.280
revision's. The Zig served backend reads the same revision from the catalog
and serves the Go adapter's descriptor under it, so there is no second
revision to move.

## Corpus at 2.1.282

`fixtures/adapters/claude-code-2.1.282` holds the 2.1.280 corpus's thirteen
cases, carried forward, because the captures show the wire did not change for
any frame they carry except `system/init`:

- **Re-derived from the 2.1.282 capture**: every `system/init` frame (23
  across the cases). Each keeps the members its 2.1.280 counterpart carried,
  with 2.1.282's values: `claude_code_version` `2.1.282` and `slash_commands`
  with `focus`. The new members (`view_mode`, `per_turn_effort_active`,
  `plugins`) are not added, as the 2.1.280 corpus carried no member of that
  kind either.
- **Carried forward unchanged**: every other native frame, script step and
  expectation, including the constructed frames the 2.1.280 ledger lists
  (*How a case was built*). No constructed frame was added.
- Expectations change only the capability revision. Provenance in `manifest.json`
  and every `case.json` names the 2.1.282 artifacts above.

No case was re-recorded from scratch: the 2.1.280 corpus's own live frames
were derived from probes, not copied whole, and the 2.1.282 probes reproduce
them. Neither the constructed `tools-catalog-sources` case nor the hook deny
path gained evidence at this move.
