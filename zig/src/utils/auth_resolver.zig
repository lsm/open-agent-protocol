const std = @import("std");
const builtin = @import("builtin");
const compat = @import("compat");
const storage_mod = @import("oauth/storage");
const custom_providers = @import("custom_providers");
const provider_catalog = @import("provider_catalog");
const provider_base_url = @import("provider_base_url");
const ai_types = @import("ai_types");

pub const AuthStorage = storage_mod.AuthStorage;
pub const ProviderAuth = storage_mod.ProviderAuth;

pub const AuthResolveError = error{
    AuthRequired,
} || std.mem.Allocator.Error;

pub const ResolvedKey = struct {
    api_key: []u8,

    pub fn deinit(self: *ResolvedKey, allocator: std.mem.Allocator) void {
        allocator.free(self.api_key);
        self.* = undefined;
    }
};

pub const CredentialKind = enum {
    any,
    api_key_only,
};

pub fn resolveApiKey(
    allocator: std.mem.Allocator,
    auth_storage: ?*AuthStorage,
    provider_id: []const u8,
    provided_api_key: ?[]const u8,
) AuthResolveError!ResolvedKey {
    return resolveApiKeyOfKind(allocator, auth_storage, provider_id, provided_api_key, .any);
}

pub const OverrideEndpoint = struct {
    base_url: []u8,
    forwards_credential: bool,
    carries_version: ?bool = null,
    headers: []ai_types.HeaderPair = &.{},

    pub fn deinit(self: *OverrideEndpoint, allocator: std.mem.Allocator) void {
        allocator.free(self.base_url);
        for (self.headers) |header| {
            allocator.free(header.name);
            allocator.free(header.value);
        }
        allocator.free(self.headers);
        self.* = undefined;
    }
};

pub fn storedCredentialWithheld(allocator: std.mem.Allocator, lookup: OverrideLookup, provider_id: []const u8, base_url: []const u8) bool {
    if (std.mem.trim(u8, base_url, " \t\r\n").len == 0) return false;
    if (!provider_catalog.servedByCatalogLoader(provider_id)) return false;
    return switch (lookup) {
        .none => false,
        .endpoint => |found| !found.forwards_credential and
            provider_base_url.sameOrigin(base_url, found.base_url) and
            !provider_base_url.knownOrigin(allocator, provider_id, base_url),
        .unreadable => provider_catalog.oauthOrigin(provider_id) == null and
            !provider_base_url.knownOrigin(allocator, provider_id, base_url),
    };
}

pub fn overrideApplies(allocator: std.mem.Allocator, found: OverrideEndpoint, provider_id: []const u8, base_url: []const u8) bool {
    if (found.base_url.len > 0) return std.mem.eql(u8, std.mem.trimEnd(u8, base_url, "/"), std.mem.trimEnd(u8, found.base_url, "/"));
    return provider_base_url.knownOrigin(allocator, provider_id, base_url);
}

pub const OverrideLookup = union(enum) {
    none,
    unreadable,
    endpoint: OverrideEndpoint,

    pub fn deinit(self: *OverrideLookup, allocator: std.mem.Allocator) void {
        switch (self.*) {
            .endpoint => |*found| found.deinit(allocator),
            else => {},
        }
        self.* = undefined;
    }
};

pub var test_override_config: ?[]const u8 = null;

pub fn overrideLookup(allocator: std.mem.Allocator, provider_id: []const u8) std.mem.Allocator.Error!OverrideLookup {
    var config = loadOverrideConfig(allocator) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return .unreadable,
    };
    defer config.deinit(allocator);
    return overrideLookupIn(allocator, config.overrides, provider_id);
}

pub fn overrideLookupIn(allocator: std.mem.Allocator, overrides: []const custom_providers.Override, provider_id: []const u8) std.mem.Allocator.Error!OverrideLookup {
    if (!provider_catalog.servedByCatalogLoader(provider_id)) return .none;
    const override = custom_providers.overrideFor(overrides, provider_id) orelse return .none;
    const base = override.base_url orelse "";
    const base_url = try allocator.dupe(u8, base);
    errdefer allocator.free(base_url);
    const headers = try allocator.alloc(ai_types.HeaderPair, override.headers.len);
    var filled: usize = 0;
    errdefer {
        for (headers[0..filled]) |header| {
            allocator.free(header.name);
            allocator.free(header.value);
        }
        allocator.free(headers);
    }
    for (override.headers, headers) |header, *slot| {
        const name = try allocator.dupe(u8, header.name);
        errdefer allocator.free(name);
        slot.* = .{ .name = name, .value = try allocator.dupe(u8, header.value) };
        filled += 1;
    }
    return .{ .endpoint = .{ .base_url = base_url, .forwards_credential = override.forwards_credential, .carries_version = override.carries_version, .headers = headers } };
}

