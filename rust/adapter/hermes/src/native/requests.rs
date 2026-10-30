use std::collections::BTreeMap;

use oap_adapter::gojson::{go_quote, raw, strings, structures, zero};
use serde::{Deserialize, Serialize};

use super::{decode_strict, go_lossy, Raw};

pub const INVALID: &str = "hermes native: invalid pinned message";

#[derive(Clone, Debug, Default, Deserialize)]
pub struct ApprovalRequestParams {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub request_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub command: String,
    #[serde(default, deserialize_with = "zero")]
    pub description: String,
    #[serde(default, deserialize_with = "strings")]
    pub choices: Vec<String>,
    #[serde(default)]
    pub allow_permanent: Option<bool>,
    #[serde(default)]
    pub allow_session: Option<bool>,
    #[serde(default)]
    pub smart_denied: Option<bool>,
    #[serde(default, deserialize_with = "zero")]
    pub tool_name: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ClarifyQuestion {
    #[serde(default, deserialize_with = "zero")]
    pub qid: String,
    #[serde(default, deserialize_with = "zero")]
    pub question: String,
    #[serde(default, deserialize_with = "strings")]
    pub choices: Vec<String>,
    #[serde(default, deserialize_with = "zero")]
    pub multi_select: bool,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ClarifyRequestParams {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "structures")]
    pub questions: Vec<ClarifyQuestion>,
    #[serde(default)]
    pub answers: Option<BTreeMap<String, Option<String>>>,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SudoRequestParams {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub command: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SecretRequestParams {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub env_var: String,
    #[serde(default, deserialize_with = "zero")]
    pub prompt: String,
    #[serde(default, deserialize_with = "raw")]
    pub metadata: Raw,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct ApprovalResult {
    pub choice: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct ClarifyAnswersResult {
    pub answers: BTreeMap<String, String>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct ValueResult {
    pub value: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RequestCancelPayload {
    #[serde(default, deserialize_with = "zero")]
    pub id: String,
    #[serde(default, deserialize_with = "zero")]
    pub method: String,
    #[serde(default, deserialize_with = "zero")]
    pub reason: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ErrorPayload {
    #[serde(default, deserialize_with = "zero")]
    pub message: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SubagentPayload {
    #[serde(default, deserialize_with = "zero")]
    pub goal: String,
    #[serde(default, deserialize_with = "zero")]
    pub task_count: i64,
    #[serde(default, deserialize_with = "zero")]
    pub task_index: i64,
    #[serde(default, deserialize_with = "zero")]
    pub subagent_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub parent_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub child_session_id: String,
    #[serde(default)]
    pub depth: Option<i64>,
    #[serde(default, deserialize_with = "zero")]
    pub model: String,
    #[serde(default)]
    pub tool_count: Option<i64>,
    #[serde(default, deserialize_with = "strings")]
    pub toolsets: Vec<String>,
    #[serde(default, deserialize_with = "zero")]
    pub status: String,
    #[serde(default, deserialize_with = "zero")]
    pub summary: String,
    #[serde(default)]
    pub duration_seconds: Option<f64>,
    #[serde(default)]
    pub input_tokens: Option<i64>,
    #[serde(default)]
    pub output_tokens: Option<i64>,
    #[serde(default)]
    pub reasoning_tokens: Option<i64>,
    #[serde(default)]
    pub api_calls: Option<i64>,
    #[serde(default, deserialize_with = "strings")]
    pub files_read: Vec<String>,
    #[serde(default, deserialize_with = "strings")]
    pub files_written: Vec<String>,
    #[serde(default, deserialize_with = "raw")]
    pub output_tail: Raw,
    #[serde(default, deserialize_with = "zero")]
    pub tool_name: String,
    #[serde(default, deserialize_with = "zero")]
    pub tool_preview: String,
    #[serde(default, deserialize_with = "zero")]
    pub text: String,
    #[serde(default, deserialize_with = "zero")]
    pub delegation_id: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TaskCompletePayload {
    #[serde(default, deserialize_with = "zero")]
    pub task_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub question: String,
    #[serde(default, deserialize_with = "zero")]
    pub text: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct SessionInfoPayload {
    #[serde(default, deserialize_with = "zero")]
    pub model: String,
    #[serde(default, deserialize_with = "zero")]
    pub provider: String,
    #[serde(default, deserialize_with = "zero")]
    pub running: bool,
    #[serde(default)]
    pub turn_started_at: Option<f64>,
    #[serde(default, deserialize_with = "zero")]
    pub title: String,
    #[serde(default, deserialize_with = "zero")]
    pub stored_session_id: String,
}

#[derive(Clone, Debug)]
pub enum ServerRequest {
    Approval(ApprovalRequestParams),
    Clarify(ClarifyRequestParams),
    Sudo(SudoRequestParams),
    Secret(SecretRequestParams),
}

fn refused(detail: impl std::fmt::Display) -> String {
    format!("{INVALID}: {detail}")
}

pub fn decode_server_request(method: &str, params: &[u8]) -> Result<Option<ServerRequest>, String> {
    match method {
        super::REQUEST_APPROVAL => {
            let request: ApprovalRequestParams = decode_strict(params)
                .map_err(|error| refused(format!("invalid approval request: {error}")))?;
            if request.session_id.is_empty()
                || request.request_id.is_empty()
                || (request.command.is_empty() && request.description.is_empty())
                || request.choices.is_empty()
            {
                return Err(refused(
                    "approval request requires session_id, request_id, a command and choices",
                ));
            }
            if let Some(choice) = request
                .choices
                .iter()
                .find(|choice| !matches!(choice.as_str(), "once" | "session" | "always" | "deny"))
            {
                return Err(refused(format!("approval choice {}", go_quote(choice))));
            }
            Ok(Some(ServerRequest::Approval(request)))
        }
        super::REQUEST_CLARIFY => {
            let request: ClarifyRequestParams = decode_strict(params)
                .map_err(|error| refused(format!("invalid clarify request: {error}")))?;
            if request.session_id.is_empty() || request.questions.is_empty() {
                return Err(refused(
                    "clarify request needs a session and at least one question",
                ));
            }
            if request
                .questions
                .iter()
                .any(|question| question.qid.is_empty() || question.question.is_empty())
            {
                return Err(refused("invalid clarify batch question"));
            }
            Ok(Some(ServerRequest::Clarify(request)))
        }
        super::REQUEST_SUDO => match decode_strict::<SudoRequestParams>(params) {
            Ok(request) if !request.session_id.is_empty() => Ok(Some(ServerRequest::Sudo(request))),
            _ => Err(refused("invalid sudo request")),
        },
        super::REQUEST_SECRET => match decode_strict::<SecretRequestParams>(params) {
            Ok(request)
                if !request.session_id.is_empty()
                    && !request.env_var.is_empty()
                    && !request.prompt.is_empty() =>
            {
                Ok(Some(ServerRequest::Secret(request)))
            }
            _ => Err(refused("invalid secret request")),
        },
        _ => Ok(None),
    }
}

pub fn request_session_id(params: &[u8]) -> String {
    let text = go_lossy(params);
    let Ok(object) = serde_json::from_str::<BTreeMap<String, Box<serde_json::value::RawValue>>>(
        &oap_adapter::gojson::replace_lone_surrogates(&text),
    ) else {
        return String::new();
    };
    object
        .get("session_id")
        .and_then(|raw| serde_json::from_str::<Option<String>>(raw.get()).ok())
        .flatten()
        .unwrap_or_default()
}
