const std = @import("std");
const json_encode = @import("json_encode");
const compat = @import("compat");
const ai_types = @import("ai_types");
const event_stream_module = @import("event_stream");
const types = @import("agent_types");
const permission = @import("permission");
const json_writer = @import("json_writer");
const owned_slice_mod = @import("owned_slice");

pub const AgentEvent = types.AgentEvent;
pub const AgentEventStream = types.AgentEventStream;
pub const AgentLoopConfig = types.AgentLoopConfig;
pub const AgentLoopResult = types.AgentLoopResult;
pub const AgentContext = types.AgentContext;
pub const AgentTool = types.AgentTool;
pub const AgentToolResult = types.AgentToolResult;
pub const ProtocolClient = types.ProtocolClient;
pub const ProtocolOptions = types.ProtocolOptions;

const ByteTokenEstimate = struct {
    bytes: u64 = 0,
    estimated_tokens: u64 = 0,
};

const ToolResultUsage = struct {
    result_bytes: u64 = 0,
    details_bytes: u64 = 0,
    total_bytes: u64 = 0,
    estimated_tokens: u64 = 0,
    artifact_count: u32 = 0,
};

const ContextUsage = struct {
    system_prompt: ByteTokenEstimate = .{},
    messages: ByteTokenEstimate = .{},
    tools: ByteTokenEstimate = .{},

    fn totalBytes(self: ContextUsage) u64 {
        return self.system_prompt.bytes + self.messages.bytes + self.tools.bytes;
    }

    fn totalEstimatedTokens(self: ContextUsage) u64 {
        return self.system_prompt.estimated_tokens + self.messages.estimated_tokens + self.tools.estimated_tokens;
    }
};

fn estimateTextTokens(len: usize) u64 {
    if (len == 0) return 0;
    return @intCast((len + 3) / 4);
}

fn addText(est: *ByteTokenEstimate, text: []const u8) void {
    est.bytes += text.len;
    est.estimated_tokens += estimateTextTokens(text.len);
}

fn addImage(est: *ByteTokenEstimate, image: ai_types.ImageContent) void {
    est.bytes += image.data.len + image.mime_type.len;
    est.estimated_tokens += 850;
}

fn estimateUserContentPart(part: ai_types.UserContentPart) ByteTokenEstimate {
    var est: ByteTokenEstimate = .{};
    switch (part) {
        .text => |text| addText(&est, text.text),
        .image => |image| addImage(&est, image),
    }
    return est;
}

fn estimateUserContentParts(parts: []const ai_types.UserContentPart) ByteTokenEstimate {
    var est: ByteTokenEstimate = .{};
    for (parts) |part| {
        const part_est = estimateUserContentPart(part);
        est.bytes += part_est.bytes;
        est.estimated_tokens += part_est.estimated_tokens;
    }
    return est;
}

fn estimateAssistantContent(block: ai_types.AssistantContent) ByteTokenEstimate {
    var est: ByteTokenEstimate = .{};
    switch (block) {
        .text => |text| addText(&est, text.text),
        .thinking => |thinking| addText(&est, thinking.thinking),
        .image => |image| addImage(&est, image),
        .tool_call => |tool_call| {
            addText(&est, tool_call.id);
            addText(&est, tool_call.name);
            addText(&est, tool_call.arguments_json);
            est.estimated_tokens += 50;
        },
    }
    return est;
}

fn estimateMessage(message: ai_types.Message) ByteTokenEstimate {
    var est: ByteTokenEstimate = .{ .estimated_tokens = 5 };
    switch (message) {
        .user => |user| switch (user.content) {
            .text => |text| addText(&est, text),
            .parts => |parts| {
                const parts_est = estimateUserContentParts(parts);
                est.bytes += parts_est.bytes;
                est.estimated_tokens += parts_est.estimated_tokens;
            },
        },
        .assistant => |assistant| {
            for (assistant.content) |block| {
                const block_est = estimateAssistantContent(block);
                est.bytes += block_est.bytes;
                est.estimated_tokens += block_est.estimated_tokens;
            }
        },
        .tool_result => |tool_result| {
            addText(&est, tool_result.tool_call_id);
            addText(&est, tool_result.tool_name);
            const parts_est = estimateUserContentParts(tool_result.content);
            est.bytes += parts_est.bytes;
            est.estimated_tokens += parts_est.estimated_tokens;
            if (tool_result.getDetailsJson()) |details| addText(&est, details);
        },
    }
    return est;
}

fn estimateMessages(messages: []const ai_types.Message) ByteTokenEstimate {
    var est: ByteTokenEstimate = .{};
    for (messages) |message| {
        const msg_est = estimateMessage(message);
        est.bytes += msg_est.bytes;
        est.estimated_tokens += msg_est.estimated_tokens;
    }
    return est;
}

fn estimateToolDefinitions(tools: ?[]const ai_types.Tool) ByteTokenEstimate {
    var est: ByteTokenEstimate = .{};
    const defs = tools orelse return est;
    for (defs) |tool| {
        addText(&est, tool.name);
        addText(&est, tool.description);
        addText(&est, tool.parameters_schema_json);
        est.estimated_tokens += 12;
    }
    return est;
}

fn estimateContextUsage(context: ai_types.Context) ContextUsage {
    const system_prompt = context.getSystemPrompt() orelse "";
    return .{
        .system_prompt = .{
            .bytes = system_prompt.len,
            .estimated_tokens = estimateTextTokens(system_prompt.len),
        },
        .messages = estimateMessages(context.messages),
        .tools = estimateToolDefinitions(context.tools),
    };
}

pub fn estimatePromptTokens(context: ai_types.Context) u64 {
    return estimateContextUsage(context).totalEstimatedTokens();
}

pub fn estimateMessageTokens(message: ai_types.Message) u64 {
    return estimateMessage(message).estimated_tokens;
}

const full_window_output_tokens: u64 = 1024;

fn inflated(estimate: u64) u64 {
    return estimate + estimate / 3;
}

fn headroom(context_window: u64) u64 {
    return @max(full_window_output_tokens, context_window / 64);
}

pub fn promptTokens(context: ai_types.Context) u64 {
    const messages = context.messages;
    var index = messages.len;
    while (index > 0) {
        index -= 1;
        if (messages[index] != .assistant) continue;
        const usage = messages[index].assistant.usage;
        const reported = usage.input + usage.cache_read + usage.cache_write;
        if (reported == 0) continue;
        return reported + usage.output + inflated(estimateMessages(messages[index + 1 ..]).estimated_tokens);
    }
    return inflated(estimatePromptTokens(context));
}

pub fn outputLimit(model: ai_types.Model, requested: ?u32, context: ai_types.Context) ?u32 {
    const wanted: u64 = requested orelse model.max_tokens;
    if (model.context_window == 0 or wanted == 0) return requested;
    const prompt = promptTokens(context) + headroom(model.context_window);
    if (model.context_window <= prompt) return @intCast(@min(wanted, full_window_output_tokens));
    const room = model.context_window - prompt;
    if (room >= wanted) return requested;
    return @intCast(room);
}

pub const default_output_tokens: u32 = 32_768;

pub const OutputSetting = union(enum) {
    auto,
    max,
    tokens: u32,
};

pub fn outputRequest(model: ai_types.Model, setting: OutputSetting) u32 {
    const asked: u32 = switch (setting) {
        .auto => default_output_tokens,
        .max => if (model.max_tokens > 0) model.max_tokens else default_output_tokens,
        .tokens => |count| count,
    };
    if (model.max_tokens == 0) return asked;
    return @min(asked, model.max_tokens);
}

fn raisedOutput(requested: ?u32, model: ai_types.Model) ?u32 {
    const current = requested orelse return null;
    if (current >= model.max_tokens) return null;
    return model.max_tokens;
}

fn outputLimitModel(context_window: u32, max_tokens: u32) ai_types.Model {
    return .{
        .id = "test-model",
        .name = "Test",
        .api = "test-api",
        .provider = "test-provider",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = context_window,
        .max_tokens = max_tokens,
    };
}

test "outputRequest asks for the default, the model's maximum, or a count, and never above the maximum the model reports" {
    const large = outputLimitModel(1_048_576, 393_216);
    try std.testing.expectEqual(default_output_tokens, outputRequest(large, .auto));
    try std.testing.expectEqual(@as(u32, 393_216), outputRequest(large, .max));
    try std.testing.expectEqual(@as(u32, 100_000), outputRequest(large, .{ .tokens = 100_000 }));
    try std.testing.expectEqual(@as(u32, 393_216), outputRequest(large, .{ .tokens = 500_000 }));

    const small = outputLimitModel(128_000, 8_192);
    try std.testing.expectEqual(@as(u32, 8_192), outputRequest(small, .auto));
    try std.testing.expectEqual(@as(u32, 8_192), outputRequest(small, .{ .tokens = 20_000 }));

    const unreported = outputLimitModel(128_000, 0);
    try std.testing.expectEqual(default_output_tokens, outputRequest(unreported, .auto));
    try std.testing.expectEqual(default_output_tokens, outputRequest(unreported, .max));
    try std.testing.expectEqual(@as(u32, 50_000), outputRequest(unreported, .{ .tokens = 50_000 }));
}

test "raisedOutput lifts a limit below the model's maximum to it, and nothing else" {
    const model = outputLimitModel(1_048_576, 393_216);
    try std.testing.expectEqual(@as(?u32, 393_216), raisedOutput(32_768, model));
    try std.testing.expectEqual(@as(?u32, null), raisedOutput(393_216, model));
    try std.testing.expectEqual(@as(?u32, null), raisedOutput(null, model));
    try std.testing.expectEqual(@as(?u32, null), raisedOutput(32_768, outputLimitModel(128_000, 0)));
}

test "outputLimit asks for no more output than the context window leaves after an estimated prompt and its headroom, and leaves an unset limit unset when it fits" {
    const text = "a" ** 3000;
    const messages = [_]ai_types.Message{.{ .user = .{ .content = .{ .text = text }, .timestamp = 0 } }};
    const context: ai_types.Context = .{ .messages = &messages };
    const prompt = inflated(estimatePromptTokens(context)) + full_window_output_tokens;

    try std.testing.expectEqual(@as(?u32, @intCast(10_000 - prompt)), outputLimit(outputLimitModel(10_000, 9_000), null, context));
    try std.testing.expectEqual(@as(?u32, 500), outputLimit(outputLimitModel(10_000, 9_000), 500, context));
    try std.testing.expectEqual(@as(?u32, 1024), outputLimit(outputLimitModel(@intCast(prompt), 9_000), null, context));
    try std.testing.expectEqual(@as(?u32, 700), outputLimit(outputLimitModel(@intCast(prompt), 700), null, context));
    try std.testing.expectEqual(@as(?u32, 500), outputLimit(outputLimitModel(@intCast(prompt + 500), 9_000), null, context));
    try std.testing.expectEqual(@as(?u32, 9_000), outputLimit(outputLimitModel(0, 9_000), 9_000, context));
    try std.testing.expectEqual(@as(?u32, null), outputLimit(outputLimitModel(1_000_000, 9_000), null, context));
}

test "outputLimit counts the prompt from the provider's last report when a reply carries one" {
    const messages = [_]ai_types.Message{
        .{ .user = .{ .content = .{ .text = "a" ** 40_000 }, .timestamp = 0 } },
        .{ .assistant = .{
            .content = &.{.{ .text = .{ .text = "ok" } }},
            .api = "test-api",
            .provider = "test-provider",
            .model = "test-model",
            .usage = .{ .input = 6_000, .output = 200, .cache_read = 3_000, .cache_write = 800 },
            .stop_reason = .stop,
            .timestamp = 0,
        } },
        .{ .user = .{ .content = .{ .text = "b" ** 400 }, .timestamp = 0 } },
    };
    const context: ai_types.Context = .{ .messages = &messages };
    const prompt = 6_000 + 3_000 + 800 + 200 + inflated(estimateMessages(messages[2..]).estimated_tokens) + full_window_output_tokens;

    try std.testing.expectEqual(@as(?u32, @intCast(20_000 - prompt)), outputLimit(outputLimitModel(20_000, 19_000), null, context));
}

test "outputLimit leaves a sixty-fourth of a large window unasked, so a prompt the estimate undercounts still fits" {
    const messages = [_]ai_types.Message{
        .{ .user = .{ .content = .{ .text = "a" ** 400 }, .timestamp = 0 } },
        .{ .assistant = .{
            .content = &.{.{ .text = .{ .text = "ok" } }},
            .api = "test-api",
            .provider = "test-provider",
            .model = "test-model",
            .usage = .{ .input = 656_000, .output = 100, .cache_read = 0, .cache_write = 0 },
            .stop_reason = .stop,
            .timestamp = 0,
        } },
        .{ .user = .{ .content = .{ .text = "b" ** 400 }, .timestamp = 0 } },
    };
    const context: ai_types.Context = .{ .messages = &messages };
    const window: u32 = 1_048_576;
    const prompt = 656_000 + 100 + inflated(estimateMessages(messages[2..]).estimated_tokens);
    const asked = outputLimit(outputLimitModel(window, 393_216), null, context).?;

    try std.testing.expectEqual(@as(u32, @intCast(window - prompt - window / 64)), asked);
    try std.testing.expect(prompt + asked + 16_000 <= window);
}

fn pushAgentEvent(event_stream: *AgentEventStream, event: AgentEvent) !void {
    if (!event_stream.pushBlocking(event)) {
        return error.StreamCompleted;
    }
}

fn delayedAgentEventPush(stream: *AgentEventStream) void {
    pushAgentEvent(stream, .{ .agent_end = .{} }) catch {};
}

test "agent event push blocks instead of dropping ordered events" {
    const allocator = std.testing.allocator;
    var stream = AgentEventStream.init(allocator);
    defer stream.deinit();

    try pushAgentEvent(&stream, .agent_start);
    for (0..AgentEventStream.usable_capacity - 1) |_| {
        try pushAgentEvent(&stream, .turn_start);
    }

    const thread = try std.Thread.spawn(.{}, delayedAgentEventPush, .{&stream});

    const first = stream.poll() orelse return error.ExpectedEvent;
    thread.join();

    try std.testing.expectEqual(AgentEvent.agent_start, first);

    var saw_agent_start = false;
    var saw_agent_end = false;
    var count: usize = 0;
    while (stream.poll()) |event| {
        count += 1;
        if (event == .agent_start) saw_agent_start = true;
        if (event == .agent_end) saw_agent_end = true;
    }

    try std.testing.expectEqual(@as(usize, AgentEventStream.usable_capacity), count);
    try std.testing.expect(!saw_agent_start);
    try std.testing.expect(saw_agent_end);
}

