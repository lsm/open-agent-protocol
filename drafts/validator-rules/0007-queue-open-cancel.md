# Queue, compound open and cancel

Decisions 0007, 0009 and 0041: the rows behind every diagnostic the validators
raise for queue admission and refusal, session-state snapshots, compound opens
and cancel acceptance. Format: [README](README.md).

### [0007] One started run, and reservations behind it

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#one-started-run-and-reservations-behind-it).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#one-started-run-and-reservations-behind-it/1` | A second admission on a session that holds a nonterminal run MUST be a reservation: `admission` `queued`, `effective_delivery` `queue`, `status` `queued`. | L42–46 | `illegal_run_transition` | `queue.go:queueOverlap` | yes | `queue.rs:queue_overlap` |
| `0007#one-started-run-and-reservations-behind-it/2` | A reservation MUST NOT be admitted unless the active descriptor advertises `session.message.delivery.queue`. | L44–45 | `unavailable_capability` | `queue.go:queueAdmission` | yes | `queue.rs:queue_admission` |
| `0007#one-started-run-and-reservations-behind-it/3` | A reservation admitted under a `degraded` queue disclosure MUST have been requested with that key in `allow_degraded_features`. | L44–45, L87–89 | `degraded_without_optin` | `queue.go:queueAdmission` | yes | `queue.rs:queue_admission` |
| `0007#one-started-run-and-reservations-behind-it/4` | An explicit `queue` delivery MUST be admitted as a reservation, whatever the session holds, and never resolves to `start`. | L48–51 | `illegal_run_transition` | `queue.go:queueAdmission` | yes | `queue.rs:queue_admission` |
| `0007#one-started-run-and-reservations-behind-it/5` | An `auto` submission a busy session turns into a reservation MUST report `delivery_resolution: "session_busy"`. | L51–55 | `illegal_run_transition` | `queue.go:queueAdmission` | yes | `queue.rs:queue_admission` |

### [0007] One run executes at a time, in admission order

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#one-run-executes-at-a-time-in-admission-order).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#one-run-executes-at-a-time-in-admission-order/1` | A later-admitted run MUST NOT publish a sequenced event while an earlier-admitted run of its session is nonterminal, except the pre-start terminal of a run that never started and run-scoped requests and responses. | L59–76 | `queue_order_violation` | `queue.go:checkQueueOrder` | yes | `queue.rs:check_queue_order` |

### [0007] A queue is a promise, and the bound is what makes it checkable

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable/1` | A descriptor advertising the queue above `unavailable` MUST disclose a positive `max_queued_runs_per_session`. | L84–90 | `undisclosed_queue_limit` | `queue.go:checkQueueLimits` | yes | `queue.rs:check_queue_limits` |
| `0007#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable/2` | Where the queue is available or `degraded`, a stated `max_active_runs_per_session` MUST be at least `max_queued_runs_per_session + 1`. | L92–100 | `undisclosed_queue_limit` | `queue.go:checkQueueLimits` | yes | `queue.rs:check_queue_limits` |
| `0007#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable/3` | An admission MUST NOT put the session's nonterminal set above the disclosed active bound; a set admitted before a lowering refresh is grandfathered. | L80–82, L102–105 | `queue_limit_exceeded` | `queue.go:queueAdmission` | yes | `queue.rs:queue_admission` |
| `0007#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable/4` | An admission MUST NOT put the session's queued subset above the disclosed queued bound; a set admitted before a lowering refresh is grandfathered. | L80–82, L102–105 | `queue_limit_exceeded` | `queue.go:queueAdmission` | yes | `queue.rs:queue_admission` |
| `0007#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable/5` | A capabilities response repeating the active revision MUST repeat the queue bounds already disclosed under it. | L107–122 | `stale_capability_revision` | `queue.go:checkQueueAdvertisement` | yes | `queue.rs:record_limits` |
| `0007#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable/6` | A capabilities response repeating the active revision MUST repeat the queue's advertised level. | L107–122 | `stale_capability_revision` | `queue.go:checkQueueAdvertisement` | yes | `queue.rs:record_limits` |

