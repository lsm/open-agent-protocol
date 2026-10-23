# Rust tool-list gates and response-local integrity

Run from the root:

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test tools_list
python3 .claude/skills/verify/scripts/rust-tools-list.py --mutation-check --output-dir /tmp/oap-tools-list-<unique-name>
python3 .claude/skills/verify/scripts/rust-tool-catalog.py --mutation-check --output-dir /tmp/oap-descriptor-catalog-<unique-name>
```

The tool-list replay uses the descriptor journey's shared build runner and sends
the same synthetic traces to public Go, Rust and Zig validators. An unadvertised
list requires its typed refusal with the feature and reason; degraded listings
require their own opt-in. An advertised, defect-free listing must be honoured.
Expectations belong to the correlated request, including overlapping opt-ins and
a refusal after a descriptor refresh. Neither the `action.tools` family key nor
its opt-in substitutes for `action.tools.list`.

Served catalogs must have unique source IDs and tool names and resolve each tool
to a source in that response. A gated response suppresses an absent attribution's
secondary diagnostic, as Go does, while still diagnosing an explicitly dangling
source. Valid catalogs, repairs and conforming refusals must pass; invalid traces
must agree on exact code multiplicity, semantic phase, response index and exit.

All sixteen tool-list source mutations and seven descriptor mutations must compile
and change the observed verdict. The location mutation changes only the diagnostic
index; the refresh mutation rejudges a pending refusal under the replacement
descriptor and must be caught. Sources are restored in `finally`, Rust rebuilt,
and the restored corpus replayed across all three validators. Raw output, inputs,
results and mutation catches remain in each artifact directory.

Run the shared baseline from `SKILL.md`, including the isolated terminal reply
and quit. Session catalog reconciliation, attachment lifetime, call attribution
and real authentication/provider behavior are outside this journey.
