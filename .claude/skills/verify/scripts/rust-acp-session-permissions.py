import argparse
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time

ROOT=Path(__file__).resolve().parents[4]
spec=importlib.util.spec_from_file_location('outbound',Path(__file__).with_name('rust-acp-outbound.py'))
outbound=importlib.util.module_from_spec(spec)
spec.loader.exec_module(outbound)
AGENT='''import json,os,sys,time
from pathlib import Path
mode,directory=sys.argv[1:]
out=Path(directory)
(out/'pid').write_text(str(os.getpid()))
def read():
 line=sys.stdin.buffer.readline()
 with (out/'wire.jsonl').open('ab') as wire: wire.write(line)
 return json.loads(line)
def send(value): print(json.dumps(dict(jsonrpc='2.0',**value)),flush=True)
request=read()
send(dict(id=request['id'],result=dict(protocolVersion=1,agentCapabilities={})))
request=read()
send(dict(id=request['id'],result=dict(sessionId='native')))
request=read()
assert request['params']['prompt']==[dict(type='text',text='synthetic')]
send(dict(id='r'*(4*1024*1024),method='session/request_permission',params=dict(sessionId='native',toolCall=dict(toolCallId='tool',title='Act'),options=[dict(optionId='allow',name='Allow',kind='allow_once')])))
if mode.startswith('resolve'):
 partial=sys.stdin.buffer.read(64)
 assert len(partial)==64
 (out/'partial-write').write_bytes(partial)
(out/'ready').touch()
time.sleep(60)
'''


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--output-dir',required=True,type=Path)
    output=parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True,exist_ok=False)
    built=subprocess.run(['cargo','build','--manifest-path','rust/Cargo.toml','-p','oap-adapter-acp','--example','session_permission_ownership'],cwd=ROOT,capture_output=True,text=True,timeout=180)
    (output/'build.log').write_text(built.stdout+built.stderr)
    assert built.returncode==0,built.stderr
    with tempfile.TemporaryDirectory(prefix='oap-session-permission-owner-') as temporary:
        agent=Path(temporary)/'agent.py'
        agent.write_text(AGENT)
        for mode in ['resolve-cancel','resolve-close','pending-close','drop']:
            case=output/mode
            case.mkdir()
            child=subprocess.Popen([str(ROOT/'rust/target/debug/examples/session_permission_ownership'),mode,sys.executable,'-u',str(agent),mode,str(case)],cwd=ROOT,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,start_new_session=True)
            observations=[]
            communicated=False
            try:
                observations.append(outbound.read(child))
                before,raw=outbound.ownership.threads(child.pid)
                (case/'threads-before.txt').write_text(raw)
                outbound.send(child)
                observations.append(outbound.read(child))
                opened,raw=outbound.ownership.threads(child.pid)
                (case/'threads-open.txt').write_text(raw)
                outbound.send(child)
                observations.append(outbound.read(child))
                deadline=time.monotonic()+10
                while not (case/'ready').exists() and time.monotonic()<deadline: time.sleep(.005)
                assert (case/'ready').exists(),'native did not reach its gate/write boundary'
                outbound.send(child)
                result=outbound.read(child)
                observations.append(result)
                assert outbound.ownership.absent(int((case/'pid').read_text())),'native survived Session cleanup'
                deadline=time.monotonic()+5
                after,raw=outbound.ownership.threads(child.pid)
                while after!=before and time.monotonic()<deadline:
                    time.sleep(.01)
                    after,raw=outbound.ownership.threads(child.pid)
                (case/'threads-after.txt').write_text(raw)
                assert after==before,(before,opened,after)
                assert result['cleanup_ms']<3000 and result['poll_us']<50000,result
                outbound.send(child)
                stdout,stderr=child.communicate(timeout=5)
                communicated=True
                (case/'stderr.txt').write_text(stderr)
                assert child.returncode==0,(stdout,stderr)
                (case/'observed.json').write_text(json.dumps(dict(observations=observations,threads=dict(before=before,opened=opened,after=after)),indent=2))
                print(f'PASS {mode}: normal Session gate/write, child reaped, joined OS threads {before}->{opened}->{after}',flush=True)
            finally:
                if child.poll() is None: os.killpg(child.pid,signal.SIGKILL)
                if not communicated:
                    stdout,stderr=child.communicate(timeout=5)
                    (case/'failure-stdout.txt').write_text(stdout)
                    (case/'stderr.txt').write_text(stderr)
                    (case/'failure-observations.json').write_text(json.dumps(observations,indent=2))
                if (case/'pid').exists() and not outbound.ownership.absent(int((case/'pid').read_text())):
                    os.kill(int((case/'pid').read_text()),signal.SIGKILL)
    print(f'PASS normal Session permission ownership. Synthetic native inputs only. Evidence {output}',flush=True)


if __name__=='__main__':
    main()
