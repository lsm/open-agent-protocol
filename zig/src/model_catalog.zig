const std = @import("std");
const builtin = @import("builtin");
const compat = @import("compat");
const ai_types = @import("ai_types");
const oauth_storage = @import("oauth/storage");
const codex_oauth = @import("oauth/openai_codex");
const anthropic_oauth = @import("oauth/anthropic");
const custom_providers = @import("custom_providers");
const github_copilot = @import("oauth/github_copilot");
const provider_catalog = @import("provider_catalog");
const provider_credential = @import("provider_credential");
const provider_base_url = @import("provider_base_url");
const anthropic_messages_api = @import("anthropic_messages_api");
const openai_completions_api = @import("openai_completions_api");
const openai_responses_api = @import("openai_responses_api");
const ollama_api = @import("ollama_api");

const openai_codex_provider_id = "openai-codex";
const openai_codex_api_id = "openai-codex-responses";
const openai_codex_base_url = provider_catalog.baseUrlOrCompileError("openai-codex", openai_codex_api_id, null);
const kimi_provider_id = "kimi";
const kimi_api_id = "openai-completions";
const kimi_model_id = "kimi-k2.7-code";
const github_copilot_provider_id = "github-copilot";
const github_copilot_api_name = "openai-completions";
const kimi_base_url = provider_catalog.baseUrlOrCompileError(kimi_provider_id, kimi_api_id, "china");
const kimi_env_key = provider_catalog.credentialEnv(kimi_provider_id)[0];
const kimi_region_env = provider_catalog.regionEnv(kimi_provider_id) orelse
    @compileError("providers/catalog.json records no region_env for kimi");
const codex_models_cache_name = "models_cache.json";
const makai_catalog_dir_name = "model_catalog";
const makai_codex_catalog_name = "openai-codex.json";
const makai_anthropic_catalog_name = "anthropic.json";
const anthropic_provider_id = "anthropic";
const anthropic_api_name = "anthropic-messages";
const anthropic_base_url = provider_catalog.baseUrlOrCompileError(anthropic_provider_id, anthropic_api_name, null);
const anthropic_env_keys = provider_catalog.credentialEnv(anthropic_provider_id);
const max_catalog_bytes = 2 * 1024 * 1024;
const catalog_context_window: u32 = 128_000;
const catalog_max_output_tokens: u32 = 8_192;
pub const catalog_fetch_timeout_ms: u64 = 20_000;

const anthropic_catalog_max_age_ms: i64 = 24 * 60 * 60 * 1000;
const default_codex_client_version = "0.0.0";
const default_max_output_tokens: u32 = 16_384;

const CodexParseOptions = struct {
    account_id: ?[]const u8 = null,
    client_version: ?[]const u8 = null,
};

const CatalogLoadMode = enum {
    allow_cache,
    force_fetch,
};

fn secureFree(allocator: std.mem.Allocator, data: []const u8) void {
    if (data.len > 0) {
        const writable: []u8 = @constCast(data);
        std.crypto.secureZero(u8, writable);
    }
    allocator.free(data);
}

fn emptyModels(allocator: std.mem.Allocator) ![]ai_types.Model {
    return allocator.alloc(ai_types.Model, 0);
}

pub fn deinitModels(allocator: std.mem.Allocator, models: []ai_types.Model) void {
    for (models) |*model| model.deinit(allocator);
    allocator.free(models);
}

pub fn loadProductionModels(allocator: std.mem.Allocator) ![]ai_types.Model {
    return loadProductionModelsWithMode(allocator, .allow_cache);
}

pub fn refreshProductionModels(allocator: std.mem.Allocator) ![]ai_types.Model {
    return loadProductionModelsWithMode(allocator, .force_fetch);
}

fn loadProductionModelsWithMode(allocator: std.mem.Allocator, mode: CatalogLoadMode) ![]ai_types.Model {
    var loaded_storage: ?oauth_storage.AuthStorage = if (builtin.is_test) null else oauth_storage.AuthStorage.loadDefault(allocator) catch null;
    defer if (loaded_storage) |*storage| storage.deinit();
    const storage: ?*oauth_storage.AuthStorage = if (loaded_storage) |*storage| storage else null;

    var codex_refresh_error: ?anyerror = null;
    var codex_models = loadOpenAICodexModels(allocator, mode, storage) catch |err| blk: {
        if (mode == .allow_cache) return err;
        codex_refresh_error = err;
        break :blk try emptyModels(allocator);
    };
    defer deinitModels(allocator, codex_models);

    var anthropic_models = try loadAnthropicModels(allocator, storage, mode);
    defer deinitModels(allocator, anthropic_models);

    var copilot_models = try loadGitHubCopilotModels(allocator, storage);
    defer deinitModels(allocator, copilot_models);

    var custom_models = try loadCustomModels(allocator, storage, mode);
    defer deinitModels(allocator, custom_models);

    var catalog_models = try loadCatalogModels(allocator, storage, mode);
    defer deinitModels(allocator, catalog_models);

    if (codex_refresh_error) |err| {
        if (anthropic_models.len == 0 and copilot_models.len == 0 and
            custom_models.len == 0 and catalog_models.len == 0)
        {
            return err;
        }
    }

    const lists = [_]*[]ai_types.Model{ &codex_models, &anthropic_models, &copilot_models, &custom_models, &catalog_models };
    var total: usize = 0;
    for (lists) |list| total += list.len;
    const models = try allocator.alloc(ai_types.Model, total);
    var offset: usize = 0;
    for (lists) |list| {
        @memcpy(models[offset .. offset + list.len], list.*);
        offset += list.len;
        allocator.free(list.*);
        list.* = &.{};
    }
    return models;
}

var test_custom_providers_config: ?[]const u8 = null;
var test_custom_discovery_ids: ?[]const []const u8 = null;


var test_force_copilot_models: bool = false;

fn copilotStringFromProviderData(allocator: std.mem.Allocator, provider_data: []const u8, key: []const u8) !?[]u8 {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, provider_data, .{}) catch return null;
    defer parsed.deinit();
    if (parsed.value != .object) return null;
    const value = parsed.value.object.get(key) orelse return null;
    if (value != .string or value.string.len == 0) return null;
    return try allocator.dupe(u8, value.string);
}

fn copilotModelIdsFromProviderData(allocator: std.mem.Allocator, provider_data: []const u8) !?[][]const u8 {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, provider_data, .{}) catch return null;
    defer parsed.deinit();
    if (parsed.value != .object) return null;
    const list = parsed.value.object.get("models") orelse return null;
    if (list != .array) return null;

    var ids = std.ArrayList([]const u8).empty;
    errdefer {
        for (ids.items) |id| allocator.free(id);
        ids.deinit(allocator);
    }
    for (list.array.items) |item| {
        if (item != .string or item.string.len == 0) continue;
        try ids.append(allocator, try allocator.dupe(u8, item.string));
    }
    if (ids.items.len == 0) {
        ids.deinit(allocator);
        return null;
    }
    return try ids.toOwnedSlice(allocator);
}

fn copilotModel(allocator: std.mem.Allocator, id_text: []const u8, base_url_text: []const u8) !ai_types.Model {
    const id = try allocator.dupe(u8, id_text);
    errdefer allocator.free(id);
    const name = try allocator.dupe(u8, id_text);
    errdefer allocator.free(name);
    const api = try allocator.dupe(u8, github_copilot_api_name);
    errdefer allocator.free(api);
    const provider = try allocator.dupe(u8, github_copilot_provider_id);
    errdefer allocator.free(provider);
    const base_url = try allocator.dupe(u8, base_url_text);
    errdefer allocator.free(base_url);
    const input = try allocator.alloc([]const u8, 1);
    errdefer allocator.free(input);
    input[0] = try allocator.dupe(u8, "text");

    return .{
        .id = id,
        .name = name,
        .api = api,
        .provider = provider,
        .base_url = base_url,
        .reasoning = false,
        .input = input,
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 16_384,
        .is_owned = true,
    };
}

fn loadGitHubCopilotModels(allocator: std.mem.Allocator, storage: ?*oauth_storage.AuthStorage) ![]ai_types.Model {
    if (builtin.is_test and !test_force_copilot_models) return emptyModels(allocator);

    const stored = storage orelse return emptyModels(allocator);
    const auth = stored.providers.get(github_copilot_provider_id) orelse return emptyModels(allocator);
    const provider_data: ?[]const u8 = switch (auth) {
        .oauth => |creds| creds.provider_data,
        .api_key => null,
    };

    var discovered: ?[][]const u8 = null;
    defer if (discovered) |ids| freeModelIds(allocator, ids);
    var base_url_owned: ?[]u8 = null;
    defer if (base_url_owned) |value| allocator.free(value);

    if (provider_data) |data| {
        discovered = copilotModelIdsFromProviderData(allocator, data) catch null;
        base_url_owned = copilotStringFromProviderData(allocator, data, "baseUrl") catch null;
        if (base_url_owned == null) {
            if (copilotStringFromProviderData(allocator, data, "enterpriseUrl") catch null) |enterprise| {
                allocator.free(enterprise);
                return emptyModels(allocator);
            }
        }
    }

    const base_url = base_url_owned orelse github_copilot.DEFAULT_BASE_URL;
    const ids: []const []const u8 = discovered orelse &github_copilot.KNOWN_COPILOT_MODELS;

    var models = std.ArrayList(ai_types.Model).empty;
    errdefer {
        for (models.items) |*model| model.deinit(allocator);
        models.deinit(allocator);
    }
    for (ids) |id| {
        var model = try copilotModel(allocator, id, base_url);
        errdefer model.deinit(allocator);
        try models.append(allocator, model);
    }
    return models.toOwnedSlice(allocator);
}

fn loadCustomModels(allocator: std.mem.Allocator, storage: ?*oauth_storage.AuthStorage, mode: CatalogLoadMode) ![]ai_types.Model {
    const providers = if (builtin.is_test)
        try custom_providers.parse(allocator, test_custom_providers_config orelse return emptyModels(allocator))
    else
        custom_providers.load(allocator, custom_providers.max_config_bytes) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return emptyModels(allocator),
        };
    defer custom_providers.deinitProviders(allocator, providers);

    var models = std.ArrayList(ai_types.Model).empty;
    errdefer {
        for (models.items) |*model| model.deinit(allocator);
        models.deinit(allocator);
    }

    for (providers) |*provider| {
        try appendCustomProviderModels(allocator, &models, provider, storage, mode);
    }
    return models.toOwnedSlice(allocator);
}

fn appendCustomProviderModels(
    allocator: std.mem.Allocator,
    models: *std.ArrayList(ai_types.Model),
    provider: *const custom_providers.CustomProvider,
    storage: ?*oauth_storage.AuthStorage,
    mode: CatalogLoadMode,
) !void {
    const discovered = try discoverCustomModelIds(allocator, provider, storage, mode);
    defer if (discovered) |ids| freeModelIds(allocator, ids);

    if (discovered) |ids| {
        for (ids) |id| {
            if (!provider.allows(id)) continue;
            const spec = provider.specFor(id);
            const display = if (spec) |found| found.name else id;
            var model = try customModel(allocator, provider, id, display, spec);
            errdefer model.deinit(allocator);
            try models.append(allocator, model);
        }
        return;
    }

    for (provider.models) |spec| {
        var model = try customModel(allocator, provider, spec.id, spec.name, spec);
        errdefer model.deinit(allocator);
        try models.append(allocator, model);
    }
}

fn customModel(
    allocator: std.mem.Allocator,
    provider: *const custom_providers.CustomProvider,
    id_text: []const u8,
    name_text: []const u8,
    spec: ?custom_providers.ModelSpec,
) !ai_types.Model {
    const id = try allocator.dupe(u8, id_text);
    errdefer allocator.free(id);
    const name = try allocator.dupe(u8, name_text);
    errdefer allocator.free(name);
    const api = try allocator.dupe(u8, provider.api);
    errdefer allocator.free(api);
    const provider_id = try allocator.dupe(u8, provider.id);
    errdefer allocator.free(provider_id);
    const base_url = try allocator.dupe(u8, provider.base_url);
    errdefer allocator.free(base_url);

    const input = try allocator.alloc([]const u8, 1);
    errdefer allocator.free(input);
    input[0] = try allocator.dupe(u8, "text");
    errdefer allocator.free(input[0]);

    var header_list = std.ArrayList(ai_types.HeaderPair).empty;
    errdefer {
        for (header_list.items) |header| {
            allocator.free(header.name);
            allocator.free(header.value);
        }
        header_list.deinit(allocator);
    }
    for (provider.headers) |header| {
        const header_name = try allocator.dupe(u8, header.name);
        errdefer allocator.free(header_name);
        const header_value = try allocator.dupe(u8, header.value);
        errdefer allocator.free(header_value);
        try header_list.append(allocator, .{ .name = header_name, .value = header_value });
    }
    const headers: ?[]ai_types.HeaderPair = if (provider.headers.len > 0)
        try header_list.toOwnedSlice(allocator)
    else
        null;

    return .{
        .id = id,
        .name = name,
        .api = api,
        .provider = provider_id,
        .base_url = base_url,
        .reasoning = provider.reasoning,
        .input = input,
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = if (spec) |found| (found.context_window orelse provider.context_window) else provider.context_window,
        .max_tokens = if (spec) |found| (found.max_tokens orelse provider.max_tokens) else provider.max_tokens,
        .headers = headers,
        .compat = provider.compat,
        .allows_anonymous = provider.auth_none,
        .carries_version = provider.carries_version,
        .is_owned = true,
    };
}

fn freeModelIds(allocator: std.mem.Allocator, ids: [][]const u8) void {
    for (ids) |id| allocator.free(id);
    allocator.free(ids);
}

const CatalogDiscovery = struct {
    id: []const u8,
    models_url: []const u8,
    model_ids: []const []const u8,
};

var test_catalog_discovery: ?[]const CatalogDiscovery = null;
var test_catalog_environment: ?[]const provider_credential.EnvironmentValue = null;
var test_catalog_base_urls: ?provider_base_url.BaseUrlOverrides = null;

const loader_wires = [_][]const []const u8{
    anthropic_messages_api.wires,
    openai_completions_api.wires,
    openai_responses_api.wires,
    ollama_api.wires,
};

fn wireIsImplemented(wire: []const u8) bool {
    for (loader_wires) |claim| {
        for (claim) |claimed| {
            if (std.mem.eql(u8, claimed, wire)) return true;
        }
    }
    return false;
}

const CatalogEndpoint = struct {
    id: []const u8,
    wire: []const u8,
    base_url: []const u8,
    models_url: []const u8,
    region: ?[]const u8 = null,
    owned_base_url: ?[]u8 = null,
    owned_models_url: ?[]u8 = null,

    fn deinit(self: *CatalogEndpoint, allocator: std.mem.Allocator) void {
        if (self.owned_base_url) |value| allocator.free(value);
        if (self.owned_models_url) |value| allocator.free(value);
        self.* = undefined;
    }
};

fn catalogTarget(id: []const u8) ?CatalogEndpoint {
    return catalogTargetInRegion(id, null);
}

fn catalogTargetInRegion(id: []const u8, region: ?[]const u8) ?CatalogEndpoint {
    const row = provider_catalog.provider(id) orelse return null;
    var chosen: ?[]const u8 = null;
    for (row.wires) |wire| {
        if (!wireIsImplemented(wire)) continue;
        if (provider_catalog.requestUrl(id, wire, region) == null) continue;
        chosen = wire;
        break;
    }
    const wire = chosen orelse return null;
    const base_url = provider_catalog.baseUrl(id, wire, region) orelse return null;
    const models_url = provider_catalog.modelsUrl(id, region) orelse return null;
    return .{ .id = row.id, .wire = wire, .base_url = base_url, .models_url = models_url, .region = region };
}

fn catalogEndpointWithBase(
    allocator: std.mem.Allocator,
    catalog: CatalogEndpoint,
    base_url: []const u8,
) !CatalogEndpoint {
    if (base_url.len == 0) {
        return .{
            .id = catalog.id,
            .wire = catalog.wire,
            .base_url = catalog.base_url,
            .models_url = catalog.models_url,
            .region = catalog.region,
        };
    }
    const models_path = provider_catalog.modelsEndpoint(catalog.id) orelse unreachable;
    errdefer allocator.free(base_url);
    const models_url = try provider_catalog.listingUrlOwned(
        allocator,
        base_url,
        models_path,
        provider_catalog.endpointCarriesVersion(catalog.id, catalog.base_url),
        true,
    );
    return .{
        .id = catalog.id,
        .wire = catalog.wire,
        .base_url = base_url,
        .models_url = models_url,
        .region = catalog.region,
        .owned_base_url = @constCast(base_url),
        .owned_models_url = @constCast(models_url),
    };
}

