use std::task::{Context, Poll};

use futures::task::noop_waker;
use oxap_adapter::{Adapter as _, OpenRequest};
use oxap_protocol::Participant;

use super::{Adapter, Config, DESCRIPTORS};
use crate::process::ProcessConfig;

#[test]
fn opening_reuses_the_descriptor_built_with_the_adapter() {
    let adapter = Adapter::new(Config {
        process: ProcessConfig {
            path: "/nonexistent/oap-acp-agent".into(),
            dir: "/".into(),
            ..ProcessConfig::default()
        },
        ..Config::default()
    })
    .unwrap();
    let built = DESCRIPTORS.with(|built| built.get());
    let mut opening = adapter.open(OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    });
    let first = opening
        .as_mut()
        .poll(&mut Context::from_waker(&noop_waker()));
    drop(opening);
    assert!(matches!(first, Poll::Pending | Poll::Ready(Err(_))));
    assert_eq!(DESCRIPTORS.with(|built| built.get()), built);
}
