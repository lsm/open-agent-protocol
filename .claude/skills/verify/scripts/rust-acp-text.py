import argparse
import copy
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[4]
RUST = r'''use std::io::Read;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use futures::{executor::block_on, StreamExt};
use oap_adapter::jsonrpc::Inbound;
use oap_adapter_acp::{native::*, pin, process::*, TextRun, TextRunContext};
use serde_json::{json, Value};

fn wait(path: &std::path::Path) {
    let limit = Instant::now() + Duration::from_secs(10);
    while !path.exists() && Instant::now() < limit {
        std::thread::sleep(Duration::from_millis(1));
    }
    assert!(path.exists());
}

fn main() {
    let mut input = String::new();
    std::io::stdin().read_to_string(&mut input).unwrap();
    let input: Value = serde_json::from_str(&input).unwrap();
    let directory = std::path::Path::new(input["case"].as_str().unwrap());
    let process = block_on(Process::start_async(
        ProcessConfig {
            path: input["path"].as_str().unwrap().into(),
            args: serde_json::from_value(input["args"].clone()).unwrap(),
            dir: "/".into(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_millis(300)),
            client_info: Some(Implementation {
                name: "open-agent-protocol".into(),
                version: oap_protocol::VERSION.into(),
                ..Implementation::default()
            }),
            ..ProcessConfig::default()
        },
        None,
    ))
    .unwrap();
    let done = process.client.done();
    let (admit, admitted) = crossbeam_channel::bounded::<()>(0);
    let slot: Arc<Mutex<Option<Arc<Mutex<TextRun>>>>> = Arc::new(Mutex::new(None));
    let receiving = slot.clone();
    let bridge = Arc::new(oap_adapter_acp::PromptBridge::new(
        process,
        move |message| match message {
            Inbound::Notification(notification) => {
                let run = receiving.lock().unwrap().clone();
                if let Some(run) = run {
                    crossbeam_channel::select! { recv(admitted) -> _ => {}, recv(done) -> _ => return }
                    run.lock().unwrap().observe(notification, 1000).unwrap();
                }
            }
            Inbound::Request(request) => {
                request
                    .respond_error(-32601, "method not supported")
                    .unwrap();
            }
            Inbound::Barrier(_) => unreachable!(),
        },
    ));
    let opened = block_on(
        bridge.call(
            METHOD_SESSION_NEW.into(),
            Some(
                serde_json::value::to_raw_value(&SessionNewParams {
                    cwd: "/".into(),
                    mcp_servers: Vec::new(),
                })
                .unwrap(),
            ),
            None,
        ),
    )
    .unwrap();
    let opened: SessionNewResult = decode(opened.get().as_bytes()).unwrap();
    let ids = Arc::new(AtomicU64::new(2));
    let next = ids.clone();
    let run = Arc::new(Mutex::new(TextRun::new(
        TextRunContext {
            session_id: "session".into(),
            native_id: opened.session_id.clone(),
            run_id: "run-1".into(),
            message_id: "session/message-2".into(),
            capability_revision: Some(pin().capability_revision.clone()),
        },
        move |kind| format!("{kind}-{}", next.fetch_add(1, Ordering::SeqCst) + 1),
    )));
    *slot.lock().unwrap() = Some(run.clone());
    let mut call = bridge.prompt(
        PromptParams {
            session_id: opened.session_id,
            prompt: vec![ContentBlock {
                kind: "text".into(),
                text: "synthetic".into(),
                ..ContentBlock::default()
            }],
        },
        None,
    );
    block_on(call.admitted()).unwrap();
    let stream = run.lock().unwrap().start(1000).unwrap();
    ids.fetch_add(1, Ordering::SeqCst);
    drop(admit);
    let closed;
    if input["mode"] == "abandon" {
        wait(&directory.join("ready"));
        let limit = Instant::now() + Duration::from_secs(10);
        while run.lock().unwrap().state().sequence < 2 && Instant::now() < limit {
            std::thread::sleep(Duration::from_millis(1));
        }
        assert_eq!(run.lock().unwrap().state().sequence, 2);
        run.lock().unwrap().close();
        closed = block_on(bridge.clone().close());
        drop(call);
    } else {
        match block_on(call) {
            Ok(result) => run.lock().unwrap().complete(result, 1000).unwrap(),
            Err(error) => run
                .lock()
                .unwrap()
                .prompt_error(
                    &error.to_string(),
                    error.remote_code().is_none(),
                    1000,
                )
                .unwrap(),
        }
        closed = block_on(bridge.clone().close());
    }
    let mut events = Vec::new();
    let mut errors = Vec::new();
    for event in block_on(stream.collect::<Vec<_>>()) {
        match event {
            Ok(event) => events.push(event),
            Err(error) => errors.push(error.to_string()),
        }
    }
    println!(
        "{}",
        json!({"events":events, "errors":errors, "state":run.lock().unwrap().state(), "closeError":closed.err().map(|error| error.to_string())})
    );
}
'''
GO = r'''package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type input struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
	Case string   `json:"case"`
	Mode string   `json:"mode"`
}
type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.UnixMilli(1000) }

type counter struct{ n atomic.Uint64 }

func (c *counter) NewID(kind string) string { return fmt.Sprintf("%s-%d", kind, c.n.Add(1)) }
func main() {
	var in input
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adapter, err := acp.New(acp.Config{Executable: in.Path, Args: in.Args, Environment: []string{}, WorkingDirectory: "/", QueueCapacity: 1, Clock: fixedClock{}, IDs: &counter{}, AbortActiveOnClose: true, ShutdownTimeout: 300 * time.Millisecond})
	if err != nil {
		panic(err)
	}
	session, err := adapter.Open(ctx, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		panic(err)
	}
	defer session.Close(context.Background())
	admission, stream, err := session.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Messages: []protocol.Message{{ID: "input-message", Role: protocol.RoleUser, Content: protocol.TextContent("synthetic")}}}})
	if err != nil {
		panic(err)
	}
	marker := "settled"
	if in.Mode == "abandon" {
		marker = "ready"
	}
	limit := time.Now().Add(10 * time.Second)
	for {
		state, _ := session.State(ctx)
		_, marked := os.Stat(filepath.Join(in.Case, marker))
		ready := state.Status == protocol.SessionIdle
		if in.Mode == "abandon" {
			ready = state.TranscriptCursor == "2"
		}
		if marked == nil && ready {
			break
		}
		if time.Now().After(limit) {
			panic("native run never reached its settlement boundary")
		}
		time.Sleep(time.Millisecond)
	}
	closed := session.Close(ctx)
	events, errors := []protocol.Envelope{}, []string{}
	for event := range stream {
		if event.Error != nil {
			errors = append(errors, event.Error.Error())
		} else {
			events = append(events, event.Envelope)
		}
	}
	var closeError any
	if closed != nil {
		closeError = closed.Error()
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"events": events, "errors": errors, "admission": admission, "closeError": closeError}); err != nil {
		panic(err)
	}
}
'''
AGENT = '''import json, os, sys, time
from pathlib import Path
mode, directory = sys.argv[1:]
out=Path(directory)
(out/'pid').write_text(str(os.getpid()))
def read():
    line=sys.stdin.readline()
    with (out/'wire.jsonl').open('a') as stream: stream.write(line)
    return json.loads(line)
def send(value):
    print(json.dumps(dict(jsonrpc='2.0',**value)),flush=True)
first=read()
send(dict(id=first['id'],result=dict(protocolVersion=1,agentCapabilities={})))
second=read()
send(dict(id=second['id'],result=dict(sessionId='native-session')))
request=read()
def update(value, session='native-session'):
    send(dict(method='session/update',params=dict(sessionId=session,update=value)))
def text(value, message='native-message'):
    update(dict(sessionUpdate='agent_message_chunk',messageId=message,content=dict(type='text',text=value)))
if mode == 'malformed':
    send(dict(method='session/update',params=[]))
elif mode == 'bad-config':
    update(dict(sessionUpdate='config_option_update',configOptions='malformed'))
elif mode == 'foreign':
    update(dict(sessionUpdate='agent_message_chunk',content=dict(type='text',text='foreign')), 'other')
elif mode == 'unknown-update':
    update(dict(sessionUpdate='new_stable'))
elif mode == 'bad-chunk':
    update(dict(sessionUpdate='agent_message_chunk',content=dict(type='image')))
elif mode == 'overflow':
    for index in range(100): text('x')
elif mode == 'empty':
    text('')
elif mode == 'multi-message':
    text('one','first')
    text('two','second')
elif mode == 'tool':
    update(dict(sessionUpdate='tool_call',toolCallId='native-tool',title='Tool',kind='execute',status='completed'))
else:
    text('Hello')
    if mode != 'abandon': text(' 世界')
if mode == 'abandon':
    (out/'ready').touch()
    time.sleep(60)
elif mode == 'remote-error':
    send(dict(id=request['id'],error=dict(code=-32000,message='synthetic refusal')))
else:
    stop={'max-tokens':'max_tokens','max-requests':'max_turn_requests','refusal':'refusal','unknown-stop':'future\\nstop','cancelled':'cancelled'}.get(mode,'end_turn')
    send(dict(id=request['id'],result=dict(stopReason=stop)))
(out/'settled').touch()
sys.stdin.read()
'''


