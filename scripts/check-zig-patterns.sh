#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "[patterns] checking runtime catch unreachable usage..."
all_catch_unreachable="$(grep -Rns "catch unreachable" zig/src || true)"
if [[ -n "$all_catch_unreachable" ]]; then
  runtime_catch_unreachable="$(printf "%s\n" "$all_catch_unreachable" \
    | grep -vE "^[^:]+:[0-9]+:\s*//" \
    | grep -v "zig/src/utils/retry.zig" || true)"
  if [[ -n "$runtime_catch_unreachable" ]]; then
    echo "[patterns] unexpected runtime 'catch unreachable' found:" >&2
    echo "$runtime_catch_unreachable" >&2
    echo "[patterns] prefer oom.unreachableOnOom(...) or explicit error handling" >&2
    exit 1
  fi
fi

echo "[patterns] checking direct std.crypto.random usage..."
all_crypto_random="$(grep -Rns "std\.crypto\.random" zig/src || true)"
if [[ -n "$all_crypto_random" ]]; then
  crypto_random_violations="$(printf "%s\n" "$all_crypto_random" \
    | grep -v "zig/src/compat/random.zig" \
    | grep -vE "^[^:]+:[0-9]+:\s*//" || true)"
  if [[ -n "$crypto_random_violations" ]]; then
    echo "[patterns] direct std.crypto.random usage found:" >&2
    echo "$crypto_random_violations" >&2
    echo "[patterns] use compat.random secure/ordinary helpers instead" >&2
    exit 1
  fi
fi

ordinary_entropy_pattern='\b(fillRandomBytes|randomBytes|randomIntRangeLessThan)\b|\b(IoSource|DefaultPrng|DeterministicSource)\b|random[[:space:]]*\.[[:space:]]*int\b|\.[[:space:]]*random[[:space:]]*[;(]|\.[[:space:]]*Random[[:space:]]*[.;]'
secure_entropy_pattern='\b(fillSecureBytes|secureBytes|secureIntRangeLessThan|randomSecure)\b'

strip_noncode() {
  awk '
  {
    line = $0
    out = ""
    n = length(line)
    i = 1
    while (i <= n) {
      c = substr(line, i, 1)
      d = substr(line, i + 1, 1)
      if (c == "/" && d == "/") break
      if (c == "\\" && d == "\\") break
      if (c == "@" && d == "\"") {
        i += 2
        while (i <= n) {
          ch = substr(line, i, 1)
          if (ch == "\\") {
            esc = substr(line, i + 1, 1)
            if (esc == "x") {
              hex = substr(line, i + 2, 2)
              if (hex ~ /^[0-9A-Fa-f][0-9A-Fa-f]$/) {
                v = (index("0123456789abcdef", tolower(substr(hex, 1, 1))) - 1) * 16 + index("0123456789abcdef", tolower(substr(hex, 2, 1))) - 1
                if (v > 0 && v < 128) out = out sprintf("%c", v); else out = out "?"
                i += 4
                continue
              }
            } else if (esc == "u") {
              rest = substr(line, i + 2)
              if (substr(rest, 1, 1) == "{") {
                end = index(rest, "}")
                if (end > 2) {
                  cp = substr(rest, 2, end - 2)
                  if (cp ~ /^[0-9A-Fa-f]+$/) {
                    v = 0
                    for (p = 1; p <= length(cp); p++) v = v * 16 + index("0123456789abcdef", tolower(substr(cp, p, 1))) - 1
                    if (v > 0 && v < 128) out = out sprintf("%c", v); else out = out "?"
                    i += 2 + end
                    continue
                  }
                }
              }
            }
            out = out substr(line, i, 2); i += 2; continue
          }
          i++
          if (ch == "\"") break
          out = out ch
        }
        continue
      }
      if (c == "\"" || c == "'"'"'") {
        quote = c
        i++
        while (i <= n) {
          ch = substr(line, i, 1)
          if (ch == "\\") { i += 2; continue }
          i++
          if (ch == quote) break
        }
        out = out " "
        continue
      }
      out = out c
      i++
    }
    print out
  }'
}

code_matches_only() {
  local pattern="$1" prefix_fields="$2" hit content stripped
  while IFS= read -r hit; do
    [[ -z "$hit" ]] && continue
    content="$hit"
    for ((i = 0; i < prefix_fields; i++)); do content="${content#*:}"; done
    stripped="$(printf '%s' "$content" | strip_noncode)"
    if printf '%s' "$stripped" | grep -qE "$pattern"; then printf '%s\n' "$hit"; fi
  done
}

secure_random_files=(
  "zig/src/oauth/pkce.zig"
  "zig/src/utils/oauth/pkce.zig"
  "zig/src/utils/oauth/openai_codex.zig"
  "zig/src/transports/websocket.zig"
  "zig/src/protocol/provider/types.zig"
  "zig/src/tui/app.zig"
)

ordinary_entropy_definition_file="zig/src/compat/random.zig"

