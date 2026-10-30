use oap_adapter::{open_setting_field, open_setting_keys, Descriptor, Error, UnsupportedControl};
use oap_protocol::{
    SessionOpenRequest, SupportLevel, ToolSourceAttachment, FEATURE_OPEN_REOPEN,
    FEATURE_OPEN_SUBSCRIBE, FEATURE_TOOL_SOURCES_ATTACH, MODE_SESSION_OPEN, TOOL_SOURCE_PROCESS,
};

use crate::{Hub, HubError, Registry};

#[derive(Clone, Debug, PartialEq, Eq, thiserror::Error)]
#[error("tool source {tool_source:?}: {reason}")]
pub struct AttachmentRefusal {
    pub tool_source: String,
    pub reason: String,
}

fn unadvertised(feature: &str, field: &str) -> HubError {
    HubError::Adapter(Error::UnsupportedControl(Box::new(UnsupportedControl {
        field: field.to_owned(),
        ..UnsupportedControl::unadvertised(feature)
    })))
}

async fn current(hub: &Hub, name: &str, revision: &str) -> Result<Descriptor, HubError> {
    let adapter = hub
        .registry()
        .lookup(name)
        .ok_or_else(|| HubError::UnknownAdapter(name.into()))?;
    let descriptor = adapter.probe().await?;
    if !revision.is_empty() && revision != descriptor.capability_revision {
        return Err(HubError::StaleRevision {
            expected: descriptor.capability_revision,
            current: revision.into(),
        });
    }
    Ok(descriptor)
}

pub async fn attachment_gate(
    hub: &Hub,
    name: &str,
    revision: &str,
    request: &SessionOpenRequest,
) -> Result<String, HubError> {
    if request.tool_sources.is_empty() {
        return Ok(String::new());
    }
    let descriptor = current(hub, name, revision).await?;
    let support = descriptor
        .capabilities
        .effective_support(FEATURE_TOOL_SOURCES_ATTACH)
        .filter(|support| !support.level.is_empty() && support.level != SupportLevel::UNAVAILABLE)
        .filter(|support| support.discloses_mode(MODE_SESSION_OPEN))
        .ok_or_else(|| unadvertised(FEATURE_TOOL_SOURCES_ATTACH, ""))?;
    if support.level == SupportLevel::DEGRADED
        && !request.allows_degraded(FEATURE_TOOL_SOURCES_ATTACH)
    {
        return Err(HubError::Adapter(Error::DegradedControl(
            FEATURE_TOOL_SOURCES_ATTACH.into(),
        )));
    }
    Ok(descriptor.capability_revision)
}

pub async fn election_gate(
    hub: &Hub,
    name: &str,
    revision: &str,
    request: &SessionOpenRequest,
) -> Result<String, HubError> {
    let settings = open_setting_keys(&request.reasoning_level, request.compaction_policy.as_ref());
    let mut elected = Vec::new();
    if request.subscribe {
        elected.push(FEATURE_OPEN_SUBSCRIBE);
    }
    if request.reopen {
        elected.push(FEATURE_OPEN_REOPEN);
    }
    elected.extend(settings.iter().copied());
    if elected.is_empty() {
        return Ok(String::new());
    }
    let descriptor = current(hub, name, revision).await?;
    for key in elected {
        let field = open_setting_field(key);
        let support = descriptor
            .capabilities
            .effective_support(key)
            .filter(|support| {
                !support.level.is_empty() && support.level != SupportLevel::UNAVAILABLE
            })
            .ok_or_else(|| unadvertised(key, field))?;
        if settings.contains(&key) && !support.discloses_mode(MODE_SESSION_OPEN) {
            return Err(unadvertised(key, field));
        }
        if support.level == SupportLevel::DEGRADED && !request.allows_degraded(key) {
            return Err(HubError::Adapter(Error::DegradedControl(key.into())));
        }
    }
    Ok(descriptor.capability_revision)
}

pub fn resolve_attachments(
    registry: &Registry,
    attachments: &[ToolSourceAttachment],
) -> Result<Vec<ToolSourceAttachment>, AttachmentRefusal> {
    let mut resolved = Vec::new();
    for attachment in attachments {
        let refuse = |reason: String| AttachmentRefusal {
            tool_source: attachment.id.clone(),
            reason,
        };
        if !attachment.command.is_empty() || !attachment.args.is_empty() {
            return Err(refuse("the daemon does not accept a command or arguments from the wire; name an operator-configured source by id".into()));
        }
        if attachment
            .environment
            .iter()
            .any(|entry| entry.contains('='))
        {
            return Err(refuse(
                "the daemon accepts only the bare NAME allowlist form in environment".into(),
            ));
        }
        let Some(configured) = registry.tool_source(&attachment.id) else {
            if attachment.kind == TOOL_SOURCE_PROCESS {
                return Err(refuse(
                    "no tool source of that id is configured on this daemon".into(),
                ));
            }
            resolved.push(attachment.clone());
            continue;
        };
        for (name, wire, operator) in [
            ("kind", &attachment.kind, &configured.kind),
            (
                "display_name",
                &attachment.display_name,
                &configured.display_name,
            ),
            ("protocol", &attachment.protocol, &configured.protocol),
            ("endpoint", &attachment.endpoint, &configured.endpoint),
        ] {
            if !wire.is_empty() && wire != operator {
                return Err(refuse(format!(
                    "the daemon does not accept {name} from the wire for a configured source; name it by id"
                )));
            }
        }
        let mut source = configured.clone();
        for entry in &attachment.environment {
            let name = entry.split('=').next().unwrap_or_default();
            let known = source
                .environment
                .iter()
                .any(|held| held.split('=').next().unwrap_or_default() == name);
            if !known {
                source.environment.push(entry.clone());
            }
        }
        resolved.push(source);
    }
    Ok(resolved)
}
