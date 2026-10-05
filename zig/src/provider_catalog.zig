const std = @import("std");
const builtin = @import("builtin");
const data = @import("data");
const ai_types = @import("ai_types");
const compat = @import("compat");

pub const AuthKind = data.AuthKind;
pub const Offering = data.Offering;
pub const Status = data.Status;
pub const Endpoint = data.Endpoint;
pub const Model = data.Model;
pub const OAuthOrigin = data.OAuthOrigin;
pub const Provider = data.Provider;
pub const Pinned = data.Pinned;

pub const all = data.providers;

pub const pinned = data.pinned;

pub const catalog_loader_rows = [_][]const u8{
    "deepseek",
    "openrouter",
    "opencode-zen",
    "opencode-go",
    "vercel",
    "zenmux",
    "deepinfra",
    "zai-coding-plan",
    "alibaba-coding-plan",
    "minimax-coding-plan",
    "tencent-coding-plan",
    "volcengine-coding-plan",
    "openai",
    "kimi",
};

pub fn acceptsOverride(id: []const u8) bool {
    return servedByCatalogLoader(id) or std.mem.eql(u8, id, "anthropic") or std.mem.eql(u8, id, "openai-codex");
}

pub fn servedByCatalogLoader(id: []const u8) bool {
    for (catalog_loader_rows) |row| {
        if (std.mem.eql(u8, row, id)) return true;
    }
    return false;
}

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

pub const EnvReader = *const fn (std.mem.Allocator, []const u8, ?*anyopaque) anyerror!?[]u8;

fn readOwnEnv(allocator: std.mem.Allocator, name: []const u8, ctx: ?*anyopaque) anyerror!?[]u8 {
    _ = ctx;
    const value = compat.getEnvVarOwned(allocator, name) catch |err| switch (err) {
        error.EnvironmentVariableMissing => return null,
        else => return err,
    };
    return value;
}

pub fn apiKeyFromNames(
    allocator: std.mem.Allocator,
    names: []const []const u8,
    reader: EnvReader,
    ctx: ?*anyopaque,
) ?[]const u8 {
    for (names) |name| {
        const maybe = reader(allocator, name, ctx) catch continue;
        const value = maybe orelse continue;
        if (value.len > 0) return value;
        allocator.free(value);
    }
    return null;
}

pub fn apiKeyForProvider(
    allocator: std.mem.Allocator,
    provider_id: []const u8,
    reader: EnvReader,
    ctx: ?*anyopaque,
) ?[]const u8 {
    return apiKeyFromNames(allocator, credentialEnv(provider_id), reader, ctx);
}

pub fn apiKeyFromEnv(allocator: std.mem.Allocator, provider_id: []const u8) ?[]const u8 {
    return apiKeyForProvider(allocator, provider_id, readOwnEnv, null);
}

pub fn regionEnv(id: []const u8) ?[]const u8 {
    const row = provider(id) orelse return null;
    return row.region_env;
}

pub fn defaultRegion(id: []const u8) ?[]const u8 {
    const row = provider(id) orelse return null;
    return row.default_region;
}

pub const global_base_url_env = "OAPX_BASE_URL";

pub fn blankEnvironment(allocator: std.mem.Allocator) !void {
    if (!builtin.is_test) return;
    try compat.setTestEnv(allocator, global_base_url_env, "");
    for (all) |row| {
        for (row.credential_env) |name| try compat.setTestEnv(allocator, name, "");
        for (row.base_url_env) |name| try compat.setTestEnv(allocator, name, "");
        if (row.region_env) |name| try compat.setTestEnv(allocator, name, "");
    }
}

pub fn modelsFor(id: []const u8) []const Model {
    const row = provider(id) orelse return &.{};
    return row.models;
}

pub fn declaredModel(id: []const u8, model_id: []const u8) ?Model {
    for (modelsFor(id)) |model| {
        if (std.mem.eql(u8, model.id, model_id)) return model;
    }
    return null;
}

pub fn rowContextWindow(id: []const u8) ?u32 {
    const row = provider(id) orelse return null;
    return row.context_window;
}

pub fn rowMaxContextWindow(id: []const u8) ?u32 {
    const row = provider(id) orelse return null;
    return row.max_context_window;
}

pub fn modelMaxContextWindow(id: []const u8, model_id: []const u8) ?u32 {
    if (declaredModel(id, model_id)) |model| {
        if (model.max_context_window) |window| return window;
    }
    return rowMaxContextWindow(id);
}

