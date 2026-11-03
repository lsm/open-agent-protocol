use std::sync::Arc;

use futures::executor::block_on;
use oap_adapter::{OpenRequest, SubmitRequest};
use oap_binding::MemoryStore;
use oap_protocol::{
    Message, MessageContent, MessageRole, MessageSubmitRequest, RequestedDeliveryMode,
};
use oap_serve::{Hub, HubOptions, Registry};

#[test]
fn closing_the_hubs_sessions_cancels_a_live_run_first_and_records_the_close() {
    let hub = Hub::with_options(
        Registry::default_registry(),
        HubOptions {
            bindings: Some(Arc::new(MemoryStore::default())),
            ..HubOptions::default()
        },
    );
    block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
    let entry = hub.session(&"s".into()).unwrap();
    block_on(entry.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "s".into(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("run"),
                ..Message::default()
            }],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }))
    .unwrap();
    assert!(
        matches!(
            block_on(entry.close()),
            Err(oap_serve::HubError::Adapter(oap_adapter::Error::RunActive))
        ),
        "the run must hold the session open for this test to mean anything"
    );
    let failed = hub.close_sessions();
    assert!(failed.is_empty(), "{failed:?}");
    assert!(entry.is_closed());
    let listed = hub.session_history("", 10).unwrap();
    assert_eq!(listed.sessions[0].session_id, "s".into());
    assert_eq!(listed.sessions[0].state.as_str(), "closed", "{listed:?}");
}
