use std::process::Command;
use std::time::Duration;

use oxap_conformance::Client;

const ENDPOINT: &str = r#"
import json,sys
mode=sys.argv[1]
current='a'
for line in sys.stdin:
 request=json.loads(line)
 kind=request['type']
 error=None
 event=None
 if kind=='protocol.initialize.request':
  payload={'protocol_version':'0.1','profile':'open-agent-protocol.agent-control-core','endpoint':{}}
  if mode=='malformed-init': payload=[]
  if mode=='private-array': payload=['synthetic-private-sentinel']
 elif kind=='capabilities.request':
  level='unavailable' if mode=='model-unavailable' else 'native'
  payload={'features':{'session.model.switch':{'level':level},'models.list':{'level':'unavailable' if mode=='no-models' else 'native'},'session.list':{'level':'unavailable' if mode=='no-list' else 'native'}}}
  if mode=='malformed-capabilities': payload=[]
 elif kind=='session.open.request':
  payload={'session_id':request['payload']['session_id'],'current_model_id':current}
  if mode=='wrong-session': payload['session_id']='other'
  if mode=='malformed-open': payload=[]
  if mode=='refused-open' or request.get('capability_revision')!='rev': error={'code':'fixture_refusal','message':'synthetic refusal'}
 elif kind=='models.request':
  payload={'session_id':'s','models':[{'id':'a','default':True},{'id':'b'}]}
  if mode=='bad-catalog': payload=[]
 elif kind=='session.list.request':
  payload={'sessions':[] if mode=='empty-list' else [{'session_id':'s','state':'live'}]}
  if mode=='no-list': error={'code':'unsupported_feature','details':{'feature':'session.list'}}
  if 'session_id' in request: error={'code':'invalid_envelope'}
 elif kind=='session.model.switch.request':
  model=request['payload']['model_id']
  if model.startswith('conformance-no-such-model-'):
   error={'code':'model_not_found','details':{'model_id':'other' if mode=='wrong-missing' else model}}
   payload={}
  else:
   previous=current
   current=model
   payload={'session_id':'s','model_id':model,'previous_model_id':'other' if mode=='wrong-previous' else previous}
   if model!=previous:
    event={'protocol':'open-agent-protocol','type':'session.state.updated','id':'state-'+request['id'],'session_id':'s','payload':{'session_id':'s','current_model_id':'other' if mode=='wrong-event' else model}}
 elif kind=='session.state.request':
  payload={'session_id':'s','current_model_id':'other' if mode=='wrong-state' else current}
 else:
  payload={}
 response={'protocol':'open-agent-protocol','version':'0.1','profile':'open-agent-protocol.agent-control-core','type':kind.replace('.request','.response'),'id':'answer-'+request['id'],'in_reply_to':request['id'],'payload':payload,'capability_revision':'rev'}
 if kind=='capabilities.request' and mode=='missing-revision': response.pop('capability_revision')
 if error:
  response['type']='error.response'
  response['payload']={'error':error}
 print(json.dumps(response),flush=True)
 if event: print(json.dumps(event),flush=True)
"#;

pub fn client(mode: &str) -> Client {
    let mut command = Command::new("python3");
    command.args(["-u", "-c", ENDPOINT, mode]);
    Client::spawn(&mut command, Duration::from_secs(2)).unwrap()
}
