const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const event_stream = @import("event_stream");
const agent = @import("agent");
const agent_types = @import("agent_types");
const transport = @import("transport");
const json_writer = @import("json_writer");
const session = @import("tui_session");
const local_tools = @import("tools/registry");
const tool_local_runtime = @import("tool_local_runtime");
const permission = @import("permission");
const OwnedSlice = @import("owned_slice").OwnedSlice;
const model_catalog = @import("model_catalog");
const json_encode = @import("json_encode");

pub const TuiSession = session.TuiSession;
pub const TuiEvent = session.TuiEvent;
pub const TuiEventStream = session.TuiEventStream;
pub const TuiEndReason = session.TuiEndReason;
pub const QueuedCounts = session.QueuedCounts;
pub const CompactOptions = session.CompactOptions;
pub const ToolApprovalCallback = session.ToolApprovalCallback;
pub const ToolApprovalDecision = session.ToolApprovalDecision;
pub const ToolApprovalRequest = session.ToolApprovalRequest;

const output_limit_warning = "The model's reply hit its output token limit, so the run stopped. Send a message to continue.";

const ApprovalDecisionState = struct {
    tool_call_id: []u8 = &.{},
    decision: ?ToolApprovalDecision = null,
    cancelled: bool = false,
};

const ApprovalContext = struct {
    runtime: *TuiRuntime,
    callback_ctx: ?*anyopaque,
    callback: ?ToolApprovalCallback,
    original_ctx: ?*anyopaque,
    original_callback: ?agent.ToolApprovalFn,
    original_ui_ctx: ?*anyopaque,
    original_ui_callback: ?agent.ToolApprovalUiFn,
    tool_name: []const u8,
};

const CompactionEnd = @TypeOf(@as(TuiEvent, undefined).compaction_end);

pub const PermissionMode = enum {
    ask,
    bypass,
};

pub const EventSink = struct {
    ctx: *anyopaque,
    push: *const fn (ctx: *anyopaque, event: TuiEvent) void,
};

pub const RemoteSettings = struct {
    model: ?ai_types.Model,
    thinking_level: ai_types.ThinkingLevel,
    context_window: ?u32,
    output: agent.OutputSetting,
    permission_mode: PermissionMode,
    workspace_root: []const u8,
    resume_session_id: ?[]const u8 = null,
};

pub const RemoteExecution = struct {
    ctx: *anyopaque,
    vtable: *const VTable,

    pub const VTable = struct {
        start: *const fn (ctx: *anyopaque, sink: EventSink, settings: RemoteSettings) anyerror!void,
        submit: *const fn (ctx: *anyopaque, text: []const u8) anyerror!void,
        cancel: *const fn (ctx: *anyopaque) void,
        switch_model: *const fn (ctx: *anyopaque, model: ai_types.Model) anyerror!void,
        set_reasoning: *const fn (ctx: *anyopaque, level: ai_types.ThinkingLevel) anyerror!void,
        compacts: *const fn (ctx: *anyopaque) bool,
        compact: *const fn (ctx: *anyopaque, focus: []const u8) anyerror!void,
        set_compaction_policy: *const fn (ctx: *anyopaque, policy_json: []const u8) anyerror!void,
        decide_approval: *const fn (ctx: *anyopaque, tool_call_id: []const u8, granted: bool) anyerror!void,
        follow_up: *const fn (ctx: *anyopaque, text: []const u8) anyerror!void,
        steer: *const fn (ctx: *anyopaque, text: []const u8) anyerror!void,
        clear_queued: *const fn (ctx: *anyopaque) void,
        queued: *const fn (ctx: *anyopaque) usize,
        steers_pending: *const fn (ctx: *anyopaque) usize,
        steers_settled: *const fn (ctx: *anyopaque) u64,
        stop: *const fn (ctx: *anyopaque) void,
    };
};

pub const TuiRuntimeOptions = struct {
    protocol: ?agent.ProtocolClient = null,
    models: []const ai_types.Model = &.{},
    initial_model_id: ?[]const u8 = null,
    initial_model: ?InitialModelRef = null,
    tools: []const agent.AgentTool = &.{},
    mcp_config_json: ?[]const u8 = null,
    permission_engine: ?*permission.PermissionEngine = null,
    workspace_root: []const u8 = "",
    tool_approval_ctx: ?*anyopaque = null,
    tool_approval_callback: ?ToolApprovalCallback = null,
    permission_mode: PermissionMode = .bypass,
    thinking_level: ai_types.ThinkingLevel = .low,
    compact_output: bool = false,
    auto_worktree: bool = false,
    run_async: bool = true,
    generate_titles: bool = false,
    context_window: ?u32 = null,
    output: agent.OutputSetting = .auto,
    remote: ?RemoteExecution = null,
};

pub const ContextWindowError = error{
    NotATokenCount,
    AboveMaximum,
};

pub fn parseContextWindow(text: []const u8) error{NotATokenCount}!u32 {
    const trimmed = std.mem.trim(u8, text, " \t\r\n");
    var digits: usize = 0;
    while (digits < trimmed.len and std.ascii.isDigit(trimmed[digits])) : (digits += 1) {}
    if (digits == 0) return error.NotATokenCount;
    const suffix = trimmed[digits..];
    const scale: u64 = if (suffix.len == 0) 1 else if (std.ascii.eqlIgnoreCase(suffix, "k")) 1_000 else if (std.ascii.eqlIgnoreCase(suffix, "m")) 1_000_000 else return error.NotATokenCount;
    const count = std.fmt.parseInt(u64, trimmed[0..digits], 10) catch return error.NotATokenCount;
    const window = std.math.mul(u64, count, scale) catch return error.NotATokenCount;
    if (window == 0 or window > std.math.maxInt(u32)) return error.NotATokenCount;
    return @intCast(window);
}

pub const InitialModelRef = struct {
    id: []const u8,
    provider: []const u8 = "",
    api: []const u8 = "",
};

fn normalizeTuiThinkingLevel(level: ai_types.ThinkingLevel) ai_types.ThinkingLevel {
    return switch (level) {
        .minimal => .low,
        else => level,
    };
}

const title_request_text = "Write a short title, at most six words, for a conversation that starts with the message below. Reply with the title only.";
const title_prompt_message_bytes = 4096;
const title_max_bytes = 80;

fn titlePrompt(allocator: std.mem.Allocator, first_message: []const u8) ![]u8 {
    const message = first_message[0..utf8Prefix(first_message, title_prompt_message_bytes)];
    return std.fmt.allocPrint(allocator, "{s}\n\n<message>\n{s}\n</message>", .{ title_request_text, message });
}

fn utf8Prefix(text: []const u8, max_bytes: usize) usize {
    if (text.len <= max_bytes) return text.len;
    var end = max_bytes;
    while (end > 0 and (text[end] & 0xC0) == 0x80) end -= 1;
    return end;
}

pub fn cleanTitle(allocator: std.mem.Allocator, reply: []const u8) !?[]u8 {
    var lines = std.mem.tokenizeAny(u8, reply, "\r\n");
    while (lines.next()) |line| {
        var title = std.mem.trim(u8, line, " \t\r\"'`*#");
        title = std.mem.trimEnd(u8, title, ". ");
        if (title.len == 0) continue;
        return try allocator.dupe(u8, title[0..utf8Prefix(title, title_max_bytes)]);
    }
    return null;
}

fn requestTitleText(allocator: std.mem.Allocator, protocol: agent.ProtocolClient, model: ai_types.Model, prompt: []const u8, cancel: ai_types.CancelToken) !?[]u8 {
    const message = ai_types.Message{ .user = .{ .content = .{ .text = prompt }, .timestamp = compat.time.nowMillis() } };
    const stream = try protocol.stream(model, .{ .messages = &.{message} }, .{
        .cancel_token = cancel,
        .thinking_level = .off,
        .max_tokens = 1024,
    }, allocator);
    defer _ = stream.deinitAndDestroy();

    var reply: ?ai_types.AssistantMessage = null;
    defer if (reply) |*finished| finished.deinit(allocator);
    while (stream.wait()) |event| {
        var owned_event = event;
        switch (owned_event) {
            .done => |done| {
                if (reply) |*previous| previous.deinit(allocator);
                reply = done.message;
                continue;
            },
            .@"error" => |failed| {
                if (reply) |*previous| previous.deinit(allocator);
                reply = failed.err;
                continue;
            },
            else => {},
        }
        if (stream.ownership.isOwned()) ai_types.deinitAssistantMessageEvent(allocator, &owned_event);
    }
    if (reply == null) reply = try stream.cloneResult(allocator);
    const final = reply orelse return null;
    if (final.stop_reason != .stop) return null;
    var text: std.ArrayList(u8) = .empty;
    defer text.deinit(allocator);
    for (final.content) |block| switch (block) {
        .text => |part| try text.appendSlice(allocator, part.text),
        else => {},
    };
    return cleanTitle(allocator, text.items);
}

fn cloneModels(allocator: std.mem.Allocator, models: []const ai_types.Model) ![]ai_types.Model {
    const cloned = try allocator.alloc(ai_types.Model, models.len);
    var initialized: usize = 0;
    errdefer {
        for (cloned[0..initialized]) |*model| model.deinit(allocator);
        allocator.free(cloned);
    }
    for (models, 0..) |model, idx| {
        cloned[idx] = try ai_types.cloneModel(allocator, model);
        initialized += 1;
    }
    return cloned;
}

fn deinitModels(allocator: std.mem.Allocator, models: []ai_types.Model) void {
    for (models) |*model| model.deinit(allocator);
    allocator.free(models);
}

