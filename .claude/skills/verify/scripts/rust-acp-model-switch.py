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
GO = r'''package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type input struct {
	Path          string  `json:"path"`
	Dir           string  `json:"dir"`
	Participant   string  `json:"participant"`
	Model         string  `json:"model"`
	AllowSwitch   bool    `json:"allow_switch"`
	SwitchSession *string `json:"switch_session"`
}

func details(err error) any {
	if err == nil {
		return nil
	}
	var control *base.UnsupportedControlError
	var degraded *base.DegradedControlError
	var model *base.ModelNotFoundError
	switch {
	case errors.As(err, &control):
		return map[string]any{"kind": "unsupported_control", "feature": control.Feature, "reason": control.Reason, "field": control.Field, "detail": control.Detail}
	case errors.As(err, &degraded):
		return map[string]any{"kind": "degraded", "feature": degraded.Feature}
	case errors.As(err, &model):
		return map[string]any{"kind": "model_not_found", "id": model.ModelID}
	case errors.Is(err, base.ErrInvalidSubmission):
		return map[string]any{"kind": "invalid_submission"}
	default:
		return map[string]any{"kind": "other"}
	}
}

func main() {
	var in input
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	adapter, err := acp.New(acp.Config{Executable: in.Path, WorkingDirectory: in.Dir, Environment: []string{}, ShutdownTimeout: 300 * time.Millisecond})
	if err != nil {
		panic(err)
	}
	session, err := adapter.Open(ctx, base.OpenRequest{Participant: protocol.Participant{ID: protocol.ParticipantID(in.Participant)}})
	if err != nil {
		panic(err)
	}
	state, _ := session.State(ctx)
	scope := state.SessionID
	if in.SwitchSession != nil {
		scope = protocol.SessionID(*in.SwitchSession)
	}
	var opted []string
	if in.AllowSwitch {
		opted = []string{protocol.FeatureSessionModelSwitch}
	}
	response, _, switched := session.(base.ModelSwitcher).SwitchModel(ctx, protocol.SessionModelSwitchRequest{SessionID: scope, ModelID: in.Model, AllowDegradedFeatures: opted})
	state, _ = session.State(ctx)
	catalog, listed := session.(base.ModelLister).Models(ctx, protocol.ModelsRequest{SessionID: state.SessionID, AllowDegradedFeatures: []string{protocol.FeatureModelsList}})
	var modelValue, modelError, switchedValue any
	if listed == nil {
		modelValue = catalog.Models
	} else {
		modelError = listed.Error()
	}
	if switched == nil {
		switchedValue = response
	}
	closed := session.Close(ctx)
	var closeError any
	if closed != nil {
		closeError = closed.Error()
	}
	out := map[string]any{"state": state, "nativeId": session.(base.NativeSession).NativeSessionID(), "models": modelValue, "modelError": modelError, "switchResponse": switchedValue, "switchDetails": details(switched), "closeError": closeError}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		panic(err)
	}
}
'''


def run(command, path, value=None):
    with subprocess.Popen(command, cwd=ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, text=True, start_new_session=True) as child:
        try:
            stdout, stderr = child.communicate(value, timeout=180)
        except subprocess.TimeoutExpired:
            os.killpg(child.pid, signal.SIGKILL)
            stdout, stderr = child.communicate()
            path.write_text(stdout + stderr)
            raise
    path.write_text(stdout + stderr)
    assert child.returncode == 0, (command, path, child.returncode)
    return stdout