fn catalogEndpointWithOverrides(
    allocator: std.mem.Allocator,
    id: []const u8,
    overrides: provider_base_url.BaseUrlOverrides,
) !?CatalogEndpoint {
    const region = catalogRegion(allocator, null, id);
    const catalog = catalogTargetInRegion(id, region) orelse return null;
    const base_url = try provider_base_url.baseUrlWithOverrides(allocator, id, catalog.wire, overrides);
    return try catalogEndpointWithBase(allocator, catalog, base_url);
}

fn catalogEndpointFromEnvironment(
    allocator: std.mem.Allocator,
    storage: ?*oauth_storage.AuthStorage,
    id: []const u8,
) !?CatalogEndpoint {
    const region = catalogRegion(allocator, storage, id);
    const catalog = catalogTargetInRegion(id, region) orelse return null;
    const base_url = try provider_base_url.defaultBaseUrlForRefWithRegion(allocator, id, catalog.wire, region);
    return try catalogEndpointWithBase(allocator, catalog, base_url);
}

fn catalogRegion(allocator: std.mem.Allocator, storage: ?*oauth_storage.AuthStorage, id: []const u8) ?[]const u8 {
    const fallback = provider_catalog.defaultRegion(id);
    if (provider_catalog.regionEnv(id)) |name| {
        if (compat.getEnvVarOwned(allocator, name) catch null) |value| {
            defer allocator.free(value);
            if (provider_catalog.regionFromValue(id, value)) |resolved| return resolved;
        }
    }
    if (catalogStoredRegion(id, storage)) |stored| return stored;
    return fallback;
}


fn catalogStoredRegion(id: []const u8, storage: ?*oauth_storage.AuthStorage) ?[]const u8 {
    const stored = storage orelse return null;
    const auth = stored.resolvedCredential(id) orelse return null;
    if (auth != .oauth) return null;
    const provider_data = auth.oauth.provider_data orelse return null;
    if (!std.mem.startsWith(u8, provider_data, "region:")) return null;
    return provider_catalog.regionFromValue(id, provider_data["region:".len..]);
}

const catalog_loader_ids = [_][]const u8{
    "deepseek",
    "openrouter",
    "opencode",
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

const deepseek_catalog_models_url = "https://api.deepseek.com/v1/models";
const proxy_models_url = "https://proxy.example/api/v1/models";
const xiaomi_catalog_models_url = "https://token-plan-cn.xiaomimimo.com/v1/models";

fn loadCatalogModels(
    allocator: std.mem.Allocator,
    storage: ?*oauth_storage.AuthStorage,
    mode: CatalogLoadMode,
) ![]ai_types.Model {
    return loadCatalogModelsWithRows(allocator, &catalog_loader_ids, storage, mode);
}

fn loadCatalogModelsWithRows(
    allocator: std.mem.Allocator,
    ids: []const []const u8,
    storage: ?*oauth_storage.AuthStorage,
    mode: CatalogLoadMode,
) ![]ai_types.Model {
    var models = std.ArrayList(ai_types.Model).empty;
    errdefer {
        for (models.items) |*model| model.deinit(allocator);
        models.deinit(allocator);
    }
    for (ids) |id| {
        const endpoint = if (builtin.is_test)
            try catalogEndpointWithOverrides(allocator, id, test_catalog_base_urls orelse .{})
        else
            try catalogEndpointFromEnvironment(allocator, storage, id);
        var held = endpoint orelse continue;
        defer held.deinit(allocator);
        try appendCatalogTargetModels(allocator, &models, held, storage, mode);
    }
    return models.toOwnedSlice(allocator);
}

fn appendCatalogTargetModels(
    allocator: std.mem.Allocator,
    out: *std.ArrayList(ai_types.Model),
    target: CatalogEndpoint,
    storage: ?*oauth_storage.AuthStorage,
    mode: CatalogLoadMode,
) !void {
    const environment = try catalogEnvironment(allocator, target.id);
    defer freeEnvironment(allocator, environment);

    var credential = (try provider_credential.lookup(allocator, environment, storage, target.id)) orelse return;
    defer credential.deinit(allocator);

    const discovered = try discoverCatalogModels(allocator, target, credential.key, mode);
    defer if (discovered) |models| freeDiscoveredModels(allocator, models);

    if (discovered) |models| {
        if (models.len > 0) {
            for (models) |model| {
                var built = try catalogModel(allocator, target, model);
                errdefer built.deinit(allocator);
                try out.append(allocator, built);
            }
            return;
        }
    }

    for (provider_catalog.modelsFor(target.id)) |declared| {
        var built = try catalogModel(allocator, target, .{ .id = declared.id });
        errdefer built.deinit(allocator);
        try out.append(allocator, built);
    }
}

fn catalogModel(allocator: std.mem.Allocator, target: CatalogEndpoint, model: DiscoveredModel) !ai_types.Model {
    const wire = provider_catalog.wireForModel(target.id, model.id) orelse return error.UnsupportedCatalogWire;
    const id = try allocator.dupe(u8, model.id);
    errdefer allocator.free(id);
    const name = try allocator.dupe(u8, displayNameFor(target.id, model));
    errdefer allocator.free(name);
    const api = try allocator.dupe(u8, wire.id);
    errdefer allocator.free(api);
    const provider_id = try allocator.dupe(u8, target.id);
    errdefer allocator.free(provider_id);
    const base_url = try allocator.dupe(u8, target.base_url);
    errdefer allocator.free(base_url);
    const image = model.image_input orelse false;
    const input = try allocator.alloc([]const u8, if (image) 2 else 1);
    errdefer allocator.free(input);
    input[0] = try allocator.dupe(u8, "text");
    var filled: usize = 1;
    errdefer for (input[0..filled]) |value| allocator.free(value);
    if (image) {
        input[1] = try allocator.dupe(u8, "image");
        filled = 2;
    }

    return .{
        .id = id,
        .name = name,
        .api = api,
        .provider = provider_id,
        .base_url = base_url,
        .reasoning = model.reasoning orelse false,
        .input = input,
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = contextWindowFor(target.id, model),
        .max_tokens = maxTokensFor(target.id, model),
        .is_owned = true,
    };
}

fn displayNameFor(id: []const u8, model: DiscoveredModel) []const u8 {
    if (model.name) |reported| return reported;
    const declared = provider_catalog.declaredModel(id, model.id) orelse return model.id;
    return declared.name orelse model.id;
}

fn contextWindowFor(id: []const u8, model: DiscoveredModel) u32 {
    if (model.context_window) |reported| return reported;
    if (provider_catalog.declaredModel(id, model.id)) |declared| {
        if (declared.context_window) |window| return window;
    }
    return provider_catalog.rowContextWindow(id) orelse catalog_context_window;
}

fn maxTokensFor(id: []const u8, model: DiscoveredModel) u32 {
    if (model.max_tokens) |reported| return reported;
    if (provider_catalog.declaredModel(id, model.id)) |declared| {
        if (declared.max_tokens) |tokens| return tokens;
    }
    return provider_catalog.rowMaxTokens(id) orelse catalog_max_output_tokens;
}

fn catalogRowCacheName(allocator: std.mem.Allocator, id: []const u8, region: ?[]const u8) ![]u8 {
    if (region) |resolved| return std.fmt.allocPrint(allocator, "catalog-{s}-{s}.json", .{ id, resolved });
    return std.fmt.allocPrint(allocator, "catalog-{s}.json", .{id});
}

fn catalogEnvironment(allocator: std.mem.Allocator, id: []const u8) ![]provider_credential.EnvironmentValue {
    const row = provider_catalog.provider(id) orelse return allocator.alloc(provider_credential.EnvironmentValue, 0);
    var out = std.ArrayList(provider_credential.EnvironmentValue).empty;
    errdefer {
        for (out.items) |held| allocator.free(held.value);
        out.deinit(allocator);
    }
    for (row.credential_env) |name| {
        const value = if (builtin.is_test)
            (try allocator.dupe(u8, testHeldValue(test_catalog_environment orelse &.{}, name) orelse continue))
        else
            (compat.getEnvVarOwned(allocator, name) catch continue);
        errdefer allocator.free(value);
        try out.append(allocator, .{ .name = name, .value = value });
    }
    return out.toOwnedSlice(allocator);
}

fn testHeldValue(held: []const provider_credential.EnvironmentValue, name: []const u8) ?[]const u8 {
    for (held) |entry| {
        if (!std.mem.eql(u8, entry.name, name)) continue;
        return entry.value;
    }
    return null;
}

fn freeEnvironment(allocator: std.mem.Allocator, values: []provider_credential.EnvironmentValue) void {
    for (values) |held| allocator.free(held.value);
    allocator.free(values);
}

fn discoverCatalogModels(
    allocator: std.mem.Allocator,
    target: CatalogEndpoint,
    token: []const u8,
    mode: CatalogLoadMode,
) !?[]DiscoveredModel {
    if (builtin.is_test) return testCatalogModels(allocator, target.id, target.models_url);

    const name = try catalogRowCacheName(allocator, target.id, target.region);
    defer allocator.free(name);

    if (mode == .allow_cache) {
        if (try loadCachedCatalogModels(allocator, name, anthropic_catalog_max_age_ms)) |models| return models;
    }

    if (fetchCatalogModelsCatalog(allocator, target, token)) |body| {
        defer allocator.free(body);
        if (parseCatalogModels(allocator, body)) |models| {
            if (models.len > 0) {
                saveMakaiCatalog(allocator, name, body) catch {};
                return models;
            }
            freeDiscoveredModels(allocator, models);
        } else |_| {}
    } else |_| {}

    return loadCachedCatalogModels(allocator, name, null);
}

fn testCatalogModels(allocator: std.mem.Allocator, id: []const u8, models_url: []const u8) !?[]DiscoveredModel {
    const rows = test_catalog_discovery orelse return null;
    for (rows) |row| {
        if (!std.mem.eql(u8, row.id, id)) continue;
        if (!std.mem.eql(u8, row.models_url, models_url)) continue;
        const out = try allocator.alloc(DiscoveredModel, row.model_ids.len);
        var filled: usize = 0;
        errdefer {
            for (out[0..filled]) |value| value.deinit(allocator);
            allocator.free(out);
        }
        for (row.model_ids, 0..) |model_id, i| {
            out[i] = .{ .id = try allocator.dupe(u8, model_id) };
            filled = i + 1;
        }
        return out;
    }
    return null;
}

fn fetchCatalogModelsCatalog(allocator: std.mem.Allocator, target: CatalogEndpoint, token: []const u8) ![]u8 {
    var headers: std.ArrayList(std.http.Header) = .empty;
    defer headers.deinit(allocator);
    try headers.append(allocator, .{ .name = "accept", .value = "application/json" });
    var owned_bearer: ?[]u8 = null;
    defer if (owned_bearer) |value| secureFree(allocator, value);
    if (std.mem.eql(u8, target.wire, "anthropic-messages")) {
        try headers.append(allocator, .{ .name = "x-api-key", .value = token });
        try headers.append(allocator, .{ .name = "anthropic-version", .value = "2023-06-01" });
    } else {
        const bearer = try std.fmt.allocPrint(allocator, "Bearer {s}", .{token});
        owned_bearer = bearer;
        try headers.append(allocator, .{ .name = "authorization", .value = bearer });
    }

    var fetched = compat.http.fetch(allocator, target.models_url, .{
        .method = .GET,
        .extra_headers = headers.items,
        .accept_encoding = "identity",
        .max_response_bytes = max_catalog_bytes,
        .timeout_ms = catalog_fetch_timeout_ms,
    }) catch return error.ModelCatalogFetchFailed;
    errdefer fetched.deinit(allocator);

    if (fetched.status != 200) return error.ModelCatalogFetchFailed;
    return fetched.body;
}

fn customCatalogName(allocator: std.mem.Allocator, provider_id: []const u8) ![]u8 {
    return std.fmt.allocPrint(allocator, "custom-{s}.json", .{provider_id});
}

fn customModelsUrl(allocator: std.mem.Allocator, provider: *const custom_providers.CustomProvider) ![]const u8 {
    return provider_catalog.joinUrlOwned(
        allocator,
        provider.base_url,
        .{ .id = "models", .suffix = "/v1/models" },
        provider_catalog.carriesVersionFor(provider.id, provider.base_url, provider.carries_version),
    );
}

fn discoverCustomModelIds(
    allocator: std.mem.Allocator,
    provider: *const custom_providers.CustomProvider,
    storage: ?*oauth_storage.AuthStorage,
    mode: CatalogLoadMode,
) !?[][]const u8 {
    if (builtin.is_test) {
        const ids = test_custom_discovery_ids orelse return null;
        const out = try allocator.alloc([]const u8, ids.len);
        var filled: usize = 0;
        errdefer {
            for (out[0..filled]) |value| allocator.free(value);
            allocator.free(out);
        }
        for (ids, 0..) |id, i| {
            out[i] = try allocator.dupe(u8, id);
            filled = i + 1;
        }
        return out;
    }

    const name = try customCatalogName(allocator, provider.id);
    defer allocator.free(name);

    if (mode == .allow_cache) {
        if (try loadCachedModelIds(allocator, name, anthropic_catalog_max_age_ms)) |ids| return ids;
        return loadCachedModelIds(allocator, name, null);
    }

    const token = customCredential(allocator, provider, storage);
    defer if (token) |value| secureFree(allocator, value);

    if (fetchCustomModelsCatalog(allocator, provider, token)) |body| {
        defer allocator.free(body);
        if (parseModelIds(allocator, body)) |ids| {
            if (ids.len > 0) {
                saveMakaiCatalog(allocator, name, body) catch {};
                return ids;
            }
            freeModelIds(allocator, ids);
        } else |_| {}
    } else |_| {}

    return loadCachedModelIds(allocator, name, null);
}

fn loadCachedModelIds(allocator: std.mem.Allocator, name: []const u8, max_age_ms: ?i64) !?[][]const u8 {
    const path = makaiCatalogPath(allocator, name) catch return null;
    defer allocator.free(path);
    if (max_age_ms) |max_age| {
        const modified = compat.fs.modifiedMillis(compat.fs.getCwd(), path) catch return null;
        if (!catalogIsFresh(modified, compat.time.nowMillis(), max_age)) return null;
    }
    const data = compat.fs.readFileAlloc(allocator, compat.fs.getCwd(), path, max_catalog_bytes) catch return null;
    defer allocator.free(data);
    const ids = parseModelIds(allocator, data) catch return null;
    if (ids.len > 0) return ids;
    freeModelIds(allocator, ids);
    return null;
}

fn loadCachedCatalogModels(allocator: std.mem.Allocator, name: []const u8, max_age_ms: ?i64) !?[]DiscoveredModel {
    const path = makaiCatalogPath(allocator, name) catch return null;
    defer allocator.free(path);
    if (max_age_ms) |max_age| {
        const modified = compat.fs.modifiedMillis(compat.fs.getCwd(), path) catch return null;
        if (!catalogIsFresh(modified, compat.time.nowMillis(), max_age)) return null;
    }
    const data = compat.fs.readFileAlloc(allocator, compat.fs.getCwd(), path, max_catalog_bytes) catch return null;
    defer allocator.free(data);
    const models = parseCatalogModels(allocator, data) catch return null;
    if (models.len > 0) return models;
    freeDiscoveredModels(allocator, models);
    return null;
}

fn customCredential(
    allocator: std.mem.Allocator,
    provider: *const custom_providers.CustomProvider,
    storage: ?*oauth_storage.AuthStorage,
) ?[]const u8 {
    if (storage) |stored| {
        if (stored.providers.get(provider.id)) |auth| {
            switch (auth) {
                .api_key => |key| return allocator.dupe(u8, key) catch null,
                .oauth => |value| return allocator.dupe(u8, value.access) catch null,
            }
        }
    }
    if (provider.env_key) |env_name| {
        if (compat.getEnvVarOwned(allocator, env_name)) |value| {
            if (value.len > 0) return value;
            allocator.free(value);
        } else |_| {}
    }
    return null;
}

const DiscoveredModel = struct {
    id: []const u8,
    name: ?[]const u8 = null,
    context_window: ?u32 = null,
    max_tokens: ?u32 = null,
    reasoning: ?bool = null,
    image_input: ?bool = null,

    fn deinit(self: DiscoveredModel, allocator: std.mem.Allocator) void {
        allocator.free(self.id);
        if (self.name) |value| allocator.free(value);
    }
};

fn freeDiscoveredModels(allocator: std.mem.Allocator, models: []DiscoveredModel) void {
    for (models) |model| model.deinit(allocator);
    allocator.free(models);
}

fn parseCatalogModels(allocator: std.mem.Allocator, data: []const u8) ![]DiscoveredModel {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, data, .{});
    defer parsed.deinit();
    if (parsed.value != .object) return error.InvalidModelCatalog;
    const list = parsed.value.object.get("data") orelse return error.InvalidModelCatalog;
    if (list != .array) return error.InvalidModelCatalog;

    var models = std.ArrayList(DiscoveredModel).empty;
    errdefer {
        for (models.items) |model| model.deinit(allocator);
        models.deinit(allocator);
    }
    for (list.array.items) |item| {
        if (item != .object) continue;
        const id = objectString(&item.object, "id") orelse continue;
        if (id.len == 0) continue;
        var model = DiscoveredModel{ .id = try allocator.dupe(u8, id) };
        errdefer model.deinit(allocator);
        if (objectString(&item.object, "display_name")) |name| {
            if (name.len > 0) model.name = try allocator.dupe(u8, name);
        }
        if (model.context_window == null) {
            model.context_window = objectU32(&item.object, "context_length") orelse
                objectU32(&item.object, "context_window");
        }
        if (model.max_tokens == null) {
            model.max_tokens = objectU32(&item.object, "max_output_tokens") orelse
                objectU32(&item.object, "max_tokens");
        }
        if (model.reasoning == null) {
            model.reasoning = objectBool(&item.object, "supports_reasoning") orelse
                objectBool(&item.object, "reasoning");
        }
        if (model.image_input == null) {
            model.image_input = objectBool(&item.object, "supports_image_in") orelse
                objectBool(&item.object, "vision");
        }
        if (model.image_input == null) {
            if (listNamesImage(&item.object, "modalities") or listNamesImage(&item.object, "input_modalities")) {
                model.image_input = true;
            }
        }
        try models.append(allocator, model);
    }
    return models.toOwnedSlice(allocator);
}

