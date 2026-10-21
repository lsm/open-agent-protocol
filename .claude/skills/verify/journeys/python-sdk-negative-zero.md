# Python SDK raw negative-zero journey

Run the standalone public consumer from the repository root:

```sh
python3 .claude/skills/verify/scripts/python-sdk-negative-zero.py --output-dir /tmp/oap-python-negative-zero-<unique-name>
```

The fixture inserts the caller's raw usage JSON without decoding or rewriting
its numbers. Each of the six counters supplied as `-0`, plus a mixed-count case,
must raise `MakaiProtocolError` with code `malformed_response` on provider unary,
provider streaming, agent unary and agent streaming APIs. Inspect each retained
raw input and public consumer output; a returned measured zero is a failure.
A tool-result control must retain ordinary numeric zero and the string `"-0"`,
including a nested application object named `usage`.

Run `python-sdk-usage.md` for absent, real zero and maximum-count controls, and
`usage-count-validation.md` for the shared raw-preserving corpus through all
three public validators with zero disagreements. Run the complete baseline too.
This checks fixture stdio traffic without credentials or live provider calls.
