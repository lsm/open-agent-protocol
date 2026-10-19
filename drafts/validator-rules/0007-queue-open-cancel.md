# Queue, compound open and cancel

Decisions 0007, 0009 and 0041: the rows behind every diagnostic the validators
raise for queue admission and refusal, session-state snapshots, compound opens
and cancel acceptance. Format: [README](README.md).

### [0007] One started run, and reservations behind it

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#one-started-run-and-reservations-behind-it).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#one-started-run-and-reservations-behind-it/1` | A second admission on a session that holds a nonterminal run MUST be a reservation: `admission` `queued`, `effective_delivery` `queue`, `status` `queued`. | L40–44 | `illegal_run_transition` | `queue.go:queueOverlap` | yes | — |
| `0007#one-started-run-and-reservations-behind-it/2` | A reservation MUST NOT be admitted unless the active descriptor advertises `session.message.delivery.queue`. | L42–43 | `unavailable_capability` | `queue.go:queueAdmission` | yes | — |
| `0007#one-started-run-and-reservations-behind-it/3` | A reservation admitted under a `degraded` queue disclosure MUST have been requested with that key in `allow_degraded_features`. | L42–43, L85–87 | `degraded_without_optin` | `queue.go:queueAdmission` | yes | — |
| `0007#one-started-run-and-reservations-behind-it/4` | An explicit `queue` delivery MUST be admitted as a reservation, whatever the session holds, and never resolves to `start`. | L46–49 | `illegal_run_transition` | `queue.go:queueAdmission` | yes | — |
| `0007#one-started-run-and-reservations-behind-it/5` | An `auto` submission a busy session turns into a reservation MUST report `delivery_resolution: "session_busy"`. | L49–53 | `illegal_run_transition` | `queue.go:queueAdmission` | yes | — |

### [0007] One run executes at a time, in admission order

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#one-run-executes-at-a-time-in-admission-order).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#one-run-executes-at-a-time-in-admission-order/1` | A later-admitted run MUST NOT publish a sequenced event while an earlier-admitted run of its session is nonterminal, except the pre-start terminal of a run that never started and run-scoped requests and responses. | L57–74 | `queue_order_violation` | `queue.go:checkQueueOrder` | yes | — |

### [0007] A queue is a promise, and the bound is what makes it checkable

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable/1` | A descriptor advertising the queue above `unavailable` MUST disclose a positive `max_queued_runs_per_session`. | L82–88 | `undisclosed_queue_limit` | `queue.go:checkQueueLimits` | yes | — |
| `0007#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable/2` | Where the queue is available or `degraded`, a stated `max_active_runs_per_session` MUST be at least `max_queued_runs_per_session + 1`. | L90–98 | `undisclosed_queue_limit` | `queue.go:checkQueueLimits` | yes | — |
| `0007#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable/3` | An admission MUST NOT put the session's nonterminal set above the disclosed active bound; a set admitted before a lowering refresh is grandfathered. | L78–80, L100–103 | `queue_limit_exceeded` | `queue.go:queueAdmission` | yes | — |
| `0007#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable/4` | An admission MUST NOT put the session's queued subset above the disclosed queued bound; a set admitted before a lowering refresh is grandfathered. | L78–80, L100–103 | `queue_limit_exceeded` | `queue.go:queueAdmission` | yes | — |
| `0007#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable/5` | A capabilities response repeating the active revision MUST repeat the queue bounds already disclosed under it. | L105–120 | `stale_capability_revision` | `queue.go:checkQueueAdvertisement` | yes | — |
| `0007#a-queue-is-a-promise-and-the-bound-is-what-makes-it-checkable/6` | A capabilities response repeating the active revision MUST repeat the queue's advertised level. | L105–120 | `stale_capability_revision` | `queue.go:checkQueueAdvertisement` | yes | — |

