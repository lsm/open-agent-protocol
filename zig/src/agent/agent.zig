const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const tool_local_runtime = @import("tool_local_runtime");
const event_stream_mod = @import("event_stream");
const types = @import("agent_types");
const agent_loop = @import("agent_loop");
const compaction = @import("compaction.zig");

fn defaultIo() std.Io {
    return if (@import("builtin").is_test)
        std.testing.io
    else
        std.Io.Threaded.global_single_threaded.io();
}

pub const AgentEvent = types.AgentEvent;
pub const AgentEventStream = types.AgentEventStream;
pub const AgentLoopResult = types.AgentLoopResult;
pub const AgentTool = types.AgentTool;
const Listener = struct {
    callback: *const fn (ctx: ?*anyopaque, event: AgentEvent) void,
    ctx: ?*anyopaque = null,
};
pub const AgentToolResult = types.AgentToolResult;
pub const AgentState = types.AgentState;
pub const AgentContext = types.AgentContext;
pub const QueueMode = types.QueueMode;
pub const ProtocolClient = types.ProtocolClient;
pub const TransformContextFn = types.TransformContextFn;
pub const ConvertToLlmFn = types.ConvertToLlmFn;
pub const GetApiKeyFn = types.GetApiKeyFn;
pub const GetSteeringMessagesFn = types.GetSteeringMessagesFn;
pub const GetFollowUpMessagesFn = types.GetFollowUpMessagesFn;

pub const AgentOptions = struct {
    initial_state: ?AgentState = null,

    protocol: ProtocolClient,

    convert_to_llm_fn: ?ConvertToLlmFn = null,
    convert_to_llm_ctx: ?*anyopaque = null,
    transform_context_fn: ?TransformContextFn = null,
    transform_context_ctx: ?*anyopaque = null,

    steering_mode: QueueMode = .one_at_a_time,
    follow_up_mode: QueueMode = .one_at_a_time,

    session_id: ?[]const u8 = null,
    execute_tool_via_protocol_fn: ?types.ToolProtocolExecuteFn = null,
    execute_tool_via_protocol_ctx: ?*anyopaque = null,
    rewrite_tool_args_fn: ?types.ToolArgsRewriteFn = null,
    rewrite_tool_args_ctx: ?*anyopaque = null,
    get_api_key_fn: ?GetApiKeyFn = null,
    get_api_key_ctx: ?*anyopaque = null,
    thinking_budgets: ?ai_types.ThinkingBudgets = null,
    max_retry_delay_ms: ?u32 = 60_000,
    compact_tool_output: bool = false,
    permission_engine: ?*types.permission.PermissionEngine = null,
};

