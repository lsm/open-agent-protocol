# Protocol Feedback From Eight Adapter Tranches

Status: accepted adjudication record
Date: 2026-09-10
Inputs: the eight pinned adapter mapping ledgers, their executable corpora, and
[Decision 0001](../decisions/0001-agent-control-v0.1-executable-core.md)
Outcome: [Decision 0002](../decisions/0002-admission-before-start.md) (one
accepted protocol extension); every other pattern is a documented confirmation
or non-change below.

This document closes the loop the adapter program was built for: eight
harnesses were adapted under the rule that every impedance mismatch must be
recorded explicitly, classified, and never silently compensated. With all eight
tranches verified, the accumulated mismatch ledgers are cross-indexed here and
each recurring pattern is adjudicated against the executable v0.1 core:

- **protocol change** — the evidence justifies an extension, landed with
  schema, validator, and normative fixture coverage;
- **documented non-change** — the divergence is real but v0.1 semantics are
  correct; the divergence is recorded as accepted behavior with rationale;
- **deferral confirmation** — a Decision 0001 deferral is re-tested against
  new evidence and stands.

Adapters and their pinned ledgers:

| Adapter | Pin | Ledger |
| --- | --- | --- |
| Codex app-server | `8d7cc24` | [codex-app-server-8d7cc24-mapping.md](codex-app-server-8d7cc24-mapping.md) |
| ACP / Devin | v1.7.0 | [acp-v1.7.0-mapping.md](acp-v1.7.0-mapping.md) |
| Makai | `67ad514` | [makai-agent-67ad514-mapping.md](makai-agent-67ad514-mapping.md) |
| OpenCode | v1.18.29 | [opencode-v1.18.29-mapping.md](opencode-v1.18.29-mapping.md) |
| pi | v0.85.1 | [pi-v0.85.1-mapping.md](pi-v0.85.1-mapping.md) |
| DeepSeek Harness | `47f9438` | [deepseek-harness-47f9438-mapping.md](deepseek-harness-47f9438-mapping.md) |
| Hermes | v2026.8.31 | [hermes-v2026.8.31-mapping.md](hermes-v2026.8.31-mapping.md) |
| Claude Code | 2.1.263 | [claude-code-agent-sdk-2.1.263-mapping.md](claude-code-agent-sdk-2.1.263-mapping.md) |

## PF-1. Admission before started — **protocol change (Decision 0002)**

**The pattern.** Harnesses accept submissions before they can observe a run
start, and accepted submissions can settle before any start is observable.
OAP v0.1 as frozen required every accepted submission to resolve immediately
to `admission=started`, `effective_delivery=start`, and forbade every
run-scoped event before `run.started` — including terminals. Three corpus
cases were pinned outside canonical validation specifically because of this
(`adapter/opencode/corpus_test.go`, "trace excluded from canonical v0.1
validation").

**Evidence (direct, executable):**

- OpenCode implementation outcomes #1, #3, #4
  ([ledger](opencode-v1.18.29-mapping.md)): `SessionInput.Admitted` without
  `promotedSeq` answers `admission=queued` truthfully; a 409 conflict and a
  foreign-aggregate event both settle a run before `run.started`. Corpus cases
  `queued-admission`, `message-conflict`, `foreign-session` pin the three
  shapes. The ledger itself classified this as "an OAP-core decision item, not
  an OpenCode defect".
- Hermes mismatch #9: busy-submit statuses (`steered`/`redirected`/`queued`)
  are a native delivery-mode surface; v1 rejected overlap rather than guessing.
- Claude Code mismatch #2: `queued_turn_count`/`still_queued` mean a
  submission's settlement may be a later result than the first after the send.
- pi mismatch #8: steering/follow-up queues are first-class natively; the
  corpus records them without an advertised delivery claim.
- Codex, ACP, Makai, DeepSeek each list queue/steer delivery as explicitly
  walled-off deferred scope.

All eight adapters had to make an explicit decision because of this surface;
four supply direct native-queue evidence, and one already pins executable
non-canonical traces pending exactly this extension.

**Adjudication.** Extend the executable core by the narrow slice the evidence
supports — canonical representation of an accepted-but-not-yet-started
submission and of pre-start settlement — without admitting steer, btw, side
runs, or concurrent foreground runs. See
[Decision 0002](../decisions/0002-admission-before-start.md).

**Consequence.** The schema already carried the vocabulary
(`admission: queued`, `effective_delivery: queue`, `status: queued`), so the
extension is validator semantics plus normative fixtures; the three pinned
OpenCode mismatch cases become canonical. `steered` and `side_started` remain
schema vocabulary for a future negotiated revision and stay rejected in this
subset.

## PF-2. Interaction richness — **deferral confirmation**

**Evidence.** Hermes #5 (typed interaction vocabulary split across two
mechanisms — `_block` with `request_id`+expiry vs the approval registry with
neither; secret and sudo gates; batch clarify requiring one native respond per
question); Codex (rich approval amendments and secret input unavailable; file
approvals lossy on `grantRoot`); Claude Code #12 (`can_use_tool` allow carries
a native `updatedInput` rewrite the OAP answer does not model; asks are
content-dependent; `permission_denials` is the authoritative denial record);
OpenCode #4/#8 (permissions and questions are separate native channels; the
durable stream carries no permission events at all).

