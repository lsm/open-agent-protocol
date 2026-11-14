use oxap_adapter::gojson::replace_lone_surrogates;
use oxap_adapter::{refuse_unadvertised_controls, Error};
use oxap_protocol::{
    ContentPart, ContentPartType, MessageId, MessageRole, MessageSubmitRequest,
    RequestedDeliveryMode,
};

use crate::native::ContentBlock;

fn text(text: String) -> ContentBlock {
    ContentBlock {
        kind: "text".into(),
        text,
        ..ContentBlock::default()
    }
}

pub fn native_prompt(
    request: &MessageSubmitRequest,
    mint: &dyn Fn(&str) -> String,
) -> Result<(Vec<ContentBlock>, Vec<MessageId>), Error> {
    refuse_unadvertised_controls(request, &[])?;
    if request.session_id.as_str().is_empty()
        || request.messages.is_empty()
        || !(request.delivery.is_empty() || request.delivery == RequestedDeliveryMode::AUTO)
    {
        return Err(Error::InvalidSubmission);
    }
    let mut blocks = Vec::new();
    let mut ids = Vec::with_capacity(request.messages.len());
    for message in &request.messages {
        if message.role != MessageRole::USER {
            return Err(Error::InvalidSubmission);
        }
        ids.push(
            message
                .id
                .clone()
                .filter(|id| !id.as_str().is_empty())
                .unwrap_or_else(|| mint("message").into()),
        );
        let raw = message.content.raw().ok_or(Error::InvalidSubmission)?;
        let content = replace_lone_surrogates(raw.get());
        if let Ok(body) = serde_json::from_str::<Option<String>>(&content) {
            blocks.push(text(body.unwrap_or_default()));
            continue;
        }
        let parts: Vec<ContentPart> =
            serde_json::from_str(&content).map_err(|_| Error::InvalidSubmission)?;
        for part in parts {
            if part.part_type != ContentPartType::TEXT {
                return Err(Error::InvalidSubmission);
            }
            blocks.push(text(part.text));
        }
    }
    Ok((blocks, ids))
}
