use std::collections::HashMap;

use oap_protocol::*;

use super::controls::{affirmative, describe_refusal, detail};
use super::{os, payload, s, State};
use crate::codes;

const UNSUPPORTED_FEATURE: &str = "unsupported_feature";
const CAPABILITY_DEGRADED: &str = "capability_degraded";
const CONTROL_UNADVERTISED: &str = "unadvertised";

pub(super) type Attached = HashMap<String, ProviderAttachment>;

impl State {
    pub(super) fn provider_attach_modes(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        p: &CapabilitiesResponse,
    ) {
        let Some(support) = p.effective_support(FEATURE_PROVIDERS_ATTACH) else {
            return;
        };
        if !affirmative(&support.level) || support.discloses_mode(MODE_SESSION_LIVE) {
            return;
        }
        let pointer = "/payload/features/action.providers.attach/modes";
        let message = "action.providers.attach is advertised without the session_live mode";
        let modes = if support.modes.is_empty() {
            "none".to_owned()
        } else {
            support.modes.join(",")
        };
        self.add_expected(
            codes::UNDISCLOSED_ATTACH_MODES,
            at,
            e,
            pointer,
            message,
            MODE_SESSION_LIVE,
            &modes,
            &[],
        );
    }

    pub(super) fn provider_attach_request(&mut self, at: (usize, usize), e: &Envelope) {
        let judged = self
            .control_descriptor(at, e, FEATURE_PROVIDERS_ATTACH)
            .is_some();
        self.judged_attaches.insert(s(&e.id), judged);
    }

    fn live_attach(&self) -> bool {
        let level = self.features.get(FEATURE_PROVIDERS_ATTACH);
        let support = self.feature_supports.get(FEATURE_PROVIDERS_ATTACH);
        level.is_some_and(affirmative)
            && support.is_some_and(|support| support.discloses_mode(MODE_SESSION_LIVE))
    }

    pub(super) fn provider_attach_response(&mut self, at: (usize, usize), e: &Envelope) {
        let p: SessionProviderAttachResponse = payload(e);
        let reply = os(&e.in_reply_to);
        let Some(request) = self.requests.get(&reply) else {
            return;
        };
        let asked: SessionProviderAttachRequest = payload(&request.envelope);
        if p.session_id != asked.session_id {
            let message = "provider attach response changed the requested session";
            let (expected, actual) = (asked.session_id.as_str(), p.session_id.as_str());
            self.add_expected(
                codes::SCOPE_MISMATCH,
                at,
                e,
                "/payload/session_id",
                message,
                expected,
                actual,
                &[reply.as_str()],
            );
            return;
        }
        if p.provider_id != asked.provider.id {
            let message =
                "attach response must name the session-local provider alias the caller requested";
            let (expected, actual) = (asked.provider.id.as_str(), p.provider_id.as_str());
            self.add_expected(
                codes::SCOPE_MISMATCH,
                at,
                e,
                "/payload/provider_id",
                message,
                expected,
                actual,
                &[reply.as_str()],
            );
            return;
        }
        let judged = self.judged_attaches.get(&reply).copied().unwrap_or(false);
        if judged && !self.live_attach() {
            let message = "provider attachment accepted without advertised session_live support";
            let expected = format!("{FEATURE_PROVIDERS_ATTACH} {MODE_SESSION_LIVE}");
            self.add_expected(
                codes::UNAVAILABLE_CAPABILITY,
                at,
                e,
                "/type",
                message,
                &expected,
                e.envelope_type.as_str(),
                &[reply.as_str()],
            );
        }
        let degraded = self.features.get(FEATURE_PROVIDERS_ATTACH) == Some(&SupportLevel::DEGRADED);
        if judged && degraded && !asked.allows_degraded(FEATURE_PROVIDERS_ATTACH) {
            let message = "degraded provider attachment accepted without opt-in";
            let expected = format!("{FEATURE_PROVIDERS_ATTACH} in allow_degraded_features");
            self.add_expected(
                codes::DEGRADED_WITHOUT_OPTIN,
                at,
                e,
                "/type",
                message,
                &expected,
                e.envelope_type.as_str(),
                &[reply.as_str()],
            );
        }
        let session = s(&p.session_id);
        let alias = p.provider_id.clone();
        let attached = self.attached_providers.entry(session.clone()).or_default();
        if let Some(old) = attached.get(&alias) {
            if *old != asked.provider {
                let message = "attach reused a provider alias for a different OAP service/provider";
                self.add_expected(
                    codes::DUPLICATE_PROVIDER,
                    at,
                    e,
                    "/payload/provider_id",
                    message,
                    "the original binding",
                    "a conflicting binding",
                    &[alias.as_str()],
                );
            }
            return;
        }
        if self.catalog_serves_provider(&session, &alias) {
            let message = "attach reused an alias already served by the session catalog";
            self.add_expected(
                codes::DUPLICATE_PROVIDER,
                at,
                e,
                "/payload/provider_id",
                message,
                "a fresh provider alias",
                &alias,
                &[],
            );
            return;
        }
        if let Some(attached) = self.attached_providers.get_mut(&session) {
            attached.insert(alias, asked.provider);
        }
        self.model_catalogs.remove(&session);
    }

    pub(super) fn provider_attach_refusal(&mut self, at: (usize, usize), e: &Envelope) {
        let reply = os(&e.in_reply_to);
        let Some(request) = self.requests.get(&reply) else {
            return;
        };
        if request.envelope_type != EnvelopeType::SESSION_PROVIDER_ATTACH_REQUEST
            || !self.judged_attaches.get(&reply).copied().unwrap_or(false)
        {
            return;
        }
        let asked: SessionProviderAttachRequest = payload(&request.envelope);
        let err = payload::<ErrorResponse>(e).error;
        let feature = detail(&err, "feature").unwrap_or_default();
        let reason = detail(&err, "reason").unwrap_or_default();
        let level = self.features.get(FEATURE_PROVIDERS_ATTACH).cloned();
        if !level.as_ref().is_some_and(affirmative) {
            if err.code != UNSUPPORTED_FEATURE
                || feature != FEATURE_PROVIDERS_ATTACH
                || reason != CONTROL_UNADVERTISED
            {
                let message = "unadvertised provider attachment needs a typed feature refusal";
                let expected = format!("{UNSUPPORTED_FEATURE} {FEATURE_PROVIDERS_ATTACH} reason={CONTROL_UNADVERTISED}");
                self.add_expected(
                    codes::UNAVAILABLE_CAPABILITY,
                    at,
                    e,
                    "/payload/error",
                    message,
                    &expected,
                    &describe_refusal(&err),
                    &[reply.as_str()],
                );
            }
        } else if level == Some(SupportLevel::DEGRADED)
            && !asked.allows_degraded(FEATURE_PROVIDERS_ATTACH)
            && (err.code != CAPABILITY_DEGRADED || feature != FEATURE_PROVIDERS_ATTACH)
        {
            let message =
                "degraded provider attachment without opt-in needs a capability_degraded refusal";
            let expected = format!("{CAPABILITY_DEGRADED} {FEATURE_PROVIDERS_ATTACH}");
            self.add_expected(
                codes::DEGRADED_WITHOUT_OPTIN,
                at,
                e,
                "/payload/error",
                message,
                &expected,
                &describe_refusal(&err),
                &[reply.as_str()],
            );
        }
    }
}