pub fn rowMaxTokens(id: []const u8) ?u32 {
    const row = provider(id) orelse return null;
    return row.max_tokens;
}

pub fn regionFromValue(id: []const u8, value: []const u8) ?[]const u8 {
    const trimmed = std.mem.trim(u8, value, " \t\r\n");
    if (trimmed.len == 0) return null;
    if (regionSynonym(id, trimmed)) |aliased| return aliased;
    for (regionsFor(id)) |region| {
        if (std.ascii.eqlIgnoreCase(region, trimmed)) return region;
    }
    return null;
}

fn regionSynonym(id: []const u8, value: []const u8) ?[]const u8 {
    if (!std.mem.eql(u8, id, "kimi")) return null;
    if (std.ascii.eqlIgnoreCase(value, "moonshot")) return "global";
    if (std.ascii.eqlIgnoreCase(value, "cn") or std.ascii.eqlIgnoreCase(value, "coding")) return "china";
    return null;
}

pub fn isRegional(id: []const u8) bool {
    const row = provider(id) orelse return false;
    for (row.endpoints) |endpoint| {
        if (endpoint.region != null) return true;
    }
    return false;
}

pub fn regionsFor(id: []const u8) []const []const u8 {
    for (resolved) |entry| {
        if (!std.mem.eql(u8, entry.id, id)) continue;
        return entry.regions;
    }
    return &.{};
}

fn regionNames(comptime row: Provider) []const []const u8 {
    comptime {
        var names: [row.endpoints.len][]const u8 = undefined;
        var total: usize = 0;
        for (row.endpoints) |endpoint| {
            const region = endpoint.region orelse continue;
            var repeated = false;
            for (names[0..total]) |prior| {
                if (std.mem.eql(u8, prior, region)) repeated = true;
            }
            if (repeated) continue;
            names[total] = region;
            total += 1;
        }
        const frozen = names;
        return frozen[0..total];
    }
}

pub fn credentialEnvIsSet(allocator: std.mem.Allocator, id: []const u8) !bool {
    const row = provider(id) orelse return false;
    for (row.credential_env) |name| {
        const value = compat.getEnvVarOwned(allocator, name) catch |err| switch (err) {
            error.EnvironmentVariableMissing => continue,
            else => return err,
        };
        defer allocator.free(value);
        if (value.len > 0) return true;
    }
    return false;
}

pub fn modelsEndpoint(id: []const u8) ?[]const u8 {
    const row = provider(id) orelse return null;
    return row.models_endpoint;
}

pub fn modelsDevKey(id: []const u8) ?[]const u8 {
    const row = provider(id) orelse return null;
    return row.models_dev;
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
    model_scoped: bool = false,
};

pub const wire_paths = [_]Wire{
    .{ .id = "openai-completions", .suffix = "/v1/chat/completions" },
    .{ .id = "openai-responses", .suffix = "/v1/responses" },
    .{ .id = "openai-codex-responses", .suffix = "/responses" },
    .{ .id = "anthropic-messages", .suffix = "/v1/messages" },
    .{ .id = "ollama", .suffix = "/api/chat" },
    .{ .id = "google-generative-ai", .suffix = "", .model_scoped = true },
};

pub fn wirePath(wire: []const u8) ?Wire {
    for (wire_paths) |path| {
        if (std.mem.eql(u8, path.id, wire)) return path;
    }
    return null;
}

pub fn firstImplementedWire(row: Provider) ?Wire {
    for (row.wires) |id| {
        if (wirePath(id)) |wire| return wire;
    }
    return null;
}

pub fn isResponsesOnlyModel(model_id: []const u8) bool {
    return std.mem.startsWith(u8, model_id, "o1-pro") or
        std.mem.startsWith(u8, model_id, "o3-pro") or
        std.mem.startsWith(u8, model_id, "gpt-5-pro") or
        std.mem.startsWith(u8, model_id, "gpt-5-codex") or
        std.mem.startsWith(u8, model_id, "gpt-5.1-codex-max") or
        std.mem.indexOf(u8, model_id, "deep-research") != null or
        std.mem.startsWith(u8, model_id, "computer-use-preview");
}

pub fn wireForModel(provider_id: []const u8, model_id: []const u8) ?Wire {
    const row = provider(provider_id) orelse return null;
    if (std.mem.eql(u8, provider_id, "openai") and isResponsesOnlyModel(model_id)) {
        if (wirePath("openai-responses")) |wire| return wire;
    }
    return firstImplementedWire(row);
}

