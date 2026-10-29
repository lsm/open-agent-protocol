use std::sync::{Arc, Mutex};

use oap_adapter::jsonrpc::{
    Dialect, Error, Fault, IncomingRequest, Kind, Message, RequestId, Responder,
};
use serde_json::value::RawValue;

const ACP: Dialect = Dialect::lenient("acp");

#[derive(Default)]
struct Recording {
    sent: Mutex<Vec<String>>,
    failures: Mutex<usize>,
}

impl Responder for Recording {
    fn send(&self, message: Message) -> Result<(), Error> {
        let mut failures = self.failures.lock().unwrap();
        if *failures > 0 {
            *failures -= 1;
            return Err(ACP.error(Fault::Transport("acp rpc: write queue is full".into())));
        }
        self.sent.lock().unwrap().push(message.encode(&ACP)?);
        Ok(())
    }
}

fn request(responder: &Arc<Recording>) -> IncomingRequest {
    IncomingRequest::new(
        ACP,
        RequestId::Integer(7),
        "session/request_permission",
        Some(RawValue::from_string(r#"{"sessionId":"s"}"#.into()).unwrap()),
        responder.clone(),
    )
}

#[test]
fn a_reverse_request_is_answered_once_and_a_second_answer_is_refused() {
    let responder = Arc::new(Recording::default());
    let incoming = request(&responder);
    assert_eq!(incoming.method, "session/request_permission");
    incoming
        .respond(&serde_json::json!({"outcome": {"outcome": "cancelled"}}))
        .unwrap();
    let refused = incoming.respond_error(-32603, "late").unwrap_err();
    assert!(matches!(refused.fault, Fault::ReverseRequestResolved));
    assert_eq!(
        refused.to_string(),
        "acp rpc: reverse request already resolved"
    );
    assert_eq!(
        *responder.sent.lock().unwrap(),
        [r#"{"id":7,"jsonrpc":"2.0","result":{"outcome":{"outcome":"cancelled"}}}"#]
    );
}

#[test]
fn an_error_answer_carries_its_code_message_and_data() {
    let responder = Arc::new(Recording::default());
    request(&responder)
        .respond_error_with(
            -32602,
            "bad <params>",
            Some(RawValue::from_string(r#"{"field":"x"}"#.into()).unwrap()),
        )
        .unwrap();
    let sent = responder.sent.lock().unwrap();
    let message = Message::parse(&ACP, sent[0].as_bytes()).unwrap();
    assert_eq!(message.kind, Kind::Error);
    let error = message.error.unwrap();
    assert_eq!(
        (error.code, error.message.as_str()),
        (-32602, "bad <params>")
    );
    assert_eq!(error.data.unwrap().get(), r#"{"field":"x"}"#);
    assert!(sent[0].contains(r#""message":"bad \u003cparams\u003e""#));
}

#[test]
fn an_answer_whose_write_fails_can_be_sent_again() {
    let responder = Arc::new(Recording::default());
    *responder.failures.lock().unwrap() = 1;
    let incoming = request(&responder);
    assert!(incoming.respond(&serde_json::json!({})).is_err());
    incoming.respond(&serde_json::json!({})).unwrap();
    assert_eq!(responder.sent.lock().unwrap().len(), 1);
}
