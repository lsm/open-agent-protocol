# Validator rules: session lifecycle

Decisions [0040](../../decisions/0040-a-session-reopens-through-its-own-binding.md),
[0044](../../decisions/0044-compaction.md),
[0045](../../decisions/0045-reasoning-level-and-compaction-policy-are-session-settings.md),
[0046](../../decisions/0046-a-session-list-is-the-hosts-bindings.md),
[0049](../../decisions/0049-a-message-reports-its-own-usage.md),
[0050](../../decisions/0050-shutdown-is-graceful-forced-or-asked.md) and
[0051](../../decisions/0051-transcript-load.md).
Each row is one diagnostic site. The Go, Zig and Rust columns name the emitting
function, `—` where that tree has none. A row whose expectation is settled on
the correlated response names the function that sets the expectation. Go files
are under `go/validation/`, Zig's is `zig/src/validation/semantic.zig`, and
Rust's are under `rust/validation/src/`. Rules that are decided but that no
validator judges yet are listed last, with no code.

## 0040: reopen

| id | rule | source | code | Go | Zig | Rust | fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0040#the-capability-key-is-sessionopenreopen-and-a-reopen-is-gated-like-subscribe | An open with `reopen: true` to an endpoint that does not advertise `session.open.reopen` above `unavailable` MUST be refused `unsupported_feature` naming the key, `details.reason: "unadvertised"`. | 0040 § The capability key is `session.open.reopen` | `unavailable_capability` | reopen.go:reopenGate | reopenGate | — | session-reopen-unadvertised, session-reopen-unadvertised-wrong-refusal |
| 0040#the-capability-key-is-sessionopenreopen-and-a-reopen-is-gated-like-subscribe/2 | A reopen under a `degraded` disclosure without the caller's opt-in MUST be refused `capability_degraded`. | 0040 § The capability key is `session.open.reopen` | `degraded_without_optin` | reopen.go:reopenGate | reopenGate | — | session-reopen-degraded-admitted-without-optin |
| 0040#the-reply-is-the-state-document-and-it-declares-what-the-session-runs-under | A successful reopen MUST declare `recovery.recovered: true`. | 0040 § The capability key is `session.open.reopen`; § The reply is the state document | `session_state_mismatch` | reopen.go:reopenResponse | reopenResponse | — | session-reopen-not-recovered |
| 0040#the-capability-key-is-sessionopenreopen-and-a-reopen-is-gated-like-subscribe/3 | A successful reopen MUST list no run under way, unless the same open carried a message that admitted one. As amended by 0050 § 5, it may also list restored runs as `queued`. | 0040 § The capability key is `session.open.reopen`; amended by 0050 § 5 | `session_state_mismatch` | reopen.go:reopenResponse (unamended; see OPEN-0050-1) | reopenResponse (unamended) | — | session-reopen-with-active-run |

## 0044: compaction

