#![cfg(unix)]

use std::process::Command;
use std::time::{Duration, Instant};

use oxap_conformance::{Client, ClientError, Runner};
use oxap_protocol::EnvelopeType;

const ENDPOINT: &str = r#"
import json,sys
mode=sys.argv[1]
sequence=0
def emit(kind,payload,reply='',run='r'):
 global sequence
 envelope={'protocol':'open-agent-protocol','type':kind,'id':kind+'-'+str(sequence),'session_id':'s','payload':payload}
 if reply: envelope['in_reply_to']=reply
 else:
  sequence+=1
  envelope['run_id']=run
  envelope['sequence']=1 if mode=='duplicate-sequence' else sequence
 print(json.dumps(envelope),flush=True)
def gate(kind):
 payload={'interaction_id':kind,'requested_by':'agent','responded_by':'human','session_id':'s','run_id':'r'}
 if kind=='permission':
  payload['choices']=[] if mode=='no-choices' else [{'id':'allow'},{'id':'deny'}]
  emit('action.permission.requested',[] if mode=='bad-permission' else payload)
 else:
  payload['questions']=[{'id':'text'},{'id':'choose','options':[{'id':'first'},{'id':'second'}]}]
  emit('user.input.requested',[] if mode=='bad-input' else payload)
for line in sys.stdin:
 request=json.loads(line)
 kind=request['type']
 payload=request['payload']
 if kind=='session.message.submit.request':
  assert payload['session_id']=='s' and payload['delivery']=='auto'
  assert payload['messages']==[{'role':'user','content':'drive one scripted run'}]
  assert payload.get('model_id')==('b' if mode=='explicit-model' else None)
  emit('run.started',{})
  admission={'accepted':mode!='rejected','run_id':'r','model_id':'wrong' if mode=='wrong-model' else 'b','requested_delivery':'queue' if mode=='wrong-requested' else 'auto','effective_delivery':'auto' if mode=='unresolved' else 'start'}
  if mode=='missing-run': admission.pop('run_id')
  emit('session.message.submit.response',[] if mode=='bad-admission' else admission,request['id'])
  if mode=='foreign-terminal': emit('run.completed',{},run='foreign')
  gate('permission')
 elif kind in ['action.permission.resolve.request','user.input.resolve.request']:
  assert request['session_id']=='s' and request['run_id']=='r'
  assert payload['session_id']=='s' and payload['run_id']=='r'
  assert payload['requested_by']=='agent' and payload['responded_by']=='human'
  if kind.startswith('action.'):
   assert payload['interaction_id']=='permission' and payload['choice_id']=='allow' and payload['granted']
  else:
   assert payload['interaction_id']=='input'
   assert payload['answers']==[{'question_id':'text','text':'conformance'},{'question_id':'choose','selected_option_ids':['first']}]
  if mode=='refused-gate':
   emit('error.response',{'error':{'code':'gate_refused','message':'synthetic refusal'}},request['id'])
   continue
  emit(kind.replace('.request','.response'),{'accepted':True},request['id'])
  if kind.startswith('action.'): gate('input')
  else: emit('run.'+('failed' if mode=='failed' else 'cancelled' if mode=='cancelled' else 'completed'),{})
"#;

fn client(mode: &str) -> Client {
    let mut command = Command::new("python3");
    command.args(["-u", "-c", ENDPOINT, mode]);
    Client::spawn(&mut command, Duration::from_secs(2)).unwrap()
}

#[test]
fn an_admitted_run_resolves_streamed_gates_and_reaches_its_own_terminal() {
    for mode in ["normal", "foreign-terminal", "failed", "cancelled"] {
        let mut client = client(mode);
        let mut runner = Runner::new(&mut client, "s");
        let run = runner.submit_run("b", true).unwrap();
        assert_eq!(run.as_str(), "r");
        assert!(runner.consume_run(&run));
        assert_eq!(runner.checks().len(), 7);
        assert!(runner.checks().iter().all(|check| check.passed));
        let checks = runner.into_checks();
        let terminal = match mode {
            "failed" => "run.failed",
            "cancelled" => "run.cancelled",
            _ => "run.completed",
        };
        assert_eq!(checks.last().unwrap().detail, format!("settled {terminal}"));
        let trace = client.transcript();
        assert_eq!(
            trace[0].envelope_type,
            EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST
        );
        assert_eq!(
            trace[1].envelope_type,
            EnvelopeType::SESSION_MESSAGE_SUBMIT_RESPONSE
        );
        assert_eq!(trace[2].envelope_type, EnvelopeType::RUN_STARTED);
        assert_eq!(
            trace
                .iter()
                .filter(|e| e.envelope_type == EnvelopeType::USER_INPUT_RESOLVE_REQUEST)
                .count(),
            1
        );
        client.close_input();
        assert!(client.wait().unwrap().success());
    }
}