pub const TuiRuntime = struct {
    allocator: std.mem.Allocator,
    protocol: ?agent.ProtocolClient,
    models: []ai_types.Model,
    selected_model_index: ?usize,
    pending_model_index: ?usize = null,
    local_agent: ?agent.Agent = null,
    event_stream: TuiEventStream,
    tool_registry: local_tools.ToolRegistry,
    mcp_bridge: ?*local_tools.mcp_bridge.McpBridge = null,
    original_tools: []agent.AgentTool,
    wrapped_tools: []agent.AgentTool,
    approval_contexts: []ApprovalContext,
    tool_protocol: tool_local_runtime.LocalToolProtocol,
    workspace_root: []u8,
    session_cwd: []u8,
    tool_protocol_override_fn: ?agent_types.ToolProtocolExecuteFn = null,
    tool_protocol_override_ctx: ?*anyopaque = null,
    pending_approval: ApprovalDecisionState = .{},
    approval_mutex: std.atomic.Mutex = .unlocked,
    tool_approval_ctx: ?*anyopaque,
    tool_approval_callback: ?ToolApprovalCallback,
    permission_engine: ?*permission.PermissionEngine,
    permission_mode: PermissionMode = .bypass,
    thinking_level: ai_types.ThinkingLevel = .low,
    output: agent.OutputSetting = .auto,
    context_window: ?u32 = null,
    suspended_context_window: ?u32 = null,
    context_window_refused: ?u32 = null,
    cancelled: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    completed: bool = false,
    started: bool = false,
    stream_active: bool = false,
    last_turn_stop_reason: ?ai_types.StopReason = null,
    compact_output: bool = false,
    run_async: bool = true,
    compaction_transcript: []u8 = &.{},
    session_id: []u8 = &.{},
    transcript_writer: ?TranscriptWriter = null,
    run_transcripts: std.ArrayList([]u8) = .empty,
    run_transcript_saved: []const u8 = "",
    semantic_wait_ms: i64 = 2_000,
    dropped_event_count: u64 = 0,
    dropped_since_warning: u64 = 0,
    current_generation: u32 = 0,
    backpressure_active: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    backpressure_status_active_emitted: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    backpressure_mutex: std.atomic.Mutex = .unlocked,
    generate_titles: bool = false,
    title_thread: ?std.Thread = null,
    title_cancel: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    title_mutex: std.atomic.Mutex = .unlocked,
    title_result: ?[]u8 = null,
    remote: ?RemoteExecution = null,
    remote_steers_started: u64 = 0,
    remote_mutex: std.atomic.Mutex = .unlocked,

    pub fn init(allocator: std.mem.Allocator, options: TuiRuntimeOptions) !TuiRuntime {
        var models = try cloneModels(allocator, options.models);
        errdefer deinitModels(allocator, models);

        var tool_registry = local_tools.ToolRegistry.init();
        errdefer tool_registry.deinit(allocator);
        try tool_registry.registerDefaults(allocator);

        for (options.tools) |tool| try tool_registry.replaceOrRegister(allocator, tool);

        var original_tools = try allocator.dupe(agent.AgentTool, tool_registry.list());
        errdefer allocator.free(original_tools);

        var wrapped_tools = try allocator.alloc(agent.AgentTool, original_tools.len);
        errdefer allocator.free(wrapped_tools);

        var approval_contexts = try allocator.alloc(ApprovalContext, original_tools.len);
        errdefer allocator.free(approval_contexts);

        var selected: ?usize = null;
        if (models.len > 0) {
            selected = 0;
            if (options.initial_model) |initial| {
                const moved: InitialModelRef = .{ .id = initial.id, .provider = initial.provider };
                selected = firstMatch(models, initial) orelse firstMatch(models, moved) orelse 0;
            } else if (options.initial_model_id) |id| {
                for (models, 0..) |model, i| {
                    if (std.mem.eql(u8, model.id, id)) {
                        selected = i;
                        break;
                    }
                }
            }
        }

        var tool_protocol = try tool_local_runtime.LocalToolProtocol.init(allocator, original_tools);
        errdefer tool_protocol.deinit();

        var workspace_root = try allocator.dupe(u8, options.workspace_root);
        errdefer allocator.free(workspace_root);
        var session_cwd = try allocator.dupe(u8, options.workspace_root);
        errdefer allocator.free(session_cwd);

        var runtime = TuiRuntime{
            .allocator = allocator,
            .protocol = options.protocol,
            .models = models,
            .selected_model_index = selected,
            .event_stream = TuiEventStream.init(allocator),
            .tool_registry = tool_registry,
            .mcp_bridge = null,
            .original_tools = original_tools,
            .wrapped_tools = wrapped_tools,
            .approval_contexts = approval_contexts,
            .tool_protocol = tool_protocol,
            .workspace_root = workspace_root,
            .session_cwd = session_cwd,
            .tool_approval_ctx = options.tool_approval_ctx,
            .tool_approval_callback = options.tool_approval_callback,
            .permission_engine = options.permission_engine,
            .permission_mode = options.permission_mode,
            .thinking_level = normalizeTuiThinkingLevel(options.thinking_level),
            .context_window = options.context_window,
            .output = options.output,
            .compact_output = options.compact_output,
            .run_async = options.run_async,
            .generate_titles = options.generate_titles,
            .remote = options.remote,
        };
        original_tools = &.{};
        wrapped_tools = &.{};
        models = &.{};
        tool_protocol = undefined;
        workspace_root = &.{};
        session_cwd = &.{};
        tool_registry = local_tools.ToolRegistry.init();
        approval_contexts = &.{};
        errdefer runtime.deinit();
        if (options.mcp_config_json) |config_json| {
            const bridge = try allocator.create(local_tools.mcp_bridge.McpBridge);
            bridge.* = local_tools.mcp_bridge.McpBridge.init(allocator);
            bridge.bind();
            runtime.mcp_bridge = bridge;
            try bridge.loadConfigJson(config_json);
            try bridge.discover();
            try runtime.tool_registry.registerMcpBridge(allocator, bridge);
            const next_original_tools = try allocator.dupe(agent.AgentTool, runtime.tool_registry.list());
            errdefer allocator.free(next_original_tools);
            const next_wrapped_tools = try allocator.alloc(agent.AgentTool, next_original_tools.len);
            errdefer allocator.free(next_wrapped_tools);
            const next_approval_contexts = try allocator.alloc(ApprovalContext, next_original_tools.len);
            errdefer allocator.free(next_approval_contexts);

            allocator.free(runtime.approval_contexts);
            allocator.free(runtime.wrapped_tools);
            allocator.free(runtime.original_tools);
            runtime.original_tools = next_original_tools;
            runtime.wrapped_tools = next_wrapped_tools;
            runtime.approval_contexts = next_approval_contexts;
        }
        runtime.suspendContextWindowAboveCeiling();
        if (runtime.permission_engine) |engine| engine.setBypassAll(runtime.permission_mode == .bypass);
        runtime.rebuildWrappedTools();
        return runtime;
    }

    fn firstMatch(models: []const ai_types.Model, initial: InitialModelRef) ?usize {
        for (models, 0..) |model, i| {
            if (modelMatchesInitial(model, initial)) return i;
        }
        return null;
    }

    fn modelMatchesInitial(model: ai_types.Model, initial: InitialModelRef) bool {
        if (!std.mem.eql(u8, model.id, initial.id)) return false;
        if (initial.provider.len > 0 and !std.mem.eql(u8, model.provider, initial.provider)) return false;
        if (initial.api.len > 0 and !std.mem.eql(u8, model.api, initial.api)) return false;
        return true;
    }

    pub fn deinit(self: *TuiRuntime) void {
        self.title_cancel.store(true, .release);
        self.waitForTitleRequest();
        if (self.title_result) |title| self.allocator.free(title);
        self.stop();
        self.event_stream.deinit();
        self.clearPendingApproval();
        self.tool_protocol.deinit();
        self.allocator.free(self.workspace_root);
        self.allocator.free(self.session_cwd);
        self.allocator.free(self.compaction_transcript);
        self.allocator.free(self.session_id);
        self.clearRunTranscripts();
        self.run_transcripts.deinit(self.allocator);
        self.allocator.free(self.approval_contexts);
        self.allocator.free(self.wrapped_tools);
        self.allocator.free(self.original_tools);
        if (self.mcp_bridge) |bridge| {
            bridge.deinit();
            self.allocator.destroy(bridge);
        }
        self.tool_registry.deinit(self.allocator);
        deinitModels(self.allocator, self.models);
        self.* = undefined;
    }

    pub fn start(self: *TuiRuntime) !void {
        if (self.started) return;
        if (self.remote) |remote| {
            try remote.vtable.start(remote.ctx, .{ .ctx = self, .push = pushRemote }, self.remoteSettings(null));
            self.started = true;
            return;
        }
        const protocol = self.protocol orelse return error.NoProtocolConfigured;
        self.rebuildWrappedTools();
        self.local_agent = agent.Agent.init(self.allocator, .{
            .protocol = protocol,
            .compact_tool_output = self.compact_output,
            .permission_engine = self.permission_engine,
            .execute_tool_via_protocol_fn = executeTuiToolProtocol,
            .execute_tool_via_protocol_ctx = self,
            .rewrite_tool_args_fn = rewriteToolArgs,
            .rewrite_tool_args_ctx = self,
        });
        self.local_agent.?.subscribeWithContext(self, onAgentEvent);
        self.local_agent.?.setCompactToolOutput(self.compact_output);
        const system_prompt = try self.workspaceSystemPrompt();
        defer self.allocator.free(system_prompt);
        try self.local_agent.?.setSystemPrompt(system_prompt);
        if (self.selected_model_index) |idx| self.local_agent.?.setModel(self.effectiveModel(self.models[idx]));
        self.local_agent.?.setThinkingLevel(self.thinking_level);
        try self.applySessionId();
        self.local_agent.?.setOutput(self.output);
        self.tool_protocol.server.tools.clearRetainingCapacity();
        try self.tool_protocol.server.registerTools(self.wrapped_tools);
        self.local_agent.?.setTools(self.wrapped_tools);
        self.started = true;
    }

    pub fn stop(self: *TuiRuntime) void {
        if (self.remote) |remote| {
            if (self.started) remote.vtable.stop(remote.ctx);
            self.started = false;
            return;
        }
        if (self.local_agent) |*local| {
            if (!local.isIdle()) {
                local.abort();
                local.waitForIdle();
            }
            local.unsubscribeWithContext(self, onAgentEvent);
            local.deinit();
            self.local_agent = null;
        }
        self.started = false;
    }

    pub fn isIdle(self: *TuiRuntime) bool {
        if (self.stream_active) return false;
        if (self.remote != null) return true;
        if (self.local_agent) |*local| return local.isIdle();
        return true;
    }

    pub fn canSteer(_: *const TuiRuntime) bool {
        return true;
    }

    pub fn createSession(self: *TuiRuntime) TuiSession {
        return .{
            .ctx = self,
            .ops = .{
                .start = sessionStart,
                .resume_session = sessionResume,
                .compact = sessionCompact,
                .history = sessionHistory,
                .cancel = sessionCancel,
                .submit_turn = sessionSubmitTurn,
                .steer = sessionSteer,
                .follow_up = sessionFollowUp,
                .clear_queued_messages = sessionClearQueuedMessages,
                .queued_counts = sessionQueuedCounts,
                .steers_consumed = sessionSteersConsumed,
                .can_steer = sessionCanSteer,
                .switch_model = sessionSwitchModel,
                .switch_model_exact = sessionSwitchModelExact,
                .current_model = sessionCurrentModel,
                .decide_tool_approval = sessionDecideToolApproval,
                .stream_events = sessionStreamEvents,
                .request_compaction = sessionRequestCompaction,
                .take_compaction_request = sessionTakeCompactionRequest,
            },
        };
    }

    pub fn availableModels(self: *TuiRuntime) []const ai_types.Model {
        return self.models;
    }

    pub fn replaceModels(self: *TuiRuntime, next_models: []const ai_types.Model, preferred_model: ?ai_types.Model) !void {
        if (self.local_agent) |*local| {
            if (!local.isIdle()) return error.AgentAlreadyStreaming;
        }

        var owned_next = try cloneModels(self.allocator, next_models);
        errdefer deinitModels(self.allocator, owned_next);

        const active_model = preferred_model orelse if (self.selected_model_index) |idx| self.models[idx] else null;

        var next_selected: ?usize = if (owned_next.len > 0) 0 else null;
        if (active_model) |active| {
            for (owned_next, 0..) |model, idx| {
                if (std.mem.eql(u8, model.id, active.id) and
                    std.mem.eql(u8, model.provider, active.provider) and
                    std.mem.eql(u8, model.api, active.api))
                {
                    next_selected = idx;
                    break;
                }
            }
        }

        var next_pending: ?usize = null;
        if (self.pending_model_index) |pending| {
            if (pending < self.models.len) {
                const target = self.models[pending];
                for (owned_next, 0..) |model, idx| {
                    if (std.mem.eql(u8, model.id, target.id) and std.mem.eql(u8, model.provider, target.provider) and std.mem.eql(u8, model.api, target.api)) {
                        next_pending = idx;
                        break;
                    }
                }
            }
        }
        if (self.remote) |remote| {
            if (self.stream_active) return error.AgentAlreadyStreaming;
            if (self.started) {
                const before = if (self.selected_model_index) |idx| self.models[idx] else null;
                const after = if (next_selected) |idx| owned_next[idx] else null;
                if (after) |chosen| {
                    const unchanged = if (before) |held| std.mem.eql(u8, held.id, chosen.id) and std.mem.eql(u8, held.provider, chosen.provider) and std.mem.eql(u8, held.api, chosen.api) else false;
                    if (!unchanged) try remote.vtable.switch_model(remote.ctx, chosen);
                }
            }
        }
        if (self.local_agent) |*local| local.requestModelSwitch(null);
        deinitModels(self.allocator, self.models);
        self.models = owned_next;
        owned_next = &.{};
        self.selected_model_index = next_selected;
        self.pending_model_index = next_pending;
        if (next_pending) |idx| {
            if (self.local_agent) |*local| local.requestModelSwitch(self.effectiveModel(self.models[idx]));
        }
        self.reconcileContextWindowAfterModelSwitch();

        if (self.local_agent) |*local| {
            if (next_selected) |idx| local.setModel(self.effectiveModel(self.models[idx]));
        }
    }

    pub fn requestTitle(self: *TuiRuntime, first_message: []const u8) !bool {
        if (!self.generate_titles or self.title_thread != null) return false;
        const protocol = self.protocol orelse return false;
        const selected = self.currentModel() orelse return false;
        var model = try ai_types.cloneModel(self.allocator, selected);
        errdefer model.deinit(self.allocator);
        const prompt = try titlePrompt(self.allocator, first_message);
        errdefer self.allocator.free(prompt);
        self.title_cancel.store(false, .release);
        self.title_thread = try std.Thread.spawn(.{}, titleThread, .{ self, protocol, model, prompt });
        return true;
    }

    pub fn waitForTitleRequest(self: *TuiRuntime) void {
        const thread = self.title_thread orelse return;
        thread.join();
        self.title_thread = null;
    }

    pub fn takeGeneratedTitle(self: *TuiRuntime) ?[]u8 {
        while (!self.title_mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.title_mutex.unlock();
        const title = self.title_result orelse return null;
        self.title_result = null;
        return title;
    }

    fn titleThread(self: *TuiRuntime, protocol: agent.ProtocolClient, model: ai_types.Model, prompt: []u8) void {
        var owned_model = model;
        defer owned_model.deinit(self.allocator);
        defer self.allocator.free(prompt);
        const title = (requestTitleText(self.allocator, protocol, model, prompt, .{ .cancelled = &self.title_cancel }) catch return) orelse return;
        while (!self.title_mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.title_mutex.unlock();
        if (self.title_result) |previous| self.allocator.free(previous);
        self.title_result = title;
    }

    pub fn currentModel(self: *TuiRuntime) ?ai_types.Model {
        if (self.selected_model_index) |idx| return self.effectiveModel(self.models[idx]);
        return null;
    }

    fn effectiveModel(self: *const TuiRuntime, model: ai_types.Model) ai_types.Model {
        var effective = model;
        if (self.context_window) |window| effective.context_window = window;
        return effective;
    }

    pub fn contextWindow(self: *const TuiRuntime) u64 {
        if (self.context_window) |window| return window;
        if (self.selected_model_index) |idx| return self.models[idx].context_window;
        return 0;
    }

    pub fn contextWindowMaximum(self: *const TuiRuntime) ?u32 {
        const index = self.selected_model_index orelse return null;
        return model_catalog.contextWindowMaximum(self.models[index]);
    }

    pub fn contextWindowIsReported(self: *const TuiRuntime) bool {
        const index = self.selected_model_index orelse return false;
        return model_catalog.contextWindowIsReported(self.models[index]);
    }

    fn remoteSettings(self: *TuiRuntime, resume_session_id: ?[]const u8) RemoteSettings {
        return .{
            .model = self.currentModel(),
            .thinking_level = self.thinking_level,
            .context_window = self.context_window,
            .output = self.output,
            .permission_mode = self.permission_mode,
            .workspace_root = self.workspace_root,
            .resume_session_id = resume_session_id,
        };
    }

    pub fn reopenSaved(self: *TuiRuntime, session_id: []const u8) !void {
        const remote = self.remote orelse return error.UnavailableOverOap;
        if (self.stream_active) return error.AgentAlreadyStreaming;
        if (self.started) {
            remote.vtable.stop(remote.ctx);
            self.started = false;
        }
        remote.vtable.start(remote.ctx, .{ .ctx = self, .push = pushRemote }, self.remoteSettings(session_id)) catch |err| {
            self.started = err == error.OapReopenRefused;
            return err;
        };
        self.started = true;
    }

    fn settingsFixedOverOap(self: *const TuiRuntime) bool {
        return self.remote != null and self.started;
    }

    pub fn setContextWindow(self: *TuiRuntime, window: ?u32) error{ AboveMaximum, AgentAlreadyStreaming, UnavailableOverOap }!void {
        if (self.settingsFixedOverOap()) return error.UnavailableOverOap;
        if (self.local_agent) |*local| {
            if (!local.isIdle()) return error.AgentAlreadyStreaming;
        }
        if (window) |held| {
            if (self.contextWindowMaximum()) |ceiling| {
                if (held > ceiling) return error.AboveMaximum;
            }
        }
        self.context_window = window;
        self.suspended_context_window = null;
        self.context_window_refused = null;
        self.applyContextWindowToAgent();
    }

    pub fn contextWindowOverride(self: *const TuiRuntime) ?u32 {
        return self.context_window;
    }

    pub fn contextWindowRefused(self: *const TuiRuntime) ?u32 {
        return self.context_window_refused;
    }

    pub fn takeContextWindowRefused(self: *TuiRuntime) ?u32 {
        const refused = self.context_window_refused;
        self.context_window_refused = null;
        return refused;
    }

    fn applyContextWindowToAgent(self: *TuiRuntime) void {
        if (self.selected_model_index) |idx| {
            if (self.local_agent) |*local| local.setModel(self.effectiveModel(self.models[idx]));
        }
    }

    fn reconcileContextWindowAfterModelSwitch(self: *TuiRuntime) void {
        if (self.context_window) |held| {
            const index = self.selected_model_index orelse return;
            const ceiling = model_catalog.contextWindowMaximum(self.models[index]) orelse return;
            if (held > ceiling) {
                self.suspended_context_window = held;
                self.context_window = null;
                self.context_window_refused = held;
                return;
            }
        }
        if (self.context_window == null) {
            const held = self.suspended_context_window orelse return;
            const index = self.selected_model_index orelse return;
            if (model_catalog.contextWindowMaximum(self.models[index])) |ceiling| {
                if (held > ceiling) return;
            }
            self.context_window = held;
            self.suspended_context_window = null;
            self.context_window_refused = null;
        }
    }

    fn suspendContextWindowAboveCeiling(self: *TuiRuntime) void {
        const held = self.context_window orelse return;
        const index = self.selected_model_index orelse return;
        const ceiling = model_catalog.contextWindowMaximum(self.models[index]) orelse return;
        if (held <= ceiling) return;
        self.suspended_context_window = held;
        self.context_window = null;
        self.context_window_refused = held;
    }

    pub fn availableTools(self: *TuiRuntime) []const agent.AgentTool {
        return self.original_tools;
    }

    pub fn permissionMode(self: *const TuiRuntime) PermissionMode {
        return self.permission_mode;
    }

    pub fn thinkingLevel(self: *const TuiRuntime) ai_types.ThinkingLevel {
        return self.thinking_level;
    }

    pub fn setThinkingLevel(self: *TuiRuntime, level: ai_types.ThinkingLevel) !void {
        const normalized = normalizeTuiThinkingLevel(level);
        if (self.settingsFixedOverOap()) try self.remote.?.vtable.set_reasoning(self.remote.?.ctx, normalized);
        self.thinking_level = normalized;
        if (self.local_agent) |*local| local.setThinkingLevel(normalized);
    }

    pub fn outputSetting(self: *const TuiRuntime) agent.OutputSetting {
        return self.output;
    }

    pub fn setOutput(self: *TuiRuntime, setting: agent.OutputSetting) error{ AboveMaximum, AgentAlreadyStreaming, UnavailableOverOap }!void {
        if (self.settingsFixedOverOap()) return error.UnavailableOverOap;
        if (self.local_agent) |*local| {
            if (!local.isIdle()) return error.AgentAlreadyStreaming;
        }
        if (setting == .tokens) {
            if (self.currentModel()) |model| {
                if (model.max_tokens > 0 and setting.tokens > model.max_tokens) return error.AboveMaximum;
            }
        }
        self.output = setting;
        if (self.local_agent) |*local| local.setOutput(setting);
    }

    pub fn setPermissionMode(self: *TuiRuntime, mode: PermissionMode) !void {
        if (self.settingsFixedOverOap()) return error.UnavailableOverOap;
        self.permission_mode = mode;
        if (self.permission_engine) |engine| engine.setBypassAll(mode == .bypass);
        self.rebuildWrappedTools();
        if (self.local_agent) |*local| {
            local.setPermissionEngine(self.permission_engine);
            local.setTools(self.wrapped_tools);
        }
        self.tool_protocol.server.tools.clearRetainingCapacity();
        try self.tool_protocol.server.registerTools(self.wrapped_tools);
    }

    pub fn setWorkspaceRoot(self: *TuiRuntime, root: []const u8) !void {
        if (self.settingsFixedOverOap()) return error.UnavailableOverOap;
        if (self.local_agent) |*local| {
            if (!local.isIdle()) return error.AgentAlreadyStreaming;
        }
        const owned = try self.allocator.dupe(u8, root);
        const owned_cwd = self.allocator.dupe(u8, root) catch |err| {
            self.allocator.free(owned);
            return err;
        };
        self.allocator.free(self.workspace_root);
        self.allocator.free(self.session_cwd);
        self.workspace_root = owned;
        self.session_cwd = owned_cwd;
        if (self.permission_engine) |engine| try engine.setWorkspaceRoot(root);
        if (self.local_agent) |*local| {
            const system_prompt = try self.workspaceSystemPrompt();
            defer self.allocator.free(system_prompt);
            try local.setSystemPrompt(system_prompt);
        }
    }

    pub fn workingDirectory(self: *const TuiRuntime) []const u8 {
        return self.session_cwd;
    }
    fn adoptWorkingDirectory(self: *TuiRuntime, reported: []const u8) void {
        if (reported.len == 0) return;
        if (!self.workingDirectoryInsideRoot(reported)) return;
        if (std.mem.eql(u8, reported, self.session_cwd)) return;
        const owned = self.allocator.dupe(u8, reported) catch return;
        self.allocator.free(self.session_cwd);
        self.session_cwd = owned;
    }

    fn adoptReportedWorkingDirectory(self: *TuiRuntime, args_json: []const u8, details_json: []const u8, result: *agent.AgentToolResult) void {
        if (details_json.len == 0) return;
        var parsed = std.json.parseFromSlice(std.json.Value, self.allocator, details_json, .{}) catch return;
        defer parsed.deinit();
        if (parsed.value != .object) return;
        const details = detailsObject(parsed.value.object) orelse return;
        const observed = details.get("working_directory_observed") orelse return;
        if (observed != .bool or !observed.bool) return;
        const directory = details.get("working_directory") orelse return;
        if (directory != .string) return;
        const started_in = startDirectoryOf(self.allocator, args_json);
        defer if (started_in) |owned| self.allocator.free(owned);
        if (started_in) |from| {
            if (std.mem.eql(u8, from, directory.string)) return;
        }
        self.adoptWorkingDirectory(directory.string);
        self.appendWorkingDirectoryLine(result);
    }

    fn appendWorkingDirectoryLine(self: *TuiRuntime, result: *agent.AgentToolResult) void {
        if (!result.content.is_owned) return;
        const parts = result.content.slice();
        if (parts.len == 0) return;
        const last = parts[parts.len - 1];
        if (last != .text) return;
        const merged = std.fmt.allocPrint(self.allocator, "{s}\ncwd: {s}", .{ last.text.text, self.session_cwd }) catch return;
        const next = self.allocator.alloc(ai_types.UserContentPart, parts.len) catch {
            self.allocator.free(merged);
            return;
        };
        @memcpy(next[0 .. parts.len - 1], parts[0 .. parts.len - 1]);
        next[parts.len - 1] = .{ .text = .{ .text = merged, .text_signature = last.text.text_signature } };
        self.allocator.free(last.text.text);
        self.allocator.free(parts);
        result.content = OwnedSlice(ai_types.UserContentPart).initOwned(next);
    }

    fn workingDirectoryInsideRoot(self: *const TuiRuntime, candidate: []const u8) bool {
        if (!std.Io.Dir.path.isAbsolute(candidate)) return false;
        const root = std.Io.Dir.path.resolve(self.allocator, &.{self.workspace_root}) catch return false;
        defer self.allocator.free(root);
        const resolved = std.Io.Dir.path.resolve(self.allocator, &.{candidate}) catch return false;
        defer self.allocator.free(resolved);
        if (std.mem.eql(u8, resolved, root)) return true;
        if (!std.mem.startsWith(u8, resolved, root)) return false;
        return resolved.len > root.len and resolved[root.len] == std.fs.path.sep;
    }

    fn rewriteWorkspaceRoot(self: *TuiRuntime, tool_name: []const u8, args_json: []const u8, allocator: std.mem.Allocator) !?[]u8 {
        if (std.mem.startsWith(u8, tool_name, local_tools.mcp_bridge.tool_prefix)) return null;
        if (self.session_cwd.len == 0) return null;
        const base = if (directoryOpens(self.session_cwd)) self.session_cwd else self.workspace_root;
        var parsed = std.json.parseFromSlice(std.json.Value, allocator, args_json, .{}) catch return null;
        defer parsed.deinit();
        if (parsed.value != .object) return null;
        if (hasAbsolutePathArgument(parsed.value.object)) return null;
        const existing = parsed.value.object.get("workspace_root") orelse return null;
        if (existing != .string) return null;
        if (!std.mem.eql(u8, existing.string, self.workspace_root)) return null;
        if (std.mem.eql(u8, existing.string, base)) return null;
        const held = parsed.value.object.getPtr("workspace_root").?;
        held.* = .{ .string = base };
        return json_encode.valueAlloc(allocator, parsed.value) catch return null;
    }

    pub fn setCompactOutput(self: *TuiRuntime, enabled: bool) void {
        self.compact_output = enabled;
        if (self.local_agent) |*local| local.setCompactToolOutput(enabled);
    }

    pub fn switchModel(self: *TuiRuntime, model_id: []const u8) !void {
        if (self.local_agent) |*local| {
            if (!local.isIdle()) return error.AgentAlreadyStreaming;
        }

        for (self.models, 0..) |model, i| {
            if (std.mem.eql(u8, model.id, model_id)) {
                if (self.remote) |remote| {
                    if (self.stream_active) return error.AgentAlreadyStreaming;
                    if (self.started) try remote.vtable.switch_model(remote.ctx, self.models[i]);
                }
                self.selected_model_index = i;
                self.reconcileContextWindowAfterModelSwitch();
                if (self.local_agent) |*local| local.setModel(self.effectiveModel(self.models[i]));
                return;
            }
        }
        return error.ModelNotFound;
    }

    pub fn requestModelSwitch(self: *TuiRuntime, model_id: []const u8) !ai_types.Model {
        for (self.models, 0..) |model, i| {
            if (std.mem.eql(u8, model.id, model_id)) return self.requestModelSwitchAt(i);
        }
        return error.ModelNotFound;
    }

    pub fn requestModelSwitchAt(self: *TuiRuntime, index: usize) !ai_types.Model {
        if (index >= self.models.len) return error.ModelNotFound;
        self.pending_model_index = index;
        if (self.local_agent) |*local| local.requestModelSwitch(self.effectiveModel(self.models[index]));
        return self.models[index];
    }

    pub fn dropPendingModelSwitch(self: *TuiRuntime) void {
        self.pending_model_index = null;
        if (self.local_agent) |*local| local.requestModelSwitch(null);
    }

    pub fn applyPendingModelSwitch(self: *TuiRuntime) !?ai_types.Model {
        const index = self.pending_model_index orelse return null;
        if (index >= self.models.len) {
            self.pending_model_index = null;
            return null;
        }
        try self.switchModelExact(self.models[index]);
        self.pending_model_index = null;
        if (self.local_agent) |*local| local.requestModelSwitch(null);
        return self.models[index];
    }

    pub fn switchModelExact(self: *TuiRuntime, selected: ai_types.Model) !void {
        if (self.local_agent) |*local| {
            if (!local.isIdle()) return error.AgentAlreadyStreaming;
        }

        for (self.models, 0..) |model, i| {
            if (std.mem.eql(u8, model.id, selected.id) and
                std.mem.eql(u8, model.provider, selected.provider) and
                std.mem.eql(u8, model.api, selected.api))
            {
                if (self.remote) |remote| {
                    if (self.stream_active) return error.AgentAlreadyStreaming;
                    if (self.started) try remote.vtable.switch_model(remote.ctx, self.models[i]);
                }
                self.selected_model_index = i;
                self.reconcileContextWindowAfterModelSwitch();
                if (self.local_agent) |*local| local.setModel(self.effectiveModel(self.models[i]));
                return;
            }
        }
        return error.ModelNotFound;
    }

    fn makeUserMessage(self: *TuiRuntime, text: []const u8) !ai_types.Message {
        const owned_text = try self.allocator.dupe(u8, text);
        return .{ .user = .{
            .content = .{ .text = owned_text },
            .timestamp = compat.time.nowMillis(),
        } };
    }

    pub fn submitTurn(self: *TuiRuntime, text: []const u8) !void {
        if (!self.started) try self.start();
        if (self.remote) |remote| {
            if (self.currentModel() == null) return error.NoModelConfigured;
            if (self.stream_active) return remote.vtable.follow_up(remote.ctx, text);
            self.resetEventStreamForTurn();
            self.cancelled.store(false, .release);
            self.completed = false;
            self.last_turn_stop_reason = null;
            return remote.vtable.submit(remote.ctx, text);
        }
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        if (self.currentModel() == null) return error.NoModelConfigured;
        if (self.run_async) local.waitForIdle();
        self.resetEventStreamForTurn();
        self.cancelled.store(false, .release);
        self.completed = false;
        self.last_turn_stop_reason = null;
        if (self.run_async) {
            var msg = try self.makeUserMessage(text);
            defer msg.deinit(self.allocator);
            try local.promptAsync(msg);
        } else {
            const msg = try self.makeUserMessage(text);
            try local.prompt(msg);
        }
    }

    pub fn steer(self: *TuiRuntime, text: []const u8) !void {
        if (self.remote) |remote| {
            if (!self.started) return error.RuntimeNotStarted;
            if (!self.stream_active) {
                try self.submitTurn(text);
                self.remote_steers_started += 1;
                return;
            }
            return remote.vtable.steer(remote.ctx, text);
        }
        if (!self.started) return error.RuntimeNotStarted;
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        var msg = try self.makeUserMessage(text);
        var queued = false;
        errdefer if (!queued) msg.deinit(self.allocator);
        try local.steer(msg);
        queued = true;
        try self.resumeQueuedMessagesIfIdle();
    }

    pub fn queueSteer(self: *TuiRuntime, text: []const u8) !void {
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        var msg = try self.makeUserMessage(text);
        errdefer msg.deinit(self.allocator);
        try local.steer(msg);
    }

    pub fn clearSteers(self: *TuiRuntime) void {
        const local = &(self.local_agent orelse return);
        local.clearSteeringQueue();
    }

    pub fn followUp(self: *TuiRuntime, text: []const u8) !void {
        if (self.remote) |remote| {
            if (!self.started) return error.RuntimeNotStarted;
            if (!self.stream_active) return self.submitTurn(text);
            return remote.vtable.follow_up(remote.ctx, text);
        }
        if (!self.started) return error.RuntimeNotStarted;
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        var msg = try self.makeUserMessage(text);
        var queued = false;
        errdefer if (!queued) msg.deinit(self.allocator);
        try local.followUp(msg);
        queued = true;
        try self.resumeQueuedMessagesIfIdle();
    }

    fn resumeQueuedMessagesIfIdle(self: *TuiRuntime) !void {
        const local = &(self.local_agent orelse return);
        if (!local.isIdle()) return;
        local.validateContinueFromContext() catch return;
        try self.resumeSession();
    }

    pub fn clearQueuedMessages(self: *TuiRuntime) void {
        if (self.remote) |remote| return remote.vtable.clear_queued(remote.ctx);
        const local = &(self.local_agent orelse return);
        local.clearAllQueues();
    }

    pub fn queuedCounts(self: *TuiRuntime) QueuedCounts {
        if (self.remote) |remote| return .{ .steering = remote.vtable.steers_pending(remote.ctx), .follow_up = remote.vtable.queued(remote.ctx) };
        const local = &(self.local_agent orelse return .{});
        return local.queuedCounts();
    }

    pub fn steersConsumedCount(self: *TuiRuntime) u64 {
        if (self.remote) |remote| return remote.vtable.steers_settled(remote.ctx) + self.remote_steers_started;
        const local = &(self.local_agent orelse return 0);
        return local.steeringConsumedCount();
    }

    pub fn replaceMessages(self: *TuiRuntime, messages: []const ai_types.Message) !void {
        if (self.remote != null) {
            if (messages.len > 0) return error.UnavailableOverOap;
            return;
        }
        if (!self.started) try self.start();
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        if (self.run_async) local.waitForIdle();
        local.clearAllQueues();
        self.resetBackpressureState();
        try local.replaceMessages(messages);
    }

    pub fn history(self: *TuiRuntime) []const ai_types.Message {
        const local = &(self.local_agent orelse return &.{});
        if (!local.isIdle()) return &.{};
        local.waitForIdle();
        return local._state.messages.items;
    }

    pub fn compact(self: *TuiRuntime, options: CompactOptions) !void {
        if (self.remote) |remote| {
            if (!self.started) try self.start();
            if (!remote.vtable.compacts(remote.ctx)) return error.UnavailableOverOap;
            if (self.stream_active) return error.AgentAlreadyStreaming;
            if (self.currentModel() == null) return error.NoModelConfigured;
            self.resetEventStreamForTurn();
            self.cancelled.store(false, .release);
            self.completed = false;
            self.push(.{ .compaction_start = .{} });
            remote.vtable.compact(remote.ctx, options.focus) catch |err| {
                self.finishCompaction(.{ .outcome = .failed, .message = OwnedSlice(u8).initBorrowed(@errorName(err)) });
                return err;
            };
            return;
        }
        if (!self.started) try self.start();
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        if (self.currentModel() == null) return error.NoModelConfigured;
        if (!local.isIdle()) return error.AgentAlreadyStreaming;
        local.waitForIdle();
        try local.ensureCompactable();
        const transcript = try self.allocator.dupe(u8, if (options.transcripts.len > 0) options.transcripts[options.transcripts.len - 1] else "");
        self.allocator.free(self.compaction_transcript);
        self.compaction_transcript = transcript;
        self.resetEventStreamForTurn();
        self.cancelled.store(false, .release);
        self.completed = false;
        self.push(.{ .compaction_start = .{} });
        local.compactAsync(.{ .focus = options.focus, .transcripts = options.transcripts }, self, onCompaction) catch |err| {
            self.finishCompaction(.{ .outcome = .failed, .message = OwnedSlice(u8).initBorrowed(@errorName(err)) });
        };
    }

    pub const TranscriptWriter = struct {
        ctx: ?*anyopaque,
        save_fn: *const fn (ctx: ?*anyopaque, allocator: std.mem.Allocator, index: usize, history: []const ai_types.Message) ?[]u8,
    };

    pub fn setSessionId(self: *TuiRuntime, session_id: []const u8) !void {
        const owned = try self.allocator.dupe(u8, session_id);
        self.allocator.free(self.session_id);
        self.session_id = owned;
        const local = &(self.local_agent orelse return);
        if (!local.isIdle()) return error.AgentAlreadyStreaming;
        try self.applySessionId();
    }

    fn applySessionId(self: *TuiRuntime) !void {
        const local = &(self.local_agent orelse return);
        try local.setSessionId(if (self.session_id.len > 0) self.session_id else null);
    }

    pub fn setCompactionPolicy(self: *TuiRuntime, policy_json: []const u8) !void {
        const remote = self.remote orelse return error.NotOverOap;
        if (!self.started) try self.start();
        try remote.vtable.set_compaction_policy(remote.ctx, policy_json);
    }

    pub fn armAutoCompact(self: *TuiRuntime, at: ?u64, transcripts: []const []const u8, writer: ?TranscriptWriter) !void {
        if (!self.started) try self.start();
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        if (!local.isIdle()) return error.AgentAlreadyStreaming;
        var copies: std.ArrayList([]u8) = .empty;
        errdefer {
            for (copies.items) |path| self.allocator.free(path);
            copies.deinit(self.allocator);
        }
        try copies.ensureTotalCapacity(self.allocator, transcripts.len);
        for (transcripts) |path| copies.appendAssumeCapacity(try self.allocator.dupe(u8, path));
        self.clearRunTranscripts();
        self.run_transcripts.deinit(self.allocator);
        self.run_transcripts = copies;
        self.transcript_writer = writer;
        local.setAutoCompact(at, .{ .ctx = self, .transcripts_fn = runTranscripts, .settled_fn = settleRunTranscript });
    }

    fn settleRunTranscript(ctx: ?*anyopaque, completed: bool) void {
        const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
        defer self.run_transcript_saved = "";
        if (completed or self.run_transcript_saved.len == 0) return;
        const items = self.run_transcripts.items;
        if (items.len == 0 or items[items.len - 1].ptr != self.run_transcript_saved.ptr) return;
        self.allocator.free(self.run_transcripts.pop().?);
    }

    fn clearRunTranscripts(self: *TuiRuntime) void {
        for (self.run_transcripts.items) |path| self.allocator.free(path);
        self.run_transcripts.clearRetainingCapacity();
        self.run_transcript_saved = "";
    }

    fn runTranscripts(ctx: ?*anyopaque, messages: []const ai_types.Message) agent.Agent.CompactionTranscripts {
        const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
        self.run_transcript_saved = "";
        const writer = self.transcript_writer orelse return .{ .paths = self.run_transcripts.items };
        const path = writer.save_fn(writer.ctx, self.allocator, self.run_transcripts.items.len + 1, messages) orelse return .{ .paths = self.run_transcripts.items };
        self.run_transcripts.append(self.allocator, path) catch {
            self.allocator.free(path);
            return .{ .paths = self.run_transcripts.items };
        };
        self.run_transcript_saved = path;
        return .{ .paths = self.run_transcripts.items, .saved = path };
    }

    fn onCompaction(ctx: ?*anyopaque, result: *const agent.compaction.Result) void {
        const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
        const payload = compactionEndPayload(self.allocator, self.compaction_transcript, result) catch |err| CompactionEnd{ .outcome = .failed, .message = OwnedSlice(u8).initBorrowed(@errorName(err)) };
        self.finishCompaction(payload);
    }

    fn finishCompaction(self: *TuiRuntime, payload: CompactionEnd) void {
        const reason: TuiEndReason = switch (payload.outcome) {
            .completed => .completed,
            .cancelled => .cancelled,
            .failed => .@"error",
        };
        self.completed = true;
        self.stream_active = false;
        self.pushTerminal(.{ .compaction_end = payload });
        self.event_stream.complete(.{ .reason = reason });
    }

    pub fn resumeSession(self: *TuiRuntime) !void {
        if (self.remote != null) return error.UnavailableOverOap;
        if (!self.started) try self.start();
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        if (self.run_async) local.waitForIdle();
        try local.validateContinueFromContext();
        self.resetEventStreamForTurn();
        self.cancelled.store(false, .release);
        self.completed = false;
        self.last_turn_stop_reason = null;
        if (self.run_async) {
            try local.continueFromContextAsync();
        } else {
            try local.continueFromContext();
        }
    }

    pub fn cancel(self: *TuiRuntime) void {
        self.cancelled.store(true, .release);
        if (self.remote) |remote| {
            if (self.stream_active) remote.vtable.cancel(remote.ctx);
            return;
        }
        if (self.local_agent) |*local| local.abort();
        while (!self.approval_mutex.tryLock()) std.atomic.spinLoopHint();
        self.pending_approval.cancelled = true;
        self.pending_approval.decision = .reject;
        self.approval_mutex.unlock();
    }

    pub fn streamEvents(self: *TuiRuntime) *TuiEventStream {
        return &self.event_stream;
    }

    pub fn backpressureState(self: *TuiRuntime) struct { active: bool, dropped_count: u64 } {
        while (!self.backpressure_mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.backpressure_mutex.unlock();
        const active = self.backpressure_active.load(.acquire);
        const dropped_count = self.dropped_event_count;
        if (active and !self.event_stream.isFull()) {
            self.backpressure_active.store(false, .release);
            self.backpressure_status_active_emitted.store(false, .release);
        }
        return .{
            .active = active,
            .dropped_count = dropped_count,
        };
    }

    fn resetBackpressureState(self: *TuiRuntime) void {
        while (!self.backpressure_mutex.tryLock()) std.atomic.spinLoopHint();
        self.dropped_event_count = 0;
        self.dropped_since_warning = 0;
        self.backpressure_active.store(false, .release);
        self.backpressure_status_active_emitted.store(false, .release);
        self.backpressure_mutex.unlock();
    }

    pub fn decideToolApproval(self: *TuiRuntime, tool_call_id: []const u8, decision: ToolApprovalDecision) !void {
        if (self.remote) |remote| return remote.vtable.decide_approval(remote.ctx, tool_call_id, decision == .approve or decision == .approve_always);
        while (!self.approval_mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.approval_mutex.unlock();
        if (self.pending_approval.tool_call_id.len > 0 and !std.mem.eql(u8, self.pending_approval.tool_call_id, tool_call_id)) return error.ToolApprovalNotPending;
        if (self.pending_approval.tool_call_id.len == 0) {
            self.pending_approval.tool_call_id = try self.allocator.dupe(u8, tool_call_id);
        }
        self.pending_approval.decision = decision;
        self.pending_approval.cancelled = false;
    }

    fn clearPendingApproval(self: *TuiRuntime) void {
        while (!self.approval_mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.approval_mutex.unlock();
        if (self.pending_approval.tool_call_id.len > 0) self.allocator.free(self.pending_approval.tool_call_id);
        self.pending_approval = .{};
    }

    fn waitForToolApproval(self: *TuiRuntime, request: ToolApprovalRequest) ToolApprovalDecision {
        while (!self.approval_mutex.tryLock()) std.atomic.spinLoopHint();
        if (self.pending_approval.tool_call_id.len > 0) self.allocator.free(self.pending_approval.tool_call_id);
        self.pending_approval = .{ .tool_call_id = self.allocator.dupe(u8, request.tool_call_id) catch {
            self.approval_mutex.unlock();
            return .approve;
        } };
        self.approval_mutex.unlock();
        while (!self.cancelled.load(.acquire)) {
            while (!self.approval_mutex.tryLock()) std.atomic.spinLoopHint();
            const decision = self.pending_approval.decision;
            const approval_cancelled = self.pending_approval.cancelled;
            self.approval_mutex.unlock();
            if (decision) |value| return value;
            if (approval_cancelled) return .reject;
            compat.time.sleepNs(1 * std.time.ns_per_ms);
        }
        return .reject;
    }

    fn resetEventStreamForTurn(self: *TuiRuntime) void {
        const guarded = self.remote != null;
        if (guarded) {
            while (!self.remote_mutex.tryLock()) std.atomic.spinLoopHint();
        }
        defer if (guarded) self.remote_mutex.unlock();
        self.current_generation +%= 1;
        if (!self.stream_active or self.event_stream.isDone()) {
            self.event_stream.deinit();
            self.event_stream = TuiEventStream.init(self.allocator);
            self.resetBackpressureState();
        }
        self.stream_active = true;
    }

    fn rebuildWrappedTools(self: *TuiRuntime) void {
        for (self.original_tools, 0..) |tool, i| {
            const bypass = self.permission_mode == .bypass;
            self.approval_contexts[i] = .{
                .runtime = self,
                .callback_ctx = self.tool_approval_ctx,
                .callback = self.tool_approval_callback,
                .original_ctx = tool.approval_ctx,
                .original_callback = tool.approval_fn,
                .original_ui_ctx = tool.approval_ui_ctx,
                .original_ui_callback = tool.approval_ui_fn,
                .tool_name = tool.name,
            };
            self.wrapped_tools[i] = .{
                .label = tool.label,
                .name = tool.name,
                .description = tool.description,
                .short_description = tool.short_description,
                .parameters_schema_json = tool.parameters_schema_json,
                .execute = tool.execute,
                .operation = tool.operation,
                .runtime_ctx = tool.runtime_ctx,
                .runtime_execute = tool.runtime_execute,
                .approval_ctx = if (bypass) null else &self.approval_contexts[i],
                .approval_fn = if (bypass) null else approveTool,
                .approval_ui_ctx = if (bypass) null else &self.approval_contexts[i],
                .approval_ui_fn = if (bypass) null else notifyToolApproval,
            };
        }
    }

    fn push(self: *TuiRuntime, event: TuiEvent) void {
        var mutable = event;
        mutable.setGeneration(self.current_generation);
        mutable.stamp(compat.time.nowMillis());
        self.pushDroppingOldestCounted(mutable);
        self.flushDroppedWarning();
    }

    fn pushTerminal(self: *TuiRuntime, event: TuiEvent) void {
        var mutable = event;
        mutable.setGeneration(self.current_generation);
        mutable.stamp(compat.time.nowMillis());
        self.pushDroppingOldestCounted(mutable);
        self.flushDroppedWarningDroppingOldest();
    }

    fn pushDroppingOldestCounted(self: *TuiRuntime, event: TuiEvent) void {
        const started_ms = compat.time.nowMillis();
        while (true) {
            if (self.pushUncounted(event)) return;
            while (!self.backpressure_mutex.tryLock()) std.atomic.spinLoopHint();
            if (self.event_stream.push(event)) {
                self.backpressure_mutex.unlock();
                return;
            } else |err| switch (err) {
                error.QueueFull => {},
                error.StreamCompleted, error.OutOfMemory => {
                    self.backpressure_mutex.unlock();
                    var mutable = event;
                    mutable.deinit(self.allocator);
                    return;
                },
            }
            if (self.shedStreamingLocked() == 0) {
                if (isStreamingChunk(event)) {
                    self.countDroppedLocked(1);
                    self.backpressure_mutex.unlock();
                    var mutable = event;
                    mutable.deinit(self.allocator);
                    return;
                }
                if (compat.time.nowMillis() -| started_ms >= self.semantic_wait_ms) self.dropOldestLocked();
            }
            self.backpressure_mutex.unlock();
        }
    }

    fn dropOldestLocked(self: *TuiRuntime) void {
        var dropped = self.event_stream.poll() orelse return;
        dropped.deinit(self.allocator);
        self.countDroppedLocked(1);
    }

    fn isStreamingChunk(event: TuiEvent) bool {
        return switch (event) {
            .text_delta, .thinking_delta, .tool_call_delta, .provider_event, .tool_execution_update => true,
            else => false,
        };
    }

    fn evictStreamingChunk(self: *TuiRuntime, event: *TuiEvent) bool {
        if (!isStreamingChunk(event.*)) return false;
        event.deinit(self.allocator);
        return true;
    }

    fn countDroppedLocked(self: *TuiRuntime, count: usize) void {
        self.dropped_event_count += count;
        self.dropped_since_warning += count;
        self.backpressure_active.store(true, .release);
    }

    fn shedStreamingLocked(self: *TuiRuntime) usize {
        const evicted = self.event_stream.evictWhere(self, evictStreamingChunk);
        if (evicted > 0) {
            self.countDroppedLocked(evicted);
            return evicted;
        }
        std.Thread.yield() catch {};
        return 0;
    }

    fn pushUncounted(self: *TuiRuntime, event: TuiEvent) bool {
        while (!self.backpressure_mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.backpressure_mutex.unlock();
        self.event_stream.push(event) catch |err| switch (err) {
            error.QueueFull => return false,
            error.StreamCompleted, error.OutOfMemory => {
                var mutable = event;
                mutable.deinit(self.allocator);
                return true;
            },
        };
        return true;
    }

    fn flushDroppedWarning(self: *TuiRuntime) void {
        while (!self.backpressure_mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.backpressure_mutex.unlock();
        if (self.dropped_since_warning == 0) return;
        const message = std.fmt.allocPrint(self.allocator, "Warning: {d} event{s} dropped due to backpressure", .{
            self.dropped_since_warning,
            if (self.dropped_since_warning == 1) "" else "s",
        }) catch |err| {
            var err_event = TuiEvent{ .@"error" = .{ .message = self.dupeOwned(@errorName(err)) catch OwnedSlice(u8).initBorrowed("") } };
            err_event.setGeneration(self.current_generation);
            self.event_stream.push(err_event) catch {
                var mutable = err_event;
                mutable.deinit(self.allocator);
            };
            return;
        };
        var warning = TuiEvent{ .system_warning = .{ .message = OwnedSlice(u8).initOwned(message) } };
        warning.setGeneration(self.current_generation);
        self.event_stream.push(warning) catch {
            var mutable = warning;
            mutable.deinit(self.allocator);
            return;
        };
        self.dropped_since_warning = 0;
    }

    fn flushDroppedWarningDroppingOldest(self: *TuiRuntime) void {
        while (true) {
            while (!self.backpressure_mutex.tryLock()) std.atomic.spinLoopHint();
            const count = self.dropped_since_warning;
            self.backpressure_mutex.unlock();
            if (count == 0) return;

            const message = std.fmt.allocPrint(self.allocator, "Warning: {d} event{s} dropped due to backpressure", .{
                count,
                if (count == 1) "" else "s",
            }) catch return;
            var warning = TuiEvent{ .system_warning = .{ .message = OwnedSlice(u8).initOwned(message) } };
            warning.setGeneration(self.current_generation);
            if (self.pushUncounted(warning)) {
                while (!self.backpressure_mutex.tryLock()) std.atomic.spinLoopHint();
                self.dropped_since_warning -|= count;
                self.backpressure_mutex.unlock();
                return;
            }
            var mutable = warning;
            mutable.deinit(self.allocator);

            while (!self.backpressure_mutex.tryLock()) std.atomic.spinLoopHint();
            const shed = self.shedStreamingLocked();
            self.backpressure_mutex.unlock();
            if (shed == 0) return;
        }
    }

    fn dupeOwned(self: *TuiRuntime, value: []const u8) !OwnedSlice(u8) {
        return OwnedSlice(u8).initOwned(try self.allocator.dupe(u8, value));
    }

    fn handleAgentEndEvent(self: *TuiRuntime) anyerror!void {
        const cancelled = self.cancelled.load(.acquire);
        if (!cancelled and self.last_turn_stop_reason == .length) {
            self.push(.{ .system_warning = .{ .message = OwnedSlice(u8).initBorrowed(output_limit_warning) } });
        }
        const reason: TuiEndReason = if (cancelled) .cancelled else if (self.last_turn_stop_reason == .@"error") .@"error" else .completed;
        return self.endRun(reason);
    }
    fn pushRemote(ctx: *anyopaque, event: TuiEvent) void {
        const self: *TuiRuntime = @ptrCast(@alignCast(ctx));
        while (!self.remote_mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.remote_mutex.unlock();
        switch (event) {
            .agent_end => |payload| {
                if (payload.reason == .completed and self.last_turn_stop_reason == .length) {
                    self.push(.{ .system_warning = .{ .message = OwnedSlice(u8).initBorrowed(output_limit_warning) } });
                }
                self.endRun(payload.reason) catch {};
            },
            .turn_end => |payload| {
                self.last_turn_stop_reason = payload.stop_reason;
                self.pushTerminal(event);
            },
            .compaction_end => |payload| if (payload.in_run) self.push(event) else self.finishCompaction(payload),
            else => self.push(event),
        }
    }

    fn endRun(self: *TuiRuntime, reason: TuiEndReason) anyerror!void {
        self.completed = true;
        self.stream_active = false;
        self.pushTerminal(.{ .agent_end = .{ .reason = reason } });
        self.event_stream.complete(.{ .reason = reason });
    }

    fn workspaceSystemPrompt(self: *TuiRuntime) ![]u8 {
        if (self.workspace_root.len == 0) return self.allocator.dupe(u8, "");
        const moved_rule = if (@import("builtin").os.tag == .windows)
            "A `cd` inside a command does not persist on this platform: every call starts in the same directory."
        else
            "A `cd` in a `Shell` call changes the working directory, and its result reports the directory as a `cwd:` line.";
        return std.fmt.allocPrint(self.allocator,
            \\Default workspace root: {s}
            \\Pass the default workspace root as `workspace_root` to work in the session's current working directory, or name another directory inside the root to work there instead; an absolute path is used as written. {s}
        , .{ self.workspace_root, moved_rule });
    }

    fn messageRole(message: ai_types.Message) TuiEvent.MessageRole {
        return switch (message) {
            .user => .user,
            .assistant => .assistant,
            .tool_result => .tool_result,
        };
    }

    fn messageEndPayload(self: *TuiRuntime, message: ai_types.Message) !@TypeOf(@as(TuiEvent, undefined).message_end) {
        var payload: @TypeOf(@as(TuiEvent, undefined).message_end) = .{ .role = messageRole(message) };
        switch (message) {
            .user => |m| {
                payload.text = try self.dupeOwned(firstUserContentText(m.content));
                const content_json = try serializeUserContent(self.allocator, m.content);
                defer self.allocator.free(content_json);
                payload.content_json = try self.dupeOwned(content_json);
            },
            .assistant => |m| {
                payload.output_tokens = m.usage.output;
                payload.input_tokens = m.usage.input;
                payload.cache_read_tokens = m.usage.cache_read;
                payload.text = try self.dupeOwned(assistantText(m.content));
                const content_json = try serializeAssistantContent(self.allocator, m.content);
                defer self.allocator.free(content_json);
                const tool_calls_json = try serializeToolCalls(self.allocator, m.content);
                defer self.allocator.free(tool_calls_json);
                payload.content_json = try self.dupeOwned(content_json);
                payload.tool_calls_json = try self.dupeOwned(tool_calls_json);
                payload.stop_reason = m.stop_reason;
            },
            .tool_result => |m| {
                payload.tool_call_id = try self.dupeOwned(m.tool_call_id);
                payload.tool_name = try self.dupeOwned(m.tool_name);
                payload.text = try self.dupeOwned(firstUserPartText(m.content));
                const content_json = try serializeUserParts(self.allocator, m.content);
                defer self.allocator.free(content_json);
                const artifacts_json = try serializeArtifacts(self.allocator, m.artifacts.slice());
                defer self.allocator.free(artifacts_json);
                payload.content_json = try self.dupeOwned(content_json);
                payload.details_json = try self.dupeOwned(m.details_json.slice());
                payload.artifacts_json = try self.dupeOwned(artifacts_json);
                payload.is_error = m.is_error;
            },
        }
        return payload;
    }

    fn firstUserContentText(content: ai_types.UserContent) []const u8 {
        return switch (content) {
            .text => |text| text,
            .parts => |parts| firstUserPartText(parts),
        };
    }

    fn firstUserPartText(parts: []const ai_types.UserContentPart) []const u8 {
        for (parts) |part| switch (part) {
            .text => |text| return text.text,
            else => {},
        };
        return "";
    }

    fn assistantText(content: []const ai_types.AssistantContent) []const u8 {
        for (content) |block| switch (block) {
            .text => |text| return text.text,
            else => {},
        };
        return "";
    }

    fn serializeUserContent(allocator: std.mem.Allocator, content: ai_types.UserContent) ![]u8 {
        return switch (content) {
            .text => |text| blk: {
                var buf: std.ArrayList(u8) = .empty;
                errdefer buf.deinit(allocator);
                var w = json_writer.JsonWriter.init(&buf, allocator);
                try w.beginArray();
                try writeUserTextPart(&w, text, null);
                try w.endArray();
                break :blk try buf.toOwnedSlice(allocator);
            },
            .parts => |parts| serializeUserParts(allocator, parts),
        };
    }

    fn serializeUserParts(allocator: std.mem.Allocator, parts: []const ai_types.UserContentPart) ![]u8 {
        var buf: std.ArrayList(u8) = .empty;
        errdefer buf.deinit(allocator);
        var w = json_writer.JsonWriter.init(&buf, allocator);
        try w.beginArray();
        for (parts) |part| switch (part) {
            .text => |text| try writeUserTextPart(&w, text.text, text.text_signature),
            .image => |image| try writeImagePart(&w, image),
        };
        try w.endArray();
        return buf.toOwnedSlice(allocator);
    }

    fn serializeAssistantContent(allocator: std.mem.Allocator, content: []const ai_types.AssistantContent) ![]u8 {
        var buf: std.ArrayList(u8) = .empty;
        errdefer buf.deinit(allocator);
        var w = json_writer.JsonWriter.init(&buf, allocator);
        try w.beginArray();
        for (content) |block| switch (block) {
            .text => |text| try writeAssistantTextPart(&w, text),
            .thinking => |thinking| try writeThinkingPart(&w, thinking),
            .tool_call => |tool| try writeToolCallPart(&w, tool),
            .image => |image| try writeImagePart(&w, image),
        };
        try w.endArray();
        return buf.toOwnedSlice(allocator);
    }

    fn serializeToolCalls(allocator: std.mem.Allocator, content: []const ai_types.AssistantContent) ![]u8 {
        var buf: std.ArrayList(u8) = .empty;
        errdefer buf.deinit(allocator);
        var w = json_writer.JsonWriter.init(&buf, allocator);
        try w.beginArray();
        for (content) |block| switch (block) {
            .tool_call => |tool| try writeToolCallPart(&w, tool),
            else => {},
        };
        try w.endArray();
        return buf.toOwnedSlice(allocator);
    }

    fn serializeArtifacts(allocator: std.mem.Allocator, artifacts: []const ai_types.ArtifactReference) ![]u8 {
        var buf: std.ArrayList(u8) = .empty;
        errdefer buf.deinit(allocator);
        var w = json_writer.JsonWriter.init(&buf, allocator);
        try w.beginArray();
        for (artifacts) |artifact| {
            try w.beginObject();
            try w.writeStringField("artifact_id", artifact.artifact_id);
            try w.writeStringField("uri", artifact.uri.slice());
            try w.writeStringField("mime_type", artifact.mime_type.slice());
            if (artifact.byte_size) |size| try w.writeIntField("byte_size", size);
            try w.writeStringField("sha256", artifact.sha256.slice());
            try w.writeStringField("description", artifact.description.slice());
            try w.endObject();
        }
        try w.endArray();
        return buf.toOwnedSlice(allocator);
    }

    fn writeUserTextPart(w: *json_writer.JsonWriter, text: []const u8, signature: ?[]const u8) !void {
        try w.beginObject();
        try w.writeStringField("type", "text");
        try w.writeStringField("text", text);
        if (signature) |sig| try w.writeStringField("text_signature", sig);
        try w.endObject();
    }

    fn writeAssistantTextPart(w: *json_writer.JsonWriter, text: ai_types.TextContent) !void {
        try w.beginObject();
        try w.writeStringField("type", "text");
        try w.writeStringField("text", text.text);
        if (text.text_signature) |sig| try w.writeStringField("text_signature", sig);
        try w.endObject();
    }

    fn writeThinkingPart(w: *json_writer.JsonWriter, thinking: ai_types.ThinkingContent) !void {
        try w.beginObject();
        try w.writeStringField("type", "thinking");
        try w.writeStringField("thinking", thinking.thinking);
        if (thinking.thinking_signature) |sig| try w.writeStringField("thinking_signature", sig);
        try w.endObject();
    }

    fn writeToolCallPart(w: *json_writer.JsonWriter, tool: ai_types.ToolCall) !void {
        try w.beginObject();
        try w.writeStringField("type", "tool_call");
        try w.writeStringField("id", tool.id);
        try w.writeStringField("name", tool.name);
        try w.writeStringField("arguments_json", tool.arguments_json);
        if (tool.thought_signature) |sig| try w.writeStringField("thought_signature", sig);
        try w.endObject();
    }

    fn writeImagePart(w: *json_writer.JsonWriter, image: ai_types.ImageContent) !void {
        try w.beginObject();
        try w.writeStringField("type", "image");
        try w.writeStringField("data", image.data);
        try w.writeStringField("mime_type", image.mime_type);
        try w.endObject();
    }

    fn onAgentEvent(ctx: ?*anyopaque, event: agent.AgentEvent) void {
        const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
        self.handleAgentEvent(event) catch |err| {
            self.push(.{ .@"error" = .{ .message = self.dupeOwned(@errorName(err)) catch OwnedSlice(u8).initBorrowed("") } });
        };
    }

    fn handleAgentEvent(self: *TuiRuntime, event: agent.AgentEvent) !void {
        switch (event) {
            .agent_start => self.push(.{ .agent_start = .{} }),
            .turn_start => self.push(.{ .turn_start = .{} }),
            .message_start => |payload| {
                self.push(.{ .message_start = .{ .role = messageRole(payload.message) } });
            },
            .message_update => |payload| {
                if (!isChunk(payload.event)) try self.pushProviderEvent(payload.event);
                try self.pushMessageUpdate(payload.event);
            },
            .message_end => |payload| {
                var message_payload = try self.messageEndPayload(payload.message);
                if (message_payload.role == .user) message_payload.steering = payload.steering;
                self.push(.{ .message_end = message_payload });
            },
            .tool_execution_start => |payload| self.push(.{ .tool_execution_start = .{
                .tool_call_id = try self.dupeOwned(payload.tool_call_id),
                .tool_name = try self.dupeOwned(payload.tool_name),
                .args_json = try self.dupeOwned(payload.args_json),
            } }),
            .tool_execution_update => |payload| self.push(.{ .tool_execution_update = .{
                .tool_call_id = try self.dupeOwned(payload.tool_call_id),
                .tool_name = try self.dupeOwned(payload.tool_name),
                .args_json = try self.dupeOwned(payload.args_json),
                .partial_result_json = try self.dupeOwned(payload.partial_result_json),
            } }),
            .tool_execution_end => |payload| self.push(.{ .tool_execution_end = .{
                .tool_call_id = try self.dupeOwned(payload.tool_call_id),
                .tool_name = try self.dupeOwned(payload.tool_name),
                .result_json = try self.dupeOwned(payload.result_json),
                .is_error = payload.is_error,
                .raw_total_bytes = payload.raw_total_bytes,
                .returned_total_bytes = payload.returned_total_bytes,
                .estimated_returned_tokens = payload.estimated_returned_tokens,
                .artifact_count = payload.artifact_count,
                .artifact_refs = try self.formatArtifactRefs(payload.artifacts),
            } }),
            .turn_end => |payload| {
                self.last_turn_stop_reason = payload.message.stop_reason;
                if (payload.message.stop_reason == .@"error") {
                    if (payload.message.getErrorMessage()) |message| {
                        self.push(.{ .@"error" = .{ .message = try self.dupeOwned(message) } });
                    }
                }
                self.pushTerminal(.{ .turn_end = .{ .stop_reason = payload.message.stop_reason } });
            },
            .agent_end => try self.handleAgentEndEvent(),
            .run_failed => |payload| {
                self.push(.{ .@"error" = .{ .message = self.dupeOwned(payload.reason.slice()) catch OwnedSlice(u8).initBorrowed(payload.reason.slice()) } });
                try self.endRun(.@"error");
            },
            .context_usage => |payload| self.push(.{ .context_usage = .{
                .system_prompt_bytes = payload.system_prompt_bytes,
                .message_bytes = payload.message_bytes,
                .tool_definition_bytes = payload.tool_definition_bytes,
                .total_bytes = payload.total_bytes,
                .estimated_tokens = payload.estimated_tokens,
                .message_count = payload.message_count,
                .tool_count = payload.tool_count,
            } }),
            .prompt_segment_usage => |payload| self.push(.{ .prompt_segment_usage = .{
                .segment = switch (payload.segment) {
                    .system_prompt => .system_prompt,
                    .message_history => .message_history,
                    .tool_definitions => .tool_definitions,
                },
                .cache_role = switch (payload.cache_role) {
                    .stable => .stable,
                    .dynamic => .dynamic,
                },
                .bytes = payload.bytes,
                .estimated_tokens = payload.estimated_tokens,
                .item_count = payload.item_count,
            } }),
            .compaction_start => self.push(.{ .compaction_start = .{ .in_run = true } }),
            .compaction_end => |payload| try self.pushRunCompactionEnd(payload),
        }
    }

    fn pushRunCompactionEnd(self: *TuiRuntime, payload: agent_types.CompactionEndPayload) !void {
        const text = try self.dupeOwned(payload.text.slice());
        errdefer {
            var owned = text;
            owned.deinit(self.allocator);
        }
        const transcript = try self.dupeOwned(payload.transcript.slice());
        errdefer {
            var owned = transcript;
            owned.deinit(self.allocator);
        }
        const message = try self.dupeOwned(payload.message.slice());
        self.push(.{ .compaction_end = .{
            .in_run = true,
            .outcome = switch (payload.outcome) {
                .completed => .completed,
                .cancelled => .cancelled,
                .failed => .failed,
            },
            .text = text,
            .transcript = transcript,
            .message = message,
            .messages_before = payload.messages_before,
            .tokens_before = payload.tokens_before,
            .tokens_after = payload.tokens_after,
        } });
    }

    fn formatArtifactRefs(self: *TuiRuntime, artifacts: []const ai_types.ArtifactReference) !OwnedSlice(u8) {
        var out: std.Io.Writer.Allocating = .init(self.allocator);
        defer out.deinit();
        const writer = &out.writer;
        for (artifacts, 0..) |artifact, i| {
            if (i > 0) try writer.writeAll(", ");
            if (artifact.getUri()) |uri| {
                try writer.writeAll(uri);
            } else {
                try writer.writeAll(artifact.artifact_id);
            }
        }
        return OwnedSlice(u8).initOwned(try out.toOwnedSlice());
    }

    fn pushMessageUpdate(self: *TuiRuntime, event: ai_types.AssistantMessageEvent) !void {
        switch (event) {
            .text_delta => |payload| self.push(.{ .text_delta = .{
                .content_index = payload.content_index,
                .delta = try self.dupeOwned(payload.delta),
            } }),
            .thinking_delta => |payload| self.push(.{ .thinking_delta = .{
                .content_index = payload.content_index,
                .delta = try self.dupeOwned(payload.delta),
            } }),
            .toolcall_delta => |payload| self.push(.{ .tool_call_delta = .{
                .content_index = payload.content_index,
                .delta = try self.dupeOwned(payload.delta),
            } }),
            else => {},
        }
    }

    fn isChunk(event: ai_types.AssistantMessageEvent) bool {
        return switch (event) {
            .text_delta, .thinking_delta, .toolcall_delta => true,
            else => false,
        };
    }

    fn pushProviderEvent(self: *TuiRuntime, event: ai_types.AssistantMessageEvent) !void {
        const event_json = try transport.serializeEvent(event, self.allocator);
        self.push(.{ .provider_event = .{ .event_json = OwnedSlice(u8).initOwned(event_json) } });
    }
};

fn compactionEndPayload(allocator: std.mem.Allocator, transcript_path: []const u8, result: *const agent.compaction.Result) !CompactionEnd {
    switch (result.*) {
        .completed => |completed| {
            const text = try allocator.dupe(u8, completed.text);
            errdefer allocator.free(text);
            const transcript = try allocator.dupe(u8, transcript_path);
            return .{
                .outcome = .completed,
                .text = OwnedSlice(u8).initOwned(text),
                .transcript = OwnedSlice(u8).initOwned(transcript),
                .messages_before = completed.messages_before,
                .tokens_before = completed.tokens_before,
                .tokens_after = completed.tokens_after,
            };
        },
        .cancelled => return .{ .outcome = .cancelled },
        .failed => |message| return .{ .outcome = .failed, .message = OwnedSlice(u8).initOwned(try allocator.dupe(u8, message)) },
    }
}

fn notifyToolApproval(ctx: ?*anyopaque, request: agent.ToolApprovalRequest, allocator: std.mem.Allocator) void {
    const approval_ctx: *ApprovalContext = @ptrCast(@alignCast(ctx.?));
    if (approval_ctx.original_ui_callback) |callback| {
        callback(approval_ctx.original_ui_ctx, request, allocator);
    }

    const runtime = approval_ctx.runtime;
    runtime.push(.{ .tool_approval_requested = .{
        .tool_call_id = runtime.dupeOwned(request.tool_call_id) catch OwnedSlice(u8).initBorrowed(""),
        .tool_name = runtime.dupeOwned(request.tool_name) catch OwnedSlice(u8).initBorrowed(""),
        .args_json = runtime.dupeOwned(request.args_json) catch OwnedSlice(u8).initBorrowed(""),
    } });
}

fn executeTuiToolProtocol(
    ctx: ?*anyopaque,
    tool_call_id: []const u8,
    tool_name: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?agent.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!agent.AgentToolResult {
    const runtime: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    var result = try runtime.tool_protocol.executeWithOverride(
        tool_call_id,
        tool_name,
        args_json,
        cancel_token,
        on_update_ctx,
        on_update,
        runtime.tool_protocol_override_ctx,
        runtime.tool_protocol_override_fn,
        allocator,
    );
    if (std.mem.eql(u8, tool_name, "Shell")) {
        if (result.getDetailsJson()) |details| runtime.adoptReportedWorkingDirectory(args_json, details, &result);
    }
    return result;
}

fn rewriteToolArgs(
    ctx: ?*anyopaque,
    tool_name: []const u8,
    args_json: []const u8,
    allocator: std.mem.Allocator,
) anyerror!?[]u8 {
    const runtime: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    return runtime.rewriteWorkspaceRoot(tool_name, args_json, allocator);
}

fn directoryOpens(path: []const u8) bool {
    var dir = std.Io.Dir.openDirAbsolute(compat.fs.defaultIo(), path, .{}) catch return false;
    dir.close(compat.fs.defaultIo());
    return true;
}

fn detailsObject(object: std.json.ObjectMap) ?std.json.ObjectMap {
    if (object.get("working_directory_observed") != null) return object;
    const nested = object.get("details") orelse return null;
    if (nested != .object) return null;
    return nested.object;
}

fn startDirectoryOf(allocator: std.mem.Allocator, args_json: []const u8) ?[]u8 {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, args_json, .{}) catch return null;
    defer parsed.deinit();
    if (parsed.value != .object) return null;
    const value = parsed.value.object.get("workspace_root") orelse return null;
    if (value != .string) return null;
    return allocator.dupe(u8, value.string) catch null;
}

fn hasAbsolutePathArgument(obj: std.json.ObjectMap) bool {
    for ([_][]const u8{ "path", "file_path", "target_path", "cwd" }) |key| {
        const value = obj.get(key) orelse continue;
        if (value == .string and std.Io.Dir.path.isAbsolute(value.string)) return true;
    }
    return false;
}

fn approveTool(ctx: ?*anyopaque, request: agent.ToolApprovalRequest) agent.ToolApprovalDecision {
    const approval_ctx: *ApprovalContext = @ptrCast(@alignCast(ctx.?));
    if (approval_ctx.original_callback) |callback| {
        switch (callback(approval_ctx.original_ctx, request)) {
            .approve, .approve_always => {},
            .reject, .reject_always => return .reject,
        }
    }
    const approval_request = ToolApprovalRequest{
        .tool_call_id = request.tool_call_id,
        .tool_name = request.tool_name,
        .args_json = request.args_json,
    };
    if (approval_ctx.callback) |callback| {
        return switch (callback(approval_ctx.callback_ctx, approval_request)) {
            .approve => .approve,
            .reject => .reject,
            .approve_always => .approve_always,
            .reject_always => .reject_always,
        };
    }
    return .approve;
}

fn sessionStart(ctx: ?*anyopaque) anyerror!void {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    try self.start();
}

fn sessionResume(ctx: ?*anyopaque) anyerror!void {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    try self.resumeSession();
}

fn sessionRequestCompaction(ctx: ?*anyopaque, focus: []const u8) anyerror!void {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    const local = &(self.local_agent orelse return error.RuntimeNotStarted);
    try local.requestCompaction(focus);
}

fn sessionTakeCompactionRequest(ctx: ?*anyopaque, allocator: std.mem.Allocator) anyerror!?[]u8 {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    const local = &(self.local_agent orelse return null);
    const focus = local.takeCompactionRequest() orelse return null;
    defer local._allocator.free(focus);
    return try allocator.dupe(u8, focus);
}

fn sessionCompact(ctx: ?*anyopaque, options: CompactOptions) anyerror!void {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    try self.compact(options);
}

fn sessionHistory(ctx: ?*anyopaque) []const ai_types.Message {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    return self.history();
}

fn sessionCancel(ctx: ?*anyopaque) void {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    self.cancel();
}

fn sessionSubmitTurn(ctx: ?*anyopaque, text: []const u8) anyerror!void {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    try self.submitTurn(text);
}

fn sessionSteer(ctx: ?*anyopaque, text: []const u8) anyerror!void {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    try self.steer(text);
}

fn sessionFollowUp(ctx: ?*anyopaque, text: []const u8) anyerror!void {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    try self.followUp(text);
}

fn sessionClearQueuedMessages(ctx: ?*anyopaque) void {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    self.clearQueuedMessages();
}

fn sessionQueuedCounts(ctx: ?*anyopaque) QueuedCounts {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    return self.queuedCounts();
}

fn sessionSteersConsumed(ctx: ?*anyopaque) u64 {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    return self.steersConsumedCount();
}

fn sessionCanSteer(ctx: ?*anyopaque) bool {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    return self.canSteer();
}

fn sessionSwitchModel(ctx: ?*anyopaque, model_id: []const u8) anyerror!void {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    try self.switchModel(model_id);
}

fn sessionSwitchModelExact(ctx: ?*anyopaque, model: ai_types.Model) anyerror!void {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    try self.switchModelExact(model);
}

fn sessionCurrentModel(ctx: ?*anyopaque) ?ai_types.Model {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    return self.currentModel();
}

fn sessionDecideToolApproval(ctx: ?*anyopaque, tool_call_id: []const u8, decision: ToolApprovalDecision) anyerror!void {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    try self.decideToolApproval(tool_call_id, decision);
}

fn sessionStreamEvents(ctx: ?*anyopaque) *TuiEventStream {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    return self.streamEvents();
}

const test_model_a = ai_types.Model{
    .id = "model-a",
    .name = "Model A",
    .api = "test-api",
    .provider = "test-provider",
    .base_url = "https://example.invalid",
    .reasoning = false,
    .input = &.{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 8192,
    .max_tokens = 1024,
};

const test_model_b = ai_types.Model{
    .id = "model-b",
    .name = "Model B",
    .api = "test-api",
    .provider = "test-provider",
    .base_url = "https://example.invalid",
    .reasoning = false,
    .input = &.{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 8192,
    .max_tokens = 1024,
};

const MockProtocolCtx = struct {
    call_count: usize = 0,
    last_model_id: []const u8 = "",
    last_thinking_level: ai_types.ThinkingLevel = .off,
    last_message_count: usize = 0,
    saw_workspace_prompt: bool = false,
    wait_for_cancel: bool = false,
    flood_count: usize = 0,
    deliver_flood_second: usize = 0,
    tool_first: bool = false,
    wait_after_tool_first: bool = false,
    wait_before_text_first: bool = false,
    tool_name: []const u8 = "demo_tool",
    force_error: bool = false,
    provider_error_message: []const u8 = "",
    reply_text: []const u8 = "hello",
};

fn makeAssistantMessage(allocator: std.mem.Allocator, model: ai_types.Model, content: []const ai_types.AssistantContent, stop_reason: ai_types.StopReason) !ai_types.AssistantMessage {
    const blocks = try allocator.alloc(ai_types.AssistantContent, content.len);
    var initialized: usize = 0;
    errdefer ai_types.deinitAssistantContent(allocator, blocks[0..initialized]);

    for (content, 0..) |block, i| {
        blocks[i] = switch (block) {
            .text => |t| .{ .text = .{
                .text = try allocator.dupe(u8, t.text),
                .text_signature = if (t.text_signature) |s| try allocator.dupe(u8, s) else null,
            } },
            .thinking => |t| .{ .thinking = .{
                .thinking = try allocator.dupe(u8, t.thinking),
                .thinking_signature = if (t.thinking_signature) |s| try allocator.dupe(u8, s) else null,
            } },
            .tool_call => |tc| .{ .tool_call = .{
                .id = try allocator.dupe(u8, tc.id),
                .name = try allocator.dupe(u8, tc.name),
                .arguments_json = try allocator.dupe(u8, tc.arguments_json),
                .thought_signature = if (tc.thought_signature) |s| try allocator.dupe(u8, s) else null,
            } },
            .image => |img| .{ .image = .{
                .data = try allocator.dupe(u8, img.data),
                .mime_type = try allocator.dupe(u8, img.mime_type),
            } },
        };
        initialized += 1;
    }

    return .{
        .content = blocks,
        .api = model.api,
        .provider = model.provider,
        .model = model.id,
        .usage = .{},
        .stop_reason = stop_reason,
        .timestamp = 0,
    };
}

fn emptyAssistantMessage(model: ai_types.Model, stop_reason: ai_types.StopReason) ai_types.AssistantMessage {
    return .{
        .content = &.{},
        .api = model.api,
        .provider = model.provider,
        .model = model.id,
        .usage = .{},
        .stop_reason = stop_reason,
        .timestamp = 0,
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
    stream.complete(result_message);
}

fn pushTextResponse(stream: *event_stream.AssistantMessageEventStream, allocator: std.mem.Allocator, model: ai_types.Model, text: []const u8) !void {
    const partial = emptyAssistantMessage(model, .stop);
    try stream.push(.{ .start = .{ .partial = partial } });
    try stream.push(.{ .text_delta = .{ .content_index = 0, .delta = text, .partial = partial } });

    const content = [_]ai_types.AssistantContent{.{ .text = .{ .text = text } }};
    try pushDoneAndComplete(stream, allocator, model, &content, .stop);
}

fn mockStream(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: agent.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream.AssistantMessageEventStream {
    const mock: *MockProtocolCtx = @ptrCast(@alignCast(ctx.?));
    mock.call_count += 1;
    mock.last_model_id = model.id;
    mock.last_thinking_level = options.thinking_level;
    mock.last_message_count = context.messages.len;
    const system_prompt = context.system_prompt.slice();
    mock.saw_workspace_prompt = std.mem.indexOf(u8, system_prompt, "Default workspace root: /tmp/makai-workspace") != null and
        std.mem.indexOf(u8, system_prompt, "`workspace_root`") != null;

    const stream = try allocator.create(event_stream.AssistantMessageEventStream);
    stream.* = event_stream.AssistantMessageEventStream.init(allocator);

    if (mock.force_error) {
        stream.completeWithError("forced provider error");
        return stream;
    }

    if (mock.provider_error_message.len > 0) {
        var event_message = emptyAssistantMessage(model, .@"error");
        event_message.error_message = OwnedSlice(u8).initBorrowed(mock.provider_error_message);
        var result_message = emptyAssistantMessage(model, .@"error");
        result_message.error_message = OwnedSlice(u8).initBorrowed(mock.provider_error_message);
        try stream.push(.{ .@"error" = .{ .reason = .@"error", .err = event_message } });
        stream.complete(result_message);
        return stream;
    }

    if (mock.wait_for_cancel) {
        if (options.cancel_token) |token| {
            var waits: usize = 0;
            while (!token.isCancelled() and waits < 100) : (waits += 1) {
                std.testing.io.sleep(.fromNanoseconds(1 * std.time.ns_per_ms), .boot) catch {};
            }
        }
        try stream.push(.{ .done = .{ .reason = .aborted, .message = emptyAssistantMessage(model, .aborted) } });
        stream.complete(emptyAssistantMessage(model, .aborted));
        return stream;
    }

    if (mock.flood_count > 0) {
        const partial = emptyAssistantMessage(model, .stop);
        try stream.push(.{ .start = .{ .partial = partial } });
        var i: usize = 0;
        while (i < mock.flood_count) : (i += 1) {
            try stream.push(.{ .text_delta = .{ .content_index = 0, .delta = "x", .partial = partial } });
            if (stream.poll()) |_| {}
        }
        const content = [_]ai_types.AssistantContent{.{ .text = .{ .text = "done" } }};
        try pushDoneAndComplete(stream, allocator, model, &content, .stop);
        return stream;
    }

    if (mock.deliver_flood_second > 0 and mock.call_count == 2) {
        const partial = emptyAssistantMessage(model, .stop);
        try stream.push(.{ .start = .{ .partial = partial } });
        var i: usize = 0;
        while (i < mock.deliver_flood_second) : (i += 1) {
            try stream.push(.{ .text_delta = .{ .content_index = 0, .delta = "x", .partial = partial } });
        }
        const content = [_]ai_types.AssistantContent{.{ .text = .{ .text = "done" } }};
        try pushDoneAndComplete(stream, allocator, model, &content, .stop);
        return stream;
    }

    if (mock.tool_first and mock.call_count == 1) {
        if (mock.wait_after_tool_first) {
            var waits: usize = 0;
            while (waits < 50) : (waits += 1) {
                std.testing.io.sleep(.fromNanoseconds(1 * std.time.ns_per_ms), .boot) catch {};
            }
        }
        const content = [_]ai_types.AssistantContent{.{ .tool_call = .{ .id = "call-1", .name = mock.tool_name, .arguments_json = "{}" } }};
        try stream.push(.{ .start = .{ .partial = emptyAssistantMessage(model, .tool_use) } });
        try pushDoneAndComplete(stream, allocator, model, &content, .tool_use);
        return stream;
    }

    if (mock.wait_before_text_first and mock.call_count == 1) {
        var waits: usize = 0;
        while (waits < 50) : (waits += 1) {
            std.testing.io.sleep(.fromNanoseconds(1 * std.time.ns_per_ms), .boot) catch {};
        }
    }

    try pushTextResponse(stream, allocator, model, mock.reply_text);
    return stream;
}

fn makeProtocol(ctx: *MockProtocolCtx) agent.ProtocolClient {
    return .{ .stream_fn = mockStream, .ctx = ctx };
}

fn collectUntilEnd(tui_session: *TuiSession, saw_turn_start: *bool, saw_message_start: *bool, saw_text_delta: *bool, saw_message_end: *bool, saw_turn_end: *bool) void {
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .turn_start => saw_turn_start.* = true,
            .message_start => |payload| {
                if (payload.role == .assistant) saw_message_start.* = true;
            },
            .text_delta => saw_text_delta.* = true,
            .message_end => |payload| {
                if (payload.role == .assistant) saw_message_end.* = true;
            },
            .turn_end => saw_turn_end.* = true,
            .agent_end => break,
            else => {},
        }
    }
}

test "a context window is a whole token count, optionally scaled, and nothing else" {
    try std.testing.expectEqual(@as(u32, 1_000_000), try parseContextWindow("1000000"));
    try std.testing.expectEqual(@as(u32, 272_000), try parseContextWindow(" 272k "));
    try std.testing.expectEqual(@as(u32, 1_000_000), try parseContextWindow("1M"));
    try std.testing.expectEqual(@as(u32, 1_000), try parseContextWindow("1k"));
    try std.testing.expectEqual(@as(u32, 7), try parseContextWindow("7"));
    for ([_][]const u8{ "", "  ", "k", "0", "0k", "-1", "1.5m", "1e6", "1 000", "99999999999m", "1g", "1kk" }) |bad| {
        try std.testing.expectError(error.NotATokenCount, parseContextWindow(bad));
    }
}

test "the window in effect is the model's own until a session sets one" {
    const models = [_]ai_types.Model{ test_model_a, test_model_b };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &models });
    defer runtime.deinit();

    try std.testing.expectEqual(@as(u64, 8192), runtime.contextWindow());
    try std.testing.expectEqual(@as(u32, 8192), runtime.currentModel().?.context_window);

    try runtime.setContextWindow(4_000);
    try std.testing.expectEqual(@as(u64, 4_000), runtime.contextWindow());
    try std.testing.expectEqual(@as(u32, 4_000), runtime.currentModel().?.context_window);
    try std.testing.expectEqual(@as(u32, 8192), runtime.availableModels()[0].context_window);

    try runtime.switchModel("model-b");
    try std.testing.expectEqual(@as(u64, 4_000), runtime.contextWindow());
    try runtime.setContextWindow(null);
    try std.testing.expectEqual(@as(u32, test_model_b.context_window), runtime.currentModel().?.context_window);
}

test "the model handed to the agent carries the window in effect" {
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &models });
    defer runtime.deinit();

    try std.testing.expectEqual(@as(u32, 8192), runtime.effectiveModel(models[0]).context_window);
    try runtime.setContextWindow(4_000);
    try std.testing.expectEqual(@as(u32, 4_000), runtime.effectiveModel(models[0]).context_window);
    try std.testing.expectEqual(@as(u32, 8192), models[0].context_window);
}

test "a session's window set before the first turn survives the agent starting" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .context_window = 4_000,
        .run_async = false,
    });
    defer runtime.deinit();
    try runtime.start();
    defer runtime.stop();

    try std.testing.expectEqual(@as(u64, 4_000), runtime.contextWindow());
    try runtime.setContextWindow(6_000);
    try std.testing.expectEqual(@as(u64, 6_000), runtime.contextWindow());
}

const wide_ceiling_model = ai_types.Model{
    .id = "gpt-5-codex",
    .name = "GPT-5 Codex",
    .api = "openai-responses",
    .provider = "openai",
    .base_url = "https://example.invalid",
    .reasoning = true,
    .input = &.{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 128_000,
    .max_tokens = 16_384,
};

const narrow_ceiling_model = ai_types.Model{
    .id = "kimi-k2.7-code",
    .name = "Kimi K2.7 Code",
    .api = "openai-completions",
    .provider = "kimi",
    .base_url = "https://example.invalid",
    .reasoning = false,
    .input = &.{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 262_144,
    .max_tokens = 16_384,
};

test "a session's window the model in effect cannot take is dropped, and named" {
    const models = [_]ai_types.Model{ wide_ceiling_model, narrow_ceiling_model };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &models, .context_window = 1_000_000 });
    defer runtime.deinit();

    try std.testing.expectEqual(@as(u64, 1_000_000), runtime.contextWindow());
    try std.testing.expect(runtime.contextWindowRefused() == null);

    try runtime.switchModel("kimi-k2.7-code");

    try std.testing.expectEqual(@as(u64, 262_144), runtime.contextWindow());
    try std.testing.expectEqual(@as(?u32, 1_000_000), runtime.takeContextWindowRefused());
    try std.testing.expect(runtime.contextWindowRefused() == null);
}

test "a model switch suspends an oversized context window and restores it when switching back" {
    const models = [_]ai_types.Model{ wide_ceiling_model, narrow_ceiling_model };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &models });
    defer runtime.deinit();

    try runtime.setContextWindow(1_000_000);
    try runtime.switchModel("kimi-k2.7-code");
    try std.testing.expectEqual(@as(u64, 262_144), runtime.contextWindow());
    try std.testing.expectEqual(@as(?u32, 1_000_000), runtime.takeContextWindowRefused());
    try std.testing.expectEqual(@as(?u32, 1_000_000), runtime.suspended_context_window);

    try runtime.switchModel("gpt-5-codex");
    try std.testing.expectEqual(@as(u64, 1_000_000), runtime.contextWindow());
    try std.testing.expectEqual(@as(?u32, 1_000_000), runtime.contextWindowOverride());
    try std.testing.expect(runtime.suspended_context_window == null);
}

test "a startup window above the selected model ceiling is suspended for a later switch back" {
    const models = [_]ai_types.Model{ narrow_ceiling_model, wide_ceiling_model };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &models, .context_window = 1_000_000 });
    defer runtime.deinit();

    try std.testing.expectEqual(@as(u64, 262_144), runtime.contextWindow());
    try std.testing.expectEqual(@as(?u32, 1_000_000), runtime.takeContextWindowRefused());
    try runtime.switchModel("gpt-5-codex");
    try std.testing.expectEqual(@as(u64, 1_000_000), runtime.contextWindow());
    try std.testing.expectEqual(@as(?u32, 1_000_000), runtime.contextWindowOverride());
}

