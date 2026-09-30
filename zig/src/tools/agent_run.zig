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