### [0007] The refusal is validated as well as the admission

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#the-refusal-is-validated-as-well-as-the-admission).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#the-refusal-is-validated-as-well-as-the-admission/1` | A refusal owed because the session is busy MUST carry the wire code `run_active`. | L131–135 | `illegal_run_transition` | `queue.go:settleQueueRefusal` | yes | `queue.rs:settle_queue_refusal` |
| `0007#the-refusal-is-validated-as-well-as-the-admission/2` | A refusal owed because a queue bound was reached MUST carry the wire code `run_active`. | L131–135 | `queue_limit_exceeded` | `queue.go:settleQueueRefusal` | yes | `queue.rs:settle_queue_refusal` |
| `0007#the-refusal-is-validated-as-well-as-the-admission/3` | A `run_active` refusal MUST NOT report a bound that was unreached throughout its request/response window, counting every other unanswered submit on the session. | L137–175 | `queue_limit_exceeded` | `queue.go:settleQueueRefusal` | yes | `queue.rs:settle_queue_refusal` |

### [0007] A snapshot states the position it was captured at

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#a-snapshot-states-the-position-it-was-captured-at).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#a-snapshot-states-the-position-it-was-captured-at/1` | A state document MUST list each run in `active_runs` at most once. | L189–190 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkListing` | `snapshot_listing.rs:check_snapshot_listing` |
| `0007#a-snapshot-states-the-position-it-was-captured-at/2` | An `active_runs` entry MUST name a run the trace carries for that session, unless it anchors only submit requests still awaiting their response. | L192–194, L419–426 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkListing` | `snapshot_listing.rs:check_snapshot_listing` |
| `0007#a-snapshot-states-the-position-it-was-captured-at/3` | An entry MUST NOT list a run that had settled before the read was requested. | L294–297 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkListing` | `snapshot_listing.rs:check_snapshot_listing` |
| `0007#a-snapshot-states-the-position-it-was-captured-at/4` | An entry MUST NOT state a position at or past the sequence its run settled at. | L297–301 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkListing` | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/5` | An entry MUST NOT carry a terminal status; a settled run is dropped and named in `as_of.settled`. | L302–304, L438–439 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queuestate.go:checkEntryStatus` | `checkListing` | `snapshot_listing.rs:check_snapshot_listing` |
| `0007#a-snapshot-states-the-position-it-was-captured-at/6` | An entry MUST NOT call a run `queued` at or after the position it started at; a capture stated before that position is judged there, when the start arrives. | L304–306 | `session_state_mismatch` | `queuestate.go:checkEntryStatus`<br>`queue.go:reconcileDeferred` | `checkListing`, `reconcileQueueClaims` | `snapshot_listing.rs:check_snapshot_listing`, `snapshot_claims.rs:reconcile_snapshot_claims` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/7` | A run that is not a reservation at the entry's moment MUST NOT carry a `queue_position`. | L272–273, L334–335, L467–469 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queuestate.go:checkActiveRunsListing`<br>`queue.go:judgeLedEntry` | `checkListing`; leads in #1295 (open) | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/8` | A reservation's `queue_position` MUST be its 1-based place among the listing's reservations; leads are judged as one arrangement once their admissions land. | L272–273, L490–504 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queue.go:settleLedGroup` | `checkListing`; leads in #1295 (open) | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/9` | `active_runs` MUST be in admission order; a lead is last in admission order, and leads are compared in the order their responses give. | L272, L345–348, L456–461, L492–495 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queue.go:judgeLedListing` | `checkListing`; leads in #1295 (open) | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/10` | A listing MUST describe at most one run as executing. | L345–352, L440–443 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queue.go:judgeLedListing` | `checkListing`; leads in #1295 (open) | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/11` | A listing MUST NOT describe a run as executing behind a reservation admitted before it. | L346–349 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkListing` | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/12` | Where the listing describes a started run, `active_run_id` MUST name it. | L275, L398–407, L502–504 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queue.go:judgeLedListing` | `checkListing`; leads in #1295 (open) | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/13` | Where the listing holds only reservations, `active_run_id` MUST be absent. | L275–276, L398–400 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queue.go:judgeLedListing` | `checkListing`; leads in #1295 (open) | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/14` | A session status of `waiting_for_input` MUST be backed by the listed started run reporting a wait, by its status or its `pending_interactions`. | L354–365 | `session_state_mismatch` | `queuestate.go:checkListedStatus` | `checkListing` | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/15` | A session status of `running` MUST NOT stand beside a listed started run that reports a wait. | L362–365 | `session_state_mismatch` | `queuestate.go:checkListedStatus` | `checkListing` | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/16` | A snapshot listing a started run MUST report `running` or `waiting_for_input`. | L354–357, L433–434 | `session_state_mismatch` | `queuestate.go:checkListedStatus` | `checkListing` | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/17` | A snapshot whose listing holds only reservations MUST report `queued`; an empty listing keeps whatever status it reports. | L365–372 | `session_state_mismatch` | `queuestate.go:checkListedStatus` | `checkListing` | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/18` | A snapshot MUST carry `active_runs` wherever `active_run_id` cannot carry the answer: a reservation, an overlap, or an unresolved interaction whose id only an entry holds. | L208–210, L276–281, as amended by Decision 0057 (OPEN-0007-2): the interaction arm binds only where the queue is advertised. | `session_state_mismatch` | `queuestate.go:checkActiveRunsOmitted`, its third arm only where the queue is advertised | `checkRequiredRuns`, its third arm only where the queue is advertised | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/19` | `active_runs` MUST list every nonterminal run of the session the capture had to reflect. | L272–273, L374–381 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkRequiredRuns` | `snapshot_listing.rs:check_snapshot_listing` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/20` | A `cancelling` entry the trace has not yet seen start answers with its `queue_position`, and the run's start MUST bear that answer out at the stated position. | L305–326, L471–473 | `session_state_mismatch` | `queue.go:reconcileDeferred`<br>`queue.go:reconcileDeferred` | `reconcileQueueClaims` | `snapshot_claims.rs:check_snapshot_claims`, `reconcile_snapshot_claims`, `close_snapshot_claims` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/21` | An entry describing a not-yet-started reservation as executing MUST be followed by that run's `run.started`. | L337–343, L484–488 | `session_state_mismatch` | `queue.go:closeQueue` | `reconcileQueueClaims` | `snapshot_claims.rs:check_snapshot_claims`, `reconcile_snapshot_claims`, `close_snapshot_claims` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/22` | An entry carrying `pending_interactions` MUST state the position it was captured at. | L288–290 | `session_state_mismatch` | `queuestate.go:checkEntryPending` | `checkEntryPending` | `snapshot_claims.rs:check_snapshot_claims`, `reconcile_snapshot_claims`, `close_snapshot_claims` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/23` | An entry stating no position MUST report the unresolved interactions its run is blocked on, as the trace stands, whatever the descriptor advertises. | L319–329, as amended by Decision 0054 (OPEN-0007-1) | `session_state_mismatch` | `queuestate.go:checkEntryPending` | `checkEntryPending` | `snapshot_claims.rs:check_snapshot_claims`, `reconcile_snapshot_claims`, `close_snapshot_claims` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/24` | An entry's `pending_interactions` MUST equal the interactions its run had open at the position it states, judged when the trace reaches that position. | L274–275, L289, L361–362 | `session_state_mismatch` | `queuestate.go:checkEntryPending`<br>`queue.go:judgePendingInteractions` | `checkEntryPending`, `reconcileCaptureClaims` | `snapshot_claims.rs:check_snapshot_claims`, `reconcile_snapshot_claims`, `close_snapshot_claims` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/25` | An entry MUST NOT state a position its run never reaches. | L525–527 | `session_state_mismatch` | `queue.go:closeQueue` | `checkEntryPending` close | `snapshot_claims.rs:check_snapshot_claims`, `reconcile_snapshot_claims`, `close_snapshot_claims` † |
| `0007#a-snapshot-states-the-position-it-was-captured-at/26` | `as_of.settled` MUST name a run at most once. | L520–523 | `session_state_mismatch` | `queuestate.go:checkSessionCapture` | `checkSettledClaims` | `snapshot_listing.rs:check_snapshot_listing` |
| `0007#a-snapshot-states-the-position-it-was-captured-at/27` | A run MUST NOT be both listed in `active_runs` and named in `as_of.settled` by one snapshot. | L510–519 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkSettledClaims` | `snapshot_listing.rs:check_snapshot_listing` |
| `0007#a-snapshot-states-the-position-it-was-captured-at/28` | Each `as_of.admitted_submit_requests` anchor MUST name a submit request, or an open carrying a message, on the snapshot's session. | L289–291, L409–417 | `session_state_mismatch` | `queuestate.go:checkSessionCapture` | `checkRequiredRuns` | `admission_anchors.rs:record_admission_anchor` |
| `0007#a-snapshot-states-the-position-it-was-captured-at/29` | Each anchor an entry names MUST be a submit request, or an open carrying a message, on that session and, once admitted, admitted to that entry's run. | L419–426 | `session_state_mismatch` | `queuestate.go:checkEntryAnchor` | `checkEntryAnchor` | `admission_anchors.rs:record_admission_anchor` |
| `0007#a-snapshot-states-the-position-it-was-captured-at/30` | A submit request a snapshot claims as admitted MUST have been admitted on that snapshot's session. | L409–413 | `session_state_mismatch` | `queuestate.go:namesSubmitRequest`<br>`queue.go:judgeAdmissionClaim` | `namesSubmitRequest`<br>`judgeAdmissionClaim` | `admission_anchors.rs:anchor_failure` |
| `0007#a-snapshot-states-the-position-it-was-captured-at/31` | A snapshot claiming an admission MUST account for the run it created, in its listing or among the runs it settled. | L414–417 | `session_state_mismatch` | `queue.go:judgeAdmissionClaim` | `judgeAdmissionClaim` | `admission_anchors.rs:anchor_failure` |
| `0007#a-snapshot-states-the-position-it-was-captured-at/32` | A submit request an entry claims as admitting its run MUST have been admitted to that run. | L411–413, L423–425 | `session_state_mismatch` | `queue.go:judgeAdmissionClaim` | `judgeAdmissionClaim` | `admission_anchors.rs:anchor_failure` |
| `0007#a-snapshot-states-the-position-it-was-captured-at/33` | A submit request a snapshot claims as admitted MUST NOT have been refused. | L411 | `session_state_mismatch` | `queue.go:judgeAdmissionClaim` | `judgeAdmissionClaim` | `admission_anchors.rs:anchor_failure` |
| `0007#a-snapshot-states-the-position-it-was-captured-at/34` | A submit request a snapshot claims as admitted MUST receive a response before the trace ends. | L413–414, L506–508 | `session_state_mismatch` | `queue.go:judgeAdmissionClaim` | `judgeAdmissionClaim` | `admission_anchors.rs:anchor_failure` |
| `0007#a-snapshot-states-the-position-it-was-captured-at/35` | A lead entry reported `queued` MUST NOT turn out to be a run its admission started. | L467–469 | `session_state_mismatch` | `queue.go:judgeLedEntry` | `judgeLedEntry` in #1295 (open) | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/36` | A snapshot captured after the session's nonterminal active run started MUST NOT drop or replace it unless `as_of.settled` claims it settled; a snapshot excused by the race does not excuse the ones after it. | L374–393, L620–624 | `session_state_mismatch` | `state.go:applyStateDocument` | `applyStateDocument` in #1294 (open) | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/37` | An `idle` snapshot MUST NOT name an `active_run_id`. | L398–400, L433–434, L440–443 | `session_state_mismatch` | `state.go:applyStateDocument` | `applyStateDocument` in #1294 (open) | — |

