import argparse
import copy
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location('peer', Path(__file__).with_name('rust-acp-text.py'))
peer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(peer)
GO = peer.GO.replace('type input struct {', 'type input struct {\n    Resolve bool `json:"resolve"`\n    Granted bool `json:"granted"`')
start = GO.index('\tmarker := "settled"')
end = GO.index('\tvar closeError any', start)
GO = GO[:start] + r'''	events, errors := []protocol.Envelope{}, []string{}
	for event := range stream {
		if event.Error != nil {
			errors = append(errors, event.Error.Error())
			continue
		}
		events = append(events, event.Envelope)
		if event.Envelope.Type == protocol.TypeActionPermissionRequested && in.Resolve {
			var permission protocol.PermissionRequestedPayload
			if err := event.Envelope.DecodePayload(&permission); err != nil { panic(err) }
			resolution := base.InteractionResolution{RunID:permission.RunID, RespondedBy:permission.RespondedBy,
				Permission:&protocol.PermissionResolveRequest{InteractionID:permission.InteractionID,
					RequestedBy:permission.RequestedBy,RespondedBy:permission.RespondedBy,
					SessionID:permission.SessionID,RunID:permission.RunID,ChoiceID:"pick",Granted:in.Granted}}
			if err := session.Resolve(ctx,resolution); err != nil { panic(err) }
		}
	}
	deadline := time.Now().Add(10*time.Second)
	for {
		if _, err := os.Stat(filepath.Join(in.Case,"finished")); err == nil { break }
		if time.Now().After(deadline) { panic("native wire did not finish") }
		time.Sleep(time.Millisecond)
	}
	closed := session.Close(ctx)
''' + GO[end:]
AGENT = '''import json, os, sys
from pathlib import Path
out=Path(sys.argv[1])
scenario=json.loads((out/'scenario.json').read_text())
(out/'pid').write_text(str(os.getpid()))
def read():
    line=sys.stdin.readline()
    with (out/'host.jsonl').open('a') as stream: stream.write(line)
    return json.loads(line)
def send(value):
    value=dict(jsonrpc='2.0',**value)
    with (out/'agent.jsonl').open('a') as stream: stream.write(json.dumps(value)+'\\n')
    print(json.dumps(value),flush=True)
request=read()
send(dict(id=request['id'],result=dict(protocolVersion=1,agentCapabilities={})))
request=read()
send(dict(id=request['id'],result=dict(sessionId='native-session')))
prompt=read()
assert prompt['params']==dict(sessionId='native-session',prompt=[dict(type='text',text='synthetic')]),prompt
for update in scenario.get('updates',[]): send(dict(method='session/update',params=dict(sessionId='native-session',update=update)))
for index,params in enumerate(scenario['permissions']):
    send(dict(id='permission-'+str(index),method='session/request_permission',params=params))
    if scenario.get('resolve') or scenario.get('invalid'):
        reply=read()
        assert reply['id']=='permission-'+str(index),reply
if scenario.get('error'):
    send(dict(id=prompt['id'],error=dict(code=-32000,message='synthetic refusal')))
else:
    send(dict(id=prompt['id'],result=dict(stopReason=scenario.get('stop','end_turn'))))
if not scenario.get('resolve') and not scenario.get('invalid'):
    for index in range(len(scenario['permissions'])):
        reply=read()
        assert reply['id']=='permission-'+str(index),reply
        assert reply['result']['outcome']['outcome']=='cancelled',reply
        if index==0 and scenario.get('trailing'):
            for _ in range(16):
                send(dict(method='session/update',params=dict(sessionId='native-session',update=dict(sessionUpdate='plan',entries=[]))))
(out/'finished').touch()
sys.stdin.read()
'''


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--output-dir',required=True,type=Path)
    parser.add_argument('--consumer',choices=['permission_projection','permission_controller','session_permissions'],default='permission_projection')
    options=parser.parse_args()
    output=options.output_dir.resolve()
    normalization=importlib.util.spec_from_file_location('normalization',Path(__file__).with_name('rust-acp-results.py'))
    normalizer=importlib.util.module_from_spec(normalization)
    normalization.loader.exec_module(normalizer)
    output.mkdir(parents=True,exist_ok=False)
    peer.run(['cargo','build','--manifest-path','rust/Cargo.toml','-p','oxap-adapter-acp','--example',options.consumer],output/'rust-build.log')
    (output/'reference.go').write_text(GO)
    peer.run(['gofmt','-w',str(output/'reference.go')],output/'gofmt.log')
    peer.run(['go','build','-o',str(output/'reference'),str(output/'reference.go')],output/'go-build.log')
    peer.run(['go','build','-o',str(output/'goap'),'./go/cmd/goap'],output/'goap-build.log')
    peer.run(['cargo','build','--manifest-path','rust/Cargo.toml','-p','oxap-validation','--example','validate'],output/'validator-build.log')
    allow=dict(optionId='pick',name='Allow',kind='allow_once')
    deny=dict(optionId='pick',name='Reject',kind='reject_once')
    def permission(identifier='tool',options=None):
        return dict(sessionId='native-session',toolCall=dict(toolCallId=identifier,title='Act',rawInput=dict(path='x')),options=options or [allow])
    scenarios={
        'allow':dict(permissions=[permission()],resolve=True,granted=True),
        'deny':dict(permissions=[permission(options=[deny])],resolve=True,granted=False),
        'parent-order':dict(permissions=[permission('z'),permission('a')]),
        'parent-trailing':dict(permissions=[permission('trailing-'+str(index)) for index in range(8)],trailing=True),
        'parent-refusal':dict(permissions=[permission()],stop='refusal'),
        'parent-rpc-error':dict(permissions=[permission()],error=True),
        'filtered-options':dict(permissions=[permission(options=[dict(optionId='',name='bad',kind='allow_once'),dict(optionId='future',name='Future',kind='new_kind'),allow])],resolve=True,granted=True),
        'duplicate-option':dict(permissions=[permission(options=[allow,deny])],resolve=True,granted=False),
        'wrong-session':dict(permissions=[dict(permission(),sessionId='foreign')],invalid=True),
        'missing-tool-id':dict(permissions=[dict(permission(),toolCall=dict(title='Act'))],invalid=True),
        'invalid-tool-title':dict(permissions=[dict(permission(),toolCall=dict(toolCallId='tool'))],invalid=True),
        'invalid-options':dict(permissions=[permission(options=[dict(optionId='future',name='Future',kind='new_kind')])],invalid=True),
        'malformed-tool':dict(permissions=[dict(permission(),toolCall=dict(toolCallId=7))],invalid=True),
        'terminal-tool':dict(permissions=[permission()],updates=[dict(sessionUpdate='tool_call',toolCallId='tool',title='Act',status='completed')],invalid=True),
    }
    with tempfile.TemporaryDirectory(prefix='oap-permission-projection-') as temporary:
        agent=Path(temporary)/'agent.py'
        agent.write_text(AGENT)
        for name,scenario in scenarios.items():
            case=output/name
            case.mkdir()
            results,wires={},{}
            for language,binary in [('rust',ROOT/'rust/target/debug/examples'/options.consumer),('go',output/'reference')]:
                directory=case/language
                directory.mkdir()
                (directory/'scenario.json').write_text(json.dumps(scenario))
                request=dict(path=sys.executable,args=['-u',str(agent),str(directory)],mode=name,case=str(directory),resolve=scenario.get('resolve',False),granted=scenario.get('granted',True))
                try:
                    results[language]=json.loads(peer.run([str(binary)],directory/'result.json',json.dumps(request)))
                    assert peer.absent(int((directory/'pid').read_text())),language+' left a child alive'
                    wires[language]=[json.loads(line) for line in (directory/'host.jsonl').read_text().splitlines()]
                finally:
                    if (directory/'pid').exists() and not peer.absent(int((directory/'pid').read_text())): os.kill(int((directory/'pid').read_text()),signal.SIGKILL)
            assert wires['rust']==wires['go'],(name,wires)
            if options.consumer=='session_permissions':
                assert normalizer.normalized(results['rust']['events'])==normalizer.normalized(results['go']['events']),(name,results)
            else:
                assert results['rust']['events']==results['go']['events'],(name,results)
            events=results['rust']['events']
            source=results['rust'] if options.consumer=='session_permissions' else results['go']
            trace=peer.trace(copy.deepcopy(events),source['admission'],source['descriptor'])
            if options.consumer=='session_permissions':
                for prefix in trace[2:4]: prefix['capability_revision']=events[0]['capability_revision']
                trace[2]['payload']['allow_degraded_features']=['session.message.submit','run.streaming']
            path=case/'trace.json'
            path.write_text(json.dumps(trace,ensure_ascii=False,indent=2))
            for language,command in [('go',[str(output/'goap'),'validate']),('rust',[str(ROOT/'rust/target/debug/examples/validate')]),('zig',[str(ROOT/'zig/zig-out/bin/oxap'),'validate'])]:
                peer.run(command+[str(path)],case/f'validate-{language}.log')
            comparison='wall-clock/revision-normalized public events' if options.consumer=='session_permissions' else 'exact projected event JSON'
            print(f'PASS {name}: Go/Rust {comparison} and parsed host frames; three validators accept actual Rust events; native children reaped',flush=True)
        observed=peer.run(['cargo','test','--manifest-path','rust/Cargo.toml','-p','oxap-adapter-acp','--test','permission_projection','native_usage_in_the_pending_window','--','--nocapture'],output/'pending-usage.log')
        proof=json.loads(next(line.split('pending-usage-proof ',1)[1] for line in observed.splitlines() if 'pending-usage-proof ' in line))
        (output/'pending-usage.json').write_text(json.dumps(proof,ensure_ascii=False,indent=2))
        assert peer.absent(proof['native_pid']),proof['native_pid']
        assert [frame['method'] for frame in proof['wire'] if 'method' in frame]==['initialize','session/new','session/prompt','fixture/pending','fixture/barrier','session/prompt']
        assert all(frame['params']['prompt']==[{'type':'text','text':'synthetic'}] for frame in proof['wire'] if frame.get('method')=='session/prompt')
        contexts=[run[-1]['extensions']['com.agentclientprotocol.context']['used_tokens'] for run in proof['runs']]
        assert contexts==[100,200],contexts
        trace=[]
        for index,events in enumerate(proof['runs']):
            admission=copy.deepcopy(results['go']['admission'])
            admission.update(run_id=events[0]['run_id'],submission_id=f'pending-submission-{index}',message_ids=[f'pending-input-{index}'])
            assembled=peer.trace(copy.deepcopy(events),admission,results['go']['descriptor'])
            prefix=assembled[2:4]
            prefix[0]['id']=f'pending-submit-{index}'
            prefix[0]['payload']['messages'][0]['id']=f'pending-input-{index}'
            prefix[1].update(id=f'pending-admission-{index}',in_reply_to=prefix[0]['id'])
            trace.extend((assembled[:2] if index==0 else [])+prefix+events)
        path=output/'pending-usage.trace.json'
        path.write_text(json.dumps(trace,ensure_ascii=False,indent=2))
        for language,command in [('go',[str(output/'goap'),'validate']),('rust',[str(ROOT/'rust/target/debug/examples/validate')]),('zig',[str(ROOT/'zig/zig-out/bin/oxap'),'validate'])]:
            peer.run(command+[str(path)],output/f'pending-usage-validate-{language}.log')
        print('PASS pending usage: native barrier proves the update reached a pending projection; first terminal stays100, next run sees200; all three validators accept; child reaped',flush=True)
    print(f'PASS ACP {options.consumer} permission proof. Evidence {output}',flush=True)


if __name__=='__main__':
    main()
