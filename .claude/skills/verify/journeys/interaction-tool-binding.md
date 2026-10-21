## Interaction tool-binding consumer journey

After changing interaction binding (Decision 0055), run:

```sh
python3 .claude/skills/verify/scripts/interaction-binding.py --output-dir /tmp/oap-interaction-binding-<unique-name>
```

It replays the shared user-input fixture through `goap validate`, Rust's public
`validate` example and the rebuilt `oapx validate`. All three must reject the
resolution that moves `t1` to `t9` with only semantic `scope_mismatch` and exit 1.
Matching `t1`, omitting the optional resolution binding, and an unbound
interaction must pass with exit 0. Inputs and diagnostic JSON are retained.
The baseline runner includes this journey.
