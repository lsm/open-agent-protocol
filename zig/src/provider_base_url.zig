const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const provider_catalog = @import("provider_catalog");

pub fn defaultBaseUrlForRef(allocator: std.mem.Allocator, provider_id: []const u8, api: []const u8) ![]const u8 {
    return defaultBaseUrlForRefWithRegion(allocator, provider_id, api, null);
}

pub fn defaultBaseUrlForRefWithRegion(
    allocator: std.mem.Allocator,
    provider_id: []const u8,
    api: []const u8,
    stored_kimi_region: ?[]const u8,
) ![]const u8 {
    return defaultBaseUrlForRefWithFile(allocator, provider_id, api, stored_kimi_region, "");
}

pub fn defaultBaseUrlForRefWithFile(
    allocator: std.mem.Allocator,
    provider_id: []const u8,
    api: []const u8,
    stored_kimi_region: ?[]const u8,
    file: []const u8,
) ![]const u8 {
    const global = try envOwnedOrNull(allocator, provider_catalog.global_base_url_env);
    defer if (global) |g| allocator.free(g);

    const row_env = try rowBaseUrlEnvOwned(allocator, provider_id);
    defer if (row_env) |value| allocator.free(value);

    const kimi_region = try resolveKimiRegion(allocator, stored_kimi_region);

    return baseUrlWithOverrides(allocator, provider_id, api, .{
        .global = global orelse "",
        .kimi_region = kimi_region,
        .row = row_env orelse "",
        .file = file,
    });
}

fn rowBaseUrlEnvOwned(allocator: std.mem.Allocator, provider_id: []const u8) !?[]const u8 {
    const names = provider_catalog.baseUrlEnv(provider_id);
    if (names.len == 0) return null;
    return try envOwnedOrNull(allocator, names[0]);
}

pub const BaseUrlOverrides = struct {
    global: []const u8 = "",
    row: []const u8 = "",
    file: []const u8 = "",
    file_carries_version: ?bool = null,
    kimi_region: []const u8 = "china",

    pub fn fileSupplies(self: BaseUrlOverrides) bool {
        return self.global.len == 0 and self.row.len == 0 and self.file.len > 0;
    }
};

const anthropic_messages_base_url = provider_catalog.baseUrlOrCompileError("anthropic", "anthropic-messages", null);
const openai_responses_base_url = provider_catalog.baseUrlOrCompileError("openai", "openai-responses", null);
const openai_completions_base_url = provider_catalog.baseUrlOrCompileError("openai", "openai-completions", null);
const deepseek_completions_base_url = provider_catalog.baseUrlOrCompileError("deepseek", "openai-completions", null);
const deepseek_anthropic_base_url = provider_catalog.baseUrlOrCompileError("deepseek", "anthropic-messages", null);
const codex_responses_base_url = provider_catalog.baseUrlOrCompileError("openai-codex", "openai-codex-responses", null);
const kimi_china_base_url = provider_catalog.baseUrlOrCompileError("kimi", "openai-completions", "china");
const kimi_global_base_url = provider_catalog.baseUrlOrCompileError("kimi", "openai-completions", "global");
const anthropic_base_url_env = provider_catalog.baseUrlEnv("anthropic")[0];
const openai_base_url_env = provider_catalog.baseUrlEnv("openai")[0];
const deepseek_base_url_env = provider_catalog.baseUrlEnv("deepseek")[0];

pub fn overriddenWire(provider_id: []const u8) ?[]const u8 {
    if (std.mem.eql(u8, provider_id, "deepseek")) return "openai-completions";
    return null;
}

pub fn normalizeVersionedBaseUrl(url: []const u8) []const u8 {
    const trimmed = std.mem.trimEnd(u8, url, "/");
    if (std.mem.endsWith(u8, trimmed, "/v1")) return trimmed[0 .. trimmed.len - 3];
    return trimmed;
}

pub fn usesVersionedRoute(provider_id: []const u8, api: []const u8) bool {
    if (std.mem.eql(u8, provider_id, "github-copilot")) return false;
    return std.mem.eql(u8, api, "anthropic-messages") or
        std.mem.eql(u8, api, "openai-completions") or
        std.mem.eql(u8, api, "openai-responses");
}

const kimi_region_env_name = provider_catalog.regionEnv("kimi") orelse
    @compileError("providers/catalog.json records no region_env for kimi");

fn resolveKimiRegion(allocator: std.mem.Allocator, stored_region: ?[]const u8) ![]const u8 {
    const fallback = provider_catalog.defaultRegion("kimi") orelse "china";
    if (envOwnedOrNull(allocator, kimi_region_env_name) catch null) |value| {
        defer allocator.free(value);
        if (provider_catalog.regionFromValue("kimi", value)) |resolved| return resolved;
    }
    if (!try provider_catalog.credentialEnvIsSet(allocator, "kimi")) {
        if (stored_region) |stored| {
            if (provider_catalog.regionFromValue("kimi", stored)) |resolved| return resolved;
        }
    }
    return fallback;
}