### [0007] A reservation's model control applies at promotion

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#a-reservations-model-control-applies-at-promotion).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#a-reservations-model-control-applies-at-promotion/1` | A snapshot without a capture position MUST report the model installed by the session's last model-affecting run, whether or not that run is still going. | L543–549 | `premature_session_mutation` | `queuestate.go:checkCaptureModel` | `checkCaptureModel` | — |
| `0007#a-reservations-model-control-applies-at-promotion/2` | The genesis position MUST NOT be stated once a model-affecting event is already behind the capture window. | L563–567 | `session_state_mismatch` | `queuestate.go:checkCaptureModel` | `checkCaptureModel` | — |
| `0007#a-reservations-model-control-applies-at-promotion/3` | A snapshot at the genesis position MUST report the session's opening model. | L264–268, L568–572 | `premature_session_mutation` | `queuestate.go:checkCaptureModel` | `checkCaptureModel` | — |
| `0007#a-reservations-model-control-applies-at-promotion/4` | A capture position MUST name a run of the snapshot's own session. | L549–551 | `session_state_mismatch` | `queuestate.go:checkCaptureModel` | `checkCaptureModel` | — |
| `0007#a-reservations-model-control-applies-at-promotion/5` | A capture position MUST name the sequence its run started at. | L551–553 | `session_state_mismatch` | `queuestate.go:checkCaptureModel` | `checkCaptureModel` | — |
| `0007#a-reservations-model-control-applies-at-promotion/6` | A capture position MUST name a run that applied a `session_mutation` model. | L553–557 | `session_state_mismatch` | `queuestate.go:checkCaptureModel`<br>`queue.go:judgeCaptureModel` | `checkCaptureModel`, `reconcileModelClaims` | — |
| `0007#a-reservations-model-control-applies-at-promotion/7` | A capture position MUST name the last model-affecting event inside the capture window, not an earlier one. | L557–563 | `session_state_mismatch` | `queuestate.go:checkCaptureModel` | `checkCaptureModel` | — |
| `0007#a-reservations-model-control-applies-at-promotion/8` | A snapshot MUST report the model in force at the position it states. | L543–545 | `premature_session_mutation` | `queuestate.go:checkCaptureModel`<br>`queue.go:judgeCaptureModel` | `checkCaptureModel`, `reconcileModelClaims` | — |
| `0007#a-reservations-model-control-applies-at-promotion/9` | A capture position MUST name a promotion that arrives. | L551–553 | `session_state_mismatch` | `queue.go:closeQueue` | `reconcileModelClaims` close | — |

