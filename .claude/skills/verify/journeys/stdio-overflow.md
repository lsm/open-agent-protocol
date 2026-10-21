# Stdio overflow and its run driver

After changing bounded stdio fan-out or its regression tests, run the public
consumer and uncached race stress from the repository root:

```sh
python3 .claude/skills/verify/scripts/stdio-overflow.py --output-dir /tmp/oap-stdio-overflow-<unique-name>
python3 .claude/skills/verify/scripts/stdio-overflow-stress.py --output-dir /tmp/oap-overflow-stress-<unique-name>
```

The first script builds a standalone consumer of the public Go hub and stdio
server APIs. It uses memory only, with a temporary HOME and no credentials.
It holds the stdio pipe unread while one healthy driver completes one run.
Inspect `wire.jsonl`: `oap-overflow` must name that session and run, and the last
delivered cursor. `result.json` must show that the healthy reader received
terminal sequence 12 and a second newline-JSON `events` request using the
reported cursor recovered terminal 12. This distinguishes
an explicitly overflowed slow subscription from a lost production terminal.
The source, binary, build output and actual stdio lines are retained.

The stress script compiles the real regression with `-race`, then runs it 200
times at each `GOMAXPROCS` value 1, 2, 4 and 8, both without load and with two
CPU workers. Every invocation must exit 0; `summary.json` and per-case logs retain
the evidence. CPU workers and timed-out children are terminated and reaped. For
a stronger bounded run like the one that reproduced #1393 on this 18-CPU Mac:

```sh
python3 .claude/skills/verify/scripts/stdio-overflow-stress.py --output-dir /tmp/oap-overflow-heavy-<unique-name> --count 200 --workers 18 --gomaxprocs 1 2 8 32
```

The regression must drive one run and then drain its queued stdio lines. Starting
another run before reading each queued line creates unrelated driver
subscriptions: one can join a previous run after its terminal and then see normal
EOF when that source closes. A mailbox overflow instead has a typed overflow
error and a resumable cursor. Increasing queues or suppressing that error does
not test the intended behavior. The local baseline includes the public consumer;
CI's Go race suite runs the regression. No live harness or GUI is covered.
