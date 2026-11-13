mod support;

use std::sync::Arc;
use std::time::{Duration, Instant};

use oap_adapter_codex_appserver::session::Session;
use support::{adapter, request, Fake};

fn open() -> (Arc<Fake>, Session) {
    let fake = Fake::new([
        Ok(r#"{"thread":{"id":"t-1"}}"#),
        Ok(r#"{"turn":{"id":"turn-1","status":"inProgress"}}"#),
    ]);
    let session = adapter(&fake, |_| {}).open(request()).unwrap();
    (fake, session)
}

#[test]
fn a_reverse_request_is_answered_as_unsupported() {
    let (fake, _session) = open();
    let answers = fake.request("item/tool/call", "{}");
    let deadline = Instant::now() + Duration::from_secs(5);
    while answers.0.lock().unwrap().is_empty() {
        assert!(Instant::now() < deadline, "the request was never answered");
        std::thread::sleep(Duration::from_millis(10));
    }
    let answer = &answers.0.lock().unwrap()[0];
    let error = answer.error.as_ref().unwrap();
    assert_eq!(
        (error.code, error.message.as_str()),
        (-32601, "unsupported Codex server request")
    );
    assert_eq!(
        error.data.as_ref().map(|data| data.get()),
        Some(r#"{"method":"item/tool/call"}"#)
    );
}
