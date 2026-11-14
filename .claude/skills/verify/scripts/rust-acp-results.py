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

RUST = r'''use futures::{executor::block_on, StreamExt};
use oxap_adapter::{Adapter as _, OpenRequest, SubmitRequest};
use oxap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oxap_protocol::{Message, MessageContent, MessageSubmitRequest, Participant};
use serde_json::{json, Value};
use std::io::Read;
use std::path::Path;
use std::time::{Duration, Instant};
fn wait(path: &Path) {
    let limit = Instant::now() + Duration::from_secs(10);
    while !path.exists() && Instant::now() < limit {
        std::thread::sleep(Duration::from_millis(1));
    }
    assert!(path.exists(), "native did not reach {}", path.display());
}
fn main() {
    let mut input = String::new();
    std::io::stdin().read_to_string(&mut input).unwrap();
    let input: Value = serde_json::from_str(&input).unwrap();
    let directory = Path::new(input["case"].as_str().unwrap());
    let adapter = Adapter::new(Config {
        process: ProcessConfig {
            path: input["path"].as_str().unwrap().into(),
            args: serde_json::from_value(input["args"].clone()).unwrap(),
            dir: "/".into(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_millis(300)),
            ..ProcessConfig::default()
        },
        abort_active_on_close: true,
        ..Config::default()
    })
    .unwrap();
    let session = block_on(adapter.open(OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }))
    .unwrap();
    std::fs::write(directory.join("dispatch-ready"), "ready").unwrap();
    wait(&directory.join("ready-0"));
    let mut turns = Vec::new();
    for index in 0..input["turns"].as_u64().unwrap() {
        let (admission, stream) = block_on(session.submit(SubmitRequest {
            request: MessageSubmitRequest {
                session_id: "session".into(),
                messages: vec![Message {
                    id: Some(format!("input-{index}").into()),
                    role: "user".into(),
                    content: MessageContent::from_text("synthetic"),
                    ..Message::default()
                }],
                allow_degraded_features: vec![
                    "session.message.submit".into(),
                    "run.streaming".into(),
                ],
                ..MessageSubmitRequest::default()
            },
            ..SubmitRequest::default()
        }))
        .unwrap();
        let events = block_on(stream.collect::<Vec<_>>())
            .into_iter()
            .collect::<Result<Vec<_>, _>>()
            .unwrap();
        let state = block_on(session.state()).unwrap();
        std::fs::write(directory.join(format!("settled-{index}")), "settled").unwrap();
        wait(&directory.join(format!("ready-{}", index + 1)));
        turns.push(json!({"admission":admission, "events":events, "state":state}));
    }
    block_on(session.close()).unwrap();
    println!(
        "{}",
        json!({"turns":turns, "descriptor":adapter.describe().capabilities, "revision":adapter.describe().capability_revision})
    );
}
'''