### [0007] The refusal is validated as well as the admission

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#the-refusal-is-validated-as-well-as-the-admission).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#the-refusal-is-validated-as-well-as-the-admission/1` | A refusal owed because the session is busy MUST carry the wire code `run_active`. | L129–133 | `illegal_run_transition` | `queue.go:settleQueueRefusal` | yes | — |
| `0007#the-refusal-is-validated-as-well-as-the-admission/2` | A refusal owed because a queue bound was reached MUST carry the wire code `run_active`. | L129–133 | `queue_limit_exceeded` | `queue.go:settleQueueRefusal` | yes | — |
| `0007#the-refusal-is-validated-as-well-as-the-admission/3` | A `run_active` refusal MUST NOT report a bound that was unreached throughout its request/response window, counting every other unanswered submit on the session. | L135–173 | `queue_limit_exceeded` | `queue.go:settleQueueRefusal` | yes | — |

### [0007] A snapshot states the position it was captured at

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#a-snapshot-states-the-position-it-was-captured-at).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#a-snapshot-states-the-position-it-was-captured-at/1` | A state document MUST list each run in `active_runs` at most once. | L187–188 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkListing` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/2` | An `active_runs` entry MUST name a run the trace carries for that session, unless it anchors only submit requests still awaiting their response. | L190–192, L412–419 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkListing` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/3` | An entry MUST NOT list a run that had settled before the read was requested. | L291–294 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkListing` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/4` | An entry MUST NOT state a position at or past the sequence its run settled at. | L294–298 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkListing` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/5` | An entry MUST NOT carry a terminal status; a settled run is dropped and named in `as_of.settled`. | L299–301, L431–432 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queuestate.go:checkEntryStatus` | `checkListing` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/6` | An entry MUST NOT call a run `queued` at or after the position it started at; a capture stated before that position is judged there, when the start arrives. | L301–303 | `session_state_mismatch` | `queuestate.go:checkEntryStatus`<br>`queue.go:reconcileDeferred` | `checkListing`, `reconcileQueueClaims` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/7` | A run that is not a reservation at the entry's moment MUST NOT carry a `queue_position`. | L270–271, L327–328, L460–462 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queuestate.go:checkActiveRunsListing`<br>`queue.go:judgeLedEntry` | `checkListing`; leads in #1295 (open) | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/8` | A reservation's `queue_position` MUST be its 1-based place among the listing's reservations; leads are judged as one arrangement once their admissions land. | L270–271, L483–497 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queue.go:settleLedGroup` | `checkListing`; leads in #1295 (open) | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/9` | `active_runs` MUST be in admission order; a lead is last in admission order, and leads are compared in the order their responses give. | L270, L338–341, L449–454, L485–488 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queue.go:judgeLedListing` | `checkListing`; leads in #1295 (open) | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/10` | A listing MUST describe at most one run as executing. | L338–345, L433–436 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queue.go:judgeLedListing` | `checkListing`; leads in #1295 (open) | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/11` | A listing MUST NOT describe a run as executing behind a reservation admitted before it. | L339–342 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkListing` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/12` | Where the listing describes a started run, `active_run_id` MUST name it. | L273, L391–400, L495–497 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queue.go:judgeLedListing` | `checkListing`; leads in #1295 (open) | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/13` | Where the listing holds only reservations, `active_run_id` MUST be absent. | L273–274, L391–393 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing`<br>`queue.go:judgeLedListing` | `checkListing`; leads in #1295 (open) | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/14` | A session status of `waiting_for_input` MUST be backed by the listed started run reporting a wait, by its status or its `pending_interactions`. | L347–358 | `session_state_mismatch` | `queuestate.go:checkListedStatus` | `checkListing` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/15` | A session status of `running` MUST NOT stand beside a listed started run that reports a wait. | L355–358 | `session_state_mismatch` | `queuestate.go:checkListedStatus` | `checkListing` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/16` | A snapshot listing a started run MUST report `running` or `waiting_for_input`. | L347–350, L426–427 | `session_state_mismatch` | `queuestate.go:checkListedStatus` | `checkListing` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/17` | A snapshot whose listing holds only reservations MUST report `queued`; an empty listing keeps whatever status it reports. | L358–365 | `session_state_mismatch` | `queuestate.go:checkListedStatus` | `checkListing` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/18` | A snapshot MUST carry `active_runs` wherever `active_run_id` cannot carry the answer: a reservation, an overlap, or an unresolved interaction whose id only an entry holds. | L206–208, L274–278; Go and Zig read "an endpoint that puts entries there at all" as one advertising the queue; see OPEN-0007-2. | `session_state_mismatch` | `queuestate.go:checkActiveRunsOmitted`, its third arm only where the queue is advertised | `checkRequiredRuns`, its third arm only where the queue is advertised | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/19` | `active_runs` MUST list every nonterminal run of the session the capture had to reflect. | L270–271, L367–374 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkRequiredRuns` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/20` | A `cancelling` entry the trace has not yet seen start answers with its `queue_position`, and the run's start MUST bear that answer out at the stated position. | L302–319, L464–466 | `session_state_mismatch` | `queue.go:reconcileDeferred`<br>`queue.go:reconcileDeferred` | `reconcileQueueClaims` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/21` | An entry describing a not-yet-started reservation as executing MUST be followed by that run's `run.started`. | L330–336, L477–481 | `session_state_mismatch` | `queue.go:closeQueue` | `reconcileQueueClaims` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/22` | An entry carrying `pending_interactions` MUST state the position it was captured at. | L285–287 | `session_state_mismatch` | `queuestate.go:checkEntryPending` | `checkEntryPending` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/23` | An entry stating no position MUST report the unresolved interactions its run is blocked on, as the trace stands, whatever the descriptor advertises. | L316–322, as amended by Decision 0054 (#1309, open) | `session_state_mismatch` | `queuestate.go:checkEntryPending`, only where the queue is advertised until OPEN-0007-1 is ported | `checkEntryPending`, only where the queue is advertised until OPEN-0007-1 is ported | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/24` | An entry's `pending_interactions` MUST equal the interactions its run had open at the position it states, judged when the trace reaches that position. | L272–273, L286, L354–355 | `session_state_mismatch` | `queuestate.go:checkEntryPending`<br>`queue.go:judgePendingInteractions` | `checkEntryPending`, `reconcileCaptureClaims` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/25` | An entry MUST NOT state a position its run never reaches. | L518–520 | `session_state_mismatch` | `queue.go:closeQueue` | `checkEntryPending` close | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/26` | `as_of.settled` MUST name a run at most once. | L513–516 | `session_state_mismatch` | `queuestate.go:checkSessionCapture` | `checkSettledClaims` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/27` | A run MUST NOT be both listed in `active_runs` and named in `as_of.settled` by one snapshot. | L503–512 | `session_state_mismatch` | `queuestate.go:checkActiveRunsListing` | `checkSettledClaims` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/28` | Each `as_of.admitted_submit_requests` anchor MUST name a submit request, or an open carrying a message, on the snapshot's session. | L286–288, L402–410 | `session_state_mismatch` | `queuestate.go:checkSessionCapture` | `checkRequiredRuns` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/29` | Each anchor an entry names MUST be a submit request, or an open carrying a message, on that session and, once admitted, admitted to that entry's run. | L412–419 | `session_state_mismatch` | `queuestate.go:checkEntryAnchor` | `checkEntryAnchor` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/30` | A submit request a snapshot claims as admitted MUST have been admitted on that snapshot's session. | L402–406 | `session_state_mismatch` | `queue.go:judgeAdmissionClaim` | `judgeAdmissionClaim` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/31` | A snapshot claiming an admission MUST account for the run it created, in its listing or among the runs it settled. | L407–410 | `session_state_mismatch` | `queue.go:judgeAdmissionClaim` | `judgeAdmissionClaim` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/32` | A submit request an entry claims as admitting its run MUST have been admitted to that run. | L404–406, L416–418 | `session_state_mismatch` | `queue.go:judgeAdmissionClaim` | `judgeAdmissionClaim` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/33` | A submit request a snapshot claims as admitted MUST NOT have been refused. | L404 | `session_state_mismatch` | `queue.go:judgeAdmissionClaim` | `judgeAdmissionClaim` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/34` | A submit request a snapshot claims as admitted MUST receive a response before the trace ends. | L406–407, L499–501 | `session_state_mismatch` | `queue.go:judgeAdmissionClaim` | `judgeAdmissionClaim` | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/35` | A lead entry reported `queued` MUST NOT turn out to be a run its admission started. | L460–462 | `session_state_mismatch` | `queue.go:judgeLedEntry` | `judgeLedEntry` in #1295 (open) | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/36` | A snapshot captured after the session's nonterminal active run started MUST NOT drop or replace it unless `as_of.settled` claims it settled; a snapshot excused by the race does not excuse the ones after it. | L367–386, L613–617 | `session_state_mismatch` | `state.go:applyStateDocument` | `applyStateDocument` in #1294 (open) | — |
| `0007#a-snapshot-states-the-position-it-was-captured-at/37` | An `idle` snapshot MUST NOT name an `active_run_id`. | L391–393, L426–427, L433–436 | `session_state_mismatch` | `state.go:applyStateDocument` | `applyStateDocument` in #1294 (open) | — |

