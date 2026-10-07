const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const event_stream = @import("event_stream");
const agent = @import("agent");

pub const test_model = ai_types.Model{
    .id = "tui-fixture-model",
    .name = "TUI Fixture Model",
    .api = "tui-fixture-api",
    .provider = "tui-fixture-provider",
    .base_url = "https://example.invalid",
    .reasoning = false,
    .input = &.{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 8192,
    .max_tokens = 1024,
};

pub const ToolCallSpec = struct {
    id: []const u8,
    name: []const u8,
    arguments_json: []const u8,
};

pub const ResponseStep = union(enum) {
    text: []const u8,
    tool_calls: []const ToolCallSpec,
    provider_error: []const u8,
    wait_for_cancel: void,
};

pub const Scenario = struct {
    steps: []const ResponseStep,
    repeat_last: bool = false,
};

pub const MockProvider = struct {
    scenario: Scenario,
    call_count: usize = 0,
    last_model_id: []const u8 = "",
    last_message_count: usize = 0,

    pub fn init(scenario: Scenario) MockProvider {
        return .{ .scenario = scenario };
    }

    pub fn protocolClient(self: *MockProvider) agent.ProtocolClient {
        return .{ .stream_fn = stream, .ctx = self };
    }

    fn stream(
        ctx: ?*anyopaque,
        model: ai_types.Model,
        context: ai_types.Context,
        options: agent.ProtocolOptions,
        allocator: std.mem.Allocator,
    ) anyerror!*event_stream.AssistantMessageEventStream {
        const self: *MockProvider = @ptrCast(@alignCast(ctx.?));
        self.last_model_id = model.id;
        self.last_message_count = context.messages.len;

        const stream_ptr = try allocator.create(event_stream.AssistantMessageEventStream);
        errdefer allocator.destroy(stream_ptr);
        stream_ptr.* = event_stream.AssistantMessageEventStream.init(allocator);
        stream_ptr.ownership = .{ .owned = ai_types.cloneAssistantMessageEvent };
        errdefer stream_ptr.deinit();

        const step = if (self.call_count < self.scenario.steps.len)
            self.scenario.steps[self.call_count]
        else if (self.scenario.repeat_last and self.scenario.steps.len > 0)
            self.scenario.steps[self.scenario.steps.len - 1]
        else
            ResponseStep{ .text = "done" };
        self.call_count += 1;

        switch (step) {
            .text => |text| try pushTextResponse(stream_ptr, allocator, model, text),
            .tool_calls => |calls| {
                var arena = std.heap.ArenaAllocator.init(allocator);
                defer arena.deinit();
                const placed = try placeWorkspaceRoot(arena.allocator(), calls, workspaceRootOf(context.getSystemPrompt() orelse ""));
                try pushToolCalls(stream_ptr, allocator, model, placed);
            },
            .provider_error => |message| stream_ptr.completeWithError(message),
            .wait_for_cancel => {
                const token = options.cancel_token orelse return error.MissingCancelToken;
                var waits: usize = 0;
                while (!token.isCancelled()) : (waits += 1) {
                    if (waits >= 60_000) return error.CancelNotObserved;
                    compat.time.sleepMs(1);
                }
                try pushDoneAndComplete(stream_ptr, allocator, model, &.{}, .aborted);
            },
        }

        return stream_ptr;
    }
};

fn emptyAssistantMessage(model: ai_types.Model, stop_reason: ai_types.StopReason) ai_types.AssistantMessage {
    return .{
        .content = &.{},
        .api = model.api,
        .provider = model.provider,
        .model = model.id,
        .usage = .{},
        .stop_reason = stop_reason,
        .timestamp = compat.time.nowMillis(),
        .is_owned = false,
    };
}

fn deinitAssistantContentBlock(allocator: std.mem.Allocator, block: ai_types.AssistantContent) void {
    switch (block) {
        .text => |t| {
            allocator.free(t.text);
            if (t.text_signature) |s| allocator.free(s);
        },
        .thinking => |t| {
            allocator.free(t.thinking);
            if (t.thinking_signature) |s| allocator.free(s);
        },
        .tool_call => |tc| {
            allocator.free(tc.id);
            allocator.free(tc.name);
            allocator.free(tc.arguments_json);
            if (tc.thought_signature) |s| allocator.free(s);
        },
        .image => |img| {
            allocator.free(img.data);
            allocator.free(img.mime_type);
        },
    }
}

fn cloneAssistantContentBlock(allocator: std.mem.Allocator, block: ai_types.AssistantContent) !ai_types.AssistantContent {
    return switch (block) {
        .text => |t| blk: {
            const text = try allocator.dupe(u8, t.text);
            errdefer allocator.free(text);
            const text_signature = if (t.text_signature) |s| try allocator.dupe(u8, s) else null;
            errdefer if (text_signature) |s| allocator.free(s);
            break :blk .{ .text = .{ .text = text, .text_signature = text_signature } };
        },
        .thinking => |t| blk: {
            const thinking = try allocator.dupe(u8, t.thinking);
            errdefer allocator.free(thinking);
            const thinking_signature = if (t.thinking_signature) |s| try allocator.dupe(u8, s) else null;
            errdefer if (thinking_signature) |s| allocator.free(s);
            break :blk .{ .thinking = .{ .thinking = thinking, .thinking_signature = thinking_signature } };
        },
        .tool_call => |tc| blk: {
            const id = try allocator.dupe(u8, tc.id);
            errdefer allocator.free(id);
            const name = try allocator.dupe(u8, tc.name);
            errdefer allocator.free(name);
            const arguments_json = try allocator.dupe(u8, tc.arguments_json);
            errdefer allocator.free(arguments_json);
            const thought_signature = if (tc.thought_signature) |s| try allocator.dupe(u8, s) else null;
            errdefer if (thought_signature) |s| allocator.free(s);
            break :blk .{ .tool_call = .{
                .id = id,
                .name = name,
                .arguments_json = arguments_json,
                .thought_signature = thought_signature,
            } };
        },
        .image => |img| blk: {
            const data = try allocator.dupe(u8, img.data);
            errdefer allocator.free(data);
            const mime_type = try allocator.dupe(u8, img.mime_type);
            errdefer allocator.free(mime_type);
            break :blk .{ .image = .{ .data = data, .mime_type = mime_type } };
        },
    };
}

fn makeAssistantMessage(allocator: std.mem.Allocator, model: ai_types.Model, content: []const ai_types.AssistantContent, stop_reason: ai_types.StopReason) !ai_types.AssistantMessage {
    const blocks = try allocator.alloc(ai_types.AssistantContent, content.len);
    var initialized: usize = 0;
    errdefer {
        for (blocks[0..initialized]) |block| deinitAssistantContentBlock(allocator, block);
        allocator.free(blocks);
    }

    for (content, 0..) |block, i| {
        blocks[i] = try cloneAssistantContentBlock(allocator, block);
        initialized += 1;
    }

    return .{
        .content = blocks,
        .api = model.api,
        .provider = model.provider,
        .model = model.id,
        .usage = .{},
        .stop_reason = stop_reason,
        .timestamp = compat.time.nowMillis(),
    };
}

fn pushDoneAndComplete(stream: *event_stream.AssistantMessageEventStream, allocator: std.mem.Allocator, model: ai_types.Model, content: []const ai_types.AssistantContent, reason: ai_types.StopReason) !void {
    const event_message = try makeAssistantMessage(allocator, model, content, reason);
    errdefer {
        var msg = event_message;
        msg.deinit(allocator);
    }
    const result_message = try makeAssistantMessage(allocator, model, content, reason);
    errdefer {
        var msg = result_message;
        msg.deinit(allocator);
    }
    if (reason == .@"error") {
        try stream.push(.{ .@"error" = .{ .reason = reason, .err = event_message } });
    } else {
        try stream.push(.{ .done = .{ .reason = reason, .message = event_message } });
    }
    var pushed = event_message;
    pushed.deinit(allocator);
    stream.complete(result_message);
}

const think_open = "<think>";
const think_close = "</think>";

fn splitThinking(text: []const u8) struct { thinking: []const u8, answer: []const u8 } {
    if (!std.mem.startsWith(u8, text, think_open)) return .{ .thinking = "", .answer = text };
    const close = std.mem.indexOf(u8, text, think_close) orelse return .{ .thinking = "", .answer = text };
    var answer = text[close + think_close.len ..];
    if (std.mem.startsWith(u8, answer, "\n")) answer = answer[1..];
    return .{ .thinking = text[think_open.len..close], .answer = answer };
}

fn pushTextResponse(stream: *event_stream.AssistantMessageEventStream, allocator: std.mem.Allocator, model: ai_types.Model, text: []const u8) !void {
    const partial = emptyAssistantMessage(model, .stop);
    try stream.push(.{ .start = .{ .partial = partial } });
    const split = splitThinking(text);
    if (split.thinking.len > 0) {
        try stream.push(.{ .thinking_delta = .{ .content_index = 0, .delta = split.thinking, .partial = partial } });
        try stream.push(.{ .text_delta = .{ .content_index = 1, .delta = split.answer, .partial = partial } });
        const content = [_]ai_types.AssistantContent{
            .{ .thinking = .{ .thinking = split.thinking } },
            .{ .text = .{ .text = split.answer } },
        };
        try pushDoneAndComplete(stream, allocator, model, &content, .stop);
        return;
    }
    try stream.push(.{ .text_delta = .{ .content_index = 0, .delta = text, .partial = partial } });

    const content = [_]ai_types.AssistantContent{.{ .text = .{ .text = text } }};
    try pushDoneAndComplete(stream, allocator, model, &content, .stop);
}

test "mock provider splits a leading think block into thinking and text deltas" {
    const steps = [_]ResponseStep{.{ .text = "<think>plan first</think>\nthen answer" }};
    var provider = MockProvider.init(.{ .steps = &steps });
    const client = provider.protocolClient();
    const stream_ptr = try client.stream(test_model, .{ .messages = &.{}, .is_owned = false }, .{}, std.testing.allocator);
    defer {
        stream_ptr.deinit();
        std.testing.allocator.destroy(stream_ptr);
    }

    var saw_thinking = false;
    var saw_text = false;
    while (stream_ptr.wait()) |event| {
        const ev = event;
        defer stream_ptr.releaseEvent(ev);
        if (ev == .thinking_delta) saw_thinking = std.mem.eql(u8, ev.thinking_delta.delta, "plan first");
        if (ev == .text_delta) saw_text = std.mem.eql(u8, ev.text_delta.delta, "then answer");
    }
    try std.testing.expect(saw_thinking);
    try std.testing.expect(saw_text);
}

pub const workspace_root_placeholder = "$WORKSPACE_ROOT";
const workspace_root_line = "Default workspace root: ";

fn workspaceRootOf(system_prompt: []const u8) []const u8 {
    const start = (std.mem.indexOf(u8, system_prompt, workspace_root_line) orelse return "") + workspace_root_line.len;
    const end = std.mem.indexOfScalarPos(u8, system_prompt, start, '\n') orelse system_prompt.len;
    return system_prompt[start..end];
}

fn placeWorkspaceRoot(arena: std.mem.Allocator, calls: []const ToolCallSpec, root: []const u8) ![]const ToolCallSpec {
    const placed = try arena.alloc(ToolCallSpec, calls.len);
    for (calls, placed) |call, *slot| {
        slot.* = call;
        if (std.mem.indexOf(u8, call.arguments_json, workspace_root_placeholder) == null) continue;
        const quoted = try std.json.Stringify.valueAlloc(arena, root, .{});
        slot.arguments_json = try std.mem.replaceOwned(u8, arena, call.arguments_json, workspace_root_placeholder, quoted[1 .. quoted.len - 1]);
    }
    return placed;
}

fn pushToolCalls(stream: *event_stream.AssistantMessageEventStream, allocator: std.mem.Allocator, model: ai_types.Model, calls: []const ToolCallSpec) !void {
    const partial = emptyAssistantMessage(model, .tool_use);
    try stream.push(.{ .start = .{ .partial = partial } });

    const content = try allocator.alloc(ai_types.AssistantContent, calls.len);
    defer allocator.free(content);
    for (calls, 0..) |call, i| {
        content[i] = .{ .tool_call = .{ .id = call.id, .name = call.name, .arguments_json = call.arguments_json } };
        try stream.push(.{ .toolcall_delta = .{ .content_index = i, .delta = call.arguments_json, .partial = partial } });
    }

    try pushDoneAndComplete(stream, allocator, model, content, .tool_use);
}

test "a tool call's workspace root placeholder becomes the root the system prompt names" {
    const calls = [_]ToolCallSpec{.{ .id = "call-1", .name = "Shell", .arguments_json = "{\"workspace_root\":\"$WORKSPACE_ROOT\",\"command\":\"pwd\"}" }};
    const steps = [_]ResponseStep{.{ .tool_calls = &calls }};
    var provider = MockProvider.init(.{ .steps = &steps });
    const client = provider.protocolClient();
    const prompt = "Default workspace root: /work/tree \"one\"\nPass the default workspace root as `workspace_root`.";
    const stream_ptr = try client.stream(test_model, .{ .system_prompt = .initBorrowed(prompt), .messages = &.{}, .is_owned = false }, .{}, std.testing.allocator);
    defer {
        stream_ptr.deinit();
        std.testing.allocator.destroy(stream_ptr);
    }
    var placed = false;
    while (stream_ptr.wait()) |event| {
        const ev = event;
        defer stream_ptr.releaseEvent(ev);
        if (ev == .toolcall_delta) placed = std.mem.eql(u8, ev.toolcall_delta.delta, "{\"workspace_root\":\"/work/tree \\\"one\\\"\",\"command\":\"pwd\"}");
    }
    try std.testing.expect(placed);
}

test "mock provider streams canned text" {
    const steps = [_]ResponseStep{.{ .text = "hello" }};
    var provider = MockProvider.init(.{ .steps = &steps });
    const client = provider.protocolClient();
    const stream_ptr = try client.stream(test_model, .{ .messages = &.{}, .is_owned = false }, .{}, std.testing.allocator);
    defer {
        stream_ptr.deinit();
        std.testing.allocator.destroy(stream_ptr);
    }

    var saw_delta = false;
    while (stream_ptr.wait()) |event| {
        const ev = event;
        defer stream_ptr.releaseEvent(ev);
        if (ev == .text_delta) saw_delta = std.mem.eql(u8, ev.text_delta.delta, "hello");
    }
    try std.testing.expect(saw_delta);
    try std.testing.expectEqual(@as(usize, 1), provider.call_count);
    try std.testing.expectEqualStrings(test_model.id, provider.last_model_id);
    try std.testing.expectEqual(@as(usize, 0), provider.last_message_count);
}

test "repeat_last scenario keeps replaying the final step" {
    const steps = [_]ResponseStep{.{ .text = "again" }};
    var provider = MockProvider.init(.{ .steps = &steps, .repeat_last = true });

    for (0..3) |_| {
        const client = provider.protocolClient();
        const stream_ptr = try client.stream(test_model, .{ .messages = &.{}, .is_owned = false }, .{}, std.testing.allocator);
        defer {
            stream_ptr.deinit();
            std.testing.allocator.destroy(stream_ptr);
        }
        var saw_delta = false;
        while (stream_ptr.wait()) |event| {
            const ev = event;
            defer stream_ptr.releaseEvent(ev);
            if (ev == .text_delta) saw_delta = std.mem.eql(u8, ev.text_delta.delta, "again");
        }
        try std.testing.expect(saw_delta);
    }
    try std.testing.expectEqual(@as(usize, 3), provider.call_count);
}
