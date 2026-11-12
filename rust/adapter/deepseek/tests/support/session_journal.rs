use std::time::Duration;

use oap_adapter::{Error, OpenRequest, ReplayGap, ResumeRequest};
use oap_protocol::{Envelope, EnvelopeType, Participant, RunId};

use crate::adapter::{Adapter, Config};
use crate::native::{SERVER_NAME, SERVER_VERSION};
use crate::process::ProcessConfig;

fn envelope(sequence: u64) -> Envelope {
    Envelope {
        envelope_type: EnvelopeType::CONTENT_DELTA,
        run_id: Some(RunId::from("run-1")),
        sequence: Some(sequence),
        ..Envelope::default()
    }
}

#[test]
fn a_resume_past_the_journal_reports_the_gap_instead_of_an_empty_replay() {
    let info = format!(
        r#"{{"serverInfo":{{"name":"{SERVER_NAME}","version":"{}"}}}}"#,
        *SERVER_VERSION
    );
    let script = format!(
        r#"read line; printf '%s\n' '{{"jsonrpc":"2.0","id":1,"result":{info}}}'
read line; printf '%s\n' '{{"jsonrpc":"2.0","id":2,"result":{{}}}}'
cat >/dev/null"#
    );
    let session = Adapter::new(Config {
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
        journal_capacity: 1,
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
    .unwrap();
    for sequence in 1..=3 {
        session.journal.append(envelope(sequence), sequence == 3);
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