fn emitContextUsage(event_stream: *AgentEventStream, context: ai_types.Context) !void {
    const usage = estimateContextUsage(context);
    const system_prompt: []const u8 = context.getSystemPrompt() orelse "";
    try pushAgentEvent(event_stream, .{ .prompt_segment_usage = .{
        .segment = .system_prompt,
        .cache_role = .stable,
        .bytes = usage.system_prompt.bytes,
        .estimated_tokens = usage.system_prompt.estimated_tokens,
        .item_count = if (system_prompt.len > 0) 1 else 0,
    } });
    try pushAgentEvent(event_stream, .{ .prompt_segment_usage = .{
        .segment = .tool_definitions,
        .cache_role = .stable,
        .bytes = usage.tools.bytes,
        .estimated_tokens = usage.tools.estimated_tokens,
        .item_count = if (context.tools) |tools| @intCast(tools.len) else 0,
    } });
    try pushAgentEvent(event_stream, .{ .prompt_segment_usage = .{
        .segment = .message_history,
        .cache_role = .dynamic,
        .bytes = usage.messages.bytes,
        .estimated_tokens = usage.messages.estimated_tokens,
        .item_count = @intCast(context.messages.len),
    } });
    try pushAgentEvent(event_stream, .{ .context_usage = .{
        .system_prompt_bytes = usage.system_prompt.bytes,
        .message_bytes = usage.messages.bytes,
        .tool_definition_bytes = usage.tools.bytes,
        .total_bytes = usage.totalBytes(),
        .estimated_tokens = usage.totalEstimatedTokens(),
        .message_count = @intCast(context.messages.len),
        .tool_count = if (context.tools) |tools| @intCast(tools.len) else 0,
    } });
}

fn measureToolResult(result: AgentToolResult) ToolResultUsage {
    const content = estimateUserContentParts(result.content.slice());
    const details_bytes: u64 = if (result.getDetailsJson()) |details| details.len else 0;
    return .{
        .result_bytes = content.bytes,
        .details_bytes = details_bytes,
        .total_bytes = content.bytes + details_bytes,
        .estimated_tokens = content.estimated_tokens + estimateTextTokens(@intCast(details_bytes)),
        .artifact_count = @intCast(result.artifacts.slice().len),
    };
}

fn buildToolsArray(
    allocator: std.mem.Allocator,
    tools: ?[]const AgentTool,
) !?[]ai_types.Tool {
    const agent_tools = tools orelse return null;
    if (agent_tools.len == 0) return null;

    var result = try allocator.alloc(ai_types.Tool, agent_tools.len);
    for (agent_tools, 0..) |tool, i| {
        result[i] = try tool.toTool(allocator);
    }
    return result;
}

fn findTool(tools: ?[]const AgentTool, name: []const u8) ?AgentTool {
    const agent_tools = tools orelse return null;
    for (agent_tools) |tool| {
        if (std.mem.eql(u8, tool.name, name)) return tool;
    }
    return null;
}

fn validateToolArguments(
    allocator: std.mem.Allocator,
    tool: AgentTool,
    args_json: []const u8,
) ![]const u8 {
    _ = allocator;
    _ = tool.parameters_schema_json;

    return args_json;
}

fn createErrorResult(allocator: std.mem.Allocator, err: anyerror) !AgentToolResult {
    const error_name = @errorName(err);
    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    errdefer allocator.free(content);
    const text = try std.fmt.allocPrint(allocator, "Tool execution failed: {s}", .{error_name});
    errdefer allocator.free(text);
    content[0] = .{ .text = .{
        .text = text,
    } };
    const details = try std.json.Stringify.valueAlloc(allocator, .{ .ok = false, .err = error_name }, .{});
    errdefer allocator.free(details);
    return .{
        .content = types.OwnedSlice(ai_types.UserContentPart).initOwned(content),
        .details_json = ai_types.OwnedSlice(u8).initOwned(details),
        .is_error = true,
    };
}

fn truncatedToolCallResult(allocator: std.mem.Allocator, tool_name: []const u8) !AgentToolResult {
    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    errdefer allocator.free(content);
    const text = try std.fmt.allocPrint(allocator, "Tool call \"{s}\" was not run: the reply hit the output token limit, so its arguments may be cut off. Call the tool again with complete arguments.", .{tool_name});
    errdefer allocator.free(text);
    content[0] = .{ .text = .{ .text = text } };
    const details = try std.json.Stringify.valueAlloc(allocator, .{ .ok = false, .err = "OutputTruncated" }, .{});
    return .{
        .content = types.OwnedSlice(ai_types.UserContentPart).initOwned(content),
        .details_json = ai_types.OwnedSlice(u8).initOwned(details),
        .is_error = true,
    };
}

fn buildAndFreeTruncatedToolCallResult(allocator: std.mem.Allocator, tool_name: []const u8) !void {
    var result = try truncatedToolCallResult(allocator, tool_name);
    result.deinit(allocator);
}

test "truncatedToolCallResult frees what it built when an allocation fails" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, buildAndFreeTruncatedToolCallResult, .{"write"});
}

fn rejectedToolResult(allocator: std.mem.Allocator) !AgentToolResult {
    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    content[0] = .{ .text = .{
        .text = try allocator.dupe(u8, "Tool execution rejected by user"),
    } };
    return .{
        .content = types.OwnedSlice(ai_types.UserContentPart).initOwned(content),
        .details_json = types.OwnedSlice(u8).initOwned(try allocator.dupe(u8, "{\"rejected\":true}")),
        .is_error = true,
    };
}

fn createToolResultMessage(
    allocator: std.mem.Allocator,
    tool_call: ai_types.ToolCall,
    result: AgentToolResult,
    is_error: bool,
) !ai_types.ToolResultMessage {
    const tool_call_id = try allocator.dupe(u8, tool_call.id);
    errdefer allocator.free(tool_call_id);
    const tool_name = try allocator.dupe(u8, tool_call.name);
    errdefer allocator.free(tool_name);

    const details_json = if (result.getDetailsJson()) |details|
        if (result.details_json.is_owned)
            ai_types.OwnedSlice(u8).initOwned(@constCast(details))
        else
            ai_types.OwnedSlice(u8).initOwned(try allocator.dupe(u8, details))
    else
        ai_types.OwnedSlice(u8).initBorrowed("");
    errdefer {
        var mutable = details_json;
        mutable.deinit(allocator);
    }

    const artifacts = if (result.artifacts.is_owned)
        ai_types.OwnedSlice(ai_types.ArtifactReference).initOwned(@constCast(result.artifacts.slice()))
    else
        ai_types.OwnedSlice(ai_types.ArtifactReference).initBorrowed(result.artifacts.slice());

    return makeToolResultMessage(tool_call_id, tool_name, result, details_json, artifacts, is_error);
}

fn makeToolResultMessage(
    tool_call_id: []const u8,
    tool_name: []const u8,
    result: AgentToolResult,
    details_json: ai_types.OwnedSlice(u8),
    artifacts: ai_types.OwnedSlice(ai_types.ArtifactReference),
    is_error: bool,
) ai_types.ToolResultMessage {
    return .{
        .tool_call_id = tool_call_id,
        .tool_name = tool_name,
        .content = result.content.slice(),
        .details_json = details_json,
        .artifacts = artifacts,
        .working_directory = result.working_directory,
        .working_directory_observed = result.working_directory_observed,
        .is_error = is_error,
        .timestamp = compat.time.nowMillis(),
    };
}

const ToolUpdateContext = struct {
    event_stream: *AgentEventStream,
    tool_call_id: []const u8,
    tool_name: []const u8,
    args_json: []const u8,
};

fn onToolUpdate(ctx: ?*anyopaque, tool_call_id: []const u8, tool_name: []const u8, partial_result_json: []const u8) void {
    const context: *ToolUpdateContext = @ptrCast(@alignCast(ctx));

    context.event_stream.push(.{ .tool_execution_update = .{
        .tool_call_id = tool_call_id,
        .tool_name = tool_name,
        .args_json = context.args_json,
        .partial_result_json = partial_result_json,
    } }) catch {};
}

fn skipToolCall(
    allocator: std.mem.Allocator,
    tool_call: ai_types.ToolCall,
    event_stream: *AgentEventStream,
) !ai_types.ToolResultMessage {
    const skip_message = "Skipped due to queued user message.";

    try pushAgentEvent(event_stream, .{ .tool_execution_start = .{
        .tool_call_id = tool_call.id,
        .tool_name = tool_call.name,
        .args_json = tool_call.arguments_json,
    } });

    try pushAgentEvent(event_stream, .{ .tool_execution_end = .{
        .tool_call_id = tool_call.id,
        .tool_name = tool_call.name,
        .result_json = skip_message,
        .is_error = true,
    } });

    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    content[0] = .{ .text = .{
        .text = try allocator.dupe(u8, skip_message),
    } };

    return .{
        .tool_call_id = try allocator.dupe(u8, tool_call.id),
        .tool_name = try allocator.dupe(u8, tool_call.name),
        .content = content,
        .details_json = ai_types.OwnedSlice(u8).initBorrowed(""),
        .is_error = true,
        .timestamp = compat.time.nowMillis(),
    };
}

fn serializeToolResultContent(allocator: std.mem.Allocator, content: []const ai_types.UserContentPart) ![]u8 {
    var buf = std.ArrayList(u8).empty;
    errdefer buf.deinit(allocator);
    var w = json_writer.JsonWriter.init(&buf, allocator);
    try w.beginArray();
    for (content) |part| {
        try w.beginObject();
        switch (part) {
            .text => |text| {
                try w.writeStringField("type", "text");
                try w.writeStringField("text", text.text);
                if (text.text_signature) |sig| try w.writeStringField("text_signature", sig);
            },
            .image => |image| {
                try w.writeStringField("type", "image");
                try w.writeStringField("data", image.data);
                try w.writeStringField("mime_type", image.mime_type);
            },
        }
        try w.endObject();
    }
    try w.endArray();
    const out = try allocator.dupe(u8, buf.items);
    buf.deinit(allocator);
    return out;
}

fn finalizeToolExecution(
    allocator: std.mem.Allocator,
    config: AgentLoopConfig,
    event_stream: *AgentEventStream,
    results: *std.ArrayList(ai_types.ToolResultMessage),
    tool_call: ai_types.ToolCall,
    args_json: []const u8,
    result: *AgentToolResult,
    is_error: bool,
) !void {
    const raw_usage = measureToolResult(result.*);

    var owned = result.*;
    errdefer owned.deinit(allocator);
    result.content = ai_types.OwnedSlice(ai_types.UserContentPart).initBorrowed(&.{});
    result.details_json = ai_types.OwnedSlice(u8).initBorrowed("");
    result.artifacts = ai_types.OwnedSlice(ai_types.ArtifactReference).initBorrowed(&.{});
    result.working_directory = ai_types.OwnedSlice(u8).initBorrowed("");

    if (config.tool_output_middleware_fn) |middleware| {
        middleware(config.tool_output_middleware_ctx, .{
            .tool_call_id = tool_call.id,
            .tool_name = tool_call.name,
            .args_json = args_json,
            .is_error = is_error,
            .raw_result_bytes = raw_usage.result_bytes,
            .raw_details_bytes = raw_usage.details_bytes,
            .raw_total_bytes = raw_usage.total_bytes,
        }, &owned, allocator) catch |err| return err;
    }

    const returned_usage = measureToolResult(owned);

    const content_json = try serializeToolResultContent(allocator, owned.content.slice());
    var content_json_owned = true;
    defer if (content_json_owned) allocator.free(content_json);
    const args_bytes: u64 = @intCast(args_json.len);

    var tool_result_msg = try createToolResultMessage(allocator, tool_call, owned, is_error);
    var unreached = tool_result_msg;
    var message_listed = false;
    errdefer if (!message_listed) unreached.deinit(allocator);
    owned = AgentToolResult{};
    try results.append(allocator, tool_result_msg);
    tool_result_msg = undefined;
    message_listed = true;

    const appended = &results.items[results.items.len - 1];
    const event_result_json = appended.getDetailsJson() orelse "null";
    const event_artifacts = appended.artifacts.slice();

    content_json_owned = false;
    pushAgentEvent(event_stream, .{ .tool_execution_end = .{
        .tool_call_id = tool_call.id,
        .tool_name = tool_call.name,
        .result_json = event_result_json,
        .content_json = types.OwnedSlice(u8).initOwned(content_json),
        .is_error = is_error,
        .args_bytes = args_bytes,
        .raw_result_bytes = raw_usage.result_bytes,
        .returned_result_bytes = returned_usage.result_bytes,
        .raw_details_bytes = raw_usage.details_bytes,
        .returned_details_bytes = returned_usage.details_bytes,
        .raw_total_bytes = raw_usage.total_bytes + args_bytes,
        .returned_total_bytes = returned_usage.total_bytes + args_bytes,
        .estimated_returned_tokens = returned_usage.estimated_tokens + estimateTextTokens(args_json.len),
        .artifact_count = returned_usage.artifact_count,
        .artifacts = event_artifacts,
    } }) catch |err| {
        allocator.free(content_json);
        return err;
    };
}

fn persistLegacyDecision(allocator: std.mem.Allocator, engine: *permission.PermissionEngine, tool: AgentTool, name: []const u8, args: []const u8, decision: permission.PermissionDecision) void {
    const call = permission.parseToolCallOf(allocator, tool.operation, name, args) catch return;
    defer permission.deinitParsedToolCall(allocator, call);
    if (permission.canPersistDecision(call)) engine.persistDecision(call, decision) catch {};
}

fn runLegacyApproval(tool: AgentTool, approval_request: types.ToolApprovalRequest, allocator: std.mem.Allocator) types.ToolApprovalDecision {
    if (tool.approval_ui_fn) |notify| {
        notify(tool.approval_ui_ctx, approval_request, allocator);
    }
    if (tool.approval_fn) |approval| {
        return approval(tool.approval_ctx, approval_request);
    }
    return .approve;
}

