const std = @import("std");
const ai_types = @import("ai_types");
const compat_mod = @import("compat");
const provider_base_url = @import("provider_base_url");
const provider_catalog = @import("provider_catalog");

pub const config_file_name = "providers.json";
pub const max_config_bytes = 2 * 1024 * 1024;

pub const supported_apis = [_][]const u8{
    "openai-completions",
    "openai-responses",
    "anthropic-messages",
};

pub const reserved_ids = provider_catalog.ids;

pub const ConfigError = error{
    InvalidConfig,
    MissingProviderId,
    InvalidProviderId,
    ReservedProviderId,
    DuplicateProviderId,
    MissingBaseUrl,
    InvalidBaseUrl,
    UnsupportedApi,
    InvalidAuthMode,
};

pub const OverrideError = error{
    InvalidConfig,
    MissingProviderId,
    UnknownProviderId,
    MissingBaseUrl,
    InvalidBaseUrl,
    DuplicateOverride,
    ForbiddenOverrideMember,
    WrongTypedOverrideMember,
    UnsupportedOverrideRow,
};

pub const override_allowed_members = [_][]const u8{
    "base_url",
    "carries_version",
    "forwards_credential",
    "headers",
    "id",
    "models",
};

pub const override_forbidden_members = [_][]const u8{
    "api",
    "auth",
    "capabilities",
    "context_window",
    "max_tokens",
    "name",
    "reasoning",
    "wire",
};

pub const Override = struct {
    id: []const u8,
    base_url: ?[]const u8 = null,
    carries_version: ?bool = null,
    forwards_credential: bool = false,
    headers: []const ai_types.HeaderPair = &.{},
    models: []const ModelSpec = &.{},

    pub fn deinit(self: *Override, allocator: std.mem.Allocator) void {
        allocator.free(self.id);
        if (self.base_url) |url| allocator.free(url);
        for (self.headers) |header| {
            allocator.free(header.name);
            allocator.free(header.value);
        }
        allocator.free(self.headers);
        for (self.models) |*spec| {
            var owned = spec.*;
            owned.deinit(allocator);
        }
        allocator.free(self.models);
        self.* = undefined;
    }
};

pub const Config = struct {
    providers: []CustomProvider = &.{},
    overrides: []Override = &.{},

    pub fn deinit(self: *Config, allocator: std.mem.Allocator) void {
        deinitProviders(allocator, self.providers);
        deinitOverrides(allocator, self.overrides);
        self.* = undefined;
    }

    pub fn takeProviders(self: *Config, allocator: std.mem.Allocator) []CustomProvider {
        const providers = self.providers;
        self.providers = &.{};
        deinitOverrides(allocator, self.overrides);
        self.overrides = &.{};
        return providers;
    }
};

pub const ModelSpec = struct {
    id: []const u8,
    name: []const u8,
    context_window: ?u32 = null,
    max_tokens: ?u32 = null,

    fn deinit(self: *ModelSpec, allocator: std.mem.Allocator) void {
        allocator.free(self.id);
        allocator.free(self.name);
        self.* = undefined;
    }
};

pub const CustomProvider = struct {
    id: []const u8,
    name: []const u8,
    api: []const u8,
    base_url: []const u8,
    env_key: ?[]const u8 = null,
    auth_none: bool = false,
    headers: []const ai_types.HeaderPair = &.{},
    models: []const ModelSpec = &.{},
    compat: ?ai_types.OpenAICompatOptions = null,
    reasoning: bool = false,
    carries_version: ?bool = null,
    context_window: u32 = 128_000,
    max_tokens: u32 = 8_192,

    pub fn deinit(self: *CustomProvider, allocator: std.mem.Allocator) void {
        allocator.free(self.id);
        allocator.free(self.name);
        allocator.free(self.api);
        allocator.free(self.base_url);
        if (self.env_key) |key| allocator.free(key);
        for (self.headers) |header| {
            allocator.free(header.name);
            allocator.free(header.value);
        }
        allocator.free(self.headers);
        for (self.models) |*model| {
            var owned = model.*;
            owned.deinit(allocator);
        }
        allocator.free(self.models);
        self.* = undefined;
    }

    pub fn allows(self: *const CustomProvider, model_id: []const u8) bool {
        if (self.models.len == 0) return true;
        for (self.models) |spec| {
            if (std.mem.eql(u8, spec.id, model_id)) return true;
        }
        return false;
    }

    pub fn specFor(self: *const CustomProvider, model_id: []const u8) ?ModelSpec {
        for (self.models) |spec| {
            if (std.mem.eql(u8, spec.id, model_id)) return spec;
        }
        return null;
    }
};

pub fn deinitProviders(allocator: std.mem.Allocator, providers: []CustomProvider) void {
    for (providers) |*provider| provider.deinit(allocator);
    allocator.free(providers);
}

pub fn configPath(allocator: std.mem.Allocator) ![]u8 {
    const home = try compat_mod.getEnvVarOwned(allocator, "HOME");
    defer allocator.free(home);
    return std.fs.path.join(allocator, &.{ home, ".oapx", config_file_name });
}

pub fn load(allocator: std.mem.Allocator, max_bytes: usize) ![]CustomProvider {
    var config = try loadConfig(allocator, max_bytes);
    return config.takeProviders(allocator);
}

pub fn loadConfig(allocator: std.mem.Allocator, max_bytes: usize) !Config {
    return loadConfigMode(allocator, max_bytes, .treat_unreadable_as_empty);
}

pub fn loadConfigStrict(allocator: std.mem.Allocator, max_bytes: usize) !Config {
    return loadConfigMode(allocator, max_bytes, .report_unreadable);
}

const UnreadableConfig = enum { treat_unreadable_as_empty, report_unreadable };

fn loadConfigMode(allocator: std.mem.Allocator, max_bytes: usize, unreadable: UnreadableConfig) !Config {
    const path = configPath(allocator) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return emptyConfig(allocator),
    };
    defer allocator.free(path);
    const data = compat_mod.fs.readFileAlloc(allocator, compat_mod.fs.getCwd(), path, max_bytes) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        error.FileNotFound => return emptyConfig(allocator),
        else => switch (unreadable) {
            .treat_unreadable_as_empty => return emptyConfig(allocator),
            .report_unreadable => return err,
        },
    };
    defer allocator.free(data);
    return parseConfig(allocator, data);
}

fn emptyConfig(allocator: std.mem.Allocator) !Config {
    return .{
        .providers = try allocator.alloc(CustomProvider, 0),
        .overrides = try allocator.alloc(Override, 0),
    };
}

pub const NewProvider = struct {
    id: []const u8,
    base_url: []const u8,
    api: ?[]const u8 = null,
    env: ?[]const u8 = null,
    auth_none: bool = false,
};

