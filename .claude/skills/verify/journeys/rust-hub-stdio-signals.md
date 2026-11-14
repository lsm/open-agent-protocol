# Signal the Rust stdio hub

Use shared setup from `SKILL.md`, then run

```sh
cargo test --manifest-path rust/Cargo.toml -p oxap-serve --test stdio_signal --test endpoint_signal
```

`stdio_signal` drives the real `oap-rust-hub serve --stdio` binary with stdin
held open. Require both tests to pass. With a live memory run, SIGINT must write
`oap-rust-hub: shutting down gracefully; still active: s/<run>; 0 queued run(s)
lost; signal again to force` to stderr, a later `open` must be refused
`shutting_down`, and SIGTERM must write `oap-rust-hub: forced shutdown; cut
off: s/<run>` and exit 0. With nothing running, one SIGTERM writes the graceful
line naming `none` and exits 0. `endpoint_signal` must still pass, since the
binary's signal relay is shared with `serve agent`.

Two local effects make these process tests time out at the first answer
without any hub defect. On macOS the first launches of a freshly linked binary
can take several seconds, longer than the tests' five-second wait, so build
`oap-rust-hub` and launch it once before running them. When Cargo's
`build-dir` is shared by several checkouts of this repository, their builds of
the same workspace overwrite each other's intermediates; point
`CARGO_BUILD_BUILD_DIR` and `CARGO_TARGET_DIR` at directories this run owns,
in the environment only, and remove them afterwards.
