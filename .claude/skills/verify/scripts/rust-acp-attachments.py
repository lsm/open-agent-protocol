import argparse
import copy
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location('settings', Path(__file__).with_name('rust-acp-opening-config.py'))
settings = importlib.util.module_from_spec(spec)
spec.loader.exec_module(settings)
peer = settings.peer

GO = r'''package main

import (
	"context"
	"encoding/json"
	"errors"
	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"io"
	"os"
	"path/filepath"
	"time"
)

type request struct {
	Attachments   []protocol.ToolSourceAttachment
	Tools         []protocol.ToolDefinition
	SessionID     *string `json:"session_id"`
	NativeID      *string `json:"native_id"`
	NoParticipant bool    `json:"no_participant"`
	Reopen        bool
	Reasoning     string
	Cancel        bool
}
type input struct {
	Python      string
	Agent       string
	Case        string
	Configured  json.RawMessage
	Environment []string
	Requests    []request
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
func must[T any](value T, err error) T { check(err); return value }
func text(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}
func failure(err error) any {
	var control *base.UnsupportedControlError
	if errors.As(err, &control) {
		return map[string]any{"feature": control.Feature, "reason": control.Reason, "tool": control.Tool, "field": control.Field, "source": control.Source, "detail": control.Detail, "error_raw": err.Error()}
	}
	kind := "other"
	if errors.Is(err, base.ErrInvalidParticipant) {
		kind = "invalid_participant"
	}
	if errors.Is(err, base.ErrInvalidResolution) {
		kind = "invalid_resolution"
	}
	return map[string]any{"kind": kind, "error_raw": err.Error()}
}
func main() {
	raw := must(io.ReadAll(os.Stdin))
	var in input
	check(json.Unmarshal(raw, &in))
	if in.Requests == nil {
		var req request
		check(json.Unmarshal(raw, &req))
		in.Requests = []request{req}
	}
	config := acp.Config{Executable: in.Python, Args: []string{"-u", in.Agent, in.Case}, Environment: in.Environment, WorkingDirectory: "/", QueueCapacity: 1, ShutdownTimeout: time.Second}
	if len(in.Configured) > 0 {
		check(json.Unmarshal(in.Configured, &config.MCPServers))
	}
	adapter := must(acp.New(config))
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	descriptor := must(adapter.Probe(ctx))
	results := []any{}
	for _, item := range in.Requests {
		participant := "user"
		if item.NoParticipant {
			participant = ""
		}
		req := base.OpenRequest{SessionID: protocol.SessionID(text(item.SessionID, "session")), Participant: protocol.Participant{ID: protocol.ParticipantID(participant)}, Reopen: item.Reopen, NativeSessionID: text(item.NativeID, "native"), ToolSources: item.Attachments, Tools: item.Tools, ReasoningLevel: protocol.ReasoningLevel(item.Reasoning)}
		if item.Cancel {
			opening, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() {
				session, err := adapter.Open(opening, req)
				if err == nil {
					_ = session.Close(context.Background())
				}
				done <- err
			}()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(in.Case, "opening")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					cancel()
					panic("native attachment request was not observed")
				}
				time.Sleep(time.Millisecond)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					panic(err)
				}
			case <-time.After(3 * time.Second):
				panic("cancelled native open did not reap")
			}
			results = append(results, map[string]any{"open": map[string]any{"cancelled": true}})
			continue
		}
		session, err := adapter.Open(ctx, req)
		var observed any
		if err != nil {
			observed = map[string]any{"failure": failure(err)}
		} else {
			state := must(session.State(ctx))
			nativeID := session.(base.NativeSession).NativeSessionID()
			check(session.Close(ctx))
			check(session.Close(ctx))
			observed = map[string]any{"state": state, "native_id": nativeID}
		}
		results = append(results, map[string]any{"open": observed})
	}
	_, err := os.Stat(filepath.Join(in.Case, "pids"))
	check(json.NewEncoder(os.Stdout).Encode(map[string]any{"descriptor": descriptor.Capabilities, "revision": descriptor.CapabilityRevision, "results": results, "spawned": err == nil}))
}
'''


def source(name='filesystem'):
    return dict(id=name, kind='process', protocol='mcp', display_name='Files', endpoint='stdio:fixture', command='/synthetic/never-executed-mcp', args=['--root','/workspace'], environment=['TOKEN','MISSING','MODE=readonly'])