### [0007] A settled run is named, not merely dropped

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#a-settled-run-is-named-not-merely-dropped).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#a-settled-run-is-named-not-merely-dropped/1` | `as_of.settled` MUST name runs the trace carries for that session. | L579–583 | `session_state_mismatch` | `queuestate.go:recordSettledClaims` | `checkSettledClaims` | `snapshot_claims.rs:check_snapshot_claims`, `reconcile_snapshot_claims`, `close_snapshot_claims` |
| `0007#a-settled-run-is-named-not-merely-dropped/2` | Where the run has settled, the sequence `as_of.settled` states MUST be its terminal's. | L581–583 | `session_state_mismatch` | `queuestate.go:recordSettledClaims` | `checkSettledClaims` | `snapshot_listing.rs:check_snapshot_listing` |
| `0007#a-settled-run-is-named-not-merely-dropped/3` | Where the run has not settled, the next envelope its domain publishes MUST be a terminal at the stated sequence. | L527–530, L581–583 | `session_state_mismatch` | `queue.go:reconcileDeferred` | `checkSettledClaims` | `snapshot_claims.rs:check_snapshot_claims`, `reconcile_snapshot_claims`, `close_snapshot_claims` |
| `0007#a-settled-run-is-named-not-merely-dropped/4` | A claimed terminal MUST arrive before the trace ends. | L527–530, L582–583 | `session_state_mismatch` | `queue.go:closeQueue` | `checkSettledClaims` close | `snapshot_claims.rs:check_snapshot_claims`, `reconcile_snapshot_claims`, `close_snapshot_claims` |