fn listNamesImage(obj: *const std.json.ObjectMap, key: []const u8) bool {
    const value = obj.get(key) orelse return false;
    if (value != .array) return false;
    for (value.array.items) |entry| {
        if (entry != .string) continue;
        if (std.ascii.eqlIgnoreCase(entry.string, "image")) return true;
    }
    return false;
}

fn parseModelIds(allocator: std.mem.Allocator, data: []const u8) ![][]const u8 {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, data, .{});
    defer parsed.deinit();
    if (parsed.value != .object) return error.InvalidModelCatalog;
    const list = parsed.value.object.get("data") orelse return error.InvalidModelCatalog;
    if (list != .array) return error.InvalidModelCatalog;

    var ids = std.ArrayList([]const u8).empty;
    errdefer {
        for (ids.items) |id| allocator.free(id);
        ids.deinit(allocator);
    }
    for (list.array.items) |item| {
        if (item != .object) continue;
        const id = objectString(&item.object, "id") orelse continue;
        if (id.len == 0) continue;
        try ids.append(allocator, try allocator.dupe(u8, id));
    }
    return ids.toOwnedSlice(allocator);
}











var test_force_codex_refresh_error: bool = false;
var test_force_anthropic_models: bool = false;

const AnthropicSpec = struct {
    prefix: []const u8,
    cost: ai_types.Cost,
    max_tokens: u32,
};

const anthropic_known_models = [_]AnthropicSpec{
    .{ .prefix = "claude-opus-4-1", .cost = .{ .input = 15.0, .output = 75.0, .cache_read = 1.50, .cache_write = 18.75 }, .max_tokens = 32_000 },
    .{ .prefix = "claude-opus-4", .cost = .{ .input = 15.0, .output = 75.0, .cache_read = 1.50, .cache_write = 18.75 }, .max_tokens = 32_000 },
    .{ .prefix = "claude-sonnet-4-5", .cost = .{ .input = 3.0, .output = 15.0, .cache_read = 0.30, .cache_write = 3.75 }, .max_tokens = 64_000 },
    .{ .prefix = "claude-sonnet-4", .cost = .{ .input = 3.0, .output = 15.0, .cache_read = 0.30, .cache_write = 3.75 }, .max_tokens = 64_000 },
    .{ .prefix = "claude-haiku-4-5", .cost = .{ .input = 1.0, .output = 5.0, .cache_read = 0.10, .cache_write = 1.25 }, .max_tokens = 64_000 },
    .{ .prefix = "claude-3-7-sonnet", .cost = .{ .input = 3.0, .output = 15.0, .cache_read = 0.30, .cache_write = 3.75 }, .max_tokens = 64_000 },
    .{ .prefix = "claude-3-5-sonnet", .cost = .{ .input = 3.0, .output = 15.0, .cache_read = 0.30, .cache_write = 3.75 }, .max_tokens = 8_192 },
    .{ .prefix = "claude-3-5-haiku", .cost = .{ .input = 0.80, .output = 4.0, .cache_read = 0.08, .cache_write = 1.0 }, .max_tokens = 8_192 },
};

const AnthropicStatic = struct { id: []const u8, name: []const u8 };

const anthropic_static_models = [_]AnthropicStatic{
    .{ .id = "claude-fable-5-1", .name = "Claude Fable 5.1" },
    .{ .id = "claude-opus-5", .name = "Claude Opus 5" },
    .{ .id = "claude-sonnet-5", .name = "Claude Sonnet 5" },
    .{ .id = "claude-sonnet-4-5", .name = "Claude Sonnet 4.5" },
    .{ .id = "claude-haiku-4-5-20251001", .name = "Claude Haiku 4.5" },
    .{ .id = "claude-opus-4-1", .name = "Claude Opus 4.1" },
};

fn anthropicSpec(id: []const u8) ?AnthropicSpec {
    for (anthropic_known_models) |spec| {
        if (std.mem.startsWith(u8, id, spec.prefix)) return spec;
    }
    return null;
}

fn anthropicModel(allocator: std.mem.Allocator, id_text: []const u8, name_text: []const u8) !ai_types.Model {
    const id = try allocator.dupe(u8, id_text);
    errdefer allocator.free(id);
    const name = try allocator.dupe(u8, name_text);
    errdefer allocator.free(name);
    const api = try allocator.dupe(u8, anthropic_api_name);
    errdefer allocator.free(api);
    const provider = try allocator.dupe(u8, anthropic_provider_id);
    errdefer allocator.free(provider);
    const base_url = try allocator.dupe(u8, anthropic_base_url);
    errdefer allocator.free(base_url);
    const input = try allocator.alloc([]const u8, 2);
    errdefer allocator.free(input);
    input[0] = try allocator.dupe(u8, "text");
    errdefer allocator.free(input[0]);
    input[1] = try allocator.dupe(u8, "image");
    errdefer allocator.free(input[1]);

    const spec = anthropicSpec(id_text);
    return .{
        .id = id,
        .name = name,
        .api = api,
        .provider = provider,
        .base_url = base_url,
        .reasoning = true,
        .input = input,
        .cost = if (spec) |known| known.cost else .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 200_000,
        .max_tokens = if (spec) |known| known.max_tokens else 32_000,
        .is_owned = true,
    };
}

fn anthropicStaticModels(allocator: std.mem.Allocator) ![]ai_types.Model {
    var models = std.ArrayList(ai_types.Model).empty;
    errdefer {
        for (models.items) |*model| model.deinit(allocator);
        models.deinit(allocator);
    }
    for (anthropic_static_models) |entry| {
        try models.append(allocator, try anthropicModel(allocator, entry.id, entry.name));
    }
    return models.toOwnedSlice(allocator);
}

fn parseAnthropicModels(allocator: std.mem.Allocator, data: []const u8) ![]ai_types.Model {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, data, .{});
    defer parsed.deinit();
    if (parsed.value != .object) return error.InvalidModelCatalog;
    const list = parsed.value.object.get("data") orelse return error.InvalidModelCatalog;
    if (list != .array) return error.InvalidModelCatalog;

    var models = std.ArrayList(ai_types.Model).empty;
    errdefer {
        for (models.items) |*model| model.deinit(allocator);
        models.deinit(allocator);
    }
    for (list.array.items) |item| {
        if (item != .object) continue;
        const obj = &item.object;
        const id = objectString(obj, "id") orelse continue;
        if (id.len == 0 or !std.mem.startsWith(u8, id, "claude")) continue;
        const name = objectString(obj, "display_name") orelse id;
        try models.append(allocator, try anthropicModel(allocator, id, name));
    }
    return models.toOwnedSlice(allocator);
}

fn refreshAnthropicCredentials(credentials: oauth_storage.Credentials, allocator: std.mem.Allocator) !oauth_storage.Credentials {
    const refreshed = try anthropic_oauth.refreshToken(.{
        .refresh = credentials.refresh,
        .access = credentials.access,
        .expires = credentials.expires,
    }, allocator);
    return .{ .refresh = refreshed.refresh, .access = refreshed.access, .expires = refreshed.expires };
}

fn getAnthropicApiKey(credentials: oauth_storage.Credentials, allocator: std.mem.Allocator) ![]const u8 {
    return try anthropic_oauth.getApiKey(.{
        .refresh = credentials.refresh,
        .access = credentials.access,
        .expires = credentials.expires,
    }, allocator);
}

fn anthropicOAuthProvider() oauth_storage.OAuthProvider {
    return .{
        .id = anthropic_provider_id,
        .name = "Anthropic",
        .refresh_fn = refreshAnthropicCredentials,
        .get_api_key_fn = getAnthropicApiKey,
    };
}

fn anthropicCredential(allocator: std.mem.Allocator, storage: ?*oauth_storage.AuthStorage) !?[]const u8 {
    if (storage) |stored| {
        if (stored.providers.contains(anthropic_provider_id)) {
            if (stored.getApiKey(anthropic_provider_id, anthropicOAuthProvider()) catch null) |token| return token;
        }
    }
    for (anthropic_env_keys) |name| {
        if (compat.getEnvVarOwned(allocator, name)) |key| {
            if (key.len > 0) return key;
            allocator.free(key);
        } else |_| {}
    }
    return null;
}

fn isAnthropicOAuthToken(token: []const u8) bool {
    return std.mem.indexOf(u8, token, "sk-ant-oat") != null;
}

fn loadAnthropicModels(allocator: std.mem.Allocator, storage: ?*oauth_storage.AuthStorage, mode: CatalogLoadMode) ![]ai_types.Model {
    if (builtin.is_test) return if (test_force_anthropic_models) anthropicStaticModels(allocator) else emptyModels(allocator);

    const token = (try anthropicCredential(allocator, storage)) orelse return emptyModels(allocator);
    defer secureFree(allocator, token);

    if (mode == .allow_cache) {
        if (try loadCachedAnthropicModels(allocator, anthropic_catalog_max_age_ms)) |models| return models;
    }
    if (fetchAnthropicModelsCatalog(allocator, token)) |body| {
        defer allocator.free(body);
        if (parseAnthropicModels(allocator, body)) |models| {
            if (models.len > 0) {
                saveMakaiCatalog(allocator, makai_anthropic_catalog_name, body) catch {};
                return models;
            }
            allocator.free(models);
        } else |_| {}
    } else |_| {}
    if (try loadCachedAnthropicModels(allocator, null)) |models| return models;
    return anthropicStaticModels(allocator);
}

fn catalogIsFresh(modified_ms: i64, now_ms: i64, max_age_ms: i64) bool {
    return now_ms - modified_ms <= max_age_ms;
}

test "catalogIsFresh accepts caches younger than the window and rejects older ones" {
    try std.testing.expect(catalogIsFresh(1_000, 1_000 + anthropic_catalog_max_age_ms, anthropic_catalog_max_age_ms));
    try std.testing.expect(!catalogIsFresh(1_000, 1_001 + anthropic_catalog_max_age_ms, anthropic_catalog_max_age_ms));
    try std.testing.expect(catalogIsFresh(5_000, 4_000, anthropic_catalog_max_age_ms));
    try std.testing.expect(catalogIsFresh(0, 0, 0));
    try std.testing.expect(!catalogIsFresh(0, 1, 0));
}

fn loadCachedAnthropicModels(allocator: std.mem.Allocator, max_age_ms: ?i64) !?[]ai_types.Model {
    const path = makaiCatalogPath(allocator, makai_anthropic_catalog_name) catch return null;
    defer allocator.free(path);
    if (max_age_ms) |max_age| {
        const modified = compat.fs.modifiedMillis(compat.fs.getCwd(), path) catch return null;
        if (!catalogIsFresh(modified, compat.time.nowMillis(), max_age)) return null;
    }
    const data = compat.fs.readFileAlloc(allocator, compat.fs.getCwd(), path, max_catalog_bytes) catch return null;
    defer allocator.free(data);
    const models = parseAnthropicModels(allocator, data) catch return null;
    if (models.len > 0) return models;
    allocator.free(models);
    return null;
}

fn fetchAnthropicModelsCatalog(allocator: std.mem.Allocator, token: []const u8) ![]u8 {
    const bearer = try std.fmt.allocPrint(allocator, "Bearer {s}", .{token});
    defer secureFree(allocator, bearer);

    var headers: std.ArrayList(std.http.Header) = .empty;
    defer headers.deinit(allocator);
    try headers.append(allocator, .{ .name = "accept", .value = "application/json" });
    try headers.append(allocator, .{ .name = "anthropic-version", .value = "2023-06-01" });
    if (isAnthropicOAuthToken(token)) {
        try headers.append(allocator, .{ .name = "authorization", .value = bearer });
        try headers.append(allocator, .{ .name = "anthropic-beta", .value = "oauth-2025-04-20" });
    } else {
        try headers.append(allocator, .{ .name = "x-api-key", .value = token });
    }

    const models_url = try std.fmt.allocPrint(allocator, "{s}/v1/models?limit=100", .{anthropic_base_url});
    defer allocator.free(models_url);
    var fetched = compat.http.fetch(allocator, models_url, .{
        .method = .GET,
        .extra_headers = headers.items,
        .accept_encoding = "identity",
        .max_response_bytes = max_catalog_bytes,
        .timeout_ms = catalog_fetch_timeout_ms,
    }) catch return error.ModelCatalogFetchFailed;
    errdefer fetched.deinit(allocator);

    if (fetched.status != 200) return error.ModelCatalogFetchFailed;
    return fetched.body;
}

fn fetchCustomModelsCatalog(
    allocator: std.mem.Allocator,
    provider: *const custom_providers.CustomProvider,
    token: ?[]const u8,
) ![]u8 {
    const url = try customModelsUrl(allocator, provider);
    defer allocator.free(url);
    var bearer: ?[]u8 = null;
    defer if (bearer) |value| secureFree(allocator, value);

    var headers: std.ArrayList(std.http.Header) = .empty;
    defer headers.deinit(allocator);
    try headers.append(allocator, .{ .name = "accept", .value = "application/json" });
    if (token) |value| {
        if (std.mem.eql(u8, provider.api, "anthropic-messages")) {
            try headers.append(allocator, .{ .name = "x-api-key", .value = value });
            try headers.append(allocator, .{ .name = "anthropic-version", .value = "2023-06-01" });
        } else {
            bearer = try std.fmt.allocPrint(allocator, "Bearer {s}", .{value});
            try headers.append(allocator, .{ .name = "authorization", .value = bearer.? });
        }
    }
    for (provider.headers) |header| {
        if (compat.http.headerPresent(headers.items, header.name)) continue;
        try headers.append(allocator, .{ .name = header.name, .value = header.value });
    }

    const fetched = compat.http.fetch(allocator, url, .{
        .method = .GET,
        .extra_headers = headers.items,
        .accept_encoding = "identity",
        .max_response_bytes = max_catalog_bytes,
        .timeout_ms = catalog_fetch_timeout_ms,
    }) catch return error.ModelCatalogFetchFailed;
    const body = fetched.body;
    errdefer allocator.free(body);

    if (fetched.status != 200) return error.ModelCatalogFetchFailed;
    return body;
}


























