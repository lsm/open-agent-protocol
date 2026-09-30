const std = @import("std");
const agent_types = @import("agent_types");
const ai_types = @import("ai_types");
const agent_loop = @import("agent_loop");
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

pub fn hasRunForGeneration(table: *const std.ArrayList(Run), session_id: SessionId, generation: u64) bool {
    for (table.items) |run| {
        if (run.settlement_frame_published) continue;
        if (run.generation == generation and std.mem.eql(u8, run.session_id[0..], session_id[0..])) return true;
    }
    return false;
}

pub fn cancelRun(
    allocator: std.mem.Allocator,
    table: *std.ArrayList(Run),
    bridge: *AgentToolBridge.Bridge,
    session_id: SessionId,
) void {
    for (table.items) |*run| {
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
    table: *std.ArrayList(Run),
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
    try table.append(allocator, run);
    appended = true;
}