**Adjudication.** Four of eight harnesses mapped native permission surfaces
through the portable interaction contract — Codex (command and file-change
approvals), ACP (`session/request_permission`), Claude Code (`can_use_tool`),
and Hermes (approval/clarify/sudo/secret gates) — each projecting the typed
request, one resolution, and cancelled through `action.permission.*`/
`user_input.*`, and disclosing the remainder as degraded or unavailable.
OpenCode is the counter-case: its ledger records separate native permission
and question channels (P0 #4), but the durable stream carries no permission
events and its adapter claims no interaction mapping at all (implementation
outcome #8); pi disables its extension surface entirely. The recurring
losses — deadlines/expiry, answers that rewrite the acted payload,
multi-question batch round trips — were all deferred by Decision 0001
(deadlines, retained reassociation) and the adapters demonstrated the loss is
survivable and visible. **Deferral stands.** The strongest future candidate is
payload-rewriting answers (Codex edited commands, Claude `updatedInput`); it
should return when a consumer requirement exists, not before.

## PF-3. Limit-reached terminals — **documented non-change**

**Evidence.** Three adapters mapped semantically adjacent output-limit
terminals differently: ACP maps `stop_reason: "max_tokens"` (and
`max_turn_requests`) to `run.completed` with the native stop reason preserved
(`adapter/acp/session.go`), Claude Code maps `error_max_turns` to
`run.completed` with `stop_reason: "max_turns"`, and DeepSeek freezes
`turn/end { max-tokens }` as `run.failed` ("truncated output is not successful
completion").

**Adjudication.** Not normalized. The two shapes encode a real semantic
difference — a limit terminal that still yields a well-formed final message
versus a generation truncated mid-stream — and both mappings are faithful to
their pinned native evidence. Both are typed and inspectable by consumers
(`stop_reason` on completed versus a `claude_*`/`acp_*` error code on failed),
and both validate. Guidance for future adapters, recorded here as the normative
reading: when the harness still delivers a usable final response at the limit,
map to `run.completed` and preserve the native reason in `stop_reason`; when
output is truncated with no final response, map to `run.failed`. Changing the
frozen DeepSeek policy would require new DeepSeek evidence, which does not
exist.

## PF-4. Background work escaping the run boundary — **deferral confirmation**

**Evidence.** Claude Code: background tasks legally outlive their turn; the
adapter publishes a held terminal on the authoritative `session_state_changed:
idle` signal and prunes child edges (review-verified). Hermes #6/#7: `btw` is
native there and nowhere else; background prompts and subagent-scoped
steer/interrupt exist natively. DeepSeek: parent terminals wait for owned child
`subagent.finished` settlement, joined by identity not ordering. pi #8:
steering/follow-up queues are first-class natively, beyond the
foreground-boundary contract (also cited under PF-1).

**Adjudication.** Decision 0001 deferred first-class subagent and
background-task lifecycles. The four adapters each demonstrated a safe
projection policy — hold the terminal until settlement evidence, or publish on
the harness's authoritative quiescence signal with the remainder recorded as
evidence — and none required protocol vocabulary beyond run-scoped tools and
terminals. **Deferral stands**, now with concrete evidence that the absorbing
terminal model holds even when background work persists past it.

## PF-5. Host strictness exceeding native tolerance — **documented non-change**

**Evidence.** Hermes #11 (the native reader tolerates and continues on
malformed lines; the adapter fails closed). DeepSeek (native transport trims
and ignores blanks; the adapter uses bounded strict LF frames — recorded as an
explicit interoperability mismatch, not silent normalization). Claude Code
(unknown frame types tolerated as evidence; known-shape violations fatal;
unknown `result` subtypes tolerated per the reference's success-or-string
typing). Codex and Makai both froze the four-way unknown-observation
classification (mapped / observed-only / required-unmapped / unsupported
request).

**Adjudication.** This is now a demonstrated cross-adapter doctrine rather
than a gap: an adapter may be strictly less tolerant than its harness, must
never be more tolerant, and the divergence must be pinned in the ledger and
disclosed through the unknown-event-handling capability. Recorded here; no
protocol change.

## PF-6. Capability truth without negotiation — **confirmation**

**Evidence.** pi #1 and Hermes #10 (no capability negotiation; descriptors
synthesized), ACP #9 (omitted native features are unavailable; config changes
revise effective behavior), DeepSeek (mutable native `initialize` frozen into
a one-shot emulated descriptor), Claude Code #10 (per-turn `system/init` is
richer than any static descriptor), OpenCode (native per-event schema
versioning alongside the OAP capability revision).