pub fn baseUrlWithOverrides(allocator: std.mem.Allocator, provider_id: []const u8, api: []const u8, ov: BaseUrlOverrides) ![]const u8 {
    if (ov.global.len > 0) {
        const global = if (usesVersionedRoute(provider_id, api)) normalizeVersionedBaseUrl(ov.global) else std.mem.trimEnd(u8, ov.global, "/");
        return try allocator.dupe(u8, global);
    }

    if (ov.row.len > 0) {
        const row = if (usesVersionedRoute(provider_id, api)) normalizeVersionedBaseUrl(ov.row) else std.mem.trimEnd(u8, ov.row, "/");
        return try allocator.dupe(u8, row);
    }

    if (ov.file.len > 0) return try allocator.dupe(u8, ov.file);

    const by_provider: ?[]const u8 = if (std.mem.eql(u8, provider_id, "anthropic") and std.mem.eql(u8, api, "anthropic-messages"))
        anthropic_messages_base_url
    else if (std.mem.eql(u8, provider_id, "openai") and (std.mem.eql(u8, api, "openai-completions") or std.mem.eql(u8, api, "openai-responses")))
        if (std.mem.eql(u8, api, "openai-responses")) openai_responses_base_url else openai_completions_base_url
    else if (std.mem.eql(u8, provider_id, "deepseek") and std.mem.eql(u8, api, "openai-completions"))
        deepseek_completions_base_url
    else if (std.mem.eql(u8, provider_id, "deepseek") and std.mem.eql(u8, api, "anthropic-messages"))
        deepseek_anthropic_base_url
    else if (std.mem.eql(u8, provider_id, "openai-codex") and std.mem.eql(u8, api, "openai-codex-responses"))
        codex_responses_base_url
    else if (std.mem.eql(u8, provider_id, "kimi") and std.mem.eql(u8, api, "openai-completions"))
        if (std.mem.eql(u8, ov.kimi_region, "global")) kimi_global_base_url else kimi_china_base_url
    else
        null;
    if (by_provider) |url| return try allocator.dupe(u8, url);

    return try allocator.dupe(u8, "");
}

pub fn envOwnedOrNull(allocator: std.mem.Allocator, key: []const u8) !?[]const u8 {
    const value = compat.getEnvVarOwned(allocator, key) catch return null;
    if (value.len == 0) {
        allocator.free(value);
        return null;
    }
    return value;
}

pub const OAuthOriginSources = struct {
    global: []const u8 = "",
    provider: []const u8 = "",
    credential: []const u8 = "",
    credential_is_api_key: bool = false,
    configured: []const u8 = "",
    override_forwards_credential: bool = false,
};

pub const ConfiguredEndpoint = struct {
    base_url: []const u8 = "",
    forwards_stored_credential: bool = false,
};

const Origin = struct {
    scheme: []const u8,
    host: []const u8,
    port: u16,
};

fn defaultPortForScheme(scheme: []const u8) u16 {
    if (std.ascii.eqlIgnoreCase(scheme, "https")) return 443;
    if (std.ascii.eqlIgnoreCase(scheme, "http")) return 80;
    return 0;
}

fn parseOrigin(url: []const u8) ?Origin {
    const trimmed = std.mem.trim(u8, url, " \t\r\n");
    const scheme_end = std.mem.indexOf(u8, trimmed, "://") orelse return null;
    const scheme = trimmed[0..scheme_end];
    if (scheme.len == 0) return null;

    var authority = trimmed[scheme_end + 3 ..];
    if (std.mem.indexOfAny(u8, authority, "/?#")) |idx| authority = authority[0..idx];
    if (std.mem.lastIndexOfScalar(u8, authority, '@')) |idx| authority = authority[idx + 1 ..];
    if (authority.len == 0) return null;

    var host = authority;
    var port = defaultPortForScheme(scheme);
    if (authority[0] == '[') {
        const close = std.mem.indexOfScalar(u8, authority, ']') orelse return null;
        host = authority[0 .. close + 1];
        const tail = authority[close + 1 ..];
        if (tail.len > 0) {
            if (tail[0] != ':') return null;
            port = std.fmt.parseInt(u16, tail[1..], 10) catch return null;
        }
    } else if (std.mem.lastIndexOfScalar(u8, authority, ':')) |idx| {
        host = authority[0..idx];
        port = std.fmt.parseInt(u16, authority[idx + 1 ..], 10) catch return null;
    }
    if (host.len == 0) return null;
    return .{ .scheme = scheme, .host = host, .port = port };
}

fn originsMatch(requested: Origin, candidate_url: []const u8) bool {
    const candidate = parseOrigin(candidate_url) orelse return false;
    return std.ascii.eqlIgnoreCase(requested.scheme, candidate.scheme) and
        std.ascii.eqlIgnoreCase(requested.host, candidate.host) and
        requested.port == candidate.port;
}

pub fn sameOrigin(left: []const u8, right: []const u8) bool {
    const parsed = parseOrigin(left) orelse return false;
    return originsMatch(parsed, right);
}

pub fn knownOrigin(allocator: std.mem.Allocator, provider_id: []const u8, base_url: []const u8) bool {
    const requested = parseOrigin(base_url) orelse return false;
    if (provider_catalog.provider(provider_id)) |row| {
        for (row.endpoints) |endpoint| {
            if (originsMatch(requested, endpoint.base_url)) return true;
        }
    }
    const global = envOwnedOrNull(allocator, provider_catalog.global_base_url_env) catch null;
    defer if (global) |value| allocator.free(value);
    if (global) |value| if (originsMatch(requested, value)) return true;
    const row_env = rowBaseUrlEnvOwned(allocator, provider_id) catch null;
    defer if (row_env) |value| allocator.free(value);
    if (row_env) |value| if (originsMatch(requested, value)) return true;
    return false;
}

