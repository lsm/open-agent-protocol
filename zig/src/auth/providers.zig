const std = @import("std");
const builtin = @import("builtin");
const compat = @import("compat");
const provider_catalog = @import("provider_catalog");

pub const AuthKind = provider_catalog.AuthKind;

pub const ProviderDefinition = struct {
    id: []const u8,
    name: []const u8,
    auth_kinds: []const AuthKind,
};

pub const fixture_provider_id = "test-fixture";
pub const fixture_provider_name = "Test Fixture (CI)";
pub const fixture_opt_in_env = "OAPX_TEST_FIXTURE_PROVIDER";

pub var test_fixture_opt_in: ?bool = null;

pub fn fixtureOptInIsSet(allocator: std.mem.Allocator, environ: std.process.Environ) bool {
    const raw = compat.getEnvVarOwnedFrom(environ, allocator, fixture_opt_in_env) catch return false;
    defer allocator.free(raw);
    return std.mem.eql(u8, raw, "1") or std.ascii.eqlIgnoreCase(raw, "true");
}

pub fn fixtureRequested() bool {
    if (builtin.is_test) return test_fixture_opt_in orelse false;
    return fixtureOptInIsSet(std.heap.page_allocator, compat.runtimeEnviron());
}

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

pub const ALL_DEFINITIONS = blk: {
    var collected: [provider_catalog.all.len + 1]ProviderDefinition = undefined;
    for (provider_catalog.all, 0..) |row, index| collected[index] = definitionOf(row);
    collected[provider_catalog.all.len] = .{
        .id = fixture_provider_id,
        .name = fixture_provider_name,
        .auth_kinds = &fixture_auth_kinds,
    };
    const frozen = collected;
    break :blk &frozen;
};

pub fn servedDefinitions() []const ProviderDefinition {
    if (fixtureRequested()) return ALL_DEFINITIONS;
    return CATALOG_PROVIDER_DEFINITIONS;
}

pub fn findProvider(provider_id: []const u8) ?ProviderDefinition {
    for (servedDefinitions()) |provider| {
        if (std.mem.eql(u8, provider.id, provider_id)) {
            return provider;
        }
    }
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
        "openai",       "anthropic",    "opencode",        "opencode-go",     "openrouter",
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

test "the CI fixture row is not a catalog row and is served last, for tests only" {
    try std.testing.expect(provider_catalog.provider(fixture_provider_id) == null);
    try std.testing.expectEqual(@as(usize, provider_catalog.all.len + 1), ALL_DEFINITIONS.len);
    try std.testing.expectEqualStrings(fixture_provider_id, ALL_DEFINITIONS[ALL_DEFINITIONS.len - 1].id);
    for (ALL_DEFINITIONS[0 .. provider_catalog.all.len], 0..) |definition, index| {
        try std.testing.expectEqualStrings(provider_catalog.all[index].id, definition.id);
    }

    test_fixture_opt_in = true;
    defer test_fixture_opt_in = null;
    const found = findProvider(fixture_provider_id) orelse return error.TestExpectedProvider;
    try std.testing.expectEqualStrings(fixture_provider_name, found.name);
    try std.testing.expectEqual(@as(usize, 1), found.auth_kinds.len);
    try std.testing.expect(found.auth_kinds[0] == .api_key);
}

fn environOf(entries: [:null]const ?[*:0]const u8) std.process.Environ {
    return .{ .block = .{ .slice = entries } };
}

test "the fixture is served only when the opt-in variable asks for it by name" {
    const cases = [_]struct { entries: [:null]const ?[*:0]const u8, want: bool, label: []const u8 }{
        .{ .entries = &.{}, .want = false, .label = "nothing set at all" },
        .{ .entries = &.{ "PATH=/usr/bin", "HOME=/home/dev" }, .want = false, .label = "a full environment without it" },
        .{ .entries = &.{"OAPX_TEST_FIXTURE_PROVIDER=1"}, .want = true, .label = "1" },
        .{ .entries = &.{"OAPX_TEST_FIXTURE_PROVIDER=true"}, .want = true, .label = "true" },
        .{ .entries = &.{"OAPX_TEST_FIXTURE_PROVIDER=TRUE"}, .want = true, .label = "TRUE" },
        .{ .entries = &.{"OAPX_TEST_FIXTURE_PROVIDER=0"}, .want = false, .label = "0 is not an opt-in" },
        .{ .entries = &.{"OAPX_TEST_FIXTURE_PROVIDER="}, .want = false, .label = "set but empty is not an opt-in" },
        .{ .entries = &.{"OAPX_TEST_FIXTURE_PROVIDER=yes"}, .want = false, .label = "yes is not an opt-in" },
        .{ .entries = &.{"OAPX_TEST_FIXTURE_PROVIDER=11"}, .want = false, .label = "not a prefix match" },
        .{ .entries = &.{"OAPX_TEST_FIXTURE_PROVIDER_=1"}, .want = false, .label = "a different variable's name" },
        .{ .entries = &.{"OAPX_TEST_FIXTURE_PROVIDER2=1"}, .want = false, .label = "a different variable's suffix" },
    };
    for (cases) |case| {
        const got = fixtureOptInIsSet(std.testing.allocator, environOf(case.entries));
        if (got != case.want) std.debug.print("\n{s} should be {}\n", .{ case.label, case.want });
        try std.testing.expectEqual(case.want, got);
    }
}

test "the opt-in is read under the name the tests are told to set" {
    try std.testing.expectEqualStrings("OAPX_TEST_FIXTURE_PROVIDER", fixture_opt_in_env);
    try std.testing.expect(fixtureOptInIsSet(std.testing.allocator, environOf(&.{"OAPX_TEST_FIXTURE_PROVIDER=1"})));
}

test "a user who did not ask for the fixture is not offered it" {
    try std.testing.expect(!fixtureRequested());
    defer test_fixture_opt_in = null;

    try std.testing.expectEqual(@as(usize, provider_catalog.all.len), servedDefinitions().len);
    try std.testing.expect(findProvider(fixture_provider_id) == null);
    for (CATALOG_PROVIDER_DEFINITIONS) |definition| {
        try std.testing.expect(findProvider(definition.id) != null);
    }
    for (servedDefinitions()) |definition| {
        try std.testing.expect(!std.mem.eql(u8, definition.id, fixture_provider_id));
    }
}

test "a test that asked for the fixture is served it, last" {
    test_fixture_opt_in = true;
    defer test_fixture_opt_in = null;

    try std.testing.expect(fixtureRequested());
    try std.testing.expectEqual(ALL_DEFINITIONS.len, servedDefinitions().len);
    const last = servedDefinitions()[servedDefinitions().len - 1];
    try std.testing.expectEqualStrings(fixture_provider_id, last.id);
    try std.testing.expectEqualStrings(fixture_provider_name, last.name);
    try std.testing.expect(findProvider(fixture_provider_id) != null);
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
