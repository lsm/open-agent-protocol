use std::sync::{mpsc, Arc};
use std::time::Duration;

use futures::executor::block_on;
use oap_adapter::jsonrpc::{Error, Fault, Inbound, IncomingRequest, Message, RequestId, Responder};
use oap_adapter_acp::process::{Process, ProcessConfig};
use oap_adapter_acp::PromptBridge;
use serde_json::json;

struct Foreign;

impl Responder for Foreign {
    fn send(&self, _: Message) -> Result<(), Error> {
        Err(oap_adapter_acp::process::DIALECT
            .error(Fault::Transport("foreign writer called".into())))
    }
}

#[test]
fn owned_notifications_and_reverse_replies_preserve_the_native_wire() {
    let process = block_on(Process::start_async(
        ProcessConfig {
            path: "/bin/sh".into(),
            args: vec!["-c".into(), r#"read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}' '{"jsonrpc":"2.0","id":"native","method":"fixture","params":{}}'; read -r reply; printf '%s\n' "$reply" >&2; read -r notification; printf '%s\n' "$notification" >&2; exit 0"#.into()],
            env: Some(Vec::new()),
            queue_capacity: 1,
            write_queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_secs(1)),
            ..ProcessConfig::default()
        },
        None,
    ))
    .unwrap();
    let (send, receive) = mpsc::channel();
    let bridge = Arc::new(PromptBridge::new(process, move |message| {
        if let Inbound::Request(request) = message {
            send.send(request).unwrap();
        }
    }));
    let request = receive.recv_timeout(Duration::from_secs(3)).unwrap();
    let foreign = Arc::new(IncomingRequest::new(
        oap_adapter_acp::process::DIALECT,
        RequestId::String("native".into()),
        "fixture",
        None,
        Arc::new(Foreign),
    ));
    let error = block_on(bridge.respond(
        foreign,
        serde_json::value::to_raw_value(&json!({})).unwrap(),
    ))
    .unwrap_err();
    assert!(error.to_string().contains("another bridge"));
    block_on(bridge.respond(
        request.clone(),
        serde_json::value::to_raw_value(&json!({"accepted":true})).unwrap(),
    ))
    .unwrap();
    let repeated = block_on(bridge.respond(
        request,
        serde_json::value::to_raw_value(&json!({})).unwrap(),
    ));
    assert!(repeated
        .unwrap_err()
        .to_string()
        .contains("already resolved"));
    block_on(bridge.notify("fixture/done".into(), None, None)).unwrap();
    block_on(bridge.clone().close()).unwrap();
}