GO = r'''package main

import (
	"context"
	"encoding/json"
	"fmt"
	base "github.com/lsm/oxap/go/adapter"
	"github.com/lsm/oxap/go/adapter/acp"
	"github.com/lsm/oxap/go/protocol"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

type input struct {
	Path  string
	Args  []string
	Case  string
	Turns int
}
type clock struct{}

func (clock) Now() time.Time { return time.UnixMilli(1000) }

type ids struct{ n atomic.Uint64 }

func (i *ids) NewID(kind string) string { return fmt.Sprintf("%s-%d", kind, i.n.Add(1)) }
func wait(path string) {
	limit := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(limit) {
			panic("missing native marker " + path)
		}
		time.Sleep(time.Millisecond)
	}
}
func main() {
	var in input
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adapter, err := acp.New(acp.Config{Executable: in.Path, Args: in.Args, Environment: []string{}, WorkingDirectory: "/", QueueCapacity: 1, Clock: clock{}, IDs: &ids{}, AbortActiveOnClose: true, ShutdownTimeout: 300 * time.Millisecond})
	if err != nil {
		panic(err)
	}
	session, err := adapter.Open(ctx, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		panic(err)
	}
	defer session.Close(context.Background())
	if err := os.WriteFile(filepath.Join(in.Case, "dispatch-ready"), []byte("ready"), 0600); err != nil {
		panic(err)
	}
	wait(filepath.Join(in.Case, "ready-0"))
	turns := []any{}
	for index := 0; index < in.Turns; index++ {
		admission, stream, err := session.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Messages: []protocol.Message{{ID: protocol.MessageID(fmt.Sprintf("input-%d", index)), Role: protocol.RoleUser, Content: protocol.TextContent("synthetic")}}}})
		if err != nil {
			panic(err)
		}
		events := []protocol.Envelope{}
		for event := range stream {
			if event.Error != nil {
				panic(event.Error)
			}
			events = append(events, event.Envelope)
		}
		state, err := session.State(ctx)
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(in.Case, fmt.Sprintf("settled-%d", index)), []byte("settled"), 0600); err != nil {
			panic(err)
		}
		wait(filepath.Join(in.Case, fmt.Sprintf("ready-%d", index+1)))
		turns = append(turns, map[string]any{"admission": admission, "events": events, "state": state})
	}
	if err := session.Close(ctx); err != nil {
		panic(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"turns": turns}); err != nil {
		panic(err)
	}
}
'''

AGENT = '''import json, os, sys, time
from pathlib import Path
out=Path(sys.argv[1])
scenario=json.loads((out/'scenario.json').read_text())
(out/'pid').write_text(str(os.getpid()))
def emit(raw):
    with (out/'agent.jsonl').open('a') as stream: stream.write(raw+'\\n')
    print(raw,flush=True)
def send(value): emit(json.dumps(dict(jsonrpc='2.0',**value)))
def read():
    line=sys.stdin.readline()
    with (out/'host.jsonl').open('a') as stream: stream.write(line)
    return json.loads(line)
def wait(name):
    limit=time.monotonic()+10
    while not (out/name).exists() and time.monotonic()<limit: time.sleep(.001)
    assert (out/name).exists(),name
def updates(values):
    for raw in values:
        emit('{'+'"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":'+raw+'}}')
def ready(index, values):
    updates(values)
    send(dict(id='barrier-'+str(index),method='idle-observation',params={}))
    reply=read()
    assert reply['id']=='barrier-'+str(index) and reply['error']['code']==-32601,reply
    (out/('ready-'+str(index))).touch()
request=read()
send(dict(id=request['id'],result=dict(protocolVersion=1,agentCapabilities={})))
request=read()
send(dict(id=request['id'],result=dict(sessionId='native-session')))
wait('dispatch-ready')
ready(0,scenario.get('idle',[]))
for index, turn in enumerate(scenario['turns']):
    request=read()
    assert request['method']=='session/prompt',request
    assert request['params']==dict(sessionId='native-session',prompt=[dict(type='text',text='synthetic')]),request
    updates(turn.get('updates',[]))
    if 'error' in turn:
        send(dict(id=request['id'],error=turn['error']))
    else:
        result='{"stopReason":'+json.dumps(turn.get('stop','end_turn'))
        if 'usage' in turn: result+=',"usage":'+turn['usage']
        emit('{"jsonrpc":"2.0","id":'+json.dumps(request['id'])+',"result":'+result+'}}')
    wait('settled-'+str(index))
    ready(index+1,turn.get('after',[]))
sys.stdin.read()
'''


def chunk(text, message=''):
    return json.dumps({'sessionUpdate':'agent_message_chunk','messageId':message,'content':{'type':'text','text':text}})


