use std::fmt;

use serde::{Deserialize, Serialize};

macro_rules! id {
    ($($name:ident),+ $(,)?) => {$(
        #[derive(Clone, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
        #[serde(transparent)]
        pub struct $name(pub String);

        impl $name {
            pub fn new(id: impl Into<String>) -> Self {
                Self(id.into())
            }

            pub fn as_str(&self) -> &str {
                &self.0
            }
        }

        impl From<String> for $name {
            fn from(id: String) -> Self {
                Self(id)
            }
        }

        impl From<&str> for $name {
            fn from(id: &str) -> Self {
                Self(id.to_owned())
            }
        }

        impl fmt::Display for $name {
            fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                f.write_str(&self.0)
            }
        }
    )+};
}

id!(
    EnvelopeId,
    ParticipantId,
    EndpointId,
    SessionId,
    SubmissionId,
    RunId,
    MessageId,
    ToolCallId,
    InteractionId,
    CompactionId,
    InferenceId,
    AuthFlowId,
);