| id | rule | source | code | Go | Zig | Rust | fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0044#a-requested-compaction-is-a-run | A `session.compact.request` sent before a refreshed descriptor arrives is judged stale. | drafted here (0001's revision rule applied to the request) | `stale_capability_revision` | compaction.go:compactRequest | compactRequest | — | — |
| 0044#a-requested-compaction-is-a-run/2 | A compaction electing a delivery the endpoint has not affirmatively advertised (`session.compact` itself, or `queue` without `session.message.delivery.queue`) MUST be refused `unsupported_feature`. | 0044 § Two features, one unit; § A requested compaction is a run | `unavailable_capability` | compaction.go:compactRequest | compactRequest | — | compaction-request-unadvertised, compaction-queued-not-offered |
| 0044#a-requested-compaction-is-a-run/3 | A compaction with `delivery` `steer` or `btw` MUST be refused `unsupported_feature` naming the delivery. | 0044 § A requested compaction is a run | `illegal_run_transition` | compaction.go:compactRequest | compactRequest | — | compaction-steer-wrong-refusal |
| 0044#a-requested-compaction-is-a-run/4 | A compaction carrying `focus` to an endpoint advertising `session.compact` as `degraded`, without the caller's opt-in, MUST be refused `capability_degraded`. | 0044 § A requested compaction is a run | `degraded_without_optin` | compaction.go:compactRequest | compactRequest | — | compaction-focus-degraded-without-optin |
| 0044#a-requested-compaction-is-a-run/5 | A `session.compact.response` MUST answer its request's session. | 0044 § A requested compaction is a run (submit's rules) | `scope_mismatch` | compaction.go:compactResponse | compactResponse | state/admissions.rs:compact_response | compaction-response-scope-mismatch |
| 0044#a-requested-compaction-is-a-run/6 | A `session.compact.response` MUST repeat the requested delivery, `auto` when none was asked. | 0044 § A requested compaction is a run (submit's rules) | `scope_mismatch` | compaction.go:compactResponse | compactResponse | state/admissions.rs:compact_response | — |
| 0044#a-requested-compaction-is-a-run/7 | A steer or btw compaction MUST NOT be answered with a compaction response. | 0044 § A requested compaction is a run | `illegal_run_transition` | compaction.go:compactResponse | compactResponse | state/admissions.rs:compact_response | compaction-steer-accepted |
| 0044#a-requested-compaction-is-a-run/8 | A refused compaction MUST be a correlated `error.response`, and a busy session without queue answers `run_active`. | 0044 § A requested compaction is a run | `illegal_run_transition` | compaction.go:compactResponse | compactResponse | state/admissions.rs:compact_response | compaction-busy-refused-other-code |
| 0044#a-requested-compaction-is-a-run/9 | An accepted compaction MUST reserve a run identity. | 0044 § A requested compaction is a run | `illegal_run_transition` | compaction.go:compactResponse | compactResponse | state/admissions.rs:compact_response | — |
| 0044#a-requested-compaction-is-a-run/10 | A started compaction MUST resolve to effective `start` with a `running` run, and a queued one to effective `queue` with a `queued` run. | 0044 § A requested compaction is a run; 0002 admission shapes | `illegal_run_transition` | compaction.go:compactResponse | compactResponse | state/admissions.rs:compact_response | compaction-explicit-queue-started, compaction-busy-auto-queued-without-resolution, compaction-status-before-start |
| 0044#a-requested-compaction-is-a-run/11 | A compaction MUST admit a started or queued run, never a steered or side one. | 0044 § A requested compaction is a run | `illegal_run_transition` | compaction.go:compactResponse | compactResponse | state/admissions.rs:compact_response | compaction-admission-steered |
| 0044#a-requested-compaction-is-a-run/12 | A compaction MUST NOT admit a run id already admitted. | 0001 one run per admission, applied to compaction | `illegal_run_transition` | compaction.go:compactResponse | compactResponse | state/admissions.rs:compact_response | — |
| 0044#the-barrier | A run admitted by `session.compact.request` MUST open with its compaction, the first thing after `run.started`. | 0044 § The barrier | `compaction_run_mismatch` | compaction.go:compactionOpening | compactionOpening | — | — |
| 0044#the-barrier/2 | A `requested` compaction MUST belong to the run a `session.compact.request` admitted, and no other run asks for one. | 0044 § What a compaction looks like on the wire; § The barrier | `compaction_run_mismatch` | compaction.go:compactionEvent | compactionEvent | — | compaction-requested-in-a-submitted-run, compaction-threshold-in-a-requested-run |
| 0044#the-barrier/3 | A run admitted by `session.compact.request` MUST carry exactly one compaction. | 0044 § The barrier | `compaction_run_mismatch` | compaction.go:compactionTerminal | runEvent | — | — |
| 0044#the-barrier/4 | A `run.compaction.started` MUST NOT follow another until that one has ended. | 0044 § The barrier | `compaction_unpaired` | compaction.go:compactionEvent | compactionEvent | — | compaction-unpaired, compaction-started-again-before-the-first-ended |
| 0044#the-barrier/5 | A `compaction_id` MUST be unique within the session. | 0044 § What a compaction looks like on the wire | `compaction_unpaired` | compaction.go:compactionEvent | compactionEvent | — | — |
| 0044#the-barrier/6 | A `run.compaction.ended` MUST follow its `started`. | 0044 § The barrier | `compaction_ended_without_start` | compaction.go:compactionEvent | compactionEvent | — | compaction-ended-without-start, compaction-ended-names-a-compaction-that-never-started |
| 0044#the-barrier/7 | A run terminal MUST NOT be published while a compaction is open. | 0044 § The barrier | `compaction_open_at_terminal` | compaction.go:compactionTerminal | runEvent | — | compaction-open-at-terminal, compaction-open-when-the-run-fails |
| 0044#what-a-compaction-looks-like-on-the-wire | An `ended` with `outcome: "failed"` MUST carry `error`. | 0044 § What a compaction looks like on the wire | `compaction_failed_without_error` | compaction.go:compactionEvent | compactionEvent | — | compaction-failed-without-error, compaction-failed-without-error-in-a-requested-run |
| 0044#how-a-requested-compaction-settles | A compaction run without `continue`, whose compaction completed, MUST settle `run.completed` with `stop_reason: "compacted"`. | 0044 § How a requested compaction settles | `compaction_stop_reason_mismatch` | compaction.go:compactionTerminal | runEvent | — | compaction-run-not-compacted, compaction-run-stops-for-another-reason |

