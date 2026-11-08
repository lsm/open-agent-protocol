use std::collections::{BTreeMap, HashSet};

use oap_adapter::{
    duplicate_environment_name, refuse_unadvertised_tool_sources, refuse_unadvertised_tools, Error,
    OpenRequest, UnsupportedControl, CONTROL_UNSATISFIABLE,
};
use oap_protocol::{
    FeatureSupport, SupportLevel, FEATURE_TOOL_SOURCES_ATTACH, LIMIT_TRANSPORTS, MODE_SESSION_OPEN,
    TOOL_SOURCE_PROCESS,
};

use crate::native::{EnvVariable, McpServer};
use crate::Config;

pub(crate) fn support() -> FeatureSupport {
    FeatureSupport {
        level: SupportLevel::NATIVE,
        modes: vec![MODE_SESSION_OPEN.into()],
        limits: [(
            LIMIT_TRANSPORTS.into(),
            serde_json::value::to_raw_value(&[TOOL_SOURCE_PROCESS]).expect("static transport list"),
        )]
        .into(),
        reason: "session/new carries the MCP server array; stdio descriptors only at this pin"
            .into(),
        ..FeatureSupport::default()
    }
}

fn refuse(source: &str, detail: impl Into<String>) -> Error {
    Error::UnsupportedControl(Box::new(UnsupportedControl {
        feature: FEATURE_TOOL_SOURCES_ATTACH.into(),
        reason: CONTROL_UNSATISFIABLE,
        source: source.into(),
        detail: detail.into(),
        ..UnsupportedControl::default()
    }))
}

pub(crate) fn attached(config: &Config, request: &OpenRequest) -> Result<Vec<McpServer>, Error> {
    refuse_unadvertised_tool_sources(request, &[support()])?;
    refuse_unadvertised_tools(request, &[])?;
    let allowlist: BTreeMap<_, _> = config
        .process
        .env
        .iter()
        .flatten()
        .filter_map(|entry| entry.split_once('='))
        .collect();
    let mut seen: HashSet<_> = config
        .mcp_servers
        .iter()
        .map(|server| server.name.as_str())
        .collect();
    let mut servers = Vec::new();
    for attachment in &request.tool_sources {
        if attachment.id.is_empty() {
            return Err(refuse("", "an attachment needs an id"));
        }
        if !seen.insert(attachment.id.as_str()) {
            return Err(refuse(
                &attachment.id,
                "the id already names a configured or attached MCP server",
            ));
        }
        if attachment.kind != TOOL_SOURCE_PROCESS {
            return Err(refuse(
                &attachment.id,
                "ACP v1 accepts stdio MCP descriptors only at this pin",
            ));
        }
        if let Some(name) =
            duplicate_environment_name(&attachment.environment).filter(|name| !name.is_empty())
        {
            return Err(refuse(
                &attachment.id,
                format!("environment names {name} twice"),
            ));
        }
        if attachment.command.is_empty() {
            return Err(Error::InvalidResolution);
        }
        let env = attachment
            .environment
            .iter()
            .filter_map(|entry| {
                if let Some((name, value)) = entry.split_once('=') {
                    Some(EnvVariable {
                        name: name.into(),
                        value: value.into(),
                    })
                } else {
                    allowlist.get(entry.as_str()).map(|value| EnvVariable {
                        name: entry.clone(),
                        value: (*value).into(),
                    })
                }
            })
            .collect();
        servers.push(McpServer {
            name: attachment.id.clone(),
            command: attachment.command.clone(),
            args: attachment.args.clone(),
            env,
        });
    }
    Ok(servers)
}
