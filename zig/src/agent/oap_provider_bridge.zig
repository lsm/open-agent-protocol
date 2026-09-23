const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const agent_types = @import("agent_types");
const event_stream = @import("event_stream");
const oap_types = @import("oap_types");
const provider_types = @import("oap_provider_types");
const provider_envelope = @import("oap_provider_envelope");
const provider_catalog = @import("oap_provider_catalog");

pub const TransportFactory = struct {
    ctx: ?*anyopaque = null,
    open_fn: *const fn (?*anyopaque, std.mem.Allocator, ai_types.Model, ?[]const u8) anyerror!Transport,
};

pub const Transport = struct {
    ctx: ?*anyopaque,
    send_line_fn: *const fn (?*anyopaque, []const u8) anyerror!void,
    pump_fn: *const fn (?*anyopaque) anyerror!void,
    recv_line_fn: *const fn (?*anyopaque, std.mem.Allocator) anyerror!?[]u8,
    close_fn: *const fn (?*anyopaque) void,

    pub fn close(self: Transport) void {
        self.close_fn(self.ctx);
    }
};

pub const InProcessOapProviderBridge = struct {
    factory: TransportFactory,

    pub fn init(factory: TransportFactory) InProcessOapProviderBridge {
        return .{ .factory = factory };
    }

    pub fn protocolClient(self: *InProcessOapProviderBridge) agent_types.ProtocolClient {
        return .{ .stream_fn = streamViaOap, .ctx = self };
    }
};

const ThreadContext = struct {
    allocator: std.mem.Allocator,
    stream: *event_stream.AssistantMessageEventStream,
    factory: TransportFactory,
    model: ai_types.Model,
    context: ai_types.Context,
    options: agent_types.ProtocolOptions,
    api_key: ?[]u8,

    fn deinit(self: *ThreadContext) void {
        self.model.deinit(self.allocator);
        self.context.deinit(self.allocator);
        if (self.api_key) |key| self.allocator.free(key);
        self.allocator.destroy(self);
    }
};

