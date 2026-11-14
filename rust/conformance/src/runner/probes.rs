use oxap_protocol::{
    CapabilityDescriptor, Envelope, EnvelopeType, ErrorResponse, ProtocolError, RunCancelRequest,
    RunId, SessionId, SessionOpenRequest, SessionStateRequest, SupportLevel, FEATURE_OPEN_REOPEN,
};

use super::{decode_payload, supports, Check, Runner};

impl Runner<'_> {
    pub fn check_state(&mut self) {
        let result = self.request(
            EnvelopeType::SESSION_STATE_REQUEST,
            &SessionStateRequest {
                session_id: self.session.clone(),
            },
        );
        self.record(
            "session.state.request is answered after the run settles",
            result,
        );
    }

    pub fn refuse_stale_revision(&mut self) {
        const NAME: &str = "a stale capability_revision is refused with stale_capabilities";
        if self.revision.is_empty() {
            self.checks.push(Check::skipped(
                NAME,
                "the endpoint issued no revision, so none can be stale",
            ));
            return;
        }
        let result = self
            .envelope(
                EnvelopeType::SESSION_STATE_REQUEST,
                &SessionStateRequest {
                    session_id: self.session.clone(),
                },
            )
            .and_then(|mut envelope| {
                envelope.capability_revision = Some(format!("{}-stale", self.revision));
                let answer = self.probe(envelope)?;
                let failure = refusal(&answer)?;
                if failure.code != "stale_capabilities" {
                    return Err(format!(
                        "refused {:?}, want stale_capabilities",
                        failure.code
                    ));
                }
                Ok(answer)
            });
        self.record(NAME, result);
    }

    pub fn answer_cancel(&mut self, descriptor: &CapabilityDescriptor, run: Option<&RunId>) {
        const NAME: &str = "run.cancel.request is supported, or refused as unavailable";
        let Some(run) = run.filter(|run| !run.as_str().is_empty()) else {
            self.checks.push(Check::skipped(
                NAME,
                "no run was admitted, so there is nothing to cancel",
            ));
            return;
        };
        let payload = RunCancelRequest {
            session_id: self.session.clone(),
            run_id: run.clone(),
            ..RunCancelRequest::default()
        };
        let result = self
            .envelope(EnvelopeType::RUN_CANCEL_REQUEST, &payload)
            .and_then(|mut envelope| {
                envelope.run_id = Some(run.clone());
                let answer = self.probe(envelope)?;
                if supports(descriptor, "run.cancel") {
                    if answer.envelope_type != EnvelopeType::RUN_CANCEL_RESPONSE
                        && answer.envelope_type != EnvelopeType::ERROR_RESPONSE
                    {
                        return Err(format!(
                            "an endpoint declaring run.cancel answered {}",
                            answer.envelope_type
                        ));
                    }
                } else if refusal(&answer)?.code != "unsupported_feature" {
                    return Err("cancellation was not refused with unsupported_feature".into());
                }
                Ok(answer)
            });
        self.record(NAME, result);
    }

    pub fn refuse_addressable_envelope(&mut self) {
        const NAME: &str = "an addressable envelope that is wrong draws a correlated refusal";
        let payload = SessionStateRequest {
            session_id: self.session.clone(),
        };
        let result = self
            .envelope(EnvelopeType::from("conformance.not.a.real.request"), &payload)
            .and_then(|envelope| {
                let answer = self.probe(envelope)?;
                refusal(&answer)?;
                self.request(EnvelopeType::SESSION_STATE_REQUEST, &payload)
                    .map_err(|error| {
                        format!(
                            "the endpoint stopped answering after a recoverable protocol error: {error}"
                        )
                    })?;
                Ok(answer)
            });
        self.record(NAME, result);
    }

    pub fn refuse_unknown_reopen(&mut self, descriptor: &CapabilityDescriptor) {
        const NAME: &str = "a reopen of a session the endpoint never had is refused, not answered with a fresh session";
        let never = SessionId::from(format!("{}-never-opened", self.session));
        let mut payload = SessionOpenRequest {
            session_id: Some(never.clone()),
            reopen: true,
            ..SessionOpenRequest::default()
        };
        if descriptor
            .features
            .get(FEATURE_OPEN_REOPEN)
            .is_some_and(|support| support.level == SupportLevel::DEGRADED)
        {
            payload
                .allow_degraded_features
                .push(FEATURE_OPEN_REOPEN.into());
        }
        let result = self
            .envelope(EnvelopeType::SESSION_OPEN_REQUEST, &payload)
            .and_then(|mut envelope| {
                envelope.session_id = Some(never);
                let answer = self.probe(envelope)?;
                let failure = refusal(&answer)?;
                if supports(descriptor, FEATURE_OPEN_REOPEN) {
                    if failure.code != "unknown_session" {
                        return Err(format!(
                            "an endpoint advertising {FEATURE_OPEN_REOPEN} refused {:?}, want unknown_session",
                            failure.code
                        ));
                    }
                } else if failure.code != "unsupported_feature"
                    || failure.details.get("feature").and_then(|value| value.as_str())
                        != Some(FEATURE_OPEN_REOPEN)
                {
                    return Err(format!(
                        "an endpoint not advertising {FEATURE_OPEN_REOPEN} must refuse with unsupported_feature naming the key"
                    ));
                }
                Ok(answer)
            });
        self.record(NAME, result);
    }

    fn probe(&mut self, envelope: Envelope) -> Result<Envelope, String> {
        let id = envelope.id.clone();
        self.client
            .probe(envelope)
            .map_err(|error| error.to_string())?;
        self.client.response(&id).map_err(|error| error.to_string())
    }
}

fn refusal(answer: &Envelope) -> Result<ProtocolError, String> {
    if answer.envelope_type != EnvelopeType::ERROR_RESPONSE {
        return Err(format!(
            "a request requiring refusal was answered {}",
            answer.envelope_type
        ));
    }
    Ok(decode_payload::<ErrorResponse>(answer)?.error)
}
