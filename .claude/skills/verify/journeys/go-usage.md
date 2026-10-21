## Go usage consumer journey

After changing Go's token usage types or JSON mapping, run:

```sh
python3 .claude/skills/verify/scripts/go-usage.py --output-dir /tmp/oap-go-usage-<unique-name>
```

The script runs a standalone Go consumer importing the public `protocol.Usage`
type. Unknown counts must remain absent, independently measured zero counters
must remain `0`, and the maximum `uint64` total must survive a JSON round trip.
It retains the consumer source, input, output and stderr for inspection. No
provider, credentials or GUI is used. The baseline includes this journey.