expected_ordinary_entropy_sites="$(cat <<'SITES'
zig/src/compat/random.zig|        const ordinary_value = randomIntRangeLessThan(usize, 62);
zig/src/compat/random.zig|        const ordinary_value = randomIntRangeLessThan(usize, 62);
zig/src/compat/random.zig|        return .{ .prng = std.Random.DefaultPrng.init(seed) };
zig/src/compat/random.zig|        self.prng.random().bytes(buf);
zig/src/compat/random.zig|    const OrdinaryHelper = @TypeOf(fillRandomBytes);
zig/src/compat/random.zig|    const ordinary = try randomBytes(std.testing.allocator, 0);
zig/src/compat/random.zig|    const ordinary = try randomBytes(std.testing.allocator, 17);
zig/src/compat/random.zig|    const ordinary = try randomBytes(std.testing.allocator, 32);
zig/src/compat/random.zig|    defaultIo().random(buf);
zig/src/compat/random.zig|    fillRandomBytes(&empty);
zig/src/compat/random.zig|    fillRandomBytes(buf);
zig/src/compat/random.zig|    prng: std.Random.DefaultPrng,
zig/src/compat/random.zig|    pub fn allocBytes(self: *DeterministicSource, allocator: std.mem.Allocator, len: usize) ![]u8 {
zig/src/compat/random.zig|    pub fn bytes(self: *DeterministicSource, buf: []u8) void {
zig/src/compat/random.zig|    pub fn init(seed: u64) DeterministicSource {
zig/src/compat/random.zig|    try std.testing.expect(fillSecureBytes != fillRandomBytes);
zig/src/compat/random.zig|    try std.testing.expectEqual(@as(usize, 0), randomIntRangeLessThan(usize, 1));
zig/src/compat/random.zig|    var different_source = DeterministicSource.init(0x8765_4321);
zig/src/compat/random.zig|    var first_source = DeterministicSource.init(0x1234_5678);
zig/src/compat/random.zig|    var second_source = DeterministicSource.init(0x1234_5678);
zig/src/compat/random.zig|    var source: std.Random.IoSource = .{ .io = defaultIo() };
zig/src/compat/random.zig|    var source: std.Random.IoSource = .{ .io = defaultIo() };
zig/src/compat/random.zig|pub const DeterministicSource = struct {
zig/src/compat/random.zig|pub fn fillRandomBytes(buf: []u8) void {
zig/src/compat/random.zig|pub fn randomBytes(allocator: std.mem.Allocator, len: usize) ![]u8 {
zig/src/compat/random.zig|pub fn randomIntRangeLessThan(comptime T: type, upper_bound: T) T {
zig/src/agent/agent.zig|        compat.random.fillRandomBytes(&bytes);
zig/src/model_catalog.zig|    const tmp_path = try std.fmt.allocPrint(allocator, "{s}.tmp.{d}.{x}", .{ path, compat.time.nowMillis(), compat.random.int(u64) });
zig/src/providers/sse_parser.zig|    const random = prng.random();
zig/src/providers/sse_parser.zig|    var prng = std.Random.DefaultPrng.init(seed);
zig/src/transports/transport_retry.zig|        return prng.random().intRangeAtMost(u64, self.base_delay_ms, capped);
zig/src/transports/transport_retry.zig|        var prng = std.Random.DefaultPrng.init(seed);
zig/src/utils/oauth/storage.zig|    const tmp_name = try std.fmt.allocPrint(allocator, "{s}{d}.{x}", .{ auth_temp_prefix, compat.time.nowMillis(), compat.random.int(u64) });
zig/src/utils/retry.zig|            const rand = prng.random().float(f32);
zig/src/utils/retry.zig|            var prng = std.Random.DefaultPrng.init(seed);
SITES
)"

expected_sensitive_noncode_matches="$(cat <<'NONCODE'
NONCODE
)"

echo "[patterns] checking security-sensitive entropy call sites..."
for file in "${secure_random_files[@]}"; do
  if [[ ! -f "$file" ]]; then
    echo "[patterns] secure_random_files lists a path that does not exist: $file" >&2
    echo "[patterns] update scripts/check-zig-patterns.sh when entropy call sites move or are deleted" >&2
    exit 1
  fi
  secure_file_matches="$(grep -nE "$ordinary_entropy_pattern" "$file" \
    | sed "s|^[0-9]*:|$file\||" || true)"
  if [[ -n "$secure_file_matches" ]]; then
    secure_file_matches="$(comm -13 \
      <(printf "%s\n" "$expected_sensitive_noncode_matches" | grep -v '^$' | sort) \
      <(printf "%s\n" "$secure_file_matches" | grep -v '^$' | sort))"
  fi
  if [[ -n "$secure_file_matches" ]]; then
    echo "[patterns] security-sensitive random path uses ordinary entropy in $file" >&2
    echo "$secure_file_matches" >&2
    echo "[patterns] use compat.random secure helpers / io.randomSecure for OAuth, WebSocket, and protocol IDs" >&2
    echo "[patterns] this check matches raw text on purpose, so a scanner bug cannot unprotect these files;" >&2
    echo "[patterns] if the match is genuinely non-code, declare it in expected_sensitive_noncode_matches" >&2
    exit 1
  fi
done

echo "[patterns] checking ordinary entropy call sites are declared..."
if [[ ! -f "$ordinary_entropy_definition_file" ]]; then
  echo "[patterns] ordinary_entropy_definition_file does not exist: $ordinary_entropy_definition_file" >&2
  exit 1
fi

escaped_identifier_pattern='\\x[0-9A-Fa-f][0-9A-Fa-f]|\\u[{][0-9A-Fa-f]'
scan_prefilter_pattern="$ordinary_entropy_pattern|$escaped_identifier_pattern"

actual_ordinary_entropy_sites="$(grep -RnsE --include="*.zig" "$scan_prefilter_pattern" zig/src \
  | code_matches_only "$ordinary_entropy_pattern" 2 \
  | sed 's/^\([^:]*\):[0-9]*:/\1|/' || true)"

undeclared_ordinary_entropy="$(comm -13 \
  <(printf "%s\n" "$expected_ordinary_entropy_sites" | grep -v '^$' | sort) \
  <(printf "%s\n" "$actual_ordinary_entropy_sites" | grep -v '^$' | sort))"
if [[ -n "$undeclared_ordinary_entropy" ]]; then
  echo "[patterns] undeclared ordinary entropy call site:" >&2
  echo "$undeclared_ordinary_entropy" >&2
  echo "[patterns] use compat.random secure helpers, or declare the exact call site in expected_ordinary_entropy_sites with rationale in the commit message" >&2
  exit 1
fi

stale_ordinary_entropy="$(comm -23 \
  <(printf "%s\n" "$expected_ordinary_entropy_sites" | grep -v '^$' | sort) \
  <(printf "%s\n" "$actual_ordinary_entropy_sites" | grep -v '^$' | sort))"
if [[ -n "$stale_ordinary_entropy" ]]; then
  echo "[patterns] expected_ordinary_entropy_sites declares a call site that no longer exists:" >&2
  echo "$stale_ordinary_entropy" >&2
  echo "[patterns] update scripts/check-zig-patterns.sh when entropy call sites move or are deleted" >&2
  exit 1
fi

echo "[patterns] checking line-broken ordinary entropy..."
while IFS= read -r -d '' file; do
  joined_hits="$(strip_noncode < "$file" \
    | awk '
    { lines[NR] = $0 }
    END {
      k = 1
      while (k <= NR) {
        acc = lines[k]; first = lines[k]
        while (k < NR && lines[k + 1] ~ /^[[:space:]]*[.(]/) { k++; acc = acc " " lines[k] }
        if (acc != first) print acc
        k++
      }
    }' \
    | grep -E "$ordinary_entropy_pattern" || true)"
  if [[ -n "$joined_hits" ]]; then
    echo "[patterns] ordinary entropy split across lines in $file" >&2
    printf "%s\n" "$joined_hits" | sed "s|^|$file: joined: |" >&2
    echo "[patterns] the deny patterns are line-based; write entropy calls on one line so the guard can see them" >&2
    exit 1
  fi
done < <(find zig/src -name '*.zig' -print0 | sort -z)

echo "[patterns] checking secure entropy call sites are present..."
expected_secure_entropy_sites="$(cat <<'SECURE'
zig/src/adapter/claude/adapter.zig|            compat.random.fillSecureBytes(&bytes);
zig/src/adapter/claude/adapter.zig|        compat.random.fillSecureBytes(&entropy);
zig/src/adapter/claude/adapter.zig|    compat.random.fillSecureBytes(&bytes);
zig/src/adapter/deepseek/adapter.zig|            compat.random.fillSecureBytes(&entropy);
zig/src/adapter/codex/bridge.zig|    compat.random.fillSecureBytes(&key_bytes);
zig/src/adapter/codex/bridge.zig|    compat.random.fillSecureBytes(&mask);
zig/src/adapter/opencode/adapter.zig|        compat.random.fillSecureBytes(&nonce);
zig/src/compat/random.zig|        const secure_value = secureIntRangeLessThan(usize, 62);
zig/src/tools/shell.zig|        compat.random.fillSecureBytes(&bytes);
zig/src/compat/random.zig|        const secure_value = secureIntRangeLessThan(usize, 62);
zig/src/compat/random.zig|        fillSecureBytes(&bytes);
zig/src/compat/random.zig|    const SecureHelper = @TypeOf(fillSecureBytes);
zig/src/compat/random.zig|    const first = try secureBytes(std.testing.allocator, 32);
zig/src/compat/random.zig|    const second = try secureBytes(std.testing.allocator, 32);
zig/src/compat/random.zig|    const secure = try secureBytes(std.testing.allocator, 0);
zig/src/compat/random.zig|    const secure = try secureBytes(std.testing.allocator, 32);
zig/src/compat/random.zig|    const secure = try secureBytes(std.testing.allocator, 32);
zig/src/compat/random.zig|    defaultIo().randomSecure(buf) catch |err| {
zig/src/compat/random.zig|    fillSecureBytes(&empty);
zig/src/compat/random.zig|    fillSecureBytes(buf);
zig/src/compat/random.zig|    try std.testing.expect(fillSecureBytes != fillRandomBytes);
zig/src/compat/random.zig|    try std.testing.expectEqual(@as(usize, 0), secureIntRangeLessThan(usize, 1));
zig/src/compat/random.zig|pub fn fillSecureBytes(buf: []u8) void {
zig/src/compat/random.zig|pub fn secureBytes(allocator: std.mem.Allocator, len: usize) ![]u8 {
zig/src/compat/random.zig|pub fn secureIntRangeLessThan(comptime T: type, upper_bound: T) T {
zig/src/oauth/pkce.zig|    return generatePKCEWithRandom(compat.random.fillSecureBytes);
zig/src/protocol/provider/types.zig|    return generateSessionIdWithRandomInt(compat.random.secureIntRangeLessThan);
zig/src/protocol/provider/types.zig|    return generateUlidWithRandom(compat.random.fillSecureBytes);
zig/src/transports/websocket.zig|        compat.random.fillSecureBytes(&mask);
zig/src/transports/websocket.zig|    compat.random.fillSecureBytes(&nonce);
zig/src/tui/app.zig|    compat.random.fillSecureBytes(&random_bytes);
zig/src/utils/oauth/openai_codex.zig|    return generateStateWithRandom(allocator, compat.random.fillSecureBytes);
zig/src/protocol/oap/provider/server.zig|        compat.random.fillSecureBytes(&raw);
zig/src/utils/oauth/pkce.zig|    return generateWithRandom(allocator, compat.random.fillSecureBytes);
SECURE
)"

actual_secure_entropy_sites="$(grep -RnsE --include="*.zig" "$secure_entropy_pattern" zig/src \
  | code_matches_only "$secure_entropy_pattern" 2 \
  | sed 's/^\([^:]*\):[0-9]*:/\1|/' || true)"

if [[ "$(printf "%s\n" "$expected_secure_entropy_sites" | sort)" != "$(printf "%s\n" "$actual_secure_entropy_sites" | sort)" ]]; then
  echo "[patterns] secure entropy call sites changed:" >&2
  diff <(printf "%s\n" "$expected_secure_entropy_sites" | sort) \
       <(printf "%s\n" "$actual_secure_entropy_sites" | sort) >&2 || true
  echo "[patterns] a security-sensitive generator must keep consuming secure entropy; declare intentional changes here" >&2
  exit 1
fi

echo "[patterns] checking compat.random public exports..."
expected_compat_random_exports="$(cat <<'EXPORTS'
pub const DeterministicSource
pub fn fillRandomBytes
pub fn fillSecureBytes
pub fn int
pub fn randomBytes
pub fn randomIntRangeLessThan
pub fn secureBytes
pub fn secureIntRangeLessThan
EXPORTS
)"

actual_compat_random_exports="$(grep -oE '^pub (const|fn) [A-Za-z_][A-Za-z0-9_]*' \
  "$ordinary_entropy_definition_file" | sort)"

if [[ "$expected_compat_random_exports" != "$actual_compat_random_exports" ]]; then
  echo "[patterns] public exports of $ordinary_entropy_definition_file changed:" >&2
  diff <(printf "%s\n" "$expected_compat_random_exports") \
       <(printf "%s\n" "$actual_compat_random_exports") >&2 || true
  echo "[patterns] every public export of the entropy module must be classified as secure or ordinary and declared here" >&2
  exit 1
fi

echo "[patterns] checking compat.random secure wrapper bodies..."
compat_random_file="zig/src/compat/random.zig"
secure_wrappers=(
  "fillSecureBytes:defaultIo().randomSecure("
  "secureBytes:fillSecureBytes("
  "secureIntRangeLessThan:fillSecureBytes("
)

for wrapper in "${secure_wrappers[@]}"; do
  wrapper_fn="${wrapper%%:*}"
  wrapper_requires="${wrapper##*:}"
  wrapper_body="$(awk -v target="pub fn $wrapper_fn(" \
    'index($0, target) == 1 { inside = 1 } inside { print } inside && $0 == "}" { exit }' \
    "$compat_random_file")"

  if [[ -z "$wrapper_body" ]]; then
    echo "[patterns] secure wrapper $wrapper_fn not found in $compat_random_file" >&2
    echo "[patterns] update scripts/check-zig-patterns.sh when the compat.random secure helpers are renamed" >&2
    exit 1
  fi

  if ! printf "%s\n" "$wrapper_body" | grep -qF "$wrapper_requires"; then
    echo "[patterns] secure wrapper $wrapper_fn no longer calls $wrapper_requires in $compat_random_file" >&2
    printf "%s\n" "$wrapper_body" >&2
    echo "[patterns] every secure call site depends on this wrapper staying on secure entropy" >&2
    exit 1
  fi

  if printf "%s\n" "$wrapper_body" | grep -qE "$ordinary_entropy_pattern"; then
    echo "[patterns] secure wrapper $wrapper_fn uses ordinary entropy in $compat_random_file" >&2
    printf "%s\n" "$wrapper_body" | grep -nE "$ordinary_entropy_pattern" >&2
    echo "[patterns] every secure call site depends on this wrapper staying on secure entropy" >&2
    exit 1
  fi
done

echo "[patterns] checking deinit poisoning in critical types..."
required_files=(
  "zig/src/event_stream.zig"
  "zig/src/api_registry.zig"
  "zig/src/agent/agent.zig"
  "zig/src/protocol/provider/client.zig"
  "zig/src/protocol/provider/server.zig"
  "zig/src/tool_call_tracker.zig"
  "zig/src/streaming_json.zig"
  "zig/src/providers/sse_parser.zig"
  "zig/src/protocol/provider/partial_reconstructor.zig"
)

for file in "${required_files[@]}"; do
  if ! grep -q "self\.\* = undefined;" "$file"; then
    echo "[patterns] missing deinit poisoning in $file" >&2
    exit 1
  fi
done

known_multi_alloc_literals="$(cat <<'LITERALS'
zig/src/agent/agent.zig|                .data = try self._allocator.dupe(u8, i.data),
zig/src/agent/agent_loop.zig|        .tool_call_id = try allocator.dupe(u8, tool_call.id),
zig/src/agent/provider_protocol_bridge.zig|        .api_key = if (options.api_key) |k| try allocator.dupe(u8, k) else null,
zig/src/protocol/auth/server.zig|                .prompt_id = OwnedSlice(u8).initOwned(try self.allocator.dupe(u8, prompt_id)),
zig/src/protocol/auth/server.zig|                .provider_id = OwnedSlice(u8).initOwned(try self.allocator.dupe(u8, flow.provider_id)),
zig/src/protocol/auth/server.zig|                .provider_id = OwnedSlice(u8).initOwned(try self.allocator.dupe(u8, flow.provider_id)),
zig/src/protocol/auth/server.zig|            .provider_id = OwnedSlice(u8).initOwned(try self.allocator.dupe(u8, flow.provider_id)),
zig/src/protocol/auth/server.zig|            .refresh = try self.allocator.dupe(u8, "fixture-refresh-token"),
zig/src/protocol/provider/partial_reconstructor.zig|                    .id = if (tc.id) |id| try self.allocator.dupe(u8, id) else try self.allocator.dupe(u8, ""),
zig/src/protocol/provider/server.zig|            .refresh = try allocator.dupe(u8, "refresh"),
zig/src/protocol/provider/server.zig|        .model_id = protocol_types.OwnedSlice(u8).initOwned(try allocator.dupe(u8, model_id_value)),
zig/src/protocol/provider/server.zig|        .refresh = try allocator.dupe(u8, "refresh"),
zig/src/protocol/provider/server.zig|        .refresh = try allocator.dupe(u8, credentials.refresh),
zig/src/tools/auth_cli.zig|            .prompt_id = OwnedSlice(u8).initOwned(try allocator.dupe(u8, prompt_id)),
zig/src/tools/permission.zig|                .tool_name = try self.allocator.dupe(u8, tool_name),
zig/src/transport.zig|            .api = try allocator.dupe(u8, ""),
zig/src/transport.zig|            .api = try allocator.dupe(u8, ""),
zig/src/transport.zig|            .api = try allocator.dupe(u8, ""),
zig/src/tui/runtime.zig|                .data = try allocator.dupe(u8, img.data),
zig/src/tui/runtime.zig|                .id = try allocator.dupe(u8, tc.id),
zig/src/tui/runtime.zig|                .text = try allocator.dupe(u8, t.text),
zig/src/tui/runtime.zig|                .thinking = try allocator.dupe(u8, t.thinking),
zig/src/tui/state.zig|            .id = try allocator.dupe(u8, id),
zig/src/tui/state.zig|            .tool_call_id = try allocator.dupe(u8, tool_call_id),
zig/src/utils/oauth/anthropic.zig|        .access_token = try allocator.dupe(u8, access_token),
zig/src/utils/oauth/anthropic.zig|        .code = try allocator.dupe(u8, input),
zig/src/utils/oauth/anthropic.zig|        .refresh = try allocator.dupe(u8, token_response.refresh_token),
zig/src/utils/oauth/anthropic.zig|        .refresh = try allocator.dupe(u8, token_response.refresh_token),
zig/src/utils/oauth/github_copilot.zig|        .device_code = try allocator.dupe(u8, parsed.value.device_code),
zig/src/utils/oauth/openai_codex.zig|        .code = try allocator.dupe(u8, trimmed),
zig/src/utils/pre_transform.zig|                                        .thinking = try allocator.dupe(u8, t.thinking),
zig/src/utils/pre_transform.zig|                                    .text = try allocator.dupe(u8, t.text),
zig/src/utils/pre_transform.zig|                                    .thinking = try allocator.dupe(u8, t.thinking),
zig/src/utils/pre_transform.zig|                                .data = try allocator.dupe(u8, img.data),
LITERALS
)"

multi_alloc_literal_pattern='try[[:space:]]+[A-Za-z_][A-Za-z0-9_.]*\\.(dupe|dupeZ|allocSentinel)[[:space:]]*\\(|try[[:space:]]+std\\.fmt\\.allocPrint[[:space:]]*\\(|try[[:space:]]+owned[[:space:]]*\\('

scan_multi_alloc_literals() {
  local file="$1"
  awk -v file="$file" -v pattern="$multi_alloc_literal_pattern" '
    NR == FNR { code[FNR] = $0; next }
    { orig[FNR] = $0; if (FNR > last) last = FNR }
    END {
      in_test = 0
      test_depth = 0
      depth = 0
      count = 0
      first = 0
      for (i = 1; i <= last; i++) {
        line = code[i]
        opens = gsub(/\{/, "{", line)
        closes = gsub(/\}/, "}", line)
        line = code[i]

        if (!in_test && orig[i] ~ /^[[:space:]]*test[[:space:]]*["{]/) {
          in_test = 1
          test_depth = opens - closes
          continue
        }
        if (in_test) {
          test_depth += opens - closes
          if (test_depth <= 0) in_test = 0
          continue
        }

        if (depth == 0) {
          trimmed = line
          sub(/[[:space:]]+$/, "", trimmed)
          if (trimmed ~ /\.\{$/ && opens > closes) {
            depth = opens - closes
            count = 0
            first = 0
          }
          continue
        }

        depth += opens - closes
        if (line ~ pattern) {
          count++
          if (first == 0) first = i
        }
        if (depth <= 0) {
          if (count >= 2) print file "|" orig[first]
          depth = 0
          count = 0
          first = 0
        }
      }
    }
  ' <(strip_noncode < "$file") "$file"
}

echo "[patterns] checking struct literals that allocate more than once..."
actual_multi_alloc_literals=""
while IFS= read -r -d '' file; do
  literal_hits="$(scan_multi_alloc_literals "$file")"
  if [[ -n "$literal_hits" ]]; then
    actual_multi_alloc_literals+="$literal_hits"$'\n'
  fi
done < <(find zig/src -name "*.zig" -print0 | sort -z)

undeclared_multi_alloc="$(comm -13 \
  <(printf "%s\n" "$known_multi_alloc_literals" | grep -v '^$' | sort) \
  <(printf "%s\n" "$actual_multi_alloc_literals" | grep -v '^$' | sort))"
if [[ -n "$undeclared_multi_alloc" ]]; then
  echo "[patterns] struct literal allocates more than once with no way to unwind:" >&2
  echo "$undeclared_multi_alloc" >&2
  echo "[patterns] Zig evaluates literal fields in order, so when a later allocation fails the" >&2
  echo "[patterns] literal never completes and every field already allocated for it leaks. An" >&2
  echo "[patterns] errdefer cannot help from inside a literal, and one placed after it never runs," >&2
  echo "[patterns] because the assignment it guards was never reached. Build each field into a" >&2
  echo "[patterns] local with its own errdefer first, so the literal itself becomes infallible;" >&2
  echo "[patterns] see cloneModelDescriptor in zig/src/protocol/model_catalog_types.zig." >&2
  echo "[patterns] Prove the fix with std.testing.checkAllAllocationFailures." >&2
  echo "[patterns] known_multi_alloc_literals is a shrinking backlog of sites that predate this" >&2
  echo "[patterns] check, not a list of approved ones. Adding to it needs a reason in the commit" >&2
  echo "[patterns] message that says why this literal cannot leak - an arena freed wholesale on" >&2
  echo "[patterns] error, for example. New code is expected to be fixed, not declared." >&2
  exit 1
fi

stale_multi_alloc="$(comm -23 \
  <(printf "%s\n" "$known_multi_alloc_literals" | grep -v '^$' | sort) \
  <(printf "%s\n" "$actual_multi_alloc_literals" | grep -v '^$' | sort))"
if [[ -n "$stale_multi_alloc" ]]; then
  echo "[patterns] known_multi_alloc_literals declares a literal that no longer exists:" >&2
  echo "$stale_multi_alloc" >&2
  echo "[patterns] a fixed site must be removed from the list so the count keeps ratcheting down" >&2
  exit 1
fi

known_defer_scope="$(cat <<'DEFER'
DEFER
)"

scan_defer_scope() {
  local file="$1"
  # A frame stack over whole-line shapes, deliberately NOT a brace count.
  #
  # The previous version counted statements between an `if (cond) {` line and the
  # next line that was exactly `}`, which cannot see a `} else if`/`} else` head, a
  # `|capture|`, or a nested block. Widening it the obvious way -- tracking brace
  # depth -- desyncs: Zig's character literals (a bare `'"'`) and its `\\` multiline
  # strings both hide braces from a line scanner, and 40 of 202 files came out
  # unbalanced. Counting no braces at all removes that failure mode: the only thing
  # that moves the stack is a line that IS an opener or IS a closing brace, so a
  # brace inside a string cannot shift anything. What is left is a line that looks
  # like an opener or like `}` while inside a string or a comment. That is a much
  # smaller surface, and guard-bad.zig pins the case that actually bit -- a `://`
  # inside a string in the head, which a naive comment strip hid.
  #
  # Loops are out of scope on purpose. A `defer` in a loop body is scoped to the
  # iteration -- the body block ends every pass -- so a lone `defer` there is the
  # idiom, not the defect. Counting loops reported six correct sites in makai.zig.
  # The defect needs a CONDITIONAL, where the defer runs once at block exit, before
  # the call it was meant to outlive.
  awk -v file="$file" '
    function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
    # String-aware, because an `if` head is full of them: `if (startsWith(url,
    # "http://")) {` ends in `{`, and cutting at the `//` inside the literal
    # truncated the line before the brace, so the frame never opened. The old
    # raw-line regex had no comment handling and did open one there, so a naive
    # strip is a coverage regression, not a tidy-up. Still not a full lexer: a
    # `//` inside a `\\` multiline string is still treated as a comment.
    function strip_line_comment(s,   i, c, n, instr, esc) {
      n = length(s); instr = 0; esc = 0
      for (i = 1; i <= n; i++) {
        c = substr(s, i, 1)
        if (instr) {
          if (esc) { esc = 0; continue }
          if (c == "\\") { esc = 1; continue }
          if (c == "\"") instr = 0
          continue
        }
        if (c == "\"") { instr = 1; continue }
        if (c == "/" && substr(s, i + 1, 1) == "/") return substr(s, 1, i - 1)
      }
      return s
    }
    function add_stmt(f, text,   t) {
      t = trim(text)
      if (t == "" || f < 1) return
      if (fstmts[f] == 0) {
        ffirst[f] = t
        if (t ~ /^defer[ \t]/) fdefer[f] = 1
      }
      fstmts[f]++
    }
    function frees_a_capture(f,   names, j, c, n) {
      if (fcaps[f] == "") return 0
      n = split(fcaps[f], names, ",")
      for (j = 1; j <= n; j++) {
        c = trim(names[j])
        if (c != "" && ffirst[f] ~ ("(^|[^A-Za-z0-9_])" c "([^A-Za-z0-9_]|$)")) return 1
      }
      return 0
    }
    function evaluate(f) {
      if (f < 1) return
      if (fkind[f] !~ /^(if|else)$/) return
      if (fstmts[f] != 1 || !fdefer[f]) return
      if (frees_a_capture(f)) return
      print file ":" fline[f] "\t" fkind[f] "\t" ffirst[f]
    }
    function push(kind, line, head,   f, caps) {
      f = ++top
      fkind[f] = kind; fline[f] = line
      fstmts[f] = 0; fdefer[f] = 0; ffirst[f] = ""; fcaps[f] = ""
      caps = ""
      if (match(head, /\|[^|]*\|[ \t]*$/)) {
        caps = substr(head, RSTART, RLENGTH)
        sub(/^[ \t|]*/, "", caps); sub(/[ \t|]+$/, "", caps)
      }
      fcaps[f] = caps
      return f
    }
    {
      code = trim(strip_line_comment($0))
      if (code == "}") { if (top >= 1) { evaluate(top); top-- } ; next }
      # `} else if (...) {` and `} else {` close one block and open the next on one
      # line. That is self-evident from the line itself, so it needs no brace count.
      # The trailing brace is required before pushing, and the pop happens either
      # way. A braceless `} else if (c) continue;` opens no block, so pushing for it
      # left a phantom frame that swallowed the lines after the if-statement and
      # would report a loop-scoped defer as a conditional one.
      if (code ~ /^\}[ \t]*else[ \t]*if[ \t]*\(/) {
        opens_block = (code ~ /\{[ \t]*$/)
        if (top >= 1) { evaluate(top); top-- }
        if (!opens_block) next
        h = code; sub(/^\}[ \t]*/, "", h); sub(/\{[ \t]*$/, "", h)
        push("if", FNR, h); next
      }
      if (code ~ /^\}[ \t]*else[ \t]*(\||\{)/) {
        opens_block = (code ~ /\{[ \t]*$/)
        if (top >= 1) { evaluate(top); top-- }
        if (!opens_block) next
        h = code; sub(/^\}[ \t]*/, "", h); sub(/\{[ \t]*$/, "", h)
        push("else", FNR, h); next
      }
      if (code ~ /^(if[ \t]*\(|else[ \t]+if[ \t]*\()/ && code ~ /\{[ \t]*$/) {
        h = code; sub(/\{[ \t]*$/, "", h)
        if (top >= 1) add_stmt(top, code)
        push("if", FNR, h); next
      }
      if (code ~ /^else[ \t]*(\||\{)/ && code ~ /\{[ \t]*$/) {
        if (top >= 1) add_stmt(top, code)
        h = code; sub(/\{[ \t]*$/, "", h)
        push("else", FNR, h); next
      }
      if (top >= 1) add_stmt(top, code)
    }
  ' "$file"
}

echo "[patterns] checking for a defer scoped inside a block that closes before the call..."
actual_defer_scope=""
while IFS= read -r -d '' file; do
  scope_hits="$(scan_defer_scope "$file")"
  if [[ -n "$scope_hits" ]]; then
    actual_defer_scope+="$scope_hits"$'\n'
  fi
done < <(find zig/src -name "*.zig" -print0 | sort -z)

undeclared_defer_scope="$(comm -13 \
  <(printf "%s\n" "$known_defer_scope" | grep -v '^$' | sort) \
  <(printf "%s\n" "$actual_defer_scope" | grep -v '^$' | sort))"
if [[ -n "$undeclared_defer_scope" ]]; then
  echo "[patterns] a defer sits in a block whose body is only that defer:" >&2
  echo "$undeclared_defer_scope" >&2
  echo "[patterns] SCOPE, exactly: a plain \`if (cond) {\` block whose body is a single" >&2
  echo "[patterns] \`defer\`, with the guarded call after the block. A defer runs when its block" >&2
  echo "[patterns] closes, so here it runs before the call it was meant to outlive." >&2
  echo "[patterns] Hold the call's result, do the free, then branch on the error; see run() in" >&2
  echo "[patterns] zig/src/transports/in_process.zig." >&2
  echo "[patterns] This is a floor, not a detector. It sees a plain \`if\`, a \`} else if (...) {\`" >&2
  echo "[patterns] or \`} else {\` / \`} else |err| {\` branch, a payload-capture head, and a block" >&2
  echo "[patterns] nested one deep, whenever the block's only top-level line starts with \`defer\`." >&2
  echo "[patterns] A line carrying more than one statement still counts as that one line, so" >&2
  echo "[patterns] \`defer release(v); use(v);\` is seen. NOT SEEN, all of it measured against the" >&2
  echo "[patterns] fixtures rather than assumed: an \`errdefer\` alone; a \`defer {\` block on its own" >&2
  echo "[patterns] lines, whose inner lines raise the statement count and whose own closing brace" >&2
  echo "[patterns] ends the conditional early; a one-line \`if (f) { defer f(); }\`, whose head does not" >&2
  echo "[patterns] end in \`{\`; a braceless \`} else if (c) continue;\`, which opens no block; and a" >&2
  echo "[patterns] switch prong. Until then, read a defer in a conditional as suspect by hand." >&2
  echo "[patterns] known_defer_scope is a backlog for sites that predate this check, not a list of" >&2
  echo "[patterns] approved ones. Adding to it needs a reason in the commit message saying why the" >&2
  echo "[patterns] defer is not meant to outlive its block. New code is expected to be fixed." >&2
  exit 1
fi

stale_defer_scope="$(comm -23 \
  <(printf "%s\n" "$known_defer_scope" | grep -v '^$' | sort) \
  <(printf "%s\n" "$actual_defer_scope" | grep -v '^$' | sort))"
if [[ -n "$stale_defer_scope" ]]; then
  echo "[patterns] known_defer_scope declares a site that no longer exists:" >&2
  echo "$stale_defer_scope" >&2
  echo "[patterns] a fixed site must be removed from the list" >&2
  exit 1
fi

defer_fixture_bad="scripts/fixtures/defer_scope/guard-bad.zig"
defer_fixture_good="scripts/fixtures/defer_scope/guard-good.zig"
scanned_zig_files=0
while IFS= read -r -d '' _; do
  scanned_zig_files=$((scanned_zig_files + 1))
done < <(find zig/src -name "*.zig" -print0)
if [[ "$scanned_zig_files" -eq 0 ]]; then
  echo "[patterns] the defer-scope scan saw no Zig files under zig/src:" >&2
  echo "[patterns] the tree scan is vacuously green, and the fixture self-test below cannot see" >&2
  echo "[patterns] that because it calls the scanner directly. Fail rather than pass on nothing." >&2
  exit 1
fi
defer_scope_expected_bad=8
defer_scope_expected_good=0
bad_fixture_hits="$(scan_defer_scope "$defer_fixture_bad")"
good_fixture_hits="$(scan_defer_scope "$defer_fixture_good")"
bad_fixture_count="$(printf '%s\n' "$bad_fixture_hits" | grep -c . || true)"
if [[ "$bad_fixture_count" -ne "$defer_scope_expected_bad" ]]; then
  echo "[patterns] the defer-scope check reports $bad_fixture_count of its $defer_scope_expected_bad bad fixtures:" >&2
  echo "$bad_fixture_hits" >&2
  echo "[patterns] guard-bad.zig holds one function per spelling this check is supposed to see: a" >&2
  echo "[patterns] plain if, a \`} else if\` branch, a \`} else\` branch, a payload-capture head, a" >&2
  echo "[patterns] block nested one deep, an \`if\` head carrying a \`://\` string literal, an" >&2
  echo "[patterns] \`} else |err| {\` branch, and a lone line carrying two statements. Fewer" >&2
  echo "[patterns] means a spelling went unseen again, which is the" >&2
  echo "[patterns] defect this check exists to prevent; more means it is matching something it should" >&2
  echo "[patterns] not. A count is checked rather than a non-empty result because a check that" >&2
  echo "[patterns] quietly stops seeing three of the eight shapes still passes an emptiness test." >&2
  exit 1
fi
good_fixture_count="$(printf '%s\n' "$good_fixture_hits" | grep -c . || true)"
if [[ "$good_fixture_count" -ne "$defer_scope_expected_good" ]]; then
  echo "[patterns] the defer-scope check reports $good_fixture_count good fixtures, want $defer_scope_expected_good:" >&2
  echo "$good_fixture_hits" >&2
  echo "[patterns] guard-good.zig holds the shapes that are correct: a defer sharing a block with the" >&2
  echo "[patterns] work it protects, one in a function body, one in a capture block that also uses the" >&2
  echo "[patterns] value, one in an if-branch with an else and a second statement, a multi-line" >&2
  echo "[patterns] \`defer {\` block, a braceless else-if leaving a loop defer, and two on a" >&2
  echo "[patterns] single line with a following statement, two loop bodies -- one freeing a local and" >&2
  echo "[patterns] one freeing the loop's own capture -- and one freeing a capture in an \`} else if\`" >&2
  echo "[patterns] head, which is the same exemption as the plain \`if\` spelled the other way. The loop" >&2
  echo "[patterns] and capture cases are there because that value dies with its block, so a defer that" >&2
  echo "[patterns] frees it is scoped correctly; a lone defer in a loop body is the idiom, not a defect." >&2
  exit 1
fi

build_zig="zig/build.zig"
build_dir="$(dirname "$build_zig")/"

module_test_scan() {
  awk -v module_root="$build_dir" '
  function paren_delta(text,   tmp, opens, closes) {
    tmp = text; opens = gsub(/\(/, "", tmp)
    tmp = text; closes = gsub(/\)/, "", tmp)
    return opens - closes
  }
  function root_path(text,   p) {
    if (match(text, /root_source_file = b\.path\("[^"]+"\)/) == 0) return ""
    p = substr(text, RSTART, RLENGTH)
    sub(/^root_source_file = b\.path\("/, "", p)
    sub(/"\)$/, "", p)
    return p
  }
  function module_name(text,   name) {
    name = text
    sub(/^[[:space:]]*/, "", name)
    sub(/^const /, "", name)
    if (match(name, /^[A-Za-z_][A-Za-z0-9_]* = b\.createModule\(/) == 0) return ""
    sub(/ = b\.createModule\(.*/, "", name)
    return name
  }
  function has_test_block(path,   file, line) {
    file = module_root path
    while ((getline line < file) > 0) {
      if (line ~ /^[[:space:]]*test[[:space:]]*("|\{)/) { close(file); return 1 }
    }
    close(file)
    return 0
  }
  function walk(pass,   i, line, p, name, ref, rest, tail, delta) {
    depth = 0; in_test = 0; test_base = 0; in_mod = 0; mod_base = 0; cur_mod = ""; cur_root = ""
    for (i = 1; i <= n; i++) {
      line = lines[i]
      delta = paren_delta(line)
      if (index(line, "b.addTest(") > 0) { in_test = 1; test_base = depth }
      name = module_name(line)
      if (name != "" && !in_test) {
        in_mod = 1; mod_base = depth; cur_mod = name; cur_root = ""
      }
      if (in_mod && index(line, "b.createModule(") > 0 && module_name(line) == "") {
        unresolved["a nested createModule inside " cur_mod] = 1
      }
      p = root_path(line)
      if (p != "") {
        declared[p] = 1
        if (in_mod) cur_root = p
        if (in_test && pass == 2) tested[p] = 1
      }
      if (pass == 2 && in_test && match(line, /\.root_module =/)) {
        rest = substr(line, RSTART + RLENGTH + 1)
        sub(/^[[:space:]]+/, "", rest)
        if (rest ~ /^b\.createModule/) {
          inline_root = 1
        } else if (match(rest, /^[A-Za-z_][A-Za-z0-9_]*/)) {
          ref = substr(rest, 1, RLENGTH)
          tail = substr(rest, RLENGTH + 1, 1)
          if (tail == ".") {
            unresolved[ref ".<field>"] = 1
          } else if (ref in modroot) {
            tested[modroot[ref]] = 1
          } else {
            unresolved[ref] = 1
          }
        }
      }
      depth += delta
      if (in_mod && depth <= mod_base) { modroot[cur_mod] = cur_root; in_mod = 0 }
      if (in_test && depth <= test_base) in_test = 0
    }
  }
  { lines[++n] = $0 }
  END {
    walk(1)
    walk(2)
    for (name in unresolved) print "unresolved " name
    for (p in declared) {
      if (p in tested) continue
      if (p !~ /\.zig$/) continue
      if (has_test_block(p)) untested[p] = 1
    }
    for (p in untested) print p
  }
  ' "$build_zig"
}

echo "[patterns] checking every module root with test blocks has an addTest..."
module_test_scan_result="$(module_test_scan)"
unresolved_root_modules="$(printf "%s\n" "$module_test_scan_result" | grep "^unresolved " | sed "s/^unresolved //" || true)"
if [[ -n "$unresolved_root_modules" ]]; then
  echo "[patterns] build.zig has a root module this check cannot resolve:" >&2
  printf "%s\n" "$unresolved_root_modules" >&2
  echo "[patterns] update scripts/check-zig-patterns.sh when build.zig hands addTest a" >&2
  echo "[patterns] module that is neither a declared name nor an inline createModule," >&2
  echo "[patterns] nests a createModule inside a declared module, or reaches a root" >&2
  echo "[patterns] through another module's field. Each would otherwise be counted" >&2
  echo "[patterns] as untested and reported with the wrong reason." >&2
  exit 1
fi

module_roots_without_test_wiring="$(printf "%s\n" "$module_test_scan_result" | grep -v "^unresolved " | sort)"
if [[ -n "$module_roots_without_test_wiring" ]]; then
  echo "[patterns] module root carries test blocks that nothing compiles:" >&2
  printf "%s\n" "$module_roots_without_test_wiring" | sed "s|^|$build_dir|" >&2
  echo "[patterns] a b.createModule root is its own Zig module, and only the root source" >&2
  echo "[patterns] file of a test compilation runs test blocks, so importing one of these" >&2
  echo "[patterns] into another module's addTest compiles none of them and no failure will" >&2
  echo "[patterns] ever report them. Give the module an addTest over the declared module," >&2
  echo "[patterns] wired into test and into the test-unit-* group CI invokes." >&2
  exit 1
fi

echo "[patterns] checking providers do not drop stream events..."
dropped_events="$(grep -n '\.push(' zig/src/providers/*.zig | grep -v 'keepalive' || true)"
if [[ -n "$dropped_events" ]]; then
  echo "[patterns] provider uses push() for a non-keepalive event:" >&2
  echo "$dropped_events" >&2
  echo "[patterns] push() returns QueueFull when a slow consumer fills the ring, and providers" >&2
  echo "[patterns] historically discarded that error, silently losing streamed content while the" >&2
  echo "[patterns] final message stayed complete. Use pushBlocking() for every semantic event;" >&2
  echo "[patterns] only keepalive may be dropped, because it is advisory." >&2
  exit 1
fi

echo "[patterns] ok"
