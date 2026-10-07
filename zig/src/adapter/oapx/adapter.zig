const std = @import("std");
const contract = @import("contract");
const oap_types = @import("oap_types");
const compat = @import("compat");
const json_encode = @import("json_encode");
const ai_types = @import("ai_types");
const tui_runtime = @import("tui_runtime");
const tui_session = @import("tui_session");
const model_ref = @import("model_ref");
const permission = @import("permission");
const interactions = @import("interactions.zig");

pub const endpoint_id = "oapx.agent";
pub const capability_revision = "oapx-agent-v9";

pub const journal_capacity: usize = 1 << 16;
pub const queue_capacity: usize = 8;

const protocol_name = "open-agent-protocol";
const protocol_version = "0.1";
const profile = "open-agent-protocol.agent-control-core";

const features = [_]contract.Feature{
    .{ .key = "protocol.initialize", .level = .native },
    .{ .key = "capabilities", .level = .native },
    .{ .key = "session.open", .level = .native },
    .{ .key = "session.state", .level = .degraded, .reason = "the state is live; no transcript is replayed and a session does not outlive the process" },
    .{ .key = contract.feature_submit, .level = .native },
    .{ .key = "session.message.delivery.auto", .level = .native },
    .{ .key = "session.message.delivery.queue", .level = .native },
    .{ .key = "session.message.delivery.steer", .level = .native, .reason = "guidance joins the running loop after its current tool result or turn; guidance still waiting when the run ends is dropped" },
    .{ .key = "action.permissions", .level = .native, .scope = "call", .reason = "ask mode waits for the declared responder, and approve_always or reject_always settles a call that names a path or a command for the rest of the session, as the loop remembers it; bypass mode skips prompts" },
    .{ .key = "user_input", .level = .native, .reason = "request_user_input asks text or choice questions and validates answers before returning them to the tool" },
    .{ .key = "run.streaming", .level = .native },
    .{ .key = "run.status", .level = .native },
    .{ .key = "run.cancel", .level = .native, .reason = "cancelling a run leaves its session open" },
    .{ .key = "run.replay", .level = .degraded, .reason = "up to 65536 events across the session's runs are retained in process memory" },
    .{ .key = "content.reasoning", .level = .native },
    .{ .key = "action.tools", .level = .native, .reason = "the agent loop runs its own workspace tools" },
    .{ .key = "action.tools.execute", .level = .native, .reason = "the agent loop runs its own workspace tools" },
    .{ .key = contract.feature_tools_list, .level = .native },
    .{ .key = contract.feature_models_list, .level = .degraded, .reason = "the catalog is the one the terminal UI offers, and a refresh there moves it at the session's next model switch under the same revision, with no capabilities.updated" },
    .{ .key = contract.feature_model_switch, .level = .native },
    .{ .key = contract.feature_session_compact, .level = .native, .reason = "a compaction is a run of its own in which the loop summarizes the history with the session's model, the focus as its instructions, admitted under submit's rules; continue is refused" },
    .{ .key = "run.compaction", .level = .native, .reason = "the loop compacts between turns once its estimate of the history reaches the session's threshold, and on request; it does not compact on a provider's overflow" },
    .{ .key = contract.feature_compaction_policy, .level = .native, .reason = "auto is the loop's own threshold below the model's window, share a percentage of the window, tokens a count, and off never; it takes effect from the next run", .modes = &.{ contract.mode_session_open, contract.mode_session_live } },
    .{ .key = contract.feature_session_reasoning, .level = .native, .reason = "the agent loop's thinking level, at open and between runs; minimal, which the loop would run as low, is refused", .modes = &.{ contract.mode_session_open, contract.mode_session_live } },
};

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = "oapx agent loop", .version = protocol_version, .adapter = "in-process" },
    .capability_revision = capability_revision,
    .features = &features,
    .limits = .{ .max_active_runs_per_session = queue_capacity + 1, .max_queued_runs_per_session = queue_capacity },
};

fn wallClock() i64 {
    return compat.time.nowMillis();
}

pub const TranscriptStore = struct {
    ctx: *anyopaque,
    save: *const fn (ctx: *anyopaque, allocator: std.mem.Allocator, session_id: []const u8, index: usize, history: []const ai_types.Message) ?[]u8,
};

pub const Recorder = struct {
    ctx: *anyopaque,
    record: *const fn (ctx: *anyopaque, session_id: []const u8, event: *const tui_session.TuiEvent) void,
};

