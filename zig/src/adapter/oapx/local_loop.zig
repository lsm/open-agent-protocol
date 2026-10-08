const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const event_stream = @import("event_stream");
const agent = @import("agent");
const agent_types = @import("agent_types");
const transport = @import("transport");
const json_writer = @import("json_writer");
const json_encode = @import("json_encode");
const session_runtime = @import("session_runtime");
const local_tools = @import("tools/registry");
const tool_local_runtime = @import("tool_local_runtime");
const permission = @import("permission");
const OwnedSlice = @import("owned_slice").OwnedSlice;

const SessionRuntime = session_runtime.SessionRuntime;
const SessionHandle = session_runtime.SessionHandle;
const SessionEvent = session_runtime.SessionEvent;
const SessionEventStream = session_runtime.SessionEventStream;
const PermissionMode = session_runtime.PermissionMode;
const output_limit_warning = session_runtime.output_limit_warning;
const SessionEndReason = session_runtime.SessionEndReason;
const QueuedCounts = session_runtime.QueuedCounts;
const CompactOptions = session_runtime.CompactOptions;
const ToolApprovalCallback = session_runtime.ToolApprovalCallback;
const ToolApprovalDecision = session_runtime.ToolApprovalDecision;
const ToolApprovalRequest = session_runtime.ToolApprovalRequest;
const CompactionEnd = @TypeOf(@as(SessionEvent, undefined).compaction_end);

pub const Options = struct {
    protocol: ?agent.ProtocolClient = null,
    permission_engine: ?*permission.PermissionEngine = null,
    permission_mode: session_runtime.PermissionMode = .bypass,
    tool_approval_ctx: ?*anyopaque = null,
    tool_approval_callback: ?ToolApprovalCallback = null,
    run_async: bool = true,
};

const ApprovalDecisionState = struct {
    tool_call_id: []u8 = &.{},
    decision: ?ToolApprovalDecision = null,
    cancelled: bool = false,
};

const ApprovalContext = struct {
    loop: *LocalLoop,
    callback_ctx: ?*anyopaque,
    callback: ?ToolApprovalCallback,
    original_ctx: ?*anyopaque,
    original_callback: ?agent.ToolApprovalFn,
    original_ui_ctx: ?*anyopaque,
    original_ui_callback: ?agent.ToolApprovalUiFn,
    tool_name: []const u8,
};

