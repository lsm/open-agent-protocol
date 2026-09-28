const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const event_stream = @import("event_stream");
const api_registry = @import("api_registry");
const register_builtins = @import("register_builtins");
const stream_mod = @import("stream");
const test_helpers = @import("test_helpers");
const provider_catalog = @import("provider_catalog");
const provider_credential = @import("provider_credential");
const provider_base_url = @import("provider_base_url");

const testing = std.testing;

const opt_in_env = "OAP_PROVIDER_SMOKE";
const opt_in_model_env = "OAP_PROVIDER_SMOKE_MODEL";

const Row = struct {
    id: []const u8,
    model: []const u8,
};

const rows = [_]Row{
    .{ .id = "openai", .model = "gpt-4o-mini" },
    .{ .id = "anthropic", .model = "claude-haiku-4-5-20251001" },
    .{ .id = "deepseek", .model = "deepseek-chat" },
    .{ .id = "kimi", .model = "kimi-k2.7-code" },
    .{ .id = "zai-coding-plan", .model = "glm-4.6" },
    .{ .id = "opencode", .model = "grok-code-fast-1" },
    .{ .id = "openrouter", .model = "openai/gpt-4o-mini" },
};

fn envOwned(allocator: std.mem.Allocator, name: []const u8) ?[]u8 {
    return compat.getEnvVarOwned(allocator, name) catch null;
}

fn envPresent(allocator: std.mem.Allocator, name: []const u8) bool {
    const value = envOwned(allocator, name) orelse return false;
    defer allocator.free(value);
    return value.len > 0;
}

fn rowFor(id: []const u8) ?Row {
    for (rows) |row| {
        if (std.mem.eql(u8, row.id, id)) return row;
    }
    return null;
}

fn optedIn(allocator: std.mem.Allocator) ?Row {
    if (envPresent(allocator, "CI")) {
        std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: the provider smoke gate never runs in CI\n", .{});
        return null;
    }
    const id = envOwned(allocator, opt_in_env) orelse {
        std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: set {s}=<row> to run a row's smoke gate\n", .{opt_in_env});
        return null;
    };
    defer allocator.free(id);
    if (id.len == 0) {
        std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: {s} is set but names no row\n", .{opt_in_env});
        return null;
    }
    const row = rowFor(id) orelse {
        std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: {s} names no row this gate knows; the rows are\n", .{opt_in_env});
        for (rows) |known| std.debug.print("  {s}\n", .{known.id});
        return null;
    };
    return row;
}

const Fixture = struct {
    row: Row,
    key: []u8,
    model: ai_types.Model,
    registry: api_registry.ApiRegistry,
    allocator: std.mem.Allocator,

    fn deinit(self: *Fixture) void {
        std.crypto.secureZero(u8, self.key);
        self.allocator.free(self.key);
        var owned = self.model;
        owned.deinit(self.allocator);
        self.registry.deinit();
        self.* = undefined;
    }

    fn options(self: *const Fixture, max_tokens: u32) ai_types.StreamOptions {
        return .{
            .api_key = ai_types.OwnedSlice(u8).initBorrowed(self.key),
            .max_tokens = max_tokens,
            .temperature = 0.0,
        };
    }
};

