use std::collections::BTreeMap;
use std::sync::Arc;

use oap_adapter::runtime::Runtime;
use oap_adapter::{
    Descriptor, Error, JournalDescriptor, OpenRequest, UnsupportedControl, CONTROL_UNSATISFIABLE,
};
use oap_protocol::{
    CapabilityDescriptor, CompactionKind, EndpointDescriptor, FeatureSupport, ReasoningLevel,
    SupportLevel, FEATURE_COMPACTION_POLICY, FEATURE_INSTRUCTIONS, FEATURE_MODEL_SELECTION,
    FEATURE_OPEN_REOPEN, FEATURE_SESSION_REASONING, FEATURE_STRUCTURED_OUTPUT,
    FEATURE_TOOL_SELECTION, MODE_SESSION_LIVE, MODE_SESSION_OPEN, PROFILE, SCOPE_RUN, VERSION,
};
use serde_json::Value;

use crate::client::ClientFactory;
use crate::process::ProcessConfig;

pub(crate) const ENDPOINT_ID: &str = "codex.app-server";
const DEFAULT_JOURNAL_CAPACITY: usize = 256;

#[derive(Clone, Default)]
pub struct Config {
    pub factory: Option<Arc<dyn ClientFactory>>,
    pub process: ProcessConfig,
    pub model: String,
    pub approval_policy: String,
    pub sandbox: String,
    pub resume_thread_id: String,
    pub journal_capacity: usize,
    pub runtime: Runtime,
}

#[derive(Clone)]
pub struct Adapter {
    config: Config,
}

pub fn pin() -> &'static oap_harness::Version {
    oap_harness::current("codex-app-server").expect("embedded catalog pins codex-app-server")
}

impl Adapter {
    pub fn new(mut config: Config) -> Result<Self, Error> {
        if config.factory.is_none() && config.process.path.is_empty() {
            return Err(Error::Harness(
                "codex app-server adapter: factory or executable is required".into(),
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
                    name: "Codex app-server Adapter".into(),
                    version: pin().endpoint_version.clone(),
                    adapter: "codex-appserver-stdio".into(),
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
            cancellation_target: "run".into(),
            cancellation_implementation: "native_turn_interrupt".into(),
        }
    }
}

fn features() -> BTreeMap<String, FeatureSupport> {
    let feature = |level, reason: &str| FeatureSupport {
        level,
        reason: reason.into(),
        ..FeatureSupport::default()
    };
    let native = || feature(SupportLevel::NATIVE, "");
    let normalized = "only pinned command, file-change, and MCP item families are normalized";
    [
        ("protocol.initialize", native()),
        ("capabilities", native()),
        ("session.open", native()),
        ("session.state", native()),
        ("session.message.submit", native()),
        ("session.message.delivery.auto", native()),
        (FEATURE_MODEL_SELECTION, FeatureSupport {
            scope: SCOPE_RUN.into(),
            ..feature(SupportLevel::NATIVE, "turn/start carries the model for one turn")
        }),
        (FEATURE_INSTRUCTIONS, feature(SupportLevel::UNAVAILABLE, "this pin exposes no per-turn instruction override")),
        (FEATURE_TOOL_SELECTION, feature(SupportLevel::UNAVAILABLE, "this pin exposes no per-turn tool policy")),
        (FEATURE_STRUCTURED_OUTPUT, feature(SupportLevel::NATIVE, "turn/start.outputSchema constrains the turn's final assistant message")),
        ("run.streaming", native()),
        ("run.status", native()),
        ("run.cancel", feature(SupportLevel::NATIVE, "turn/interrupt targets an exact native thread and turn; settlement is asynchronous")),
        ("run.resume", feature(SupportLevel::DEGRADED, "thread/resume restores native attachment; canonical replay is bounded process memory")),
        ("run.reconciliation", feature(SupportLevel::EMULATED, "state is the adapter's canonical projection of native observations")),
        ("run.replay", feature(SupportLevel::DEGRADED, "only adapter-emitted events in bounded process memory are replayable")),
        ("action.tools", feature(SupportLevel::DEGRADED, normalized)),
        ("action.tools.execute", feature(SupportLevel::DEGRADED, normalized)),
        ("action.permissions", feature(SupportLevel::NATIVE, "command and file-change reverse approvals are correlated and round-trip once")),
        ("user_input", feature(SupportLevel::DEGRADED, "Codex option questions normalize to OAP single-choice input")),
        (FEATURE_OPEN_REOPEN, feature(SupportLevel::NATIVE, "thread/resume reloads the thread the session's binding names and reports the model it resumed under")),
        (FEATURE_SESSION_REASONING, FeatureSupport {
            modes: vec![MODE_SESSION_OPEN.into(), MODE_SESSION_LIVE.into()],
            ..feature(SupportLevel::NATIVE, "thread/start's config sets model_reasoning_effort, and a live change rides the next turn/start's effort, which Codex keeps for the turns after it; off is Codex's none")
        }),
        (FEATURE_COMPACTION_POLICY, FeatureSupport {
            modes: vec![MODE_SESSION_OPEN.into()],
            ..feature(SupportLevel::NATIVE, "thread/start's config sets model_auto_compact_token_limit for tokens; Codex has no off, and its share applies only at a turn's end, so both are refused")
        }),
    ]
    .into_iter()
    .map(|(key, support)| (key.to_owned(), support))
    .collect()
}

pub fn thread_config(request: &OpenRequest) -> Result<BTreeMap<String, Value>, Error> {
    let mut config = BTreeMap::new();
    if !request.reasoning_level.is_empty() {
        config.insert(
            "model_reasoning_effort".into(),
            Value::from(effort(&request.reasoning_level)),
        );
    }
    if let Some(policy) = &request.compaction_policy {
        if policy.kind == CompactionKind::TOKENS {
            config.insert(
                "model_auto_compact_token_limit".into(),
                Value::from(policy.tokens),
            );
        } else if policy.kind != CompactionKind::AUTO {
            return Err(Error::UnsupportedControl(Box::new(UnsupportedControl {
                feature: FEATURE_COMPACTION_POLICY.into(),
                reason: CONTROL_UNSATISFIABLE,
                field: "compaction_policy".into(),
                detail: "Codex takes a token limit only; it has no off, and its share applies only at a turn's end".into(),
                ..UnsupportedControl::default()
            })));
        }
    }
    Ok(config)
}

pub fn effort(level: &ReasoningLevel) -> &str {
    if *level == ReasoningLevel::OFF {
        "none"
    } else {
        level.as_str()
    }
}

pub fn reasoning_level(effort: &str) -> ReasoningLevel {
    if effort == "none" {
        return ReasoningLevel::OFF;
    }
    let level = ReasoningLevel::from(effort);
    if level.is_known() && level != ReasoningLevel::OFF {
        level
    } else {
        ReasoningLevel::default()
    }
}