const ToolExecutionResult = struct {
    tool_results: []ai_types.ToolResultMessage,
    compact_args: [][]u8 = &.{},
    retained_args: [][]u8 = &.{},
    has_steering: bool,
    steering_messages: ?[]const ai_types.Message,

    fn deinit(self: *ToolExecutionResult, allocator: std.mem.Allocator) void {
        for (self.tool_results) |*result| {
            result.deinit(allocator);
        }
        allocator.free(self.tool_results);
        for (self.compact_args) |args| allocator.free(args);
        allocator.free(self.compact_args);
        for (self.retained_args) |args| allocator.free(args);
        allocator.free(self.retained_args);
        if (self.steering_messages) |msgs| {
            const mut_msgs: []ai_types.Message = @constCast(msgs);
            for (mut_msgs) |*msg| msg.deinit(allocator);
            allocator.free(mut_msgs);
        }
    }

    fn deinitAfterToolResultsTransferred(self: *const ToolExecutionResult, allocator: std.mem.Allocator) void {
        allocator.free(self.tool_results);
        for (self.compact_args) |args| allocator.free(args);
        allocator.free(self.compact_args);
        for (self.retained_args) |args| allocator.free(args);
        allocator.free(self.retained_args);
        if (self.steering_messages) |msgs| {
            const mut_msgs: []ai_types.Message = @constCast(msgs);
            for (mut_msgs) |*msg| msg.deinit(allocator);
            allocator.free(mut_msgs);
        }
    }
};

test "ToolExecutionResult transferred cleanup frees compact args" {
    const allocator = std.testing.allocator;

    const tool_results = try allocator.alloc(ai_types.ToolResultMessage, 0);
    const compact_args = try allocator.alloc([]u8, 1);
    compact_args[0] = try allocator.dupe(u8, "{\"compact_output\":false}");

    const result = ToolExecutionResult{
        .tool_results = tool_results,
        .compact_args = compact_args,
        .has_steering = false,
        .steering_messages = null,
    };
    result.deinitAfterToolResultsTransferred(allocator);
}

fn supportsCompactToolOutput(allocator: std.mem.Allocator, tool: AgentTool) !bool {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, tool.parameters_schema_json, .{});
    defer parsed.deinit();
    if (parsed.value != .object) return false;
    const properties = parsed.value.object.get("properties") orelse return false;
    if (properties != .object) return false;
    return properties.object.contains("compact_output");
}

test "compact output support requires root schema property" {
    const nested = AgentTool{
        .label = "Nested",
        .name = "nested",
        .description = "Nested compact_output mention only.",
        .parameters_schema_json = "{\"type\":\"object\",\"properties\":{\"options\":{\"type\":\"object\",\"properties\":{\"compact_output\":{\"type\":\"boolean\"}}}},\"additionalProperties\":false}",
        .execute = undefined,
    };
    try std.testing.expect(!try supportsCompactToolOutput(std.testing.allocator, nested));
    const root = AgentTool{
        .label = "Root",
        .name = "root",
        .description = "Root compact_output property.",
        .parameters_schema_json = "{\"type\":\"object\",\"properties\":{\"compact_output\":{\"type\":\"boolean\"}},\"additionalProperties\":false}",
        .execute = undefined,
    };
    try std.testing.expect(try supportsCompactToolOutput(std.testing.allocator, root));
}

test "compact output injection preserves explicit caller choice" {
    const explicit_false = try withCompactToolOutput(std.testing.allocator, "{\"command\":\"ls -al\",\"compact_output\":false}");
    defer std.testing.allocator.free(explicit_false);
    try std.testing.expectEqualStrings("{\"command\":\"ls -al\",\"compact_output\":false}", explicit_false);

    const missing = try withCompactToolOutput(std.testing.allocator, "{\"command\":\"ls -al\"}");
    defer std.testing.allocator.free(missing);
    try std.testing.expect(std.mem.indexOf(u8, missing, "\"compact_output\":true") != null);
}

fn withCompactToolOutput(allocator: std.mem.Allocator, args_json: []const u8) ![]u8 {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, args_json, .{});
    defer parsed.deinit();
    if (parsed.value != .object) return try allocator.dupe(u8, args_json);
    if (parsed.value.object.contains("compact_output")) return try allocator.dupe(u8, args_json);
    try parsed.value.object.put(parsed.arena.allocator(), "compact_output", .{ .bool = true });
    return json_encode.valueAlloc(allocator, parsed.value);
}

test "compact output injection grows parsed object with parser arena" {
    const args = "{\"workspace_root\":\"/workspace\",\"command\":\"ls -al\",\"timeout_ms\":10000,\"a\":1,\"b\":2,\"c\":3,\"d\":4,\"e\":5,\"f\":6,\"g\":7,\"h\":8}";
    const injected = try withCompactToolOutput(std.testing.allocator, args);
    defer std.testing.allocator.free(injected);
    try std.testing.expect(std.mem.indexOf(u8, injected, "\"compact_output\":true") != null);
}

test "compact output injection keeps model arguments nested past 256 levels whole" {
    const nested = ("[" ** 400) ++ "1" ++ ("]" ** 400);
    const injected = try withCompactToolOutput(std.testing.allocator, "{\"command\":\"ls\",\"nested\":" ++ nested ++ "}");
    defer std.testing.allocator.free(injected);
    try std.testing.expectEqualStrings("{\"command\":\"ls\",\"nested\":" ++ nested ++ ",\"compact_output\":true}", injected);
}

fn pushProviderMessageUpdate(
    allocator: std.mem.Allocator,
    event_stream: *AgentEventStream,
    provider_event: ai_types.AssistantMessageEvent,
    partial: ai_types.AssistantMessage,
    owns_event: bool,
) !void {
    var cleanup_event = provider_event;
    errdefer if (owns_event) {
        ai_types.deinitAssistantMessageEvent(allocator, &cleanup_event);
    };
    try pushAgentEvent(event_stream, .{ .message_update = .{
        .message = partial,
        .event = provider_event,
        .owns_event = owns_event,
    } });
}

fn executeToolCalls(
    allocator: std.mem.Allocator,
    assistant_message: ai_types.AssistantMessage,
    config: AgentLoopConfig,
    event_stream: *AgentEventStream,
) !ToolExecutionResult {
    var tool_calls: std.ArrayList(ai_types.ToolCall) = .empty;
    defer tool_calls.deinit(allocator);

    for (assistant_message.content) |block| {
        if (block == .tool_call) {
            try tool_calls.append(allocator, block.tool_call);
        }
    }

    var results: std.ArrayList(ai_types.ToolResultMessage) = .empty;
    errdefer {
        for (results.items) |*result| result.deinit(allocator);
        results.deinit(allocator);
    }
    var compact_args: std.ArrayList([]u8) = .empty;
    errdefer {
        for (compact_args.items) |args| allocator.free(args);
        compact_args.deinit(allocator);
    }
    var retained_args: std.ArrayList([]u8) = .empty;
    errdefer {
        for (retained_args.items) |args| allocator.free(args);
        retained_args.deinit(allocator);
    }
    var has_steering = false;
    var steering_messages: ?[]const ai_types.Message = null;
    errdefer if (steering_messages) |msgs| {
        const mut_msgs: []ai_types.Message = @constCast(msgs);
        for (mut_msgs) |*msg| msg.deinit(allocator);
        allocator.free(mut_msgs);
    };

    for (tool_calls.items, 0..) |tool_call, index| {
        const tool = findTool(config.tools, tool_call.name);

        try pushAgentEvent(event_stream, .{ .tool_execution_start = .{
            .tool_call_id = tool_call.id,
            .tool_name = tool_call.name,
            .args_json = tool_call.arguments_json,
        } });

        var result: AgentToolResult = undefined;
        var is_error = false;
        var execution_args = tool_call.arguments_json;

        if (assistant_message.stop_reason == .length) {
            result = try truncatedToolCallResult(allocator, tool_call.name);
            is_error = true;
            try finalizeToolExecution(allocator, config, event_stream, &results, tool_call, execution_args, &result, is_error);
            continue;
        }

        if (tool) |t| {
            const validated_args = validateToolArguments(allocator, t, tool_call.arguments_json) catch |err| {
                result = try createErrorResult(allocator, err);
                is_error = true;
                try finalizeToolExecution(allocator, config, event_stream, &results, tool_call, execution_args, &result, is_error);
                continue;
            };
            const effective_args = if (config.rewrite_tool_args_fn) |rewrite| blk: {
                const rewritten = try rewrite(config.rewrite_tool_args_ctx, tool_call.name, validated_args, allocator);
                if (rewritten) |owned| {
                    errdefer allocator.free(owned);
                    try retained_args.append(allocator, owned);
                    break :blk owned;
                }
                break :blk validated_args;
            } else validated_args;
            execution_args = effective_args;
            const should_compact = if (config.compact_tool_output) supportsCompactToolOutput(allocator, t) catch |err| {
                result = try createErrorResult(allocator, err);
                is_error = true;
                try finalizeToolExecution(allocator, config, event_stream, &results, tool_call, execution_args, &result, is_error);
                continue;
            } else false;
            if (should_compact) {
                const owned_args = withCompactToolOutput(allocator, effective_args) catch |err| {
                    result = try createErrorResult(allocator, err);
                    is_error = true;
                    try finalizeToolExecution(allocator, config, event_stream, &results, tool_call, execution_args, &result, is_error);
                    continue;
                };
                errdefer allocator.free(owned_args);
                try compact_args.append(allocator, owned_args);
                execution_args = owned_args;
            }

            const approval_request = types.ToolApprovalRequest{
                .tool_call_id = tool_call.id,
                .tool_name = tool_call.name,
                .args_json = execution_args,
            };
            if (config.permission_engine) |engine| {
                const policy_decision = engine.evaluateTool(t.operation, tool_call.name, effective_args);
                if (policy_decision == .deny) {
                    result = try rejectedToolResult(allocator);
                    is_error = true;
                    try finalizeToolExecution(allocator, config, event_stream, &results, tool_call, execution_args, &result, is_error);
                    continue;
                }
                if (policy_decision != .allow) {
                    const legacy_decision = runLegacyApproval(t, approval_request, allocator);
                    switch (legacy_decision) {
                        .approve_always => persistLegacyDecision(allocator, engine, t, tool_call.name, effective_args, .allow),
                        .reject_always => persistLegacyDecision(allocator, engine, t, tool_call.name, effective_args, .deny),
                        .approve, .reject => {},
                    }
                    if (legacy_decision == .reject or legacy_decision == .reject_always) {
                        result = try rejectedToolResult(allocator);
                        is_error = true;
                        try finalizeToolExecution(allocator, config, event_stream, &results, tool_call, execution_args, &result, is_error);
                        continue;
                    }

                    if (policy_decision == .prompt and engine.approval_callback != null and legacy_decision != .approve_always) {
                        const decision = try engine.approveTool(t.operation, tool_call.name, effective_args);
                        if (decision == .reject or decision == .reject_always) {
                            result = try rejectedToolResult(allocator);
                            is_error = true;
                            try finalizeToolExecution(allocator, config, event_stream, &results, tool_call, execution_args, &result, is_error);
                            continue;
                        }
                    }
                }
            } else {
                const decision = runLegacyApproval(t, approval_request, allocator);
                if (decision == .reject or decision == .reject_always) {
                    result = try rejectedToolResult(allocator);
                    is_error = true;
                    try finalizeToolExecution(allocator, config, event_stream, &results, tool_call, execution_args, &result, is_error);
                    continue;
                }
            }

            var update_ctx = ToolUpdateContext{
                .event_stream = event_stream,
                .tool_call_id = tool_call.id,
                .tool_name = tool_call.name,
                .args_json = execution_args,
            };

            result = config.execute_tool_via_protocol_fn(
                config.execute_tool_via_protocol_ctx,
                tool_call.id,
                tool_call.name,
                execution_args,
                config.cancel_token,
                &update_ctx,
                onToolUpdate,
                allocator,
            ) catch |err| blk: {
                result = try createErrorResult(allocator, err);
                is_error = true;
                break :blk result;
            };
            is_error = result.is_error;
        } else {
            result = try createErrorResult(allocator, error.ToolNotFound);
            is_error = true;
        }

        try finalizeToolExecution(allocator, config, event_stream, &results, tool_call, execution_args, &result, is_error);

        if (config.get_steering_messages_fn) |get_steering| {
            if (try get_steering(config.get_steering_messages_ctx, allocator)) |msgs| {
                if (msgs.len > 0) {
                    steering_messages = msgs;
                    has_steering = true;

                    const remaining = tool_calls.items[index + 1 ..];
                    for (remaining) |skipped_call| {
                        const skipped_result = try skipToolCall(allocator, skipped_call, event_stream);
                        try results.append(allocator, skipped_result);
                    }
                    break;
                } else {
                    allocator.free(msgs);
                }
            }
        }
    }

    const tool_results = try results.toOwnedSlice(allocator);
    errdefer {
        for (tool_results) |*result| result.deinit(allocator);
        allocator.free(tool_results);
    }
    const owned_compact_args = try compact_args.toOwnedSlice(allocator);
    const owned_retained_args = try retained_args.toOwnedSlice(allocator);
    return .{
        .tool_results = tool_results,
        .compact_args = owned_compact_args,
        .retained_args = owned_retained_args,
        .has_steering = has_steering,
        .steering_messages = steering_messages,
    };
}

