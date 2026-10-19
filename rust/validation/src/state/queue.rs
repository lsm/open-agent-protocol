use oap_protocol::*;

use super::controls::{affirmative, Expectation};
use super::{os, payload, State};
use crate::codes;

const RUN_ACTIVE: &str = "run_active";
const SESSION_BUSY: &str = "session_busy";

#[derive(Clone, Default)]
pub(super) struct Window {
    delivery: RequestedDeliveryMode,
    max_active: Option<i64>,
    max_queued: Option<i64>,
    reached_strict: bool,
    reached_loose: bool,
    busy_at_request: bool,
    busy_ever: bool,
    started_ever: bool,
    mutation: bool,
    offered: bool,
}

#[derive(PartialEq)]
enum Condition {
    None,
    BusySession,
    QueueLimit,
}

impl Window {
    fn condition(&self) -> Condition {
        let delivery = &self.delivery;
        if !delivery.is_empty()
            && *delivery != RequestedDeliveryMode::AUTO
            && *delivery != RequestedDeliveryMode::QUEUE
        {
            return Condition::None;
        }
        let queue = *delivery == RequestedDeliveryMode::QUEUE;
        if self.offered && (queue || self.busy_ever) {
            Condition::QueueLimit
        } else if !self.offered && !queue && self.busy_ever {
            Condition::BusySession
        } else {
            Condition::None
        }
    }

    fn exceeds(&self, active: usize, queued: usize, outstanding: usize) -> bool {
        let over = |bound: Option<i64>, count: usize| {
            bound.is_some_and(|max| (count + outstanding + 1) as i64 > max)
        };
        over(self.max_active, active) || over(self.max_queued, queued)
    }

    fn describe_bounds(&self) -> String {
        let mut parts = Vec::new();
        if let Some(max) = self.max_active {
            parts.push(format!("max_active_runs_per_session={max}"));
        }
        if let Some(max) = self.max_queued {
            parts.push(format!("max_queued_runs_per_session={max}"));
        }
        if parts.is_empty() {
            return "(no bound disclosed)".to_owned();
        }
        format!("({})", parts.join(" "))
    }
}

#[derive(Default)]
pub(super) struct Snapshot {
    revision: String,
    stale: bool,
    queue: Option<SupportLevel>,
    limits: Option<CapabilityLimits>,
}

fn describe_level(level: Option<&SupportLevel>) -> String {
    level.map_or_else(
        || "unadvertised".to_owned(),
        |level| level.as_str().to_owned(),
    )
}

fn describe_bound(bound: Option<i64>) -> String {
    bound.map_or_else(|| "absent".to_owned(), |bound| bound.to_string())
}

impl State {
    pub(super) fn advertised_level(&self, key: &str) -> Option<SupportLevel> {
        if self.current_capability.is_empty() || self.capabilities_stale {
            return None;
        }
        Some(
            self.features
                .get(key)
                .cloned()
                .unwrap_or(SupportLevel::UNAVAILABLE),
        )
    }

    pub(super) fn queue_offered(&self) -> bool {
        self.advertised_level(FEATURE_DELIVERY_QUEUE)
            .is_some_and(|level| affirmative(&level))
    }

    pub(super) fn queue_counts(&self, session: &str) -> (usize, usize, usize) {
        let (mut active, mut queued, mut started) = (0, 0, 0);
        for id in self.order.get(session).into_iter().flatten() {
            let Some(run) = self.runs.get(id).filter(|run| !run.terminal) else {
                continue;
            };
            active += 1;
            if run.admitted_queued && !run.started {
                queued += 1;
            } else {
                started += 1;
            }
        }
        (active, queued, started)
    }

    pub(super) fn refresh_queue_windows(&mut self, session: &str) {
        let Some(open) = self.open_submits.get(session).cloned() else {
            return;
        };
        if open.is_empty() {
            return;
        }
        let (active, queued, started) = self.queue_counts(session);
        let unanswered =
            |state: &Self, request: &str| !state.requests.get(request).is_some_and(|r| r.responded);
        let outstanding = open
            .iter()
            .filter(|request| {
                self.pending_controls
                    .get(*request)
                    .is_some_and(|p| p.queue.is_some())
                    && unanswered(self, request)
            })
            .count();
        for request in &open {
            let others = if unanswered(self, request) {
                outstanding.saturating_sub(1)
            } else {
                outstanding
            };
            let Some(window) = self
                .pending_controls
                .get_mut(request)
                .and_then(|p| p.queue.as_mut())
            else {
                continue;
            };
            window.busy_ever |= active > 0;
            window.started_ever |= started > 0;
            window.reached_strict |= window.exceeds(active, queued, 0);
            window.reached_loose |= window.exceeds(active, queued, others);
        }
    }