fn prepare(allocator: std.mem.Allocator) !?Fixture {
    const row = optedIn(allocator) orelse return null;
    const target = (try catalogTargetInRegion(row.id, regionFor(allocator, row.id))) orelse {
        std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: {s} has no endpoint the catalog resolves\n", .{row.id});
        return null;
    };

    var environment: std.ArrayList(provider_credential.EnvironmentValue) = .empty;
    defer {
        for (environment.items) |held| allocator.free(held.value);
        environment.deinit(allocator);
    }
    for (provider_catalog.credentialEnv(row.id)) |name| {
        const value = envOwned(allocator, name) orelse continue;
        if (value.len == 0) {
            allocator.free(value);
            continue;
        }
        try environment.append(allocator, .{ .name = name, .value = value });
    }

    var loaded: ?provider_credential.AuthStorage = provider_credential.AuthStorage.loadDefaultStoredOnly(allocator) catch null;
    defer if (loaded) |*held| held.deinit();
    const storage: ?*provider_credential.AuthStorage = if (loaded) |*held| held else null;

    const found = try provider_credential.lookup(allocator, environment.items, storage, row.id);
    if (found == null) {
        std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: {s} has no credential; set one of ", .{row.id});
        for (provider_catalog.credentialEnv(row.id)) |name| std.debug.print("{s} ", .{name});
        std.debug.print("or log in first\n", .{});
        return null;
    }
    var credential = found.?;
    defer credential.deinit(allocator);

    const model_id = envOwned(allocator, opt_in_model_env) orelse try allocator.dupe(u8, row.model);
    defer allocator.free(model_id);

    const owned_key = try allocator.dupe(u8, credential.key);

    const id = try allocator.dupe(u8, model_id);
    errdefer allocator.free(id);
    const name = try allocator.dupe(u8, model_id);
    errdefer allocator.free(name);
    const api = try allocator.dupe(u8, target.wire);
    errdefer allocator.free(api);
    const provider = try allocator.dupe(u8, row.id);
    errdefer allocator.free(provider);
    const base_url = try allocator.dupe(u8, target.base_url);
    errdefer allocator.free(base_url);
    const input = try allocator.alloc([]const u8, 1);
    errdefer allocator.free(input);
    input[0] = try allocator.dupe(u8, "text");

    var registry = api_registry.ApiRegistry.init(allocator);
    errdefer registry.deinit();
    try register_builtins.registerBuiltInApiProviders(&registry);

    return .{
        .row = row,
        .key = owned_key,
        .model = .{
            .id = id,
            .name = name,
            .api = api,
            .provider = provider,
            .base_url = base_url,
            .reasoning = false,
            .input = input,
            .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
            .context_window = 128_000,
            .max_tokens = 64,
            .is_owned = true,
        },
        .registry = registry,
        .allocator = allocator,
    };
}

const CatalogTarget = struct { wire: []const u8, base_url: []const u8 };

fn regionFor(allocator: std.mem.Allocator, id: []const u8) ?[]const u8 {
    const row = provider_catalog.provider(id) orelse return null;
    if (row.endpoints.len == 0 or row.endpoints[0].region == null) return null;
    const name = provider_catalog.regionEnv(id) orelse return row.endpoints[0].region;
    const value = envOwned(allocator, name) orelse return row.endpoints[0].region;
    defer allocator.free(value);
    return provider_base_url.normalizeKimiRegion(value) orelse row.endpoints[0].region;
}

fn catalogTargetInRegion(id: []const u8, region: ?[]const u8) !?CatalogTarget {
    const row = provider_catalog.provider(id) orelse return null;
    for (row.wires) |wire| {
        if (provider_catalog.wirePath(wire) == null) continue;
        if (provider_catalog.requestUrl(id, wire, region) == null) continue;
        const base_url = provider_catalog.baseUrl(id, wire, region) orelse continue;
        return .{ .wire = wire, .base_url = base_url };
    }
    return null;
}

fn catalogTargetFor(id: []const u8) ?CatalogTarget {
    return catalogTargetInRegion(id, regionFor(testing.allocator, id)) catch null;
}

fn report(row: []const u8, case: []const u8, ok: bool) void {
    if (ok) {
        std.debug.print("\x1b[32mPASS\x1b[0m {s}: {s}\n", .{ row, case });
    } else {
        std.debug.print("\x1b[91mFAIL\x1b[0m {s}: {s}\n", .{ row, case });
    }
}

