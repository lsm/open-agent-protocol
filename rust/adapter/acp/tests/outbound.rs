use std::sync::{mpsc, Arc};
use std::time::Duration;

use futures::executor::block_on;
use oap_adapter::jsonrpc::Inbound;
use oap_adapter_acp::process::{Process, ProcessConfig};
use oap_adapter_acp::PromptBridge;
use serde_json::json;

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