fn refreshOpenAICodexCredentials(credentials: oauth_storage.Credentials, allocator: std.mem.Allocator) !oauth_storage.Credentials {
    const refreshed = try codex_oauth.refreshToken(.{
        .refresh = credentials.refresh,
        .access = credentials.access,
        .expires = credentials.expires,
        .provider_data = credentials.provider_data,
    }, allocator);
    errdefer {
        secureFree(allocator, refreshed.refresh);
        secureFree(allocator, refreshed.access);
    }

    const provider_data = refreshed.provider_data;
    errdefer if (provider_data) |data| secureFree(allocator, data);

    return .{
        .refresh = refreshed.refresh,
        .access = refreshed.access,
        .expires = refreshed.expires,
        .provider_data = provider_data,
    };
}

fn getOpenAICodexApiKey(credentials: oauth_storage.Credentials, allocator: std.mem.Allocator) ![]const u8 {
    return try codex_oauth.getApiKey(.{
        .refresh = credentials.refresh,
        .access = credentials.access,
        .expires = credentials.expires,
    }, allocator);
}

fn codexOAuthProvider() oauth_storage.OAuthProvider {
    return .{
        .id = openai_codex_provider_id,
        .name = "OpenAI Codex",
        .refresh_fn = refreshOpenAICodexCredentials,
        .get_api_key_fn = getOpenAICodexApiKey,
    };
}

fn loadOpenAICodexModels(allocator: std.mem.Allocator, mode: CatalogLoadMode, storage_opt: ?*oauth_storage.AuthStorage) ![]ai_types.Model {
    if (builtin.is_test and mode == .force_fetch and test_force_codex_refresh_error) return error.ModelCatalogFetchFailed;
    if (builtin.is_test) return emptyModels(allocator);

    const storage = storage_opt orelse return emptyModels(allocator);
    if (!storage.providers.contains(openai_codex_provider_id)) return emptyModels(allocator);

    const account_id = try codexAccountIdFromStorage(allocator, storage);
    defer if (account_id) |id| allocator.free(id);

    if (mode == .allow_cache) {
        if (try loadCachedCodexModels(allocator, account_id)) |models| {
            if (models.len > 0) return models;
            allocator.free(models);
        }
    }

    const token = storage.getApiKey(openai_codex_provider_id, codexOAuthProvider()) catch null;
    if (token) |access_token| {
        defer secureFree(allocator, access_token);
        const client_version = try codexClientVersion(allocator);
        defer allocator.free(client_version);

        if (fetchCodexModelsCatalog(allocator, access_token, account_id, client_version)) |body| {
            defer allocator.free(body);
            saveMakaiCodexCatalog(allocator, body) catch {};
            return try parseCodexModelsCacheWithOptions(allocator, body, .{
                .account_id = account_id,
                .client_version = client_version,
            });
        } else |err| {
            if (mode == .force_fetch) {
                if (try loadCachedCodexModels(allocator, account_id)) |models| {
                    if (models.len > 0) return models;
                    allocator.free(models);
                }
                return err;
            }
        }
    }

    return emptyModels(allocator);
}

fn loadCachedCodexModels(allocator: std.mem.Allocator, account_id: ?[]const u8) !?[]ai_types.Model {
    const paths = [_]?[]u8{
        makaiCodexCatalogPath(allocator) catch null,
        codexModelsCachePath(allocator) catch null,
    };
    defer {
        for (paths) |maybe_path| {
            if (maybe_path) |path| allocator.free(path);
        }
    }

    for (paths) |maybe_path| {
        const path = maybe_path orelse continue;
        const data = compat.fs.readFileAlloc(allocator, compat.fs.getCwd(), path, max_catalog_bytes) catch continue;
        defer allocator.free(data);

        const models = parseCodexModelsCacheWithOptions(allocator, data, .{ .account_id = account_id }) catch continue;
        if (models.len > 0) return models;
        allocator.free(models);
    }

    return null;
}

fn codexHomePath(allocator: std.mem.Allocator) ![]u8 {
    if (compat.getEnvVarOwned(allocator, "CODEX_HOME")) |codex_home| {
        return codex_home;
    } else |_| {}

    const home = try compat.getEnvVarOwned(allocator, "HOME");
    defer allocator.free(home);
    return try std.fs.path.join(allocator, &.{ home, ".codex" });
}

fn codexModelsCachePath(allocator: std.mem.Allocator) ![]u8 {
    const codex_home = try codexHomePath(allocator);
    defer allocator.free(codex_home);
    return try std.fs.path.join(allocator, &.{ codex_home, codex_models_cache_name });
}

fn makaiCodexCatalogPath(allocator: std.mem.Allocator) ![]u8 {
    return makaiCatalogPath(allocator, makai_codex_catalog_name);
}

fn makaiCatalogPath(allocator: std.mem.Allocator, name: []const u8) ![]u8 {
    const home = try compat.getEnvVarOwned(allocator, "HOME");
    defer allocator.free(home);
    return try std.fs.path.join(allocator, &.{ home, ".oapx", makai_catalog_dir_name, name });
}

fn makaiCatalogDirPath(allocator: std.mem.Allocator) ![]u8 {
    const home = try compat.getEnvVarOwned(allocator, "HOME");
    defer allocator.free(home);
    return try std.fs.path.join(allocator, &.{ home, ".oapx", makai_catalog_dir_name });
}

fn saveMakaiCodexCatalog(allocator: std.mem.Allocator, data: []const u8) !void {
    return saveMakaiCatalog(allocator, makai_codex_catalog_name, data);
}

fn saveMakaiCatalog(allocator: std.mem.Allocator, name: []const u8, data: []const u8) !void {
    const dir_path = try makaiCatalogDirPath(allocator);
    defer allocator.free(dir_path);
    try compat.fs.createDir(compat.fs.getCwd(), dir_path);

    const path = try makaiCatalogPath(allocator, name);
    defer allocator.free(path);

    const tmp_path = try std.fmt.allocPrint(allocator, "{s}.tmp.{d}.{x}", .{ path, compat.time.nowMillis(), compat.random.int(u64) });
    defer allocator.free(tmp_path);

    try compat.fs.atomicReplace(compat.fs.getCwd(), path, tmp_path, data);
}

fn codexClientVersion(allocator: std.mem.Allocator) ![]u8 {
    if (try codexClientVersionFromModelsCache(allocator)) |version| return version;
    if (try codexClientVersionFromVersionFile(allocator)) |version| return version;
    return try allocator.dupe(u8, default_codex_client_version);
}

fn codexClientVersionFromModelsCache(allocator: std.mem.Allocator) !?[]u8 {
    const path = codexModelsCachePath(allocator) catch return null;
    defer allocator.free(path);
    const data = compat.fs.readFileAlloc(allocator, compat.fs.getCwd(), path, max_catalog_bytes) catch return null;
    defer allocator.free(data);
    return try parseRootStringField(allocator, data, "client_version");
}

fn codexClientVersionFromVersionFile(allocator: std.mem.Allocator) !?[]u8 {
    const codex_home = codexHomePath(allocator) catch return null;
    defer allocator.free(codex_home);
    const path = try std.fs.path.join(allocator, &.{ codex_home, "version.json" });
    defer allocator.free(path);
    const data = compat.fs.readFileAlloc(allocator, compat.fs.getCwd(), path, 16 * 1024) catch return null;
    defer allocator.free(data);
    return try parseRootStringField(allocator, data, "latest_version");
}

fn parseRootStringField(allocator: std.mem.Allocator, data: []const u8, key: []const u8) !?[]u8 {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, data, .{}) catch return null;
    defer parsed.deinit();
    if (parsed.value != .object) return null;
    const value = parsed.value.object.get(key) orelse return null;
    if (value != .string) return null;
    return try allocator.dupe(u8, value.string);
}

fn fetchCodexModelsCatalog(
    allocator: std.mem.Allocator,
    access_token: []const u8,
    account_id: ?[]const u8,
    client_version: []const u8,
) ![]u8 {
    const url = try std.fmt.allocPrint(
        allocator,
        "{s}/models?client_version={s}",
        .{ openai_codex_base_url, client_version },
    );
    defer allocator.free(url);

    const auth = try std.fmt.allocPrint(allocator, "Bearer {s}", .{access_token});
    defer secureFree(allocator, auth);

    var headers: std.ArrayList(std.http.Header) = .empty;
    defer headers.deinit(allocator);
    try headers.append(allocator, .{ .name = "accept", .value = "application/json" });
    try headers.append(allocator, .{ .name = "authorization", .value = auth });
    try headers.append(allocator, .{ .name = "version", .value = client_version });
    if (account_id) |id| {
        try headers.append(allocator, .{ .name = "ChatGPT-Account-ID", .value = id });
    }

    const fetched = compat.http.fetch(allocator, url, .{
        .method = .GET,
        .extra_headers = headers.items,
        .accept_encoding = "identity",
        .max_response_bytes = max_catalog_bytes,
        .timeout_ms = catalog_fetch_timeout_ms,
    }) catch return error.ModelCatalogFetchFailed;
    const body = fetched.body;
    errdefer allocator.free(body);

    if (fetched.status != 200) return error.ModelCatalogFetchFailed;
    return body;
}

fn codexAccountIdFromStorage(allocator: std.mem.Allocator, storage: *const oauth_storage.AuthStorage) !?[]u8 {
    const auth = storage.providers.get(openai_codex_provider_id) orelse return null;
    return switch (auth) {
        .api_key => null,
        .oauth => |credentials| blk: {
            const provider_data = credentials.provider_data orelse break :blk null;
            break :blk try parseProviderDataAccountId(allocator, provider_data);
        },
    };
}

fn parseProviderDataAccountId(allocator: std.mem.Allocator, provider_data: []const u8) !?[]u8 {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, provider_data, .{}) catch return null;
    defer parsed.deinit();
    if (parsed.value != .object) return null;
    const account_id = parsed.value.object.get("account_id") orelse return null;
    if (account_id != .string or account_id.string.len == 0) return null;
    return try allocator.dupe(u8, account_id.string);
}

fn parseCodexModelsCacheWithOptions(
    allocator: std.mem.Allocator,
    data: []const u8,
    options: CodexParseOptions,
) ![]ai_types.Model {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, data, .{});
    defer parsed.deinit();

    if (parsed.value != .object) return error.InvalidModelCatalog;
    const root = &parsed.value.object;

    const root_client_version = if (options.client_version) |version|
        version
    else if (objectString(root, "client_version")) |version|
        version
    else
        null;

    const models_value = root.get("models") orelse return error.InvalidModelCatalog;
    if (models_value != .array) return error.InvalidModelCatalog;

    var models = std.ArrayList(ai_types.Model).empty;
    errdefer {
        for (models.items) |*model| model.deinit(allocator);
        models.deinit(allocator);
    }

    for (models_value.array.items) |item| {
        if (item != .object) continue;
        const obj = &item.object;
        if (!isVisibleSupportedCodexModel(obj)) continue;

        const slug = objectString(obj, "slug") orelse continue;
        if (slug.len == 0) continue;

        const context_window = objectU32(obj, "context_window") orelse
            objectU32(obj, "max_context_window") orelse
            continue;
        const max_tokens = objectU32(obj, "max_output_tokens") orelse
            objectU32(obj, "max_tokens") orelse
            @min(context_window, default_max_output_tokens);

        const model = try codexModelFromObject(allocator, obj, slug, context_window, max_tokens, .{
            .account_id = options.account_id,
            .client_version = root_client_version,
        });
        try models.append(allocator, model);
    }

    return try models.toOwnedSlice(allocator);
}

fn isVisibleSupportedCodexModel(obj: *const std.json.ObjectMap) bool {
    if (objectString(obj, "visibility")) |visibility| {
        if (!std.mem.eql(u8, visibility, "list")) return false;
    }
    if (objectBool(obj, "supported_in_api")) |supported| {
        if (!supported) return false;
    }
    return true;
}

fn codexModelFromObject(
    allocator: std.mem.Allocator,
    obj: *const std.json.ObjectMap,
    slug: []const u8,
    context_window: u32,
    max_tokens: u32,
    options: CodexParseOptions,
) !ai_types.Model {
    const id = try allocator.dupe(u8, slug);
    errdefer allocator.free(id);
    const display = objectString(obj, "display_name") orelse slug;
    const name = try allocator.dupe(u8, display);
    errdefer allocator.free(name);
    const api = try allocator.dupe(u8, openai_codex_api_id);
    errdefer allocator.free(api);
    const provider = try allocator.dupe(u8, openai_codex_provider_id);
    errdefer allocator.free(provider);
    const base_url = try allocator.dupe(u8, openai_codex_base_url);
    errdefer allocator.free(base_url);

    const input = try parseInputModalities(allocator, obj);
    errdefer freeInput(allocator, input);

    const headers = try codexModelHeaders(allocator, options);
    errdefer if (headers) |pairs| freeHeaders(allocator, pairs);

    return .{
        .id = id,
        .name = name,
        .api = api,
        .provider = provider,
        .base_url = base_url,
        .reasoning = modelSupportsReasoning(obj),
        .input = input,
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = context_window,
        .max_tokens = max_tokens,
        .headers = headers,
        .is_owned = true,
    };
}

fn parseInputModalities(allocator: std.mem.Allocator, obj: *const std.json.ObjectMap) ![]const []const u8 {
    if (obj.get("input_modalities")) |value| {
        if (value == .array and value.array.items.len > 0) {
            var modalities = std.ArrayList([]const u8).empty;
            errdefer {
                for (modalities.items) |item| allocator.free(item);
                modalities.deinit(allocator);
            }

            for (value.array.items) |item| {
                if (item != .string or item.string.len == 0) continue;
                const modality = try allocator.dupe(u8, item.string);
                errdefer allocator.free(modality);
                try modalities.append(allocator, modality);
            }

            if (modalities.items.len > 0) return try modalities.toOwnedSlice(allocator);
            modalities.deinit(allocator);
        }
    }

    const fallback = try allocator.alloc([]const u8, 1);
    errdefer allocator.free(fallback);
    fallback[0] = try allocator.dupe(u8, "text");
    return fallback;
}

fn codexModelHeaders(allocator: std.mem.Allocator, options: CodexParseOptions) !?[]const ai_types.HeaderPair {
    const count: usize = (if (options.client_version) |_| @as(usize, 1) else 0) +
        (if (options.account_id) |_| @as(usize, 1) else 0);
    if (count == 0) return null;

    var headers = try allocator.alloc(ai_types.HeaderPair, count);
    errdefer allocator.free(headers);

    var idx: usize = 0;
    if (options.client_version) |version| {
        const name = try allocator.dupe(u8, "version");
        errdefer allocator.free(name);
        const value = try allocator.dupe(u8, version);
        errdefer allocator.free(value);
        headers[idx] = .{ .name = name, .value = value };
        idx += 1;
    }
    errdefer for (headers[0..idx]) |*header| header.deinit(allocator);

    if (options.account_id) |account_id| {
        const name = try allocator.dupe(u8, "ChatGPT-Account-ID");
        errdefer allocator.free(name);
        const value = try allocator.dupe(u8, account_id);
        errdefer allocator.free(value);
        headers[idx] = .{ .name = name, .value = value };
        idx += 1;
    }

    return headers;
}

fn modelSupportsReasoning(obj: *const std.json.ObjectMap) bool {
    if (obj.get("supported_reasoning_levels")) |value| {
        return value == .array and value.array.items.len > 0;
    }
    if (objectString(obj, "default_reasoning_level")) |level| {
        return level.len > 0 and !std.mem.eql(u8, level, "off");
    }
    return false;
}

fn freeInput(allocator: std.mem.Allocator, input: []const []const u8) void {
    for (input) |item| allocator.free(item);
    allocator.free(input);
}

fn freeHeaders(allocator: std.mem.Allocator, headers: []const ai_types.HeaderPair) void {
    for (headers) |header| {
        allocator.free(header.name);
        allocator.free(header.value);
    }
    allocator.free(headers);
}

fn objectString(obj: *const std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = obj.get(key) orelse return null;
    if (value != .string) return null;
    return value.string;
}

fn objectBool(obj: *const std.json.ObjectMap, key: []const u8) ?bool {
    const value = obj.get(key) orelse return null;
    if (value != .bool) return null;
    return value.bool;
}

fn objectU32(obj: *const std.json.ObjectMap, key: []const u8) ?u32 {
    const value = obj.get(key) orelse return null;
    return switch (value) {
        .integer => |v| {
            if (v < 0 or v > std.math.maxInt(u32)) return null;
            return @intCast(v);
        },
        .float => |v| {
            if (!std.math.isFinite(v)) return null;
            if (v < 0 or v > @as(f64, @floatFromInt(std.math.maxInt(u32)))) return null;
            return @intFromFloat(v);
        },
        else => return null,
    };
}