def tool_scenarios():
    def call(identifier='tool', status='pending', **fields):
        return json.dumps(dict(sessionUpdate='tool_call', toolCallId=identifier,
                               title='Read', status=status, **fields))

    def patch(identifier='tool', **fields):
        return json.dumps(dict(sessionUpdate='tool_call_update', toolCallId=identifier, **fields))

    def turn(*updates, **fields):
        return dict(updates=list(updates), **fields)

    return {
        'tool-progress': {'turns': [turn(
            call(rawInput={'path': 'x'}, name='read_file'),
            patch(status='in_progress'), patch(title='Read v2', name=7, content=['working']),
            patch(status='completed', name='grep', rawOutput={'ok': True}))]},
        'tool-direct-complete': {'turns': [turn(call(status='completed'))]},
        'tool-direct-fail': {'turns': [turn(call(status='failed', rawOutput={'error': 'native'}))]},
        'tool-unfinished-order': {'turns': [turn(call('z'), call('a'))]},
        'tool-refusal-settles': {'turns': [turn(call(), stop='refusal')]},
        'tool-rpc-error-settles': {'turns': [turn(call(), error={'code': -32000, 'message': 'synthetic'})]},
        'tool-sparse-before-start': {'turns': [turn(call(), patch(title='Read v2'), patch(status='in_progress'), patch(status='completed'))]},
        'tool-repeated-call': {'turns': [turn(call(rawInput={'path': 'x'}), call(status='in_progress'), patch(status='completed'))]},
        'tool-after-terminal': {'turns': [turn(call(status='completed'), patch(status='in_progress'))]},
        'tool-recreated-terminal': {'turns': [turn(call(status='completed'), call())]},
        'tool-reused-across-turns': {'turns': [turn(call(status='completed')), turn(call())]},
        'tool-patch-before-call': {'turns': [turn(patch(status='completed'))]},
        'tool-invalid-call': {'turns': [turn('{"sessionUpdate":"tool_call","toolCallId":7}')]},
        'tool-invalid-patch': {'turns': [turn('{"sessionUpdate":"tool_call_update","toolCallId":7}')]},
        'tool-missing-title': {'turns': [turn('{"sessionUpdate":"tool_call","toolCallId":"tool"}')]},
        'tool-invalid-status': {'turns': [turn(call(status='mystery'))]},
        'tool-null-raw': {'turns': [turn(call(rawInput=None), patch(status='in_progress', rawOutput=None), patch(title='', status='completed'))]},
        'tool-invalid-with-child': {'turns': [turn(call(), '{"sessionUpdate":"tool_call_update","toolCallId":7}')]},
        'tool-raw-values': {'turns': [turn(
            '{"sessionUpdate":"tool_call","toolCallId":"tool","title":"Read","name":"\\ud800","status":"in_progress","rawInput":{"n":18446744073709551615}}',
            '{"sessionUpdate":"tool_call_update","toolCallId":"tool","content":{"n":18446744073709551615,"x":2},"locations":null}',
            '{"sessionUpdate":"tool_call_update","toolCallId":"tool","status":"completed","rawOutput":{"n":18446744073709551615}}')]},
    }