fn hostUnderDomain(host: []const u8, domain: []const u8) bool {
    if (std.ascii.eqlIgnoreCase(host, domain)) return true;
    if (host.len <= domain.len + 1) return false;
    const suffix_start = host.len - domain.len;
    if (host[suffix_start - 1] != '.') return false;
    return std.ascii.eqlIgnoreCase(host[suffix_start..], domain);
}

fn oauthOriginPolicyFor(provider_id: []const u8) ?provider_catalog.OAuthOrigin {
    return provider_catalog.oauthOrigin(provider_id);
}

pub fn oauthOriginAllowedWithSources(
    provider_id: []const u8,
    base_url: []const u8,
    sources: OAuthOriginSources,
) bool {
    const trimmed = std.mem.trim(u8, base_url, " \t\r\n");
    if (trimmed.len == 0) return true;

    const policy_for_provider = oauthOriginPolicyFor(provider_id);
    if (sources.credential_is_api_key and policy_for_provider == null) return true;

    const requested = parseOrigin(trimmed) orelse return false;
    if (sources.global.len > 0 and originsMatch(requested, sources.global)) return true;
    if (sources.provider.len > 0 and originsMatch(requested, sources.provider)) return true;

    if (sources.configured.len > 0 and originsMatch(requested, sources.configured)) {
        return sources.override_forwards_credential;
    }

    const policy = policy_for_provider orelse return false;
    if (policy.credential_declares_origin and
        sources.credential.len > 0 and
        originsMatch(requested, sources.credential))
    {
        return true;
    }
    for (policy.exact) |allowed| {
        if (originsMatch(requested, allowed)) return true;
    }
    if (policy.domain) |domain| {
        if (hostUnderDomain(requested.host, domain)) return true;
    }
    return false;
}

fn providerBaseUrlEnvName(provider_id: []const u8) ?[]const u8 {
    const names = provider_catalog.baseUrlEnv(provider_id);
    if (names.len == 0) return null;
    return names[0];
}

pub fn credentialDeclaredBaseUrl(allocator: std.mem.Allocator, provider_data: []const u8) ?[]u8 {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, provider_data, .{}) catch return null;
    defer parsed.deinit();
    if (parsed.value != .object) return null;
    const value = parsed.value.object.get("baseUrl") orelse return null;
    if (value != .string or value.string.len == 0) return null;
    return allocator.dupe(u8, value.string) catch null;
}

pub fn storedCredentialIsApiKeyShaped(refresh: []const u8) bool {
    return refresh.len == 0;
}

pub fn oauthOriginAllowed(
    allocator: std.mem.Allocator,
    provider_id: []const u8,
    base_url: []const u8,
    refresh: []const u8,
    provider_data: ?[]const u8,
    configured_endpoint: ConfiguredEndpoint,
) bool {
    const trimmed = std.mem.trim(u8, base_url, " \t\r\n");
    if (trimmed.len == 0) return true;
    if (storedCredentialIsApiKeyShaped(refresh) and oauthOriginPolicyFor(provider_id) == null) return true;

    const global = envOwnedOrNull(allocator, "OAPX_BASE_URL") catch null;
    defer if (global) |value| allocator.free(value);

    const provider_override: ?[]const u8 = blk: {
        const name = providerBaseUrlEnvName(provider_id) orelse break :blk null;
        break :blk envOwnedOrNull(allocator, name) catch null;
    };
    defer if (provider_override) |value| allocator.free(value);

    const credential: ?[]u8 = blk: {
        const data = provider_data orelse break :blk null;
        break :blk credentialDeclaredBaseUrl(allocator, data);
    };
    defer if (credential) |value| allocator.free(value);

    return oauthOriginAllowedWithSources(provider_id, trimmed, .{
        .global = global orelse "",
        .provider = provider_override orelse "",
        .credential = credential orelse "",
        .credential_is_api_key = storedCredentialIsApiKeyShaped(refresh),
        .configured = configured_endpoint.base_url,
        .override_forwards_credential = configured_endpoint.forwards_stored_credential,
    });
}

pub const ProxyCompatFlags = struct {
    global_base_set: bool = false,
    global_proxy: bool = false,
    openai_proxy: bool = false,
    deepseek_proxy: bool = false,
    anthropic_proxy: bool = false,
};

pub fn proxyCompatFlagsFromEnv(allocator: std.mem.Allocator) !ProxyCompatFlags {
    const global_base = try envOwnedOrNull(allocator, "OAPX_BASE_URL");
    defer if (global_base) |value| allocator.free(value);

    return .{
        .global_base_set = global_base != null,
        .global_proxy = try envFlag(allocator, "OAPX_BASE_URL_IS_PROXY"),
        .openai_proxy = try envFlag(allocator, "OPENAI_BASE_URL_IS_PROXY"),
        .deepseek_proxy = try envFlag(allocator, "DEEPSEEK_BASE_URL_IS_PROXY"),
        .anthropic_proxy = try envFlag(allocator, "ANTHROPIC_BASE_URL_IS_PROXY"),
    };
}

pub fn transparentProxyCompat(allocator: std.mem.Allocator, provider_id: []const u8) !?ai_types.OpenAICompatOptions {
    return transparentProxyCompatForFlags(provider_id, try proxyCompatFlagsFromEnv(allocator));
}

