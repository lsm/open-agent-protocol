use std::path::{Component, Path};

use oap_adapter::gojson::{go_quote, members};

use crate::native::{ThreadResumeParams, ThreadResumeResponse};

fn sandbox_policy(sandbox: &str) -> Option<&'static str> {
    match sandbox {
        "read-only" => Some("readOnly"),
        "workspace-write" => Some("workspaceWrite"),
        "danger-full-access" => Some("dangerFullAccess"),
        _ => None,
    }
}

fn sandbox_type(raw: &crate::native::Raw) -> Option<String> {
    let mut found = String::new();
    for (name, value) in members(raw.as_deref()?.get()).ok()? {
        if name.eq_ignore_ascii_case("type") {
            if let Some(text) = serde_json::from_str::<Option<String>>(value.get()).ok()? {
                found = text;
            }
        }
    }
    Some(found)
}

fn raw_text(raw: &crate::native::Raw) -> &str {
    raw.as_deref().map_or("", |value| value.get())
}

fn clean(path: &str) -> Vec<Component<'_>> {
    let mut parts: Vec<Component<'_>> = Vec::new();
    for part in Path::new(path).components() {
        match part {
            Component::CurDir => {}
            Component::ParentDir => match parts.last() {
                Some(Component::Normal(_)) => {
                    parts.pop();
                }
                Some(Component::RootDir) => {}
                _ => parts.push(part),
            },
            _ => parts.push(part),
        }
    }
    parts
}

pub fn confirm(sent: &ThreadResumeParams, answered: &ThreadResumeResponse) -> Result<(), String> {
    if !sent.sandbox.is_empty() {
        let Some(wanted) = sandbox_policy(&sent.sandbox) else {
            return Err(format!(
                "the configured sandbox {} has no thread/resume policy the adapter can confirm",
                sent.sandbox
            ));
        };
        if sandbox_type(&answered.sandbox).as_deref() != Some(wanted) {
            return Err(format!(
                "Codex resumed the thread under sandbox {}, not the configured {}",
                raw_text(&answered.sandbox),
                sent.sandbox
            ));
        }
    }
    if !sent.approval_policy.is_empty() {
        let policy = answered
            .approval_policy
            .as_deref()
            .and_then(|raw| serde_json::from_str::<String>(raw.get()).ok());
        if policy.as_deref() != Some(sent.approval_policy.as_str()) {
            return Err(format!(
                "Codex resumed the thread under approval policy {}, not the configured {}",
                raw_text(&answered.approval_policy),
                sent.approval_policy
            ));
        }
    }
    if !sent.cwd.is_empty() && clean(&answered.cwd) != clean(&sent.cwd) {
        return Err(format!(
            "Codex resumed the thread in {}, not the configured {}",
            go_quote(&answered.cwd),
            go_quote(&sent.cwd)
        ));
    }
    Ok(())
}
