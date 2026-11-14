import argparse
import copy
import importlib.util
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location('cancel', Path(__file__).with_name('rust-acp-cancel.py'))
cancel = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cancel)
peer = cancel.peer

GO = r'''package main
import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "sync/atomic"
 "time"
 base "github.com/lsm/oxap/go/adapter"
 "github.com/lsm/oxap/go/adapter/acp"
 "github.com/lsm/oxap/go/protocol"
)
type input struct { Python string; Agent string; Directory string; Mode string }
type clock struct{}
func (clock) Now() time.Time { return time.UnixMilli(1000) }
type ids struct { n atomic.Uint64 }
func (i *ids) NewID(kind string) string { return fmt.Sprintf("%s-%d",kind,i.n.Add(1)) }
func check(err error) { if err!=nil {panic(err)} }
func must[T any](value T, err error) T { check(err);return value }
func collect(stream base.EventStream) ([]protocol.Envelope,error) {
 events:=[]protocol.Envelope{}
 for event:=range stream {if event.Error!=nil {return events,event.Error};events=append(events,event.Envelope)}
 return events,nil
}
func next(stream base.EventStream) protocol.Envelope {event,ok:=<-stream;if !ok {panic("early end")};check(event.Error);return event.Envelope}
func replay(ctx context.Context,s base.Session,id protocol.RunID,after uint64) (any,base.EventStream) {
 recovery,stream,err:=s.Resume(ctx,base.ResumeRequest{RunID:id,AfterSequence:after});check(err)
 return map[string]any{"state":recovery.State,"run_id":recovery.RunID,"requested_after":recovery.RequestedAfter,"replayed_from":recovery.ReplayedFrom,"replayed_through":recovery.ReplayedThrough},stream
}
func submit(ctx context.Context,s base.Session) (protocol.MessageSubmitResponse,base.EventStream) {
 admission,stream,err:=s.Submit(ctx,base.SubmitRequest{Request:protocol.MessageSubmitRequest{SessionID:"session",Messages:[]protocol.Message{{Role:protocol.RoleUser,Content:protocol.TextContent("hello")}}}});check(err);return admission,stream
}
func idle(ctx context.Context,s base.Session) {deadline:=time.Now().Add(10*time.Second);for must(s.State(ctx)).Status!=protocol.SessionIdle {if time.Now().After(deadline){panic("run did not settle")};time.Sleep(time.Millisecond)}}
func main() {
 var in input;check(json.NewDecoder(os.Stdin).Decode(&in));mode:=in.Mode
 ctx,stop:=context.WithTimeout(context.Background(),30*time.Second);defer stop()
 capacity:=256;if mode=="evicted"||mode=="cross-run"||mode=="overflow" {capacity=2};if mode=="default" {capacity=0}
 adapter:=must(acp.New(acp.Config{Executable:in.Python,Args:[]string{"-u",in.Agent,in.Directory},Environment:[]string{},WorkingDirectory:"/",QueueCapacity:1,JournalCapacity:capacity,Clock:clock{},IDs:&ids{},AbortActiveOnClose:true,ShutdownTimeout:time.Second}))
 descriptor:=must(adapter.Probe(ctx));session:=must(adapter.Open(ctx,base.OpenRequest{SessionID:"session",Participant:protocol.Participant{ID:"user"}}));defer session.Close(context.Background())
 opened:=must(session.State(ctx));admission,primary:=submit(ctx,session);id:=admission.RunID
 out:=map[string]any{"journal":map[string]any{"scope":descriptor.Journal.Scope,"persistence":descriptor.Journal.Persistence,"replay":descriptor.Journal.Replay,"capacity":descriptor.Journal.Capacity},"opened":opened,"admission":admission}
 release:=func(){check(os.WriteFile(filepath.Join(in.Directory,"release-1"),[]byte("go"),0600))}
 if mode=="active"||mode=="close"||mode=="overflow" {
  prefix:=[]protocol.Envelope{next(primary),next(primary)};after:=uint64(0);if mode=="active" {after=1}
  recovery,following:=replay(ctx,session,id,after);out["recovery"]=recovery
  if mode=="active" {
   first:=next(following);select {case event:=<-following:panic(fmt.Sprintf("early replay: %+v",event));default:}
   release();tail,err:=collect(primary);check(err);replayed,err:=collect(following);check(err)
   out["events"]=append(prefix,tail...);out["replayed"]=append([]protocol.Envelope{first},replayed...)
  } else if mode=="close" {
   err:=session.Close(ctx);if err!=nil {out["close_error"]=err.Error()}
   replayed,err:=collect(following);check(err);out["events"]=prefix;out["replayed"]=replayed
   _,_,err=session.Resume(ctx,base.ResumeRequest{RunID:id});if !errors.Is(err,base.ErrSessionClosed) {panic(err)}
  } else {
   release();idle(ctx,session);replayed,err:=collect(following);if !errors.Is(err,base.ErrEventStreamOverflow) {panic(fmt.Sprint("missing overflow: ",err))}
   out["events"]=prefix;out["replayed"]=replayed;out["overflow"]=true
  }
 } else {
  if mode=="large-close" {idle(ctx,session)}
  events,err:=collect(primary)
  if mode=="large-close" {
   if !errors.Is(err,base.ErrEventStreamOverflow) {panic(fmt.Sprint("missing overflow: ",err))}
   _,reference:=replay(ctx,session,id,0);expected,err:=collect(reference);check(err)
   recovery,following:=replay(ctx,session,id,0);check(session.Close(ctx));replayed,err:=collect(following);check(err)
   out["events"]=expected;out["replayed"]=replayed;out["recovery"]=recovery
  } else {
   check(err);_,_,err=session.Resume(ctx,base.ResumeRequest{RunID:"missing"});if !errors.Is(err,base.ErrRunNotFound){panic(err)}
   _,_,err=session.Resume(ctx,base.ResumeRequest{RunID:id,AfterSequence:4});if !errors.Is(err,base.ErrReplayCursorFuture){panic(err)}
   after:=uint64(1);if mode=="cross-run" {after=3;_,second:=submit(ctx,session);_,err:=collect(second);check(err)}
   if mode=="evicted"||mode=="cross-run" {
    _,_,err=session.Resume(ctx,base.ResumeRequest{RunID:id});var gap *base.ReplayGap;if !errors.As(err,&gap){panic(err)}
    out["gap"]=map[string]any{"requested_after":gap.RequestedAfter,"oldest_available":gap.OldestAvailable,"latest_available":gap.LatestAvailable}
   }
   recovery,following:=replay(ctx,session,id,after);replayed,err:=collect(following);check(err)
   _,ended:=replay(ctx,session,id,3);empty,err:=collect(ended);check(err);if len(empty)!=0 {panic("ended replay revived")}
   out["events"]=events;out["replayed"]=replayed;out["recovery"]=recovery
  }
 }
 check(session.Close(ctx))
 if mode=="reopen" {
  reopened:=must(adapter.Open(ctx,base.OpenRequest{SessionID:"session",Participant:protocol.Participant{ID:"user"},Reopen:true,NativeSessionID:"native-session"}))
  _,_,err:=reopened.Resume(ctx,base.ResumeRequest{RunID:id});if !errors.Is(err,base.ErrRunNotFound){panic(err)}
  out["reopened"]=must(reopened.State(ctx));check(reopened.Close(ctx))
 }
 check(json.NewEncoder(os.Stdout).Encode(out))
}
'''


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    peer.run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oap-adapter-acp', '--example', 'journal'], output/'rust-build.log')
    (output/'reference.go').write_text(GO)
    peer.run(['gofmt', '-w', str(output/'reference.go')], output/'gofmt.log')
    peer.run(['go', 'build', '-o', str(output/'reference'), str(output/'reference.go')], output/'go-build.log')
    peer.run(['go', 'build', '-o', str(output/'goap'), './go/cmd/goap'], output/'goap-build.log')
    peer.run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oap-validation', '--example', 'validate'], output/'validator-build.log')
    agent = Path(__file__).with_name('fixtures')/'acp-journal-agent.py'
    for mode in ['default', 'complete', 'evicted', 'cross-run', 'active', 'close', 'overflow', 'large-close', 'reopen']:
        case = output/mode
        case.mkdir()
        rust = case/'rust'
        rust.mkdir()
        previous = os.environ.get('OAP_ACP_CORPUS_EVIDENCE')
        os.environ['OAP_ACP_CORPUS_EVIDENCE'] = str(rust/'native')
        try:
            observed = json.loads(peer.run([str(ROOT/'rust/target/debug/examples/journal'), mode], rust/'result.json'))
        finally:
            if previous is None:
                os.environ.pop('OAP_ACP_CORPUS_EVIDENCE', None)
            else:
                os.environ['OAP_ACP_CORPUS_EVIDENCE'] = previous
            for pidfile in rust.rglob('pids'):
                for raw in pidfile.read_text().splitlines():
                    pid = int(raw)
                    if not peer.absent(pid):
                        os.kill(pid, signal.SIGKILL)
                        raise AssertionError('Rust left a native child alive')
        native = next((rust/'native').iterdir())
        go = case/'go'
        go.mkdir()
        shutil.copy(native/'native.jsonl', go/'native.jsonl')
        shutil.copy(native/'options.json', go/'options.json')
        request = dict(python=sys.executable, agent=str(agent), directory=str(go), mode=mode)
        try:
            reference = json.loads(peer.run([str(output/'reference')], go/'result.json', json.dumps(request)))
        finally:
            if (go/'pids').exists():
                for raw in (go/'pids').read_text().splitlines():
                    pid = int(raw)
                    if not peer.absent(pid):
                        os.kill(pid, signal.SIGKILL)
                        raise AssertionError('Go left a native child alive')
        comparable = {key:value for key,value in observed.items() if key not in ['descriptor','revision','close_error']}
        other = {key:value for key,value in reference.items() if key!='close_error'}
        assert cancel.results.normalized(comparable) == cancel.results.normalized(other), (mode, comparable, other)
        for filename in ['host.jsonl', 'agent.jsonl']:
            actual = [json.loads(line) for line in (native/filename).read_text().splitlines()]
            wanted = [json.loads(line) for line in (go/filename).read_text().splitlines()]
            assert actual == wanted, (mode, filename)
        frames = cancel.trace(observed)
        frames[2]['payload']['messages'] = [dict(role='user', content='hello')]
        path = case/'trace.json'
        path.write_text(json.dumps(frames, ensure_ascii=False, indent=2))
        for language, command in [('go',[str(output/'goap'),'validate']),('rust',[str(ROOT/'rust/target/debug/examples/validate')]),('zig',[str(ROOT/'zig/zig-out/bin/oxap'),'validate'])]:
            checked = subprocess.run(command+[str(path)], cwd=ROOT, capture_output=True, text=True, timeout=90)
            raw = checked.stdout + checked.stderr
            (case/f'validate-{language}.log').write_text(raw)
            if mode in ['close','overflow']:
                assert checked.returncode == 1 and 'missing_run_terminal' in raw, (mode, language, raw)
            else:
                assert checked.returncode == 0, (mode, language, raw)
        disposition = 'expected missing_run_terminal for the unconfirmed prefix' if mode in ['close','overflow'] else 'complete original trace accepted'
        print(f'PASS {mode}: actual Go/Rust recovery/events and parsed native frames agree, three validators: {disposition}, children reaped', flush=True)
    peer.run(['cargo','test','--manifest-path','rust/Cargo.toml','-p','oap-adapter-acp','--test','journal','--','--nocapture'], output/'integration.log')
    print(f'PASS ACP bounded journal/replay. Only clocks and staged revision normalized; raw close diagnostics retained. Evidence: {output}', flush=True)


if __name__ == '__main__':
    main()