fn streamAssistantResponse(
    allocator: std.mem.Allocator,
    context: *AgentContext,
    config: AgentLoopConfig,
    event_stream: *AgentEventStream,
) !ai_types.AssistantMessage {
    var messages = context.messagesSlice();

    var transformed_messages: ?[]const ai_types.Message = null;
    defer if (transformed_messages) |tm| allocator.free(tm);

    if (config.transform_context_fn) |transform| {
        transformed_messages = try transform(config.transform_context_ctx, messages, allocator);
        messages = transformed_messages.?;
    }

    var llm_messages: ?[]const ai_types.Message = null;
    defer if (llm_messages) |_| allocator.free(llm_messages.?);

    if (config.convert_to_llm_fn) |convert| {
        llm_messages = try convert(config.convert_to_llm_ctx, messages, allocator);
        messages = llm_messages.?;
    }

    var tools: ?[]ai_types.Tool = null;
    defer if (tools) |t| allocator.free(t);
    tools = try buildToolsArray(allocator, context.tools);

    const llm_context = ai_types.Context{
        .system_prompt = ai_types.OwnedSlice(u8).initBorrowed(context.getSystemPrompt() orelse ""),
        .messages = messages,
        .tools = tools,
        .is_owned = false,
    };
    try emitContextUsage(event_stream, llm_context);

    const options = ProtocolOptions{
        .api_key = config.api_key,
        .session_id = config.session_id,
        .cancel_token = config.cancel_token,
        .thinking_level = config.thinking_level,
        .thinking_budgets = config.thinking_budgets,
        .max_retry_delay_ms = config.max_retry_delay_ms orelse 60_000,
        .temperature = config.temperature,
        .max_tokens = outputLimit(config.model, config.max_tokens, llm_context),
    };

    const provider_stream = try config.protocol.stream(
        config.model,
        llm_context,
        options,
        allocator,
    );
    defer _ = provider_stream.deinitAndDestroy();

    var final_message: ?ai_types.AssistantMessage = null;
    var message_started = false;

    while (provider_stream.wait()) |provider_event| {
        switch (provider_event) {
            .start => |s| {
                var owned_start_event = provider_event;
                errdefer if (provider_stream.ownership.isOwned()) {
                    ai_types.deinitAssistantMessageEvent(allocator, &owned_start_event);
                };

                const msg: ai_types.Message = .{ .assistant = .{
                    .content = &.{},
                    .api = config.model.api,
                    .provider = config.model.provider,
                    .model = config.model.id,
                    .usage = s.partial.usage,
                    .stop_reason = s.partial.stop_reason,
                    .timestamp = s.partial.timestamp,
                    .is_owned = false,
                } };
                try pushAgentEvent(event_stream, .{ .message_start = .{
                    .message = msg,
                } });
                if (provider_stream.ownership.isOwned()) {
                    ai_types.deinitAssistantMessageEvent(allocator, &owned_start_event);
                }
                message_started = true;
            },
            .text_start => |evt| {
                try pushProviderMessageUpdate(allocator, event_stream, provider_event, evt.partial, provider_stream.ownership.isOwned());
            },
            .text_delta => |evt| {
                try pushProviderMessageUpdate(allocator, event_stream, provider_event, evt.partial, provider_stream.ownership.isOwned());
            },
            .text_end => |evt| {
                try pushProviderMessageUpdate(allocator, event_stream, provider_event, evt.partial, provider_stream.ownership.isOwned());
            },
            .thinking_start => |evt| {
                try pushProviderMessageUpdate(allocator, event_stream, provider_event, evt.partial, provider_stream.ownership.isOwned());
            },
            .thinking_delta => |evt| {
                try pushProviderMessageUpdate(allocator, event_stream, provider_event, evt.partial, provider_stream.ownership.isOwned());
            },
            .thinking_end => |evt| {
                try pushProviderMessageUpdate(allocator, event_stream, provider_event, evt.partial, provider_stream.ownership.isOwned());
            },
            .toolcall_start => |evt| {
                try pushProviderMessageUpdate(allocator, event_stream, provider_event, evt.partial, provider_stream.ownership.isOwned());
            },
            .toolcall_delta => |evt| {
                try pushProviderMessageUpdate(allocator, event_stream, provider_event, evt.partial, provider_stream.ownership.isOwned());
            },
            .toolcall_end => |evt| {
                try pushProviderMessageUpdate(allocator, event_stream, provider_event, evt.partial, provider_stream.ownership.isOwned());
            },
            .done => |d| {
                var cleanup_event = provider_event;
                var final_transferred = false;
                errdefer if (provider_stream.ownership.isOwned() and !final_transferred) {
                    ai_types.deinitAssistantMessageEvent(allocator, &cleanup_event);
                };
                final_message = d.message;
                const msg: ai_types.Message = .{ .assistant = d.message };
                try pushAgentEvent(event_stream, .{ .message_end = .{
                    .message = msg,
                } });
                final_transferred = true;
            },
            .@"error" => |e| {
                var cleanup_event = provider_event;
                var final_transferred = false;
                errdefer if (provider_stream.ownership.isOwned() and !final_transferred) {
                    ai_types.deinitAssistantMessageEvent(allocator, &cleanup_event);
                };
                final_message = e.err;
                const msg: ai_types.Message = .{ .assistant = e.err };
                try pushAgentEvent(event_stream, .{ .message_end = .{
                    .message = msg,
                } });
                final_transferred = true;
            },
            .keepalive => {},
        }
    }

    if (final_message == null) {
        if (provider_stream.getResult()) |result| {
            var cloned = try ai_types.cloneAssistantMessage(allocator, result);
            errdefer cloned.deinit(allocator);
            final_message = cloned;
            const msg: ai_types.Message = .{ .assistant = final_message.? };
            try pushAgentEvent(event_stream, .{ .message_end = .{
                .message = msg,
            } });
        }
    }

    if (final_message == null) {
        if (provider_stream.getError()) |provider_error| {
            return try makeProviderErrorAssistantMessage(allocator, config.model, provider_error);
        }
    }

    return final_message orelse error.NoFinalMessage;
}

fn makeProviderErrorAssistantMessage(
    allocator: std.mem.Allocator,
    model: ai_types.Model,
    provider_error: []const u8,
) !ai_types.AssistantMessage {
    const content = try allocator.alloc(ai_types.AssistantContent, 1);
    errdefer allocator.free(content);
    content[0] = .{ .text = .{ .text = "" } };

    const error_message = try allocator.dupe(u8, provider_error);
    errdefer allocator.free(error_message);

    return .{
        .content = content,
        .api = model.api,
        .provider = model.provider,
        .model = model.id,
        .usage = .{},
        .stop_reason = .@"error",
        .error_message = ai_types.OwnedSlice(u8).initOwned(error_message),
        .timestamp = compat.time.nowMillis(),
        .is_owned = false,
    };
}

const LoopState = struct {
    messages: std.ArrayList(ai_types.Message),
    iterations: u32,
    final_message: ?ai_types.AssistantMessage,

    fn deinit(self: *LoopState, allocator: std.mem.Allocator) void {
        for (self.messages.items) |*msg| {
            msg.deinit(allocator);
        }
        self.messages.deinit(allocator);
        if (self.final_message) |*fm| {
            fm.deinit(allocator);
        }
    }
};

fn appendClonedStateMessage(
    messages: *std.ArrayList(ai_types.Message),
    allocator: std.mem.Allocator,
    msg: ai_types.Message,
) !void {
    var cloned = try ai_types.cloneMessage(allocator, msg);
    errdefer cloned.deinit(allocator);
    try messages.append(allocator, cloned);
}

fn setFinalMessage(state: *LoopState, allocator: std.mem.Allocator, msg: ai_types.AssistantMessage) !void {
    var cloned = try ai_types.cloneAssistantMessage(allocator, msg);
    errdefer cloned.deinit(allocator);

    if (state.final_message) |*prev| {
        prev.deinit(allocator);
    }

    state.final_message = cloned;
}

fn runCancelled(config: AgentLoopConfig) bool {
    const token = config.cancel_token orelse return false;
    return token.isCancelled();
}

fn withinTurnLimit(iterations: u32, max_iterations: ?u32) bool {
    const limit = max_iterations orelse return true;
    return iterations < limit;
}

const TurnOutcome = enum { failed, answered, called_tools, reasoned_only, cut_off };

pub const answer_request_text = "Your last reply held only reasoning and no answer. Write your answer now.";
pub const continue_request_text = "Your last reply was cut off at the output limit. Continue from exactly where it stopped.";

fn reasonedWithoutAnswer(content: []const ai_types.AssistantContent) bool {
    var reasoned = false;
    for (content) |block| switch (block) {
        .text => |t| if (std.mem.trim(u8, t.text, " \t\r\n").len > 0) return false,
        .thinking => |t| {
            if (std.mem.trim(u8, t.thinking, " \t\r\n").len > 0) reasoned = true;
        },
        .tool_call => return false,
        .image => {},
    };
    return reasoned;
}

fn requestMessage(allocator: std.mem.Allocator, request_text: []const u8) !ai_types.Message {
    const text = try allocator.dupe(u8, request_text);
    return .{ .user = .{ .content = .{ .text = text }, .timestamp = compat.time.nowMillis() } };
}

pub const compacted_request_text = "The conversation was compacted in the middle of this task. Carry on with the task from where it stopped, using the summary above.";

fn compactedRequest(allocator: std.mem.Allocator) !ai_types.Message {
    const text = try allocator.dupe(u8, compacted_request_text);
    return .{ .user = .{ .content = .{ .text = text }, .timestamp = compat.time.nowMillis() } };
}

const max_cut_off_tool_turns: u32 = 3;

fn turnOutcome(message: ai_types.AssistantMessage, cut_off_tool_turns: u32) TurnOutcome {
    switch (message.stop_reason) {
        .@"error", .aborted => return .failed,
        .content_filter => return .answered,
        .length => if (cut_off_tool_turns >= max_cut_off_tool_turns) return .answered,
        .stop, .tool_use => {},
    }
    for (message.content) |block| {
        if (block == .tool_call) return .called_tools;
    }
    if (message.stop_reason == .stop and reasonedWithoutAnswer(message.content)) return .reasoned_only;
    return .answered;
}

test "withinTurnLimit stops at a set limit and never without one" {
    try std.testing.expect(withinTurnLimit(std.math.maxInt(u32), null));
    try std.testing.expect(withinTurnLimit(1, 2));
    try std.testing.expect(!withinTurnLimit(2, 2));
    try std.testing.expect(!withinTurnLimit(0, 0));
}

fn outcomeOf(stop_reason: ai_types.StopReason, content: []const ai_types.AssistantContent, cut_off_tool_turns: u32) TurnOutcome {
    return turnOutcome(.{
        .content = content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = stop_reason,
        .timestamp = 0,
    }, cut_off_tool_turns);
}

test "turnOutcome runs a reply's tool calls whatever stop reason it reports" {
    const calls = [_]ai_types.AssistantContent{
        .{ .text = .{ .text = "reading" } },
        .{ .tool_call = .{ .id = "call_1", .name = "read", .arguments_json = "{}" } },
    };
    try std.testing.expectEqual(TurnOutcome.called_tools, outcomeOf(.tool_use, &calls, 0));
    try std.testing.expectEqual(TurnOutcome.called_tools, outcomeOf(.stop, &calls, 0));
    try std.testing.expectEqual(TurnOutcome.called_tools, outcomeOf(.length, &calls, 0));
}

test "turnOutcome ends the run on a reply without tool calls, even one reporting tool_use" {
    const text = [_]ai_types.AssistantContent{.{ .text = .{ .text = "done" } }};
    try std.testing.expectEqual(TurnOutcome.answered, outcomeOf(.tool_use, &text, 0));
    try std.testing.expectEqual(TurnOutcome.answered, outcomeOf(.stop, &text, 0));
    try std.testing.expectEqual(TurnOutcome.answered, outcomeOf(.length, &.{}, 0));
}

const ReplyCtx = struct {
    text: []const u8,
    stop_reason: ai_types.StopReason,
    reasoning_only: bool = false,
};