pub const Agent = struct {
    const ContinueRequest = struct {
        messages: []ai_types.Message,
        skip_steering: bool,
        steering: bool = false,
    };

    _state: AgentState,
    _allocator: std.mem.Allocator,

    _protocol: ProtocolClient,

    _listeners: std.ArrayList(Listener),

    _cancel_token: ?ai_types.CancelToken,
    _pending_cancel: std.atomic.Value(bool),
    _is_running: bool,

    _steering_queue: std.ArrayList(ai_types.Message),
    _follow_up_queue: std.ArrayList(ai_types.Message),
    _steering_mode: QueueMode,
    _follow_up_mode: QueueMode,
    _steering_consumed: std.atomic.Value(u64) = std.atomic.Value(u64).init(0),

    _skip_initial_steering_poll: bool,

    _convert_to_llm_fn: ?ConvertToLlmFn,
    _convert_to_llm_ctx: ?*anyopaque,
    _transform_context_fn: ?TransformContextFn,
    _transform_context_ctx: ?*anyopaque,
    _session_id: ?[]const u8,
    _execute_tool_via_protocol_fn: ?types.ToolProtocolExecuteFn,
    _execute_tool_via_protocol_ctx: ?*anyopaque,
    _rewrite_tool_args_fn: ?types.ToolArgsRewriteFn,
    _rewrite_tool_args_ctx: ?*anyopaque,
    _local_tool_protocol: ?*tool_local_runtime.LocalToolProtocol,
    _get_api_key_fn: ?GetApiKeyFn,
    _get_api_key_ctx: ?*anyopaque,
    _thinking_budgets: ?ai_types.ThinkingBudgets,
    _max_retry_delay_ms: ?u32,
    _compact_tool_output: bool,
    _output: agent_loop.OutputSetting = .auto,
    _auto_compact_at: ?u64 = null,
    _compaction_request: ?[]u8 = null,
    _compaction_host: ?CompactionHost = null,
    _next_model: ?ai_types.Model = null,
    _retired_messages: std.ArrayList(ai_types.Message) = .empty,
    _permission_engine: ?*types.permission.PermissionEngine,

    _thread: ?std.Thread,
    _done_event: std.Io.Event,
    _mutex: std.Io.Mutex,

    pub fn init(allocator: std.mem.Allocator, options: AgentOptions) Agent {
        var initial_state = options.initial_state;
        if (initial_state == null) {
            initial_state = AgentState.init(allocator);
        }

        return .{
            ._state = initial_state.?,
            ._allocator = allocator,
            ._protocol = options.protocol,
            ._listeners = std.ArrayList(Listener).empty,
            ._cancel_token = null,
            ._pending_cancel = std.atomic.Value(bool).init(false),
            ._is_running = false,
            ._steering_queue = std.ArrayList(ai_types.Message).empty,
            ._follow_up_queue = std.ArrayList(ai_types.Message).empty,
            ._steering_mode = options.steering_mode,
            ._follow_up_mode = options.follow_up_mode,
            ._skip_initial_steering_poll = false,
            ._convert_to_llm_fn = options.convert_to_llm_fn,
            ._convert_to_llm_ctx = options.convert_to_llm_ctx,
            ._transform_context_fn = options.transform_context_fn,
            ._transform_context_ctx = options.transform_context_ctx,
            ._session_id = options.session_id,
            ._execute_tool_via_protocol_fn = options.execute_tool_via_protocol_fn,
            ._execute_tool_via_protocol_ctx = options.execute_tool_via_protocol_ctx,
            ._rewrite_tool_args_fn = options.rewrite_tool_args_fn,
            ._rewrite_tool_args_ctx = options.rewrite_tool_args_ctx,
            ._local_tool_protocol = null,
            ._get_api_key_fn = options.get_api_key_fn,
            ._get_api_key_ctx = options.get_api_key_ctx,
            ._thinking_budgets = options.thinking_budgets,
            ._max_retry_delay_ms = options.max_retry_delay_ms,
            ._compact_tool_output = options.compact_tool_output,
            ._permission_engine = options.permission_engine,
            ._thread = null,
            ._done_event = .is_set,
            ._mutex = .init,
        };
    }

    pub fn deinit(self: *Agent) void {
        if (self._thread != null) {
            self.waitForIdle();
        }

        if (self._local_tool_protocol) |local| {
            local.deinit();
            self._allocator.destroy(local);
            self._local_tool_protocol = null;
        }

        self.clearAllQueues();
        if (self._compaction_request) |focus| self._allocator.free(focus);
        self._compaction_request = null;
        self._steering_queue.deinit(self._allocator);
        self._follow_up_queue.deinit(self._allocator);

        self._listeners.deinit(self._allocator);

        self.freeRetiredMessages();
        self._retired_messages.deinit(self._allocator);
        self._state.deinit();

        if (self._session_id) |sid| {
            self._allocator.free(sid);
        }

        self.* = undefined;
    }

    fn legacyListenerShim(ctx: ?*anyopaque, event: AgentEvent) void {
        const callback: *const fn (event: AgentEvent) void = @ptrCast(@alignCast(ctx.?));
        callback(event);
    }

    pub fn subscribe(self: *Agent, callback: *const fn (event: AgentEvent) void) void {
        self.subscribeWithContext(@ptrCast(@constCast(callback)), legacyListenerShim);
    }

    pub fn subscribeWithContext(
        self: *Agent,
        ctx: ?*anyopaque,
        callback: *const fn (ctx: ?*anyopaque, event: AgentEvent) void,
    ) void {
        self._listeners.append(self._allocator, .{ .callback = callback, .ctx = ctx }) catch {};
    }

    pub fn unsubscribe(self: *Agent, callback: *const fn (event: AgentEvent) void) void {
        for (self._listeners.items, 0..) |listener, i| {
            if (listener.callback == legacyListenerShim and listener.ctx == @as(?*anyopaque, @ptrCast(@constCast(callback)))) {
                _ = self._listeners.orderedRemove(i);
                return;
            }
        }
    }

    pub fn unsubscribeWithContext(self: *Agent, ctx: ?*anyopaque, callback: *const fn (ctx: ?*anyopaque, event: AgentEvent) void) void {
        for (self._listeners.items, 0..) |listener, i| {
            if (listener.callback == callback and listener.ctx == ctx) {
                _ = self._listeners.orderedRemove(i);
                return;
            }
        }
    }

    pub fn state(self: Agent) AgentState {
        return self._state;
    }

    pub fn isStreaming(self: Agent) bool {
        return self._state.is_streaming;
    }

    pub const QueuedCounts = struct {
        steering: usize = 0,
        follow_up: usize = 0,

        pub fn total(self: QueuedCounts) usize {
            return self.steering + self.follow_up;
        }
    };

    pub fn hasQueuedMessages(self: Agent) bool {
        return self._steering_queue.items.len > 0 or self._follow_up_queue.items.len > 0;
    }

    pub fn queuedCounts(self: *Agent) QueuedCounts {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());
        return .{
            .steering = self._steering_queue.items.len,
            .follow_up = self._follow_up_queue.items.len,
        };
    }

    pub fn validateContinueFromContext(self: *Agent) !void {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());

        if (self._state.is_streaming or self._thread != null) {
            return error.AgentAlreadyStreaming;
        }
        if (self._state.model == null) {
            return error.NoModelConfigured;
        }

        const messages = self._state.messages.items;
        if (messages.len == 0) return error.NoMessagesToContinue;
        if (messages[messages.len - 1] == .assistant and self._steering_queue.items.len == 0 and self._follow_up_queue.items.len == 0) {
            return error.CannotContinueFromAssistant;
        }
    }

    pub fn setSystemPrompt(self: *Agent, system_prompt: []const u8) !void {
        const owned = try self._allocator.dupe(u8, system_prompt);
        if (self._state.system_prompt.len > 0) {
            self._allocator.free(self._state.system_prompt);
        }
        self._state.system_prompt = owned;
    }

    pub fn setModel(self: *Agent, model: ai_types.Model) void {
        self._state.model = model;
    }

    fn ensureSessionId(self: *Agent) !void {
        if (self._session_id != null) return;
        var bytes: [8]u8 = undefined;
        compat.random.fillRandomBytes(&bytes);
        self._session_id = try std.fmt.allocPrint(self._allocator, "oapx-{s}", .{&std.fmt.bytesToHex(bytes, .lower)});
    }

    pub fn setSessionId(self: *Agent, session_id: ?[]const u8) !void {
        const next: ?[]const u8 = if (session_id) |id| try self._allocator.dupe(u8, id) else null;
        if (self._session_id) |previous| self._allocator.free(previous);
        self._session_id = next;
    }

    pub fn setThinkingLevel(self: *Agent, level: ai_types.ThinkingLevel) void {
        self._state.thinking_level = level;
    }

    pub fn setTools(self: *Agent, tools: []const AgentTool) void {
        self._state.tools = tools;
    }

    pub fn setOutput(self: *Agent, setting: agent_loop.OutputSetting) void {
        self._output = setting;
    }

    pub const CompactionTranscripts = struct {
        paths: []const []const u8 = &.{},
        saved: []const u8 = "",
    };

    pub const CompactionHost = struct {
        ctx: ?*anyopaque = null,
        transcripts_fn: *const fn (ctx: ?*anyopaque, history: []const ai_types.Message) CompactionTranscripts,
        settled_fn: ?*const fn (ctx: ?*anyopaque, completed: bool) void = null,
    };

    fn settleTranscripts(self: *Agent, completed: bool) void {
        const host = self._compaction_host orelse return;
        const settled = host.settled_fn orelse return;
        settled(host.ctx, completed);
    }

    pub fn requestModelSwitch(self: *Agent, model: ?ai_types.Model) void {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());
        self._next_model = model;
    }

    fn nextModel(ctx: ?*anyopaque) ?ai_types.Model {
        const self: *Agent = @ptrCast(@alignCast(ctx.?));
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());
        const model = self._next_model;
        self._next_model = null;
        if (model) |next| self._state.model = next;
        return model;
    }

    pub fn setAutoCompact(self: *Agent, at: ?u64, host: ?CompactionHost) void {
        self._auto_compact_at = at;
        self._compaction_host = host;
    }

    pub fn requestCompaction(self: *Agent, focus: []const u8) !void {
        const owned = try self._allocator.dupe(u8, focus);
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());
        if (self._compaction_request) |previous| self._allocator.free(previous);
        self._compaction_request = owned;
    }

    pub fn takeCompactionRequest(self: *Agent) ?[]u8 {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());
        const focus = self._compaction_request;
        self._compaction_request = null;
        return focus;
    }

    fn compactBetweenTurns(ctx: ?*anyopaque, context: *AgentContext, event_stream: *AgentEventStream) anyerror!bool {
        const self: *Agent = @ptrCast(@alignCast(ctx.?));
        const history = context.messages.items;
        const requested = self.takeCompactionRequest();
        defer if (requested) |focus| self._allocator.free(focus);
        if (history.len == 0 or compaction.isCompacted(history)) return false;
        if (requested == null) {
            const at = self._auto_compact_at orelse return false;
            if (agent_loop.promptTokens(.{ .messages = history }) < at) return false;
        }

        if (!event_stream.pushBlocking(.compaction_start)) return error.StreamCompleted;
        const transcripts: CompactionTranscripts = if (self._compaction_host) |host| host.transcripts_fn(host.ctx, history) else .{};
        var summary = self.summarize(history, .{ .focus = requested orelse "", .transcripts = transcripts.paths }) catch |err| Summary{
            .result = compaction.failure(self._allocator, @errorName(err)),
        };
        if (summary.replacement) |*replacement| {
            self.retireHistory(&context.messages, replacement) catch |err| {
                summary.result.deinit(self._allocator);
                self.settleTranscripts(false);
                var failed: AgentEvent = .{ .compaction_end = .{ .outcome = .failed, .message = types.OwnedSlice(u8).initBorrowed(@errorName(err)) } };
                if (!event_stream.pushBlocking(failed)) {
                    failed.deinit(self._allocator);
                    return error.StreamCompleted;
                }
                return false;
            };
        }
        const completed = summary.replacement != null;
        const transcript: types.OwnedSlice(u8) = if (completed and transcripts.saved.len > 0)
            (if (self._allocator.dupe(u8, transcripts.saved)) |copy| types.OwnedSlice(u8).initOwned(copy) else |_| types.OwnedSlice(u8).initBorrowed(""))
        else
            types.OwnedSlice(u8).initBorrowed("");
        self.settleTranscripts(completed);
        var event: AgentEvent = .{ .compaction_end = switch (summary.result) {
            .completed => |done| .{
                .outcome = .completed,
                .text = types.OwnedSlice(u8).initOwned(done.text),
                .transcript = transcript,
                .messages_before = done.messages_before,
                .tokens_before = done.tokens_before,
                .tokens_after = done.tokens_after,
            },
            .cancelled => .{ .outcome = .cancelled },
            .failed => |message| .{ .outcome = .failed, .message = types.OwnedSlice(u8).initOwned(message) },
        } };
        if (!event_stream.pushBlocking(event)) {
            event.deinit(self._allocator);
            return error.StreamCompleted;
        }
        return completed;
    }

    fn adoptCompactedHistory(self: *Agent, text: []const u8, model: ai_types.Model) !void {
        var replacement = try compaction.historyMessages(self._allocator, text, .{ .api = model.api, .provider = model.provider, .model = model.id });
        try self.installHistory(&self._state.messages, &replacement);
    }

    pub fn setCompactToolOutput(self: *Agent, enabled: bool) void {
        self._compact_tool_output = enabled;
    }

    pub fn setPermissionEngine(self: *Agent, engine: ?*types.permission.PermissionEngine) void {
        self._permission_engine = engine;
    }

    pub fn setSteeringMode(self: *Agent, mode: QueueMode) void {
        self._steering_mode = mode;
    }

    pub fn getSteeringMode(self: Agent) QueueMode {
        return self._steering_mode;
    }

    pub fn setFollowUpMode(self: *Agent, mode: QueueMode) void {
        self._follow_up_mode = mode;
    }

    pub fn getFollowUpMode(self: Agent) QueueMode {
        return self._follow_up_mode;
    }

    pub fn replaceMessages(self: *Agent, messages: []const ai_types.Message) !void {
        for (self._state.messages.items) |*msg| {
            msg.deinit(self._allocator);
        }
        self._state.messages.clearRetainingCapacity();

        for (messages) |msg| {
            try self._state.messages.append(self._allocator, try ai_types.cloneMessage(self._allocator, msg));
        }
    }

    pub fn ensureCompactable(self: *Agent) !void {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());
        try self.ensureCompactableLocked();
    }

    fn ensureCompactableLocked(self: *Agent) !void {
        if (self._state.is_streaming or self._thread != null) return error.AgentAlreadyStreaming;
        if (self._state.model == null) return error.NoModelConfigured;
        const messages = self._state.messages.items;
        if (messages.len == 0 or compaction.isCompacted(messages)) return error.NothingToCompact;
    }

    pub fn compactAsync(self: *Agent, options: compaction.Options, ctx: ?*anyopaque, callback: compaction.Callback) !void {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());
        try self.ensureCompactableLocked();

        const job = try self._allocator.create(CompactJob);
        errdefer self._allocator.destroy(job);
        var owned = try compaction.OwnedOptions.init(self._allocator, options);
        errdefer owned.deinit(self._allocator);
        job.* = .{ .options = owned, .ctx = ctx, .callback = callback };

        self._pending_cancel.store(false, .release);
        self._cancel_token = .{ .cancelled = &self._pending_cancel };
        self._done_event.reset();
        self._state.is_streaming = true;
        errdefer {
            self._state.is_streaming = false;
            self._cancel_token = null;
        }
        self._thread = try std.Thread.spawn(.{}, compactThread, .{ self, job });
    }

    const CompactJob = struct {
        options: compaction.OwnedOptions,
        ctx: ?*anyopaque,
        callback: compaction.Callback,
    };

    fn compactThread(self: *Agent, job: *CompactJob) void {
        var result = self.summarizeHistory(job.options.view()) catch |err| blk: {
            if (self._pending_cancel.load(.acquire)) break :blk compaction.Result{ .cancelled = {} };
            break :blk compaction.failure(self._allocator, @errorName(err));
        };
        defer {
            result.deinit(self._allocator);
            job.options.deinit(self._allocator);
            self._allocator.destroy(job);
            self._mutex.lockUncancelable(defaultIo());
            self._state.is_streaming = false;
            self._cancel_token = null;
            self._mutex.unlock(defaultIo());
            self._pending_cancel.store(false, .release);
            self._done_event.set(defaultIo());
        }
        job.callback(job.ctx, &result);
    }

    fn compactionTools(self: *Agent) !?[]ai_types.Tool {
        const tools = self._state.tools;
        if (tools.len == 0) return null;
        const converted = try self._allocator.alloc(ai_types.Tool, tools.len);
        errdefer self._allocator.free(converted);
        for (tools, 0..) |tool, i| converted[i] = try tool.toTool(self._allocator);
        return converted;
    }

    const SummaryReply = union(enum) {
        text: []u8,
        cancelled,
        failed: []u8,
    };

    fn summarizeHistory(self: *Agent, options: compaction.Options) !compaction.Result {
        var summary = try self.summarize(self._state.messages.items, options);
        if (summary.replacement) |*replacement| {
            self.installHistory(&self._state.messages, replacement) catch |err| {
                summary.result.deinit(self._allocator);
                return err;
            };
        }
        return summary.result;
    }

    fn retireHistory(self: *Agent, messages: *std.ArrayList(ai_types.Message), replacement: *[2]ai_types.Message) !void {
        errdefer for (replacement) |*message| message.deinit(self._allocator);
        try self._retired_messages.ensureUnusedCapacity(self._allocator, messages.items.len);
        try messages.ensureTotalCapacity(self._allocator, replacement.len);
        self._retired_messages.appendSliceAssumeCapacity(messages.items);
        messages.clearRetainingCapacity();
        messages.appendSliceAssumeCapacity(replacement);
    }

    fn freeRetiredMessages(self: *Agent) void {
        for (self._retired_messages.items) |*message| message.deinit(self._allocator);
        self._retired_messages.clearRetainingCapacity();
    }

    fn installHistory(self: *Agent, messages: *std.ArrayList(ai_types.Message), replacement: *[2]ai_types.Message) !void {
        messages.ensureTotalCapacity(self._allocator, replacement.len) catch |err| {
            for (replacement) |*message| message.deinit(self._allocator);
            return err;
        };
        for (messages.items) |*message| message.deinit(self._allocator);
        messages.clearRetainingCapacity();
        messages.appendSliceAssumeCapacity(replacement);
    }

    const Summary = struct {
        result: compaction.Result,
        replacement: ?[2]ai_types.Message = null,
    };

    fn summarize(self: *Agent, history: []const ai_types.Message, options: compaction.Options) !Summary {
        const allocator = self._allocator;
        const model = self._state.model orelse return error.NoModelConfigured;

        const tools = try self.compactionTools();
        defer if (tools) |converted| allocator.free(converted);

        const request_text = try compaction.requestText(allocator, options.focus);
        defer allocator.free(request_text);
        const request = ai_types.Message{ .user = .{ .content = .{ .text = request_text }, .timestamp = compat.time.nowMillis() } };

        const system_prompt = ai_types.OwnedSlice(u8).initBorrowed(self._state.system_prompt);
        const max_output = compaction.maxOutputTokens(model);
        const fixed_tokens = agent_loop.estimatePromptTokens(.{ .system_prompt = system_prompt, .messages = &.{request}, .tools = tools });
        var budget = compaction.historyBudget(model.context_window, fixed_tokens, max_output);

        var start: usize = 0;
        var reply_text: []u8 = &.{};
        var attempts: usize = 0;
        while (true) {
            start = compaction.firstIncluded(history, budget) orelse
                return .{ .result = compaction.failure(allocator, "the latest turn alone does not fit in the model's context window") };
            switch (try self.requestSummary(model, history[start..], request, tools, max_output)) {
                .text => |text| {
                    reply_text = text;
                    break;
                },
                .cancelled => return .{ .result = .cancelled },
                .failed => |message| {
                    attempts += 1;
                    if (attempts >= compaction.max_attempts or !compaction.isContextOverflow(message)) return .{ .result = .{ .failed = message } };
                    allocator.free(message);
                    budget = compaction.shrunkBudget(history[start..]);
                },
            }
        }
        defer allocator.free(reply_text);

        const summary = compaction.extractSummary(reply_text);
        if (summary.len == 0) return .{ .result = compaction.failure(allocator, "the model returned an empty summary") };

        const text = try compaction.installedText(allocator, summary, options.transcripts, start > 0);
        errdefer allocator.free(text);
        var replacement = try compaction.historyMessages(allocator, text, .{ .api = model.api, .provider = model.provider, .model = model.id });
        errdefer for (&replacement) |*message| message.deinit(allocator);

        const messages_before = history.len;
        const tokens_before = agent_loop.estimatePromptTokens(.{ .system_prompt = system_prompt, .messages = history, .tools = tools });
        const tokens_after = agent_loop.estimatePromptTokens(.{ .system_prompt = system_prompt, .messages = &replacement, .tools = tools });

        return .{
            .result = .{ .completed = .{
                .text = text,
                .messages_before = messages_before,
                .tokens_before = tokens_before,
                .tokens_after = tokens_after,
                .head_truncated = start > 0,
            } },
            .replacement = replacement,
        };
    }

    fn requestSummary(self: *Agent, model: ai_types.Model, included: []const ai_types.Message, request: ai_types.Message, tools: ?[]ai_types.Tool, max_output: u32) !SummaryReply {
        const allocator = self._allocator;
        const request_messages = try allocator.alloc(ai_types.Message, included.len + 1);
        defer allocator.free(request_messages);
        @memcpy(request_messages[0..included.len], included);
        request_messages[included.len] = request;

        var messages: []const ai_types.Message = request_messages;
        var transformed: ?[]const ai_types.Message = null;
        defer if (transformed) |slice| allocator.free(slice);
        if (self._transform_context_fn) |transform| {
            transformed = try transform(self._transform_context_ctx, messages, allocator);
            messages = transformed.?;
        }
        var converted: ?[]const ai_types.Message = null;
        defer if (converted) |slice| allocator.free(slice);
        if (self._convert_to_llm_fn) |convert| {
            converted = try convert(self._convert_to_llm_ctx, messages, allocator);
            messages = converted.?;
        }

        try self.ensureSessionId();
        const stream = try self._protocol.stream(model, .{
            .system_prompt = ai_types.OwnedSlice(u8).initBorrowed(self._state.system_prompt),
            .messages = messages,
            .tools = tools,
        }, .{
            .session_id = self._session_id,
            .cancel_token = self._cancel_token,
            .thinking_level = self._state.thinking_level,
            .thinking_budgets = self._thinking_budgets,
            .max_retry_delay_ms = self._max_retry_delay_ms orelse 60_000,
            .max_tokens = max_output,
        }, allocator);
        defer _ = stream.deinitAndDestroy();

        var reply: ?ai_types.AssistantMessage = null;
        defer if (reply) |*message| message.deinit(allocator);
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

        if (self._pending_cancel.load(.acquire)) return .cancelled;
        if (reply == null) reply = try stream.cloneResult(allocator);
        const final = reply orelse return .{ .failed = try allocator.dupe(u8, stream.getError() orelse "the model returned no summary") };
        return switch (final.stop_reason) {
            .aborted => .cancelled,
            .@"error" => .{ .failed = try allocator.dupe(u8, final.getErrorMessage() orelse "the summary request failed") },
            .length => .{ .failed = try allocator.dupe(u8, "the summary hit the output limit; try /compact with a narrower focus") },
            .content_filter => .{ .failed = try allocator.dupe(u8, "the provider filtered the summary") },
            .stop, .tool_use => .{ .text = try compaction.replyText(allocator, final.content) },
        };
    }

    pub fn appendMessage(self: *Agent, message: ai_types.Message) !void {
        try self._state.messages.append(self._allocator, message);
    }

    pub fn clearMessages(self: *Agent) void {
        for (self._state.messages.items) |*msg| {
            msg.deinit(self._allocator);
        }
        self._state.messages.clearRetainingCapacity();
    }

    pub fn steer(self: *Agent, message: ai_types.Message) !void {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());
        try self._steering_queue.append(self._allocator, message);
    }

    pub fn followUp(self: *Agent, message: ai_types.Message) !void {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());
        try self._follow_up_queue.append(self._allocator, message);
    }

    pub fn clearSteeringQueue(self: *Agent) void {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());
        for (self._steering_queue.items) |*msg| {
            msg.deinit(self._allocator);
        }
        self._steering_queue.clearRetainingCapacity();
    }

    pub fn clearFollowUpQueue(self: *Agent) void {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());
        for (self._follow_up_queue.items) |*msg| {
            msg.deinit(self._allocator);
        }
        self._follow_up_queue.clearRetainingCapacity();
    }

    pub fn clearAllQueues(self: *Agent) void {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());
        for (self._steering_queue.items) |*msg| {
            msg.deinit(self._allocator);
        }
        self._steering_queue.clearRetainingCapacity();
        for (self._follow_up_queue.items) |*msg| {
            msg.deinit(self._allocator);
        }
        self._follow_up_queue.clearRetainingCapacity();
    }

    pub fn prompt(self: *Agent, message_or_messages: anytype) !void {
        if (self._state.is_streaming) {
            return error.AgentAlreadyStreaming;
        }

        const T = @TypeOf(message_or_messages);
        const messages: []const ai_types.Message = switch (T) {
            []const u8, *const []const u8 => blk: {
                const text = if (T == *const []const u8) message_or_messages.* else message_or_messages;
                const msg = ai_types.Message{
                    .user = .{
                        .content = .{ .text = text },
                        .timestamp = compat.time.nowMillis(),
                    },
                };
                break :blk @as([]const ai_types.Message, &.{msg});
            },
            []const ai_types.Message => message_or_messages,
            ai_types.Message => blk: {
                break :blk @as([]const ai_types.Message, &.{message_or_messages});
            },
            else => @compileError("prompt expects a string, Message, or []const Message"),
        };

        try self.runLoop(messages);
    }

    pub fn promptWithImages(
        self: *Agent,
        text: []const u8,
        images: ?[]const ai_types.ImageContent,
    ) !void {
        if (self._state.is_streaming) {
            return error.AgentAlreadyStreaming;
        }

        var content_parts: std.ArrayList(ai_types.UserContentPart) = .empty;
        defer content_parts.deinit(self._allocator);

        try content_parts.append(self._allocator, .{
            .text = .{ .text = text },
        });

        if (images) |imgs| {
            for (imgs) |img| {
                try content_parts.append(self._allocator, .{
                    .image = img,
                });
            }
        }

        const msg = ai_types.Message{
            .user = .{
                .content = .{ .parts = content_parts.items },
                .timestamp = compat.time.nowMillis(),
            },
        };

        try self.runLoop(&.{msg});
    }

    pub fn continueFromContext(self: *Agent) !void {
        if (self._state.is_streaming) {
            return error.AgentAlreadyStreaming;
        }

        const messages = self._state.messages.items;
        if (messages.len == 0) {
            return error.NoMessagesToContinue;
        }

        if (messages[messages.len - 1] == .assistant) {
            if (self._steering_queue.items.len > 0) {
                const steering = try self.dequeueSteeringMessages();
                defer if (steering) |s| self._allocator.free(s);

                var run_messages: ?[]const ai_types.Message = if (steering) |s| s else null;
                try self.runLoopInternal(
                    &run_messages,
                    .{ .skip_initial_steering_poll = true, .prompts_are_steering = true },
                );
                return;
            }

            if (self._follow_up_queue.items.len > 0) {
                const follow_up = try self.dequeueFollowUpMessages();
                defer if (follow_up) |f| self._allocator.free(f);

                var run_messages: ?[]const ai_types.Message = if (follow_up) |f| f else null;
                try self.runLoopInternal(
                    &run_messages,
                    .{},
                );
                return;
            }

            return error.CannotContinueFromAssistant;
        }

        var run_messages: ?[]const ai_types.Message = null;
        try self.runLoopInternal(&run_messages, .{});
    }

    pub fn abort(self: *Agent) void {
        self._pending_cancel.store(true, .release);
        if (self._cancel_token) |token| {
            token.cancelled.store(true, .release);
        }
    }

    pub fn isIdle(self: *Agent) bool {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());
        return !self._state.is_streaming;
    }

    fn prepareContinueFromContextLocked(self: *Agent) !ContinueRequest {
        const messages = self._state.messages.items;
        if (messages.len == 0) {
            return error.NoMessagesToContinue;
        }

        if (messages[messages.len - 1] == .assistant) {
            if (self._steering_queue.items.len > 0) {
                const steering = try self.dequeueSteeringMessagesLocked();
                _ = self._steering_consumed.fetchAdd(steering.len, .release);
                return .{ .messages = steering, .skip_steering = true, .steering = true };
            }

            if (self._follow_up_queue.items.len > 0) {
                const follow_up = try self.dequeueFollowUpMessagesLocked();
                return .{ .messages = follow_up, .skip_steering = false };
            }

            return error.CannotContinueFromAssistant;
        }

        return .{ .messages = try self._allocator.alloc(ai_types.Message, 0), .skip_steering = false };
    }

    pub fn continueFromContextAsync(self: *Agent) !void {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());

        if (self._state.is_streaming or self._thread != null) {
            return error.AgentAlreadyStreaming;
        }
        if (self._state.model == null) {
            return error.NoModelConfigured;
        }

        const request = try self.prepareContinueFromContextLocked();
        errdefer {
            for (request.messages) |*msg| msg.deinit(self._allocator);
            self._allocator.free(request.messages);
        }

        self._pending_cancel.store(false, .release);
        self._cancel_token = .{ .cancelled = &self._pending_cancel };
        self._done_event.reset();
        self._state.is_streaming = true;
        errdefer {
            self._state.is_streaming = false;
            self._cancel_token = null;
        }
        self._thread = try std.Thread.spawn(.{}, runLoopThread, .{ self, request.messages, request.skip_steering, request.steering });
    }

    pub fn waitForIdle(self: *Agent) void {
        self._mutex.lockUncancelable(defaultIo());
        const should_wait = self._state.is_streaming or self._thread != null;
        self._mutex.unlock(defaultIo());

        if (!should_wait) {
            return;
        }

        self._done_event.waitUncancelable(defaultIo());

        self._mutex.lockUncancelable(defaultIo());
        const thread = self._thread;
        self._thread = null;
        self._mutex.unlock(defaultIo());

        if (thread) |t| {
            t.join();
        }
    }

    pub fn promptAsync(self: *Agent, message_or_messages: anytype) !void {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());

        if (self._state.is_streaming or self._thread != null) {
            return error.AgentAlreadyStreaming;
        }

        const messages: []const ai_types.Message = switch (@TypeOf(message_or_messages)) {
            []const ai_types.Message => message_or_messages,
            ai_types.Message => blk: {
                break :blk @as([]const ai_types.Message, &.{message_or_messages});
            },
            else => @compileError("promptAsync expects a Message or []const Message"),
        };

        const owned_messages = try self.copyMessagesForThread(messages);

        self._pending_cancel.store(false, .release);
        self._cancel_token = .{ .cancelled = &self._pending_cancel };
        self._done_event.reset();
        self._state.is_streaming = true;
        errdefer {
            self._state.is_streaming = false;
            self._cancel_token = null;
        }

        self._thread = try std.Thread.spawn(.{}, runLoopThread, .{ self, owned_messages, true, false });
    }

    fn copyMessagesForThread(self: *Agent, messages: []const ai_types.Message) ![]ai_types.Message {
        const owned = try self._allocator.alloc(ai_types.Message, messages.len);
        var initialized: usize = 0;
        errdefer {
            for (owned[0..initialized]) |*msg| msg.deinit(self._allocator);
            self._allocator.free(owned);
        }
        for (messages, 0..) |msg, i| {
            owned[i] = try self.cloneMessage(msg);
            initialized += 1;
        }
        return owned;
    }

    fn cloneMessage(self: *Agent, msg: ai_types.Message) !ai_types.Message {
        return switch (msg) {
            .user => |u| .{ .user = .{
                .content = try self.cloneUserContent(u.content),
                .timestamp = u.timestamp,
            } },
            .assistant => |a| .{ .assistant = try self.cloneAssistantMessage(a) },
            .tool_result => |t| .{ .tool_result = try self.cloneToolResultMessage(t) },
        };
    }

    fn setStreamMessage(self: *Agent, msg: ai_types.Message) !void {
        var cloned = try self.cloneMessage(msg);
        errdefer cloned.deinit(self._allocator);

        self._state.clearStreamMessage();
        self._state.stream_message = cloned;
    }

    fn cloneUserContent(self: *Agent, content: ai_types.UserContent) !ai_types.UserContent {
        return switch (content) {
            .text => |t| .{ .text = try self._allocator.dupe(u8, t) },
            .parts => |parts| blk: {
                var cloned_parts = try self._allocator.alloc(ai_types.UserContentPart, parts.len);
                var initialized: usize = 0;
                errdefer {
                    for (cloned_parts[0..initialized]) |*part| part.deinit(self._allocator);
                    self._allocator.free(cloned_parts);
                }
                for (parts, 0..) |p, i| {
                    cloned_parts[i] = try self.cloneUserContentPart(p);
                    initialized += 1;
                }
                break :blk .{ .parts = cloned_parts };
            },
        };
    }

    fn cloneUserContentPart(self: *Agent, part: ai_types.UserContentPart) !ai_types.UserContentPart {
        return switch (part) {
            .text => |t| .{ .text = .{ .text = try self._allocator.dupe(u8, t.text) } },
            .image => |i| .{ .image = .{
                .data = try self._allocator.dupe(u8, i.data),
                .mime_type = try self._allocator.dupe(u8, i.mime_type),
            } },
        };
    }

    fn cloneAssistantMessage(self: *Agent, msg: ai_types.AssistantMessage) !ai_types.AssistantMessage {
        return ai_types.cloneAssistantMessage(self._allocator, msg);
    }

    fn cloneToolResultMessage(self: *Agent, msg: ai_types.ToolResultMessage) !ai_types.ToolResultMessage {
        var content = try self._allocator.alloc(ai_types.UserContentPart, msg.content.len);
        var initialized: usize = 0;
        errdefer {
            for (content[0..initialized]) |*part| part.deinit(self._allocator);
            self._allocator.free(content);
        }
        for (msg.content, 0..) |c, i| {
            content[i] = .{ .text = .{ .text = try self._allocator.dupe(u8, c.text.text) } };
            initialized += 1;
        }

        const details_json = if (msg.getDetailsJson()) |d|
            ai_types.OwnedSlice(u8).initOwned(try self._allocator.dupe(u8, d))
        else
            ai_types.OwnedSlice(u8).initBorrowed("");
        errdefer {
            var mutable = details_json;
            mutable.deinit(self._allocator);
        }

        const artifacts = try self.cloneArtifactReferences(msg.artifacts.slice());
        errdefer {
            var mutable = ai_types.OwnedSlice(ai_types.ArtifactReference).initOwned(artifacts);
            mutable.deinit(self._allocator);
        }

        const tool_call_id = try self._allocator.dupe(u8, msg.tool_call_id);
        errdefer self._allocator.free(tool_call_id);
        const tool_name = try self._allocator.dupe(u8, msg.tool_name);
        errdefer self._allocator.free(tool_name);
        const directory = try self._allocator.dupe(u8, msg.working_directory.slice());
        errdefer self._allocator.free(directory);

        return .{
            .tool_call_id = tool_call_id,
            .tool_name = tool_name,
            .content = content,
            .details_json = details_json,
            .artifacts = ai_types.OwnedSlice(ai_types.ArtifactReference).initOwned(artifacts),
            .working_directory = ai_types.OwnedSlice(u8).initOwned(directory),
            .working_directory_observed = msg.working_directory_observed,
            .is_error = msg.is_error,
            .timestamp = msg.timestamp,
        };
    }

    fn cloneArtifactReferences(self: *Agent, artifacts: []const ai_types.ArtifactReference) ![]ai_types.ArtifactReference {
        const cloned = try self._allocator.alloc(ai_types.ArtifactReference, artifacts.len);
        var initialized: usize = 0;
        errdefer {
            for (cloned[0..initialized]) |*artifact| artifact.deinit(self._allocator);
            self._allocator.free(cloned);
        }

        for (artifacts, 0..) |artifact, i| {
            cloned[i] = try self.cloneArtifactReference(artifact);
            initialized += 1;
        }

        return cloned;
    }

    fn cloneArtifactReference(self: *Agent, artifact: ai_types.ArtifactReference) !ai_types.ArtifactReference {
        const artifact_id = try self._allocator.dupe(u8, artifact.artifact_id);
        errdefer self._allocator.free(artifact_id);

        const uri = if (artifact.getUri()) |value|
            ai_types.OwnedSlice(u8).initOwned(try self._allocator.dupe(u8, value))
        else
            ai_types.OwnedSlice(u8).initBorrowed("");
        errdefer {
            var mutable = uri;
            mutable.deinit(self._allocator);
        }

        const mime_type = if (artifact.getMimeType()) |value|
            ai_types.OwnedSlice(u8).initOwned(try self._allocator.dupe(u8, value))
        else
            ai_types.OwnedSlice(u8).initBorrowed("");
        errdefer {
            var mutable = mime_type;
            mutable.deinit(self._allocator);
        }

        const sha256 = if (artifact.getSha256()) |value|
            ai_types.OwnedSlice(u8).initOwned(try self._allocator.dupe(u8, value))
        else
            ai_types.OwnedSlice(u8).initBorrowed("");
        errdefer {
            var mutable = sha256;
            mutable.deinit(self._allocator);
        }

        const description = if (artifact.getDescription()) |value|
            ai_types.OwnedSlice(u8).initOwned(try self._allocator.dupe(u8, value))
        else
            ai_types.OwnedSlice(u8).initBorrowed("");

        return .{
            .artifact_id = artifact_id,
            .uri = uri,
            .mime_type = mime_type,
            .byte_size = artifact.byte_size,
            .sha256 = sha256,
            .description = description,
        };
    }

    fn runLoopThread(self: *Agent, messages: []ai_types.Message, skip_steering: bool, steering: bool) void {
        var messages_owned = true;
        var failed_because: ?[]const u8 = null;
        self._state.terminal_sent = false;
        defer {
            if (messages_owned) {
                for (messages) |*m| {
                    m.deinit(self._allocator);
                }
            }
            self._allocator.free(messages);

            if (!self._state.terminal_sent) {
                self._state.terminal_sent = true;
                const reason = failed_because orelse "RunEndedWithoutReason";
                self.emit(.{ .run_failed = .{ .reason = types.OwnedSlice(u8).initBorrowed(reason) } });
            }

            self._mutex.lockUncancelable(defaultIo());
            self._state.is_streaming = false;
            self._mutex.unlock(defaultIo());

            self._done_event.set(defaultIo());
        }

        if (messages.len > 0 and self._state.model == null) {
            failed_because = @errorName(error.NoModelConfigured);
            return;
        }

        var run_messages: ?[]const ai_types.Message = if (messages.len > 0) messages else null;
        if (messages.len == 0 and self._state.messages.items.len == 0) {
            failed_because = "NothingToRun";
            return;
        }

        self.runLoopInternal(
            &run_messages,
            .{ .skip_initial_steering_poll = skip_steering, .prompts_are_steering = steering },
        ) catch |err| {
            failed_because = @errorName(err);
        };
        messages_owned = run_messages != null;
    }

    pub fn reset(self: *Agent) void {
        self.clearMessages();
        self.clearAllQueues();
        self._state.is_streaming = false;
        self._state.clearStreamMessage();
        self._state.pending_tool_calls.clearRetainingCapacity();
        self._state.error_message.deinit(self._allocator);
        self._state.error_message = types.OwnedSlice(u8).initBorrowed("");
    }

    const RunLoopOptions = struct {
        skip_initial_steering_poll: bool = false,
        prompts_are_steering: bool = false,
    };

    fn runLoop(self: *Agent, messages: []const ai_types.Message) !void {
        var run_messages: ?[]const ai_types.Message = messages;
        try self.runLoopInternal(&run_messages, .{ .skip_initial_steering_poll = true });
    }

    fn runLoopInternal(
        self: *Agent,
        messages: *?[]const ai_types.Message,
        options: RunLoopOptions,
    ) !void {
        const model = self._state.model orelse return error.NoModelConfigured;

        if (self._cancel_token == null) {
            self._cancel_token = .{ .cancelled = &self._pending_cancel };
        }
        self._state.is_streaming = true;
        self._state.terminal_sent = false;
        errdefer {
            self._state.is_streaming = false;
            self._cancel_token = null;
            self._state.clearStreamMessage();
        }
        self._state.clearStreamMessage();
        self._state.error_message.deinit(self._allocator);
        self._state.error_message = types.OwnedSlice(u8).initBorrowed("");

        self._skip_initial_steering_poll = options.skip_initial_steering_poll;

        var context = AgentContext.init(self._allocator);
        defer context.deinit();

        context.system_prompt = types.OwnedSlice(u8).initBorrowed(self._state.system_prompt);
        context.tools = self._state.tools;

        for (self._state.messages.items) |msg| {
            var cloned = try self.cloneMessage(msg);
            errdefer cloned.deinit(self._allocator);
            try context.appendMessage(cloned);
        }

        if (self._execute_tool_via_protocol_fn == null) {
            if (self._local_tool_protocol == null) {
                const local = try self._allocator.create(tool_local_runtime.LocalToolProtocol);
                errdefer self._allocator.destroy(local);
                local.* = try tool_local_runtime.LocalToolProtocol.init(self._allocator, self._state.tools);
                self._local_tool_protocol = local;
            } else {
                self._local_tool_protocol.?.server.tools.clearRetainingCapacity();
                try self._local_tool_protocol.?.server.registerTools(self._state.tools);
            }
        }

        try self.ensureSessionId();
        const config = agent_loop.AgentLoopConfig{
            .model = model,
            .protocol = self._protocol,
            .tools = self._state.tools,
            .execute_tool_via_protocol_fn = self._execute_tool_via_protocol_fn orelse tool_local_runtime.LocalToolProtocol.executeFn,
            .execute_tool_via_protocol_ctx = self._execute_tool_via_protocol_ctx orelse self._local_tool_protocol,
            .rewrite_tool_args_fn = self._rewrite_tool_args_fn,
            .rewrite_tool_args_ctx = self._rewrite_tool_args_ctx,
            .temperature = null,
            .max_tokens = agent_loop.outputRequest(model, self._output),
            .raise_max_tokens_on_cut_off = self._output == .auto,
            .api_key = null,
            .cancel_token = self._cancel_token,
            .thinking_level = self._state.thinking_level,
            .max_iterations = null,
            .session_id = self._session_id,
            .thinking_budgets = self._thinking_budgets,
            .max_retry_delay_ms = self._max_retry_delay_ms,
            .compact_tool_output = self._compact_tool_output,
            .permission_engine = self._permission_engine,
            .transform_context_fn = self._transform_context_fn,
            .transform_context_ctx = self._transform_context_ctx,
            .get_steering_messages_fn = getSteeringMessages,
            .get_steering_messages_ctx = self,
            .prompts_are_steering = options.prompts_are_steering,
            .get_follow_up_messages_fn = getFollowUpMessages,
            .get_follow_up_messages_ctx = self,
            .compact_between_turns_fn = compactBetweenTurns,
            .compact_between_turns_ctx = self,
            .next_model_fn = nextModel,
            .next_model_ctx = self,
            .convert_to_llm_fn = self._convert_to_llm_fn,
            .convert_to_llm_ctx = self._convert_to_llm_ctx,
            .get_api_key_fn = self._get_api_key_fn,
            .get_api_key_ctx = self._get_api_key_ctx,
        };

        const initial_message_count = context.messages.items.len;
        var compacted_in_run = false;

        const stream = if (messages.*) |msgs|
            try agent_loop.agentLoop(self._allocator, msgs, &context, config)
        else
            try agent_loop.agentLoopContinue(self._allocator, &context, config);

        defer {
            if (stream.deinitAndDestroy()) self.freeRetiredMessages();
        }

        while (stream.wait()) |event| {
            var owned_event = event;
            defer owned_event.deinit(self._allocator);

            switch (owned_event) {
                .message_start => |e| {
                    try self.setStreamMessage(e.message);
                },
                .message_update => |e| {
                    try self.setStreamMessage(.{ .assistant = e.message });
                },
                .message_end => |e| {
                    var cloned_message = try self.cloneMessage(e.message);
                    errdefer cloned_message.deinit(self._allocator);
                    try self._state.messages.append(self._allocator, cloned_message);
                    self._state.clearStreamMessage();
                },
                .tool_execution_start => |e| {
                    try self._state.pending_tool_calls.put(e.tool_call_id, {});
                },
                .tool_execution_end => |e| {
                    _ = self._state.pending_tool_calls.remove(e.tool_call_id);
                },
                .compaction_end => |e| {
                    if (e.outcome == .completed) {
                        const current = current: {
                            self._mutex.lockUncancelable(defaultIo());
                            defer self._mutex.unlock(defaultIo());
                            break :current self._state.model orelse model;
                        };
                        try self.adoptCompactedHistory(e.text.slice(), current);
                        compacted_in_run = true;
                    }
                },
                .turn_end => |e| {
                    if (e.message.getErrorMessage()) |err| {
                        self._state.error_message.deinit(self._allocator);
                        self._state.error_message = types.OwnedSlice(u8).initOwned(try self._allocator.dupe(u8, err));
                    }
                },
                .agent_end => {
                    self._state.is_streaming = false;
                    self._state.terminal_sent = true;
                    self._state.clearStreamMessage();
                },
                else => {},
            }

            self.emit(owned_event);
        }

        if (compacted_in_run or context.messages.items.len > initial_message_count) {
            messages.* = null;
        }

        if (stream.getError() != null) {
            self._state.is_streaming = false;
            self._cancel_token = null;
            self._pending_cancel.store(false, .release);
            return error.AgentLoopFailed;
        }
        if (stream.getResult()) |result| {
            if (result.final_message.stop_reason == .@"error") {
                self._state.is_streaming = false;
                self._cancel_token = null;
                self._pending_cancel.store(false, .release);
                return error.AgentLoopFailed;
            }
        } else if (stream.isDone()) {
            self._state.is_streaming = false;
            self._cancel_token = null;
            self._pending_cancel.store(false, .release);
            return error.AgentLoopFailed;
        }

        self._state.is_streaming = false;
        self._cancel_token = null;
        self._pending_cancel.store(false, .release);
    }

    fn emit(self: *Agent, event: AgentEvent) void {
        for (self._listeners.items) |listener| {
            listener.callback(listener.ctx, event);
        }
    }

    fn dequeueSteeringMessagesLocked(self: *Agent) ![]ai_types.Message {
        if (self._steering_mode == .one_at_a_time) {
            const first = self._steering_queue.orderedRemove(0);
            const result = try self._allocator.alloc(ai_types.Message, 1);
            result[0] = first;
            return result;
        }

        const count = self._steering_queue.items.len;
        const result = try self._allocator.alloc(ai_types.Message, count);
        for (self._steering_queue.items, 0..) |msg, i| {
            result[i] = msg;
        }
        self._steering_queue.clearRetainingCapacity();
        return result;
    }

    fn dequeueSteeringMessages(self: *Agent) !?[]ai_types.Message {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());

        if (self._steering_queue.items.len == 0) return null;
        const dequeued = try self.dequeueSteeringMessagesLocked();
        _ = self._steering_consumed.fetchAdd(dequeued.len, .release);
        return dequeued;
    }

    pub fn steeringConsumedCount(self: *Agent) u64 {
        return self._steering_consumed.load(.acquire);
    }

    fn dequeueFollowUpMessagesLocked(self: *Agent) ![]ai_types.Message {
        if (self._follow_up_mode == .one_at_a_time) {
            const first = self._follow_up_queue.orderedRemove(0);
            const result = try self._allocator.alloc(ai_types.Message, 1);
            result[0] = first;
            return result;
        }

        const count = self._follow_up_queue.items.len;
        const result = try self._allocator.alloc(ai_types.Message, count);
        for (self._follow_up_queue.items, 0..) |msg, i| {
            result[i] = msg;
        }
        self._follow_up_queue.clearRetainingCapacity();
        return result;
    }

    fn dequeueFollowUpMessages(self: *Agent) !?[]ai_types.Message {
        self._mutex.lockUncancelable(defaultIo());
        defer self._mutex.unlock(defaultIo());

        if (self._follow_up_queue.items.len == 0) return null;
        return try self.dequeueFollowUpMessagesLocked();
    }

    fn getSteeringMessages(ctx: ?*anyopaque, allocator: std.mem.Allocator) anyerror!?[]ai_types.Message {
        _ = allocator;
        const self: *Agent = @ptrCast(@alignCast(ctx));

        if (self._skip_initial_steering_poll) {
            self._skip_initial_steering_poll = false;
            return null;
        }

        return self.dequeueSteeringMessages();
    }

    fn getFollowUpMessages(ctx: ?*anyopaque, allocator: std.mem.Allocator) anyerror!?[]ai_types.Message {
        _ = allocator;
        const self: *Agent = @ptrCast(@alignCast(ctx));
        return self.dequeueFollowUpMessages();
    }
};

