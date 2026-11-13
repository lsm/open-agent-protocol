use std::collections::BTreeMap;

use oap_adapter::runtime::Runtime;
use oap_adapter::{Descriptor, Error, JournalDescriptor};
use oap_protocol::{
    CapabilityDescriptor, EndpointDescriptor, FeatureSupport, SupportLevel,
    FEATURE_COMPACTION_POLICY, FEATURE_OPEN_REOPEN, FEATURE_SESSION_REASONING, MODE_SESSION_LIVE,
    MODE_SESSION_OPEN, PROFILE, VERSION,
};

use crate::process::ProcessConfig;

const ENDPOINT_ID: &str = "hermes.gateway";
const DEFAULT_JOURNAL_CAPACITY: usize = 256;
pub(crate) const REOPEN_SUPPORT_REASON: &str = "session.resume reloads the bound stored session; one Hermes would auto-continue after a crash is refused and its process ended, taking the scheduled turn with it";

#[derive(Clone, Default)]
pub struct Config {
    pub process: ProcessConfig,
    pub model: String,
    pub journal_capacity: usize,
    pub runtime: Runtime,
}

#[derive(Clone)]
pub struct Adapter {
    config: Config,
}

pub fn pin() -> &'static oap_harness::Version {
    oap_harness::current("hermes").expect("embedded catalog pins hermes")
}

impl Adapter {
    pub fn new(mut config: Config) -> Result<Self, Error> {
        if config.process.path.is_empty() {
            return Err(Error::Harness(
                "hermes adapter: factory or executable is required".into(),
            ));
        }
        if config.journal_capacity == 0 {
            config.journal_capacity = DEFAULT_JOURNAL_CAPACITY;
        }
        Ok(Self { config })
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