fn textOf(message: ai_types.AssistantMessage) []const u8 {
    for (message.content) |part| {
        switch (part) {
            .text => |t| {
                if (t.text.len > 0) return t.text;
            },
            else => {},
        }
    }
    return "";
}

fn toolNameIn(message: ai_types.AssistantMessage, wanted: []const u8) bool {
    for (message.content) |part| {
        switch (part) {
            .tool_call => |call| {
                if (std.mem.eql(u8, call.name, wanted)) return true;
            },
            else => {},
        }
    }
    return false;
}

test "provider smoke: one completion" {
    var fixture = (try prepare(testing.allocator)) orelse return error.SkipZigTest;
    defer fixture.deinit();

    const messages = [_]ai_types.Message{.{ .user = .{
        .content = .{ .text = "Reply with: ok" },
        .timestamp = compat.time.nowSeconds(),
    } }};
    const ctx = ai_types.Context{ .messages = &messages };

    const stream = try stream_mod.stream(&fixture.registry, fixture.model, ctx, fixture.options(32), testing.allocator);
    defer _ = stream.deinitAndDestroy();

    var result = try test_helpers.waitForResult(testing.allocator, stream, fixture.row.id);
    defer ai_types.deinitAssistantMessageOwned(testing.allocator, &result);

    const saw_text = textOf(result).len > 0;
    report(fixture.row.id, "completion", saw_text);
    try testing.expect(saw_text);
}

test "provider smoke: a streamed completion arrives as deltas" {
    var fixture = (try prepare(testing.allocator)) orelse return error.SkipZigTest;
    defer fixture.deinit();

    const messages = [_]ai_types.Message{.{ .user = .{
        .content = .{ .text = "Count from one to twenty, one number per line, and nothing else." },
        .timestamp = compat.time.nowSeconds(),
    } }};
    const ctx = ai_types.Context{ .messages = &messages };

    const stream = try stream_mod.stream(&fixture.registry, fixture.model, ctx, fixture.options(128), testing.allocator);
    defer _ = stream.deinitAndDestroy();

    var deltas: usize = 0;
    const deadline = test_helpers.createDeadline(test_helpers.DEFAULT_E2E_TIMEOUT_MS);
    while (!stream.isDone()) {
        if (test_helpers.isDeadlineExceeded(deadline)) {
            report(fixture.row.id, "streamed deltas", false);
            return error.TimeoutExceeded;
        }
        while (stream.poll()) |event| {
            switch (event) {
                .text_delta, .thinking_delta => deltas += 1,
                else => {},
            }
        }
        compat.time.sleepNs(5 * std.time.ns_per_ms);
    }
    if (stream.getError()) |err| {
        report(fixture.row.id, "streamed deltas", false);
        std.debug.print("\n\x1b[91mFAILED\x1b[0m {s}: stream error {s}\n", .{ fixture.row.id, err });
        return error.TestFailed;
    }

    report(fixture.row.id, "streamed deltas", deltas > 1);
    try testing.expect(deltas > 1);
}

test "provider smoke: one tool call" {
    var fixture = (try prepare(testing.allocator)) orelse return error.SkipZigTest;
    defer fixture.deinit();

    const tools = [_]ai_types.Tool{.{
        .name = "record_quirk",
        .description = "Record one provider quirk",
        .parameters_schema_json = "{\"type\":\"object\",\"properties\":{\"quirk\":{\"type\":\"string\"}},\"required\":[\"quirk\"]}",
    }};
    const messages = [_]ai_types.Message{.{ .user = .{
        .content = .{ .text = "Call record_quirk with quirk set to whatever you notice first." },
        .timestamp = compat.time.nowSeconds(),
    } }};
    const ctx = ai_types.Context{ .messages = &messages, .tools = &tools };

    const stream = try stream_mod.stream(&fixture.registry, fixture.model, ctx, fixture.options(128), testing.allocator);
    defer _ = stream.deinitAndDestroy();

    var result = try test_helpers.waitForResult(testing.allocator, stream, fixture.row.id);
    defer ai_types.deinitAssistantMessageOwned(testing.allocator, &result);

    const saw_tool = toolNameIn(result, "record_quirk");
    report(fixture.row.id, "tool call", saw_tool);
    try testing.expect(saw_tool);
}

