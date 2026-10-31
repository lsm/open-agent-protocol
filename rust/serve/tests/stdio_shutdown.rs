mod support;

use std::io::{BufReader, Read};
use std::sync::mpsc;
use std::sync::Arc;
use std::time::Duration;

use futures::executor::block_on;
use oap_adapter::{OpenRequest, SubmitRequest};
use oap_protocol::{
    Message, MessageContent, MessageRole, MessageSubmitRequest, RequestedDeliveryMode,
};
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

struct Held(std::io::Cursor<Vec<u8>>);

impl Read for Held {
    fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
        let read = self.0.read(buf)?;
        if read > 0 {
            return Ok(read);
        }
        loop {
            std::thread::park();
        }
    }
}

fn held_open(lines: &[&str]) -> BufReader<Held> {
    let text: String = lines.iter().map(|line| format!("{line}\n")).collect();
    BufReader::new(Held(std::io::Cursor::new(text.into_bytes())))
}

fn served_within(hub: Arc<Hub>, input: BufReader<Held>) -> Option<Vec<Value>> {
    let output = support::Captured::default();
    let captured = output.clone();
    let (done, finished) = mpsc::channel();
    std::thread::spawn(move || {
        let _ = done.send(serve(&hub, input, output, &StdioOptions::default()));
    });
    finished
        .recv_timeout(Duration::from_secs(10))
        .ok()?
        .unwrap();
    Some(
        String::from_utf8(captured.bytes())
            .unwrap()
            .lines()
            .map(|line| serde_json::from_str(line).unwrap())
            .collect(),
    )
}

fn with_a_run() -> (Arc<Hub>, oap_protocol::RunId) {
    let hub = Hub::new(Registry::default_registry());
    let opened = block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
    let run = block_on(opened.session.submit(SubmitRequest {
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
    .unwrap()
    .run_id
    .unwrap();
    (Arc::new(hub), run)
}

fn answer(lines: &[Value], id: i64) -> &Value {
    lines.iter().find(|line| line["id"] == id).unwrap()
}

#[test]
fn a_shutdown_with_nothing_running_stops_serving_with_input_still_open() {
    let hub = Arc::new(Hub::new(Registry::default_registry()));
    let answered = served_within(hub, held_open(&[r#"{"id":1,"op":"shutdown"}"#]))
        .expect("serve kept reading after the shutdown completed");
    let result = &answer(&answered, 1)["result"];
    assert_eq!(result["outcome"], "stopping", "{}", answered[0]);
    assert_eq!(result["active_runs"], serde_json::json!([]));
}

#[test]
fn a_shutdown_naming_no_mode_while_a_run_is_live_asks_the_host_to_choose() {
    let (hub, run) = with_a_run();
    let answered = served_within(
        hub,
        held_open(&[
            r#"{"id":1,"op":"shutdown","request":{}}"#,
            r#"{"id":2,"op":"shutdown","request":{"mode":"soon"}}"#,
            r#"{"id":3,"op":"shutdown","request":{"mode":"force"}}"#,
        ]),
    )
    .expect("serve kept reading after a forced shutdown");
    let chose = &answer(&answered, 1)["result"];
    assert_eq!(chose["outcome"], "choose", "{chose}");
    assert_eq!(chose["active_runs"][0]["run_id"], run.as_str());
    assert_eq!(answer(&answered, 2)["error"]["code"], "invalid_request");
    assert_eq!(answer(&answered, 3)["result"]["outcome"], "stopping");
}

#[test]
fn a_graceful_shutdown_refuses_new_work_and_stops_once_its_runs_settle() {
    let (hub, run) = with_a_run();
    let cancel = format!(
        r#"{{"id":3,"op":"cancel","session_id":"s","request":{{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.cancel.request","id":"cancel-1","session_id":"s","run_id":"{run}","payload":{{"session_id":"s","run_id":"{run}"}}}}}}"#,
        run = run.as_str()
    );
    let answered = served_within(
        hub,
        held_open(&[
            r#"{"id":1,"op":"shutdown","request":{"mode":"graceful"}}"#,
            r#"{"id":2,"op":"open","adapter":"memory","request":{}}"#,
            &cancel,
        ]),
    )
    .expect("serve kept reading after the graceful shutdown drained");
    let draining = &answer(&answered, 1)["result"];
    assert_eq!(draining["outcome"], "draining", "{draining}");
    assert_eq!(answer(&answered, 2)["error"]["code"], "shutting_down");
    assert_eq!(answer(&answered, 3)["ok"], true, "{}", answer(&answered, 3));
}