## 0045: session settings

| id | rule | source | code | Go | Zig | Rust | fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0045#where-the-settings-go | An open or update supplying `reasoning_level` or `compaction_policy` through a mode the feature does not affirmatively disclose MUST be refused `unsupported_feature` naming the key. | 0045 § Validator; § Where the settings go | `unavailable_capability` | settings.go:gateSettings | settingsGate | — | session-settings-unadvertised-admitted, session-settings-unadvertised-wrong-refusal, session-settings-live-only-admitted-at-open, session-settings-live-unadvertised-admitted, session-settings-live-policy-unadvertised-admitted |
| 0045#where-the-settings-go/2 | A setting under a `degraded` feature without the caller's opt-in MUST be refused `capability_degraded`. | 0045 § Capabilities and conformance (0005's opt-in) | `degraded_without_optin` | settings.go:gateSettings | settingsGate | — | session-settings-degraded-admitted-without-optin, session-settings-live-degraded-admitted-without-optin |
| 0045#what-the-state-reports | An admitted open MUST report the reasoning level it was asked for, or refuse it. | 0045 § What the state reports | `session_state_mismatch` | settings.go:settingsResponse | settingsResponse | — | session-settings-level-rounded |
| 0045#what-the-state-reports/2 | An admitted open MUST report the compaction policy it was asked for, or refuse it. | 0045 § What the state reports | `session_state_mismatch` | settings.go:settingsResponse | settingsResponse | — | session-settings-policy-rounded |
| 0045#validator | A `session.settings.update.response` MUST answer the requested session. | 0045 § Validator (request/response scope) | `scope_mismatch` | settings.go:settingsUpdateResponse | settingsUpdateResponse | — | — |
| 0045#validator/2 | An admitted update MUST repeat the effective reasoning level it was asked for, or refuse it. | 0045 § Validator | `unapplied_control` | settings.go:settingsUpdateResponse | settingsUpdateResponse | — | session-settings-live-level-rounded |
| 0045#validator/3 | An admitted update MUST repeat the effective compaction policy it was asked for, or refuse it. | 0045 § Validator | `unapplied_control` | settings.go:settingsUpdateResponse | settingsUpdateResponse | — | session-settings-live-policy-rounded |
| 0045#validator/4 | `session.state.updated` reflecting an accepted update MUST precede the start of any run admitted after the update's response. | 0045 § Validator | `session_state_mismatch` | settings.go:settingsBeforeRun | settingsBeforeRun | — | session-settings-live-run-before-state |
| 0045#validator/5 | An accepted update MUST be reflected by a `session.state.updated` before the trace ends. | 0045 § Validator ("follows a successful update") | `session_state_mismatch` | settings.go:closeSettingsUpdates | — | — | session-settings-live-never-reflected |

## 0046: session list

