#![cfg(unix)]

use std::process::Command;
use std::time::Duration;

use oap_conformance::{Client, Runner};

const ENDPOINT: &str = r#"
import json,sys
mode=sys.argv[1]
for line in sys.stdin:
 request=json.loads(line)
 kind=request['type']
 if kind=='protocol.initialize.request':
  payload={'protocol_version':'0.1','profile':'open-agent-protocol.agent-control-core','endpoint':{}}
  if mode=='malformed-init': payload=[]
  if mode=='private-array': payload=['synthetic-private-sentinel']
 elif kind=='capabilities.request':
  level='unavailable' if mode=='model-unavailable' else 'native'
  payload={'features':{'session.model.switch':{'level':level}}}
  if mode=='malformed-capabilities': payload=[]
 else:
  payload={'session_id':request['payload']['session_id']}
  if mode=='wrong-session': payload['session_id']='other'
  if mode=='malformed-open': payload=[]
 response={'protocol':'open-agent-protocol','version':'0.1','profile':'open-agent-protocol.agent-control-core','type':kind.replace('.request','.response'),'id':'answer-'+request['id'],'in_reply_to':request['id'],'payload':payload,'capability_revision':'rev'}
 if kind=='capabilities.request' and mode=='missing-revision': response.pop('capability_revision')
 if kind=='session.open.request' and (mode=='refused-open' or request.get('capability_revision')!='rev'):
  response['type']='error.response'
  response['payload']={'error':{'code':'fixture_refusal','message':'synthetic refusal'}}
 print(json.dumps(response),flush=True)
"#;

fn client(mode: &str) -> Client {
    let mut command = Command::new("python3");
    command.args(["-u", "-c", ENDPOINT, mode]);
    Client::spawn(&mut command, Duration::from_secs(2)).unwrap()
}

#[test]
fn bootstrap_checks_bind_open_to_the_discovered_revision_and_requested_session() {
    let mut client = client("normal");
    let mut runner = Runner::new(&mut client, "s");
    runner.initialize();
    assert!(runner.capabilities().is_some());
    assert_eq!(runner.open().unwrap().session_id.as_str(), "s");
    assert_eq!(runner.checks().len(), 6);
    assert!(runner.checks().iter().all(|check| check.passed));
    let checks = runner.into_checks();
    let value = serde_json::to_value(&checks).unwrap();
    assert_eq!(value[0]["name"], "protocol.initialize.request is answered");
    assert!(value[0].get("detail").is_none());
    assert!(value[0].get("skipped").is_none());
    let trace = client.transcript();
    assert_eq!(trace[0].id.as_str(), "conformance-request-1");
    assert_eq!(trace[2].id.as_str(), "conformance-request-2");
    assert_eq!(trace[4].id.as_str(), "conformance-request-3");
    assert_eq!(trace[4].capability_revision.as_deref(), Some("rev"));
    assert_eq!(trace[4].session_id.as_ref().unwrap().as_str(), "s");
    client.close_input();
    assert!(client.wait().unwrap().success());
}

#[test]
fn a_fault_is_reported_under_the_obligation_that_failed() {
    for (mode, name, detail) in [
        ("malformed-init", "protocol.initialize.response decodes", ""),
        ("private-array", "protocol.initialize.response decodes", ""),
        (
            "malformed-capabilities",
            "capabilities.response decodes as a descriptor",
            "",
        ),
        (
            "missing-revision",
            "capabilities.response carries a capability revision",
            "no capability_revision",
        ),
        (
            "model-unavailable",
            "core session.model.switch is advertised",
            "did not advertise",
        ),
        (
            "wrong-session",
            "the open names the session it was asked for",
            "other",
        ),
        (
            "refused-open",
            "session.open.request is answered",
            "fixture_refusal",
        ),
        (
            "malformed-open",
            "session.open.response decodes as a session state",
            "",
        ),
    ] {
        let mut client = client(mode);
        let mut runner = Runner::new(&mut client, "s");
        runner.initialize();
        if runner.capabilities().is_some() {
            runner.open();
        }
        assert!(
            runner
                .checks()
                .iter()
                .any(|check| !check.passed && check.name == name && check.detail.contains(detail)),
            "{mode}: {:?}",
            runner.checks()
        );
        assert!(runner
            .checks()
            .iter()
            .all(|check| !check.detail.contains("synthetic-private-sentinel")));
    }
}

#[test]
fn an_empty_session_selects_the_default_session_name() {
    let mut client = client("normal");
    let mut runner = Runner::new(&mut client, "");
    runner.initialize();
    runner.capabilities().unwrap();
    assert_eq!(runner.open().unwrap().session_id.as_str(), "conformance");
}