fn mockStreamFn(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream_mod.AssistantMessageEventStream {
    _ = ctx;
    _ = model;
    _ = context;
    _ = options;
    _ = allocator;
    return error.NotImplemented;
}

fn createMockProtocol() types.ProtocolClient {
    return .{
        .stream_fn = mockStreamFn,
        .ctx = null,
    };
}

test "Agent init and deinit" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createMockProtocol() });
    defer agent.deinit();

    try std.testing.expect(!agent.isStreaming());
    try std.testing.expect(!agent.hasQueuedMessages());
}

test "Agent setSystemPrompt" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createMockProtocol() });
    defer agent.deinit();

    try agent.setSystemPrompt("You are helpful.");
    try std.testing.expectEqualStrings("You are helpful.", agent._state.system_prompt);

    try agent.setSystemPrompt("New prompt");
    try std.testing.expectEqualStrings("New prompt", agent._state.system_prompt);
}

test "Agent message queues" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createMockProtocol() });
    defer agent.deinit();

    const text1 = try std.testing.allocator.dupe(u8, "test1");
    const msg1 = ai_types.Message{
        .user = .{
            .content = .{ .text = text1 },
            .timestamp = 0,
        },
    };

    const text2 = try std.testing.allocator.dupe(u8, "test2");
    const msg2 = ai_types.Message{
        .user = .{
            .content = .{ .text = text2 },
            .timestamp = 0,
        },
    };

    try agent.steer(msg1);
    try std.testing.expect(agent.hasQueuedMessages());

    try agent.followUp(msg2);
    try std.testing.expect(agent.hasQueuedMessages());

    agent.clearAllQueues();
    try std.testing.expect(!agent.hasQueuedMessages());
}

