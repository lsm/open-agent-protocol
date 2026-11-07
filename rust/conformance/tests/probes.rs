#![cfg(unix)]

use std::process::Command;
use std::time::Duration;

use oap_conformance::{Client, Runner};
use oap_protocol::{CapabilityDescriptor, EnvelopeType};

const ENDPOINT: &str = r#"
import json,sys
mode=sys.argv[1]
for line in sys.stdin:
 request=json.loads(line)
 kind=request['type']
 payload={}
 error=None
 if kind=='capabilities.request':
  payload={'features':{'session.model.switch':{'level':'native'},'session.open.reopen':{'level':'unavailable' if mode.startswith('no-reopen') else 'degraded' if mode=='degraded' else 'native'},'run.cancel':{'level':'unavailable' if mode.startswith('no-cancel') else 'native'}}}
 elif request.get('capability_revision')=='rev-stale':
  error={'code':'wrong' if mode=='wrong-stale' else 'stale_capabilities'}
  if mode=='accepted-stale': error=None
 elif kind=='conformance.not.a.real.request': error={'code':'invalid_envelope'}
 elif kind=='session.open.request':
  assert request['session_id']=='s-never-opened' and request['payload']['session_id']=='s-never-opened'
  assert request['payload']['reopen']
  assert request['payload'].get('allow_degraded_features',[])==(['session.open.reopen'] if mode=='degraded' else [])
  error={'code':'unsupported_feature','details':{'feature':'wrong' if mode=='no-reopen-wrong-feature' else 'session.open.reopen'}} if mode.startswith('no-reopen') else {'code':'wrong' if mode=='wrong-reopen' else 'unknown_session'}
  if mode=='accepted-reopen': error=None
 elif kind=='run.cancel.request':
  assert request['run_id']=='r' and request['payload']['run_id']=='r' and request['payload']['session_id']=='s'
  error={'code':'wrong' if mode=='no-cancel-wrong-code' else 'unsupported_feature'} if mode.startswith('no-cancel') else {'code':'run_already_terminal'}
  if mode in ['accepted-cancel','wrong-cancel','no-cancel-answered']: error=None
 response={'protocol':'open-agent-protocol','type':kind.replace('.request','.response'),'id':'answer-'+request['id'],'in_reply_to':request['id'],'payload':payload,'capability_revision':'rev'}
 if error:
  response['type']='error.response'
  response['payload']=[] if mode=='malformed-refusal' else {'error':error}
 if mode=='wrong-cancel' and kind=='run.cancel.request': response['type']='session.state.response'
 print(json.dumps(response),flush=True)
 if mode=='unrecoverable' and kind=='conformance.not.a.real.request': break
"#;

fn client(mode: &str) -> Client {
    let mut command = Command::new("python3");
    command.args(["-u", "-c", ENDPOINT, mode]);
    Client::spawn(&mut command, Duration::from_secs(2)).unwrap()
}

#[test]
fn typed_refusals_leave_the_endpoint_usable_and_stay_out_of_the_trace() {
    for mode in [
        "normal",
        "degraded",
        "no-reopen",
        "no-cancel",
        "accepted-cancel",
    ] {
        let mut client = client(mode);
        let mut runner = Runner::new(&mut client, "s");
        let descriptor = runner.capabilities().unwrap();
        runner.check_state();
        runner.refuse_stale_revision();
        runner.answer_cancel(&descriptor, Some(&"r".into()));
        runner.refuse_addressable_envelope();
        runner.refuse_unknown_reopen(&descriptor);
        assert_eq!(runner.checks().len(), 8);
        assert!(
            runner.checks().iter().all(|check| check.passed),
            "{mode}: {:?}",
            runner.checks()
        );
        runner.into_checks();
        let trace = client.transcript();
        assert_eq!(trace.len(), 6);
        assert!(trace.iter().all(|envelope| [
            EnvelopeType::CAPABILITIES_REQUEST,
            EnvelopeType::CAPABILITIES_RESPONSE,
            EnvelopeType::SESSION_STATE_REQUEST,
            EnvelopeType::SESSION_STATE_RESPONSE
        ]
        .contains(&envelope.envelope_type)));
        client.close_input();
        assert!(client.wait().unwrap().success());
    }
}

#[test]
fn stale_revision_requires_its_typed_refusal() {
    for mode in ["wrong-stale", "accepted-stale", "malformed-refusal"] {
        let mut client = client(mode);
        let mut runner = Runner::new(&mut client, "s");
        runner.capabilities().unwrap();
        runner.refuse_stale_revision();
        assert!(!runner.checks().last().unwrap().passed, "{mode}");
    }
}

#[test]
fn reopening_requires_the_advertised_support_and_refusal_identity() {
    for mode in [
        "wrong-reopen",
        "accepted-reopen",
        "no-reopen-wrong-feature",
        "malformed-refusal",
    ] {
        let mut client = client(mode);
        let mut runner = Runner::new(&mut client, "s");
        let descriptor = runner.capabilities().unwrap();
        runner.refuse_unknown_reopen(&descriptor);
        assert!(!runner.checks().last().unwrap().passed, "{mode}");
    }
}

#[test]
fn cancellation_checks_the_declared_availability_and_answer_type() {
    for mode in ["wrong-cancel", "no-cancel-wrong-code", "no-cancel-answered"] {
        let mut client = client(mode);
        let mut runner = Runner::new(&mut client, "s");
        let descriptor = runner.capabilities().unwrap();
        runner.answer_cancel(&descriptor, Some(&"r".into()));
        assert!(!runner.checks().last().unwrap().passed, "{mode}");
    }
}

#[test]
fn a_correlated_refusal_is_insufficient_if_the_endpoint_stops_answering() {
    let mut client = client("unrecoverable");
    let mut runner = Runner::new(&mut client, "s");
    runner.capabilities().unwrap();
    runner.refuse_addressable_envelope();
    assert!(!runner.checks().last().unwrap().passed);
    assert!(runner
        .checks()
        .last()
        .unwrap()
        .detail
        .contains("recoverable protocol error"));
}

#[test]
fn absent_revision_and_absent_run_are_explicit_skips_without_sending_probes() {
    let mut client = client("normal");
    let mut runner = Runner::new(&mut client, "s");
    runner.refuse_stale_revision();
    runner.answer_cancel(&CapabilityDescriptor::default(), None);
    assert_eq!(runner.checks().len(), 2);
    assert!(runner
        .checks()
        .iter()
        .all(|check| check.passed && check.skipped && !check.detail.is_empty()));
    runner.into_checks();
    assert!(client.transcript().is_empty());
}
