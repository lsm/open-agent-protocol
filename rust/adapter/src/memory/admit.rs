use std::collections::HashSet;

use oap_protocol::*;
use serde_json::value::RawValue;

use super::{
    declared_sources, scripted_catalog, ATTACH_TRANSPORTS, MAX_ATTACHED_SOURCES,
    MAX_PROVIDED_TOOLS, PROVIDED_DIALECT, PROVIDED_NAME_PATTERN, REFERENCE_WINDOW,
};
use crate::{
    duplicate_environment_name, refuse_unadvertised_tool_sources, refuse_unadvertised_tools, Error,
    OpenRequest, UnsupportedControl, CONTROL_UNSATISFIABLE,
};

fn unsatisfiable(feature: &str, detail: String) -> UnsupportedControl {
    UnsupportedControl {
        feature: feature.to_owned(),
        reason: CONTROL_UNSATISFIABLE,
        detail,
        ..UnsupportedControl::default()
    }
}

pub(super) fn compaction_threshold(policy: Option<&CompactionPolicy>) -> Result<u64, Error> {
    let window = REFERENCE_WINDOW as i64;
    let threshold = match policy {
        None => Some(window * 80 / 100),
        Some(policy) => match policy.kind.as_str() {
            "off" => return Ok(u64::MAX),
            "auto" => Some(window * 80 / 100),
            "share" if (1..=100).contains(&policy.share_percent) => {
                Some(window * policy.share_percent / 100)
            }
            "tokens" if policy.tokens >= 1 => Some(policy.tokens),
            _ => None,
        },
    };
    threshold.map(|value| value as u64).ok_or_else(|| {
        let detail = "the reference adapter takes auto, off, a share from 1 to 100 or a positive token count".to_owned();
        let control = UnsupportedControl { field: "compaction_policy".into(), ..unsatisfiable(FEATURE_COMPACTION_POLICY, detail) };
        Error::UnsupportedControl(Box::new(control))
    })
}

pub(super) fn effective_policy(policy: Option<&CompactionPolicy>) -> CompactionPolicy {
    let Some(policy) = policy else {
        return CompactionPolicy {
            kind: CompactionKind::AUTO,
            ..CompactionPolicy::default()
        };
    };
    match policy.kind.as_str() {
        "share" => CompactionPolicy {
            kind: policy.kind.clone(),
            share_percent: policy.share_percent,
            ..CompactionPolicy::default()
        },
        "tokens" => CompactionPolicy {
            kind: policy.kind.clone(),
            tokens: policy.tokens,
            ..CompactionPolicy::default()
        },
        _ => CompactionPolicy {
            kind: policy.kind.clone(),
            ..CompactionPolicy::default()
        },
    }
}

pub fn history_tokens(messages: &[Message]) -> u64 {
    let bytes: usize = messages
        .iter()
        .map(|message| match message.content.text() {
            Some(text) => text.len(),
            None => message
                .content
                .parts()
                .unwrap_or_default()
                .iter()
                .filter(|part| part.part_type == ContentPartType::TEXT)
                .map(|part| part.text.len())
                .sum(),
        })
        .sum();
    (bytes as u64).div_ceil(4)
}

