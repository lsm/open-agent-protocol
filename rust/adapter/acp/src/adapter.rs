use std::collections::{BTreeMap, HashSet};
use std::path::Path;
use std::sync::atomic::AtomicU64;

use oap_adapter::{
    refuse_unadvertised_settings, refuse_unadvertised_tool_sources, refuse_unadvertised_tools,
    Adapter as AdapterContract, Admitter, BoxFuture, Descriptor, Error, JournalDescriptor,
    OpenRequest, Session, UnsupportedControl,
};
use oap_protocol::{
    CapabilityDescriptor, EndpointDescriptor, FeatureSupport, SupportLevel, ToolSourceDescriptor,
    FEATURE_COMPACTION_POLICY, FEATURE_MODELS_LIST, FEATURE_OPEN_REOPEN,
    FEATURE_SESSION_MODEL_SWITCH, FEATURE_SESSION_REASONING, FEATURE_TOOL_SOURCES_ATTACH, PROFILE,
    TOOL_SOURCE_MCP, TOOL_SOURCE_PROCESS, VERSION,
};

use crate::native::McpServer;
use crate::process::ProcessConfig;

#[derive(Clone, Debug, Default)]
pub struct Config {
    pub process: ProcessConfig,
    pub mcp_servers: Vec<McpServer>,
}

pub struct Adapter {
    config: Config,
    next_session: AtomicU64,
}

pub fn pin() -> &'static oap_harness::Version {
    oap_harness::current("acp").expect("embedded ACP catalog has a current version")
}

pub(crate) fn unavailable(feature: &str) -> Error {
    Error::UnsupportedControl(Box::new(UnsupportedControl::unadvertised(feature)))
}

impl Adapter {
    pub fn new(config: Config) -> Result<Self, Error> {
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
            config,
            next_session: AtomicU64::new(0),
        })
    }

    pub fn describe(&self) -> Descriptor {
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
            ("session.state", SupportLevel::EMULATED, "adapter-owned projection"),
            (FEATURE_MODELS_LIST, SupportLevel::DEGRADED, "the session/new model configOption snapshot; its category is a UI hint and supplies no provider, context window or pricing"),
        ] {
            features.insert(key.into(), FeatureSupport { level, reason: reason.into(), ..FeatureSupport::default() });
        }
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
                sources: self
                    .config
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
            capability_revision: format!("{}-rust-open", pin().capability_revision),
            journal: JournalDescriptor {
                replay: SupportLevel::UNAVAILABLE,
                ..JournalDescriptor::default()
            },
            ..Descriptor::default()
        }
    }

    fn validate_open(&self, request: &OpenRequest) -> Result<(), Error> {
        if request.participant.id.as_str().is_empty() {
            return Err(Error::InvalidParticipant);
        }
        if request.reopen {
            return Err(unavailable(FEATURE_OPEN_REOPEN));
        }
        refuse_unadvertised_tool_sources(request, &[])?;
        refuse_unadvertised_tools(request, &[])?;
        refuse_unadvertised_settings(request, &self.describe().capabilities)
    }
}

impl AdapterContract for Adapter {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        Box::pin(async { Ok(self.describe()) })
    }

    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        Box::pin(async move {
            self.validate_open(&request)?;
            crate::session::Session::open(
                &self.config,
                request,
                self.describe(),
                &self.next_session,
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
        let result = self.validate_open(request);
        Box::pin(async move { result })
    }
}
