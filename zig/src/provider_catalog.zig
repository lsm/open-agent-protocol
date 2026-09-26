const std = @import("std");
const data = @import("data");

pub const AuthKind = data.AuthKind;
pub const Offering = data.Offering;
pub const Status = data.Status;
pub const Endpoint = data.Endpoint;
pub const OAuthOrigin = data.OAuthOrigin;
pub const Provider = data.Provider;

pub const all = data.providers;

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
    try std.testing.expect(modelsEndpoint("openai-codex") == null);
    try std.testing.expect(modelsEndpoint("ollama") == null);
    try std.testing.expect(modelsEndpoint("no-such-provider") == null);
}

test "an offering is a plan or an api key, and one host serves one row" {
    const plans = coding_plan_ids;
    try std.testing.expect(plans.len > 0);
    for (plans) |id| {
        try std.testing.expect(offering(id).? == .coding_plan);
    }
    var meters: usize = 0;
    for (all) |row| {
        if (row.offering != null and row.offering.? == .api_key) meters += 1;
    }
    try std.testing.expect(meters > 0);
    try std.testing.expectEqualStrings("zai-coding-plan", plans[0]);
    try std.testing.expect(offering("kimi").? == .coding_plan);
    try std.testing.expect(offering("xiaomi").? == .api_key);
    try std.testing.expect(offering("openrouter").? == .api_key);
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
