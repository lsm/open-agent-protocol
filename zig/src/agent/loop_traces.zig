const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const event_stream_module = @import("event_stream");
const types = @import("agent_types");
const agent_loop = @import("agent_loop.zig");

const allocator = std.testing.allocator;

const Step = union(enum) {
    text: []const u8,
    tool: struct { name: []const u8, args: []const u8 },
    drop: []const u8,
    drop_after_text: []const u8,
};

const Scenario = struct {
    name: []const u8,
    expected: []const u8,
    steps: []const Step,
    steering: ?[]const u8 = null,
    follow_up: ?[]const u8 = null,
    cancel_in_tool: bool = false,
    max_iterations: ?u32 = null,
};

const Line = struct {
    event: []const u8,
    message: ?[]const u8 = null,
    steering: ?bool = null,
    input_dropped: ?bool = null,
    stop_reason: ?[]const u8 = null,
    error_message: ?[]const u8 = null,
    update: ?[]const u8 = null,
    delta: ?[]const u8 = null,
    tool: ?[]const u8 = null,
    args: ?[]const u8 = null,
    result: ?[]const u8 = null,
    is_error: ?bool = null,
    returned_bytes: ?u64 = null,
    termination: ?[]const u8 = null,
    total_bytes: ?u64 = null,
    estimated_tokens: ?u64 = null,
    message_count: ?u32 = null,
    tool_count: ?u32 = null,
    segment: ?[]const u8 = null,
    item_count: ?u32 = null,
    messages: ?[]const []const u8 = null,
};

const Recorder = struct {
    arena: std.heap.ArenaAllocator,
    request_arena: std.heap.ArenaAllocator,
    trace: std.ArrayList(u8) = .empty,
    requests: std.ArrayList(u8) = .empty,
    scenario: *const Scenario,
    next_step: usize = 0,
    cancelled: std.atomic.Value(bool) = .init(false),
    steered: bool = false,
    followed: bool = false,

    fn add(list: *std.ArrayList(u8), a: std.mem.Allocator, line: Line) !void {
        const text = try std.json.Stringify.valueAlloc(a, line, .{ .emit_null_optional_fields = false });
        try list.appendSlice(a, text);
        try list.append(a, '\n');
    }
};

fn describe(a: std.mem.Allocator, message: ai_types.Message) ![]const u8 {
    var text: std.ArrayList(u8) = .empty;
    switch (message) {
        .user => |user| {
            try text.appendSlice(a, "user:");
            switch (user.content) {
                .text => |plain| try text.appendSlice(a, plain),
                .parts => |parts| for (parts) |part| if (part == .text) try text.appendSlice(a, part.text.text),
            }
        },
        .assistant => |assistant| {
            try text.appendSlice(a, "assistant:");
            for (assistant.content) |block| switch (block) {
                .text => |plain| try text.appendSlice(a, plain.text),
                .thinking => |thought| try text.appendSlice(a, thought.thinking),
                .tool_call => |call| try text.print(a, "[{s} {s}]", .{ call.name, call.arguments_json }),
                .image => try text.appendSlice(a, "[image]"),
            };
        },
        .tool_result => |result| {
            try text.print(a, "tool_result:{s}:{}:", .{ result.tool_name, result.is_error });
            for (result.content) |part| if (part == .text) try text.appendSlice(a, part.text.text);
        },
    }
    return text.items;
}

fn updateName(event: ai_types.AssistantMessageEvent) []const u8 {
    return @tagName(event);
}

fn updateDelta(event: ai_types.AssistantMessageEvent) ?[]const u8 {
    return switch (event) {
        .text_delta => |delta| delta.delta,
        else => null,
    };
}