    pub(super) fn close_submit_window(&mut self, e: &Envelope) {
        let request = os(&e.in_reply_to);
        let Some(session) = self
            .pending_controls
            .get(&request)
            .filter(|pending| pending.queue.is_some())
            .map(|pending| pending.session.clone())
        else {
            return;
        };
        if let Some(open) = self.open_submits.get_mut(&session) {
            open.retain(|candidate| *candidate != request);
        }
        self.refresh_queue_windows(&session);
    }

    pub(super) fn open_queue_window(
        &self,
        session: &str,
        delivery: &RequestedDeliveryMode,
        mutation: bool,
    ) -> Window {
        let (active, queued, started) = self.queue_counts(session);
        let limits = self.limits.unwrap_or_default();
        let mut window = Window {
            delivery: delivery.clone(),
            max_active: limits.max_active_runs_per_session,
            max_queued: limits.max_queued_runs_per_session,
            busy_at_request: active > 0,
            busy_ever: active > 0,
            started_ever: started > 0,
            mutation,
            offered: self.queue_offered(),
            ..Window::default()
        };
        window.reached_strict = window.exceeds(active, queued, 0);
        let open = self.open_submits.get(session).map_or(0, Vec::len);
        window.reached_loose = window.exceeds(active, queued, open);
        window
    }

    pub(super) fn delivery_expectations(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        p: &MessageSubmitRequest,
    ) -> (Window, Vec<Expectation>) {
        let session_scoped = self
            .feature_supports
            .get(FEATURE_MODEL_SELECTION)
            .is_some_and(|support| support.scope == SCOPE_SESSION);
        let window = self.open_queue_window(
            p.session_id.as_str(),
            &p.delivery,
            p.model_id.is_some() && session_scoped,
        );
        let mut expectations = Vec::new();
        if p.delivery.is_empty() || p.delivery == RequestedDeliveryMode::AUTO {
            return (window, expectations);
        }
        let key = format!("session.message.delivery.{}", p.delivery.as_str());
        let Some(level) = self.control_descriptor(at, e, &key) else {
            return (window, expectations);
        };
        let gated = p.delivery == RequestedDeliveryMode::QUEUE
            || p.delivery == RequestedDeliveryMode::STEER;
        if gated && !affirmative(&level) {
            expectations.push(Expectation::delivery(&key, false));
        } else if level == SupportLevel::DEGRADED && !p.allows_degraded(&key) {
            expectations.push(Expectation::delivery(&key, true));
        }
        (window, expectations)
    }

    pub(super) fn settle_queue_refusal(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        window: &Window,
        err: &ProtocolError,
        described: &str,
    ) {
        let reply = os(&e.in_reply_to);
        match window.condition() {
            Condition::BusySession if err.code != RUN_ACTIVE => {
                self.add_expected(
                    codes::ILLEGAL_RUN_TRANSITION,
                    at,
                    e,
                    "/payload/error",
                    "a busy session's refusal must be the wire's run_active",
                    RUN_ACTIVE,
                    described,
                    &[&reply],
                );
            }
            Condition::QueueLimit if window.reached_strict && err.code != RUN_ACTIVE => {
                self.add_expected(
                    codes::QUEUE_LIMIT_EXCEEDED,
                    at,
                    e,
                    "/payload/error",
                    "a refusal at a reached queue bound must be the wire's run_active",
                    RUN_ACTIVE,
                    described,
                    &[&reply],
                );
            }
            Condition::QueueLimit
                if !window.reached_strict
                    && err.code == RUN_ACTIVE
                    && !window.reached_loose
                    && !(window.mutation && window.started_ever) =>
            {
                let actual = format!("{described} {}", window.describe_bounds());
                self.add_expected(
                    codes::QUEUE_LIMIT_EXCEEDED,
                    at,
                    e,
                    "/payload/error",
                    "refusal reports a queue bound that was never reached",
                    "admission",
                    &actual,
                    &[&reply],
                );
            }
            _ => {}
        }
    }

    pub(super) fn queue_overlap(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        p: &MessageSubmitResponse,
    ) -> bool {
        let (active, _, _) = self.queue_counts(p.session_id.as_str());
        if active == 0 || (p.admission == Admission::QUEUED && self.queue_offered()) {
            return false;
        }
        let expected =
            format!("a queued reservation on an endpoint advertising {FEATURE_DELIVERY_QUEUE}");
        self.add_expected(
            codes::ILLEGAL_RUN_TRANSITION,
            at,
            e,
            "/payload/run_id",
            "session already has a nonterminal run",
            &expected,
            p.admission.as_str(),
            &[],
        );
        true
    }