### [0009] The response carries two facts, and already had the shape for both

Source: [`decisions/0009-compound-open.md`](../../decisions/0009-compound-open.md#the-response-carries-two-facts-and-already-had-the-shape-for-both).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0009#the-response-carries-two-facts-and-already-had-the-shape-for-both/1` | A successful compound open's response MUST list exactly one run its message admitted, anchored to the open request. | L87–91 | `session_state_mismatch` | `compoundopen.go:compoundOpenResponse` | — | — |

### [0009] One refusal answers one election

Source: [`decisions/0009-compound-open.md`](../../decisions/0009-compound-open.md#one-refusal-answers-one-election).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0009#one-refusal-answers-one-election/1` | A refusal of an open MUST NOT refuse `subscribe` for a key the endpoint advertises and the request satisfied, when no retained expectation was answered. | L155–159 | `unhonoured_capability` | `compoundopen.go:settleSubscribeRefusal` | `settleControlRefusal` | yes |

### [0009] An endpoint that cannot subscribe at open says so

Source: [`decisions/0009-compound-open.md`](../../decisions/0009-compound-open.md#an-endpoint-that-cannot-subscribe-at-open-says-so).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0009#an-endpoint-that-cannot-subscribe-at-open-says-so/1` | An open electing `subscribe` against an unadvertised, or a degraded and unconsented, `session.open.subscribe` MUST be refused with a typed refusal naming that key. | L192–200 | `unavailable_capability`, `degraded_without_optin` | `compoundopen.go:settleSubscribeRefusal`<br>`compoundopen.go:compoundOpenResponse` | `subscribeGate` | yes |

### [0041] Acceptance is judged when the cancel is checked, and a trace shows that as the request's position

Source: [`decisions/0041-cancel-acceptance-is-judged-when-the-cancel-is-checked.md`](../../decisions/0041-cancel-acceptance-is-judged-when-the-cancel-is-checked.md#acceptance-is-judged-when-the-cancel-is-checked-and-a-trace-shows-that-as-the-requests-position).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0041#acceptance-is-judged-when-the-cancel-is-checked-and-a-trace-shows-that-as-the-requests-position/1` | A `run.cancel.response` MUST NOT accept a cancel whose request arrived after the run's non-`run.cancelled` terminal. | L48–50 | `illegal_run_transition` | `state.go:apply` | yes | yes |

## Rust listing port

The listing slice judges state documents when they arrive, including ordinary
and recovered opens. It records admission, first start and terminal positions
so a run settled before the read differs from one settling inside its window.
Only an observed first terminal fixes that boundary; later invalid events do
not move it. The same change removes 37 now-agreeing Rust known-gap ids.

† These rows have their immediate listing checks. Capture positions whose
terminal has not drained still need the remaining active-state reconciliation.
Entries anchored to pending admissions may lead the trace; their eventual
admission order, classification, queue positions and required-run accounting
remain for the admission-leading slice.

## Rust deferred per-run port

For already admitted runs, `snapshot_claims.rs` reconciles rows 20–25 and the
future-start part of row 6. Pending sets use recorded interaction opening and
resolution positions. Unreached positions and executing claims without a start
fail at trace closure. Future settled claims must be answered by the next event
in that run, with the claimed terminal sequence. Settled rows 1–4 are now ported.
Sixteen now-agreeing Rust known-gap ids are removed by this slice.

Admission-leading entries still need these per-run checks attached after their
admissions arrive; the listing's arrangement and lead classification (row 35), model
captures and active-state reconciliation (rows 36–37) remain separate. The
omitted-list check retains both Decision 0057 advertisement arms. The maintained
per-run journey is `.claude/skills/verify/scripts/rust-deferred-captures.py`.

The maintained public-consumer journey is
`.claude/skills/verify/scripts/rust-snapshot-listing.py`. It compares all ported
listing fixtures with Go and Zig, mutates and repairs listing fields, and pins
both arms of Decision 0057. It requires semantic diagnostics at the affected
pointer rather than accepting a schema failure as proof.

## Rust admission-anchor port

`admission_anchors.rs` checks rows 28–34 when the snapshot arrives and retains
unanswered claims until admissions, refusals or trace closure answer them.
Diagnostics stay attached to the original snapshot. Message-carrying opens bind
the session allocated by their response; submit and accepted steer admissions
must match the snapshot session and entry run. A pending session-level claim
must account for its admitted run in the listing, settled set or scalar active
pointer. Go and Zig now also check the admitted session in the immediate path,
matching their deferred checks and row 30.

Nine Rust known-gap ids now agree. Some admission-leading arrangement fixtures
now draw the expected code through their invalid anchors; that does not port
their separate arrangement checks. Pending-admission classification, ordering,
queue placement, per-run claim attachment and row 35 remain separate, along
with active-state reconciliation, future-terminal listings and model captures.
The maintained journey is `.claude/skills/verify/scripts/rust-admission-anchors.py`.