test "provider smoke: an unknown model is refused" {
    var fixture = (try prepare(testing.allocator)) orelse return error.SkipZigTest;
    defer fixture.deinit();

    const unknown_id = try testing.allocator.dupe(u8, "oapx-provider-smoke-no-such-model");
    const unknown_name = try testing.allocator.dupe(u8, "oapx-provider-smoke-no-such-model");
    const api = try testing.allocator.dupe(u8, fixture.model.api);
    const provider = try testing.allocator.dupe(u8, fixture.model.provider);
    const base_url = try testing.allocator.dupe(u8, fixture.model.base_url);
    const input = try testing.allocator.alloc([]const u8, 1);
    input[0] = try testing.allocator.dupe(u8, "text");
    var refused = ai_types.Model{
        .id = unknown_id,
        .name = unknown_name,
        .api = api,
        .provider = provider,
        .base_url = base_url,
        .reasoning = false,
        .input = input,
        .cost = fixture.model.cost,
        .context_window = fixture.model.context_window,
        .max_tokens = 16,
        .is_owned = true,
    };
    defer refused.deinit(testing.allocator);

    const messages = [_]ai_types.Message{.{ .user = .{
        .content = .{ .text = "Reply with: ok" },
        .timestamp = compat.time.nowSeconds(),
    } }};
    const ctx = ai_types.Context{ .messages = &messages };

    const stream = stream_mod.stream(&fixture.registry, refused, ctx, fixture.options(16), testing.allocator) catch {
        report(fixture.row.id, "unknown model refused", true);
        return;
    };
    defer _ = stream.deinitAndDestroy();

    const deadline = test_helpers.createDeadline(test_helpers.DEFAULT_E2E_TIMEOUT_MS);
    while (!stream.isDone()) {
        if (test_helpers.isDeadlineExceeded(deadline)) {
            report(fixture.row.id, "unknown model refused", false);
            return error.TimeoutExceeded;
        }
        _ = stream.poll();
        compat.time.sleepNs(10 * std.time.ns_per_ms);
    }

    const refused_it = stream.getError() != null;
    report(fixture.row.id, "unknown model refused", refused_it);
    try testing.expect(refused_it);
}

test "the smoke gate lists every current row and no other" {
    try testing.expectEqual(provider_catalog.current_ids.len, rows.len);
    for (rows) |row| {
        try testing.expect(provider_catalog.status(row.id) == .current);
        try testing.expect(rowFor(row.id) != null);
        try testing.expect(catalogTargetFor(row.id) != null);
    }
    for (provider_catalog.current_ids) |id| {
        if (rowFor(id) == null) {
            std.debug.print("\n{s} is current and this gate does not list it\n", .{id});
            return error.TestCurrentRowNotGated;
        }
    }
    try testing.expect(rowFor("google") == null);
    try testing.expect(rowFor("no-such-provider") == null);
    try testing.expect(catalogTargetFor("no-such-provider") == null);
}

test "a regional row answers only for the region it is asked for" {
    try testing.expectEqualStrings("https://api.kimi.com/coding", (try catalogTargetInRegion("kimi", "china")).?.base_url);
    try testing.expectEqualStrings("https://api.moonshot.ai", (try catalogTargetInRegion("kimi", "global")).?.base_url);
    try testing.expect((try catalogTargetInRegion("kimi", "atlantis")) == null);
    try testing.expect((try catalogTargetInRegion("kimi", null)) == null);
    try testing.expect(try catalogTargetInRegion("deepseek", "any") == null);
    try testing.expect(try catalogTargetInRegion("deepseek", null) != null);
}