pub const LocalLoop = struct {
    allocator: std.mem.Allocator,
    protocol: ?agent.ProtocolClient,
    runtime: ?*SessionRuntime = null,
    local_agent: ?agent.Agent = null,
    wrapped_tools: []agent.AgentTool = &.{},
    approval_contexts: []ApprovalContext = &.{},
    tool_protocol: ?tool_local_runtime.LocalToolProtocol = null,
    tool_protocol_override_fn: ?agent_types.ToolProtocolExecuteFn = null,
    tool_protocol_override_ctx: ?*anyopaque = null,
    pending_approval: ApprovalDecisionState = .{},
    approval_mutex: std.atomic.Mutex = .unlocked,
    tool_approval_ctx: ?*anyopaque,
    tool_approval_callback: ?ToolApprovalCallback,
    permission_engine: ?*permission.PermissionEngine,
    compaction_transcript: []u8 = &.{},
    transcript_writer: ?SessionRuntime.TranscriptWriter = null,
    run_transcripts: std.ArrayList([]u8) = .empty,
    run_transcript_saved: []const u8 = "",
    run_async: bool,

    pub fn init(allocator: std.mem.Allocator, options: Options) LocalLoop {
        if (options.permission_engine) |engine| engine.setBypassAll(options.permission_mode == .bypass);
        return .{
            .allocator = allocator,
            .protocol = options.protocol,
            .tool_approval_ctx = options.tool_approval_ctx,
            .tool_approval_callback = options.tool_approval_callback,
            .permission_engine = options.permission_engine,
            .run_async = options.run_async,
        };
    }

    pub fn deinit(self: *LocalLoop) void {
        self.stopAgent();
        self.clearPendingApproval();
        if (self.tool_protocol) |*protocol| protocol.deinit();
        self.allocator.free(self.compaction_transcript);
        self.clearRunTranscripts();
        self.run_transcripts.deinit(self.allocator);
        self.allocator.free(self.approval_contexts);
        self.allocator.free(self.wrapped_tools);
        self.* = undefined;
    }

    pub fn loop(self: *LocalLoop) session_runtime.Loop {
        return .{ .ctx = self, .vtable = &vtable };
    }

    const vtable = session_runtime.Loop.VTable{
        .bind = bind,
        .start = start,
        .stop = stop,
        .idle = idle,
        .set_model = setModel,
        .request_model_switch = requestModelSwitch,
        .set_thinking_level = setThinkingLevel,
        .set_output = setOutput,
        .set_permission_mode = setPermissionMode,
        .adopt_workspace_root = adoptWorkspaceRoot,
        .set_workspace_root = setWorkspaceRoot,
        .set_compact_output = setCompactOutput,
        .submit = submit,
        .steer = steer,
        .queue_steer = queueSteer,
        .clear_steers = clearSteers,
        .follow_up = followUp,
        .clear_queued = clearQueued,
        .queued_counts = queuedCounts,
        .steers_consumed = steersConsumed,
        .replace_messages = replaceMessages,
        .history = history,
        .compact = compact,
        .set_session_id = setSessionId,
        .arm_auto_compact = armAutoCompact,
        .resume_session = resumeSession,
        .cancel = cancel,
        .decide_approval = decideApproval,
        .request_compaction = requestCompaction,
        .take_compaction_request = takeCompactionRequest,
    };

    fn cast(ctx: *anyopaque) *LocalLoop {
        return @ptrCast(@alignCast(ctx));
    }

    fn rt(self: *LocalLoop) *SessionRuntime {
        return self.runtime.?;
    }

    fn bind(ctx: *anyopaque, runtime: *SessionRuntime) anyerror!void {
        const self = cast(ctx);
        self.runtime = runtime;
        if (self.tool_protocol != null) return;
        const count = runtime.original_tools.len;
        const wrapped = try self.allocator.alloc(agent.AgentTool, count);
        errdefer self.allocator.free(wrapped);
        const contexts = try self.allocator.alloc(ApprovalContext, count);
        errdefer self.allocator.free(contexts);
        var protocol = try tool_local_runtime.LocalToolProtocol.init(self.allocator, runtime.original_tools);
        errdefer protocol.deinit();
        self.allocator.free(self.wrapped_tools);
        self.allocator.free(self.approval_contexts);
        if (self.tool_protocol) |*held| held.deinit();
        self.wrapped_tools = wrapped;
        self.approval_contexts = contexts;
        self.tool_protocol = protocol;
        if (self.permission_engine) |engine| engine.setBypassAll(runtime.permission_mode == .bypass);
        self.rebuildWrappedTools();
    }

    fn start(ctx: *anyopaque) anyerror!void {
        const self = cast(ctx);
        const runtime = self.rt();
        const protocol = self.protocol orelse return error.NoProtocolConfigured;
        self.rebuildWrappedTools();
        self.local_agent = agent.Agent.init(self.allocator, .{
            .protocol = protocol,
            .compact_tool_output = runtime.compact_output,
            .permission_engine = self.permission_engine,
            .execute_tool_via_protocol_fn = executeToolProtocol,
            .execute_tool_via_protocol_ctx = self,
            .rewrite_tool_args_fn = rewriteToolArgs,
            .rewrite_tool_args_ctx = self,
        });
        self.local_agent.?.subscribeWithContext(self, onAgentEvent);
        self.local_agent.?.setCompactToolOutput(runtime.compact_output);
        const system_prompt = try self.workspaceSystemPrompt();
        defer self.allocator.free(system_prompt);
        try self.local_agent.?.setSystemPrompt(system_prompt);
        if (runtime.currentModel()) |model| self.local_agent.?.setModel(model);
        self.local_agent.?.setThinkingLevel(runtime.thinking_level);
        try self.applySessionId();
        self.local_agent.?.setOutput(runtime.output);
        self.tool_protocol.?.server.tools.clearRetainingCapacity();
        try self.tool_protocol.?.server.registerTools(self.wrapped_tools);
        self.local_agent.?.setTools(self.wrapped_tools);
    }

    fn stop(ctx: *anyopaque) void {
        cast(ctx).stopAgent();
    }

    fn stopAgent(self: *LocalLoop) void {
        if (self.local_agent) |*local| {
            if (!local.isIdle()) {
                local.abort();
                local.waitForIdle();
            }
            local.unsubscribeWithContext(self, onAgentEvent);
            local.deinit();
            self.local_agent = null;
        }
    }

    fn idle(ctx: *anyopaque) bool {
        const self = cast(ctx);
        if (self.local_agent) |*local| return local.isIdle();
        return true;
    }

    fn setModel(ctx: *anyopaque, model: ai_types.Model) void {
        const self = cast(ctx);
        if (self.local_agent) |*local| local.setModel(model);
    }

    fn requestModelSwitch(ctx: *anyopaque, model: ?ai_types.Model) void {
        const self = cast(ctx);
        if (self.local_agent) |*local| local.requestModelSwitch(model);
    }

    fn setThinkingLevel(ctx: *anyopaque, level: ai_types.ThinkingLevel) void {
        const self = cast(ctx);
        if (self.local_agent) |*local| local.setThinkingLevel(level);
    }

    fn setOutput(ctx: *anyopaque, setting: agent.OutputSetting) void {
        const self = cast(ctx);
        if (self.local_agent) |*local| local.setOutput(setting);
    }

    fn setPermissionMode(ctx: *anyopaque, mode: session_runtime.PermissionMode) anyerror!void {
        const self = cast(ctx);
        if (self.permission_engine) |engine| engine.setBypassAll(mode == .bypass);
        self.rebuildWrappedTools();
        if (self.local_agent) |*local| {
            local.setPermissionEngine(self.permission_engine);
            local.setTools(self.wrapped_tools);
        }
        self.tool_protocol.?.server.tools.clearRetainingCapacity();
        try self.tool_protocol.?.server.registerTools(self.wrapped_tools);
    }

    fn adoptWorkspaceRoot(ctx: *anyopaque, root: []const u8) anyerror!void {
        const self = cast(ctx);
        if (self.permission_engine) |engine| try engine.setWorkspaceRoot(root);
    }

    fn setWorkspaceRoot(ctx: *anyopaque) anyerror!void {
        const self = cast(ctx);
        if (self.local_agent) |*local| {
            const system_prompt = try self.workspaceSystemPrompt();
            defer self.allocator.free(system_prompt);
            try local.setSystemPrompt(system_prompt);
        }
    }

    fn setCompactOutput(ctx: *anyopaque, enabled: bool) void {
        const self = cast(ctx);
        if (self.local_agent) |*local| local.setCompactToolOutput(enabled);
    }

    fn makeUserMessage(self: *LocalLoop, text: []const u8) !ai_types.Message {
        const owned_text = try self.allocator.dupe(u8, text);
        return .{ .user = .{
            .content = .{ .text = owned_text },
            .timestamp = compat.time.nowMillis(),
        } };
    }

    fn submit(ctx: *anyopaque, text: []const u8) anyerror!void {
        const self = cast(ctx);
        const runtime = self.rt();
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        if (runtime.currentModel() == null) return error.NoModelConfigured;
        if (self.run_async) local.waitForIdle();
        runtime.resetEventStreamForTurn();
        runtime.cancelled.store(false, .release);
        runtime.completed = false;
        runtime.last_turn_stop_reason = null;
        if (self.run_async) {
            var msg = try self.makeUserMessage(text);
            defer msg.deinit(self.allocator);
            try local.promptAsync(msg);
        } else {
            const msg = try self.makeUserMessage(text);
            try local.prompt(msg);
        }
    }

    fn steer(ctx: *anyopaque, text: []const u8) anyerror!void {
        const self = cast(ctx);
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        var msg = try self.makeUserMessage(text);
        var queued = false;
        errdefer if (!queued) msg.deinit(self.allocator);
        try local.steer(msg);
        queued = true;
        try self.resumeQueuedMessagesIfIdle();
    }

    fn queueSteer(ctx: *anyopaque, text: []const u8) anyerror!void {
        const self = cast(ctx);
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        var msg = try self.makeUserMessage(text);
        errdefer msg.deinit(self.allocator);
        try local.steer(msg);
    }

    fn clearSteers(ctx: *anyopaque) void {
        const self = cast(ctx);
        const local = &(self.local_agent orelse return);
        local.clearSteeringQueue();
    }

    fn followUp(ctx: *anyopaque, text: []const u8) anyerror!void {
        const self = cast(ctx);
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        var msg = try self.makeUserMessage(text);
        var queued = false;
        errdefer if (!queued) msg.deinit(self.allocator);
        try local.followUp(msg);
        queued = true;
        try self.resumeQueuedMessagesIfIdle();
    }

    fn resumeQueuedMessagesIfIdle(self: *LocalLoop) !void {
        const local = &(self.local_agent orelse return);
        if (!local.isIdle()) return;
        local.validateContinueFromContext() catch return;
        try self.rt().resumeSession();
    }

    fn clearQueued(ctx: *anyopaque) void {
        const self = cast(ctx);
        const local = &(self.local_agent orelse return);
        local.clearAllQueues();
    }

    fn queuedCounts(ctx: *anyopaque) QueuedCounts {
        const self = cast(ctx);
        const local = &(self.local_agent orelse return .{});
        return local.queuedCounts();
    }

    fn steersConsumed(ctx: *anyopaque) u64 {
        const self = cast(ctx);
        const local = &(self.local_agent orelse return 0);
        return local.steeringConsumedCount();
    }

    fn replaceMessages(ctx: *anyopaque, messages: []const ai_types.Message) anyerror!void {
        const self = cast(ctx);
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        if (self.run_async) local.waitForIdle();
        local.clearAllQueues();
        self.rt().resetBackpressureState();
        try local.replaceMessages(messages);
    }

    fn history(ctx: *anyopaque) []const ai_types.Message {
        const self = cast(ctx);
        const local = &(self.local_agent orelse return &.{});
        if (!local.isIdle()) return &.{};
        local.waitForIdle();
        return local._state.messages.items;
    }

    fn compact(ctx: *anyopaque, options: CompactOptions) anyerror!void {
        const self = cast(ctx);
        const runtime = self.rt();
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        if (runtime.currentModel() == null) return error.NoModelConfigured;
        if (!local.isIdle()) return error.AgentAlreadyStreaming;
        local.waitForIdle();
        try local.ensureCompactable();
        const transcript = try self.allocator.dupe(u8, if (options.transcripts.len > 0) options.transcripts[options.transcripts.len - 1] else "");
        self.allocator.free(self.compaction_transcript);
        self.compaction_transcript = transcript;
        runtime.resetEventStreamForTurn();
        runtime.cancelled.store(false, .release);
        runtime.completed = false;
        runtime.push(.{ .compaction_start = .{} });
        local.compactAsync(.{ .focus = options.focus, .transcripts = options.transcripts }, self, onCompaction) catch |err| {
            runtime.finishCompaction(.{ .outcome = .failed, .message = OwnedSlice(u8).initBorrowed(@errorName(err)) });
        };
    }

    fn setSessionId(ctx: *anyopaque) anyerror!void {
        const self = cast(ctx);
        const local = &(self.local_agent orelse return);
        if (!local.isIdle()) return error.AgentAlreadyStreaming;
        try self.applySessionId();
    }

    fn applySessionId(self: *LocalLoop) !void {
        const local = &(self.local_agent orelse return);
        const session_id = self.rt().session_id;
        try local.setSessionId(if (session_id.len > 0) session_id else null);
    }

    fn armAutoCompact(ctx: *anyopaque, at: ?u64, transcripts: []const []const u8, writer: ?SessionRuntime.TranscriptWriter) anyerror!void {
        const self = cast(ctx);
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
        const self: *LocalLoop = @ptrCast(@alignCast(ctx.?));
        defer self.run_transcript_saved = "";
        if (completed or self.run_transcript_saved.len == 0) return;
        const items = self.run_transcripts.items;
        if (items.len == 0 or items[items.len - 1].ptr != self.run_transcript_saved.ptr) return;
        self.allocator.free(self.run_transcripts.pop().?);
    }

    fn clearRunTranscripts(self: *LocalLoop) void {
        for (self.run_transcripts.items) |path| self.allocator.free(path);
        self.run_transcripts.clearRetainingCapacity();
        self.run_transcript_saved = "";
    }

    fn runTranscripts(ctx: ?*anyopaque, messages: []const ai_types.Message) agent.Agent.CompactionTranscripts {
        const self: *LocalLoop = @ptrCast(@alignCast(ctx.?));
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
        const self: *LocalLoop = @ptrCast(@alignCast(ctx.?));
        const payload = compactionEndPayload(self.allocator, self.compaction_transcript, result) catch |err| CompactionEnd{ .outcome = .failed, .message = OwnedSlice(u8).initBorrowed(@errorName(err)) };
        self.rt().finishCompaction(payload);
    }

    fn resumeSession(ctx: *anyopaque) anyerror!void {
        const self = cast(ctx);
        const runtime = self.rt();
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        if (self.run_async) local.waitForIdle();
        try local.validateContinueFromContext();
        runtime.resetEventStreamForTurn();
        runtime.cancelled.store(false, .release);
        runtime.completed = false;
        runtime.last_turn_stop_reason = null;
        if (self.run_async) {
            try local.continueFromContextAsync();
        } else {
            try local.continueFromContext();
        }
    }

    fn cancel(ctx: *anyopaque) void {
        const self = cast(ctx);
        if (self.local_agent) |*local| local.abort();
        while (!self.approval_mutex.tryLock()) std.atomic.spinLoopHint();
        self.pending_approval.cancelled = true;
        self.pending_approval.decision = .reject;
        self.approval_mutex.unlock();
    }

    fn decideApproval(ctx: *anyopaque, tool_call_id: []const u8, decision: ToolApprovalDecision) anyerror!void {
        const self = cast(ctx);
        while (!self.approval_mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.approval_mutex.unlock();
        if (self.pending_approval.tool_call_id.len > 0 and !std.mem.eql(u8, self.pending_approval.tool_call_id, tool_call_id)) return error.ToolApprovalNotPending;
        if (self.pending_approval.tool_call_id.len == 0) {
            self.pending_approval.tool_call_id = try self.allocator.dupe(u8, tool_call_id);
        }
        self.pending_approval.decision = decision;
        self.pending_approval.cancelled = false;
    }

    fn clearPendingApproval(self: *LocalLoop) void {
        while (!self.approval_mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.approval_mutex.unlock();
        if (self.pending_approval.tool_call_id.len > 0) self.allocator.free(self.pending_approval.tool_call_id);
        self.pending_approval = .{};
    }

    fn requestCompaction(ctx: *anyopaque, focus: []const u8) anyerror!void {
        const self = cast(ctx);
        const local = &(self.local_agent orelse return error.RuntimeNotStarted);
        try local.requestCompaction(focus);
    }

    fn takeCompactionRequest(ctx: *anyopaque, allocator: std.mem.Allocator) anyerror!?[]u8 {
        const self = cast(ctx);
        const local = &(self.local_agent orelse return null);
        const focus = local.takeCompactionRequest() orelse return null;
        defer local._allocator.free(focus);
        return try allocator.dupe(u8, focus);
    }

    fn rebuildWrappedTools(self: *LocalLoop) void {
        const runtime = self.rt();
        for (runtime.original_tools, 0..) |tool, i| {
            const bypass = runtime.permission_mode == .bypass;
            self.approval_contexts[i] = .{
                .loop = self,
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

    fn workspaceSystemPrompt(self: *LocalLoop) ![]u8 {
        const workspace_root = self.rt().workspace_root;
        if (workspace_root.len == 0) return self.allocator.dupe(u8, "");
        const moved_rule = if (@import("builtin").os.tag == .windows)
            "A `cd` inside a command does not persist on this platform: every call starts in the same directory."
        else
            "A `cd` in a `Shell` call changes the working directory, and its result reports the directory as a `cwd:` line.";
        return std.fmt.allocPrint(self.allocator,
            \\Default workspace root: {s}
            \\Pass the default workspace root as `workspace_root` to work in the session's current working directory, or name another directory inside the root to work there instead; an absolute path is used as written. {s}
        , .{ workspace_root, moved_rule });
    }

    fn adoptReportedWorkingDirectory(self: *LocalLoop, args_json: []const u8, details_json: []const u8, result: *agent.AgentToolResult) void {
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
        self.rt().adoptWorkingDirectory(directory.string);
        self.appendWorkingDirectoryLine(result);
    }

    fn appendWorkingDirectoryLine(self: *LocalLoop, result: *agent.AgentToolResult) void {
        if (!result.content.is_owned) return;
        const parts = result.content.slice();
        if (parts.len == 0) return;
        const last = parts[parts.len - 1];
        if (last != .text) return;
        const merged = std.fmt.allocPrint(self.allocator, "{s}\ncwd: {s}", .{ last.text.text, self.rt().session_cwd }) catch return;
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

    fn rewriteWorkspaceRoot(self: *LocalLoop, tool_name: []const u8, args_json: []const u8, allocator: std.mem.Allocator) !?[]u8 {
        const runtime = self.rt();
        if (std.mem.startsWith(u8, tool_name, local_tools.mcp_bridge.tool_prefix)) return null;
        if (runtime.session_cwd.len == 0) return null;
        const base = if (directoryOpens(runtime.session_cwd)) runtime.session_cwd else runtime.workspace_root;
        var parsed = std.json.parseFromSlice(std.json.Value, allocator, args_json, .{}) catch return null;
        defer parsed.deinit();
        if (parsed.value != .object) return null;
        if (hasAbsolutePathArgument(parsed.value.object)) return null;
        const existing = parsed.value.object.get("workspace_root") orelse return null;
        if (existing != .string) return null;
        if (!std.mem.eql(u8, existing.string, runtime.workspace_root)) return null;
        if (std.mem.eql(u8, existing.string, base)) return null;
        const held = parsed.value.object.getPtr("workspace_root").?;
        held.* = .{ .string = base };
        return json_encode.valueAlloc(allocator, parsed.value) catch return null;
    }

    fn dupeOwned(self: *LocalLoop, value: []const u8) !OwnedSlice(u8) {
        return OwnedSlice(u8).initOwned(try self.allocator.dupe(u8, value));
    }

    fn firstContentText(self: *LocalLoop, content_json: []const u8) !OwnedSlice(u8) {
        if (content_json.len == 0) return OwnedSlice(u8).initBorrowed("");
        const parsed = std.json.parseFromSlice(std.json.Value, self.allocator, content_json, .{}) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return OwnedSlice(u8).initBorrowed(""),
        };
        defer parsed.deinit();
        if (parsed.value != .array) return OwnedSlice(u8).initBorrowed("");
        for (parsed.value.array.items) |part| {
            if (part != .object) continue;
            const kind = part.object.get("type") orelse continue;
            if (kind != .string or !std.mem.eql(u8, kind.string, "text")) continue;
            const text = part.object.get("text") orelse continue;
            if (text == .string) return self.dupeOwned(text.string);
        }
        return OwnedSlice(u8).initBorrowed("");
    }

    fn handleAgentEndEvent(self: *LocalLoop) anyerror!void {
        const runtime = self.rt();
        const cancelled = runtime.cancelled.load(.acquire);
        if (!cancelled and runtime.last_turn_stop_reason == .length) {
            runtime.push(.{ .system_warning = .{ .message = OwnedSlice(u8).initBorrowed(session_runtime.output_limit_warning) } });
        }
        const reason: SessionEndReason = if (cancelled) .cancelled else if (runtime.last_turn_stop_reason == .@"error") .@"error" else .completed;
        return runtime.endRun(reason);
    }

    fn messageRole(message: ai_types.Message) SessionEvent.MessageRole {
        return switch (message) {
            .user => .user,
            .assistant => .assistant,
            .tool_result => .tool_result,
        };
    }

    fn messageEndPayload(self: *LocalLoop, message: ai_types.Message) !@TypeOf(@as(SessionEvent, undefined).message_end) {
        var payload: @TypeOf(@as(SessionEvent, undefined).message_end) = .{ .role = messageRole(message) };
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

    fn onAgentEvent(ctx: ?*anyopaque, event: agent.AgentEvent) void {
        const self: *LocalLoop = @ptrCast(@alignCast(ctx.?));
        self.handleAgentEvent(event) catch |err| {
            self.rt().push(.{ .@"error" = .{ .message = self.dupeOwned(@errorName(err)) catch OwnedSlice(u8).initBorrowed("") } });
        };
    }

    fn handleAgentEvent(self: *LocalLoop, event: agent.AgentEvent) !void {
        const runtime = self.rt();
        switch (event) {
            .agent_start => runtime.push(.{ .agent_start = .{} }),
            .turn_start => runtime.push(.{ .turn_start = .{} }),
            .message_start => |payload| {
                runtime.push(.{ .message_start = .{ .role = messageRole(payload.message) } });
            },
            .message_update => |payload| {
                if (!isChunk(payload.event)) try self.pushProviderEvent(payload.event);
                try self.pushMessageUpdate(payload.event);
            },
            .message_end => |payload| {
                var message_payload = try self.messageEndPayload(payload.message);
                if (message_payload.role == .user) message_payload.steering = payload.steering;
                runtime.push(.{ .message_end = message_payload });
            },
            .tool_execution_start => |payload| runtime.push(.{ .tool_execution_start = .{
                .tool_call_id = try self.dupeOwned(payload.tool_call_id),
                .tool_name = try self.dupeOwned(payload.tool_name),
                .args_json = try self.dupeOwned(payload.args_json),
            } }),
            .tool_execution_update => |payload| runtime.push(.{ .tool_execution_update = .{
                .tool_call_id = try self.dupeOwned(payload.tool_call_id),
                .tool_name = try self.dupeOwned(payload.tool_name),
                .args_json = try self.dupeOwned(payload.args_json),
                .partial_result_json = try self.dupeOwned(payload.partial_result_json),
            } }),
            .tool_execution_end => |payload| runtime.push(.{ .tool_execution_end = .{
                .tool_call_id = try self.dupeOwned(payload.tool_call_id),
                .tool_name = try self.dupeOwned(payload.tool_name),
                .result_json = try self.dupeOwned(payload.result_json),
                .is_error = payload.is_error,
                .raw_total_bytes = payload.raw_total_bytes,
                .returned_total_bytes = payload.returned_total_bytes,
                .estimated_returned_tokens = payload.estimated_returned_tokens,
                .artifact_count = payload.artifact_count,
                .artifact_refs = try self.formatArtifactRefs(payload.artifacts),
                .result_text = try self.firstContentText(payload.content_json.slice()),
            } }),
            .turn_end => |payload| {
                runtime.last_turn_stop_reason = payload.message.stop_reason;
                if (payload.message.stop_reason == .@"error") {
                    if (payload.message.getErrorMessage()) |message| {
                        runtime.push(.{ .@"error" = .{ .message = try self.dupeOwned(message) } });
                    }
                }
                runtime.pushTerminal(.{ .turn_end = .{ .stop_reason = payload.message.stop_reason } });
            },
            .agent_end => try self.handleAgentEndEvent(),
            .run_failed => |payload| {
                runtime.push(.{ .@"error" = .{ .message = self.dupeOwned(payload.reason.slice()) catch OwnedSlice(u8).initBorrowed(payload.reason.slice()) } });
                try runtime.endRun(.@"error");
            },
            .context_usage => |payload| runtime.push(.{ .context_usage = .{
                .system_prompt_bytes = payload.system_prompt_bytes,
                .message_bytes = payload.message_bytes,
                .tool_definition_bytes = payload.tool_definition_bytes,
                .total_bytes = payload.total_bytes,
                .estimated_tokens = payload.estimated_tokens,
                .message_count = payload.message_count,
                .tool_count = payload.tool_count,
            } }),
            .prompt_segment_usage => |payload| runtime.push(.{ .prompt_segment_usage = .{
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
            .compaction_start => runtime.push(.{ .compaction_start = .{ .in_run = true } }),
            .compaction_end => |payload| try self.pushRunCompactionEnd(payload),
        }
    }

    pub fn pushRunCompactionEnd(self: *LocalLoop, payload: agent_types.CompactionEndPayload) !void {
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
        self.rt().push(.{ .compaction_end = .{
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

    fn formatArtifactRefs(self: *LocalLoop, artifacts: []const ai_types.ArtifactReference) !OwnedSlice(u8) {
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

    fn pushMessageUpdate(self: *LocalLoop, event: ai_types.AssistantMessageEvent) !void {
        const runtime = self.rt();
        switch (event) {
            .text_delta => |payload| runtime.push(.{ .text_delta = .{
                .content_index = payload.content_index,
                .delta = try self.dupeOwned(payload.delta),
            } }),
            .thinking_delta => |payload| runtime.push(.{ .thinking_delta = .{
                .content_index = payload.content_index,
                .delta = try self.dupeOwned(payload.delta),
            } }),
            .toolcall_delta => |payload| runtime.push(.{ .tool_call_delta = .{
                .content_index = payload.content_index,
                .delta = try self.dupeOwned(payload.delta),
            } }),
            else => {},
        }
    }

    fn pushProviderEvent(self: *LocalLoop, event: ai_types.AssistantMessageEvent) !void {
        const event_json = try transport.serializeEvent(event, self.allocator);
        self.rt().push(.{ .provider_event = .{ .event_json = OwnedSlice(u8).initOwned(event_json) } });
    }
};

fn isChunk(event: ai_types.AssistantMessageEvent) bool {
    return switch (event) {
        .text_delta, .thinking_delta, .toolcall_delta => true,
        else => false,
    };
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

pub fn compactionEndPayload(allocator: std.mem.Allocator, transcript_path: []const u8, result: *const agent.compaction.Result) !CompactionEnd {
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

    const self = approval_ctx.loop;
    self.rt().push(.{ .tool_approval_requested = .{
        .tool_call_id = self.dupeOwned(request.tool_call_id) catch OwnedSlice(u8).initBorrowed(""),
        .tool_name = self.dupeOwned(request.tool_name) catch OwnedSlice(u8).initBorrowed(""),
        .args_json = self.dupeOwned(request.args_json) catch OwnedSlice(u8).initBorrowed(""),
    } });
}

fn executeToolProtocol(
    ctx: ?*anyopaque,
    tool_call_id: []const u8,
    tool_name: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?agent.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!agent.AgentToolResult {
    const self: *LocalLoop = @ptrCast(@alignCast(ctx.?));
    var result = try self.tool_protocol.?.executeWithOverride(
        tool_call_id,
        tool_name,
        args_json,
        cancel_token,
        on_update_ctx,
        on_update,
        self.tool_protocol_override_ctx,
        self.tool_protocol_override_fn,
        allocator,
    );
    if (std.mem.eql(u8, tool_name, "Shell")) {
        if (result.getDetailsJson()) |details| self.adoptReportedWorkingDirectory(args_json, details, &result);
    }
    return result;
}

fn rewriteToolArgs(
    ctx: ?*anyopaque,
    tool_name: []const u8,
    args_json: []const u8,
    allocator: std.mem.Allocator,
) anyerror!?[]u8 {
    const self: *LocalLoop = @ptrCast(@alignCast(ctx.?));
    return self.rewriteWorkspaceRoot(tool_name, args_json, allocator);
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

const LoopRuntime = struct {
    loop: LocalLoop,
    runtime: SessionRuntime,

    fn init(allocator: std.mem.Allocator, options: session_runtime.SessionRuntimeOptions) !*LoopRuntime {
        const self = try allocator.create(LoopRuntime);
        errdefer allocator.destroy(self);
        self.loop = LocalLoop.init(allocator, .{
            .protocol = options.protocol,
            .permission_engine = options.permission_engine,
            .permission_mode = options.permission_mode,
            .tool_approval_ctx = options.tool_approval_ctx,
            .tool_approval_callback = options.tool_approval_callback,
            .run_async = options.run_async,
        });
        errdefer self.loop.deinit();
        var with_loop = options;
        with_loop.loop = self.loop.loop();
        self.runtime = try SessionRuntime.init(allocator, with_loop);
        errdefer self.runtime.deinit();
        try LocalLoop.bind(&self.loop, &self.runtime);
        return self;
    }

    fn deinit(self: *LoopRuntime) void {
        const allocator = self.loop.allocator;
        self.runtime.deinit();
        self.loop.deinit();
        allocator.destroy(self);
    }
};

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
            .text => |t| blk: {
                const text = try allocator.dupe(u8, t.text);
                errdefer allocator.free(text);
                const signature = if (t.text_signature) |s| try allocator.dupe(u8, s) else null;
                break :blk .{ .text = .{ .text = text, .text_signature = signature } };
            },
            .thinking => |t| blk: {
                const thinking = try allocator.dupe(u8, t.thinking);
                errdefer allocator.free(thinking);
                const signature = if (t.thinking_signature) |s| try allocator.dupe(u8, s) else null;
                break :blk .{ .thinking = .{ .thinking = thinking, .thinking_signature = signature } };
            },
            .tool_call => |tc| .{ .tool_call = try ai_types.cloneToolCall(allocator, tc) },
            .image => |img| blk: {
                const data = try allocator.dupe(u8, img.data);
                errdefer allocator.free(data);
                const mime_type = try allocator.dupe(u8, img.mime_type);
                break :blk .{ .image = .{ .data = data, .mime_type = mime_type } };
            },
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

fn collectUntilEnd(handle: *SessionHandle, saw_turn_start: *bool, saw_message_start: *bool, saw_text_delta: *bool, saw_message_end: *bool, saw_turn_end: *bool) void {
    while (handle.popEvent()) |event| {
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

fn compactionEndPayloadProbe(allocator: std.mem.Allocator) !void {
    const completed = agent.compaction.Result{ .completed = .{ .text = @constCast("summary"), .messages_before = 3, .tokens_before = 10, .tokens_after = 2, .head_truncated = false } };
    var completed_event = SessionEvent{ .compaction_end = try compactionEndPayload(allocator, "/sessions/s1/compaction-1.jsonl", &completed) };
    completed_event.deinit(allocator);
    const failed = agent.compaction.Result{ .failed = @constCast("overloaded") };
    var failed_event = SessionEvent{ .compaction_end = try compactionEndPayload(allocator, "", &failed) };
    failed_event.deinit(allocator);
}

const CompactionSeen = struct {
    started: bool = false,
    outcome: ?SessionEvent.CompactionOutcome = null,
    text_has_summary: bool = false,
    transcript_matches: bool = false,
    message_has_error: bool = false,
    messages_before: u64 = 0,
};

fn collectCompaction(handle: *SessionHandle, expected_transcript: []const u8) CompactionSeen {
    var seen = CompactionSeen{};
    while (handle.popEvent()) |event| {
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

fn saveNumberedTranscript(ctx: ?*anyopaque, allocator: std.mem.Allocator, index: usize, history: []const ai_types.Message) ?[]u8 {
    _ = ctx;
    _ = history;
    return std.fmt.allocPrint(allocator, "/t/compaction-{d}.jsonl", .{index}) catch null;
}

fn drainEndOfRun(runtime: *SessionRuntime) !struct { warning: ?[]u8, reason: ?SessionEndReason } {
    var warning: ?[]u8 = null;
    errdefer if (warning) |text| std.testing.allocator.free(text);
    var reason: ?SessionEndReason = null;
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

test "a session's window set before the first turn survives the agent starting" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .context_window = 4_000,
        .run_async = false,
    });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;
    try runtime.start();
    defer runtime.stop();

    try std.testing.expectEqual(@as(u64, 4_000), runtime.contextWindow());
    try runtime.setContextWindow(6_000);
    try std.testing.expectEqual(@as(u64, 6_000), runtime.contextWindow());
}

test "runtime registers default local tools and allows overrides" {
    var mock = MockProtocolCtx{};
    const replacement = agent.AgentTool{ .label = "Wrapped Shell", .name = "Shell", .description = "Wrapped shell tool", .parameters_schema_json = "{}", .execute = demoTool };
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .tools = &.{replacement}, .run_async = false });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;
    try std.testing.expect(runtime.tool_registry.resolve("Shell") != null);
    try std.testing.expect(runtime.tool_registry.resolve("Read") != null);
    try std.testing.expectEqualStrings("Wrapped Shell", runtime.tool_registry.resolve("Shell").?.label);
    try std.testing.expect(runtime.original_tools.len >= 4);
}

test "runtime submit turn emits normalized events" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("hi");
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    var saw_turn_start = false;
    var saw_message_start = false;
    var saw_text_delta = false;
    var saw_message_end = false;
    var saw_turn_end = false;
    collectUntilEnd(&handle, &saw_turn_start, &saw_message_start, &saw_text_delta, &saw_message_end, &saw_turn_end);

    try std.testing.expect(saw_turn_start);
    try std.testing.expect(saw_message_start);
    try std.testing.expect(saw_text_delta);
    try std.testing.expect(saw_message_end);
    try std.testing.expect(saw_turn_end);
}

test "local runtime includes startup cwd as default workspace root in provider prompt" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .workspace_root = "/tmp/makai-workspace",
        .run_async = false,
    });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("pwd");
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    try std.testing.expect(mock.saw_workspace_prompt);
}

test "local runtime surfaces provider error message details" {
    var mock = MockProtocolCtx{ .provider_error_message = "provider rejected request: missing workspace_root" };
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try std.testing.expectError(error.AgentLoopFailed, handle.submitTurn("hi"));
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    var saw_detail = false;
    while (handle.popEvent()) |event| {
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
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("hi");
    handle.cancel();
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    var saw_cancelled = false;
    while (handle.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .agent_end) {
            saw_cancelled = ev.agent_end.reason == .cancelled;
            break;
        }
    }
    try std.testing.expect(saw_cancelled);
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
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .tools = &tools,
        .run_async = false,
    });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("use context tool");
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    var saw_tool_end = false;
    while (handle.waitEvent()) |event| {
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
    const approve_runtime_loop = try LoopRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&approve_mock),
        .models = &models,
        .tools = &tools,
        .tool_approval_ctx = &approve_ctx,
        .tool_approval_callback = approvalCallback,
        .permission_mode = .ask,
        .run_async = false,
    });
    defer approve_runtime_loop.deinit();
    const approve_runtime = &approve_runtime_loop.runtime;
    var approve_session = approve_runtime.createSession();
    try approve_session.start();
    try approve_session.submitTurn("use tool");
    if (approve_runtime_loop.loop.local_agent) |*local| local.waitForIdle();

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
    const reject_runtime_loop = try LoopRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&reject_mock),
        .models = &models,
        .tools = &tools,
        .tool_approval_ctx = &reject_ctx,
        .tool_approval_callback = approvalCallback,
        .permission_mode = .ask,
        .run_async = false,
    });
    defer reject_runtime_loop.deinit();
    const reject_runtime = &reject_runtime_loop.runtime;
    var reject_session = reject_runtime.createSession();
    try reject_session.start();
    try reject_session.submitTurn("use tool");
    if (reject_runtime_loop.loop.local_agent) |*local| local.waitForIdle();

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
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("first");

    while (handle.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .agent_end) break;
    }
    try std.testing.expectEqual(@as(usize, 1), mock.call_count);

    try handle.steer("steer after idle");
    try std.testing.expectEqual(@as(usize, 2), mock.call_count);
    try std.testing.expectEqual(@as(usize, 0), handle.queuedCounts().steering);

    var saw_steering_user = false;
    while (handle.popEvent()) |event| {
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
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("first");
    while (handle.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .agent_end) break;
    }

    try handle.steer("steer after idle");

    var tagged_user_message_end = false;
    while (handle.popEvent()) |event| {
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
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("first");
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();
    while (handle.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .agent_end) break;
    }

    try handle.steer("steer after idle");
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    var tagged_user_message_end = false;
    while (handle.popEvent()) |event| {
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
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("first");
    try handle.steer("steer mid tool");

    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    try std.testing.expectEqual(@as(usize, 2), mock.call_count);
    try std.testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
    try std.testing.expectEqual(@as(usize, 4), mock.last_message_count);

    var tagged_user_message_end = false;
    while (handle.popEvent()) |event| {
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

test "a prompt whose end is handled after the loop took a steer is not tagged as the steer" {
    var mock = MockProtocolCtx{ .tool_first = true, .wait_after_tool_first = true };
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    const local = &runtime_loop.loop.local_agent.?;
    var hold = PromptEndHold{ .agent = local };
    local.unsubscribeWithContext(&runtime_loop.loop, LocalLoop.onAgentEvent);
    local.subscribeWithContext(&hold, PromptEndHold.onEvent);
    local.subscribeWithContext(&runtime_loop.loop, LocalLoop.onAgentEvent);
    try handle.submitTurn("first");
    try handle.steer("steer mid tool");
    local.waitForIdle();

    try std.testing.expect(hold.held);
    try std.testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
    var prompt_tagged: ?bool = null;
    var steer_tagged: ?bool = null;
    while (handle.popEvent()) |event| {
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
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("first");
    try handle.steer("steer during response");

    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    try std.testing.expectEqual(@as(usize, 2), mock.call_count);
    try std.testing.expectEqual(@as(usize, 0), handle.queuedCounts().steering);

    var saw_steering_user = false;
    while (handle.popEvent()) |event| {
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
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("first");
    try handle.followUp("queued follow-up");

    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    try std.testing.expectEqual(@as(usize, 2), mock.call_count);
    try std.testing.expectEqual(@as(usize, 0), handle.queuedCounts().follow_up);
    try std.testing.expectEqual(@as(u64, 0), runtime.steersConsumedCount());

    var follow_up_ends: usize = 0;
    var follow_up_tagged = false;
    while (handle.popEvent()) |event| {
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

test "compactionEndPayload survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, compactionEndPayloadProbe, .{});
}

test "runtime compaction swaps in the model's summary and reports it with its transcript" {
    var mock = MockProtocolCtx{};
    var wide = test_model_a;
    wide.context_window = 200_000;
    const models = [_]ai_types.Model{wide};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("first");
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();
    _ = collectCompaction(&handle, "");

    mock.reply_text = "<summary>\nkept state\n</summary>";
    const transcripts = [_][]const u8{ "/sessions/s1/compaction-1.jsonl", "/sessions/s1/compaction-2.jsonl" };
    try handle.compact(.{ .focus = "tests", .transcripts = &transcripts });
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    const seen = collectCompaction(&handle, "/sessions/s1/compaction-2.jsonl");
    try std.testing.expect(seen.started);
    try std.testing.expectEqual(@as(?SessionEvent.CompactionOutcome, .completed), seen.outcome);
    try std.testing.expect(seen.text_has_summary);
    try std.testing.expect(seen.transcript_matches);
    try std.testing.expectEqual(@as(u64, 2), seen.messages_before);
    try std.testing.expectEqual(@as(usize, 2), mock.call_count);
    try std.testing.expectEqual(@as(usize, 2), runtime.history().len);
    try std.testing.expect(std.mem.indexOf(u8, runtime.history()[0].user.content.text, "- /sessions/s1/compaction-1.jsonl\n- /sessions/s1/compaction-2.jsonl") != null);
    try std.testing.expectError(error.NothingToCompact, handle.compact(.{}));
}

test "runtime compaction reports a provider failure and keeps the history" {
    var mock = MockProtocolCtx{};
    var wide = test_model_a;
    wide.context_window = 200_000;
    const models = [_]ai_types.Model{wide};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("first");
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();
    _ = collectCompaction(&handle, "");

    mock.provider_error_message = "overloaded";
    try handle.compact(.{});
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    const seen = collectCompaction(&handle, "");
    try std.testing.expectEqual(@as(?SessionEvent.CompactionOutcome, .failed), seen.outcome);
    try std.testing.expect(seen.message_has_error);
    try std.testing.expectEqual(@as(usize, 2), runtime.history().len);
    try std.testing.expectEqualStrings("first", runtime.history()[0].user.content.text);
}

test "runtime tags consumed steer message_end with steering provenance" {
    var mock = MockProtocolCtx{ .wait_before_text_first = true };
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("first");
    try handle.steer("steer during response");

    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    try std.testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
    try std.testing.expectEqual(@as(u64, 1), handle.steersConsumedCount());

    var steer_message_end_tagged = false;
    var prompt_message_end_untagged = false;
    while (handle.popEvent()) |event| {
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
    var mock = MockProtocolCtx{ .wait_before_text_first = true, .deliver_flood_second = SessionEventStream.usable_capacity - 2 };
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("first");
    try handle.steer("steer during response");

    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();
    try std.testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
    try std.testing.expect(runtime.dropped_event_count > 0);

    var saw_user_message_end = false;
    while (handle.popEvent()) |event| {
        var ev = event;
        defer ev.deinit(std.testing.allocator);
        if (ev == .message_end and ev.message_end.role == .user) saw_user_message_end = true;
    }
    try std.testing.expect(saw_user_message_end);
    try std.testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
}

test "a session id set before the agent starts reaches it at start, and a later one replaces it" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    try runtime.setSessionId("ses-before-start");
    try std.testing.expect(runtime_loop.loop.local_agent == null);
    try runtime.start();
    try std.testing.expectEqualStrings("ses-before-start", runtime_loop.loop.local_agent.?._session_id.?);

    try runtime.setSessionId("ses-resumed");
    try std.testing.expectEqualStrings("ses-resumed", runtime_loop.loop.local_agent.?._session_id.?);
}

test "runtime clears queued messages before replacing messages" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.steer("steer now");
    try std.testing.expectEqual(@as(usize, 1), handle.queuedCounts().total());

    try runtime.replaceMessages(&.{});

    try std.testing.expectEqual(@as(usize, 0), handle.queuedCounts().total());
}

test "event stream resets between turns" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("first");

    var saw_turn_start = false;
    var saw_message_start = false;
    var saw_text_delta = false;
    var saw_message_end = false;
    var saw_turn_end = false;
    collectUntilEnd(&handle, &saw_turn_start, &saw_message_start, &saw_text_delta, &saw_message_end, &saw_turn_end);

    try handle.submitTurn("second");
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    saw_turn_start = false;
    saw_message_start = false;
    saw_text_delta = false;
    saw_message_end = false;
    saw_turn_end = false;
    collectUntilEnd(&handle, &saw_turn_start, &saw_message_start, &saw_text_delta, &saw_message_end, &saw_turn_end);

    try std.testing.expectEqual(@as(usize, 2), mock.call_count);
    try std.testing.expect(saw_turn_start);
    try std.testing.expect(saw_message_start);
    try std.testing.expect(saw_text_delta);
    try std.testing.expect(saw_message_end);
    try std.testing.expect(saw_turn_end);
}

test "terminal events survive full TUI queue" {
    var mock = MockProtocolCtx{ .flood_count = SessionEventStream.usable_capacity + 45 };
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("flood");

    var saw_turn_end = false;
    var saw_agent_end = false;
    while (handle.waitEvent()) |event| {
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
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .tools = &tools,
        .permission_mode = .ask,
        .run_async = false,
    });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("use tool");
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    var saw_rejected_tool = false;
    while (handle.waitEvent()) |event| {
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
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .tools = &tools,
        .permission_engine = &engine,
        .run_async = false,
    });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    try std.testing.expectEqual(PermissionMode.bypass, runtime.permissionMode());
    try std.testing.expect(engine.evaluate("shell", "{\"command\":\"rm -rf /\"}") == .allow);
    for (runtime_loop.loop.wrapped_tools) |tool| {
        try std.testing.expect(tool.approval_fn == null);
        try std.testing.expect(tool.approval_ui_fn == null);
    }

    try runtime.setPermissionMode(.ask);
    try std.testing.expectEqual(PermissionMode.ask, runtime.permissionMode());
    try std.testing.expect(engine.evaluate("shell", "{\"command\":\"rm -rf /\"}") == .deny);
    var found_demo = false;
    for (runtime_loop.loop.wrapped_tools) |tool| {
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
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .tools = &tools,
        .tool_approval_ctx = &approval_ctx,
        .tool_approval_callback = approvalCallback,
        .permission_mode = .ask,
        .run_async = false,
    });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("use tool");
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    var saw_tui_approval = false;
    while (handle.waitEvent()) |event| {
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
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.switchModel("model-b");
    try handle.submitTurn("hi");
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    var saw_turn_start = false;
    var saw_message_start = false;
    var saw_text_delta = false;
    var saw_message_end = false;
    var saw_turn_end = false;
    collectUntilEnd(&handle, &saw_turn_start, &saw_message_start, &saw_text_delta, &saw_message_end, &saw_turn_end);

    try std.testing.expectEqualStrings("model-b", mock.last_model_id);
}

test "thinking level affects next local turn" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try runtime.setThinkingLevel(.high);
    try handle.submitTurn("hi");
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    var saw_turn_start = false;
    var saw_message_start = false;
    var saw_text_delta = false;
    var saw_message_end = false;
    var saw_turn_end = false;
    collectUntilEnd(&handle, &saw_turn_start, &saw_message_start, &saw_text_delta, &saw_message_end, &saw_turn_end);

    try std.testing.expectEqual(ai_types.ThinkingLevel.high, mock.last_thinking_level);
}

test "model switch is rejected while async turn is running" {
    var mock = MockProtocolCtx{ .wait_for_cancel = true };
    const models = [_]ai_types.Model{ test_model_a, test_model_b };
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("hi");
    try std.testing.expectError(error.AgentAlreadyStreaming, handle.switchModel("model-b"));
    handle.cancel();
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();
    try handle.switchModel("model-b");
    try std.testing.expectEqualStrings("model-b", runtime.currentModel().?.id);
}

test "failed resume does not reset event stream" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try std.testing.expectError(error.NoMessagesToContinue, handle.resumeSession());
    try std.testing.expectEqual(@as(usize, 0), mock.call_count);
    try std.testing.expect(!runtime.stream_active);
    try std.testing.expect(handle.popEvent() == null);
}

test "async submit without selected model fails before stream reset" {
    var mock = MockProtocolCtx{};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .run_async = true });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try std.testing.expectError(error.NoModelConfigured, handle.submitTurn("hi"));
    try std.testing.expectEqual(@as(usize, 0), mock.call_count);
    try std.testing.expect(!runtime.stream_active);
    try std.testing.expect(handle.popEvent() == null);
}

test "failed turns emit error end reason" {
    var mock = MockProtocolCtx{ .force_error = true };
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try std.testing.expectError(error.AgentLoopFailed, handle.submitTurn("fail"));
    if (runtime_loop.loop.local_agent) |*local| local.waitForIdle();

    var saw_error_detail = false;
    var saw_error_end = false;
    while (handle.waitEvent()) |event| {
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

test "a compaction inside a run that does not complete gives back the transcript slot it saved" {
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{test_model_a}, .run_async = false });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;
    try runtime_loop.loop.run_transcripts.append(std.testing.allocator, try std.testing.allocator.dupe(u8, "/t/compaction-1.jsonl"));
    runtime_loop.loop.transcript_writer = .{ .ctx = null, .save_fn = saveNumberedTranscript };

    const first = LocalLoop.runTranscripts(&runtime_loop.loop, &.{});
    try std.testing.expectEqual(@as(usize, 2), first.paths.len);
    try std.testing.expectEqualStrings("/t/compaction-2.jsonl", first.paths[1]);
    try std.testing.expectEqualStrings("/t/compaction-2.jsonl", first.saved);

    LocalLoop.settleRunTranscript(&runtime_loop.loop, false);
    try std.testing.expectEqual(@as(usize, 1), runtime_loop.loop.run_transcripts.items.len);
    try runtime_loop.loop.pushRunCompactionEnd(.{ .outcome = .failed });

    const second = LocalLoop.runTranscripts(&runtime_loop.loop, &.{});
    try std.testing.expectEqualStrings("/t/compaction-2.jsonl", second.paths[1]);
    LocalLoop.settleRunTranscript(&runtime_loop.loop, true);
    try std.testing.expectEqual(@as(usize, 2), runtime_loop.loop.run_transcripts.items.len);
    try runtime_loop.loop.pushRunCompactionEnd(.{ .outcome = .completed, .text = OwnedSlice(u8).initBorrowed("summary"), .transcript = OwnedSlice(u8).initBorrowed(second.saved) });

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

test "SessionRuntime replaceMessages clears stale backpressure counters" {
    var mock = MockProtocolCtx{};
    const models = [_]ai_types.Model{test_model_a};
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .protocol = makeProtocol(&mock), .models = &models, .run_async = false });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;
    var handle = runtime.createSession();
    try handle.start();

    runtime.dropped_event_count = 9;
    runtime.dropped_since_warning = 2;
    runtime.backpressure_active.store(true, .release);

    try runtime.replaceMessages(&.{});

    const bp = runtime.backpressureState();
    try std.testing.expect(!bp.active);
    try std.testing.expectEqual(@as(u64, 0), bp.dropped_count);
}

test "runtime warns before ending a run whose last reply hit the output token limit" {
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{test_model_a}, .run_async = false });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    try runtime_loop.loop.handleAgentEvent(.{ .turn_end = .{ .message = replyEndingWith(.length) } });
    try runtime_loop.loop.handleAgentEvent(.{ .agent_end = .{} });

    const ended = try drainEndOfRun(runtime);
    defer if (ended.warning) |text| std.testing.allocator.free(text);
    try std.testing.expectEqualStrings(output_limit_warning, ended.warning.?);
    try std.testing.expectEqual(@as(?SessionEndReason, .completed), ended.reason);
}