| id | rule | source | code | Go | Zig | Rust | fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0046#the-capability-key-is-sessionlist-advertised-by-the-host | A `session.list.request` to an endpoint that does not affirmatively advertise `session.list` MUST be refused `unsupported_feature` naming it. | 0046 § The capability key is `session.list` | `unavailable_capability` | sessionlist.go:sessionListRequest | sessionListRequest | — | session-list-unadvertised, session-list-unadvertised-wrong-refusal |
| 0046#the-capability-key-is-sessionlist-advertised-by-the-host/2 | A list under a `degraded` `session.list` without the caller's opt-in MUST be refused `capability_degraded`. | 0046 § The capability key is `session.list` | `degraded_without_optin` | sessionlist.go:sessionListRequest | sessionListResponse | — | session-list-degraded-admitted-without-optin |
| 0046#a-list-is-paged-on-an-opaque-cursor-newest-first | A page MUST hold no more entries than the request's `limit`, or 50 when none was given. | 0046 § A list is paged on an opaque cursor | `session_list_over_limit` | sessionlist.go:sessionListResponse | sessionListResponse | — | session-list-over-limit, session-list-over-the-default-limit |
| 0046#the-list-is-the-hosts-bindings-and-only-those | A page MUST list each session once. | 0046 § The list is the host's bindings | `duplicate_session_entry` | sessionlist.go:sessionListResponse | sessionListResponse | — | session-list-duplicate-entry, session-list-lists-one-session-twice-apart |
| 0046#a-list-is-paged-on-an-opaque-cursor-newest-first/2 | Entries MUST be ordered by `updated_at_ms` descending, ties by `session_id` ascending. | 0046 § A list is paged on an opaque cursor | `session_list_order` | sessionlist.go:sessionListResponse | sessionListResponse | — | session-list-out-of-order, session-list-tie-out-of-order |

## 0049: per-message usage

| id | rule | source | code | Go | Zig | Rust | fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0049#decisions/2 | A second `content.completed` for one `message_id` in a run MUST NOT occur. | 0049 § Decisions 2 | `duplicate_message_completion` | state.go:runEvent | runEvent | state/runs.rs:advance | content-completed-twice, content-completed-twice-after-another-message |
| 0049#decisions/2/2 | A `content.delta` MUST NOT name a `message_id` a `content.completed` already closed. | 0049 § Decisions 2 | `content_after_completion` | state.go:runEvent | runEvent | state/runs.rs:advance | content-after-completion, content-after-completion-across-an-interleaved-message |

0049's rules 1 and 3 (a run event, `message_id` required) are the run-event
rules of 0001 and the schema.

## 0051: transcript load

| id | rule | source | code | Go | Zig | Rust | fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0051#8-it-is-declinable | A `transcript.load.request` to an endpoint that does not affirmatively advertise `transcript.load` MUST be refused `unsupported_feature` naming it. | 0051 § 8 | `unavailable_capability` | transcript.go:transcriptLoadRequest | transcriptLoadRequest (gatedRequest) | — | transcript-load-unadvertised |
| 0051#8-it-is-declinable/2 | A load under a `degraded` `transcript.load` without the caller's opt-in MUST be refused `capability_degraded`. | 0051 § 8 (0005's opt-in) | `degraded_without_optin` | transcript.go:transcriptLoadRequest | transcriptLoadResponse | — | transcript-load-degraded-admitted-without-optin |
| 0051#4-each-page-is-bounded-and-says-so | A page MUST hold no more entries than the request's `limit`, or 100 when none was given. | 0051 § 4 | `transcript_over_limit` | transcript.go:transcriptLoadResponse | transcriptLoadResponse | — | transcript-load-over-limit, transcript-load-over-the-default-limit |
| 0051#2-an-entry-is-oaps-and-the-transcript-is-a-list | A page MUST return each `entry_id` once. | 0051 § 2 | `duplicate_transcript_entry` | transcript.go:transcriptLoadResponse | transcriptLoadResponse | — | transcript-load-duplicate-entry, transcript-load-returns-an-entry-twice-out-of-order |
| 0051#5-a-cursor-that-is-gone-is-an-explicit-gap | A read with no cursor MUST NOT carry a `gap`. | 0051 § 1 (no cursor starts at the oldest entry); § 5 | `transcript_gap_mismatch` | transcript.go:transcriptLoadResponse | transcriptLoadResponse | — | transcript-load-gap-without-cursor |
| 0051#5-a-cursor-that-is-gone-is-an-explicit-gap/2 | A gap on a page with no entries MUST omit `resumed_at`. | 0051 § 5 | `transcript_gap_mismatch` | transcript.go:transcriptLoadResponse | transcriptLoadResponse | — | — |
| 0051#5-a-cursor-that-is-gone-is-an-explicit-gap/3 | A gap's `resumed_at` MUST be the first entry the page returns. | 0051 § 5 | `transcript_gap_mismatch` | transcript.go:transcriptLoadResponse | transcriptLoadResponse | — | transcript-load-gap-elsewhere |