pub const Adapter = struct {
    allocator: std.mem.Allocator,
    options: tui_runtime.TuiRuntimeOptions,
    catalog: ?[]ai_types.Model = null,
    catalog_generation: u64 = 0,
    transcripts: ?TranscriptStore = null,
    recorder: ?Recorder = null,
    ids: u64 = 0,
    now_ms: *const fn () i64 = wallClock,

    pub fn init(allocator: std.mem.Allocator, options: tui_runtime.TuiRuntimeOptions) Adapter {
        var runtime_options = options;
        runtime_options.run_async = true;
        runtime_options.generate_titles = false;
        return .{ .allocator = allocator, .options = runtime_options };
    }

    pub fn deinit(self: *Adapter) void {
        if (self.catalog) |held| tui_runtime.deinitModels(self.allocator, held);
        self.catalog = null;
    }

    pub fn setCatalog(self: *Adapter, models: []const ai_types.Model) !void {
        const next = try tui_runtime.cloneModels(self.allocator, models);
        if (self.catalog) |held| tui_runtime.deinitModels(self.allocator, held);
        self.catalog = next;
        self.catalog_generation += 1;
    }

    pub fn adapter(self: *Adapter) contract.Adapter {
        return .{ .ptr = self, .vtable = &.{ .probe = probe, .open = open } };
    }

    fn probe(ptr: *anyopaque, refusal: *contract.Refusal) contract.Failure!contract.Descriptor {
        _ = ptr;
        _ = refusal;
        return descriptor;
    }

    fn open(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!contract.Session {
        _ = arena;
        const self: *Adapter = @ptrCast(@alignCast(ptr));
        if (request.participant.len == 0) return refusal.fail(error.InvalidSubmission, "open requires a non-empty participant id");
        if (contract.carriesEntries(request.tools_json)) return refusal.unsupported(contract.feature_tools_provide, contract.reason_unadvertised);
        if (contract.carriesEntries(request.tool_sources_json)) return refusal.unsupported(contract.feature_tool_sources_attach, contract.reason_unadvertised);
        const session = try Session.create(self, request, refusal);
        return session.handle();
    }

    fn nextID(self: *Adapter, allocator: std.mem.Allocator, kind: []const u8) ![]u8 {
        self.ids += 1;
        return std.fmt.allocPrint(allocator, "{s}-{d}-{d}", .{ kind, self.now_ms(), self.ids });
    }
};

const Run = struct {
    id: []const u8,
    input_text: []const u8 = "",
    submit_id: []const u8 = "",
    started: bool = false,
    next_sequence: u64 = 1,
    status: oap_types.RunStatus = .running,
    terminal: bool = false,
    model_id: []const u8 = "",
    message_id: []const u8 = "",
    text: std.ArrayList(u8) = .empty,
    stop_reason: []const u8 = "end_turn",
    error_text: std.ArrayList(u8) = .empty,
    output_tokens: u64 = 0,
    context_tokens: u64 = 0,
    compaction: bool = false,
    compact_focus: []const u8 = "",
    compaction_id: []const u8 = "",
    steers: std.ArrayList(PendingSteer) = .empty,
    admitted_steers: std.ArrayList([]const u8) = .empty,
    after_tool: bool = false,
};

const PendingSteer = struct {
    submission_id: []const u8,
    request_id: []const u8,
    message_ids: []const []const u8,
};

const PendingInteraction = struct {
    id: []const u8,
    kind: interactions.Kind,
    tool_call_id: []const u8,
    arguments: []const u8,
};

const PreparedEvent = struct { line: []u8, kept: []u8 };

const Journaled = struct {
    line: []u8,
    run_id: []const u8,
    sequence: u64,
};

pub const Session = struct {
    owner: *Adapter,
    gpa: std.mem.Allocator,
    keep: std.heap.ArenaAllocator,
    id: []const u8,
    participant: []const u8,
    runtime: *tui_runtime.TuiRuntime,
    engine: ?*permission.PermissionEngine = null,
    updated_at_ms: i64,
    catalog_seen: u64 = 0,
    run: ?*Run = null,
    runs: std.ArrayList(*Run) = .empty,
    gate: interactions.Gate = .{},
    pending: ?PendingInteraction = null,
    outbox: std.ArrayList(Journaled) = .empty,
    journal: std.ArrayList(Journaled) = .empty,
    policy_json: ?[]const u8 = null,

    fn create(owner: *Adapter, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const gpa = owner.allocator;
        const self = try gpa.create(Session);
        errdefer gpa.destroy(self);
        const runtime = try gpa.create(tui_runtime.TuiRuntime);
        errdefer gpa.destroy(runtime);
        const offers_input = offersUserInput(request.metadata);
        const session_tools = try gpa.alloc(agent.AgentTool, owner.options.tools.len + @intFromBool(offers_input));
        defer gpa.free(session_tools);
        @memcpy(session_tools[0..owner.options.tools.len], owner.options.tools);
        if (offers_input) session_tools[owner.options.tools.len] = inputTool(self);
        var options = sessionOptions(owner.options, request.metadata);
        options.models = owner.catalog orelse owner.options.models;
        if (request.reasoning_level) |level| options.thinking_level = try thinkingLevel(level, refusal);
        if (try requestedModel(gpa, options.models, request.metadata, refusal)) |chosen| options.initial_model = chosen;
        const engine = try ownEngine(gpa, owner.options.permission_engine, options.workspace_root);
        errdefer if (engine) |held| {
            held.deinit();
            gpa.destroy(held);
        };
        if (engine) |held| options.permission_engine = held;
        options.tools = session_tools;
        options.tool_approval_ctx = self;
        options.tool_approval_callback = approveTool;
        runtime.* = tui_runtime.TuiRuntime.init(gpa, options) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return refusal.fail(error.BackendFailed, @errorName(err)),
        };
        errdefer runtime.deinit();
        self.* = .{ .owner = owner, .gpa = gpa, .keep = std.heap.ArenaAllocator.init(gpa), .id = "", .participant = "", .runtime = runtime, .engine = engine, .updated_at_ms = owner.now_ms(), .catalog_seen = owner.catalog_generation };
        errdefer self.keep.deinit();
        const keep = self.keep.allocator();
        self.participant = try keep.dupe(u8, request.participant);
        self.id = if (request.session_id.len > 0) try keep.dupe(u8, request.session_id) else try owner.nextID(keep, "session");
        if (request.compaction_policy_json) |raw| try self.applyPolicy(raw, refusal);
        return self;
    }

    fn handle(self: *Session) contract.Session {
        return .{ .ptr = self, .vtable = &vtable };
    }

    const vtable = contract.Session.VTable{
        .id = idOf,
        .state = state,
        .submit = submit,
        .resolve = resolve,
        .cancel = cancel,
        .pump = pump,
        .drain = drain,
        .activity = activity,
        .close = close,
        .tools = tools,
        .models = models,
        .switch_model = switchModel,
        .replay = replay,
        .update_settings = updateSettings,
        .compact = compact,
    };

    fn applyPolicy(self: *Session, raw: []const u8, refusal: *contract.Refusal) contract.Failure!void {
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const at = try compactAt(scratch.allocator(), raw, self.runtime, refusal);
        self.runtime.armAutoCompact(at, self.runtime.run_transcripts.items, self.transcriptWriter()) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return refusal.fail(error.BackendFailed, @errorName(err)),
        };
        self.policy_json = try self.keep.allocator().dupe(u8, raw);
    }

    fn rearm(self: *Session) contract.Failure!void {
        const raw = self.policy_json orelse return;
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        var ignored = contract.Refusal{};
        const at = compactAt(scratch.allocator(), raw, self.runtime, &ignored) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return,
        };
        self.runtime.armAutoCompact(at, self.runtime.run_transcripts.items, self.transcriptWriter()) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => {},
        };
    }

    fn transcriptWriter(self: *Session) ?tui_runtime.TuiRuntime.TranscriptWriter {
        if (self.owner.transcripts == null) return null;
        return .{ .ctx = self, .save_fn = saveTranscript };
    }

    fn saveTranscript(ctx: ?*anyopaque, allocator: std.mem.Allocator, index: usize, history: []const ai_types.Message) ?[]u8 {
        const self: *Session = @ptrCast(@alignCast(ctx.?));
        const store = self.owner.transcripts orelse return null;
        return store.save(store.ctx, allocator, self.id, index, history);
    }

    fn compactionTranscripts(self: *Session, arena: std.mem.Allocator) error{ OutOfMemory, TranscriptSaveFailed }![]const []const u8 {
        const kept = self.runtime.run_transcripts.items;
        const history = self.runtime.history();
        if (self.owner.transcripts == null or history.len == 0 or agent.compaction.isCompacted(history)) return arena.dupe([]const u8, @ptrCast(kept));
        const saved = saveTranscript(self, self.runtime.allocator, kept.len + 1, history) orelse return error.TranscriptSaveFailed;
        self.runtime.run_transcripts.append(self.runtime.allocator, saved) catch |err| {
            self.runtime.allocator.free(saved);
            return err;
        };
        return arena.dupe([]const u8, @ptrCast(self.runtime.run_transcripts.items));
    }

    fn compact(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.SessionCompactRequest, envelope_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const self = cast(ptr);
        switch (request.delivery) {
            .auto, .queue => {},
            .steer, .btw => {
                refusal.* = .{ .feature = if (request.delivery == .steer) "session.message.delivery.steer" else "session.message.delivery.btw", .reason = contract.reason_unadvertised, .detail = "a compaction takes auto or queue delivery" };
                return error.UnsupportedFeature;
            },
        }
        if (request.continue_run) {
            refusal.* = .{ .feature = contract.feature_session_compact, .reason = contract.reason_unsatisfiable, .field = "continue", .detail = "a compaction run ends with the compaction" };
            return error.UnsupportedFeature;
        }
        if (request.session_id.len == 0) return error.InvalidSubmission;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        const busy = self.live() != null or self.queuedCount() > 0 or !self.runtime.isIdle();
        const reservation = busy or request.delivery == .queue;
        if (reservation and self.queuedCount() >= queue_capacity) return error.RunActive;
        const keep = self.keep.allocator();
        const run_id = try self.owner.nextID(keep, "run");
        const submit_id = try keep.dupe(u8, envelope_id);
        const model_id = (try self.currentModelRef(keep)) orelse "";
        const focus = if (request.focus) |text| try keep.dupe(u8, text) else "";
        const run = try keep.create(Run);
        run.* = .{ .id = run_id, .submit_id = submit_id, .model_id = model_id, .compaction = true, .compact_focus = focus, .status = if (reservation) .queued else .running };
        try self.runs.ensureUnusedCapacity(self.gpa, 1);
        if (!reservation) try self.startRun(run, refusal);
        self.runs.appendAssumeCapacity(run);
        self.updated_at_ms = self.owner.now_ms();
        return .{
            .session_id = self.id,
            .accepted = true,
            .submission_id = try self.owner.nextID(arena, "submission"),
            .requested_delivery = request.delivery,
            .effective_delivery = if (reservation) .queue else .start,
            .delivery_resolution = if (busy) "session_busy" else "session_idle",
            .admission = if (reservation) .queued else .started,
            .run_id = run.id,
            .status = run.status,
        };
    }

    fn compactionStarted(self: *Session, a: std.mem.Allocator, run: *Run, reason: []const u8) contract.Failure!void {
        run.compaction_id = try self.owner.nextID(self.keep.allocator(), "compaction");
        var started = Payload.init(a);
        try started.run(self, run);
        try started.put("compaction_id", .{ .string = run.compaction_id });
        try started.put("reason", .{ .string = reason });
        try self.emit(run, "run.compaction.started", started.value(), false);
    }

    fn compactionEnded(self: *Session, a: std.mem.Allocator, run: *Run, payload: anytype) contract.Failure!?std.json.Value {
        if (run.compaction_id.len == 0) return null;
        var ended = Payload.init(a);
        try ended.run(self, run);
        try ended.put("compaction_id", .{ .string = run.compaction_id });
        try ended.put("outcome", .{ .string = @tagName(payload.outcome) });
        var summary: ?std.json.Value = null;
        switch (payload.outcome) {
            .completed => {
                var message = Payload.init(a);
                try message.put("id", .{ .string = try self.owner.nextID(a, "message") });
                try message.put("role", .{ .string = "assistant" });
                try message.put("content", .{ .string = payload.text.slice() });
                summary = message.value();
                try ended.put("summary", summary.?);
                if (payload.tokens_after > 0) try ended.put("history_tokens", .{ .integer = @intCast(payload.tokens_after) });
            },
            .failed => {
                var failure = Payload.init(a);
                try failure.put("code", .{ .string = "compaction_failed" });
                try failure.put("message", .{ .string = if (payload.message.slice().len > 0) payload.message.slice() else "the compaction failed" });
                try failure.put("retriable", .{ .bool = true });
                try ended.put("error", failure.value());
            },
            .cancelled => {},
        }
        try self.emit(run, "run.compaction.ended", ended.value(), false);
        run.compaction_id = "";
        return summary;
    }

    fn settleCompaction(self: *Session, a: std.mem.Allocator, run: *Run, payload: anytype) contract.Failure!void {
        const summary = try self.compactionEnded(a, run, payload);
        var settled = Payload.init(a);
        try settled.run(self, run);
        if (payload.outcome == .cancelled or run.status == .cancelling) {
            try settled.put("reason", .{ .string = "cancel confirmed" });
            return self.emit(run, "run.cancelled", settled.value(), true);
        }
        if (payload.outcome == .failed) {
            var failure = Payload.init(a);
            try failure.put("code", .{ .string = "compaction_failed" });
            try failure.put("message", .{ .string = if (payload.message.slice().len > 0) payload.message.slice() else "the compaction failed" });
            try failure.put("retriable", .{ .bool = true });
            try settled.put("error", failure.value());
            return self.emit(run, "run.failed", settled.value(), true);
        }
        try settled.put("final_response", summary.?);
        try settled.put("stop_reason", .{ .string = "compacted" });
        if (run.model_id.len > 0) try settled.put("model_id", .{ .string = run.model_id });
        return self.emit(run, "run.completed", settled.value(), true);
    }

    fn updateSettings(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.SessionSettingsUpdateRequest, refusal: *contract.Refusal) contract.Failure!contract.Updated {
        const self = cast(ptr);
        try contract.refuseUnadvertisedLiveSettings(descriptor, request, refusal);
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        if (request.reasoning_level == null and request.compaction_policy_json == null) return error.InvalidSubmission;
        const level: ?ai_types.ThinkingLevel = if (request.reasoning_level) |asked| try thinkingLevel(asked, refusal) else null;
        const extended = if (request.extensions_json) |raw| try parseLiveSettings(arena, raw, refusal) else LiveSettings{};
        if (request.compaction_policy_json) |raw| _ = try compactAt(arena, raw, self.runtime, refusal);
        if (self.live() != null or self.queuedCount() > 0 or !self.runtime.isIdle()) return error.RunActive;
        try checkLiveSettings(self.runtime, extended, refusal);
        var response = oap_types.SessionSettingsUpdateResponse{ .session_id = self.id };
        if (level) |chosen| {
            response.previous_reasoning_level = @tagName(self.runtime.thinkingLevel());
            self.runtime.setThinkingLevel(chosen) catch return refusal.fail(error.BackendFailed, "the agent loop's thinking level is fixed");
            response.reasoning_level = @tagName(chosen);
        }
        if (request.compaction_policy_json) |raw| {
            response.previous_compaction_policy_json = self.policy_json;
            try self.applyPolicy(raw, refusal);
            response.compaction_policy_json = self.policy_json;
        }
        try applyLiveSettings(self.runtime, extended, refusal);
        self.updated_at_ms = self.owner.now_ms();
        return .{ .response = response, .state = try self.snapshot(arena) };
    }

    fn syncCatalog(self: *Session) contract.Failure!void {
        const catalog = self.owner.catalog orelse return;
        if (self.catalog_seen == self.owner.catalog_generation) return;
        if (self.live() != null or !self.runtime.isIdle()) return error.RunActive;
        self.runtime.replaceModels(catalog, self.runtime.currentModel()) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return,
        };
        self.catalog_seen = self.owner.catalog_generation;
    }

    fn cast(ptr: *anyopaque) *Session {
        return @ptrCast(@alignCast(ptr));
    }

    fn idOf(ptr: *anyopaque) []const u8 {
        return cast(ptr).id;
    }

    fn live(self: *Session) ?*Run {
        const run = self.run orelse return null;
        return if (run.terminal) null else run;
    }

    fn destroy(self: *Session) void {
        const gpa = self.gpa;
        for (self.runs.items) |run| releaseRun(gpa, run);
        self.runs.deinit(gpa);
        for (self.outbox.items) |entry| gpa.free(entry.line);
        self.outbox.deinit(gpa);
        self.forgetJournal();
        self.journal.deinit(gpa);
        self.runtime.deinit();
        gpa.destroy(self.runtime);
        if (self.engine) |engine| {
            engine.deinit();
            gpa.destroy(engine);
        }
        self.keep.deinit();
        gpa.destroy(self);
    }

    fn evictOldestHalf(self: *Session) void {
        const items = self.journal.items;
        const drop = items.len / 2;
        for (items[0..drop]) |entry| self.gpa.free(entry.line);
        std.mem.copyForwards(Journaled, items[0 .. items.len - drop], items[drop..]);
        self.journal.shrinkRetainingCapacity(items.len - drop);
    }

    fn forgetJournal(self: *Session) void {
        for (self.journal.items) |entry| self.gpa.free(entry.line);
        self.journal.clearRetainingCapacity();
    }

    fn releaseRun(gpa: std.mem.Allocator, run: *Run) void {
        if (run.input_text.len > 0) gpa.free(run.input_text);
        run.input_text = "";
        run.text.deinit(gpa);
        run.error_text.deinit(gpa);
        run.text = .empty;
        run.error_text = .empty;
    }

    fn currentModelRef(self: *Session, allocator: std.mem.Allocator) !?[]const u8 {
        const model = self.runtime.currentModel() orelse return null;
        return refFor(allocator, model) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => null,
        };
    }

    fn state(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        _ = refusal;
        return cast(ptr).snapshot(arena);
    }

    fn findRun(self: *Session, id: []const u8) ?*Run {
        if (self.run) |run| if (std.mem.eql(u8, run.id, id)) return run;
        for (self.runs.items) |run| if (std.mem.eql(u8, run.id, id)) return run;
        return null;
    }

    fn queuedCount(self: *Session) usize {
        var count: usize = 0;
        for (self.runs.items) |run| if (!run.started and !run.terminal) {
            count += 1;
        };
        return count;
    }

    fn snapshot(self: *Session, arena: std.mem.Allocator) contract.Failure!oap_types.SessionState {
        var entries: std.ArrayList(oap_types.ActiveRun) = .empty;
        var admitted: std.ArrayList([]const u8) = .empty;
        var settled: std.ArrayList(oap_types.RunPosition) = .empty;
        var position: u64 = 0;
        for (self.runs.items) |run| {
            if (!listsId(admitted.items, run.submit_id)) try admitted.append(arena, run.submit_id);
            if (run.terminal) {
                try settled.append(arena, .{ .run_id = run.id, .sequence = run.next_sequence - 1 });
                continue;
            }
            if (!run.started) position += 1;
            const pending: []const []const u8 = if (run.started and self.pending != null) try arena.dupe([]const u8, &.{self.pending.?.id}) else &.{};
            const anchors = try arena.alloc([]const u8, 1 + run.admitted_steers.items.len);
            anchors[0] = run.submit_id;
            @memcpy(anchors[1..], run.admitted_steers.items);
            const steers = try arena.alloc(oap_types.PendingSteer, run.steers.items.len);
            for (run.steers.items, steers) |pending_steer, *slot| slot.* = .{ .submission_id = pending_steer.submission_id, .request_id = pending_steer.request_id, .message_ids = pending_steer.message_ids };
            try entries.append(arena, .{
                .run_id = run.id,
                .status = run.status,
                .relationship = "primary",
                .queue_position = if (run.started) null else position,
                .as_of_sequence = run.next_sequence - 1,
                .admitted_submit_requests = anchors,
                .pending_interactions = pending,
                .pending_steers = steers,
            });
        }
        var result = oap_types.SessionState{
            .session_id = self.id,
            .status = if (entries.items.len > 0) .queued else .idle,
            .active_runs = entries.items,
            .current_model_id = try self.currentModelRef(arena),
            .updated_at_ms = self.updated_at_ms,
            .reasoning_level = @tagName(self.runtime.thinkingLevel()),
            .compaction_policy_json = self.policy_json,
            .as_of = .{ .admitted_submit_requests = admitted.items, .settled = settled.items },
        };
        if (self.live()) |run| {
            result.active_run_id = run.id;
            result.status = if (self.pending != null) .waiting_for_input else .running;
        }
        return result;
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, envelope_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const self = cast(ptr);
        try contract.refuseUnadvertisedControls(descriptor, request, refusal);
        if (request.session_id.len == 0 or request.messages.len == 0) return error.InvalidSubmission;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        if (request.delivery == .steer) return self.steer(arena, request, envelope_id, refusal);
        const busy = self.live() != null or self.queuedCount() > 0;
        const reservation = busy or request.delivery == .queue;
        if (reservation and self.queuedCount() >= queue_capacity) return error.RunActive;
        const text = try userText(arena, request.messages);
        if (text.len == 0) return refusal.fail(error.InvalidSubmission, "the submission carries no user text");
        const keep = self.keep.allocator();
        const run_id = try self.owner.nextID(keep, "run");
        const input_text = try self.gpa.dupe(u8, text);
        errdefer self.gpa.free(input_text);
        const submit_id = try keep.dupe(u8, envelope_id);
        const model_id = (try self.currentModelRef(keep)) orelse "";
        const run = try keep.create(Run);
        run.* = .{ .id = run_id, .input_text = input_text, .submit_id = submit_id, .model_id = model_id, .status = if (reservation) .queued else .running };
        try self.runs.ensureUnusedCapacity(self.gpa, 1);
        const message_ids = try arena.alloc([]const u8, request.messages.len);
        for (request.messages, message_ids) |message, *slot| slot.* = if (message.id) |carried| carried else try self.owner.nextID(arena, "message");
        const response = oap_types.MessageSubmitResponse{
            .session_id = self.id,
            .accepted = true,
            .submission_id = try self.owner.nextID(arena, "submission"),
            .requested_delivery = request.delivery,
            .effective_delivery = if (reservation) .queue else .start,
            .delivery_resolution = if (busy) "session_busy" else "session_idle",
            .admission = if (reservation) .queued else .started,
            .run_id = run.id,
            .status = run.status,
            .model_id = if (model_id.len > 0) model_id else null,
            .message_ids = message_ids,
        };
        if (!reservation) try self.startRun(run, refusal);
        self.runs.appendAssumeCapacity(run);
        self.updated_at_ms = self.owner.now_ms();
        return response;
    }

    fn steer(self: *Session, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, envelope_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const named = request.target_run_id orelse "";
        const reason = self.steerRefusal(named);
        if (reason.len > 0) {
            refusal.* = .{
                .reason = reason,
                .message = try std.fmt.allocPrint(arena, "adapter: steer target cannot take guidance: run \"{s}\" is {s}", .{ named, reason }),
            };
            return error.InvalidSteerTarget;
        }
        const run = self.live().?;
        const text = try userText(arena, request.messages);
        if (text.len == 0) return refusal.fail(error.InvalidSubmission, "the submission carries no user text");
        const keep = self.keep.allocator();
        const message_ids = try arena.alloc([]const u8, request.messages.len);
        const kept_ids = try keep.alloc([]const u8, request.messages.len);
        for (request.messages, message_ids, kept_ids) |message, *slot, *kept| {
            slot.* = if (message.id) |carried| carried else try self.owner.nextID(arena, "message");
            kept.* = try keep.dupe(u8, slot.*);
        }
        const submission_id = try self.owner.nextID(arena, "submission");
        const pending = PendingSteer{
            .submission_id = try keep.dupe(u8, submission_id),
            .request_id = try keep.dupe(u8, envelope_id),
            .message_ids = kept_ids,
        };
        try run.steers.ensureUnusedCapacity(keep, 1);
        try run.admitted_steers.ensureUnusedCapacity(keep, 1);
        self.runtime.queueSteer(text) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return refusal.fail(error.BackendFailed, @errorName(err)),
        };
        run.steers.appendAssumeCapacity(pending);
        run.admitted_steers.appendAssumeCapacity(pending.request_id);
        self.updated_at_ms = self.owner.now_ms();
        return .{
            .session_id = self.id,
            .accepted = true,
            .submission_id = submission_id,
            .requested_delivery = .steer,
            .effective_delivery = .steer,
            .admission = .steered,
            .run_id = run.id,
            .status = .running,
            .message_ids = message_ids,
            .target_sequence = run.next_sequence - 1,
        };
    }

    fn steerRefusal(self: *Session, named: []const u8) []const u8 {
        const live_run = self.live();
        if (named.len > 0) {
            const run = self.findRun(named) orelse return "unknown_target";
            if (run.terminal) return "terminal";
            if (!run.started) return "queued";
            if (live_run != run or run.status == .cancelling or run.compaction) return "not_steerable";
            return "";
        }
        const run = live_run orelse return "no_active_run";
        if (run.status == .cancelling or run.compaction) return "not_steerable";
        return "";
    }

    fn applySteer(self: *Session, a: std.mem.Allocator, run: *Run) contract.Failure!void {
        if (run.steers.items.len == 0) return;
        const pending = run.steers.orderedRemove(0);
        var applied = Payload.init(a);
        try applied.run(self, run);
        try applied.put("submission_id", .{ .string = pending.submission_id });
        try applied.put("request_id", .{ .string = pending.request_id });
        var ids = std.json.Array.init(a);
        for (pending.message_ids) |id| try ids.append(.{ .string = id });
        try applied.put("message_ids", .{ .array = ids });
        try applied.put("boundary", .{ .string = if (run.after_tool) "tool_result" else "turn" });
        try self.emit(run, "run.steer.applied", applied.value(), false);
    }

    fn dropSteers(self: *Session, a: std.mem.Allocator, run: *Run) contract.Failure!void {
        self.runtime.clearSteers();
        for (run.steers.items) |pending| {
            var dropped = Payload.init(a);
            try dropped.run(self, run);
            try dropped.put("submission_id", .{ .string = pending.submission_id });
            try dropped.put("request_id", .{ .string = pending.request_id });
            var reason = Payload.init(a);
            try reason.put("code", .{ .string = "run_terminated" });
            try reason.put("message", .{ .string = "the run ended before the guidance was applied" });
            try dropped.put("reason", reason.value());
            try self.emit(run, "run.steer.dropped", dropped.value(), false);
        }
        run.steers.clearRetainingCapacity();
    }

    fn startRun(self: *Session, run: *Run, refusal: *contract.Refusal) contract.Failure!void {
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        var started = Payload.init(a);
        try started.run(self, run);
        try started.put("status", .{ .string = "running" });
        if (run.model_id.len > 0) try started.put("model_id", .{ .string = run.model_id });
        try started.put("started_at_ms", .{ .integer = self.owner.now_ms() });
        const prepared = try self.prepareEvent(run, "run.started", started.value(), false);
        errdefer {
            self.gpa.free(prepared.line);
            self.gpa.free(prepared.kept);
        }
        self.gate.cancelled.store(false, .release);
        if (run.compaction) {
            const transcripts = self.compactionTranscripts(a) catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                error.TranscriptSaveFailed => return refusal.fail(error.BackendFailed, "the transcript could not be saved, so the history was kept"),
            };
            self.runtime.compact(.{ .focus = run.compact_focus, .transcripts = transcripts }) catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                error.NothingToCompact => return refusal.fail(error.InvalidSubmission, "the session has no history to compact"),
                else => return refusal.fail(error.BackendFailed, @errorName(err)),
            };
        } else {
            try self.rearm();
            self.runtime.submitTurn(run.input_text) catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                else => return refusal.fail(error.BackendFailed, @errorName(err)),
            };
        }
        self.gpa.free(run.input_text);
        run.input_text = "";
        self.run = run;
        run.started = true;
        run.status = .running;
        self.publishEvent(run, prepared, "run.started", false);
        if (run.compaction) try self.compactionStarted(a, run, "requested");
    }

    fn promote(self: *Session) contract.Failure!bool {
        if (self.live() != null or !self.runtime.isIdle()) return false;
        for (self.runs.items) |run| {
            if (run.terminal or run.started) continue;
            var refusal = contract.Refusal{};
            self.startRun(run, &refusal) catch |err| {
                if (err == error.OutOfMemory) return err;
                var scratch = std.heap.ArenaAllocator.init(self.gpa);
                defer scratch.deinit();
                const a = scratch.allocator();
                var payload = Payload.init(a);
                try payload.run(self, run);
                var failure = Payload.init(a);
                try failure.put("code", .{ .string = "provider_error" });
                try failure.put("message", .{ .string = if (refusal.detail.len > 0) refusal.detail else @errorName(err) });
                try failure.put("retriable", .{ .bool = false });
                try payload.put("error", failure.value());
                try self.emit(run, "run.failed", payload.value(), true);
            };
            return true;
        }
        return false;
    }

    fn resolve(ptr: *anyopaque, arena: std.mem.Allocator, resolution: contract.Resolution, refusal: *contract.Refusal) contract.Failure!void {
        const self = cast(ptr);
        const run = self.live() orelse return error.InteractionNotFound;
        const pending = self.pending orelse return error.InteractionNotFound;
        const id, const session_id, const run_id, const requested_by, const responded_by = switch (resolution) {
            .permission => |answer| .{ answer.interaction_id, answer.session_id, answer.run_id, answer.requested_by, answer.responded_by },
            .input => |answer| .{ answer.interaction_id, answer.session_id, answer.run_id, answer.requested_by, answer.responded_by },
        };
        if (!std.mem.eql(u8, id, pending.id) or !std.mem.eql(u8, session_id, self.id) or !std.mem.eql(u8, run_id, run.id) or
            !std.mem.eql(u8, requested_by, endpoint_id) or !std.mem.eql(u8, responded_by, self.participant)) return error.InvalidResolution;
        if (run.status == .cancelling) return error.InteractionNotFound;
        var resolved = try self.interactionPayload(arena, run, pending);
        const response: []const u8 = switch (resolution) {
            .permission => |answer| answer: {
                if (pending.kind != .permission) return error.InvalidResolution;
                if (answer.updated_arguments_json != null) return refusal.unsupportedField("action.permissions", contract.reason_unsatisfiable, "updated_arguments_json");
                const choice = answer.choice_id orelse return error.InvalidResolution;
                const granted = if (std.mem.eql(u8, choice, "approve") or std.mem.eql(u8, choice, "approve_always")) true else if (std.mem.eql(u8, choice, "deny") or std.mem.eql(u8, choice, "reject_always")) false else return error.InvalidResolution;
                if (granted != answer.granted) return error.InvalidResolution;
                try resolved.put("outcome", .{ .string = "resolved" });
                try resolved.put("choice_id", .{ .string = choice });
                try resolved.put("granted", .{ .bool = granted });
                break :answer try self.gpa.dupe(u8, choice);
            },
            .input => |answer| answer: {
                if (pending.kind != .input) return error.InvalidResolution;
                const prompt = try parseValue(arena, pending.arguments);
                const questions = prompt.object.get("questions").?.array.items;
                if (answer.answers.len == 0 or answer.answers.len > questions.len) return error.InvalidResolution;
                for (questions) |question| {
                    if (question.object.get("required")) |required| {
                        if (!required.bool) continue;
                    }
                    var covered = false;
                    for (answer.answers) |value| {
                        if (std.mem.eql(u8, question.object.get("id").?.string, value.question_id)) covered = true;
                    }
                    if (!covered) return error.InvalidResolution;
                }
                var listed = std.json.Array.init(arena);
                for (answer.answers, 0..) |value, index| {
                    for (answer.answers[0..index]) |earlier| {
                        if (std.mem.eql(u8, earlier.question_id, value.question_id)) return error.InvalidResolution;
                    }
                    var found = false;
                    for (questions) |question| {
                        if (!std.mem.eql(u8, question.object.get("id").?.string, value.question_id)) continue;
                        const kind = std.meta.stringToEnum(contract.QuestionKind, question.object.get("kind").?.string) orelse return error.InvalidResolution;
                        var options: std.ArrayList([]const u8) = .empty;
                        if (question.object.get("options")) |offered| {
                            for (offered.array.items) |option| try options.append(arena, option.object.get("id").?.string);
                        }
                        if (!contract.validInputAnswer(.{ .id = value.question_id, .kind = kind, .options = options.items }, value)) return error.InvalidResolution;
                        found = true;
                    }
                    if (!found) return error.InvalidResolution;
                    var entry = Payload.init(arena);
                    try entry.put("question_id", .{ .string = value.question_id });
                    if (value.text) |text| try entry.put("text", .{ .string = text });
                    var selected = std.json.Array.init(arena);
                    for (value.selected_option_ids) |option| try selected.append(.{ .string = option });
                    if (value.text == null) try entry.put("selected_option_ids", .{ .array = selected });
                    try listed.append(entry.value());
                }
                try resolved.put("status", .{ .string = "submitted" });
                try resolved.put("answers", .{ .array = listed });
                break :answer try json_encode.valueAlloc(self.gpa, .{ .array = listed });
            },
        };
        errdefer self.gpa.free(response);
        self.gate.lock();
        defer self.gate.mutex.unlock();
        const native = self.gate.request orelse return error.InteractionNotFound;
        if (native.response != null or self.gate.cancelled.load(.acquire)) return error.InteractionNotFound;
        try self.emit(run, if (pending.kind == .permission) "action.permission.resolved" else "user.input.resolved", resolved.value(), false);
        native.response = response;
        self.pending = null;
        run.status = .running;
    }

    fn interactionPayload(self: *Session, a: std.mem.Allocator, run: *Run, pending: PendingInteraction) !Payload {
        var payload = Payload.init(a);
        try payload.run(self, run);
        try payload.put("interaction_id", .{ .string = pending.id });
        try payload.put("requested_by", .{ .string = endpoint_id });
        try payload.put("responded_by", .{ .string = self.participant });
        if (pending.kind == .permission) try payload.put("tool_call_id", .{ .string = pending.tool_call_id });
        return payload;
    }

    fn pumpInteraction(self: *Session) contract.Failure!bool {
        const run = self.live() orelse return false;
        if (run.status == .cancelling) return false;
        self.gate.lock();
        defer self.gate.mutex.unlock();
        const native = self.gate.request orelse return false;
        if (native.published) return false;
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        const keep = self.keep.allocator();
        const interaction_id = try self.owner.nextID(keep, "interaction");
        const tool_call_id = try keep.dupe(u8, native.tool_call_id);
        const arguments = try keep.dupe(u8, native.arguments);
        const pending = PendingInteraction{
            .id = interaction_id,
            .kind = native.kind,
            .tool_call_id = tool_call_id,
            .arguments = arguments,
        };
        var payload = try self.interactionPayload(a, run, pending);
        if (native.kind == .permission) {
            try payload.put("title", .{ .string = native.tool_name });
            try payload.put("arguments_json", try jsonOrString(a, native.arguments));
            try payload.put("choices", try parseValue(a, "[{\"id\":\"approve\",\"label\":\"Allow once\"},{\"id\":\"approve_always\",\"label\":\"Always allow\"},{\"id\":\"deny\",\"label\":\"Deny\"},{\"id\":\"reject_always\",\"label\":\"Always deny\"}]"));
        } else {
            const prompt = try parseValue(a, native.arguments);
            try payload.put("title", prompt.object.get("title") orelse .{ .string = "User input" });
            const questions = prompt.object.get("questions").?;
            for (questions.array.items) |*question| {
                if (!question.object.contains("required")) try question.object.put(a, "required", .{ .bool = true });
            }
            try payload.put("questions", questions);
            try payload.put("allow_cancel", .{ .bool = false });
        }
        try self.emit(run, if (native.kind == .permission) "action.permission.requested" else "user.input.requested", payload.value(), false);
        native.published = true;
        self.pending = pending;
        run.status = .waiting_for_input;
        return true;
    }

    fn approveTool(ctx: ?*anyopaque, request: tui_session.ToolApprovalRequest) tui_session.ToolApprovalDecision {
        const self: *Session = @ptrCast(@alignCast(ctx.?));
        if (std.mem.eql(u8, request.tool_name, "request_user_input")) return .approve;
        const answer = self.gate.wait(self.gpa, .permission, request.tool_call_id, request.tool_name, request.args_json, null) catch return .reject;
        defer self.gpa.free(answer);
        return decisionFor(answer);
    }

    fn inputTool(self: *Session) agent.AgentTool {
        return .{
            .label = "Ask user",
            .name = "request_user_input",
            .description = "Ask the user for information needed to continue.",
            .parameters_schema_json = "{\"type\":\"object\",\"required\":[\"questions\"],\"properties\":{\"title\":{\"type\":\"string\"},\"questions\":{\"type\":\"array\",\"minItems\":1,\"items\":{\"type\":\"object\",\"required\":[\"id\",\"prompt\",\"kind\"],\"properties\":{\"id\":{\"type\":\"string\",\"minLength\":1},\"prompt\":{\"type\":\"string\"},\"kind\":{\"enum\":[\"text\",\"single_choice\",\"multi_choice\"]},\"required\":{\"type\":\"boolean\"},\"options\":{\"type\":\"array\",\"items\":{\"type\":\"object\",\"required\":[\"id\",\"label\"],\"properties\":{\"id\":{\"type\":\"string\"},\"label\":{\"type\":\"string\"}}}}}}}}}",
            .execute = unavailableInput,
            .runtime_ctx = self,
            .runtime_execute = executeInput,
        };
    }

    fn unavailableInput(tool_call_id: []const u8, args: []const u8, token: ?ai_types.CancelToken, update_ctx: ?*anyopaque, update: ?agent.ToolUpdateCallback, allocator: std.mem.Allocator) anyerror!agent.AgentToolResult {
        return executeInput(null, tool_call_id, args, token, update_ctx, update, allocator);
    }

    fn executeInput(ctx: ?*anyopaque, tool_call_id: []const u8, args: []const u8, token: ?ai_types.CancelToken, update_ctx: ?*anyopaque, update: ?agent.ToolUpdateCallback, allocator: std.mem.Allocator) anyerror!agent.AgentToolResult {
        _ = update_ctx;
        _ = update;
        const self: *Session = @ptrCast(@alignCast(ctx orelse return error.InputUnavailable));
        try validatePrompt(allocator, args);
        const answer = try self.gate.wait(self.gpa, .input, tool_call_id, "request_user_input", args, token);
        defer self.gpa.free(answer);
        const text = try allocator.dupe(u8, answer);
        errdefer allocator.free(text);
        const content = try allocator.alloc(ai_types.UserContentPart, 1);
        content[0] = .{ .text = .{ .text = text } };
        return .{ .content = @FieldType(agent.AgentToolResult, "content").initOwned(content) };
    }

    fn cancel(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
        _ = refusal;
        const self = cast(ptr);
        const run = self.findRun(run_id) orelse return error.RunNotFound;
        const owned_run_id = try arena.dupe(u8, run.id);
        if (run.terminal) {
            if (run.status == .cancelled) return .{ .session_id = self.id, .run_id = owned_run_id, .accepted = true, .status = .cancelled };
            return error.RunTerminal;
        }
        if (run.status == .cancelling) return .{ .session_id = self.id, .run_id = owned_run_id, .accepted = true, .status = .cancelling };
        if (!run.started) {
            var scratch = std.heap.ArenaAllocator.init(self.gpa);
            defer scratch.deinit();
            var payload = Payload.init(scratch.allocator());
            try payload.run(self, run);
            try payload.put("reason", .{ .string = "cancelled before promotion" });
            try self.emit(run, "run.cancelled", payload.value(), true);
            return .{ .session_id = self.id, .run_id = owned_run_id, .accepted = true, .status = .cancelling };
        }
        run.status = .cancelling;
        self.gate.cancelled.store(true, .release);
        self.runtime.cancel();

        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        var status = Payload.init(a);
        try status.run(self, run);
        try status.put("status", .{ .string = "cancelling" });
        try status.put("updated_at_ms", .{ .integer = self.owner.now_ms() });
        try self.emit(run, "run.status.updated", status.value(), false);
        return .{ .session_id = self.id, .run_id = owned_run_id, .accepted = true, .status = .cancelling };
    }

    fn pump(ptr: *anyopaque, wait_ns: u64) contract.Failure!bool {
        _ = wait_ns;
        const self = cast(ptr);
        var moved = try self.promote();
        const stream = self.runtime.streamEvents();
        while (stream.poll()) |event| {
            var owned = event;
            defer owned.deinit(self.gpa);
            if (self.owner.recorder) |recorder| recorder.record(recorder.ctx, self.id, &owned);
            try self.translate(owned);
            moved = true;
        }
        return try self.pumpInteraction() or moved;
    }

    fn translate(self: *Session, event: tui_session.TuiEvent) contract.Failure!void {
        const run = self.live() orelse return;
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        switch (event) {
            .message_start => |payload| {
                if (payload.role != .assistant) return;
                run.message_id = try self.owner.nextID(self.keep.allocator(), "message");
                run.text.clearRetainingCapacity();
                run.after_tool = false;
            },
            .text_delta => |payload| {
                try run.text.appendSlice(self.gpa, payload.delta.slice());
                try self.emitPart(a, run, "text", "text", payload.delta.slice());
            },
            .thinking_delta => |payload| try self.emitPart(a, run, "reasoning", "reasoning", payload.delta.slice()),
            .tool_execution_start => |payload| {
                var requested = try self.callPayload(a, run, payload.tool_call_id.slice(), payload.tool_name.slice());
                try requested.put("requested_by", .{ .string = endpoint_id });
                try requested.put("arguments_json", try jsonOrString(a, payload.args_json.slice()));
                try self.emit(run, "action.call.requested", requested.value(), false);
                var started = try self.callPayload(a, run, payload.tool_call_id.slice(), payload.tool_name.slice());
                try self.emit(run, "action.call.started", started.value(), false);
            },
            .tool_execution_end => |payload| {
                run.after_tool = true;
                var ended = try self.callPayload(a, run, payload.tool_call_id.slice(), payload.tool_name.slice());
                if (payload.is_error) {
                    var failure = Payload.init(a);
                    try failure.put("code", .{ .string = "tool_failed" });
                    const text = payload.result_text.slice();
                    try failure.put("message", .{ .string = if (text.len > 0) text else try errorText(a, payload.result_json.slice()) });
                    const details = try jsonOrString(a, payload.result_json.slice());
                    if (details == .object and details.object.count() > 0) try failure.put("details", details);
                    try ended.put("error", failure.value());
                    try self.emit(run, "action.call.failed", ended.value(), false);
                } else {
                    try ended.put("result", try jsonOrString(a, payload.result_json.slice()));
                    try self.emit(run, "action.call.completed", ended.value(), false);
                }
            },
            .turn_end => |payload| run.stop_reason = stopReasonText(payload.stop_reason),
            .message_end => |payload| {
                if (payload.role == .assistant) run.output_tokens += payload.output_tokens;
                if (payload.role == .user and payload.steering) try self.applySteer(a, run);
            },
            .context_usage => |payload| run.context_tokens = payload.estimated_tokens,
            .@"error" => |payload| {
                run.error_text.clearRetainingCapacity();
                try run.error_text.appendSlice(self.gpa, payload.message.slice());
            },
            .agent_end => |payload| try self.settle(a, run, payload.reason),
            .compaction_start => if (!run.compaction) try self.compactionStarted(a, run, "threshold"),
            .compaction_end => |payload| if (run.compaction and !payload.in_run) try self.settleCompaction(a, run, payload) else {
                _ = try self.compactionEnded(a, run, payload);
            },
            else => {},
        }
    }

    fn emitPart(self: *Session, a: std.mem.Allocator, run: *Run, kind: []const u8, field: []const u8, text: []const u8) contract.Failure!void {
        if (text.len == 0) return;
        if (run.message_id.len == 0) run.message_id = try self.owner.nextID(self.keep.allocator(), "message");
        var delta = Payload.init(a);
        try delta.run(self, run);
        try delta.put("message_id", .{ .string = run.message_id });
        var part = Payload.init(a);
        try part.put("type", .{ .string = kind });
        try part.put(field, .{ .string = text });
        try delta.put("part", part.value());
        try self.emit(run, "content.delta", delta.value(), false);
    }

    fn callPayload(self: *Session, a: std.mem.Allocator, run: *Run, tool_call_id: []const u8, name: []const u8) !Payload {
        var call = Payload.init(a);
        try call.run(self, run);
        try call.put("tool_call_id", .{ .string = tool_call_id });
        try call.put("execution_owner", .{ .string = endpoint_id });
        try call.put("name", .{ .string = name });
        return call;
    }

    fn settle(self: *Session, a: std.mem.Allocator, run: *Run, reason: tui_session.TuiEndReason) contract.Failure!void {
        if (self.pending) |pending| {
            var resolved = try self.interactionPayload(a, run, pending);
            if (pending.kind == .permission) {
                try resolved.put("outcome", .{ .string = "cancelled" });
            } else {
                try resolved.put("status", .{ .string = "cancelled" });
            }
            try self.emit(run, if (pending.kind == .permission) "action.permission.resolved" else "user.input.resolved", resolved.value(), false);
            self.pending = null;
        }
        try self.dropSteers(a, run);
        var payload = Payload.init(a);
        try payload.run(self, run);
        if (run.output_tokens > 0) {
            var usage = Payload.init(a);
            try usage.put("output_tokens", .{ .integer = @intCast(run.output_tokens) });
            try payload.put("usage", usage.value());
        }
        const cancelled = reason == .cancelled or run.status == .cancelling;
        if (cancelled) {
            try payload.put("reason", .{ .string = "cancel confirmed" });
            return self.emit(run, "run.cancelled", payload.value(), true);
        }
        if (reason == .@"error") {
            var failure = Payload.init(a);
            try failure.put("code", .{ .string = "provider_error" });
            try failure.put("message", .{ .string = if (run.error_text.items.len > 0) run.error_text.items else "the agent loop ended in an error" });
            try failure.put("retriable", .{ .bool = false });
            try payload.put("error", failure.value());
            return self.emit(run, "run.failed", payload.value(), true);
        }
        var final = Payload.init(a);
        try final.put("role", .{ .string = "assistant" });
        try final.put("content", .{ .string = run.text.items });
        try payload.put("final_response", final.value());
        try payload.put("stop_reason", .{ .string = run.stop_reason });
        if (run.model_id.len > 0) try payload.put("model_id", .{ .string = run.model_id });
        return self.emit(run, "run.completed", payload.value(), true);
    }

    fn emit(self: *Session, run: *Run, kind: []const u8, payload: std.json.Value, terminal: bool) contract.Failure!void {
        if (run.terminal) return;
        self.publishEvent(run, try self.prepareEvent(run, kind, payload, terminal), kind, terminal);
    }

    fn prepareEvent(self: *Session, run: *Run, kind: []const u8, payload: std.json.Value, terminal: bool) contract.Failure!PreparedEvent {
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        const sequence = run.next_sequence;
        const now = self.owner.now_ms();
        var envelope = Payload.init(a);
        try envelope.put("protocol", .{ .string = protocol_name });
        try envelope.put("version", .{ .string = protocol_version });
        try envelope.put("profile", .{ .string = profile });
        try envelope.put("type", .{ .string = kind });
        try envelope.put("id", .{ .string = try self.owner.nextID(a, "event") });
        try envelope.put("payload", payload);
        try envelope.put("sequence", .{ .integer = @intCast(sequence) });
        try envelope.put("timestamp_ms", .{ .integer = now });
        try envelope.put("session_id", .{ .string = self.id });
        try envelope.put("run_id", .{ .string = run.id });
        if (std.mem.startsWith(u8, kind, "action.call.") or std.mem.startsWith(u8, kind, "action.permission.") or std.mem.startsWith(u8, kind, "user.input.")) {
            if (payload.object.get("tool_call_id")) |tool_call_id| try envelope.put("tool_call_id", tool_call_id);
        }
        try envelope.put("capability_revision", .{ .string = capability_revision });
        if (terminal and run.context_tokens > 0) {
            var context = Payload.init(a);
            try context.put("context_tokens", .{ .integer = @intCast(run.context_tokens) });
            var extensions = Payload.init(a);
            try extensions.put(settings_key, context.value());
            try envelope.put("extensions", extensions.value());
        }
        const line = try json_encode.valueAlloc(self.gpa, envelope.value());
        errdefer self.gpa.free(line);
        const kept = try self.gpa.dupe(u8, line);
        errdefer self.gpa.free(kept);
        try self.outbox.ensureUnusedCapacity(self.gpa, 1);
        try self.journal.ensureUnusedCapacity(self.gpa, 1);
        return .{ .line = line, .kept = kept };
    }

    fn publishEvent(self: *Session, run: *Run, event: PreparedEvent, kind: []const u8, terminal: bool) void {
        const sequence = run.next_sequence;
        self.outbox.appendAssumeCapacity(.{ .line = event.line, .run_id = run.id, .sequence = sequence });
        if (self.journal.items.len == journal_capacity) self.evictOldestHalf();
        self.journal.appendAssumeCapacity(.{ .line = event.kept, .run_id = run.id, .sequence = sequence });
        run.next_sequence += 1;
        self.updated_at_ms = self.owner.now_ms();
        if (!terminal) return;
        run.terminal = true;
        if (std.mem.eql(u8, kind, "run.completed")) run.status = .completed;
        if (std.mem.eql(u8, kind, "run.failed")) run.status = .failed;
        if (std.mem.eql(u8, kind, "run.cancelled")) run.status = .cancelled;
        releaseRun(self.gpa, run);
    }

    fn drain(ptr: *anyopaque, allocator: std.mem.Allocator, out: *std.ArrayList(contract.Event)) contract.Failure!void {
        const self = cast(ptr);
        try out.ensureUnusedCapacity(allocator, self.outbox.items.len);
        for (self.outbox.items) |queued| {
            const line = try allocator.dupe(u8, queued.line);
            errdefer allocator.free(line);
            const run_id = try allocator.dupe(u8, queued.run_id);
            out.appendAssumeCapacity(.{ .line = line, .run_id = run_id, .sequence = queued.sequence });
        }
        for (self.outbox.items) |queued| self.gpa.free(queued.line);
        self.outbox.clearRetainingCapacity();
    }

    fn replay(ptr: *anyopaque, allocator: std.mem.Allocator, run_id: []const u8, after: u64, refusal: *contract.Refusal) contract.Failure!contract.Replay {
        _ = refusal;
        const self = cast(ptr);
        const run = self.findRun(run_id) orelse return error.RunNotFound;
        const latest = run.next_sequence - 1;
        if (after > latest) return error.ReplayCursorFuture;
        var oldest: u64 = 0;
        for (self.journal.items) |event| {
            if (std.mem.eql(u8, event.run_id, run_id)) {
                oldest = event.sequence;
                break;
            }
        }
        if (after < latest and (oldest == 0 or after + 1 < oldest)) {
            return .{ .gap = .{ .requested_after = after, .oldest_available = oldest, .latest_available = latest } };
        }
        var suffix = std.ArrayList(contract.Event).empty;
        errdefer {
            for (suffix.items) |event| {
                allocator.free(event.line);
                allocator.free(event.run_id);
            }
            suffix.deinit(allocator);
        }
        for (self.journal.items) |kept| {
            if (!std.mem.eql(u8, kept.run_id, run_id) or kept.sequence <= after) continue;
            const line = try allocator.dupe(u8, kept.line);
            errdefer allocator.free(line);
            const owned_run_id = try allocator.dupe(u8, kept.run_id);
            errdefer allocator.free(owned_run_id);
            try suffix.append(allocator, .{ .line = line, .run_id = owned_run_id, .sequence = kept.sequence });
        }
        return .{ .events = try suffix.toOwnedSlice(allocator) };
    }

    fn activity(ptr: *anyopaque) contract.Activity {
        const self = cast(ptr);
        return if (self.live() != null) (if (self.pending != null) .waiting else .running) else if (self.queuedCount() > 0) .running else .idle;
    }

    fn close(ptr: *anyopaque, force: bool) contract.Failure!void {
        const self = cast(ptr);
        if (!force and (self.live() != null or self.queuedCount() > 0)) return error.RunActive;
        self.gate.cancelled.store(true, .release);
        if (self.live() != null) self.runtime.cancel();
        self.destroy();
    }

    fn tools(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ToolsListRequest, refusal: *contract.Refusal) contract.Failure!contract.ToolSet {
        _ = refusal;
        const self = cast(ptr);
        const named = request.session_id orelse "";
        if (named.len > 0 and !std.mem.eql(u8, named, self.id)) return error.RunNotFound;
        const available = self.runtime.availableTools();
        const definitions = try arena.alloc(oap_types.ToolDefinition, available.len);
        for (available, definitions) |tool, *slot| {
            slot.* = .{
                .name = tool.name,
                .description = tool.description,
                .input_schema_json = tool.parameters_schema_json,
                .execution_owner = endpoint_id,
            };
        }
        return .{ .revision = capability_revision, .response = .{ .session_id = request.session_id, .tools = definitions } };
    }

    fn models(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ModelsRequest, refusal: *contract.Refusal) contract.Failure!contract.Catalog {
        const self = cast(ptr);
        if (request.session_id.len > 0 and !std.mem.eql(u8, request.session_id, self.id)) return error.InvalidSubmission;
        if (!request.allowsDegraded(contract.feature_models_list)) return refusal.degraded(contract.feature_models_list);
        const current = try self.currentModelRef(arena);
        const available = self.runtime.availableModels();
        var catalog = try std.ArrayList(oap_types.ModelDescriptor).initCapacity(arena, available.len);
        for (available) |model| {
            const ref = refFor(arena, model) catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                else => continue,
            };
            catalog.appendAssumeCapacity(.{
                .id = ref,
                .display_name = if (model.name.len > 0) model.name else null,
                .provider_id = model.provider,
                .context_window = model.context_window,
                .default = if (current) |selected| std.mem.eql(u8, selected, ref) else false,
            });
        }
        return .{ .revision = capability_revision, .response = .{
            .session_id = self.id,
            .current_model_id = current,
            .models = catalog.items,
        } };
    }

    fn switchModel(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.SessionModelSwitchRequest, refusal: *contract.Refusal) contract.Failure!contract.Switched {
        const self = cast(ptr);
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.InvalidSubmission;
        if (self.live() != null or self.queuedCount() > 0) return error.RunActive;
        try self.syncCatalog();
        const chosen = findModel(arena, self.runtime.availableModels(), request.model_id) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
        } orelse return refusal.missingModel(request.model_id);
        const previous = try self.currentModelRef(arena);
        self.runtime.switchModelExact(chosen) catch |err| switch (err) {
            error.ModelNotFound => return refusal.missingModel(request.model_id),
            else => return refusal.fail(error.BackendFailed, @errorName(err)),
        };
        self.updated_at_ms = self.owner.now_ms();
        return .{
            .response = .{ .session_id = self.id, .model_id = request.model_id, .previous_model_id = previous },
            .state = try self.snapshot(arena),
        };
    }
};

