use serde::ser::SerializeMap;
use serde::{Deserialize, Serialize, Serializer};

use crate::serde_util::is_false;
use crate::vocabulary::vocabulary;
use crate::{InteractionId, ParticipantId, RunId, SessionId, ToolCallId};

vocabulary!(InputQuestionKind {
    TEXT = "text",
    SINGLE_CHOICE = "single_choice",
    MULTI_CHOICE = "multi_choice",
});

vocabulary!(UserInputResolutionStatus {
    SUBMITTED = "submitted",
    CANCELLED = "cancelled",
});

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct InputOption {
    pub id: String,
    pub label: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct InputQuestion {
    pub id: String,
    pub prompt: String,
    pub kind: InputQuestionKind,
    #[serde(skip_serializing_if = "is_false")]
    pub required: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub options: Vec<InputOption>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
#[serde(default)]
pub struct InputAnswer {
    pub question_id: String,
    pub text: String,
    pub selected_option_ids: Vec<String>,
}

impl Serialize for InputAnswer {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut answer = serializer.serialize_map(None)?;
        answer.serialize_entry("question_id", &self.question_id)?;
        if self.selected_option_ids.is_empty() || !self.text.is_empty() {
            answer.serialize_entry("text", &self.text)?;
        }
        if !self.selected_option_ids.is_empty() {
            answer.serialize_entry("selected_option_ids", &self.selected_option_ids)?;
        }
        answer.end()
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct UserInputRequestedPayload {
    pub interaction_id: InteractionId,
    pub requested_by: ParticipantId,
    pub responded_by: ParticipantId,
    pub session_id: SessionId,
    pub run_id: RunId,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tool_call_id: Option<ToolCallId>,
    pub title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    pub questions: Vec<InputQuestion>,
    #[serde(skip_serializing_if = "is_false")]
    pub allow_cancel: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub draft_answers: Vec<InputAnswer>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct UserInputResolveRequest {
    pub interaction_id: InteractionId,
    pub requested_by: ParticipantId,
    pub responded_by: ParticipantId,
    pub session_id: SessionId,
    pub run_id: RunId,
    pub answers: Vec<InputAnswer>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct UserInputResolvedPayload {
    pub interaction_id: InteractionId,
    pub requested_by: ParticipantId,
    pub responded_by: ParticipantId,
    pub session_id: SessionId,
    pub run_id: RunId,
    pub status: UserInputResolutionStatus,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub answers: Vec<InputAnswer>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct UserInputResolveResponse {
    pub interaction_id: InteractionId,
    pub session_id: SessionId,
    pub run_id: RunId,
    pub accepted: bool,
}

pub type UserInputCancelResponse = UserInputResolveResponse;

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct UserInputCancelRequest {
    pub interaction_id: InteractionId,
    pub requested_by: ParticipantId,
    pub responded_by: ParticipantId,
    pub session_id: SessionId,
    pub run_id: RunId,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
}
