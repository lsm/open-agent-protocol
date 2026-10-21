#!/usr/bin/env python3
"""Require optimized Python to reject missing content and inconsistent merge trees."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

SCRIPT = Path(__file__).with_name('journey-merges.py')
WRAPPER = '''#!/usr/bin/env python3
import os, subprocess, sys
real_git = REAL_GIT
mode = MODE
arguments = sys.argv[1:]
if 'merge-tree' not in arguments:
    os.execv(real_git, [real_git, *arguments])
result = subprocess.run([real_git, *arguments], capture_output=True, text=True, timeout=30)
if result.returncode:
    sys.stdout.write(result.stdout)
    sys.stderr.write(result.stderr)
    sys.exit(result.returncode)
def git(*args, input=None):
    return subprocess.run([real_git, *args], input=input, capture_output=True,
                          text=True, check=True, timeout=30).stdout.strip()
tree = result.stdout.splitlines()[0]
git('read-tree', tree)
skill = '.claude/skills/verify/SKILL.md'
if mode == 'paths':
    git('update-index', '--force-remove', '.claude/skills/verify/journeys/synthetic-alpha.md')
elif mode == 'content':
    blob = git('hash-object', '-w', '--stdin', input='Changed shared skill\\n')
    git('update-index', '--cacheinfo', '100644', blob, skill)
elif arguments[-2:] == ['beta', 'alpha']:
    blob = git('rev-parse', tree + ':' + skill)
    git('update-index', '--cacheinfo', '100755', blob, skill)
print(git('write-tree'))
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-dir', required=True)
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    real_git = shutil.which('git')
    if real_git is None:
        raise RuntimeError('git is required')
    failures = []
    messages = {
        'paths': 'Merged journey paths changed',
        'content': 'Merged journey content changed',
        'trees': 'Merge orders produced different trees',
    }
    with tempfile.TemporaryDirectory(prefix='oap-journey-mutations-') as temporary:
        wrapper = Path(temporary) / 'git'
        environment = {'PATH': temporary + os.pathsep + os.environ['PATH'], 'HOME': temporary}
        for mode, message in messages.items():
            wrapper.write_text(WRAPPER.replace('REAL_GIT', repr(real_git)).replace('MODE', repr(mode)))
            wrapper.chmod(0o755)
            result = subprocess.run([sys.executable, '-O', str(SCRIPT), '--output-dir', str(output / mode)],
                                    env=environment, capture_output=True, text=True, timeout=60)
            content = result.stdout + result.stderr
            (output / f'{mode}.log').write_text(content + f'\nEXIT {result.returncode}\n')
            if result.returncode == 0 or message not in content:
                failures.append(mode)
                print(f'FAIL: optimized proof did not reject {mode}: exit {result.returncode}')
            else:
                print(f'PASS: optimized proof rejected {mode} with {message!r}')
    if failures:
        raise RuntimeError(f'Mutations survived: {", ".join(failures)}; see {output}')
    print(f'PASS: all three mutations caught; artifacts: {output}; temporary wrappers removed')


if __name__ == '__main__':
    main()
