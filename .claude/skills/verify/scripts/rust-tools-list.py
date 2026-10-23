#!/usr/bin/env python3
"""Replay correlated tool-list gates and response shape through public CLIs."""
import argparse
import copy
import importlib.util
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location('descriptor_journey', Path(__file__).with_name('rust-tool-catalog.py'))
common = importlib.util.module_from_spec(spec)
spec.loader.exec_module(common)
IDS = {
    'tools-list-unadvertised-served', 'tools-list-refused-advertised',
    'tools-list-degraded-without-optin', 'tools-list-ungated-wrong-refusal',
    'tools-list-ungated-wrong-feature', 'tools-list-ungated-wrong-reason',
    'tools-duplicate-name', 'tools-duplicate-source-id', 'tools-unmatched-source',
    'tools-unattributed-tool', 'tools-list-ungated-refused',
    'tools-list-degraded-optin', 'tools-list-degraded-refused',
    'tools-list-gated-dangling-source', 'tools-list-overlapping-optins',
    'tools-list-refusal-keeps-requested-gate', 'tools-list-overlapping-optins-refused',
}


def refusal(response, degraded=False):
    response['type'] = 'error.response'
    error = {'code': 'capability_degraded' if degraded else 'unsupported_feature',
             'message': 'synthetic refusal', 'details': {'feature': 'action.tools.list'}}
    if not degraded:
        error['details']['reason'] = 'unadvertised'
    response['payload'] = {'error': error}


def cases():
    result = {}
    for entry in json.loads((ROOT / 'fixtures/manifest.json').read_text())['fixtures']:
        name = entry['id']
        if name not in IDS:
            continue
        trace = json.loads((ROOT / 'fixtures' / entry['path']).read_text())
        at = 5 if name == 'tools-list-overlapping-optins' else 3
        expected = sorted([[code, at] for code in entry['codes']])
        result[name] = (trace, expected)
        if entry['valid']:
            continue
        repaired = copy.deepcopy(trace)
        p = repaired[-1]['payload']
        if name == 'tools-list-overlapping-optins':
            repaired[3]['payload']['allow_degraded_features'] = ['action.tools.list']
        elif name in ['tools-duplicate-name', 'tools-duplicate-source-id']:
            if name == 'tools-duplicate-name':
                p['tools'][-1]['name'] = 'another'
            else:
                p['sources'][-1]['id'] = 'another'
        elif name in ['tools-unmatched-source', 'tools-unattributed-tool']:
            p['tools'][0]['source'] = p['sources'][0]['id']
        elif name == 'tools-list-refused-advertised':
            repaired[3]['type'] = 'action.tools.list.response'
            repaired[3]['payload'] = {'sources': [], 'tools': []}
        else:
            refusal(repaired[-1], name == 'tools-list-degraded-without-optin')
        result['repair-' + name] = (repaired, [])
    base = result['repair-tools-unattributed-tool'][0]
    for label, mutate in [
        ('empty-catalog', lambda p: p.update(sources=[], tools=[])),
        ('case-sensitive-names', lambda p: p['tools'].append(dict(p['tools'][0], name=p['tools'][0]['name'].upper()))),
        ('unused-source', lambda p: p['sources'].append({'id':'unused','kind':'native'})),
    ]:
        trace = copy.deepcopy(base)
        mutate(trace[3]['payload'])
        result[label] = (trace, [])
    trace = copy.deepcopy(result['tools-list-degraded-optin'][0])
    trace[2]['payload']['allow_degraded_features'] = ['action.tools']
    result['family-key-is-not-list-optin'] = (trace, [['degraded_without_optin', 3]])
    trace = copy.deepcopy(result['tools-list-degraded-refused'][0])
    trace[3]['payload']['error']['details'].pop('feature')
    result['degraded-refusal-needs-feature'] = (trace, [['degraded_without_optin', 3]])
    trace = copy.deepcopy(result['tools-list-unadvertised-served'][0])
    trace[1]['payload']['features']['action.tools'] = {'level':'native'}
    result['family-key-is-not-list-support'] = (trace, [['unavailable_capability', 3]])
    trace = copy.deepcopy(result['tools-list-refused-advertised'][0])
    trace[3]['payload']['error'] = {'code':'internal','message':'synthetic refusal'}
    result['advertised-refusal-without-details'] = (trace, [['unhonoured_capability', 3]])
    return result


def replay(language, output, label, case):
    trace, expected = case
    path = output / (label + '.input.json')
    path.write_text(json.dumps(trace, indent=2) + '\n')
    command = {
        'go': [str(output / 'goap'), 'validate', '--format', 'json'],
        'rust': [str(ROOT / 'rust/target/debug/examples/validate')],
        'zig': [str(ROOT / 'zig/zig-out/bin/oapx'), 'validate', '--format', 'json'],
    }[language]
    result = common.run(command + [str(path)], output, label)
    data = json.loads(result.stdout)
    reports = data if isinstance(data, list) else data.get('reports', [data])
    ds = [d for report in reports for d in report.get('diagnostics') or []]
    got = {'exit':result.returncode, 'diagnostics':sorted([d['code'],d.get('index',0)] for d in ds),
           'phases':sorted({d['phase'] for d in ds})}
    want = {'exit':int(bool(expected)), 'diagnostics':expected, 'phases':['semantic'] if expected else []}
    return got, want