fn replyStream(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream_module.AssistantMessageEventStream {
    _ = context;
    _ = options;
    const reply = @as(*ReplyCtx, @ptrCast(@alignCast(ctx.?)));
    const stream_ptr = try allocator.create(event_stream_module.AssistantMessageEventStream);
    errdefer allocator.destroy(stream_ptr);
    stream_ptr.* = event_stream_module.AssistantMessageEventStream.init(allocator);
    const blocks = try allocator.alloc(ai_types.AssistantContent, 1);
    errdefer allocator.free(blocks);
    const text = try allocator.dupe(u8, reply.text);
    errdefer allocator.free(text);
    blocks[0] = if (reply.reasoning_only) .{ .thinking = .{ .thinking = text } } else .{ .text = .{ .text = text } };
    const api = try allocator.dupe(u8, model.api);
    errdefer allocator.free(api);
    const provider = try allocator.dupe(u8, model.provider);
    errdefer allocator.free(provider);
    const owned_model = try allocator.dupe(u8, model.id);
    errdefer allocator.free(owned_model);
    stream_ptr.complete(.{
        .content = blocks,
        .api = api,
        .provider = provider,
        .model = owned_model,
        .usage = .{},
        .stop_reason = reply.stop_reason,
        .timestamp = 0,
        .is_owned = true,
    });
    return stream_ptr;
}

fn steeringCallbackFails(ctx: ?*anyopaque, allocator: std.mem.Allocator) anyerror!?[]ai_types.Message {
    const seen: *usize = @ptrCast(@alignCast(ctx.?));
    _ = allocator;
    seen.* += 1;
    if (seen.* > 1) return error.SteeringFailed;
    return null;
}

test "a message the context already owns is not freed again when a later callback fails" {
    const model = testModel();
    var events_storage: AgentEventStream = undefined;
    const events = &events_storage;
    events.* = AgentEventStream.init(std.testing.allocator);
    defer events.deinit();

    var context = AgentContext.init(std.testing.allocator);
    defer context.deinit();

    var steering_calls: usize = 0;
    const answered = ReplyCtx{ .text = "reply", .stop_reason = .stop };
    try std.testing.expectError(error.SteeringFailed, runLoop(std.testing.allocator, &.{}, &context, .{
        .model = model,
        .protocol = .{ .stream_fn = replyStream, .ctx = @constCast(&answered) },
        .max_iterations = 2,
        .get_steering_messages_fn = steeringCallbackFails,
        .get_steering_messages_ctx = &steering_calls,
    }, events, &events.retention));
    try std.testing.expect(steering_calls > 1);

    while (events.poll()) |event| {
        var mutable = event;
        defer mutable.deinit(std.testing.allocator);
        switch (mutable) {
            .message_end => |payload| {
                try std.testing.expectEqualStrings("reply", payload.message.assistant.content[0].text.text);
            },
            .turn_end => |payload| {
                try std.testing.expectEqualStrings("reply", payload.message.content[0].text.text);
            },
            else => {},
        }
    }
}

fn erroredStream(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream_module.AssistantMessageEventStream {
    _ = ctx;
    _ = model;
    _ = context;
    _ = options;
    const stream_ptr = try allocator.create(event_stream_module.AssistantMessageEventStream);
    errdefer allocator.destroy(stream_ptr);
    stream_ptr.* = event_stream_module.AssistantMessageEventStream.init(allocator);
    stream_ptr.completeWithError("provider said no");
    return stream_ptr;
}

test "an errored turn's events stay readable after the message is released" {
    const model = testModel();
    var events_storage: AgentEventStream = undefined;
    const events = &events_storage;
    events.* = AgentEventStream.init(std.testing.allocator);
    defer events.deinit();

    var context = AgentContext.init(std.testing.allocator);
    defer context.deinit();

    try runLoop(std.testing.allocator, &.{}, &context, .{
        .model = model,
        .protocol = .{ .stream_fn = erroredStream },
        .max_iterations = 1,
    }, events, &events.retention);

    var saw_turn_end = false;
    while (events.poll()) |event| {
        var mutable = event;
        defer mutable.deinit(std.testing.allocator);
        switch (mutable) {
            .turn_end => |payload| {
                saw_turn_end = true;
                try std.testing.expectEqualStrings("provider said no", payload.message.error_message.slice());
            },
            else => {},
        }
    }
    try std.testing.expect(saw_turn_end);
}

test "an aborted turn's events stay readable after the message is released" {
    const model = testModel();
    var events_storage: AgentEventStream = undefined;
    const events = &events_storage;
    events.* = AgentEventStream.init(std.testing.allocator);
    defer events.deinit();

    var context = AgentContext.init(std.testing.allocator);
    defer context.deinit();
    const aborted = ReplyCtx{ .text = "partial", .stop_reason = .aborted };

    try runLoop(std.testing.allocator, &.{}, &context, .{
        .model = model,
        .protocol = .{ .stream_fn = replyStream, .ctx = @constCast(&aborted) },
        .max_iterations = 1,
    }, events, &events.retention);

    var saw_message_end = false;
    while (events.poll()) |event| {
        var mutable = event;
        defer mutable.deinit(std.testing.allocator);
        switch (mutable) {
            .message_end => |payload| {
                saw_message_end = true;
                try std.testing.expectEqualStrings("partial", payload.message.assistant.content[0].text.text);
            },
            else => {},
        }
    }
    try std.testing.expect(saw_message_end);
}

test "an aborted turn frees the message the stream handed over" {
    const model = testModel();
    var events_storage: AgentEventStream = undefined;
    const events = &events_storage;
    events.* = AgentEventStream.init(std.testing.allocator);
    defer events.deinit();

    var context = AgentContext.init(std.testing.allocator);
    defer context.deinit();
    const aborted = ReplyCtx{ .text = "partial", .stop_reason = .aborted };
    const config = AgentLoopConfig{
        .model = model,
        .protocol = .{ .stream_fn = replyStream, .ctx = @constCast(&aborted) },
        .max_iterations = 1,
    };
    try runLoop(std.testing.allocator, &.{}, &context, config, events, &events.retention);
    var saw_turn_end = false;
    while (events.poll()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .turn_end) saw_turn_end = true;
    }
    try std.testing.expect(saw_turn_end);
}

fn failOnAgentEndClone(allocator: std.mem.Allocator, event: AgentEvent) error{OutOfMemory}!AgentEvent {
    _ = allocator;
    return switch (event) {
        .agent_end => error.OutOfMemory,
        else => event,
    };
}

fn failOnMessageStartClone(allocator: std.mem.Allocator, event: AgentEvent) error{OutOfMemory}!AgentEvent {
    _ = allocator;
    return switch (event) {
        .message_start => error.OutOfMemory,
        else => event,
    };
}

test "a rejected final publication leaves the events already queued readable" {
    const allocator = std.testing.allocator;
    var events_storage = AgentEventStream.init(allocator);
    defer events_storage.deinit();
    const events = &events_storage;
    events.ownership = .{ .owned = failOnAgentEndClone };
    var context = AgentContext.init(allocator);
    defer context.deinit();
    const aborted = ReplyCtx{ .text = "partial", .stop_reason = .aborted };

    try std.testing.expectError(error.StreamCompleted, runLoop(allocator, &.{}, &context, .{
        .model = testModel(),
        .protocol = .{ .stream_fn = replyStream, .ctx = @constCast(&aborted) },
        .max_iterations = 1,
    }, events, &events.retention));

    try std.testing.expect(events.retention.result != null);
    try std.testing.expect(events.getResult() == null);

    var saw_turn_end = false;
    while (events.poll()) |event| {
        var drained = event;
        defer drained.deinit(allocator);
        switch (drained) {
            .turn_end => |payload| {
                saw_turn_end = true;
                try std.testing.expectEqualStrings("partial", payload.message.content[0].text.text);
                try std.testing.expectEqual(ai_types.StopReason.aborted, payload.message.stop_reason);
            },
            else => {},
        }
    }
    try std.testing.expect(saw_turn_end);
}

test "a failed run publishes the error and leaves the queued events readable" {
    const allocator = std.testing.allocator;
    var events_storage = AgentEventStream.init(allocator);
    defer events_storage.deinit();
    const events = &events_storage;
    events.ownership = .{ .owned = failOnMessageStartClone };
    var context = AgentContext.init(allocator);
    defer context.deinit();
    const answered = ReplyCtx{ .text = "the answer, written as reasoning", .stop_reason = .stop, .reasoning_only = true };
    var steering_calls: usize = 0;
    const runner = try allocator.create(RunLoopThreadCtx);
    runner.* = .{
        .allocator = allocator,
        .prompts = &.{},
        .context = &context,
        .config = .{
            .model = testModel(),
            .protocol = .{ .stream_fn = replyStream, .ctx = @constCast(&answered) },
            .max_iterations = 2,
            .get_steering_messages_fn = steeringCallbackFails,
            .get_steering_messages_ctx = &steering_calls,
        },
        .stream = events,
    };
    runLoopThread(runner);

    try std.testing.expectEqualStrings("StreamCompleted", events.getError().?);
    try std.testing.expect(events.getResult() == null);

    var saw_turn_end = false;
    while (events.poll()) |event| {
        var drained = event;
        defer drained.deinit(allocator);
        switch (drained) {
            .turn_end => |payload| {
                saw_turn_end = true;
                try std.testing.expectEqualStrings("the answer, written as reasoning", payload.message.content[0].thinking.thinking);
            },
            else => {},
        }
    }
    try std.testing.expect(saw_turn_end);
}

fn handoffProbe(allocator: std.mem.Allocator) !void {
    var events = AgentEventStream.init(allocator);
    defer events.deinit();
    var context = AgentContext.init(allocator);
    defer context.deinit();
    const aborted = ReplyCtx{ .text = "partial", .stop_reason = .aborted };
    try runLoop(allocator, &.{}, &context, .{
        .model = testModel(),
        .protocol = .{ .stream_fn = replyStream, .ctx = @constCast(&aborted) },
        .max_iterations = 1,
    }, &events, &events.retention);
    while (events.poll()) |event| {
        var drained = event;
        drained.deinit(allocator);
    }
}

test "the parked hand-off survives an exhausted allocator" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, handoffProbe, .{});
}

test "turnOutcome never runs the tool calls of a failed, aborted or filtered reply" {
    const calls = [_]ai_types.AssistantContent{
        .{ .tool_call = .{ .id = "call_1", .name = "read", .arguments_json = "{}" } },
    };
    try std.testing.expectEqual(TurnOutcome.failed, outcomeOf(.@"error", &calls, 0));
    try std.testing.expectEqual(TurnOutcome.failed, outcomeOf(.aborted, &calls, 0));
    try std.testing.expectEqual(TurnOutcome.answered, outcomeOf(.content_filter, &calls, 0));
}

test "turnOutcome marks a finished reply that holds only reasoning" {
    const reasoning = [_]ai_types.AssistantContent{.{ .thinking = .{ .thinking = "the answer, written as reasoning" } }};
    try std.testing.expectEqual(TurnOutcome.reasoned_only, outcomeOf(.stop, &reasoning, 0));
    try std.testing.expectEqual(TurnOutcome.answered, outcomeOf(.length, &reasoning, 0));

    const answered = [_]ai_types.AssistantContent{
        .{ .thinking = .{ .thinking = "plan" } },
        .{ .text = .{ .text = "the answer" } },
    };
    try std.testing.expectEqual(TurnOutcome.answered, outcomeOf(.stop, &answered, 0));

    const blank = [_]ai_types.AssistantContent{
        .{ .thinking = .{ .thinking = " \n" } },
        .{ .text = .{ .text = "\n" } },
    };
    try std.testing.expectEqual(TurnOutcome.answered, outcomeOf(.stop, &blank, 0));
}

test "requestMessage survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, requestMessageProbe, .{});
}

fn requestMessageProbe(allocator: std.mem.Allocator) !void {
    var message = try requestMessage(allocator, answer_request_text);
    message.deinit(allocator);
}

test "turnOutcome ends the run on a cut-off tool call once three in a row were answered" {
    const calls = [_]ai_types.AssistantContent{
        .{ .tool_call = .{ .id = "call_1", .name = "write", .arguments_json = "{\"text\":\"cut" } },
    };
    try std.testing.expectEqual(TurnOutcome.called_tools, outcomeOf(.length, &calls, 2));
    try std.testing.expectEqual(TurnOutcome.answered, outcomeOf(.length, &calls, 3));
    try std.testing.expectEqual(TurnOutcome.called_tools, outcomeOf(.tool_use, &calls, 3));
}

