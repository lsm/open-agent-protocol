const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const event_stream = @import("event_stream");
const agent_types = @import("agent_types");
const agent_loop = @import("agent_loop");

const testing = std.testing;

const AgentEvent = agent_types.AgentEvent;
const AgentContext = agent_types.AgentContext;
const AgentLoopConfig = agent_types.AgentLoopConfig;
const ProtocolOptions = agent_types.ProtocolOptions;

test "AgentContext: init and deinit" {
    var ctx = AgentContext.init(testing.allocator);
    defer ctx.deinit();

    try testing.expect(ctx.messages.items.len == 0);
    try testing.expect(ctx.getSystemPrompt() == null);
}

test "AgentContext: append and retrieve messages" {
    const allocator = testing.allocator;

    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const text1 = try allocator.dupe(u8, "Hello");
    const text2 = try allocator.dupe(u8, "World");

    const msg1 = ai_types.Message{ .user = .{
        .content = .{ .text = text1 },
        .timestamp = 1,
    } };
    const msg2 = ai_types.Message{ .user = .{
        .content = .{ .text = text2 },
        .timestamp = 2,
    } };

    try ctx.appendMessage(msg1);
    try ctx.appendMessage(msg2);

    const messages = ctx.messagesSlice();
    try testing.expectEqual(@as(usize, 2), messages.len);
}

test "AgentContext: with system prompt" {
    const allocator = testing.allocator;

    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    ctx.system_prompt = agent_types.OwnedSlice(u8).initOwned(try allocator.dupe(u8, "You are a helpful assistant."));

    try testing.expectEqualStrings("You are a helpful assistant.", ctx.getSystemPrompt().?);
}

test "AgentEvent: tags are correct" {
    const event: AgentEvent = .agent_start;
    try testing.expect(std.meta.activeTag(event) == .agent_start);
}

test "ProtocolOptions: default values" {
    const opts = ProtocolOptions{};
    try testing.expect(opts.api_key == null);
    try testing.expect(opts.session_id == null);
    try testing.expect(opts.cancel_token == null);
    try testing.expect(opts.temperature == null);
    try testing.expect(opts.max_tokens == null);
}

const MockMode = enum { done, err };

const MockProtocolState = struct {
    mode: MockMode,
    text: []const u8 = "ok",
    stop_reason: ai_types.StopReason = .stop,
    tool_turns: usize = 0,
    tool_turn_stop_reason: ai_types.StopReason = .tool_use,
    call_count: usize = 0,
    last_options: ?ProtocolOptions = null,

    pub fn deinit(self: *MockProtocolState, allocator: std.mem.Allocator) void {
        if (self.last_options) |opts| {
            if (opts.api_key) |k| allocator.free(k);
            if (opts.session_id) |s| allocator.free(s);
        }
    }
};

fn createModel() ai_types.Model {
    return .{
        .id = "mock-model",
        .name = "Mock",
        .api = "mock-api",
        .provider = "mock-provider",
        .base_url = "",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };
}

fn makeOwnedAssistantMessage(
    allocator: std.mem.Allocator,
    text: []const u8,
    stop_reason: ai_types.StopReason,
) !ai_types.AssistantMessage {
    const content = try allocator.alloc(ai_types.AssistantContent, 1);
    content[0] = .{ .text = .{ .text = try allocator.dupe(u8, text) } };

    return .{
        .content = content,
        .api = "mock-api",
        .provider = "mock-provider",
        .model = "mock-model",
        .usage = .{},
        .stop_reason = stop_reason,
        .timestamp = compat.time.nowMillis(),
        .is_owned = false,
    };
}

fn makeOwnedToolCallMessage(
    allocator: std.mem.Allocator,
    stop_reason: ai_types.StopReason,
) !ai_types.AssistantMessage {
    const id = try allocator.dupe(u8, "call_probe");
    errdefer allocator.free(id);
    const name = try allocator.dupe(u8, "probe");
    errdefer allocator.free(name);
    const arguments_json = try allocator.dupe(u8, "{}");
    errdefer allocator.free(arguments_json);
    const content = try allocator.alloc(ai_types.AssistantContent, 1);
    content[0] = .{ .tool_call = .{ .id = id, .name = name, .arguments_json = arguments_json } };

    return .{
        .content = content,
        .api = "mock-api",
        .provider = "mock-provider",
        .model = "mock-model",
        .usage = .{},
        .stop_reason = stop_reason,
        .timestamp = compat.time.nowMillis(),
        .is_owned = false,
    };
}

