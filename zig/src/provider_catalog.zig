const std = @import("std");
const data = @import("data");

pub const AuthKind = data.AuthKind;
pub const Offering = data.Offering;
pub const Status = data.Status;
pub const Endpoint = data.Endpoint;
pub const OAuthOrigin = data.OAuthOrigin;
pub const Provider = data.Provider;
pub const Pinned = data.Pinned;

pub const all = data.providers;

pub const pinned = data.pinned;

pub fn offering(id: []const u8) ?Offering {
    const row = provider(id) orelse return null;
    return row.offering;
}

pub fn status(id: []const u8) Status {
    const row = provider(id) orelse return .supported;
    return row.status orelse .supported;
}

pub const coding_plan_ids = blk: {
    var plan_rows: usize = 0;
    for (all) |row| {
        if (row.offering != null and row.offering.? == .coding_plan) plan_rows += 1;
    }
    var collected: [plan_rows][]const u8 = undefined;
    var index: usize = 0;
    for (all) |row| {
        if (row.offering != null and row.offering.? == .coding_plan) {
            collected[index] = row.id;
            index += 1;
        }
    }
    const frozen = collected;
    break :blk &frozen;
};

pub const current_ids = blk: {
    var current_rows: usize = 0;
    for (all) |row| {
        if (row.status != null and row.status.? == .current) current_rows += 1;
    }
    var collected: [current_rows][]const u8 = undefined;
    var index: usize = 0;
    for (all) |row| {
        if (row.status != null and row.status.? == .current) {
            collected[index] = row.id;
            index += 1;
        }
    }
    const frozen = collected;
    break :blk &frozen;
};

pub fn count() usize {
    return all.len;
}

pub fn provider(id: []const u8) ?Provider {
    for (all) |row| {
        if (std.mem.eql(u8, row.id, id)) return row;
    }
    return null;
}

pub const ids = blk: {
    var collected: [all.len][]const u8 = undefined;
    for (all, 0..) |row, index| collected[index] = row.id;
    const frozen = collected;
    break :blk &frozen;
};

pub fn credentialEnv(id: []const u8) []const []const u8 {
    const row = provider(id) orelse return &.{};
    return row.credential_env;
}

pub fn baseUrlEnv(id: []const u8) []const []const u8 {
    const row = provider(id) orelse return &.{};
    return row.base_url_env;
}

pub fn regionEnv(id: []const u8) ?[]const u8 {
    const row = provider(id) orelse return null;
    return row.region_env;
}

pub fn modelsEndpoint(id: []const u8) ?[]const u8 {
    const row = provider(id) orelse return null;
    return row.models_endpoint;
}

pub fn baseUrl(id: []const u8, wire: []const u8, region: ?[]const u8) ?[]const u8 {
    return endpointOf(id, wire, region);
}

pub fn defaultBaseUrl(id: []const u8) ?[]const u8 {
    return defaultBaseUrlOf(id, null);
}

fn defaultBaseUrlOf(id: []const u8, region: ?[]const u8) ?[]const u8 {
    const row = provider(id) orelse return null;
    if (row.base_url_source == null) return null;
    for (row.endpoints) |endpoint| {
        if (region) |wanted| {
            const served = endpoint.region orelse continue;
            if (!std.mem.eql(u8, served, wanted)) continue;
        } else if (endpoint.region != null) continue;
        return endpoint.base_url;
    }
    return null;
}

fn endpointOf(id: []const u8, wire: []const u8, region: ?[]const u8) ?[]const u8 {
    const row = provider(id) orelse return null;
    for (row.endpoints) |endpoint| {
        if (!std.mem.eql(u8, endpoint.wire, wire)) continue;
        if (region) |wanted| {
            const served = endpoint.region orelse continue;
            if (!std.mem.eql(u8, served, wanted)) continue;
        } else if (endpoint.region != null) continue;
        return endpoint.base_url;
    }
    return null;
}

pub fn baseUrlOrCompileError(id: []const u8, wire: []const u8, region: ?[]const u8) []const u8 {
    return baseUrl(id, wire, region) orelse
        @compileError("providers/catalog.json records no such endpoint; add the row rather than the literal");
}

pub fn oauthOrigin(id: []const u8) ?OAuthOrigin {
    const row = provider(id) orelse return null;
    return row.oauth_origin;
}

