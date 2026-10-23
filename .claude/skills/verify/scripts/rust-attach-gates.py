#!/usr/bin/env python3
"""Replay attachment gates, limits and compound-open ownership at public CLIs."""
import argparse
import copy
import importlib.util
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location('descriptor_journey', Path(__file__).with_name('rust-tool-catalog.py'))
common = importlib.util.module_from_spec(spec)
spec.loader.exec_module(common)
KEY = 'action.tool_sources.attach'
IDS = {
 'open-attach-unadvertised','open-unadvertised-wrong-reason','open-attach-remote-unadvertised',
 'open-attach-remote-wrong-refusal','open-attach-without-session-open-mode-refused',
 'attach-advertised-without-modes','open-attach-without-session-open-mode',
 'open-degraded-attach-without-optin','open-attach-colliding-source-admitted',
 'tool-source-collision-wrong-refusal','tool-source-over-limit-unnamed-source',
 'tool-source-over-limit-untyped-refusal','tool-source-attach-refused-within-limits',
 'tool-source-attach-refused-at-its-limit','open-attach-process-source','open-attach-layered-capability',
 'open-attach-unadvertised-rejected','open-attach-remote-unadvertised-rejected',
 'tool-source-remote-unadvertised-attach','tool-source-collision-refused',
 'tool-source-collision-unadvertised-attach','tool-source-attach-refused-over-limit',
 'open-degraded-attach-optin','open-degraded-attach-refused',
 'attach-refusal-discharges-provided-tools','attach-capability-before-provided-owner',
 'attach-and-provide-have-one-honour-owner','attach-transport-refusal-names-source',
 'attach-above-disclosed-limit-admitted','attach-request-source-id-repeated',
 'attach-preserves-unknown-provided-pattern-boundary',
}


def caps(trace):
    return next(e for e in trace if e['type'] == 'capabilities.response')


def request(trace):
    return next(e for e in trace if e['type'] == 'session.open.request')


def reply(trace):
    return next(e for e in trace if e.get('in_reply_to') == request(trace)['id'])


def refuse(trace, code='unsupported_feature', reason='unadvertised', source=None):
    response = reply(trace)
    details = {'feature':KEY}
    if reason:
        details['reason'] = reason
    if source is not None:
        details['source'] = source
    response['type'] = 'error.response'
    response['payload'] = {'error':{'code':code,'message':'synthetic refusal','details':details}}


def admit(trace):
    asked = request(trace)['payload']
    response = reply(trace)
    response['type'] = 'session.open.response'
    sources = copy.deepcopy(caps(trace)['payload'].get('sources', []))
    sources += [{k:v for k,v in a.items() if k not in ['command','args','environment']} for a in asked.get('tool_sources',[])]
    response['payload'] = {'session_id':asked['session_id'],'status':'idle','sources':sources}


