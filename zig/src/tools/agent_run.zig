pub const Driver = struct {
    runs: std.ArrayList(Run),

    pub fn init() Driver {
        return .{ .runs = std.ArrayList(Run).empty };
    }

    pub fn deinit(self: *Driver, allocator: std.mem.Allocator) void {
        for (self.runs.items) |*run| run.deinit(allocator);
        self.runs.deinit(allocator);
        self.* = undefined;
    }

    pub fn len(self: *Driver) usize {
        return self.runs.items.len;
    }
};

pub const STDIO_DISCONNECT_TOOL_WAIT_MESSAGE = "client disconnected while the run waited for a distributed tool_result";
pub const STDIO_EVENT_PUBLICATION_FAILED_MESSAGE = "run event publication failed; event stream truncated before settlement";
pub const STDIO_RUN_WITHOUT_OUTCOME_MESSAGE = "run ended without a deliverable outcome (terminal lost to allocation failure)";

const std = @import("std");
const agent_types = @import("agent_types");
const ai_types = @import("ai_types");
const agent_loop = @import("agent_loop");
const json_writer = @import("json_writer");
const transport = @import("transport");
const compat = @import("compat");
const agent_server_module = @import("agent_server");

const AgentProtocolServer = agent_server_module.AgentProtocolServer;
const AgentToolBridge = @import("tools/agent_tool_bridge");

pub const SessionId = agent_types.SessionId;

pub const Options = struct {
    temperature: ?f32 = null,
    max_tokens: ?u32 = null,
    max_iterations: ?u32 = null,
    thinking_level: ai_types.ThinkingLevel = .low,
    has_explicit_thinking_level: bool = false,
    api_key: ?[]u8 = null,

    pub fn deinit(self: *Options, allocator: std.mem.Allocator) void {
        if (self.api_key) |key| allocator.free(key);
        self.api_key = null;
    }
};

pub const Prepared = struct {
    model: ai_types.Model,
    prompts: []ai_types.Message,
    system_prompt: []u8,
    tools: []agent_loop.AgentTool,
    options: Options,

    pub fn deinit(self: *Prepared, allocator: std.mem.Allocator) void {
        self.model.deinit(allocator);
        for (self.prompts) |*message| {
            message.deinit(allocator);
        }
        allocator.free(self.prompts);
        allocator.free(self.system_prompt);
        deinitTools(allocator, self.tools);
        self.options.deinit(allocator);
        self.* = undefined;
    }

    pub fn disarm(self: *Prepared) void {
        self.model.is_owned = false;
        self.prompts = &.{};
        self.system_prompt = &.{};
        self.tools = &.{};
        self.options.api_key = null;
    }
};

pub const Run = struct {
    session_id: SessionId,
    generation: u64,
    stream: *agent_loop.AgentEventStream,
    context: *agent_loop.AgentContext,
    model: ai_types.Model,
    prompts: []ai_types.Message,
    tools: []agent_loop.AgentTool,
    cancel_flag: *std.atomic.Value(bool),
    disconnect_failed: *std.atomic.Value(bool),
    tool_executor: *AgentToolBridge.Executor,
    terminal_event_json: ?[]u8 = null,
    settlement_frame_published: bool = false,
    failure_event_published: bool = false,
    event_publication_failed: bool = false,

    pub fn cancel(self: *Run) void {
        self.cancel_flag.store(true, .release);
    }

    pub fn deinit(self: *Run, allocator: std.mem.Allocator) void {
        self.cancel();
        if (!self.stream.deinitAndDestroy()) return;
        self.context.deinit();
        allocator.destroy(self.context);
        self.model.deinit(allocator);
        allocator.free(self.prompts);
        deinitTools(allocator, self.tools);
        allocator.destroy(self.cancel_flag);
        allocator.destroy(self.disconnect_failed);
        allocator.destroy(self.tool_executor);
        if (self.terminal_event_json) |event_json| allocator.free(event_json);
        self.* = undefined;
    }
};