**Adjudication.** Decision 0001's rule — capabilities disclose effective OAP
behavior, fixed for a run — held across all eight. Per-turn refreshes are
recorded as evidence and never mutate a live run's revision. Confirmed; no
change.

## PF-7. Decision 0001 invariants — **confirmed by all eight**

Each frozen invariant was exercised by every tranche and its adversarial
review, with defects found and fixed where an adapter initially violated it:

- **Typed identity domains** — every ledger records its conflation traps
  (Claude Code #6 lists five; Makai #4/#8; Hermes #1; Codex and DeepSeek
  identity tables; native IDs ride in namespaced metadata or stay private).
- **Exactly one absorbing terminal** — Makai #9's duplicate terminal channels,
  OpenCode's derived settlement, Hermes' restart-reset sequence, and Claude's
  post-result informational frames all reduced to one arbiter per adapter.
- **Adapter-owned contiguous sequence** — no harness provides per-run portable
  sequence; all eight mint it (OpenCode #2, Hermes #3, Makai #7, pi #10,
  Claude #8, DeepSeek, Codex, ACP).
- **Cancellation intent is not settlement** — confirmed by Codex (interrupt
  response vs `turn/completed`), ACP #7 (unacknowledged session notification),
  Makai #5 (session-destructive), pi #6/#7 (pre-start abort retention,
  `agent_settled` authority), Claude #3 (three mechanisms, settlement only via
  `terminal_reason: aborted_*`), and Hermes; DeepSeek has no native
  cancellation and advertises none.
- **Resume ≠ reconciliation ≠ replay** — Codex (`thread/resume` is attachment,
  not replay), ACP #8 (load history is not replay), pi #10/#11, DeepSeek
  (internal persistence is not a wire capability), Claude #8 (persistence is
  not replay); OpenCode is the counterexample that proves the separation — it
  has genuine native replay and is the only adapter advertising it `native`.

## PF-8. Restart and cursor invalidation — **deferral confirmation**

**Evidence.** Hermes #3 (per-session sequence that resets on restart; the
epoch field is the native honesty marker; flagged as a candidate OAP-level
decision).

**Adjudication.** The v0.1 replay-gap contract (an expired cursor returns an
explicit gap plus authoritative state, never fake continuity) already carries
the portable requirement; a native epoch is one harness's mechanism for it.
**Deferral stands** with Hermes cited as the evidence should cross-process
continuity ever be negotiated.

## PF-9. Non-protocol findings