test "wrapping a tool preserves the operation kind its definition declares" {
    const owned = try std.testing.allocator.dupe(agent.AgentTool, local_tools.defaultTools());
    defer std.testing.allocator.free(owned);
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .tools = owned, .workspace_root = "/workspace" });
    defer runtime_loop.deinit();
    runtime_loop.loop.rebuildWrappedTools();
    try std.testing.expectEqual(owned.len, runtime_loop.loop.wrapped_tools.len);
    for (runtime_loop.loop.wrapped_tools, owned) |wrapped, original| {
        try std.testing.expectEqualStrings(original.name, wrapped.name);
        try std.testing.expectEqual(original.operation, wrapped.operation);
    }
}

test "the rewrite replaces the session root with the working directory and leaves other roots alone" {
    const cwd = try std.process.currentPathAlloc(std.testing.io, std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const sub = try std.fs.path.join(std.testing.allocator, &.{ cwd, "zig" });
    defer std.testing.allocator.free(sub);
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .workspace_root = cwd });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;
    runtime.adoptWorkingDirectory(sub);

    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"path\":\"a.txt\"}}", .{cwd});
    defer std.testing.allocator.free(args);
    const rewritten = (try runtime_loop.loop.rewriteWorkspaceRoot("file_read", args, std.testing.allocator)).?;
    defer std.testing.allocator.free(rewritten);
    var parsed = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, rewritten, .{});
    defer parsed.deinit();
    try std.testing.expectEqualStrings(sub, parsed.value.object.get("workspace_root").?.string);
    try std.testing.expectEqualStrings("a.txt", parsed.value.object.get("path").?.string);

    try std.testing.expect(try runtime_loop.loop.rewriteWorkspaceRoot("artifact_retrieve", "{\"reference\":\"shell_execute:1\"}", std.testing.allocator) == null);
    const nested = ("[" ** 400) ++ "1" ++ ("]" ** 400);
    const deep_args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"path\":\"a.txt\",\"deep\":{s}}}", .{ cwd, nested });
    defer std.testing.allocator.free(deep_args);
    const deep_rewritten = (try runtime_loop.loop.rewriteWorkspaceRoot("file_read", deep_args, std.testing.allocator)).?;
    defer std.testing.allocator.free(deep_rewritten);
    try std.testing.expect(std.mem.indexOf(u8, deep_rewritten, nested) != null);
    const abs_args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"path\":\"{s}\"}}", .{ cwd, sub });
    defer std.testing.allocator.free(abs_args);
    try std.testing.expect(try runtime_loop.loop.rewriteWorkspaceRoot("file_read", abs_args, std.testing.allocator) == null);
    const other = try std.fs.path.join(std.testing.allocator, &.{ sub, "src" });
    defer std.testing.allocator.free(other);
    const args_other = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"path\":\"a.txt\"}}", .{other});
    defer std.testing.allocator.free(args_other);
    try std.testing.expect(try runtime_loop.loop.rewriteWorkspaceRoot("file_read", args_other, std.testing.allocator) == null);

    const missing = try std.fs.path.join(std.testing.allocator, &.{ cwd, "no-such-working-directory" });
    defer std.testing.allocator.free(missing);
    runtime.adoptWorkingDirectory(missing);
    try std.testing.expectEqualStrings(missing, runtime.workingDirectory());
    try std.testing.expect(try runtime_loop.loop.rewriteWorkspaceRoot("file_read", args, std.testing.allocator) == null);
}