pub fn hasRunForGeneration(driver: *const Driver, session_id: SessionId, generation: u64) bool {
    for (driver.runs.items) |run| {
        if (run.settlement_frame_published) continue;
        if (run.generation == generation and std.mem.eql(u8, run.session_id[0..], session_id[0..])) return true;
    }
    return false;
}

pub fn cancelRun(
    allocator: std.mem.Allocator,
    driver: *Driver,
    bridge: *AgentToolBridge.Bridge,
    session_id: SessionId,
) void {
    for (driver.runs.items) |*run| {
        if (std.mem.eql(u8, run.session_id[0..], session_id[0..])) run.cancel();
    }
    bridge.discardSession(allocator, session_id);
}

pub fn deinitTools(allocator: std.mem.Allocator, tools: []agent_loop.AgentTool) void {
    for (tools) |*tool| deinitToolFields(allocator, tool);
    allocator.free(tools);
}

pub fn deinitToolFields(allocator: std.mem.Allocator, tool: *agent_loop.AgentTool) void {
    allocator.free(tool.label);
    allocator.free(tool.name);
    allocator.free(tool.description);
    if (tool.short_description) |short| allocator.free(short);
    allocator.free(tool.parameters_schema_json);
}

pub fn admit(
    allocator: std.mem.Allocator,
    driver: *Driver,
    bridge: *AgentToolBridge.Bridge,
    prepared: *Prepared,
    session_id: SessionId,
    generation: u64,
    protocol_client: agent_loop.ProtocolClient,
) !void {
    const context = try allocator.create(agent_loop.AgentContext);
    var context_owned_by_run = false;
    errdefer if (!context_owned_by_run) allocator.destroy(context);
    context.* = agent_loop.AgentContext.init(allocator);
    errdefer if (!context_owned_by_run) context.deinit();
    context.system_prompt = ai_types.OwnedSlice(u8).initOwned(prepared.system_prompt);
    prepared.system_prompt = &.{};
    context.tools = prepared.tools;

    const cancel_flag = try allocator.create(std.atomic.Value(bool));
    var cancel_owned_by_run = false;
    errdefer if (!cancel_owned_by_run) allocator.destroy(cancel_flag);
    cancel_flag.* = std.atomic.Value(bool).init(false);

    const disconnect_failed = try allocator.create(std.atomic.Value(bool));
    var disconnect_owned_by_run = false;
    errdefer if (!disconnect_owned_by_run) allocator.destroy(disconnect_failed);
    disconnect_failed.* = std.atomic.Value(bool).init(false);

    const tool_executor = try allocator.create(AgentToolBridge.Executor);
    var tool_executor_owned_by_run = false;
    errdefer if (!tool_executor_owned_by_run) allocator.destroy(tool_executor);
    tool_executor.* = .{
        .bridge = bridge,
        .session_id = session_id,
        .generation = generation,
        .disconnect_failed = disconnect_failed,
    };

    const session_id_text = try agent_types.sessionIdToString(session_id, allocator);
    defer allocator.free(session_id_text);

    const config = agent_loop.AgentLoopConfig{
        .model = prepared.model,
        .protocol = protocol_client,
        .tools = prepared.tools,
        .execute_tool_via_protocol_fn = AgentToolBridge.executeViaAgentProtocol,
        .execute_tool_via_protocol_ctx = tool_executor,
        .temperature = prepared.options.temperature,
        .max_tokens = prepared.options.max_tokens,
        .max_iterations = prepared.options.max_iterations,
        .thinking_level = prepared.options.thinking_level,
        .session_id = session_id_text,
        .api_key = prepared.options.api_key,
        .cancel_token = .{ .cancelled = cancel_flag },
    };

    const stream = try agent_loop.agentLoop(allocator, prepared.prompts, context, config);
    var stream_owned_by_run = false;
    errdefer if (!stream_owned_by_run) {
        _ = stream.deinitAndDestroy();
    };

    var run = Run{
        .session_id = session_id,
        .generation = generation,
        .stream = stream,
        .context = context,
        .model = prepared.model,
        .prompts = prepared.prompts,
        .tools = prepared.tools,
        .cancel_flag = cancel_flag,
        .disconnect_failed = disconnect_failed,
        .tool_executor = tool_executor,
    };
    prepared.options.deinit(allocator);
    context_owned_by_run = true;
    cancel_owned_by_run = true;
    disconnect_owned_by_run = true;
    tool_executor_owned_by_run = true;
    stream_owned_by_run = true;
    prepared.disarm();

    var appended = false;
    errdefer if (!appended) run.deinit(allocator);
    try driver.runs.append(allocator, run);
    appended = true;
}