fn runLoop(
    allocator: std.mem.Allocator,
    prompts: ?[]const ai_types.Message,
    context: *AgentContext,
    config: AgentLoopConfig,
    event_stream: *AgentEventStream,
    retained: *types.StreamRetention,
) !void {
    var state = LoopState{
        .messages = std.ArrayList(ai_types.Message).empty,
        .iterations = 0,
        .final_message = null,
    };
    defer state.deinit(allocator);
    errdefer if (state.final_message) |final_message| {
        state.final_message = null;
        retained.park(final_message);
    };

    if (prompts) |initial_prompts| {
        for (initial_prompts) |prompt| {
            try context.appendMessage(prompt);

            try pushAgentEvent(event_stream, .{ .message_start = .{
                .message = prompt,
            } });
            try pushAgentEvent(event_stream, .{ .message_end = .{
                .message = prompt,
                .steering = config.prompts_are_steering,
            } });

            try appendClonedStateMessage(&state.messages, allocator, prompt);
        }
    }

    try pushAgentEvent(event_stream, .agent_start);

    var ended_before_cap = false;
    var cancelled_run = false;
    var cut_off_tool_turns: u32 = 0;
    var asked_for_answer = false;
    var turn_config = config;

    outer: while (withinTurnLimit(state.iterations, config.max_iterations)) {
        if (config.cancel_token) |token| {
            if (token.isCancelled()) {
                ended_before_cap = true;
                cancelled_run = true;
                break;
            }
        }

        while (withinTurnLimit(state.iterations, config.max_iterations)) {
            if (runCancelled(config)) {
                ended_before_cap = true;
                cancelled_run = true;
                break :outer;
            }
            if (state.iterations > 0) {
                if (config.next_model_fn) |next_model| {
                    if (next_model(config.next_model_ctx)) |model| {
                        turn_config.model = model;
                        if (turn_config.max_tokens) |requested| {
                            if (model.max_tokens > 0 and requested > model.max_tokens) turn_config.max_tokens = model.max_tokens;
                        }
                    }
                }
                if (config.compact_between_turns_fn) |compact| {
                    if (try compact(config.compact_between_turns_ctx, context, event_stream)) {
                        const request = try compactedRequest(context.allocator);
                        context.appendMessage(request) catch |err| {
                            var owned = request;
                            owned.deinit(context.allocator);
                            return err;
                        };
                        try pushAgentEvent(event_stream, .{ .message_start = .{
                            .message = request,
                        } });
                        try pushAgentEvent(event_stream, .{ .message_end = .{
                            .message = request,
                        } });
                        try appendClonedStateMessage(&state.messages, allocator, request);
                    }
                    if (runCancelled(config)) {
                        ended_before_cap = true;
                        cancelled_run = true;
                        break :outer;
                    }
                }
            }
            var steering_messages: ?[]const ai_types.Message = null;
            if (config.get_steering_messages_fn) |get_steering| {
                steering_messages = try get_steering(config.get_steering_messages_ctx, allocator);
            }

            if (steering_messages) |msgs| {
                if (msgs.len > 0) {
                    for (msgs) |steering_msg| {
                        try context.appendMessage(steering_msg);
                        try pushAgentEvent(event_stream, .{ .message_start = .{
                            .message = steering_msg,
                        } });
                        try pushAgentEvent(event_stream, .{ .message_end = .{
                            .message = steering_msg,
                            .steering = true,
                        } });
                        try appendClonedStateMessage(&state.messages, allocator, steering_msg);
                    }
                    allocator.free(msgs);
                } else {
                    allocator.free(msgs);
                }
            }

            try pushAgentEvent(event_stream, .turn_start);

            const assistant_message = streamAssistantResponse(
                allocator,
                context,
                turn_config,
                event_stream,
            ) catch |err| {
                const error_content = [_]ai_types.AssistantContent{.{
                    .text = .{ .text = "" },
                }};
                const error_msg = ai_types.AssistantMessage{
                    .content = &error_content,
                    .api = turn_config.model.api,
                    .provider = turn_config.model.provider,
                    .model = turn_config.model.id,
                    .usage = .{},
                    .stop_reason = .@"error",
                    .error_message = ai_types.OwnedSlice(u8).initBorrowed(@errorName(err)),
                    .timestamp = compat.time.nowMillis(),
                    .is_owned = false,
                };
                try setFinalMessage(&state, allocator, error_msg);
                try appendClonedStateMessage(&state.messages, allocator, .{ .assistant = error_msg });

                const final_error_msg = state.final_message orelse error_msg;
                try pushAgentEvent(event_stream, .{ .turn_end = .{
                    .message = final_error_msg,
                    .tool_results = types.OwnedSlice(ai_types.ToolResultMessage).initBorrowed(&.{}),
                } });

                ended_before_cap = true;
                cancelled_run = runCancelled(config);
                break :outer;
            };

            state.iterations += 1;
            const loop_owns_message = assistant_message.is_owned or assistant_message.error_message.is_owned;
            var message_transferred = false;
            errdefer if (loop_owns_message and !message_transferred) retained.park(assistant_message);
            try setFinalMessage(&state, allocator, assistant_message);
            try appendClonedStateMessage(&state.messages, allocator, .{ .assistant = assistant_message });

            const raised = if (config.raise_max_tokens_on_cut_off and assistant_message.stop_reason == .length) raisedOutput(turn_config.max_tokens, turn_config.model) else null;
            if (raised) |higher| turn_config.max_tokens = higher;
            const outcome = switch (turnOutcome(assistant_message, cut_off_tool_turns)) {
                .reasoned_only => if (asked_for_answer) TurnOutcome.answered else TurnOutcome.reasoned_only,
                .answered => if (raised != null) TurnOutcome.cut_off else TurnOutcome.answered,
                else => |value| value,
            };
            cut_off_tool_turns = if (outcome == .called_tools and assistant_message.stop_reason == .length) cut_off_tool_turns + 1 else 0;
            switch (outcome) {
                .failed => {
                    cancelled_run = runCancelled(config);
                    const final_error_msg = state.final_message orelse assistant_message;
                    try pushAgentEvent(event_stream, .{ .turn_end = .{
                        .message = final_error_msg,
                        .tool_results = types.OwnedSlice(ai_types.ToolResultMessage).initBorrowed(&.{}),
                    } });
                    if (loop_owns_message) {
                        retained.park(assistant_message);
                        message_transferred = true;
                    }
                    ended_before_cap = true;
                    break :outer;
                },
                .answered => {
                    try pushAgentEvent(event_stream, .{ .turn_end = .{
                        .message = assistant_message,
                        .tool_results = types.OwnedSlice(ai_types.ToolResultMessage).initBorrowed(&.{}),
                    } });

                    try context.appendMessage(.{ .assistant = assistant_message });
                    message_transferred = true;

                    if (config.get_steering_messages_fn) |get_steering| {
                        if (try get_steering(config.get_steering_messages_ctx, allocator)) |queued_steering| {
                            if (queued_steering.len > 0) {
                                for (queued_steering) |steering_msg| {
                                    try context.appendMessage(steering_msg);
                                    try pushAgentEvent(event_stream, .{ .message_start = .{
                                        .message = steering_msg,
                                    } });
                                    try pushAgentEvent(event_stream, .{ .message_end = .{
                                        .message = steering_msg,
                                        .steering = true,
                                    } });
                                    try appendClonedStateMessage(&state.messages, allocator, steering_msg);
                                }
                                allocator.free(queued_steering);
                                continue;
                            }
                            allocator.free(queued_steering);
                        }
                    }

                    if (config.get_follow_up_messages_fn) |get_follow_up| {
                        if (try get_follow_up(config.get_follow_up_messages_ctx, allocator)) |follow_ups| {
                            if (follow_ups.len > 0) {
                                for (follow_ups) |follow_up| {
                                    try context.appendMessage(follow_up);
                                    try pushAgentEvent(event_stream, .{ .message_start = .{
                                        .message = follow_up,
                                    } });
                                    try pushAgentEvent(event_stream, .{ .message_end = .{
                                        .message = follow_up,
                                    } });
                                    try appendClonedStateMessage(&state.messages, allocator, follow_up);
                                }
                                allocator.free(follow_ups);
                                continue :outer;
                            }
                            allocator.free(follow_ups);
                        }
                    }

                    ended_before_cap = true;
                    break :outer;
                },
                .reasoned_only, .cut_off => {
                    if (outcome == .reasoned_only) asked_for_answer = true;
                    try pushAgentEvent(event_stream, .{ .turn_end = .{
                        .message = assistant_message,
                        .tool_results = types.OwnedSlice(ai_types.ToolResultMessage).initBorrowed(&.{}),
                    } });
                    try context.appendMessage(.{ .assistant = assistant_message });
                    message_transferred = true;

                    const request = try requestMessage(context.allocator, if (outcome == .reasoned_only) answer_request_text else continue_request_text);
                    context.appendMessage(request) catch |err| {
                        var owned = request;
                        owned.deinit(context.allocator);
                        return err;
                    };
                    try pushAgentEvent(event_stream, .{ .message_start = .{
                        .message = request,
                    } });
                    try pushAgentEvent(event_stream, .{ .message_end = .{
                        .message = request,
                    } });
                    try appendClonedStateMessage(&state.messages, allocator, request);
                },
                .called_tools => {
                    var tool_result = try executeToolCalls(
                        allocator,
                        assistant_message,
                        config,
                        event_stream,
                    );
                    defer tool_result.deinitAfterToolResultsTransferred(allocator);

                    try pushAgentEvent(event_stream, .{ .turn_end = .{
                        .message = assistant_message,
                        .tool_results = types.OwnedSlice(ai_types.ToolResultMessage).initBorrowed(tool_result.tool_results),
                    } });

                    try context.appendMessage(.{ .assistant = assistant_message });
                    message_transferred = true;

                    for (tool_result.tool_results) |tool_result_msg| {
                        const msg: ai_types.Message = .{ .tool_result = tool_result_msg };
                        try pushAgentEvent(event_stream, .{ .message_start = .{
                            .message = msg,
                        } });
                        try context.appendMessage(msg);
                        try pushAgentEvent(event_stream, .{ .message_end = .{
                            .message = msg,
                        } });
                        try appendClonedStateMessage(&state.messages, allocator, msg);
                    }

                    if (tool_result.steering_messages) |steering_msgs| {
                        tool_result.steering_messages = null;
                        for (steering_msgs) |steering_msg| {
                            try context.appendMessage(steering_msg);
                            try pushAgentEvent(event_stream, .{ .message_start = .{
                                .message = steering_msg,
                            } });
                            try pushAgentEvent(event_stream, .{ .message_end = .{
                                .message = steering_msg,
                                .steering = true,
                            } });
                            try appendClonedStateMessage(&state.messages, allocator, steering_msg);
                        }
                        const mutable_msgs: []ai_types.Message = @constCast(steering_msgs);
                        allocator.free(mutable_msgs);
                    }
                },
            }
        }
    }

    const result_messages = try state.messages.toOwnedSlice(allocator);

    const termination: ?types.AgentTermination = if (cancelled_run)
        .cancelled
    else if (!ended_before_cap)
        .max_turns
    else
        null;

    const result_final_message: ai_types.AssistantMessage = if (state.final_message) |fm| blk: {
        state.final_message = null;
        break :blk fm;
    } else blk: {
        break :blk .{
            .content = try allocator.alloc(ai_types.AssistantContent, 0),
            .api = turn_config.model.api,
            .provider = turn_config.model.provider,
            .model = turn_config.model.id,
            .usage = .{},
            .stop_reason = .stop,
            .timestamp = compat.time.nowMillis(),
            .is_owned = false,
        };
    };

    var result = AgentLoopResult{
        .messages = owned_slice_mod.OwnedSlice(ai_types.Message).initOwned(result_messages),
        .final_message = result_final_message,
        .iterations = state.iterations,
        .termination = termination,
    };
    var result_owned = false;
    errdefer if (!result_owned) retained.retainResult(result);

    try pushAgentEvent(event_stream, .{
        .agent_end = .{
            .messages = types.OwnedSlice(ai_types.Message).initBorrowed(result.messages.slice()),
            .termination = termination,
            .final_message = result_final_message,
        },
    });

    result_owned = true;
    event_stream.complete(result);
}

const RunLoopThreadCtx = struct {
    allocator: std.mem.Allocator,
    prompts: ?[]const ai_types.Message,
    context: *AgentContext,
    config: AgentLoopConfig,
    stream: *AgentEventStream,
    owned_api_key: ?[]u8 = null,
    owned_session_id: ?[]u8 = null,
};

fn runLoopThread(ctx: *RunLoopThreadCtx) void {
    const allocator = ctx.allocator;
    const stream = ctx.stream;

    runLoop(allocator, ctx.prompts, ctx.context, ctx.config, stream, &stream.retention) catch |err| {
        stream.completeWithError(@errorName(err));
    };

    if (ctx.owned_api_key) |key| allocator.free(key);
    if (ctx.owned_session_id) |sid| allocator.free(sid);

    allocator.destroy(ctx);
    stream.markThreadDone();
}

fn cloneConfigStrings(
    allocator: std.mem.Allocator,
    config: AgentLoopConfig,
    out_owned_api_key: *?[]u8,
    out_owned_session_id: *?[]u8,
) !AgentLoopConfig {
    var cloned = config;
    if (config.api_key) |key| {
        out_owned_api_key.* = try allocator.dupe(u8, key);
        cloned.api_key = out_owned_api_key.*;
    }
    if (config.session_id) |sid| {
        out_owned_session_id.* = try allocator.dupe(u8, sid);
        cloned.session_id = out_owned_session_id.*;
    }
    return cloned;
}

pub fn agentLoop(
    allocator: std.mem.Allocator,
    prompts: []const ai_types.Message,
    context: *AgentContext,
    config: AgentLoopConfig,
) !*AgentEventStream {
    var owned_api_key: ?[]u8 = null;
    var owned_session_id: ?[]u8 = null;
    const thread_config = cloneConfigStrings(allocator, config, &owned_api_key, &owned_session_id) catch |err| {
        if (owned_api_key) |key| allocator.free(key);
        return err;
    };
    errdefer {
        if (owned_api_key) |key| allocator.free(key);
        if (owned_session_id) |sid| allocator.free(sid);
    }

    const stream = try allocator.create(AgentEventStream);
    errdefer allocator.destroy(stream);
    stream.* = AgentEventStream.init(allocator);
    stream.wait_for_thread_on_deinit = true;

    const ctx = try allocator.create(RunLoopThreadCtx);
    errdefer allocator.destroy(ctx);
    ctx.* = .{
        .allocator = allocator,
        .prompts = prompts,
        .context = context,
        .config = thread_config,
        .stream = stream,
        .owned_api_key = owned_api_key,
        .owned_session_id = owned_session_id,
    };

    const th = try std.Thread.spawn(.{}, runLoopThread, .{ctx});
    th.detach();

    return stream;
}

pub fn agentLoopContinue(
    allocator: std.mem.Allocator,
    context: *AgentContext,
    config: AgentLoopConfig,
) !*AgentEventStream {
    var owned_api_key: ?[]u8 = null;
    var owned_session_id: ?[]u8 = null;
    const thread_config = cloneConfigStrings(allocator, config, &owned_api_key, &owned_session_id) catch |err| {
        if (owned_api_key) |key| allocator.free(key);
        return err;
    };
    errdefer {
        if (owned_api_key) |key| allocator.free(key);
        if (owned_session_id) |sid| allocator.free(sid);
    }

    const stream = try allocator.create(AgentEventStream);
    errdefer allocator.destroy(stream);
    stream.* = AgentEventStream.init(allocator);
    stream.wait_for_thread_on_deinit = true;

    const ctx = try allocator.create(RunLoopThreadCtx);
    errdefer allocator.destroy(ctx);
    ctx.* = .{
        .allocator = allocator,
        .prompts = null,
        .context = context,
        .config = thread_config,
        .stream = stream,
        .owned_api_key = owned_api_key,
        .owned_session_id = owned_session_id,
    };

    const th = try std.Thread.spawn(.{}, runLoopThread, .{ctx});
    th.detach();

    return stream;
}

test "findTool finds tool by name" {
    const tools = [_]AgentTool{
        .{
            .label = "Tool A",
            .name = "tool_a",
            .description = "First tool",
            .parameters_schema_json = "{}",
            .execute = undefined,
        },
        .{
            .label = "Tool B",
            .name = "tool_b",
            .description = "Second tool",
            .parameters_schema_json = "{}",
            .execute = undefined,
        },
    };

    const found = findTool(&tools, "tool_b");
    try std.testing.expect(found != null);
    try std.testing.expectEqualStrings("tool_b", found.?.name);

    const not_found = findTool(&tools, "tool_c");
    try std.testing.expect(not_found == null);
}

test "buildToolsArray creates correct array" {
    const tools = [_]AgentTool{
        .{
            .label = "Test",
            .name = "test_tool",
            .description = "A test tool",
            .parameters_schema_json = "{\"type\": \"object\"}",
            .execute = undefined,
        },
    };

    const result = try buildToolsArray(std.testing.allocator, &tools);
    try std.testing.expect(result != null);
    defer std.testing.allocator.free(result.?);

    try std.testing.expectEqual(@as(usize, 1), result.?.len);
    try std.testing.expectEqualStrings("test_tool", result.?[0].name);
}

test "buildToolsArray returns null for empty tools" {
    const result = try buildToolsArray(std.testing.allocator, null);
    try std.testing.expect(result == null);
}

test "createErrorResult creates valid result" {
    const result = try createErrorResult(std.testing.allocator, error.TestError);
    defer {
        var mut_result = result;
        mut_result.deinit(std.testing.allocator);
    }

    try std.testing.expectEqual(@as(usize, 1), result.content.slice().len);
    try std.testing.expect(result.content.slice()[0] == .text);
}

