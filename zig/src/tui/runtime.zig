const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const event_stream = @import("event_stream");
const agent = @import("agent");
const session = @import("tui_session");
const local_tools = @import("tools/registry");
const permission = @import("permission");
const OwnedSlice = @import("owned_slice").OwnedSlice;
const model_catalog = @import("model_catalog");

pub const TuiSession = session.TuiSession;
pub const TuiEvent = session.TuiEvent;
pub const TuiEventStream = session.TuiEventStream;
pub const TuiEndReason = session.TuiEndReason;
pub const QueuedCounts = session.QueuedCounts;
pub const CompactOptions = session.CompactOptions;
pub const ToolApprovalCallback = session.ToolApprovalCallback;
pub const ToolApprovalDecision = session.ToolApprovalDecision;
pub const ToolApprovalRequest = session.ToolApprovalRequest;

pub const output_limit_warning = "The model's reply hit its output token limit, so the run stopped. Send a message to continue.";

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
        set_catalog: *const fn (ctx: *anyopaque, models: []const ai_types.Model) anyerror!void,
        compacts: *const fn (ctx: *anyopaque) bool,
        settings_live: *const fn (ctx: *anyopaque) bool,
        take_record: *const fn (ctx: *anyopaque) ?TuiEvent,
        records_session: *const fn (ctx: *anyopaque) bool,
        compactable: *const fn (ctx: *anyopaque) bool,
        compact: *const fn (ctx: *anyopaque, focus: []const u8) anyerror!void,
        set_compaction_policy: *const fn (ctx: *anyopaque, policy_json: []const u8) anyerror!void,
        set_settings: *const fn (ctx: *anyopaque, level: ai_types.ThinkingLevel, settings_json: []const u8) anyerror!void,
        decide_approval: *const fn (ctx: *anyopaque, tool_call_id: []const u8, decision: ToolApprovalDecision) anyerror!void,
        follow_up: *const fn (ctx: *anyopaque, text: []const u8) anyerror!void,
        steer: *const fn (ctx: *anyopaque, text: []const u8) anyerror!void,
        clear_queued: *const fn (ctx: *anyopaque) void,
        queued: *const fn (ctx: *anyopaque) usize,
        steers_pending: *const fn (ctx: *anyopaque) usize,
        steers_settled: *const fn (ctx: *anyopaque) u64,
        stop: *const fn (ctx: *anyopaque) void,
    };
};