test "a startup window above the ceiling is dropped before the first turn" {
    const models = [_]ai_types.Model{narrow_ceiling_model};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &models, .context_window = 1_000_000 });
    defer runtime.deinit();

    try std.testing.expectEqual(@as(u64, 262_144), runtime.contextWindow());
    try std.testing.expectEqual(@as(?u32, 1_000_000), runtime.takeContextWindowRefused());
}

test "a window the model in effect can take survives a switch to another that can" {
    const models = [_]ai_types.Model{ wide_ceiling_model, narrow_ceiling_model };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &models });
    defer runtime.deinit();

    try runtime.setContextWindow(200_000);
    try runtime.switchModel("kimi-k2.7-code");

    try std.testing.expectEqual(@as(u64, 200_000), runtime.contextWindow());
    try std.testing.expect(runtime.contextWindowRefused() == null);
}

test "runtime registers default local tools and allows overrides" {
    var mock = MockProtocolCtx{};
    const replacement = agent.AgentTool{ .label = "Wrapped Shell", .name = "Shell", .description = "Wrapped shell tool", .parameters_schema_json = "{}", .execute = demoTool };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .tools = &.{replacement}, .run_async = false });
    defer runtime.deinit();
    try std.testing.expect(runtime.tool_registry.resolve("Shell") != null);
    try std.testing.expect(runtime.tool_registry.resolve("Read") != null);
    try std.testing.expectEqualStrings("Wrapped Shell", runtime.tool_registry.resolve("Shell").?.label);
    try std.testing.expect(runtime.original_tools.len >= 4);
}

