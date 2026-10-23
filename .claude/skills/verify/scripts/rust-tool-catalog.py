#!/usr/bin/env python3
"""Prove descriptor catalog integrity at three public validator boundaries."""
import argparse
import copy
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / 'rust/validation/src/state/tool_sources.rs'
IDS = {
    'descriptor-duplicate-source-id', 'descriptor-duplicate-tool-name',
    'tools-descriptor-dangling-source', 'descriptor-source-id-repeated-across-layers',
    'descriptor-layer-tool-dangling-source', 'descriptor-tool-without-source',
}


def run(command, output, label):
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=900)
    (output / (label + '.stdout')).write_text(result.stdout)
    (output / (label + '.stderr')).write_text(result.stderr)
    return result


def build(language, output, label):
    command = {
        'go': ['go', 'build', '-o', str(output / 'goap'), './go/cmd/goap'],
        'rust': ['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oap-validation', '--example', 'validate'],
        'zig': ['zig', 'build', '--build-file', 'zig/build.zig', '-Doptimize=ReleaseSafe'],
    }[language]
    if run(command, output, label).returncode:
        raise RuntimeError(label + ': build failed; compiler errors are not mutation catches')


def cases():
    result = {}
    for entry in json.loads((ROOT / 'fixtures/manifest.json').read_text())['fixtures']:
        if entry['id'] not in IDS:
            continue
        trace = json.loads((ROOT / 'fixtures' / entry['path']).read_text())
        result[entry['id']] = (trace, sorted(entry['codes']))
        if entry['valid']:
            continue
        repaired = copy.deepcopy(trace)
        p = repaired[1]['payload']
        if 'source-id' in entry['id']:
            if 'layers' in p:
                p['layers']['a']['sources'][0]['id'] = 'another'
            else:
                p['sources'][-1]['id'] = 'another'
        elif 'tool-name' in entry['id']:
            p['layers']['action']['tools'][0]['name'] = 'another'
        else:
            tools = p.get('tools') or p['layers']['z']['tools']
            tools[0]['source'] = 'native'
        result['repair-' + entry['id']] = (repaired, [])
    valid = copy.deepcopy(result['descriptor-tool-without-source'][0])
    p = valid[1]['payload']
    p['layers']['z']['tools'][0]['source'] = 'native'
    result['layer-tool-resolves-top-source'] = (copy.deepcopy(valid), [])
    p['layers']['a'] = {'sources': p.pop('sources')}
    result['layer-tool-resolves-other-layer'] = (copy.deepcopy(valid), [])
    p['layers']['z']['tools'].append(dict(p['layers']['z']['tools'][0], name='READ_FILE'))
    result['case-sensitive-names'] = (copy.deepcopy(valid), [])
    p['sources'] = [dict(p['layers']['a']['sources'][0], id='Native')]
    result['case-sensitive-source-ids'] = (copy.deepcopy(valid), [])
    combined = copy.deepcopy(valid)
    combined[1]['payload']['sources'][0]['id'] = 'native'
    combined[1]['payload']['layers']['z']['tools'][1].update(name='read_file', source='ghost')
    result['all-three-defects'] = (combined, ['duplicate_tool_name', 'duplicate_tool_source', 'unmatched_tool_source'])
    repeated = copy.deepcopy(valid)
    request, response = copy.deepcopy(repeated[0]), copy.deepcopy(repeated[1])
    request['id'] = 'again'
    response.update(id='again-response', in_reply_to='again')
    repeated.extend([request, response])
    result['repeat-valid-descriptor'] = (repeated, [])
    return result


def replay(language, output, label, case):
    trace, codes = case
    path = output / (label + '.input.json')
    path.write_text(json.dumps(trace, indent=2) + '\n')
    command = {
        'go': [str(output / 'goap'), 'validate', '--format', 'json'],
        'rust': [str(ROOT / 'rust/target/debug/examples/validate')],
        'zig': [str(ROOT / 'zig/zig-out/bin/oapx'), 'validate', '--format', 'json'],
    }[language]
    result = run(command + [str(path)], output, label)
    data = json.loads(result.stdout)
    reports = data if isinstance(data, list) else data.get('reports', [data])
    ds = [d for report in reports for d in report.get('diagnostics') or []]
    got = {'exit': result.returncode, 'diagnostics': sorted([d['code'], d.get('index', 0)] for d in ds),
           'phases': sorted({d['phase'] for d in ds})}
    want = {'exit': int(bool(codes)), 'diagnostics': sorted([c, 1] for c in codes),
            'phases': ['semantic'] if codes else []}
    return got, want


def mutate(output, corpus):
    mutations = [
        ('source-uniqueness', 'if let Some(id) = duplicate_source {', 'if let Some(id) = None::<&str> {', 'descriptor-source-id-repeated-across-layers'),
        ('tool-uniqueness', 'if let Some(name) = duplicate_name {', 'if let Some(name) = None::<&str> {', 'descriptor-duplicate-tool-name'),
        ('source-membership', '!declared.contains(tool.source.as_str())', 'false', 'descriptor-layer-tool-dangling-source'),
        ('absent-source-unknown', '!tool.source.is_empty()', 'true', 'descriptor-tool-without-source'),
        ('layer-sources', '.chain(p.layers.values().flat_map(|layer| &layer.sources))', '.chain(std::iter::empty())', 'layer-tool-resolves-other-layer'),
        ('layer-tools', '.chain(p.layers.values().flat_map(|layer| &layer.tools))', '.chain(std::iter::empty())', 'descriptor-layer-tool-dangling-source'),
        ('diagnostic-index', 'codes::DUPLICATE_TOOL_SOURCE, at, e,', 'codes::DUPLICATE_TOOL_SOURCE, (at.0 + 1, at.1), e,', 'descriptor-duplicate-source-id'),
    ]
    original = SOURCE.read_bytes()
    catches = []
    try:
        for name, before, after, target in mutations:
            text = original.decode()
            if text.count(before) != 1:
                raise RuntimeError(name + ': target not unique')
            SOURCE.write_text(text.replace(before, after, 1))
            build('rust', output, name + '-build')
            got, want = replay('rust', output, 'mutation-' + name, corpus[target])
            if got == want or got['exit'] not in (0, 1) or any(p != 'semantic' for p in got['phases']):
                raise RuntimeError(name + ': survived or inconclusive: ' + str(got))
            catches.append({'mutation': name, 'got': got, 'want': want})
            print('CAUGHT ' + name, flush=True)
    finally:
        SOURCE.write_bytes(original)
        build('rust', output, 'restored-build')
    (output / 'mutations.json').write_text(json.dumps(catches, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-dir', required=True)
    parser.add_argument('--mutation-check', action='store_true')
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    corpus = cases()
    for language in ('go', 'rust', 'zig'):
        build(language, output, language + '-build')
    if args.mutation_check:
        mutate(output, corpus)
    results, wrong = [], []
    for name, case in corpus.items():
        for language in ('go', 'rust', 'zig'):
            got, want = replay(language, output, language + '.' + name, case)
            results.append({'case': name, 'language': language, 'got': got, 'want': want})
            if got != want:
                wrong.append(language + '.' + name + ': ' + str(got) + ' != ' + str(want))
    (output / 'results.json').write_text(json.dumps(results, indent=2) + '\n')
    if wrong:
        raise RuntimeError('\n'.join(wrong))
    print(f'PASS {len(corpus)} traces x 3 validators; 0 disagreements; {output}')


if __name__ == '__main__':
    main()