pub fn storedKimiRegion(storage: ?*const AuthStorage) ?[]const u8 {
    const stored = storage orelse return null;
    const auth = stored.resolvedCredential("kimi") orelse return null;
    if (auth != .oauth) return null;
    const provider_data = auth.oauth.provider_data orelse return null;
    if (!std.mem.startsWith(u8, provider_data, "region:")) return null;
    return provider_catalog.regionFromValue("kimi", provider_data["region:".len..]);
}

pub fn overrideHost(allocator: std.mem.Allocator, overrides: []const custom_providers.Override, provider_id: []const u8, storage: ?*const AuthStorage) !?[]u8 {
    var lookup = try overrideLookupIn(allocator, overrides, provider_id);
    defer lookup.deinit(allocator);
    const found = switch (lookup) {
        .endpoint => |endpoint| endpoint,
        else => return null,
    };
    const row = provider_catalog.provider(provider_id) orelse return null;
    const wire = provider_catalog.firstImplementedWire(row) orelse return null;
    const region = if (std.mem.eql(u8, provider_id, "kimi")) storedKimiRegion(storage) else null;
    const resolved = try provider_base_url.defaultBaseUrlForRefWithFile(allocator, provider_id, wire.id, region, found.base_url);
    defer allocator.free(resolved);
    const effective = if (resolved.len > 0) resolved else provider_catalog.baseUrl(provider_id, wire.id, region) orelse return null;
    if (!overrideApplies(allocator, found, provider_id, effective)) return null;
    const uri = std.Uri.parse(effective) catch return null;
    const host = uri.host orelse return null;
    const text = switch (host) {
        .raw => |raw| raw,
        .percent_encoded => |encoded| encoded,
    };
    if (text.len == 0) return null;
    return try allocator.dupe(u8, text);
}

pub fn loadOverrides(allocator: std.mem.Allocator) std.mem.Allocator.Error!custom_providers.Config {
    return loadOverrideConfig(allocator) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return .{},
    };
}

fn loadOverrideConfig(allocator: std.mem.Allocator) !custom_providers.Config {
    if (builtin.is_test) {
        const json = test_override_config orelse return .{};
        return custom_providers.parseConfig(allocator, json);
    }
    return custom_providers.loadConfigStrict(allocator, custom_providers.max_config_bytes);
}

fn rowEnvironmentKey(allocator: std.mem.Allocator, provider_id: []const u8) ?[]u8 {
    const key = provider_catalog.apiKeyFromEnv(allocator, provider_id) orelse return null;
    if (key.len == 0) {
        allocator.free(key);
        return null;
    }
    return @constCast(key);
}

pub fn resolveApiKeyOfKind(
    allocator: std.mem.Allocator,
    auth_storage: ?*AuthStorage,
    provider_id: []const u8,
    provided_api_key: ?[]const u8,
    kind: CredentialKind,
) AuthResolveError!ResolvedKey {
    if (provided_api_key) |k| {
        if (k.len > 0) {
            const dup = try allocator.dupe(u8, k);
            return .{ .api_key = dup };
        }
    }

    if (rowEnvironmentKey(allocator, provider_id)) |key| return .{ .api_key = key };

    if (auth_storage) |storage| {
        if (storage.resolvedCredential(provider_id)) |auth| {
            switch (auth) {
                .api_key => |key| {
                    const dup = try allocator.dupe(u8, key);
                    return .{ .api_key = dup };
                },
                .oauth => |creds| {
                    if (kind == .any) {
                        const dup = try allocator.dupe(u8, creds.access);
                        return .{ .api_key = dup };
                    }
                },
            }
        }
    }

    if (try customProviderEnvKey(allocator, provider_id)) |key| return .{ .api_key = key };
    return error.AuthRequired;
}