test "runtime submit turn emits normalized events" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("hi");
    if (runtime.local_agent) |*local| local.waitForIdle();

    var saw_turn_start = false;
    var saw_message_start = false;
    var saw_text_delta = false;
    var saw_message_end = false;
    var saw_turn_end = false;
    collectUntilEnd(&tui_session, &saw_turn_start, &saw_message_start, &saw_text_delta, &saw_message_end, &saw_turn_end);

    try std.testing.expect(saw_turn_start);
    try std.testing.expect(saw_message_start);
    try std.testing.expect(saw_text_delta);
    try std.testing.expect(saw_message_end);
    try std.testing.expect(saw_turn_end);
}

test "local runtime includes startup cwd as default workspace root in provider prompt" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .workspace_root = "/tmp/makai-workspace",
        .run_async = false,
    });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("pwd");
    if (runtime.local_agent) |*local| local.waitForIdle();

    try std.testing.expect(mock.saw_workspace_prompt);
}

test "local runtime surfaces provider error message details" {
    var mock = MockProtocolCtx{ .provider_error_message = "provider rejected request: missing workspace_root" };
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try std.testing.expectError(error.AgentLoopFailed, tui_session.submitTurn("hi"));
    if (runtime.local_agent) |*local| local.waitForIdle();

    var saw_detail = false;
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .@"error" and std.mem.eql(u8, ev.@"error".message.slice(), "provider rejected request: missing workspace_root")) {
            saw_detail = true;
        }
    }
    try std.testing.expect(saw_detail);
}