fn record(recorder: *Recorder, event: types.AgentEvent) !void {
    const a = recorder.arena.allocator();
    const name = @tagName(event);
    const line: Line = switch (event) {
        .message_start => |payload| .{ .event = name, .message = try describe(a, payload.message) },
        .message_update => |payload| .{ .event = name, .update = updateName(payload.event), .delta = updateDelta(payload.event) },
        .message_end => |payload| .{ .event = name, .message = try describe(a, payload.message), .steering = payload.steering },
        .turn_end => |payload| .{
            .event = name,
            .message = try describe(a, .{ .assistant = payload.message }),
            .stop_reason = @tagName(payload.message.stop_reason),
            .error_message = payload.message.getErrorMessage(),
            .input_dropped = payload.input_dropped,
        },
        .context_usage => |payload| .{ .event = name, .total_bytes = payload.total_bytes, .estimated_tokens = payload.estimated_tokens, .message_count = payload.message_count, .tool_count = payload.tool_count },
        .prompt_segment_usage => |payload| .{ .event = name, .segment = @tagName(payload.segment), .total_bytes = payload.bytes, .item_count = payload.item_count },
        .tool_execution_start => |payload| .{ .event = name, .tool = payload.tool_name, .args = payload.args_json },
        .tool_execution_update => |payload| .{ .event = name, .tool = payload.tool_name, .result = payload.partial_result_json },
        .tool_execution_end => |payload| .{ .event = name, .tool = payload.tool_name, .result = payload.result_json, .is_error = payload.is_error, .returned_bytes = payload.returned_total_bytes },
        .agent_end => |payload| .{
            .event = name,
            .termination = if (payload.termination) |termination| @tagName(termination) else null,
            .stop_reason = if (payload.final_message) |final| @tagName(final.stop_reason) else null,
        },
        .run_failed => |payload| .{ .event = name, .error_message = payload.reason.slice() },
        else => .{ .event = name },
    };
    try Recorder.add(&recorder.trace, a, line);
}

fn owned(a: std.mem.Allocator, text: []const u8) ![]u8 {
    return a.dupe(u8, text);
}

fn reply(model: ai_types.Model, a: std.mem.Allocator, stream: *event_stream_module.AssistantMessageEventStream, step: Step) !void {
    const partial = ai_types.AssistantMessage{ .content = &.{}, .api = model.api, .provider = model.provider, .model = model.id, .usage = .{}, .stop_reason = .stop, .timestamp = 0 };
    try stream.push(.{ .start = .{ .partial = partial } });
    if (step == .text) try stream.push(.{ .text_delta = .{ .content_index = 0, .delta = step.text, .partial = partial } });
    const blocks = try a.alloc(ai_types.AssistantContent, 1);
    errdefer a.free(blocks);
    const api = try owned(a, model.api);
    errdefer a.free(api);
    const provider = try owned(a, model.provider);
    errdefer a.free(provider);
    const model_id = try owned(a, model.id);
    errdefer a.free(model_id);
    var stop_reason: ai_types.StopReason = .stop;
    switch (step) {
        .text => |text| blocks[0] = .{ .text = .{ .text = try owned(a, text) } },
        .tool => |call| {
            const id = try owned(a, "call-1");
            errdefer a.free(id);
            const name = try owned(a, call.name);
            errdefer a.free(name);
            const arguments = try owned(a, call.args);
            blocks[0] = .{ .tool_call = .{ .id = id, .name = name, .arguments_json = arguments } };
            stop_reason = .tool_use;
        },
        else => unreachable,
    }
    stream.complete(.{ .content = blocks, .api = api, .provider = provider, .model = model_id, .usage = .{}, .stop_reason = stop_reason, .timestamp = 0, .is_owned = true });
}