pub const settings_key = "oapx";

fn decisionFor(answer: []const u8) tui_session.ToolApprovalDecision {
    if (std.mem.eql(u8, answer, "approve")) return .approve;
    if (std.mem.eql(u8, answer, "approve_always")) return .approve_always;
    if (std.mem.eql(u8, answer, "reject_always")) return .reject_always;
    return .reject;
}

fn thinkingLevel(text: []const u8, refusal: *contract.Refusal) contract.Failure!ai_types.ThinkingLevel {
    const level = std.meta.stringToEnum(ai_types.ThinkingLevel, text) orelse return refusal.unsupportedField(contract.feature_session_reasoning, contract.reason_unsatisfiable, "reasoning_level");
    if (level == .minimal) return refusal.unsupportedField(contract.feature_session_reasoning, contract.reason_unsatisfiable, "reasoning_level");
    return level;
}

fn offersUserInput(metadata: ?std.json.Value) bool {
    const document = metadata orelse return true;
    if (document != .object) return true;
    const settings = document.object.get(settings_key) orelse return true;
    if (settings != .object) return true;
    const offered = settings.object.get("user_input") orelse return true;
    return !(offered == .bool and !offered.bool);
}

const ContextWindowSetting = union(enum) {
    default,
    tokens: u32,
};