test "Agent queue modes" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createMockProtocol() });
    defer agent.deinit();

    agent.setSteeringMode(.all);
    try std.testing.expectEqual(QueueMode.all, agent.getSteeringMode());

    agent.setFollowUpMode(.one_at_a_time);
    try std.testing.expectEqual(QueueMode.one_at_a_time, agent.getFollowUpMode());
}

test "Agent reset" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createMockProtocol() });
    defer agent.deinit();

    const text1 = try std.testing.allocator.dupe(u8, "test1");
    const msg1 = ai_types.Message{
        .user = .{
            .content = .{ .text = text1 },
            .timestamp = 0,
        },
    };

    const text2 = try std.testing.allocator.dupe(u8, "test2");
    const msg2 = ai_types.Message{
        .user = .{
            .content = .{ .text = text2 },
            .timestamp = 0,
        },
    };

    try agent.appendMessage(msg1);
    try agent.steer(msg2);

    agent.reset();

    try std.testing.expectEqual(@as(usize, 0), agent._state.messages.items.len);
    try std.testing.expect(!agent.hasQueuedMessages());
}

test "Agent subscribe and unsubscribe" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createMockProtocol() });
    defer agent.deinit();

    const callback = struct {
        fn onEvent(event: AgentEvent) void {
            _ = event;
        }
    }.onEvent;

    agent.subscribe(callback);
    try std.testing.expectEqual(@as(usize, 1), agent._listeners.items.len);

    agent.unsubscribe(callback);
    try std.testing.expectEqual(@as(usize, 0), agent._listeners.items.len);
}

