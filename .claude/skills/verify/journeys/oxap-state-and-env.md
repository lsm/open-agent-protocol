# Journey: OXAP state paths and environment precedence

Decision 0067 moves local state to `~/.oxap` and workspace state to `.oxap/`,
reading the old `~/.oapx` / `.oapx/` only when the new one is absent, and reads
`OXAP_*` before the legacy `OAPX_*`. Nothing is moved or deleted. Build the
ReleaseSafe binary first:

```sh
zig build --build-file zig/build.zig install --prefix /tmp/oxap-state
```

## A fresh home writes only the new directory

```sh
home=$(mktemp -d)
HOME="$home" /tmp/oxap-state/bin/oxap serve agent --stdio </dev/null >/dev/null 2>&1; echo "exit=$?"
find "$home" -maxdepth 2 -type d | sort
```

Require exit 0 and `$home/.oxap` in the listing, with no `.oapx`.

## A home whose state is only under the legacy name keeps working

```sh
home=$(mktemp -d)
mkdir -p "$home/.oapx"
printf '%s' '{"providers":[{"id":"legacy-gw","base_url":"https://gw.test"}]}' > "$home/.oapx/providers.json"
HOME="$home" /tmp/oxap-state/bin/oxap serve agent --stdio </dev/null >/dev/null 2>&1; echo "exit=$?"
find "$home" -maxdepth 2 -type d | sort
```

Require exit 0, `.oapx` still present, and no `.oxap`: the runtime read the old
file and did not fork its state into a second directory.

## A home holding both reads the new one

```sh
home=$(mktemp -d)
mkdir -p "$home/.oapx" "$home/.oxap"
HOME="$home" /tmp/oxap-state/bin/oxap opencode install 2>&1 | head -3
find "$home" -maxdepth 2 -type d | sort
```

Any new file lands under `.oxap`; the old directory is left alone.

## The new environment name wins, and the old one is still read

`OXAP_PROVIDER_SERVICE_URL` is the provider service's endpoint and its legacy
name is `OAPX_PROVIDER_SERVICE_URL`; `serve agent,provider` reads both. Give the
new name a value that passes the security check and the old name a value that
does not, and require the new one to be the one used:

```sh
OXAP_PROVIDER_SERVICE_URL=http://127.0.0.1:1/ \
  OXAP_PROVIDER_SERVICE_SECURITY=loopback \
  OAPX_PROVIDER_SERVICE_URL=http://127.0.0.1:2/ \
  OAPX_PROVIDER_SERVICE_SECURITY=loopback \
  /tmp/oxap-state/bin/oxap serve provider --stdio </dev/null 2>&1 | head -2
```

With the legacy name alone the same command behaves identically, which is the
fallback. Swap the pair (only `OAPX_*` set) and the refusal, if any, must name
the legacy value, not the OXAP one.

## The metadata reader accepts both namespaces and refuses a disagreement

An open carrying `extensions.oxap` is read as settings; one carrying only the
legacy `extensions.oapx` is still read; one carrying both with different
contents is refused rather than silently choosing. The unit tests are
`zig/src/adapter/oapx/adapter.zig`'s `metadata naming only the new namespace`,
`metadata naming only the legacy namespace`, and `metadata naming both
namespaces with settings that disagree`:

```sh
HOME=$(mktemp -d) zig build --build-file zig/build.zig test-unit-adapter -Doptimize=ReleaseSafe
```