pub const Wire = struct {
    id: []const u8,
    suffix: []const u8,
    dedup_version: bool = false,
    model_scoped: bool = false,
};

pub const wire_paths = [_]Wire{
    .{ .id = "openai-completions", .suffix = "/v1/chat/completions", .dedup_version = true },
    .{ .id = "openai-responses", .suffix = "/v1/responses" },
    .{ .id = "openai-codex-responses", .suffix = "/responses" },
    .{ .id = "anthropic-messages", .suffix = "/v1/messages", .dedup_version = true },
    .{ .id = "ollama", .suffix = "/api/chat" },
    .{ .id = "google-generative-ai", .suffix = "", .model_scoped = true },
};

pub fn wirePath(wire: []const u8) ?Wire {
    for (wire_paths) |path| {
        if (std.mem.eql(u8, path.id, wire)) return path;
    }
    return null;
}

fn isVersionSegment(segment: []const u8) bool {
    if (segment.len < 2 or segment[0] != 'v') return false;
    for (segment[1..]) |digit| {
        if (digit < '0' or digit > '9') return false;
    }
    return true;
}

fn pathHasVersion(base_url: []const u8) bool {
    @setEvalBranchQuota(4000);
    const scheme = std.mem.indexOf(u8, base_url, "://") orelse return false;
    var rest: []const u8 = base_url[scheme + 3 ..];
    const cut = std.mem.indexOfScalar(u8, rest, '/') orelse return false;
    rest = rest[cut + 1 ..];
    var segments = std.mem.tokenizeScalar(u8, rest, '/');
    while (segments.next()) |segment| {
        if (isVersionSegment(segment)) return true;
    }
    return false;
}

pub const UrlParts = struct {
    head: []const u8,
    tail: []const u8,
};

pub fn urlParts(base: []const u8, wire: Wire) UrlParts {
    const head = std.mem.trimEnd(u8, base, "/");
    if (wire.suffix.len == 0) return .{ .head = head, .tail = "" };
    if (std.mem.endsWith(u8, head, wire.suffix)) return .{ .head = head, .tail = "" };
    const tail = if (wire.dedup_version and pathHasVersion(head) and std.mem.startsWith(u8, wire.suffix, "/v1/"))
        wire.suffix["/v1".len..]
    else
        wire.suffix;
    return .{ .head = head, .tail = tail };
}

pub fn joinUrl(comptime base: []const u8, comptime wire: Wire) []const u8 {
    const parts = comptime urlParts(base, wire);
    if (parts.tail.len == 0) return base[0..parts.head.len];
    return base[0..parts.head.len] ++ parts.tail;
}

pub fn joinUrlOwned(allocator: std.mem.Allocator, base: []const u8, wire: Wire) ![]const u8 {
    const parts = urlParts(base, wire);
    if (parts.tail.len == 0) return allocator.dupe(u8, parts.head);
    return std.mem.concat(allocator, u8, &.{ parts.head, parts.tail });
}

pub fn joinModelsUrl(comptime base: []const u8, comptime path: []const u8) []const u8 {
    return joinUrl(base, .{ .id = "models", .suffix = path });
}

pub const Resolved = struct {
    id: []const u8,
    wire: []const u8,
    region: ?[]const u8,
    base_url: []const u8,
    models_url: ?[]const u8,
    request_url: ?[]const u8,
};

pub const resolved = blk: {
    var endpoints: usize = 0;
    for (all) |row| endpoints += row.endpoints.len;
    var collected: [endpoints]Resolved = undefined;
    var index: usize = 0;
    for (all) |row| {
        for (row.endpoints) |endpoint| {
            const path = wirePath(endpoint.wire);
            collected[index] = .{
                .id = row.id,
                .wire = endpoint.wire,
                .region = endpoint.region,
                .base_url = endpoint.base_url,
                .models_url = if (row.models_endpoint) |models_path| joinModelsUrl(endpoint.base_url, models_path) else null,
                .request_url = if (path) |known| if (known.model_scoped) null else joinUrl(endpoint.base_url, known) else null,
            };
            index += 1;
        }
    }
    const frozen = collected;
    break :blk &frozen;
};

fn serves(entry: Resolved, id: []const u8, region: ?[]const u8) bool {
    if (!std.mem.eql(u8, entry.id, id)) return false;
    if (region) |wanted| {
        const served_region = entry.region orelse return false;
        return std.mem.eql(u8, served_region, wanted);
    }
    return entry.region == null;
}

