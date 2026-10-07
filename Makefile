ZIG ?= zig
ARGS ?=
OPTIMIZE ?= ReleaseSafe
OAPX_CODESIGN_IDENTITY ?=
PREFIX ?= $(HOME)/.local
ZIG_GLOBAL_CACHE := $(shell $(ZIG) env 2>/dev/null | sed -n 's/.*global_cache_dir[" ]*[:=] *"\([^"]*\)".*/\1/p')

.PHONY: help build install tui test test-tui check clean clean-all

help:
	@echo "make build      build oapx into zig/zig-out/bin, ReleaseSafe (OPTIMIZE=Debug for a debug build; OAPX_CODESIGN_IDENTITY=<sha1> to sign)"
	@echo "make install    build, then put oapx in $(PREFIX)/bin (PREFIX=<dir> to change)"
	@echo "make tui        build, then start the TUI (extra flags: make tui ARGS='--model ...')"
	@echo "make test       run every unit test group"
	@echo "make test-tui   run the TUI unit tests"
	@echo "make check      run the no-comments and Zig pattern guardrails"
	@echo "make clean      remove the project build cache (zig/.zig-cache) and zig/zig-out"
	@echo "make clean-all  clean, then also remove the global zig cache ($(ZIG_GLOBAL_CACHE))"

build:
	$(ZIG) build --build-file zig/build.zig -Doptimize=$(OPTIMIZE)
ifneq ($(OAPX_CODESIGN_IDENTITY),)
	codesign --force --identifier ai.hyperneo.oap --sign "$(OAPX_CODESIGN_IDENTITY)" zig/zig-out/bin/oapx
endif

install: build
	mkdir -p "$(PREFIX)/bin"
	tmp="$(PREFIX)/bin/.oapx.install.$$$$" && trap 'rm -f "$$tmp"' EXIT && cp zig/zig-out/bin/oapx "$$tmp" && mv -f "$$tmp" "$(PREFIX)/bin/oapx"

tui: build
	./zig/zig-out/bin/oapx $(ARGS)

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