pub fn deinitStdioAgentEvent(allocator: std.mem.Allocator, event: *agent_loop.AgentEvent) void {
    event.deinit(allocator);
}

pub fn serializeAgentLoopEvent(
    allocator: std.mem.Allocator,
    session_id: agent_types.SessionId,
    event: agent_loop.AgentEvent,
) ![]u8 {
    var buffer = std.ArrayList(u8).empty;
    errdefer buffer.deinit(allocator);
    var w = json_writer.JsonWriter.init(&buffer, allocator);

    try w.beginObject();
    switch (event) {
        .agent_start => {
            const session_text = try agent_types.sessionIdToString(session_id, allocator);
            defer allocator.free(session_text);
            try w.writeStringField("type", "agent_start");
            try w.writeStringField("session_id", session_text);
        },
        .run_failed => |payload| {
            try w.writeStringField("type", "run_failed");
            try w.writeStringField("reason", payload.reason.slice());
        },
        .agent_end => |payload| {
            try w.writeStringField("type", "agent_end");
            var terminal: ?ai_types.AssistantMessage = payload.final_message;
            if (terminal == null) {
                const messages = payload.messages.slice();
                var idx = messages.len;
                while (idx > 0) {
                    idx -= 1;
                    if (messages[idx] == .assistant) {
                        terminal = messages[idx].assistant;
                        break;
                    }
                }
            }
            const agent_stop_reason: ?[]const u8 = if (payload.termination) |termination|
                @tagName(termination)
            else if (terminal) |message|
                @tagName(message.stop_reason)
            else
                null;
            if (agent_stop_reason) |reason| {
                try w.writeStringField("stop_reason", reason);
            }
            if (terminal) |message| {
                if (message.provider.len > 0) {
                    try w.writeStringField("provider_id", message.provider);
                }
                if (message.api.len > 0) {
                    try w.writeStringField("api", message.api);
                }
                if (message.error_message.slice().len > 0) {
                    try w.writeStringField("error_message", message.error_message.slice());
                }
            }
        },
        .turn_start => {
            try w.writeStringField("type", "turn_start");
        },
        .turn_end => |payload| {
            try w.writeStringField("type", "turn_end");
            try w.writeStringField("stop_reason", @tagName(payload.message.stop_reason));
            if (payload.message.error_message.slice().len > 0) {
                try w.writeStringField("error_message", payload.message.error_message.slice());
            }
        },
        .message_start => |payload| {
            try w.writeStringField("type", "message_start");
            writeMessageMetadata(&w, payload.message) catch {};
        },
        .message_update => |payload| {
            const provider_event_json = try transport.serializeEvent(payload.event, allocator);
            defer allocator.free(provider_event_json);
            try w.writeStringField("type", "message_update");
            try w.writeKey("event");
            try w.writeRawJson(provider_event_json);
        },
        .message_end => |payload| {
            try w.writeStringField("type", "message_end");
            if (payload.message == .assistant) {
                try w.writeStringField("stop_reason", @tagName(payload.message.assistant.stop_reason));
                if (payload.message.assistant.error_message.slice().len > 0) {
                    try w.writeStringField("error_message", payload.message.assistant.error_message.slice());
                }
                try writeUsageField(&w, payload.message.assistant.usage);
            }
        },
        .context_usage => |payload| {
            try w.writeStringField("type", "context_usage");
            try w.writeIntField("system_prompt_bytes", payload.system_prompt_bytes);
            try w.writeIntField("message_bytes", payload.message_bytes);
            try w.writeIntField("tool_definition_bytes", payload.tool_definition_bytes);
            try w.writeIntField("total_bytes", payload.total_bytes);
            try w.writeIntField("estimated_tokens", payload.estimated_tokens);
            try w.writeIntField("message_count", payload.message_count);
            try w.writeIntField("tool_count", payload.tool_count);
        },
        .prompt_segment_usage => |payload| {
            try w.writeStringField("type", "prompt_segment_usage");
            try w.writeStringField("segment", @tagName(payload.segment));
            try w.writeStringField("cache_role", @tagName(payload.cache_role));
            try w.writeIntField("bytes", payload.bytes);
            try w.writeIntField("estimated_tokens", payload.estimated_tokens);
            try w.writeIntField("item_count", payload.item_count);
        },
        .tool_execution_start => |payload| {
            try w.writeStringField("type", "tool_execution_start");
            try w.writeStringField("tool_call_id", payload.tool_call_id);
            try w.writeStringField("tool_name", payload.tool_name);
            try w.writeStringField("args_json", payload.args_json);
        },
        .tool_execution_update => |payload| {
            try w.writeStringField("type", "tool_execution_update");
            try w.writeStringField("tool_call_id", payload.tool_call_id);
            try w.writeStringField("tool_name", payload.tool_name);
            try w.writeStringField("partial_result_json", payload.partial_result_json);
        },
        .tool_execution_end => |payload| {
            try w.writeStringField("type", "tool_execution_end");
            try w.writeStringField("tool_call_id", payload.tool_call_id);
            try w.writeStringField("tool_name", payload.tool_name);
            try w.writeStringField("result_json", payload.result_json);
            if (payload.content_json.len > 0) try w.writeStringField("content_json", payload.content_json);
            try w.writeBoolField("is_error", payload.is_error);
            try w.writeIntField("args_bytes", payload.args_bytes);
            try w.writeIntField("raw_result_bytes", payload.raw_result_bytes);
            try w.writeIntField("returned_result_bytes", payload.returned_result_bytes);
            try w.writeIntField("raw_details_bytes", payload.raw_details_bytes);
            try w.writeIntField("returned_details_bytes", payload.returned_details_bytes);
            try w.writeIntField("raw_total_bytes", payload.raw_total_bytes);
            try w.writeIntField("returned_total_bytes", payload.returned_total_bytes);
            try w.writeIntField("estimated_returned_tokens", payload.estimated_returned_tokens);
            try w.writeIntField("artifact_count", payload.artifact_count);
            try writeArtifactReferences(&w, payload.artifacts);
        },
    }
    try w.endObject();

    const out = try allocator.dupe(u8, buffer.items);
    buffer.deinit(allocator);
    return out;
}

