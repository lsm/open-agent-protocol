#![cfg(unix)]

use std::process::Command;
use std::time::{Duration, Instant};

use oxap_conformance::{framing_contract, Client, Runner};
use oxap_protocol::{Envelope, EnvelopeType};
use serde_json::json;

const ENDPOINT: &str = r#"
import json,sys,time
mode=sys.argv[1]
events=[{'protocol':'open-agent-protocol','type':kind,'id':str(i),'run_id':'r','sequence':i,'payload':{}} for i,kind in enumerate(['run.started','content.delta','run.completed'],1)]
def emit(value): print(json.dumps(value),flush=True)
for line in sys.stdin:
 request=json.loads(line)
 if 'control' not in request:
  emit({'protocol':'open-agent-protocol','type':'session.message.submit.response','id':'admitted','in_reply_to':request['id'],'payload':{'run_id':'r','accepted':True}})
  if mode!='empty-original':
   for event in events: emit(event)
  continue
 assert request['control']=='replay' and request['session_id']=='s' and request['run_id']=='r' and request['after']==0
 answer={'control':'replay.accepted','id':request['id']}
 if mode=='unsupported': answer.update(control='error',code='unsupported_control')
 if mode=='control-error': answer.update(control='error',code='other')
 if mode=='gap': answer.update(control='replay.gap',oldest_available=8,latest_available=10)
 if mode=='silent': continue
 emit(answer)
 if mode in ['unsupported','control-error','gap']: continue
 replay=[dict(event) for event in events]
 if mode=='wrong-id': replay[1]['id']='different'
 if mode=='wrong-type': replay[1]['type']='action.call.started'
 if mode=='wrong-sequence': replay[1]['sequence']=99
 if mode=='reordered': replay[0],replay[1]=replay[1],replay[0]
 if mode=='missing': replay.pop(1)
 if mode=='extra': replay.insert(1,dict(replay[0]))
 if mode in ['foreign-flood','unsequenced-flood']:
  for i in range(400):
   event=dict(events[-1],id='noise-'+str(i),run_id='foreign' if mode=='foreign-flood' else 'r')
   if mode=='unsequenced-flood': event.pop('sequence')
   emit(event)
   time.sleep(.005)
 else:
  emit(dict(events[-1],id='foreign',run_id='foreign'))
  for event in replay: emit(event)
if mode=='delayed-exit': time.sleep(.75)
"#;

fn seeded(mode: &str) -> Client {
    let mut command = Command::new("python3");
    command.args(["-u", "-c", ENDPOINT, mode]);
    let within = match mode {
        "normal" | "delayed-exit" => Duration::from_secs(2),
        _ => Duration::from_millis(500),
    };
    let mut client = Client::spawn(&mut command, within).unwrap();
    client
        .send(
            Envelope::new(
                EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
                "seed",
                &json!({}),
            )
            .unwrap(),
        )
        .unwrap();
    client.response(&"seed".into()).unwrap();
    if mode != "empty-original" {
        for _ in 0..3 {
            client.event().unwrap();
        }
    }
    client
}

fn exact_replay(mode: &str) {
    let mut client = seeded(mode);
    let before = client.transcript().len();
    let mut runner = Runner::new(&mut client, "s");
    runner.replay_run(&"r".into());
    assert!(runner
        .checks()
        .iter()
        .all(|check| check.passed && !check.skipped));
    runner.into_checks();
    assert_eq!(client.transcript().len(), before + 1);
    assert_eq!(client.event().unwrap().id.as_str(), "foreign");
    client.close_input();
    assert!(client.wait().unwrap().success());
}

#[test]
fn an_exact_replay_passes_without_duplicating_the_collected_trace() {
    exact_replay("normal");
}

#[test]
fn cooperative_replay_endpoint_has_time_to_exit_after_eof() {
    exact_replay("delayed-exit");
}

#[test]
fn only_an_explicit_unsupported_control_is_skipped() {
    let mut client = seeded("unsupported");
    let mut runner = Runner::new(&mut client, "s");
    runner.replay_run(&"r".into());
    let check = runner.checks().last().unwrap();
    assert!(check.passed && check.skipped);
    for mode in ["gap", "control-error", "silent", "empty-original"] {
        let mut client = seeded(mode);
        let mut runner = Runner::new(&mut client, "s");
        runner.replay_run(&"r".into());
        let check = runner.checks().last().unwrap();
        assert!(!check.passed && !check.skipped, "{mode}: {check:?}");
    }
}

#[test]
fn changed_identity_type_sequence_order_and_membership_fail_replay() {
    for mode in [
        "wrong-id",
        "wrong-type",
        "wrong-sequence",
        "reordered",
        "missing",
        "extra",
    ] {
        let mut client = seeded(mode);
        let mut runner = Runner::new(&mut client, "s");
        runner.replay_run(&"r".into());
        assert!(!runner.checks().last().unwrap().passed, "{mode}");
    }
}

#[test]
fn foreign_or_unsequenced_events_cannot_satisfy_or_renew_replay_collection() {
    for mode in ["foreign-flood", "unsequenced-flood"] {
        let mut client = seeded(mode);
        let mut runner = Runner::new(&mut client, "s");
        let start = Instant::now();
        runner.replay_run(&"r".into());
        assert!(!runner.checks().last().unwrap().passed);
        assert!(start.elapsed() >= Duration::from_millis(500));
        assert!(start.elapsed() < Duration::from_secs(2));
    }
}

#[test]
fn malformed_and_unaddressable_lines_must_end_the_endpoint_nonzero() {
    let mut command = Command::new("python3");
    command.args([
        "-u",
        "-c",
        r#"
import json,sys
line=sys.stdin.readline().strip()
if line!='this is not an envelope':
 request=json.loads(line)
 assert request['type']=='capabilities.request' and 'id' not in request
sys.stdin.read()
sys.exit(7)
"#,
    ]);
    assert!(framing_contract(&mut command, Duration::from_secs(2))
        .iter()
        .all(|check| check.passed));
    let mut command = Command::new("python3");
    command.args(["-u", "-c", "import sys; sys.stdin.read()"]);
    assert!(framing_contract(&mut command, Duration::from_secs(2))
        .iter()
        .all(|check| !check.passed));
    let mut command = Command::new("/no-such-conformance-command");
    assert!(framing_contract(&mut command, Duration::from_secs(2))
        .iter()
        .all(|check| !check.passed));
}

#[test]
fn a_chatty_peer_ignoring_fatal_input_is_bounded_and_cleaned_up() {
    let mut command = Command::new("python3");
    command.args(["-u", "-c", r#"
import json,sys,time
sys.stdin.read()
for i in range(1000):
 print(json.dumps({'protocol':'open-agent-protocol','type':'run.started','id':str(i),'payload':{}}),flush=True)
 time.sleep(.005)
"#]);
    let start = Instant::now();
    let checks = framing_contract(&mut command, Duration::from_millis(500));
    assert!(checks.iter().all(|check| !check.passed));
    assert!(start.elapsed() >= Duration::from_secs(1));
    assert!(start.elapsed() < Duration::from_secs(3));
}