fn customProviderEnvKey(allocator: std.mem.Allocator, provider_id: []const u8) std.mem.Allocator.Error!?[]u8 {
    if (builtin.is_test) return null;
    const providers = custom_providers.load(allocator, custom_providers.max_config_bytes) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return null,
    };
    defer custom_providers.deinitProviders(allocator, providers);
    return envKeyForProvider(allocator, providers, provider_id);
}

pub fn envKeyForProvider(
    allocator: std.mem.Allocator,
    providers: []const custom_providers.CustomProvider,
    provider_id: []const u8,
) std.mem.Allocator.Error!?[]u8 {
    for (providers) |provider| {
        if (!std.mem.eql(u8, provider.id, provider_id)) continue;
        const env_name = provider.env_key orelse return null;
        const value = compat.getEnvVarOwned(allocator, env_name) catch return null;
        if (value.len == 0) {
            allocator.free(value);
            return null;
        }
        return value;
    }
    return null;
}

const testing = std.testing;

fn makeStorage(allocator: std.mem.Allocator) AuthStorage {
    return .{
        .providers = std.StringHashMap(ProviderAuth).init(allocator),
        .allocator = allocator,
    };
}

test "resolveApiKey - explicit api key wins, no storage lookup" {
    try provider_catalog.blankEnvironment(std.testing.allocator);
    defer compat.clearTestEnv();
    var storage = makeStorage(testing.allocator);
    defer storage.deinit();

    const provider_id = try testing.allocator.dupe(u8, "anthropic");
    const stored = try testing.allocator.dupe(u8, "stored-key");
    try storage.providers.put(provider_id, .{ .api_key = stored });

    var resolved = try resolveApiKey(testing.allocator, &storage, "anthropic", "explicit-key");
    defer resolved.deinit(testing.allocator);

    try testing.expectEqualStrings("explicit-key", resolved.api_key);
}

test "resolveApiKey - empty explicit key falls through to storage" {
    try provider_catalog.blankEnvironment(std.testing.allocator);
    defer compat.clearTestEnv();
    var storage = makeStorage(testing.allocator);
    defer storage.deinit();

    const provider_id = try testing.allocator.dupe(u8, "anthropic");
    const stored = try testing.allocator.dupe(u8, "stored-key");
    try storage.providers.put(provider_id, .{ .api_key = stored });

    var resolved = try resolveApiKey(testing.allocator, &storage, "anthropic", "");
    defer resolved.deinit(testing.allocator);

    try testing.expectEqualStrings("stored-key", resolved.api_key);
}

test "resolveApiKey - loads api_key from storage by provider_id" {
    try provider_catalog.blankEnvironment(std.testing.allocator);
    defer compat.clearTestEnv();
    var storage = makeStorage(testing.allocator);
    defer storage.deinit();

    const provider_id = try testing.allocator.dupe(u8, "openai");
    const stored = try testing.allocator.dupe(u8, "sk-test");
    try storage.providers.put(provider_id, .{ .api_key = stored });

    var resolved = try resolveApiKey(testing.allocator, &storage, "openai", null);
    defer resolved.deinit(testing.allocator);

    try testing.expectEqualStrings("sk-test", resolved.api_key);
}

test "resolveApiKey - loads oauth access token from storage by provider_id" {
    try provider_catalog.blankEnvironment(std.testing.allocator);
    defer compat.clearTestEnv();
    var storage = makeStorage(testing.allocator);
    defer storage.deinit();

    const provider_id = try testing.allocator.dupe(u8, "anthropic");
    const refresh = try testing.allocator.dupe(u8, "refresh-token");
    const access = try testing.allocator.dupe(u8, "oauth-access");
    try storage.providers.put(provider_id, .{ .oauth = .{
        .refresh = refresh,
        .access = access,
        .expires = compat.time.nowMillis() + 3_600_000,
    } });

    var resolved = try resolveApiKey(testing.allocator, &storage, "anthropic", null);
    defer resolved.deinit(testing.allocator);

    try testing.expectEqualStrings("oauth-access", resolved.api_key);
}

