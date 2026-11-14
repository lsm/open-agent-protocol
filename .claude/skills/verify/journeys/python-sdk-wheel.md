# Python SDK wheel journey

After changing the Python SDK's packaging, name or version, run from the
repository root:

```sh
python3 .claude/skills/verify/scripts/python-sdk-wheel.py --output-dir /tmp/oxap-python-wheel-<unique-name>
```

The script builds the wheel from `sdk/python` with pip, then installs it into a
fresh venv with `--no-index` and imports it in isolated mode from outside the
checkout. The wheel must be `oxap_sdk-0.1.0a33-py3-none-any.whl`, hold only
`oxap_sdk/` (with `py.typed`) and its `dist-info`, and the installed
distribution `oxap-sdk` must report the same version as `oxap_sdk.__version__`.
No `oap_sdk` module may be importable. Inspect `wheel-contents.json` and
`probe.json`. Building needs pip to fetch `hatchling`; nothing is published.