fn scriptedStream(ctx: ?*anyopaque, model: ai_types.Model, context: ai_types.Context, options: types.ProtocolOptions, a: std.mem.Allocator) anyerror!*event_stream_module.AssistantMessageEventStream {
    _ = options;
    const recorder: *Recorder = @ptrCast(@alignCast(ctx.?));
    const scratch = recorder.request_arena.allocator();
    var sent: std.ArrayList([]const u8) = .empty;
    for (context.messages) |message| try sent.append(scratch, try describe(scratch, message));
    try Recorder.add(&recorder.requests, scratch, .{ .event = "model_request", .messages = sent.items });

    const stream = try a.create(event_stream_module.AssistantMessageEventStream);
    stream.* = event_stream_module.AssistantMessageEventStream.init(a);
    const step = recorder.scenario.steps[recorder.next_step];
    recorder.next_step += 1;
    switch (step) {
        .drop => |failure| stream.completeWithError(failure),
        .drop_after_text => |failure| {
            const partial = ai_types.AssistantMessage{ .content = &.{}, .api = model.api, .provider = model.provider, .model = model.id, .usage = .{}, .stop_reason = .stop, .timestamp = 0 };
            try stream.push(.{ .start = .{ .partial = partial } });
            try stream.push(.{ .text_delta = .{ .content_index = 0, .delta = "half an ans", .partial = partial } });
            stream.completeWithError(failure);
        },
        else => try reply(model, a, stream, step),
    }
    return stream;
}

fn scriptedTool(ctx: ?*anyopaque, tool_call_id: []const u8, tool_name: []const u8, args_json: []const u8, cancel_token: ?ai_types.CancelToken, on_update_ctx: ?*anyopaque, on_update: ?types.ToolUpdateCallback, a: std.mem.Allocator) anyerror!types.AgentToolResult {
    _ = cancel_token;
    const recorder: *Recorder = @ptrCast(@alignCast(ctx.?));
    if (on_update) |update| update(on_update_ctx, tool_call_id, tool_name, "{\"partial\":true}");
    if (recorder.scenario.cancel_in_tool) recorder.cancelled.store(true, .release);
    if (std.mem.indexOf(u8, args_json, "fail") != null) return error.ScriptedToolFailure;
    const content = try a.alloc(ai_types.UserContentPart, 1);
    content[0] = .{ .text = .{ .text = try owned(a, "echoed") } };
    return .{ .content = types.OwnedSlice(ai_types.UserContentPart).initOwned(content) };
}

fn oneMessage(a: std.mem.Allocator, text: []const u8) ![]const ai_types.Message {
    const messages = try a.alloc(ai_types.Message, 1);
    messages[0] = .{ .user = .{ .content = .{ .text = try owned(a, text) }, .timestamp = 0 } };
    return messages;
}

fn steeringOnce(ctx: ?*anyopaque, a: std.mem.Allocator) anyerror!?[]const ai_types.Message {
    const recorder: *Recorder = @ptrCast(@alignCast(ctx.?));
    if (recorder.steered or recorder.next_step == 0) return null;
    recorder.steered = true;
    return try oneMessage(a, recorder.scenario.steering.?);
}

fn followUpOnce(ctx: ?*anyopaque, a: std.mem.Allocator) anyerror!?[]const ai_types.Message {
    const recorder: *Recorder = @ptrCast(@alignCast(ctx.?));
    if (recorder.followed) return null;
    recorder.followed = true;
    return try oneMessage(a, recorder.scenario.follow_up.?);
}

fn traceModel() ai_types.Model {
    return .{ .id = "trace-model", .name = "Trace", .api = "trace-api", .provider = "trace-provider", .base_url = "", .reasoning = false, .input = &.{"text"}, .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 }, .context_window = 100_000, .max_tokens = 4096 };
}

fn writeTrace(name: []const u8, trace: []const u8) !void {
    const dir_path = compat.getEnvVarOwned(allocator, "OAP_LOOP_TRACE_OUT") catch return;
    defer allocator.free(dir_path);
    const path = try std.fmt.allocPrint(allocator, "{s}/{s}.jsonl", .{ dir_path, name });
    defer allocator.free(path);
    try std.Io.Dir.cwd().writeFile(std.testing.io, .{ .sub_path = path, .data = trace });
}