pub fn modelsUrl(id: []const u8, region: ?[]const u8) ?[]const u8 {
    for (resolved) |entry| {
        if (serves(entry, id, region)) return entry.models_url;
    }
    return null;
}

pub fn requestUrl(id: []const u8, wire: []const u8, region: ?[]const u8) ?[]const u8 {
    for (resolved) |entry| {
        if (!std.mem.eql(u8, entry.wire, wire)) continue;
        if (serves(entry, id, region)) return entry.request_url;
    }
    return null;
}

test "every row names a unique id" {
    try std.testing.expect(all.len > 0);
    try std.testing.expectEqual(all.len, count());
    try std.testing.expectEqual(all.len, ids.len);
    for (all, 0..) |row, index| {
        try std.testing.expect(row.id.len > 0);
        for (ids[index + 1 ..]) |other| {
            try std.testing.expect(!std.mem.eql(u8, row.id, other));
        }
    }
}

test "every static row answers a base URL for each wire it declares" {
    for (all) |row| {
        if (row.base_url_source == null or !std.mem.eql(u8, row.base_url_source.?, "static")) continue;
        try std.testing.expect(row.endpoints.len > 0);
        for (row.wires) |wire| {
            var served = false;
            for (row.endpoints) |endpoint| {
                if (std.mem.eql(u8, endpoint.wire, wire)) served = true;
            }
            try std.testing.expect(served);
        }
    }
}

test "a regional provider answers no endpoint without naming its region" {
    for (all) |row| {
        var regional = false;
        for (row.endpoints) |endpoint| {
            if (endpoint.region != null) regional = true;
        }
        if (!regional) continue;
        for (row.endpoints) |endpoint| {
            try std.testing.expect(baseUrl(row.id, endpoint.wire, null) == null);
            try std.testing.expectEqualStrings(
                endpoint.base_url,
                baseUrl(row.id, endpoint.wire, endpoint.region.?).?,
            );
        }
    }
}

test "a default base URL is answered only by a row that records one" {
    try std.testing.expectEqualStrings(
        "https://generativelanguage.googleapis.com",
        defaultBaseUrl("google").?,
    );
    try std.testing.expect(defaultBaseUrl("kimi") == null);
    try std.testing.expect(defaultBaseUrl("azure") == null);
    try std.testing.expect(defaultBaseUrl("ollama") == null);
    try std.testing.expect(defaultBaseUrl("github-copilot") == null);
    try std.testing.expect(defaultBaseUrl("no-such-provider") == null);
}

test "a base URL is answered for the wire that names it and for no other" {
    try std.testing.expectEqualStrings(
        "https://api.anthropic.com",
        baseUrl("anthropic", "anthropic-messages", null).?,
    );
    try std.testing.expect(baseUrl("anthropic", "openai-completions", null) == null);
    try std.testing.expectEqualStrings(
        "https://api.openai.com",
        baseUrl("openai", "openai-responses", null).?,
    );
    try std.testing.expect(baseUrl("no-such-provider", "openai-completions", null) == null);
    try std.testing.expect(baseUrl("kimi", "openai-completions", "atlantis") == null);
}

test "credential and base URL environment names are recorded as names" {
    try std.testing.expectEqual(@as(usize, 2), credentialEnv("anthropic").len);
    try std.testing.expectEqualStrings("ANTHROPIC_AUTH_TOKEN", credentialEnv("anthropic")[0]);
    try std.testing.expectEqualStrings("KIMI_API_KEY", credentialEnv("kimi")[0]);
    try std.testing.expect(credentialEnv("openai-codex").len == 0);
    try std.testing.expectEqualStrings("ANTHROPIC_BASE_URL", baseUrlEnv("anthropic")[0]);
    try std.testing.expect(baseUrlEnv("kimi").len == 0);
    try std.testing.expectEqualStrings("KIMI_REGION", regionEnv("kimi").?);
    try std.testing.expect(regionEnv("anthropic") == null);
}

test "at most one base URL environment variable is resolved per provider" {
    for (all) |row| {
        try std.testing.expect(row.base_url_env.len <= 1);
        try std.testing.expect(row.credential_env.len <= 2);
    }
}