const LiveSettings = struct {
    context_window: ?ContextWindowSetting = null,
    output: ?agent.OutputSetting = null,
    permission_mode: ?tui_runtime.PermissionMode = null,
    workspace_root: ?[]const u8 = null,
};

fn parseLiveSettings(arena: std.mem.Allocator, raw: []const u8, refusal: *contract.Refusal) contract.Failure!LiveSettings {
    var settings = LiveSettings{};
    const document = std.json.parseFromSliceLeaky(std.json.Value, arena, raw, .{}) catch return settings;
    if (document != .object) return settings;
    const named = document.object.get(settings_key) orelse return settings;
    if (named != .object) return settings;
    const fields = named.object;
    if (fields.get("context_window")) |value| {
        settings.context_window = switch (value) {
            .null => .default,
            .integer => |count| if (count > 0 and count <= std.math.maxInt(u32)) .{ .tokens = @intCast(count) } else return refusal.fail(error.InvalidSubmission, "context_window must be a positive token count or null"),
            else => return refusal.fail(error.InvalidSubmission, "context_window must be a positive token count or null"),
        };
    }
    if (fields.get("output")) |value| {
        settings.output = switch (value) {
            .string => |text| if (std.mem.eql(u8, text, "auto")) .auto else if (std.mem.eql(u8, text, "max")) .max else return refusal.fail(error.InvalidSubmission, "output must be auto, max or a token count"),
            .integer => |count| if (count > 0 and count <= std.math.maxInt(u32)) .{ .tokens = @intCast(count) } else return refusal.fail(error.InvalidSubmission, "output must be auto, max or a token count"),
            else => return refusal.fail(error.InvalidSubmission, "output must be auto, max or a token count"),
        };
    }
    if (fields.get("permission_mode")) |value| {
        const mode = if (value == .string) std.meta.stringToEnum(tui_runtime.PermissionMode, value.string) else null;
        settings.permission_mode = mode orelse return refusal.fail(error.InvalidSubmission, "permission_mode is not a mode the loop has");
    }
    if (fields.get("workspace_root")) |value| {
        if (value != .string or value.string.len == 0) return refusal.fail(error.InvalidSubmission, "workspace_root must be a directory");
        settings.workspace_root = value.string;
    }
    return settings;
}

fn checkLiveSettings(runtime: *tui_runtime.TuiRuntime, settings: LiveSettings, refusal: *contract.Refusal) contract.Failure!void {
    if (settings.context_window) |window| if (window == .tokens) {
        if (runtime.contextWindowMaximum()) |ceiling| {
            if (window.tokens > ceiling) return refusal.fail(error.InvalidSubmission, "context_window is above the model's window");
        }
    };
    if (settings.output) |setting| if (setting == .tokens) {
        if (runtime.currentModel()) |model| {
            if (model.max_tokens > 0 and setting.tokens > model.max_tokens) return refusal.fail(error.InvalidSubmission, "output is above the model's output limit");
        }
    };
}

fn applyLiveSettings(runtime: *tui_runtime.TuiRuntime, settings: LiveSettings, refusal: *contract.Refusal) contract.Failure!void {
    if (settings.context_window) |window| runtime.setContextWindow(switch (window) {
        .default => null,
        .tokens => |count| count,
    }) catch |err| return refusal.fail(error.InvalidSubmission, @errorName(err));
    if (settings.output) |setting| runtime.setOutput(setting) catch |err| return refusal.fail(error.InvalidSubmission, @errorName(err));
    if (settings.permission_mode) |mode| runtime.setPermissionMode(mode) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return refusal.fail(error.BackendFailed, @errorName(err)),
    };
    if (settings.workspace_root) |root| runtime.setWorkspaceRoot(root) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return refusal.fail(error.BackendFailed, @errorName(err)),
    };
}

pub fn sessionOptions(base: tui_runtime.TuiRuntimeOptions, metadata: ?std.json.Value) tui_runtime.TuiRuntimeOptions {
    var options = base;
    const document = metadata orelse return options;
    if (document != .object) return options;
    const settings = document.object.get(settings_key) orelse return options;
    if (settings != .object) return options;
    const fields = settings.object;
    if (fields.get("thinking_level")) |value| {
        if (value == .string) {
            if (std.meta.stringToEnum(ai_types.ThinkingLevel, value.string)) |level| options.thinking_level = level;
        }
    }
    if (fields.get("context_window")) |value| {
        if (value == .integer and value.integer > 0 and value.integer <= std.math.maxInt(u32)) options.context_window = @intCast(value.integer);
    }
    if (fields.get("permission_mode")) |value| {
        if (value == .string) {
            if (std.meta.stringToEnum(tui_runtime.PermissionMode, value.string)) |mode| options.permission_mode = mode;
        }
    }
    if (fields.get("output")) |value| {
        switch (value) {
            .string => |text| {
                if (std.mem.eql(u8, text, "auto")) options.output = .auto;
                if (std.mem.eql(u8, text, "max")) options.output = .max;
            },
            .integer => |count| if (count > 0 and count <= std.math.maxInt(u32)) {
                options.output = .{ .tokens = @intCast(count) };
            },
            else => {},
        }
    }
    if (fields.get("workspace_root")) |value| {
        if (value == .string and value.string.len > 0) options.workspace_root = value.string;
    }
    return options;
}

fn ownEngine(gpa: std.mem.Allocator, shared: ?*permission.PermissionEngine, workspace_root: []const u8) contract.Failure!?*permission.PermissionEngine {
    const template = shared orelse return null;
    const engine = try gpa.create(permission.PermissionEngine);
    errdefer gpa.destroy(engine);
    const settings = permission.PermissionEngineOptions{
        .workspace_root = if (workspace_root.len > 0) workspace_root else template.workspace_root,
        .persistence_path = template.persistence_path,
        .approval_callback = template.approval_callback,
    };
    engine.* = permission.PermissionEngine.init(gpa, settings) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => try permission.PermissionEngine.initEmpty(gpa, settings),
    };
    return engine;
}

pub fn requestedModel(
    allocator: std.mem.Allocator,
    available: []const ai_types.Model,
    metadata: ?std.json.Value,
    refusal: *contract.Refusal,
) contract.Failure!?tui_runtime.InitialModelRef {
    const document = metadata orelse return null;
    if (document != .object) return null;
    const settings = document.object.get(settings_key) orelse return null;
    if (settings != .object) return null;
    const named = settings.object.get("model") orelse return null;
    if (named != .string or named.string.len == 0) return null;
    var scratch = std.heap.ArenaAllocator.init(allocator);
    defer scratch.deinit();
    const chosen = (try findModel(scratch.allocator(), available, named.string)) orelse return refusal.missingModel(named.string);
    return .{ .id = chosen.id, .provider = chosen.provider, .api = chosen.api };
}

fn refFor(allocator: std.mem.Allocator, model: ai_types.Model) ![]u8 {
    return model_ref.formatModelRef(allocator, model.provider, model.api, model.id);
}

fn findModel(arena: std.mem.Allocator, available: []const ai_types.Model, wanted: []const u8) error{OutOfMemory}!?ai_types.Model {
    for (available) |model| {
        const ref = refFor(arena, model) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => continue,
        };
        if (std.mem.eql(u8, ref, wanted)) return model;
    }
    return null;
}

fn userText(arena: std.mem.Allocator, messages: []const oap_types.Message) ![]const u8 {
    var text = std.ArrayList(u8).empty;
    for (messages) |message| {
        if (message.role != .user) continue;
        switch (message.content) {
            .text => |value| try appendParagraph(arena, &text, value),
            .parts => |parts| for (parts) |part| switch (part) {
                .text => |value| try appendParagraph(arena, &text, value),
                else => {},
            },
        }
    }
    return text.items;
}

fn appendParagraph(arena: std.mem.Allocator, text: *std.ArrayList(u8), value: []const u8) !void {
    if (value.len == 0) return;
    if (text.items.len > 0) try text.appendSlice(arena, "\n\n");
    try text.appendSlice(arena, value);
}

fn stopReasonText(reason: ai_types.StopReason) []const u8 {
    return switch (reason) {
        .stop => "end_turn",
        .length => "max_tokens",
        .tool_use => "tool_use",
        .content_filter => "content_filter",
        .@"error" => "error",
        .aborted => "cancelled",
    };
}

fn jsonOrString(arena: std.mem.Allocator, text: []const u8) !std.json.Value {
    if (text.len == 0) return .{ .object = .empty };
    return std.json.parseFromSliceLeaky(std.json.Value, arena, text, .{}) catch |err| switch (err) {
        error.OutOfMemory => error.OutOfMemory,
        else => .{ .string = text },
    };
}

fn errorText(arena: std.mem.Allocator, result_json: []const u8) ![]const u8 {
    const parsed = jsonOrString(arena, result_json) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
    };
    switch (parsed) {
        .string => |value| return if (value.len > 0) value else "the tool failed",
        .object => |object| {
            for ([_][]const u8{ "error", "message", "output", "stderr" }) |key| {
                if (object.get(key)) |member| {
                    if (member == .string and member.string.len > 0) return member.string;
                }
            }
        },
        else => {},
    }
    return if (result_json.len > 0) result_json else "the tool failed";
}

const Payload = struct {
    allocator: std.mem.Allocator,
    map: std.json.ObjectMap = .empty,

    fn init(allocator: std.mem.Allocator) Payload {
        return .{ .allocator = allocator };
    }

    fn put(self: *Payload, key: []const u8, member: std.json.Value) !void {
        try self.map.put(self.allocator, key, member);
    }

    fn value(self: *Payload) std.json.Value {
        return .{ .object = self.map };
    }

    fn run(self: *Payload, session: *Session, owner: *Run) !void {
        try self.put("session_id", .{ .string = session.id });
        try self.put("run_id", .{ .string = owner.id });
    }
};

fn compactAt(arena: std.mem.Allocator, raw: []const u8, runtime: *tui_runtime.TuiRuntime, refusal: *contract.Refusal) contract.Failure!?u64 {
    const unsatisfiable = contract.reason_unsatisfiable;
    const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, raw, .{}) catch return refusal.unsupportedField(contract.feature_compaction_policy, unsatisfiable, "compaction_policy");
    if (parsed != .object) return refusal.unsupportedField(contract.feature_compaction_policy, unsatisfiable, "compaction_policy");
    const kind = parsed.object.get("kind") orelse return refusal.unsupportedField(contract.feature_compaction_policy, unsatisfiable, "compaction_policy");
    if (kind != .string) return refusal.unsupportedField(contract.feature_compaction_policy, unsatisfiable, "compaction_policy");
    if (std.mem.eql(u8, kind.string, "off")) return null;
    if (std.mem.eql(u8, kind.string, "tokens")) {
        const tokens = parsed.object.get("tokens") orelse return refusal.unsupportedField(contract.feature_compaction_policy, unsatisfiable, "compaction_policy");
        if (tokens != .integer or tokens.integer < 1) return refusal.unsupportedField(contract.feature_compaction_policy, unsatisfiable, "compaction_policy");
        return @intCast(tokens.integer);
    }
    const window = runtime.contextWindow();
    if (window == 0 or window > std.math.maxInt(u32)) return refusal.unsupportedField(contract.feature_compaction_policy, unsatisfiable, "compaction_policy");
    const model = runtime.currentModel() orelse return refusal.unsupportedField(contract.feature_compaction_policy, unsatisfiable, "compaction_policy");
    if (std.mem.eql(u8, kind.string, "auto")) return agent.compaction.autoCompactAt(@intCast(window), agent.compaction.maxOutputTokens(model));
    if (std.mem.eql(u8, kind.string, "share")) {
        const share = parsed.object.get("share_percent") orelse return refusal.unsupportedField(contract.feature_compaction_policy, unsatisfiable, "compaction_policy");
        if (share != .integer or share.integer < 1 or share.integer > 100) return refusal.unsupportedField(contract.feature_compaction_policy, unsatisfiable, "compaction_policy");
        return agent.compaction.shareAt(@intCast(window), @intCast(share.integer));
    }
    return refusal.unsupportedField(contract.feature_compaction_policy, unsatisfiable, "compaction_policy");
}

fn listsId(ids: []const []const u8, id: []const u8) bool {
    for (ids) |listed| {
        if (std.mem.eql(u8, listed, id)) return true;
    }
    return false;
}

const testing = std.testing;
const agent = @import("agent");
const event_stream = @import("event_stream");