pub fn declaresWire(provider_id: []const u8, wire: []const u8) bool {
    const row = provider(provider_id) orelse return false;
    for (row.wires) |declared| {
        if (std.mem.eql(u8, declared, wire)) return true;
    }
    return false;
}

pub fn endpointCarriesVersion(provider_id: []const u8, base_url: []const u8) bool {
    const row = provider(provider_id) orelse return false;
    const wanted = std.mem.trimEnd(u8, base_url, "/");
    for (row.endpoints) |endpoint| {
        if (!std.mem.eql(u8, std.mem.trimEnd(u8, endpoint.base_url, "/"), wanted)) continue;
        return endpoint.carries_version;
    }
    return false;
}

pub fn carriesVersionFor(provider_id: []const u8, base_url: []const u8, stated: ?bool) bool {
    if (stated) |given| return given;
    if (endpointCarriesVersion(provider_id, base_url)) return true;
    return baseCarriesTrailingVersion(base_url);
}

pub fn baseCarriesTrailingVersion(base_url: []const u8) bool {
    const trimmed = std.mem.trimEnd(u8, base_url, "/");
    return std.mem.endsWith(u8, trimmed, "/v1");
}

pub const UrlParts = struct {
    head: []const u8,
    tail: []const u8,
};

pub fn urlParts(base: []const u8, wire: Wire, carries_version: bool) UrlParts {
    const head = std.mem.trimEnd(u8, base, "/");
    if (wire.suffix.len == 0) return .{ .head = head, .tail = "" };
    if (std.mem.endsWith(u8, head, wire.suffix)) return .{ .head = head, .tail = "" };
    const tail = if (carries_version and std.mem.startsWith(u8, wire.suffix, "/v1/"))
        wire.suffix["/v1".len..]
    else
        wire.suffix;
    return .{ .head = head, .tail = tail };
}

pub fn joinUrl(comptime base: []const u8, comptime wire: Wire, comptime carries_version: bool) []const u8 {
    const parts = comptime urlParts(base, wire, carries_version);
    if (parts.tail.len == 0) return base[0..parts.head.len];
    return base[0..parts.head.len] ++ parts.tail;
}

pub fn joinUrlOwned(allocator: std.mem.Allocator, base: []const u8, wire: Wire, carries_version: bool) ![]const u8 {
    const parts = urlParts(base, wire, carries_version);
    if (parts.tail.len == 0) return allocator.dupe(u8, parts.head);
    return std.mem.concat(allocator, u8, &.{ parts.head, parts.tail });
}

pub fn joinModelUrlOwned(allocator: std.mem.Allocator, model: ai_types.Model, wire: Wire) ![]const u8 {
    return joinUrlOwned(
        allocator,
        model.base_url,
        wire,
        carriesVersionFor(model.provider, model.base_url, model.carries_version),
    );
}

pub fn joinModelsUrl(comptime base: []const u8, comptime path: []const u8, comptime carries_version: bool) []const u8 {
    return joinUrl(base, .{ .id = "models", .suffix = path }, carries_version);
}

pub fn modelsListingPath(allocator: std.mem.Allocator, models_path: []const u8, carries_version: bool, overridden: bool) ![]u8 {
    if (carries_version and overridden) return std.fmt.allocPrint(allocator, "/v1{s}", .{models_path});
    return allocator.dupe(u8, models_path);
}

pub fn listingUrlOwned(
    allocator: std.mem.Allocator,
    base_url: []const u8,
    models_path: []const u8,
    carries_version: bool,
    overridden: bool,
) ![]const u8 {
    const path = try modelsListingPath(allocator, models_path, carries_version, overridden);
    defer allocator.free(path);
    return joinUrlOwned(allocator, base_url, .{ .id = "models", .suffix = path }, carries_version and !overridden);
}