fn mockContextUsageStream(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream_module.AssistantMessageEventStream {
    _ = ctx;
    _ = model;
    _ = context;
    _ = options;

    const stream = try allocator.create(event_stream_module.AssistantMessageEventStream);
    stream.* = event_stream_module.AssistantMessageEventStream.init(allocator);

    const content = try allocator.alloc(ai_types.AssistantContent, 1);
    content[0] = .{ .text = .{ .text = try allocator.dupe(u8, "ok") } };
    stream.complete(.{
        .content = content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 0,
    });

    return stream;
}

test "streamAssistantResponse emits context and prompt segment usage" {
    const allocator = std.testing.allocator;

    var context = AgentContext.init(allocator);
    defer context.deinit();
    context.system_prompt = types.OwnedSlice(u8).initOwned(try allocator.dupe(u8, "stable system prompt"));

    const user_text = try allocator.dupe(u8, "dynamic user prompt");
    try context.appendMessage(.{ .user = .{
        .content = .{ .text = user_text },
        .timestamp = 0,
    } });

    const tools = [_]AgentTool{.{
        .label = "Search",
        .name = "search",
        .description = "Search indexed artifacts",
        .parameters_schema_json = "{\"type\":\"object\"}",
        .execute = undefined,
    }};
    context.tools = &tools;

    const model = ai_types.Model{
        .id = "test-model",
        .name = "Test",
        .api = "test-api",
        .provider = "test-provider",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };

    var agent_events = AgentEventStream.init(allocator);
    defer agent_events.deinit();

    var final = try streamAssistantResponse(
        allocator,
        &context,
        .{
            .model = model,
            .protocol = .{ .stream_fn = mockContextUsageStream },
        },
        &agent_events,
    );
    defer final.deinit(allocator);

    var saw_context_usage = false;
    var segment_count: usize = 0;
    while (agent_events.poll()) |evt| {
        switch (evt) {
            .context_usage => |usage| {
                saw_context_usage = true;
                try std.testing.expectEqual(@as(u32, 1), usage.message_count);
                try std.testing.expectEqual(@as(u32, 1), usage.tool_count);
                try std.testing.expect(usage.system_prompt_bytes > 0);
                try std.testing.expect(usage.message_bytes > 0);
                try std.testing.expect(usage.tool_definition_bytes > 0);
                try std.testing.expect(usage.estimated_tokens > 0);
            },
            .prompt_segment_usage => |segment| {
                segment_count += 1;
                if (segment.segment == .system_prompt or segment.segment == .tool_definitions) {
                    try std.testing.expectEqual(types.PromptSegmentCacheRole.stable, segment.cache_role);
                }
                if (segment.segment == .message_history) {
                    try std.testing.expectEqual(types.PromptSegmentCacheRole.dynamic, segment.cache_role);
                }
            },
            else => {},
        }
    }

    try std.testing.expect(saw_context_usage);
    try std.testing.expectEqual(@as(usize, 3), segment_count);
}

const MockProtocolToolContext = struct {
    call_count: usize = 0,
    saw_update: bool = false,
};

fn mockProtocolExecute(
    ctx: ?*anyopaque,
    tool_call_id: []const u8,
    tool_name: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?types.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!AgentToolResult {
    _ = args_json;
    _ = cancel_token;
    const protocol_ctx: *MockProtocolToolContext = @ptrCast(@alignCast(ctx.?));
    protocol_ctx.call_count += 1;

    if (on_update) |update| {
        update(on_update_ctx, tool_call_id, tool_name, "{\"status\":\"running\"}");
        protocol_ctx.saw_update = true;
    }

    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    content[0] = .{ .text = .{
        .text = try allocator.dupe(u8, "executed via protocol"),
    } };
    return .{
        .content = types.OwnedSlice(ai_types.UserContentPart).initOwned(content),
        .details_json = types.OwnedSlice(u8).initOwned(try allocator.dupe(u8, "{\"source\":\"protocol\"}")),
    };
}

fn mockProtocolExecuteCancelled(
    ctx: ?*anyopaque,
    tool_call_id: []const u8,
    tool_name: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?types.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!AgentToolResult {
    _ = ctx;
    _ = tool_call_id;
    _ = tool_name;
    _ = args_json;
    _ = on_update_ctx;
    _ = on_update;
    _ = allocator;
    if (cancel_token) |token| {
        if (token.isCancelled()) return error.Cancelled;
    }
    return error.Cancelled;
}

fn mockLargeOutputTool(
    tool_call_id: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?types.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!AgentToolResult {
    _ = tool_call_id;
    _ = args_json;
    _ = cancel_token;
    _ = on_update_ctx;
    _ = on_update;

    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    content[0] = .{ .text = .{
        .text = try allocator.dupe(u8, "raw log line 1\nraw log line 2\nraw log line 3"),
    } };
    return .{
        .content = types.OwnedSlice(ai_types.UserContentPart).initOwned(content),
        .details_json = types.OwnedSlice(u8).initOwned(try allocator.dupe(u8, "{\"raw\":true,\"lines\":3}")),
    };
}

fn testOutputMiddleware(ctx: ?*anyopaque, input: types.ToolOutputMiddlewareInput, result: *AgentToolResult, allocator: std.mem.Allocator) anyerror!void {
    _ = ctx;
    _ = input;
    result.content.deinit(allocator);
    result.details_json.deinit(allocator);
    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    errdefer allocator.free(content);
    content[0] = .{ .text = .{ .text = try allocator.dupe(u8, "middleware summary") } };
    errdefer content[0].deinit(allocator);
    result.content = types.OwnedSlice(ai_types.UserContentPart).initOwned(content);
    result.details_json = types.OwnedSlice(u8).initOwned(try allocator.dupe(u8, "{\"compressed\":true}"));
}

const ApprovalRecorder = struct {
    decision: types.ToolApprovalDecision = .approve,
    calls: usize = 0,
};

fn recordingApproval(ctx: ?*anyopaque, request: types.ToolApprovalRequest) types.ToolApprovalDecision {
    _ = request;
    const recorder: *ApprovalRecorder = @ptrCast(@alignCast(ctx.?));
    recorder.calls += 1;
    return recorder.decision;
}

test "executeToolCalls uses protocol executor when configured" {
    const allocator = std.testing.allocator;

    const tools = [_]AgentTool{
        .{
            .label = "Remote Tool",
            .name = "remote_tool",
            .description = "Remote tool",
            .parameters_schema_json = "{}",
            .execute = undefined,
        },
    };

    const assistant_content = [_]ai_types.AssistantContent{
        .{ .tool_call = .{
            .id = "call_1",
            .name = "remote_tool",
            .arguments_json = "{\"q\":\"x\"}",
        } },
    };
    const assistant_message = ai_types.AssistantMessage{
        .content = &assistant_content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .tool_use,
        .timestamp = 0,
    };

    const model = ai_types.Model{
        .id = "test-model",
        .name = "Test",
        .api = "test-api",
        .provider = "test-provider",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };

    var protocol_ctx = MockProtocolToolContext{};
    var agent_events = AgentEventStream.init(allocator);
    defer agent_events.deinit();

    var tool_result = try executeToolCalls(
        allocator,
        assistant_message,
        .{
            .model = model,
            .protocol = .{ .stream_fn = undefined },
            .tools = &tools,
            .execute_tool_via_protocol_fn = mockProtocolExecute,
            .execute_tool_via_protocol_ctx = &protocol_ctx,
        },
        &agent_events,
    );
    defer tool_result.deinit(allocator);

    try std.testing.expectEqual(@as(usize, 1), protocol_ctx.call_count);
    try std.testing.expectEqual(@as(usize, 1), tool_result.tool_results.len);
    try std.testing.expect(!tool_result.tool_results[0].is_error);
    try std.testing.expectEqualStrings("executed via protocol", tool_result.tool_results[0].content[0].text.text);

    var saw_update = false;
    while (agent_events.poll()) |evt| {
        var owned_evt = evt;
        defer owned_evt.deinit(allocator);
        if (evt == .tool_execution_update) saw_update = true;
    }
    try std.testing.expect(protocol_ctx.saw_update);
    try std.testing.expect(saw_update);
}

test "executeToolCalls skips legacy approval when policy already allows" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "test-model",
        .name = "Test",
        .api = "test-api",
        .provider = "test-provider",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };

    var approval = ApprovalRecorder{ .decision = .reject };
    const tools = [_]AgentTool{.{
        .label = "Read",
        .name = "file_read",
        .description = "Read file",
        .parameters_schema_json = "{}",
        .execute = mockLargeOutputTool,
        .approval_ctx = &approval,
        .approval_fn = recordingApproval,
    }};
    const assistant_content = [_]ai_types.AssistantContent{.{ .tool_call = .{
        .id = "call_read",
        .name = "file_read",
        .arguments_json = "{\"path\":\"/workspace/src/main.zig\"}",
    } }};
    const assistant_message = ai_types.AssistantMessage{
        .content = &assistant_content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .tool_use,
        .timestamp = 0,
    };
    var engine = try permission.PermissionEngine.initEmpty(allocator, .{
        .workspace_root = "/workspace",
    });
    defer engine.deinit();
    var agent_events = AgentEventStream.init(allocator);
    defer agent_events.deinit();

    var tool_result = try executeToolCalls(
        allocator,
        assistant_message,
        .{
            .model = model,
            .protocol = .{ .stream_fn = undefined },
            .tools = &tools,
            .permission_engine = &engine,
        },
        &agent_events,
    );
    defer tool_result.deinit(allocator);

    try std.testing.expectEqual(@as(usize, 0), approval.calls);
    try std.testing.expectEqual(@as(usize, 1), tool_result.tool_results.len);
}

test "executeToolCalls persists legacy reject always with permission engine" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "test-model",
        .name = "Test",
        .api = "test-api",
        .provider = "test-provider",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };

    var approval = ApprovalRecorder{ .decision = .reject_always };
    const scoped_tools = [_]AgentTool{.{
        .label = "Edit",
        .name = "file_edit",
        .description = "Edit file",
        .parameters_schema_json = "{}",
        .execute = mockLargeOutputTool,
        .approval_ctx = &approval,
        .approval_fn = recordingApproval,
    }};
    const scoped_content = [_]ai_types.AssistantContent{.{ .tool_call = .{
        .id = "call_edit",
        .name = "file_edit",
        .arguments_json = "{\"path\":\"src/main.zig\"}",
    } }};
    const scoped_message = ai_types.AssistantMessage{
        .content = &scoped_content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .tool_use,
        .timestamp = 0,
    };
    var engine = try permission.PermissionEngine.initEmpty(allocator, .{
        .workspace_root = "/workspace",
    });
    defer engine.deinit();
    var agent_events = AgentEventStream.init(allocator);
    defer agent_events.deinit();

    var scoped_result = try executeToolCalls(
        allocator,
        scoped_message,
        .{
            .model = model,
            .protocol = .{ .stream_fn = undefined },
            .tools = &scoped_tools,
            .permission_engine = &engine,
        },
        &agent_events,
    );
    defer scoped_result.deinit(allocator);

    try std.testing.expectEqual(@as(usize, 1), approval.calls);
    try std.testing.expectEqual(@as(usize, 1), engine.persisted.items.len);
    try std.testing.expectEqualStrings("/workspace/src/main.zig", engine.persisted.items[0].path.?);
    try std.testing.expectEqual(permission.PermissionDecision.deny, engine.evaluate("file_edit", "{\"path\":\"/workspace/src/main.zig\"}"));
    try std.testing.expect(scoped_result.tool_results[0].is_error);
}

test "executeToolCalls persists legacy approve always with permission engine" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "test-model",
        .name = "Test",
        .api = "test-api",
        .provider = "test-provider",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };

    var approval = ApprovalRecorder{ .decision = .approve_always };
    const scoped_tools = [_]AgentTool{.{
        .label = "Edit",
        .name = "file_edit",
        .description = "Edit file",
        .parameters_schema_json = "{}",
        .execute = mockLargeOutputTool,
        .approval_ctx = &approval,
        .approval_fn = recordingApproval,
    }};
    const scoped_content = [_]ai_types.AssistantContent{.{ .tool_call = .{
        .id = "call_edit",
        .name = "file_edit",
        .arguments_json = "{\"path\":\"src/main.zig\"}",
    } }};
    const scoped_message = ai_types.AssistantMessage{
        .content = &scoped_content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .tool_use,
        .timestamp = 0,
    };
    var engine = try permission.PermissionEngine.initEmpty(allocator, .{
        .workspace_root = "/workspace",
    });
    defer engine.deinit();
    var agent_events = AgentEventStream.init(allocator);
    defer agent_events.deinit();

    var scoped_result = try executeToolCalls(
        allocator,
        scoped_message,
        .{
            .model = model,
            .protocol = .{ .stream_fn = undefined },
            .tools = &scoped_tools,
            .permission_engine = &engine,
        },
        &agent_events,
    );
    defer scoped_result.deinit(allocator);

    try std.testing.expectEqual(@as(usize, 1), approval.calls);
    try std.testing.expectEqual(@as(usize, 1), engine.persisted.items.len);
    try std.testing.expectEqualStrings("/workspace/src/main.zig", engine.persisted.items[0].path.?);
    try std.testing.expectEqual(permission.PermissionDecision.allow, engine.evaluate("file_edit", "{\"path\":\"/workspace/src/main.zig\"}"));

    approval.calls = 0;
    const unscoped_tools = [_]AgentTool{.{
        .label = "Remote",
        .name = "remote_tool",
        .description = "Remote tool",
        .parameters_schema_json = "{}",
        .execute = mockLargeOutputTool,
        .approval_ctx = &approval,
        .approval_fn = recordingApproval,
    }};
    const unscoped_content = [_]ai_types.AssistantContent{.{ .tool_call = .{
        .id = "call_remote",
        .name = "remote_tool",
        .arguments_json = "{\"q\":\"x\"}",
    } }};
    const unscoped_message = ai_types.AssistantMessage{
        .content = &unscoped_content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .tool_use,
        .timestamp = 0,
    };
    var unscoped_result = try executeToolCalls(
        allocator,
        unscoped_message,
        .{
            .model = model,
            .protocol = .{ .stream_fn = undefined },
            .tools = &unscoped_tools,
            .permission_engine = &engine,
        },
        &agent_events,
    );
    defer unscoped_result.deinit(allocator);

    try std.testing.expectEqual(@as(usize, 1), approval.calls);
    try std.testing.expectEqual(@as(usize, 1), engine.persisted.items.len);
}

test "executeToolCalls denies policy before legacy approval can persist always" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "test-model",
        .name = "Test",
        .api = "test-api",
        .provider = "test-provider",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };

    var approval = ApprovalRecorder{ .decision = .approve_always };
    const tools = [_]AgentTool{.{
        .label = "Edit",
        .name = "file_edit",
        .description = "Edit file",
        .parameters_schema_json = "{}",
        .execute = mockLargeOutputTool,
        .approval_ctx = &approval,
        .approval_fn = recordingApproval,
    }};
    const content = [_]ai_types.AssistantContent{.{ .tool_call = .{
        .id = "call_outside",
        .name = "file_edit",
        .arguments_json = "{\"path\":\"/tmp/outside.zig\"}",
    } }};
    const message = ai_types.AssistantMessage{
        .content = &content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .tool_use,
        .timestamp = 0,
    };
    var engine = try permission.PermissionEngine.initEmpty(allocator, .{
        .workspace_root = "/workspace",
    });
    defer engine.deinit();
    var agent_events = AgentEventStream.init(allocator);
    defer agent_events.deinit();

    var result = try executeToolCalls(
        allocator,
        message,
        .{
            .model = model,
            .protocol = .{ .stream_fn = undefined },
            .tools = &tools,
            .permission_engine = &engine,
        },
        &agent_events,
    );
    defer result.deinit(allocator);

    try std.testing.expectEqual(@as(usize, 0), approval.calls);
    try std.testing.expectEqual(@as(usize, 0), engine.persisted.items.len);
    try std.testing.expect(result.tool_results[0].is_error);
}