test "runtime cancel emits cancelled agent_end" {
    var mock = MockProtocolCtx{ .wait_for_cancel = true };
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("hi");
    tui_session.cancel();
    if (runtime.local_agent) |*local| local.waitForIdle();

    var saw_cancelled = false;
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .agent_end) {
            saw_cancelled = ev.agent_end.reason == .cancelled;
            break;
        }
    }
    try std.testing.expect(saw_cancelled);
}

const ApprovalCtx = struct { decision: ToolApprovalDecision, calls: usize = 0 };
const OriginalApprovalCtx = struct { decision: agent.ToolApprovalDecision, calls: usize = 0 };
const OriginalApprovalUiCtx = struct { calls: usize = 0 };

fn approvalCallback(ctx: ?*anyopaque, request: ToolApprovalRequest) ToolApprovalDecision {
    _ = request;
    const approval: *ApprovalCtx = @ptrCast(@alignCast(ctx.?));
    approval.calls += 1;
    return approval.decision;
}

fn originalApprovalCallback(ctx: ?*anyopaque, request: agent.ToolApprovalRequest) agent.ToolApprovalDecision {
    _ = request;
    const approval: *OriginalApprovalCtx = @ptrCast(@alignCast(ctx.?));
    approval.calls += 1;
    return approval.decision;
}