test "parseAnthropicModels maps the models endpoint into owned Anthropic models" {
    const body =
        \\{"data":[{"type":"model","id":"claude-sonnet-4-5-20250929","display_name":"Claude Sonnet 4.5","created_at":"2025-09-29T00:00:00Z"},{"type":"model","id":"claude-opus-4-1-20250805","display_name":"Claude Opus 4.1"},{"type":"model","id":"claude-future-9","display_name":"Claude Future"},{"type":"model","id":"not-a-claude"}],"has_more":false}
    ;
    const models = try parseAnthropicModels(std.testing.allocator, body);
    defer deinitModels(std.testing.allocator, models);
    try std.testing.expectEqual(@as(usize, 3), models.len);
    try std.testing.expectEqualStrings("claude-sonnet-4-5-20250929", models[0].id);
    try std.testing.expectEqualStrings("Claude Sonnet 4.5", models[0].name);
    try std.testing.expectEqualStrings(anthropic_provider_id, models[0].provider);
    try std.testing.expectEqualStrings(anthropic_api_name, models[0].api);
    try std.testing.expectEqualStrings("https://api.anthropic.com", models[0].base_url);
    try std.testing.expectEqual(@as(f64, 3.0), models[0].cost.input);
    try std.testing.expectEqual(@as(u32, 64_000), models[0].max_tokens);
    try std.testing.expectEqual(@as(f64, 15.0), models[1].cost.input);
    try std.testing.expectEqual(@as(u32, 32_000), models[1].max_tokens);
    try std.testing.expectEqual(@as(f64, 0), models[2].cost.input);
    try std.testing.expect(models[2].reasoning);
}

const custom_gateway_config =
    \\{"providers":[{"id":"gateway","name":"Gateway","api":"anthropic-messages",
    \\ "base_url":"https://gw.test/anthropic/v1",
    \\ "headers":{"X-Tenant":"acme"},
    \\ "reasoning":true,
    \\ "models":[{"id":"claude-x","name":"Claude X","context_window":250000,"max_tokens":40000},"claude-y"],
    \\ "capabilities":{"cache_ttl":true}}]}
;

const custom_two_provider_config =
    \\{"providers":[
    \\ {"id":"aaa","base_url":"https://aaa.test","models":["keep-a"]},
    \\ {"id":"zzz","base_url":"https://zzz.test","models":["keep-z"]}
    \\]}
;

test "discovery result is filtered per provider and never falls back to the declared list" {
    test_custom_providers_config = custom_two_provider_config;
    test_custom_discovery_ids = &[_][]const u8{ "keep-a", "keep-z", "noisy" };
    defer {
        test_custom_providers_config = null;
        test_custom_discovery_ids = null;
    }

    const models = try loadCustomModels(std.testing.allocator, null, .allow_cache);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 2), models.len);
    try std.testing.expectEqualStrings("keep-a", models[0].id);
    try std.testing.expectEqualStrings("aaa", models[0].provider);
    try std.testing.expectEqualStrings("keep-z", models[1].id);
    try std.testing.expectEqualStrings("zzz", models[1].provider);
}

test "auth none reaches the model as allows_anonymous" {
    test_custom_providers_config =
        \\{"providers":[
        \\ {"id":"local","base_url":"http://localhost:8000/v1","models":["m"],"auth":"none"},
        \\ {"id":"gw","base_url":"https://gw.test","models":["m"],"auth":{"env":"GW_KEY"}}
        \\]}
    ;
    defer test_custom_providers_config = null;

    const models = try loadCustomModels(std.testing.allocator, null, .allow_cache);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 2), models.len);
    try std.testing.expectEqualStrings("local", models[0].provider);
    try std.testing.expect(models[0].allows_anonymous);
    try std.testing.expectEqualStrings("gw", models[1].provider);
    try std.testing.expect(!models[1].allows_anonymous);
}

test "github copilot models come from the persisted login list" {
    test_force_copilot_models = true;
    defer test_force_copilot_models = false;

    var storage = oauth_storage.AuthStorage{
        .providers = std.StringHashMap(oauth_storage.ProviderAuth).init(std.testing.allocator),
        .allocator = std.testing.allocator,
    };
    defer storage.deinit();
    try storage.providers.put(
        try std.testing.allocator.dupe(u8, github_copilot_provider_id),
        .{ .oauth = .{
            .refresh = try std.testing.allocator.dupe(u8, "gho"),
            .access = try std.testing.allocator.dupe(u8, "tok"),
            .expires = compat.time.nowMillis() + 3_600_000,
            .provider_data = try std.testing.allocator.dupe(u8,
                \\{"baseUrl":"https://api.acme.githubcopilot.com","models":["gpt-5","claude-opus-4.5"]}
            ),
        } },
    );

    const models = try loadGitHubCopilotModels(std.testing.allocator, &storage);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 2), models.len);
    try std.testing.expectEqualStrings("gpt-5", models[0].id);
    try std.testing.expectEqualStrings(github_copilot_provider_id, models[0].provider);
    try std.testing.expectEqualStrings(github_copilot_api_name, models[0].api);
    try std.testing.expectEqualStrings("https://api.acme.githubcopilot.com", models[0].base_url);
    try std.testing.expectEqualStrings("claude-opus-4.5", models[1].id);
}

test "github copilot falls back to the known list and the default base url" {
    test_force_copilot_models = true;
    defer test_force_copilot_models = false;

    var storage = oauth_storage.AuthStorage{
        .providers = std.StringHashMap(oauth_storage.ProviderAuth).init(std.testing.allocator),
        .allocator = std.testing.allocator,
    };
    defer storage.deinit();
    try storage.providers.put(
        try std.testing.allocator.dupe(u8, github_copilot_provider_id),
        .{ .oauth = .{
            .refresh = try std.testing.allocator.dupe(u8, "gho"),
            .access = try std.testing.allocator.dupe(u8, "tok"),
            .expires = compat.time.nowMillis() + 3_600_000,
        } },
    );

    const models = try loadGitHubCopilotModels(std.testing.allocator, &storage);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(github_copilot.KNOWN_COPILOT_MODELS.len, models.len);
    try std.testing.expectEqualStrings(github_copilot.DEFAULT_BASE_URL, models[0].base_url);
}

test "an enterprise login with no stored base url contributes nothing" {
    test_force_copilot_models = true;
    defer test_force_copilot_models = false;

    var storage = oauth_storage.AuthStorage{
        .providers = std.StringHashMap(oauth_storage.ProviderAuth).init(std.testing.allocator),
        .allocator = std.testing.allocator,
    };
    defer storage.deinit();
    try storage.providers.put(
        try std.testing.allocator.dupe(u8, github_copilot_provider_id),
        .{ .oauth = .{
            .refresh = try std.testing.allocator.dupe(u8, "gho"),
            .access = try std.testing.allocator.dupe(u8, "tok"),
            .expires = compat.time.nowMillis() + 3_600_000,
            .provider_data = try std.testing.allocator.dupe(u8,
                \\{"enterpriseUrl":"https://gh.acme.com"}
            ),
        } },
    );

    const models = try loadGitHubCopilotModels(std.testing.allocator, &storage);
    defer deinitModels(std.testing.allocator, models);
    try std.testing.expectEqual(@as(usize, 0), models.len);
}

test "an enterprise login that stored its base url still lists models there" {
    test_force_copilot_models = true;
    defer test_force_copilot_models = false;

    var storage = oauth_storage.AuthStorage{
        .providers = std.StringHashMap(oauth_storage.ProviderAuth).init(std.testing.allocator),
        .allocator = std.testing.allocator,
    };
    defer storage.deinit();
    try storage.providers.put(
        try std.testing.allocator.dupe(u8, github_copilot_provider_id),
        .{ .oauth = .{
            .refresh = try std.testing.allocator.dupe(u8, "gho"),
            .access = try std.testing.allocator.dupe(u8, "tok"),
            .expires = compat.time.nowMillis() + 3_600_000,
            .provider_data = try std.testing.allocator.dupe(u8,
                \\{"enterpriseUrl":"https://gh.acme.com","baseUrl":"https://api.acme.githubcopilot.com","models":["gpt-5"]}
            ),
        } },
    );

    const models = try loadGitHubCopilotModels(std.testing.allocator, &storage);
    defer deinitModels(std.testing.allocator, models);
    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings("https://api.acme.githubcopilot.com", models[0].base_url);
}

test "github copilot contributes nothing when it is not logged in" {
    test_force_copilot_models = true;
    defer test_force_copilot_models = false;

    var storage = oauth_storage.AuthStorage{
        .providers = std.StringHashMap(oauth_storage.ProviderAuth).init(std.testing.allocator),
        .allocator = std.testing.allocator,
    };
    defer storage.deinit();

    const models = try loadGitHubCopilotModels(std.testing.allocator, &storage);
    defer deinitModels(std.testing.allocator, models);
    try std.testing.expectEqual(@as(usize, 0), models.len);

    const none = try loadGitHubCopilotModels(std.testing.allocator, null);
    defer deinitModels(std.testing.allocator, none);
    try std.testing.expectEqual(@as(usize, 0), none.len);
}

test "a provider whose discovery is fully filtered contributes nothing regardless of file order" {
    const orders = [_][]const u8{
        \\{"providers":[
        \\ {"id":"empty","base_url":"https://empty.test","models":["absent"]},
        \\ {"id":"full","base_url":"https://full.test","models":["present"]}
        \\]}
        ,
        \\{"providers":[
        \\ {"id":"full","base_url":"https://full.test","models":["present"]},
        \\ {"id":"empty","base_url":"https://empty.test","models":["absent"]}
        \\]}
        ,
    };
    test_custom_discovery_ids = &[_][]const u8{"present"};
    defer test_custom_discovery_ids = null;

    for (orders) |config| {
        test_custom_providers_config = config;
        defer test_custom_providers_config = null;

        const models = try loadCustomModels(std.testing.allocator, null, .allow_cache);
        defer deinitModels(std.testing.allocator, models);

        try std.testing.expectEqual(@as(usize, 1), models.len);
        try std.testing.expectEqualStrings("present", models[0].id);
        try std.testing.expectEqualStrings("full", models[0].provider);
    }
}

test "loadProductionModels includes models from a declared custom provider" {
    test_custom_providers_config = custom_gateway_config;
    defer test_custom_providers_config = null;

    const models = try loadProductionModels(std.testing.allocator);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 2), models.len);

    const first = models[0];
    try std.testing.expectEqualStrings("claude-x", first.id);
    try std.testing.expectEqualStrings("Claude X", first.name);
    try std.testing.expectEqualStrings("gateway", first.provider);
    try std.testing.expectEqualStrings("anthropic-messages", first.api);
    try std.testing.expectEqualStrings("https://gw.test/anthropic", first.base_url);
    try std.testing.expectEqual(@as(u32, 250000), first.context_window);
    try std.testing.expectEqual(@as(u32, 40000), first.max_tokens);
    try std.testing.expect(first.reasoning);
    try std.testing.expectEqual(@as(?bool, true), first.compat.?.supports_anthropic_cache_ttl);
    try std.testing.expectEqual(@as(usize, 1), first.headers.?.len);
    try std.testing.expectEqualStrings("X-Tenant", first.headers.?[0].name);
    try std.testing.expectEqualStrings("acme", first.headers.?[0].value);

    const second = models[1];
    try std.testing.expectEqualStrings("claude-y", second.id);
    try std.testing.expectEqualStrings("claude-y", second.name);
    try std.testing.expectEqual(@as(u32, 128_000), second.context_window);
    try std.testing.expectEqual(@as(u32, 8_192), second.max_tokens);
}

fn customCatalogProbe(allocator: std.mem.Allocator) !void {
    test_custom_providers_config = custom_gateway_config;
    defer test_custom_providers_config = null;
    const models = try loadCustomModels(allocator, null, .allow_cache);
    defer deinitModels(allocator, models);
    try std.testing.expectEqual(@as(usize, 2), models.len);
}

test "custom catalog models free every allocation when one fails midway" {
    try customCatalogProbe(std.testing.allocator);
    try std.testing.checkAllAllocationFailures(std.testing.allocator, customCatalogProbe, .{});
}

test "loadProductionModels includes the Anthropic static list when forced" {
    test_force_anthropic_models = true;
    defer test_force_anthropic_models = false;
    const models = try loadProductionModels(std.testing.allocator);
    defer deinitModels(std.testing.allocator, models);
    try std.testing.expectEqual(anthropic_static_models.len, models.len);
    for (models) |model| try std.testing.expectEqualStrings(anthropic_provider_id, model.provider);
    try std.testing.expectEqualStrings("claude-fable-5-1", models[0].id);
}

test "refreshProductionModels keeps Anthropic models when Codex refresh fails" {
    test_force_anthropic_models = true;
    test_force_codex_refresh_error = true;
    defer {
        test_force_anthropic_models = false;
        test_force_codex_refresh_error = false;
    }
    const models = try refreshProductionModels(std.testing.allocator);
    defer deinitModels(std.testing.allocator, models);
    try std.testing.expectEqual(anthropic_static_models.len, models.len);
}

test "parseCodexModelsCache maps visible supported Codex models" {
    const data =
        \\{
        \\  "client_version": "0.135.0",
        \\  "models": [
        \\    {
        \\      "slug": "gpt-test-codex",
        \\      "display_name": "GPT Test Codex",
        \\      "visibility": "list",
        \\      "supported_in_api": true,
        \\      "context_window": 272000,
        \\      "input_modalities": ["text", "image"],
        \\      "supported_reasoning_levels": [{"effort": "low"}]
        \\    },
        \\    {
        \\      "slug": "hidden-model",
        \\      "visibility": "hidden",
        \\      "supported_in_api": true,
        \\      "context_window": 128000
        \\    },
        \\    {
        \\      "slug": "unsupported-model",
        \\      "visibility": "list",
        \\      "supported_in_api": false,
        \\      "context_window": 128000
        \\    }
        \\  ]
        \\}
    ;

    const models = try parseCodexModelsCacheWithOptions(std.testing.allocator, data, .{});
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings("gpt-test-codex", models[0].id);
    try std.testing.expectEqualStrings("GPT Test Codex", models[0].name);
    try std.testing.expectEqualStrings(openai_codex_provider_id, models[0].provider);
    try std.testing.expectEqualStrings(openai_codex_api_id, models[0].api);
    try std.testing.expectEqualStrings(openai_codex_base_url, models[0].base_url);
    try std.testing.expect(models[0].reasoning);
    try std.testing.expectEqual(@as(u32, 272000), models[0].context_window);
    try std.testing.expectEqual(@as(u32, default_max_output_tokens), models[0].max_tokens);
    try std.testing.expectEqual(@as(usize, 2), models[0].input.len);
    try std.testing.expectEqualStrings("text", models[0].input[0]);
    try std.testing.expectEqualStrings("image", models[0].input[1]);
    try std.testing.expect(models[0].headers != null);
    try std.testing.expectEqualStrings("version", models[0].headers.?[0].name);
    try std.testing.expectEqualStrings("0.135.0", models[0].headers.?[0].value);
}

test "loadProductionModels includes the Kimi model, discovered like any other row" {
    try compat.setTestEnv(std.testing.allocator, kimi_region_env, "");
    defer compat.clearTestEnv();
    const target = catalogTargetInRegion("kimi", "china") orelse return error.TestExpectedTarget;
    test_catalog_discovery = &[_]CatalogDiscovery{.{
        .id = "kimi",
        .models_url = target.models_url,
        .model_ids = &.{kimi_model_id},
    }};
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = kimi_env_key, .value = "kimi-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadProductionModels(std.testing.allocator);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings(kimi_model_id, models[0].id);
    try std.testing.expectEqualStrings(kimi_provider_id, models[0].provider);
    try std.testing.expectEqualStrings(kimi_api_id, models[0].api);
    try std.testing.expectEqualStrings(kimi_base_url, models[0].base_url);
    try std.testing.expectEqualStrings("Kimi K2.7 Code", models[0].name);
    try std.testing.expectEqual(@as(u32, 262_144), models[0].context_window);
    try std.testing.expectEqual(@as(u32, 16_384), models[0].max_tokens);
}