fn mockProtocolStream(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: agent_types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream.AssistantMessageEventStream {
    _ = model;
    _ = context;

    const state: *MockProtocolState = @ptrCast(@alignCast(ctx));
    state.call_count += 1;
    state.last_options = .{
        .api_key = if (options.api_key) |k| try allocator.dupe(u8, k) else null,
        .session_id = if (options.session_id) |s| try allocator.dupe(u8, s) else null,
        .cancel_token = options.cancel_token,
        .thinking_budgets = options.thinking_budgets,
        .max_retry_delay_ms = options.max_retry_delay_ms,
        .temperature = options.temperature,
        .max_tokens = options.max_tokens,
    };

    const stream = try allocator.create(event_stream.AssistantMessageEventStream);
    stream.* = event_stream.AssistantMessageEventStream.init(allocator);

    const msg = if (state.mode == .done and state.call_count <= state.tool_turns)
        try makeOwnedToolCallMessage(allocator, state.tool_turn_stop_reason)
    else if (state.mode == .done)
        try makeOwnedAssistantMessage(allocator, state.text, state.stop_reason)
    else
        ai_types.AssistantMessage{
            .content = &[_]ai_types.AssistantContent{
                .{ .text = .{ .text = "" } },
            },
            .api = "mock-api",
            .provider = "mock-provider",
            .model = "mock-model",
            .usage = .{},
            .stop_reason = .@"error",
            .error_message = ai_types.OwnedSlice(u8).initBorrowed("mock provider error"),
            .timestamp = compat.time.nowMillis(),
            .is_owned = false,
        };

    if (state.mode == .done) {
        try stream.push(.{ .done = .{
            .reason = .stop,
            .message = msg,
        } });
    } else {
        try stream.push(.{ .@"error" = .{
            .reason = .@"error",
            .err = msg,
        } });
    }

    stream.completeWithError("");
    stream.markThreadDone();
    return stream;
}

fn createMockProtocol(state: *MockProtocolState) agent_types.ProtocolClient {
    return .{
        .stream_fn = mockProtocolStream,
        .ctx = state,
    };
}

test "agentLoop: basic single turn with text response" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .done, .text = "hello from mock" };

    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "Hello");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    while (stream.wait()) |event| {
        var owned_event = event;
        owned_event.deinit(allocator);
    }

    const result = stream.getResult().?;
    try testing.expectEqual(@as(usize, 2), result.messages.slice().len);
    try testing.expect(result.final_message.stop_reason == .stop);
    try testing.expectEqual(@as(usize, 1), state.call_count);
}

test "agentLoop: collects events in correct order" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .done, .text = "event order" };

    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "Hi");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    var saw_agent_start = false;
    var saw_turn_start = false;
    var saw_agent_end = false;

    while (stream.wait()) |event| {
        switch (event) {
            .agent_start => saw_agent_start = true,
            .turn_start => saw_turn_start = true,
            .agent_end => saw_agent_end = true,
            else => {},
        }
    }

    try testing.expect(saw_agent_start);
    try testing.expect(saw_turn_start);
    try testing.expect(saw_agent_end);
}

test "agentLoop: emits lifecycle events in strict sequence" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .done, .text = "ordered" };
    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "Hi");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    var step: u8 = 0;
    while (stream.wait()) |event| {
        switch (event) {
            .message_start => {
                if (step == 0) step = 1;
            },
            .message_end => {
                if (step == 1) step = 2;
            },
            .agent_start => {
                if (step == 2) step = 3;
            },
            .turn_start => {
                if (step == 3) step = 4;
            },
            .turn_end => {
                if (step == 4) step = 5;
            },
            .agent_end => {
                if (step == 5) step = 6;
            },
            else => {},
        }
    }

    try testing.expectEqual(@as(u8, 6), step);
}

test "agentLoop: handles provider error" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .err, .text = "" };

    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "Fail please");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    while (stream.wait()) |event| {
        var owned_event = event;
        owned_event.deinit(allocator);
    }

    const result = stream.getResult().?;
    try testing.expect(result.final_message.stop_reason == .@"error");
    try testing.expect(result.final_message.getErrorMessage() != null);
}

test "agentLoop: iteration cap reports max_turns on agent_end" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .done, .tool_turns = std.math.maxInt(usize) };

    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "Run the tool");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
        .max_iterations = 1,
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    var agent_end_termination: ?agent_types.AgentTermination = null;
    while (stream.wait()) |event| {
        var owned_event = event;
        defer owned_event.deinit(allocator);
        switch (event) {
            .agent_end => |payload| agent_end_termination = payload.termination,
            else => {},
        }
    }

    const result = stream.getResult().?;
    var owned_result = result;
    defer owned_result.deinit(allocator);
    stream.result = null;

    try testing.expect(result.final_message.stop_reason == .tool_use);
    try testing.expectEqual(@as(?agent_types.AgentTermination, .max_turns), result.termination);
    try testing.expectEqual(@as(?agent_types.AgentTermination, .max_turns), agent_end_termination);
}