fn writeArtifactReferences(w: *json_writer.JsonWriter, artifacts: []const ai_types.ArtifactReference) !void {
    if (artifacts.len == 0) return;
    try w.writeKey("artifacts");
    try w.beginArray();
    for (artifacts) |artifact| {
        try w.beginObject();
        try w.writeStringField("artifact_id", artifact.artifact_id);
        if (artifact.getUri()) |uri| try w.writeStringField("uri", uri);
        if (artifact.getMimeType()) |mime_type| try w.writeStringField("mime_type", mime_type);
        if (artifact.byte_size) |byte_size| try w.writeIntField("byte_size", byte_size);
        if (artifact.getSha256()) |sha256| try w.writeStringField("sha256", sha256);
        if (artifact.getDescription()) |description| try w.writeStringField("description", description);
        try w.endObject();
    }
    try w.endArray();
}

fn writeMessageMetadata(w: *json_writer.JsonWriter, message: ai_types.Message) !void {
    if (message != .assistant) return;
    try w.writeStringField("api", message.assistant.api);
    try w.writeStringField("provider", message.assistant.provider);
    try w.writeStringField("model", message.assistant.model);
}

fn writeUsageField(w: *json_writer.JsonWriter, usage: ai_types.Usage) !void {
    try w.writeKey("usage");
    try w.beginObject();
    try w.writeIntField("input", usage.input);
    try w.writeIntField("output", usage.output);
    try w.writeIntField("cache_read", usage.cache_read);
    try w.writeIntField("cache_write", usage.cache_write);
    try w.endObject();
}