const test_model = ai_types.Model{
    .id = "scripted-model",
    .name = "Scripted Model",
    .api = "openai-completions",
    .provider = "scripted",
    .base_url = "http://127.0.0.1:1",
    .reasoning = false,
    .input = &.{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 8192,
    .max_tokens = 1024,
};

const other_model = ai_types.Model{
    .id = "other-model",
    .name = "Other Model",
    .api = "openai-completions",
    .provider = "scripted",
    .base_url = "http://127.0.0.1:1",
    .reasoning = false,
    .input = &.{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 4096,
    .max_tokens = 1024,
};

const Script = struct {
    calls: usize = 0,
    reply: []const u8 = "hello",
    tool_first: bool = false,
    tool_name: []const u8 = "echo_tool",
    tool_arguments: []const u8 = "{\"say\":\"hi\"}",
    wait_for_cancel: bool = false,
    received_answers: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    saw_steer: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
};

var held_tool = std.atomic.Value(bool).init(false);

fn scriptedMessage(allocator: std.mem.Allocator, model: ai_types.Model, content: []const ai_types.AssistantContent, reason: ai_types.StopReason) !ai_types.AssistantMessage {
    const blocks = try allocator.alloc(ai_types.AssistantContent, content.len);
    for (content, blocks) |block, *slot| {
        slot.* = switch (block) {
            .text => |t| .{ .text = .{ .text = try allocator.dupe(u8, t.text) } },
            .tool_call => |call| tool_call: {
                const id = try allocator.dupe(u8, call.id);
                errdefer allocator.free(id);
                const name = try allocator.dupe(u8, call.name);
                errdefer allocator.free(name);
                const arguments_json = try allocator.dupe(u8, call.arguments_json);
                break :tool_call .{ .tool_call = .{ .id = id, .name = name, .arguments_json = arguments_json } };
            },
            else => unreachable,
        };
    }
    return .{ .content = blocks, .api = model.api, .provider = model.provider, .model = model.id, .usage = .{}, .stop_reason = reason, .timestamp = 0 };
}

fn bareMessage(model: ai_types.Model, reason: ai_types.StopReason) ai_types.AssistantMessage {
    return .{ .content = &.{}, .api = model.api, .provider = model.provider, .model = model.id, .usage = .{}, .stop_reason = reason, .timestamp = 0 };
}

fn finish(stream: *event_stream.AssistantMessageEventStream, allocator: std.mem.Allocator, model: ai_types.Model, content: []const ai_types.AssistantContent, reason: ai_types.StopReason) !void {
    try stream.push(.{ .done = .{ .reason = reason, .message = try scriptedMessage(allocator, model, content, reason) } });
    stream.complete(try scriptedMessage(allocator, model, content, reason));
}

fn scriptedStream(ctx: ?*anyopaque, model: ai_types.Model, context: ai_types.Context, options: agent.ProtocolOptions, allocator: std.mem.Allocator) anyerror!*event_stream.AssistantMessageEventStream {
    const script: *Script = @ptrCast(@alignCast(ctx.?));
    script.calls += 1;
    for (context.messages) |message| {
        if (message == .user and message.user.content == .text and std.mem.indexOf(u8, message.user.content.text, "change course") != null) script.saw_steer.store(true, .release);
        if (message != .tool_result) continue;
        for (message.tool_result.content) |part| {
            if (part == .text and std.mem.indexOf(u8, part.text.text, "careful") != null and std.mem.indexOf(u8, part.text.text, "safe") != null) script.received_answers.store(true, .release);
        }
    }
    const stream = try allocator.create(event_stream.AssistantMessageEventStream);
    stream.* = event_stream.AssistantMessageEventStream.init(allocator);
    if (script.wait_for_cancel) {
        if (options.cancel_token) |token| {
            var waits: usize = 0;
            while (!token.isCancelled() and waits < 2000) : (waits += 1) {
                std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
            }
        }
        try stream.push(.{ .done = .{ .reason = .aborted, .message = bareMessage(model, .aborted) } });
        stream.complete(bareMessage(model, .aborted));
        return stream;
    }
    if (script.tool_first and script.calls == 1) {
        try stream.push(.{ .start = .{ .partial = bareMessage(model, .tool_use) } });
        const content = [_]ai_types.AssistantContent{.{ .tool_call = .{ .id = "call-1", .name = script.tool_name, .arguments_json = script.tool_arguments } }};
        try finish(stream, allocator, model, &content, .tool_use);
        return stream;
    }
    const partial = bareMessage(model, .stop);
    try stream.push(.{ .start = .{ .partial = partial } });
    try stream.push(.{ .text_delta = .{ .content_index = 0, .delta = script.reply, .partial = partial } });
    const content = [_]ai_types.AssistantContent{.{ .text = .{ .text = script.reply } }};
    try finish(stream, allocator, model, &content, .stop);
    return stream;
}

fn echoTool(tool_call_id: []const u8, args_json: []const u8, cancel_token: ?ai_types.CancelToken, on_update_ctx: ?*anyopaque, on_update: ?agent.ToolUpdateCallback, allocator: std.mem.Allocator) anyerror!agent.AgentToolResult {
    _ = tool_call_id;
    _ = args_json;
    _ = cancel_token;
    _ = on_update_ctx;
    _ = on_update;
    var waits: usize = 0;
    while (held_tool.load(.acquire) and waits < 5000) : (waits += 1) {
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    content[0] = .{ .text = .{ .text = try allocator.dupe(u8, "echoed") } };
    return .{ .content = @FieldType(agent.AgentToolResult, "content").initOwned(content) };
}

const echo_tools = [_]agent.AgentTool{.{
    .label = "Echo",
    .name = "echo_tool",
    .description = "Echo a word back",
    .parameters_schema_json = "{\"type\":\"object\"}",
    .execute = echoTool,
}};

const scripted_models = [_]ai_types.Model{ test_model, other_model };

const Harness = struct {
    script: *Script,
    owner: Adapter,
    session: contract.Session,
    arena: std.heap.ArenaAllocator,
    seen: std.ArrayList(std.json.Parsed(std.json.Value)) = .empty,

    fn init(self: *Harness, script: *Script) !void {
        self.script = script;
        self.seen = .empty;
        self.arena = std.heap.ArenaAllocator.init(testing.allocator);
        self.owner = Adapter.init(testing.allocator, .{
            .protocol = .{ .stream_fn = scriptedStream, .ctx = script },
            .models = &scripted_models,
            .initial_model_id = test_model.id,
            .tools = &echo_tools,
        });
        var refusal = contract.Refusal{};
        self.session = try self.owner.adapter().open(self.arena.allocator(), .{ .participant = "user" }, &refusal);
    }

    fn deinit(self: *Harness) void {
        for (self.seen.items) |*parsed| parsed.deinit();
        self.seen.deinit(testing.allocator);
        self.session.teardown();
        self.owner.deinit();
        self.arena.deinit();
    }

    fn submit(self: *Harness, text: []const u8) !oap_types.MessageSubmitResponse {
        var refusal = contract.Refusal{};
        var parts = [_]oap_types.ContentPart{.{ .text = text }};
        var messages = [_]oap_types.Message{.{ .role = .user, .content = .{ .parts = &parts } }};
        return self.session.submit(self.arena.allocator(), &.{ .session_id = self.session.id(), .messages = &messages, .delivery = .auto }, "submit-envelope", &refusal);
    }

    fn steer(self: *Harness, text: []const u8, target: ?[]const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        var parts = [_]oap_types.ContentPart{.{ .text = text }};
        var messages = [_]oap_types.Message{.{ .role = .user, .content = .{ .parts = &parts } }};
        return self.session.submit(self.arena.allocator(), &.{ .session_id = self.session.id(), .messages = &messages, .delivery = .steer, .target_run_id = target }, "steer-envelope", refusal);
    }

    fn collect(self: *Harness) !void {
        var out = std.ArrayList(contract.Event).empty;
        defer out.deinit(testing.allocator);
        try self.session.drain(testing.allocator, &out);
        for (out.items) |event| {
            defer testing.allocator.free(event.line);
            defer testing.allocator.free(event.run_id);
            const parsed = try std.json.parseFromSlice(std.json.Value, testing.allocator, event.line, .{});
            errdefer parsed.deinit();
            var registry = try @import("jsonschema").Registry.initFromBundled(testing.allocator);
            defer registry.deinit();
            var validator = @import("jsonschema").Validator.init(testing.allocator, &registry);
            defer validator.deinit();
            if (try validator.validate("envelope.schema.json", parsed.value)) |failure| {
                std.debug.print("{s} fails {s} at {s}\n", .{ event.line, failure.keyword, failure.pointer });
                return error.SchemaInvalid;
            }
            try self.seen.append(testing.allocator, parsed);
        }
    }

    fn untilTerminal(self: *Harness) !void {
        var waits: usize = 0;
        while (waits < 5000) : (waits += 1) {
            _ = try self.session.pump(0);
            try self.collect();
            if (self.terminal() != null) return;
            std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
        }
        return error.TestRunNeverSettled;
    }

    fn terminal(self: *Harness) ?std.json.Value {
        for (self.seen.items) |parsed| {
            const kind = parsed.value.object.get("type").?.string;
            if (std.mem.eql(u8, kind, "run.completed") or std.mem.eql(u8, kind, "run.failed") or std.mem.eql(u8, kind, "run.cancelled")) return parsed.value;
        }
        return null;
    }

    fn count(self: *Harness, kind: []const u8) usize {
        var found: usize = 0;
        for (self.seen.items) |parsed| {
            if (std.mem.eql(u8, parsed.value.object.get("type").?.string, kind)) found += 1;
        }
        return found;
    }

    fn reset(self: *Harness) void {
        for (self.seen.items) |*parsed| parsed.deinit();
        self.seen.clearRetainingCapacity();
    }
};

test "a submit streams the agent loop's reply as one run with contiguous sequences and a completed terminal" {
    var script = Script{ .reply = "hello from the loop" };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();

    const admitted = try harness.submit("say hello");
    try testing.expectEqual(oap_types.Admission.started, admitted.admission);
    try harness.untilTerminal();

    const first = harness.seen.items[0].value.object;
    try testing.expectEqualStrings("run.started", first.get("type").?.string);
    for (harness.seen.items, 1..) |parsed, expected| {
        try testing.expectEqual(@as(i64, @intCast(expected)), parsed.value.object.get("sequence").?.integer);
        try testing.expectEqualStrings(admitted.run_id.?, parsed.value.object.get("run_id").?.string);
    }
    try testing.expect(harness.count("content.delta") >= 1);
    const settled = harness.terminal().?.object;
    try testing.expectEqualStrings("run.completed", settled.get("type").?.string);
    const final = settled.get("payload").?.object.get("final_response").?.object;
    try testing.expectEqualStrings("hello from the loop", final.get("content").?.string);
    try testing.expectEqual(contract.Activity.idle, harness.session.activity());
}

test "a tool the loop runs is published as an endpoint-owned call, requested, started and completed, each envelope naming its call" {
    var script = Script{ .tool_first = true, .reply = "done" };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();

    _ = try harness.submit("use the tool");
    try harness.untilTerminal();

    try testing.expectEqual(@as(usize, 1), harness.count("action.call.requested"));
    try testing.expectEqual(@as(usize, 1), harness.count("action.call.started"));
    try testing.expectEqual(@as(usize, 1), harness.count("action.call.completed"));
    for (harness.seen.items) |parsed| {
        const kind = parsed.value.object.get("type").?.string;
        if (!std.mem.startsWith(u8, kind, "action.call.")) continue;
        const payload = parsed.value.object.get("payload").?.object;
        try testing.expectEqualStrings(payload.get("tool_call_id").?.string, parsed.value.object.get("tool_call_id").?.string);
        try testing.expectEqualStrings(endpoint_id, payload.get("execution_owner").?.string);
        try testing.expectEqualStrings("echo_tool", payload.get("name").?.string);
    }
    try testing.expectEqualStrings("run.completed", harness.terminal().?.object.get("type").?.string);
}

test "cancelling a run settles it cancelled and leaves the session able to take the next submit" {
    var script = Script{ .wait_for_cancel = true };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();

    const admitted = try harness.submit("wait");
    var refusal = contract.Refusal{};
    const cancelled = try harness.session.cancel(harness.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expect(cancelled.accepted);
    try harness.untilTerminal();
    try testing.expectEqualStrings("run.cancelled", harness.terminal().?.object.get("type").?.string);

    harness.reset();
    script.wait_for_cancel = false;
    const next = try harness.submit("again");
    try testing.expect(!std.mem.eql(u8, admitted.run_id.?, next.run_id.?));
    try harness.untilTerminal();
    try testing.expectEqualStrings("run.completed", harness.terminal().?.object.get("type").?.string);
}

test "busy auto submissions reserve runs until the disclosed queue bound is reached" {
    var script = Script{ .wait_for_cancel = true };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();

    const admitted = try harness.submit("wait");
    for (0..queue_capacity) |_| {
        const queued = try harness.submit("later");
        try testing.expectEqual(oap_types.Admission.queued, queued.admission);
        try testing.expectEqualStrings("session_busy", queued.delivery_resolution.?);
    }
    try testing.expectError(error.RunActive, harness.submit("too soon"));
    var refusal = contract.Refusal{};
    const captured = try harness.session.state(harness.arena.allocator(), &refusal);
    try testing.expectEqual(@as(usize, 1), captured.as_of.?.admitted_submit_requests.len);
    _ = try harness.session.cancel(harness.arena.allocator(), admitted.run_id.?, &refusal);
    try harness.untilTerminal();
}

test "models lists the runtime's catalog as model refs, and a switch takes one ref and refuses an unknown one" {
    var script = Script{};
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    const a = harness.arena.allocator();
    var refusal = contract.Refusal{};

    const catalog = try harness.session.vtable.models.?(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .allow_degraded_features = &.{contract.feature_models_list} }, &refusal);
    try testing.expectEqual(@as(usize, 2), catalog.response.models.len);
    try testing.expectEqualStrings("scripted/openai-completions@scripted-model", catalog.response.current_model_id.?);
    try testing.expect(catalog.response.models[0].default);

    const switched = try harness.session.vtable.switch_model.?(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .model_id = "scripted/openai-completions@other-model" }, &refusal);
    try testing.expectEqualStrings("scripted/openai-completions@other-model", switched.state.current_model_id.?);
    try testing.expectEqualStrings("scripted/openai-completions@scripted-model", switched.response.previous_model_id.?);

    try testing.expectError(error.ModelNotFound, harness.session.vtable.switch_model.?(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .model_id = "scripted/openai-completions@missing" }, &refusal));
    try testing.expectEqualStrings("scripted/openai-completions@missing", refusal.model_id);
}

test "a catalog the terminal UI refreshes is served once the session next switches, and a listing before that keeps the one it serves" {
    var script = Script{};
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    const a = harness.arena.allocator();
    var refusal = contract.Refusal{};
    var fresh = other_model;
    fresh.id = "fresh-model";
    try harness.owner.setCatalog(&.{ test_model, other_model, fresh });

    try testing.expectError(error.CapabilityDegraded, harness.session.vtable.models.?(harness.session.ptr, a, &.{ .session_id = harness.session.id() }, &refusal));
    try testing.expectEqualStrings(contract.feature_models_list, refusal.feature);
    const before = try harness.session.vtable.models.?(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .allow_degraded_features = &.{contract.feature_models_list} }, &refusal);
    try testing.expectEqual(@as(usize, 2), before.response.models.len);

    const switched = try harness.session.vtable.switch_model.?(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .model_id = "scripted/openai-completions@fresh-model" }, &refusal);
    try testing.expectEqualStrings("scripted/openai-completions@fresh-model", switched.state.current_model_id.?);
    const after = try harness.session.vtable.models.?(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .allow_degraded_features = &.{contract.feature_models_list} }, &refusal);
    try testing.expectEqual(@as(usize, 3), after.response.models.len);
}

test "a session opened after a catalog refresh serves the refreshed catalog and can open on a model only it holds" {
    var script = Script{};
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    const a = harness.arena.allocator();
    var refusal = contract.Refusal{};
    var fresh = other_model;
    fresh.id = "fresh-model";
    try harness.owner.setCatalog(&.{ test_model, other_model, fresh });

    const metadata = try std.json.parseFromSliceLeaky(std.json.Value, a, "{\"oapx\":{\"model\":\"scripted/openai-completions@fresh-model\"}}", .{});
    const opened = try harness.owner.adapter().open(a, .{ .participant = "user", .metadata = metadata }, &refusal);
    defer opened.teardown();
    const listed = try opened.vtable.models.?(opened.ptr, a, &.{ .session_id = "", .allow_degraded_features = &.{contract.feature_models_list} }, &refusal);
    try testing.expectEqual(@as(usize, 3), listed.response.models.len);
    try testing.expectEqualStrings("scripted/openai-completions@fresh-model", listed.response.current_model_id.?);
}

test "the tool catalog is the loop's own tools, each owned by the endpoint" {
    var script = Script{};
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    var refusal = contract.Refusal{};
    const listed = try harness.session.vtable.tools.?(harness.session.ptr, harness.arena.allocator(), &.{ .session_id = harness.session.id() }, &refusal);
    var saw_echo = false;
    for (listed.response.tools) |tool| {
        try testing.expectEqualStrings(endpoint_id, tool.execution_owner);
        if (std.mem.eql(u8, tool.name, "echo_tool")) saw_echo = true;
    }
    try testing.expect(saw_echo);
}

test "an open that provides tools or attaches sources is refused, since neither is advertised" {
    var owner = Adapter.init(testing.allocator, .{});
    var refusal = contract.Refusal{};
    try testing.expectError(error.UnsupportedFeature, owner.adapter().open(testing.allocator, .{ .participant = "user", .tools_json = "[{\"name\":\"t\"}]" }, &refusal));
    try testing.expectEqualStrings(contract.feature_tools_provide, refusal.feature);
    try testing.expectError(error.UnsupportedFeature, owner.adapter().open(testing.allocator, .{ .participant = "user", .tool_sources_json = "[{\"id\":\"s\"}]" }, &refusal));
    try testing.expectEqualStrings(contract.feature_tool_sources_attach, refusal.feature);
}

test "a replay from zero re-delivers the latest run's events in order, and a future cursor or another run is refused" {
    var script = Script{ .reply = "replayed" };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    const admitted = try harness.submit("say it");
    try harness.untilTerminal();

    var refusal = contract.Refusal{};
    const replayed = try harness.session.vtable.replay.?(harness.session.ptr, testing.allocator, admitted.run_id.?, 0, &refusal);
    const events = replayed.events;
    defer {
        for (events) |event| {
            testing.allocator.free(event.line);
            testing.allocator.free(event.run_id);
        }
        testing.allocator.free(events);
    }
    try testing.expectEqual(harness.seen.items.len, events.len);
    for (events, harness.seen.items, 1..) |event, parsed, expected| {
        try testing.expectEqual(@as(u64, expected), event.sequence);
        var reparsed = try std.json.parseFromSlice(std.json.Value, testing.allocator, event.line, .{});
        defer reparsed.deinit();
        try testing.expectEqualStrings(parsed.value.object.get("type").?.string, reparsed.value.object.get("type").?.string);
    }
    try testing.expectError(error.ReplayCursorFuture, harness.session.vtable.replay.?(harness.session.ptr, testing.allocator, admitted.run_id.?, events.len + 1, &refusal));
    try testing.expectError(error.RunNotFound, harness.session.vtable.replay.?(harness.session.ptr, testing.allocator, "run-elsewhere", 0, &refusal));
}

test "a full journal drops its oldest half at once, keeping the newest entries in order" {
    var script = Script{};
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    const session = Session.cast(harness.session.ptr);
    var sequence: u64 = 1;
    while (sequence <= 8) : (sequence += 1) {
        try session.journal.append(testing.allocator, .{ .line = try testing.allocator.dupe(u8, "x"), .run_id = "run", .sequence = sequence });
    }
    session.evictOldestHalf();
    try testing.expectEqual(@as(usize, 4), session.journal.items.len);
    for (session.journal.items, 5..) |entry, expected| try testing.expectEqual(@as(u64, expected), entry.sequence);
}

test "an open's oapx metadata sets the session's thinking level, window, output limit and workspace, and anything else is ignored" {
    const base = tui_runtime.TuiRuntimeOptions{ .thinking_level = .low, .permission_mode = .bypass, .workspace_root = "/base" };
    var parsed = try std.json.parseFromSlice(std.json.Value, testing.allocator,
        \\{"oapx":{"thinking_level":"high","context_window":1000000,"output":64000,"permission_mode":"bypass","workspace_root":"/work","unknown":1}}
    , .{});
    defer parsed.deinit();
    const applied = sessionOptions(base, parsed.value);
    try testing.expectEqual(ai_types.ThinkingLevel.high, applied.thinking_level);
    try testing.expectEqual(@as(?u32, 1_000_000), applied.context_window);
    try testing.expectEqual(@as(u32, 64_000), applied.output.tokens);
    try testing.expectEqual(tui_runtime.PermissionMode.bypass, applied.permission_mode);
    try testing.expectEqualStrings("/work", applied.workspace_root);

    var named = try std.json.parseFromSlice(std.json.Value, testing.allocator, "{\"oapx\":{\"output\":\"max\"}}", .{});
    defer named.deinit();
    try testing.expect(sessionOptions(base, named.value).output == .max);

    var wrong = try std.json.parseFromSlice(std.json.Value, testing.allocator,
        \\{"oapx":{"thinking_level":"loud","context_window":-1,"output":-5}}
    , .{});
    defer wrong.deinit();
    const kept = sessionOptions(base, wrong.value);
    try testing.expectEqual(ai_types.ThinkingLevel.low, kept.thinking_level);
    try testing.expectEqual(@as(?u32, null), kept.context_window);
    try testing.expect(kept.output == .auto);
    try testing.expectEqualStrings("/base", kept.workspace_root);
}