test "the Kimi row serves the China base by default and the global base when the region says so" {
    try compat.setTestEnv(std.testing.allocator, kimi_region_env, "");
    defer compat.clearTestEnv();
    const china = catalogRegion(std.testing.allocator, null, "kimi");
    try std.testing.expectEqualStrings("china", china.?);
    const china_target = catalogTargetInRegion("kimi", china) orelse return error.TestExpectedTarget;
    try std.testing.expectEqualStrings("https://api.kimi.com/coding", china_target.base_url);
    try std.testing.expectEqualStrings("https://api.kimi.com/coding/v1/models", china_target.models_url);

    const global_target = catalogTargetInRegion("kimi", "global") orelse return error.TestExpectedTarget;
    try std.testing.expectEqualStrings("https://api.moonshot.ai", global_target.base_url);
    try std.testing.expectEqualStrings("https://api.moonshot.ai/v1/models", global_target.models_url);
}

test "KIMI_REGION chooses the region, and an unusable value falls back to the row's default" {
    const cases = [_]struct { set: []const u8, want: []const u8 }{
        .{ .set = "global", .want = "global" },
        .{ .set = " global", .want = "global" },
        .{ .set = "global ", .want = "global" },
        .{ .set = "\tglobal\r\n", .want = "global" },
        .{ .set = "GLOBAL", .want = "global" },
        .{ .set = "moonshot", .want = "global" },
        .{ .set = "china", .want = "china" },
        .{ .set = "cn", .want = "china" },
        .{ .set = "coding", .want = "china" },
        .{ .set = "mars", .want = "china" },
        .{ .set = "", .want = "china" },
    };
    for (cases) |case| {
        try compat.setTestEnv(std.testing.allocator, kimi_region_env, case.set);
        const got = catalogRegion(std.testing.allocator, null, "kimi");
        if (!std.mem.eql(u8, case.want, got orelse "")) {
            std.debug.print("\nKIMI_REGION={s} should resolve to {s}\n", .{ case.set, case.want });
        }
        try std.testing.expectEqualStrings(case.want, got.?);
    }
    compat.clearTestEnv();

    try compat.setTestEnv(std.testing.allocator, "KIMI_REGION", "global");
    defer compat.clearTestEnv();
    const chosen = catalogRegion(std.testing.allocator, null, "kimi");
    try std.testing.expectEqualStrings("global", chosen.?);
    const target = catalogTargetInRegion("kimi", chosen) orelse return error.TestExpectedTarget;
    try std.testing.expectEqualStrings("https://api.moonshot.ai", target.base_url);
}

test "loadProductionModels omits Kimi model by default in tests" {
    const models = try loadProductionModels(std.testing.allocator);
    defer deinitModels(std.testing.allocator, models);

    for (models) |model| {
        try std.testing.expect(!std.mem.eql(u8, kimi_provider_id, model.provider));
    }
}

test "the region resolution a user chose at login reaches discovery and the model's base" {
    const allocator = std.testing.allocator;
    var storage = oauth_storage.AuthStorage{
        .providers = std.StringHashMap(oauth_storage.ProviderAuth).init(allocator),
        .allocator = allocator,
    };
    defer storage.deinit();
    try storage.providers.put(try allocator.dupe(u8, kimi_provider_id), .{ .oauth = .{
        .access = try allocator.dupe(u8, "sk-kimi"),
        .refresh = try allocator.dupe(u8, ""),
        .expires = std.math.maxInt(i64),
        .provider_data = try allocator.dupe(u8, "region:global"),
    } });

    const region = catalogRegion(allocator, &storage, "kimi");
    try std.testing.expectEqualStrings("global", region.?);
    const target = catalogTargetInRegion("kimi", region) orelse return error.TestExpectedTarget;
    try std.testing.expectEqualStrings("https://api.moonshot.ai", target.base_url);

    const maybe_endpoint = try catalogEndpointFromEnvironment(allocator, &storage, "kimi");
    var held_endpoint = maybe_endpoint;
    defer if (held_endpoint) |*held| held.deinit(allocator);
    const endpoint = held_endpoint;
    try std.testing.expectEqualStrings("https://api.moonshot.ai", endpoint.?.base_url);
    try std.testing.expectEqualStrings("https://api.moonshot.ai/v1/models", endpoint.?.models_url);
}

test "an environment region still wins over the one chosen at login" {
    const allocator = std.testing.allocator;
    var storage = oauth_storage.AuthStorage{
        .providers = std.StringHashMap(oauth_storage.ProviderAuth).init(allocator),
        .allocator = allocator,
    };
    defer storage.deinit();
    try storage.providers.put(try allocator.dupe(u8, kimi_provider_id), .{ .oauth = .{
        .access = try allocator.dupe(u8, "sk-kimi"),
        .refresh = try allocator.dupe(u8, ""),
        .expires = std.math.maxInt(i64),
        .provider_data = try allocator.dupe(u8, "region:global"),
    } });
    try compat.setTestEnv(allocator, kimi_region_env, "china");
    defer compat.clearTestEnv();

    const region = catalogRegion(allocator, &storage, "kimi");
    try std.testing.expectEqualStrings("china", region.?);
}

test "a stored OAuth credential's region picks the row's endpoint, and an unusable one does not" {
    const allocator = std.testing.allocator;
    const cases = [_]struct { provider_data: ?[]const u8, want: ?[]const u8, label: []const u8 }{
        .{ .provider_data = "region:global", .want = "global", .label = "the stored region" },
        .{ .provider_data = "region:moonshot", .want = "global", .label = "a synonym, read the same way the variable is" },
        .{ .provider_data = "region: global ", .want = "global", .label = "a padded value" },
        .{ .provider_data = "region:mars", .want = null, .label = "a region no endpoint has" },
        .{ .provider_data = null, .want = null, .label = "no region stored" },
    };
    for (cases) |case| {
        var storage = oauth_storage.AuthStorage{
            .providers = std.StringHashMap(oauth_storage.ProviderAuth).init(allocator),
            .allocator = allocator,
        };
        defer storage.deinit();
        const creds: oauth_storage.ProviderAuth = if (case.provider_data) |data| .{ .oauth = .{
            .access = try allocator.dupe(u8, "access"),
            .refresh = try allocator.dupe(u8, "refresh"),
            .expires = 0,
            .provider_data = try allocator.dupe(u8, data),
        } } else .{ .api_key = try allocator.dupe(u8, "key") };
        try storage.providers.put(try allocator.dupe(u8, kimi_provider_id), creds);

        const got = catalogStoredRegion(kimi_provider_id, &storage);
        if (!std.mem.eql(u8, case.want orelse "", got orelse "")) {
            std.debug.print("\n{s} should resolve to {s}\n", .{ case.label, case.want orelse "nothing" });
        }
        try std.testing.expectEqualStrings(case.want orelse "", got orelse "");
    }

    try std.testing.expect(catalogStoredRegion(kimi_provider_id, null) == null);
    try std.testing.expect(catalogStoredRegion("anthropic", null) == null);
}





test "refreshProductionModels keeps Kimi when Codex refresh fails" {
    try compat.setTestEnv(std.testing.allocator, kimi_region_env, "");
    defer compat.clearTestEnv();
    const target = catalogTargetInRegion("kimi", "china") orelse return error.TestExpectedTarget;
    test_catalog_discovery = &[_]CatalogDiscovery{.{
        .id = "kimi",
        .models_url = target.models_url,
        .model_ids = &.{kimi_model_id},
    }};
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = kimi_env_key, .value = "kimi-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }
    test_force_codex_refresh_error = true;
    defer test_force_codex_refresh_error = false;

    const models = try refreshProductionModels(std.testing.allocator);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings(kimi_model_id, models[0].id);
    try std.testing.expectEqualStrings(kimi_provider_id, models[0].provider);
}

test "parseCodexModelsCache accepts models response body" {
    const data =
        \\{"models":[{"slug":"gpt-api","visibility":"list","supported_in_api":true,"max_context_window":128000}]}
    ;

    const models = try parseCodexModelsCacheWithOptions(std.testing.allocator, data, .{});
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings("gpt-api", models[0].id);
    try std.testing.expectEqual(@as(u32, 128000), models[0].context_window);
    try std.testing.expectEqual(@as(u32, default_max_output_tokens), models[0].max_tokens);
    try std.testing.expect(models[0].headers == null);
}

test "parseCodexModelsCache rejects oversized floating catalog sizes" {
    const oversized_context =
        \\{"models":[{"slug":"bad-context","visibility":"list","supported_in_api":true,"context_window":1e40}]}
    ;

    const no_models = try parseCodexModelsCacheWithOptions(std.testing.allocator, oversized_context, .{});
    defer deinitModels(std.testing.allocator, no_models);
    try std.testing.expectEqual(@as(usize, 0), no_models.len);

    const oversized_max_tokens =
        \\{"models":[{"slug":"valid-context","visibility":"list","supported_in_api":true,"context_window":128000,"max_tokens":1e40}]}
    ;

    const models = try parseCodexModelsCacheWithOptions(std.testing.allocator, oversized_max_tokens, .{});
    defer deinitModels(std.testing.allocator, models);
    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqual(@as(u32, default_max_output_tokens), models[0].max_tokens);
}

test "the loader builds models on a wire a built-in module claims, and no other" {
    const claimed = [_][]const u8{
        "openai-completions",
        "anthropic-messages",
        "openai-responses",
        "openai-codex-responses",
        "ollama",
    };
    for (claimed) |wire| try std.testing.expect(wireIsImplemented(wire));

    const unclaimed = [_][]const u8{ "google-generative-ai", "azure-openai-responses", "no-such-wire" };
    for (unclaimed) |wire| try std.testing.expect(!wireIsImplemented(wire));

    for (provider_catalog.wire_paths) |wire| {
        for (claimed) |named| {
            if (!std.mem.eql(u8, named, wire.id)) continue;
            try std.testing.expectEqualStrings(wire.suffix, provider_catalog.wirePath(named).?.suffix);
        }
    }
}

test "a catalog target names the row's own wire, base and models url" {
    const target = catalogTarget("deepseek").?;
    try std.testing.expectEqualStrings("deepseek", target.id);
    try std.testing.expectEqualStrings("openai-completions", target.wire);
    try std.testing.expectEqualStrings("https://api.deepseek.com", target.base_url);
    try std.testing.expectEqualStrings("https://api.deepseek.com/v1/models", target.models_url);
}

fn countVersions(url: []const u8) usize {
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
    return versions;
}

test "a carries-version row's listing and its request agree under an override" {
    const rows = [_][]const u8{
        "opencode",          "openrouter",              "vercel",            "zenmux",           "deepinfra",
        "zai-coding-plan",   "alibaba-coding-plan",     "minimax-coding-plan", "tencent-coding-plan", "volcengine-coding-plan",
    };
    for (rows) |id| {
        const target = catalogTarget(id) orelse return error.TestExpectedTarget;
        try std.testing.expect(provider_catalog.endpointCarriesVersion(id, target.base_url));

        const proxy = try std.testing.allocator.dupe(u8, "https://proxy.example");
        var endpoint = try catalogEndpointWithBase(std.testing.allocator, target, proxy);
        defer endpoint.deinit(std.testing.allocator);
        try std.testing.expectEqualStrings("https://proxy.example", endpoint.base_url);

        const request = try provider_catalog.joinUrlOwned(
            std.testing.allocator,
            endpoint.base_url,
            provider_catalog.wirePath(target.wire).?,
            false,
        );
        defer std.testing.allocator.free(request);
        try std.testing.expect(countVersions(endpoint.models_url) == countVersions(request));
        try std.testing.expect(countVersions(request) == 1);
    }
}

test "a custom entry's discovery url follows its stated version fact" {
    const bare =
        \\{"providers":[{"id":"gw","base_url":"https://gw.test"}]}
    ;
    const versioned =
        \\{"providers":[{"id":"gw","base_url":"https://gw.test/api/v1"}]}
    ;
    const stated_true =
        \\{"providers":[{"id":"gw","base_url":"https://gw.test/api/v1","carries_version":true}]}
    ;
    const stated_false =
        \\{"providers":[{"id":"gw","base_url":"https://gw.test/api/v1","carries_version":false}]}
    ;
    const non_trailing =
        \\{"providers":[{"id":"gw","base_url":"https://gw.test/api/coding/paas/v4","carries_version":true}]}
    ;
    const cases = [_]struct { config: []const u8, want: []const u8 }{
        .{ .config = bare, .want = "https://gw.test/v1/models" },
        .{ .config = versioned, .want = "https://gw.test/api/v1/models" },
        .{ .config = stated_true, .want = "https://gw.test/api/v1/models" },
        .{ .config = stated_false, .want = "https://gw.test/api/v1/models" },
        .{ .config = non_trailing, .want = "https://gw.test/api/coding/paas/v4/models" },
    };
    for (cases) |case| {
        var providers = try custom_providers.parse(std.testing.allocator, case.config);
        defer custom_providers.deinitProviders(std.testing.allocator, providers);
        const url = try customModelsUrl(std.testing.allocator, &providers[0]);
        defer std.testing.allocator.free(url);
        try std.testing.expectEqualStrings(case.want, url);
    }
}

test "a catalog target keeps the catalog base when no override is set" {
    var endpoint = (try catalogEndpointWithOverrides(std.testing.allocator, "deepseek", .{})).?;
    defer endpoint.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("https://api.deepseek.com", endpoint.base_url);
    try std.testing.expectEqualStrings("https://api.deepseek.com/v1/models", endpoint.models_url);
}

test "a row the override machinery does not know falls back to the catalog base" {
    var endpoint = (try catalogEndpointWithOverrides(std.testing.allocator, "xiaomi-token-plan-cn", .{})).?;
    defer endpoint.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings(
        catalogTarget("xiaomi-token-plan-cn").?.base_url,
        endpoint.base_url,
    );
    try std.testing.expectEqualStrings(
        catalogTarget("xiaomi-token-plan-cn").?.models_url,
        endpoint.models_url,
    );
}

test "the row's base-url override moves both the base and the models url" {
    var endpoint = (try catalogEndpointWithOverrides(std.testing.allocator, "deepseek", .{
        .deepseek = "https://proxy.example/api",
    })).?;
    defer endpoint.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("https://proxy.example/api", endpoint.base_url);
    try std.testing.expectEqualStrings("https://proxy.example/api/v1/models", endpoint.models_url);
}

test "a versioned base-url override loses the trailing version the wire would re-add" {
    var endpoint = (try catalogEndpointWithOverrides(std.testing.allocator, "deepseek", .{
        .deepseek = "https://proxy.example/api/v1/",
    })).?;
    defer endpoint.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("https://proxy.example/api", endpoint.base_url);
    try std.testing.expectEqualStrings("https://proxy.example/api/v1/models", endpoint.models_url);
}

test "the global base-url override outranks the row's own" {
    var endpoint = (try catalogEndpointWithOverrides(std.testing.allocator, "deepseek", .{
        .global = "https://everywhere.example",
        .deepseek = "https://proxy.example/api",
    })).?;
    defer endpoint.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("https://everywhere.example", endpoint.base_url);
    try std.testing.expectEqualStrings("https://everywhere.example/v1/models", endpoint.models_url);
}

test "a discovered model carries the overridden base rather than the catalog's" {
    test_catalog_discovery = &[_]CatalogDiscovery{
        .{ .id = "deepseek", .models_url = proxy_models_url, .model_ids = &.{"deepseek-chat"} },
    };
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "DEEPSEEK_API_KEY", .value = "row-key" },
    };
    test_catalog_base_urls = .{ .deepseek = "https://proxy.example/api" };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
        test_catalog_base_urls = null;
    }

    const models = try loadCatalogModels(std.testing.allocator, null, .allow_cache);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings("https://proxy.example/api", models[0].base_url);
    try std.testing.expectEqualStrings("deepseek", models[0].provider);
}

test "a row's discovery is read from its overridden models url, not the catalog's" {
    test_catalog_discovery = &[_]CatalogDiscovery{
        .{ .id = "deepseek", .models_url = proxy_models_url, .model_ids = &.{"deepseek-chat"} },
    };
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "DEEPSEEK_API_KEY", .value = "row-key" },
    };
    test_catalog_base_urls = .{ .deepseek = "https://proxy.example/api" };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
        test_catalog_base_urls = null;
    }

    const models = try loadCatalogModels(std.testing.allocator, null, .allow_cache);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings("deepseek-chat", models[0].id);
    try std.testing.expectEqualStrings("https://proxy.example/api", models[0].base_url);
}

