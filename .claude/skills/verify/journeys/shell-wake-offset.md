# Shell wake offsets

Run the tools unit group on the designated test host:

```sh
zig build --build-file zig/build.zig test-unit-tools -Doptimize=ReleaseSafe --summary all
```

The shell offset regression blocks the second line on a FIFO. A first rule
reports the first matching line and advances the observed offset through its
newline. The test then releases the writer and waits until the second line is
present before installing a later rule. That rule must report the second line,
even though that line was already written before the later rule was installed.
The first line must not match again. The test establishes the observed offset
through a real notice rather than assuming that a foreground budget expired
between two shell writes. It uses neither a timed inter-line gap nor a fixed sleep before
rule installation.

Require exit 0 and an explicit passing test count. The existing foreground
handoff tests separately exercise the initial output snapshot and job lifetime.