def scenarios():
    fixed = [dict(name='fixed', command='operator-mcp', args=['--fixed'], env=[dict(name='CONFIG',value='operator-config')])]
    one = source()
    cases = {
        'empty': ({}, {}, None),
        'captured': (dict(environment=['MCP_TOKEN=operator-secret'], attachments=[dict(id='filesystem',kind='process',protocol='mcp',display_name='Filesystem',endpoint='stdio:filesystem-tools',command='/usr/local/bin/mcp-filesystem',args=['--root','/workspace'],environment=['MCP_TOKEN','NEVER_EXPOSED','MCP_MODE=readonly'])]), {}, None),
        'configured-plus-attached': (dict(configured=fixed, environment=['TOKEN=operator','UNREQUESTED=private'],attachments=[one]), {}, None),
        'explicit-only-env': (dict(attachments=[one]), {}, None),
        'last-operator-value': (dict(environment=['TOKEN=first','TOKEN=last'],attachments=[one]), {}, None),
        'empty-literal': (dict(environment=['TOKEN=operator'],attachments=[dict(one,environment=['TOKEN='])]), {}, None),
        'escaped-literals': (dict(attachments=[dict(one,environment=['VALUE=<>&\u2028🧪','EXPR=x=y=z'])]), {}, None),
        'descriptor-protocol-metadata': (dict(attachments=[dict(one,protocol='custom-protocol')]), {}, None),
        'load': (dict(configured=fixed,environment=['TOKEN=operator'],attachments=[one],reopen=True), dict(caps=dict(loadSession=True)), None),
        'load-precedence': (dict(attachments=[one],reopen=True), dict(caps=dict(loadSession=True,sessionCapabilities=dict(resume={}))), None),
        'resume': (dict(attachments=[one],reopen=True), dict(caps=dict(loadSession=False,sessionCapabilities=dict(resume={}))), None),
        'isolated-opens': (dict(configured=fixed,requests=[dict(session_id='a',attachments=[one]),dict(session_id='b',attachments=[source('second')])]), {}, None),
        'cancel-new': (dict(attachments=[one],cancel=True), dict(quiet=True), None),
        'cancel-load': (dict(attachments=[one],cancel=True,reopen=True), dict(quiet=True), None),
        'cancel-resume': (dict(attachments=[one],cancel=True,reopen=True), dict(quiet=True,caps=dict(sessionCapabilities=dict(resume={}))), None),
        'missing-id': (dict(attachments=[dict(one,id='')]), {}, dict(feature='action.tool_sources.attach',reason='unsatisfiable')),
        'configured-collision': (dict(configured=fixed,attachments=[source('fixed')]), {}, dict(feature='action.tool_sources.attach',reason='unsatisfiable')),
        'duplicate-attachments': (dict(attachments=[one,one]), {}, dict(feature='action.tool_sources.attach',reason='unsatisfiable')),
        'unsupported-transport': (dict(attachments=[dict(one,kind='http')]), {}, dict(feature='action.tool_sources.attach',reason='unsatisfiable')),
        'empty-kind': (dict(attachments=[dict(one,kind='')]), {}, dict(feature='action.tool_sources.attach',reason='unsatisfiable')),
        'duplicate-bare-literal-env': (dict(attachments=[dict(one,environment=['TOKEN','TOKEN=value'])]), {}, dict(feature='action.tool_sources.attach',reason='unsatisfiable')),
        'duplicate-literal-env': (dict(attachments=[dict(one,environment=['TOKEN=first','TOKEN=last'])]), {}, dict(feature='action.tool_sources.attach',reason='unsatisfiable')),
        'duplicate-bare-env': (dict(attachments=[dict(one,environment=['TOKEN','TOKEN'])]), {}, dict(feature='action.tool_sources.attach',reason='unsatisfiable')),
        'missing-command': (dict(attachments=[dict(one,command='')]), {}, dict(kind='invalid_resolution')),
        'inline-tools': (dict(attachments=[one],tools=[{}]), {}, dict(feature='action.tools.provide',reason='unadvertised')),
        'tools-before-source-validation': (dict(attachments=[dict(one,id='')],tools=[{}]), {}, dict(feature='action.tools.provide',reason='unadvertised')),
        'participant-first': (dict(no_participant=True,attachments=[dict(one,id='')]), {}, dict(kind='invalid_participant')),
        'binding-first': (dict(reopen=True,native_id='',attachments=[dict(one,id='')]), {}, dict(feature='session.open.reopen',reason='unsatisfiable')),
        'source-before-reasoning': (dict(reasoning='high',attachments=[dict(one,kind='http')]), {}, dict(feature='action.tool_sources.attach',reason='unsatisfiable')),
    }
    return cases


def comparable(value):
    if isinstance(value, dict):
        return {key:comparable(item) for key,item in value.items() if key not in ['error_raw','descriptor','revision','admit']}
    if isinstance(value, list):
        return [comparable(item) for item in value]
    return value