test "an environment key wins over a stored login, and a row that names no variable is untouched" {
    const allocator = testing.allocator;
    try provider_catalog.blankEnvironment(allocator);
    defer compat.clearTestEnv();

    var storage = AuthStorage{
        .providers = std.StringHashMap(ProviderAuth).init(allocator),
        .allocator = allocator,
    };
    defer storage.deinit();
    try storage.providers.put(try allocator.dupe(u8, "kimi"), .{ .oauth = .{
        .access = try allocator.dupe(u8, "sk-stored"),
        .refresh = try allocator.dupe(u8, ""),
        .expires = 0,
    } });

    var with_env = try resolveApiKeyOfKind(allocator, &storage, "kimi", null, .any);
    defer with_env.deinit(allocator);
    try testing.expectEqualStrings("sk-stored", with_env.api_key);

    try compat.setTestEnv(allocator, "KIMI_API_KEY", "sk-env");
    var env_wins = try resolveApiKeyOfKind(allocator, &storage, "kimi", null, .any);
    defer env_wins.deinit(allocator);
    try testing.expectEqualStrings("sk-env", env_wins.api_key);

    try storage.providers.put(try allocator.dupe(u8, "openai-codex"), .{ .oauth = .{
        .access = try allocator.dupe(u8, "codex-access"),
        .refresh = try allocator.dupe(u8, "codex-refresh"),
        .expires = 0,
    } });
    var codex = try resolveApiKeyOfKind(allocator, &storage, "openai-codex", null, .any);
    defer codex.deinit(allocator);
    try testing.expectEqualStrings("codex-access", codex.api_key);

    try testing.expectError(
        error.AuthRequired,
        resolveApiKeyOfKind(allocator, &storage, "no-such-row", null, .any),
    );
}

test "resolveApiKeyOfKind - api_key_only refuses a stored oauth token" {
    try provider_catalog.blankEnvironment(std.testing.allocator);
    defer compat.clearTestEnv();
    var storage = makeStorage(testing.allocator);
    defer storage.deinit();

    const provider_id = try testing.allocator.dupe(u8, "openai-codex");
    const refresh = try testing.allocator.dupe(u8, "refresh-token");
    const access = try testing.allocator.dupe(u8, "oauth-access");
    try storage.providers.put(provider_id, .{ .oauth = .{
        .refresh = refresh,
        .access = access,
        .expires = compat.time.nowMillis() + 3_600_000,
    } });

    try testing.expectError(
        error.AuthRequired,
        resolveApiKeyOfKind(testing.allocator, &storage, "openai-codex", null, .api_key_only),
    );

    var any = try resolveApiKeyOfKind(testing.allocator, &storage, "openai-codex", null, .any);
    defer any.deinit(testing.allocator);
    try testing.expectEqualStrings("oauth-access", any.api_key);
}

test "resolveApiKeyOfKind - api_key_only still returns a stored api key" {
    try provider_catalog.blankEnvironment(std.testing.allocator);
    defer compat.clearTestEnv();
    var storage = makeStorage(testing.allocator);
    defer storage.deinit();

    const provider_id = try testing.allocator.dupe(u8, "gateway");
    const stored = try testing.allocator.dupe(u8, "gateway-key");
    try storage.providers.put(provider_id, .{ .api_key = stored });

    var resolved = try resolveApiKeyOfKind(testing.allocator, &storage, "gateway", null, .api_key_only);
    defer resolved.deinit(testing.allocator);
    try testing.expectEqualStrings("gateway-key", resolved.api_key);
}

test "resolveApiKey - missing storage and no key returns AuthRequired" {
    try provider_catalog.blankEnvironment(std.testing.allocator);
    defer compat.clearTestEnv();
    try testing.expectError(
        error.AuthRequired,
        resolveApiKey(testing.allocator, null, "anthropic", null),
    );
}

test "resolveApiKey - empty storage returns AuthRequired" {
    try provider_catalog.blankEnvironment(std.testing.allocator);
    defer compat.clearTestEnv();
    var storage = makeStorage(testing.allocator);
    defer storage.deinit();

    try testing.expectError(
        error.AuthRequired,
        resolveApiKey(testing.allocator, &storage, "anthropic", null),
    );
}

