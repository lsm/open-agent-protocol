use std::collections::BTreeMap;
use std::sync::Arc;

use oap_adapter::runtime::Runtime;
use oap_adapter::{
    refuse_unadvertised_settings, refuse_unadvertised_tool_sources, refuse_unadvertised_tools,
    Descriptor, Error, JournalDescriptor, OpenRequest, UnsupportedControl, CONTROL_UNSATISFIABLE,
};
use oap_protocol::{
    CapabilityDescriptor, EndpointDescriptor, FeatureSupport, ReasoningLevel, SessionState,
    SessionStatus, SupportLevel, FEATURE_COMPACTION_POLICY, FEATURE_OPEN_REOPEN,
    FEATURE_SESSION_REASONING, MODE_SESSION_LIVE, MODE_SESSION_OPEN, PROFILE, VERSION,
};

use crate::client::{call, native_protocol, Client, ClientFactory, ProcessFactory};
use crate::native::{ConfigSetParams, ConfigSetResult, METHOD_CONFIG_SET};
use crate::process::ProcessConfig;
use crate::session::Session;

pub(crate) const ENDPOINT_ID: &str = "hermes.gateway";
const DEFAULT_JOURNAL_CAPACITY: usize = 256;
pub(crate) const REOPEN_SUPPORT_REASON: &str = "session.resume reloads the bound stored session; one Hermes would auto-continue after a crash is refused and its process ended, taking the scheduled turn with it";

#[derive(Clone, Default)]
pub struct Config {
    pub factory: Option<Arc<dyn ClientFactory>>,
    pub process: ProcessConfig,
    pub model: String,
    pub journal_capacity: usize,
    pub runtime: Runtime,
}

#[derive(Clone)]
pub struct Adapter {
    config: Config,
    factory: Arc<dyn ClientFactory>,
}

pub fn pin() -> &'static oap_harness::Version {
    oap_harness::current("hermes").expect("embedded catalog pins hermes")
}

impl Adapter {
    pub fn new(mut config: Config) -> Result<Self, Error> {
        if config.factory.is_none() && config.process.path.is_empty() {
            return Err(Error::Harness(
                "hermes adapter: factory or executable is required".into(),
            ));
        }
        if config.journal_capacity == 0 {
            config.journal_capacity = DEFAULT_JOURNAL_CAPACITY;
        }
        let factory = config.factory.clone().unwrap_or_else(|| {
            Arc::new(ProcessFactory {
                config: config.process.clone(),
                model: config.model.clone(),
            })
        });
        Ok(Self { config, factory })
    }

    pub fn open(&self, request: OpenRequest) -> Result<Session, Error> {
        refuse_unadvertised_tool_sources(&request, &[])?;
        refuse_unadvertised_tools(&request, &[])?;
        refuse_unadvertised_settings(&request, &self.describe().capabilities)?;
        let level = &request.reasoning_level;
        let (client, native_id, resumed) = if request.reopen {
            if request.native_session_id.trim().is_empty() {
                return Err(reopen_refusal("the binding names no stored session"));
            }
            let (client, resumed) = self
                .factory
                .reopen(&request.native_session_id, None)
                .map_err(|detail| reopen_refusal(&detail))?;
            let native_id = resumed.session_id.clone();
            (client, native_id, Some(resumed))
        } else if level.is_empty() {
            let (client, native_id) = self.factory.start(None)?;
            (client, native_id, None)
        } else {
            let (client, native_id) = self.factory.start_with_reasoning(effort(level), None)?;
            (client, native_id, None)
        };
        let fail = |failure: Error| {
            let _ = client.close();
            failure
        };
        if native_id.is_empty() {
            return Err(fail(native_protocol()));
        }
        if !level.is_empty() {
            set_reasoning(&*client, &native_id, level).map_err(fail)?;
        }
        let session_id = if request.session_id.as_str().is_empty() {
            self.config.runtime.new_id("session").into()
        } else {
            request.session_id
        };
        let state = SessionState {
            session_id,
            status: SessionStatus::IDLE,
            current_model_id: self.config.model.clone(),
            updated_at_ms: self.config.runtime.now_ms(),
            reasoning_level: request.reasoning_level,
            ..SessionState::default()
        };
        let participant = match request.participant.id.as_str() {
            "" => "user".into(),
            _ => request.participant.id.clone(),
        };
        let session = Session::new(
            client,
            native_id,
            participant,
            self.config.runtime.clone(),
            self.config.journal_capacity,
            state,
        );
        if let Some(resumed) = resumed {
            session.restore_state(resumed.info.as_deref());
        }
        Ok(session)
    }