test "executeToolCalls applies output middleware and reports byte telemetry" {
    const allocator = std.testing.allocator;

    const tools = [_]AgentTool{
        .{
            .label = "Logs",
            .name = "logs",
            .description = "Collect logs",
            .parameters_schema_json = "{}",
            .execute = mockLargeOutputTool,
        },
    };

    const assistant_content = [_]ai_types.AssistantContent{
        .{ .tool_call = .{
            .id = "call_logs",
            .name = "logs",
            .arguments_json = "{\"path\":\"server.log\"}",
        } },
    };
    const assistant_message = ai_types.AssistantMessage{
        .content = &assistant_content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .tool_use,
        .timestamp = 0,
    };

    const model = ai_types.Model{
        .id = "test-model",
        .name = "Test",
        .api = "test-api",
        .provider = "test-provider",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };

    var agent_events = AgentEventStream.init(allocator);
    defer agent_events.deinit();

    var tool_result = try executeToolCalls(
        allocator,
        assistant_message,
        .{
            .model = model,
            .protocol = .{ .stream_fn = undefined },
            .tools = &tools,
            .tool_output_middleware_fn = testOutputMiddleware,
        },
        &agent_events,
    );
    defer tool_result.deinit(allocator);

    try std.testing.expectEqual(@as(usize, 1), tool_result.tool_results.len);
    try std.testing.expectEqual(@as(usize, 0), tool_result.tool_results[0].artifacts.slice().len);
    try std.testing.expectEqualStrings("middleware summary", tool_result.tool_results[0].content[0].text.text);

    var saw_end = false;
    while (agent_events.poll()) |evt| {
        var owned_evt = evt;
        defer owned_evt.deinit(allocator);
        if (evt == .tool_execution_end) {
            saw_end = true;
            try std.testing.expect(evt.tool_execution_end.raw_total_bytes > 0);
            try std.testing.expect(evt.tool_execution_end.returned_total_bytes > 0);
            try std.testing.expect(evt.tool_execution_end.raw_total_bytes != evt.tool_execution_end.returned_total_bytes);
            try std.testing.expectEqual(@as(u32, 0), evt.tool_execution_end.artifact_count);
            try std.testing.expectEqual(@as(usize, 0), evt.tool_execution_end.artifacts.len);
        }
    }
    try std.testing.expect(saw_end);
}

test "executeToolCalls emits terminal events on protocol cancellation" {
    const allocator = std.testing.allocator;

    const tools = [_]AgentTool{
        .{
            .label = "Remote Tool",
            .name = "remote_tool",
            .description = "Remote tool",
            .parameters_schema_json = "{}",
            .execute = undefined,
        },
    };

    const assistant_content = [_]ai_types.AssistantContent{
        .{ .tool_call = .{
            .id = "call_2",
            .name = "remote_tool",
            .arguments_json = "{\"q\":\"x\"}",
        } },
    };
    const assistant_message = ai_types.AssistantMessage{
        .content = &assistant_content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .tool_use,
        .timestamp = 0,
    };

    const model = ai_types.Model{
        .id = "test-model",
        .name = "Test",
        .api = "test-api",
        .provider = "test-provider",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };

    var cancelled = std.atomic.Value(bool).init(true);
    const cancel_token = ai_types.CancelToken{ .cancelled = &cancelled };

    var agent_events = AgentEventStream.init(allocator);
    defer agent_events.deinit();

    var tool_result = try executeToolCalls(
        allocator,
        assistant_message,
        .{
            .model = model,
            .protocol = .{ .stream_fn = undefined },
            .tools = &tools,
            .cancel_token = cancel_token,
            .execute_tool_via_protocol_fn = mockProtocolExecuteCancelled,
        },
        &agent_events,
    );
    defer tool_result.deinit(allocator);

    try std.testing.expectEqual(@as(usize, 1), tool_result.tool_results.len);
    try std.testing.expect(tool_result.tool_results[0].is_error);

    var start_count: usize = 0;
    var end_count: usize = 0;
    while (agent_events.poll()) |evt| {
        var owned_evt = evt;
        defer owned_evt.deinit(allocator);
        switch (evt) {
            .tool_execution_start => start_count += 1,
            .tool_execution_end => end_count += 1,
            else => {},
        }
    }

    try std.testing.expectEqual(@as(usize, 1), start_count);
    try std.testing.expectEqual(@as(usize, 1), end_count);
}

fn unavailableSteering(ctx: ?*anyopaque, allocator: std.mem.Allocator) anyerror!?[]const ai_types.Message {
    _ = ctx;
    _ = allocator;
    return error.SteeringUnavailable;
}

test "executeToolCalls frees the results it built when a later step fails" {
    const allocator = std.testing.allocator;

    const tools = [_]AgentTool{
        .{
            .label = "Remote Tool",
            .name = "remote_tool",
            .description = "Remote tool",
            .parameters_schema_json = "{}",
            .execute = undefined,
        },
    };

    const assistant_content = [_]ai_types.AssistantContent{
        .{ .tool_call = .{
            .id = "call_1",
            .name = "remote_tool",
            .arguments_json = "{\"q\":\"x\"}",
        } },
    };
    const assistant_message = ai_types.AssistantMessage{
        .content = &assistant_content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .tool_use,
        .timestamp = 0,
    };

    const model = ai_types.Model{
        .id = "test-model",
        .name = "Test",
        .api = "test-api",
        .provider = "test-provider",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };

    var protocol_ctx = MockProtocolToolContext{};
    var agent_events = AgentEventStream.init(allocator);
    defer agent_events.deinit();

    try std.testing.expectError(error.SteeringUnavailable, executeToolCalls(
        allocator,
        assistant_message,
        .{
            .model = model,
            .protocol = .{ .stream_fn = undefined },
            .tools = &tools,
            .execute_tool_via_protocol_fn = mockProtocolExecute,
            .execute_tool_via_protocol_ctx = &protocol_ctx,
            .get_steering_messages_fn = unavailableSteering,
        },
        &agent_events,
    ));
    try std.testing.expectEqual(@as(usize, 1), protocol_ctx.call_count);
}

fn testModel() ai_types.Model {
    return .{
        .id = "test-model",
        .name = "Test",
        .api = "test-api",
        .provider = "test-provider",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };
}

fn directoryReportingExecute(
    ctx: ?*anyopaque,
    tool_call_id: []const u8,
    tool_name: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?types.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!types.AgentToolResult {
    _ = ctx;
    _ = tool_call_id;
    _ = tool_name;
    _ = args_json;
    _ = cancel_token;
    _ = on_update_ctx;
    _ = on_update;
    const text = try allocator.dupe(u8, "done");
    errdefer allocator.free(text);
    const parts = try allocator.alloc(ai_types.UserContentPart, 1);
    errdefer allocator.free(parts);
    parts[0] = .{ .text = .{ .text = text } };
    const details = try allocator.dupe(u8, "{\"ok\":true}");
    errdefer allocator.free(details);
    const directory = try allocator.dupe(u8, "/observed/dir");
    errdefer allocator.free(directory);
    const artifacts = try allocator.alloc(ai_types.ArtifactReference, 1);
    errdefer allocator.free(artifacts);
    artifacts[0] = .{ .artifact_id = try allocator.dupe(u8, "artifact-1") };
    return types.AgentToolResult{
        .content = ai_types.OwnedSlice(ai_types.UserContentPart).initOwned(parts),
        .details_json = ai_types.OwnedSlice(u8).initOwned(details),
        .artifacts = ai_types.OwnedSlice(ai_types.ArtifactReference).initOwned(artifacts),
        .working_directory = ai_types.OwnedSlice(u8).initOwned(directory),
        .working_directory_observed = true,
    };
}

const directoryReportingTool = types.AgentTool{
    .label = "Directory Reporting Tool",
    .name = "dirtool",
    .description = "Reports a working directory.",
    .parameters_schema_json = "{\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false}",
    .execute = struct {
        fn run(
            tool_call_id: []const u8,
            args_json: []const u8,
            cancel_token: ?ai_types.CancelToken,
            on_update_ctx: ?*anyopaque,
            on_update: ?types.ToolUpdateCallback,
            allocator: std.mem.Allocator,
        ) anyerror!types.AgentToolResult {
            return directoryReportingExecute(null, tool_call_id, "dirtool", args_json, cancel_token, on_update_ctx, on_update, allocator);
        }
    }.run,
};

fn runDirectoryReportingTool(allocator: std.mem.Allocator) !void {
    const tool_list = [_]types.AgentTool{directoryReportingTool};
    const tools_slice: []const types.AgentTool = &tool_list;
    const content = [_]ai_types.AssistantContent{
        .{ .tool_call = .{ .id = "call_dir", .name = "dirtool", .arguments_json = "{}" } },
    };
    const assistant_message = ai_types.AssistantMessage{
        .content = &content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .tool_use,
        .timestamp = 0,
    };
    const model = testModel();
    var events_storage: AgentEventStream = undefined;
    const events = &events_storage;
    events.* = AgentEventStream.init(allocator);
    defer events.deinit();

    const result = try executeToolCalls(
        allocator,
        assistant_message,
        .{
            .model = model,
            .protocol = .{ .stream_fn = undefined },
            .tools = tools_slice,
            .execute_tool_via_protocol_fn = directoryReportingExecute,
        },
        events,
    );
    var owned = result;
    defer owned.deinit(allocator);
    try std.testing.expectEqual(@as(usize, 1), owned.tool_results.len);
    try std.testing.expectEqualStrings("/observed/dir", owned.tool_results[0].observedWorkingDirectory().?);
}

const HandoffCase = struct {
    let_pass: bool = false,

    fn run(allocator: std.mem.Allocator) !void {
        var self = HandoffCase{};
        self.drive(allocator, true) catch |err| switch (err) {
            error.OutOfMemory, error.MiddlewareRefused => {},
            else => return err,
        };
        self.drive(allocator, false) catch |err| switch (err) {
            error.OutOfMemory, error.MiddlewareRefused => return err,
            else => return err,
        };
    }

    fn drive(self: *HandoffCase, allocator: std.mem.Allocator, middleware_fails: bool) !void {
        const tool_list = [_]types.AgentTool{directoryReportingTool};
        const content = [_]ai_types.AssistantContent{
            .{ .tool_call = .{ .id = "call_dir", .name = "dirtool", .arguments_json = "{}" } },
        };
        const assistant_message = ai_types.AssistantMessage{
            .content = &content,
            .api = "test-api",
            .provider = "test-provider",
            .model = "test-model",
            .usage = .{},
            .stop_reason = .tool_use,
            .timestamp = 0,
        };
        const model = testModel();
        var events_storage: AgentEventStream = undefined;
        const events = &events_storage;
        events.* = AgentEventStream.init(allocator);
        defer events.deinit();
        defer drainEvents(events, allocator);

        self.let_pass = !middleware_fails;
        const result = executeToolCalls(
            allocator,
            assistant_message,
            .{
                .model = model,
                .protocol = .{ .stream_fn = undefined },
                .tools = &tool_list,
                .execute_tool_via_protocol_fn = directoryReportingExecute,
                .tool_output_middleware_fn = failingMiddleware,
                .tool_output_middleware_ctx = self,
            },
            events,
        ) catch |err| return err;
        var owned = result;
        defer owned.deinit(allocator);
        for (owned.tool_results) |message| {
            var clone = try ai_types.cloneMessage(allocator, .{ .tool_result = message });
            clone.deinit(allocator);
        }
    }
};

fn drainEvents(events: *AgentEventStream, allocator: std.mem.Allocator) void {
    while (events.poll()) |event| {
        var drained = event;
        drained.deinit(allocator);
    }
}

fn failingMiddleware(
    ctx: ?*anyopaque,
    input: types.ToolOutputMiddlewareInput,
    result: *types.AgentToolResult,
    allocator: std.mem.Allocator,
) anyerror!void {
    const self: *HandoffCase = @ptrCast(@alignCast(ctx.?));
    _ = input;
    _ = result;
    _ = allocator;
    if (!self.let_pass) return error.MiddlewareRefused;
}

test "a message already in the results list is not freed again when the stream closes mid-turn" {
    var events_storage: AgentEventStream = undefined;
    const events = &events_storage;
    events.* = AgentEventStream.init(std.testing.allocator);
    defer events.deinit();
    events.complete(.{
        .messages = types.OwnedSlice(ai_types.Message).initBorrowed(&.{}),
        .final_message = .{ .content = &.{}, .api = "a", .provider = "p", .model = "m", .usage = .{}, .stop_reason = .stop, .timestamp = 0 },
        .iterations = 0,
    });
    while (events.poll()) |_| {}

    var results_storage: std.ArrayList(ai_types.ToolResultMessage) = .empty;
    defer {
        for (results_storage.items) |*item| item.deinit(std.testing.allocator);
        results_storage.deinit(std.testing.allocator);
    }

    const text = try std.testing.allocator.dupe(u8, "done");
    const parts = try std.testing.allocator.alloc(ai_types.UserContentPart, 1);
    parts[0] = .{ .text = .{ .text = text } };
    const details = try std.testing.allocator.dupe(u8, "{\"ok\":true}");
    const directory = try std.testing.allocator.dupe(u8, "/observed/dir");
    var result = AgentToolResult{
        .content = ai_types.OwnedSlice(ai_types.UserContentPart).initOwned(parts),
        .details_json = ai_types.OwnedSlice(u8).initOwned(details),
        .working_directory = ai_types.OwnedSlice(u8).initOwned(directory),
        .working_directory_observed = true,
    };

    try std.testing.expectError(error.StreamCompleted, finalizeToolExecution(
        std.testing.allocator,
        .{ .model = testModel(), .protocol = .{ .stream_fn = undefined } },
        events,
        &results_storage,
        .{ .id = "call-1", .name = "dirtool", .arguments_json = "{}" },
        "{}",
        &result,
        false,
    ));

    try std.testing.expectEqual(@as(usize, 1), results_storage.items.len);
    try std.testing.expectEqualStrings("call-1", results_storage.items[0].tool_call_id);
    try std.testing.expectEqualStrings("/observed/dir", results_storage.items[0].workingDirectory().?);
}

test "an exhausted allocator loses nothing across the tool handoff" {
    if (@import("builtin").os.tag == .wasi) return error.SkipZigTest;
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, HandoffCase.run, .{});
}

test "a tool result's working directory survives the loop and is freed with the message" {
    try runDirectoryReportingTool(std.testing.allocator);
}