fn originalApprovalUiCallback(ctx: ?*anyopaque, request: agent.ToolApprovalRequest, allocator: std.mem.Allocator) void {
    _ = request;
    _ = allocator;
    const approval: *OriginalApprovalUiCtx = @ptrCast(@alignCast(ctx.?));
    approval.calls += 1;
}

fn demoTool(
    tool_call_id: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?agent.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!agent.AgentToolResult {
    _ = tool_call_id;
    _ = args_json;
    _ = cancel_token;
    _ = on_update_ctx;
    _ = on_update;
    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    content[0] = .{ .text = .{ .text = try allocator.dupe(u8, "tool ok") } };
    return .{ .content = OwnedSlice(ai_types.UserContentPart).initOwned(content) };
}

const ContextToolCtx = struct { calls: usize = 0 };

fn contextOnlyTool(
    ctx: ?*anyopaque,
    tool_call_id: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?agent.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!agent.AgentToolResult {
    _ = tool_call_id;
    _ = args_json;
    _ = cancel_token;
    _ = on_update_ctx;
    _ = on_update;
    const state: *ContextToolCtx = @ptrCast(@alignCast(ctx.?));
    state.calls += 1;
    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    content[0] = .{ .text = .{ .text = try allocator.dupe(u8, "context tool ok") } };
    return .{ .content = OwnedSlice(ai_types.UserContentPart).initOwned(content) };
}

fn cdReportingTool(
    ctx: ?*anyopaque,
    tool_call_id: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?agent.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!agent.AgentToolResult {
    _ = ctx;
    _ = tool_call_id;
    _ = args_json;
    _ = cancel_token;
    _ = on_update_ctx;
    _ = on_update;
    const details = try std.json.Stringify.valueAlloc(allocator, .{
        .working_directory = "/tmp/makai-workspace/sub",
        .working_directory_observed = true,
    }, .{});
    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    content[0] = .{ .text = .{ .text = try allocator.dupe(u8, "moved") } };
    return .{
        .content = OwnedSlice(ai_types.UserContentPart).initOwned(content),
        .details_json = OwnedSlice(u8).initOwned(details),
    };
}

test "runtime wrapper preserves context-aware tool execution" {
    var context = ContextToolCtx{};
    const tools = [_]agent.AgentTool{.{
        .label = "Context",
        .name = "context_tool",
        .description = "Context tool",
        .parameters_schema_json = "{}",
        .execute = demoTool,
        .runtime_ctx = &context,
        .runtime_execute = contextOnlyTool,
    }};
    const models = [_]ai_types.Model{test_model_a};
    var mock = MockProtocolCtx{ .tool_first = true, .tool_name = "context_tool" };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .tools = &tools,
        .run_async = false,
    });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("use context tool");
    if (runtime.local_agent) |*local| local.waitForIdle();

    var saw_tool_end = false;
    while (tui_session.waitEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .tool_execution_end => saw_tool_end = !ev.tool_execution_end.is_error,
            .agent_end => break,
            else => {},
        }
    }
    try std.testing.expectEqual(@as(usize, 1), context.calls);
    try std.testing.expect(saw_tool_end);
}

test "MCP bridge exec context address remains stable in TUI runtime" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const script = "python3 -u -c 'import json,sys\n" ++
        "for line in sys.stdin:\n" ++
        " msg=json.loads(line); method=msg.get(\"method\")\n" ++
        " if method==\"initialize\": print(json.dumps({\"jsonrpc\":\"2.0\",\"id\":msg[\"id\"],\"result\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{},\"serverInfo\":{\"name\":\"fake\",\"version\":\"1\"}}}), flush=True)\n" ++
        " elif method==\"tools/list\": print(json.dumps({\"jsonrpc\":\"2.0\",\"id\":msg[\"id\"],\"result\":{\"tools\":[{\"name\":\"echo\",\"description\":\"Echo\",\"inputSchema\":{\"type\":\"object\"}}]}}), flush=True)'";
    const script_json = try std.json.Stringify.valueAlloc(std.testing.allocator, script, .{});
    defer std.testing.allocator.free(script_json);
    const config_json = try std.fmt.allocPrint(std.testing.allocator, "[{{\"name\":\"mock\",\"command\":\"/bin/sh\",\"args\":[\"-c\",{s}]}}]", .{script_json});
    defer std.testing.allocator.free(config_json);
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .mcp_config_json = config_json });
    defer runtime.deinit();
    const bridge = runtime.mcp_bridge orelse return error.MissingBridge;
    for (bridge.tools.items) |record| {
        try std.testing.expect(record.exec_ctx.bridge.* == bridge);
    }
}

test "tool approval approve and reject paths emit tool events" {
    const tools = [_]agent.AgentTool{.{
        .label = "Demo",
        .name = "demo_tool",
        .description = "Demo tool",
        .parameters_schema_json = "{}",
        .execute = demoTool,
    }};
    const models = [_]ai_types.Model{test_model_a};

    var approve_mock = MockProtocolCtx{ .tool_first = true };
    var approve_ctx = ApprovalCtx{ .decision = .approve };
    var approve_runtime = try TuiRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&approve_mock),
        .models = &models,
        .tools = &tools,
        .tool_approval_ctx = &approve_ctx,
        .tool_approval_callback = approvalCallback,
        .permission_mode = .ask,
        .run_async = false,
    });
    defer approve_runtime.deinit();
    var approve_session = approve_runtime.createSession();
    try approve_session.start();
    try approve_session.submitTurn("use tool");
    if (approve_runtime.local_agent) |*local| local.waitForIdle();

    var approve_saw_approval = false;
    var approve_saw_tool_end = false;
    while (approve_session.waitEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .tool_approval_requested => approve_saw_approval = true,
            .tool_execution_end => approve_saw_tool_end = !ev.tool_execution_end.is_error,
            .agent_end => break,
            else => {},
        }
    }
    try std.testing.expectEqual(@as(usize, 1), approve_ctx.calls);
    try std.testing.expect(approve_saw_approval);
    try std.testing.expect(approve_saw_tool_end);

    var reject_mock = MockProtocolCtx{ .tool_first = true };
    var reject_ctx = ApprovalCtx{ .decision = .reject };
    var reject_runtime = try TuiRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&reject_mock),
        .models = &models,
        .tools = &tools,
        .tool_approval_ctx = &reject_ctx,
        .tool_approval_callback = approvalCallback,
        .permission_mode = .ask,
        .run_async = false,
    });
    defer reject_runtime.deinit();
    var reject_session = reject_runtime.createSession();
    try reject_session.start();
    try reject_session.submitTurn("use tool");
    if (reject_runtime.local_agent) |*local| local.waitForIdle();

    var reject_saw_error_tool = false;
    while (reject_session.waitEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .tool_execution_end => reject_saw_error_tool = ev.tool_execution_end.is_error,
            .agent_end => break,
            else => {},
        }
    }
    try std.testing.expectEqual(@as(usize, 1), reject_ctx.calls);
    try std.testing.expect(reject_saw_error_tool);
}

test "runtime idle steering resumes immediately" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("first");

    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .agent_end) break;
    }
    try std.testing.expectEqual(@as(usize, 1), mock.call_count);

    try tui_session.steer("steer after idle");
    try std.testing.expectEqual(@as(usize, 2), mock.call_count);
    try std.testing.expectEqual(@as(usize, 0), tui_session.queuedCounts().steering);

    var saw_steering_user = false;
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .message_end => |payload| {
                if (payload.role == .user and std.mem.eql(u8, payload.text.slice(), "steer after idle")) {
                    saw_steering_user = true;
                }
            },
            .agent_end => break,
            else => {},
        }
    }
    try std.testing.expect(saw_steering_user);
}

test "runtime tags auto-resumed steer prompt with steering provenance" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("first");
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .agent_end) break;
    }

    try tui_session.steer("steer after idle");

    var tagged_user_message_end = false;
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .message_end => |payload| {
                if (payload.role == .user and payload.steering) tagged_user_message_end = true;
            },
            .agent_end => break,
            else => {},
        }
    }
    try std.testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
    try std.testing.expect(tagged_user_message_end);
}

test "runtime tags async auto-resumed steer prompt with steering provenance" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("first");
    if (runtime.local_agent) |*local| local.waitForIdle();
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .agent_end) break;
    }

    try tui_session.steer("steer after idle");
    if (runtime.local_agent) |*local| local.waitForIdle();

    var tagged_user_message_end = false;
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .message_end => |payload| {
                if (payload.role == .user and payload.steering) tagged_user_message_end = true;
            },
            else => {},
        }
    }
    try std.testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
    try std.testing.expect(tagged_user_message_end);
}

test "runtime tags post-tool consumed steer and feeds it to the model" {
    var mock = MockProtocolCtx{ .tool_first = true, .wait_after_tool_first = true };
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("first");
    try tui_session.steer("steer mid tool");

    if (runtime.local_agent) |*local| local.waitForIdle();

    try std.testing.expectEqual(@as(usize, 2), mock.call_count);
    try std.testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
    try std.testing.expectEqual(@as(usize, 4), mock.last_message_count);

    var tagged_user_message_end = false;
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .message_end => |payload| {
                if (payload.role == .user and std.mem.eql(u8, payload.text.slice(), "steer mid tool")) {
                    tagged_user_message_end = payload.steering;
                }
            },
            else => {},
        }
    }
    try std.testing.expect(tagged_user_message_end);
}

const PromptEndHold = struct {
    agent: *agent.Agent,
    held: bool = false,

    fn onEvent(ctx: ?*anyopaque, event: agent.AgentEvent) void {
        const self: *PromptEndHold = @ptrCast(@alignCast(ctx.?));
        if (self.held or event != .message_end or event.message_end.message != .user) return;
        self.held = true;
        var waits: usize = 0;
        while (self.agent.steeringConsumedCount() == 0 and waits < 5000) : (waits += 1) {
            std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
        }
    }
};

test "a prompt whose end is handled after the loop took a steer is not tagged as the steer" {
    var mock = MockProtocolCtx{ .tool_first = true, .wait_after_tool_first = true };
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    const local = &runtime.local_agent.?;
    var hold = PromptEndHold{ .agent = local };
    local.unsubscribeWithContext(&runtime, TuiRuntime.onAgentEvent);
    local.subscribeWithContext(&hold, PromptEndHold.onEvent);
    local.subscribeWithContext(&runtime, TuiRuntime.onAgentEvent);
    try tui_session.submitTurn("first");
    try tui_session.steer("steer mid tool");
    local.waitForIdle();

    try std.testing.expect(hold.held);
    try std.testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
    var prompt_tagged: ?bool = null;
    var steer_tagged: ?bool = null;
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .message_end => |payload| if (payload.role == .user) {
                if (std.mem.eql(u8, payload.text.slice(), "first")) prompt_tagged = payload.steering;
                if (std.mem.eql(u8, payload.text.slice(), "steer mid tool")) steer_tagged = payload.steering;
            },
            else => {},
        }
    }
    try std.testing.expectEqual(@as(?bool, false), prompt_tagged);
    try std.testing.expectEqual(@as(?bool, true), steer_tagged);
}

test "runtime active steering continues after plain assistant stop" {
    var mock = MockProtocolCtx{ .wait_before_text_first = true };
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("first");
    try tui_session.steer("steer during response");

    if (runtime.local_agent) |*local| local.waitForIdle();

    try std.testing.expectEqual(@as(usize, 2), mock.call_count);
    try std.testing.expectEqual(@as(usize, 0), tui_session.queuedCounts().steering);

    var saw_steering_user = false;
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .message_end => |payload| {
                if (payload.role == .user and std.mem.eql(u8, payload.text.slice(), "steer during response")) {
                    saw_steering_user = true;
                }
            },
            else => {},
        }
    }
    try std.testing.expect(saw_steering_user);
}