    pub(super) fn queue_admission(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        p: &MessageSubmitResponse,
    ) {
        let reply = os(&e.in_reply_to);
        let pending = self.pending_controls.get(&reply);
        let window = pending.and_then(|pending| pending.queue.clone());
        let gated = pending
            .and_then(|pending| pending.expectation.as_ref())
            .is_some_and(|expectation| expectation.key == FEATURE_DELIVERY_QUEUE);
        let requested = window
            .as_ref()
            .map(|window| window.delivery.clone())
            .filter(|delivery| !delivery.is_empty())
            .unwrap_or_else(|| p.requested_delivery.clone());
        let queued = p.admission == Admission::QUEUED;
        let busy = window.as_ref().is_some_and(|window| window.busy_at_request);
        if requested == RequestedDeliveryMode::QUEUE && !queued {
            self.add_expected(
                codes::ILLEGAL_RUN_TRANSITION,
                at,
                e,
                "/payload/admission",
                "an explicit queue request may only be admitted as a reservation",
                Admission::QUEUED.as_str(),
                p.admission.as_str(),
                &[&reply],
            );
        } else if requested == RequestedDeliveryMode::AUTO
            && queued
            && busy
            && p.delivery_resolution != SESSION_BUSY
        {
            self.add_expected(codes::ILLEGAL_RUN_TRANSITION, at, e, "/payload/delivery_resolution", "an auto submission a busy session turned into a reservation must report the resolution that produced it", SESSION_BUSY, &p.delivery_resolution, &[&reply]);
        }
        if !queued {
            return;
        }
        if !gated {
            match self.advertised_level(FEATURE_DELIVERY_QUEUE) {
                Some(level) if !affirmative(&level) => {
                    let message = format!("a queued reservation requires the endpoint to advertise {FEATURE_DELIVERY_QUEUE}");
                    self.add_expected(
                        codes::UNAVAILABLE_CAPABILITY,
                        at,
                        e,
                        "/payload/admission",
                        &message,
                        "native, emulated, or degraded",
                        level.as_str(),
                        &[&reply],
                    );
                }
                Some(level)
                    if level == SupportLevel::DEGRADED && !self.opted_into_queue(&reply) =>
                {
                    let expected = format!("{FEATURE_DELIVERY_QUEUE} in allow_degraded_features");
                    self.add_expected(
                        codes::DEGRADED_WITHOUT_OPTIN,
                        at,
                        e,
                        "/payload/admission",
                        "a degraded queue was used without the caller's opt-in",
                        &expected,
                        "absent",
                        &[&reply],
                    );
                }
                _ => {}
            }
        }
        let (active, queued_count, _) = self.queue_counts(p.session_id.as_str());
        let Some(limits) = self.limits else {
            return;
        };
        if let Some(max) = limits
            .max_active_runs_per_session
            .filter(|max| (active + 1) as i64 > *max)
        {
            self.add_expected(
                codes::QUEUE_LIMIT_EXCEEDED,
                at,
                e,
                "/payload/admission",
                "reservation puts the session's nonterminal set above the disclosed bound",
                &format!("at most {max}"),
                &(active + 1).to_string(),
                &[&reply],
            );
            return;
        }
        if let Some(max) = limits
            .max_queued_runs_per_session
            .filter(|max| (queued_count + 1) as i64 > *max)
        {
            self.add_expected(
                codes::QUEUE_LIMIT_EXCEEDED,
                at,
                e,
                "/payload/admission",
                "reservation puts the session's queued subset above the disclosed bound",
                &format!("at most {max}"),
                &(queued_count + 1).to_string(),
                &[&reply],
            );
        }
    }

    fn opted_into_queue(&self, request: &str) -> bool {
        self.requests.get(request).is_some_and(|request| {
            payload::<MessageSubmitRequest>(&request.envelope)
                .allows_degraded(FEATURE_DELIVERY_QUEUE)
        })
    }

    pub(super) fn check_queue_order(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        run_id: &str,
        started: bool,
    ) {
        let Some(run) = self.runs.get(run_id) else {
            return;
        };
        if !run.admitted || e.sequence.is_none() || (is_terminal_type(&e.envelope_type) && !started)
        {
            return;
        }
        let blocking = self
            .order
            .get(&run.session)
            .into_iter()
            .flatten()
            .find(|id| {
                self.runs.get(*id).is_some_and(|earlier| {
                    *id != run_id && earlier.order < run.order && !earlier.terminal
                })
            });
        if let Some(earlier) = blocking.cloned() {
            self.add_expected(codes::QUEUE_ORDER_VIOLATION, at, e, "/sequence", "a later-admitted run published an event while an earlier-admitted run was nonterminal", "every earlier admission terminal", &earlier, &[&earlier]);
        }
    }