def normalized(value):
    if isinstance(value, dict):
        result={}
        for key,item in value.items():
            if key in ['timestamp_ms','started_at_ms','updated_at_ms']:
                result[key]=1000
            elif key=='capability_revision':
                result[key]='staged-revision'
            else:
                result[key]=normalized(item)
        return result
    if isinstance(value, list):
        return [normalized(item) for item in value]
    return value


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--output-dir',required=True,type=Path)
    parser.add_argument('--suite',choices=['results','tools'],default='results')
    arguments=parser.parse_args()
    output=arguments.output_dir.resolve()
    output.mkdir(parents=True,exist_ok=False)
    source=output/'consumer'
    (source/'src').mkdir(parents=True)
    (source/'src/main.rs').write_text(RUST)
    dependencies='\n'.join(f'{name}={{path={json.dumps(str(ROOT/path))}}}' for name,path in [
        ('oxap-adapter-acp','rust/adapter/acp'),('oxap-adapter','rust/adapter'),('oxap-protocol','rust/protocol')])
    (source/'Cargo.toml').write_text('[package]\nname="acp-result-consumer"\nversion="0.0.0"\nedition="2021"\n[dependencies]\nfutures="0.3"\nserde_json="1"\n'+dependencies+'\n')
    peer.run(['cargo','build','--offline','--manifest-path',str(source/'Cargo.toml'),'--target-dir',str(ROOT/'rust/target')],output/'rust-build.log')
    (output/'reference.go').write_text(GO)
    peer.run(['gofmt','-w',str(output/'reference.go')],output/'gofmt.log')
    peer.run(['go','build','-o',str(output/'reference'),str(output/'reference.go')],output/'go-build.log')
    peer.run(['go','build','-o',str(output/'goap'),'./go/cmd/goap'],output/'goap-build.log')
    peer.run(['cargo','build','--manifest-path','rust/Cargo.toml','-p','oxap-validation','--example','validate'],output/'validator-build.log')
    usage='{"totalTokens":10,"inputTokens":8,"outputTokens":2,"thoughtTokens":1,"cachedReadTokens":4,"cachedWriteTokens":0}'
    context='{"sessionUpdate":"usage_update","used":0,"size":1000,"cost":{"amount":1.0,"currency":"USD"}}'
    scenarios={
        'two-turn-scope':{'idle':[context],'turns':[{'usage':usage,'updates':[chunk('Hello 世界')]},{'usage':usage,'updates':[chunk('next')]}]},
        'first-error':{'idle':[context],'turns':[{'error':{'code':-32000,'message':'synthetic refusal'}},{'usage':usage}]},
        'context-replacement':{'idle':[context],'turns':[{'updates':['{"sessionUpdate":"usage_update","used":200,"size":1000,"cost":{"amount":"bad","currency":"USD"}}','{"sessionUpdate":"usage_update","used":-1,"size":1000}'],'after':['{"sessionUpdate":"usage_update","used":300,"size":1000}']},{'usage':usage}]},
        'max-counts':{'turns':[{'usage':'{"totalTokens":18446744073709551615,"inputTokens":18446744073709551615,"outputTokens":0,"thoughtTokens":0,"cachedReadTokens":0}'}]},
        'case-fold-last-wins':{'idle':['{"sessionUpdate":"usage_update","Used":3,"USED":0,"\\u017fize":1000,"COST":{"Amount":-0.0,"CURRENCY":"<\\u0026\\u2028"}}'],'turns':[{'usage':'{"totalTokens":10,"TOTALTOKENS":12,"inputTokens":10,"outputTokens":2,"thoughtTo\\u212aens":1}'}]},
        'bad-optional':{'turns':[{'usage':'{"totalTokens":12,"inputTokens":10,"outputTokens":2,"thoughtTokens":-4,"cachedWriteTokens":3}'}]},
        'bad-cost-overflow':{'idle':['{"sessionUpdate":"usage_update","used":0,"size":1000,"cost":{"amount":1e1000,"currency":"USD"}}'],'turns':[{}]},
        'cost-unpaired-surrogate':{'idle':['{"sessionUpdate":"usage_update","used":0,"size":1000,"cost":{"amount":1,"currency":"\\ud800"}}'],'turns':[{}]},
        'bad-first-count':{'turns':[{'usage':'{"totalTokens":10,"inputTokens":-0,"outputTokens":10}'},{'usage':usage}]},
        'last-null-count':{'turns':[{'usage':'{"totalTokens":10,"TOTALTOKENS":null,"inputTokens":8,"outputTokens":2}'}]},
        'fractional-count':{'turns':[{'usage':'{"totalTokens":10,"inputTokens":1.0,"outputTokens":9}'}]},
        'exponent-count':{'turns':[{'usage':'{"totalTokens":10,"inputTokens":1e0,"outputTokens":9}'}]},
        'multi-message':{'turns':[{'usage':usage,'updates':[chunk('prefix'),chunk('one','one'),chunk('two','two'),chunk('again','one'),chunk('tail')]}]},
        'refusal':{'idle':[context],'turns':[{'stop':'refusal','usage':usage}]},
        'unknown-stop':{'idle':[context],'turns':[{'stop':'future\nstop','usage':usage}]},
    }
    if arguments.suite=='tools':
        scenarios=tool_scenarios()
    with tempfile.TemporaryDirectory(prefix='oap-result-agent-') as temporary:
        agent=Path(temporary)/'agent.py'
        agent.write_text(AGENT)
        for name, scenario in scenarios.items():
            case=output/name
            case.mkdir()
            results,wires={},{}
            for language,binary in [('rust',ROOT/'rust/target/debug/acp-result-consumer'),('go',output/'reference')]:
                directory=case/language
                directory.mkdir()
                (directory/'scenario.json').write_text(json.dumps(scenario))
                request={'path':sys.executable,'args':['-u',str(agent),str(directory)],'case':str(directory),'turns':len(scenario['turns'])}
                try:
                    results[language]=json.loads(peer.run([str(binary)],directory/'result.json',json.dumps(request)))
                    assert peer.absent(int((directory/'pid').read_text())), language+' left a child alive'
                    wires[language]=[json.loads(line) for line in (directory/'host.jsonl').read_text().splitlines()]
                finally:
                    if (directory/'pid').exists() and not peer.absent(int((directory/'pid').read_text())):
                        os.kill(int((directory/'pid').read_text()),signal.SIGKILL)
            assert wires['rust']==wires['go'],(name,wires)
            assert normalized(results['rust']['turns'])==normalized(results['go']['turns']),(name,results)
            turns=results['rust']['turns']
            assert all(turn['state']['status']=='idle' for turn in turns),turns
            assert all('usage' not in turn['events'][-1]['payload'] for turn in turns[1:]),turns
            if name=='context-replacement':
                contexts=[turn['events'][-1]['extensions']['com.agentclientprotocol.context'] for turn in turns]
                assert contexts==[{'used_tokens':200,'size_tokens':1000},{'used_tokens':300,'size_tokens':1000}],contexts
            if name=='multi-message':
                assert turns[0]['events'][-1]['payload']['final_response']['content']=='prefixoneagaintail'
            if name=='max-counts':
                assert turns[0]['events'][-1]['payload']['usage']['input_tokens']==18446744073709551615
            common={'protocol':'open-agent-protocol','version':'0.1','profile':'open-agent-protocol.agent-control-core'}
            trace=[dict(common,id='cap-request',type='capabilities.request',payload={}),
                   dict(common,id='cap-response',type='capabilities.response',in_reply_to='cap-request',
                        capability_revision=results['rust']['revision'],payload=results['rust']['descriptor'])]
            for index,turn in enumerate(results['rust']['turns']):
                prefix=copy.deepcopy(json.loads((ROOT/'fixtures/valid/core-completed.json').read_text())[:2])
                prefix[0].update(id=f'submit-{index}',session_id='session',capability_revision=results['rust']['revision'])
                prefix[0]['payload']={'session_id':'session','delivery':'auto','messages':[{'id':f'input-{index}','role':'user','content':'synthetic'}],'allow_degraded_features':['session.message.submit','run.streaming']}
                prefix[1].update(id=f'accepted-{index}',in_reply_to=f'submit-{index}',session_id='session',capability_revision=results['rust']['revision'])
                prefix[1]['payload']=turn['admission']
                trace.extend(prefix+turn['events'])
            path=case/'trace.json'
            path.write_text(json.dumps(trace,ensure_ascii=False,indent=2))
            for language,command in [('go',[str(output/'goap'),'validate']),('rust',[str(ROOT/'rust/target/debug/examples/validate')]),('zig',[str(ROOT/'zig/zig-out/bin/oxap'),'validate'])]:
                peer.run(command+[str(path)],case/f'validate-{language}.log')
            print(f'PASS {name}: public Go/Rust results and native frames agree, three validators accept Rust trace, children reaped',flush=True)
    print(f'PASS public {arguments.suite} mapping. Comparison normalizes wall-clock timestamps and the declared staged capability revision. Raw outputs retained at {output}',flush=True)


if __name__=='__main__':
    main()