pub fn appendProvider(allocator: std.mem.Allocator, existing: ?[]const u8, new: NewProvider) ![]u8 {
    if (existing) |data| {
        var current = try parseConfig(allocator, data);
        current.deinit(allocator);
    }
    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();

    var root = std.json.Value{ .object = .empty };
    if (existing) |data| root = try std.json.parseFromSliceLeaky(std.json.Value, arena, data, .{});
    if (root != .object) return ConfigError.InvalidConfig;

    var entry = std.json.ObjectMap.empty;
    try entry.put(arena, "id", .{ .string = new.id });
    try entry.put(arena, "base_url", .{ .string = new.base_url });
    if (new.api) |api| try entry.put(arena, "api", .{ .string = api });
    if (new.auth_none) {
        try entry.put(arena, "auth", .{ .string = "none" });
    } else if (new.env) |name| {
        var auth = std.json.ObjectMap.empty;
        try auth.put(arena, "env", .{ .string = name });
        try entry.put(arena, "auth", .{ .object = auth });
    }

    const providers = try root.object.getOrPut(arena, "providers");
    if (!providers.found_existing) providers.value_ptr.* = .{ .array = std.json.Array.init(arena) };
    if (providers.value_ptr.* != .array) return ConfigError.InvalidConfig;
    try providers.value_ptr.array.append(.{ .object = entry });

    const text = try std.json.Stringify.valueAlloc(allocator, root, .{ .whitespace = .indent_2 });
    errdefer allocator.free(text);
    var checked = try parseConfig(allocator, text);
    checked.deinit(allocator);
    return text;
}

pub fn addProvider(allocator: std.mem.Allocator, new: NewProvider) ![]u8 {
    const path = try configPath(allocator);
    errdefer allocator.free(path);
    const cwd = compat_mod.fs.getCwd();
    const existing: ?[]u8 = compat_mod.fs.readFileAlloc(allocator, cwd, path, max_config_bytes) catch |err| switch (err) {
        error.FileNotFound => null,
        else => return err,
    };
    defer if (existing) |data| allocator.free(data);
    const text = try appendProvider(allocator, existing, new);
    defer allocator.free(text);
    if (std.fs.path.dirname(path)) |dir| try compat_mod.fs.createDir(cwd, dir);
    const tmp_path = try std.fmt.allocPrint(allocator, "{s}.tmp", .{path});
    defer allocator.free(tmp_path);
    try compat_mod.fs.atomicReplace(cwd, path, tmp_path, text);
    return path;
}

pub const DeleteError = error{ProviderNotDeclared};

pub fn dropProvider(allocator: std.mem.Allocator, existing: []const u8, id: []const u8) ![]u8 {
    var current = try parseConfig(allocator, existing);
    current.deinit(allocator);
    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();

    var root = try std.json.parseFromSliceLeaky(std.json.Value, arena, existing, .{});
    if (root != .object) return ConfigError.InvalidConfig;
    const providers = root.object.getPtr("providers") orelse return DeleteError.ProviderNotDeclared;
    if (providers.* != .array) return ConfigError.InvalidConfig;
    var kept = std.json.Array.init(arena);
    var dropped = false;
    for (providers.array.items) |item| {
        const named = item == .object and if (item.object.get("id")) |value| value == .string and std.mem.eql(u8, value.string, id) else false;
        if (named) {
            dropped = true;
            continue;
        }
        try kept.append(item);
    }
    if (!dropped) return DeleteError.ProviderNotDeclared;
    providers.* = .{ .array = kept };

    const text = try std.json.Stringify.valueAlloc(allocator, root, .{ .whitespace = .indent_2 });
    errdefer allocator.free(text);
    var checked = try parseConfig(allocator, text);
    checked.deinit(allocator);
    return text;
}

pub fn deleteProvider(allocator: std.mem.Allocator, id: []const u8) ![]u8 {
    const path = try configPath(allocator);
    errdefer allocator.free(path);
    const cwd = compat_mod.fs.getCwd();
    const existing = compat_mod.fs.readFileAlloc(allocator, cwd, path, max_config_bytes) catch |err| switch (err) {
        error.FileNotFound => return DeleteError.ProviderNotDeclared,
        else => return err,
    };
    defer allocator.free(existing);
    const text = try dropProvider(allocator, existing, id);
    defer allocator.free(text);
    const tmp_path = try std.fmt.allocPrint(allocator, "{s}.tmp", .{path});
    defer allocator.free(tmp_path);
    try compat_mod.fs.atomicReplace(cwd, path, tmp_path, text);
    return path;
}

pub fn deinitOverrides(allocator: std.mem.Allocator, overrides: []Override) void {
    for (overrides) |*override| override.deinit(allocator);
    allocator.free(overrides);
}

pub fn parse(allocator: std.mem.Allocator, data: []const u8) ![]CustomProvider {
    var config = try parseConfig(allocator, data);
    return config.takeProviders(allocator);
}

pub fn parseConfig(allocator: std.mem.Allocator, data: []const u8) !Config {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, data, .{}) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return ConfigError.InvalidConfig,
    };
    defer parsed.deinit();
    if (parsed.value != .object) return ConfigError.InvalidConfig;

    var config = Config{
        .providers = try parseProviders(allocator, parsed.value.object),
        .overrides = &.{},
    };
    errdefer config.deinit(allocator);

    if (parsed.value.object.get("overrides")) |list| {
        if (list != .array) return OverrideError.InvalidConfig;
        config.overrides = try parseOverrides(allocator, list.array.items);
    }
    return config;
}

fn parseProviders(allocator: std.mem.Allocator, root: std.json.ObjectMap) ![]CustomProvider {
    const list = root.get("providers") orelse return allocator.alloc(CustomProvider, 0);
    if (list != .array) return ConfigError.InvalidConfig;

    var providers = std.ArrayList(CustomProvider).empty;
    errdefer {
        for (providers.items) |*provider| provider.deinit(allocator);
        providers.deinit(allocator);
    }

    for (list.array.items) |item| {
        if (item != .object) return ConfigError.InvalidConfig;
        const provider = try parseProvider(allocator, &item.object);
        errdefer {
            var owned = provider;
            owned.deinit(allocator);
        }
        for (providers.items) |existing| {
            if (std.mem.eql(u8, existing.id, provider.id)) return ConfigError.DuplicateProviderId;
        }
        try providers.append(allocator, provider);
    }

    return providers.toOwnedSlice(allocator);
}

fn parseOverrides(allocator: std.mem.Allocator, items: []const std.json.Value) ![]Override {
    var overrides = std.ArrayList(Override).empty;
    errdefer {
        for (overrides.items) |*override| override.deinit(allocator);
        overrides.deinit(allocator);
    }

    for (items) |item| {
        if (item != .object) return OverrideError.InvalidConfig;
        const override = try parseOverride(allocator, &item.object);
        errdefer {
            var owned = override;
            owned.deinit(allocator);
        }
        for (overrides.items) |existing| {
            if (std.mem.eql(u8, existing.id, override.id)) return OverrideError.DuplicateOverride;
        }
        try overrides.append(allocator, override);
    }

    return overrides.toOwnedSlice(allocator);
}

