const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const api_registry = @import("api_registry");
const event_stream = @import("event_stream");
const agent_types = @import("agent_types");
const protocol_server = @import("protocol_server");
const protocol_client = @import("protocol_client");
const protocol_runtime = @import("protocol_runtime");
const in_process = @import("transports/in_process");

const ProtocolServer = protocol_server.ProtocolServer;
const ProtocolClient = protocol_client.ProtocolClient;
const ProviderProtocolRuntime = protocol_runtime.ProviderProtocolRuntime;

const default_idle_timeout_ms: i64 = 600_000;

pub const InProcessProviderProtocolBridge = struct {
    registry: *api_registry.ApiRegistry,
    idle_timeout_ms: i64 = default_idle_timeout_ms,

    pub fn init(registry: *api_registry.ApiRegistry) InProcessProviderProtocolBridge {
        return .{ .registry = registry };
    }

    pub fn protocolClient(self: *InProcessProviderProtocolBridge) agent_types.ProtocolClient {
        return .{
            .stream_fn = streamViaProtocol,
            .ctx = self,
        };
    }
};

const StreamThreadContext = struct {
    allocator: std.mem.Allocator,
    out_stream: *event_stream.AssistantMessageEventStream,
    registry: *api_registry.ApiRegistry,
    model: ai_types.Model,
    context: ai_types.Context,
    options: agent_types.ProtocolOptions,
    api_key: ?[]u8,
    session_id: ?[]u8,
    idle_timeout_ms: i64,

    fn deinit(self: *StreamThreadContext) void {
        self.model.deinit(self.allocator);
        self.context.deinit(self.allocator);
        if (self.api_key) |k| self.allocator.free(k);
        if (self.session_id) |sid| self.allocator.free(sid);
        self.allocator.destroy(self);
    }
};