test "Agent isIdle and waitForIdle" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createMockProtocol() });
    defer agent.deinit();

    try std.testing.expect(agent.isIdle());

    agent.waitForIdle();

    try std.testing.expect(agent.isIdle());
}

fn delayedErrorStreamFn(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream_mod.AssistantMessageEventStream {
    _ = ctx;
    _ = model;
    _ = context;
    _ = options;

    defaultIo().sleep(.fromNanoseconds(10 * std.time.ns_per_ms), .boot) catch {};

    const stream = try allocator.create(event_stream_mod.AssistantMessageEventStream);
    stream.* = event_stream_mod.AssistantMessageEventStream.init(allocator);
    const message = ai_types.AssistantMessage{
        .content = &.{},
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .@"error",
        .error_message = ai_types.OwnedSlice(u8).initBorrowed("test done"),
        .timestamp = 0,
    };
    stream.push(.{ .done = .{ .reason = .@"error", .message = message } }) catch {};
    stream.complete(message);
    return stream;
}

fn createDelayedErrorProtocol() types.ProtocolClient {
    return .{
        .stream_fn = delayedErrorStreamFn,
        .ctx = null,
    };
}

const CaptureOptionsCtx = struct {
    max_tokens: ?u32 = null,
    session_id: [32]u8 = undefined,
    session_id_len: usize = 0,
};

fn captureOptionsStreamFn(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream_mod.AssistantMessageEventStream {
    _ = context;
    const capture: *CaptureOptionsCtx = @ptrCast(@alignCast(ctx.?));
    capture.max_tokens = options.max_tokens;
    if (options.session_id) |sid| {
        capture.session_id_len = @min(sid.len, capture.session_id.len);
        @memcpy(capture.session_id[0..capture.session_id_len], sid[0..capture.session_id_len]);
    }

    const stream = try allocator.create(event_stream_mod.AssistantMessageEventStream);
    stream.* = event_stream_mod.AssistantMessageEventStream.init(allocator);
    const message = ai_types.AssistantMessage{
        .content = &.{},
        .api = model.api,
        .provider = model.provider,
        .model = model.id,
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 0,
    };
    stream.push(.{ .done = .{ .reason = .stop, .message = message } }) catch {};
    stream.complete(message);
    return stream;
}

const test_model = ai_types.Model{
    .id = "test-model",
    .name = "Test Model",
    .api = "test-api",
    .provider = "test-provider",
    .base_url = "https://example.invalid",
    .reasoning = false,
    .input = &.{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 8192,
    .max_tokens = 1024,
};

test "Agent async completion signals waitForIdle" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createDelayedErrorProtocol() });
    defer agent.deinit();
    agent.setModel(test_model);

    try agent.promptAsync(@as([]const ai_types.Message, &.{}));

    agent.waitForIdle();

    try std.testing.expect(agent.isIdle());
    try std.testing.expect(agent._thread == null);
}

test "Agent without a session id makes one and keeps it across runs" {
    var capture = CaptureOptionsCtx{};
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = captureOptionsStreamFn, .ctx = &capture } });
    defer agent.deinit();
    agent.setModel(test_model);

    const first_text = try std.testing.allocator.dupe(u8, "hello");
    try agent.prompt(@as([]const ai_types.Message, &.{.{ .user = .{ .content = .{ .text = first_text }, .timestamp = 0 } }}));
    var first: [32]u8 = undefined;
    const first_len = capture.session_id_len;
    @memcpy(first[0..first_len], capture.session_id[0..first_len]);
    try std.testing.expect(std.mem.startsWith(u8, first[0..first_len], "oapx-"));

    try agent.replaceMessages(&.{});
    const second_text = try std.testing.allocator.dupe(u8, "again");
    try agent.prompt(@as([]const ai_types.Message, &.{.{ .user = .{ .content = .{ .text = second_text }, .timestamp = 0 } }}));
    try std.testing.expectEqualStrings(first[0..first_len], capture.session_id[0..capture.session_id_len]);
}

