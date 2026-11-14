import argparse
import copy
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location('results', Path(__file__).with_name('rust-acp-results.py'))
results = importlib.util.module_from_spec(spec)
spec.loader.exec_module(results)
peer = results.peer

GO = r'''package main
import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "strconv"
 "syscall"
 "path/filepath"
 "sync/atomic"
 "time"
 base "github.com/lsm/open-agent-protocol/go/adapter"
 "github.com/lsm/open-agent-protocol/go/adapter/acp"
 "github.com/lsm/open-agent-protocol/go/protocol"
)
type input struct { Path string; Args []string; Case string; Mode string }
type clock struct{}
func (clock) Now() time.Time { return time.UnixMilli(1000) }
type ids struct { n atomic.Uint64 }
func (i *ids) NewID(kind string) string { return fmt.Sprintf("%s-%d", kind, i.n.Add(1)) }
func check(err error) { if err != nil { panic(err) } }
func must[T any](value T, err error) T { check(err); return value }
func release(path string) { check(os.WriteFile(path, []byte("go"), 0600)) }
func wait(path string) {
 deadline := time.Now().Add(10*time.Second)
 for { if _, err := os.Stat(path); err == nil { return }; if time.Now().After(deadline) { panic("missing " + path) }; time.Sleep(time.Millisecond) }
}
func next(stream base.EventStream) protocol.Envelope {
 event, ok := <-stream
 if !ok { panic("early stream close") }
 check(event.Error)
 return event.Envelope
}
func control(response protocol.RunCancelResponse, err error) any {
 if err == nil { return response }
 var terminal *base.RunTerminalError
 if errors.As(err, &terminal) { return map[string]any{"error":"run_terminal","run_id":terminal.RunID,"status":terminal.Status} }
 panic(err)
}
func main() {
 var in input
 check(json.NewDecoder(os.Stdin).Decode(&in))
 ctx, cancel := context.WithTimeout(context.Background(),30*time.Second)
 defer cancel()
 adapter := must(acp.New(acp.Config{Executable:in.Path,Args:in.Args,Environment:[]string{},WorkingDirectory:"/",QueueCapacity:1,Clock:clock{},IDs:&ids{},ShutdownTimeout:time.Second,AbortActiveOnClose:true}))
 session := must(adapter.Open(ctx,base.OpenRequest{SessionID:"session",Participant:protocol.Participant{ID:"user"}}))
 defer session.Close(context.Background())
 admission, stream, err := session.Submit(ctx,base.SubmitRequest{Request:protocol.MessageSubmitRequest{SessionID:"session",Messages:[]protocol.Message{{ID:"input-message",Role:protocol.RoleUser,Content:protocol.TextContent("synthetic")}}}})
 check(err)
 events := []protocol.Envelope{}
 prefix := 2
 if in.Mode == "gates" { prefix = 4 }
 for index:=0;index<prefix;index++ { events=append(events,next(stream)) }
 intent := must(session.Cancel(ctx,admission.RunID))
 cancelling := must(session.State(ctx))
 if in.Mode == "gates" { release(filepath.Join(in.Case,"late")); wait(filepath.Join(in.Case,"late-answer.json")) }
 release(filepath.Join(in.Case,"stream"))
 for {
  event := next(stream)
  events=append(events,event)
  if event.Type == protocol.TypeContentDelta {
   var payload protocol.ContentDeltaPayload
   check(json.Unmarshal(event.Payload,&payload))
   if payload.Part.Text == "after" { break }
  }
 }
 duplicate := must(session.Cancel(ctx,admission.RunID))
 stillRunning := must(session.State(ctx))
 select { case event := <-stream: panic(fmt.Sprintf("settled before native confirmation: %+v",event)); default: }
 release(filepath.Join(in.Case,"finish"))
 for event := range stream { check(event.Error); events=append(events,event.Envelope) }
 if in.Mode == "exit" {
  release(filepath.Join(in.Case,"exit"))
  raw := must(os.ReadFile(filepath.Join(in.Case,"pid")))
  pid := must(strconv.Atoi(string(raw)))
  deadline := time.Now().Add(3*time.Second)
  for syscall.Kill(pid,0)==nil && time.Now().Before(deadline) { time.Sleep(time.Millisecond) }
  if syscall.Kill(pid,0)==nil { panic("native survived exit") }
 }
 retry := control(session.Cancel(ctx,admission.RunID))
 state := must(session.State(ctx))
 check(session.Close(ctx))
 check(json.NewEncoder(os.Stdout).Encode(map[string]any{"admission":admission,"events":events,"cancel":intent,"cancelling":cancelling,"duplicate":duplicate,"still_running":stillRunning,"pending":true,"retry":retry,"state":state}))
}
'''


