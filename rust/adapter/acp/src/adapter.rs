use std::collections::{BTreeMap, HashSet};
use std::path::Path;

use oap_adapter::{
    refuse_unadvertised_settings, Adapter as AdapterContract, Admitter, BoxFuture, Descriptor,
    Error, JournalDescriptor, OpenRequest, Session,
};
use oap_protocol::{
    CapabilityDescriptor, EndpointDescriptor, FeatureSupport, SupportLevel, ToolSourceDescriptor,
    FEATURE_COMPACTION_POLICY, FEATURE_MODELS_LIST, FEATURE_OPEN_REOPEN,
    FEATURE_SESSION_MODEL_SWITCH, FEATURE_SESSION_REASONING, FEATURE_TOOL_SOURCES_ATTACH,
    MODE_SESSION_OPEN, PROFILE, TOOL_SOURCE_MCP, TOOL_SOURCE_PROCESS, VERSION,
};

use crate::native::McpServer;
use crate::process::ProcessConfig;

#[derive(Clone, Debug, Default)]
pub struct Config {
    pub process: ProcessConfig,
    pub runtime: Option<crate::Runtime>,
    pub mcp_servers: Vec<McpServer>,
    pub session_config: BTreeMap<String, String>,
    pub journal_capacity: usize,
    pub abort_active_on_close: bool,
}

#[cfg(test)]
thread_local! {
    pub(crate) static DESCRIPTORS: std::cell::Cell<usize> = const { std::cell::Cell::new(0) };
}

pub struct Adapter {
    config: Config,
    descriptor: Descriptor,
    runtime: crate::Runtime,
}

pub fn pin() -> &'static oap_harness::Version {
    oap_harness::current("acp").expect("embedded ACP catalog has a current version")
}

impl Adapter {
    pub fn new(mut config: Config) -> Result<Self, Error> {
        if config.journal_capacity == 0 {
            config.journal_capacity = 256;
        }
        if config.process.path.is_empty() {
            return Err(Error::Harness("acp adapter: executable is required".into()));
        }
        if config.process.dir.is_empty() || !Path::new(&config.process.dir).is_absolute() {
            return Err(Error::Harness(
                "acp adapter: absolute working directory is required".into(),
            ));
        }
        let mut seen = HashSet::new();
        for server in &config.mcp_servers {
            if server.name.is_empty() {
                return Err(Error::Harness(
                    "acp adapter: a configured MCP server needs a name".into(),
                ));
            }
            if !seen.insert(&server.name) {
                return Err(Error::Harness(format!(
                    "acp adapter: MCP server {} is configured twice",
                    oap_adapter::gojson::go_quote(&server.name)
                )));
            }
        }
        Ok(Self {
            descriptor: Self::build(&config),
            runtime: config.runtime.clone().unwrap_or_default(),
            config,
        })
    }

    pub fn describe(&self) -> Descriptor {
        self.descriptor.clone()
    }