fn streamViaOap(
    raw_ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: agent_types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream.AssistantMessageEventStream {
    const bridge: *InProcessOapProviderBridge = @ptrCast(@alignCast(raw_ctx));
    const stream = try allocator.create(event_stream.AssistantMessageEventStream);
    errdefer allocator.destroy(stream);
    stream.* = event_stream.AssistantMessageEventStream.init(allocator);
    stream.owns_events = true;
    stream.clone_event_fn = ai_types.cloneAssistantMessageEvent;
    stream.wait_for_thread_on_deinit = true;
    errdefer stream.deinit();

    const thread_ctx = try allocator.create(ThreadContext);
    errdefer allocator.destroy(thread_ctx);
    var cloned_model = try ai_types.cloneModel(allocator, model);
    errdefer cloned_model.deinit(allocator);
    var cloned_context = try ai_types.cloneContext(allocator, context);
    errdefer cloned_context.deinit(allocator);
    const cloned_key = if (options.api_key) |key| try allocator.dupe(u8, key) else null;
    errdefer if (cloned_key) |key| allocator.free(key);

    thread_ctx.* = .{
        .allocator = allocator,
        .stream = stream,
        .factory = bridge.factory,
        .model = cloned_model,
        .context = cloned_context,
        .options = options,
        .api_key = cloned_key,
    };
    const thread = try std.Thread.spawn(.{}, runThread, .{thread_ctx});
    thread.detach();
    return stream;
}

fn push(stream: *event_stream.AssistantMessageEventStream, event: ai_types.AssistantMessageEvent) !void {
    while (true) {
        stream.push(event) catch |err| switch (err) {
            error.QueueFull => {
                compat.time.sleepNs(std.time.ns_per_ms);
                continue;
            },
            else => return err,
        };
        return;
    }
}

fn modelRef(allocator: std.mem.Allocator, model: ai_types.Model) ![]const u8 {
    if (provider_types.parseModelRef(model.id) != null) return model.id;
    if (provider_catalog.mapApiToWire(model.api)) |mapping| {
        return provider_catalog.buildModelRef(allocator, model.provider, mapping.wire, mapping.wire_id, model.id);
    }
    const wire = provider_types.parseWireComponent(model.api) orelse return error.UnsupportedProviderWire;
    const wire_id = provider_types.wireIdComponent(model.api);
    if (wire == .other and wire_id == null) return error.UnsupportedProviderWire;
    return provider_catalog.buildModelRef(allocator, model.provider, wire, wire_id, model.id);
}

fn toolResultJson(allocator: std.mem.Allocator, result: ai_types.ToolResultMessage) ![]const u8 {
    if (result.getDetailsJson()) |details| return details;
    var text = std.ArrayList(u8).empty;
    for (result.content) |part| switch (part) {
        .text => |value| try text.appendSlice(allocator, value.text),
        .image => return error.UnsupportedInputContent,
    };
    return std.json.Stringify.valueAlloc(allocator, text.items, .{});
}

fn requestMessages(allocator: std.mem.Allocator, context: ai_types.Context) ![]oap_types.Message {
    var messages = std.ArrayList(oap_types.Message).empty;
    if (context.getSystemPrompt()) |system| {
        try messages.append(allocator, .{ .role = .system, .content = .{ .text = system } });
    }
    for (context.messages) |message| switch (message) {
        .user => |user| {
            const content: oap_types.Content = switch (user.content) {
                .text => |value| .{ .text = value },
                .parts => |parts| blk: {
                    const out = try allocator.alloc(oap_types.ContentPart, parts.len);
                    for (parts, 0..) |part, index| out[index] = switch (part) {
                        .text => |value| .{ .text = value.text },
                        .image => return error.UnsupportedInputContent,
                    };
                    break :blk .{ .parts = out };
                },
            };
            try messages.append(allocator, .{ .role = .user, .content = content });
        },
        .assistant => |assistant| {
            const parts = try allocator.alloc(oap_types.ContentPart, assistant.content.len);
            for (assistant.content, 0..) |part, index| parts[index] = switch (part) {
                .text => |value| .{ .text = value.text },
                .thinking => |value| .{ .reasoning = .{ .text = value.thinking, .carry = value.thinking_signature } },
                .tool_call => |value| .{ .tool_call = .{
                    .tool_call_id = value.id,
                    .name = value.name,
                    .arguments_json = value.arguments_json,
                    .carry = value.thought_signature,
                } },
                .image => return error.UnsupportedInputContent,
            };
            try messages.append(allocator, .{ .role = .assistant, .content = .{ .parts = parts } });
        },
        .tool_result => |result| {
            const parts = try allocator.alloc(oap_types.ContentPart, 1);
            parts[0] = .{ .tool_result = .{
                .tool_call_id = result.tool_call_id,
                .result_json = try toolResultJson(allocator, result),
                .is_error = result.is_error,
            } };
            try messages.append(allocator, .{ .role = .tool, .content = .{ .parts = parts } });
        },
    };
    return messages.toOwnedSlice(allocator);
}

fn requestTools(allocator: std.mem.Allocator, context: ai_types.Context) ![]provider_types.ToolDefinition {
    const source = context.tools orelse return &.{};
    const out = try allocator.alloc(provider_types.ToolDefinition, source.len);
    for (source, 0..) |tool, index| out[index] = .{
        .name = tool.name,
        .description = tool.description,
        .input_schema_json = tool.parameters_schema_json,
    };
    return out;
}

fn reasoningBudget(level: ai_types.ThinkingLevel, budgets: ?ai_types.ThinkingBudgets) ?u32 {
    const b = budgets orelse return null;
    return switch (level) {
        .off => null,
        .minimal => b.minimal,
        .low => b.low,
        .medium => b.medium,
        .high => b.high,
        .xhigh => b.xhigh,
    };
}

fn createRequest(ctx: *ThreadContext, arena: std.mem.Allocator) ![]u8 {
    const ref = try modelRef(arena, ctx.model);
    const messages = try requestMessages(arena, ctx.context);
    const tools = try requestTools(arena, ctx.context);
    const enabled = ctx.model.reasoning and ctx.options.thinking_level != .off;
    const request: provider_types.Envelope = .{
        .id = "bridge.create",
        .payload = .{ .inference_create_request = .{
            .model_ref = ref,
            .messages = messages,
            .tools = tools,
            .max_output_tokens = ctx.options.max_tokens,
            .temperature = ctx.options.temperature,
            .stream = true,
            .reasoning = if (enabled) provider_types.ReasoningOptions{
                .enabled = enabled,
                .budget_tokens = reasoningBudget(ctx.options.thinking_level, ctx.options.thinking_budgets),
                .effort = @tagName(ctx.options.thinking_level),
            } else null,
        } },
    };
    return provider_envelope.serializeEnvelope(request, arena);
}

fn emptyPartial(model: ai_types.Model) ai_types.AssistantMessage {
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

fn mapStopReason(reason: provider_types.StopReason) ai_types.StopReason {
    return switch (reason) {
        .stop => .stop,
        .length => .length,
        .tool_use => .tool_use,
        .content_filter => .content_filter,
        .@"error" => .@"error",
        .aborted => .aborted,
    };
}

fn dupeOwnedOrEmpty(allocator: std.mem.Allocator, value: []const u8) ![]const u8 {
    return if (value.len == 0) "" else try allocator.dupe(u8, value);
}

fn appendTextContent(allocator: std.mem.Allocator, blocks: *std.ArrayList(ai_types.AssistantContent), value: []const u8) !void {
    const body = try dupeOwnedOrEmpty(allocator, value);
    errdefer if (body.len > 0) allocator.free(body);
    try blocks.append(allocator, .{ .text = .{ .text = body } });
}

fn appendReasoningContent(allocator: std.mem.Allocator, blocks: *std.ArrayList(ai_types.AssistantContent), value: oap_types.ReasoningPart) !void {
    const body = try dupeOwnedOrEmpty(allocator, value.text);
    errdefer if (body.len > 0) allocator.free(body);
    const carry = if (value.carry) |signature| try allocator.dupe(u8, signature) else null;
    errdefer if (carry) |signature| allocator.free(signature);
    try blocks.append(allocator, .{ .thinking = .{ .thinking = body, .thinking_signature = carry } });
}

fn appendToolCallContent(allocator: std.mem.Allocator, blocks: *std.ArrayList(ai_types.AssistantContent), value: oap_types.ToolCallPart) !void {
    const id = try allocator.dupe(u8, value.tool_call_id);
    errdefer allocator.free(id);
    const name = try allocator.dupe(u8, value.name);
    errdefer allocator.free(name);
    const arguments_json = try dupeOwnedOrEmpty(allocator, value.arguments_json);
    errdefer if (arguments_json.len > 0) allocator.free(arguments_json);
    const carry = if (value.carry) |signature| try allocator.dupe(u8, signature) else null;
    errdefer if (carry) |signature| allocator.free(signature);
    try blocks.append(allocator, .{ .tool_call = .{
        .id = id,
        .name = name,
        .arguments_json = arguments_json,
        .thought_signature = carry,
    } });
}

fn assistantMessage(allocator: std.mem.Allocator, model: ai_types.Model, source: oap_types.Message, reason: ai_types.StopReason, usage: ?oap_types.Usage) !ai_types.AssistantMessage {
    if (source.role != .assistant) return error.UnexpectedTerminalRole;
    var blocks = std.ArrayList(ai_types.AssistantContent).empty;
    errdefer {
        ai_types.deinitAssistantContentElements(allocator, blocks.items);
        blocks.deinit(allocator);
    }
    switch (source.content) {
        .text => |value| try appendTextContent(allocator, &blocks, value),
        .parts => |parts| for (parts) |part| switch (part) {
            .text => |value| try appendTextContent(allocator, &blocks, value),
            .reasoning => |value| try appendReasoningContent(allocator, &blocks, value),
            .tool_call => |value| try appendToolCallContent(allocator, &blocks, value),
            .tool_result => return error.UnexpectedTerminalContent,
        },
    }
    const content = try blocks.toOwnedSlice(allocator);
    errdefer ai_types.deinitAssistantContent(allocator, content);
    const api = try allocator.dupe(u8, model.api);
    errdefer allocator.free(api);
    const provider = try allocator.dupe(u8, model.provider);
    errdefer allocator.free(provider);
    const model_id = try allocator.dupe(u8, model.id);
    return .{
        .content = content,
        .api = api,
        .provider = provider,
        .model = model_id,
        .usage = if (usage) |value| .{
            .input = value.input_tokens orelse 0,
            .output = value.output_tokens orelse 0,
            .total_tokens = value.total_tokens orelse 0,
        } else .{},
        .stop_reason = reason,
        .timestamp = compat.time.nowMillis(),
        .is_owned = true,
    };
}

const Part = struct {
    kind: provider_types.PartKind,
    text: std.ArrayList(u8) = .empty,
    tool_call_id: ?[]const u8 = null,
    name: ?[]const u8 = null,
    carry: ?[]const u8 = null,
};

const StreamState = struct {
    arena: std.mem.Allocator,
    model: ai_types.Model,
    stream: *event_stream.AssistantMessageEventStream,
    inference_id: ?[]const u8 = null,
    accepted: bool = false,
    terminal: bool = false,
    parts: std.ArrayList(Part) = .empty,

    fn partial(self: *StreamState) !ai_types.AssistantMessage {
        const content = try self.arena.alloc(ai_types.AssistantContent, self.parts.items.len);
        for (self.parts.items, 0..) |part, index| content[index] = switch (part.kind) {
            .text => .{ .text = .{ .text = part.text.items } },
            .reasoning => .{ .thinking = .{ .thinking = part.text.items, .thinking_signature = part.carry } },
            .tool_call => .{ .tool_call = .{
                .id = part.tool_call_id orelse "",
                .name = part.name orelse "",
                .arguments_json = part.text.items,
                .thought_signature = part.carry,
            } },
        };
        var value = emptyPartial(self.model);
        value.content = content;
        return value;
    }

    fn appendStartedPart(self: *StreamState, value: provider_types.PartStarted) !void {
        const tool_call_id = if (value.tool_call_id) |id| try self.arena.dupe(u8, id) else null;
        errdefer if (tool_call_id) |id| self.arena.free(id);
        const name = if (value.name) |text| try self.arena.dupe(u8, text) else null;
        errdefer if (name) |text| self.arena.free(text);
        try self.parts.append(self.arena, .{
            .kind = value.part_kind,
            .tool_call_id = tool_call_id,
            .name = name,
        });
    }

    fn process(self: *StreamState, env: provider_types.Envelope) !void {
        switch (env.payload) {
            .inference_create_response => |response| {
                if (self.accepted or env.in_reply_to == null or !std.mem.eql(u8, env.in_reply_to.?, "bridge.create")) return error.UnexpectedCreateResponse;
                if (!response.accepted) {
                    const message = if (response.err) |refusal|
                        try std.fmt.allocPrint(self.arena, "{s}: {s}", .{ @tagName(refusal.code), refusal.message })
                    else
                        "provider_unavailable: inference refused";
                    self.stream.completeWithError(message);
                    self.terminal = true;
                    return;
                }
                self.inference_id = try self.arena.dupe(u8, env.inference_id orelse return error.MissingInferenceId);
                self.accepted = true;
            },
            .protocol_error => |refusal| {
                const message = try std.fmt.allocPrint(self.arena, "{s}: {s}", .{ @tagName(refusal.err.code), refusal.err.message });
                self.stream.completeWithError(message);
                self.terminal = true;
            },
            else => {
                if (!self.accepted or env.inference_id == null or !std.mem.eql(u8, self.inference_id.?, env.inference_id.?)) return error.UnexpectedInferenceEvent;
                try self.processEvent(env);
            },
        }
    }

    fn processEvent(self: *StreamState, env: provider_types.Envelope) !void {
        switch (env.payload) {
            .inference_started => try push(self.stream, .{ .start = .{ .partial = emptyPartial(self.model) } }),
            .inference_part_started => |value| {
                if (value.part_index != self.parts.items.len) return error.InvalidPartIndex;
                try self.appendStartedPart(value);
                const partial_message = try self.partial();
                switch (value.part_kind) {
                    .text => try push(self.stream, .{ .text_start = .{ .content_index = value.part_index, .partial = partial_message } }),
                    .reasoning => try push(self.stream, .{ .thinking_start = .{ .content_index = value.part_index, .partial = partial_message } }),
                    .tool_call => try push(self.stream, .{ .toolcall_start = .{
                        .content_index = value.part_index,
                        .id = value.tool_call_id orelse return error.MissingToolCallId,
                        .name = value.name orelse return error.MissingToolCallName,
                        .partial = partial_message,
                    } }),
                }
            },
            .inference_part_delta => |value| {
                if (value.part_index >= self.parts.items.len) return error.InvalidPartIndex;
                const part = &self.parts.items[value.part_index];
                try part.text.appendSlice(self.arena, value.delta);
                const partial_message = try self.partial();
                switch (part.kind) {
                    .text => try push(self.stream, .{ .text_delta = .{ .content_index = value.part_index, .delta = value.delta, .partial = partial_message } }),
                    .reasoning => try push(self.stream, .{ .thinking_delta = .{ .content_index = value.part_index, .delta = value.delta, .partial = partial_message } }),
                    .tool_call => try push(self.stream, .{ .toolcall_delta = .{ .content_index = value.part_index, .delta = value.delta, .partial = partial_message } }),
                }
            },
            .inference_part_ended => |value| {
                if (value.part_index >= self.parts.items.len) return error.InvalidPartIndex;
                const part = &self.parts.items[value.part_index];
                if (part.kind != value.part_kind) return error.PartKindMismatch;
                if (value.tool_call) |tool_call| {
                    part.tool_call_id = try self.arena.dupe(u8, tool_call.tool_call_id);
                    part.name = try self.arena.dupe(u8, tool_call.name);
                    part.text = .empty;
                    try part.text.appendSlice(self.arena, tool_call.arguments_json);
                    if (value.carry) |carry| part.carry = try self.arena.dupe(u8, carry);
                } else if (value.text) |body| {
                    part.text = .empty;
                    try part.text.appendSlice(self.arena, body);
                    if (value.carry) |carry| part.carry = try self.arena.dupe(u8, carry);
                }
                const partial_message = try self.partial();
                switch (part.kind) {
                    .text => try push(self.stream, .{ .text_end = .{ .content_index = value.part_index, .content = part.text.items, .partial = partial_message } }),
                    .reasoning => try push(self.stream, .{ .thinking_end = .{ .content_index = value.part_index, .content = part.text.items, .partial = partial_message } }),
                    .tool_call => try push(self.stream, .{ .toolcall_end = .{
                        .content_index = value.part_index,
                        .tool_call = .{
                            .id = part.tool_call_id orelse return error.MissingToolCallId,
                            .name = part.name orelse return error.MissingToolCallName,
                            .arguments_json = part.text.items,
                            .thought_signature = part.carry,
                        },
                        .partial = partial_message,
                    } }),
                }
            },
            .inference_completed => |value| {
                var final = try assistantMessage(self.stream.allocator, self.model, value.message, mapStopReason(value.stop_reason), value.usage);
                defer final.deinit(self.stream.allocator);
                try push(self.stream, .{ .done = .{ .reason = final.stop_reason, .message = final } });
                self.stream.complete(try ai_types.cloneAssistantMessage(self.stream.allocator, final));
                self.terminal = true;
            },
            .inference_failed => |value| {
                const coded_message = try std.fmt.allocPrint(self.arena, "{s}: {s}", .{ @tagName(value.err.code), value.err.message });
                self.stream.completeWithError(coded_message);
                self.terminal = true;
            },
            .inference_cancel_response => {},
            else => return error.UnexpectedInferenceEvent,
        }
    }
};

fn runThread(ctx: *ThreadContext) void {
    defer {
        const stream = ctx.stream;
        ctx.deinit();
        stream.markThreadDone();
    }
    runThreadFallible(ctx) catch |err| ctx.stream.completeWithError(@errorName(err));
}

fn runThreadFallible(ctx: *ThreadContext) !void {
    var arena_state = std.heap.ArenaAllocator.init(ctx.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const transport = try ctx.factory.open_fn(ctx.factory.ctx, ctx.allocator, ctx.model, ctx.api_key);
    defer transport.close();
    const request_line = try createRequest(ctx, arena);
    try transport.send_line_fn(transport.ctx, request_line);

    var state = StreamState{ .arena = arena, .model = ctx.model, .stream = ctx.stream };
    var cancel_sent = false;
    var last_progress_ms = compat.time.nowMillis();
    while (!state.terminal) {
        if (ctx.stream.completed.load(.acquire)) {
            if (!cancel_sent and state.inference_id != null) {
                const cancel: provider_types.Envelope = .{
                    .id = "bridge.cancel",
                    .inference_id = state.inference_id,
                    .payload = .{ .inference_cancel_request = .{ .reason = "agent stream closed" } },
                };
                const line = try provider_envelope.serializeEnvelope(cancel, arena);
                try transport.send_line_fn(transport.ctx, line);
            }
            return;
        }
        try transport.pump_fn(transport.ctx);
        var had_line = false;
        while (try transport.recv_line_fn(transport.ctx, ctx.allocator)) |line| {
            had_line = true;
            last_progress_ms = compat.time.nowMillis();
            defer ctx.allocator.free(line);
            var env = try provider_envelope.deserializeEnvelope(line, ctx.allocator);
            defer env.deinit(ctx.allocator);
            try state.process(env);
            if (state.terminal) break;
        }
        if (state.terminal) break;
        if (!cancel_sent and state.inference_id != null and ctx.options.cancel_token != null and ctx.options.cancel_token.?.isCancelled()) {
            const cancel: provider_types.Envelope = .{
                .id = "bridge.cancel",
                .inference_id = state.inference_id,
                .payload = .{ .inference_cancel_request = .{ .reason = "agent cancelled" } },
            };
            const line = try provider_envelope.serializeEnvelope(cancel, arena);
            try transport.send_line_fn(transport.ctx, line);
            cancel_sent = true;
        }
        if (compat.time.nowMillis() - last_progress_ms > 120_000) return error.ProviderInferenceTimedOut;
        if (!had_line) compat.time.sleepNs(std.time.ns_per_ms);
    }
}

test "canonical model reference uses OAP wire, not legacy API name" {
    const model: ai_types.Model = .{
        .id = "gpt-test",
        .name = "test",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 128,
    };
    const ref = try modelRef(std.testing.allocator, model);
    defer std.testing.allocator.free(ref);
    try std.testing.expectEqualStrings("openai/openai-chat-completions@gpt-test", ref);
}

test "opaque remote wire model reference survives agent model conversion" {
    const model: ai_types.Model = .{
        .id = "sample",
        .name = "sample",
        .api = "other:mock",
        .provider = "remote",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 128,
    };
    const ref = try modelRef(std.testing.allocator, model);
    defer std.testing.allocator.free(ref);
    try std.testing.expectEqualStrings("remote/other:mock@sample", ref);
}

test "bridge sends OAP inference and yields streamed provider events" {
    const Mock = struct {
        allocator: std.mem.Allocator,
        sent: bool = false,
        next: usize = 0,

        const lines = [_][]const u8{
            \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.create.response","id":"response","in_reply_to":"bridge.create","inference_id":"inf-1","payload":{"accepted":true}}
            ,
            \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.started","id":"event-1","inference_id":"inf-1","sequence":1,"payload":{"model_ref":"openai/openai-responses@test-model","started_at_ms":1}}
            ,
            \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.part.started","id":"event-2","inference_id":"inf-1","sequence":2,"payload":{"part_index":0,"part_kind":"text"}}
            ,
            \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.part.delta","id":"event-3","inference_id":"inf-1","sequence":3,"payload":{"part_index":0,"delta":"hello"}}
            ,
            \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.part.ended","id":"event-4","inference_id":"inf-1","sequence":4,"payload":{"part_index":0,"part_kind":"text","text":"hello"}}
            ,
            \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.completed","id":"event-5","inference_id":"inf-1","sequence":5,"payload":{"message":{"role":"assistant","content":"hello"},"stop_reason":"stop","usage":{"input_tokens":3,"output_tokens":1}}}
            ,
        };

        fn open(_: ?*anyopaque, allocator: std.mem.Allocator, _: ai_types.Model, api_key: ?[]const u8) !Transport {
            try std.testing.expectEqualStrings("test-secret", api_key.?);
            const self = try allocator.create(@This());
            self.* = .{ .allocator = allocator };
            return .{
                .ctx = self,
                .send_line_fn = send,
                .pump_fn = pump,
                .recv_line_fn = recv,
                .close_fn = close,
            };
        }

        fn send(raw: ?*anyopaque, line: []const u8) !void {
            const self: *@This() = @ptrCast(@alignCast(raw));
            try std.testing.expect(std.mem.indexOf(u8, line, "test-secret") == null);
            var env = try provider_envelope.deserializeEnvelope(line, self.allocator);
            defer env.deinit(self.allocator);
            try std.testing.expect(env.payload == .inference_create_request);
            try std.testing.expectEqualStrings("openai/openai-responses@test-model", env.payload.inference_create_request.model_ref);
            try std.testing.expectEqual(@as(usize, 1), env.payload.inference_create_request.messages.len);
            self.sent = true;
        }

        fn pump(_: ?*anyopaque) !void {}

        fn recv(raw: ?*anyopaque, allocator: std.mem.Allocator) !?[]u8 {
            const self: *@This() = @ptrCast(@alignCast(raw));
            if (!self.sent or self.next == lines.len) return null;
            const line = try allocator.dupe(u8, lines[self.next]);
            self.next += 1;
            return line;
        }

        fn close(raw: ?*anyopaque) void {
            const self: *@This() = @ptrCast(@alignCast(raw));
            self.allocator.destroy(self);
        }
    };

    var bridge = InProcessOapProviderBridge.init(.{ .open_fn = Mock.open });
    const protocol = bridge.protocolClient();
    const model: ai_types.Model = .{
        .id = "test-model",
        .name = "test",
        .api = "openai-responses",
        .provider = "openai",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 128,
    };
    const context: ai_types.Context = .{ .messages = &.{.{ .user = .{
        .content = .{ .text = "hi" },
        .timestamp = 0,
    } }} };
    const stream = try protocol.stream(model, context, .{ .api_key = "test-secret" }, std.testing.allocator);
    defer _ = stream.deinitAndDestroy();

    var saw_start = false;
    var saw_delta = false;
    var saw_done = false;
    while (stream.wait()) |event| {
        var owned = event;
        defer ai_types.deinitAssistantMessageEvent(std.testing.allocator, &owned);
        switch (event) {
            .start => saw_start = true,
            .text_delta => |value| {
                try std.testing.expectEqualStrings("hello", value.delta);
                saw_delta = true;
            },
            .done => saw_done = true,
            else => {},
        }
    }
    try std.testing.expect(saw_start and saw_delta and saw_done);
    const result = stream.getResult() orelse return error.MissingResult;
    try std.testing.expectEqualStrings("hello", result.content[0].text.text);
    try std.testing.expectEqual(@as(u64, 3), result.usage.input);
}

test "bridge retains terminal tool calls and typed credential failures" {
    const allocator = std.testing.allocator;
    const model: ai_types.Model = .{
        .id = "test-model",
        .name = "test",
        .api = "openai-responses",
        .provider = "openai",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 128,
    };
    const oap_parts = [_]oap_types.ContentPart{.{ .tool_call = .{
        .tool_call_id = "call-1",
        .name = "shell_execute",
        .arguments_json = "{\"command\":\"ls\"}",
    } }};
    const oap_message: oap_types.Message = .{
        .role = .assistant,
        .content = .{ .parts = @constCast(&oap_parts) },
    };
    var terminal = try assistantMessage(allocator, model, oap_message, .tool_use, .{ .input_tokens = 4, .output_tokens = 2 });
    defer terminal.deinit(allocator);
    try std.testing.expectEqual(ai_types.StopReason.tool_use, terminal.stop_reason);
    try std.testing.expectEqualStrings("shell_execute", terminal.content[0].tool_call.name);
    try std.testing.expectEqual(@as(u64, 4), terminal.usage.input);

    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    var stream = event_stream.AssistantMessageEventStream.init(allocator);
    defer stream.deinit();
    var state = StreamState{
        .arena = arena_state.allocator(),
        .model = model,
        .stream = &stream,
        .inference_id = "inf-1",
        .accepted = true,
    };
    const failed: provider_types.Envelope = .{
        .id = "failure",
        .inference_id = "inf-1",
        .sequence = 1,
        .payload = .{ .inference_failed = .{
            .err = .{ .code = .credential_missing, .message = "login needed" },
        } },
    };
    try state.process(failed);
    try std.testing.expectEqualStrings("credential_missing: login needed", stream.getError().?);
}

test "bridge preserves typed inference admission refusal" {
    const model: ai_types.Model = .{
        .id = "test-model",
        .name = "test",
        .api = "openai-responses",
        .provider = "openai",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 128,
    };
    var arena_state = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena_state.deinit();
    var stream = event_stream.AssistantMessageEventStream.init(std.testing.allocator);
    defer stream.deinit();
    var state = StreamState{ .arena = arena_state.allocator(), .model = model, .stream = &stream };
    const refused: provider_types.Envelope = .{
        .id = "response",
        .in_reply_to = "bridge.create",
        .payload = .{ .inference_create_response = .{
            .accepted = false,
            .err = .{ .code = .credential_expired, .message = "sign in again" },
        } },
    };
    try state.process(refused);
    try std.testing.expect(state.terminal);
    try std.testing.expectEqualStrings("credential_expired: sign in again", stream.getError().?);
}

test "terminal message conversion unwinds every allocation failure" {
    const model: ai_types.Model = .{
        .id = "test-model",
        .name = "test",
        .api = "openai-responses",
        .provider = "openai",
        .base_url = "",
        .reasoning = true,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 128,
    };
    const parts = [_]oap_types.ContentPart{
        .{ .text = "hello" },
        .{ .reasoning = .{ .text = "thinking", .carry = "reasoning-signature" } },
        .{ .tool_call = .{
            .tool_call_id = "call-1",
            .name = "shell_execute",
            .arguments_json = "{\"command\":\"ls\"}",
            .carry = "tool-signature",
        } },
    };
    const message: oap_types.Message = .{ .role = .assistant, .content = .{ .parts = @constCast(&parts) } };
    const Probe = struct {
        fn run(allocator: std.mem.Allocator, source_model: ai_types.Model, source_message: oap_types.Message) !void {
            var completed = try assistantMessage(allocator, source_model, source_message, .tool_use, null);
            defer completed.deinit(allocator);
        }
    };
    try std.testing.checkAllAllocationFailures(std.testing.allocator, Probe.run, .{ model, message });
}

test "part-start metadata unwinds every allocation failure" {
    const Probe = struct {
        fn run(allocator: std.mem.Allocator) !void {
            var state = StreamState{ .arena = allocator, .model = undefined, .stream = undefined };
            defer {
                for (state.parts.items) |part| {
                    if (part.tool_call_id) |id| allocator.free(id);
                    if (part.name) |name| allocator.free(name);
                }
                state.parts.deinit(allocator);
            }
            try state.appendStartedPart(.{
                .part_index = 0,
                .part_kind = .tool_call,
                .tool_call_id = "call-1",
                .name = "shell_execute",
            });
        }
    };
    try std.testing.checkAllAllocationFailures(std.testing.allocator, Probe.run, .{});
}