def reaped(case):
    pid = int((case / 'pid').read_text())
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return
    os.kill(pid, signal.SIGKILL)
    raise AssertionError(f'native child {pid} remains')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oxap-adapter-acp',
         '--example', 'foundation'], output / 'rust-build.log')
    reference = output / 'reference.go'
    reference.write_text(GO)
    go = output / 'go-reference'
    run(['go', 'build', '-o', str(go), str(reference)], output / 'go-build.log')
    rust = ROOT / 'rust/target/debug/examples/foundation'
    model = {'id':'model', 'category':'model', 'type':'select', 'currentValue':'sol',
             'options':[{'value':'sol','name':'Sol'}, {'value':'luna','name':'Luna'}]}
    selected = dict(model, currentValue='luna')
    thought = {'id':'thought', 'category':'thought_level', 'currentValue':'adaptive',
               'options':[{'value':'adaptive', 'name':'HİGH'}]}
    observed = None
    modes = ['confirmed', 'idle-inbound', 'current-level', 'unknown-level', 'unconfirmed', 'duplicate',
             'wrong-kind', 'refused', 'no-optin', 'unknown-model', 'wrong-scope', 'no-model']
    with tempfile.TemporaryDirectory(prefix='oap-model-switch-') as temporary:
        directory = Path(temporary)
        for mode in modes:
            case = output / mode
            case.mkdir()
            opened = {'sessionId':'native-session', 'configOptions':[model]}
            options = [copy.deepcopy(selected), copy.deepcopy(thought)]
            if mode == 'current-level':
                options[1]['currentValue'] = 'LOW'
            if mode == 'unknown-level':
                options[1]['options'][0]['name'] = 'unknown'
            if mode == 'unconfirmed':
                options[0]['currentValue'] = 'sol'
            if mode == 'duplicate':
                options.append(copy.deepcopy(selected))
            if mode == 'wrong-kind':
                options[0]['type'] = 'number'
            if mode == 'no-model':
                opened['configOptions'] = []
            response = {'result':{'configOptions':options}}
            if mode == 'refused':
                response = {'error':{'code':-32602,'message':'synthetic refusal'}}
            data = {'opened':opened, 'switched':response, 'inbound':mode == 'idle-inbound'}
            (case / 'native.json').write_text(json.dumps(data))
            fake = directory / f'agent-{mode}'
            fake.write_text(f'#!{sys.executable} -u\n' + f'''import json, os, sys
from pathlib import Path
out = Path({str(case)!r})
data = json.loads((out / 'native.json').read_text())
(out / 'pid').write_text(str(os.getpid()))
for index, line in enumerate(sys.stdin):
    with (out / 'wire.jsonl').open('a') as wire:
        wire.write(line)
    request = json.loads(line)
    if index == 0:
        response = {{'result':{{'protocolVersion':1,'agentCapabilities':{{}}}}}}
    elif index == 1:
        response = {{'result':data['opened']}}
    else:
        response = data['switched']
        if data['inbound']:
            for update in range(256):
                print(json.dumps({{'jsonrpc':'2.0', 'method':'session/update', 'params':{{'sessionId':'native-session', 'update':{{'sessionUpdate':'session_info_update', 'title':str(update)}}}}}}), flush=True)
            print(json.dumps({{'jsonrpc':'2.0', 'id':'fixture-native', 'method':'fixture/unsupported', 'params':{{}}}}), flush=True)
            answer = sys.stdin.readline()
            with (out / 'wire.jsonl').open('a') as wire:
                wire.write(answer)
            assert json.loads(answer) == {{'jsonrpc':'2.0', 'id':'fixture-native', 'error':{{'code':-32601,'message':'method not supported'}}}}, answer
    print(json.dumps(dict(response, jsonrpc='2.0', id=request['id'])), flush=True)
''')
            fake.chmod(0o700)
            request = {'path':str(fake), 'dir':str(directory), 'participant':'synthetic-user',
                       'open':True, 'model':'luna', 'allow_switch':mode != 'no-optin'}
            if mode == 'unknown-model':
                request['model'] = 'absent'
            if mode == 'wrong-scope':
                request['switch_session'] = 'other'
            (case / 'input.json').write_text(json.dumps(request))
            results = {}
            wires = {}
            for name, command in [('rust', [str(rust)]), ('go', [str(go)])]:
                wire = case / 'wire.jsonl'
                if wire.exists():
                    wire.unlink()
                result = json.loads(run(command, case / f'{name}.json', json.dumps(request)))
                reaped(case)
                frames = [json.loads(line) for line in wire.read_text().splitlines()]
                (case / f'{name}.wire.json').write_text(json.dumps(frames, indent=2))
                results[name] = {key:result[key] for key in [
                    'state', 'nativeId', 'models', 'modelError', 'switchResponse', 'switchDetails', 'closeError']}
                wires[name] = frames
                if name == 'rust' and mode == 'confirmed':
                    observed = copy.deepcopy(result)
                    probe_input = dict(request, open=False)
                    probe = json.loads(run(command, case / 'probe.json', json.dumps(probe_input)))
            for result in results.values():
                result['state'].pop('updated_at_ms', None)
            assert results['rust'] == results['go'], (mode, results)
            assert wires['rust'] == wires['go'], (mode, wires)
            expected_count = 2 if mode in ['no-optin', 'unknown-model', 'wrong-scope', 'no-model'] else 3
            if mode == 'idle-inbound':
                expected_count += 1
            assert len(wires['rust']) == expected_count, (mode, wires)
            if expected_count >= 3:
                assert wires['rust'][2]['params'] == {'sessionId':'native-session', 'configId':'model', 'value':'luna'}
            print(f'PASS {mode}: public Go/Rust results and native wire agree; children reaped')
    assert observed['state']['current_model_id'] == 'luna'
    assert observed['state']['reasoning_level'] == 'high'
    common = {'protocol':'open-agent-protocol', 'version':'0.1',
              'profile':'open-agent-protocol.agent-control-core'}
    revision = observed['revision']
    session = observed['state']['session_id']
    state = observed['initialState']
    trace = [
        dict(common, id='init', type='protocol.initialize.request', payload={'protocol_versions':['0.1'], 'profiles':[common['profile']], 'participant':{'id':'synthetic-user'}}),
        dict(common, id='ready', type='protocol.initialize.response', in_reply_to='init', payload={'protocol_version':'0.1', 'profile':common['profile'], 'endpoint':probe['descriptor']['endpoint']}),
        dict(common, id='caps', type='capabilities.request', payload={}),
        dict(common, id='capabilities', type='capabilities.response', in_reply_to='caps', capability_revision=revision, payload=probe['descriptor']),
        dict(common, id='open', type='session.open.request', capability_revision=revision, payload={'session_id':session}),
        dict(common, id='opened', type='session.open.response', session_id=session, in_reply_to='open', capability_revision=revision, payload=state),
        dict(common, id='switch', type='session.model.switch.request', session_id=session, capability_revision=revision, payload={'session_id':session, 'model_id':'luna', 'allow_degraded_features':['session.model.switch']}),
        dict(common, id='switched', type='session.model.switch.response', session_id=session, in_reply_to='switch', capability_revision=revision, payload=observed['switchResponse']),
        dict(common, id='changed', type='session.state.updated', session_id=session,
             capability_revision=revision, sequence=1, payload=observed['state']),
        dict(common, id='state', type='session.state.request', session_id=session, capability_revision=revision, payload={'session_id':session}),
        dict(common, id='updated', type='session.state.response', session_id=session, in_reply_to='state', capability_revision=revision, payload=observed['state']),
    ]
    trace_path = output / 'model-switch.trace.json'
    trace_path.write_text(json.dumps(trace))
    run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oxap-validation',
         '--example', 'validate'], output / 'validator-build.log')
    run(['go', 'build', '-o', str(output / 'goap'), './go/cmd/goap'], output / 'goap-build.log')
    for name, command in [('go', [str(output / 'goap'), 'validate']),
                          ('rust', [str(ROOT / 'rust/target/debug/examples/validate')]),
                          ('zig', [str(ROOT / 'zig/zig-out/bin/oxap'), 'validate'])]:
        run(command + [str(trace_path)], output / f'trace-{name}.log')
        print(f'PASS confirmed switch/state trace validates with {name}')
    print(f'PASS model-switch confirmation. Evidence: {output}')


if __name__ == '__main__':
    main()