test "a row whose override leaves the catalog models url reads that url's listing" {
    test_catalog_discovery = &[_]CatalogDiscovery{
        .{ .id = "xiaomi-token-plan-cn", .models_url = xiaomi_catalog_models_url, .model_ids = &.{"mimo-1"} },
    };
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "XIAOMI_API_KEY", .value = "row-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadCatalogModelsWithRows(std.testing.allocator, &.{"xiaomi-token-plan-cn"}, null, .allow_cache);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings("mimo-1", models[0].id);
    try std.testing.expectEqualStrings("https://token-plan-cn.xiaomimimo.com/v1", models[0].base_url);
}

test "a discovery registered for one models url answers for no other" {
    test_catalog_discovery = &[_]CatalogDiscovery{
        .{ .id = "deepseek", .models_url = proxy_models_url, .model_ids = &.{"deepseek-chat"} },
    };
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "DEEPSEEK_API_KEY", .value = "row-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadCatalogModels(std.testing.allocator, null, .allow_cache);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 0), models.len);
}

test "a row with no implemented wire, endpoint or models listing has no target" {
    try std.testing.expect(catalogTarget("openai-codex") == null);
    try std.testing.expect(catalogTarget("github-copilot") == null);
    try std.testing.expect(catalogTarget("azure") == null);
    try std.testing.expect(catalogTarget("ollama") == null);
    try std.testing.expect(catalogTarget("kimi") == null);
    try std.testing.expect(catalogTarget("no-such-provider") == null);
}

test "every gateway row the loader enables has its own target, wire and version fact" {
    const gateways = [_][]const u8{ "openrouter", "opencode", "vercel", "zenmux", "deepinfra" };
    for (gateways) |id| {
        const target = catalogTarget(id) orelse return error.TestExpectedTarget;
        try std.testing.expectEqualStrings(id, target.id);
        try std.testing.expectEqualStrings("openai-completions", target.wire);
        try std.testing.expect(provider_catalog.endpointCarriesVersion(id, target.base_url));
        try std.testing.expectEqualStrings("openai-completions", provider_catalog.wirePath(target.wire).?.id);
    }
}

test "a gateway row's discovered models carry that row's base and its own listing url" {
    const cases = [_]struct { id: []const u8, env: []const u8, model: []const u8 }{
        .{ .id = "openrouter", .env = "OPENROUTER_API_KEY", .model = "openai/gpt-4o-mini" },
        .{ .id = "opencode", .env = "OPENCODE_API_KEY", .model = "grok-code-fast-1" },
        .{ .id = "vercel", .env = "AI_GATEWAY_API_KEY", .model = "anthropic/claude-sonnet-4.5" },
        .{ .id = "zenmux", .env = "ZENMUX_API_KEY", .model = "bigseek/code" },
        .{ .id = "deepinfra", .env = "DEEPINFRA_API_KEY", .model = "meta-llama/Llama-3.3-70B-Instruct" },
    };
    for (cases) |case| {
        const target = catalogTarget(case.id) orelse return error.TestExpectedTarget;
        test_catalog_discovery = &[_]CatalogDiscovery{.{
            .id = case.id,
            .models_url = target.models_url,
            .model_ids = &.{case.model},
        }};
        test_catalog_environment = &[_]provider_credential.EnvironmentValue{
            .{ .name = case.env, .value = "row-key" },
        };
        const models = try loadCatalogModelsWithRows(
            std.testing.allocator,
            &.{case.id},
            null,
            .allow_cache,
        );
        defer {
            for (models) |*model| model.deinit(std.testing.allocator);
            std.testing.allocator.free(models);
        }
        test_catalog_discovery = null;
        test_catalog_environment = null;

        try std.testing.expectEqual(@as(usize, 1), models.len);
        try std.testing.expectEqualStrings(case.model, models[0].id);
        try std.testing.expectEqualStrings(case.id, models[0].provider);
        try std.testing.expectEqualStrings("openai-completions", models[0].api);
        try std.testing.expectEqualStrings(target.base_url, models[0].base_url);
    }
}

test "a gateway row with a credential but no discovery contributes nothing" {
    test_catalog_discovery = &[_]CatalogDiscovery{};
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "OPENROUTER_API_KEY", .value = "row-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadCatalogModelsWithRows(std.testing.allocator, &.{"openrouter"}, null, .allow_cache);
    defer deinitModels(std.testing.allocator, models);
    try std.testing.expectEqual(@as(usize, 0), models.len);
}

test "the production loader enables deepseek, every gateway and every coding plan" {
    const enabled = [_][]const u8{
        "deepseek",
        "openrouter",
        "opencode",
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
    try std.testing.expectEqual(enabled.len, catalog_loader_ids.len);
    for (enabled, 0..) |id, index| {
        try std.testing.expectEqualStrings(id, catalog_loader_ids[index]);
        try std.testing.expect(catalogTargetInRegion(id, provider_catalog.defaultRegion(id)) != null);
    }
}

test "loadProductionModels serves a gateway row's discovered models beside deepseek's" {
    const deepseek = catalogTarget("deepseek") orelse return error.TestExpectedTarget;
    const openrouter = catalogTarget("openrouter") orelse return error.TestExpectedTarget;
    test_catalog_discovery = &[_]CatalogDiscovery{
        .{ .id = "deepseek", .models_url = deepseek.models_url, .model_ids = &.{"deepseek-chat"} },
        .{ .id = "openrouter", .models_url = openrouter.models_url, .model_ids = &.{"openai/gpt-4o-mini"} },
    };
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "DEEPSEEK_API_KEY", .value = "deepseek-key" },
        .{ .name = "OPENROUTER_API_KEY", .value = "openrouter-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadProductionModels(std.testing.allocator);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 2), models.len);
    try std.testing.expectEqualStrings("deepseek", models[0].provider);
    try std.testing.expectEqualStrings("openrouter", models[1].provider);
    try std.testing.expectEqualStrings("https://api.deepseek.com", models[0].base_url);
    try std.testing.expectEqualStrings("https://openrouter.ai/api/v1", models[1].base_url);
}

test "every coding plan row the loader enables carries its own version in the base" {
    const plans = [_][]const u8{
        "zai-coding-plan",
        "alibaba-coding-plan",
        "minimax-coding-plan",
        "tencent-coding-plan",
        "volcengine-coding-plan",
    };
    for (plans) |id| {
        const target = catalogTarget(id) orelse return error.TestExpectedTarget;
        try std.testing.expectEqualStrings(id, target.id);
        try std.testing.expect(provider_catalog.endpointCarriesVersion(id, target.base_url));
        try std.testing.expectEqualStrings("/models", provider_catalog.modelsEndpoint(id).?);
        const listed = try provider_catalog.listingUrlOwned(
            std.testing.allocator,
            target.base_url,
            "/models",
            true,
            false,
        );
        defer std.testing.allocator.free(listed);
        try std.testing.expectEqualStrings(target.models_url, listed);
        try std.testing.expect(std.mem.endsWith(u8, listed, "/models"));
        try std.testing.expect(!std.mem.endsWith(u8, listed, "/v4/v1/models"));
    }
}

test "an override on a coding plan row lists and requests through the loader's own resolution" {
    const id = "volcengine-coding-plan";
    const catalogued = catalogTarget(id) orelse return error.TestExpectedTarget;
    try std.testing.expect(std.mem.endsWith(u8, catalogued.models_url, "/coding/v3/models"));

    const proxy_base = "https://proxy.example/api";
    test_catalog_base_urls = .{ .global = proxy_base };
    test_catalog_discovery = &[_]CatalogDiscovery{.{
        .id = id,
        .models_url = "https://proxy.example/api/v1/models",
        .model_ids = &.{"doubao-seed-code"},
    }};
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "ARK_CODING_PLAN_API_KEY", .value = "row-key" },
    };
    defer {
        test_catalog_base_urls = null;
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadCatalogModelsWithRows(
        std.testing.allocator,
        &.{id},
        null,
        .allow_cache,
    );
    defer deinitModels(std.testing.allocator, models);
    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings(proxy_base, models[0].base_url);

    const maybe_models_url = provider_catalog.modelsUrl(id, null);
    try std.testing.expect(maybe_models_url != null);
    const target_models_url = maybe_models_url.?;

    const request = try provider_catalog.joinModelUrlOwned(
        std.testing.allocator,
        models[0],
        provider_catalog.wirePath(models[0].api).?,
    );
    defer std.testing.allocator.free(request);
    try std.testing.expectEqualStrings("https://proxy.example/api/v1/chat/completions", request);
    try std.testing.expect(countVersions(target_models_url) == countVersions(request));
}

test "an override base that already carries a version is normalised before the listing sees it" {
    const id = "volcengine-coding-plan";
    test_catalog_base_urls = .{ .global = "https://proxy.example/api/v1" };
    test_catalog_discovery = &[_]CatalogDiscovery{.{
        .id = id,
        .models_url = "https://proxy.example/api/v1/models",
        .model_ids = &.{"doubao-seed-code"},
    }};
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "ARK_CODING_PLAN_API_KEY", .value = "row-key" },
    };
    defer {
        test_catalog_base_urls = null;
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadCatalogModelsWithRows(
        std.testing.allocator,
        &.{id},
        null,
        .allow_cache,
    );
    defer deinitModels(std.testing.allocator, models);
    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings("https://proxy.example/api", models[0].base_url);
}

test "a coding plan row's discovered models carry that row's base, wire and own listing url" {
    const cases = [_]struct { id: []const u8, env: []const u8, wire: []const u8, model: []const u8 }{
        .{ .id = "zai-coding-plan", .env = "ZHIPU_API_KEY", .wire = "openai-completions", .model = "glm-4.6" },
        .{ .id = "alibaba-coding-plan", .env = "ALIBABA_CODING_PLAN_API_KEY", .wire = "openai-completions", .model = "qwen3-coder-plus" },
        .{ .id = "minimax-coding-plan", .env = "MINIMAX_API_KEY", .wire = "anthropic-messages", .model = "MiniMax-M2" },
        .{ .id = "tencent-coding-plan", .env = "TENCENT_CODING_PLAN_API_KEY", .wire = "openai-completions", .model = "hunyuan-turbos" },
        .{ .id = "volcengine-coding-plan", .env = "ARK_CODING_PLAN_API_KEY", .wire = "openai-completions", .model = "doubao-seed-code" },
    };
    for (cases) |case| {
        const target = catalogTarget(case.id) orelse return error.TestExpectedTarget;
        test_catalog_discovery = &[_]CatalogDiscovery{.{
            .id = case.id,
            .models_url = target.models_url,
            .model_ids = &.{case.model},
        }};
        test_catalog_environment = &[_]provider_credential.EnvironmentValue{
            .{ .name = case.env, .value = "row-key" },
        };
        const models = try loadCatalogModelsWithRows(
            std.testing.allocator,
            &.{case.id},
            null,
            .allow_cache,
        );
        defer {
            for (models) |*model| model.deinit(std.testing.allocator);
            std.testing.allocator.free(models);
        }
        test_catalog_discovery = null;
        test_catalog_environment = null;

        try std.testing.expectEqual(@as(usize, 1), models.len);
        try std.testing.expectEqualStrings(case.model, models[0].id);
        try std.testing.expectEqualStrings(case.id, models[0].provider);
        try std.testing.expectEqualStrings(case.wire, models[0].api);
        try std.testing.expectEqualStrings(target.base_url, models[0].base_url);
    }
}

test "loadProductionModels serves a coding plan row's discovered models beside deepseek's" {
    const deepseek = catalogTarget("deepseek") orelse return error.TestExpectedTarget;
    const tencent = catalogTarget("tencent-coding-plan") orelse return error.TestExpectedTarget;
    test_catalog_discovery = &[_]CatalogDiscovery{
        .{ .id = "deepseek", .models_url = deepseek.models_url, .model_ids = &.{"deepseek-chat"} },
        .{ .id = "tencent-coding-plan", .models_url = tencent.models_url, .model_ids = &.{"hunyuan-turbos"} },
    };
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "DEEPSEEK_API_KEY", .value = "deepseek-key" },
        .{ .name = "TENCENT_CODING_PLAN_API_KEY", .value = "tencent-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadProductionModels(std.testing.allocator);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 2), models.len);
    try std.testing.expectEqualStrings("deepseek", models[0].provider);
    try std.testing.expectEqualStrings("tencent-coding-plan", models[1].provider);
    try std.testing.expectEqualStrings("https://api.deepseek.com", models[0].base_url);
    try std.testing.expectEqualStrings("https://api.lkeap.cloud.tencent.com/coding/v3", models[1].base_url);
}

test "openai is served on the completions wire except for the models that need responses" {
    const cases = [_]struct { model: []const u8, wire: []const u8 }{
        .{ .model = "gpt-4o", .wire = "openai-completions" },
        .{ .model = "gpt-4o-mini", .wire = "openai-completions" },
        .{ .model = "gpt-5", .wire = "openai-completions" },
        .{ .model = "gpt-5.1-codex", .wire = "openai-completions" },
        .{ .model = "o1-pro", .wire = "openai-responses" },
        .{ .model = "o3-pro", .wire = "openai-responses" },
        .{ .model = "gpt-5-pro", .wire = "openai-responses" },
        .{ .model = "gpt-5-codex", .wire = "openai-responses" },
        .{ .model = "gpt-5.1-codex-max", .wire = "openai-responses" },
        .{ .model = "deep-research-preview", .wire = "openai-responses" },
        .{ .model = "computer-use-preview", .wire = "openai-responses" },
    };
    for (cases) |case| {
        const wire = provider_catalog.wireForModel("openai", case.model) orelse return error.TestExpectedTarget;
        try std.testing.expectEqualStrings(case.wire, wire.id);
    }
}

test "a row with one wire keeps it whatever the model is called" {
    for (provider_catalog.all) |row| {
        if (row.wires.len != 1) continue;
        const only = provider_catalog.wirePath(row.wires[0]) orelse continue;
        for ([_][]const u8{ "gpt-4o", "o1-pro", "gpt-5-codex", "deep-research-preview" }) |model| {
            const wire = provider_catalog.wireForModel(row.id, model) orelse continue;
            try std.testing.expectEqualStrings(only.id, wire.id);
        }
    }
}

test "openai's responses-only models reach the loader on the responses wire" {
    const target = catalogTarget("openai") orelse return error.TestExpectedTarget;
    try std.testing.expectEqualStrings("openai-completions", target.wire);
    try std.testing.expectEqualStrings("https://api.openai.com", target.base_url);
    test_catalog_discovery = &[_]CatalogDiscovery{.{
        .id = "openai",
        .models_url = target.models_url,
        .model_ids = &.{ "gpt-4o-mini", "gpt-5-codex" },
    }};
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "OPENAI_API_KEY", .value = "openai-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadCatalogModelsWithRows(
        std.testing.allocator,
        &.{"openai"},
        null,
        .allow_cache,
    );
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 2), models.len);
    try std.testing.expectEqualStrings("gpt-4o-mini", models[0].id);
    try std.testing.expectEqualStrings("openai-completions", models[0].api);
    try std.testing.expectEqualStrings("gpt-5-codex", models[1].id);
    try std.testing.expectEqualStrings("openai-responses", models[1].api);
    try std.testing.expectEqualStrings("https://api.openai.com", models[1].base_url);
}

test "the wire a model is served on is the one the loader would pick for the row" {
    for (provider_catalog.all) |row| {
        const target = catalogTarget(row.id) orelse continue;
        const per_model = provider_catalog.wireForModel(row.id, "gpt-4o") orelse return error.TestExpectedTarget;
        try std.testing.expectEqualStrings(target.wire, per_model.id);
    }
}

test "the loader's rows are catalog rows the target answers for" {
    try std.testing.expect(catalog_loader_ids.len > 0);
    for (catalog_loader_ids) |id| {
        try std.testing.expect(provider_catalog.provider(id) != null);
        try std.testing.expect(catalogTargetInRegion(id, provider_catalog.defaultRegion(id)) != null);
    }
}

test "a discovered catalog row builds models on the row's wire and base url" {
    test_catalog_discovery = &[_]CatalogDiscovery{
        .{ .id = "deepseek", .models_url = deepseek_catalog_models_url, .model_ids = &.{ "deepseek-chat", "deepseek-reasoner" } },
    };
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "DEEPSEEK_API_KEY", .value = "row-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadCatalogModels(std.testing.allocator, null, .allow_cache);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 2), models.len);
    try std.testing.expectEqualStrings("deepseek-chat", models[0].id);
    try std.testing.expectEqualStrings("deepseek-chat", models[0].name);
    try std.testing.expectEqualStrings("deepseek", models[0].provider);
    try std.testing.expectEqualStrings("openai-completions", models[0].api);
    try std.testing.expectEqualStrings("https://api.deepseek.com", models[0].base_url);
    try std.testing.expectEqual(@as(u32, catalog_context_window), models[0].context_window);
    try std.testing.expectEqual(@as(u32, catalog_max_output_tokens), models[0].max_tokens);
    try std.testing.expectEqual(@as(usize, 1), models[0].input.len);
    try std.testing.expectEqualStrings("text", models[0].input[0]);

    try std.testing.expectEqualStrings("deepseek-reasoner", models[1].id);
}

