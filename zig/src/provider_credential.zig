const std = @import("std");
const provider_catalog = @import("provider_catalog");
const auth_resolver = @import("auth_resolver");

pub const AuthStorage = auth_resolver.AuthStorage;

pub const Source = enum {
    none,
    environment,
    stored,
    oauth,
};

pub const EnvironmentValue = struct {
    name: []const u8,
    value: []const u8,
};

pub const Credential = struct {
    key: []u8,
    source: Source,
    name: []const u8,

    pub fn deinit(self: *Credential, allocator: std.mem.Allocator) void {
        if (self.key.len > 0) {
            const writable: []u8 = @constCast(self.key);
            std.crypto.secureZero(u8, writable);
        }
        allocator.free(self.key);
        self.* = undefined;
    }
};

pub fn needsNoCredential(id: []const u8) bool {
    const row = provider_catalog.provider(id) orelse return false;
    for (row.auth) |kind| {
        if (kind == .none) return true;
    }
    return false;
}

fn accepts(row: provider_catalog.Provider, kind: provider_catalog.AuthKind) bool {
    for (row.auth) |accepted| {
        if (accepted == kind) return true;
    }
    return false;
}

pub fn lookup(
    allocator: std.mem.Allocator,
    environment: []const EnvironmentValue,
    auth_storage: ?*AuthStorage,
    id: []const u8,
) std.mem.Allocator.Error!?Credential {
    const row = provider_catalog.provider(id) orelse return null;
    if (try environmentCredential(allocator, environment, row)) |found| return found;
    return storedCredential(allocator, auth_storage, row);
}

fn environmentCredential(
    allocator: std.mem.Allocator,
    environment: []const EnvironmentValue,
    row: provider_catalog.Provider,
) std.mem.Allocator.Error!?Credential {
    for (row.credential_env) |name| {
        for (environment) |held| {
            if (!std.mem.eql(u8, held.name, name)) continue;
            if (held.value.len == 0) break;
            return .{
                .key = try allocator.dupe(u8, held.value),
                .source = .environment,
                .name = name,
            };
        }
    }
    return null;
}

fn storedCredential(
    allocator: std.mem.Allocator,
    auth_storage: ?*AuthStorage,
    row: provider_catalog.Provider,
) std.mem.Allocator.Error!?Credential {
    const id = row.id;
    const held = auth_storage orelse return null;
    const stored = held.resolvedCredential(id) orelse return null;
    switch (stored) {
        .api_key => |key| {
            if (!accepts(row, .api_key)) return null;
            if (key.len == 0) return null;
            return .{
                .key = try allocator.dupe(u8, key),
                .source = .stored,
                .name = id,
            };
        },
        .oauth => |credentials| {
            if (credentials.access.len == 0) return null;
            if (accepts(row, .oauth)) {
                return .{
                    .key = try allocator.dupe(u8, credentials.access),
                    .source = .oauth,
                    .name = id,
                };
            }
            if (!accepts(row, .api_key)) return null;
            if (credentials.refresh.len != 0) return null;
            return .{
                .key = try allocator.dupe(u8, credentials.access),
                .source = .stored,
                .name = id,
            };
        },
    }
}

fn emptyStorage(allocator: std.mem.Allocator) AuthStorage {
    return .{
        .providers = std.StringHashMap(auth_resolver.ProviderAuth).init(allocator),
        .allocator = allocator,
    };
}

const testing = std.testing;

test "kimi lists under the environment key even when a login is stored" {
    var store = emptyStorage(testing.allocator);
    defer store.deinit();
    try store.providers.put(try testing.allocator.dupe(u8, "kimi"), .{ .oauth = .{
        .access = try testing.allocator.dupe(u8, "sk-stored"),
        .refresh = try testing.allocator.dupe(u8, ""),
        .expires = 0,
        .provider_data = try testing.allocator.dupe(u8, "region:global"),
    } });
    const environment = [_]EnvironmentValue{.{ .name = "KIMI_API_KEY", .value = "sk-env" }};

    var found = (try lookup(testing.allocator, &environment, &store, "kimi")).?;
    defer found.deinit(testing.allocator);
    try testing.expectEqual(Source.environment, found.source);
    try testing.expectEqualStrings("sk-env", found.key);

    var without_login = (try lookup(testing.allocator, &environment, null, "kimi")).?;
    defer without_login.deinit(testing.allocator);
    try testing.expectEqual(Source.environment, without_login.source);
    try testing.expectEqualStrings("sk-env", without_login.key);

    const none = [_]EnvironmentValue{};
    var stored_only = (try lookup(testing.allocator, &none, &store, "kimi")).?;
    defer stored_only.deinit(testing.allocator);
    try testing.expectEqual(Source.stored, stored_only.source);
    try testing.expectEqualStrings("sk-stored", stored_only.key);
}

test "an environment variable wins over a stored key" {
    var store = emptyStorage(testing.allocator);
    defer store.deinit();
    try store.providers.put(try testing.allocator.dupe(u8, "deepseek"), .{ .api_key = try testing.allocator.dupe(u8, "stored-key") });
    const environment = [_]EnvironmentValue{.{ .name = "DEEPSEEK_API_KEY", .value = "env-key" }};
    var found = (try lookup(testing.allocator, &environment, &store, "deepseek")).?;
    defer found.deinit(testing.allocator);
    try testing.expectEqual(Source.environment, found.source);
    try testing.expectEqualStrings("DEEPSEEK_API_KEY", found.name);
    try testing.expectEqualStrings("env-key", found.key);
}

