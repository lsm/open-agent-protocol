import argparse, importlib.util, json, os, shutil, tempfile, uuid
from pathlib import Path
ROOT=Path(__file__).resolve().parents[4]
spec=importlib.util.spec_from_file_location('driver', ROOT/'scripts/tui-pty-driver.py')
driver=importlib.util.module_from_spec(spec); spec.loader.exec_module(driver)
parser=argparse.ArgumentParser(); parser.add_argument('--output-dir', required=True); options=parser.parse_args()
out=Path(options.output_dir).resolve(); out.mkdir(parents=True,exist_ok=False)
with tempfile.TemporaryDirectory(prefix='oap-copilot-context-') as tmp:
 root=Path(tmp).resolve(); home=root/'home'; data=home/'.oapx'; cache=data/'model_catalog'; cache.mkdir(parents=True)
 binary=root/'oapx'; shutil.copy2(ROOT/'zig/zig-out/bin/oapx',binary)
 (data/'auth.json').write_text(json.dumps({'github-copilot': {'api_key':'synthetic-test-value-not-a-real-credential'}}))
 (data/'config.json').write_text(json.dumps({'model':'claude-sonnet-4.6','provider':'github-copilot','api':'openai-completions','workspace':str(root)}))
 metadata={'data':[{'id':'claude-sonnet-4.6','name':'Synthetic Copilot Claude','model_picker_enabled':True,'capabilities':{'type':'chat','limits':{'max_prompt_tokens':200000,'max_context_window_tokens':1000000,'max_output_tokens':128000}},'billing':{'token_prices':{'default':{'context_max':200000},'long_context':{'context_max':872000}}}}]}
 (cache/'github-copilot.json').write_text(json.dumps(metadata)); (out/'synthetic-models.json').write_text(json.dumps(metadata,indent=2))
 profile=root/'isolation.sb'; profile.write_text('(version 1) (allow default)\n(deny network*)\n(deny mach-lookup (global-name-regex #".*[Ss]ecurity.*"))\n'+f'(deny file-read* (subpath {json.dumps(str(Path.home()))}))\n')
 environment={'PATH':'/usr/bin:/bin','HOME':str(home),'TMPDIR':str(root),'TERM':'xterm-256color','OAPX_KEYCHAIN_SERVICE':'oap-verify-'+str(uuid.uuid4())}
 os.environ.clear(); os.environ.update(environment)
 session=None
 try:
  args=argparse.Namespace(binary='/usr/bin/sandbox-exec',width=150,height=35,fixture_text='')
  session=driver.PtySession(args,home=str(home),use_fixture=False,cwd=str(root),argv=['-f',str(profile),str(binary)])
  session.wait_for(driver.WELCOME_MARKER,15,'welcome',since=0); session.quiesce(.3)
  for command,marker in [('/context','context window: 200000'),('/context 872k','context window: 872000'),('/context 900k','takes at most 872000 context tokens'),('/context default','context window: 200000'),('/context 200k','context window: 200000'),('/quit','')]:
   since=len(session.plain); session.type_text(command); session.send(b'\r',command)
   if marker: session.wait_for(marker.encode(),10,command,since=since); session.quiesce(.3)
  assert session.wait_exit(10)==0
  print('PASS: real Copilot TUI selected 200K and metadata-provided 872K, refused 900K, restored default 200K; exit 0; isolated synthetic catalog, no live provider')
 finally:
  if session:
   (out/'transcript.bin').write_bytes(b''.join(chunk for _,chunk in session.chunks)); (out/'screen.txt').write_bytes(session.screen_text()); (out/'plain.txt').write_bytes(session.plain); session.close()