fn runScenario(scenario: *const Scenario) !void {
    var recorder = Recorder{ .arena = std.heap.ArenaAllocator.init(allocator), .request_arena = std.heap.ArenaAllocator.init(allocator), .scenario = scenario };
    defer recorder.arena.deinit();
    defer recorder.request_arena.deinit();

    const tools = [_]types.AgentTool{.{ .label = "Echo", .name = "echo", .description = "Echoes.", .parameters_schema_json = "{\"type\":\"object\"}", .execute = undefined }};
    var context = types.AgentContext.init(allocator);
    defer context.deinit();
    context.tools = &tools;
    const prompts = [_]ai_types.Message{try ai_types.cloneMessage(allocator, .{ .user = .{ .content = .{ .text = "start" }, .timestamp = 0 } })};
    const stream = try agent_loop.agentLoop(allocator, &prompts, &context, .{
        .model = traceModel(),
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &recorder },
        .tools = &tools,
        .execute_tool_via_protocol_fn = scriptedTool,
        .execute_tool_via_protocol_ctx = &recorder,
        .max_retry_delay_ms = 1,
        .max_iterations = scenario.max_iterations,
        .cancel_token = .{ .cancelled = &recorder.cancelled },
        .get_steering_messages_fn = if (scenario.steering != null) steeringOnce else null,
        .get_steering_messages_ctx = &recorder,
        .get_follow_up_messages_fn = if (scenario.follow_up != null) followUpOnce else null,
        .get_follow_up_messages_ctx = &recorder,
    });
    defer _ = stream.deinitAndDestroy();
    while (stream.wait()) |event| {
        var held = event;
        defer held.deinit(allocator);
        try record(&recorder, held);
    }
    try recorder.trace.appendSlice(recorder.arena.allocator(), recorder.requests.items);
    try writeTrace(scenario.name, recorder.trace.items);
    try std.testing.expectEqualStrings(scenario.expected, recorder.trace.items);
}

const tool_then_text = [_]Step{ .{ .tool = .{ .name = "echo", .args = "{\"say\":\"hi\"}" } }, .{ .text = "done" } };

test "a text reply traces as recorded" {
    try runScenario(&.{ .name = "text", .expected = @embedFile("loop_traces/text.jsonl"), .steps = &.{.{ .text = "hello" }} });
}

test "a tool call traces as recorded" {
    try runScenario(&.{ .name = "tool_call", .expected = @embedFile("loop_traces/tool_call.jsonl"), .steps = &tool_then_text });
}

test "a tool failure traces as recorded" {
    try runScenario(&.{ .name = "tool_failure", .expected = @embedFile("loop_traces/tool_failure.jsonl"), .steps = &.{ .{ .tool = .{ .name = "echo", .args = "{\"say\":\"fail\"}" } }, .{ .text = "done" } } });
}

test "an unknown tool traces as recorded" {
    try runScenario(&.{ .name = "unknown_tool", .expected = @embedFile("loop_traces/unknown_tool.jsonl"), .steps = &.{ .{ .tool = .{ .name = "missing", .args = "{}" } }, .{ .text = "done" } } });
}

test "a retry before any output traces as recorded" {
    try runScenario(&.{ .name = "retry", .expected = @embedFile("loop_traces/retry.jsonl"), .steps = &.{ .{ .drop = "read error" }, .{ .text = "recovered" } } });
}

test "a drop after output traces as recorded" {
    try runScenario(&.{ .name = "drop_after_output", .expected = @embedFile("loop_traces/drop_after_output.jsonl"), .steps = &.{.{ .drop_after_text = "read error" }} });
}

test "a steering message traces as recorded" {
    try runScenario(&.{ .name = "steering", .expected = @embedFile("loop_traces/steering.jsonl"), .steps = &tool_then_text, .steering = "steer" });
}

test "a follow-up traces as recorded" {
    try runScenario(&.{ .name = "follow_up", .expected = @embedFile("loop_traces/follow_up.jsonl"), .steps = &.{ .{ .text = "first" }, .{ .text = "second" } }, .follow_up = "more" });
}

test "a cancel during a tool traces as recorded" {
    try runScenario(&.{ .name = "cancel_in_tool", .expected = @embedFile("loop_traces/cancel_in_tool.jsonl"), .steps = &tool_then_text, .cancel_in_tool = true });
}

test "a turn limit traces as recorded" {
    try runScenario(&.{ .name = "turn_limit", .expected = @embedFile("loop_traces/turn_limit.jsonl"), .steps = &tool_then_text, .max_iterations = 1 });
}