test "the row's credential variables are tried in the order the row records" {
    const environment = [_]EnvironmentValue{
        .{ .name = "ANTHROPIC_API_KEY", .value = "second" },
        .{ .name = "ANTHROPIC_AUTH_TOKEN", .value = "first" },
    };
    var found = (try lookup(testing.allocator, &environment, null, "anthropic")).?;
    defer found.deinit(testing.allocator);
    try testing.expectEqualStrings("ANTHROPIC_AUTH_TOKEN", found.name);
    try testing.expectEqualStrings("first", found.key);
}

test "an empty environment variable counts as unset" {
    const environment = [_]EnvironmentValue{
        .{ .name = "ANTHROPIC_AUTH_TOKEN", .value = "" },
        .{ .name = "ANTHROPIC_API_KEY", .value = "second" },
    };
    var found = (try lookup(testing.allocator, &environment, null, "anthropic")).?;
    defer found.deinit(testing.allocator);
    try testing.expectEqualStrings("ANTHROPIC_API_KEY", found.name);
    try testing.expectEqualStrings("second", found.key);
}

test "a stored key answers a row with no environment variable set" {
    var store = emptyStorage(testing.allocator);
    defer store.deinit();
    try store.providers.put(try testing.allocator.dupe(u8, "deepinfra"), .{ .api_key = try testing.allocator.dupe(u8, "stored-key") });
    var found = (try lookup(testing.allocator, &.{}, &store, "deepinfra")).?;
    defer found.deinit(testing.allocator);
    try testing.expectEqual(Source.stored, found.source);
    try testing.expectEqualStrings("stored-key", found.key);
}

test "an api key stored as an oauth entry with no refresh still answers its row" {
    var store = emptyStorage(testing.allocator);
    defer store.deinit();
    try store.providers.put(try testing.allocator.dupe(u8, "kimi"), .{ .oauth = .{
        .refresh = try testing.allocator.dupe(u8, ""),
        .access = try testing.allocator.dupe(u8, "sk-kimi"),
        .expires = std.math.maxInt(i64),
        .provider_data = try testing.allocator.dupe(u8, "region:global"),
    } });

    var found = (try lookup(testing.allocator, &.{}, &store, "kimi")) orelse return error.TestNoCredential;
    defer found.deinit(testing.allocator);
    try testing.expectEqualStrings("sk-kimi", found.key);
    try testing.expectEqual(Source.stored, found.source);
}

test "an oauth entry with a refresh is still refused for an api-key-only row" {
    var store = emptyStorage(testing.allocator);
    defer store.deinit();
    try store.providers.put(try testing.allocator.dupe(u8, "kimi"), .{ .oauth = .{
        .refresh = try testing.allocator.dupe(u8, "a-refresh-token"),
        .access = try testing.allocator.dupe(u8, "sk-kimi"),
        .expires = std.math.maxInt(i64),
        .provider_data = try testing.allocator.dupe(u8, "region:global"),
    } });
    try testing.expect((try lookup(testing.allocator, &.{}, &store, "kimi")) == null);
}

test "an oauth credential answers an oauth row and no other" {
    var store = emptyStorage(testing.allocator);
    defer store.deinit();
    try store.providers.put(try testing.allocator.dupe(u8, "github-copilot"), .{ .oauth = .{
        .access = try testing.allocator.dupe(u8, "copilot-access"),
        .refresh = try testing.allocator.dupe(u8, "copilot-refresh"),
        .expires = 0,
    } });
    var found = (try lookup(testing.allocator, &.{}, &store, "github-copilot")).?;
    defer found.deinit(testing.allocator);
    try testing.expectEqual(Source.oauth, found.source);
    try testing.expectEqualStrings("copilot-access", found.key);

    var api_key_store = emptyStorage(testing.allocator);
    defer api_key_store.deinit();
    try api_key_store.providers.put(try testing.allocator.dupe(u8, "deepseek"), .{ .oauth = .{
        .access = try testing.allocator.dupe(u8, "not-for-this-row"),
        .refresh = try testing.allocator.dupe(u8, "refresh"),
        .expires = 0,
    } });
    const refused = try lookup(testing.allocator, &.{}, &api_key_store, "deepseek");
    try testing.expect(refused == null);
    if (refused) |held_const| {
        var held = held_const;
        held.deinit(testing.allocator);
    }
}

test "a row with nothing set resolves no credential" {
    var store = emptyStorage(testing.allocator);
    defer store.deinit();
    for ([_][]const u8{ "ollama", "vercel", "deepseek", "no-such-provider" }) |id| {
        const found = try lookup(testing.allocator, &.{}, &store, id);
        try testing.expect(found == null);
        if (found) |held_const| {
            var held = held_const;
            held.deinit(testing.allocator);
        }
    }
    const without_storage = try lookup(testing.allocator, &.{}, null, "ollama");
    try testing.expect(without_storage == null);
}

test "a row that needs no credential says so, and one that does not" {
    try testing.expect(needsNoCredential("ollama"));
    try testing.expect(!needsNoCredential("deepseek"));
    try testing.expect(!needsNoCredential("kimi"));
    try testing.expect(!needsNoCredential("github-copilot"));
    try testing.expect(!needsNoCredential("no-such-provider"));
}