pub const Resolved = struct {
    id: []const u8,
    wire: []const u8,
    region: ?[]const u8,
    base_url: []const u8,
    models_url: ?[]const u8,
    request_url: ?[]const u8,
    regions: []const []const u8 = &.{},
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
                .models_url = if (row.models_endpoint) |models_path| joinModelsUrl(endpoint.base_url, models_path, endpoint.carries_version) else null,
                .request_url = if (path) |known| if (known.model_scoped) null else joinUrl(endpoint.base_url, known, endpoint.carries_version) else null,
                .regions = regionNames(row),
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
    try std.testing.expectEqualStrings("opencode", modelsDevKey("opencode-zen").?);
    try std.testing.expectEqualStrings("opencode-go", modelsDevKey("opencode-go").?);
    try std.testing.expect(modelsDevKey("deepseek") == null);
    try std.testing.expect(modelsDevKey("no-such-provider") == null);
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

test "a row records the largest context window its models can be given, and only the rows that state one do" {
    const with_ceiling = [_][]const u8{ "openai", "openai-codex" };
    for (all) |row| {
        var expected = false;
        for (with_ceiling) |id| {
            if (std.mem.eql(u8, row.id, id)) expected = true;
        }
        try std.testing.expectEqual(expected, rowMaxContextWindow(row.id) != null);
    }
    try std.testing.expectEqual(@as(?u32, 1_000_000), rowMaxContextWindow("openai"));
    try std.testing.expectEqual(@as(?u32, 1_000_000), rowMaxContextWindow("openai-codex"));
    try std.testing.expectEqual(@as(?u32, 1_000_000), modelMaxContextWindow("openai", "gpt-5-codex"));
    try std.testing.expectEqual(@as(?u32, null), rowMaxContextWindow("no-such-provider"));
    try std.testing.expectEqual(@as(?u32, null), modelMaxContextWindow("no-such-provider", "gpt-5-codex"));
}

test "a row that serves a window states no ceiling for it, because the window is not the ceiling" {
    try std.testing.expectEqual(@as(?u32, 262_144), rowContextWindow("kimi"));
    try std.testing.expectEqual(@as(?u32, null), modelMaxContextWindow("kimi", "kimi-k2.7-code"));
    try std.testing.expectEqual(@as(?u32, null), modelMaxContextWindow("anthropic", "claude-sonnet-4-5"));
}

test "no row or model records a context window above the ceiling it states" {
    for (all) |row| {
        if (row.context_window) |window| {
            if (rowMaxContextWindow(row.id)) |ceiling| try std.testing.expect(window <= ceiling);
        }
        for (row.models) |model| {
            if (model.context_window) |window| {
                if (modelMaxContextWindow(row.id, model.id)) |ceiling| try std.testing.expect(window <= ceiling);
            }
        }
    }
}

pub fn wireTakesVersionedPath(wire: []const u8) bool {
    const path = wirePath(wire) orelse return false;
    return std.mem.startsWith(u8, path.suffix, "/v1/");
}

test "no endpoint records carries_version for a wire whose path has no leading /v1/" {
    for (all) |row| {
        for (row.endpoints) |endpoint| {
            if (!endpoint.carries_version) continue;
            if (wirePath(endpoint.wire) == null) return error.TestWireClaimNotInCatalog;
            try std.testing.expect(wireTakesVersionedPath(endpoint.wire));
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

fn looksLikeVersion(segment: []const u8) bool {
    if (segment.len < 2 or segment[0] != 'v') return false;
    for (segment[1..]) |digit| {
        if (digit < '0' or digit > '9') return false;
    }
    return true;
}

test "a base recorded as carrying the version gains no second one on its wire" {
    for (resolved) |entry| {
        const url = entry.request_url orelse continue;
        var versions: usize = 0;
        var segments = std.mem.tokenizeScalar(u8, url, '/');
        while (segments.next()) |segment| {
            if (looksLikeVersion(segment)) versions += 1;
        }
        try std.testing.expect(versions <= 1);
    }
    try std.testing.expectEqualStrings("https://openrouter.ai/api/v1/chat/completions", requestUrl("openrouter", "openai-completions", null).?);
    try std.testing.expectEqualStrings("https://api.xiaomimimo.com/v1/chat/completions", requestUrl("xiaomi", "openai-completions", null).?);
    try std.testing.expectEqualStrings("https://api.z.ai/api/coding/paas/v4/chat/completions", requestUrl("zai-coding-plan", "openai-completions", null).?);
    try std.testing.expectEqualStrings("https://api.lkeap.cloud.tencent.com/coding/v3/chat/completions", requestUrl("tencent-coding-plan", "openai-completions", null).?);
    try std.testing.expectEqualStrings("https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions", requestUrl("volcengine-coding-plan", "openai-completions", null).?);
}

test "an endpoint recorded as carrying the version holds one in its base, and one that does not holds none" {
    for (all) |row| {
        for (row.endpoints) |endpoint| {
            var found = false;
            var segments = std.mem.tokenizeScalar(u8, endpoint.base_url, '/');
            while (segments.next()) |segment| {
                if (looksLikeVersion(segment)) found = true;
            }
            try std.testing.expectEqual(found, endpoint.carries_version);
        }
    }
}

test "an unstated fact resolves from the row's own endpoint, else from a trailing v1" {
    try std.testing.expect(carriesVersionFor("openrouter", "https://openrouter.ai/api/v1", null));
    try std.testing.expect(!carriesVersionFor("openrouter", "https://openrouter.ai/api/v1", false));
    try std.testing.expect(carriesVersionFor("openrouter", "https://openrouter.ai/api/v1", true));

    try std.testing.expect(!carriesVersionFor("deepseek", "https://api.deepseek.com", null));
    try std.testing.expect(carriesVersionFor("deepseek", "https://api.deepseek.com", true));

    try std.testing.expect(!carriesVersionFor("kimi", "https://api.kimi.com/coding", null));
    try std.testing.expect(!carriesVersionFor("no-such-provider", "https://api.openai.com", null));

    try std.testing.expect(endpointCarriesVersion("openrouter", "https://openrouter.ai/api/v1/"));
    try std.testing.expect(!endpointCarriesVersion("kimi", "https://api.kimi.com/coding"));
    try std.testing.expect(endpointCarriesVersion("deepinfra", "https://api.deepinfra.com/v1/openai"));
    try std.testing.expect(endpointCarriesVersion("deepinfra", "https://api.deepinfra.com/v1/openai/"));
}

test "a wire drops its leading version only when the fact says the base has one" {
    const completions = comptime wirePath("openai-completions").?;
    const messages = comptime wirePath("anthropic-messages").?;
    const responses = comptime wirePath("openai-responses").?;
    const codex = comptime wirePath("openai-codex-responses").?;
    const ollama = comptime wirePath("ollama").?;

    try std.testing.expectEqualStrings("https://proxy.example/v1/chat/completions", joinUrl("https://proxy.example", completions, false));
    try std.testing.expectEqualStrings("https://proxy.example/v1/chat/completions", joinUrl("https://proxy.example/v1", completions, true));
    try std.testing.expectEqualStrings("https://proxy.example/v1/messages", joinUrl("https://proxy.example/v1", messages, true));
    try std.testing.expectEqualStrings("https://proxy.example/v1/messages", joinUrl("https://proxy.example", messages, false));
    try std.testing.expectEqualStrings("https://proxy.example/v1/responses", joinUrl("https://proxy.example/v1", responses, true));
    try std.testing.expectEqualStrings("https://chatgpt.example/responses", joinUrl("https://chatgpt.example", codex, true));
    try std.testing.expectEqualStrings("https://proxy.example/v1/api/chat", joinUrl("https://proxy.example/v1", ollama, true));
    try std.testing.expectEqualStrings("https://proxy.example/api/chat", joinUrl("https://proxy.example", ollama, false));
}

test "an unstated fact keeps the documented trailing v1 and appends the full path to anything else" {
    const completions = comptime wirePath("openai-completions").?;
    const messages = comptime wirePath("anthropic-messages").?;
    try std.testing.expectEqualStrings("https://proxy.example/v1/chat/completions", joinUrl("https://proxy.example/v1", completions, carriesVersionFor("gateway", "https://proxy.example/v1", null)));
    try std.testing.expectEqualStrings("https://proxy.example/v1/messages", joinUrl("https://proxy.example/v1", messages, carriesVersionFor("gateway", "https://proxy.example/v1", null)));
    try std.testing.expectEqualStrings("https://proxy.example/v1/chat/completions", joinUrl("https://proxy.example", completions, carriesVersionFor("gateway", "https://proxy.example", null)));
    try std.testing.expectEqualStrings("https://gw.test/api/coding/paas/v4/v1/chat/completions", joinUrl("https://gw.test/api/coding/paas/v4", completions, carriesVersionFor("gateway", "https://gw.test/api/coding/paas/v4", null)));
    try std.testing.expectEqualStrings("https://gw.test/api/coding/paas/v4/chat/completions", joinUrl("https://gw.test/api/coding/paas/v4", completions, carriesVersionFor("gateway", "https://gw.test/api/coding/paas/v4", true)));
    try std.testing.expectEqualStrings("https://proxy.example/v1/v1/chat/completions", joinUrl("https://proxy.example/v1", completions, false));
}

test "a base the catalog does not hold reads the trailing v1 a client or an override supplies" {
    try std.testing.expect(baseCarriesTrailingVersion("http://host:8000/v1"));
    try std.testing.expect(baseCarriesTrailingVersion("https://proxy.example/v1/"));
    try std.testing.expect(!baseCarriesTrailingVersion("https://proxy.example"));
    try std.testing.expect(!baseCarriesTrailingVersion("https://gw.test/api/coding/paas/v4"));
    try std.testing.expect(!baseCarriesTrailingVersion("https://api.deepinfra.com/v1/openai"));
    try std.testing.expect(!baseCarriesTrailingVersion(""));
    try std.testing.expect(carriesVersionFor("gateway", "http://host:8000/v1", null));
    try std.testing.expect(!carriesVersionFor("gateway", "http://host:8000/v1", false));
    try std.testing.expect(carriesVersionFor("openrouter", "https://openrouter.ai/api/v1", null));
}

test "a base that already ends with its wire's path is used whole, whatever the fact" {
    const completions = comptime wirePath("openai-completions").?;
    try std.testing.expectEqualStrings("https://proxy.example/v1/chat/completions", joinUrl("https://proxy.example/v1/chat/completions", completions, false));
    try std.testing.expectEqualStrings("https://proxy.example/v1/chat/completions", joinUrl("https://proxy.example/v1/chat/completions", completions, true));
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

test "a listing and its request agree about the version, under an override and without one" {
    const rows = [_]struct { id: []const u8, wire: []const u8 }{
        .{ .id = "openrouter", .wire = "openai-completions" },
        .{ .id = "vercel", .wire = "openai-completions" },
        .{ .id = "zenmux", .wire = "openai-completions" },
        .{ .id = "opencode-zen", .wire = "openai-completions" },
        .{ .id = "deepinfra", .wire = "openai-completions" },
        .{ .id = "deepseek", .wire = "openai-completions" },
        .{ .id = "anthropic", .wire = "anthropic-messages" },
    };
    const override_base = "https://proxy.example";
    for (rows) |row| {
        const target = catalogTargetForTest(row.id, row.wire) orelse return error.TestUnexpectedResult;
        const models_path = modelsEndpoint(row.id) orelse continue;
        const carries = target.carries_version;

        const own_listing = try listingUrlOwned(std.testing.allocator, target.base_url, models_path, carries, false);
        defer std.testing.allocator.free(own_listing);
        try std.testing.expectEqualStrings(target.models_url, own_listing);

        const own_request = try joinUrlOwned(std.testing.allocator, target.base_url, target.wire, carries);
        defer std.testing.allocator.free(own_request);
        try std.testing.expectEqualStrings(target.request_url, own_request);

        const proxied_listing = try listingUrlOwned(std.testing.allocator, override_base, models_path, carries, true);
        defer std.testing.allocator.free(proxied_listing);
        const proxied_request = try joinUrlOwned(std.testing.allocator, override_base, target.wire, false);
        defer std.testing.allocator.free(proxied_request);

        if (carries) {
            var expected_prefix: [64]u8 = undefined;
            const prefix = try std.fmt.bufPrint(&expected_prefix, "{s}/v1/", .{override_base});
            try std.testing.expect(std.mem.startsWith(u8, proxied_listing, prefix));
        }
        try std.testing.expect(countVersions(proxied_listing) == countVersions(proxied_request));
    }
}

fn countVersions(url: []const u8) usize {
    var versions: usize = 0;
    var segments = std.mem.tokenizeScalar(u8, url, '/');
    while (segments.next()) |segment| {
        if (looksLikeVersion(segment)) versions += 1;
    }
    return versions;
}

const CatalogTargetForTest = struct {
    base_url: []const u8,
    models_url: []const u8,
    request_url: []const u8,
    wire: Wire,
    carries_version: bool,
};

fn catalogTargetForTest(id: []const u8, wire_id: []const u8) ?CatalogTargetForTest {
    const row = provider(id) orelse return null;
    const wire = wirePath(wire_id) orelse return null;
    for (row.endpoints) |endpoint| {
        if (!std.mem.eql(u8, endpoint.wire, wire_id)) continue;
        return .{
            .base_url = endpoint.base_url,
            .models_url = modelsUrl(id, endpoint.region) orelse return null,
            .request_url = requestUrl(id, wire_id, endpoint.region) orelse return null,
            .wire = wire,
            .carries_version = endpoint.carries_version,
        };
    }
    return null;
}

test "the catalog orders every plan a shared key opens before the row it also opens" {
    var payg_index: usize = 0;
    for (all, 0..) |row, index| {
        if (std.mem.eql(u8, row.id, "xiaomi")) payg_index = index;
    }
    const plans = [_][]const u8{ "xiaomi-token-plan-cn", "xiaomi-token-plan-sgp", "xiaomi-token-plan-ams" };
    for (plans) |plan| {
        var found = false;
        for (all, 0..) |row, index| {
            if (!std.mem.eql(u8, row.id, plan)) continue;
            found = true;
            try std.testing.expect(index < payg_index);
        }
        try std.testing.expect(found);
    }
}

test "a wire with no path joins to the base, trailing slash dropped" {
    try std.testing.expectEqualStrings("https://generativelanguage.example", joinUrl("https://generativelanguage.example/", google_wire, false));
    const owned = try joinUrlOwned(std.testing.allocator, "https://generativelanguage.example///", google_wire, false);
    defer std.testing.allocator.free(owned);
    try std.testing.expectEqualStrings("https://generativelanguage.example", owned);
}

test "the models path is joined by the same rule as a wire path" {
    const wire = Wire{ .id = "models", .suffix = "/v1/models" };
    try std.testing.expectEqualStrings("https://api.openai.com/v1/models", joinModelsUrl("https://api.openai.com/", "/v1/models", false));
    const cases = [_]struct { base: []const u8, want: []const u8 }{
        .{ .base = "https://api.openai.com", .want = "https://api.openai.com/v1/models" },
        .{ .base = "https://api.openai.com/", .want = "https://api.openai.com/v1/models" },
        .{ .base = "https://api.openai.com///", .want = "https://api.openai.com/v1/models" },
        .{ .base = "https://api.openai.com/v1/models", .want = "https://api.openai.com/v1/models" },
        .{ .base = "https://api.openai.com/v1/models/", .want = "https://api.openai.com/v1/models" },
    };
    for (cases) |case| {
        const owned = try joinUrlOwned(std.testing.allocator, case.base, wire, false);
        defer std.testing.allocator.free(owned);
        try std.testing.expectEqualStrings(case.want, owned);
    }
}

fn joinProbeModel(provider_id: []const u8, base_url: []const u8, carries_version: ?bool) ai_types.Model {
    return .{
        .id = "x",
        .name = "x",
        .api = "openai-completions",
        .provider = provider_id,
        .base_url = base_url,
        .reasoning = false,
        .input = &.{},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 0,
        .max_tokens = 0,
        .carries_version = carries_version,
    };
}

test "a model's unstated fact resolves from its own row, and a wire-supplied base keeps its trailing v1" {
    const completions = comptime wirePath("openai-completions").?;

    const openrouter = try joinModelUrlOwned(std.testing.allocator, joinProbeModel("openrouter", "https://openrouter.ai/api/v1", null), completions);
    defer std.testing.allocator.free(openrouter);
    try std.testing.expectEqualStrings("https://openrouter.ai/api/v1/chat/completions", openrouter);

    const supplied = try joinModelUrlOwned(std.testing.allocator, joinProbeModel("gateway", "http://host:8000/v1", null), completions);
    defer std.testing.allocator.free(supplied);
    try std.testing.expectEqualStrings("http://host:8000/v1/chat/completions", supplied);

    const stated = try joinModelUrlOwned(std.testing.allocator, joinProbeModel("openrouter", "https://proxy.example/api/v1", true), completions);
    defer std.testing.allocator.free(stated);
    try std.testing.expectEqualStrings("https://proxy.example/api/v1/chat/completions", stated);

    const denied = try joinModelUrlOwned(std.testing.allocator, joinProbeModel("openrouter", "https://openrouter.ai/api/v1", false), completions);
    defer std.testing.allocator.free(denied);
    try std.testing.expectEqualStrings("https://openrouter.ai/api/v1/v1/chat/completions", denied);
}

const RecordedEnv = struct {
    asked: std.ArrayList([]const u8) = .empty,

    fn deinit(self: *RecordedEnv, allocator: std.mem.Allocator) void {
        for (self.asked.items) |name| allocator.free(name);
        self.asked.deinit(allocator);
    }

    fn read(allocator: std.mem.Allocator, name: []const u8, ctx: ?*anyopaque) anyerror!?[]u8 {
        const self: *RecordedEnv = @ptrCast(@alignCast(ctx orelse return null));
        try self.asked.append(allocator, try allocator.dupe(u8, name));
        return try allocator.dupe(u8, "row-key");
    }
};

test "a request consults exactly the names the row records, for every row that records any" {
    for (all) |row| {
        var recorded = RecordedEnv{};
        defer recorded.deinit(std.testing.allocator);
        const found = apiKeyForProvider(std.testing.allocator, row.id, RecordedEnv.read, &recorded);
        defer if (found) |value| std.testing.allocator.free(value);

        if (row.credential_env.len == 0) {
            try std.testing.expect(found == null);
            try std.testing.expectEqual(@as(usize, 0), recorded.asked.items.len);
            continue;
        }
        if (found == null) {
            std.debug.print("\n{s} records {s} but a request would find no key\n", .{ row.id, row.credential_env[0] });
            return error.TestRowKeyNotFound;
        }
        try std.testing.expectEqualStrings("row-key", found.?);
        try std.testing.expectEqual(@as(usize, 1), recorded.asked.items.len);
        try std.testing.expectEqualStrings(row.credential_env[0], recorded.asked.items[0]);
    }
    try std.testing.expect(apiKeyForProvider(std.testing.allocator, "no-such-provider", RecordedEnv.read, null) == null);
}

test "anthropic's row keeps the auth token ahead of the api key, and the anthropic wire uses it" {
    try std.testing.expectEqualStrings("ANTHROPIC_AUTH_TOKEN", credentialEnv("anthropic")[0]);
    try std.testing.expectEqualStrings("ANTHROPIC_API_KEY", credentialEnv("anthropic")[1]);
    const seen = struct {
        names: std.ArrayList([]const u8) = .empty,
        fn read(allocator: std.mem.Allocator, name: []const u8, ctx: ?*anyopaque) anyerror!?[]u8 {
            const self: *@This() = @ptrCast(@alignCast(ctx orelse return null));
            try self.names.append(allocator, try allocator.dupe(u8, name));
            if (std.mem.eql(u8, name, "ANTHROPIC_AUTH_TOKEN")) return null;
            return try allocator.dupe(u8, "from-the-second-name");
        }
    };
    var probe = seen{};
    defer {
        for (probe.names.items) |name| std.testing.allocator.free(name);
        probe.names.deinit(std.testing.allocator);
    }
    const found = apiKeyForProvider(std.testing.allocator, "anthropic", seen.read, &probe);
    defer std.testing.allocator.free(found.?);
    try std.testing.expectEqualStrings("from-the-second-name", found.?);
    try std.testing.expectEqual(@as(usize, 2), probe.names.items.len);
}

test "blankEnvironment hides every variable the catalog reads, including ones a test has not heard of" {
    const allocator = std.testing.allocator;
    for (all) |row| {
        for (row.credential_env) |name| try compat.setTestEnv(allocator, name, "leaked");
        for (row.base_url_env) |name| try compat.setTestEnv(allocator, name, "https://leaked.example");
        if (row.region_env) |name| try compat.setTestEnv(allocator, name, "leaked");
    }
    try compat.setTestEnv(allocator, global_base_url_env, "https://leaked.example");
    defer compat.clearTestEnv();

    try blankEnvironment(allocator);

    for (all) |row| {
        for (row.credential_env) |name| try expectBlanked(allocator, name);
        if (apiKeyFromEnv(allocator, row.id)) |value| {
            std.debug.print("\n{s} still reads a key after blankEnvironment\n", .{row.id});
            allocator.free(value);
            return error.TestEnvironmentNotBlanked;
        }
        for (row.base_url_env) |name| {
            try expectBlanked(allocator, name);
        }
        if (row.region_env) |name| {
            try expectBlanked(allocator, name);
        }
    }
    try expectBlanked(allocator, global_base_url_env);
}

fn expectBlanked(allocator: std.mem.Allocator, name: []const u8) !void {
    const value = (compat.getEnvVarOwned(allocator, name) catch null) orelse return;
    defer allocator.free(value);
    if (value.len != 0) {
        std.debug.print("\n{s} is {s} after blankEnvironment\n", .{ name, value });
        return error.TestEnvironmentNotBlanked;
    }
}

test "a set but empty variable is skipped for the next name the row records" {
    const EmptyFirst = struct {
        fn read(allocator: std.mem.Allocator, name: []const u8, ctx: ?*anyopaque) anyerror!?[]u8 {
            _ = ctx;
            if (std.mem.eql(u8, name, "FIRST")) return try allocator.dupe(u8, "");
            return try allocator.dupe(u8, "second");
        }
    };
    const names = [_][]const u8{ "FIRST", "SECOND" };
    const found = apiKeyFromNames(std.testing.allocator, &names, EmptyFirst.read, null);
    defer std.testing.allocator.free(found.?);
    try std.testing.expectEqualStrings("second", found.?);
    try std.testing.expect(apiKeyFromNames(std.testing.allocator, &.{}, EmptyFirst.read, null) == null);
}