def traces(observed, request):
    common = dict(protocol='open-agent-protocol',version='0.1',profile='open-agent-protocol.agent-control-core')
    revision = observed['revision']
    requests = request.get('requests',[request])
    for index,(item,result) in enumerate(zip(requests,observed['results'])):
        opened=result['open']
        if 'state' not in opened:
            continue
        payload=dict(session_id=item.get('session_id','session'))
        if item.get('attachments'):
            payload['tool_sources']=item['attachments']
        if item.get('reopen'):
            payload['reopen']=True
        frames=[dict(common,id='caps',type='capabilities.request',payload={}),dict(common,id='described',type='capabilities.response',in_reply_to='caps',capability_revision=revision,payload=observed['descriptor']),dict(common,id='open',type='session.open.request',capability_revision=revision,payload=payload),dict(common,id='opened',type='session.open.response',in_reply_to='open',session_id=payload['session_id'],capability_revision=revision,payload=opened['state'])]
        yield index,frames


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--output-dir',required=True,type=Path)
    output=parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True,exist_ok=False)
    peer.run(['cargo','build','--manifest-path','rust/Cargo.toml','-p','oap-adapter-acp','--example','attachments'],output/'rust-build.log')
    (output/'reference.go').write_text(GO)
    peer.run(['gofmt','-w',str(output/'reference.go')],output/'gofmt.log')
    peer.run(['go','build','-o',str(output/'reference'),str(output/'reference.go')],output/'go-build.log')
    peer.run(['go','build','-o',str(output/'goap'),'./go/cmd/goap'],output/'goap-build.log')
    peer.run(['cargo','build','--manifest-path','rust/Cargo.toml','-p','oap-validation','--example','validate'],output/'validator-build.log')
    agent=Path(__file__).with_name('fixtures')/'acp-attachments-agent.py'
    for name,(request,native,expected) in scenarios().items():
        case=output/name;case.mkdir()
        observed,wires={},{}
        for language,binary in [('rust',ROOT/'rust/target/debug/examples/attachments'),('go',output/'reference')]:
            directory=case/language;directory.mkdir()
            (directory/'native.json').write_text(json.dumps(native))
            data=dict(environment=[],python=sys.executable,agent=str(agent),case=str(directory));data.update(copy.deepcopy(request))
            (directory/'input.json').write_text(json.dumps(data,ensure_ascii=False,indent=2))
            try:
                observed[language]=json.loads(peer.run([str(binary)],directory/'result.json',json.dumps(data)))
                if (directory/'pids').exists():
                    assert all(peer.absent(int(pid)) for pid in (directory/'pids').read_text().splitlines()),language+' native child survived'
            finally:
                if (directory/'pids').exists():
                    for pid in (directory/'pids').read_text().splitlines():
                        if not peer.absent(int(pid)):
                            os.kill(int(pid),signal.SIGKILL)
            wires[language]=[json.loads(line) for line in (directory/'wire.jsonl').read_text().splitlines()] if (directory/'wire.jsonl').exists() else []
        assert settings.results.normalized(comparable(observed['rust']))==settings.results.normalized(comparable(observed['go'])),(name,observed)
        assert observed['rust']['descriptor']['features']['action.tool_sources.attach']==observed['go']['descriptor']['features']['action.tool_sources.attach'],(name,observed)
        assert wires['rust']==wires['go'],(name,wires)
        for result in observed['rust']['results']:
            failure=result['open'].get('failure')
            assert result['admit']==failure,(name,result)
            if expected is not None:
                assert failure and all(failure.get(key)==value for key,value in expected.items()),(name,result)
                assert not observed['rust']['spawned'] and not observed['go']['spawned'],name
            else:
                assert failure is None and observed['rust']['spawned'],(name,result)
        validated = 0
        for index,frames in traces(observed['rust'],request):
            validated += 1
            path=case/f'trace-{index}.json';path.write_text(json.dumps(frames,ensure_ascii=False,indent=2))
            for language,command in [('go',[str(output/'goap'),'validate']),('rust',[str(ROOT/'rust/target/debug/examples/validate')]),('zig',[str(ROOT/'zig/zig-out/bin/oxap'),'validate'])]:
                peer.run(command+[str(path)],case/f'validate-{index}-{language}.log')
        if name=='captured':
            host=[item['frame'] for item in wires['rust'] if item['from']=='host']
            requested=next(item for item in host if item.get('method')=='session/new')['params']['mcpServers']
            assert requested==[dict(name='filesystem',command='/usr/local/bin/mcp-filesystem',args=['--root','/workspace'],env=[dict(name='MCP_TOKEN',value='operator-secret'),dict(name='MCP_MODE',value='readonly')])],requested
        disposition = f'{validated} actual state exchanges pass three validators' if validated else 'preflight refusal, no native startup' if expected is not None else 'cancelled after actual native MCP request, no Session published'
        print(f'PASS {name}: Go/Rust public outcome and native wire agree; {disposition}; admission consistent; native children reaped',flush=True)
    peer.run(['cargo','test','--manifest-path','rust/Cargo.toml','-p','oap-adapter-acp','--test','attachments','--test','attachment_cases','--','--nocapture'],output/'integration.log')
    print(f'PASS ACP per-open MCP attachments. Only clocks/staged revision normalized; raw diagnostic wording retained. Evidence: {output}',flush=True)


if __name__=='__main__':
    main()