pub fn transparentProxyCompatForFlags(provider_id: []const u8, flags: ProxyCompatFlags) ?ai_types.OpenAICompatOptions {
    if (std.mem.eql(u8, provider_id, "openai") and (if (flags.global_base_set) flags.global_proxy else flags.openai_proxy)) {
        return .{
            .supports_store = true,
            .supports_developer_role = true,
            .supports_reasoning_effort = true,
            .max_tokens_field = .max_completion_tokens,
        };
    }
    if (std.mem.eql(u8, provider_id, "deepseek") and (if (flags.global_base_set) flags.global_proxy else flags.deepseek_proxy)) {
        return .{
            .max_tokens_field = .max_tokens,
            .supports_strict_mode = false,
        };
    }
    if (std.mem.eql(u8, provider_id, "anthropic") and (if (flags.global_base_set) flags.global_proxy else flags.anthropic_proxy)) {
        return .{ .supports_anthropic_cache_ttl = true };
    }
    return null;
}

fn envFlag(allocator: std.mem.Allocator, key: []const u8) !bool {
    const value = try envOwnedOrNull(allocator, key) orelse return false;
    defer allocator.free(value);
    return std.mem.eql(u8, value, "1") or std.ascii.eqlIgnoreCase(value, "true");
}

pub fn isReasoningModelRef(provider_id: []const u8, model_id: []const u8) bool {
    if (std.mem.eql(u8, provider_id, "openai") or std.mem.eql(u8, provider_id, "openai-codex")) {
        return std.mem.startsWith(u8, model_id, "o1") or
            std.mem.startsWith(u8, model_id, "o3") or
            std.mem.startsWith(u8, model_id, "o4") or
            (std.mem.startsWith(u8, model_id, "gpt-5") and std.mem.indexOf(u8, model_id, "-chat") == null);
    }
    if (std.mem.eql(u8, provider_id, "deepseek")) {
        return std.mem.startsWith(u8, model_id, "deepseek-reasoner");
    }
    if (std.mem.eql(u8, provider_id, "anthropic")) {
        if (!std.mem.startsWith(u8, model_id, "claude-")) return false;
        if (std.mem.startsWith(u8, model_id, "claude-1")) return false;
        if (std.mem.startsWith(u8, model_id, "claude-2")) return false;
        if (std.mem.startsWith(u8, model_id, "claude-v")) return false;
        if (std.mem.startsWith(u8, model_id, "claude-instant")) return false;
        if (std.mem.startsWith(u8, model_id, "claude-3-7")) return true;
        return !std.mem.startsWith(u8, model_id, "claude-3");
    }
    return false;
}

pub fn defaultMaxTokensForRef(provider_id: []const u8, api: []const u8) u32 {
    if (provider_catalog.declaresWire(provider_id, api)) {
        if (provider_catalog.rowMaxTokens(provider_id)) |tokens| return tokens;
    }
    return 4_096;
}

test "KIMI_REGION still decides the region for a caller that resolves nothing itself" {
    try provider_catalog.blankEnvironment(std.testing.allocator);
    defer compat.clearTestEnv();
    try std.testing.expectEqualStrings("china", try resolveKimiRegion(std.testing.allocator, null));
    try std.testing.expectEqualStrings("global", try resolveKimiRegion(std.testing.allocator, "global"));
    try std.testing.expectEqualStrings("china", try resolveKimiRegion(std.testing.allocator, "china"));
    try std.testing.expectEqualStrings("global", try resolveKimiRegion(std.testing.allocator, "moonshot"));
    try std.testing.expectEqualStrings("global", try resolveKimiRegion(std.testing.allocator, " global "));
    try std.testing.expectEqualStrings("china", try resolveKimiRegion(std.testing.allocator, "mars"));

    try compat.setTestEnv(std.testing.allocator, kimi_region_env_name, "global");
    try std.testing.expectEqualStrings("global", try resolveKimiRegion(std.testing.allocator, null));
    try std.testing.expectEqualStrings("global", try resolveKimiRegion(std.testing.allocator, "china"));

    try compat.setTestEnv(std.testing.allocator, kimi_region_env_name, " global ");
    try std.testing.expectEqualStrings("global", try resolveKimiRegion(std.testing.allocator, null));

    try compat.setTestEnv(std.testing.allocator, kimi_region_env_name, "mars");
    try std.testing.expectEqualStrings("global", try resolveKimiRegion(std.testing.allocator, "global"));
    try std.testing.expectEqualStrings("global", try resolveKimiRegion(std.testing.allocator, "moonshot"));

    try provider_catalog.blankEnvironment(std.testing.allocator);
    try std.testing.expectEqualStrings("global", try resolveKimiRegion(std.testing.allocator, "moonshot"));
    try std.testing.expectEqualStrings("china", try resolveKimiRegion(std.testing.allocator, null));
}

test "baseUrlWithOverrides resolves canonical provider defaults" {
    const allocator = std.testing.allocator;

    const anthropic = try baseUrlWithOverrides(allocator, "anthropic", "anthropic-messages", .{});
    defer allocator.free(anthropic);
    try std.testing.expectEqualStrings("https://api.anthropic.com", anthropic);

    const openai = try baseUrlWithOverrides(allocator, "openai", "openai-completions", .{});
    defer allocator.free(openai);
    try std.testing.expectEqualStrings("https://api.openai.com", openai);

    const deepseek = try baseUrlWithOverrides(allocator, "deepseek", "openai-completions", .{});
    defer allocator.free(deepseek);
    try std.testing.expectEqualStrings("https://api.deepseek.com", deepseek);
}