fn namedIn(names: []const []const u8, value: []const u8) bool {
    for (names) |name| {
        if (std.mem.eql(u8, name, value)) return true;
    }
    return false;
}

fn typedString(obj: *const std.json.ObjectMap, key: []const u8) !?[]const u8 {
    const value = obj.get(key) orelse return null;
    if (value != .string) return OverrideError.WrongTypedOverrideMember;
    return value.string;
}

fn typedBool(obj: *const std.json.ObjectMap, key: []const u8) !?bool {
    const value = obj.get(key) orelse return null;
    if (value != .bool) return OverrideError.WrongTypedOverrideMember;
    return value.bool;
}

fn parseOverride(allocator: std.mem.Allocator, obj: *const std.json.ObjectMap) !Override {
    var it = obj.iterator();
    while (it.next()) |entry| {
        if (namedIn(&override_forbidden_members, entry.key_ptr.*)) return OverrideError.ForbiddenOverrideMember;
        if (!namedIn(&override_allowed_members, entry.key_ptr.*)) return OverrideError.ForbiddenOverrideMember;
    }

    const raw_id = try typedString(obj, "id") orelse return OverrideError.MissingProviderId;
    if (provider_catalog.provider(raw_id) == null) return OverrideError.UnknownProviderId;
    if (std.mem.eql(u8, raw_id, "github-copilot")) return OverrideError.UnsupportedOverrideRow;

    const base_url = if (try typedString(obj, "base_url")) |raw| url: {
        const stated = try typedBool(obj, "carries_version");
        const trimmed = if (stated orelse false)
            std.mem.trimEnd(u8, raw, "/")
        else
            provider_base_url.normalizeVersionedBaseUrl(raw);
        if (trimmed.len == 0) return OverrideError.MissingBaseUrl;
        _ = std.Uri.parse(trimmed) catch return OverrideError.InvalidBaseUrl;
        break :url try allocator.dupe(u8, trimmed);
    } else null;
    errdefer if (base_url) |url| allocator.free(url);

    const id = try allocator.dupe(u8, raw_id);
    errdefer allocator.free(id);

    if (obj.get("headers") != null and obj.get("headers").? != .object) return OverrideError.WrongTypedOverrideMember;
    if (obj.get("models") != null and obj.get("models").? != .array) return OverrideError.WrongTypedOverrideMember;
    const headers = try parseHeaders(allocator, obj);
    errdefer {
        for (headers) |header| {
            allocator.free(header.name);
            allocator.free(header.value);
        }
        allocator.free(headers);
    }

    const models = try parseModels(allocator, obj);
    errdefer {
        for (models) |*spec| {
            var owned = spec.*;
            owned.deinit(allocator);
        }
        allocator.free(models);
    }

    return .{
        .id = id,
        .base_url = base_url,
        .carries_version = try typedBool(obj, "carries_version"),
        .forwards_credential = (try typedBool(obj, "forwards_credential")) orelse false,
        .headers = headers,
        .models = models,
    };
}

pub fn overrideFor(overrides: []const Override, id: []const u8) ?Override {
    for (overrides) |override| {
        if (std.mem.eql(u8, override.id, id)) return override;
    }
    return null;
}

fn parseProvider(allocator: std.mem.Allocator, obj: *const std.json.ObjectMap) !CustomProvider {
    const raw_id = objectString(obj, "id") orelse return ConfigError.MissingProviderId;
    try validateId(raw_id);

    const raw_base = objectString(obj, "base_url") orelse return ConfigError.MissingBaseUrl;
    const stated_version = objectBool(obj, "carries_version");
    const base_trimmed = if (stated_version orelse false)
        std.mem.trimEnd(u8, raw_base, "/")
    else
        provider_base_url.normalizeVersionedBaseUrl(raw_base);
    if (base_trimmed.len == 0) return ConfigError.MissingBaseUrl;
    _ = std.Uri.parse(base_trimmed) catch return ConfigError.InvalidBaseUrl;

    const raw_api = objectString(obj, "api") orelse "openai-completions";
    if (!isSupportedApi(raw_api)) return ConfigError.UnsupportedApi;

    var provider = CustomProvider{
        .id = try allocator.dupe(u8, raw_id),
        .name = undefined,
        .api = undefined,
        .base_url = undefined,
    };
    errdefer allocator.free(provider.id);

    provider.name = try allocator.dupe(u8, objectString(obj, "name") orelse raw_id);
    errdefer allocator.free(provider.name);

    provider.api = try allocator.dupe(u8, raw_api);
    errdefer allocator.free(provider.api);

    provider.base_url = try allocator.dupe(u8, base_trimmed);
    errdefer allocator.free(provider.base_url);

    if (obj.getPtr("auth")) |auth_value| {
        switch (auth_value.*) {
            .string => |mode| {
                if (!std.mem.eql(u8, mode, "none")) return ConfigError.InvalidAuthMode;
                provider.auth_none = true;
            },
            .object => {
                const env_name = objectString(&auth_value.object, "env") orelse
                    return ConfigError.InvalidAuthMode;
                if (env_name.len == 0) return ConfigError.InvalidAuthMode;
                provider.env_key = try allocator.dupe(u8, env_name);
            },
            else => return ConfigError.InvalidAuthMode,
        }
    }
    errdefer if (provider.env_key) |key| allocator.free(key);

    provider.headers = try parseHeaders(allocator, obj);
    errdefer {
        for (provider.headers) |header| {
            allocator.free(header.name);
            allocator.free(header.value);
        }
        allocator.free(provider.headers);
    }

    provider.models = try parseModels(allocator, obj);
    errdefer {
        for (provider.models) |*model| {
            var owned = model.*;
            owned.deinit(allocator);
        }
        allocator.free(provider.models);
    }

    provider.reasoning = objectBool(obj, "reasoning") orelse false;
    provider.carries_version = stated_version;
    provider.context_window = objectU32(obj, "context_window") orelse 128_000;
    provider.max_tokens = objectU32(obj, "max_tokens") orelse 8_192;
    provider.compat = parseCapabilities(obj);
    return provider;
}

fn parseHeaders(allocator: std.mem.Allocator, obj: *const std.json.ObjectMap) ![]const ai_types.HeaderPair {
    const headers = objectObject(obj, "headers") orelse return &.{};
    var list = std.ArrayList(ai_types.HeaderPair).empty;
    errdefer {
        for (list.items) |header| {
            allocator.free(header.name);
            allocator.free(header.value);
        }
        list.deinit(allocator);
    }
    var it = headers.iterator();
    while (it.next()) |entry| {
        if (entry.value_ptr.* != .string) continue;
        if (entry.key_ptr.*.len == 0) continue;
        const name = try allocator.dupe(u8, entry.key_ptr.*);
        errdefer allocator.free(name);
        const value = try allocator.dupe(u8, entry.value_ptr.*.string);
        errdefer allocator.free(value);
        try list.append(allocator, .{ .name = name, .value = value });
    }
    return list.toOwnedSlice(allocator);
}