def mutation_check(output, corpus):
    controls = ROOT / 'rust/validation/src/state/controls.rs'
    shape = ROOT / 'rust/validation/src/state/tool_sources.rs'
    mutations = [
        ('support-gate', controls, 'tools_list_gate', 'if !affirmative(&level)', 'if false', 'tools-list-unadvertised-served'),
        ('degraded-optin', controls, 'tools_list_gate', '!p.allows_degraded(FEATURE_TOOLS_LIST)', 'false', 'tools-list-degraded-without-optin'),
        ('own-feature-optin', controls, 'tools_list_gate', 'p.allows_degraded(FEATURE_TOOLS_LIST)', 'p.allows_degraded("action.tools")', 'family-key-is-not-list-optin'),
        ('honour', controls, 'tools_list_gate', 'pending.honour = true;', 'pending.honour = false;', 'tools-list-refused-advertised'),
        ('served-gate', controls, 'tools_list_admission', 'if let Some(expectation) = expectation {', 'if let Some(expectation) = None::<Expectation> {', 'tools-list-unadvertised-served'),
        ('refusal-feature', controls, 'tools_list_refusal', '!expectation.conformed_by(&err)', 'false', 'tools-list-ungated-wrong-feature'),
        ('refusal-reason', controls, 'tools_list_refusal', '!expectation.conformed_by(&err)', 'false', 'tools-list-ungated-wrong-reason'),
        ('refusal-code', controls, 'tools_list_refusal', '!expectation.conformed_by(&err)', 'false', 'tools-list-ungated-wrong-refusal'),
        ('reply-correlation', controls, 'tools_list_admission', '.get(&reply)', '.values().find(|pending| pending.gated())', 'tools-list-overlapping-optins-refused'),
        ('retained-gate', controls, 'tools_list_refusal', 'let err = payload::<ErrorResponse>(e).error;', 'let err = payload::<ErrorResponse>(e).error;\n        let pending = if self.features.get(FEATURE_TOOLS_LIST) == Some(&SupportLevel::NATIVE) { PendingToolList { expectation: None, honour: true } } else { pending };', 'tools-list-refusal-keeps-requested-gate'),
        ('unique-list-sources', shape, 'check_tools_list_shape', 'if let Some(id) = list_duplicate_source {', 'if let Some(id) = None::<&str> {', 'tools-duplicate-source-id'),
        ('unique-list-names', shape, 'check_tools_list_shape', 'if let Some(name) = list_duplicate_name {', 'if let Some(name) = None::<&str> {', 'tools-duplicate-name'),
        ('listed-source-resolves', shape, 'check_tools_list_shape', 'else if !declared.contains(tool.source.as_str()) {', 'else if false {', 'tools-unmatched-source'),
        ('listed-source-required', shape, 'check_tools_list_shape', 'if !gated {', 'if false {', 'tools-unattributed-tool'),
        ('gated-omission-suppressed', shape, 'check_tools_list_shape', 'if !gated {', 'if true {', 'tools-list-unadvertised-served'),
        ('diagnostic-location', controls, 'tools_list_admission', 'expectation.diagnostic,\n                at,', 'expectation.diagnostic,\n                (at.0 + 1, at.1),', 'tools-list-unadvertised-served'),
    ]
    originals = {p: p.read_bytes() for p in [controls, shape]}
    catches = []
    try:
        for name, path, method, before, after, target in mutations:
            for p, data in originals.items():
                p.write_bytes(data)
            text = originals[path].decode()
            marker = '    fn ' + method + '('
            if marker not in text:
                marker = '    pub(super) fn ' + method + '('
            start = text.index(marker)
            ends = [text.find(token, start + len(marker)) for token in ['\n    fn ', '\n    pub(super) fn ']]
            end = min([i for i in ends if i >= 0] or [len(text)])
            body = text[start:end]
            if body.count(before) != 1:
                raise RuntimeError(name + ': method target not unique')
            path.write_text(text[:start] + body.replace(before, after, 1) + text[end:])
            common.build('rust', output, name + '-build')
            got, want = replay('rust', output, 'mutation-' + name, corpus[target])
            if got == want or got['exit'] not in [0, 1] or any(p != 'semantic' for p in got['phases']):
                raise RuntimeError(name + ': survived or inconclusive: ' + str(got))
            catches.append({'mutation':name, 'got':got, 'want':want})
            print('CAUGHT ' + name, flush=True)
    finally:
        for path, data in originals.items():
            path.write_bytes(data)
        common.build('rust', output, 'restored-build')
    (output / 'mutations.json').write_text(json.dumps(catches, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-dir', required=True)
    parser.add_argument('--mutation-check', action='store_true')
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    corpus = cases()
    for language in ['go','rust','zig']:
        common.build(language, output, language + '-build')
    if args.mutation_check:
        mutation_check(output, corpus)
    results, wrong = [], []
    for name, case in corpus.items():
        for language in ['go','rust','zig']:
            got, want = replay(language, output, language + '.' + name, case)
            results.append({'case':name,'language':language,'got':got,'want':want})
            if got != want:
                wrong.append(language + '.' + name + ': ' + str(got) + ' != ' + str(want))
    (output / 'results.json').write_text(json.dumps(results, indent=2) + '\n')
    if wrong:
        raise RuntimeError('\n'.join(wrong))
    print(f'PASS {len(corpus)} traces x 3 validators; 0 disagreements; {output}')


if __name__ == '__main__':
    main()