pub const Loop = struct {
    ctx: *anyopaque,
    vtable: *const VTable,

    pub const VTable = struct {
        bind: *const fn (ctx: *anyopaque, runtime: *TuiRuntime) anyerror!void,
        start: *const fn (ctx: *anyopaque) anyerror!void,
        stop: *const fn (ctx: *anyopaque) void,
        idle: *const fn (ctx: *anyopaque) bool,
        set_model: *const fn (ctx: *anyopaque, model: ai_types.Model) void,
        request_model_switch: *const fn (ctx: *anyopaque, model: ?ai_types.Model) void,
        set_thinking_level: *const fn (ctx: *anyopaque, level: ai_types.ThinkingLevel) void,
        set_output: *const fn (ctx: *anyopaque, setting: agent.OutputSetting) void,
        set_permission_mode: *const fn (ctx: *anyopaque, mode: PermissionMode) anyerror!void,
        adopt_workspace_root: *const fn (ctx: *anyopaque, root: []const u8) anyerror!void,
        set_workspace_root: *const fn (ctx: *anyopaque) anyerror!void,
        set_compact_output: *const fn (ctx: *anyopaque, enabled: bool) void,
        submit: *const fn (ctx: *anyopaque, text: []const u8) anyerror!void,
        steer: *const fn (ctx: *anyopaque, text: []const u8) anyerror!void,
        queue_steer: *const fn (ctx: *anyopaque, text: []const u8) anyerror!void,
        clear_steers: *const fn (ctx: *anyopaque) void,
        follow_up: *const fn (ctx: *anyopaque, text: []const u8) anyerror!void,
        clear_queued: *const fn (ctx: *anyopaque) void,
        queued_counts: *const fn (ctx: *anyopaque) QueuedCounts,
        steers_consumed: *const fn (ctx: *anyopaque) u64,
        replace_messages: *const fn (ctx: *anyopaque, messages: []const ai_types.Message) anyerror!void,
        history: *const fn (ctx: *anyopaque) []const ai_types.Message,
        compact: *const fn (ctx: *anyopaque, options: CompactOptions) anyerror!void,
        set_session_id: *const fn (ctx: *anyopaque) anyerror!void,
        arm_auto_compact: *const fn (ctx: *anyopaque, at: ?u64, transcripts: []const []const u8, writer: ?TuiRuntime.TranscriptWriter) anyerror!void,
        resume_session: *const fn (ctx: *anyopaque) anyerror!void,
        cancel: *const fn (ctx: *anyopaque) void,
        decide_approval: *const fn (ctx: *anyopaque, tool_call_id: []const u8, decision: ToolApprovalDecision) anyerror!void,
        request_compaction: *const fn (ctx: *anyopaque, focus: []const u8) anyerror!void,
        take_compaction_request: *const fn (ctx: *anyopaque, allocator: std.mem.Allocator) anyerror!?[]u8,
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
    loop: ?Loop = null,
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

pub fn cloneModels(allocator: std.mem.Allocator, models: []const ai_types.Model) ![]ai_types.Model {
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

pub fn deinitModels(allocator: std.mem.Allocator, models: []ai_types.Model) void {
    for (models) |*model| model.deinit(allocator);
    allocator.free(models);
}

pub const TuiRuntime = struct {
    allocator: std.mem.Allocator,
    protocol: ?agent.ProtocolClient,
    models: []ai_types.Model,
    selected_model_index: ?usize,
    pending_model_index: ?usize = null,
    event_stream: TuiEventStream,
    tool_registry: local_tools.ToolRegistry,
    mcp_bridge: ?*local_tools.mcp_bridge.McpBridge = null,
    original_tools: []agent.AgentTool,
    workspace_root: []u8,
    session_cwd: []u8,
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
    session_id: []u8 = &.{},
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
    loop: ?Loop = null,
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
            .workspace_root = workspace_root,
            .session_cwd = session_cwd,
            .permission_mode = options.permission_mode,
            .thinking_level = normalizeTuiThinkingLevel(options.thinking_level),
            .context_window = options.context_window,
            .output = options.output,
            .compact_output = options.compact_output,
            .generate_titles = options.generate_titles,
            .remote = options.remote,
            .loop = options.loop,
        };
        original_tools = &.{};
        models = &.{};
        workspace_root = &.{};
        session_cwd = &.{};
        tool_registry = local_tools.ToolRegistry.init();
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
            allocator.free(runtime.original_tools);
            runtime.original_tools = next_original_tools;
        }
        runtime.suspendContextWindowAboveCeiling();
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
        self.allocator.free(self.workspace_root);
        self.allocator.free(self.session_cwd);
        self.allocator.free(self.session_id);
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
        const loop = try self.boundLoop() orelse return error.NoProtocolConfigured;
        try loop.vtable.start(loop.ctx);
        self.started = true;
    }

    fn boundLoop(self: *TuiRuntime) !?Loop {
        const loop = self.loop orelse return null;
        try loop.vtable.bind(loop.ctx, self);
        return loop;
    }

    fn loopBusy(self: *const TuiRuntime) bool {
        const loop = self.loop orelse return false;
        return !loop.vtable.idle(loop.ctx);
    }

    fn loopSetModel(self: *TuiRuntime, model: ai_types.Model) void {
        if (self.loop) |loop| loop.vtable.set_model(loop.ctx, self.effectiveModel(model));
    }

    fn loopRequestModelSwitch(self: *TuiRuntime, model: ?ai_types.Model) void {
        const loop = self.loop orelse return;
        loop.vtable.request_model_switch(loop.ctx, if (model) |held| self.effectiveModel(held) else null);
    }

    pub fn stop(self: *TuiRuntime) void {
        if (self.remote) |remote| {
            if (self.started) remote.vtable.stop(remote.ctx);
            self.started = false;
            return;
        }
        if (self.loop) |loop| loop.vtable.stop(loop.ctx);
        self.started = false;
    }

    pub fn isIdle(self: *TuiRuntime) bool {
        if (self.stream_active) return false;
        if (self.remote != null) return true;
        if (self.loop) |loop| return loop.vtable.idle(loop.ctx);
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
        if (self.loopBusy()) return error.AgentAlreadyStreaming;

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
            try remote.vtable.set_catalog(remote.ctx, owned_next);
            if (self.started) {
                const before = if (self.selected_model_index) |idx| self.models[idx] else null;
                const after = if (next_selected) |idx| owned_next[idx] else null;
                if (after) |chosen| {
                    const unchanged = if (before) |held| std.mem.eql(u8, held.id, chosen.id) and std.mem.eql(u8, held.provider, chosen.provider) and std.mem.eql(u8, held.api, chosen.api) else false;
                    if (!unchanged) try remote.vtable.switch_model(remote.ctx, chosen);
                }
            }
        }
        self.loopRequestModelSwitch(null);
        deinitModels(self.allocator, self.models);
        self.models = owned_next;
        owned_next = &.{};
        self.selected_model_index = next_selected;
        self.pending_model_index = next_pending;
        if (next_pending) |idx| self.loopRequestModelSwitch(self.models[idx]);
        self.reconcileContextWindowAfterModelSwitch();

        if (next_selected) |idx| self.loopSetModel(self.models[idx]);
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

    pub fn effectiveModel(self: *const TuiRuntime, model: ai_types.Model) ai_types.Model {
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

    pub fn reopenSaved(self: *TuiRuntime, session_id: []const u8, workspace_root: ?[]const u8) !void {
        const remote = self.remote orelse return error.UnavailableOverOap;
        if (self.stream_active) return error.AgentAlreadyStreaming;
        if (self.started) {
            remote.vtable.stop(remote.ctx);
            self.started = false;
        }
        var settings = self.remoteSettings(session_id);
        if (workspace_root) |root| settings.workspace_root = root;
        remote.vtable.start(remote.ctx, .{ .ctx = self, .push = pushRemote }, settings) catch |err| {
            self.started = err == error.OapReopenRefused;
            return err;
        };
        if (workspace_root) |root| try self.adoptWorkspaceRoot(root);
        self.started = true;
    }

    fn openOverOap(self: *const TuiRuntime) bool {
        return self.remote != null and self.started;
    }

    fn sendRemoteSetting(self: *TuiRuntime, settings: anytype) error{ AgentAlreadyStreaming, UnavailableOverOap }!void {
        const remote = self.remote.?;
        const json = std.json.Stringify.valueAlloc(self.allocator, .{ .oapx = settings }, .{}) catch return error.UnavailableOverOap;
        defer self.allocator.free(json);
        remote.vtable.set_settings(remote.ctx, self.thinking_level, json) catch |err| return switch (err) {
            error.RunInProgress => error.AgentAlreadyStreaming,
            else => error.UnavailableOverOap,
        };
    }

    pub fn setContextWindow(self: *TuiRuntime, window: ?u32) error{ AboveMaximum, AgentAlreadyStreaming, UnavailableOverOap }!void {
        if (self.loopBusy()) return error.AgentAlreadyStreaming;
        if (window) |held| {
            if (self.contextWindowMaximum()) |ceiling| {
                if (held > ceiling) return error.AboveMaximum;
            }
        }
        if (self.openOverOap()) try self.sendRemoteSetting(.{ .context_window = window });
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
        if (self.selected_model_index) |idx| self.loopSetModel(self.models[idx]);
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
        if (self.openOverOap()) try self.remote.?.vtable.set_reasoning(self.remote.?.ctx, normalized);
        self.thinking_level = normalized;
        if (self.loop) |loop| loop.vtable.set_thinking_level(loop.ctx, normalized);
    }

    pub fn outputSetting(self: *const TuiRuntime) agent.OutputSetting {
        return self.output;
    }

    pub fn setOutput(self: *TuiRuntime, setting: agent.OutputSetting) error{ AboveMaximum, AgentAlreadyStreaming, UnavailableOverOap }!void {
        if (self.loopBusy()) return error.AgentAlreadyStreaming;
        if (setting == .tokens) {
            if (self.currentModel()) |model| {
                if (model.max_tokens > 0 and setting.tokens > model.max_tokens) return error.AboveMaximum;
            }
        }
        if (self.openOverOap()) switch (setting) {
            .tokens => |count| try self.sendRemoteSetting(.{ .output = count }),
            else => try self.sendRemoteSetting(.{ .output = @tagName(setting) }),
        };
        self.output = setting;
        if (self.loop) |loop| loop.vtable.set_output(loop.ctx, setting);
    }

    pub fn setPermissionMode(self: *TuiRuntime, mode: PermissionMode) !void {
        if (self.openOverOap()) try self.sendRemoteSetting(.{ .permission_mode = @tagName(mode) });
        self.permission_mode = mode;
        if (try self.boundLoop()) |loop| try loop.vtable.set_permission_mode(loop.ctx, mode);
    }

    fn adoptWorkspaceRoot(self: *TuiRuntime, root: []const u8) !void {
        const owned = try self.allocator.dupe(u8, root);
        const owned_cwd = self.allocator.dupe(u8, root) catch |err| {
            self.allocator.free(owned);
            return err;
        };
        self.allocator.free(self.workspace_root);
        self.allocator.free(self.session_cwd);
        self.workspace_root = owned;
        self.session_cwd = owned_cwd;
        if (self.loop) |loop| try loop.vtable.adopt_workspace_root(loop.ctx, root);
    }

    pub fn setWorkspaceRoot(self: *TuiRuntime, root: []const u8) !void {
        if (self.openOverOap()) try self.sendRemoteSetting(.{ .workspace_root = root });
        if (self.loopBusy()) return error.AgentAlreadyStreaming;
        try self.adoptWorkspaceRoot(root);
        if (try self.boundLoop()) |loop| try loop.vtable.set_workspace_root(loop.ctx);
    }

    pub fn workingDirectory(self: *const TuiRuntime) []const u8 {
        return self.session_cwd;
    }
    pub fn adoptWorkingDirectory(self: *TuiRuntime, reported: []const u8) void {
        if (reported.len == 0) return;
        if (!self.workingDirectoryInsideRoot(reported)) return;
        if (std.mem.eql(u8, reported, self.session_cwd)) return;
        const owned = self.allocator.dupe(u8, reported) catch return;
        self.allocator.free(self.session_cwd);
        self.session_cwd = owned;
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

    pub fn setCompactOutput(self: *TuiRuntime, enabled: bool) void {
        self.compact_output = enabled;
        if (self.loop) |loop| loop.vtable.set_compact_output(loop.ctx, enabled);
    }

    pub fn switchModel(self: *TuiRuntime, model_id: []const u8) !void {
        if (self.loopBusy()) return error.AgentAlreadyStreaming;

        for (self.models, 0..) |model, i| {
            if (std.mem.eql(u8, model.id, model_id)) {
                if (self.remote) |remote| {
                    if (self.stream_active) return error.AgentAlreadyStreaming;
                    if (self.started) try remote.vtable.switch_model(remote.ctx, self.models[i]);
                }
                self.selected_model_index = i;
                self.reconcileContextWindowAfterModelSwitch();
                self.loopSetModel(self.models[i]);
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
        self.loopRequestModelSwitch(self.models[index]);
        return self.models[index];
    }

    pub fn dropPendingModelSwitch(self: *TuiRuntime) void {
        self.pending_model_index = null;
        self.loopRequestModelSwitch(null);
    }

    pub fn applyPendingModelSwitch(self: *TuiRuntime) !?ai_types.Model {
        const index = self.pending_model_index orelse return null;
        if (index >= self.models.len) {
            self.pending_model_index = null;
            return null;
        }
        try self.switchModelExact(self.models[index]);
        self.pending_model_index = null;
        self.loopRequestModelSwitch(null);
        return self.models[index];
    }

    pub fn switchModelExact(self: *TuiRuntime, selected: ai_types.Model) !void {
        if (self.loopBusy()) return error.AgentAlreadyStreaming;

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
                self.loopSetModel(self.models[i]);
                return;
            }
        }
        return error.ModelNotFound;
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
        const loop = try self.boundLoop() orelse return error.RuntimeNotStarted;
        try loop.vtable.submit(loop.ctx, text);
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
        const loop = try self.boundLoop() orelse return error.RuntimeNotStarted;
        try loop.vtable.steer(loop.ctx, text);
    }

    pub fn queueSteer(self: *TuiRuntime, text: []const u8) !void {
        const loop = try self.boundLoop() orelse return error.RuntimeNotStarted;
        try loop.vtable.queue_steer(loop.ctx, text);
    }

    pub fn clearSteers(self: *TuiRuntime) void {
        if (self.loop) |loop| loop.vtable.clear_steers(loop.ctx);
    }

    pub fn followUp(self: *TuiRuntime, text: []const u8) !void {
        if (self.remote) |remote| {
            if (!self.started) return error.RuntimeNotStarted;
            if (!self.stream_active) return self.submitTurn(text);
            return remote.vtable.follow_up(remote.ctx, text);
        }
        if (!self.started) return error.RuntimeNotStarted;
        const loop = try self.boundLoop() orelse return error.RuntimeNotStarted;
        try loop.vtable.follow_up(loop.ctx, text);
    }

    pub fn clearQueuedMessages(self: *TuiRuntime) void {
        if (self.remote) |remote| return remote.vtable.clear_queued(remote.ctx);
        if (self.loop) |loop| loop.vtable.clear_queued(loop.ctx);
    }

    pub fn queuedCounts(self: *TuiRuntime) QueuedCounts {
        if (self.remote) |remote| return .{ .steering = remote.vtable.steers_pending(remote.ctx), .follow_up = remote.vtable.queued(remote.ctx) };
        const loop = self.loop orelse return .{};
        return loop.vtable.queued_counts(loop.ctx);
    }

    pub fn steersConsumedCount(self: *TuiRuntime) u64 {
        if (self.remote) |remote| return remote.vtable.steers_settled(remote.ctx) + self.remote_steers_started;
        const loop = self.loop orelse return 0;
        return loop.vtable.steers_consumed(loop.ctx);
    }

    pub fn replaceMessages(self: *TuiRuntime, messages: []const ai_types.Message) !void {
        if (self.remote != null) {
            if (messages.len > 0) return error.UnavailableOverOap;
            return;
        }
        if (!self.started) try self.start();
        const loop = try self.boundLoop() orelse return error.RuntimeNotStarted;
        try loop.vtable.replace_messages(loop.ctx, messages);
    }

    pub fn history(self: *TuiRuntime) []const ai_types.Message {
        const loop = self.loop orelse return &.{};
        return loop.vtable.history(loop.ctx);
    }

    pub fn compact(self: *TuiRuntime, options: CompactOptions) !void {
        if (self.remote) |remote| {
            if (!self.started) try self.start();
            if (!remote.vtable.compacts(remote.ctx)) return error.UnavailableOverOap;
            if (self.stream_active) return error.AgentAlreadyStreaming;
            if (!remote.vtable.compactable(remote.ctx)) return error.NothingToCompact;
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
        const loop = try self.boundLoop() orelse return error.RuntimeNotStarted;
        try loop.vtable.compact(loop.ctx, options);
    }

    pub const TranscriptWriter = struct {
        ctx: ?*anyopaque,
        save_fn: *const fn (ctx: ?*anyopaque, allocator: std.mem.Allocator, index: usize, history: []const ai_types.Message) ?[]u8,
    };

    pub fn setSessionId(self: *TuiRuntime, session_id: []const u8) !void {
        const owned = try self.allocator.dupe(u8, session_id);
        self.allocator.free(self.session_id);
        self.session_id = owned;
        const loop = try self.boundLoop() orelse return;
        try loop.vtable.set_session_id(loop.ctx);
    }

    pub fn setCompactionPolicy(self: *TuiRuntime, policy_json: []const u8) !void {
        const remote = self.remote orelse return error.NotOverOap;
        if (!self.started) try self.start();
        try remote.vtable.set_compaction_policy(remote.ctx, policy_json);
    }

    pub fn armAutoCompact(self: *TuiRuntime, at: ?u64, transcripts: []const []const u8, writer: ?TranscriptWriter) !void {
        if (!self.started) try self.start();
        const loop = try self.boundLoop() orelse return error.RuntimeNotStarted;
        try loop.vtable.arm_auto_compact(loop.ctx, at, transcripts, writer);
    }

    pub fn finishCompaction(self: *TuiRuntime, payload: CompactionEnd) void {
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
        const loop = try self.boundLoop() orelse return error.RuntimeNotStarted;
        try loop.vtable.resume_session(loop.ctx);
    }

    pub fn cancel(self: *TuiRuntime) void {
        self.cancelled.store(true, .release);
        if (self.remote) |remote| {
            if (self.stream_active) remote.vtable.cancel(remote.ctx);
            return;
        }
        if (self.loop) |loop| loop.vtable.cancel(loop.ctx);
    }

    pub fn movesWorkspaceLive(self: *const TuiRuntime) bool {
        const remote = self.remote orelse return true;
        return remote.vtable.settings_live(remote.ctx);
    }

    pub fn recordsFromEndpoint(self: *const TuiRuntime) bool {
        const remote = self.remote orelse return false;
        return remote.vtable.records_session(remote.ctx);
    }

    pub fn takeEndpointRecord(self: *TuiRuntime) ?TuiEvent {
        const remote = self.remote orelse return null;
        return remote.vtable.take_record(remote.ctx);
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

    pub fn resetBackpressureState(self: *TuiRuntime) void {
        while (!self.backpressure_mutex.tryLock()) std.atomic.spinLoopHint();
        self.dropped_event_count = 0;
        self.dropped_since_warning = 0;
        self.backpressure_active.store(false, .release);
        self.backpressure_status_active_emitted.store(false, .release);
        self.backpressure_mutex.unlock();
    }

    pub fn decideToolApproval(self: *TuiRuntime, tool_call_id: []const u8, decision: ToolApprovalDecision) !void {
        if (self.remote) |remote| return remote.vtable.decide_approval(remote.ctx, tool_call_id, decision);
        const loop = self.loop orelse return error.ToolApprovalNotPending;
        try loop.vtable.decide_approval(loop.ctx, tool_call_id, decision);
    }

    pub fn resetEventStreamForTurn(self: *TuiRuntime) void {
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

    pub fn push(self: *TuiRuntime, event: TuiEvent) void {
        var mutable = event;
        mutable.setGeneration(self.current_generation);
        mutable.stamp(compat.time.nowMillis());
        self.pushDroppingOldestCounted(mutable);
        self.flushDroppedWarning();
    }

    pub fn pushTerminal(self: *TuiRuntime, event: TuiEvent) void {
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

    fn dupeOwned(self: *TuiRuntime, value: []const u8) !OwnedSlice(u8) {
        return OwnedSlice(u8).initOwned(try self.allocator.dupe(u8, value));
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

    pub fn endRun(self: *TuiRuntime, reason: TuiEndReason) anyerror!void {
        self.completed = true;
        self.stream_active = false;
        self.pushTerminal(.{ .agent_end = .{ .reason = reason } });
        self.event_stream.complete(.{ .reason = reason });
    }

};

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
    const loop = self.loop orelse return error.RuntimeNotStarted;
    try loop.vtable.request_compaction(loop.ctx, focus);
}

fn sessionTakeCompactionRequest(ctx: ?*anyopaque, allocator: std.mem.Allocator) anyerror!?[]u8 {
    const self: *TuiRuntime = @ptrCast(@alignCast(ctx.?));
    const loop = self.loop orelse return null;
    return loop.vtable.take_compaction_request(loop.ctx, allocator);
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

test "local runtime reports steering available" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{});
    defer runtime.deinit();
    try std.testing.expect(runtime.canSteer());
    try std.testing.expect(runtime.createSession().canSteer());
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

test "TUI runtime normalizes hidden minimal thinking level" {
    var runtime = try TuiRuntime.init(std.testing.allocator, .{ .thinking_level = .minimal });
    defer runtime.deinit();

    try std.testing.expectEqual(ai_types.ThinkingLevel.low, runtime.thinkingLevel());
    try runtime.setThinkingLevel(.minimal);
    try std.testing.expectEqual(ai_types.ThinkingLevel.low, runtime.thinkingLevel());
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
