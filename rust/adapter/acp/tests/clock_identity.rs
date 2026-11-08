use std::path::Path;

use futures::{executor::block_on, StreamExt};
use oap_adapter_acp::{TextRun, TextRunContext};
use serde_json::Value;

fn expected() -> Vec<Value> {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../fixtures/adapters/acp-v1.15.1/cases/new-prompt-completed/expected-oap.json");
    serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap()
}

#[test]
fn the_recorded_start_payload_and_envelope_have_distinct_clock_reads() {
    let expected = expected();
    let event = &expected[0];
    let mut run = TextRun::new(
        TextRunContext {
            session_id: "session".into(),
            native_id: "native-session".into(),
            run_id: "run-b".into(),
            message_id: "session/message-c".into(),
            capability_revision: Some(event["capability_revision"].as_str().unwrap().into()),
        },
        |_| "event-d".into(),
    );
    let mut stream = run.start(3).unwrap();
    let actual = block_on(stream.next()).unwrap().unwrap();
    assert_eq!(serde_json::to_value(actual).unwrap(), *event);
}
