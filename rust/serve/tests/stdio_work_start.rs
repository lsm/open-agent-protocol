mod support;

use std::sync::Arc;

use futures::executor::block_on;
use oap_adapter::{
    Adapter, BoxFuture, Descriptor, NativeListRequest, NativeLister, NativeListing, OpenRequest,
    Session,
};
use oap_binding::MemoryStore;
use oap_serve::stdio::StdioOptions;
use oap_serve::{Hub, HubOptions, Registry};
use serde_json::Value;

struct Busy(Arc<dyn Adapter>);

impl Adapter for Busy {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        self.0.probe()
    }
    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        self.0.open(request)
    }
    fn native_lister(&self) -> Option<&dyn NativeLister> {
        Some(self)
    }
}

impl NativeLister for Busy {
    fn native_list(&self, _: NativeListRequest) -> BoxFuture<'_, Vec<NativeListing>> {
        Box::pin(async {
            Ok(vec![NativeListing {
                native_id: "busy".into(),
                running: true,
                ..NativeListing::default()
            }])
        })
    }
}

fn lines(hub: &Hub, input: &[&str]) -> Vec<Value> {
    support::in_turn(hub, input, &StdioOptions::default())
}

fn recorded() -> Hub {
    Hub::with_options(
        Registry::default_registry(),
        HubOptions {
            bindings: Some(Arc::new(MemoryStore::default())),
            ..HubOptions::default()
        },
    )
}

#[test]
fn work_start_and_send_refuse_what_they_cannot_serve_with_gos_messages() {
    let hub = Hub::new(Registry::default_registry());
    let answered = lines(
        &hub,
        &[
            r#"{"id":1,"op":"work.start","request":{"message":"go"}}"#,
            r#"{"id":2,"op":"work.start","adapter":"memory"}"#,
            r#"{"id":3,"op":"work.start","adapter":"memory","request":"x"}"#,
            r#"{"id":4,"op":"work.start","adapter":"memory","request":{}}"#,
            r#"{"id":5,"op":"work.start","adapter":"memory","request":{"message":""}}"#,
            r#"{"id":6,"op":"work.start","adapter":"nope","request":{"message":"go"}}"#,
            r#"{"id":7,"op":"work.start","adapter":"memory","request":{"message":"go","directory":"/elsewhere"}}"#,
            r#"{"id":8,"op":"work.start","adapter":"memory","request":{"message":"go","native_id":""}}"#,
            r#"{"id":9,"op":"work.send","session_id":"absent","request":{"message":"hi"}}"#,
            r#"{"id":10,"op":"work.send","session_id":"absent"}"#,
        ],
    );
    let expected = [
        ("invalid_request", "adapter is required"),
        ("invalid_request", "request is required"),
        ("invalid_request", "request is an object"),
        ("invalid_request", "request.message is required"),
        ("invalid_request", "request.message is a non-empty string"),
        ("unknown_adapter", "no adapter is registered as \"nope\""),
        (
            "invalid_request",
            "a session runs in its adapter's working directory; another directory needs its own adapter entry, or \"any_directory\": true on this one",
        ),
        ("invalid_request", "request.native_id is a non-empty string"),
        ("unknown_session", "no session \"absent\""),
        ("invalid_request", "request is required"),
    ];
    for (line, (code, message)) in answered.iter().zip(expected) {
        assert_eq!(line["error"]["code"], code, "{line}");
        assert_eq!(line["error"]["message"], message, "{line}");
    }
    assert_eq!(answered[6]["error"]["details"]["working_directory"], "");
}

#[test]
fn work_start_opens_a_titled_session_and_work_send_adds_to_it() {
    let hub = Hub::new(Registry::default_registry());
    let started = lines(
        &hub,
        &[
            r#"{"id":1,"op":"work.start","adapter":"memory","request":{"message":"go","title":"T"}}"#,
        ],
    );
    let work = &started[0]["result"];
    assert_eq!(work["title"], "T", "{work}");
    assert_eq!(work["ref"]["adapter"], "memory");
    assert!(work["run_id"].is_string());
    let id = work["ref"]["session_id"].as_str().unwrap().to_owned();
    let send = format!(
        r#"{{"id":2,"op":"work.send","session_id":"{id}","request":{{"message":"more"}}}}"#
    );
    let sent = lines(&hub, &[send.as_str()]);
    assert_eq!(sent[0]["ok"], true, "{}", sent[0]);
    let turns = hub.session(&id.as_str().into()).unwrap().turns(None, 10);
    let texts: Vec<&str> = turns.turns.iter().map(|turn| turn.text.as_str()).collect();
    assert_eq!(texts, ["go", "more"]);
}

#[test]
fn work_send_reopens_a_session_only_the_history_names() {
    let hub = recorded();
    block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
    block_on(hub.session(&"s".into()).unwrap().close()).unwrap();
    let sent = lines(
        &hub,
        &[r#"{"id":1,"op":"work.send","session_id":"s","request":{"message":"again"}}"#],
    );
    assert_eq!(sent[0]["result"]["ref"]["session_id"], "s", "{}", sent[0]);
    assert!(hub.session(&"s".into()).is_ok());
}

#[test]
fn work_start_refuses_to_adopt_a_harness_session_another_process_is_running() {
    let memory = Registry::default_registry().lookup("memory").unwrap();
    let mut registry = Registry::new();
    registry.register("busy", Arc::new(Busy(memory))).unwrap();
    let hub = Hub::new(registry);
    let answered = lines(
        &hub,
        &[
            r#"{"id":1,"op":"work.start","adapter":"busy","request":{"message":"go","native_id":"busy"}}"#,
        ],
    );
    assert_eq!(
        answered[0]["error"]["code"], "run_active",
        "{}",
        answered[0]
    );
}