test "the system prompt is fixed and adoption does not rewrite it" {
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .workspace_root = "/workspace" });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    const before = try runtime_loop.loop.workspaceSystemPrompt();
    defer std.testing.allocator.free(before);
    try std.testing.expect(std.mem.indexOf(u8, before, "Default workspace root: /workspace") != null);
    try std.testing.expect(std.mem.indexOf(u8, before, "Current working directory:") == null);

    runtime.adoptWorkingDirectory("/workspace/sub");
    try std.testing.expectEqualStrings("/workspace/sub", runtime.workingDirectory());

    const after = try runtime_loop.loop.workspaceSystemPrompt();
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
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{
        .protocol = makeProtocol(&mock),
        .models = &models,
        .tools = &tools,
        .workspace_root = "/tmp/makai-workspace",
        .run_async = false,
    });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    var handle = runtime.createSession();
    try handle.start();
    try handle.submitTurn("move");
    try std.testing.expectEqualStrings("/tmp/makai-workspace/sub", runtime.workingDirectory());
}

test "only a command that moved moves the session, and the result says where it is" {
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .workspace_root = "/tmp/makai-workspace" });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    const parts = try std.testing.allocator.alloc(ai_types.UserContentPart, 1);
    parts[0] = .{ .text = .{ .text = try std.testing.allocator.dupe(u8, "stdout:\n\nstderr:\n") } };
    var result = agent.AgentToolResult{ .content = OwnedSlice(ai_types.UserContentPart).initOwned(parts) };
    defer result.deinit(std.testing.allocator);
    const details = "{\"working_directory\":\"/tmp/makai-workspace/sub\",\"working_directory_observed\":true}";

    runtime_loop.loop.adoptReportedWorkingDirectory("{\"workspace_root\":\"/tmp/makai-workspace/sub\"}", details, &result);
    try std.testing.expectEqualStrings("/tmp/makai-workspace", runtime.workingDirectory());
    try std.testing.expectEqualStrings("stdout:\n\nstderr:\n", result.content.slice()[0].text.text);

    runtime_loop.loop.adoptReportedWorkingDirectory("{\"workspace_root\":\"/tmp/makai-workspace\"}", details, &result);
    try std.testing.expectEqualStrings("/tmp/makai-workspace/sub", runtime.workingDirectory());
    try std.testing.expect(std.mem.endsWith(u8, result.content.slice()[0].text.text, "\ncwd: /tmp/makai-workspace/sub"));

    const big = "{\"raw_bytes\":40000,\"compressed\":true,\"details\":{\"working_directory\":\"/tmp/makai-workspace/big\",\"working_directory_observed\":true}}";
    runtime_loop.loop.adoptReportedWorkingDirectory("{\"workspace_root\":\"/tmp/makai-workspace\"}", big, &result);
    try std.testing.expectEqualStrings("/tmp/makai-workspace/big", runtime.workingDirectory());
    try std.testing.expect(std.mem.endsWith(u8, result.content.slice()[0].text.text, "\ncwd: /tmp/makai-workspace/big"));
}

test "runtime ends a run whose last reply finished without an output-limit warning" {
    const runtime_loop = try LoopRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{test_model_a}, .run_async = false });
    defer runtime_loop.deinit();
    const runtime = &runtime_loop.runtime;

    try runtime_loop.loop.handleAgentEvent(.{ .turn_end = .{ .message = replyEndingWith(.stop) } });
    try runtime_loop.loop.handleAgentEvent(.{ .agent_end = .{} });

    const ended = try drainEndOfRun(runtime);
    defer if (ended.warning) |text| std.testing.allocator.free(text);
    try std.testing.expect(ended.warning == null);
    try std.testing.expectEqual(@as(?SessionEndReason, .completed), ended.reason);
}
