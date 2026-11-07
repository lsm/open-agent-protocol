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
 "sync/atomic"
 "time"
 base "github.com/lsm/open-agent-protocol/go/adapter"
 "github.com/lsm/open-agent-protocol/go/adapter/acp"
 "github.com/lsm/open-agent-protocol/go/protocol"
)
type input struct { Python string; Agent string; Case string; Submit bool; NativeID *string `json:"native_id"` }
type ids struct { n atomic.Uint64 }
func (i *ids) NewID(kind string) string { return fmt.Sprintf("%s-%d",kind,i.n.Add(1)) }
func check(err error) { if err!=nil { panic(err) } }
func must[T any](value T, err error) T { check(err);return value }
func failure(err error) any {
 var control *base.UnsupportedControlError
 if errors.As(err,&control) { return map[string]any{"feature":control.Feature,"reason":control.Reason,"detail":control.Detail} }
 return map[string]any{"error":err.Error()}
}
func main() {
 var in input
 check(json.NewDecoder(os.Stdin).Decode(&in))
 ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second)
 defer cancel()
 nativeID:="bound-native"
 if in.NativeID!=nil { nativeID=*in.NativeID }
 adapter:=must(acp.New(acp.Config{Executable:in.Python,Args:[]string{"-u",in.Agent,in.Case},WorkingDirectory:"/",Environment:[]string{},QueueCapacity:1,ShutdownTimeout:time.Second,IDs:&ids{},MCPServers:[]acp.MCPServer{{Name:"fixture",Command:"synthetic-mcp"}}}))
 session,err:=adapter.Open(ctx,base.OpenRequest{SessionID:"session",Participant:protocol.Participant{ID:"user"},Reopen:true,NativeSessionID:nativeID})
 if err!=nil { check(json.NewEncoder(os.Stdout).Encode(map[string]any{"failure":failure(err)}));return }
 defer session.Close(context.Background())
 state:=must(session.State(ctx))
 catalog,err:=session.(base.ModelLister).Models(ctx,protocol.ModelsRequest{SessionID:"session",AllowDegradedFeatures:[]string{protocol.FeatureModelsList}})
 var models any
 if err!=nil { models=map[string]any{"failure":failure(err)} } else { models=map[string]any{"ok":catalog.Models} }
 var admission any
 events:=[]protocol.Envelope{}
 if in.Submit {
  accepted,stream,err:=session.Submit(ctx,base.SubmitRequest{Request:protocol.MessageSubmitRequest{SessionID:"session",Messages:[]protocol.Message{{ID:"input",Role:protocol.RoleUser,Content:protocol.TextContent("continue")}}}})
  check(err);admission=accepted
  for event:=range stream { check(event.Error);events=append(events,event.Envelope) }
 }
 final:=must(session.State(ctx))
 check(session.Close(ctx))
 check(json.NewEncoder(os.Stdout).Encode(map[string]any{"state":state,"native_id":session.(base.NativeSession).NativeSessionID(),"models":models,"admission":admission,"events":events,"final":final}))
}
'''


def trace(observed):
    common = dict(protocol='open-agent-protocol', version='0.1', profile='open-agent-protocol.agent-control-core')
    revision = observed['revision']
    frames = [dict(common, id='caps-request', type='capabilities.request', payload={}),
              dict(common, id='caps-response', type='capabilities.response', in_reply_to='caps-request', capability_revision=revision, payload=observed['descriptor']),
              dict(common, id='open', type='session.open.request', capability_revision=revision, payload=dict(session_id='session', reopen=True)),
              dict(common, id='opened', type='session.open.response', in_reply_to='open', session_id='session', capability_revision=revision, payload=observed['state'])]
    if observed['admission'] is not None:
        prefix = copy.deepcopy(json.loads((ROOT / 'fixtures/valid/core-completed.json').read_text())[:2])
        prefix[0].update(id='submit', session_id='session', capability_revision=revision)
        prefix[0]['payload'] = dict(session_id='session', delivery='auto', messages=[dict(id='input', role='user', content='continue')], allow_degraded_features=['session.message.submit', 'run.streaming'])
        prefix[1].update(id='accepted', in_reply_to='submit', session_id='session', capability_revision=revision)
        prefix[1]['payload'] = observed['admission']
        frames += prefix + observed['events']
    frames += [dict(common, id='state-request', type='session.state.request', session_id='session', capability_revision=revision, payload=dict(session_id='session')),
               dict(common, id='state-response', type='session.state.response', in_reply_to='state-request', session_id='session', capability_revision=revision, payload=observed['final'])]
    return frames


def comparable(observed):
    if 'failure' in observed:
        return {'failure': {key: observed['failure'][key] for key in ['feature', 'reason']}}
    return results.normalized({key: observed[key] for key in ['state', 'native_id', 'models', 'admission', 'events', 'final']})


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    peer.run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oap-adapter-acp', '--example', 'reload'], output / 'rust-build.log')
    (output / 'reference.go').write_text(GO)
    peer.run(['gofmt', '-w', str(output / 'reference.go')], output / 'gofmt.log')
    peer.run(['go', 'build', '-o', str(output / 'reference'), str(output / 'reference.go')], output / 'go-build.log')
    peer.run(['go', 'build', '-o', str(output / 'goap'), './go/cmd/goap'], output / 'goap-build.log')
    peer.run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oap-validation', '--example', 'validate'], output / 'validator-build.log')
    options = [dict(id='picker', category='model', currentValue='restored', options=[dict(value='restored', name='Restored')]),
               dict(id='effort', category='thought_level', currentValue='deep', options=[dict(value='deep', name='High')])]
    cases = {
        'load-before-resume': (dict(loadSession=True, sessionCapabilities=dict(resume={})), dict(configOptions=options), {}),
        'resume': (dict(loadSession=False, sessionCapabilities=dict(resume={})), dict(configOptions=options), {}),
        'resume-only': (dict(sessionCapabilities=dict(resume={})), dict(configOptions=options), {}),
        'null-id': (dict(loadSession=True), dict(sessionId=None, configOptions=options), {}),
        'opaque-settings': (dict(loadSession=True), dict(configOptions=[dict(category='model', currentValue='default'), dict(category='thought_level', currentValue='custom')]), {}),
        'no-capability': ({}, {}, {}),
        'malformed-load': (dict(loadSession='true'), {}, {}),
        'malformed-resume': (dict(sessionCapabilities=dict(resume=True)), {}, {}),
        'null-resume': (dict(sessionCapabilities=dict(resume=None)), {}, {}),
        'missing-binding': (dict(loadSession=True), {}, dict(native_id='')),
        'wrong-id': (dict(loadSession=True), dict(sessionId='other'), {}),
        'empty-id': (dict(loadSession=True), dict(sessionId=''), {}),
        'null-result': (dict(loadSession=True), None, {}),
        'array-result': (dict(loadSession=True), [], {}),
        'bad-options': (dict(loadSession=True), dict(configOptions=[dict(currentValue=7)]), {}),
        'remote-refusal': (dict(loadSession=True), {}, {}),
    }
    agent = Path(__file__).with_name('fixtures') / 'acp-reload-agent.py'
    for name, (capabilities, result, extra) in cases.items():
        case = output / name
        case.mkdir()
        observed, wires = {}, {}
        for language, binary in [('rust', ROOT / 'rust/target/debug/examples/reload'), ('go', output / 'reference')]:
            directory = case / language
            directory.mkdir()
            data = dict(capabilities=capabilities, result=result)
            if name == 'remote-refusal':
                data['error'] = dict(code=-32602, message='unknown session')
            (directory / 'native.json').write_text(json.dumps(data))
            request = dict(python=sys.executable, agent=str(agent), case=str(directory), submit=name in ['load-before-resume', 'resume', 'resume-only'], **extra)
            (directory / 'input.json').write_text(json.dumps(request))
            try:
                observed[language] = json.loads(peer.run([str(binary)], directory / 'result.json', json.dumps(request)))
                if (directory / 'pid').exists():
                    assert peer.absent(int((directory / 'pid').read_text())), f'{language} native child remains'
            finally:
                if (directory / 'pid').exists():
                    pid = int((directory / 'pid').read_text())
                    if not peer.absent(pid):
                        os.kill(pid, signal.SIGKILL)
            wire = directory / 'wire.jsonl'
            wires[language] = [json.loads(line) for line in wire.read_text().splitlines()] if wire.exists() else []
        assert comparable(observed['rust']) == comparable(observed['go']), (name, observed)
        assert wires['rust'] == wires['go'], (name, wires)
        if 'failure' not in observed['rust']:
            trace_path = case / 'trace.json'
            trace_path.write_text(json.dumps(trace(observed['rust'])))
            for validator, command in [('go', [str(output / 'goap'), 'validate']), ('rust', [str(ROOT / 'rust/target/debug/examples/validate')]), ('zig', [str(ROOT / 'zig/zig-out/bin/oapx'), 'validate'])]:
                peer.run(command + [str(trace_path)], case / f'{validator}-validation.log')
        print(f'PASS {name}: public Go/Rust state, events and structured refusal agree; parsed native wire matches; children reaped', flush=True)
    print(f'PASS capability-gated reload. Evidence: {output}', flush=True)


if __name__ == '__main__':
    main()