pub fn publishRunFailurePair(
    server: *AgentProtocolServer,
    allocator: std.mem.Allocator,
    run: *Run,
    code: agent_types.AgentErrorCode,
    message: []const u8,
) !void {
    if (!run.failure_event_published) {
        const event_json = try serializeAgentErrorEvent(allocator, message, @tagName(code));
        defer allocator.free(event_json);
        server.publishAgentEvent(run.session_id, event_json) catch |err| switch (err) {
            error.OutOfMemory => return err,
            else => {},
        };
        run.failure_event_published = true;
    }
    server.publishAgentError(run.session_id, code, message) catch |err| switch (err) {
        error.OutOfMemory => return err,
        else => {},
    };
    run.settlement_frame_published = true;
}

pub fn dropTerminalProjection(allocator: std.mem.Allocator, run: *Run) void {
    if (run.terminal_event_json) |event_json| allocator.free(event_json);
    run.terminal_event_json = null;
}

pub fn publishAgentLoopError(
    server: *AgentProtocolServer,
    allocator: std.mem.Allocator,
    session_id: SessionId,
    code: agent_types.AgentErrorCode,
    message: []const u8,
) !void {
    const event_json = try serializeAgentErrorEvent(allocator, message, @tagName(code));
    defer allocator.free(event_json);
    server.publishAgentEvent(session_id, event_json) catch |err| {
        if (err == error.OutOfMemory) return err;
    };
    server.publishAgentError(session_id, code, message) catch |err| {
        if (err == error.OutOfMemory) return err;
    };
}

pub fn publishPendingToolRequests(
    server: *AgentProtocolServer,
    allocator: std.mem.Allocator,
    bridge: *AgentToolBridge.Bridge,
) !usize {
    var published: usize = 0;
    while (true) {
        const request = bridge.peekFrontRequest() orelse break;

        const registration_current = blk: {
            const current = server.sessionGeneration(request.session_id);
            break :blk current != null and current.? == request.generation;
        };
        if (!registration_current) {
            bridge.popFrontRequest(allocator);
            continue;
        }
        if (bridge.isDisconnected()) {
            bridge.popFrontRequest(allocator);
            continue;
        }

        var payload_owned_by_env = false;
        const owned_tool_call_id = try allocator.dupe(u8, request.tool_call_id);
        errdefer if (!payload_owned_by_env) allocator.free(owned_tool_call_id);
        const owned_tool_name = try allocator.dupe(u8, request.tool_name);
        errdefer if (!payload_owned_by_env) allocator.free(owned_tool_name);
        const owned_args_json = try allocator.dupe(u8, request.args_json);
        errdefer if (!payload_owned_by_env) allocator.free(owned_args_json);

        var env = agent_types.Envelope{
            .session_id = request.session_id,
            .message_id = agent_types.generateUlid(),
            .sequence = try server.nextOutgoingSequence(request.session_id),
            .timestamp = compat.time.nowMillis(),
            .payload = .{ .tool_execute = .{
                .tool_call_id = owned_tool_call_id,
                .tool_name = owned_tool_name,
                .args_json = owned_args_json,
            } },
        };
        errdefer env.deinit(allocator);
        payload_owned_by_env = true;

        try bridge.markInFlight(allocator, request.session_id, request.tool_call_id, env.message_id, request.generation);
        errdefer bridge.discardInFlight(allocator, request.session_id, request.tool_call_id);
        try server.enqueueEnvelope(env);
        bridge.popFrontRequest(allocator);
        published += 1;
    }
    return published;
}

fn serializeAgentErrorEvent(allocator: std.mem.Allocator, message: []const u8, code: []const u8) ![]u8 {
    var buffer = std.ArrayList(u8).empty;
    errdefer buffer.deinit(allocator);
    var w = json_writer.JsonWriter.init(&buffer, allocator);
    try w.beginObject();
    try w.writeStringField("type", "error");
    try w.writeStringField("message", message);
    try w.writeStringField("code", code);
    try w.endObject();
    const out = try allocator.dupe(u8, buffer.items);
    buffer.deinit(allocator);
    return out;
}