test "a provider with a base URL override names it, and one without names none" {
    const with_override = [_][]const u8{ "anthropic", "openai", "deepseek", "ollama", "google", "azure" };
    for (with_override) |id| {
        try std.testing.expectEqual(@as(usize, 1), baseUrlEnv(id).len);
    }
    for (all) |row| {
        var expected = false;
        for (with_override) |id| {
            if (std.mem.eql(u8, row.id, id)) expected = true;
        }
        if (expected) continue;
        try std.testing.expect(baseUrlEnv(row.id).len == 0);
    }
}

test "the origin policy is data, including a per-tenant domain" {
    const anthropic_policy = oauthOrigin("anthropic").?;
    try std.testing.expectEqual(@as(usize, 1), anthropic_policy.exact.len);
    try std.testing.expect(anthropic_policy.domain == null);
    try std.testing.expect(!anthropic_policy.credential_declares_origin);

    const copilot_policy = oauthOrigin("github-copilot").?;
    try std.testing.expectEqualStrings("githubcopilot.com", copilot_policy.domain.?);
    try std.testing.expect(copilot_policy.credential_declares_origin);
    try std.testing.expect(copilot_policy.exact.len == 0);

    try std.testing.expect(oauthOrigin("kimi") == null);
    try std.testing.expect(oauthOrigin("no-such-provider") == null);
}

test "a models listing is recorded only where the provider answers one" {
    try std.testing.expectEqualStrings("/v1/models", modelsEndpoint("anthropic").?);
    try std.testing.expectEqualStrings("/v1/models", modelsEndpoint("deepseek").?);
    try std.testing.expectEqualStrings("/models", modelsEndpoint("openrouter").?);
    try std.testing.expectEqualStrings("/models", modelsEndpoint("xiaomi").?);
    try std.testing.expect(modelsEndpoint("openai-codex") == null);
    try std.testing.expect(modelsEndpoint("github-copilot") == null);
    try std.testing.expect(modelsEndpoint("ollama") == null);
    try std.testing.expect(modelsEndpoint("no-such-provider") == null);
}

test "a models listing is an absolute path appended to a base that does not end in a slash" {
    for (all) |row| {
        const path_text = row.models_endpoint orelse continue;
        try std.testing.expect(std.mem.startsWith(u8, path_text, "/"));
        for (row.endpoints) |endpoint| {
            try std.testing.expect(!std.mem.endsWith(u8, endpoint.base_url, "/"));
            var composed: [512]u8 = undefined;
            const url = try std.fmt.bufPrint(&composed, "{s}{s}", .{ endpoint.base_url, path_text });
            var versions: usize = 0;
            var segments = std.mem.tokenizeScalar(u8, url, '/');
            while (segments.next()) |segment| {
                if (segment.len < 2 or segment[0] != 'v') continue;
                for (segment[1..]) |digit| {
                    if (digit < '0' or digit > '9') break;
                } else {
                    versions += 1;
                }
            }
            try std.testing.expect(versions <= 1);
        }
    }
}

test "every wire a row names is a wire this file joins, or is model-scoped" {
    for (all) |row| {
        for (row.wires) |wire| {
            const path = wirePath(wire) orelse return error.TestUnexpectedResult;
            for (row.endpoints) |endpoint| {
                if (!std.mem.eql(u8, endpoint.wire, wire)) continue;
                if (path.model_scoped) {
                    try std.testing.expect(requestUrl(row.id, wire, endpoint.region) == null);
                } else {
                    try std.testing.expect(requestUrl(row.id, wire, endpoint.region) != null);
                }
            }
        }
    }
}

test "a request URL is the row's base and its wire's path, joined as the wire's module joins it" {
    try std.testing.expectEqualStrings("https://api.openai.com/v1/chat/completions", requestUrl("openai", "openai-completions", null).?);
    try std.testing.expectEqualStrings("https://api.openai.com/v1/responses", requestUrl("openai", "openai-responses", null).?);
    try std.testing.expectEqualStrings("https://chatgpt.com/backend-api/codex/responses", requestUrl("openai-codex", "openai-codex-responses", null).?);
    try std.testing.expectEqualStrings("https://api.anthropic.com/v1/messages", requestUrl("anthropic", "anthropic-messages", null).?);
    try std.testing.expectEqualStrings("https://api.minimax.io/anthropic/v1/messages", requestUrl("minimax-coding-plan", "anthropic-messages", null).?);
    try std.testing.expectEqualStrings("https://api.deepinfra.com/v1/openai/chat/completions", requestUrl("deepinfra", "openai-completions", null).?);
}

