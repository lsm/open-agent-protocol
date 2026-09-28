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

fn rowFor(id: []const u8) ?Row {
    for (rows) |row| {
        if (std.mem.eql(u8, row.id, id)) return row;
    }
    return null;
}

fn optedIn() ?Row {
    if (envOwned(testing.allocator, "CI") != null) {
        std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: the provider smoke gate never runs in CI\n", .{});
        return null;
    }
    const id = envOwned(testing.allocator, opt_in_env) orelse {
        std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: set {s}=<row> to run a row's smoke gate\n", .{opt_in_env});
        return null;
    };
    defer testing.allocator.free(id);
    if (id.len == 0) {
        std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: {s} is set but names no row\n", .{opt_in_env});
        return null;
    }
    const row = rowFor(id) orelse {
        std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: {s} names no row this gate knows; the rows are\n", .{opt_in_env});
        for (rows) |known| std.debug.print("  {s}\n", .{known.id});
        return null;
    };
    if (envOwned(testing.allocator, "OAPX_KEYCHAIN_SERVICE") == null and
        envOwned(testing.allocator, "HOME") == null)
    {
        return null;
    }
    return row;
}

const Fixture = struct {
    row: Row,
    key: []const u8,
    model: ai_types.Model,
    registry: api_registry.ApiRegistry,

    fn deinit(self: *Fixture, allocator: std.mem.Allocator) void {
        var owned = self.model;
        owned.deinit(allocator);
        self.registry.deinit();
    }
};

fn prepare(allocator: std.mem.Allocator) !?Fixture {
    const row = optedIn() orelse return null;
    const target = (try catalogTargetInRegion(row.id, regionFor(allocator, row.id))) orelse {
        std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: {s} has no endpoint the catalog resolves\n", .{row.id});
        return null;
    };

    var environment: std.ArrayList(provider_credential.EnvironmentValue) = .empty;
    defer {
        for (environment.items) |held| allocator.free(held.value);
        environment.deinit(allocator);
    }
    const names = provider_catalog.credentialEnv(row.id);
    for (names) |name| {
        const value = try compat.getEnvVarOwned(allocator, name);
        if (value.len == 0) {
            allocator.free(value);
            continue;
        }
        try environment.append(allocator, .{ .name = name, .value = value });
    }

    var loaded: ?provider_credential.AuthStorage = provider_credential.AuthStorage.loadDefaultStoredOnly(allocator) catch null;
    defer if (loaded) |*held| held.deinit();
    const storage: ?*provider_credential.AuthStorage = if (loaded) |*held| held else null;

    var credential = (try provider_credential.lookup(allocator, environment.items, storage, row.id)) orelse {
        std.debug.print(
            "\n\x1b[90mSKIPPED\x1b[0m: {s} has no credential; set {s} or log in first\n",
            .{ row.id, opt_in_env },
        );
        return null;
    };
    defer credential.deinit(allocator);

    const model_id = envOwned(allocator, opt_in_model_env) orelse try allocator.dupe(u8, row.model);
    defer allocator.free(model_id);

    var registry = api_registry.ApiRegistry.init(allocator);
    errdefer registry.deinit();
    try register_builtins.registerBuiltInApiProviders(&registry);

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

    return .{
        .row = row,
        .key = credential.key,
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
    };
}

const CatalogTarget = struct { wire: []const u8, base_url: []const u8 };

fn regionFor(allocator: std.mem.Allocator, id: []const u8) ?[]const u8 {
    const row = provider_catalog.provider(id) orelse return null;
    if (row.endpoints.len == 0) return null;
    if (row.endpoints[0].region == null) return null;
    const name = provider_catalog.regionEnv(id) orelse return row.endpoints[0].region;
    const value = envOwned(allocator, name) orelse return row.endpoints[0].region;
    defer allocator.free(value);
    const trimmed = std.mem.trim(u8, value, " \t\r\n");
    if (std.ascii.eqlIgnoreCase(trimmed, "global") or std.ascii.eqlIgnoreCase(trimmed, "moonshot")) return "global";
    if (std.ascii.eqlIgnoreCase(trimmed, "china") or std.ascii.eqlIgnoreCase(trimmed, "cn")) return "china";
    return row.endpoints[0].region;
}

