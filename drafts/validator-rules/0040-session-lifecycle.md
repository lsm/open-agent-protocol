# Session lifecycle

Decisions 0040, 0044, 0045, 0046, 0049, 0050 and 0051: the rows behind every
diagnostic the validators raise for reopen, compaction, session settings, the
session list, per-message usage and transcript load, and the decided rules no
validator judges yet (code and implementations `—`). Go names the function in
`go/validation/`, Zig in `zig/src/validation/`, Rust in `rust/validation/src/`.
A row whose expectation is settled on the correlated response names the
function that sets it. Open item: OPEN-0050-1 below. Format:
[README](README.md).

### [0040] The capability key is `session.open.reopen`, and a reopen is gated like `subscribe`

Source: [`decisions/0040-a-session-reopens-through-its-own-binding.md`](../../decisions/0040-a-session-reopens-through-its-own-binding.md#the-capability-key-is-sessionopenreopen-and-a-reopen-is-gated-like-subscribe).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0040#the-capability-key-is-sessionopenreopen-and-a-reopen-is-gated-like-subscribe/1` | An open with `reopen: true` to an endpoint that does not advertise `session.open.reopen` above `unavailable` MUST be refused `unsupported_feature` naming the key, `details.reason: "unadvertised"`. | L89–106 | `unavailable_capability` | `reopen.go:reopenGate` | `semantic.zig:reopenGate` | — |
| `0040#the-capability-key-is-sessionopenreopen-and-a-reopen-is-gated-like-subscribe/2` | A reopen under a `degraded` disclosure without the caller's opt-in MUST be refused `capability_degraded`. | L89–106 | `degraded_without_optin` | `reopen.go:reopenGate` | `semantic.zig:reopenGate` | — |
| `0040#the-capability-key-is-sessionopenreopen-and-a-reopen-is-gated-like-subscribe/3` | A successful reopen MUST list no run under way, unless the same open carried a message that admitted one. As amended by 0050 § 5, it may also list restored runs as `queued`. | L89–106 | `session_state_mismatch` | `reopen.go:reopenResponse` (unamended; see OPEN-0050-1) | `semantic.zig:reopenResponse` (unamended) | — |

### [0040] The reply is the state document, and it declares what the session runs under

Source: [`decisions/0040-a-session-reopens-through-its-own-binding.md`](../../decisions/0040-a-session-reopens-through-its-own-binding.md#the-reply-is-the-state-document-and-it-declares-what-the-session-runs-under).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0040#the-reply-is-the-state-document-and-it-declares-what-the-session-runs-under/1` | A successful reopen MUST declare `recovery.recovered: true`. | L89–106, L109–130 | `session_state_mismatch` | `reopen.go:reopenResponse` | `semantic.zig:reopenResponse` | — |

### [0044] A requested compaction is a run

Source: [`decisions/0044-compaction.md`](../../decisions/0044-compaction.md#a-requested-compaction-is-a-run).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0044#a-requested-compaction-is-a-run/1` | A `session.compact.request` sent before a refreshed descriptor arrives is judged stale. | agent-control-core L777–812 (the freshness rule, `agent-control-core#capability-snapshot-freshness/1`, applied to the request) | `stale_capability_revision` | `compaction.go:compactRequest` | `semantic.zig:compactRequest` | — |
| `0044#a-requested-compaction-is-a-run/2` | A compaction electing a delivery the endpoint has not affirmatively advertised (`session.compact` itself, or `queue` without `session.message.delivery.queue`) MUST be refused `unsupported_feature`. | L62–72, L75–107 | `unavailable_capability` | `compaction.go:compactRequest` | `semantic.zig:compactRequest` | — |
| `0044#a-requested-compaction-is-a-run/3` | A compaction with `delivery` `steer` or `btw` MUST be refused `unsupported_feature` naming the delivery. | L75–107 | `illegal_run_transition` | `compaction.go:compactRequest` | `semantic.zig:compactRequest` | — |
| `0044#a-requested-compaction-is-a-run/4` | A compaction carrying `focus` to an endpoint advertising `session.compact` as `degraded`, without the caller's opt-in, MUST be refused `capability_degraded`. | L75–107 | `degraded_without_optin` | `compaction.go:compactRequest` | `semantic.zig:compactRequest` | — |
| `0044#a-requested-compaction-is-a-run/5` | A `session.compact.response` MUST answer its request's session. | L75–107 | `scope_mismatch` | `compaction.go:compactResponse` | `semantic.zig:compactResponse` | `state/admissions.rs:compact_response` |
| `0044#a-requested-compaction-is-a-run/6` | A `session.compact.response` MUST repeat the requested delivery, `auto` when none was asked. | L75–107 | `scope_mismatch` | `compaction.go:compactResponse` | `semantic.zig:compactResponse` | `state/admissions.rs:compact_response` |
| `0044#a-requested-compaction-is-a-run/7` | A steer or btw compaction MUST NOT be answered with a compaction response. | L75–107 | `illegal_run_transition` | `compaction.go:compactResponse` | `semantic.zig:compactResponse` | `state/admissions.rs:compact_response` |
| `0044#a-requested-compaction-is-a-run/8` | A refused compaction MUST be a correlated `error.response`, and a busy session without queue answers `run_active`. | L75–107 | `illegal_run_transition` | `compaction.go:compactResponse` | `semantic.zig:compactResponse` | `state/admissions.rs:compact_response` |
| `0044#a-requested-compaction-is-a-run/9` | An accepted compaction MUST reserve a run identity. | L75–107 | `illegal_run_transition` | `compaction.go:compactResponse` | `semantic.zig:compactResponse` | `state/admissions.rs:compact_response` |
| `0044#a-requested-compaction-is-a-run/10` | A started compaction MUST resolve to effective `start` with a `running` run, and a queued one to effective `queue` with a `queued` run. | L75–107 | `illegal_run_transition` | `compaction.go:compactResponse` | `semantic.zig:compactResponse` | `state/admissions.rs:compact_response` |
| `0044#a-requested-compaction-is-a-run/11` | A compaction MUST admit a started or queued run, never a steered or side one. | L75–107 | `illegal_run_transition` | `compaction.go:compactResponse` | `semantic.zig:compactResponse` | `state/admissions.rs:compact_response` |
| `0044#a-requested-compaction-is-a-run/12` | A compaction MUST NOT admit a run id already admitted. | 0001 L46–61 (one admission, one run; applied to compaction) | `illegal_run_transition` | `compaction.go:compactResponse` | `semantic.zig:compactResponse` | `state/admissions.rs:compact_response` |

### [0044] The barrier

Source: [`decisions/0044-compaction.md`](../../decisions/0044-compaction.md#the-barrier).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0044#the-barrier/1` | A run admitted by `session.compact.request` MUST open with its compaction, the first thing after `run.started`. | L165–173 | `compaction_run_mismatch` | `compaction.go:compactionOpening` | `semantic.zig:compactionOpening` | — |
| `0044#the-barrier/2` | A `requested` compaction MUST belong to the run a `session.compact.request` admitted, and no other run asks for one. | L110–145, L165–173 | `compaction_run_mismatch` | `compaction.go:compactionEvent` | `semantic.zig:compactionEvent` | — |
| `0044#the-barrier/3` | A run admitted by `session.compact.request` MUST carry exactly one compaction. | L165–173 | `compaction_run_mismatch` | `compaction.go:compactionTerminal` | `semantic.zig:runEvent` | — |
| `0044#the-barrier/4` | A `run.compaction.started` MUST NOT follow another until that one has ended. | L165–173 | `compaction_unpaired` | `compaction.go:compactionEvent` | `semantic.zig:compactionEvent` | — |
| `0044#the-barrier/5` | A `compaction_id` MUST be unique within the session. | L110–145 | `compaction_unpaired` | `compaction.go:compactionEvent` | `semantic.zig:compactionEvent` | — |
| `0044#the-barrier/6` | A `run.compaction.ended` MUST follow its `started`. | L165–173 | `compaction_ended_without_start` | `compaction.go:compactionEvent` | `semantic.zig:compactionEvent` | — |
| `0044#the-barrier/7` | A run terminal MUST NOT be published while a compaction is open. | L165–173 | `compaction_open_at_terminal` | `compaction.go:compactionTerminal` | `semantic.zig:runEvent` | — |

### [0044] What a compaction looks like on the wire

Source: [`decisions/0044-compaction.md`](../../decisions/0044-compaction.md#what-a-compaction-looks-like-on-the-wire).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0044#what-a-compaction-looks-like-on-the-wire/1` | An `ended` with `outcome: "failed"` MUST carry `error`. | L110–145 | `compaction_failed_without_error` | `compaction.go:compactionEvent` | `semantic.zig:compactionEvent` | — |

### [0044] How a requested compaction settles

Source: [`decisions/0044-compaction.md`](../../decisions/0044-compaction.md#how-a-requested-compaction-settles).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0044#how-a-requested-compaction-settles/1` | A compaction run without `continue`, whose compaction completed, MUST settle `run.completed` with `stop_reason: "compacted"`. | L148–162 | `compaction_stop_reason_mismatch` | `compaction.go:compactionTerminal` | `semantic.zig:runEvent` | — |

### [0045] Where the settings go

Source: [`decisions/0045-reasoning-level-and-compaction-policy-are-session-settings.md`](../../decisions/0045-reasoning-level-and-compaction-policy-are-session-settings.md#where-the-settings-go).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0045#where-the-settings-go/1` | An open or update supplying `reasoning_level` or `compaction_policy` through a mode the feature does not affirmatively disclose MUST be refused `unsupported_feature` naming the key. | L247–260, L162–194 | `unavailable_capability` | `settings.go:gateSettings` | `semantic.zig:settingsGate` | — |
| `0045#where-the-settings-go/2` | A setting under a `degraded` feature without the caller's opt-in MUST be refused `capability_degraded`. | L225–244 | `degraded_without_optin` | `settings.go:gateSettings` | `semantic.zig:settingsGate` | — |

### [0045] What the state reports

Source: [`decisions/0045-reasoning-level-and-compaction-policy-are-session-settings.md`](../../decisions/0045-reasoning-level-and-compaction-policy-are-session-settings.md#what-the-state-reports).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0045#what-the-state-reports/1` | An admitted open MUST report the reasoning level it was asked for, or refuse it. | L197–222 | `session_state_mismatch` | `settings.go:settingsResponse` | `semantic.zig:settingsResponse` | — |
| `0045#what-the-state-reports/2` | An admitted open MUST report the compaction policy it was asked for, or refuse it. | L197–222 | `session_state_mismatch` | `settings.go:settingsResponse` | `semantic.zig:settingsResponse` | — |

### [0045] Validator

Source: [`decisions/0045-reasoning-level-and-compaction-policy-are-session-settings.md`](../../decisions/0045-reasoning-level-and-compaction-policy-are-session-settings.md#validator).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0045#validator/1` | A `session.settings.update.response` MUST answer the requested session. | L247–260 | `scope_mismatch` | `settings.go:settingsUpdateResponse` | `semantic.zig:settingsUpdateResponse` | — |
| `0045#validator/2` | An admitted update MUST repeat the effective reasoning level it was asked for, or refuse it. | L247–260 | `unapplied_control` | `settings.go:settingsUpdateResponse` | `semantic.zig:settingsUpdateResponse` | — |
| `0045#validator/3` | An admitted update MUST repeat the effective compaction policy it was asked for, or refuse it. | L247–260 | `unapplied_control` | `settings.go:settingsUpdateResponse` | `semantic.zig:settingsUpdateResponse` | — |
| `0045#validator/4` | `session.state.updated` reflecting an accepted update MUST precede the start of any run admitted after the update's response. | L247–260 | `session_state_mismatch` | `settings.go:settingsBeforeRun` | `semantic.zig:settingsBeforeRun` | — |
| `0045#validator/5` | An accepted update MUST be reflected by a `session.state.updated` before the trace ends. | L247–260 | `session_state_mismatch` | `settings.go:closeSettingsUpdates` | — | — |

### [0046] The capability key is `session.list`, advertised by the host

Source: [`decisions/0046-a-session-list-is-the-hosts-bindings.md`](../../decisions/0046-a-session-list-is-the-hosts-bindings.md#the-capability-key-is-sessionlist-advertised-by-the-host).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0046#the-capability-key-is-sessionlist-advertised-by-the-host/1` | A `session.list.request` to an endpoint that does not affirmatively advertise `session.list` MUST be refused `unsupported_feature` naming it. | L91–96 | `unavailable_capability` | `sessionlist.go:sessionListRequest` | `semantic.zig:sessionListRequest` | — |
| `0046#the-capability-key-is-sessionlist-advertised-by-the-host/2` | A list under a `degraded` `session.list` without the caller's opt-in MUST be refused `capability_degraded`. | L91–96 | `degraded_without_optin` | `sessionlist.go:sessionListRequest` | `semantic.zig:sessionListResponse` | — |

### [0046] A list is paged on an opaque cursor, newest first

Source: [`decisions/0046-a-session-list-is-the-hosts-bindings.md`](../../decisions/0046-a-session-list-is-the-hosts-bindings.md#a-list-is-paged-on-an-opaque-cursor-newest-first).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0046#a-list-is-paged-on-an-opaque-cursor-newest-first/1` | A page MUST hold no more entries than the request's `limit`, or 50 when none was given. | L77–88 | `session_list_over_limit` | `sessionlist.go:sessionListResponse` | `semantic.zig:sessionListResponse` | — |
| `0046#a-list-is-paged-on-an-opaque-cursor-newest-first/2` | Entries MUST be ordered by `updated_at_ms` descending, ties by `session_id` ascending. | L77–88 | `session_list_order` | `sessionlist.go:sessionListResponse` | `semantic.zig:sessionListResponse` | — |

### [0046] The list is the host's bindings, and only those

Source: [`decisions/0046-a-session-list-is-the-hosts-bindings.md`](../../decisions/0046-a-session-list-is-the-hosts-bindings.md#the-list-is-the-hosts-bindings-and-only-those).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0046#the-list-is-the-hosts-bindings-and-only-those/1` | A page MUST list each session once. | L51–74 | `duplicate_session_entry` | `sessionlist.go:sessionListResponse` | `semantic.zig:sessionListResponse` | — |

### [0049] 2. It closes the message it names, once

Source: [`decisions/0049-a-message-reports-its-own-usage.md`](../../decisions/0049-a-message-reports-its-own-usage.md#decisions).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0049#2/1` | A second `content.completed` for one `message_id` in a run MUST NOT occur. | L38–41 | `duplicate_message_completion` | `state.go:runEvent` | `semantic.zig:runEvent` | `state/runs.rs:advance` |
| `0049#2/2` | A `content.delta` MUST NOT name a `message_id` a `content.completed` already closed. | L38–41 | `content_after_completion` | `state.go:runEvent` | `semantic.zig:runEvent` | `state/runs.rs:advance` |

### [0051] 8. It is declinable

Source: [`decisions/0051-transcript-load.md`](../../decisions/0051-transcript-load.md#8-it-is-declinable).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0051#8/1` | A `transcript.load.request` to an endpoint that does not affirmatively advertise `transcript.load` MUST be refused `unsupported_feature` naming it. | L136–143 | `unavailable_capability` | `transcript.go:transcriptLoadRequest` | `semantic.zig:transcriptLoadRequest` (gatedRequest) | — |
| `0051#8/2` | A load under a `degraded` `transcript.load` without the caller's opt-in MUST be refused `capability_degraded`. | L136–143 | `degraded_without_optin` | `transcript.go:transcriptLoadRequest` | `semantic.zig:transcriptLoadResponse` | — |

### [0051] 4. Each page is bounded and says so

Source: [`decisions/0051-transcript-load.md`](../../decisions/0051-transcript-load.md#4-each-page-is-bounded-and-says-so).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0051#4/1` | A page MUST hold no more entries than the request's `limit`, or 100 when none was given. | L89–96 | `transcript_over_limit` | `transcript.go:transcriptLoadResponse` | `semantic.zig:transcriptLoadResponse` | — |
| `0051#4/2` | An entry with `truncated: true` MUST be the only entry on its page. | L89–96 | — | — | — | — |

### [0051] 2. An entry is OAP's, and the transcript is a list

Source: [`decisions/0051-transcript-load.md`](../../decisions/0051-transcript-load.md#2-an-entry-is-oaps-and-the-transcript-is-a-list).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0051#2/1` | A page MUST return each `entry_id` once. | L51–73 | `duplicate_transcript_entry` | `transcript.go:transcriptLoadResponse` | `semantic.zig:transcriptLoadResponse` | — |

### [0051] 5. A cursor that is gone is an explicit gap

Source: [`decisions/0051-transcript-load.md`](../../decisions/0051-transcript-load.md#5-a-cursor-that-is-gone-is-an-explicit-gap).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0051#5/1` | A read with no cursor MUST NOT carry a `gap`. | L41–48, L99–116 | `transcript_gap_mismatch` | `transcript.go:transcriptLoadResponse` | `semantic.zig:transcriptLoadResponse` | — |
| `0051#5/2` | A gap on a page with no entries MUST omit `resumed_at`. | L99–116 | `transcript_gap_mismatch` | `transcript.go:transcriptLoadResponse` | `semantic.zig:transcriptLoadResponse` | — |
| `0051#5/3` | A gap's `resumed_at` MUST be the first entry the page returns. | L99–116 | `transcript_gap_mismatch` | `transcript.go:transcriptLoadResponse` | `semantic.zig:transcriptLoadResponse` | — |
| `0051#5/4` | A `pruned` gap from a store holding no entries MUST carry no entries and `complete: true`. | L99–116 | — | — | — | — |

### [0040] The three refusals, and each fails closed

Source: [`decisions/0040-a-session-reopens-through-its-own-binding.md`](../../decisions/0040-a-session-reopens-through-its-own-binding.md#the-three-refusals-and-each-fails-closed).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0040#the-three-refusals-and-each-fails-closed/1` | A create naming a bound id MUST be refused `session_exists`, a reopen with no binding `unknown_session`, and a reopen the harness cannot load `unsupported_feature`. | L133–142 | — | — | — | — |

### [0040] A store that is gone answers `unsupported_feature`, and the code comes from the binding

Source: [`decisions/0040-a-session-reopens-through-its-own-binding.md`](../../decisions/0040-a-session-reopens-through-its-own-binding.md#a-store-that-is-gone-answers-unsupported_feature-and-the-code-comes-from-the-binding).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0040#a-store-that-is-gone-answers-unsupported_feature-and-the-code-comes-from-the-binding/1` | A reopen whose binding names a store that is gone MUST be refused `unsupported_feature`, never `unknown_session`. | L145–170 | — | — | — | — |

### [0044] Two features, one unit

Source: [`decisions/0044-compaction.md`](../../decisions/0044-compaction.md#two-features-one-unit).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0044#two-features-one-unit/1` | An endpoint advertising `session.compact` MUST advertise `run.compaction` at `native` or `emulated`. | L62–72 | — | — | — | — |

### [0050] 2. Graceful freezes the queue, and the next start resumes it

Source: [`decisions/0050-shutdown-is-graceful-forced-or-asked.md`](../../decisions/0050-shutdown-is-graceful-forced-or-asked.md#2-graceful-freezes-the-queue-and-the-next-start-resumes-it).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0050#2/1` | Once a shutdown is accepted, new work MUST be refused `shutting_down` and never accepted. | L83–109 | — | — | — | — |

### [0050] 1. Three ways to stop, one restart rule

Source: [`decisions/0050-shutdown-is-graceful-forced-or-asked.md`](../../decisions/0050-shutdown-is-graceful-forced-or-asked.md#1-three-ways-to-stop-one-restart-rule).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0050#1/1` | The run executing at a restart MUST settle `failed` with `error.code: "interrupted"`, `details.by: "restart"` and `settled_by: "inferred"`, and never rerun. | L38–80 | — | — | — | — |

### [0050] 5. A reopen after a restart says what happened

Source: [`decisions/0050-shutdown-is-graceful-forced-or-asked.md`](../../decisions/0050-shutdown-is-graceful-forced-or-asked.md#5-a-reopen-after-a-restart-says-what-happened).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0050#5/1` | A recovered reopen MUST list no run as `running`. It MAY list restored runs as `queued` in their original order, and only when `recovery.recovered: true`. | L156–185 | — | — | — | — |
| `0050#5/2` | `recovery.interrupted_runs` MUST list each run settled under § 1 as `{run_id, cause: "restart", settled_by: "inferred"}`. | L156–185 | — | — | — | — |
| `0050#5/3` | `recovery.redelivered_runs` MUST pair each held prompt with its new run in the original order, and each `run_id` MUST appear in `state.active_runs` as `queued`. A withdrawn prompt carries `withdrawn: true` and no `run_id`. | L156–185 | — | — | — | — |

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