test "an open's oapx metadata names the session's model by the reference the catalog prints, and one it lacks refuses the open" {
    var script = Script{};
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    const a = harness.arena.allocator();
    var parsed = try std.json.parseFromSlice(std.json.Value, testing.allocator,
        \\{"oapx":{"model":"scripted/openai-completions@other-model"}}
    , .{});
    defer parsed.deinit();
    var refusal = contract.Refusal{};
    const chosen = try harness.owner.adapter().open(a, .{ .participant = "user", .session_id = "picked", .metadata = parsed.value }, &refusal);
    defer chosen.teardown();
    const opened = try chosen.state(a, &refusal);
    try testing.expectEqualStrings("scripted/openai-completions@other-model", opened.current_model_id.?);

    var missing = try std.json.parseFromSlice(std.json.Value, testing.allocator,
        \\{"oapx":{"model":"scripted/openai-completions@missing"}}
    , .{});
    defer missing.deinit();
    try testing.expectError(error.ModelNotFound, harness.owner.adapter().open(a, .{ .participant = "user", .session_id = "lacking", .metadata = missing.value }, &refusal));
    try testing.expectEqualStrings("scripted/openai-completions@missing", refusal.model_id);
}

test "each session holds its own permission engine, so one session's bypass never answers another's ask" {
    var script = Script{};
    var shared = try permission.PermissionEngine.initEmpty(testing.allocator, .{ .workspace_root = "/shared", .persistence_path = "/nonexistent/oapx-permissions.json" });
    defer shared.deinit();
    var owner = Adapter.init(testing.allocator, .{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &script },
        .models = &scripted_models,
        .initial_model_id = test_model.id,
        .tools = &echo_tools,
        .permission_engine = &shared,
    });
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const a = arena_state.allocator();
    var asking = try std.json.parseFromSlice(std.json.Value, testing.allocator,
        \\{"oapx":{"permission_mode":"ask"}}
    , .{});
    defer asking.deinit();
    var refusal = contract.Refusal{};
    const first = try owner.adapter().open(a, .{ .participant = "user", .session_id = "asks", .metadata = asking.value }, &refusal);
    defer first.teardown();
    const second = try owner.adapter().open(a, .{ .participant = "user", .session_id = "bypasses" }, &refusal);
    defer second.teardown();
    const asks: *Session = @ptrCast(@alignCast(first.ptr));
    const bypasses: *Session = @ptrCast(@alignCast(second.ptr));
    try testing.expect(asks.engine.? != &shared);
    try testing.expect(asks.engine.? != bypasses.engine.?);
    try testing.expect(!asks.engine.?.bypass_all);
    try testing.expect(bypasses.engine.?.bypass_all);
    try testing.expectEqualStrings("/shared", asks.engine.?.workspace_root);
}

test "a permissions file the engine cannot read leaves a session its own empty engine rather than refusing the open" {
    var script = Script{};
    var tmp = testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.writeFile(testing.io, .{ .sub_path = "permissions.json", .data = "{not json" });
    const corrupt = try tmp.dir.realPathFileAlloc(testing.io, "permissions.json", testing.allocator);
    defer testing.allocator.free(corrupt);
    var shared = try permission.PermissionEngine.initEmpty(testing.allocator, .{ .workspace_root = "/shared", .persistence_path = corrupt });
    defer shared.deinit();
    var owner = Adapter.init(testing.allocator, .{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &script },
        .models = &scripted_models,
        .initial_model_id = test_model.id,
        .tools = &echo_tools,
        .permission_engine = &shared,
    });
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    var refusal = contract.Refusal{};
    const opened = try owner.adapter().open(arena_state.allocator(), .{ .participant = "user", .session_id = "tolerant" }, &refusal);
    defer opened.teardown();
    const held: *Session = @ptrCast(@alignCast(opened.ptr));
    try testing.expectEqual(@as(usize, 0), held.engine.?.persisted.items.len);
}

test "an open's oapx metadata can ask for ask mode, which this adapter now answers with permission interactions" {
    const base = tui_runtime.TuiRuntimeOptions{ .permission_mode = .bypass };
    var parsed = try std.json.parseFromSlice(std.json.Value, testing.allocator, "{\"oapx\":{\"permission_mode\":\"ask\"}}", .{});
    defer parsed.deinit();
    try testing.expectEqual(tui_runtime.PermissionMode.ask, sessionOptions(base, parsed.value).permission_mode);
}

fn parseValue(arena: std.mem.Allocator, text: []const u8) contract.Failure!std.json.Value {
    return std.json.parseFromSliceLeaky(std.json.Value, arena, text, .{ .allocate = .alloc_always }) catch |err| {
        if (err == error.OutOfMemory) return error.OutOfMemory;
        return error.InvalidSubmission;
    };
}