def absent(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return True
    return False


def run(command, output, data=None):
    child = subprocess.Popen(command, cwd=ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                             stderr=subprocess.PIPE, text=True, start_new_session=True)
    try:
        stdout, stderr = child.communicate(data, timeout=90)
        output.write_text(stdout)
        output.with_suffix('.stderr').write_text(stderr)
        assert child.returncode == 0, stderr
        return stdout
    finally:
        if child.poll() is None:
            os.killpg(child.pid, signal.SIGKILL)
            child.communicate()


def trace(events, admission):
    template = json.loads((ROOT / 'fixtures/valid/core-completed.json').read_text())[:2]
    template[0]['session_id'] = 'session'
    template[0]['payload'] = {'session_id':'session','delivery':'auto','messages':[{'id':'input-message','role':'user','content':'synthetic'}]}
    template[1]['session_id'] = 'session'
    template[1]['payload'] = admission
    return template + events


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    consumer = output / 'consumer'
    (consumer/'src').mkdir(parents=True)
    (consumer/'src/main.rs').write_text(RUST)
    dependencies = '\n'.join(f'{name}={{path={json.dumps(str(ROOT/path))}}}' for name,path in [
        ('oap-adapter-acp','rust/adapter/acp'),('oap-adapter','rust/adapter'),('oap-protocol','rust/protocol')])
    (consumer/'Cargo.toml').write_text('[package]\nname="text-engine-consumer"\nversion="0.0.0"\nedition="2021"\n[dependencies]\nfutures="0.3"\nserde_json="1"\ncrossbeam-channel="0.5"\n'+dependencies+'\n')
    run(['cargo','build','--offline','--manifest-path',str(consumer/'Cargo.toml'),'--target-dir',str(ROOT/'rust/target')], output/'rust-build.log')
    (output/'reference.go').write_text(GO)
    run(['go','build','-o',str(output/'go-reference'),str(output/'reference.go')], output/'go-build.log')
    run(['go','build','-o',str(output/'goap'),'./go/cmd/goap'], output/'goap-build.log')
    cases=['complete','bad-config','empty','max-tokens','max-requests','refusal','unknown-stop','remote-error','malformed','foreign','unknown-update','bad-chunk','overflow','abandon','multi-message','tool','cancelled']
    unavailable={'tool':'acp_unavailable_update','cancelled':'acp_unavailable_cancellation'}
    with tempfile.TemporaryDirectory(prefix='oap-text-engine-') as temporary:
        fake = Path(temporary)/'agent.py'
        fake.write_text(AGENT)
        for mode in cases:
            case = output/mode
            case.mkdir()
            request={'path':sys.executable,'args':['-u',str(fake),mode,str(case)],'mode':mode,'case':str(case)}
            (case/'input.json').write_text(json.dumps(request))
            results, wires = {}, {}
            for name, binary in [('rust',ROOT/'rust/target/debug/text-engine-consumer'),('go',output/'go-reference')]:
                wire=case/'wire.jsonl'
                if wire.exists(): wire.unlink()
                try:
                    results[name]=json.loads(run([str(binary)],case/f'{name}.json',json.dumps(request)))
                    assert absent(int((case/'pid').read_text())), f'{name} left its native child alive'
                    wires[name]=[json.loads(line) for line in wire.read_text().splitlines()]
                    (case/f'{name}.native.json').write_text(json.dumps(wires[name],indent=2))
                finally:
                    pid=case/'pid'
                    if pid.exists() and not absent(int(pid.read_text())):
                        os.kill(int(pid.read_text()),signal.SIGKILL)
            assert wires['rust'] == wires['go'], (mode,wires)
            assert wires['rust'][2]['params'] == {'sessionId':'native-session','prompt':[{'type':'text','text':'synthetic'}]}, wires
            rust, go = results['rust'], results['go']
            if mode in unavailable:
                assert rust['events'][-1]['payload']['error']['code'] == unavailable[mode], rust
                assert rust['events'] != go['events'], mode
                print(f'PASS {mode}: explicit staged unavailable outcome retained; native wire agrees; full mapping deferred',flush=True)
            else:
                assert rust['events'] == go['events'] and rust['errors'] == go['errors'], (mode,results)
                print(f'PASS {mode}: exact Go/Rust event JSON and native wire agree; children reaped',flush=True)
            if mode in ['overflow','abandon']:
                continue
            assembled=trace(copy.deepcopy(rust['events']),go['admission'])
            path=case/'trace.json'
            path.write_text(json.dumps(assembled,indent=2))
            commands={
                'go':[str(output/'goap'),'validate','--format','json',str(path)],
                'rust':['cargo','run','--quiet','--manifest-path','rust/Cargo.toml','-p','oap-validation','--example','validate','--',str(path)],
                'zig':[str(ROOT/'zig/zig-out/bin/oapx'),'validate',str(path)],
            }
            for name, command in commands.items():
                run(command,case/f'validate-{name}.log')
            print(f'PASS {mode}: host-prefix plus actual engine events accepted by all three validators',flush=True)
    print(f'PASS bounded native text-run engine. Standalone engine proof. Evidence: {output}')


if __name__ == '__main__':
    main()
