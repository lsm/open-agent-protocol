# Native OAP Implementers

This is the register named by section 6 of the
[stability commitment](STABILITY.md). After the first tagged v0.1 release,
everyone on it is sent any proposed breaking change to released core, as a
`proposed` decision record, at least 30 days before it can land — and their
response is recorded verbatim in that decision. Before that first tag, v0.1
remains a pre-release working contract.

**The register is empty today.** That is a statement of fact, not a filter:
nobody has been turned away, and no entry is pending.

## Who belongs here

Anyone shipping, or actively building, an endpoint that speaks OAP natively —
an implementation of the wire rather than an adapter maintained in this
repository. Being listed costs nothing, commits you to nothing, confers no
veto, and is not a conformance claim.

## How to add yourself

Open a pull request adding a row below. A contact route we can actually reach
is the only requirement; it does not have to be a personal address, and a
repository issue tracker is fine.

| Implementation | What it implements | Contact route | Added |
| --- | --- | --- | --- |
| _(none yet)_ | | | |

To remove yourself, open a pull request deleting your row. No reason is
required and none will be asked for.

## What being listed means in practice

- You are notified before a breaking change is decided, not after it lands.
- Your response is recorded in the decision even when we disagree with it, and
  the decision states what changed because of it.
- Silence does not block a decision. After 30 days it may proceed, recording
  who did not respond.
- Nothing here obliges you to keep implementing OAP, to track a version, or to
  respond at all.