test "every row that declares a base_url_env is routed by it, not only three of them" {
    const allocator = std.testing.allocator;

    const cases = [_]struct { id: []const u8, api: []const u8, env: []const u8 }{
        .{ .id = "anthropic", .api = "anthropic-messages", .env = "ANTHROPIC_BASE_URL" },
        .{ .id = "openai", .api = "openai-completions", .env = "OPENAI_BASE_URL" },
        .{ .id = "openai", .api = "openai-responses", .env = "OPENAI_BASE_URL" },
        .{ .id = "deepseek", .api = "openai-completions", .env = "DEEPSEEK_BASE_URL" },
        .{ .id = "ollama", .api = "ollama", .env = "OLLAMA_BASE_URL" },
        .{ .id = "azure", .api = "openai-responses", .env = "AZURE_OPENAI_BASE_URL" },
        .{ .id = "google", .api = "google-generative-ai", .env = "GOOGLE_BASE_URL" },
    };

    for (cases) |case| {
        const catalogued = provider_catalog.baseUrlEnv(case.id);
        try std.testing.expectEqualStrings(case.env, catalogued[0]);

        const routed = try baseUrlWithOverrides(allocator, case.id, case.api, .{
            .row = "https://proxy.example/mirror",
        });
        defer allocator.free(routed);
        try std.testing.expectEqualStrings("https://proxy.example/mirror", routed);
    }
}

test "a row's base_url_env outranks the catalog and loses to the global one" {
    const allocator = std.testing.allocator;

    const catalogued = try baseUrlWithOverrides(allocator, "deepseek", "openai-completions", .{
        .row = "https://proxy.example/deepseek",
    });
    defer allocator.free(catalogued);
    try std.testing.expectEqualStrings("https://proxy.example/deepseek", catalogued);

    const global = try baseUrlWithOverrides(allocator, "deepseek", "openai-completions", .{
        .global = "https://everywhere.example",
        .row = "https://proxy.example/deepseek",
    });
    defer allocator.free(global);
    try std.testing.expectEqualStrings("https://everywhere.example", global);
}

test "a row's base_url_env keeps the version rule its own wire has" {
    const allocator = std.testing.allocator;

    const versioned = try baseUrlWithOverrides(allocator, "ollama", "ollama", .{
        .row = "http://127.0.0.1:11434/",
    });
    defer allocator.free(versioned);
    try std.testing.expectEqualStrings("http://127.0.0.1:11434", versioned);

    const messages = try baseUrlWithOverrides(allocator, "anthropic", "anthropic-messages", .{
        .row = "https://proxy.example/anthropic/v1",
    });
    defer allocator.free(messages);
    try std.testing.expectEqualStrings("https://proxy.example/anthropic", messages);
}

test "baseUrlWithOverrides prefers env overrides and global override" {
    const allocator = std.testing.allocator;

    const proxied = try baseUrlWithOverrides(allocator, "anthropic", "anthropic-messages", .{
        .row = "https://proxy.example.com",
    });
    defer allocator.free(proxied);
    try std.testing.expectEqualStrings("https://proxy.example.com", proxied);

    const versioned_anthropic = try baseUrlWithOverrides(allocator, "anthropic", "anthropic-messages", .{
        .row = "https://proxy.example.com/anthropic/v1/",
    });
    defer allocator.free(versioned_anthropic);
    try std.testing.expectEqualStrings("https://proxy.example.com/anthropic", versioned_anthropic);

    const versioned_openai = try baseUrlWithOverrides(allocator, "openai", "openai-completions", .{
        .row = "https://proxy.example.com/openai/v1/",
    });
    defer allocator.free(versioned_openai);
    try std.testing.expectEqualStrings("https://proxy.example.com/openai", versioned_openai);

    const global = try baseUrlWithOverrides(allocator, "kimi", "openai-completions", .{
        .global = "https://everywhere.example.com",
    });
    defer allocator.free(global);
    try std.testing.expectEqualStrings("https://everywhere.example.com", global);

    const versioned_global = try baseUrlWithOverrides(allocator, "openai", "openai-completions", .{
        .global = "https://proxy.example.com/v1",
    });
    defer allocator.free(versioned_global);
    try std.testing.expectEqualStrings("https://proxy.example.com", versioned_global);

    const copilot_global = try baseUrlWithOverrides(allocator, "github-copilot", "openai-completions", .{
        .global = "https://proxy.example.com/v1",
    });
    defer allocator.free(copilot_global);
    try std.testing.expectEqualStrings("https://proxy.example.com/v1", copilot_global);

    const versioned_deepseek = try baseUrlWithOverrides(allocator, "deepseek", "openai-completions", .{
        .row = "https://proxy.example.com/v1/",
    });
    defer allocator.free(versioned_deepseek);
    try std.testing.expectEqualStrings("https://proxy.example.com", versioned_deepseek);
}