test "agentLoop: zero max_iterations still reports max_turns termination" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .done, .text = "unused" };

    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "Hello");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
        .max_iterations = 0,
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    var agent_end_termination: ?agent_types.AgentTermination = null;
    while (stream.wait()) |event| {
        var owned_event = event;
        defer owned_event.deinit(allocator);
        switch (event) {
            .agent_end => |payload| agent_end_termination = payload.termination,
            else => {},
        }
    }

    try testing.expectEqual(@as(usize, 0), state.call_count);
    try testing.expectEqual(@as(?agent_types.AgentTermination, .max_turns), agent_end_termination);

    const result = stream.getResult().?;
    var owned_result = result;
    defer owned_result.deinit(allocator);
    stream.result = null;
    try testing.expectEqual(@as(?agent_types.AgentTermination, .max_turns), result.termination);
}

test "agentLoop: cancellation reports cancelled termination" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .done, .text = "unused" };

    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "Hello");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    var cancel_flag = std.atomic.Value(bool).init(true);
    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
        .cancel_token = .{ .cancelled = &cancel_flag },
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    var agent_end_termination: ?agent_types.AgentTermination = null;
    while (stream.wait()) |event| {
        var owned_event = event;
        defer owned_event.deinit(allocator);
        switch (event) {
            .agent_end => |payload| agent_end_termination = payload.termination,
            else => {},
        }
    }

    try testing.expectEqual(@as(usize, 0), state.call_count);
    try testing.expectEqual(@as(?agent_types.AgentTermination, .cancelled), agent_end_termination);

    const result = stream.getResult().?;
    var owned_result = result;
    defer owned_result.deinit(allocator);
    stream.result = null;
    try testing.expectEqual(@as(?agent_types.AgentTermination, .cancelled), result.termination);
}

test "ProtocolOptions: passed through to protocol client" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .done, .text = "options" };
    defer state.deinit(allocator);

    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "options test");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
        .temperature = 0.25,
        .max_tokens = 42,
        .api_key = "key-123",
        .session_id = "session-abc",
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    while (stream.wait()) |event| {
        var owned_event = event;
        owned_event.deinit(allocator);
    }

    const seen = state.last_options.?;
    try testing.expectEqual(@as(?f32, 0.25), seen.temperature);
    try testing.expectEqual(@as(?u32, 42), seen.max_tokens);
    try testing.expectEqualStrings("key-123", seen.api_key.?);
    try testing.expectEqualStrings("session-abc", seen.session_id.?);
}

test "agentLoop: cancellation token stops before protocol stream call" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .done, .text = "unused" };
    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "cancel");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    var cancelled = std.atomic.Value(bool).init(true);
    const cancel_token = ai_types.CancelToken{ .cancelled = &cancelled };

    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
        .cancel_token = cancel_token,
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }
    while (stream.wait()) |event| {
        var owned_event = event;
        owned_event.deinit(allocator);
    }

    const result = stream.getResult().?;
    try testing.expectEqual(@as(usize, 0), result.iterations);
    try testing.expectEqual(@as(usize, 0), state.call_count);
}

test "agentLoop: max_iterations caps repeated tool_use loop" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .done, .tool_turns = std.math.maxInt(usize) };
    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "loop");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
        .max_iterations = 2,
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }
    while (stream.wait()) |event| {
        var owned_event = event;
        owned_event.deinit(allocator);
    }

    const result = stream.getResult().?;
    try testing.expectEqual(@as(usize, 2), result.iterations);
    try testing.expectEqual(@as(usize, 2), state.call_count);
}

const ProbeTool = struct {
    runs: usize = 0,
};

fn runProbeTool(
    ctx: ?*anyopaque,
    tool_call_id: []const u8,
    tool_name: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?agent_types.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!agent_types.AgentToolResult {
    _ = tool_call_id;
    _ = tool_name;
    _ = args_json;
    _ = cancel_token;
    _ = on_update_ctx;
    _ = on_update;
    const probe: *ProbeTool = @ptrCast(@alignCast(ctx.?));
    probe.runs += 1;
    const text = try allocator.dupe(u8, "probed");
    errdefer allocator.free(text);
    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    content[0] = .{ .text = .{ .text = text } };
    return .{ .content = agent_types.OwnedSlice(ai_types.UserContentPart).initOwned(content) };
}

test "agentLoop: without max_iterations a run keeps calling tools past a hundred turns" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .done, .text = "finished", .tool_turns = 150 };
    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "keep going");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    var agent_end_termination: ?agent_types.AgentTermination = .max_turns;
    while (stream.wait()) |event| {
        var owned_event = event;
        defer owned_event.deinit(allocator);
        switch (event) {
            .agent_end => |payload| agent_end_termination = payload.termination,
            else => {},
        }
    }

    const result = stream.getResult().?;
    try testing.expectEqual(@as(usize, 151), state.call_count);
    try testing.expectEqual(@as(u32, 151), result.iterations);
    try testing.expect(result.final_message.stop_reason == .stop);
    try testing.expectEqual(@as(?agent_types.AgentTermination, null), result.termination);
    try testing.expectEqual(@as(?agent_types.AgentTermination, null), agent_end_termination);
}