fn parseModels(allocator: std.mem.Allocator, obj: *const std.json.ObjectMap) ![]const ModelSpec {
    const value = obj.get("models") orelse return &.{};
    if (value != .array) return ConfigError.InvalidConfig;

    var list = std.ArrayList(ModelSpec).empty;
    errdefer {
        for (list.items) |*spec| spec.deinit(allocator);
        list.deinit(allocator);
    }

    for (value.array.items) |entry| {
        switch (entry) {
            .string => |id| {
                if (id.len == 0) continue;
                const owned_id = try allocator.dupe(u8, id);
                errdefer allocator.free(owned_id);
                const owned_name = try allocator.dupe(u8, id);
                errdefer allocator.free(owned_name);
                try list.append(allocator, .{ .id = owned_id, .name = owned_name });
            },
            .object => |model_obj| {
                const id = objectString(&model_obj, "id") orelse return ConfigError.InvalidConfig;
                if (id.len == 0) return ConfigError.InvalidConfig;
                const owned_id = try allocator.dupe(u8, id);
                errdefer allocator.free(owned_id);
                const owned_name = try allocator.dupe(u8, objectString(&model_obj, "name") orelse id);
                errdefer allocator.free(owned_name);
                try list.append(allocator, .{
                    .id = owned_id,
                    .name = owned_name,
                    .context_window = objectU32(&model_obj, "context_window"),
                    .max_tokens = objectU32(&model_obj, "max_tokens"),
                });
            },
            else => return ConfigError.InvalidConfig,
        }
    }

    return list.toOwnedSlice(allocator);
}

fn parseCapabilities(obj: *const std.json.ObjectMap) ?ai_types.OpenAICompatOptions {
    const caps = objectObject(obj, "capabilities") orelse return null;
    var options = ai_types.OpenAICompatOptions{};
    var any = false;

    if (objectBool(caps, "cache_ttl")) |value| {
        options.supports_anthropic_cache_ttl = value;
        any = true;
    }
    if (objectBool(caps, "reasoning_effort")) |value| {
        options.supports_reasoning_effort = value;
        any = true;
    }
    if (objectBool(caps, "developer_role")) |value| {
        options.supports_developer_role = value;
        any = true;
    }
    if (objectBool(caps, "store")) |value| {
        options.supports_store = value;
        any = true;
    }
    if (objectBool(caps, "strict_mode")) |value| {
        options.supports_strict_mode = value;
        any = true;
    }
    if (objectBool(caps, "thinking_as_text")) |value| {
        options.requires_thinking_as_text = value;
        any = true;
    }
    if (objectBool(caps, "usage_in_streaming")) |value| {
        options.supports_usage_in_streaming = value;
        any = true;
    }
    if (objectString(caps, "max_tokens_field")) |value| {
        if (std.mem.eql(u8, value, "max_tokens")) {
            options.max_tokens_field = .max_tokens;
            any = true;
        } else if (std.mem.eql(u8, value, "max_completion_tokens")) {
            options.max_tokens_field = .max_completion_tokens;
            any = true;
        }
    }
    if (objectString(caps, "thinking_format")) |value| {
        if (std.mem.eql(u8, value, "zai")) {
            options.thinking_format = .zai;
            any = true;
        } else if (std.mem.eql(u8, value, "qwen")) {
            options.thinking_format = .qwen;
            any = true;
        } else if (std.mem.eql(u8, value, "openai")) {
            options.thinking_format = .openai;
            any = true;
        }
    }

    return if (any) options else null;
}

test "declaring one capability leaves the rest unset" {
    const json =
        \\{"providers":[{
        \\  "id":"gateway","name":"Gateway","api":"openai-completions",
        \\  "base_url":"https://gw.internal",
        \\  "capabilities":{"cache_ttl":true}
        \\}]}
    ;
    const providers = try parse(testing.allocator, json);
    defer deinitProviders(testing.allocator, providers);

    const caps = providers[0].compat orelse return error.TestExpectedCapabilities;
    try testing.expectEqual(@as(?bool, true), caps.supports_anthropic_cache_ttl);
    try testing.expect(caps.max_tokens_field == null);
    try testing.expect(caps.thinking_format == null);
    try testing.expectEqual(@as(?bool, null), caps.supports_strict_mode);
    try testing.expectEqual(@as(?bool, null), caps.supports_usage_in_streaming);
    try testing.expectEqual(@as(?bool, null), caps.supports_store);
    try testing.expectEqual(@as(?bool, null), caps.supports_developer_role);
    try testing.expectEqual(@as(?bool, null), caps.supports_reasoning_effort);
}

test "an explicit max_tokens_field is carried through" {
    const json =
        \\{"providers":[{
        \\  "id":"gateway","name":"Gateway","api":"openai-completions",
        \\  "base_url":"https://gw.internal",
        \\  "capabilities":{"max_tokens_field":"max_completion_tokens","strict_mode":true}
        \\}]}
    ;
    const providers = try parse(testing.allocator, json);
    defer deinitProviders(testing.allocator, providers);

    const caps = providers[0].compat orelse return error.TestExpectedCapabilities;
    try testing.expect(caps.max_tokens_field.? == .max_completion_tokens);
    try testing.expectEqual(@as(?bool, true), caps.supports_strict_mode);
}

fn validateId(id: []const u8) ConfigError!void {
    if (id.len == 0) return ConfigError.MissingProviderId;
    for (id) |c| {
        const ok = std.ascii.isAlphanumeric(c) or c == '-' or c == '_' or c == '.';
        if (!ok) return ConfigError.InvalidProviderId;
    }
    for (provider_catalog.ids) |reserved| {
        if (std.mem.eql(u8, id, reserved)) return ConfigError.ReservedProviderId;
    }
}

fn isSupportedApi(api: []const u8) bool {
    for (supported_apis) |supported| {
        if (std.mem.eql(u8, api, supported)) return true;
    }
    return false;
}

fn objectString(obj: *const std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = obj.get(key) orelse return null;
    if (value != .string) return null;
    return value.string;
}

test "a declared carries_version is read and an absent one is not stated" {
    const stated = try parse(testing.allocator,
        \\{"providers":[{"id":"zai","base_url":"https://api.z.ai/api/coding/paas/v4","carries_version":true}]}
    );
    defer deinitProviders(testing.allocator, stated);
    try testing.expectEqual(@as(?bool, true), stated[0].carries_version);

    const absent = try parse(testing.allocator,
        \\{"providers":[{"id":"gw","base_url":"https://gw.test"}]}
    );
    defer deinitProviders(testing.allocator, absent);
    try testing.expectEqual(@as(?bool, null), absent[0].carries_version);

    const denied = try parse(testing.allocator,
        \\{"providers":[{"id":"gw","base_url":"https://gw.test","carries_version":false}]}
    );
    defer deinitProviders(testing.allocator, denied);
    try testing.expectEqual(@as(?bool, false), denied[0].carries_version);
}