test "baseUrlWithOverrides requires explicit endpoint for unknown providers" {
    const allocator = std.testing.allocator;

    const unknown = try baseUrlWithOverrides(allocator, "openrouter", "openai-completions", .{});
    defer allocator.free(unknown);
    try std.testing.expectEqualStrings("", unknown);

    const explicit = try baseUrlWithOverrides(allocator, "openrouter", "openai-completions", .{
        .global = "https://openrouter.example.com/api",
    });
    defer allocator.free(explicit);
    try std.testing.expectEqualStrings("https://openrouter.example.com/api", explicit);
}

test "baseUrlWithOverrides rejects provider/API mismatches" {
    const allocator = std.testing.allocator;

    const anthropic_openai = try baseUrlWithOverrides(allocator, "anthropic", "openai-completions", .{});
    defer allocator.free(anthropic_openai);
    try std.testing.expectEqualStrings("", anthropic_openai);

    const openai_anthropic = try baseUrlWithOverrides(allocator, "openai", "anthropic-messages", .{});
    defer allocator.free(openai_anthropic);
    try std.testing.expectEqualStrings("", openai_anthropic);

    const openai_codex_responses = try baseUrlWithOverrides(allocator, "openai", "openai-codex-responses", .{});
    defer allocator.free(openai_codex_responses);
    try std.testing.expectEqualStrings("", openai_codex_responses);

    const deepseek_responses = try baseUrlWithOverrides(allocator, "deepseek", "openai-responses", .{});
    defer allocator.free(deepseek_responses);
    try std.testing.expectEqualStrings("", deepseek_responses);
}

test "transparent proxy compat preserves vendor token-limit fields" {
    const deepseek = transparentProxyCompatForFlags("deepseek", .{ .deepseek_proxy = true });
    try std.testing.expect(deepseek != null);
    try std.testing.expectEqual(@as(?bool, null), deepseek.?.requires_thinking_as_text);
    try std.testing.expect(deepseek.?.max_tokens_field.? == .max_tokens);
    try std.testing.expectEqual(@as(?bool, false), deepseek.?.supports_strict_mode);

    const deepseek_global = transparentProxyCompatForFlags("deepseek", .{
        .global_base_set = true,
        .global_proxy = true,
        .deepseek_proxy = false,
    });
    try std.testing.expect(deepseek_global != null);
    try std.testing.expect(deepseek_global.?.max_tokens_field.? == .max_tokens);

    try std.testing.expect(transparentProxyCompatForFlags("deepseek", .{}) == null);

    const openai = transparentProxyCompatForFlags("openai", .{ .openai_proxy = true });
    try std.testing.expect(openai != null);
    try std.testing.expect(openai.?.max_tokens_field.? == .max_completion_tokens);
}

test "baseUrlWithOverrides resolves production catalog pairs" {
    const allocator = std.testing.allocator;

    const codex = try baseUrlWithOverrides(allocator, "openai-codex", "openai-codex-responses", .{});
    defer allocator.free(codex);
    try std.testing.expectEqualStrings(provider_catalog.baseUrl("openai-codex", "openai-codex-responses", null).?, codex);

    const kimi_china = try baseUrlWithOverrides(allocator, "kimi", "openai-completions", .{});
    defer allocator.free(kimi_china);
    try std.testing.expectEqualStrings(provider_catalog.baseUrl("kimi", "openai-completions", "china").?, kimi_china);

    const kimi_global = try baseUrlWithOverrides(allocator, "kimi", "openai-completions", .{
        .kimi_region = "global",
    });
    defer allocator.free(kimi_global);
    try std.testing.expectEqualStrings(provider_catalog.baseUrl("kimi", "openai-completions", "global").?, kimi_global);

    const kimi_overridden = try baseUrlWithOverrides(allocator, "kimi", "openai-completions", .{
        .global = "https://everywhere.example.com",
    });
    defer allocator.free(kimi_overridden);
    try std.testing.expectEqualStrings("https://everywhere.example.com", kimi_overridden);

    const kimi_wrong_api = try baseUrlWithOverrides(allocator, "kimi", "openai-responses", .{});
    defer allocator.free(kimi_wrong_api);
    try std.testing.expectEqualStrings("", kimi_wrong_api);

    const codex_wrong_provider = try baseUrlWithOverrides(allocator, "openai", "openai-codex-responses", .{});
    defer allocator.free(codex_wrong_provider);
    try std.testing.expectEqualStrings("", codex_wrong_provider);
}

test "the deepseek anthropic pair resolves its catalogued base rather than no base at all" {
    const allocator = std.testing.allocator;
    const anthropic = try baseUrlWithOverrides(allocator, "deepseek", "anthropic-messages", .{});
    defer allocator.free(anthropic);
    try std.testing.expectEqualStrings(provider_catalog.baseUrl("deepseek", "anthropic-messages", null).?, anthropic);
    const completions = try baseUrlWithOverrides(allocator, "deepseek", "openai-completions", .{});
    defer allocator.free(completions);
    try std.testing.expectEqualStrings(provider_catalog.baseUrl("deepseek", "openai-completions", null).?, completions);
}