    pub(super) fn snapshot_descriptor(&mut self) {
        self.outgoing = Snapshot {
            revision: self.current_capability.clone(),
            stale: self.capabilities_stale,
            queue: self.features.get(FEATURE_DELIVERY_QUEUE).cloned(),
            limits: self.limits,
        };
    }

    pub(super) fn record_limits(&mut self, at: (usize, usize), e: &Envelope) {
        let p: CapabilitiesResponse = payload(e);
        self.limits = p.limits;
        self.check_queue_limits(at, e, &p);
        let outgoing = std::mem::take(&mut self.outgoing);
        let revision = e.capability_revision.clone().unwrap_or_default();
        if outgoing.revision.is_empty() || outgoing.stale || outgoing.revision != revision {
            return;
        }
        let current = self.features.get(FEATURE_DELIVERY_QUEUE).cloned();
        if current != outgoing.queue {
            let pointer = format!("/payload/features/{FEATURE_DELIVERY_QUEUE}");
            let message = format!("{FEATURE_DELIVERY_QUEUE} changed under one capability revision without a capabilities.updated");
            self.add_expected(
                codes::STALE_CAPABILITY_REVISION,
                at,
                e,
                &pointer,
                &message,
                &describe_level(outgoing.queue.as_ref()),
                &describe_level(current.as_ref()),
                &[],
            );
        }
        let before = outgoing.limits.unwrap_or_default();
        let after = self.limits.unwrap_or_default();
        for (name, was, is) in [
            (
                "max_active_runs_per_session",
                before.max_active_runs_per_session,
                after.max_active_runs_per_session,
            ),
            (
                "max_queued_runs_per_session",
                before.max_queued_runs_per_session,
                after.max_queued_runs_per_session,
            ),
        ] {
            if was != is {
                let message = format!(
                    "{name} changed under one capability revision without a capabilities.updated"
                );
                self.add_expected(
                    codes::STALE_CAPABILITY_REVISION,
                    at,
                    e,
                    &format!("/payload/limits/{name}"),
                    &message,
                    &describe_bound(was),
                    &describe_bound(is),
                    &[],
                );
            }
        }
    }

    fn check_queue_limits(&mut self, at: (usize, usize), e: &Envelope, p: &CapabilitiesResponse) {
        if !p
            .effective_support(FEATURE_DELIVERY_QUEUE)
            .is_some_and(|support| affirmative(&support.level))
        {
            return;
        }
        let limits = p.limits;
        let queued = limits
            .as_ref()
            .and_then(|l| l.max_queued_runs_per_session)
            .unwrap_or(0);
        if queued < 1 {
            let actual = match &limits {
                None => "absent".to_owned(),
                Some(limits) => describe_bound(limits.max_queued_runs_per_session),
            };
            let message = format!("{FEATURE_DELIVERY_QUEUE} is advertised without a queue bound a submission could ever reach");
            self.add_expected(
                codes::UNDISCLOSED_QUEUE_LIMIT,
                at,
                e,
                "/payload/limits/max_queued_runs_per_session",
                &message,
                "at least 1",
                &actual,
                &[],
            );
            return;
        }
        if let Some(active) = limits
            .and_then(|l| l.max_active_runs_per_session)
            .filter(|active| *active < queued + 1)
        {
            self.add_expected(codes::UNDISCLOSED_QUEUE_LIMIT, at, e, "/payload/limits/max_active_runs_per_session", "the disclosed active bound leaves no room for the disclosed queue beside a started run", &format!("at least {}", queued + 1), &active.to_string(), &[]);
        }
    }

    pub(super) fn enter_order(&mut self, run_id: &str) {
        let Some(session) = self.runs.get(run_id).map(|run| run.session.clone()) else {
            return;
        };
        let order = self.order.entry(session.clone()).or_default();
        let position = order.len();
        order.push(run_id.to_owned());
        if let Some(run) = self.runs.get_mut(run_id) {
            run.order = position;
        }
        self.refresh_queue_windows(&session);
    }

    pub(super) fn open_submit_window(&mut self, session: &str, request: &str) {
        self.open_submits
            .entry(session.to_owned())
            .or_default()
            .push(request.to_owned());
        self.refresh_queue_windows(session);
    }
}

fn is_terminal_type(t: &EnvelopeType) -> bool {
    *t == EnvelopeType::RUN_COMPLETED
        || *t == EnvelopeType::RUN_FAILED
        || *t == EnvelopeType::RUN_CANCELLED
}