test "Agent sends the session id it was given, and a later one replaces it" {
    var capture = CaptureOptionsCtx{};
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = captureOptionsStreamFn, .ctx = &capture } });
    defer agent.deinit();
    agent.setModel(test_model);
    try agent.setSessionId("first-session");
    try agent.setSessionId("tui-session-1");

    const text = try std.testing.allocator.dupe(u8, "hello");
    const message = ai_types.Message{ .user = .{ .content = .{ .text = text }, .timestamp = 0 } };
    try agent.prompt(@as([]const ai_types.Message, &.{message}));

    try std.testing.expectEqualStrings("tui-session-1", capture.session_id[0..capture.session_id_len]);
}

test "Agent passes model max_tokens to protocol" {
    var capture = CaptureOptionsCtx{};
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = captureOptionsStreamFn, .ctx = &capture } });
    defer agent.deinit();
    var model = test_model;
    model.max_tokens = 8192;
    model.context_window = 1_000_000;
    agent.setModel(model);

    const text = try std.testing.allocator.dupe(u8, "hello");
    const message = ai_types.Message{ .user = .{ .content = .{ .text = text }, .timestamp = 0 } };
    try agent.prompt(@as([]const ai_types.Message, &.{message}));

    try std.testing.expectEqual(@as(?u32, 8192), capture.max_tokens);
}

test "Agent asks for less output when the prompt leaves less room in the context window" {
    var capture = CaptureOptionsCtx{};
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = captureOptionsStreamFn, .ctx = &capture } });
    defer agent.deinit();
    var model = test_model;
    model.max_tokens = 8192;
    model.context_window = 10_000;
    agent.setModel(model);

    const text = try std.testing.allocator.dupe(u8, "a" ** 8000);
    const message = ai_types.Message{ .user = .{ .content = .{ .text = text }, .timestamp = 0 } };
    const expected = agent_loop.outputLimit(model, model.max_tokens, .{ .messages = &.{message} }).?;
    try std.testing.expect(expected < 8192);
    try agent.prompt(@as([]const ai_types.Message, &.{message}));

    try std.testing.expectEqual(@as(?u32, expected), capture.max_tokens);
}

test "Agent validateContinueFromContext reports resume errors" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createDelayedErrorProtocol() });
    defer agent.deinit();

    try std.testing.expectError(error.NoModelConfigured, agent.validateContinueFromContext());
    agent.setModel(test_model);
    try std.testing.expectError(error.NoMessagesToContinue, agent.validateContinueFromContext());
}

test "Agent continueFromContextAsync rejects missing model" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createDelayedErrorProtocol() });
    defer agent.deinit();

    const text = try std.testing.allocator.dupe(u8, "hi");
    try agent.appendMessage(.{ .user = .{ .content = .{ .text = text }, .timestamp = 0 } });
    try std.testing.expectError(error.NoModelConfigured, agent.continueFromContextAsync());
    try std.testing.expect(!agent.isStreaming());
    try std.testing.expect(agent._thread == null);
}

const TerminalTally = struct {
    terminals: usize = 0,
    reason: []const u8 = "",
    saw_end: bool = false,

    fn onEvent(ctx: ?*anyopaque, event: AgentEvent) void {
        const self: *TerminalTally = @ptrCast(@alignCast(ctx.?));
        if (!event.isTerminal()) return;
        self.terminals += 1;
        switch (event) {
            .agent_end => self.saw_end = true,
            .run_failed => |payload| self.reason = payload.reason.slice(),
            else => {},
        }
    }
};

fn refusingStreamFn(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream_mod.AssistantMessageEventStream {
    _ = ctx;
    _ = model;
    _ = context;
    _ = options;
    _ = allocator;
    return error.BackendRefused;
}

test "a run that ends because its provider refused still ends exactly once" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{
        .stream_fn = refusingStreamFn,
        .ctx = null,
    } });
    defer agent.deinit();
    agent.setModel(test_model);

    var tally = TerminalTally{};
    agent.subscribeWithContext(&tally, TerminalTally.onEvent);
    try agent.promptAsync(ai_types.Message{ .user = .{ .content = .{ .text = "hello" }, .timestamp = 0 } });
    agent.waitForIdle();

    try std.testing.expectEqual(@as(usize, 1), tally.terminals);
    try std.testing.expect(tally.saw_end);
}

const CutOffMock = struct {
    cut_off_replies: usize,
    calls: usize = 0,
    asked: [4]?u32 = .{ null, null, null, null },
    continue_requests: usize = 0,
};

fn cutOffStreamFn(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream_mod.AssistantMessageEventStream {
    const mock: *CutOffMock = @ptrCast(@alignCast(ctx.?));
    if (mock.calls < mock.asked.len) mock.asked[mock.calls] = options.max_tokens;
    mock.calls += 1;
    const last = context.messages[context.messages.len - 1];
    if (last == .user and last.user.content == .text and std.mem.eql(u8, last.user.content.text, agent_loop.continue_request_text)) mock.continue_requests += 1;

    const stream = try allocator.create(event_stream_mod.AssistantMessageEventStream);
    stream.* = event_stream_mod.AssistantMessageEventStream.init(allocator);
    errdefer _ = stream.deinitAndDestroy();
    const body = try allocator.dupe(u8, "part of the answer");
    errdefer allocator.free(body);
    const content = try allocator.alloc(ai_types.AssistantContent, 1);
    content[0] = .{ .text = .{ .text = body } };
    const stop: ai_types.StopReason = if (mock.calls <= mock.cut_off_replies) .length else .stop;
    stream.complete(.{ .content = content, .api = model.api, .provider = model.provider, .model = model.id, .usage = .{}, .stop_reason = stop, .timestamp = 0 });
    return stream;
}

fn runCutOff(agent: *Agent) !void {
    var model = test_model;
    model.context_window = 1_000_000;
    model.max_tokens = 100_000;
    agent.setModel(model);
    try agent.promptAsync(ai_types.Message{ .user = .{ .content = .{ .text = "write it all" }, .timestamp = 0 } });
    agent.waitForIdle();
}

test "a reply cut off at the default output limit is continued once at the model's maximum" {
    var mock = CutOffMock{ .cut_off_replies = 1 };
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = cutOffStreamFn, .ctx = &mock } });
    defer agent.deinit();

    try runCutOff(&agent);

    try std.testing.expectEqual(@as(usize, 2), mock.calls);
    try std.testing.expectEqual(@as(?u32, agent_loop.default_output_tokens), mock.asked[0]);
    try std.testing.expectEqual(@as(?u32, 100_000), mock.asked[1]);
    try std.testing.expectEqual(@as(usize, 1), mock.continue_requests);
}

test "a reply cut off at the model's maximum ends the run without a continue request" {
    var mock = CutOffMock{ .cut_off_replies = 2 };
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = cutOffStreamFn, .ctx = &mock } });
    defer agent.deinit();
    agent.setOutput(.max);

    try runCutOff(&agent);

    try std.testing.expectEqual(@as(usize, 1), mock.calls);
    try std.testing.expectEqual(@as(?u32, 100_000), mock.asked[0]);
    try std.testing.expectEqual(@as(usize, 0), mock.continue_requests);
}

test "a reply cut off at a count the user set ends the run without raising it" {
    var mock = CutOffMock{ .cut_off_replies = 2 };
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = cutOffStreamFn, .ctx = &mock } });
    defer agent.deinit();
    agent.setOutput(.{ .tokens = 8_000 });

    try runCutOff(&agent);

    try std.testing.expectEqual(@as(usize, 1), mock.calls);
    try std.testing.expectEqual(@as(?u32, 8_000), mock.asked[0]);
    try std.testing.expectEqual(@as(usize, 0), mock.continue_requests);
}

test "a reply cut off twice is continued only once" {
    var mock = CutOffMock{ .cut_off_replies = 3 };
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = cutOffStreamFn, .ctx = &mock } });
    defer agent.deinit();

    try runCutOff(&agent);

    try std.testing.expectEqual(@as(usize, 2), mock.calls);
    try std.testing.expectEqual(@as(usize, 1), mock.continue_requests);
}

const ReasoningOnlyMock = struct {
    reasoning_replies: usize,
    calls: usize = 0,
    answer_requests: usize = 0,
};

fn reasoningOnlyStreamFn(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream_mod.AssistantMessageEventStream {
    _ = options;
    const mock: *ReasoningOnlyMock = @ptrCast(@alignCast(ctx.?));
    mock.calls += 1;
    const last = context.messages[context.messages.len - 1];
    if (last == .user and last.user.content == .text and std.mem.eql(u8, last.user.content.text, agent_loop.answer_request_text)) mock.answer_requests += 1;

    const stream = try allocator.create(event_stream_mod.AssistantMessageEventStream);
    stream.* = event_stream_mod.AssistantMessageEventStream.init(allocator);
    errdefer _ = stream.deinitAndDestroy();
    const body = try allocator.dupe(u8, "the answer");
    errdefer allocator.free(body);
    const content = try allocator.alloc(ai_types.AssistantContent, 1);
    content[0] = if (mock.calls <= mock.reasoning_replies)
        .{ .thinking = .{ .thinking = body } }
    else
        .{ .text = .{ .text = body } };
    stream.complete(.{ .content = content, .api = model.api, .provider = model.provider, .model = model.id, .usage = .{}, .stop_reason = .stop, .timestamp = 0 });
    return stream;
}

fn runReasoningOnly(agent: *Agent) !void {
    agent.setModel(test_model);
    try agent.promptAsync(ai_types.Message{ .user = .{ .content = .{ .text = "what is the plan?" }, .timestamp = 0 } });
    agent.waitForIdle();
}

test "a reply that holds only reasoning gets one request for the answer, and the run goes on" {
    var mock = ReasoningOnlyMock{ .reasoning_replies = 1 };
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = reasoningOnlyStreamFn, .ctx = &mock } });
    defer agent.deinit();
    try runReasoningOnly(&agent);

    try std.testing.expectEqual(@as(usize, 2), mock.calls);
    try std.testing.expectEqual(@as(usize, 1), mock.answer_requests);
    const messages = agent._state.messages.items;
    const final = messages[messages.len - 1];
    try std.testing.expect(final == .assistant);
    try std.testing.expect(final.assistant.content[0] == .text);
}

test "a run whose replies hold only reasoning asks for the answer once, then ends" {
    var mock = ReasoningOnlyMock{ .reasoning_replies = std.math.maxInt(usize) };
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = reasoningOnlyStreamFn, .ctx = &mock } });
    defer agent.deinit();
    try runReasoningOnly(&agent);

    try std.testing.expectEqual(@as(usize, 2), mock.calls);
    try std.testing.expectEqual(@as(usize, 1), mock.answer_requests);
    try std.testing.expect(!agent.isStreaming());
}

const CancellingToolMock = struct {
    calls: usize = 0,
};