test "the deepseek anthropic pair is reached through the public entry, defaulted, empty, overridden and normalized" {
    const allocator = std.testing.allocator;
    const row_env = provider_catalog.baseUrlEnv("deepseek")[0];
    const global_env = provider_catalog.global_base_url_env;
    const catalogued = provider_catalog.baseUrl("deepseek", "anthropic-messages", null).?;
    defer compat.clearTestEnv();

    try compat.setTestEnv(allocator, row_env, "");
    try compat.setTestEnv(allocator, global_env, "");
    const defaulted = try defaultBaseUrlForRef(allocator, "deepseek", "anthropic-messages");
    defer allocator.free(defaulted);
    try std.testing.expectEqualStrings(catalogued, defaulted);

    try compat.setTestEnv(allocator, row_env, "https://proxy.invalid/deepseek");
    const rowed = try defaultBaseUrlForRef(allocator, "deepseek", "anthropic-messages");
    defer allocator.free(rowed);
    try std.testing.expectEqualStrings("https://proxy.invalid/deepseek", rowed);

    try compat.setTestEnv(allocator, global_env, "https://everywhere.invalid");
    const global = try defaultBaseUrlForRef(allocator, "deepseek", "anthropic-messages");
    defer allocator.free(global);
    try std.testing.expectEqualStrings("https://everywhere.invalid", global);

    try compat.setTestEnv(allocator, global_env, "");
    try compat.setTestEnv(allocator, row_env, "https://proxy.invalid/v1");
    const normalized = try defaultBaseUrlForRef(allocator, "deepseek", "anthropic-messages");
    defer allocator.free(normalized);
    try std.testing.expectEqualStrings("https://proxy.invalid", normalized);
}

test "oauthOriginAllowedWithSources binds vendor tokens to vendor origins" {
    try std.testing.expect(oauthOriginAllowedWithSources("anthropic", "https://api.anthropic.com", .{}));
    try std.testing.expect(oauthOriginAllowedWithSources("anthropic", "https://api.anthropic.com/v1/", .{}));
    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "https://attacker.test", .{}));
    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "https://api.anthropic.com.attacker.test", .{}));
    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "http://api.anthropic.com", .{}));
    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "https://api.anthropic.com:8443", .{}));

    try std.testing.expect(oauthOriginAllowedWithSources("openai-codex", "https://chatgpt.com/backend-api/codex", .{}));
    try std.testing.expect(!oauthOriginAllowedWithSources("openai-codex", "https://chatgpt.attacker.test/backend-api/codex", .{}));
}

test "an endpoint a configuration file names is not a destination for a vendor token" {
    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "https://gw.internal/anthropic", .{
        .configured = "https://gw.internal/anthropic",
    }));
    try std.testing.expect(!oauthOriginAllowedWithSources("openai-codex", "https://gw.internal/codex", .{
        .configured = "https://gw.internal/codex",
    }));
}

test "a configuration file's endpoint takes the token only when the same file says so" {
    try std.testing.expect(oauthOriginAllowedWithSources("anthropic", "https://gw.internal/anthropic", .{
        .configured = "https://gw.internal/anthropic",
        .override_forwards_credential = true,
    }));

    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "https://elsewhere.test", .{
        .configured = "https://gw.internal/anthropic",
        .override_forwards_credential = true,
    }));
}

test "the environment is still a destination without a second signal" {
    try std.testing.expect(oauthOriginAllowedWithSources("anthropic", "https://proxy.example", .{
        .global = "https://proxy.example",
    }));
    try std.testing.expect(oauthOriginAllowedWithSources("anthropic", "https://proxy.example", .{
        .provider = "https://proxy.example",
    }));
    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "https://proxy.example", .{
        .configured = "https://proxy.example",
    }));
}

test "oauthOriginAllowedWithSources allows an absent base_url" {
    try std.testing.expect(oauthOriginAllowedWithSources("anthropic", "", .{}));
    try std.testing.expect(oauthOriginAllowedWithSources("anthropic", "   ", .{}));
    try std.testing.expect(oauthOriginAllowedWithSources("test-fixture", "", .{}));
}

test "oauthOriginAllowedWithSources fails closed for an unpoliced provider id" {
    try std.testing.expect(!oauthOriginAllowedWithSources("test-fixture", "https://anywhere.test", .{}));
    try std.testing.expect(!oauthOriginAllowedWithSources("gateway", "https://gateway.test", .{}));
    try std.testing.expect(oauthOriginAllowedWithSources("gateway", "https://gateway.test", .{
        .global = "https://gateway.test",
    }));
}

test "oauthOriginAllowedWithSources exempts an api-key-shaped entry under an unpoliced id" {
    try std.testing.expect(oauthOriginAllowedWithSources("kimi", "https://api.kimi.com/coding", .{
        .credential_is_api_key = true,
    }));
    try std.testing.expect(oauthOriginAllowedWithSources("kimi", "https://api.moonshot.ai", .{
        .credential_is_api_key = true,
    }));
    try std.testing.expect(oauthOriginAllowedWithSources("gateway", "https://gateway.test", .{
        .credential_is_api_key = true,
    }));
    try std.testing.expect(!oauthOriginAllowedWithSources("kimi", "https://api.kimi.com/coding", .{}));

    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "https://attacker.test", .{
        .credential_is_api_key = true,
    }));
    try std.testing.expect(!oauthOriginAllowedWithSources("github-copilot", "https://attacker.test", .{
        .credential_is_api_key = true,
    }));
    try std.testing.expect(!oauthOriginAllowedWithSources("openai-codex", "https://attacker.test", .{
        .credential_is_api_key = true,
    }));
}

test "storedCredentialIsApiKeyShaped keys on the absent refresh token" {
    try std.testing.expect(storedCredentialIsApiKeyShaped(""));
    try std.testing.expect(!storedCredentialIsApiKeyShaped("refresh-token"));
}