- **OpenCode upstream bug** (implementation outcome #2):
  `session.next.step.failed` is defined durable but absent from
  `DurableDefinitions`, so the pinned SSE filter drops it and a manifest-driven
  history reader faults. The adapter defensively decodes it (fixture
  `step-failure`). Upstream report material, not an OAP item.
- **Adapter-side convention** surfaced by this synthesis: OpenCode's
  conflict-admission path returned a submit error together with a non-nil
  event stream, against the repository convention established in the Hermes
  review. Resolved alongside PF-1 (Decision 0002 gives the path a canonical
  accepted-then-pre-start-failed shape).

## PF-10. Unowned-frame attribution — **confirmation**

**Evidence.** The same reducer discipline recurs across at least five
ledgers: Claude Code #7 (runtime-injected user-role frames must not become
phantom runs; `origin.kind` is the discriminator); Codex ("unscoped
observations are not guessed onto the active run"); Hermes (a `message.start`
turn on the session is not proof that a specific submit started it;
correlation is by construction); DeepSeek ("a candidate turn must never be
assigned merely from adjacency" — the retrospective entered-message proof);
ACP P0 #2 (updates carry no prompt/run ID, so the adapter must scope them).

**Adjudication.** Confirmed as the load-bearing reducer discipline behind
Decision 0001's typed identities and PF-1: frames are attributed to a run
only through an ownership proof minted by the admission path (an echo, a
receipt-to-turn correlation, a thread/turn scope), never through adjacency,
session identity, or arrival order — and unattributable frames are evidence
only. Every adapter implements such a proof and every corpus pins a
phantom-run rejection case. No protocol vocabulary is needed: this is
reducer-side behavior the canonical trace invariants already force (a run
cannot exist without an accepted admission). Recorded here so the pattern
has an adjudication slot like the others.

## PF-11. Native errors carry no portable codes — **documented non-change**

**Evidence.** Hermes #8: native error frames carry no code; codes live in an
optional `error_surface` and the adapter synthesizes stable namespaced codes.
Codex statuses, DeepSeek `turn/end` reasons, and Claude Code result subtypes
are similarly richer or flatter than OAP's `error.code`/`stop_reason`.

**Adjudication.** Every adapter already synthesizes stable, namespaced,
adapter-prefixed codes (`acp_*`, `claude_*`, `opencode_*`, `deepseek_*`, …)
and the schema keeps `code` an open string. A shared cross-harness code
registry would be premature: no consumer requirement exists and the mappings
are pinned per ledger. No change.

## PF-12. Provider provisioning has no native operation to map — **unit held**

**Evidence.** Searched for a provider write across all eight ledgers. The only
provider-shaped operations recorded anywhere are OpenCode's `provider.list` and
`provider.list/get`, both reads. No create, add, register, attach or set
appears in any tranche.

Three harnesses take a `provider` argument and all three are selecting from a
catalog the endpoint already held, not introducing one:

- **Hermes** — `session.create {… profile?, model?, provider?, …}`. The
  integration gate records the mechanism plainly: the provider endpoint is an
  in-process loopback reached through `OPENAI_BASE_URL`, the credential is a
  test-owned fixture key, and the model "statically resolves to the openai
  provider in the pinned catalog".
- **DeepSeek** — `initialize {cwd, provider, model, maxTokens?}`, with the
  endpoint supplied out of band through `DEEPSEEK_BASE_URL`.
- **OpenCode** — `ModelRef {id, providerID, variant?}` on a session, resolved
  against `provider.list/get`.

How the operator supplies the endpoint is not stated here, because it differs
per harness and the adjudication does not turn on it. Hermes is the reason to
say so rather than generalise: its gate reaches the loopback through a
`config.yaml` `custom_providers` entry, and its ledger records that for a named
provider `OPENAI_BASE_URL` is *deliberately ignored* as stale env poisoning, so
"the environment cannot do it". Only the credential is env-borne there.

What all three do share is the only thing the adjudication rests on: the
session argument names an entry the endpoint resolves against state it already
held — `provider.list/get` for OpenCode, the pinned catalog for Hermes — rather
than introducing one. How the operator configured that state varies and does
not matter here.

**Adjudication.** [The composition draft](../drafts/composition.md) records an
empty provisioning row beside a complete tool-sources row, and
[Decision 0017](../decisions/0017-provider-provisioning.md) writes that row as
a `providers[]` attachment at session open. The row is real, but it is two
questions and they have opposite standing.

*Selecting* a provider is already expressible and needs no unit.
`modelDescriptor` carries `provider_id` and `modelsResponse` carries
`providers[]` of `providerDescriptor` with `wire`, `kind` and `endpoint`, so a
control layer reads `models.list`, sees every model tagged with its provider
and every provider's shape, and names one with `submit.model_id`. Choosing the
model chooses the provider. This is the half with native evidence -- every
harness that exposes a provider catalog at all supports it, and the ledgers
record no harness refusing it -- and it landed with 0006 and 0014 without
anyone recording that it closed half of the missing row.

*Attaching* a provider the loop does not have is what 0017 specifies, and it
has no native evidence at this pin. [Decision 0003](../decisions/0003-staged-unit-graduation.md)
step 3 requires a native adapter this project does not control to execute the
unit through its production codec and reducer, advertised at the level the
evidence supports. There is no operation to execute. It also cannot honestly
graduate `emulated`, because emulation needs a native behavior to stand over
and the only available substitute — respawning the child with a different
environment — is process lifecycle rather than a session operation, and would
silently change the identity of every session already open on that child.

0017 is therefore **held at proposed**, not withdrawn. Its reasoning survives
intact and its dependency on 0014 has since been satisfied; what is missing is
upstream, not in this tree. It becomes graduable when a pinned harness gains a
provider-introduction operation, or when a tranche brings a harness that has
one. The `environment` bare-`NAME` allowlist it specifies is the right shape
for that day and should not be redesigned in the meantime.

Recorded because the composition table invites the opposite conclusion. Read
alone it shows one empty row and one accepted precedent (`tool_sources`,
Decision 0008) and reads as a single unit of work, unblocked. It is not, and
the reason is not visible from the table.

**Actionable instead.** `run.model_selection` is declared by exactly one
adapter, Codex, at `native`/`ScopeRun`. Hermes, OpenCode, Pi and DeepSeek all
accept a model natively — session-scoped for the first three, connection-scoped
for DeepSeek, which fits neither `ScopeRun` nor `ScopeSession` and is its own
mismatch to record. Those are unmapped rows with evidence available today,
where 0017's is a mapped design with no evidence at all.

## What was deliberately not changed

No evidence in eight tranches supported pulling forward: durable idempotent
admission, contextual/provisional capability composition, concurrent foreground
runs, steer or btw delivery, continuity leases, cross-process persistence,
orphan terminals, or first-class subagent vocabulary. Each remains deferred
exactly as Decision 0001 records, now with the additional citations above.

## Outcome summary

| Pattern | Adjudication | Artifact |
| --- | --- | --- |
| PF-1 admission before started | protocol change | Decision 0002; validator + fixtures; three OpenCode corpus cases canonicalized |
| PF-2 interaction richness | deferral stands | this document |
| PF-3 limit terminals | non-change + guidance | this document |
| PF-4 background/side work | deferral stands | this document |
| PF-5 strictness doctrine | non-change, recorded | this document |
| PF-6 capability disclosure | confirmation | this document |
| PF-7 Decision 0001 invariants | confirmed ×8 | this document |
| PF-8 restart/cursor epochs | deferral stands | this document |
| PF-9 upstream/convention | recorded | ledger + adapter fix |
| PF-10 unowned-frame attribution | confirmation | this document |
| PF-11 error-code synthesis | non-change, recorded | this document |
| PF-12 provider provisioning | unit held, no native evidence | this document; Decision 0017 stays proposed |

## Review outcome (tranche verified)

Independent adversarial review of this tranche returned six findings; each
was independently verified before fixing:

1. **The validator accepted multiple pre-start terminals** for a
   never-started run — the duplicate-terminal check still gated on
   `run.started`. Reproduced fail-before (a queued admission followed by two
   `run.failed` events validated) and fixed; the new negative fixture
   `admission-prestart-duplicate-terminal` pins it (pass-after).
2. **The error-plus-dangling-stream convention survived on two OpenCode
   paths** (invalid native message identity, foreign admission) that PF-9
   described as resolved. Fixed: every reserved-run failure path now returns
   the accepted queued reservation (regression test
   `TestPreStartFailuresReportReservation`).
3. **The `status` member of the admission shapes was unenforced.** Now
   canonical: `started` must report `running`, `queued` must report
   `queued`. Reproduced fail-before (`admission-status-mismatch` fixture
   validated), fixed, pass-after. This also exposed and fixed a genuine
   pre-existing incoherence: the Codex adapter reported
   `admission=started` with `status=queued` since the first tranche.
4. **PF-2's "five of eight … all five projected" was unfaithful to
   OpenCode**, which claims no interaction mapping. Rewritten: four
   projectors (Codex, ACP, Claude Code, Hermes) with OpenCode as the
   recorded counter-case.
5. **PF-4 cited pi #11 for follow-up queues; #11 is session-tree/fork.**
   Corrected to pi #8.
6. **Material omission:** unowned-frame attribution (the phantom-run
   discipline) recurs across five ledgers and had no adjudication slot.
   Added as PF-10, with the adjacent native-error-code synthesis recorded as
   PF-11.

Clean bills from the review, spot-verified here: the PF-1 evidence chain and
every other ledger citation; validator negative probes (pre-start
non-terminal events, pre-start completion, cancelled-without-accepted-pair,
unstarted-unsettled reservations, sequence discipline, one-reservation
sessions); fixture/manifest exactness; the OpenCode locking and teardown
review; and decision 0002's internal consistency apart from findings 1 and 3.
Post-fix battery: `oap check` (43 fixtures), full, short, and race suites,
vet, gofmt, and `git diff --check` all green.