test "a stated carries_version keeps the read-time /v1 strip from deleting the version" {
    const stated = try parse(testing.allocator,
        \\{"providers":[{"id":"gw","base_url":"https://gw.test/api/v1","carries_version":true}]}
    );
    defer deinitProviders(testing.allocator, stated);
    try testing.expectEqualStrings("https://gw.test/api/v1", stated[0].base_url);
    try testing.expectEqual(@as(?bool, true), stated[0].carries_version);

    const unstated = try parse(testing.allocator,
        \\{"providers":[{"id":"gw","base_url":"https://gw.test/api/v1"}]}
    );
    defer deinitProviders(testing.allocator, unstated);
    try testing.expectEqualStrings("https://gw.test/api", unstated[0].base_url);

    const denied = try parse(testing.allocator,
        \\{"providers":[{"id":"gw","base_url":"https://gw.test/api/v1","carries_version":false}]}
    );
    defer deinitProviders(testing.allocator, denied);
    try testing.expectEqualStrings("https://gw.test/api", denied[0].base_url);
}

fn objectBool(obj: *const std.json.ObjectMap, key: []const u8) ?bool {
    const value = obj.get(key) orelse return null;
    if (value != .bool) return null;
    return value.bool;
}

fn objectU32(obj: *const std.json.ObjectMap, key: []const u8) ?u32 {
    const value = obj.get(key) orelse return null;
    if (value != .integer) return null;
    if (value.integer < 0 or value.integer > std.math.maxInt(u32)) return null;
    return @intCast(value.integer);
}

fn objectObject(obj: *const std.json.ObjectMap, key: []const u8) ?*const std.json.ObjectMap {
    const value = obj.getPtr(key) orelse return null;
    if (value.* != .object) return null;
    return &value.object;
}

const testing = std.testing;

fn parseOne(allocator: std.mem.Allocator, data: []const u8) !CustomProvider {
    const providers = try parse(allocator, data);
    errdefer deinitProviders(allocator, providers);
    try testing.expectEqual(@as(usize, 1), providers.len);
    const single = providers[0];
    allocator.free(providers);
    return single;
}

test "custom providers parse a minimal entry and default the api" {
    var provider = try parseOne(testing.allocator,
        \\{"providers":[{"id":"vllm","base_url":"http://localhost:8000/v1/"}]}
    );
    defer provider.deinit(testing.allocator);

    try testing.expectEqualStrings("vllm", provider.id);
    try testing.expectEqualStrings("vllm", provider.name);
    try testing.expectEqualStrings("openai-completions", provider.api);
    try testing.expectEqualStrings("http://localhost:8000", provider.base_url);
    try testing.expect(provider.env_key == null);
    try testing.expect(provider.compat == null);
    try testing.expectEqual(@as(usize, 0), provider.models.len);
    try testing.expect(provider.allows("anything-at-all"));
}

test "custom providers parse headers models capabilities and auth" {
    var provider = try parseOne(testing.allocator,
        \\{"providers":[{
        \\  "id":"gateway","name":"Internal Gateway","api":"anthropic-messages",
        \\  "base_url":"https://gw.internal/anthropic",
        \\  "auth":{"env":"GATEWAY_TOKEN"},
        \\  "headers":{"X-Tenant":"acme"},
        \\  "reasoning":true,
        \\  "models":["claude-sonnet-4-5",{"id":"claude-opus-4-1","name":"Opus","context_window":200000,"max_tokens":32000}],
        \\  "capabilities":{"cache_ttl":true,"max_tokens_field":"max_tokens","thinking_format":"zai"}
        \\}]}
    );
    defer provider.deinit(testing.allocator);

    try testing.expectEqualStrings("Internal Gateway", provider.name);
    try testing.expectEqualStrings("anthropic-messages", provider.api);
    try testing.expectEqualStrings("GATEWAY_TOKEN", provider.env_key.?);
    try testing.expect(provider.reasoning);

    try testing.expectEqual(@as(usize, 1), provider.headers.len);
    try testing.expectEqualStrings("X-Tenant", provider.headers[0].name);
    try testing.expectEqualStrings("acme", provider.headers[0].value);

    try testing.expectEqual(@as(usize, 2), provider.models.len);
    try testing.expectEqualStrings("claude-sonnet-4-5", provider.models[0].id);
    try testing.expectEqualStrings("claude-sonnet-4-5", provider.models[0].name);
    try testing.expect(provider.models[0].context_window == null);
    try testing.expectEqualStrings("Opus", provider.models[1].name);
    try testing.expectEqual(@as(?u32, 200000), provider.models[1].context_window);

    const caps = provider.compat.?;
    try testing.expectEqual(@as(?bool, true), caps.supports_anthropic_cache_ttl);
    try testing.expect(caps.max_tokens_field.? == .max_tokens);
    try testing.expect(caps.thinking_format.? == .zai);
}

test "custom providers treat a declared model list as an allowlist" {
    var provider = try parseOne(testing.allocator,
        \\{"providers":[{"id":"router","base_url":"https://openrouter.ai/api/v1","models":["a/one","b/two"]}]}
    );
    defer provider.deinit(testing.allocator);

    try testing.expect(provider.allows("a/one"));
    try testing.expect(provider.allows("b/two"));
    try testing.expect(!provider.allows("c/three"));
    try testing.expect(provider.specFor("b/two") != null);
    try testing.expect(provider.specFor("c/three") == null);
}

