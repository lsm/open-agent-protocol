import json,os,sys,time
from pathlib import Path
out=Path(sys.argv[1])
mode=sys.argv[2]
(out/'pid').write_text(str(os.getpid()))
def read():
 line=sys.stdin.readline()
 if line:
  with (out/'wire.jsonl').open('a') as wire: wire.write(line)
 return json.loads(line) if line else None
def send(value):
 line=json.dumps(dict(jsonrpc='2.0',**value))
 with (out/'native.jsonl').open('a') as wire: wire.write(line+'\n')
 print(line,flush=True)
def wait(name):
 deadline=time.monotonic()+10
 while not (out/name).exists() and time.monotonic()<deadline: time.sleep(.001)
 assert (out/name).exists(),name
def chunk(text): send(dict(method='session/update',params=dict(sessionId='native',update=dict(sessionUpdate='agent_message_chunk',content=dict(type='text',text=text)))))
def ask(name): send(dict(id=name,method='session/request_permission',params=dict(sessionId='native',toolCall=dict(toolCallId=name,title='Act'),options=[dict(optionId='allow',name='Allow',kind='allow_once')])))
request=read()
send(dict(id=request['id'],result=dict(protocolVersion=1,agentCapabilities={})))
request=read()
send(dict(id=request['id'],result=dict(sessionId='native')))
prompt=read()
if prompt is not None:
 chunk('before')
 if mode=='auto':
  send(dict(id=prompt['id'],result=dict(stopReason='end_turn')))
 else:
  if mode=='gates': ask('initial')
  cancel=read()
  assert cancel==dict(jsonrpc='2.0',method='session/cancel',params=dict(sessionId='native')),cancel
  (out/'cancel').write_text(json.dumps(cancel))
  if mode=='gates':
   (out/'gate-answer.json').write_text(json.dumps(read()))
   wait('late')
   ask('late')
   (out/'late-answer.json').write_text(json.dumps(read()))
  wait('stream')
  chunk('after')
  wait('finish')
  if mode in ['remote','failure']: send(dict(id=prompt['id'],error=dict(code=-32800 if mode=='remote' else -32000,message='cancel acknowledged')))
  else: send(dict(id=prompt['id'],result=dict(stopReason='end_turn' if mode=='complete' else 'cancelled',usage=dict(inputTokens=3,outputTokens=1,totalTokens=4))))
  if mode=='exit':
   wait('exit')
   sys.exit(0)
 while True:
  request=read()
  if request is None: break
  if mode=='auto' and request.get('method')=='session/prompt':
   chunk('next')
   wait('second-finish')
   send(dict(id=request['id'],result=dict(stopReason='end_turn')))