def trace(observed):
    common = dict(protocol='open-agent-protocol', version='0.1', profile='open-agent-protocol.agent-control-core')
    revision = observed['revision']
    prefix = copy.deepcopy(json.loads((ROOT/'fixtures/valid/core-completed.json').read_text())[:2])
    prefix[0].update(id='submit', session_id='session', capability_revision=revision)
    prefix[0]['payload'] = dict(session_id='session', delivery='auto', messages=[dict(id='input-message', role='user', content='synthetic')], allow_degraded_features=['session.message.submit', 'run.streaming'])
    prefix[1].update(id='accepted', in_reply_to='submit', session_id='session', capability_revision=revision)
    prefix[1]['payload'] = observed['admission']
    frames = [dict(common, id='cap-request', type='capabilities.request', payload={}),
              dict(common, id='cap-response', type='capabilities.response', in_reply_to='cap-request', capability_revision=revision, payload=observed['descriptor'])] + prefix
    control = dict(common, session_id='session', run_id=observed['admission']['run_id'], capability_revision=revision)
    for event in observed['events']:
        if event['type'] == 'run.status.updated' and event['payload']['status'] == 'cancelling':
            frames.extend([dict(control, id='cancel-request', type='run.cancel.request', payload=dict(session_id='session', run_id=control['run_id'])),
                           dict(control, id='cancel-response', type='run.cancel.response', in_reply_to='cancel-request', payload=observed['cancel'])])
        frames.append(event)
    return frames


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    peer.run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oxap-adapter-acp', '--example', 'session_cancel'], output/'rust-build.log')
    (output/'reference.go').write_text(GO)
    peer.run(['gofmt', '-w', str(output/'reference.go')], output/'gofmt.log')
    peer.run(['go', 'build', '-o', str(output/'reference'), str(output/'reference.go')], output/'go-build.log')
    peer.run(['go', 'build', '-o', str(output/'goap'), './go/cmd/goap'], output/'goap-build.log')
    peer.run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oxap-validation', '--example', 'validate'], output/'validator-build.log')
    agent = Path(__file__).with_name('fixtures')/'acp-cancel-agent.py'
    for mode in ['cancelled', 'remote', 'complete', 'failure', 'gates', 'exit']:
        case = output/mode
        case.mkdir()
        observed, wires = {}, {}
        for language, binary in [('rust', ROOT/'rust/target/debug/examples/session_cancel'), ('go', output/'reference')]:
            directory = case/language
            directory.mkdir()
            request = dict(path=sys.executable, args=['-u', str(agent), str(directory), mode], case=str(directory), mode=mode)
            try:
                observed[language] = json.loads(peer.run([str(binary)], directory/'result.json', json.dumps(request)))
                assert peer.absent(int((directory/'pid').read_text())), language+' left a native child alive'
                wires[language] = [json.loads(line) for line in (directory/'wire.jsonl').read_text().splitlines()]
            finally:
                pid = directory/'pid'
                if pid.exists() and not peer.absent(int(pid.read_text())):
                    os.kill(int(pid.read_text()), signal.SIGKILL)
        assert wires['rust'] == wires['go'], (mode, wires)
        assert [frame for frame in wires['rust'] if frame.get('method') == 'session/cancel'] == [dict(jsonrpc='2.0', method='session/cancel', params=dict(sessionId='native'))], wires
        rust = {key:value for key,value in observed['rust'].items() if key not in ['descriptor', 'revision']}
        assert results.normalized(rust) == results.normalized(observed['go']), (mode, observed)
        assert rust['cancel']['status'] == rust['duplicate']['status'] == 'cancelling'
        assert rust['pending'] and rust['still_running']['status'] == 'running'
        assert rust['state']['status'] == 'idle'
        expected = 'run.completed' if mode == 'complete' else 'run.failed' if mode == 'failure' else 'run.cancelled'
        assert rust['events'][-1]['type'] == expected
        if mode == 'gates':
            assert sum(event['type'] == 'action.permission.requested' for event in rust['events']) == 1
            for name in ['gate-answer.json', 'late-answer.json']:
                answer = json.loads((case/'rust'/name).read_text())
                assert answer['result']['outcome'] == dict(outcome='cancelled'), answer
        path = case/'trace.json'
        path.write_text(json.dumps(trace(observed['rust']), ensure_ascii=False, indent=2))
        for language, command in [('go', [str(output/'goap'), 'validate']), ('rust', [str(ROOT/'rust/target/debug/examples/validate')]), ('zig', [str(ROOT/'zig/zig-out/bin/oxap'), 'validate'])]:
            peer.run(command+[str(path)], case/f'validate-{language}.log')
        print(f'PASS {mode}: public cancel intent/confirmation, Go/Rust events and parsed native frames agree; three validators accept actual Rust events; peers reaped', flush=True)
    integration = peer.run(['cargo', 'test', '--manifest-path', 'rust/Cargo.toml', '-p', 'oxap-adapter-acp', '--test', 'cancel', '--test', 'cancel_order', '--', '--nocapture'], output/'integration.log')
    order = json.loads(next(line.split('cancel-order-proof ', 1)[1] for line in integration.splitlines() if 'cancel-order-proof ' in line))
    assert order['parent_pending'] and order['reaped'], order
    assert [event['type'] for event in order['events']] == ['run.started', 'run.status.updated', 'run.cancelled'], order
    (output/'order.json').write_text(json.dumps(order, indent=2))
    print('PASS native control refusals, stale-run isolation and accepted-write/confirmation ordering; peers reaped', flush=True)
    print(f'PASS ACP public cancellation. Only clock/revision fields normalized; synthetic native inputs. Evidence: {output}')


if __name__ == '__main__':
    main()
