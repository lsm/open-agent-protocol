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

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location('outbound', Path(__file__).with_name('rust-acp-outbound.py'))
outbound = importlib.util.module_from_spec(spec)
spec.loader.exec_module(outbound)
AGENT = '''import json, os, sys, time
from pathlib import Path
out=Path(sys.argv[1])
(out/'pid').write_text(str(os.getpid()))
request=json.loads(sys.stdin.buffer.readline())
print(json.dumps(dict(jsonrpc='2.0',id=request['id'],result=dict(protocolVersion=1,agentCapabilities={}))),flush=True)
print(json.dumps(dict(jsonrpc='2.0',id='r'*(4*1024*1024),method='session/request_permission',params=dict(sessionId='native',toolCall=dict(toolCallId='tool',title='Act'),options=[dict(optionId='allow',name='Allow',kind='allow_once')]))),flush=True)
partial=sys.stdin.buffer.read(64)
assert len(partial)==64,partial
(out/'partial-write').write_bytes(partial)
(out/'ready').touch()
time.sleep(60)
'''


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--output-dir',required=True,type=Path)
    output=parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True,exist_ok=False)
    built=subprocess.run(['cargo','build','--manifest-path','rust/Cargo.toml','-p','oap-adapter-acp','--example','permission_controller_ownership'],cwd=ROOT,capture_output=True,text=True,timeout=180)
    (output/'build.log').write_text(built.stdout+built.stderr)
    assert built.returncode==0,built.stderr
    with tempfile.TemporaryDirectory(prefix='oap-permission-owner-') as temporary:
        agent=Path(temporary)/'agent.py'
        agent.write_text(AGENT)
        for mode in ['resolve-cancel','settle-cancel','queued-cancel','resolve-close','settle-close']:
            case=output/mode
            case.mkdir()
            child=subprocess.Popen([str(ROOT/'rust/target/debug/examples/permission_controller_ownership'),mode,sys.executable,'-u',str(agent),str(case)],cwd=ROOT,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,start_new_session=True)
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
                assert (case/'ready').exists(),'native did not observe the blocked permission reply'
                outbound.send(child)
                result=outbound.read(child)
                observations.append(result)
                assert outbound.ownership.absent(int((case/'pid').read_text())),'native survived cleanup'
                deadline=time.monotonic()+5
                after,raw=outbound.ownership.threads(child.pid)
                while after!=before and time.monotonic()<deadline:
                    time.sleep(.01)
                    after,raw=outbound.ownership.threads(child.pid)
                (case/'threads-after.txt').write_text(raw)
                assert after==before,(before,opened,after)
                assert result['poll_us']<50000 and result['cleanup_ms']<3000,result
                outbound.send(child)
                stdout,stderr=child.communicate(timeout=5)
                communicated=True
                (case/'stderr.txt').write_text(stderr)
                assert child.returncode==0,(stdout,stderr)
                (case/'observed.json').write_text(json.dumps(dict(observations=observations,threads=dict(before=before,opened=opened,after=after)),indent=2))
                print(f'PASS {mode}: native response bytes observed, nonblocking poll, child reaped, threads {before}->{opened}->{after}',flush=True)
            finally:
                if child.poll() is None: os.killpg(child.pid,signal.SIGKILL)
                if not communicated:
                    stdout,stderr=child.communicate(timeout=5)
                    (case/'failure-stdout.txt').write_text(stdout)
                    (case/'stderr.txt').write_text(stderr)
                    (case/'failure-observations.json').write_text(json.dumps(observations,indent=2))
                if (case/'pid').exists() and not outbound.ownership.absent(int((case/'pid').read_text())):
                    os.kill(int((case/'pid').read_text()),signal.SIGKILL)
    print(f'PASS owned permission controller. Synthetic native inputs only. Evidence {output}',flush=True)


if __name__=='__main__':
    main()