test "a base ending in a version segment gains no second one on the wire that dedups it" {
    for (resolved) |entry| {
        const url = entry.request_url orelse continue;
        var versions: usize = 0;
        var segments = std.mem.tokenizeScalar(u8, url, '/');
        while (segments.next()) |segment| {
            if (isVersionSegment(segment)) versions += 1;
        }
        try std.testing.expect(versions <= 1);
    }
    try std.testing.expectEqualStrings("https://openrouter.ai/api/v1/chat/completions", requestUrl("openrouter", "openai-completions", null).?);
    try std.testing.expectEqualStrings("https://api.xiaomimimo.com/v1/chat/completions", requestUrl("xiaomi", "openai-completions", null).?);
    try std.testing.expectEqualStrings("https://api.z.ai/api/coding/paas/v4/chat/completions", requestUrl("zai-coding-plan", "openai-completions", null).?);
    try std.testing.expectEqualStrings("https://api.lkeap.cloud.tencent.com/coding/v3/chat/completions", requestUrl("tencent-coding-plan", "openai-completions", null).?);
    try std.testing.expectEqualStrings("https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions", requestUrl("volcengine-coding-plan", "openai-completions", null).?);
}

test "a models URL is the row's own base and the path it records" {
    try std.testing.expectEqualStrings("https://api.anthropic.com/v1/models", modelsUrl("anthropic", null).?);
    try std.testing.expectEqualStrings("https://openrouter.ai/api/v1/models", modelsUrl("openrouter", null).?);
    try std.testing.expectEqualStrings("https://api.z.ai/api/coding/paas/v4/models", modelsUrl("zai-coding-plan", null).?);
    try std.testing.expectEqualStrings("https://api.minimax.io/anthropic/v1/models", modelsUrl("minimax-coding-plan", null).?);
}

test "a region picks the endpoint that serves it, and names no default without one" {
    try std.testing.expectEqualStrings("https://api.kimi.com/coding/v1/chat/completions", requestUrl("kimi", "openai-completions", "china").?);
    try std.testing.expectEqualStrings("https://api.moonshot.ai/v1/chat/completions", requestUrl("kimi", "openai-completions", "global").?);
    try std.testing.expectEqualStrings("https://api.kimi.com/coding/v1/models", modelsUrl("kimi", "china").?);
    try std.testing.expectEqualStrings("https://api.moonshot.ai/v1/models", modelsUrl("kimi", "global").?);
    try std.testing.expect(requestUrl("kimi", "openai-completions", null) == null);
    try std.testing.expect(modelsUrl("kimi", null) == null);
    try std.testing.expect(requestUrl("kimi", "openai-completions", "mars") == null);
    try std.testing.expect(modelsUrl("kimi", "mars") == null);
}

test "a row with no static endpoint resolves no URL, and a model-scoped wire resolves no request URL" {
    try std.testing.expect(requestUrl("ollama", "ollama", null) == null);
    try std.testing.expect(modelsUrl("ollama", null) == null);
    try std.testing.expect(requestUrl("github-copilot", "openai-completions", null) == null);
    try std.testing.expect(requestUrl("azure", "openai-responses", null) == null);
    try std.testing.expect(requestUrl("google", "google-generative-ai", null) == null);
    try std.testing.expect(modelsUrl("google", null) == null);
    try std.testing.expect(requestUrl("no-such-provider", "openai-completions", null) == null);
    try std.testing.expect(requestUrl("openai", "no-such-wire", null) == null);
    try std.testing.expect(modelsUrl("no-such-provider", null) == null);
}