test "runtime follow-up runs once the turn stops and is not tagged as steering" {
    var mock = MockProtocolCtx{ .wait_before_text_first = true };
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("first");
    try tui_session.followUp("queued follow-up");

    if (runtime.local_agent) |*local| local.waitForIdle();

    try std.testing.expectEqual(@as(usize, 2), mock.call_count);
    try std.testing.expectEqual(@as(usize, 0), tui_session.queuedCounts().follow_up);
    try std.testing.expectEqual(@as(u64, 0), runtime.steersConsumedCount());

    var follow_up_ends: usize = 0;
    var follow_up_tagged = false;
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .message_end => |payload| {
                if (payload.role == .user and std.mem.eql(u8, payload.text.slice(), "queued follow-up")) {
                    follow_up_ends += 1;
                    follow_up_tagged = follow_up_tagged or payload.steering;
                }
            },
            else => {},
        }
    }
    try std.testing.expectEqual(@as(usize, 1), follow_up_ends);
    try std.testing.expect(!follow_up_tagged);
}

fn compactionEndPayloadProbe(allocator: std.mem.Allocator) !void {
    const completed = agent.compaction.Result{ .completed = .{ .text = @constCast("summary"), .messages_before = 3, .tokens_before = 10, .tokens_after = 2, .head_truncated = false } };
    var completed_event = TuiEvent{ .compaction_end = try compactionEndPayload(allocator, "/sessions/s1/compaction-1.jsonl", &completed) };
    completed_event.deinit(allocator);
    const failed = agent.compaction.Result{ .failed = @constCast("overloaded") };
    var failed_event = TuiEvent{ .compaction_end = try compactionEndPayload(allocator, "", &failed) };
    failed_event.deinit(allocator);
}

test "compactionEndPayload survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, compactionEndPayloadProbe, .{});
}

const CompactionSeen = struct {
    started: bool = false,
    outcome: ?TuiEvent.CompactionOutcome = null,
    text_has_summary: bool = false,
    transcript_matches: bool = false,
    message_has_error: bool = false,
    messages_before: u64 = 0,
};

fn collectCompaction(tui_session: *TuiSession, expected_transcript: []const u8) CompactionSeen {
    var seen = CompactionSeen{};
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .compaction_start => seen.started = true,
            .compaction_end => |payload| {
                seen.outcome = payload.outcome;
                seen.text_has_summary = std.mem.indexOf(u8, payload.text.slice(), "\nkept state\n") != null;
                seen.transcript_matches = std.mem.eql(u8, payload.transcript.slice(), expected_transcript);
                seen.message_has_error = std.mem.indexOf(u8, payload.message.slice(), "overloaded") != null;
                seen.messages_before = payload.messages_before;
            },
            else => {},
        }
    }
    return seen;
}

test "runtime compaction swaps in the model's summary and reports it with its transcript" {
    var mock = MockProtocolCtx{};
    var wide = test_model_a;
    wide.context_window = 200_000;
    const models = [_]ai_types.Model{wide};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("first");
    if (runtime.local_agent) |*local| local.waitForIdle();
    _ = collectCompaction(&tui_session, "");

    mock.reply_text = "<summary>\nkept state\n</summary>";
    const transcripts = [_][]const u8{ "/sessions/s1/compaction-1.jsonl", "/sessions/s1/compaction-2.jsonl" };
    try tui_session.compact(.{ .focus = "tests", .transcripts = &transcripts });
    if (runtime.local_agent) |*local| local.waitForIdle();

    const seen = collectCompaction(&tui_session, "/sessions/s1/compaction-2.jsonl");
    try std.testing.expect(seen.started);
    try std.testing.expectEqual(@as(?TuiEvent.CompactionOutcome, .completed), seen.outcome);
    try std.testing.expect(seen.text_has_summary);
    try std.testing.expect(seen.transcript_matches);
    try std.testing.expectEqual(@as(u64, 2), seen.messages_before);
    try std.testing.expectEqual(@as(usize, 2), mock.call_count);
    try std.testing.expectEqual(@as(usize, 2), runtime.history().len);
    try std.testing.expect(std.mem.indexOf(u8, runtime.history()[0].user.content.text, "- /sessions/s1/compaction-1.jsonl\n- /sessions/s1/compaction-2.jsonl") != null);
    try std.testing.expectError(error.NothingToCompact, tui_session.compact(.{}));
}

test "runtime compaction reports a provider failure and keeps the history" {
    var mock = MockProtocolCtx{};
    var wide = test_model_a;
    wide.context_window = 200_000;
    const models = [_]ai_types.Model{wide};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("first");
    if (runtime.local_agent) |*local| local.waitForIdle();
    _ = collectCompaction(&tui_session, "");

    mock.provider_error_message = "overloaded";
    try tui_session.compact(.{});
    if (runtime.local_agent) |*local| local.waitForIdle();

    const seen = collectCompaction(&tui_session, "");
    try std.testing.expectEqual(@as(?TuiEvent.CompactionOutcome, .failed), seen.outcome);
    try std.testing.expect(seen.message_has_error);
    try std.testing.expectEqual(@as(usize, 2), runtime.history().len);
    try std.testing.expectEqualStrings("first", runtime.history()[0].user.content.text);
}

test "runtime tags consumed steer message_end with steering provenance" {
    var mock = MockProtocolCtx{ .wait_before_text_first = true };
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("first");
    try tui_session.steer("steer during response");

    if (runtime.local_agent) |*local| local.waitForIdle();

    try std.testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
    try std.testing.expectEqual(@as(u64, 1), tui_session.steersConsumedCount());

    var steer_message_end_tagged = false;
    var prompt_message_end_untagged = false;
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .message_end => |payload| {
                if (payload.role == .user) {
                    if (std.mem.eql(u8, payload.text.slice(), "steer during response")) {
                        steer_message_end_tagged = payload.steering;
                    } else if (std.mem.eql(u8, payload.text.slice(), "first")) {
                        prompt_message_end_untagged = !payload.steering;
                    }
                }
            },
            else => {},
        }
    }
    try std.testing.expect(steer_message_end_tagged);
    try std.testing.expect(prompt_message_end_untagged);
}

test "steer consumption count and the steered message survive a flood that sheds streaming chunks" {
    var mock = MockProtocolCtx{ .wait_before_text_first = true, .deliver_flood_second = TuiEventStream.usable_capacity - 2 };
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("first");
    try tui_session.steer("steer during response");

    if (runtime.local_agent) |*local| local.waitForIdle();
    try std.testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
    try std.testing.expect(runtime.dropped_event_count > 0);

    var saw_user_message_end = false;
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .message_end and ev.message_end.role == .user) saw_user_message_end = true;
    }
    try std.testing.expect(saw_user_message_end);
    try std.testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
}

test "local runtime reports steering available" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{});
    defer runtime.deinit();
    try std.testing.expect(runtime.canSteer());
    try std.testing.expect(runtime.createSession().canSteer());
}

test "a session id set before the agent starts reaches it at start, and a later one replaces it" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models });
    defer runtime.deinit();

    try runtime.setSessionId("ses-before-start");
    try std.testing.expect(runtime.local_agent == null);
    try runtime.start();
    try std.testing.expectEqualStrings("ses-before-start", runtime.local_agent.?._session_id.?);

    try runtime.setSessionId("ses-resumed");
    try std.testing.expectEqualStrings("ses-resumed", runtime.local_agent.?._session_id.?);
}

test "runtime clears queued messages before replacing messages" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.steer("steer now");
    try std.testing.expectEqual(@as(usize, 1), tui_session.queuedCounts().total());

    try runtime.replaceMessages(&.{});

    try std.testing.expectEqual(@as(usize, 0), tui_session.queuedCounts().total());
}

test "event stream resets between turns" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("first");

    var saw_turn_start = false;
    var saw_message_start = false;
    var saw_text_delta = false;
    var saw_message_end = false;
    var saw_turn_end = false;
    collectUntilEnd(&tui_session, &saw_turn_start, &saw_message_start, &saw_text_delta, &saw_message_end, &saw_turn_end);

    try tui_session.submitTurn("second");
    if (runtime.local_agent) |*local| local.waitForIdle();

    saw_turn_start = false;
    saw_message_start = false;
    saw_text_delta = false;
    saw_message_end = false;
    saw_turn_end = false;
    collectUntilEnd(&tui_session, &saw_turn_start, &saw_message_start, &saw_text_delta, &saw_message_end, &saw_turn_end);

    try std.testing.expectEqual(@as(usize, 2), mock.call_count);
    try std.testing.expect(saw_turn_start);
    try std.testing.expect(saw_message_start);
    try std.testing.expect(saw_text_delta);
    try std.testing.expect(saw_message_end);
    try std.testing.expect(saw_turn_end);
}

test "terminal events survive full TUI queue" {
    var mock = MockProtocolCtx{ .flood_count = TuiEventStream.usable_capacity + 45 };
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("flood");

    var saw_turn_end = false;
    var saw_agent_end = false;
    while (tui_session.waitEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .turn_end => saw_turn_end = true,
            .agent_end => saw_agent_end = true,
            else => {},
        }
    }

    try std.testing.expect(saw_turn_end);
    try std.testing.expect(saw_agent_end);
}

test "preserves original tool approval when wrapping" {
    var original_ctx = OriginalApprovalCtx{ .decision = .reject_always };
    const tools = [_]agent.AgentTool{.{
        .label = "Demo",
        .name = "demo_tool",
        .description = "Demo tool",
        .parameters_schema_json = "{}",
        .execute = demoTool,
        .approval_ctx = &original_ctx,
        .approval_fn = originalApprovalCallback,
    }};
    const models = [_]ai_types.Model{test_model_a};
    var mock = MockProtocolCtx{ .tool_first = true };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .tools = &tools,
        .permission_mode = .ask,
        .run_async = false,
    });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("use tool");
    if (runtime.local_agent) |*local| local.waitForIdle();

    var saw_rejected_tool = false;
    while (tui_session.waitEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .tool_execution_end => saw_rejected_tool = ev.tool_execution_end.is_error,
            .agent_end => break,
            else => {},
        }
    }

    try std.testing.expectEqual(@as(usize, 1), original_ctx.calls);
    try std.testing.expect(saw_rejected_tool);
}

test "permission bypass disables policy engine and approval wrappers" {
    var engine = try permission.PermissionEngine.initEmpty(std.testing.allocator, .{
        .workspace_root = "/workspace",
        .persistence_path = "zig-cache/test-tui-permission-bypass.json",
    });
    defer engine.deinit();

    var original_ctx = OriginalApprovalCtx{ .decision = .reject };
    const tools = [_]agent.AgentTool{.{
        .label = "Demo",
        .name = "demo_tool",
        .description = "Demo tool",
        .parameters_schema_json = "{}",
        .execute = demoTool,
        .approval_ctx = &original_ctx,
        .approval_fn = originalApprovalCallback,
    }};
    const models = [_]ai_types.Model{test_model_a};
    var mock = MockProtocolCtx{};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .tools = &tools,
        .permission_engine = &engine,
        .run_async = false,
    });
    defer runtime.deinit();

    try std.testing.expectEqual(PermissionMode.bypass, runtime.permissionMode());
    try std.testing.expect(engine.evaluate("shell", "{\"command\":\"rm -rf /\"}") == .allow);
    for (runtime.wrapped_tools) |tool| {
        try std.testing.expect(tool.approval_fn == null);
        try std.testing.expect(tool.approval_ui_fn == null);
    }

    try runtime.setPermissionMode(.ask);
    try std.testing.expectEqual(PermissionMode.ask, runtime.permissionMode());
    try std.testing.expect(engine.evaluate("shell", "{\"command\":\"rm -rf /\"}") == .deny);
    var found_demo = false;
    for (runtime.wrapped_tools) |tool| {
        if (std.mem.eql(u8, tool.name, "demo_tool")) {
            found_demo = true;
            try std.testing.expect(tool.approval_fn != null);
            try std.testing.expect(tool.approval_ui_fn != null);
        }
    }
    try std.testing.expect(found_demo);
}

test "preserves original tool approval UI when wrapping" {
    var original_ctx = OriginalApprovalUiCtx{};
    var approval_ctx = ApprovalCtx{ .decision = .approve };
    const tools = [_]agent.AgentTool{.{
        .label = "Demo",
        .name = "demo_tool",
        .description = "Demo tool",
        .parameters_schema_json = "{}",
        .execute = demoTool,
        .approval_ui_ctx = &original_ctx,
        .approval_ui_fn = originalApprovalUiCallback,
    }};
    const models = [_]ai_types.Model{test_model_a};
    var mock = MockProtocolCtx{ .tool_first = true };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .tools = &tools,
        .tool_approval_ctx = &approval_ctx,
        .tool_approval_callback = approvalCallback,
        .permission_mode = .ask,
        .run_async = false,
    });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("use tool");
    if (runtime.local_agent) |*local| local.waitForIdle();

    var saw_tui_approval = false;
    while (tui_session.waitEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .tool_approval_requested => saw_tui_approval = true,
            .agent_end => break,
            else => {},
        }
    }

    try std.testing.expectEqual(@as(usize, 1), original_ctx.calls);
    try std.testing.expect(saw_tui_approval);
}

test "model switch affects next turn" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{ test_model_a, test_model_b };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.switchModel("model-b");
    try tui_session.submitTurn("hi");
    if (runtime.local_agent) |*local| local.waitForIdle();

    var saw_turn_start = false;
    var saw_message_start = false;
    var saw_text_delta = false;
    var saw_message_end = false;
    var saw_turn_end = false;
    collectUntilEnd(&tui_session, &saw_turn_start, &saw_message_start, &saw_text_delta, &saw_message_end, &saw_turn_end);

    try std.testing.expectEqualStrings("model-b", mock.last_model_id);
}

test "exact model switch distinguishes duplicate ids" {
    const first = ai_types.Model{
        .id = "gpt-4o",
        .name = "GPT-4o Completions",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://example.invalid",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 8192,
        .max_tokens = 1024,
    };
    const second = ai_types.Model{
        .id = "gpt-4o",
        .name = "GPT-4o Responses",
        .api = "openai-responses",
        .provider = "openai",
        .base_url = "https://example.invalid",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 8192,
        .max_tokens = 1024,
    };
    const models = [_]ai_types.Model{ first, second };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &models });
    defer runtime.deinit();

    try runtime.switchModelExact(second);

    try std.testing.expectEqualStrings("gpt-4o", runtime.currentModel().?.id);
    try std.testing.expectEqualStrings("openai-responses", runtime.currentModel().?.api);
}

test "initial model id selects matching model" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{ test_model_a, test_model_b };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .initial_model_id = "model-b",
        .run_async = false,
    });
    defer runtime.deinit();

    try std.testing.expectEqualStrings("model-b", runtime.currentModel().?.id);
}

test "initial model ref selects exact duplicate id provider api tuple" {
    const first = ai_types.Model{
        .id = "gpt-4o",
        .name = "GPT-4o Completions",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://example.invalid",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 8192,
        .max_tokens = 1024,
    };
    const second = ai_types.Model{
        .id = "gpt-4o",
        .name = "GPT-4o Responses",
        .api = "openai-responses",
        .provider = "openai",
        .base_url = "https://example.invalid",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 8192,
        .max_tokens = 1024,
    };
    const models = [_]ai_types.Model{ first, second };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{
        .models = &models,
        .initial_model = .{ .id = "gpt-4o", .provider = "openai", .api = "openai-responses" },
    });
    defer runtime.deinit();

    try std.testing.expectEqualStrings("openai-responses", runtime.currentModel().?.api);
}

test "a saved model whose provider moved it to another wire is still the one selected" {
    var moved = test_model_b;
    moved.id = "deepseek-flash";
    moved.provider = "deepseek";
    moved.api = "anthropic-messages";
    const models = [_]ai_types.Model{ test_model_a, moved };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{
        .models = &models,
        .initial_model = .{ .id = "deepseek-flash", .provider = "deepseek", .api = "openai-completions" },
    });
    defer runtime.deinit();

    try std.testing.expectEqualStrings("deepseek-flash", runtime.currentModel().?.id);
    try std.testing.expectEqualStrings("anthropic-messages", runtime.currentModel().?.api);
}

test "replaceModels preserves selected model when still available" {
    const initial = [_]ai_types.Model{ test_model_a, test_model_b };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &initial, .initial_model_id = "model-b" });
    defer runtime.deinit();

    const replacement = [_]ai_types.Model{test_model_b};
    try runtime.replaceModels(&replacement, null);

    try std.testing.expectEqual(@as(usize, 1), runtime.availableModels().len);
    try std.testing.expectEqualStrings("model-b", runtime.currentModel().?.id);
}

test "thinking level affects next local turn" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try runtime.setThinkingLevel(.high);
    try tui_session.submitTurn("hi");
    if (runtime.local_agent) |*local| local.waitForIdle();

    var saw_turn_start = false;
    var saw_message_start = false;
    var saw_text_delta = false;
    var saw_message_end = false;
    var saw_turn_end = false;
    collectUntilEnd(&tui_session, &saw_turn_start, &saw_message_start, &saw_text_delta, &saw_message_end, &saw_turn_end);

    try std.testing.expectEqual(ai_types.ThinkingLevel.high, mock.last_thinking_level);
}

test "TUI runtime normalizes hidden minimal thinking level" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .thinking_level = .minimal });
    defer runtime.deinit();

    try std.testing.expectEqual(ai_types.ThinkingLevel.low, runtime.thinkingLevel());
    try runtime.setThinkingLevel(.minimal);
    try std.testing.expectEqual(ai_types.ThinkingLevel.low, runtime.thinkingLevel());
}

test "model switch is rejected while async turn is running" {
    var mock = MockProtocolCtx{ .wait_for_cancel = true };
    const models = [_]ai_types.Model{ test_model_a, test_model_b };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("hi");
    try std.testing.expectError(error.AgentAlreadyStreaming, tui_session.switchModel("model-b"));
    tui_session.cancel();
    if (runtime.local_agent) |*local| local.waitForIdle();
    try tui_session.switchModel("model-b");
    try std.testing.expectEqualStrings("model-b", runtime.currentModel().?.id);
}

test "failed resume does not reset event stream" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try std.testing.expectError(error.NoMessagesToContinue, tui_session.resumeSession());
    try std.testing.expectEqual(@as(usize, 0), mock.call_count);
    try std.testing.expect(!runtime.stream_active);
    try std.testing.expect(tui_session.popEvent() == null);
}

test "async submit without selected model fails before stream reset" {
    var mock = MockProtocolCtx{};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .run_async = true });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try std.testing.expectError(error.NoModelConfigured, tui_session.submitTurn("hi"));
    try std.testing.expectEqual(@as(usize, 0), mock.call_count);
    try std.testing.expect(!runtime.stream_active);
    try std.testing.expect(tui_session.popEvent() == null);
}

test "failed turns emit error end reason" {
    var mock = MockProtocolCtx{ .force_error = true };
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try std.testing.expectError(error.AgentLoopFailed, tui_session.submitTurn("fail"));
    if (runtime.local_agent) |*local| local.waitForIdle();

    var saw_error_detail = false;
    var saw_error_end = false;
    while (tui_session.waitEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .@"error" => {
                if (std.mem.eql(u8, ev.@"error".message.slice(), "forced provider error")) saw_error_detail = true;
            },
            .agent_end => {
                saw_error_end = ev.agent_end.reason == .@"error";
                break;
            },
            else => {},
        }
    }
    try std.testing.expect(saw_error_detail);
    try std.testing.expect(saw_error_end);
}