test "custom providers reject malformed entries" {
    const cases = [_]struct { data: []const u8, want: ConfigError }{
        .{ .data =
        \\{"providers":[{"base_url":"https://x.test"}]}
        , .want = ConfigError.MissingProviderId },
        .{ .data =
        \\{"providers":[{"id":"anthropic","base_url":"https://x.test"}]}
        , .want = ConfigError.ReservedProviderId },
        .{ .data =
        \\{"providers":[{"id":"has space","base_url":"https://x.test"}]}
        , .want = ConfigError.InvalidProviderId },
        .{ .data =
        \\{"providers":[{"id":"ok"}]}
        , .want = ConfigError.MissingBaseUrl },
        .{ .data =
        \\{"providers":[{"id":"ok","base_url":"not a url"}]}
        , .want = ConfigError.InvalidBaseUrl },
        .{ .data =
        \\{"providers":[{"id":"ok","base_url":"https://x.test","api":"google-generative-ai"}]}
        , .want = ConfigError.UnsupportedApi },
        .{ .data =
        \\{"providers":[{"id":"dup","base_url":"https://x.test"},{"id":"dup","base_url":"https://y.test"}]}
        , .want = ConfigError.DuplicateProviderId },
        .{ .data =
        \\not json at all
        , .want = ConfigError.InvalidConfig },
        .{ .data =
        \\{"providers":[{"id":"ok","base_url":"https://x.test","models":[12]}]}
        , .want = ConfigError.InvalidConfig },
        .{ .data =
        \\{"providers":[{"id":"ok","base_url":"https://x.test","auth":"anonymous"}]}
        , .want = ConfigError.InvalidAuthMode },
        .{ .data =
        \\{"providers":[{"id":"ok","base_url":"https://x.test","auth":true}]}
        , .want = ConfigError.InvalidAuthMode },
        .{ .data =
        \\{"providers":[{"id":"ok","base_url":"https://x.test","auth":{"env":5}}]}
        , .want = ConfigError.InvalidAuthMode },
        .{ .data =
        \\{"providers":[{"id":"ok","base_url":"https://x.test","auth":{"env":""}}]}
        , .want = ConfigError.InvalidAuthMode },
        .{ .data =
        \\{"providers":[{"id":"ok","base_url":"https://x.test","auth":{}}]}
        , .want = ConfigError.InvalidAuthMode },
        .{ .data =
        \\{"providers":[{"id":"ok","base_url":"https://x.test","auth":{"environment":"K"}}]}
        , .want = ConfigError.InvalidAuthMode },
    };

    for (cases) |case| {
        try testing.expectError(case.want, parse(testing.allocator, case.data));
    }
}

test "auth none is an explicit opt-in and stays off otherwise" {
    var keyless = try parseOne(testing.allocator,
        \\{"providers":[{"id":"local","base_url":"http://localhost:8000/v1","auth":"none"}]}
    );
    defer keyless.deinit(testing.allocator);
    try testing.expect(keyless.auth_none);
    try testing.expect(keyless.env_key == null);

    var env_keyed = try parseOne(testing.allocator,
        \\{"providers":[{"id":"gw","base_url":"https://gw.test","auth":{"env":"GW_KEY"}}]}
    );
    defer env_keyed.deinit(testing.allocator);
    try testing.expect(!env_keyed.auth_none);
    try testing.expectEqualStrings("GW_KEY", env_keyed.env_key.?);

    var silent = try parseOne(testing.allocator,
        \\{"providers":[{"id":"gw","base_url":"https://gw.test"}]}
    );
    defer silent.deinit(testing.allocator);
    try testing.expect(!silent.auth_none);
    try testing.expect(silent.env_key == null);
}

test "custom providers tolerate an absent or empty providers array" {
    for ([_][]const u8{ "{}", "{\"providers\":[]}" }) |data| {
        const providers = try parse(testing.allocator, data);
        defer deinitProviders(testing.allocator, providers);
        try testing.expectEqual(@as(usize, 0), providers.len);
    }
}

test "an override may move a catalogued row's endpoint and states the fields it changed" {
    var config = try parseConfig(testing.allocator,
        \\{"overrides":[{"id":"deepseek","base_url":"https://proxy.example/api/v1",
        \\ "carries_version":true,"headers":{"X-Tenant":"acme"},"models":["deepseek-chat"]}]}
    );
    defer config.deinit(testing.allocator);

    try testing.expectEqual(@as(usize, 0), config.providers.len);
    try testing.expectEqual(@as(usize, 1), config.overrides.len);

    const override = config.overrides[0];
    try testing.expectEqualStrings("deepseek", override.id);
    try testing.expectEqualStrings("https://proxy.example/api/v1", override.base_url.?);
    try testing.expectEqual(@as(?bool, true), override.carries_version);
    try testing.expectEqual(@as(usize, 1), override.headers.len);
    try testing.expectEqualStrings("X-Tenant", override.headers[0].name);
    try testing.expectEqualStrings("acme", override.headers[0].value);
    try testing.expectEqual(@as(usize, 1), override.models.len);
    try testing.expectEqualStrings("deepseek-chat", override.models[0].id);
}

test "an override forwards no row credential unless it says so, and the flag must be a boolean" {
    var silent = try parseConfig(testing.allocator,
        \\{"overrides":[{"id":"deepseek","base_url":"https://proxy.example/api"}]}
    );
    defer silent.deinit(testing.allocator);
    try testing.expect(!silent.overrides[0].forwards_credential);

    var stated = try parseConfig(testing.allocator,
        \\{"overrides":[{"id":"deepseek","base_url":"https://proxy.example/api","forwards_credential":true}]}
    );
    defer stated.deinit(testing.allocator);
    try testing.expect(stated.overrides[0].forwards_credential);

    try testing.expectError(OverrideError.WrongTypedOverrideMember, parseConfig(testing.allocator,
        \\{"overrides":[{"id":"deepseek","base_url":"https://proxy.example/api","forwards_credential":"yes"}]}
    ));
}

test "an override may not change the id or the wire of the row it overrides" {
    const cases = [_]struct { data: []const u8, want: OverrideError }{
        .{ .data =
        \\{"overrides":[{"id":"deepseek","base_url":"https://x.test","api":"anthropic-messages"}]}
        , .want = OverrideError.ForbiddenOverrideMember },
        .{ .data =
        \\{"overrides":[{"id":"deepseek","base_url":"https://x.test","wire":"openai-completions"}]}
        , .want = OverrideError.ForbiddenOverrideMember },
        .{ .data =
        \\{"overrides":[{"id":"deepseek","base_url":"https://x.test","auth":{"env":"K"}}]}
        , .want = OverrideError.ForbiddenOverrideMember },
        .{ .data =
        \\{"overrides":[{"id":"deepseek","base_url":"https://x.test","name":"Mine"}]}
        , .want = OverrideError.ForbiddenOverrideMember },
        .{ .data =
        \\{"overrides":[{"id":"deepseek","base_url":"https://x.test","capabilities":{"store":true}}]}
        , .want = OverrideError.ForbiddenOverrideMember },
        .{ .data =
        \\{"overrides":[{"id":"deepseek","base_url":"https://x.test","context_window":4096}]}
        , .want = OverrideError.ForbiddenOverrideMember },
        .{ .data =
        \\{"overrides":[{"id":"deepseek","base_url":"https://x.test","max_tokens":99}]}
        , .want = OverrideError.ForbiddenOverrideMember },
        .{ .data =
        \\{"overrides":[{"id":"deepseek","base_url":"https://x.test","reasoning":true}]}
        , .want = OverrideError.ForbiddenOverrideMember },
        .{ .data =
        \\{"overrides":[{"id":"deepseek","base_url":"https://x.test","headers":{"A":"1"},"passthrough":true}]}
        , .want = OverrideError.ForbiddenOverrideMember },
    };
    for (cases) |case| {
        try testing.expectError(case.want, parseConfig(testing.allocator, case.data));
    }
}

