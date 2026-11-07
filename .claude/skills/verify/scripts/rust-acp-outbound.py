import argparse
import importlib.util
import json
import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location('ownership', Path(__file__).with_name('rust-acp-session-bridge.py'))
ownership = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ownership)
AGENT = '''import json, os, sys, time
from pathlib import Path
mode, directory = sys.argv[1:]
out=Path(directory)
(out/'pid').write_text(str(os.getpid()))
def send(value): print(json.dumps(dict(jsonrpc='2.0',**value)),flush=True)
def read():
    line=sys.stdin.buffer.readline()
    with (out/'wire.jsonl').open('ab') as stream: stream.write(line)
    return json.loads(line)
request=read()
send(dict(id=request['id'],result=dict(protocolVersion=1,agentCapabilities={})))
send(dict(id='native',method='fixture/request',params={}))
if mode in ['complete','error']:
    reply=read()
    assert reply['id']=='native',reply
    if mode=='complete': assert reply['result']==dict(accepted=True),reply
    else: assert reply['error']==dict(code=-32601,message='synthetic refusal',data=dict(reason='fixture')),reply
    assert read()==dict(jsonrpc='2.0',method='fixture/done'), 'notification mismatch'
    sys.stdin.buffer.read()
else:
    partial=sys.stdin.buffer.read(64)
    assert len(partial)==64,'EOF before the outbound write'
    (out/'partial-write').write_bytes(partial)
    (out/'ready').touch()
    time.sleep(60)
'''


def read(child):
    ready, _, _ = select.select([child.stdout], [], [], 10)
    assert ready, 'consumer failed to reach its public boundary'
    return json.loads(child.stdout.readline())


def send(child):
    child.stdin.write('continue\n')
    child.stdin.flush()


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--output-dir',required=True,type=Path)
    output=parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True,exist_ok=False)
    built=subprocess.run(['cargo','build','--manifest-path','rust/Cargo.toml','-p','oap-adapter-acp','--example','outbound'],cwd=ROOT,capture_output=True,text=True,timeout=180)
    (output/'build.log').write_text(built.stdout+built.stderr)
    assert built.returncode==0,built.stderr
    with tempfile.TemporaryDirectory(prefix='oap-outbound-agent-') as temporary:
        agent=Path(temporary)/'agent.py'
        agent.write_text(AGENT)
        for mode in ['complete','error','notify-cancel','reply-cancel','error-cancel','notify-close','reply-close','notify-drop','reply-drop','notify-deadline','queue']:
            case=output/mode
            case.mkdir()
            child=subprocess.Popen([str(ROOT/'rust/target/debug/examples/outbound'),mode,sys.executable,'-u',str(agent),mode,str(case)],cwd=ROOT,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,start_new_session=True)
            observations=[]
            communicated=False
            try:
                observations.append(read(child))
                before, raw=ownership.threads(child.pid)
                (case/'threads-before.txt').write_text(raw)
                send(child)
                observations.append(read(child))
                opened, raw=ownership.threads(child.pid)
                (case/'threads-open.txt').write_text(raw)
                send(child)
                if mode not in ['complete','error']:
                    observations.append(read(child))
                    deadline=time.monotonic()+10
                    while not (case/'ready').exists() and time.monotonic()<deadline: time.sleep(.005)
                    assert (case/'ready').exists(),'native did not observe the blocked write'
                    send(child)
                result=read(child)
                observations.append(result)
                pid=int((case/'pid').read_text())
                assert ownership.absent(pid),'native child survived completion/cleanup'
                deadline=time.monotonic()+5
                after,raw=ownership.threads(child.pid)
                while after!=before and time.monotonic()<deadline:
                    time.sleep(.01)
                    after,raw=ownership.threads(child.pid)
                (case/'threads-after.txt').write_text(raw)
                assert after==before,(before,opened,after)
                if mode not in ['complete','error']:
                    assert result['poll_us']<50000,result
                    assert result['cleanup_ms']<3000,result
                    assert result['queue_full']==(mode=='queue'),result
                send(child)
                stdout,stderr=child.communicate(timeout=5)
                communicated=True
                (case/'stderr.txt').write_text(stderr)
                assert child.returncode==0,(stdout,stderr)
                (case/'observed.json').write_text(json.dumps(dict(observations=observations,threads=dict(before=before,opened=opened,after=after)),indent=2))
                print(f'PASS {mode}: actual native write, reaped child, joined OS threads {before}->{opened}->{after}',flush=True)
            finally:
                if child.poll() is None:
                    os.killpg(child.pid,signal.SIGKILL)
                if not communicated:
                    stdout,stderr=child.communicate(timeout=5)
                    (case/'failure-stdout.txt').write_text(stdout)
                    (case/'stderr.txt').write_text(stderr)
                    (case/'failure-observations.json').write_text(json.dumps(observations,indent=2))
                if (case/'pid').exists() and not ownership.absent(int((case/'pid').read_text())):
                    os.kill(int((case/'pid').read_text()),signal.SIGKILL)
    print(f'PASS owned ACP outbound bridge. Synthetic stdio only. Evidence {output}',flush=True)


if __name__=='__main__':
    main()
