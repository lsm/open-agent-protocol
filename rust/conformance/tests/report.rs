#![cfg(unix)]

use std::process::Command;
use std::time::{Duration, Instant};

use oap_conformance::{run, Options};
use oap_validation::codes;

const ENDPOINT: &str = r#"
import json,sys
mode=sys.argv[1]
session='custom' if mode=='preferred' else 's'
current='a'
events=[{'protocol':'open-agent-protocol','type':kind,'id':str(i),'session_id':session,'run_id':'r','sequence':i,'payload':{}} for i,kind in enumerate(['run.started','run.completed'],1)]
def emit(value): print(json.dumps(value),flush=True)
for line in sys.stdin:
 try: request=json.loads(line)
 except ValueError: sys.exit(7)
 if 'control' in request:
  emit({'control':'replay.accepted','id':request['id']})
  for event in events: emit(event)
  continue
 if 'id' not in request: sys.exit(7)
 kind=request['type']
 if kind!='session.list.request': assert request['session_id']==(session+'-never-opened' if request['payload'].get('reopen') else session)
 error=None
 follow=[]
 payload={}
 if request.get('capability_revision','').endswith('-stale'): error={'code':'stale_capabilities'}
 elif kind=='protocol.initialize.request': payload={}
 elif kind=='capabilities.request': payload={'features':{'session.model.switch':{'level':'native'},'run.cancel':{'level':'native'}}}
 elif kind=='session.open.request':
  if request['payload'].get('reopen'): error={'code':'unsupported_feature','details':{'feature':'session.open.reopen'}}
  else: payload={'session_id':session,'current_model_id':current}
 elif kind=='session.list.request': error={'code':'unsupported_feature'}
 elif kind=='session.model.switch.request':
  model=request['payload']['model_id']
  if model.startswith('conformance-no-such-model-'): error={'code':'model_not_found','details':{'model_id':model}}
  else:
   payload={'session_id':session,'model_id':model,'previous_model_id':current}
   if model!=current: follow=[{'protocol':'open-agent-protocol','type':'session.state.updated','id':'model-state','session_id':session,'payload':{'session_id':session,'current_model_id':model}}]
   current=model
 elif kind=='session.state.request': payload={'session_id':session,'current_model_id':current}
 elif kind=='session.message.submit.request':
  assert current==('b' if mode=='preferred' else 'a')
  payload={'accepted':True,'run_id':'r','model_id':current,'requested_delivery':'auto','effective_delivery':'start'}
  follow=events
 elif kind=='run.cancel.request': error={'code':'run_already_terminal'}
 else: error={'code':'invalid_envelope'}
 answer={'protocol':'open-agent-protocol','type':kind.replace('.request','.response'),'id':'answer-'+request['id'],'in_reply_to':request['id'],'session_id':session,'capability_revision':'rev','payload':payload}
 if error: answer.update(type='error.response',payload={'error':error})
 emit(answer)
 for event in follow: emit(event)
"#;

fn command(mode: &str) -> Command {
    let mut command = Command::new("python3");
    command.args(["-u", "-c", ENDPOINT, mode]);
    command
}

#[test]
fn a_completed_script_cannot_pass_without_a_validated_transcript() {
    let report = run(
        &mut command("normal"),
        Options {
            session_id: "s".into(),
            line_deadline: Duration::from_secs(2),
            ..Options::default()
        },
    )
    .unwrap();
    assert!(!report.passed);
    assert!(report
        .diagnostics
        .iter()
        .any(|diagnostic| diagnostic.code == codes::SCHEMA_INVALID));
    assert!(report
        .checks
        .iter()
        .any(|check| check.name == "the exchange validates as an OAP trace" && !check.passed));
    assert!(
        report
            .checks
            .iter()
            .filter(|check| check.name != "the exchange validates as an OAP trace")
            .all(|check| check.passed),
        "{:?}",
        report.checks
    );
    assert!(!report.trace.is_empty());
    let value = serde_json::to_value(&report).unwrap();
    assert!(value.get("trace").is_none());
    assert_eq!(value["passed"], false);
    assert!(!value["diagnostics"].as_array().unwrap().is_empty());
}

#[test]
fn explicit_session_and_model_options_reach_the_spawned_endpoint() {
    let report = run(
        &mut command("preferred"),
        Options {
            session_id: "custom".into(),
            model: "b".into(),
            line_deadline: Duration::from_secs(2),
        },
    )
    .unwrap();
    assert!(
        report
            .checks
            .iter()
            .filter(|check| check.name != "the exchange validates as an OAP trace")
            .all(|check| check.passed),
        "{:?}",
        report.checks
    );
    assert!(!report.passed);
}

#[test]
fn spawn_failure_is_an_error_and_a_transport_timeout_is_a_failed_report() {
    assert!(run(
        &mut Command::new("/no-such-conformance-command"),
        Options::default()
    )
    .is_err());
    let mut command = Command::new("python3");
    command.args(["-u", "-c", "import sys; sys.stdin.read()"]);
    let start = Instant::now();
    let report = run(
        &mut command,
        Options {
            line_deadline: Duration::from_millis(100),
            ..Options::default()
        },
    )
    .unwrap();
    assert!(!report.passed);
    assert!(report
        .checks
        .iter()
        .any(|check| !check.passed && check.detail.contains("100ms")));
    assert!(start.elapsed() < Duration::from_secs(3));
}
