# Validator rules

The normative rule register for every behaviour a validator judges. A
diagnostic any implementation (Go, Zig, Rust) raises must trace to a row here,
and every row traces to the decision text that makes it a rule. No
implementation is the answer key (Decision 0032): where the decisions are
silent, or the implementations disagree, the behaviour is not a rule yet. It is
an open item in `research/validator-divergences.md`
until a decision settles it, and no port copies it in the meantime.

## Files

One file per decision group, named for its first decision, as
[`docs/validation-coverage.md`](../../docs/validation-coverage.md) groups them:

| File | Decisions |
| --- | --- |
| [`0001-core.md`](0001-core.md) | 0001, 0002 |
| [`0004-packs.md`](0004-packs.md) | 0004 |
| [`0005-run-controls.md`](0005-run-controls.md) | 0005, 0019, 0021, 0022, 0023, 0024, 0031, 0034 |
| [`0006-models.md`](0006-models.md) | 0006, 0014, 0028, 0035 |
| [`0007-queue-open-cancel.md`](0007-queue-open-cancel.md) | 0007, 0009, 0041 |
| [`0008-tool-sources.md`](0008-tool-sources.md) | 0008, 0011, 0048 |
| [`0010-amendments-auth.md`](0010-amendments-auth.md) | 0010, 0012, 0013, 0026, 0029, 0043 |
| [`0016-provider.md`](0016-provider.md) | 0016, 0030 (the `model-provider-core` profile) |

## Rows

| Column | Meaning |
| --- | --- |
| id | `<decision>#<heading anchor>/<n>`: the decision's `###` heading is the rule, and `/n` numbers the distinct obligations its text states |
| rule | the obligation, as one normative sentence |
| lines | the lines of the decision that state it, so a reader can check the sentence against its source |
| code | the diagnostic a trace breaking it draws; a validator conforms when the set of codes it raises equals the set a fixture declares, so how many diagnostics one fault yields is not part of any rule |
| Go, Zig, Rust | where each implementation judges it: a function on `main`, `yes` where one is known to and the function is not named, `` `fn` in #N (open) `` for a pull request not yet merged, or `—` |

One row may name several implementation sites when they judge one obligation at
different moments, such as an entry judged at the snapshot and a lead judged
when its admission lands.

A row written where a decision is silent says so in its lines column and is
proposed until a decision adopts it; an open item that a decision settles
becomes a row.

## Open items

Behaviour no text states, or that the trees judge differently, is an open item
rather than a row. Its id is `OPEN-<decision>-<n>`, numbered within the
decision whose text it falls under, and it is described in
[`research/validator-divergences.md`](../../research/validator-divergences.md)
with the options and a recommendation. A row that depends on one names it
in its lines or implementation cells; the decision that settles it names it in
its heading.