pub fn pump(allocator: std.mem.Allocator, driver: *Driver, server: *AgentProtocolServer) !usize {
    var forwarded: usize = 0;
    var idx: usize = 0;
    while (idx < driver.runs.items.len) {
        var run = &driver.runs.items[idx];

        const registration_current = blk: {
            const generation = server.sessionGeneration(run.session_id);
            break :blk generation != null and generation.? == run.generation;
        };
        if (!registration_current) run.cancel();

        while (run.stream.poll()) |event| {
            var owned_event = event;
            errdefer deinitStdioAgentEvent(allocator, &owned_event);

            if (!registration_current) {
                deinitStdioAgentEvent(allocator, &owned_event);
                continue;
            }

            if (std.meta.activeTag(event) == .agent_end) {
                run.terminal_event_json = serializeAgentLoopEvent(allocator, run.session_id, event) catch |err| {
                    run.event_publication_failed = true;
                    return err;
                };
                deinitStdioAgentEvent(allocator, &owned_event);
                continue;
            }

            const event_json = serializeAgentLoopEvent(allocator, run.session_id, event) catch |err| {
                run.event_publication_failed = true;
                return err;
            };
            defer allocator.free(event_json);
            server.publishAgentEvent(run.session_id, event_json) catch |err| {
                run.event_publication_failed = true;
                if (err == error.OutOfMemory) return err;
            };
            deinitStdioAgentEvent(allocator, &owned_event);
            forwarded += 1;
        }

        if (!run.stream.isDone()) {
            idx += 1;
            continue;
        }

        if (registration_current) {
            if (!run.settlement_frame_published) {
                if (run.disconnect_failed.load(.acquire)) {
                    dropTerminalProjection(allocator, run);
                    try publishRunFailurePair(server, allocator, run, .tool_execution_error, STDIO_DISCONNECT_TOOL_WAIT_MESSAGE);
                    forwarded += 1;
                } else if (run.stream.getError()) |msg| {
                    dropTerminalProjection(allocator, run);
                    try publishRunFailurePair(server, allocator, run, .internal_error, msg);
                    forwarded += 1;
                } else if (run.event_publication_failed) {
                    dropTerminalProjection(allocator, run);
                    try publishRunFailurePair(server, allocator, run, .internal_error, STDIO_EVENT_PUBLICATION_FAILED_MESSAGE);
                    forwarded += 1;
                } else if (run.stream.getResult()) |result| {
                    const result_reason: []const u8 = if (result.termination) |termination|
                        @tagName(termination)
                    else
                        @tagName(result.final_message.stop_reason);
                    const result_json = try transport.serializeResultWithStopReason(result.final_message, result_reason, allocator);
                    defer allocator.free(result_json);
                    server.publishAgentResult(run.session_id, result_json) catch |err| switch (err) {
                        error.OutOfMemory => return err,
                        else => {},
                    };
                    run.settlement_frame_published = true;
                    forwarded += 1;
                } else {
                    dropTerminalProjection(allocator, run);
                    try publishRunFailurePair(server, allocator, run, .internal_error, STDIO_RUN_WITHOUT_OUTCOME_MESSAGE);
                    forwarded += 1;
                }
            }

            if (run.settlement_frame_published) {
                if (run.terminal_event_json) |event_json| {
                    server.publishAgentEvent(run.session_id, event_json) catch |err| switch (err) {
                        error.OutOfMemory => return err,
                        else => {},
                    };
                    allocator.free(event_json);
                    run.terminal_event_json = null;
                    forwarded += 1;
                }
                var removed = driver.runs.orderedRemove(idx);
                removed.deinit(allocator);
                continue;
            }
        } else {
            var removed = driver.runs.orderedRemove(idx);
            removed.deinit(allocator);
            continue;
        }

        idx += 1;
    }
    return forwarded;
}