fn streamViaProtocol(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: agent_types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream.AssistantMessageEventStream {
    const bridge: *InProcessProviderProtocolBridge = @ptrCast(@alignCast(ctx));

    const out_stream = try allocator.create(event_stream.AssistantMessageEventStream);
    out_stream.* = event_stream.AssistantMessageEventStream.init(allocator);
    out_stream.ownership = .{ .owned = ai_types.cloneAssistantMessageEvent };
    out_stream.wait_for_thread_on_deinit = true;

    const thread_ctx = try allocator.create(StreamThreadContext);
    errdefer allocator.destroy(thread_ctx);

    thread_ctx.* = .{
        .allocator = allocator,
        .out_stream = out_stream,
        .registry = bridge.registry,
        .model = try ai_types.cloneModel(allocator, model),
        .context = try ai_types.cloneContext(allocator, context),
        .options = options,
        .api_key = if (options.api_key) |k| try allocator.dupe(u8, k) else null,
        .session_id = if (options.session_id) |sid| try allocator.dupe(u8, sid) else null,
        .idle_timeout_ms = bridge.idle_timeout_ms,
    };

    const thread = try std.Thread.spawn(.{}, runStreamThread, .{thread_ctx});
    thread.detach();

    return out_stream;
}

fn pushEventBlocking(stream: *event_stream.AssistantMessageEventStream, ev: ai_types.AssistantMessageEvent) !void {
    while (true) {
        stream.push(ev) catch |err| switch (err) {
            error.QueueFull => {
                compat.time.sleepNs(1 * std.time.ns_per_ms);
                continue;
            },
            error.StreamCompleted => return error.StreamCompleted,
            error.OutOfMemory => return error.OutOfMemory,
        };
        return;
    }
}

fn drainClientEvents(client: *ProtocolClient, out_stream: *event_stream.AssistantMessageEventStream, allocator: std.mem.Allocator) !bool {
    var drained = false;
    while (client.getEventStream().poll()) |ev| {
        var owned_ev = ev;
        defer ai_types.deinitAssistantMessageEvent(allocator, &owned_ev);
        try pushEventBlocking(out_stream, ev);
        drained = true;
    }
    return drained;
}

fn reasoningEffort(level: ai_types.ThinkingLevel, model_id: []const u8) []const u8 {
    return switch (level) {
        .off => if (isGpt51OrLater(model_id)) "none" else "low",
        .minimal => "low",
        .low => "low",
        .medium => "medium",
        .high => "high",
        .xhigh, .max => if (supportsXhighReasoning(model_id)) "xhigh" else "high",
    };
}

fn isGpt51OrLater(model_id: []const u8) bool {
    const prefix = "gpt-5.";
    if (!std.mem.startsWith(u8, model_id, prefix)) return false;
    const minor = model_id[prefix.len..];
    if (minor.len == 0) return false;
    return std.ascii.isDigit(minor[0]) and minor[0] >= '1';
}

fn supportsXhighReasoning(model_id: []const u8) bool {
    if (std.mem.indexOf(u8, model_id, "codex-max") != null) return true;
    const prefix = "gpt-5.";
    if (!std.mem.startsWith(u8, model_id, prefix)) return false;
    const minor = model_id[prefix.len..];
    if (minor.len == 0) return false;
    return std.ascii.isDigit(minor[0]) and minor[0] >= '2';
}

fn thinkingEffort(level: ai_types.ThinkingLevel) []const u8 {
    return switch (level) {
        .off => "",
        .minimal => "low",
        .low => "low",
        .medium => "medium",
        .high => "high",
        .xhigh, .max => "max",
    };
}

fn thinkingBudget(level: ai_types.ThinkingLevel, budgets: ?ai_types.ThinkingBudgets) ?u32 {
    if (level == .off) return null;
    if (budgets) |b| {
        return switch (level) {
            .off => null,
            .minimal => b.minimal orelse 256,
            .low => b.low orelse 512,
            .medium => b.medium orelse 1024,
            .high => b.high orelse 2048,
            .xhigh => b.xhigh orelse 4096,
            .max => b.max orelse 8192,
        };
    }
    return switch (level) {
        .off => null,
        .minimal => 256,
        .low => 512,
        .medium => 1024,
        .high => 2048,
        .xhigh => 4096,
        .max => 8192,
    };
}

fn streamOptionsFromProtocolOptions(options: agent_types.ProtocolOptions, model_id: []const u8, api_key: ?[]const u8, session_id: ?[]const u8) ai_types.StreamOptions {
    const reason_effort = reasoningEffort(options.thinking_level, model_id);
    const think_effort = thinkingEffort(options.thinking_level);
    return .{
        .api_key = if (api_key) |k| ai_types.OwnedSlice(u8).initBorrowed(k) else ai_types.OwnedSlice(u8).initBorrowed(""),
        .session_id = if (session_id) |sid| ai_types.OwnedSlice(u8).initBorrowed(sid) else ai_types.OwnedSlice(u8).initBorrowed(""),
        .cancel_token = options.cancel_token,
        .temperature = options.temperature,
        .max_tokens = options.max_tokens,
        .thinking_enabled = options.thinking_level != .off,
        .thinking_budget_tokens = thinkingBudget(options.thinking_level, options.thinking_budgets),
        .thinking_effort = ai_types.OwnedSlice(u8).initBorrowed(think_effort),
        .reasoning_effort = ai_types.OwnedSlice(u8).initBorrowed(reason_effort),
        .reasoning_enabled = options.thinking_level != .off,
    };
}

fn tokenCancelled(token: ?ai_types.CancelToken) bool {
    const held = token orelse return false;
    return held.isCancelled();
}

fn runStreamThread(ctx: *StreamThreadContext) void {
    defer {
        const out_stream = ctx.out_stream;
        ctx.deinit();
        out_stream.markThreadDone();
    }

    var pipe = in_process.createSerializedPipe(ctx.allocator);
    defer pipe.deinit();

    var server = ProtocolServer.init(ctx.allocator, ctx.registry, .{});
    defer server.deinit();

    var client = ProtocolClient.init(ctx.allocator, .{
        .event_delivery = .global,
    });
    defer client.deinit();
    client.setSender(pipe.clientSender());

    var runtime = ProviderProtocolRuntime{
        .server = &server,
        .pipe = &pipe,
        .allocator = ctx.allocator,
    };

    const stream_options = streamOptionsFromProtocolOptions(ctx.options, ctx.model.id, ctx.api_key, ctx.session_id);

    var request_model = ctx.model;
    request_model.is_owned = false;
    var request_context = ctx.context;
    request_context.is_owned = false;
    request_context.system_prompt = ai_types.OwnedSlice(u8).initBorrowed(ctx.context.system_prompt.slice());

    if (tokenCancelled(ctx.options.cancel_token)) {
        ctx.out_stream.completeWithError("request cancelled");
        return;
    }

    _ = client.sendStreamRequest(request_model, request_context, stream_options) catch |err| {
        ctx.out_stream.completeWithError(@errorName(err));
        return;
    };

    var last_progress_ms = compat.time.nowMillis();
    var abort_sent = false;

    while (!client.isComplete()) {
        if (!abort_sent and tokenCancelled(ctx.options.cancel_token)) {
            abort_sent = true;
            client.sendAbortRequest("request cancelled") catch {};
        }
        _ = runtime.pumpOnce(&client) catch |err| {
            ctx.out_stream.completeWithError(@errorName(err));
            return;
        };

        const drained = drainClientEvents(&client, ctx.out_stream, ctx.allocator) catch |err| {
            ctx.out_stream.completeWithError(@errorName(err));
            return;
        };

        const now_ms = compat.time.nowMillis();
        if (drained) last_progress_ms = now_ms;
        if (now_ms - last_progress_ms > ctx.idle_timeout_ms) {
            ctx.out_stream.completeWithError("Provider protocol stream timed out");
            return;
        }

        compat.time.sleepNs(1 * std.time.ns_per_ms);
    }

    _ = runtime.pumpOnce(&client) catch {};
    _ = drainClientEvents(&client, ctx.out_stream, ctx.allocator) catch |err| {
        ctx.out_stream.completeWithError(@errorName(err));
        return;
    };

    var final_result = client.waitResult(1) catch {
        if (client.getLastError()) |last_err| {
            ctx.out_stream.completeWithError(last_err);
        } else {
            ctx.out_stream.completeWithError("Provider protocol stream failed");
        }
        return;
    };

    if (final_result) |*owned| {
        ctx.out_stream.complete(owned.intoMessage());
        return;
    }

    if (client.getLastError()) |last_err| {
        ctx.out_stream.completeWithError(last_err);
    } else {
        ctx.out_stream.completeWithError("Provider protocol completed without result");
    }
}

test "InProcessProviderProtocolBridge smoke test" {
    const allocator = std.testing.allocator;

    var registry = api_registry.ApiRegistry.init(allocator);
    defer registry.deinit();

    const Mock = struct {
        fn stream(
            model: ai_types.Model,
            context: ai_types.Context,
            options: ?ai_types.StreamOptions,
            a: std.mem.Allocator,
        ) anyerror!*event_stream.AssistantMessageEventStream {
            _ = model;
            _ = context;

            const s = try a.create(event_stream.AssistantMessageEventStream);
            s.* = event_stream.AssistantMessageEventStream.init(a);
            _ = options;
            s.ownership = .{ .owned = ai_types.cloneAssistantMessageEvent };

            s.push(.{ .start = .{ .partial = .{
                .content = &.{},
                .api = "mock-api",
                .provider = "mock",
                .model = "mock-model",
                .usage = .{},
                .stop_reason = .stop,
                .timestamp = compat.time.nowMillis(),
                .is_owned = false,
            } } }) catch {};

            s.complete(try ai_types.cloneAssistantMessage(a, .{
                .content = &.{.{ .text = .{ .text = "ok" } }},
                .api = "mock-api",
                .provider = "mock",
                .model = "mock-model",
                .usage = .{},
                .stop_reason = .stop,
                .timestamp = compat.time.nowMillis(),
                .is_owned = false,
            }));
            s.markThreadDone();
            return s;
        }

        fn streamSimple(
            model: ai_types.Model,
            context: ai_types.Context,
            options: ?ai_types.SimpleStreamOptions,
            a: std.mem.Allocator,
        ) anyerror!*event_stream.AssistantMessageEventStream {
            _ = options;
            return stream(model, context, null, a);
        }
    };

    try registry.registerApiProvider(.{
        .api = "mock-api",
        .stream = Mock.stream,
        .stream_simple = Mock.streamSimple,
    }, null);

    var bridge = InProcessProviderProtocolBridge.init(&registry);
    const protocol = bridge.protocolClient();

    const model = ai_types.Model{
        .id = "mock-model",
        .name = "Mock",
        .api = "mock-api",
        .provider = "mock",
        .base_url = "",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };

    const user = ai_types.Message{ .user = .{
        .content = .{ .text = "hi" },
        .timestamp = compat.time.nowMillis(),
    } };

    const ctx = ai_types.Context{ .messages = &[_]ai_types.Message{user} };

    const stream = try protocol.stream(model, ctx, .{ .api_key = "test-key" }, allocator);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    var saw_start = false;
    while (stream.wait()) |ev| {
        var owned_ev = ev;
        defer ai_types.deinitAssistantMessageEvent(allocator, &owned_ev);
        if (ev == .start) saw_start = true;
    }

    const result = stream.getResult().?;
    var owned_result = result;
    owned_result.deinit(allocator);
    stream.result = null;

    try std.testing.expect(saw_start);
}

test "provider protocol bridge maps max to each provider's highest level" {
    const claude = streamOptionsFromProtocolOptions(.{ .thinking_level = .max }, "claude-opus-4-6", null, null);
    try std.testing.expectEqualStrings("max", claude.getThinkingEffort().?);
    try std.testing.expectEqual(@as(?u32, 8192), claude.thinking_budget_tokens);

    const budgeted = streamOptionsFromProtocolOptions(.{ .thinking_level = .max, .thinking_budgets = .{ .max = 16384 } }, "claude-opus-4-6", null, null);
    try std.testing.expectEqual(@as(?u32, 16384), budgeted.thinking_budget_tokens);

    const gpt52 = streamOptionsFromProtocolOptions(.{ .thinking_level = .max }, "gpt-5.2", null, null);
    try std.testing.expectEqualStrings("xhigh", gpt52.getReasoningEffort().?);

    const gpt51 = streamOptionsFromProtocolOptions(.{ .thinking_level = .max }, "gpt-5.1", null, null);
    try std.testing.expectEqualStrings("high", gpt51.getReasoningEffort().?);
}

test "provider protocol bridge maps thinking level to stream options" {
    const opts = streamOptionsFromProtocolOptions(.{
        .api_key = "key",
        .session_id = "sid",
        .thinking_level = .xhigh,
        .thinking_budgets = .{ .xhigh = 8192 },
    }, "gpt-5.1", "key", "sid");

    try std.testing.expect(opts.thinking_enabled);
    try std.testing.expect(opts.reasoning_enabled);
    try std.testing.expectEqual(@as(?u32, 8192), opts.thinking_budget_tokens);
    try std.testing.expectEqualStrings("max", opts.getThinkingEffort().?);
    try std.testing.expectEqualStrings("high", opts.getReasoningEffort().?);

    const codex_max = streamOptionsFromProtocolOptions(.{ .thinking_level = .xhigh }, "gpt-5.1-codex-max", null, null);
    try std.testing.expectEqualStrings("xhigh", codex_max.getReasoningEffort().?);

    const gpt52_xhigh = streamOptionsFromProtocolOptions(.{ .thinking_level = .xhigh }, "gpt-5.2", null, null);
    try std.testing.expectEqualStrings("xhigh", gpt52_xhigh.getReasoningEffort().?);

    const gpt52_off = streamOptionsFromProtocolOptions(.{ .thinking_level = .off }, "gpt-5.2", null, null);
    try std.testing.expectEqualStrings("none", gpt52_off.getReasoningEffort().?);
    const gpt5_off = streamOptionsFromProtocolOptions(.{ .thinking_level = .off }, "gpt-5", null, null);
    try std.testing.expectEqualStrings("low", gpt5_off.getReasoningEffort().?);

    const off = streamOptionsFromProtocolOptions(.{ .thinking_level = .off }, "gpt-5.1", null, null);
    try std.testing.expect(!off.thinking_enabled);
    try std.testing.expect(!off.reasoning_enabled);
    try std.testing.expect(off.getThinkingEffort() == null);
    try std.testing.expectEqualStrings("none", off.getReasoningEffort().?);

    const minimal = streamOptionsFromProtocolOptions(.{ .thinking_level = .minimal }, "gpt-5", null, null);
    try std.testing.expectEqualStrings("low", minimal.getReasoningEffort().?);
}

test "InProcessProviderProtocolBridge preserves streamed tool call terminal result" {
    const allocator = std.testing.allocator;

    var registry = api_registry.ApiRegistry.init(allocator);
    defer registry.deinit();

    const Mock = struct {
        fn partial(model: ai_types.Model) ai_types.AssistantMessage {
            return .{
                .content = &.{},
                .api = model.api,
                .provider = model.provider,
                .model = model.id,
                .usage = .{},
                .stop_reason = .stop,
                .timestamp = compat.time.nowMillis(),
                .is_owned = false,
            };
        }

        fn stream(
            model: ai_types.Model,
            context: ai_types.Context,
            options: ?ai_types.StreamOptions,
            a: std.mem.Allocator,
        ) anyerror!*event_stream.AssistantMessageEventStream {
            _ = context;
            _ = options;

            const s = try a.create(event_stream.AssistantMessageEventStream);
            s.* = event_stream.AssistantMessageEventStream.init(a);
            s.ownership = .{ .owned = ai_types.cloneAssistantMessageEvent };
            const p = partial(model);

            s.push(.{ .start = .{ .partial = p } }) catch {};
            s.push(.{ .toolcall_start = .{
                .content_index = 0,
                .id = "call_shell",
                .name = "shell_execute",
                .partial = p,
            } }) catch {};
            s.push(.{ .toolcall_delta = .{
                .content_index = 0,
                .delta = "{\"command\":\"ls -al\"}",
                .partial = p,
            } }) catch {};
            s.push(.{ .toolcall_end = .{
                .content_index = 0,
                .tool_call = .{ .id = "call_shell", .name = "shell_execute", .arguments_json = "{\"command\":\"ls -al\"}" },
                .partial = p,
            } }) catch {};

            s.complete(try ai_types.cloneAssistantMessage(a, .{
                .content = &.{},
                .api = model.api,
                .provider = model.provider,
                .model = model.id,
                .usage = .{},
                .stop_reason = .stop,
                .timestamp = compat.time.nowMillis(),
                .is_owned = false,
            }));
            s.markThreadDone();
            return s;
        }

        fn streamSimple(
            model: ai_types.Model,
            context: ai_types.Context,
            options: ?ai_types.SimpleStreamOptions,
            a: std.mem.Allocator,
        ) anyerror!*event_stream.AssistantMessageEventStream {
            _ = options;
            return stream(model, context, null, a);
        }
    };

    try registry.registerApiProvider(.{
        .api = "mock-tool-api",
        .stream = Mock.stream,
        .stream_simple = Mock.streamSimple,
    }, null);

    var bridge = InProcessProviderProtocolBridge.init(&registry);
    const protocol = bridge.protocolClient();
    const model = ai_types.Model{
        .id = "mock-tool-model",
        .name = "Mock Tool",
        .api = "mock-tool-api",
        .provider = "mock",
        .base_url = "",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };
    const user = ai_types.Message{ .user = .{
        .content = .{ .text = "run ls -al" },
        .timestamp = compat.time.nowMillis(),
    } };
    const ctx = ai_types.Context{ .messages = &[_]ai_types.Message{user} };

    const stream = try protocol.stream(model, ctx, .{ .api_key = "test-key" }, allocator);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    var saw_tool_end = false;
    while (stream.wait()) |ev| {
        var owned_ev = ev;
        defer ai_types.deinitAssistantMessageEvent(allocator, &owned_ev);
        if (ev == .toolcall_end) saw_tool_end = true;
    }

    const result = stream.getResult().?;
    try std.testing.expectEqual(ai_types.StopReason.tool_use, result.stop_reason);
    try std.testing.expectEqual(@as(usize, 1), result.content.len);
    try std.testing.expect(result.content[0] == .tool_call);
    try std.testing.expectEqualStrings("shell_execute", result.content[0].tool_call.name);

    var owned_result = result;
    owned_result.deinit(allocator);
    stream.result = null;
    try std.testing.expect(saw_tool_end);
}

const PacedProvider = struct {
    var deltas: usize = 0;
    var gap_ms: u64 = 0;
    var hold_ms: u64 = 0;
    var started: usize = 0;

    const Job = struct {
        stream: *event_stream.AssistantMessageEventStream,
        cancel_token: ?ai_types.CancelToken,
        allocator: std.mem.Allocator,
    };

    fn partial() ai_types.AssistantMessage {
        return .{
            .content = &.{},
            .api = "paced-api",
            .provider = "mock",
            .model = "paced-model",
            .usage = .{},
            .stop_reason = .stop,
            .timestamp = compat.time.nowMillis(),
            .is_owned = false,
        };
    }

    fn cancelled(token: ?ai_types.CancelToken) bool {
        const t = token orelse return false;
        return t.isCancelled();
    }

    fn run(job: *Job) void {
        const s = job.stream;
        const token = job.cancel_token;
        const a = job.allocator;
        std.heap.page_allocator.destroy(job);
        defer s.markThreadDone();

        s.push(.{ .start = .{ .partial = partial() } }) catch {};
        s.push(.{ .text_start = .{ .content_index = 0, .partial = partial() } }) catch {};
        var sent: usize = 0;
        while (sent < deltas and !cancelled(token)) : (sent += 1) {
            compat.time.sleepMs(gap_ms);
            s.push(.{ .text_delta = .{ .content_index = 0, .delta = "x", .partial = partial() } }) catch {};
        }
        var held: u64 = 0;
        while (held < hold_ms and !cancelled(token)) : (held += 1) compat.time.sleepMs(1);
        if (cancelled(token)) {
            s.completeWithError("cancelled");
            return;
        }

        const text = a.alloc(u8, deltas) catch {
            s.completeWithError("OutOfMemory");
            return;
        };
        defer a.free(text);
        @memset(text, 'x');
        s.push(.{ .text_end = .{ .content_index = 0, .content = text, .partial = partial() } }) catch {};

        const content = [_]ai_types.AssistantContent{.{ .text = .{ .text = text } }};
        const result = ai_types.cloneAssistantMessage(a, .{
            .content = &content,
            .api = "paced-api",
            .provider = "mock",
            .model = "paced-model",
            .usage = .{},
            .stop_reason = .stop,
            .timestamp = compat.time.nowMillis(),
            .is_owned = false,
        }) catch {
            s.completeWithError("OutOfMemory");
            return;
        };
        s.complete(result);
    }

    fn stream(
        model: ai_types.Model,
        context: ai_types.Context,
        options: ?ai_types.StreamOptions,
        a: std.mem.Allocator,
    ) anyerror!*event_stream.AssistantMessageEventStream {
        _ = model;
        _ = context;
        started += 1;

        const s = try a.create(event_stream.AssistantMessageEventStream);
        errdefer a.destroy(s);
        s.* = event_stream.AssistantMessageEventStream.init(a);
        s.ownership = .{ .owned = ai_types.cloneAssistantMessageEvent };

        const job = try std.heap.page_allocator.create(Job);
        errdefer std.heap.page_allocator.destroy(job);
        job.* = .{
            .stream = s,
            .cancel_token = if (options) |o| o.cancel_token else null,
            .allocator = a,
        };
        const thread = try std.Thread.spawn(.{}, run, .{job});
        thread.detach();
        return s;
    }

    fn streamSimple(
        model: ai_types.Model,
        context: ai_types.Context,
        options: ?ai_types.SimpleStreamOptions,
        a: std.mem.Allocator,
    ) anyerror!*event_stream.AssistantMessageEventStream {
        _ = options;
        return stream(model, context, null, a);
    }

    const paced_model = ai_types.Model{
        .id = "paced-model",
        .name = "Paced",
        .api = "paced-api",
        .provider = "mock",
        .base_url = "",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };
};

test "a stream that keeps delivering outlives the idle window" {
    const allocator = std.testing.allocator;

    PacedProvider.deltas = 60;
    PacedProvider.gap_ms = 10;
    PacedProvider.hold_ms = 0;

    var registry = api_registry.ApiRegistry.init(allocator);
    defer registry.deinit();
    try registry.registerApiProvider(.{
        .api = "paced-api",
        .stream = PacedProvider.stream,
        .stream_simple = PacedProvider.streamSimple,
    }, null);

    var bridge = InProcessProviderProtocolBridge.init(&registry);
    bridge.idle_timeout_ms = 250;
    const protocol = bridge.protocolClient();

    const user = ai_types.Message{ .user = .{
        .content = .{ .text = "write a long answer" },
        .timestamp = compat.time.nowMillis(),
    } };
    const ctx = ai_types.Context{ .messages = &[_]ai_types.Message{user} };

    const started_ms = compat.time.nowMillis();
    const stream = try protocol.stream(PacedProvider.paced_model, ctx, .{ .api_key = "test-key" }, allocator);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    var deltas_seen: usize = 0;
    while (stream.wait()) |ev| {
        var owned_ev = ev;
        defer ai_types.deinitAssistantMessageEvent(allocator, &owned_ev);
        if (ev == .text_delta) deltas_seen += 1;
    }
    const elapsed_ms = compat.time.nowMillis() - started_ms;

    try std.testing.expect(stream.getError() == null);
    try std.testing.expect(elapsed_ms > bridge.idle_timeout_ms);
    try std.testing.expectEqual(PacedProvider.deltas, deltas_seen);

    const result = stream.getResult() orelse return error.TestUnexpectedResult;
    var owned_result = result;
    defer {
        owned_result.deinit(allocator);
        stream.result = null;
    }
    try std.testing.expectEqual(@as(usize, 1), result.content.len);
    try std.testing.expectEqual(PacedProvider.deltas, result.content[0].text.text.len);
}

fn pacedBridge(registry: *api_registry.ApiRegistry) !InProcessProviderProtocolBridge {
    try registry.registerApiProvider(.{
        .api = "paced-api",
        .stream = PacedProvider.stream,
        .stream_simple = PacedProvider.streamSimple,
    }, null);
    var bridge = InProcessProviderProtocolBridge.init(registry);
    bridge.idle_timeout_ms = 30_000;
    return bridge;
}

const CancelLater = struct {
    fn run(flag: *std.atomic.Value(bool)) void {
        compat.time.sleepMs(50);
        flag.store(true, .release);
    }
};

test "a cancel while a reply streams aborts the provider's stream instead of waiting it out" {
    const allocator = std.testing.allocator;
    PacedProvider.deltas = 0;
    PacedProvider.gap_ms = 0;
    PacedProvider.hold_ms = 10_000;

    var registry = api_registry.ApiRegistry.init(allocator);
    defer registry.deinit();
    var bridge = try pacedBridge(&registry);
    const protocol = bridge.protocolClient();

    const user = ai_types.Message{ .user = .{ .content = .{ .text = "take your time" }, .timestamp = 0 } };
    const ctx = ai_types.Context{ .messages = &[_]ai_types.Message{user} };
    var flag = std.atomic.Value(bool).init(false);

    const started_ms = compat.time.nowMillis();
    const stream = try protocol.stream(PacedProvider.paced_model, ctx, .{ .api_key = "test-key", .cancel_token = .{ .cancelled = &flag } }, allocator);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }
    const canceller = try std.Thread.spawn(.{}, CancelLater.run, .{&flag});
    defer canceller.join();

    while (stream.wait()) |ev| {
        var owned_ev = ev;
        ai_types.deinitAssistantMessageEvent(allocator, &owned_ev);
    }

    try std.testing.expect(stream.getError() != null);
    try std.testing.expect(compat.time.nowMillis() - started_ms < 5_000);
}

