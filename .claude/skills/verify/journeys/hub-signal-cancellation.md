## Refused HTTP body interrupted by a signal

Use the shared toolchain/build setup in `SKILL.md`, then run:

```sh
bash .claude/skills/verify/scripts/hub-signal-cancellation.sh /tmp/oap-hub-signal-<unique-name>
```

The script uses an empty temporary HOME/data/config and one Go scheduler thread
to expose startup ordering races. It repeats the actual loopback daemon test
under race detection, with its remaining writer held until the refusal arrives;
require exit 0 and `PASS`. Inspect `signal.txt` for a complete 403 refusal while
some body bytes have been written and some remain unwritten, followed by SIGINT,
writer termination and a closed listener. These are client write counts, not
claims about bytes consumed by the daemon. Every test owns and reaps its daemon;
the script removes the temporary HOME. No provider or authentication is used.