fn waitForPrompt(harness: *Harness) !PendingInteraction {
    for (0..5000) |_| {
        _ = try harness.session.pump(0);
        try harness.collect();
        if (Session.cast(harness.session.ptr).pending) |pending| return pending;
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
    return error.TestPromptNeverArrived;
}

fn permissionAnswer(harness: *Harness, pending: PendingInteraction) oap_types.PermissionResolveRequest {
    return .{ .interaction_id = pending.id, .requested_by = endpoint_id, .responded_by = "user", .session_id = harness.session.id(), .run_id = Session.cast(harness.session.ptr).run.?.id, .granted = true, .choice_id = "approve" };
}

test "each permission choice reaches the loop as its own decision, and anything else is a refusal" {
    try testing.expectEqual(tui_session.ToolApprovalDecision.approve, decisionFor("approve"));
    try testing.expectEqual(tui_session.ToolApprovalDecision.approve_always, decisionFor("approve_always"));
    try testing.expectEqual(tui_session.ToolApprovalDecision.reject_always, decisionFor("reject_always"));
    try testing.expectEqual(tui_session.ToolApprovalDecision.reject, decisionFor("deny"));
    try testing.expectEqual(tui_session.ToolApprovalDecision.reject, decisionFor("approve_forever"));
}

test "a permission prompt offers the always choices, and an always answer is accepted and published as chosen" {
    var script = Script{ .tool_first = true };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    try Session.cast(harness.session.ptr).runtime.setPermissionMode(.ask);
    _ = try harness.submit("use the tool");
    const pending = try waitForPrompt(&harness);
    var offered: usize = 0;
    for (harness.seen.items) |parsed| {
        if (!std.mem.eql(u8, parsed.value.object.get("type").?.string, "action.permission.requested")) continue;
        for (parsed.value.object.get("payload").?.object.get("choices").?.array.items) |choice| {
            const id = choice.object.get("id").?.string;
            if (std.mem.eql(u8, id, "approve_always") or std.mem.eql(u8, id, "reject_always")) offered += 1;
        }
    }
    try testing.expectEqual(@as(usize, 2), offered);
    var always = permissionAnswer(&harness, pending);
    always.choice_id = "approve_always";
    var refusal = contract.Refusal{};
    try harness.session.resolve(harness.arena.allocator(), .{ .permission = &always }, &refusal);
    try harness.untilTerminal();
    try testing.expectEqual(@as(usize, 1), harness.count("action.call.completed"));
    for (harness.seen.items) |parsed| {
        if (!std.mem.eql(u8, parsed.value.object.get("type").?.string, "action.permission.resolved")) continue;
        try testing.expectEqualStrings("approve_always", parsed.value.object.get("payload").?.object.get("choice_id").?.string);
    }
}

test "an ask-mode tool blocks for its declared responder, refuses contradictory and repeated answers, and executes after approval" {
    var script = Script{ .tool_first = true };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    try Session.cast(harness.session.ptr).runtime.setPermissionMode(.ask);
    _ = try harness.submit("use the tool");
    const pending = try waitForPrompt(&harness);
    try testing.expectEqual(interactions.Kind.permission, pending.kind);
    try testing.expectEqual(contract.Activity.waiting, harness.session.activity());
    try testing.expectEqual(@as(usize, 0), harness.count("action.call.completed"));
    var refusal = contract.Refusal{};
    const state_now = try harness.session.state(harness.arena.allocator(), &refusal);
    try testing.expectEqual(oap_types.SessionStatus.waiting_for_input, state_now.status);
    try testing.expectEqualStrings(pending.id, state_now.active_runs[0].pending_interactions[0]);
    const good = permissionAnswer(&harness, pending);
    for (0..7) |defect| {
        var wrong = good;
        switch (defect) {
            0 => wrong.responded_by = "stranger",
            1 => wrong.requested_by = "wrong-endpoint",
            2 => wrong.run_id = "another-run",
            3 => wrong.session_id = "another-session",
            4 => wrong.interaction_id = "another-interaction",
            5 => wrong.granted = false,
            6 => wrong.choice_id = "approve_forever",
            else => unreachable,
        }
        try testing.expectError(error.InvalidResolution, harness.session.resolve(harness.arena.allocator(), .{ .permission = &wrong }, &refusal));
        try testing.expectEqual(@as(usize, 0), harness.count("action.call.completed"));
        try testing.expect(Session.cast(harness.session.ptr).pending != null);
    }
    try harness.session.resolve(harness.arena.allocator(), .{ .permission = &good }, &refusal);
    try testing.expectError(error.InteractionNotFound, harness.session.resolve(harness.arena.allocator(), .{ .permission = &good }, &refusal));
    try harness.untilTerminal();
    try testing.expectEqual(@as(usize, 1), harness.count("action.permission.requested"));
    try testing.expectEqual(@as(usize, 1), harness.count("action.permission.resolved"));
    try testing.expectEqual(@as(usize, 1), harness.count("action.call.completed"));
    for (harness.seen.items, 1..) |parsed, sequence| try testing.expectEqual(@as(i64, @intCast(sequence)), parsed.value.object.get("sequence").?.integer);
}

test "denying a permission leaves the native tool unexecuted and closes the interaction once" {
    var script = Script{ .tool_first = true };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    try Session.cast(harness.session.ptr).runtime.setPermissionMode(.ask);
    _ = try harness.submit("use the tool");
    const pending = try waitForPrompt(&harness);
    var denied = permissionAnswer(&harness, pending);
    denied.choice_id = "deny";
    denied.granted = false;
    var refusal = contract.Refusal{};
    try harness.session.resolve(harness.arena.allocator(), .{ .permission = &denied }, &refusal);
    try harness.untilTerminal();
    try testing.expectEqual(@as(usize, 0), harness.count("action.call.completed"));
    try testing.expectEqual(@as(usize, 1), harness.count("action.call.failed"));
    try testing.expectEqual(@as(usize, 1), harness.count("action.permission.resolved"));
}

const input_prompt =
    \\{"title":"Pick a plan","questions":[{"id":"note","kind":"text","prompt":"Why?"},{"id":"pick","prompt":"Which plan?","kind":"single_choice","options":[{"id":"safe","label":"Safe"}]},{"id":"many","prompt":"Which extras?","kind":"multi_choice","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]}
;

test "user input validates every question before native delivery and returns the answers to the running tool" {
    var script = Script{ .tool_first = true, .tool_name = "request_user_input", .tool_arguments = input_prompt };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    _ = try harness.submit("ask me");
    const pending = try waitForPrompt(&harness);
    try testing.expectEqual(interactions.Kind.input, pending.kind);
    var answers = [_]oap_types.InputAnswer{
        .{ .question_id = "note", .text = "careful" },
        .{ .question_id = "pick", .selected_option_ids = &.{"safe"} },
        .{ .question_id = "many", .selected_option_ids = &.{ "a", "b" } },
    };
    var request = oap_types.UserInputResolveRequest{ .interaction_id = pending.id, .requested_by = endpoint_id, .responded_by = "user", .session_id = harness.session.id(), .run_id = Session.cast(harness.session.ptr).run.?.id, .answers = &answers };
    var refusal = contract.Refusal{};
    for (0..8) |defect| {
        var wrong_answers = answers;
        var wrong = request;
        wrong.answers = &wrong_answers;
        switch (defect) {
            0 => wrong.responded_by = "stranger",
            1 => wrong_answers[0].question_id = "unknown",
            2 => wrong_answers[0].text = "",
            3 => wrong_answers[1].selected_option_ids = &.{"unknown"},
            4 => wrong_answers[1].selected_option_ids = &.{ "safe", "safe" },
            5 => wrong_answers[2].selected_option_ids = &.{ "a", "a" },
            6 => wrong_answers[2].question_id = "pick",
            7 => wrong.answers = wrong_answers[0..2],
            else => unreachable,
        }
        try testing.expectError(error.InvalidResolution, harness.session.resolve(harness.arena.allocator(), .{ .input = &wrong }, &refusal));
        try testing.expect(Session.cast(harness.session.ptr).pending != null);
        try testing.expectEqual(@as(usize, 0), harness.count("action.call.completed"));
    }
    try harness.session.resolve(harness.arena.allocator(), .{ .input = &request }, &refusal);
    try testing.expectError(error.InteractionNotFound, harness.session.resolve(harness.arena.allocator(), .{ .input = &request }, &refusal));
    try harness.untilTerminal();
    try testing.expectEqual(@as(usize, 1), harness.count("user.input.requested"));
    try testing.expectEqual(@as(usize, 1), harness.count("user.input.resolved"));
    try testing.expectEqual(@as(usize, 1), harness.count("action.call.completed"));
    try testing.expect(script.received_answers.load(.acquire));
}

test "cancelling either prompt releases its native wait, resolves it once, and keeps the session reusable" {
    for ([_]bool{ false, true }) |input| {
        var script = Script{ .tool_first = true, .tool_name = if (input) "request_user_input" else "echo_tool", .tool_arguments = if (input) input_prompt else "{}" };
        var harness: Harness = undefined;
        try harness.init(&script);
        defer harness.deinit();
        try Session.cast(harness.session.ptr).runtime.setPermissionMode(.ask);
        const admitted = try harness.submit("wait for me");
        _ = try waitForPrompt(&harness);
        var refusal = contract.Refusal{};
        _ = try harness.session.cancel(harness.arena.allocator(), admitted.run_id.?, &refusal);
        try harness.untilTerminal();
        try testing.expectEqualStrings("run.cancelled", harness.terminal().?.object.get("type").?.string);
        try testing.expectEqual(@as(usize, 1), harness.count(if (input) "user.input.resolved" else "action.permission.resolved"));
        harness.reset();
        script.tool_first = false;
        _ = try harness.submit("continue");
        try harness.untilTerminal();
        try testing.expectEqualStrings("run.completed", harness.terminal().?.object.get("type").?.string);
    }
}

fn validatePrompt(allocator: std.mem.Allocator, text: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    const value = try parseValue(arena.allocator(), text);
    if (value != .object) return error.InvalidSubmission;
    const questions = value.object.get("questions") orelse return error.InvalidSubmission;
    if (questions != .array or questions.array.items.len == 0) return error.InvalidSubmission;
    var registry = try @import("jsonschema").Registry.initFromBundled(allocator);
    defer registry.deinit();
    if (value.object.get("title")) |title| {
        if (title != .string or title.string.len == 0) return error.InvalidSubmission;
    }
    for (questions.array.items, 0..) |question, index| {
        var validator = @import("jsonschema").Validator.init(allocator, &registry);
        defer validator.deinit();
        if (try validator.validateSchema(registry.root("interaction.schema.json").?.object.get("$defs").?.object.get("question").?, "interaction.schema.json", question) != null) return error.InvalidSubmission;
        if (question != .object) return error.InvalidSubmission;
        const id = question.object.get("id") orelse return error.InvalidSubmission;
        const kind = question.object.get("kind") orelse return error.InvalidSubmission;
        if (id != .string or id.string.len == 0 or kind != .string) return error.InvalidSubmission;
        const parsed_kind = std.meta.stringToEnum(contract.QuestionKind, kind.string) orelse return error.InvalidSubmission;
        for (questions.array.items[0..index]) |earlier| {
            if (std.mem.eql(u8, earlier.object.get("id").?.string, id.string)) return error.InvalidSubmission;
        }
        const options = question.object.get("options");
        if (parsed_kind != .text and options == null) return error.InvalidSubmission;
        if (options) |offered| {
            if (offered != .array or (parsed_kind != .text and offered.array.items.len == 0)) return error.InvalidSubmission;
            for (offered.array.items, 0..) |option, option_index| {
                if (option != .object) return error.InvalidSubmission;
                const option_id = option.object.get("id") orelse return error.InvalidSubmission;
                if (option_id != .string or option_id.string.len == 0) return error.InvalidSubmission;
                for (offered.array.items[0..option_index]) |earlier| {
                    if (std.mem.eql(u8, earlier.object.get("id").?.string, option_id.string)) return error.InvalidSubmission;
                }
            }
        }
    }
}

const Wire = struct {
    arena: std.heap.ArenaAllocator,
    owner: Adapter,
    endpoint: @import("endpoint").Endpoint,
    trace: std.ArrayList(std.json.Value) = .empty,
    ids: usize = 0,

    fn init(self: *Wire, script: *Script) void {
        self.arena = std.heap.ArenaAllocator.init(testing.allocator);
        self.owner = Adapter.init(testing.allocator, .{
            .protocol = .{ .stream_fn = scriptedStream, .ctx = script },
            .models = &scripted_models,
            .initial_model_id = test_model.id,
            .tools = &echo_tools,
            .permission_mode = .ask,
        });
        self.endpoint = @import("endpoint").Endpoint.init(testing.allocator, self.owner.adapter(), .{});
        self.trace = .empty;
        self.ids = 0;
    }

    fn deinit(self: *Wire) void {
        self.endpoint.deinit();
        self.arena.deinit();
    }

    fn collect(self: *Wire) !void {
        while (self.endpoint.popOutbound()) |line| {
            defer testing.allocator.free(line);
            const owned = try self.arena.allocator().dupe(u8, line);
            try self.trace.append(self.arena.allocator(), try parseValue(self.arena.allocator(), owned));
        }
    }

    fn send(self: *Wire, kind: []const u8, scope: []const u8, payload: std.json.Value) !void {
        self.ids += 1;
        const a = self.arena.allocator();
        const encoded = try json_encode.valueAlloc(a, payload);
        const line = try std.fmt.allocPrint(a,
            \\{{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"{s}","id":"client-{d}"{s},"payload":{s}}}
        , .{ kind, self.ids, scope, encoded });
        try self.trace.append(a, try parseValue(a, line));
        try self.endpoint.handleLine(line);
        try self.collect();
    }

    fn wait(self: *Wire, kind: []const u8) !std.json.Value {
        for (0..5000) |_| {
            _ = try self.endpoint.pump(0);
            try self.collect();
            for (self.trace.items) |event| {
                if (std.mem.eql(u8, event.object.get("type").?.string, kind)) return event;
            }
            std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
        }
        return error.EventNeverArrived;
    }

    fn validate(self: *Wire) !void {
        var registry = try @import("jsonschema").Registry.initFromBundled(testing.allocator);
        defer registry.deinit();
        var machine = @import("semantic").Machine.init(testing.allocator);
        defer machine.deinit();
        for (self.trace.items, 0..) |event, index| {
            var validator = @import("jsonschema").Validator.init(testing.allocator, &registry);
            defer validator.deinit();
            if (try validator.validate("envelope.schema.json", event)) |failure| {
                std.debug.print("wire {d} {s} fails {s} at {s}\n", .{ index, event.object.get("type").?.string, failure.keyword, failure.pointer });
                return error.SchemaInvalid;
            }
            try machine.apply(index, event);
        }
        try machine.close();
        for (machine.diagnostics.items) |diagnostic| std.debug.print("wire {d}: {s}\n", .{ diagnostic.index, diagnostic.code });
        try testing.expectEqual(@as(usize, 0), machine.diagnostics.items.len);
    }
};

test "the served endpoint routes or cancels permission and input prompts with schema and semantic valid conversations" {
    for (0..4) |variant| {
        const input = variant % 2 == 1;
        const cancelled = variant >= 2;
        var script = Script{ .tool_first = true, .tool_name = if (input) "request_user_input" else "echo_tool", .tool_arguments = if (input) input_prompt else "{}" };
        var wire: Wire = undefined;
        wire.init(&script);
        defer wire.deinit();
        const a = wire.arena.allocator();
        try wire.send("protocol.initialize.request", "", try parseValue(a,
            \\{"participant":{"id":"user","name":"Test"},"protocol_versions":["0.1"],"profiles":["open-agent-protocol.agent-control-core"]}
        ));
        try wire.send("capabilities.request", "", try parseValue(a, "{}"));
        const scope = ",\"session_id\":\"wire-session\",\"capability_revision\":\"" ++ capability_revision ++ "\"";
        try wire.send("session.open.request", scope, try parseValue(a, "{\"session_id\":\"wire-session\"}"));
        try wire.send("session.message.submit.request", scope, try parseValue(a,
            \\{"session_id":"wire-session","delivery":"auto","messages":[{"role":"user","content":"test prompts"}]}
        ));
        const prompt = try wire.wait(if (input) "user.input.requested" else "action.permission.requested");
        const asked = prompt.object.get("payload").?.object;
        var answer = Payload.init(a);
        for ([_][]const u8{ "interaction_id", "session_id", "run_id", "requested_by", "responded_by" }) |key| try answer.put(key, asked.get(key).?);
        if (input) {
            try answer.put("answers", try parseValue(a,
                \\[{"question_id":"note","text":"careful"},{"question_id":"pick","selected_option_ids":["safe"]},{"question_id":"many","selected_option_ids":["a"]}]
            ));
        } else {
            try answer.put("granted", .{ .bool = true });
            try answer.put("choice_id", .{ .string = "approve" });
        }
        const run_scope = try std.fmt.allocPrint(a, "{s},\"run_id\":\"{s}\"", .{ scope, asked.get("run_id").?.string });
        if (cancelled) {
            var cancellation = Payload.init(a);
            try cancellation.put("session_id", asked.get("session_id").?);
            try cancellation.put("run_id", asked.get("run_id").?);
            try wire.send("run.cancel.request", run_scope, cancellation.value());
        } else {
            try wire.send(if (input) "user.input.resolve.request" else "action.permission.resolve.request", run_scope, answer.value());
        }
        _ = try wire.wait(if (cancelled) "run.cancelled" else "run.completed");
        try wire.validate();
        if (input and !cancelled) try testing.expect(script.received_answers.load(.acquire));
    }
}

test "native input rejects malformed questions before publishing a wait" {
    for ([_][]const u8{
        "{}",
        "{\"questions\":[]}",
        "{\"questions\":[{\"id\":\"q\",\"kind\":\"text\"}]}",
        "{\"questions\":[{\"id\":\"q\",\"prompt\":\"Q?\",\"kind\":\"text\",\"options\":[]}]}",
        "{\"questions\":[{\"id\":\"q\",\"prompt\":\"Q?\",\"kind\":\"single_choice\",\"options\":[{\"id\":\"a\",\"label\":\"\"}]}]}",
        "{\"questions\":[{\"id\":\"q\",\"prompt\":\"Q?\",\"kind\":\"text\"},{\"id\":\"q\",\"prompt\":\"Again?\",\"kind\":\"text\"}]}",
        "{\"questions\":[{\"id\":\"q\",\"prompt\":\"Q?\",\"kind\":\"multi_choice\",\"options\":[{\"id\":\"a\",\"label\":\"A\"},{\"id\":\"a\",\"label\":\"Again\"}]}]}",
    }) |invalid| try testing.expectError(error.InvalidSubmission, validatePrompt(testing.allocator, invalid));
}

test "an optional question can remain unanswered while required input reaches the native tool" {
    var script = Script{ .tool_first = true, .tool_name = "request_user_input", .tool_arguments =
        \\{"questions":[{"id":"note","prompt":"Why?","kind":"text"},{"id":"optional","prompt":"Anything else?","kind":"text","required":false}]}
    };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    _ = try harness.submit("ask me");
    const pending = try waitForPrompt(&harness);
    const questions = harness.seen.items[harness.seen.items.len - 1].value.object.get("payload").?.object.get("questions").?.array.items;
    try testing.expect(questions[0].object.get("required").?.bool);
    var answers = [_]oap_types.InputAnswer{.{ .question_id = "note", .text = "careful safe" }};
    const request = oap_types.UserInputResolveRequest{ .interaction_id = pending.id, .session_id = harness.session.id(), .run_id = Session.cast(harness.session.ptr).run.?.id, .requested_by = endpoint_id, .responded_by = "user", .answers = &answers };
    var refusal = contract.Refusal{};
    try harness.session.resolve(harness.arena.allocator(), .{ .input = &request }, &refusal);
    try harness.untilTerminal();
    try testing.expect(script.received_answers.load(.acquire));
}

fn wireOpen(wire: *Wire) !void {
    const a = wire.arena.allocator();
    try wire.send("protocol.initialize.request", "", try parseValue(a,
        \\{"participant":{"id":"user","name":"Test"},"protocol_versions":["0.1"],"profiles":["open-agent-protocol.agent-control-core"]}
    ));
    try wire.send("capabilities.request", "", try parseValue(a, "{}"));
    try wire.send("session.open.request", wire_scope, try parseValue(a, "{\"session_id\":\"wire-session\"}"));
}

const wire_scope = ",\"session_id\":\"wire-session\",\"capability_revision\":\"" ++ capability_revision ++ "\"";

fn wireLast(wire: *Wire, kind: []const u8) std.json.Value {
    var index = wire.trace.items.len;
    while (index > 0) {
        index -= 1;
        const event = wire.trace.items[index];
        if (std.mem.eql(u8, event.object.get("type").?.string, kind)) return event;
    }
    unreachable;
}

fn wireSubmit(wire: *Wire, delivery: []const u8) !std.json.Value {
    const a = wire.arena.allocator();
    const text = try std.fmt.allocPrint(a, "{{\"session_id\":\"wire-session\",\"delivery\":\"{s}\",\"messages\":[{{\"role\":\"user\",\"content\":\"queued task\"}}]}}", .{delivery});
    try wire.send("session.message.submit.request", wire_scope, try parseValue(a, text));
    return wire.trace.items[wire.trace.items.len - 1];
}

fn wireCancel(wire: *Wire, run_id: []const u8) !void {
    const a = wire.arena.allocator();
    const scope = try std.fmt.allocPrint(a, "{s},\"run_id\":\"{s}\"", .{ wire_scope, run_id });
    const payload = try std.fmt.allocPrint(a, "{{\"session_id\":\"wire-session\",\"run_id\":\"{s}\"}}", .{run_id});
    try wire.send("run.cancel.request", scope, try parseValue(a, payload));
}

fn wireState(wire: *Wire, queued: usize, executing: bool) !void {
    try wire.send("session.state.request", wire_scope, try parseValue(wire.arena.allocator(), "{\"session_id\":\"wire-session\"}"));
    const state_value = wireLast(wire, "session.state.response").object.get("payload").?.object;
    const entries: []const std.json.Value = if (state_value.get("active_runs")) |value| value.array.items else &.{};
    try testing.expectEqual(queued + @as(usize, if (executing) 1 else 0), entries.len);
    try testing.expectEqual(executing, state_value.get("active_run_id") != null);
    var position: i64 = 0;
    for (entries) |entry| {
        if (std.mem.eql(u8, entry.object.get("status").?.string, "queued")) {
            position += 1;
            try testing.expectEqual(position, entry.object.get("queue_position").?.integer);
        }
    }
    try testing.expectEqual(@as(i64, @intCast(queued)), position);
}

fn wireUntilSettled(wire: *Wire, count: usize) !void {
    for (0..5000) |_| {
        _ = try wire.endpoint.pump(0);
        try wire.collect();
        var found: usize = 0;
        for (wire.trace.items) |event| {
            const kind = event.object.get("type").?.string;
            if (std.mem.eql(u8, kind, "run.completed") or std.mem.eql(u8, kind, "run.cancelled") or std.mem.eql(u8, kind, "run.failed")) found += 1;
        }
        if (found == count) return;
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
    return error.QueueNeverSettled;
}

test "idle explicit queue reserves before promotion, preserves admission order and updates counters after reservation cancellation" {
    var script = Script{};
    var wire: Wire = undefined;
    wire.init(&script);
    defer wire.deinit();
    try wireOpen(&wire);
    _ = try wireSubmit(&wire, "queue");
    const first = wireLast(&wire, "session.message.submit.response").object.get("payload").?.object;
    try testing.expectEqualStrings("queue", first.get("effective_delivery").?.string);
    try testing.expectEqualStrings("queued", first.get("admission").?.string);
    try testing.expectEqual(@as(usize, 0), script.calls);
    try wireState(&wire, 1, false);
    _ = try wireSubmit(&wire, "auto");
    const middle = wireLast(&wire, "session.message.submit.response").object.get("payload").?.object;
    try testing.expectEqualStrings("session_busy", middle.get("delivery_resolution").?.string);
    _ = try wireSubmit(&wire, "queue");
    const last = wireLast(&wire, "session.message.submit.response").object.get("payload").?.object;
    try wireState(&wire, 3, false);
    try wireCancel(&wire, middle.get("run_id").?.string);
    try wireState(&wire, 2, false);
    try wireUntilSettled(&wire, 3);
    try wireState(&wire, 0, false);
    var started: usize = 0;
    for (wire.trace.items) |event| {
        if (!std.mem.eql(u8, event.object.get("type").?.string, "run.started")) continue;
        try testing.expectEqualStrings(if (started == 0) first.get("run_id").?.string else last.get("run_id").?.string, event.object.get("run_id").?.string);
        started += 1;
    }
    try testing.expectEqual(@as(usize, 2), started);
    try testing.expectEqual(@as(usize, 2), script.calls);
    try wire.validate();
}

test "buffered reservation cancellation frees a full queue slot before the next submit without executing cancelled work" {
    var script = Script{ .tool_first = true };
    var wire: Wire = undefined;
    wire.init(&script);
    defer wire.deinit();
    try wireOpen(&wire);
    const a = wire.arena.allocator();
    _ = try wireSubmit(&wire, "auto");
    const prompt = try wire.wait("action.permission.requested");
    const asked = prompt.object.get("payload").?.object;
    var reserved: [queue_capacity][]const u8 = undefined;
    for (&reserved) |*id| {
        _ = try wireSubmit(&wire, "auto");
        id.* = wireLast(&wire, "session.message.submit.response").object.get("payload").?.object.get("run_id").?.string;
    }
    try wireState(&wire, queue_capacity, true);
    _ = try wireSubmit(&wire, "auto");
    try testing.expectEqualStrings("run_active", wireLast(&wire, "error.response").object.get("payload").?.object.get("error").?.object.get("code").?.string);
    _ = try wireSubmit(&wire, "steer");
    try testing.expectEqualStrings("steered", wireLast(&wire, "session.message.submit.response").object.get("payload").?.object.get("admission").?.string);
    try wireCancel(&wire, reserved[2]);
    try wireState(&wire, queue_capacity - 1, true);
    _ = try wireSubmit(&wire, "queue");
    reserved[2] = wireLast(&wire, "session.message.submit.response").object.get("payload").?.object.get("run_id").?.string;
    try wireState(&wire, queue_capacity, true);
    for (reserved) |id| try wireCancel(&wire, id);
    try wireState(&wire, 0, true);
    var answer = Payload.init(a);
    for ([_][]const u8{ "interaction_id", "session_id", "run_id", "requested_by", "responded_by" }) |key| try answer.put(key, asked.get(key).?);
    try answer.put("granted", .{ .bool = false });
    try answer.put("choice_id", .{ .string = "deny" });
    const scope = try std.fmt.allocPrint(a, "{s},\"run_id\":\"{s}\"", .{ wire_scope, asked.get("run_id").?.string });
    try wire.send("action.permission.resolve.request", scope, answer.value());
    try wireUntilSettled(&wire, queue_capacity + 2);
    try testing.expectEqual(@as(usize, 2), script.calls);
    try wireState(&wire, 0, false);
    try wire.validate();
}

test "cancelling the executing run promotes reservations in order and retains each run's replay" {
    var script = Script{ .tool_first = true };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    try Session.cast(harness.session.ptr).runtime.setPermissionMode(.ask);
    const first = try harness.submit("first");
    _ = try waitForPrompt(&harness);
    const second = try harness.submit("second");
    const third = try harness.submit("third");
    var refusal = contract.Refusal{};
    _ = try harness.session.cancel(harness.arena.allocator(), first.run_id.?, &refusal);
    for (0..5000) |_| {
        _ = try harness.session.pump(0);
        try harness.collect();
        if (harness.count("run.completed") == 2 and harness.count("run.cancelled") == 1) break;
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
    try testing.expectEqual(@as(usize, 2), harness.count("run.completed"));
    var index: usize = 0;
    const expected = [_][]const u8{ first.run_id.?, second.run_id.?, third.run_id.? };
    for (harness.seen.items) |event| {
        if (!std.mem.eql(u8, event.value.object.get("type").?.string, "run.started")) continue;
        try testing.expectEqualStrings(expected[index], event.value.object.get("run_id").?.string);
        index += 1;
    }
    try testing.expectEqual(expected.len, index);
    for (expected) |id| {
        const replayed = try harness.session.vtable.replay.?(harness.session.ptr, testing.allocator, id, 0, &refusal);
        try testing.expect(replayed == .events);
        defer {
            for (replayed.events) |event| {
                testing.allocator.free(event.line);
                testing.allocator.free(event.run_id);
            }
            testing.allocator.free(replayed.events);
        }
        try testing.expect(replayed.events.len > 1);
        for (replayed.events, 1..) |event, sequence| {
            try testing.expectEqualStrings(id, event.run_id);
            try testing.expectEqual(@as(u64, @intCast(sequence)), event.sequence);
        }
    }
}

test "an allocation failure preparing admission publishes nothing and starts no native work" {
    for (0..6) |fail_index| {
        var script = Script{};
        var harness: Harness = undefined;
        try harness.init(&script);
        defer harness.deinit();
        const session = Session.cast(harness.session.ptr);
        var failing = testing.FailingAllocator.init(testing.allocator, .{ .fail_index = fail_index });
        session.gpa = failing.allocator();
        defer session.gpa = testing.allocator;
        _ = harness.submit("first") catch |err| {
            try testing.expectEqual(error.OutOfMemory, err);
            try testing.expectEqual(@as(usize, 0), script.calls);
            try testing.expectEqual(@as(usize, 0), session.runs.items.len);
            try testing.expectEqual(@as(usize, 0), session.outbox.items.len);
            try testing.expect(session.live() == null);
            continue;
        };
        session.gpa = testing.allocator;
        try harness.untilTerminal();
        break;
    }
}

test "buffered reservation cancellation makes idle activity, model switch and close available without a pump" {
    var script = Script{};
    var wire: Wire = undefined;
    wire.init(&script);
    defer wire.deinit();
    try wireOpen(&wire);
    var reserved: [queue_capacity][]const u8 = undefined;
    for (&reserved) |*id| {
        _ = try wireSubmit(&wire, "queue");
        id.* = wireLast(&wire, "session.message.submit.response").object.get("payload").?.object.get("run_id").?.string;
    }
    for (reserved) |id| try wireCancel(&wire, id);
    try wireState(&wire, 0, false);
    try testing.expectEqual(contract.Activity.idle, wire.endpoint.entries.items[0].session.activity());
    const a = wire.arena.allocator();
    try wire.send("session.model.switch.request", wire_scope, try parseValue(a,
        \\{"session_id":"wire-session","model_id":"scripted/openai-completions@other-model"}
    ));
    try testing.expectEqualStrings("scripted/openai-completions@other-model", wireLast(&wire, "session.model.switch.response").object.get("payload").?.object.get("model_id").?.string);
    var owner = Adapter.init(testing.allocator, wire.owner.options);
    var refusal = contract.Refusal{};
    const session = try owner.adapter().open(a, .{ .participant = "user" }, &refusal);
    var closed = false;
    defer if (!closed) session.teardown();
    var messages = [_]oap_types.Message{.{ .role = .user, .content = .{ .text = "queued" } }};
    const admission = try session.submit(a, &.{ .session_id = session.id(), .messages = &messages, .delivery = .queue }, "close-submit", &refusal);
    _ = try session.cancel(a, admission.run_id.?, &refusal);
    try session.close();
    closed = true;
    try testing.expectEqual(@as(usize, 0), script.calls);
    try wire.validate();
}

test "a session whose client declines user_input is not given the input tool" {
    try testing.expect(offersUserInput(null));
    var declined = try std.json.parseFromSlice(std.json.Value, testing.allocator, "{\"oapx\":{\"user_input\":false}}", .{});
    defer declined.deinit();
    try testing.expect(!offersUserInput(declined.value));
    var offered = try std.json.parseFromSlice(std.json.Value, testing.allocator, "{\"oapx\":{\"user_input\":true}}", .{});
    defer offered.deinit();
    try testing.expect(offersUserInput(offered.value));
}

test "an open's reasoning level sets the loop's thinking level, and minimal, which the loop would round, is refused" {
    var script = Script{};
    var owner = Adapter.init(testing.allocator, .{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &script },
        .models = &scripted_models,
        .initial_model_id = test_model.id,
        .tools = &echo_tools,
    });
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var refusal = contract.Refusal{};
    const opened = try owner.adapter().open(arena.allocator(), .{ .participant = "user", .reasoning_level = "xhigh" }, &refusal);
    defer opened.teardown();
    const reported = try opened.state(arena.allocator(), &refusal);
    try testing.expectEqualStrings("xhigh", reported.reasoning_level.?);
    try testing.expectError(error.UnsupportedFeature, owner.adapter().open(arena.allocator(), .{ .participant = "user", .reasoning_level = "minimal" }, &refusal));
    try testing.expectEqualStrings("reasoning_level", refusal.field);
}

test "a live update changes the loop's thinking level between runs and is refused during one" {
    var script = Script{ .wait_for_cancel = true };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    const a = harness.arena.allocator();
    var refusal = contract.Refusal{};
    const updater = harness.session.vtable.update_settings.?;

    const updated = try updater(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .reasoning_level = "max" }, &refusal);
    try testing.expectEqualStrings("max", updated.response.reasoning_level.?);
    try testing.expectEqualStrings("low", updated.response.previous_reasoning_level.?);
    try testing.expectEqualStrings("max", updated.state.reasoning_level.?);

    try testing.expectError(error.UnsupportedFeature, updater(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .reasoning_level = "minimal" }, &refusal));
    try testing.expectEqualStrings("reasoning_level", refusal.field);
    refusal = .{};
    try testing.expectError(error.UnsupportedFeature, updater(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .compaction_policy_json = "{\"kind\":\"share\",\"share_percent\":0}" }, &refusal));
    try testing.expectEqualStrings(contract.feature_compaction_policy, refusal.feature);
    try testing.expectEqualStrings(contract.reason_unsatisfiable, refusal.reason);

    const admitted = try harness.submit("wait");
    try testing.expectError(error.RunActive, updater(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .reasoning_level = "off" }, &refusal));
    _ = try harness.session.cancel(a, admitted.run_id.?, &refusal);
    try harness.untilTerminal();
    const after = try harness.session.state(a, &refusal);
    try testing.expectEqualStrings("max", after.reasoning_level.?);
}

fn compactRequest(harness: *Harness, focus: ?[]const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
    return harness.session.vtable.compact.?(harness.session.ptr, harness.arena.allocator(), &.{ .session_id = harness.session.id(), .focus = focus }, "compact-envelope", refusal);
}

fn kinds(harness: *Harness, allocator: std.mem.Allocator) ![]const []const u8 {
    const names = try allocator.alloc([]const u8, harness.seen.items.len);
    for (harness.seen.items, names) |parsed, *name| name.* = parsed.value.object.get("type").?.string;
    return names;
}

fn ofType(harness: *Harness, kind: []const u8) ?std.json.ObjectMap {
    for (harness.seen.items) |parsed| {
        if (std.mem.eql(u8, parsed.value.object.get("type").?.string, kind)) return parsed.value.object.get("payload").?.object;
    }
    return null;
}

test "a compaction on an idle session is a run of its own that settles compacted with the loop's summary" {
    var script = Script{ .reply = "the session so far" };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    _ = try harness.submit("remember the parser");
    try harness.untilTerminal();
    harness.reset();

    var refusal = contract.Refusal{};
    const admitted = try compactRequest(&harness, "the parser", &refusal);
    try testing.expectEqual(oap_types.Admission.started, admitted.admission);
    try harness.untilTerminal();
    const names = try kinds(&harness, harness.arena.allocator());
    try testing.expectEqual(@as(usize, 4), names.len);
    for ([_][]const u8{ "run.started", "run.compaction.started", "run.compaction.ended", "run.completed" }, names) |want, got| try testing.expectEqualStrings(want, got);
    try testing.expectEqualStrings("requested", ofType(&harness, "run.compaction.started").?.get("reason").?.string);
    const ended = ofType(&harness, "run.compaction.ended").?;
    try testing.expectEqualStrings("completed", ended.get("outcome").?.string);
    const completed = ofType(&harness, "run.completed").?;
    try testing.expectEqualStrings("compacted", completed.get("stop_reason").?.string);
    try testing.expectEqualStrings(ended.get("summary").?.object.get("content").?.string, completed.get("final_response").?.object.get("content").?.string);
    try testing.expectEqual(@as(usize, 2), script.calls);
}

const SavedTranscripts = struct {
    saved: usize = 0,
    session_id: [64]u8 = undefined,
    session_len: usize = 0,
    messages: usize = 0,

    fn save(ctx: *anyopaque, allocator: std.mem.Allocator, session_id: []const u8, index: usize, history: []const ai_types.Message) ?[]u8 {
        const self: *SavedTranscripts = @ptrCast(@alignCast(ctx));
        self.saved += 1;
        self.session_len = @min(session_id.len, self.session_id.len);
        @memcpy(self.session_id[0..self.session_len], session_id[0..self.session_len]);
        self.messages = history.len;
        return std.fmt.allocPrint(allocator, "/saved/{s}-compaction-{d}.jsonl", .{ session_id, index }) catch null;
    }
};

test "a requested compaction saves the session's transcript through the store and hands its path to the loop, and one with nothing to compact saves nothing" {
    var script = Script{ .reply = "the session so far" };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    var store = SavedTranscripts{};
    harness.owner.transcripts = .{ .ctx = &store, .save = SavedTranscripts.save };
    var empty_refusal = contract.Refusal{};
    try testing.expectError(error.InvalidSubmission, compactRequest(&harness, "nothing yet", &empty_refusal));
    try testing.expectEqual(@as(usize, 0), store.saved);
    _ = try harness.submit("remember the parser");
    try harness.untilTerminal();
    harness.reset();

    var refusal = contract.Refusal{};
    _ = try compactRequest(&harness, "the parser", &refusal);
    try harness.untilTerminal();
    try testing.expectEqual(@as(usize, 1), store.saved);
    try testing.expectEqualStrings(harness.session.id(), store.session_id[0..store.session_len]);
    try testing.expect(store.messages > 0);
    const runtime = Session.cast(harness.session.ptr).runtime;
    try testing.expectEqual(@as(usize, 1), runtime.run_transcripts.items.len);
    try testing.expect(std.mem.endsWith(u8, runtime.run_transcripts.items[0], "-compaction-1.jsonl"));
}

fn refuseTranscript(ctx: *anyopaque, allocator: std.mem.Allocator, session_id: []const u8, index: usize, history: []const ai_types.Message) ?[]u8 {
    const saved: *usize = @ptrCast(@alignCast(ctx));
    saved.* += 1;
    _ = allocator;
    _ = session_id;
    _ = index;
    _ = history;
    return null;
}

test "a requested compaction whose transcript the store cannot save fails and keeps the history" {
    var script = Script{ .reply = "the session so far" };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    var attempts: usize = 0;
    harness.owner.transcripts = .{ .ctx = &attempts, .save = refuseTranscript };
    _ = try harness.submit("remember the parser");
    try harness.untilTerminal();
    harness.reset();
    const runtime = Session.cast(harness.session.ptr).runtime;
    const before = runtime.history().len;
    const calls = script.calls;

    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, compactRequest(&harness, "the parser", &refusal));
    try testing.expectEqualStrings("the transcript could not be saved, so the history was kept", refusal.message);
    try testing.expectEqual(@as(usize, 1), attempts);
    try testing.expect(ofType(&harness, "run.compaction.started") == null);
    try testing.expectEqual(calls, script.calls);
    try testing.expectEqual(before, runtime.history().len);
    try testing.expect(!agent.compaction.isCompacted(runtime.history()));
    try testing.expectEqual(@as(usize, 0), runtime.run_transcripts.items.len);
}