def cases():
    result = {}
    for entry in json.loads((ROOT / 'fixtures/manifest.json').read_text())['fixtures']:
        name = entry['id']
        if name not in IDS:
            continue
        trace = json.loads((ROOT / 'fixtures' / entry['path']).read_text())
        expected = []
        for code in entry['codes']:
            at = trace.index(caps(trace)) if code == 'undisclosed_attach_modes' else trace.index(reply(trace))
            expected.append([code, at])
        result[name] = (trace, sorted(expected))
        if entry['valid']:
            continue
        fixed = copy.deepcopy(trace)
        if 'undisclosed_attach_modes' in entry['codes']:
            caps(fixed)['payload']['features'][KEY]['level'] = 'unavailable'
            if any(e['type'] == 'session.open.request' for e in fixed):
                refuse(fixed)
        elif 'undisclosed_attach_limit' in entry['codes']:
            admit(fixed)
        elif 'duplicate_tool_source' in entry['codes']:
            refuse(fixed,reason='unsatisfiable',source=request(fixed)['payload']['tool_sources'][0]['id'])
        elif name == 'open-degraded-attach-without-optin':
            refuse(fixed,code='capability_degraded',reason=None)
        elif name in ['tool-source-over-limit-unnamed-source','tool-source-over-limit-untyped-refusal']:
            p = request(fixed)['payload']
            maximum = caps(fixed)['payload']['features'][KEY]['limits']['max_sources']
            refuse(fixed,reason='unsatisfiable',source=p['tool_sources'][maximum]['id'])
        elif name in ['open-attach-remote-unadvertised','open-attach-remote-wrong-refusal','attach-transport-refusal-names-source']:
            refuse(fixed,reason='unsatisfiable',source=request(fixed)['payload']['tool_sources'][0]['id'])
        else:
            refuse(fixed)
        result['repair-' + name] = (fixed, [])
    trace = copy.deepcopy(result['open-degraded-attach-optin'][0])
    request(trace)['payload']['allow_degraded_features'] = ['action.tools.provide']
    result['wrong-feature-optin'] = (trace, [['degraded_without_optin',3]])
    trace = copy.deepcopy(result['open-attach-process-source'][0][:4])
    caps(trace)['payload']['features'][KEY]['modes'].append('remote')
    request(trace)['payload']['tool_sources'][0]['kind'] = 'remote'
    caps(trace)['payload']['features'][KEY].pop('limits',None)
    admit(trace)
    result['remote-mode-and-source-admitted'] = (trace, [])
    trace = copy.deepcopy(result['open-attach-process-source'][0][:4])
    request(trace)['payload']['tool_sources'] = []
    caps(trace)['payload']['features'].pop(KEY)
    reply(trace)['payload']['sources'] = caps(trace)['payload']['sources']
    result['empty-array-does-not-elect-attach'] = (trace, [])
    trace = copy.deepcopy(result['attach-and-provide-have-one-honour-owner'][0])
    reply(trace)['payload']['error'] = {'code':'session_exists','message':'synthetic open-level refusal'}
    result['open-level-refusal-discharges-both'] = (trace, [])
    trace = copy.deepcopy(result['open-attach-unadvertised-rejected'][0])
    last = trace.pop()
    update = copy.deepcopy(trace[0])
    update.update(type='capabilities.updated',id='updated',capability_revision='tools-v2',sequence=1,payload={'previous_revision':'tools-v1'})
    asked = copy.deepcopy(trace[0]);asked['id']='refresh'
    refreshed = copy.deepcopy(caps(trace));refreshed.update(id='refreshed',in_reply_to='refresh',capability_revision='tools-v2')
    refreshed['payload']['features'][KEY]={'level':'native','modes':['session_open']}
    trace.extend([update,asked,refreshed,last])
    result['refusal-retains-request-time-capability'] = (trace, [])
    return result


def replay(language, output, label, case):
    trace, expected = case
    path = output / (label + '.input.json');path.write_text(json.dumps(trace,indent=2)+'\n')
    command = {
      'go':[str(output/'goap'),'validate','--format','json'],
      'rust':[str(ROOT/'rust/target/debug/examples/validate')],
      'zig':[str(ROOT/'zig/zig-out/bin/oapx'),'validate','--format','json'],
    }[language]
    result = common.run(command+[str(path)],output,label)
    data = json.loads(result.stdout);reports = data if isinstance(data,list) else data.get('reports',[data])
    ds=[d for report in reports for d in report.get('diagnostics') or []]
    got={'exit':result.returncode,'diagnostics':sorted([d['code'],d.get('index',0)] for d in ds),'phases':sorted({d['phase'] for d in ds})}
    want={'exit':int(bool(expected)),'diagnostics':expected,'phases':['semantic'] if expected else []}
    return got,want


def main():
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--output-dir',required=True);args=parser.parse_args()
    output=Path(args.output_dir).resolve();output.mkdir(parents=True,exist_ok=False);corpus=cases()
    for language in ['go','rust','zig']:common.build(language,output,language+'-build')
    results,wrong=[],[]
    for name,case in corpus.items():
        for language in ['go','rust','zig']:
            got,want=replay(language,output,language+'.'+name,case);results.append({'case':name,'language':language,'got':got,'want':want})
            if got!=want:wrong.append(language+'.'+name+': '+str(got)+' != '+str(want))
    (output/'results.json').write_text(json.dumps(results,indent=2)+'\n')
    if wrong:raise RuntimeError('\n'.join(wrong))
    print(f'PASS {len(corpus)} traces x 3 validators; 0 disagreements; {output}')


if __name__=='__main__':main()