test "an override must name a catalogued row and may not name one twice" {
    const cases = [_]struct { data: []const u8, want: OverrideError }{
        .{ .data =
        \\{"overrides":[{"base_url":"https://x.test"}]}
        , .want = OverrideError.MissingProviderId },
        .{ .data =
        \\{"overrides":[{"id":"not-a-catalog-row","base_url":"https://x.test"}]}
        , .want = OverrideError.UnknownProviderId },
        .{ .data =
        \\{"overrides":[{"id":"deepseek","base_url":"https://x.test"},
        \\ {"id":"deepseek","base_url":"https://y.test"}]}
        , .want = OverrideError.DuplicateOverride },
        .{ .data =
        \\{"overrides":[{"id":"deepseek","base_url":"not a url"}]}
        , .want = OverrideError.InvalidBaseUrl },
        .{ .data =
        \\{"overrides":[{"id":"github-copilot","base_url":"https://x.test"}]}
        , .want = OverrideError.UnsupportedOverrideRow },
        .{ .data =
        \\{"overrides":["deepseek"]}
        , .want = OverrideError.InvalidConfig },
        .{ .data =
        \\{"overrides":{"id":"deepseek"}}
        , .want = OverrideError.InvalidConfig },
    };
    for (cases) |case| {
        try testing.expectError(case.want, parseConfig(testing.allocator, case.data));
    }
}

test "an override may change only its endpoint, so one that names no base url still parses" {
    var narrowed = try parseConfig(testing.allocator,
        \\{"overrides":[{"id":"deepseek","models":["deepseek-chat"]}]}
    );
    defer narrowed.deinit(testing.allocator);
    try testing.expect(narrowed.overrides[0].base_url == null);
    try testing.expectEqual(@as(usize, 1), narrowed.overrides[0].models.len);
}

test "an override states where its version sits, so no request URL is a guess" {
    var stated = try parseConfig(testing.allocator,
        \\{"overrides":[{"id":"deepseek","base_url":"https://proxy.example/api/v1","carries_version":true}]}
    );
    defer stated.deinit(testing.allocator);
    try testing.expectEqualStrings("https://proxy.example/api/v1", stated.overrides[0].base_url.?);
    try testing.expectEqual(@as(?bool, true), stated.overrides[0].carries_version);

    var denied = try parseConfig(testing.allocator,
        \\{"overrides":[{"id":"deepseek","base_url":"https://proxy.example/api/v1","carries_version":false}]}
    );
    defer denied.deinit(testing.allocator);
    try testing.expectEqualStrings("https://proxy.example/api", denied.overrides[0].base_url.?);
    try testing.expectEqual(@as(?bool, false), denied.overrides[0].carries_version);
}

test "a caller that reads only the providers leaves nothing behind" {
    var config = try parseConfig(testing.allocator,
        \\{"providers":[{"id":"gw","base_url":"https://gw.test"}],
        \\ "overrides":[{"id":"deepseek","base_url":"https://proxy.example","headers":{"X-Tenant":"acme"},
        \\ "models":["deepseek-chat"]}]}
    );
    const providers = config.takeProviders(testing.allocator);
    defer deinitProviders(testing.allocator, providers);
    try testing.expectEqual(@as(usize, 1), providers.len);
    try testing.expectEqual(@as(usize, 0), providers[0].headers.len);
    try testing.expectEqual(@as(usize, 0), config.overrides.len);

    const from_parse = try parse(testing.allocator,
        \\{"providers":[{"id":"gw","base_url":"https://gw.test"}],
        \\ "overrides":[{"id":"deepseek","base_url":"https://proxy.example","headers":{"X-Tenant":"acme"}}]}
    );
    defer deinitProviders(testing.allocator, from_parse);
    try testing.expectEqual(@as(usize, 1), from_parse.len);
}

test "an override member of the wrong type is refused rather than read as absent" {
    const cases = [_][]const u8{
        "{\"overrides\":[{\"id\":\"deepseek\",\"base_url\":123}]}",
        "{\"overrides\":[{\"id\":\"deepseek\",\"base_url\":\"https://x.test\",\"carries_version\":\"yes\"}]}",
        "{\"overrides\":[{\"id\":\"deepseek\",\"carries_version\":1}]}",
        "{\"overrides\":[{\"id\":\"deepseek\",\"base_url\":\"https://x.test\",\"headers\":[{\"A\":\"1\"}]}]}",
        "{\"overrides\":[{\"id\":123,\"base_url\":\"https://x.test\"}]}",
        "{\"overrides\":[{\"id\":\"deepseek\",\"models\":\"deepseek-chat\"}]}",
    };
    for (cases) |data| {
        try testing.expectError(OverrideError.WrongTypedOverrideMember, parseConfig(testing.allocator, data));
    }
}

test "a wrong-typed member is refused rather than read as an unstated one" {
    try testing.expectError(
        OverrideError.WrongTypedOverrideMember,
        parseConfig(testing.allocator, "{\"overrides\":[{\"id\":\"deepseek\",\"carries_version\":\"true\"}]}"),
    );
}

test "an override is found by the row it names and by no other row" {
    var config = try parseConfig(testing.allocator,
        \\{"overrides":[{"id":"deepseek","base_url":"https://proxy.example"},
        \\ {"id":"openai","base_url":"https://gw.example"}]}
    );
    defer config.deinit(testing.allocator);

    const found = overrideFor(config.overrides, "deepseek") orelse return error.TestExpectedOverride;
    try testing.expectEqualStrings("https://proxy.example", found.base_url.?);
    try testing.expect(overrideFor(config.overrides, "openai") != null);
    try testing.expect(overrideFor(config.overrides, "kimi") == null);
    try testing.expect(overrideFor(&.{}, "deepseek") == null);
}

test "an absent or empty overrides array is not an error" {
    for ([_][]const u8{ "{}", "{\"providers\":[]}", "{\"overrides\":[]}" }) |data| {
        var config = try parseConfig(testing.allocator, data);
        defer config.deinit(testing.allocator);
        try testing.expectEqual(@as(usize, 0), config.overrides.len);
    }
}

fn parseProbe(allocator: std.mem.Allocator) !void {
    const providers = try parse(allocator,
        \\{"providers":[
        \\ {"id":"one","base_url":"https://one.test","headers":{"A":"1","B":"2"},"models":["m1",{"id":"m2"}]},
        \\ {"id":"two","name":"Two","base_url":"https://two.test","auth":{"env":"K"},"capabilities":{"store":true}}
        \\]}
    );
    defer deinitProviders(allocator, providers);
    try testing.expectEqual(@as(usize, 2), providers.len);
}

test "custom providers free every allocation when parsing fails midway" {
    try parseProbe(testing.allocator);
    try testing.checkAllAllocationFailures(testing.allocator, parseProbe, .{});
}

fn parseConfigProbe(allocator: std.mem.Allocator) !void {
    var config = try parseConfig(allocator,
        \\{"providers":[{"id":"gw","base_url":"https://gw.test"}],
        \\ "overrides":[{"id":"deepseek","base_url":"https://proxy.example/api/v1","carries_version":true,
        \\ "headers":{"X-Tenant":"acme"},"models":["deepseek-chat"]}]}
    );
    defer config.deinit(allocator);
    try testing.expectEqual(@as(usize, 1), config.providers.len);
    try testing.expectEqual(@as(usize, 1), config.overrides.len);
}