test "a catalog row with no credential set contributes nothing" {
    test_catalog_discovery = &[_]CatalogDiscovery{
        .{ .id = "deepseek", .models_url = deepseek_catalog_models_url, .model_ids = &.{"deepseek-chat"} },
    };
    defer test_catalog_discovery = null;

    const models = try loadCatalogModels(std.testing.allocator, null, .allow_cache);
    defer deinitModels(std.testing.allocator, models);
    try std.testing.expectEqual(@as(usize, 0), models.len);

    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "OPENROUTER_API_KEY", .value = "another-row-key" },
    };
    defer test_catalog_environment = null;

    const other_key = try loadCatalogModels(std.testing.allocator, null, .allow_cache);
    defer deinitModels(std.testing.allocator, other_key);
    try std.testing.expectEqual(@as(usize, 0), other_key.len);
}

test "a catalog row with discovery but no model id contributes nothing" {
    test_catalog_discovery = &[_]CatalogDiscovery{.{ .id = "deepseek", .models_url = deepseek_catalog_models_url, .model_ids = &.{} }};
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "DEEPSEEK_API_KEY", .value = "row-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadCatalogModels(std.testing.allocator, null, .allow_cache);
    defer deinitModels(std.testing.allocator, models);
    try std.testing.expectEqual(@as(usize, 0), models.len);
}

test "a row's own limits reach a model it does not declare, and a row that declares none keeps the generic ones" {
    try compat.setTestEnv(std.testing.allocator, kimi_region_env, "");
    defer compat.clearTestEnv();
    const target = catalogTargetInRegion("kimi", "china") orelse return error.TestExpectedTarget;
    test_catalog_discovery = &[_]CatalogDiscovery{
        .{ .id = "kimi", .models_url = target.models_url, .model_ids = &.{"kimi-k2-turbo-preview"} },
        .{ .id = "deepseek", .models_url = deepseek_catalog_models_url, .model_ids = &.{"deepseek-chat"} },
    };
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = kimi_env_key, .value = "kimi-key" },
        .{ .name = "DEEPSEEK_API_KEY", .value = "row-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadCatalogModels(std.testing.allocator, null, .allow_cache);
    defer deinitModels(std.testing.allocator, models);

    var kimi_seen = false;
    var deepseek_seen = false;
    for (models) |model| {
        if (std.mem.eql(u8, "kimi", model.provider)) {
            kimi_seen = true;
            try std.testing.expectEqualStrings("kimi-k2-turbo-preview", model.name);
            try std.testing.expectEqual(@as(u32, 262_144), model.context_window);
            try std.testing.expectEqual(@as(u32, 16_384), model.max_tokens);
        }
        if (std.mem.eql(u8, "deepseek", model.provider)) {
            deepseek_seen = true;
            try std.testing.expectEqualStrings("deepseek-chat", model.name);
            try std.testing.expectEqual(@as(u32, catalog_context_window), model.context_window);
            try std.testing.expectEqual(@as(u32, catalog_max_output_tokens), model.max_tokens);
        }
    }
    try std.testing.expect(kimi_seen);
    try std.testing.expect(deepseek_seen);
}

test "a row that declares models still serves them when its own listing answers with none" {
    try compat.setTestEnv(std.testing.allocator, kimi_region_env, "");
    defer compat.clearTestEnv();
    const target = catalogTargetInRegion("kimi", "china") orelse return error.TestExpectedTarget;
    test_catalog_discovery = &[_]CatalogDiscovery{.{
        .id = "kimi",
        .models_url = target.models_url,
        .model_ids = &.{},
    }};
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = kimi_env_key, .value = "kimi-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadCatalogModels(std.testing.allocator, null, .allow_cache);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings("kimi-k2.7-code", models[0].id);
    try std.testing.expectEqualStrings("Kimi K2.7 Code", models[0].name);
    try std.testing.expectEqualStrings("kimi", models[0].provider);
    try std.testing.expectEqualStrings("openai-completions", models[0].api);
    try std.testing.expectEqualStrings("https://api.kimi.com/coding", models[0].base_url);
    try std.testing.expectEqual(@as(u32, 262_144), models[0].context_window);
    try std.testing.expectEqual(@as(u32, 16_384), models[0].max_tokens);
}

test "a provider's own listing speaks for its models, and a row's figures are only the default" {
    const allocator = std.testing.allocator;
    const body =
        \\{"data":[
        \\  {"id":"kimi-for-coding","display_name":"Kimi For Coding","context_length":1048576,"max_output_tokens":32768,"supports_reasoning":true,"supports_image_in":true},
        \\  {"id":"kimi-k2-turbo-preview","context_window":262144,"max_tokens":16384,"reasoning":false,"modalities":["text","image"]},
        \\  {"id":"plain-model"}
        \\]}
    ;

    const parsed = try parseCatalogModels(allocator, body);
    defer freeDiscoveredModels(allocator, parsed);
    try std.testing.expectEqual(@as(usize, 3), parsed.len);

    try std.testing.expectEqualStrings("kimi-for-coding", parsed[0].id);
    try std.testing.expectEqualStrings("Kimi For Coding", parsed[0].name orelse "<none>");
    try std.testing.expectEqual(@as(?u32, 1_048_576), parsed[0].context_window);
    try std.testing.expectEqual(@as(?u32, 32_768), parsed[0].max_tokens);
    try std.testing.expectEqual(@as(?bool, true), parsed[0].reasoning);
    try std.testing.expectEqual(@as(?bool, true), parsed[0].image_input);

    try std.testing.expectEqualStrings("kimi-k2-turbo-preview", parsed[1].id);
    try std.testing.expect(parsed[1].name == null);
    try std.testing.expectEqual(@as(?u32, 262_144), parsed[1].context_window);
    try std.testing.expectEqual(@as(?u32, 16_384), parsed[1].max_tokens);
    try std.testing.expectEqual(@as(?bool, false), parsed[1].reasoning);
    try std.testing.expectEqual(@as(?bool, true), parsed[1].image_input);

    try std.testing.expectEqualStrings("plain-model", parsed[2].id);
    try std.testing.expect(parsed[2].name == null);
    try std.testing.expect(parsed[2].context_window == null);
    try std.testing.expect(parsed[2].max_tokens == null);
    try std.testing.expect(parsed[2].reasoning == null);
    try std.testing.expect(parsed[2].image_input == null);
}

test "a discovered model's own figures outrank the row's, which outrank the generic guess" {
    const allocator = std.testing.allocator;
    const target = catalogTargetInRegion("kimi", "global") orelse return error.TestExpectedTarget;

    var reported = try catalogModel(allocator, target, .{
        .id = "kimi-for-coding",
        .name = "Kimi For Coding",
        .context_window = 1_048_576,
        .max_tokens = 32_768,
        .reasoning = true,
        .image_input = true,
    });
    defer reported.deinit(allocator);

    try std.testing.expectEqualStrings("Kimi For Coding", reported.name);
    try std.testing.expectEqual(@as(u32, 1_048_576), reported.context_window);
    try std.testing.expectEqual(@as(u32, 32_768), reported.max_tokens);
    try std.testing.expect(reported.reasoning);
    try std.testing.expectEqual(@as(usize, 2), reported.input.len);
    try std.testing.expectEqualStrings("text", reported.input[0]);
    try std.testing.expectEqualStrings("image", reported.input[1]);

    var undeclared = try catalogModel(allocator, target, .{ .id = "kimi-something-new" });
    defer undeclared.deinit(allocator);

    try std.testing.expectEqualStrings("kimi-something-new", undeclared.name);
    try std.testing.expectEqual(@as(u32, 262_144), undeclared.context_window);
    try std.testing.expectEqual(@as(u32, 16_384), undeclared.max_tokens);
    try std.testing.expect(!undeclared.reasoning);
    try std.testing.expectEqual(@as(usize, 1), undeclared.input.len);

    var declared = try catalogModel(allocator, target, .{ .id = "kimi-k2.7-code" });
    defer declared.deinit(allocator);
    try std.testing.expectEqualStrings("Kimi K2.7 Code", declared.name);

    var plain = try catalogModel(allocator, target, .{
        .id = "plain-model",
    });
    defer plain.deinit(allocator);
    const deepseek = catalogTargetInRegion("deepseek", null) orelse return error.TestExpectedTarget;
    var generic = try catalogModel(allocator, deepseek, .{ .id = "deepseek-chat" });
    defer generic.deinit(allocator);
    try std.testing.expectEqual(@as(u32, catalog_context_window), generic.context_window);
    try std.testing.expectEqual(@as(u32, catalog_max_output_tokens), generic.max_tokens);
    try std.testing.expectEqual(@as(u32, 262_144), plain.context_window);
}

test "loadProductionModels carries a catalog row's discovered models" {
    test_catalog_discovery = &[_]CatalogDiscovery{
        .{ .id = "deepseek", .models_url = deepseek_catalog_models_url, .model_ids = &.{"deepseek-chat"} },
    };
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "DEEPSEEK_API_KEY", .value = "row-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }

    const models = try loadProductionModels(std.testing.allocator);
    defer deinitModels(std.testing.allocator, models);

    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings("deepseek", models[0].provider);
    try std.testing.expectEqualStrings("openai-completions", models[0].api);
}

fn catalogLoadProbe(allocator: std.mem.Allocator) !void {
    const openai = catalogTarget("openai") orelse return error.TestExpectedTarget;
    test_catalog_discovery = &[_]CatalogDiscovery{
        .{ .id = "deepseek", .models_url = deepseek_catalog_models_url, .model_ids = &.{ "deepseek-chat", "deepseek-reasoner" } },
        .{ .id = "openai", .models_url = openai.models_url, .model_ids = &.{ "gpt-4o-mini", "gpt-5-codex" } },
    };
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "DEEPSEEK_API_KEY", .value = "row-key" },
        .{ .name = "OPENAI_API_KEY", .value = "openai-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }
    const models = try loadCatalogModels(allocator, null, .allow_cache);
    defer deinitModels(allocator, models);
    try std.testing.expectEqual(@as(usize, 4), models.len);
    try std.testing.expectEqualStrings("openai-responses", models[3].api);
}

fn declaredModelsFallbackProbe(allocator: std.mem.Allocator) !void {
    defer compat.clearTestEnv();
    try compat.setTestEnv(allocator, kimi_region_env, "");
    const target = catalogTargetInRegion("kimi", "china") orelse return error.TestExpectedTarget;
    test_catalog_discovery = &[_]CatalogDiscovery{
        .{ .id = "kimi", .models_url = target.models_url, .model_ids = &.{} },
    };
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = kimi_env_key, .value = "kimi-key" },
    };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
    }
    const models = try loadCatalogModels(allocator, null, .allow_cache);
    defer deinitModels(allocator, models);
    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings("kimi-k2.7-code", models[0].id);
    try std.testing.expectEqualStrings("Kimi K2.7 Code", models[0].name);
    try std.testing.expectEqual(@as(u32, 262_144), models[0].context_window);
    try std.testing.expectEqual(@as(u32, 16_384), models[0].max_tokens);
}

test "a row's declared models free every allocation when one fails midway" {
    try declaredModelsFallbackProbe(std.testing.allocator);
    try std.testing.checkAllAllocationFailures(std.testing.allocator, declaredModelsFallbackProbe, .{});
}

fn overriddenCatalogLoadProbe(allocator: std.mem.Allocator) !void {
    test_catalog_discovery = &[_]CatalogDiscovery{
        .{ .id = "deepseek", .models_url = proxy_models_url, .model_ids = &.{"deepseek-chat"} },
    };
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "DEEPSEEK_API_KEY", .value = "row-key" },
    };
    test_catalog_base_urls = .{ .deepseek = "https://proxy.example/api" };
    defer {
        test_catalog_discovery = null;
        test_catalog_environment = null;
        test_catalog_base_urls = null;
    }
    const models = try loadCatalogModels(allocator, null, .allow_cache);
    defer deinitModels(allocator, models);
    try std.testing.expectEqual(@as(usize, 1), models.len);
    try std.testing.expectEqualStrings("https://proxy.example/api", models[0].base_url);
}

test "an overridden catalog row frees every allocation when one fails midway" {
    try overriddenCatalogLoadProbe(std.testing.allocator);
    try std.testing.checkAllAllocationFailures(std.testing.allocator, overriddenCatalogLoadProbe, .{});
}

test "catalog row models free every allocation when one fails midway" {
    try catalogLoadProbe(std.testing.allocator);
    try std.testing.checkAllAllocationFailures(std.testing.allocator, catalogLoadProbe, .{});
}

test "the row environment names every credential variable the catalog records, in order" {
    test_catalog_environment = &[_]provider_credential.EnvironmentValue{
        .{ .name = "XIAOMI_API_KEY", .value = "xiaomi-key" },
        .{ .name = "ANTHROPIC_API_KEY", .value = "anthropic-key" },
        .{ .name = "ANTHROPIC_AUTH_TOKEN", .value = "anthropic-token" },
        .{ .name = "DEEPSEEK_API_KEY", .value = "deepseek-key" },
    };
    defer test_catalog_environment = null;

    const xiaomi = try catalogEnvironment(std.testing.allocator, "xiaomi-token-plan-cn");
    defer freeEnvironment(std.testing.allocator, xiaomi);
    try std.testing.expectEqual(@as(usize, 1), xiaomi.len);
    try std.testing.expectEqualStrings("XIAOMI_API_KEY", xiaomi[0].name);
    try std.testing.expectEqualStrings("xiaomi-key", xiaomi[0].value);

    const deepseek = try catalogEnvironment(std.testing.allocator, "deepseek");
    defer freeEnvironment(std.testing.allocator, deepseek);
    try std.testing.expectEqual(@as(usize, 1), deepseek.len);
    try std.testing.expectEqualStrings("DEEPSEEK_API_KEY", deepseek[0].name);
    try std.testing.expectEqualStrings("deepseek-key", deepseek[0].value);

    const anthropic = try catalogEnvironment(std.testing.allocator, "anthropic");
    defer freeEnvironment(std.testing.allocator, anthropic);
    try std.testing.expectEqual(@as(usize, 2), anthropic.len);
    try std.testing.expectEqualStrings("ANTHROPIC_AUTH_TOKEN", anthropic[0].name);
    try std.testing.expectEqualStrings("anthropic-token", anthropic[0].value);
    try std.testing.expectEqualStrings("ANTHROPIC_API_KEY", anthropic[1].name);
    try std.testing.expectEqualStrings("anthropic-key", anthropic[1].value);

    const unknown = try catalogEnvironment(std.testing.allocator, "no-such-provider");
    defer freeEnvironment(std.testing.allocator, unknown);
    try std.testing.expectEqual(@as(usize, 0), unknown.len);
}

test "a models cache name is the row's own, apart from any custom row of the same id" {
    const name = try catalogRowCacheName(std.testing.allocator, "deepseek", null);
    defer std.testing.allocator.free(name);
    try std.testing.expectEqualStrings("catalog-deepseek.json", name);

    const custom = try customCatalogName(std.testing.allocator, "deepseek");
    defer std.testing.allocator.free(custom);
    try std.testing.expectEqualStrings("custom-deepseek.json", custom);
}

test "a row that ships an endpoint per region caches each region's models apart" {
    const china = try catalogRowCacheName(std.testing.allocator, "kimi", "china");
    defer std.testing.allocator.free(china);
    try std.testing.expectEqualStrings("catalog-kimi-china.json", china);

    const global = try catalogRowCacheName(std.testing.allocator, "kimi", "global");
    defer std.testing.allocator.free(global);
    try std.testing.expectEqualStrings("catalog-kimi-global.json", global);
    try std.testing.expect(!std.mem.eql(u8, china, global));

    const resolved = catalogTargetInRegion("kimi", "global") orelse return error.TestExpectedTarget;
    const from_target = try catalogRowCacheName(std.testing.allocator, resolved.id, resolved.region);
    defer std.testing.allocator.free(from_target);
    try std.testing.expectEqualStrings("catalog-kimi-global.json", from_target);
}