test "oauthOriginAllowedWithSources honours operator-configured overrides" {
    try std.testing.expect(oauthOriginAllowedWithSources("anthropic", "https://proxy.corp.test/anthropic", .{
        .provider = "https://proxy.corp.test",
    }));
    try std.testing.expect(oauthOriginAllowedWithSources("anthropic", "https://proxy.corp.test/anthropic", .{
        .global = "https://proxy.corp.test/v1",
    }));
    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "https://other.corp.test", .{
        .provider = "https://proxy.corp.test",
    }));
}

test "oauthOriginAllowedWithSources accepts the GitHub Copilot domain and the stored origin" {
    try std.testing.expect(oauthOriginAllowedWithSources("github-copilot", "https://api.individual.githubcopilot.com", .{}));
    try std.testing.expect(oauthOriginAllowedWithSources("github-copilot", "https://api.acme.githubcopilot.com", .{}));
    try std.testing.expect(oauthOriginAllowedWithSources("github-copilot", "https://githubcopilot.com", .{}));
    try std.testing.expect(!oauthOriginAllowedWithSources("github-copilot", "https://notgithubcopilot.com", .{}));
    try std.testing.expect(!oauthOriginAllowedWithSources("github-copilot", "https://githubcopilot.com.attacker.test", .{}));

    try std.testing.expect(oauthOriginAllowedWithSources("github-copilot", "https://copilot.acme.test", .{
        .credential = "https://copilot.acme.test",
    }));
    try std.testing.expect(!oauthOriginAllowedWithSources("github-copilot", "https://copilot.acme.test", .{}));
    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "https://copilot.acme.test", .{
        .credential = "https://copilot.acme.test",
    }));
}

test "oauthOriginAllowedWithSources reads the host rather than the userinfo" {
    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "https://api.anthropic.com@attacker.test", .{}));
    try std.testing.expect(!oauthOriginAllowedWithSources("github-copilot", "https://api.githubcopilot.com@attacker.test", .{}));
}

test "oauthOriginAllowedWithSources rejects unparseable base urls" {
    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "api.anthropic.com", .{}));
    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "https://", .{}));
    try std.testing.expect(!oauthOriginAllowedWithSources("anthropic", "https://api.anthropic.com:notaport", .{}));
}

test "credentialDeclaredBaseUrl reads the login-recorded endpoint" {
    const allocator = std.testing.allocator;

    const found = credentialDeclaredBaseUrl(allocator, "{\"baseUrl\":\"https://api.acme.githubcopilot.com\"}").?;
    defer allocator.free(found);
    try std.testing.expectEqualStrings("https://api.acme.githubcopilot.com", found);

    try std.testing.expect(credentialDeclaredBaseUrl(allocator, "{\"enterpriseUrl\":\"https://gh.acme.test\"}") == null);
    try std.testing.expect(credentialDeclaredBaseUrl(allocator, "{\"baseUrl\":\"\"}") == null);
    try std.testing.expect(credentialDeclaredBaseUrl(allocator, "not json") == null);
    try std.testing.expect(credentialDeclaredBaseUrl(allocator, "[]") == null);
}

test "oauthOriginAllowed reads the credential-declared endpoint" {
    const allocator = std.testing.allocator;
    try std.testing.expect(oauthOriginAllowed(
        allocator,
        "github-copilot",
        "https://copilot.acme.test",
        "gho-refresh",
        "{\"baseUrl\":\"https://copilot.acme.test\"}",
        .{},
    ));
    try std.testing.expect(!oauthOriginAllowed(
        allocator,
        "github-copilot",
        "https://attacker.test",
        "gho-refresh",
        "{\"baseUrl\":\"https://copilot.acme.test\"}",
        .{},
    ));
    try std.testing.expect(oauthOriginAllowed(allocator, "anthropic", "", "refresh", null, .{}));
}

test "oauthOriginAllowed lets a kimi login keep streaming" {
    const allocator = std.testing.allocator;
    try std.testing.expect(oauthOriginAllowed(allocator, "kimi", "https://api.kimi.com/coding", "", "region:china", .{}));
    try std.testing.expect(oauthOriginAllowed(allocator, "kimi", "https://api.moonshot.ai", "", "region:global", .{}));
    try std.testing.expect(!oauthOriginAllowed(allocator, "anthropic", "https://attacker.test", "", null, .{}));
}

test "defaultMaxTokensForRef reads the row's own figure, and only for a wire the row declares" {
    const kimi_row = provider_catalog.rowMaxTokens("kimi").?;
    try std.testing.expectEqual(kimi_row, defaultMaxTokensForRef("kimi", "openai-completions"));
    try std.testing.expectEqual(@as(u32, 4_096), defaultMaxTokensForRef("anthropic", "anthropic-messages"));
    try std.testing.expectEqual(@as(u32, 4_096), defaultMaxTokensForRef("openai", "openai-completions"));
    try std.testing.expectEqual(@as(u32, 4_096), defaultMaxTokensForRef("kimi", "openai-responses"));
    try std.testing.expect(provider_catalog.declaresWire("kimi", "openai-completions"));
    try std.testing.expect(!provider_catalog.declaresWire("kimi", "openai-responses"));
    try std.testing.expect(!provider_catalog.declaresWire("no-such-provider", "openai-completions"));
}