fn cancellingToolStreamFn(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream_mod.AssistantMessageEventStream {
    _ = context;
    _ = options;
    const mock: *CancellingToolMock = @ptrCast(@alignCast(ctx.?));
    mock.calls += 1;

    const stream = try allocator.create(event_stream_mod.AssistantMessageEventStream);
    stream.* = event_stream_mod.AssistantMessageEventStream.init(allocator);
    errdefer _ = stream.deinitAndDestroy();
    const id = try allocator.dupe(u8, "call_1");
    errdefer allocator.free(id);
    const name = try allocator.dupe(u8, "slow_tool");
    errdefer allocator.free(name);
    const args = try allocator.dupe(u8, "{}");
    errdefer allocator.free(args);
    const content = try allocator.alloc(ai_types.AssistantContent, 1);
    content[0] = .{ .tool_call = .{ .id = id, .name = name, .arguments_json = args } };
    stream.complete(.{ .content = content, .api = model.api, .provider = model.provider, .model = model.id, .usage = .{}, .stop_reason = .tool_use, .timestamp = 0 });
    return stream;
}

fn userAbortsDuringTool(
    tool_call_id: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?types.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!types.AgentToolResult {
    _ = tool_call_id;
    _ = args_json;
    _ = on_update_ctx;
    _ = on_update;
    _ = allocator;
    cancel_token.?.cancelled.store(true, .release);
    return error.Cancelled;
}

const TerminationCapture = struct {
    termination: ?types.AgentTermination = null,
    ended: bool = false,

    fn onEvent(ctx: ?*anyopaque, event: AgentEvent) void {
        const self: *TerminationCapture = @ptrCast(@alignCast(ctx.?));
        switch (event) {
            .agent_end => |payload| {
                self.ended = true;
                self.termination = payload.termination;
            },
            else => {},
        }
    }
};

test "a run cancelled while a tool runs asks the model for nothing more and ends cancelled" {
    var mock = CancellingToolMock{};
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = cancellingToolStreamFn, .ctx = &mock } });
    defer agent.deinit();
    agent.setModel(test_model);
    const tools = [_]AgentTool{.{
        .label = "Slow",
        .name = "slow_tool",
        .description = "A tool the user aborts while it runs",
        .parameters_schema_json = "{\"type\":\"object\"}",
        .execute = userAbortsDuringTool,
    }};
    agent.setTools(&tools);

    var capture = TerminationCapture{};
    agent.subscribeWithContext(&capture, TerminationCapture.onEvent);
    try agent.promptAsync(ai_types.Message{ .user = .{ .content = .{ .text = "run it" }, .timestamp = 0 } });
    agent.waitForIdle();

    try std.testing.expectEqual(@as(usize, 1), mock.calls);
    try std.testing.expect(capture.ended);
    try std.testing.expectEqual(@as(?types.AgentTermination, .cancelled), capture.termination);
}

test "a run that stops before it starts still ends, and says why" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createMockProtocol() });
    defer agent.deinit();
    var tally = TerminalTally{};
    agent.subscribeWithContext(&tally, TerminalTally.onEvent);

    const empty = try std.testing.allocator.alloc(ai_types.Message, 0);
    agent.runLoopThread(empty, false, false);
    try std.testing.expectEqual(@as(usize, 1), tally.terminals);
    try std.testing.expectEqualStrings("NothingToRun", tally.reason);

    const one = try std.testing.allocator.alloc(ai_types.Message, 1);
    one[0] = .{ .user = .{ .content = .{ .text = try std.testing.allocator.dupe(u8, "hello") }, .timestamp = 0 } };
    agent.runLoopThread(one, false, false);
    try std.testing.expectEqual(@as(usize, 2), tally.terminals);
    try std.testing.expectEqualStrings("NoModelConfigured", tally.reason);
    try std.testing.expect(!tally.saw_end);
}

test "isTerminal names exactly the two events that end a run" {
    try std.testing.expect(!(AgentEvent{ .agent_start = {} }).isTerminal());
    try std.testing.expect(!(AgentEvent{ .turn_start = {} }).isTerminal());
    try std.testing.expect((AgentEvent{ .agent_end = .{} }).isTerminal());
    try std.testing.expect((AgentEvent{ .run_failed = .{} }).isTerminal());
}

test "Agent continueFromContextAsync mirrors sync resume checks" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createDelayedErrorProtocol() });
    defer agent.deinit();
    agent.setModel(test_model);

    const assistant = ai_types.Message{ .assistant = .{
        .content = &.{},
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 0,
    } };
    try agent.appendMessage(assistant);

    try std.testing.expectError(error.CannotContinueFromAssistant, agent.continueFromContextAsync());

    const steering_text = try std.testing.allocator.dupe(u8, "steer");
    try agent.steer(.{ .user = .{ .content = .{ .text = steering_text }, .timestamp = 1 } });
    try agent.continueFromContextAsync();
    try std.testing.expect(agent.isStreaming());
    agent.waitForIdle();
    try std.testing.expectEqual(@as(usize, 0), agent._steering_queue.items.len);
}

test "Agent installs cancel token before async worker can run" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createMockProtocol() });
    defer agent.deinit();
    agent.setModel(test_model);

    const msg = ai_types.Message{ .user = .{ .content = .{ .text = "cancel me" }, .timestamp = 0 } };
    try agent.promptAsync(msg);
    try std.testing.expect(agent._cancel_token != null);
    agent.abort();
    try std.testing.expect(agent._cancel_token.?.isCancelled());
    agent.waitForIdle();
}
test "Agent cloneMessage" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createMockProtocol() });
    defer agent.deinit();

    const original = ai_types.Message{
        .user = .{
            .content = .{ .text = "Hello, world!" },
            .timestamp = 12345,
        },
    };

    var cloned = try agent.cloneMessage(original);
    defer cloned.deinit(std.testing.allocator);

    try std.testing.expect(cloned == .user);
    try std.testing.expectEqualStrings("Hello, world!", cloned.user.content.text);
}

test "Agent cloneMessage preserves assistant signatures" {
    var agent = Agent.init(std.testing.allocator, .{ .protocol = createMockProtocol() });
    defer agent.deinit();

    const content = [_]ai_types.AssistantContent{
        .{ .text = .{ .text = "text", .text_signature = "text-sig" } },
        .{ .thinking = .{ .thinking = "thought", .thinking_signature = "thinking-sig" } },
        .{ .tool_call = .{ .id = "tool-id", .name = "tool", .arguments_json = "{}", .thought_signature = "thought-sig" } },
    };
    const original = ai_types.Message{ .assistant = .{
        .content = &content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 12345,
    } };

    var cloned = try agent.cloneMessage(original);
    defer cloned.deinit(std.testing.allocator);

    try std.testing.expect(cloned == .assistant);
    try std.testing.expectEqualStrings("text-sig", cloned.assistant.content[0].text.text_signature.?);
    try std.testing.expectEqualStrings("thinking-sig", cloned.assistant.content[1].thinking.thinking_signature.?);
    try std.testing.expectEqualStrings("thought-sig", cloned.assistant.content[2].tool_call.thought_signature.?);
}

const SummaryMock = struct {
    reply: []const u8 = "<analysis>notes</analysis>\n<summary>\nstate of work\n</summary>",
    fail: bool = false,
    overflow_first: bool = false,
    wait_for_cancel: bool = false,
    calls: usize = 0,
    message_count: usize = 0,
    max_tokens: ?u32 = null,
    request_has_focus: bool = false,
};

fn summaryStreamFn(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream_mod.AssistantMessageEventStream {
    const mock: *SummaryMock = @ptrCast(@alignCast(ctx.?));
    mock.calls += 1;
    mock.message_count = context.messages.len;
    mock.max_tokens = options.max_tokens;
    const last = context.messages[context.messages.len - 1];
    if (last == .user and last.user.content == .text) mock.request_has_focus = std.mem.indexOf(u8, last.user.content.text, "the parser") != null;

    const stream = try allocator.create(event_stream_mod.AssistantMessageEventStream);
    stream.* = event_stream_mod.AssistantMessageEventStream.init(allocator);
    errdefer _ = stream.deinitAndDestroy();
    if (mock.wait_for_cancel) {
        var waits: usize = 0;
        while (!options.cancel_token.?.isCancelled() and waits < 1000) : (waits += 1) {
            defaultIo().sleep(.fromNanoseconds(1 * std.time.ns_per_ms), .boot) catch {};
        }
        stream.complete(.{ .content = &.{}, .api = model.api, .provider = model.provider, .model = model.id, .usage = .{}, .stop_reason = .aborted, .timestamp = 0 });
        return stream;
    }
    if (mock.fail) {
        stream.completeWithError("provider unavailable");
        return stream;
    }
    if (mock.overflow_first and mock.calls == 1) {
        stream.complete(.{
            .content = &.{},
            .api = model.api,
            .provider = model.provider,
            .model = model.id,
            .usage = .{},
            .stop_reason = .@"error",
            .error_message = ai_types.OwnedSlice(u8).initBorrowed("prompt is too long: 9000 tokens > 8192 maximum"),
            .timestamp = 0,
        });
        return stream;
    }
    const text = try allocator.dupe(u8, mock.reply);
    errdefer allocator.free(text);
    const content = try allocator.alloc(ai_types.AssistantContent, 1);
    content[0] = .{ .text = .{ .text = text } };
    stream.complete(.{ .content = content, .api = model.api, .provider = model.provider, .model = model.id, .usage = .{}, .stop_reason = .stop, .timestamp = 0 });
    return stream;
}

const CompactionCapture = struct {
    outcome: ?std.meta.Tag(compaction.Result) = null,
    text: []u8 = &.{},
    failure: []u8 = &.{},
    messages_before: usize = 0,

    fn record(ctx: ?*anyopaque, result: *const compaction.Result) void {
        const self: *CompactionCapture = @ptrCast(@alignCast(ctx.?));
        self.outcome = std.meta.activeTag(result.*);
        switch (result.*) {
            .completed => |completed| {
                self.text = std.testing.allocator.dupe(u8, completed.text) catch &.{};
                self.messages_before = completed.messages_before;
            },
            .failed => |message| self.failure = std.testing.allocator.dupe(u8, message) catch &.{},
            .cancelled => {},
        }
    }

    fn deinit(self: *CompactionCapture) void {
        std.testing.allocator.free(self.text);
        std.testing.allocator.free(self.failure);
    }
};

fn appendExchange(agent: *Agent, question: []const u8, answer: []const u8) !void {
    const allocator = agent._allocator;
    const question_text = try allocator.dupe(u8, question);
    {
        errdefer allocator.free(question_text);
        try agent.appendMessage(.{ .user = .{ .content = .{ .text = question_text }, .timestamp = 0 } });
    }
    const text = try allocator.dupe(u8, answer);
    errdefer allocator.free(text);
    const content = try allocator.alloc(ai_types.AssistantContent, 1);
    errdefer allocator.free(content);
    content[0] = .{ .text = .{ .text = text } };
    try agent.appendMessage(.{ .assistant = .{ .content = content, .api = "test-api", .provider = "test-provider", .model = "test-model", .usage = .{}, .stop_reason = .stop, .timestamp = 0 } });
}

fn summarizeHistoryProbe(allocator: std.mem.Allocator) !void {
    var mock = SummaryMock{};
    var agent = Agent.init(allocator, .{ .protocol = .{ .stream_fn = summaryStreamFn, .ctx = &mock } });
    defer agent.deinit();
    agent.setModel(test_model);
    try appendExchange(&agent, "first question", "first answer");
    const transcripts = [_][]const u8{"/sessions/s1/compaction-1.jsonl"};
    var result = try agent.summarizeHistory(.{ .focus = "the parser", .transcripts = &transcripts });
    result.deinit(allocator);
}

test "summarizeHistory survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, summarizeHistoryProbe, .{});
}

test "Agent compactAsync replaces the history with the model's summary and an acknowledgement" {
    var mock = SummaryMock{};
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = summaryStreamFn, .ctx = &mock } });
    defer agent.deinit();
    agent.setModel(test_model);
    try appendExchange(&agent, "first question", "first answer");

    var capture = CompactionCapture{};
    defer capture.deinit();
    const transcripts = [_][]const u8{"/sessions/s1/compaction-1.jsonl"};
    try agent.compactAsync(.{ .focus = "the parser", .transcripts = &transcripts }, &capture, CompactionCapture.record);
    agent.waitForIdle();

    try std.testing.expectEqual(@as(?std.meta.Tag(compaction.Result), .completed), capture.outcome);
    try std.testing.expectEqual(@as(usize, 2), capture.messages_before);
    try std.testing.expectEqual(@as(usize, 3), mock.message_count);
    try std.testing.expect(mock.request_has_focus);
    try std.testing.expectEqual(@as(?u32, test_model.max_tokens), mock.max_tokens);

    const messages = agent._state.messages.items;
    try std.testing.expectEqual(@as(usize, 2), messages.len);
    const summary = messages[0].user.content.text;
    try std.testing.expectEqualStrings(capture.text, summary);
    try std.testing.expect(std.mem.startsWith(u8, summary, compaction.header));
    try std.testing.expectEqualStrings("state of work", compaction.summaryOf(summary));
    try std.testing.expect(std.mem.indexOf(u8, summary, "notes") == null);
    try std.testing.expect(std.mem.indexOf(u8, summary, "- /sessions/s1/compaction-1.jsonl") != null);
    try std.testing.expectEqualStrings(compaction.acknowledgement, messages[1].assistant.content[0].text.text);
    try std.testing.expectError(error.NothingToCompact, agent.ensureCompactable());
    try std.testing.expect(agent.isIdle());
}