test "resolveApiKey - provider not in storage returns AuthRequired" {
    try provider_catalog.blankEnvironment(std.testing.allocator);
    defer compat.clearTestEnv();
    var storage = makeStorage(testing.allocator);
    defer storage.deinit();

    const provider_id = try testing.allocator.dupe(u8, "openai");
    const stored = try testing.allocator.dupe(u8, "sk-test");
    try storage.providers.put(provider_id, .{ .api_key = stored });

    try testing.expectError(
        error.AuthRequired,
        resolveApiKey(testing.allocator, &storage, "anthropic", null),
    );
}

test "resolveApiKey - empty key with no storage returns AuthRequired" {
    try provider_catalog.blankEnvironment(std.testing.allocator);
    defer compat.clearTestEnv();
    try testing.expectError(
        error.AuthRequired,
        resolveApiKey(testing.allocator, null, "anthropic", ""),
    );
}

test "envKeyForProvider reads the declared variable and ignores other providers" {
    const providers = [_]custom_providers.CustomProvider{
        .{ .id = "gateway", .name = "Gateway", .api = "openai-completions", .base_url = "https://gw.test", .env_key = "OAPX_TEST_GATEWAY_KEY" },
        .{ .id = "keyless", .name = "Keyless", .api = "openai-completions", .base_url = "http://localhost:8000" },
    };

    try testing.expect(try envKeyForProvider(testing.allocator, &providers, "absent") == null);
    try testing.expect(try envKeyForProvider(testing.allocator, &providers, "keyless") == null);

    const unset = try envKeyForProvider(testing.allocator, &providers, "gateway");
    if (unset) |value| testing.allocator.free(value);
}

test "a granted credential outranks a configured one on the resolution path" {
    try provider_catalog.blankEnvironment(std.testing.allocator);
    defer compat.clearTestEnv();
    const allocator = std.testing.allocator;

    var storage = AuthStorage{
        .providers = std.StringHashMap(storage_mod.ProviderAuth).init(allocator),
        .allocator = allocator,
    };
    defer storage.deinit();

    try storage.providers.put(
        try allocator.dupe(u8, "tenant"),
        .{ .api_key = try allocator.dupe(u8, "sk-configured") },
    );

    var configured = try resolveApiKeyOfKind(allocator, &storage, "tenant", null, .any);
    defer configured.deinit(allocator);
    try std.testing.expectEqualStrings("sk-configured", configured.api_key);

    try storage.putEphemeral("tenant", .{ .api_key = try allocator.dupe(u8, "sk-granted") });

    var granted = try resolveApiKeyOfKind(allocator, &storage, "tenant", null, .any);
    defer granted.deinit(allocator);
    try std.testing.expectEqualStrings("sk-granted", granted.api_key);

    storage.releaseEphemeral();

    var restored = try resolveApiKeyOfKind(allocator, &storage, "tenant", null, .any);
    defer restored.deinit(allocator);
    try std.testing.expectEqualStrings("sk-configured", restored.api_key);
}

test "an override's host follows the region the stored kimi login resolves to" {
    const allocator = std.testing.allocator;
    try provider_catalog.blankEnvironment(allocator);
    defer compat.clearTestEnv();
    var storage = AuthStorage{
        .providers = std.StringHashMap(ProviderAuth).init(allocator),
        .allocator = allocator,
    };
    defer storage.deinit();
    try storage.providers.put(try allocator.dupe(u8, "kimi"), .{ .oauth = .{
        .refresh = try allocator.dupe(u8, "refresh"),
        .access = try allocator.dupe(u8, "access"),
        .expires = 0,
        .provider_data = try allocator.dupe(u8, "region:global"),
    } });
    const headers = [_]ai_types.HeaderPair{.{ .name = "X-Tenant", .value = "acme" }};
    const overrides = [_]custom_providers.Override{.{ .id = "kimi", .headers = &headers }};

    const global = (try overrideHost(allocator, &overrides, "kimi", &storage)).?;
    defer allocator.free(global);
    const global_uri = try std.Uri.parse(provider_catalog.baseUrl("kimi", "openai-completions", "global").?);
    try std.testing.expectEqualStrings(global_uri.host.?.percent_encoded, global);

    const unset = (try overrideHost(allocator, &overrides, "kimi", null)).?;
    defer allocator.free(unset);
    try std.testing.expect(!std.mem.eql(u8, global, unset));
}
