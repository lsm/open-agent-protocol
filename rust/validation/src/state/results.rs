use oap_protocol::*;
use serde_json::Value;

use super::{is_request, State};
use crate::codes;

fn carries_image(result: Option<&Value>) -> bool {
    let Some(Value::Array(parts)) = result else {
        return false;
    };
    if parts.is_empty() {
        return false;
    }
    let mut image = false;
    for part in parts {
        match part.get("type").and_then(Value::as_str) {
            Some("text") => {}
            Some("image") => image = true,
            _ => return false,
        }
    }
    image
}

fn tool_result_image(node: &Value, pointer: &str) -> Option<String> {
    match node {
        Value::Object(object) => {
            if object.get("type").and_then(Value::as_str) == Some("tool_result")
                && carries_image(object.get("result"))
            {
                return Some(format!("{pointer}/result"));
            }
            object
                .iter()
                .find_map(|(key, child)| tool_result_image(child, &format!("{pointer}/{key}")))
        }
        Value::Array(items) => items
            .iter()
            .enumerate()
            .find_map(|(index, child)| tool_result_image(child, &format!("{pointer}/{index}"))),
        _ => None,
    }
}

impl State {
    pub(super) fn check_result_images(&mut self, at: (usize, usize), e: &Envelope) {
        if is_request(&e.envelope_type) {
            return;
        }
        if !self.current_capability.is_empty()
            && !self.capabilities_stale
            && self
                .features
                .get(FEATURE_CONTENT_IMAGE)
                .is_some_and(|level| *level != SupportLevel::UNAVAILABLE)
        {
            return;
        }
        let Some(raw) = e.payload.as_ref() else {
            return;
        };
        let Ok(payload) = serde_json::from_str::<Value>(raw.get()) else {
            return;
        };
        let pointer = if e.envelope_type == EnvelopeType::ACTION_CALL_COMPLETED
            && carries_image(payload.get("result"))
        {
            Some("/payload/result".to_owned())
        } else {
            tool_result_image(&payload, "/payload")
        };
        if let Some(pointer) = pointer {
            self.add_expected(
                codes::UNAVAILABLE_CAPABILITY,
                at,
                e,
                &pointer,
                "a tool result carries an image part where content.image is not advertised",
                FEATURE_CONTENT_IMAGE,
                "unadvertised",
                &[],
            );
        }
    }
}