pub(super) fn admit_tool_sources(
    request: &OpenRequest,
) -> Result<Vec<ToolSourceAttachment>, Error> {
    refuse_unadvertised_tool_sources(request, &[super::attach_support()])?;
    let refuse = |source: &str, detail: String| {
        let control = UnsupportedControl {
            source: source.to_owned(),
            ..unsatisfiable(FEATURE_TOOL_SOURCES_ATTACH, detail)
        };
        Err(Error::UnsupportedControl(Box::new(control)))
    };
    if let Some(extra) = request.tool_sources.get(MAX_ATTACHED_SOURCES) {
        return refuse(
            &extra.id,
            format!("at most {MAX_ATTACHED_SOURCES} sources may be attached"),
        );
    }
    let declared: HashSet<String> = declared_sources()
        .into_iter()
        .map(|source| source.id)
        .collect();
    let mut seen = HashSet::new();
    for attachment in &request.tool_sources {
        if attachment.id.is_empty() || attachment.kind.is_empty() {
            return refuse(
                &attachment.id,
                "an attachment needs an id and a kind".into(),
            );
        }
        if seen.contains(&attachment.id) || declared.contains(&attachment.id) {
            return refuse(
                &attachment.id,
                "the id already resolves to a declared or attached source".into(),
            );
        }
        if !ATTACH_TRANSPORTS.contains(&attachment.kind.as_str()) {
            return refuse(
                &attachment.id,
                format!(
                    "kind {} is outside the disclosed transports",
                    attachment.kind
                ),
            );
        }
        if let Some(name) = duplicate_environment_name(&attachment.environment) {
            return refuse(&attachment.id, format!("environment names {name} twice"));
        }
        seen.insert(attachment.id.clone());
    }
    Ok(request.tool_sources.clone())
}

pub(super) fn session_sources(attached: &[ToolSourceAttachment]) -> Vec<ToolSourceDescriptor> {
    declared_sources()
        .into_iter()
        .chain(attached.iter().map(ToolSourceAttachment::descriptor))
        .collect()
}

fn in_name_pattern(name: &str) -> bool {
    let mut chars = name.chars();
    chars.next().is_some_and(|first| first.is_ascii_lowercase())
        && chars.all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_')
}

fn admissible_dialect(schema: Option<&RawValue>) -> bool {
    let Some(schema) = schema else { return true };
    match serde_json::from_str::<serde_json::Map<String, serde_json::Value>>(schema.get()) {
        Ok(object) => match object.get("$schema") {
            None => true,
            Some(serde_json::Value::String(dialect)) => {
                dialect.is_empty() || dialect == PROVIDED_DIALECT
            }
            Some(_) => false,
        },
        Err(_) => schema.get().trim() == "null",
    }
}

pub(super) fn admit_provided_tools(
    request: &OpenRequest,
    attached: &[ToolSourceAttachment],
) -> Result<Vec<ToolDefinition>, Error> {
    refuse_unadvertised_tools(request, &[super::provide_support()])?;
    let refuse = |tool: &str, detail: String| {
        let control = UnsupportedControl {
            tool: tool.to_owned(),
            ..unsatisfiable(FEATURE_TOOLS_PROVIDE, detail)
        };
        Err(Error::UnsupportedControl(Box::new(control)))
    };
    let resolvable: HashSet<String> = session_sources(attached)
        .into_iter()
        .map(|source| source.id)
        .collect();
    let mut taken: HashSet<String> = scripted_catalog()
        .into_iter()
        .map(|tool| tool.name)
        .collect();
    if let Some(extra) = request.tools.get(MAX_PROVIDED_TOOLS) {
        return refuse(
            &extra.name,
            format!("at most {MAX_PROVIDED_TOOLS} tools may be provided"),
        );
    }
    for tool in &request.tools {
        let detail = if tool.name.is_empty() {
            Some("a provided tool needs a name".to_owned())
        } else if tool.execution_owner != request.participant.id {
            Some("execution_owner must be the opening participant".to_owned())
        } else if tool.source.is_empty() {
            Some("a provided tool must name a source, since the session's catalog lists every tool by one".to_owned())
        } else if !resolvable.contains(&tool.source) {
            Some(format!(
                "source {} resolves to no declared or attached source",
                tool.source
            ))
        } else if taken.contains(&tool.name) {
            Some("the name already resolves to a catalog entry".to_owned())
        } else if !in_name_pattern(&tool.name) {
            Some(format!(
                "the name is outside the disclosed name_pattern {PROVIDED_NAME_PATTERN}"
            ))
        } else if !admissible_dialect(tool.input_schema.as_deref()) {
            Some(format!(
                "the input schema declares a dialect outside the disclosed {PROVIDED_DIALECT}"
            ))
        } else {
            None
        };
        if let Some(detail) = detail {
            return refuse(&tool.name, detail);
        }
        taken.insert(tool.name.clone());
    }
    Ok(request.tools.clone())
}
