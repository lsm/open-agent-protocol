#!/usr/bin/env python3
"""Prove independent journey additions merge in either order without an index edit."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]
SKILL = Path('.claude/skills/verify')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-dir', required=True)
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix='oap-journey-merges-') as temporary:
        repository = Path(temporary) / 'repository'
        repository.mkdir()
        environment = {
            'PATH': os.environ['PATH'],
            'HOME': temporary,
            'GIT_CONFIG_NOSYSTEM': '1',
            'GIT_CONFIG_GLOBAL': '/dev/null',
            'GIT_AUTHOR_NAME': 'Journey merge proof',
            'GIT_AUTHOR_EMAIL': 'journeys@example.invalid',
            'GIT_COMMITTER_NAME': 'Journey merge proof',
            'GIT_COMMITTER_EMAIL': 'journeys@example.invalid',
        }

        def git(*arguments, check=True):
            result = subprocess.run(['git', '-c', 'core.hooksPath=/dev/null', *arguments],
                                    cwd=repository, env=environment, text=True,
                                    capture_output=True, timeout=30)
            if check and result.returncode:
                raise RuntimeError(f'git {arguments}: {result.stdout}{result.stderr}')
            return result

        target = repository / SKILL
        target.mkdir(parents=True)
        shutil.copy2(ROOT / SKILL / 'SKILL.md', target / 'SKILL.md')
        journeys = ROOT / SKILL / 'journeys'
        if journeys.is_dir():
            shutil.copytree(journeys, target / 'journeys')
        original = {path.relative_to(repository): path.read_text()
                    for path in target.rglob('*.md')}
        git('init', '-q', '-b', 'base')
        git('add', '.')
        git('commit', '-qm', 'verify: Record current journey layout')
        additions = {}
        for name in ('alpha', 'beta'):
            git('checkout', '-q', '-b', name, 'base')
            content = f'## Synthetic {name} journey\n\nObserve the {name} result.\n'
            path = SKILL / 'journeys' / f'synthetic-{name}.md'
            additions[path] = content
            if journeys.is_dir():
                (repository / path).write_text(content)
            else:
                with (target / 'SKILL.md').open('a') as document:
                    document.write('\n' + content)
            git('add', '.')
            git('commit', '-qm', f'verify: Add {name} journey')

        trees = []
        failures = []
        for first, second in (('alpha', 'beta'), ('beta', 'alpha')):
            result = git('merge-tree', '--write-tree', first, second, check=False)
            (output / f'{first}-{second}.txt').write_text(
                result.stdout + result.stderr + f'\nEXIT {result.returncode}\n')
            print(f'git merge-tree --write-tree {first} {second}: EXIT {result.returncode}')
            if result.returncode:
                failures.append(f'{first} + {second}')
                continue
            tree = result.stdout.splitlines()[0]
            expected = original | additions
            actual = set(git('ls-tree', '-r', '--name-only', tree).stdout.splitlines())
            if actual != {str(path) for path in expected}:
                raise RuntimeError(f'Merged journey paths changed: {actual}')
            for path, content in expected.items():
                if git('show', f'{tree}:{path}').stdout != content:
                    raise RuntimeError(f'Merged journey content changed: {path}')
            trees.append(tree)
        if failures:
            raise RuntimeError(f'Journey additions conflict: {", ".join(failures)}; see {output}')
        if len(set(trees)) != 1:
            raise RuntimeError(f'Merge orders produced different trees: {trees}')
    print('PASS: both merge orders retain every journey and leave SKILL.md unchanged')
    print(f'Artifacts: {output}; temporary repository removed; children stopped')


if __name__ == '__main__':
    main()
