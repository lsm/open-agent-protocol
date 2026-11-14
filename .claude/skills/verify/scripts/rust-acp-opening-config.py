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
 "os"
 "strings"
 "time"
 base "github.com/lsm/oxap/go/adapter"
 "github.com/lsm/oxap/go/adapter/acp"
 "github.com/lsm/oxap/go/protocol"
)
type input struct { Python string; Agent string; Case string; Wanted map[string]string; Reasoning string; Reopen bool }
func check(err error) { if err!=nil { panic(err) } }
func must[T any](value T, err error) T { check(err);return value }
func failure(err error) any {
 var control *base.UnsupportedControlError
 if errors.As(err,&control) { return map[string]any{"feature":control.Feature,"reason":control.Reason,"field":control.Field,"detail":control.Detail} }
 kind:="other"
 if strings.HasPrefix(err.Error(),"ACP session config ") { kind="config" }
 return map[string]any{"kind":kind,"error":err.Error()}
}
func main() {
 var in input
 check(json.NewDecoder(os.Stdin).Decode(&in))
 ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second)
 defer cancel()
 adapter:=must(acp.New(acp.Config{Executable:in.Python,Args:[]string{"-u",in.Agent,in.Case},WorkingDirectory:"/",Environment:[]string{},QueueCapacity:1,ShutdownTimeout:time.Second,SessionConfig:in.Wanted}))
 session,err:=adapter.Open(ctx,base.OpenRequest{SessionID:"session",Participant:protocol.Participant{ID:"user"},Reopen:in.Reopen,NativeSessionID:"native",ReasoningLevel:protocol.ReasoningLevel(in.Reasoning)})
 if err!=nil { check(json.NewEncoder(os.Stdout).Encode(map[string]any{"failure":failure(err)}));return }
 defer session.Close(context.Background())
 state:=must(session.State(ctx))
 catalog,err:=session.(base.ModelLister).Models(ctx,protocol.ModelsRequest{SessionID:"session",AllowDegradedFeatures:[]string{protocol.FeatureModelsList}})
 var models any
 if err!=nil {models=map[string]any{"failure":failure(err)}} else {models=map[string]any{"ok":catalog.Models}}
 nativeID:=session.(base.NativeSession).NativeSessionID()
 check(session.Close(ctx))
 check(json.NewEncoder(os.Stdout).Encode(map[string]any{"state":state,"models":models,"native_id":nativeID}))
}
'''


def options():
    return [dict(id='mode', category='mode', currentValue='code', options=[dict(value='ask', name='Ask'), dict(value='code', name='Code')]),
            dict(id='model', category='model', currentValue='sol', options=[dict(value='sol', name='Sol'), dict(value='luna', name='Luna')]),
            dict(id='effort', category='thought_level', currentValue='medium', options=[dict(value='medium', name='Medium'), dict(value='deep', name='HIGH')])]


def changed(index, **patch):
    value = options()
    value[index].update(patch)
    return value


def scenarios():
    wanted = dict(model='luna', mode='ask')
    first = changed(0, currentValue='ask')
    reset = changed(1, currentValue='luna')
    confirmed = changed(0, currentValue='ask')
    return {
        'fresh-defaults': ({}, {}),
        'configured-order': ({}, dict(wanted=wanted)),
        'configured-reasoning': ({}, dict(wanted=wanted, reasoning='high')),
        'reloaded-settings': ({}, dict(wanted=wanted, reasoning='high', reopen=True)),
        'missing-option': ({}, dict(wanted=dict(missing='x'))),
        'missing-value': ({}, dict(wanted=dict(model='unknown'))),
        'empty-id': ({}, dict(wanted={'': 'ask'})),
        'empty-value': ({}, dict(wanted=dict(model=''))),
        'duplicate-offer': (dict(options=options() + [options()[0]]), dict(wanted=dict(mode='ask'))),
        'wrong-offer-kind': (dict(options=changed(0, type='boolean')), dict(wanted=dict(mode='ask'))),
        'config-rejected': (dict(error=dict(code=-32602, message='refused')), dict(wanted=dict(mode='ask'))),
        'config-unconfirmed': (dict(replies=[dict(configOptions=options())]), dict(wanted=dict(mode='ask'))),
        'config-removed': (dict(replies=[dict(configOptions=[])]), dict(wanted=dict(mode='ask'))),
        'duplicate-confirmation': (dict(replies=[dict(configOptions=confirmed + [options()[0]])]), dict(wanted=dict(mode='ask'))),
        'wrong-confirmed-kind': (dict(replies=[dict(configOptions=changed(0, currentValue='ask', type='boolean'))]), dict(wanted=dict(mode='ask'))),
        'dependent-reset': (dict(replies=[dict(configOptions=first), dict(configOptions=reset)]), dict(wanted=wanted)),
        'reasoning-reset': (dict(replies=[dict(configOptions=first), dict(configOptions=changed(2, currentValue='deep'))]), dict(wanted=dict(mode='ask'), reasoning='high')),
        'reasoning-conflict': ({}, dict(wanted=dict(effort='medium'), reasoning='high')),
        'reasoning-alias': ({}, dict(reasoning='high')),
        'reasoning-no-option': (dict(options=[]), dict(reasoning='high')),
        'reasoning-no-value': ({}, dict(reasoning='max')),
        'reasoning-unconfirmed': (dict(replies=[dict(configOptions=options())]), dict(reasoning='high')),
        'reasoning-rejected': (dict(error=dict(code=-32602, message='refused')), dict(reasoning='high')),
        'reasoning-explicit': (dict(replies=[dict(configOptions=[dict(id='effort', currentValue='deep')])]), dict(reasoning='high')),
        'reasoning-dotted-i': (dict(options=changed(2, options=[dict(value='deep', name='HİGH')])), dict(reasoning='high')),
        'reasoning-first-alias': (dict(options=changed(2, options=[dict(value='low', name='High'), dict(value='HIGH', name='Low')])), dict(reasoning='high')),
        'malformed-config-result': (dict(replies=[dict(configOptions=[dict(currentValue=7)])]), dict(wanted=dict(mode='ask'))),
        'malformed-reasoning-result': (dict(replies=[dict(configOptions=[dict(currentValue=7)])]), dict(reasoning='high')),
    }


def comparable(value):
    if 'failure' in value:
        failure = value['failure']
        if 'feature' in failure:
            return dict(failure={key: failure[key] for key in ['feature', 'reason', 'field']})
        return dict(failure=dict(kind=failure['kind']))
    return results.normalized({key: value[key] for key in ['state', 'models', 'native_id']})


def trace(observed, request):
    common = dict(protocol='open-agent-protocol', version='0.1', profile='open-agent-protocol.agent-control-core')
    revision = observed['revision']
    payload = dict(session_id='session')
    if request.get('reopen'):
        payload['reopen'] = True
    if request.get('reasoning'):
        payload['reasoning_level'] = request['reasoning']
    return [dict(common, id='caps-request', type='capabilities.request', payload={}),
            dict(common, id='caps-response', type='capabilities.response', in_reply_to='caps-request', capability_revision=revision, payload=observed['descriptor']),
            dict(common, id='open', type='session.open.request', capability_revision=revision, payload=payload),
            dict(common, id='opened', type='session.open.response', in_reply_to='open', session_id='session', capability_revision=revision, payload=observed['state'])]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    peer.run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oxap-adapter-acp', '--example', 'opening_config'], output / 'rust-build.log')
    (output / 'reference.go').write_text(GO)
    peer.run(['gofmt', '-w', str(output / 'reference.go')], output / 'gofmt.log')
    peer.run(['go', 'build', '-o', str(output / 'reference'), str(output / 'reference.go')], output / 'go-build.log')
    peer.run(['go', 'build', '-o', str(output / 'goap'), './go/cmd/goap'], output / 'goap-build.log')
    peer.run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oxap-validation', '--example', 'validate'], output / 'validator-build.log')
    agent = Path(__file__).with_name('fixtures') / 'acp-opening-config-agent.py'
    for name, (patch, request) in scenarios().items():
        case = output / name
        case.mkdir()
        observed, wires = {}, {}
        for language, binary in [('rust', ROOT / 'rust/target/debug/examples/opening_config'), ('go', output / 'reference')]:
            directory = case / language
            directory.mkdir()
            data = dict(options=options())
            data.update(copy.deepcopy(patch))
            (directory / 'native.json').write_text(json.dumps(data))
            value = dict(python=sys.executable, agent=str(agent), case=str(directory), **request)
            (directory / 'input.json').write_text(json.dumps(value))
            try:
                observed[language] = json.loads(peer.run([str(binary)], directory / 'result.json', json.dumps(value)))
                if (directory / 'pid').exists():
                    assert peer.absent(int((directory / 'pid').read_text())), f'{language} native child remains'
            finally:
                if (directory / 'pid').exists():
                    pid = int((directory / 'pid').read_text())
                    if not peer.absent(pid):
                        os.kill(pid, signal.SIGKILL)
            path = directory / 'wire.jsonl'
            wires[language] = [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []
        assert comparable(observed['rust']) == comparable(observed['go']), (name, observed)
        assert wires['rust'] == wires['go'], (name, wires)
        if 'failure' not in observed['rust']:
            path = case / 'trace.json'
            path.write_text(json.dumps(trace(observed['rust'], request)))
            for validator, command in [('go', [str(output / 'goap'), 'validate']), ('rust', [str(ROOT / 'rust/target/debug/examples/validate')]), ('zig', [str(ROOT / 'zig/zig-out/bin/oxap'), 'validate'])]:
                peer.run(command + [str(path)], case / f'{validator}-validation.log')
        print(f'PASS {name}: public state/catalog/refusal metadata and parsed native wire agree; native children reaped', flush=True)
    print(f'PASS confirmed opening config/reasoning. Evidence: {output}', flush=True)


if __name__ == '__main__':
    main()