    fn build(config: &Config) -> Descriptor {
        #[cfg(test)]
        DESCRIPTORS.with(|built| built.set(built.get() + 1));
        let mut features: BTreeMap<String, FeatureSupport> = [
            "session.open",
            FEATURE_OPEN_REOPEN,
            "session.state",
            FEATURE_MODELS_LIST,
            FEATURE_SESSION_MODEL_SWITCH,
            FEATURE_SESSION_REASONING,
            FEATURE_COMPACTION_POLICY,
            "session.message.submit",
            "session.message.delivery.auto",
            "run.streaming",
            "run.status",
            "run.cancel",
            "run.resume",
            "run.reconciliation",
            "run.replay",
            "action.tools",
            "action.tools.execute",
            FEATURE_TOOL_SOURCES_ATTACH,
            "action.permissions",
        ]
        .into_iter()
        .map(|key| {
            (
                key.into(),
                FeatureSupport {
                    level: SupportLevel::UNAVAILABLE,
                    reason: "the Rust ACP session implementation is not yet ported".into(),
                    ..FeatureSupport::default()
                },
            )
        })
        .collect();
        for key in ["protocol.initialize", "capabilities"] {
            features.insert(
                key.into(),
                FeatureSupport {
                    level: SupportLevel::EMULATED,
                    reason:
                        "the adapter boundary describes ACP initialization and effective support"
                            .into(),
                    ..FeatureSupport::default()
                },
            );
        }
        for (key, level, reason) in [
            ("session.open", SupportLevel::NATIVE, "session/new opens an ACP session"),
            (FEATURE_OPEN_REOPEN, SupportLevel::NATIVE, "session/load when loadSession is advertised, otherwise session/resume when advertised; agents offering neither refuse the binding"),
            ("session.state", SupportLevel::EMULATED, "adapter-owned projection"),
            (FEATURE_MODELS_LIST, SupportLevel::DEGRADED, "the agent's model configOption snapshot; its category is a UI hint and supplies no provider, context window or pricing"),
            (FEATURE_SESSION_MODEL_SWITCH, SupportLevel::DEGRADED, "session/set_config_option confirms a listed model value; which model then runs remains the agent's word"),
            ("session.message.submit", SupportLevel::DEGRADED, "ACP usage semantics remain lossy and unverified"),
            ("run.streaming", SupportLevel::DEGRADED, "ordered text and tool events from the staged run projection"),
            ("session.message.delivery.auto", SupportLevel::EMULATED, "start when idle and refuse an active run"),
            ("run.status", SupportLevel::EMULATED, "adapter-owned text-run projection"),
            ("run.cancel", SupportLevel::NATIVE, "session/cancel intent with native prompt settlement"),
            ("action.permissions", SupportLevel::NATIVE, "ACP permission choices with synthesized portable identity"),
            ("action.tools", SupportLevel::DEGRADED, "observed ACP presentation tool calls only; no catalog"),
            ("action.tools.execute", SupportLevel::DEGRADED, "observed tool lifecycle is normalized"),
            ("run.resume", SupportLevel::DEGRADED, "canonical replay is bounded process memory only"),
            ("run.reconciliation", SupportLevel::EMULATED, "state is adapter-owned"),
            ("run.replay", SupportLevel::DEGRADED, "bounded process-memory journal; gaps are explicit"),
        ] {
            features.insert(key.into(), FeatureSupport { level, reason: reason.into(), ..FeatureSupport::default() });
        }
        features.insert(FEATURE_SESSION_REASONING.into(), FeatureSupport {
            level:SupportLevel::EMULATED,modes:vec![MODE_SESSION_OPEN.into()],
            reason:"session/set_config_option on the agent's thought_level option, matched by value or name and confirmed in the returned option list; an agent that offers none refuses every level".into(),
            ..FeatureSupport::default()
        });
        features.insert(
            FEATURE_TOOL_SOURCES_ATTACH.into(),
            crate::attachments::support(),
        );
        Descriptor {
            capabilities: CapabilityDescriptor {
                endpoint: EndpointDescriptor {
                    id: "acp.v1".into(),
                    name: "ACP v1 Adapter".into(),
                    version: pin().endpoint_version.clone(),
                    adapter: "acp-v1-stdio".into(),
                },
                protocol_versions: vec![VERSION.into()],
                profiles: vec![PROFILE.into()],
                features,
                sources: config
                    .mcp_servers
                    .iter()
                    .map(|server| ToolSourceDescriptor {
                        id: server.name.clone(),
                        kind: TOOL_SOURCE_PROCESS.into(),
                        protocol: TOOL_SOURCE_MCP.into(),
                        ..ToolSourceDescriptor::default()
                    })
                    .collect(),
                ..CapabilityDescriptor::default()
            },
            capability_revision: format!("{}-rust-attachments", pin().capability_revision),
            max_active_runs_per_session: 1,
            interactive_gates: true,
            journal: JournalDescriptor {
                replay: SupportLevel::DEGRADED,
                scope: "session".into(),
                persistence: "process_memory".into(),
                capacity: config.journal_capacity,
            },
            ..Descriptor::default()
        }
    }

    fn validate_open(&self, request: &OpenRequest) -> Result<Vec<McpServer>, Error> {
        if request.participant.id.as_str().is_empty() {
            return Err(Error::InvalidParticipant);
        }
        if request.reopen && request.native_session_id.is_empty() {
            return Err(crate::reload::refusal("the binding names no ACP session"));
        }
        let attached = crate::attachments::attached(&self.config, request)?;
        refuse_unadvertised_settings(request, &self.describe().capabilities)?;
        Ok(attached)
    }
}

impl AdapterContract for Adapter {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        Box::pin(async { Ok(self.describe()) })
    }

    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        Box::pin(async move {
            let attached = self.validate_open(&request)?;
            let mut servers = self.config.mcp_servers.clone();
            servers.extend(attached);
            crate::session::Session::open(
                &self.config,
                request,
                self.describe(),
                &self.runtime,
                servers,
            )
            .await
        })
    }

    fn admitter(&self) -> Option<&dyn Admitter> {
        Some(self)
    }
}

impl Admitter for Adapter {
    fn admit(&self, request: &OpenRequest) -> BoxFuture<'_, ()> {
        let result = self.validate_open(request).map(|_| ());
        Box::pin(async move { result })
    }
}

#[cfg(test)]
#[path = "../tests/support/adapter.rs"]
mod tests;
