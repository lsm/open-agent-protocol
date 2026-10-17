# Zig 0.17.0 Downstream Migration Guide

OAP's Zig runtime (`.makai`, executable `oapx`) and its vendored zigzag now require Zig 0.17.0. Update the compiler, CI pins, and package metadata together. The product version in `zig/build.zig.zon` is independent of the compiler version.

This migration preserves OAP envelopes, provider policies, public runtime boundaries, and the supported `compat.*` I/O abstractions documented in [the 0.16 guide](zig-0.16.0-downstream-migration.md). It does not require downstream applications to expose `std.Io` in their constructors.

## Build and dependency changes

Use Zig 0.17.0 with `zig/build.zig`. From the repository root:

```sh
zig version
zig build --build-file zig/build.zig
zig build --build-file zig/build.zig test
zig build --build-file zig/build.zig test-unit-core
```

`zigzag` remains vendored at `zig/vendor/zigzag`; this migration updates that copy's compiler compatibility without changing its product version. The main package and vendor manifest both declare the new compiler floor.

The build passes arguments with `Run.addPassthruArgs()`. Downstream build scripts should replace `b.args` forwarding with that API. `b.root` is now a `std.Build.Cache.Path`, not the old `b.build_root` directory; use its path-aware operations rather than assuming a `.handle` field. Build string helpers return immutable slices: allocate mutable copies before editing generated option names.

The build explicitly tracks `providers/catalog.json`, `providers/resolved_urls.json`, and all seven `harnesses/*.json` inputs using `dependOnFileContents`. A configuration reading additional files must register them too. macOS SDK discovery observes `SDKROOT` or `xcrun`, so it explicitly poisons configuration caching to avoid stale SDK paths. Pure configurations for other targets retain caching. The framework search path is explicit; the removed `b.sysroot` field is no longer assigned.

CI toolchain pins use 0.17.0, and Zig cache keys include the compiler version and runner architecture. Do not reuse benchmark baselines across toolchain versions as though compiler changes were application performance changes.

## Source compatibility

- Array multiplication (`a ** n`) is removed. Single-value arrays use `@splat` with a known array type. Repeated strings use a compile-time helper that copies the whole byte pattern, preserves UTF-8, and provides a trailing zero sentinel. Repeating `"é"`, `"漢"`, or `"😀"` must not become a splat of an individual UTF-8 byte.
- `std.ascii.indexOfIgnoreCase` becomes `std.ascii.findIgnoreCase`.
- `Uri.getHost` becomes `std.Io.net.HostName.fromUri`; OAP maps host-validation failures to its existing invalid-provider-URL error and retains its loopback/TLS checks.
- Struct reflection uses parallel `field_names`, `field_types`, and related arrays. Enum reflection also uses `field_names` and `field_values`; runtime compilation identified four additional enum loops beyond the initial struct audit.
- `std.StaticBitSet` becomes `std.bit_set.Static`, and `.initFull()` becomes `.full`.
- Prefer `std.lang.Optimize` and the `debug`, `safe`, `fast`, and `small` tags.
- The compat layer uses `@Int` instead of the removed `std.meta.Int` and the new networking operation interface for socket writes.

Deprecated `std.fmt.allocPrint`/`allocPrintSentinel`, `@intFromEnum`/`@enumFromInt`, and builtin target shortcut fields are not all removed by this migration. They still compile in 0.17.0 and can be cleaned up in focused follow-ups. New enum conversions must use the exact backing integer type. The five audited OAP `@bitCast` calls are integer-to-integer conversions, unaffected by the new array/vector logical-bit semantics.

## Zig 0.17.0 regression handling

The shipped `std.Io.net.Stream.read` attempts to destructure `ReadResult` as a tuple and fails when instantiated. OAP's `compat.net.Stream.readSome` uses `readWithControl` with an empty control buffer and reads `data_len` instead. This retains a single socket read without modifying the compiler.

The other published regressions include response-file Run-step use cases, weak/strong symbol selection with some self-hosted linkers, soft-float compiler-rt, simple panic, and SPIR-V. Do not infer that a green cross-compilation validates execution on the target OS. Keep Windows stdio/hub and platform smoke jobs enabled. Apple Silicon builds retain the normal LLVM backend; this upgrade does not force `-fno-llvm`.

## Validation and developer tooling

Run all 17 CI unit groups, mock protocol/fullstack tests, PTY/TUI, Windows stdio/hub, and all six release-target builds. Set `OAP_SDK_BINARY_PATH` explicitly for SDK binary smoke coverage; without it those tests may skip. The existing macOS Keychain behavior means credential-persistence binary smoke belongs on Linux, as described in `CLAUDE.md`.

Check configuration invalidation by reusing the cache, changing each of the nine JSON inputs independently, and confirming the configurer runs again. Restore input bytes after each check; changing only timestamps is not a content change.

Use a Zig 0.17-compatible ZLS. At the October 17 migration research checkpoint, build integration had landed in ZLS master but the 0.17 stable release was not yet available, and dependency-module resolution still had limitations. Verify navigation and diagnostics for the zigzag dependency separately from successful CLI builds.

Incremental compilation in 0.17 primarily targets x86_64 Linux with `-fincremental --watch`. Do not expect the same incremental rebuild support on Apple Silicon macOS. New SafeAllocator checks exposed a queued hub event borrowing a run ID from a released session. Queued and replayed events now own their run ID until consumed or discarded. The existing close-and-drain test and allocation-failure sweeps cover this boundary. Two allocation-failure sweeps use the repository-recommended `smp_allocator` backing: SafeAllocator remap behavior made their allocation counts nondeterministic; FailingAllocator still injects failures and checks allocated/freed byte counts. Other hub tests retain the testing allocator.

## Sources

- [Zig 0.17.0 release notes](https://ziglang.org/download/0.17.0/release-notes.html)
- [Release announcement, October 2, 2026](https://ziglang.org/news/0.17.0-released/)
- [Zig downloads and patch-release availability](https://ziglang.org/download/index.json)
- [ZLS build integration and remaining limitations](https://github.com/zigtools/zls/pull/3270)
- [ZLS release tracking](https://github.com/zigtools/zls/issues/3267)
