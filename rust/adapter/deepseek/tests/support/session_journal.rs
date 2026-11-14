use std::time::Duration;

use oxap_adapter::{Error, OpenRequest, ReplayGap, ResumeRequest};
use oxap_protocol::{Envelope, EnvelopeType, Participant, RunId, SessionStatus};
use serde_json::value::RawValue;

use crate::adapter::{Adapter, Config};
use crate::native::{NOTIFY_SESSION_STATUS, SERVER_NAME, SERVER_VERSION};
use crate::process::ProcessConfig;
use crate::rpc::{Inbound, NotificationMessage};

fn envelope(sequence: u64) -> Envelope {
    Envelope {
        envelope_type: EnvelopeType::CONTENT_DELTA,
        run_id: Some(RunId::from("run-1")),
        sequence: Some(sequence),
        ..Envelope::default()
    }
}

fn open(journal_capacity: usize) -> super::Session {
    let info = format!(
        r#"{{"serverInfo":{{"name":"{SERVER_NAME}","version":"{}"}}}}"#,
        *SERVER_VERSION
    );
    let script = format!(
        r#"read line; printf '%s\n' '{{"jsonrpc":"2.0","id":1,"result":{info}}}'
read line; printf '%s\n' '{{"jsonrpc":"2.0","id":2,"result":{{}}}}'
cat >/dev/null"#
    );
    Adapter::new(Config {
        process: ProcessConfig {
            path: "/bin/sh".into(),
            args: vec!["-c".into(), script],
            dir: "/".into(),
            env: Some(vec!["PATH=/usr/bin:/bin".into()]),
            shutdown_timeout: Some(Duration::from_secs(2)),
            ..ProcessConfig::default()
        },
        provider: "deepseek".into(),
        model: "m".into(),
        journal_capacity,
        ..Config::default()
    })
    .unwrap()
    .open(OpenRequest {
        session_id: "s".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    })
    .unwrap()
}

#[test]
fn a_resume_past_the_journal_reports_the_gap_instead_of_an_empty_replay() {
    let session = open(1);
    for sequence in 1..=3 {
        session
            .shared
            .journal
            .append(envelope(sequence), sequence == 3);
    }
    assert_eq!(
        session
            .resume(ResumeRequest {
                run_id: "run-1".into(),
                after_sequence: 0,
            })
            .err(),
        Some(Error::ReplayGap(ReplayGap {
            requested_after: 0,
            oldest_available: 3,
            latest_available: 3,
        }))
    );
    session.close().unwrap();
}

fn notification(method: &str, params: &str) -> Inbound {
    Inbound::Notification(NotificationMessage {
        method: method.into(),
        params: Some(RawValue::from_string(params.into()).unwrap()),
    })
}

#[test]
fn a_status_for_this_session_while_idle_is_not_external_activity() {
    let session = open(1);
    session.shared.reduce(notification(
        NOTIFY_SESSION_STATUS,
        r#"{"sessionId":"s","status":"idle"}"#,
    ));
    assert_eq!(session.state().unwrap().status, SessionStatus::IDLE);
    session.shared.reduce(notification(
        NOTIFY_SESSION_STATUS,
        r#"{"sessionId":"t","status":"idle"}"#,
    ));
    assert_eq!(session.state().err(), Some(Error::SessionClosed));
    session.close().unwrap();
}

#[test]
fn a_barrier_is_acknowledged_without_touching_the_session() {
    let session = open(1);
    let (ack, acked) = crossbeam_channel::bounded(1);
    session.shared.reduce(Inbound::Barrier(ack));
    assert_eq!(
        acked.recv_timeout(Duration::from_secs(5)),
        Err(crossbeam_channel::RecvTimeoutError::Disconnected)
    );
    assert_eq!(session.state().unwrap().status, SessionStatus::IDLE);
    session.close().unwrap();
}