    pub fn describe(&self) -> Descriptor {
        Descriptor {
            capabilities: CapabilityDescriptor {
                endpoint: EndpointDescriptor {
                    id: ENDPOINT_ID.into(),
                    name: "Hermes Gateway Adapter".into(),
                    version: pin().endpoint_version.clone(),
                    adapter: "hermes-tui-gateway".into(),
                },
                protocol_versions: vec![VERSION.into()],
                profiles: vec![PROFILE.into()],
                features: features(),
                ..CapabilityDescriptor::default()
            },
            capability_revision: pin().capability_revision.clone(),
            journal: JournalDescriptor {
                scope: "session".into(),
                persistence: "process_memory".into(),
                replay: SupportLevel::DEGRADED,
                capacity: self.config.journal_capacity,
            },
            max_active_runs_per_session: 1,
            interactive_gates: true,
            cancellation_target: "session".into(),
            cancellation_implementation: "session.interrupt".into(),
        }
    }
}

pub(crate) fn features() -> BTreeMap<String, FeatureSupport> {
    let feature = |level, reason: &str| FeatureSupport {
        level,
        reason: reason.into(),
        ..FeatureSupport::default()
    };
    [
        ("protocol.initialize", feature(SupportLevel::NATIVE, "gateway.ready frame with replay epoch before any input")),
        ("capabilities", feature(SupportLevel::EMULATED, "conservative descriptor for the pinned gateway")),
        ("session.open", feature(SupportLevel::NATIVE, "session.create mints the runtime session id")),
        (FEATURE_OPEN_REOPEN, feature(SupportLevel::NATIVE, REOPEN_SUPPORT_REASON)),
        ("session.state", feature(SupportLevel::DEGRADED, "reducer-owned live projection corroborated by session.info")),
        ("session.message.submit", feature(SupportLevel::DEGRADED, "status-only result; ownership by construction via message.start")),
        ("session.message.delivery.auto", feature(SupportLevel::DEGRADED, "accepted only for known idle sessions")),
        ("session.message.delivery.queue", feature(SupportLevel::UNAVAILABLE, "busy statuses are rejected rather than guessed")),
        ("session.message.delivery.steer", feature(SupportLevel::UNAVAILABLE, "native session.steer not exposed in v1")),
        ("run.streaming", feature(SupportLevel::NATIVE, "immediate frames on stdio; post-scrubber provenance disclosed")),
        ("run.status", feature(SupportLevel::EMULATED, "")),
        ("run.cancel", feature(SupportLevel::DEGRADED, "session.interrupt intent; settlement via message.complete interrupted")),
        ("run.resume", feature(SupportLevel::DEGRADED, "the native recovery family is not exercised; OAP resume replays the adapter journal")),
        ("run.reconciliation", feature(SupportLevel::DEGRADED, "session.info running/turn_started_at corroboration")),
        ("run.replay", feature(SupportLevel::DEGRADED, "bounded adapter journal; gaps are explicit and the native events.since ring is not consulted")),
        ("action.tools", feature(SupportLevel::DEGRADED, "tool.start/complete only; started synthesized; failures ride in result without a pinned discriminator")),
        ("action.tools.execute", feature(SupportLevel::UNAVAILABLE, "the gateway executes tools internally")),
        ("action.permissions", feature(SupportLevel::DEGRADED, "approval gates surface as input interactions")),
        ("user_input", feature(SupportLevel::NATIVE, "approval/clarify/sudo/secret server requests, withdrawn by request.cancel")),
        (FEATURE_SESSION_REASONING, FeatureSupport {
            modes: vec![MODE_SESSION_OPEN.into(), MODE_SESSION_LIVE.into()],
            ..feature(SupportLevel::NATIVE, "session.create's reasoning_effort, then config.set reasoning scoped to the session, which validates it and sets it again between runs; off is Hermes's none")
        }),
        (FEATURE_COMPACTION_POLICY, feature(SupportLevel::UNAVAILABLE, "compression is config.yaml under HERMES_HOME, which also holds the gateway's credentials, and no gateway method sets it")),
    ]
    .into_iter()
    .map(|(key, support)| (key.to_owned(), support))
    .collect()
}

pub fn effort(level: &ReasoningLevel) -> &str {
    if *level == ReasoningLevel::OFF {
        "none"
    } else {
        level.as_str()
    }
}

pub(crate) fn set_reasoning(
    client: &dyn Client,
    native_id: &str,
    level: &ReasoningLevel,
) -> Result<(), Error> {
    let params = ConfigSetParams {
        session_id: native_id.into(),
        key: "reasoning".into(),
        value: effort(level).into(),
    };
    call::<ConfigSetResult>(client, METHOD_CONFIG_SET, &params, None)
        .map(drop)
        .map_err(|failure| {
            Error::UnsupportedControl(Box::new(UnsupportedControl {
                feature: FEATURE_SESSION_REASONING.into(),
                reason: CONTROL_UNSATISFIABLE,
                field: "reasoning_level".into(),
                detail: format!("Hermes refused the reasoning level: {failure}"),
                ..UnsupportedControl::default()
            }))
        })
}

fn reopen_refusal(detail: &str) -> Error {
    Error::UnsupportedControl(Box::new(UnsupportedControl {
        feature: FEATURE_OPEN_REOPEN.into(),
        reason: CONTROL_UNSATISFIABLE,
        detail: format!("Hermes could not reload its binding: {detail}"),
        ..UnsupportedControl::default()
    }))
}