test "a request made after its run was cancelled never reaches the provider" {
    const allocator = std.testing.allocator;
    PacedProvider.deltas = 1;
    PacedProvider.gap_ms = 0;
    PacedProvider.hold_ms = 0;
    PacedProvider.started = 0;

    var registry = api_registry.ApiRegistry.init(allocator);
    defer registry.deinit();
    var bridge = try pacedBridge(&registry);
    const protocol = bridge.protocolClient();

    const user = ai_types.Message{ .user = .{ .content = .{ .text = "anything" }, .timestamp = 0 } };
    const ctx = ai_types.Context{ .messages = &[_]ai_types.Message{user} };
    var flag = std.atomic.Value(bool).init(true);

    const stream = try protocol.stream(PacedProvider.paced_model, ctx, .{ .api_key = "test-key", .cancel_token = .{ .cancelled = &flag } }, allocator);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }
    while (stream.wait()) |ev| {
        var owned_ev = ev;
        ai_types.deinitAssistantMessageEvent(allocator, &owned_ev);
    }

    try std.testing.expectEqualStrings("request cancelled", stream.getError() orelse "");
    try std.testing.expectEqual(@as(usize, 0), PacedProvider.started);
}

test "a stream that goes silent for the idle window fails as timed out" {
    const allocator = std.testing.allocator;

    PacedProvider.deltas = 0;
    PacedProvider.gap_ms = 0;
    PacedProvider.hold_ms = 5_000;

    var registry = api_registry.ApiRegistry.init(allocator);
    defer registry.deinit();
    try registry.registerApiProvider(.{
        .api = "paced-api",
        .stream = PacedProvider.stream,
        .stream_simple = PacedProvider.streamSimple,
    }, null);

    var bridge = InProcessProviderProtocolBridge.init(&registry);
    bridge.idle_timeout_ms = 100;
    const protocol = bridge.protocolClient();

    const user = ai_types.Message{ .user = .{
        .content = .{ .text = "think quietly" },
        .timestamp = compat.time.nowMillis(),
    } };
    const ctx = ai_types.Context{ .messages = &[_]ai_types.Message{user} };

    const stream = try protocol.stream(PacedProvider.paced_model, ctx, .{ .api_key = "test-key" }, allocator);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    while (stream.wait()) |ev| {
        var owned_ev = ev;
        ai_types.deinitAssistantMessageEvent(allocator, &owned_ev);
    }

    try std.testing.expectEqualStrings("Provider protocol stream timed out", stream.getError() orelse "");
}
