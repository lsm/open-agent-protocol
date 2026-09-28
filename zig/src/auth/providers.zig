const std = @import("std");
const provider_catalog = @import("provider_catalog");

pub const AuthKind = provider_catalog.AuthKind;

pub const ProviderDefinition = struct {
    id: []const u8,
    name: []const u8,
    auth_kinds: []const AuthKind,
};

pub const fixture_provider_id = "test-fixture";
pub const fixture_provider_name = "Test Fixture (CI)";

fn definitionOf(comptime row: anytype) ProviderDefinition {
    return .{
        .id = row.id,
        .name = row.display_name orelse row.id,
        .auth_kinds = row.auth,
    };
}

pub const CATALOG_PROVIDER_DEFINITIONS = blk: {
    var collected: [provider_catalog.all.len]ProviderDefinition = undefined;
    for (provider_catalog.all, 0..) |row, index| collected[index] = definitionOf(row);
    const frozen = collected;
    break :blk &frozen;
};

const fixture_auth_kinds = [_]AuthKind{.api_key};

pub const fixture_provider = ProviderDefinition{
    .id = fixture_provider_id,
    .name = fixture_provider_name,
    .auth_kinds = &fixture_auth_kinds,
};

pub fn findProvider(provider_id: []const u8) ?ProviderDefinition {
    for (CATALOG_PROVIDER_DEFINITIONS) |provider| {
        if (std.mem.eql(u8, provider.id, provider_id)) {
            return provider;
        }
    }
    if (std.mem.eql(u8, provider_id, fixture_provider_id)) return fixture_provider;
    return null;
}

test "the definitions are the catalog's rows, in catalog order" {
    try std.testing.expectEqual(provider_catalog.all.len, CATALOG_PROVIDER_DEFINITIONS.len);
    for (provider_catalog.all, 0..) |row, index| {
        const definition = CATALOG_PROVIDER_DEFINITIONS[index];
        try std.testing.expectEqualStrings(row.id, definition.id);
        try std.testing.expectEqualStrings(row.display_name orelse row.id, definition.name);
        try std.testing.expectEqual(row.auth.len, definition.auth_kinds.len);
        for (row.auth, 0..) |kind, kind_index| {
            try std.testing.expectEqual(kind, definition.auth_kinds[kind_index]);
        }
    }
}

test "every catalogued row is listed with a display name" {
    for (provider_catalog.all) |row| {
        if (row.display_name == null or row.display_name.?.len == 0) {
            return error.TestExpectedDisplayName;
        }
        const definition = findProvider(row.id) orelse return error.TestExpectedProvider;
        try std.testing.expectEqualStrings(row.display_name.?, definition.name);
    }
}

test "every catalogued row is listed, including the ones the runtime cannot load yet" {
    const listed = [_][]const u8{
        "openai",       "anthropic",    "opencode",        "openrouter",
        "deepseek",     "zai-coding-plan", "kimi",         "alibaba-coding-plan",
        "minimax-coding-plan", "tencent-coding-plan", "volcengine-coding-plan", "openai-codex",
        "xiaomi-token-plan-cn", "xiaomi-token-plan-sgp", "xiaomi-token-plan-ams", "deepinfra",
        "xiaomi",       "vercel",       "zenmux",          "ollama",
        "azure",        "github-copilot", "google",
    };
    try std.testing.expectEqual(listed.len, CATALOG_PROVIDER_DEFINITIONS.len);
    for (listed, 0..) |id, index| {
        try std.testing.expectEqualStrings(id, CATALOG_PROVIDER_DEFINITIONS[index].id);
    }
}

test "a row's auth kinds are the ones the catalog records" {
    const anthropic = findProvider("anthropic") orelse return error.TestExpectedProvider;
    try std.testing.expectEqual(@as(usize, 2), anthropic.auth_kinds.len);
    try std.testing.expect(anthropic.auth_kinds[0] == .api_key);
    try std.testing.expect(anthropic.auth_kinds[1] == .oauth);

    const codex = findProvider("openai-codex") orelse return error.TestExpectedProvider;
    try std.testing.expectEqual(@as(usize, 1), codex.auth_kinds.len);
    try std.testing.expect(codex.auth_kinds[0] == .oauth);

    const ollama = findProvider("ollama") orelse return error.TestExpectedProvider;
    try std.testing.expectEqual(@as(usize, 2), ollama.auth_kinds.len);
    var needs_none = false;
    for (ollama.auth_kinds) |kind| {
        if (kind == .none) needs_none = true;
    }
    try std.testing.expect(needs_none);

    const vercel = findProvider("vercel") orelse return error.TestExpectedProvider;
    try std.testing.expectEqual(@as(usize, 1), vercel.auth_kinds.len);
    try std.testing.expect(vercel.auth_kinds[0] == .api_key);
}

test "the CI fixture row is not a catalog row and is not in the served list" {
    try std.testing.expect(provider_catalog.provider(fixture_provider_id) == null);
    try std.testing.expectEqual(@as(usize, provider_catalog.all.len), CATALOG_PROVIDER_DEFINITIONS.len);
    for (CATALOG_PROVIDER_DEFINITIONS) |definition| {
        if (std.mem.eql(u8, definition.id, fixture_provider_id)) return error.TestFixtureInServedList;
    }

    const found = findProvider(fixture_provider_id) orelse return error.TestExpectedProvider;
    try std.testing.expectEqualStrings(fixture_provider_name, found.name);
    try std.testing.expectEqual(@as(usize, 1), found.auth_kinds.len);
    try std.testing.expect(found.auth_kinds[0] == .api_key);
}

test "findProvider answers a catalogued id and refuses an unknown one" {
    const provider = findProvider("anthropic") orelse return error.TestExpectedEqual;
    try std.testing.expectEqualStrings("Anthropic", provider.name);

    const codex = findProvider("openai-codex") orelse return error.TestExpectedEqual;
    try std.testing.expectEqualStrings("OpenAI Codex", codex.name);

    const deepinfra = findProvider("deepinfra") orelse return error.TestExpectedEqual;
    try std.testing.expectEqualStrings("Deep Infra", deepinfra.name);

    try std.testing.expect(findProvider("no-such-provider") == null);
}