const MidRunMock = struct {
    calls: usize = 0,
    summary_requests: usize = 0,
    carried_on: usize = 0,
    reported_input: u64 = 9_000,
    model_ids: [4][]const u8 = .{ "", "", "", "" },
    max_tokens: [4]u32 = .{ 0, 0, 0, 0 },
};

fn midRunStreamFn(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: types.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream_mod.AssistantMessageEventStream {
    const mock: *MidRunMock = @ptrCast(@alignCast(ctx.?));
    if (mock.calls < mock.model_ids.len) {
        mock.model_ids[mock.calls] = model.id;
        mock.max_tokens[mock.calls] = options.max_tokens orelse 0;
    }
    mock.calls += 1;
    const last = context.messages[context.messages.len - 1];
    const last_text: []const u8 = if (last == .user and last.user.content == .text) last.user.content.text else "";

    const stream = try allocator.create(event_stream_mod.AssistantMessageEventStream);
    stream.* = event_stream_mod.AssistantMessageEventStream.init(allocator);
    errdefer _ = stream.deinitAndDestroy();
    const content = try allocator.alloc(ai_types.AssistantContent, 1);
    errdefer allocator.free(content);
    var usage: ai_types.Usage = .{};
    if (std.mem.indexOf(u8, last_text, "about to be compacted") != null) {
        mock.summary_requests += 1;
        content[0] = .{ .text = .{ .text = try allocator.dupe(u8, "<summary>state of work</summary>") } };
    } else if (std.mem.eql(u8, last_text, agent_loop.compacted_request_text)) {
        mock.carried_on += 1;
        content[0] = .{ .text = .{ .text = try allocator.dupe(u8, "finished") } };
    } else if (std.mem.eql(u8, last_text, agent_loop.answer_request_text)) {
        content[0] = .{ .text = .{ .text = try allocator.dupe(u8, "finished") } };
    } else {
        content[0] = .{ .thinking = .{ .thinking = try allocator.dupe(u8, "planning") } };
        usage.input = mock.reported_input;
    }
    stream.complete(.{ .content = content, .api = model.api, .provider = model.provider, .model = model.id, .usage = usage, .stop_reason = .stop, .timestamp = 0 });
    return stream;
}

const CompactionEvents = struct {
    started: usize = 0,
    completed: usize = 0,
    history_len: usize = 0,
    reported_transcript: bool = false,

    fn onEvent(ctx: ?*anyopaque, event: AgentEvent) void {
        const self: *CompactionEvents = @ptrCast(@alignCast(ctx.?));
        switch (event) {
            .compaction_start => self.started += 1,
            .compaction_end => |payload| {
                if (payload.outcome == .completed) self.completed += 1;
                if (std.mem.eql(u8, payload.transcript.slice(), "/sessions/s1/compaction-1.jsonl")) self.reported_transcript = true;
            },
            else => {},
        }
    }

    fn transcripts(ctx: ?*anyopaque, history: []const ai_types.Message) Agent.CompactionTranscripts {
        const self: *CompactionEvents = @ptrCast(@alignCast(ctx.?));
        self.history_len = history.len;
        const paths = struct {
            const list = [_][]const u8{"/sessions/s1/compaction-1.jsonl"};
        };
        return .{ .paths = &paths.list, .saved = paths.list[0] };
    }
};

fn runMidRun(agent: *Agent, events: *CompactionEvents, at: ?u64) !void {
    var model = test_model;
    model.context_window = 100_000;
    agent.setModel(model);
    agent.subscribeWithContext(events, CompactionEvents.onEvent);
    agent.setAutoCompact(at, .{ .ctx = events, .transcripts_fn = CompactionEvents.transcripts });
    try agent.promptAsync(ai_types.Message{ .user = .{ .content = .{ .text = "plan the work" }, .timestamp = 0 } });
    agent.waitForIdle();
}

test "a run past the threshold compacts between turns and carries on from the summary" {
    var mock = MidRunMock{};
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = midRunStreamFn, .ctx = &mock } });
    defer agent.deinit();
    var events = CompactionEvents{};

    try runMidRun(&agent, &events, 5_000);

    try std.testing.expectEqual(@as(usize, 3), mock.calls);
    try std.testing.expectEqual(@as(usize, 1), mock.summary_requests);
    try std.testing.expectEqual(@as(usize, 1), mock.carried_on);
    try std.testing.expectEqual(@as(usize, 1), events.started);
    try std.testing.expectEqual(@as(usize, 1), events.completed);
    try std.testing.expectEqual(@as(usize, 3), events.history_len);
    try std.testing.expect(events.reported_transcript);

    const messages = agent._state.messages.items;
    try std.testing.expectEqual(@as(usize, 4), messages.len);
    try std.testing.expect(std.mem.startsWith(u8, messages[0].user.content.text, compaction.header));
    try std.testing.expect(std.mem.indexOf(u8, messages[0].user.content.text, "- /sessions/s1/compaction-1.jsonl") != null);
    try std.testing.expectEqualStrings(compaction.acknowledgement, messages[1].assistant.content[0].text.text);
    try std.testing.expectEqualStrings(agent_loop.compacted_request_text, messages[2].user.content.text);
    try std.testing.expectEqualStrings("finished", messages[3].assistant.content[0].text.text);
}

test "a model switch requested during a run takes effect from the next turn" {
    var mock = MidRunMock{};
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = midRunStreamFn, .ctx = &mock } });
    defer agent.deinit();
    var events = CompactionEvents{};
    var next = test_model;
    next.id = "next-model";
    next.max_tokens = 512;

    agent.requestModelSwitch(next);
    try runMidRun(&agent, &events, null);

    try std.testing.expectEqual(@as(usize, 2), mock.calls);
    try std.testing.expectEqualStrings(test_model.id, mock.model_ids[0]);
    try std.testing.expectEqualStrings("next-model", mock.model_ids[1]);
    try std.testing.expectEqualStrings("next-model", agent._state.model.?.id);
    try std.testing.expect(mock.max_tokens[0] > 512);
    try std.testing.expectEqual(@as(u32, 512), mock.max_tokens[1]);
    try std.testing.expect(Agent.nextModel(&agent) == null);
}

test "a run under the threshold, or with no threshold, does not compact" {
    const thresholds = [_]?u64{ 50_000, null };
    for (thresholds) |at| {
        var mock = MidRunMock{};
        var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = midRunStreamFn, .ctx = &mock } });
        defer agent.deinit();
        var events = CompactionEvents{};

        try runMidRun(&agent, &events, at);

        try std.testing.expectEqual(@as(usize, 2), mock.calls);
        try std.testing.expectEqual(@as(usize, 0), mock.summary_requests);
        try std.testing.expectEqual(@as(usize, 0), events.started);
        try std.testing.expectEqual(@as(usize, 4), agent._state.messages.items.len);
    }
}

test "a requested compaction runs between turns without a threshold and is consumed" {
    var mock = MidRunMock{};
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = midRunStreamFn, .ctx = &mock } });
    defer agent.deinit();
    var events = CompactionEvents{};

    try agent.requestCompaction("the parser rewrite");
    try runMidRun(&agent, &events, null);

    try std.testing.expectEqual(@as(usize, 1), mock.summary_requests);
    try std.testing.expectEqual(@as(usize, 1), events.completed);
    try std.testing.expectEqual(@as(usize, 1), mock.carried_on);
    try std.testing.expect(agent.takeCompactionRequest() == null);
}

test "a compaction request is replaced by a later one and taken once" {
    var mock = MidRunMock{};
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = midRunStreamFn, .ctx = &mock } });
    defer agent.deinit();
    try agent.requestCompaction("first");
    try agent.requestCompaction("second");
    const taken = agent.takeCompactionRequest() orelse return error.TestExpectedRequest;
    defer std.testing.allocator.free(taken);
    try std.testing.expectEqualStrings("second", taken);
    try std.testing.expect(agent.takeCompactionRequest() == null);
}

test "Agent compactAsync retries with less history when the request overflows the context" {
    var mock = SummaryMock{ .overflow_first = true };
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = summaryStreamFn, .ctx = &mock } });
    defer agent.deinit();
    agent.setModel(test_model);
    try appendExchange(&agent, "first question", "first answer");
    try appendExchange(&agent, "second question", "second answer");

    var capture = CompactionCapture{};
    defer capture.deinit();
    try agent.compactAsync(.{}, &capture, CompactionCapture.record);
    agent.waitForIdle();

    try std.testing.expectEqual(@as(?std.meta.Tag(compaction.Result), .completed), capture.outcome);
    try std.testing.expectEqual(@as(usize, 2), mock.calls);
    try std.testing.expectEqual(@as(usize, 3), mock.message_count);
    try std.testing.expectEqual(@as(usize, 4), capture.messages_before);
    try std.testing.expect(std.mem.indexOf(u8, capture.text, "did not fit in one request") != null);
}

test "Agent compactAsync keeps the history when the summary request fails" {
    var mock = SummaryMock{ .fail = true };
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = summaryStreamFn, .ctx = &mock } });
    defer agent.deinit();
    agent.setModel(test_model);
    try appendExchange(&agent, "first question", "first answer");

    var capture = CompactionCapture{};
    defer capture.deinit();
    try agent.compactAsync(.{}, &capture, CompactionCapture.record);
    agent.waitForIdle();

    try std.testing.expectEqual(@as(?std.meta.Tag(compaction.Result), .failed), capture.outcome);
    try std.testing.expectEqualStrings("provider unavailable", capture.failure);
    try std.testing.expectEqual(@as(usize, 2), agent._state.messages.items.len);
    try std.testing.expectEqualStrings("first question", agent._state.messages.items[0].user.content.text);
}

test "Agent compactAsync reports a cancelled request and keeps the history" {
    var mock = SummaryMock{ .wait_for_cancel = true };
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = summaryStreamFn, .ctx = &mock } });
    defer agent.deinit();
    agent.setModel(test_model);
    try appendExchange(&agent, "first question", "first answer");

    var capture = CompactionCapture{};
    defer capture.deinit();
    try agent.compactAsync(.{}, &capture, CompactionCapture.record);
    agent.abort();
    agent.waitForIdle();

    try std.testing.expectEqual(@as(?std.meta.Tag(compaction.Result), .cancelled), capture.outcome);
    try std.testing.expectEqual(@as(usize, 2), agent._state.messages.items.len);
    try std.testing.expectEqualStrings("first answer", agent._state.messages.items[1].assistant.content[0].text.text);
}

test "Agent refuses to compact without a model or without history" {
    var mock = SummaryMock{};
    var agent = Agent.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = summaryStreamFn, .ctx = &mock } });
    defer agent.deinit();

    try appendExchange(&agent, "first question", "first answer");
    try std.testing.expectError(error.NoModelConfigured, agent.ensureCompactable());
    agent.setModel(test_model);
    agent.clearMessages();
    var capture = CompactionCapture{};
    defer capture.deinit();
    try std.testing.expectError(error.NothingToCompact, agent.compactAsync(.{}, &capture, CompactionCapture.record));
    try std.testing.expect(capture.outcome == null);
    try std.testing.expect(!agent.isStreaming());
}
