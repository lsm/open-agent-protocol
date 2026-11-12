use oap_adapter::Error;
use oap_protocol::{MessageId, MessageSubmitRequest};

use crate::native::ContentBlock;

pub fn native_prompt(
    _request: &MessageSubmitRequest,
    _mint: &dyn Fn(&str) -> String,
) -> Result<(Vec<ContentBlock>, Vec<MessageId>), Error> {
    Ok((Vec::new(), Vec::new()))
}