test "an override frees every allocation when parsing fails midway" {
    try parseConfigProbe(testing.allocator);
    try testing.checkAllAllocationFailures(testing.allocator, parseConfigProbe, .{});
}

test "custom providers normalise a versioned base url to the origin the providers expect" {
    const cases = [_]struct { given: []const u8, want: []const u8 }{
        .{ .given = "https://api.groq.com/openai/v1", .want = "https://api.groq.com/openai" },
        .{ .given = "https://api.groq.com/openai/v1/", .want = "https://api.groq.com/openai" },
        .{ .given = "http://localhost:8000/v1", .want = "http://localhost:8000" },
        .{ .given = "https://gw.internal/anthropic", .want = "https://gw.internal/anthropic" },
    };
    for (cases) |case| {
        const data = try std.fmt.allocPrint(testing.allocator,
            \\{{"providers":[{{"id":"probe","base_url":"{s}"}}]}}
        , .{case.given});
        defer testing.allocator.free(data);
        var provider = try parseOne(testing.allocator, data);
        defer provider.deinit(testing.allocator);
        try testing.expectEqualStrings(case.want, provider.base_url);
    }
}

test "appendProvider writes a provider the loader reads back field for field" {
    const text = try appendProvider(testing.allocator, null, .{ .id = "gateway", .base_url = "https://gw.test/v1", .api = "openai-responses", .env = "GW_KEY" });
    defer testing.allocator.free(text);
    const providers = try parse(testing.allocator, text);
    defer deinitProviders(testing.allocator, providers);
    try testing.expectEqual(@as(usize, 1), providers.len);
    try testing.expectEqualStrings("gateway", providers[0].id);
    try testing.expectEqualStrings("openai-responses", providers[0].api);
    try testing.expectEqualStrings("GW_KEY", providers[0].env_key.?);
    try testing.expect(!providers[0].auth_none);

    const keyless = try appendProvider(testing.allocator, null, .{ .id = "local", .base_url = "http://127.0.0.1:8080", .auth_none = true });
    defer testing.allocator.free(keyless);
    const local = try parse(testing.allocator, keyless);
    defer deinitProviders(testing.allocator, local);
    try testing.expect(local[0].auth_none);
    try testing.expectEqualStrings("openai-completions", local[0].api);
}

test "appendProvider keeps every existing entry, member and override as written" {
    const existing =
        \\{"providers":[{"id":"first","base_url":"https://one.test/v1","x-note":"kept"}],
        \\ "overrides":[{"id":"deepseek","base_url":"https://proxy.test"}]}
    ;
    const text = try appendProvider(testing.allocator, existing, .{ .id = "second", .base_url = "https://two.test" });
    defer testing.allocator.free(text);
    try testing.expect(std.mem.indexOf(u8, text, "\"https://one.test/v1\"") != null);
    try testing.expect(std.mem.indexOf(u8, text, "\"x-note\": \"kept\"") != null);
    var config = try parseConfig(testing.allocator, text);
    defer config.deinit(testing.allocator);
    try testing.expectEqual(@as(usize, 2), config.providers.len);
    try testing.expectEqualStrings("first", config.providers[0].id);
    try testing.expectEqualStrings("second", config.providers[1].id);
    try testing.expectEqual(@as(usize, 1), config.overrides.len);
}

test "appendProvider refuses what the loader refuses, with the loader's error" {
    const existing =
        \\{"providers":[{"id":"gateway","base_url":"https://gw.test"}]}
    ;
    try testing.expectError(ConfigError.ReservedProviderId, appendProvider(testing.allocator, null, .{ .id = "openai", .base_url = "https://x.test" }));
    try testing.expectError(ConfigError.InvalidProviderId, appendProvider(testing.allocator, null, .{ .id = "my gateway", .base_url = "https://x.test" }));
    try testing.expectError(ConfigError.DuplicateProviderId, appendProvider(testing.allocator, existing, .{ .id = "gateway", .base_url = "https://x.test" }));
    try testing.expectError(ConfigError.UnsupportedApi, appendProvider(testing.allocator, null, .{ .id = "gw", .base_url = "https://x.test", .api = "grpc" }));
    try testing.expectError(ConfigError.InvalidAuthMode, appendProvider(testing.allocator, null, .{ .id = "gw", .base_url = "https://x.test", .env = "" }));
    try testing.expectError(ConfigError.InvalidConfig, appendProvider(testing.allocator, "{\"providers\":{}}", .{ .id = "gw", .base_url = "https://x.test" }));
}

fn appendProviderProbe(allocator: std.mem.Allocator) !void {
    const text = try appendProvider(allocator, "{\"providers\":[{\"id\":\"first\",\"base_url\":\"https://one.test\"}]}", .{ .id = "second", .base_url = "https://two.test", .env = "KEY" });
    allocator.free(text);
}

test "appendProvider frees what it built on every allocation failure" {
    try testing.checkAllAllocationFailures(std.heap.smp_allocator, appendProviderProbe, .{});
}

test "dropProvider removes only the named provider and keeps the rest as written" {
    const existing =
        \\{"providers":[{"id":"first","base_url":"https://one.test/v1","x-note":"kept"},{"id":"second","base_url":"https://two.test"}],
        \\ "overrides":[{"id":"deepseek","base_url":"https://proxy.test"}]}
    ;
    const text = try dropProvider(testing.allocator, existing, "second");
    defer testing.allocator.free(text);
    try testing.expect(std.mem.indexOf(u8, text, "\"x-note\": \"kept\"") != null);
    var config = try parseConfig(testing.allocator, text);
    defer config.deinit(testing.allocator);
    try testing.expectEqual(@as(usize, 1), config.providers.len);
    try testing.expectEqualStrings("first", config.providers[0].id);
    try testing.expectEqual(@as(usize, 1), config.overrides.len);

    try testing.expectError(DeleteError.ProviderNotDeclared, dropProvider(testing.allocator, existing, "third"));
    try testing.expectError(DeleteError.ProviderNotDeclared, dropProvider(testing.allocator, "{}", "first"));
    try testing.expectError(ConfigError.InvalidConfig, dropProvider(testing.allocator, "{\"providers\":{}}", "first"));
}

fn dropProviderProbe(allocator: std.mem.Allocator) !void {
    const text = try dropProvider(allocator, "{\"providers\":[{\"id\":\"first\",\"base_url\":\"https://one.test\"},{\"id\":\"second\",\"base_url\":\"https://two.test\"}]}", "first");
    allocator.free(text);
}

test "dropProvider frees what it built on every allocation failure" {
    try testing.checkAllAllocationFailures(std.heap.smp_allocator, dropProviderProbe, .{});
}