test "agentLoop: a tool call in a reply reported as stop still runs" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .done, .text = "finished", .tool_turns = 1, .tool_turn_stop_reason = .stop };
    var probe = ProbeTool{};
    const tools = [_]agent_types.AgentTool{.{
        .label = "Probe",
        .name = "probe",
        .description = "Probe",
        .parameters_schema_json = "{\"type\":\"object\"}",
        .execute = undefined,
    }};
    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "probe it");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
        .tools = &tools,
        .execute_tool_via_protocol_fn = runProbeTool,
        .execute_tool_via_protocol_ctx = &probe,
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }
    while (stream.wait()) |event| {
        var owned_event = event;
        owned_event.deinit(allocator);
    }

    const result = stream.getResult().?;
    try testing.expectEqual(@as(usize, 1), probe.runs);
    try testing.expectEqual(@as(usize, 2), state.call_count);
    try testing.expectEqual(@as(u32, 2), result.iterations);
    try testing.expect(result.final_message.stop_reason == .stop);
}

test "agentLoop: a tool call cut off at the output limit is answered with an error instead of run, and the run goes on" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .done, .text = "finished", .tool_turns = 1, .tool_turn_stop_reason = .length };
    var probe = ProbeTool{};
    const tools = [_]agent_types.AgentTool{.{
        .label = "Probe",
        .name = "probe",
        .description = "Probe",
        .parameters_schema_json = "{\"type\":\"object\"}",
        .execute = undefined,
    }};
    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "probe it");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
        .tools = &tools,
        .execute_tool_via_protocol_fn = runProbeTool,
        .execute_tool_via_protocol_ctx = &probe,
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }
    while (stream.wait()) |event| {
        var owned_event = event;
        owned_event.deinit(allocator);
    }

    const result = stream.getResult().?;
    var answers: usize = 0;
    for (result.messages.slice()) |message| {
        if (message != .tool_result) continue;
        try testing.expect(message.tool_result.is_error);
        try testing.expect(std.mem.indexOf(u8, message.tool_result.content[0].text.text, "output token limit") != null);
        answers += 1;
    }
    try testing.expectEqual(@as(usize, 1), answers);
    try testing.expectEqual(@as(usize, 0), probe.runs);
    try testing.expectEqual(@as(usize, 2), state.call_count);
    try testing.expectEqual(@as(u32, 2), result.iterations);
    try testing.expect(result.final_message.stop_reason == .stop);
}

test "agentLoop: a run whose tool calls keep getting cut off ends after three error answers" {
    const allocator = testing.allocator;

    var state = MockProtocolState{ .mode = .done, .tool_turns = std.math.maxInt(usize), .tool_turn_stop_reason = .length };
    var probe = ProbeTool{};
    const tools = [_]agent_types.AgentTool{.{
        .label = "Probe",
        .name = "probe",
        .description = "Probe",
        .parameters_schema_json = "{\"type\":\"object\"}",
        .execute = undefined,
    }};
    var ctx = AgentContext.init(allocator);
    defer ctx.deinit();

    const prompt_text = try allocator.dupe(u8, "write the whole file");
    const prompt = ai_types.Message{ .user = .{
        .content = .{ .text = prompt_text },
        .timestamp = compat.time.nowMillis(),
    } };

    const config = AgentLoopConfig{
        .model = createModel(),
        .protocol = createMockProtocol(&state),
        .tools = &tools,
        .execute_tool_via_protocol_fn = runProbeTool,
        .execute_tool_via_protocol_ctx = &probe,
    };

    const stream = try agent_loop.agentLoop(allocator, &.{prompt}, &ctx, config);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }
    while (stream.wait()) |event| {
        var owned_event = event;
        owned_event.deinit(allocator);
    }

    const result = stream.getResult().?;
    var answers: usize = 0;
    for (result.messages.slice()) |message| {
        if (message != .tool_result) continue;
        try testing.expect(message.tool_result.is_error);
        answers += 1;
    }
    try testing.expectEqual(@as(usize, 3), answers);
    try testing.expectEqual(@as(usize, 0), probe.runs);
    try testing.expectEqual(@as(usize, 4), state.call_count);
    try testing.expect(result.final_message.stop_reason == .length);
    try testing.expectEqual(@as(?agent_types.AgentTermination, null), result.termination);
}