### [0007] A reservation's model control applies at promotion

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#a-reservations-model-control-applies-at-promotion).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#a-reservations-model-control-applies-at-promotion/1` | A snapshot without a capture position MUST report the model installed by the session's last model-affecting run, whether or not that run is still going. | L536–542 | `premature_session_mutation` | `queuestate.go:checkCaptureModel` | `checkCaptureModel` | — |
| `0007#a-reservations-model-control-applies-at-promotion/2` | The genesis position MUST NOT be stated once a model-affecting event is already behind the capture window. | L556–560 | `session_state_mismatch` | `queuestate.go:checkCaptureModel` | `checkCaptureModel` | — |
| `0007#a-reservations-model-control-applies-at-promotion/3` | A snapshot at the genesis position MUST report the session's opening model. | L262–266, L561–565 | `premature_session_mutation` | `queuestate.go:checkCaptureModel` | `checkCaptureModel` | — |
| `0007#a-reservations-model-control-applies-at-promotion/4` | A capture position MUST name a run of the snapshot's own session. | L542–544 | `session_state_mismatch` | `queuestate.go:checkCaptureModel` | `checkCaptureModel` | — |
| `0007#a-reservations-model-control-applies-at-promotion/5` | A capture position MUST name the sequence its run started at. | L544–546 | `session_state_mismatch` | `queuestate.go:checkCaptureModel` | `checkCaptureModel` | — |
| `0007#a-reservations-model-control-applies-at-promotion/6` | A capture position MUST name a run that applied a `session_mutation` model. | L546–550 | `session_state_mismatch` | `queuestate.go:checkCaptureModel`<br>`queue.go:judgeCaptureModel` | `checkCaptureModel`, `reconcileModelClaims` | — |
| `0007#a-reservations-model-control-applies-at-promotion/7` | A capture position MUST name the last model-affecting event inside the capture window, not an earlier one. | L550–556 | `session_state_mismatch` | `queuestate.go:checkCaptureModel` | `checkCaptureModel` | — |
| `0007#a-reservations-model-control-applies-at-promotion/8` | A snapshot MUST report the model in force at the position it states. | L536–538 | `premature_session_mutation` | `queuestate.go:checkCaptureModel`<br>`queue.go:judgeCaptureModel` | `checkCaptureModel`, `reconcileModelClaims` | — |
| `0007#a-reservations-model-control-applies-at-promotion/9` | A capture position MUST name a promotion that arrives. | L544–546 | `session_state_mismatch` | `queue.go:closeQueue` | `reconcileModelClaims` close | — |

### [0007] A settled run is named, not merely dropped

Source: [`decisions/0007-queue-delivery.md`](../../decisions/0007-queue-delivery.md#a-settled-run-is-named-not-merely-dropped).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0007#a-settled-run-is-named-not-merely-dropped/1` | `as_of.settled` MUST name runs the trace carries for that session. | L572–576 | `session_state_mismatch` | `queuestate.go:recordSettledClaims` | `checkSettledClaims` | — |
| `0007#a-settled-run-is-named-not-merely-dropped/2` | Where the run has settled, the sequence `as_of.settled` states MUST be its terminal's. | L574–576 | `session_state_mismatch` | `queuestate.go:recordSettledClaims` | `checkSettledClaims` | — |
| `0007#a-settled-run-is-named-not-merely-dropped/3` | Where the run has not settled, the next envelope its domain publishes MUST be a terminal at the stated sequence. | L520–523, L574–576 | `session_state_mismatch` | `queue.go:reconcileDeferred` | `checkSettledClaims` | — |
| `0007#a-settled-run-is-named-not-merely-dropped/4` | A claimed terminal MUST arrive before the trace ends. | L520–523, L575–576 | `session_state_mismatch` | `queue.go:closeQueue` | `checkSettledClaims` close | — |

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