fn saveNumberedTranscript(ctx: ?*anyopaque, allocator: std.mem.Allocator, index: usize, history: []const ai_types.Message) ?[]u8 {
    _ = ctx;
    _ = history;
    return std.fmt.allocPrint(allocator, "/t/compaction-{d}.jsonl", .{index}) catch null;
}

test "a compaction inside a run that does not complete gives back the transcript slot it saved" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{test_model_a}, .run_async = false });
    defer runtime.deinit();
    try runtime.run_transcripts.append(std.testing.allocator, try std.testing.allocator.dupe(u8, "/t/compaction-1.jsonl"));
    runtime.transcript_writer = .{ .ctx = null, .save_fn = saveNumberedTranscript };

    const first = TuiRuntime.runTranscripts(&runtime, &.{});
    try std.testing.expectEqual(@as(usize, 2), first.paths.len);
    try std.testing.expectEqualStrings("/t/compaction-2.jsonl", first.paths[1]);
    try std.testing.expectEqualStrings("/t/compaction-2.jsonl", first.saved);

    TuiRuntime.settleRunTranscript(&runtime, false);
    try std.testing.expectEqual(@as(usize, 1), runtime.run_transcripts.items.len);
    try runtime.pushRunCompactionEnd(.{ .outcome = .failed });

    const second = TuiRuntime.runTranscripts(&runtime, &.{});
    try std.testing.expectEqualStrings("/t/compaction-2.jsonl", second.paths[1]);
    TuiRuntime.settleRunTranscript(&runtime, true);
    try std.testing.expectEqual(@as(usize, 2), runtime.run_transcripts.items.len);
    try runtime.pushRunCompactionEnd(.{ .outcome = .completed, .text = OwnedSlice(u8).initBorrowed("summary"), .transcript = OwnedSlice(u8).initBorrowed(second.saved) });

    var completed_with_slot = false;
    while (runtime.event_stream.poll()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .compaction_end and ev.compaction_end.outcome == .completed) {
            completed_with_slot = std.mem.eql(u8, ev.compaction_end.transcript.slice(), "/t/compaction-2.jsonl");
        }
    }
    try std.testing.expect(completed_with_slot);
}

test "runtime push preserves newest event when a full queue holds nothing it may shed and nobody drains it" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{test_model_a}, .run_async = false });
    defer runtime.deinit();
    runtime.semantic_wait_ms = 0;

    for (0..TuiEventStream.usable_capacity) |_| {
        runtime.push(.{ .turn_start = .{} });
    }
    runtime.push(.{ .@"error" = .{ .message = try runtime.dupeOwned("latest error") } });

    var saw_latest_error = false;
    while (runtime.event_stream.poll()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .@"error" and std.mem.eql(u8, ev.@"error".message.slice(), "latest error")) {
            saw_latest_error = true;
        }
    }
    try std.testing.expect(saw_latest_error);
}

test "TuiRuntime terminal event sheds streaming chunks from a full queue and warns" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{test_model_a}, .run_async = false });
    defer runtime.deinit();

    for (0..TuiEventStream.usable_capacity) |i| {
        runtime.push(.{ .text_delta = .{ .content_index = i, .delta = OwnedSlice(u8).initBorrowed("x") } });
    }
    try std.testing.expect(runtime.event_stream.isFull());

    runtime.pushTerminal(.{ .agent_end = .{ .reason = .completed } });

    var saw_agent_end = false;
    var saw_warning = false;
    while (runtime.event_stream.poll()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .agent_end => saw_agent_end = true,
            .system_warning => saw_warning = true,
            else => {},
        }
    }
    try std.testing.expect(saw_agent_end);
    try std.testing.expect(saw_warning);
}

test "TuiRuntime counts dropped events and emits warning" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{test_model_a} });
    defer runtime.deinit();
    var tui_session = runtime.createSession();

    var i: usize = 0;
    while (i < TuiEventStream.usable_capacity) : (i += 1) {
        runtime.push(.{ .text_delta = .{ .content_index = i, .delta = OwnedSlice(u8).initBorrowed("x") } });
    }
    try std.testing.expect(runtime.event_stream.isFull());

    runtime.push(.{ .text_delta = .{ .content_index = TuiEventStream.usable_capacity, .delta = OwnedSlice(u8).initBorrowed("after-full") } });
    const shed: u64 = TuiEventStream.usable_capacity;
    try std.testing.expectEqual(shed, runtime.dropped_event_count);
    try std.testing.expect(runtime.backpressure_active.load(.acquire));

    const bp_active = runtime.backpressureState();
    try std.testing.expect(bp_active.active);
    try std.testing.expectEqual(shed, bp_active.dropped_count);

    var saw_warning = false;
    var saw_newest = false;
    while (tui_session.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .text_delta and std.mem.eql(u8, ev.text_delta.delta.slice(), "after-full")) saw_newest = true;
        if (ev == .system_warning) {
            saw_warning = true;
            try std.testing.expect(std.mem.indexOf(u8, ev.system_warning.message.slice(), "1023 events dropped due to backpressure") != null);
        }
    }
    try std.testing.expect(saw_warning);
    try std.testing.expect(saw_newest);

    const bp_cleared = runtime.backpressureState();
    try std.testing.expect(!bp_cleared.active);
    try std.testing.expectEqual(shed, bp_cleared.dropped_count);
}

test "a full queue sheds only streaming chunks, keeping message and turn events in order" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{test_model_a}, .run_async = false });
    defer runtime.deinit();

    runtime.push(.{ .message_start = .{ .role = .assistant } });
    for (0..TuiEventStream.usable_capacity - 3) |i| {
        runtime.push(.{ .text_delta = .{ .content_index = i, .delta = OwnedSlice(u8).initBorrowed("x") } });
    }
    runtime.push(.{ .thinking_delta = .{ .content_index = 0, .delta = OwnedSlice(u8).initBorrowed("t") } });
    runtime.push(.{ .message_end = .{ .role = .assistant, .text = OwnedSlice(u8).initBorrowed("whole reply") } });
    try std.testing.expect(runtime.event_stream.isFull());

    runtime.push(.{ .turn_end = .{ .stop_reason = .stop } });

    try std.testing.expectEqual(@as(u64, TuiEventStream.usable_capacity - 2), runtime.dropped_event_count);
    var order: [4]std.meta.Tag(TuiEvent) = undefined;
    var count: usize = 0;
    while (runtime.event_stream.poll()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (count < order.len) order[count] = std.meta.activeTag(ev);
        count += 1;
    }
    try std.testing.expectEqual(@as(usize, 4), count);
    try std.testing.expectEqual(std.meta.Tag(TuiEvent).message_start, order[0]);
    try std.testing.expectEqual(std.meta.Tag(TuiEvent).message_end, order[1]);
    try std.testing.expectEqual(std.meta.Tag(TuiEvent).turn_end, order[2]);
    try std.testing.expectEqual(std.meta.Tag(TuiEvent).system_warning, order[3]);
}

test "TuiRuntime replaceMessages clears stale backpressure counters" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime.deinit();
    var tui_session = runtime.createSession();
    try tui_session.start();

    runtime.dropped_event_count = 9;
    runtime.dropped_since_warning = 2;
    runtime.backpressure_active.store(true, .release);

    try runtime.replaceMessages(&.{});

    const bp = runtime.backpressureState();
    try std.testing.expect(!bp.active);
    try std.testing.expectEqual(@as(u64, 0), bp.dropped_count);
}

fn drainEndOfRun(runtime: *TuiRuntime) !struct { warning: ?[]u8, reason: ?TuiEndReason } {
    var warning: ?[]u8 = null;
    errdefer if (warning) |text| std.testing.allocator.free(text);
    var reason: ?TuiEndReason = null;
    while (runtime.event_stream.poll()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        switch (ev) {
            .system_warning => |payload| {
                try std.testing.expect(reason == null);
                warning = try std.testing.allocator.dupe(u8, payload.message.slice());
            },
            .agent_end => |payload| reason = payload.reason,
            else => {},
        }
    }
    return .{ .warning = warning, .reason = reason };
}

fn replyEndingWith(stop_reason: ai_types.StopReason) ai_types.AssistantMessage {
    return .{ .content = &.{}, .api = "", .provider = "", .model = "", .usage = .{}, .stop_reason = stop_reason, .timestamp = 0 };
}

test "runtime warns before ending a run whose last reply hit the output token limit" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{test_model_a}, .run_async = false });
    defer runtime.deinit();

    try runtime.handleAgentEvent(.{ .turn_end = .{ .message = replyEndingWith(.length) } });
    try runtime.handleAgentEvent(.{ .agent_end = .{} });

    const ended = try drainEndOfRun(&runtime);
    defer if (ended.warning) |text| std.testing.allocator.free(text);
    try std.testing.expectEqualStrings(output_limit_warning, ended.warning.?);
    try std.testing.expectEqual(@as(?TuiEndReason, .completed), ended.reason);
}

test "wrapping a tool preserves the operation kind its definition declares" {
    const owned = try std.testing.allocator.dupe(agent.AgentTool, local_tools.defaultTools());
    defer std.testing.allocator.free(owned);
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .tools = owned, .workspace_root = "/workspace" });
    defer runtime.deinit();
    runtime.rebuildWrappedTools();
    try std.testing.expectEqual(owned.len, runtime.wrapped_tools.len);
    for (runtime.wrapped_tools, owned) |wrapped, original| {
        try std.testing.expectEqualStrings(original.name, wrapped.name);
        try std.testing.expectEqual(original.operation, wrapped.operation);
    }
}

test "the rewrite replaces the session root with the working directory and leaves other roots alone" {
    const cwd = try std.process.currentPathAlloc(std.testing.io, std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const sub = try std.fs.path.join(std.testing.allocator, &.{ cwd, "zig" });
    defer std.testing.allocator.free(sub);
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .workspace_root = cwd });
    defer runtime.deinit();
    runtime.adoptWorkingDirectory(sub);

    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"path\":\"a.txt\"}}", .{cwd});
    defer std.testing.allocator.free(args);
    const rewritten = (try runtime.rewriteWorkspaceRoot("file_read", args, std.testing.allocator)).?;
    defer std.testing.allocator.free(rewritten);
    var parsed = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, rewritten, .{});
    defer parsed.deinit();
    try std.testing.expectEqualStrings(sub, parsed.value.object.get("workspace_root").?.string);
    try std.testing.expectEqualStrings("a.txt", parsed.value.object.get("path").?.string);

    try std.testing.expect(try runtime.rewriteWorkspaceRoot("artifact_retrieve", "{\"reference\":\"shell_execute:1\"}", std.testing.allocator) == null);
    const nested = ("[" ** 400) ++ "1" ++ ("]" ** 400);
    const deep_args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"path\":\"a.txt\",\"deep\":{s}}}", .{ cwd, nested });
    defer std.testing.allocator.free(deep_args);
    const deep_rewritten = (try runtime.rewriteWorkspaceRoot("file_read", deep_args, std.testing.allocator)).?;
    defer std.testing.allocator.free(deep_rewritten);
    try std.testing.expect(std.mem.indexOf(u8, deep_rewritten, nested) != null);
    const abs_args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"path\":\"{s}\"}}", .{ cwd, sub });
    defer std.testing.allocator.free(abs_args);
    try std.testing.expect(try runtime.rewriteWorkspaceRoot("file_read", abs_args, std.testing.allocator) == null);
    const other = try std.fs.path.join(std.testing.allocator, &.{ sub, "src" });
    defer std.testing.allocator.free(other);
    const args_other = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"path\":\"a.txt\"}}", .{other});
    defer std.testing.allocator.free(args_other);
    try std.testing.expect(try runtime.rewriteWorkspaceRoot("file_read", args_other, std.testing.allocator) == null);

    const missing = try std.fs.path.join(std.testing.allocator, &.{ cwd, "no-such-working-directory" });
    defer std.testing.allocator.free(missing);
    runtime.adoptWorkingDirectory(missing);
    try std.testing.expectEqualStrings(missing, runtime.workingDirectory());
    try std.testing.expect(try runtime.rewriteWorkspaceRoot("file_read", args, std.testing.allocator) == null);
}

test "a command's end directory is adopted only while it stays inside the session root" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .workspace_root = "/workspace" });
    defer runtime.deinit();
    try std.testing.expectEqualStrings("/workspace", runtime.workingDirectory());

    runtime.adoptWorkingDirectory("/workspace/sub");
    try std.testing.expectEqualStrings("/workspace/sub", runtime.workingDirectory());

    runtime.adoptWorkingDirectory("/workspace-ish");
    try std.testing.expectEqualStrings("/workspace/sub", runtime.workingDirectory());

    runtime.adoptWorkingDirectory("/elsewhere");
    try std.testing.expectEqualStrings("/workspace/sub", runtime.workingDirectory());
}

test "moving the workspace root resets the working directory to it" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .workspace_root = "/workspace" });
    defer runtime.deinit();
    runtime.adoptWorkingDirectory("/workspace/sub");
    try std.testing.expectEqualStrings("/workspace/sub", runtime.workingDirectory());

    try runtime.setWorkspaceRoot("/worktree");
    try std.testing.expectEqualStrings("/worktree", runtime.workingDirectory());
}

test "the system prompt is fixed and adoption does not rewrite it" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .workspace_root = "/workspace" });
    defer runtime.deinit();

    const before = try runtime.workspaceSystemPrompt();
    defer std.testing.allocator.free(before);
    try std.testing.expect(std.mem.indexOf(u8, before, "Default workspace root: /workspace") != null);
    try std.testing.expect(std.mem.indexOf(u8, before, "Current working directory:") == null);

    runtime.adoptWorkingDirectory("/workspace/sub");
    try std.testing.expectEqualStrings("/workspace/sub", runtime.workingDirectory());

    const after = try runtime.workspaceSystemPrompt();
    defer std.testing.allocator.free(after);
    try std.testing.expectEqualStrings(before, after);
}

test "a shell result moves the working directory and leaves the prompt alone" {
    const tools = [_]agent.AgentTool{.{
        .label = "Shell",
        .name = "Shell",
        .description = "Reports a new working directory",
        .parameters_schema_json = "{}",
        .execute = demoTool,
        .runtime_execute = cdReportingTool,
    }};
    const models = [_]ai_types.Model{test_model_a};
    var mock = MockProtocolCtx{ .tool_first = true, .tool_name = "Shell" };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .tools = &tools,
        .workspace_root = "/tmp/makai-workspace",
        .run_async = false,
    });
    defer runtime.deinit();

    var tui_session = runtime.createSession();
    try tui_session.start();
    try tui_session.submitTurn("move");
    try std.testing.expectEqualStrings("/tmp/makai-workspace/sub", runtime.workingDirectory());
}

test "only a command that moved moves the session, and the result says where it is" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .workspace_root = "/tmp/makai-workspace" });
    defer runtime.deinit();

    const parts = try std.testing.allocator.alloc(ai_types.UserContentPart, 1);
    parts[0] = .{ .text = .{ .text = try std.testing.allocator.dupe(u8, "stdout:\n\nstderr:\n") } };
    var result = agent.AgentToolResult{ .content = OwnedSlice(ai_types.UserContentPart).initOwned(parts) };
    defer result.deinit(std.testing.allocator);
    const details = "{\"working_directory\":\"/tmp/makai-workspace/sub\",\"working_directory_observed\":true}";

    runtime.adoptReportedWorkingDirectory("{\"workspace_root\":\"/tmp/makai-workspace/sub\"}", details, &result);
    try std.testing.expectEqualStrings("/tmp/makai-workspace", runtime.workingDirectory());
    try std.testing.expectEqualStrings("stdout:\n\nstderr:\n", result.content.slice()[0].text.text);

    runtime.adoptReportedWorkingDirectory("{\"workspace_root\":\"/tmp/makai-workspace\"}", details, &result);
    try std.testing.expectEqualStrings("/tmp/makai-workspace/sub", runtime.workingDirectory());
    try std.testing.expect(std.mem.endsWith(u8, result.content.slice()[0].text.text, "\ncwd: /tmp/makai-workspace/sub"));

    const big = "{\"raw_bytes\":40000,\"compressed\":true,\"details\":{\"working_directory\":\"/tmp/makai-workspace/big\",\"working_directory_observed\":true}}";
    runtime.adoptReportedWorkingDirectory("{\"workspace_root\":\"/tmp/makai-workspace\"}", big, &result);
    try std.testing.expectEqualStrings("/tmp/makai-workspace/big", runtime.workingDirectory());
    try std.testing.expect(std.mem.endsWith(u8, result.content.slice()[0].text.text, "\ncwd: /tmp/makai-workspace/big"));
}

test "runtime ends a run whose last reply finished without an output-limit warning" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{test_model_a}, .run_async = false });
    defer runtime.deinit();

    try runtime.handleAgentEvent(.{ .turn_end = .{ .message = replyEndingWith(.stop) } });
    try runtime.handleAgentEvent(.{ .agent_end = .{} });

    const ended = try drainEndOfRun(&runtime);
    defer if (ended.warning) |text| std.testing.allocator.free(text);
    try std.testing.expect(ended.warning == null);
    try std.testing.expectEqual(@as(?TuiEndReason, .completed), ended.reason);
}

test "requestTitle keeps the first line of the model's reply, thinking off" {
    var mock = MockProtocolCtx{ .reply_text = "  \"Fix the resume freeze.\"  \nsecond line" };
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &[_]ai_types.Model{test_model_a}, .generate_titles = true });
    defer runtime.deinit();

    try std.testing.expect(try runtime.requestTitle("the resume freezes on long sessions"));
    runtime.waitForTitleRequest();
    const title = runtime.takeGeneratedTitle().?;
    defer std.testing.allocator.free(title);
    try std.testing.expectEqualStrings("Fix the resume freeze", title);
    try std.testing.expectEqual(@as(usize, 1), mock.call_count);
    try std.testing.expectEqual(ai_types.ThinkingLevel.off, mock.last_thinking_level);
    try std.testing.expect(runtime.takeGeneratedTitle() == null);
}

test "requestTitle sends nothing unless titles are enabled" {
    var mock = MockProtocolCtx{};
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &[_]ai_types.Model{test_model_a} });
    defer runtime.deinit();

    try std.testing.expect(!try runtime.requestTitle("the resume freezes on long sessions"));
    runtime.waitForTitleRequest();
    try std.testing.expect(runtime.takeGeneratedTitle() == null);
    try std.testing.expectEqual(@as(usize, 0), mock.call_count);
}

test "cleanTitle takes the first non-empty line, unquoted, within 80 bytes" {
    const cases = [_]struct { reply: []const u8, title: ?[]const u8 }{
        .{ .reply = "\n\n**Fix the resume freeze.**\nmore", .title = "Fix the resume freeze" },
        .{ .reply = "'Session titles'", .title = "Session titles" },
        .{ .reply = " \n\t\n", .title = null },
    };
    for (cases) |case| {
        const title = try cleanTitle(std.testing.allocator, case.reply);
        defer if (title) |value| std.testing.allocator.free(value);
        if (case.title) |expected| {
            try std.testing.expectEqualStrings(expected, title.?);
        } else {
            try std.testing.expect(title == null);
        }
    }
    const long = try cleanTitle(std.testing.allocator, "é" ** 50);
    defer std.testing.allocator.free(long.?);
    try std.testing.expectEqual(@as(usize, 80), long.?.len);
}