test "a compaction is refused for what the loop cannot do, and on a busy session it waits its turn" {
    var script = Script{ .wait_for_cancel = true };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    const a = harness.arena.allocator();
    const compactor = harness.session.vtable.compact.?;
    var refusal = contract.Refusal{};
    try testing.expectError(error.InvalidSubmission, compactRequest(&harness, null, &refusal));
    refusal = .{};
    try testing.expectError(error.UnsupportedFeature, compactor(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .continue_run = true }, "c", &refusal));
    try testing.expectEqualStrings("continue", refusal.field);
    refusal = .{};
    try testing.expectError(error.UnsupportedFeature, compactor(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .delivery = .steer }, "c", &refusal));
    try testing.expectEqualStrings("session.message.delivery.steer", refusal.feature);
    try testing.expectError(error.RunNotFound, compactor(harness.session.ptr, a, &.{ .session_id = "other" }, "c", &refusal));
    const admitted = try harness.submit("wait");
    const waiting = try compactRequest(&harness, null, &refusal);
    try testing.expectEqual(oap_types.Admission.queued, waiting.admission);
    try testing.expectEqualStrings("session_busy", waiting.delivery_resolution.?);
    _ = try harness.session.cancel(a, waiting.run_id.?, &refusal);
    _ = try harness.session.cancel(a, admitted.run_id.?, &refusal);
    try harness.untilTerminal();
    try testing.expectEqual(@as(usize, 0), harness.count("run.compaction.started"));
}

test "a compaction queued behind a run starts once the run settles and opens with its compaction" {
    var script = Script{ .reply = "the session so far" };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    var refusal = contract.Refusal{};
    _ = try harness.submit("remember the parser");
    const queued = try harness.session.vtable.compact.?(harness.session.ptr, harness.arena.allocator(), &.{ .session_id = harness.session.id(), .delivery = .queue }, "compact-envelope", &refusal);
    try testing.expectEqual(oap_types.Admission.queued, queued.admission);
    var waits: usize = 0;
    while (harness.count("run.completed") < 2 and waits < 5000) : (waits += 1) {
        _ = try harness.session.pump(0);
        try harness.collect();
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
    try testing.expectEqual(@as(usize, 2), harness.count("run.completed"));
    var after_start = false;
    for (harness.seen.items) |parsed| {
        const event = parsed.value.object;
        if (!std.mem.eql(u8, event.get("run_id").?.string, queued.run_id.?)) continue;
        const kind = event.get("type").?.string;
        if (std.mem.eql(u8, kind, "run.started")) {
            after_start = true;
            continue;
        }
        try testing.expect(after_start);
        try testing.expectEqualStrings("run.compaction.started", kind);
        break;
    }
}

test "a token threshold set live compacts inside the next run between its turns" {
    var script = Script{ .reply = "noted", .tool_first = true };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    const a = harness.arena.allocator();
    var refusal = contract.Refusal{};
    const updated = try harness.session.vtable.update_settings.?(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .compaction_policy_json = "{\"kind\":\"tokens\",\"tokens\":1}" }, &refusal);
    try testing.expectEqualStrings("{\"kind\":\"tokens\",\"tokens\":1}", updated.state.compaction_policy_json.?);
    try testing.expect(updated.response.previous_compaction_policy_json == null);
    _ = try harness.submit("use the tool");
    try harness.untilTerminal();
    const names = try kinds(&harness, a);
    var completed_call: ?usize = null;
    var started: ?usize = null;
    for (names, 0..) |name, index| {
        if (std.mem.eql(u8, name, "action.call.completed")) completed_call = index;
        if (std.mem.eql(u8, name, "run.compaction.started")) started = index;
    }
    try testing.expect(started.? > completed_call.?);
    try testing.expectEqualStrings("run.compaction.ended", names[started.? + 1]);
    try testing.expectEqualStrings("threshold", ofType(&harness, "run.compaction.started").?.get("reason").?.string);
    try testing.expectEqualStrings("completed", ofType(&harness, "run.compaction.ended").?.get("outcome").?.string);
    try testing.expectEqualStrings("run.completed", names[names.len - 1]);
    try testing.expectEqual(@as(usize, 3), script.calls);
}

test "an open takes a compaction policy and the state reports it, and off never compacts" {
    var script = Script{};
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    const a = harness.arena.allocator();
    var refusal = contract.Refusal{};
    const opened = try harness.owner.adapter().open(a, .{ .participant = "user", .compaction_policy_json = "{\"kind\":\"off\"}" }, &refusal);
    defer opened.teardown();
    try testing.expectEqualStrings("{\"kind\":\"off\"}", (try opened.state(a, &refusal)).compaction_policy_json.?);
    try testing.expectError(error.UnsupportedFeature, harness.owner.adapter().open(a, .{ .participant = "user", .compaction_policy_json = "{\"kind\":\"tokens\",\"tokens\":0}" }, &refusal));
    try testing.expectEqualStrings(contract.feature_compaction_policy, refusal.feature);
}

test "a compaction requested over the wire and a policy updated live leave a trace the validator accepts" {
    var script = Script{ .reply = "the session so far" };
    var wire: Wire = undefined;
    wire.init(&script);
    defer wire.deinit();
    try wireOpen(&wire);
    const a = wire.arena.allocator();
    _ = try wireSubmit(&wire, "auto");
    try wireUntilSettled(&wire, 1);
    try wire.send("session.settings.update.request", wire_scope, try parseValue(a, "{\"session_id\":\"wire-session\",\"compaction_policy\":{\"kind\":\"share\",\"share_percent\":90}}"));
    _ = try wire.wait("session.settings.update.response");
    try wire.send("session.compact.request", wire_scope, try parseValue(a, "{\"session_id\":\"wire-session\",\"focus\":\"the parser\"}"));
    _ = try wire.wait("session.compact.response");
    try wireUntilSettled(&wire, 2);
    try testing.expectEqualStrings("completed", wireLast(&wire, "run.compaction.ended").object.get("payload").?.object.get("outcome").?.string);
    try wire.validate();
}

test "a share policy is measured against the window of the model a run starts on" {
    var script = Script{};
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    const a = harness.arena.allocator();
    var refusal = contract.Refusal{};
    _ = try harness.session.vtable.update_settings.?(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .compaction_policy_json = "{\"kind\":\"share\",\"share_percent\":50}" }, &refusal);
    const live: *Session = @ptrCast(@alignCast(harness.session.ptr));
    try testing.expectEqual(@as(?u64, agent.compaction.shareAt(test_model.context_window, 50)), live.runtime.local_agent.?._auto_compact_at);
    _ = try harness.session.vtable.switch_model.?(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .model_id = "scripted/openai-completions@other-model" }, &refusal);
    _ = try harness.submit("after the switch");
    try harness.untilTerminal();
    try testing.expectEqual(@as(?u64, agent.compaction.shareAt(other_model.context_window, 50)), live.runtime.local_agent.?._auto_compact_at);
}

test "a steer joins the running loop after its tool result and is applied before the guided turn" {
    var script = Script{ .tool_first = true, .reply = "changed course" };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    held_tool.store(true, .release);
    defer held_tool.store(false, .release);
    const started = try harness.submit("start");
    var refusal = contract.Refusal{};
    const steered = try harness.steer("change course", started.run_id, &refusal);
    held_tool.store(false, .release);
    try testing.expectEqual(oap_types.Admission.steered, steered.admission);
    try testing.expectEqual(oap_types.EffectiveDelivery.steer, steered.effective_delivery);
    try testing.expectEqualStrings(started.run_id.?, steered.run_id.?);
    try harness.untilTerminal();

    const names = try kinds(&harness, harness.arena.allocator());
    var applied_at: ?usize = null;
    var completed_call_at: ?usize = null;
    var guided_delta_at: ?usize = null;
    for (names, 0..) |name, index| {
        if (std.mem.eql(u8, name, "run.steer.applied")) applied_at = index;
        if (std.mem.eql(u8, name, "action.call.completed")) completed_call_at = index;
        if (std.mem.eql(u8, name, "content.delta") and guided_delta_at == null and applied_at != null) guided_delta_at = index;
    }
    try testing.expect(completed_call_at.? < applied_at.?);
    try testing.expect(applied_at.? < guided_delta_at.?);
    const applied = ofType(&harness, "run.steer.applied").?;
    try testing.expectEqualStrings("steer-envelope", applied.get("request_id").?.string);
    try testing.expectEqualStrings(steered.submission_id, applied.get("submission_id").?.string);
    try testing.expectEqualStrings("tool_result", applied.get("boundary").?.string);
    try testing.expect(script.saw_steer.load(.acquire));
    try testing.expectEqualStrings("run.completed", harness.terminal().?.object.get("type").?.string);
    try testing.expectEqual(@as(usize, 0), harness.count("run.steer.dropped"));
}

test "a steer still waiting when its run is cancelled is dropped before the terminal" {
    var script = Script{ .wait_for_cancel = true };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    const started = try harness.submit("start");
    var refusal = contract.Refusal{};
    _ = try harness.steer("change course", null, &refusal);
    _ = try harness.session.cancel(harness.arena.allocator(), started.run_id.?, &refusal);
    try harness.untilTerminal();

    const names = try kinds(&harness, harness.arena.allocator());
    var dropped_at: ?usize = null;
    var cancelled_at: ?usize = null;
    for (names, 0..) |name, index| {
        if (std.mem.eql(u8, name, "run.steer.dropped")) dropped_at = index;
        if (std.mem.eql(u8, name, "run.cancelled")) cancelled_at = index;
    }
    try testing.expect(dropped_at.? < cancelled_at.?);
    const dropped = ofType(&harness, "run.steer.dropped").?;
    try testing.expectEqualStrings("steer-envelope", dropped.get("request_id").?.string);
    try testing.expectEqualStrings("run_terminated", dropped.get("reason").?.object.get("code").?.string);
    try testing.expectEqual(@as(usize, 0), harness.count("run.steer.applied"));
}

test "a steer with no running loop to take it names why" {
    var script = Script{};
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.InvalidSteerTarget, harness.steer("change course", null, &refusal));
    try testing.expectEqualStrings("no_active_run", refusal.reason);
    refusal = .{};
    try testing.expectError(error.InvalidSteerTarget, harness.steer("change course", "run-unknown", &refusal));
    try testing.expectEqualStrings("unknown_target", refusal.reason);
    const finished = try harness.submit("start");
    try harness.untilTerminal();
    refusal = .{};
    try testing.expectError(error.InvalidSteerTarget, harness.steer("change course", finished.run_id, &refusal));
    try testing.expectEqualStrings("terminal", refusal.reason);
}

fn updateWith(harness: *Harness, extensions: []const u8, refusal: *contract.Refusal) contract.Failure!contract.Updated {
    const request = oap_types.SessionSettingsUpdateRequest{ .session_id = harness.session.id(), .reasoning_level = "low", .extensions_json = extensions };
    return harness.session.vtable.update_settings.?(harness.session.ptr, harness.arena.allocator(), &request, refusal);
}

test "a live update sets the context window, a null clears it, and a refused update changes none of its keys, the reasoning level included" {
    var script = Script{};
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();
    const runtime = Session.cast(harness.session.ptr).runtime;
    var refusal = contract.Refusal{};

    _ = try updateWith(&harness, "{\"oapx\":{\"context_window\":4096}}", &refusal);
    try testing.expectEqual(@as(?u32, 4096), runtime.contextWindowOverride());
    _ = try updateWith(&harness, "{\"oapx\":{\"context_window\":null}}", &refusal);
    try testing.expectEqual(@as(?u32, null), runtime.contextWindowOverride());

    try runtime.setThinkingLevel(.high);
    try testing.expectError(error.InvalidSubmission, updateWith(&harness, "{\"oapx\":{\"context_window\":2048,\"output\":4000000000}}", &refusal));
    try testing.expectEqual(@as(?u32, null), runtime.contextWindowOverride());
    try testing.expectEqual(ai_types.ThinkingLevel.high, runtime.thinkingLevel());
}
