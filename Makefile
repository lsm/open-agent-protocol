ZIG ?= zig
ARGS ?=
MAKAI_CODESIGN_IDENTITY ?=
ZIG_GLOBAL_CACHE := $(shell $(ZIG) env 2>/dev/null | sed -n 's/.*global_cache_dir[" ]*[:=] *"\([^"]*\)".*/\1/p')

.PHONY: help build tui test test-tui check clean clean-all

help:
	@echo "make build      build the makai CLI into zig/zig-out/bin (MAKAI_CODESIGN_IDENTITY=<sha1> to sign)"
	@echo "make tui        build, then start the TUI (extra flags: make tui ARGS='--model ...')"
	@echo "make test       run every unit test group"
	@echo "make test-tui   run the TUI unit tests"
	@echo "make check      run the no-comments and Zig pattern guardrails"
	@echo "make clean      remove the project build cache (zig/.zig-cache) and zig/zig-out"
	@echo "make clean-all  clean, then also remove the global zig cache ($(ZIG_GLOBAL_CACHE))"

build:
	$(ZIG) build --build-file zig/build.zig
ifneq ($(MAKAI_CODESIGN_IDENTITY),)
	codesign --force --identifier ai.hyperneo.oap --sign "$(MAKAI_CODESIGN_IDENTITY)" zig/zig-out/bin/oapx
endif

tui: build
	./zig/zig-out/bin/oapx --tui $(ARGS)

test:
	$(ZIG) build --build-file zig/build.zig test

test-tui:
	$(ZIG) build --build-file zig/build.zig test-unit-tui

check:
	node scripts/check-no-comments.mjs --check
	./scripts/check-zig-patterns.sh

clean:
	rm -rf zig/.zig-cache zig/zig-out

clean-all: clean
	@if [ -n "$(ZIG_GLOBAL_CACHE)" ]; then echo "rm -rf $(ZIG_GLOBAL_CACHE)"; rm -rf "$(ZIG_GLOBAL_CACHE)"; else echo "could not determine the global zig cache from 'zig env'"; exit 1; fi
