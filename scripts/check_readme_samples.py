#!/usr/bin/env python3
"""Type-check every ``python`` block in sdk/python/README.md (#182).

Nothing compiled the README, so its samples rotted silently: the package rename
left thirty-five ``makai.`` attribute accesses that no longer resolve, and the
suite stayed green. Each block is written to a temp file and the set is judged
by mypy against the installed SDK. Errors are reported at their README line,
not the temp file's.

Samples are type-checked, never run: several spawn a runtime.

Needs the SDK installed with its dev extras, which is what the Python CI job
already does before running mypy.
"""

from __future__ import annotations

import ast
import re
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
README = ROOT / "sdk" / "python" / "README.md"
FENCE = "```python"


def extract(markdown: str) -> list[tuple[int, list[str]]]:
    blocks: list[tuple[int, list[str]]] = []
    start: int | None = None
    body: list[str] = []
    for index, line in enumerate(markdown.split("\n"), start=1):
        stripped = line.strip()
        if start is None and stripped == FENCE:
            start, body = index + 1, []
        elif start is not None and stripped == "```":
            blocks.append((start, body))
            start = None
        elif start is not None:
            body.append(line)
    if start is not None:
        sys.exit(f"check_readme_samples: FAIL: unterminated {FENCE} block at line {start - 1}")
    return blocks


IMPORTS = "import asyncio\nimport os\n\nimport oap_sdk\n"

# The names the prose establishes before a fragment, with the types it
# establishes them as, so an attribute reached through one is checked against
# the SDK rather than against Any.
ESTABLISHED = {
    "client": "oap_sdk.MakaiClient",
    "model_ref": "str",
    "messages": "list[oap_sdk.ChatMessage]",
}


def bound_names(source: str) -> set[str]:
    """Names the block binds for itself, at any depth."""
    try:
        tree = ast.parse(source)
    except SyntaxError:
        try:
            tree = ast.parse("async def _outer() -> None:\n" + "\n".join(f"    {l}" for l in source.split("\n")))
        except SyntaxError:
            return set()
    names: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Name) and isinstance(node.ctx, ast.Store):
            names.add(node.id)
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            names.add(node.name)
        elif isinstance(node, ast.arg):
            names.add(node.arg)
        elif isinstance(node, ast.alias):
            names.add((node.asname or node.name).split(".")[0])
    return names


def preamble_for(source: str) -> str:
    bound = bound_names(source)
    declared = "".join(
        f"{name}: {annotation}\n" for name, annotation in ESTABLISHED.items() if name not in bound
    )
    return IMPORTS + declared


def needs_wrapping(source: str) -> bool:
    """True when the block is only invalid because it awaits at module level.

    Asked of the compiler rather than of a regex: an `await` nested in a `try`
    or an `if` is still top-level, and a pattern matching column zero says it is
    not. `compile` rather than `ast.parse`, because the parser builds the tree
    for a top-level await happily and only compilation rejects it. A block that
    will not compile either way is left alone, so mypy reports the real syntax
    error instead of one shifted by an indent.
    """
    try:
        compile(source, "<sample>", "exec")
        return False
    except SyntaxError:
        pass
    try:
        compile(source, "<sample>", "exec", ast.PyCF_ALLOW_TOP_LEVEL_AWAIT)
        return True
    except SyntaxError:
        return False


def prepare(body: list[str]) -> tuple[str, int]:
    """Give a fragment the context the prose gives a reader.

    This README documents in fragments: a block shows the lines that matter and
    the sentence above it supplies the `async def`, the import, and a `client`.
    rustdoc has the same problem and wraps a block with no `fn main` in one.
    The preamble declares the three names the prose establishes, with their
    real types, so every attribute reached through them is checked against the
    SDK rather than against `Any`. A block that already stands alone is checked
    as written.

    Returns the sample text and the number of lines standing above the block's
    first line, which varies with how many established names the block binds
    for itself and with whether it had to be wrapped. The caller subtracts it
    to report an error at the README line rather than that many lines past it.
    """
    source = "\n".join(body) + "\n"
    preamble = preamble_for(source)
    if not needs_wrapping(source):
        prefix = preamble + "\n"
        return prefix + source, prefix.count("\n")
    indented = "\n".join(f"    {line}" if line.strip() else line for line in body)
    prefix = f"{preamble}\n\nasync def _sample() -> None:\n"
    return prefix + indented + "\n", prefix.count("\n")


def sdk_resolves(work: Path) -> bool:
    """Whether mypy can see real types behind `oap_sdk`, not `Any`.

    `--ignore-missing-imports` is what makes an absent SDK survivable, and it
    is also what makes this check worthless without one: the import becomes
    `Any`, every attribute reached through the declared `client` is allowed,
    and the run reports OK having checked nothing. That is the failure #182
    exists to prevent, one layer down, so it is asked directly rather than
    assumed from the CI job order.

    Asked of mypy rather than of `importlib`, because an installed package
    without a `py.typed` marker is `Any` to mypy while importing fine.

    Stated as what must be present rather than what must be absent. Absence is
    satisfied by a mypy that is missing, crashed, or has reworded its note --
    the dev extras pin `mypy>=1.11` with no upper bound -- and each of those
    would hand back a pass over an `Any` SDK, which is this check's own failure
    mode reappearing one level up.
    """
    probe = work / "probe_sdk.py"
    probe.write_text("import oap_sdk\n\nclient: oap_sdk.MakaiClient\nreveal_type(client)\n")
    seen = subprocess.run(
        [sys.executable, "-m", "mypy", "--no-error-summary", "--ignore-missing-imports", str(probe)],
        capture_output=True,
        text=True,
    )
    probe.unlink()
    return "MakaiClient" in seen.stdout


def main() -> int:
    blocks = extract(README.read_text())
    if not blocks:
        return fail(f"no {FENCE} blocks found — did the fence marker change?")

    with tempfile.TemporaryDirectory(prefix="oap-readme-samples-") as work:
        samples = Path(work)
        if not sdk_resolves(samples):
            return fail(
                "oap_sdk is Any to mypy, so every sample would pass without being checked — "
                "install the SDK with its dev extras first (pip install -e 'sdk/python[dev]')"
            )

        above: dict[int, int] = {}
        for start, body in blocks:
            text, offset = prepare(body)
            (samples / f"sample_{start}.py").write_text(text)
            above[start] = offset

        print(f"type-checking {len(blocks)} README samples against the installed oap_sdk...")
        result = subprocess.run(
            [
                sys.executable,
                "-m",
                "mypy",
                "--no-error-summary",
                "--ignore-missing-imports",
                str(samples),
            ],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            print("check_readme_samples: OK")
            return 0

        output = (result.stdout or "") + (result.stderr or "")
        located = re.sub(
            r"\S*sample_(\d+)\.py:(\d+)",
            lambda m: f"sdk/python/README.md:{int(m.group(1)) + int(m.group(2)) - 1 - above.get(int(m.group(1)), 0)} (sample at line {m.group(1)})",
            output,
        )
        print(located.strip(), file=sys.stderr)
        return fail("a README sample does not type-check")


def fail(message: str) -> int:
    print(f"check_readme_samples: FAIL: {message}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