fn catalogTargetFor(id: []const u8) ?CatalogTarget {
    return catalogTargetInRegion(id, regionFor(testing.allocator, id)) catch null;
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

fn waitResult(
    allocator: std.mem.Allocator,
    row_id: []const u8,
    stream: *event_stream.AssistantMessageEventStream,
) !ai_types.AssistantMessage {
    const deadline = test_helpers.createDeadline(test_helpers.DEFAULT_E2E_TIMEOUT_MS);
    while (!stream.isDone()) {
        if (test_helpers.isDeadlineExceeded(deadline)) return error.TimeoutExceeded;
        _ = stream.poll();
        compat.time.sleepNs(10 * std.time.ns_per_ms);
    }
    if (stream.getError()) |err| {
        std.debug.print("\n\x1b[91mFAILED\x1b[0m {s}: stream error {s}\n", .{ row_id, err });
        return error.TestFailed;
    }
    return ai_types.cloneAssistantMessage(allocator, stream.getResult() orelse return error.NoResult);
}

fn prompt(text: []const u8) ai_types.Context {
    const user = ai_types.Message{ .user = .{
        .content = .{ .text = text },
        .timestamp = compat.time.nowSeconds(),
    } };
    return .{ .messages = &[_]ai_types.Message{user} };
}

fn report(row: []const u8, case: []const u8, ok: bool) void {
    if (ok) {
        std.debug.print("\x1b[32mPASS\x1b[0m {s}: {s}\n", .{ row, case });
    } else {
        std.debug.print("\x1b[91mFAIL\x1b[0m {s}: {s}\n", .{ row, case });
    }
}

test "provider smoke: one completion" {
    var fixture = (try prepare(testing.allocator)) orelse return error.SkipZigTest;
    defer fixture.deinit(testing.allocator);

    const ctx = prompt("Reply with: ok");
    const stream = try stream_mod.stream(&fixture.registry, fixture.model, ctx, .{
        .api_key = ai_types.OwnedSlice(u8).initBorrowed(fixture.key),
        .max_tokens = 32,
        .temperature = 0.0,
    }, testing.allocator);
    defer _ = stream.deinitAndDestroy();

    var result = try waitResult(testing.allocator, fixture.row.id, stream);
    defer ai_types.deinitAssistantMessageOwned(testing.allocator, &result);

    var saw_text = false;
    for (result.content) |part| {
        switch (part) {
            .text => |t| {
                if (t.text.len > 0) saw_text = true;
            },
            else => {},
        }
    }
    report(fixture.row.id, "completion", saw_text);
    try testing.expect(saw_text);
}

test "provider smoke: a streamed completion arrives as deltas" {
    var fixture = (try prepare(testing.allocator)) orelse return error.SkipZigTest;
    defer fixture.deinit(testing.allocator);

    const ctx = prompt("Count from one to twenty, one number per line, and nothing else.");
    const stream = try stream_mod.stream(&fixture.registry, fixture.model, ctx, .{
        .api_key = ai_types.OwnedSlice(u8).initBorrowed(fixture.key),
        .max_tokens = 128,
        .temperature = 0.0,
    }, testing.allocator);
    defer _ = stream.deinitAndDestroy();

    var deltas: usize = 0;
    while (!stream.isDone()) {
        while (stream.poll()) |event| {
            switch (event) {
                .text_delta, .thinking_delta => deltas += 1,
                else => {},
            }
        }
        compat.time.sleepNs(5 * std.time.ns_per_ms);
    }

    report(fixture.row.id, "streamed deltas", deltas > 1);
    try testing.expect(deltas > 1);
}

test "provider smoke: one tool call" {
    var fixture = (try prepare(testing.allocator)) orelse return error.SkipZigTest;
    defer fixture.deinit(testing.allocator);

    const tools = [_]ai_types.Tool{.{
        .name = "record_quirk",
        .description = "Record one provider quirk",
        .parameters_schema_json = "{\"type\":\"object\",\"properties\":{\"quirk\":{\"type\":\"string\"}},\"required\":[\"quirk\"]}",
    }};
    const ctx = ai_types.Context{
        .messages = &[_]ai_types.Message{.{ .user = .{
            .content = .{ .text = "Call record_quirk with quirk set to whatever you notice first." },
            .timestamp = compat.time.nowSeconds(),
        } }},
        .tools = &tools,
    };

    const stream = try stream_mod.stream(&fixture.registry, fixture.model, ctx, .{
        .api_key = ai_types.OwnedSlice(u8).initBorrowed(fixture.key),
        .max_tokens = 128,
        .temperature = 0.0,
    }, testing.allocator);
    defer _ = stream.deinitAndDestroy();

    var result = try waitResult(testing.allocator, fixture.row.id, stream);
    defer ai_types.deinitAssistantMessageOwned(testing.allocator, &result);

    var saw_tool = false;
    for (result.content) |part| {
        switch (part) {
            .tool_call => |call| {
                if (std.mem.eql(u8, call.name, "record_quirk")) saw_tool = true;
            },
            else => {},
        }
    }
    report(fixture.row.id, "tool call", saw_tool);
    try testing.expect(saw_tool);
}

fn modelWithId(allocator: std.mem.Allocator, source: ai_types.Model, id_text: []const u8) !ai_types.Model {
    const id = try allocator.dupe(u8, id_text);
    errdefer allocator.free(id);
    const name = try allocator.dupe(u8, id_text);
    errdefer allocator.free(name);
    const api = try allocator.dupe(u8, source.api);
    errdefer allocator.free(api);
    const provider = try allocator.dupe(u8, source.provider);
    errdefer allocator.free(provider);
    const base_url = try allocator.dupe(u8, source.base_url);
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
        .cost = source.cost,
        .context_window = source.context_window,
        .max_tokens = source.max_tokens,
        .is_owned = true,
    };
}

test "provider smoke: an unknown model is refused" {
    var fixture = (try prepare(testing.allocator)) orelse return error.SkipZigTest;
    defer fixture.deinit(testing.allocator);

    var refused = try modelWithId(testing.allocator, fixture.model, "oapx-provider-smoke-no-such-model");
    defer refused.deinit(testing.allocator);

    const ctx = prompt("Reply with: ok");
    const stream = stream_mod.stream(&fixture.registry, refused, ctx, .{
        .api_key = ai_types.OwnedSlice(u8).initBorrowed(fixture.key),
        .max_tokens = 16,
        .temperature = 0.0,
    }, testing.allocator) catch {
        report(fixture.row.id, "unknown model refused", true);
        return;
    };
    defer _ = stream.deinitAndDestroy();

    var result = try waitResult(testing.allocator, fixture.row.id, stream);
    defer ai_types.deinitAssistantMessageOwned(testing.allocator, &result);

    const served = result.content.len > 0;
    report(fixture.row.id, "unknown model refused", !served);
    try testing.expect(!served);
}

test "the smoke gate knows the current rows and nothing else" {
    for (rows) |row| {
        try testing.expect(provider_catalog.status(row.id) == .current);
        try testing.expect(rowFor(row.id) != null);
        try testing.expect(catalogTargetFor(row.id) != null);
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