#[test]
fn a_model_is_explicit_only_when_the_session_switch_did_not_apply() {
    let mut client = client("explicit-model");
    let mut runner = Runner::new(&mut client, "s");
    let run = runner.submit_run("b", false).unwrap();
    assert!(runner.consume_run(&run));
    assert!(runner.checks().iter().all(|check| check.passed));
    assert!(!runner
        .checks()
        .iter()
        .any(|check| check.name == "the switched model is used by the next run"));
}

#[test]
fn rejected_or_unaddressable_admissions_stop_and_wrong_model_or_delivery_facts_fail() {
    for mode in ["rejected", "missing-run", "bad-admission"] {
        let mut client = client(mode);
        let mut runner = Runner::new(&mut client, "s");
        assert!(runner.submit_run("b", true).is_none(), "{mode}");
        assert!(!runner.checks().last().unwrap().passed, "{mode}");
    }
    for mode in ["wrong-model", "wrong-requested", "unresolved"] {
        let mut client = client(mode);
        let mut runner = Runner::new(&mut client, "s");
        assert!(runner.submit_run("b", true).is_some());
        assert!(runner.checks().iter().any(|check| !check.passed), "{mode}");
    }
}

#[test]
fn malformed_or_refused_gates_and_nonadvancing_sequences_fail_the_run() {
    for mode in [
        "no-choices",
        "bad-permission",
        "bad-input",
        "refused-gate",
        "duplicate-sequence",
    ] {
        let mut client = client(mode);
        let mut runner = Runner::new(&mut client, "s");
        let run = runner.submit_run("b", true).unwrap();
        assert!(!runner.consume_run(&run), "{mode}");
        assert!(!runner.checks().last().unwrap().passed, "{mode}");
        assert!(!runner
            .checks()
            .iter()
            .any(|check| check.name == "the run reaches a terminal event" && check.passed));
    }
}

#[test]
fn unrelated_run_events_cannot_renew_the_requested_run_budget() {
    let mut command = Command::new("python3");
    command.args(["-u", "-c", r#"
import json,time
for i in range(1000):
 print(json.dumps({'protocol':'open-agent-protocol','type':'run.completed','id':str(i),'run_id':'other','payload':{}}),flush=True)
 time.sleep(.005)
"#]);
    let mut client = Client::spawn(&mut command, Duration::from_millis(500)).unwrap();
    let start = Instant::now();
    assert!(matches!(
        client.event_for(&"wanted".into()),
        Err(ClientError::Timeout { .. })
    ));
    assert!(start.elapsed() >= Duration::from_millis(500));
    assert!(start.elapsed() < Duration::from_secs(2));
    assert!(client.closed_by_runner().is_some());
}

#[test]
fn selecting_a_run_preserves_already_received_events_for_other_consumers() {
    let mut command = Command::new("python3");
    command.args(["-u", "-c", r#"
import json,sys
for run in ['other','wanted']:
 print(json.dumps({'protocol':'open-agent-protocol','type':'run.completed','id':run,'run_id':run,'payload':{}}),flush=True)
sys.stdin.read()
"#]);
    let mut client = Client::spawn(&mut command, Duration::from_secs(2)).unwrap();
    assert_eq!(
        client.event_for(&"wanted".into()).unwrap().id.as_str(),
        "wanted"
    );
    assert_eq!(client.event().unwrap().id.as_str(), "other");
    client.close_input();
    assert!(client.wait().unwrap().success());
}