test "an offering is a plan, a subscription or an api key, and one host serves one row" {
    const plans = coding_plan_ids;
    try std.testing.expect(plans.len > 0);
    for (plans) |id| {
        try std.testing.expect(offering(id).? == .coding_plan);
    }
    var meters: usize = 0;
    var subscriptions: usize = 0;
    for (all) |row| {
        if (row.offering != null and row.offering.? == .api_key) meters += 1;
        const subscribes = row.offering != null and row.offering.? == .subscription;
        if (subscribes) subscriptions += 1;
        try std.testing.expectEqual(!subscribes, row.credential_env.len > 0);
    }
    try std.testing.expect(meters > 0);
    try std.testing.expect(subscriptions > 0);
    try std.testing.expectEqualStrings("zai-coding-plan", plans[0]);
    try std.testing.expect(offering("kimi").? == .coding_plan);
    try std.testing.expect(offering("xiaomi").? == .api_key);
    try std.testing.expect(offering("openrouter").? == .api_key);
    try std.testing.expect(offering("openai-codex").? == .subscription);
    try std.testing.expect(offering("no-such-provider") == null);
    for (all, 0..) |row, index| {
        for (row.endpoints) |endpoint| {
            for (all[index + 1 ..]) |other| {
                for (other.endpoints) |served| {
                    try std.testing.expect(!std.mem.eql(u8, endpoint.base_url, served.base_url));
                }
            }
        }
    }
}

test "the current rows are the ones the catalog names first" {
    const current = current_ids;
    try std.testing.expect(current.len > 0);
    var named_first = true;
    var index: usize = 0;
    for (all) |row| {
        if (row.status != null and row.status.? == .current) {
            try std.testing.expect(named_first);
            try std.testing.expectEqualStrings(current[index], row.id);
            index += 1;
        } else {
            named_first = false;
        }
    }
    try std.testing.expectEqual(current.len, index);
    try std.testing.expectEqualStrings("openai", current[0]);
    try std.testing.expect(status("kimi") == .current);
    try std.testing.expect(status("vercel") == .supported);
    try std.testing.expect(status("no-such-provider") == .supported);
}

test "every row records how it authenticates, and an origin policy belongs to an oauth row" {
    for (all) |row| {
        try std.testing.expect(row.auth.len > 0);
        var speaks_oauth = false;
        for (row.auth) |kind| {
            switch (kind) {
                .api_key => {},
                .oauth => speaks_oauth = true,
                .none => {},
            }
        }
        try std.testing.expectEqual(speaks_oauth, row.oauth_origin != null);
    }
}


fn expectSameOptionalString(want: ?[]const u8, got: ?[]const u8) !void {
    if (want == null or got == null) {
        try std.testing.expect(want == null and got == null);
        return;
    }
    try std.testing.expectEqualStrings(want.?, got.?);
}

test "every catalogued endpoint resolves the two URLs providers/resolved_urls.json pins" {
    try std.testing.expectEqual(pinned.len, resolved.len);
    for (resolved, 0..) |entry, index| {
        const want = pinned[index];
        try std.testing.expectEqualStrings(want.id, entry.id);
        try std.testing.expectEqualStrings(want.wire, entry.wire);
        try expectSameOptionalString(want.region, entry.region);
        try std.testing.expectEqualStrings(want.base_url, entry.base_url);
        try expectSameOptionalString(want.models_url, entry.models_url);
        try expectSameOptionalString(want.request_url, entry.request_url);
    }
}

const google_wire = wirePath("google-generative-ai") orelse unreachable;

test "a wire with no path joins to the base, trailing slash dropped" {
    try std.testing.expectEqualStrings("https://generativelanguage.example", joinUrl("https://generativelanguage.example/", google_wire));
    const owned = try joinUrlOwned(std.testing.allocator, "https://generativelanguage.example///", google_wire);
    defer std.testing.allocator.free(owned);
    try std.testing.expectEqualStrings("https://generativelanguage.example", owned);
}

test "the models path is joined by the same rule as a wire path" {
    const wire = Wire{ .id = "models", .suffix = "/v1/models" };
    try std.testing.expectEqualStrings("https://api.openai.com/v1/models", joinModelsUrl("https://api.openai.com/", "/v1/models"));
    const cases = [_]struct { base: []const u8, want: []const u8 }{
        .{ .base = "https://api.openai.com", .want = "https://api.openai.com/v1/models" },
        .{ .base = "https://api.openai.com/", .want = "https://api.openai.com/v1/models" },
        .{ .base = "https://api.openai.com///", .want = "https://api.openai.com/v1/models" },
        .{ .base = "https://api.openai.com/v1/models", .want = "https://api.openai.com/v1/models" },
        .{ .base = "https://api.openai.com/v1/models/", .want = "https://api.openai.com/v1/models" },
    };
    for (cases) |case| {
        const owned = try joinUrlOwned(std.testing.allocator, case.base, wire);
        defer std.testing.allocator.free(owned);
        try std.testing.expectEqualStrings(case.want, owned);
    }
}