A load response answering another session is 0001's request/response scope
rule (`scope_mismatch`, fixture `transcript-load-other-session`).

## Decided, not yet judged

These rules have decision text, and neither Go nor Zig judges them yet. Each
needs a code and a fixture when it is ported. They are listed so that the
rules are numbered before any port starts.

| id | rule | source |
| --- | --- | --- |
| 0040#the-three-refusals-and-each-fails-closed | A create naming a bound id MUST be refused `session_exists`, a reopen with no binding `unknown_session`, and a reopen the harness cannot load `unsupported_feature`. | 0040 § The three refusals, and each fails closed |
| 0040#a-store-that-is-gone-answers-unsupported_feature-and-the-code-comes-from-the-binding | A reopen whose binding names a store that is gone MUST be refused `unsupported_feature`, never `unknown_session`. | 0040 § A store that is gone answers `unsupported_feature` |
| 0044#two-features-one-unit | An endpoint advertising `session.compact` MUST advertise `run.compaction` at `native` or `emulated`. | 0044 § Two features, one unit |
| 0050#2-graceful-freezes-the-queue-and-the-next-start-resumes-it | Once a shutdown is accepted, new work MUST be refused `shutting_down` and never accepted. | 0050 § 2 |
| 0050#1-three-ways-to-stop-one-restart-rule | The run executing at a restart MUST settle `failed` with `error.code: "interrupted"`, `details.by: "restart"` and `settled_by: "inferred"`, and never rerun. | 0050 § 1 |
| 0050#5-a-reopen-after-a-restart-says-what-happened | A recovered reopen MUST list no run as `running`. It MAY list restored runs as `queued` in their original order, and only when `recovery.recovered: true`. | 0050 § 5 |
| 0050#5-a-reopen-after-a-restart-says-what-happened/2 | `recovery.interrupted_runs` MUST list each run settled under § 1 as `{run_id, cause: "restart", settled_by: "inferred"}`. | 0050 § 5 |
| 0050#5-a-reopen-after-a-restart-says-what-happened/3 | `recovery.redelivered_runs` MUST pair each held prompt with its new run in the original order, and each `run_id` MUST appear in `state.active_runs` as `queued`. A withdrawn prompt carries `withdrawn: true` and no `run_id`. | 0050 § 5 |
| 0051#4-each-page-is-bounded-and-says-so/2 | An entry with `truncated: true` MUST be the only entry on its page. | 0051 § 4 |
| 0051#5-a-cursor-that-is-gone-is-an-explicit-gap/4 | A `pruned` gap from a store holding no entries MUST carry no entries and `complete: true`. | 0051 § 5 |

## Divergences

- OPEN-0050-1: 0050 § 5 amends 0040 so that a recovered reopen may list
  restored runs as `queued`, and says the validator accepts them there and
  nowhere else. Both Go and Zig still diagnose any listed run on a reopen that
  carried no message. The decision decides it, so the trees follow it when
  restart recovery lands, together with the 0050 rows above. This goes into
  `research/validator-divergences.md` once that file is on main.

- Zig has no end-of-trace check that an accepted settings update was reflected
  (`closeSettingsUpdates`, row 0045#validator/5). The decision text supports
  Go, so this is a port gap and not an OPEN item.
- Rust ports only the compaction response and the per-message rules here.
