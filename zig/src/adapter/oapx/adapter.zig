const std = @import("std");
const contract = @import("contract");
const oap_types = @import("oap_types");
const compat = @import("compat");
const json_encode = @import("json_encode");
const ai_types = @import("ai_types");
const tui_runtime = @import("tui_runtime");
const tui_session = @import("tui_session");
const model_ref = @import("model_ref");

pub const endpoint_id = "oapx.agent";
pub const capability_revision = "oapx-agent-v1";

pub const journal_capacity: usize = 1 << 16;

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
    .{ .key = "run.streaming", .level = .native },
    .{ .key = "run.status", .level = .native },
    .{ .key = "run.cancel", .level = .native, .reason = "cancelling a run leaves its session open" },
    .{ .key = "run.replay", .level = .degraded, .reason = "only the session's latest run is retained, up to 65536 events, in process memory" },
    .{ .key = "content.reasoning", .level = .native },
    .{ .key = "action.tools", .level = .native, .reason = "the agent loop runs its own workspace tools" },
    .{ .key = "action.tools.execute", .level = .native, .reason = "the agent loop runs its own workspace tools" },
    .{ .key = contract.feature_tools_list, .level = .native },
    .{ .key = contract.feature_models_list, .level = .native, .reason = "the catalog is the one the terminal UI offers" },
    .{ .key = contract.feature_model_switch, .level = .native },
};

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = "oapx agent loop", .version = protocol_version, .adapter = "in-process" },
    .capability_revision = capability_revision,
    .features = &features,
};

fn wallClock() i64 {
    return compat.time.nowMillis();
}

pub const Adapter = struct {
    allocator: std.mem.Allocator,
    options: tui_runtime.TuiRuntimeOptions,
    ids: u64 = 0,
    now_ms: *const fn () i64 = wallClock,

    pub fn init(allocator: std.mem.Allocator, options: tui_runtime.TuiRuntimeOptions) Adapter {
        var runtime_options = options;
        runtime_options.run_async = true;
        runtime_options.generate_titles = false;
        return .{ .allocator = allocator, .options = runtime_options };
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
    next_sequence: u64 = 1,
    status: oap_types.RunStatus = .running,
    terminal: bool = false,
    model_id: []const u8 = "",
    message_id: []const u8 = "",
    text: std.ArrayList(u8) = .empty,
    stop_reason: []const u8 = "end_turn",
    error_text: std.ArrayList(u8) = .empty,
};

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
    updated_at_ms: i64,
    run: ?*Run = null,
    outbox: std.ArrayList(Journaled) = .empty,
    journal: std.ArrayList(Journaled) = .empty,

    fn create(owner: *Adapter, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const gpa = owner.allocator;
        const self = try gpa.create(Session);
        errdefer gpa.destroy(self);
        const runtime = try gpa.create(tui_runtime.TuiRuntime);
        errdefer gpa.destroy(runtime);
        runtime.* = tui_runtime.TuiRuntime.init(gpa, sessionOptions(owner.options, request.metadata)) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return refusal.fail(error.BackendFailed, @errorName(err)),
        };
        errdefer runtime.deinit();
        self.* = .{ .owner = owner, .gpa = gpa, .keep = std.heap.ArenaAllocator.init(gpa), .id = "", .participant = "", .runtime = runtime, .updated_at_ms = owner.now_ms() };
        errdefer self.keep.deinit();
        const keep = self.keep.allocator();
        self.participant = try keep.dupe(u8, request.participant);
        self.id = if (request.session_id.len > 0) try keep.dupe(u8, request.session_id) else try owner.nextID(keep, "session");
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
    };

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
        if (self.run) |run| releaseRun(gpa, run);
        for (self.outbox.items) |entry| gpa.free(entry.line);
        self.outbox.deinit(gpa);
        self.forgetJournal();
        self.journal.deinit(gpa);
        self.runtime.deinit();
        gpa.destroy(self.runtime);
        self.keep.deinit();
        gpa.destroy(self);
    }

    fn forgetJournal(self: *Session) void {
        for (self.journal.items) |entry| self.gpa.free(entry.line);
        self.journal.clearRetainingCapacity();
    }

    fn releaseRun(gpa: std.mem.Allocator, run: *Run) void {
        run.text.deinit(gpa);
        run.error_text.deinit(gpa);
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

    fn snapshot(self: *Session, arena: std.mem.Allocator) contract.Failure!oap_types.SessionState {
        var result = oap_types.SessionState{
            .session_id = self.id,
            .status = .idle,
            .current_model_id = try self.currentModelRef(arena),
            .updated_at_ms = self.updated_at_ms,
        };
        if (self.live()) |run| {
            const entries = try arena.alloc(oap_types.ActiveRun, 1);
            entries[0] = .{ .run_id = run.id, .status = run.status, .relationship = "primary", .as_of_sequence = run.next_sequence - 1 };
            result.active_runs = entries;
            result.active_run_id = run.id;
            result.status = .running;
        }
        return result;
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const self = cast(ptr);
        try contract.refuseUnadvertisedControls(descriptor, request, refusal);
        if (request.session_id.len == 0 or request.messages.len == 0) return error.InvalidSubmission;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        if (self.live() != null) return error.RunActive;

        const text = try userText(arena, request.messages);
        if (text.len == 0) return refusal.fail(error.InvalidSubmission, "the submission carries no user text");

        const keep = self.keep.allocator();
        const run_id = try self.owner.nextID(keep, "run");
        const model_id = (try self.currentModelRef(keep)) orelse "";
        self.runtime.submitTurn(text) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            error.NoModelConfigured => return refusal.fail(error.BackendFailed, "no model is selected"),
            else => return refusal.fail(error.BackendFailed, @errorName(err)),
        };

        if (self.run) |previous| {
            releaseRun(self.gpa, previous);
            self.run = null;
        }
        self.forgetJournal();
        const run = try keep.create(Run);
        run.* = .{ .id = run_id, .model_id = model_id };
        self.run = run;
        self.updated_at_ms = self.owner.now_ms();

        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        var started = Payload.init(a);
        try started.run(self, run);
        try started.put("status", .{ .string = "running" });
        if (model_id.len > 0) try started.put("model_id", .{ .string = model_id });
        try started.put("started_at_ms", .{ .integer = self.owner.now_ms() });
        try self.emit(run, "run.started", started.value(), false);

        const message_ids = try arena.alloc([]const u8, request.messages.len);
        for (request.messages, message_ids) |message, *slot| {
            slot.* = if (message.id) |carried| carried else try self.owner.nextID(arena, "message");
        }
        return .{
            .session_id = self.id,
            .accepted = true,
            .submission_id = try self.owner.nextID(arena, "submission"),
            .requested_delivery = request.delivery,
            .effective_delivery = .start,
            .delivery_resolution = "session_idle",
            .admission = .started,
            .run_id = run.id,
            .status = .running,
            .model_id = if (model_id.len > 0) model_id else null,
            .message_ids = message_ids,
        };
    }

    fn resolve(ptr: *anyopaque, arena: std.mem.Allocator, resolution: contract.Resolution, refusal: *contract.Refusal) contract.Failure!void {
        _ = ptr;
        _ = arena;
        _ = resolution;
        _ = refusal;
        return error.InteractionNotFound;
    }

    fn cancel(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
        _ = refusal;
        const self = cast(ptr);
        const run = self.run orelse return error.RunNotFound;
        if (!std.mem.eql(u8, run.id, run_id)) return error.RunNotFound;
        const owned_run_id = try arena.dupe(u8, run.id);
        if (run.terminal) {
            if (run.status == .cancelled) return .{ .session_id = self.id, .run_id = owned_run_id, .accepted = true, .status = .cancelled };
            return error.RunTerminal;
        }
        if (run.status == .cancelling) return .{ .session_id = self.id, .run_id = owned_run_id, .accepted = true, .status = .cancelling };
        run.status = .cancelling;
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
        const stream = self.runtime.streamEvents();
        var moved = false;
        while (stream.poll()) |event| {
            var owned = event;
            defer owned.deinit(self.gpa);
            try self.translate(owned);
            moved = true;
        }
        return moved;
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
                var ended = try self.callPayload(a, run, payload.tool_call_id.slice(), payload.tool_name.slice());
                if (payload.is_error) {
                    var failure = Payload.init(a);
                    try failure.put("code", .{ .string = "tool_failed" });
                    try failure.put("message", .{ .string = try errorText(a, payload.result_json.slice()) });
                    try ended.put("error", failure.value());
                    try self.emit(run, "action.call.failed", ended.value(), false);
                } else {
                    try ended.put("result", try jsonOrString(a, payload.result_json.slice()));
                    try self.emit(run, "action.call.completed", ended.value(), false);
                }
            },
            .turn_end => |payload| run.stop_reason = stopReasonText(payload.stop_reason),
            .@"error" => |payload| {
                run.error_text.clearRetainingCapacity();
                try run.error_text.appendSlice(self.gpa, payload.message.slice());
            },
            .agent_end => |payload| try self.settle(a, run, payload.reason),
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
        var payload = Payload.init(a);
        try payload.run(self, run);
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
        if (std.mem.startsWith(u8, kind, "action.call.")) {
            if (payload.object.get("tool_call_id")) |tool_call_id| try envelope.put("tool_call_id", tool_call_id);
        }
        try envelope.put("capability_revision", .{ .string = capability_revision });
        const line = try json_encode.valueAlloc(self.gpa, envelope.value());
        errdefer self.gpa.free(line);
        const kept = try self.gpa.dupe(u8, line);
        errdefer self.gpa.free(kept);
        try self.outbox.ensureUnusedCapacity(self.gpa, 1);
        try self.journal.ensureUnusedCapacity(self.gpa, 1);
        self.outbox.appendAssumeCapacity(.{ .line = line, .run_id = run.id, .sequence = sequence });
        if (self.journal.items.len == journal_capacity) self.gpa.free(self.journal.orderedRemove(0).line);
        self.journal.appendAssumeCapacity(.{ .line = kept, .run_id = run.id, .sequence = sequence });
        run.next_sequence += 1;
        self.updated_at_ms = now;
        if (!terminal) return;
        run.terminal = true;
        if (std.mem.eql(u8, kind, "run.completed")) run.status = .completed;
        if (std.mem.eql(u8, kind, "run.failed")) run.status = .failed;
        if (std.mem.eql(u8, kind, "run.cancelled")) run.status = .cancelled;
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
        const run = self.run orelse return error.RunNotFound;
        if (!std.mem.eql(u8, run.id, run_id)) return error.RunNotFound;
        const latest = run.next_sequence - 1;
        if (after > latest) return error.ReplayCursorFuture;
        const oldest: u64 = if (self.journal.items.len > 0) self.journal.items[0].sequence else 0;
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
            if (kept.sequence <= after) continue;
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
        return if (self.live() != null) .running else .idle;
    }

    fn close(ptr: *anyopaque, force: bool) contract.Failure!void {
        const self = cast(ptr);
        if (!force and self.live() != null) return error.RunActive;
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
        _ = refusal;
        const self = cast(ptr);
        if (request.session_id.len > 0 and !std.mem.eql(u8, request.session_id, self.id)) return error.InvalidSubmission;
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
        if (self.live() != null) return error.RunActive;
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
    if (fields.get("workspace_root")) |value| {
        if (value == .string and value.string.len > 0) options.workspace_root = value.string;
    }
    return options;
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
    wait_for_cancel: bool = false,
};

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
    _ = context;
    const script: *Script = @ptrCast(@alignCast(ctx.?));
    script.calls += 1;
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
        const content = [_]ai_types.AssistantContent{.{ .tool_call = .{ .id = "call-1", .name = "echo_tool", .arguments_json = "{\"say\":\"hi\"}" } }};
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
        self.arena.deinit();
    }

    fn submit(self: *Harness, text: []const u8) !oap_types.MessageSubmitResponse {
        var refusal = contract.Refusal{};
        var parts = [_]oap_types.ContentPart{.{ .text = text }};
        var messages = [_]oap_types.Message{.{ .role = .user, .content = .{ .parts = &parts } }};
        return self.session.submit(self.arena.allocator(), &.{ .session_id = self.session.id(), .messages = &messages, .delivery = .auto }, &refusal);
    }

    fn collect(self: *Harness) !void {
        var out = std.ArrayList(contract.Event).empty;
        defer out.deinit(testing.allocator);
        try self.session.drain(testing.allocator, &out);
        for (out.items) |event| {
            defer testing.allocator.free(event.line);
            defer testing.allocator.free(event.run_id);
            try self.seen.append(testing.allocator, try std.json.parseFromSlice(std.json.Value, testing.allocator, event.line, .{}));
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

test "a second submit while a run is live is refused as run_active" {
    var script = Script{ .wait_for_cancel = true };
    var harness: Harness = undefined;
    try harness.init(&script);
    defer harness.deinit();

    const admitted = try harness.submit("wait");
    try testing.expectError(error.RunActive, harness.submit("too soon"));
    var refusal = contract.Refusal{};
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

    const catalog = try harness.session.vtable.models.?(harness.session.ptr, a, &.{ .session_id = harness.session.id() }, &refusal);
    try testing.expectEqual(@as(usize, 2), catalog.response.models.len);
    try testing.expectEqualStrings("scripted/openai-completions@scripted-model", catalog.response.current_model_id.?);
    try testing.expect(catalog.response.models[0].default);

    const switched = try harness.session.vtable.switch_model.?(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .model_id = "scripted/openai-completions@other-model" }, &refusal);
    try testing.expectEqualStrings("scripted/openai-completions@other-model", switched.state.current_model_id.?);
    try testing.expectEqualStrings("scripted/openai-completions@scripted-model", switched.response.previous_model_id.?);

    try testing.expectError(error.ModelNotFound, harness.session.vtable.switch_model.?(harness.session.ptr, a, &.{ .session_id = harness.session.id(), .model_id = "scripted/openai-completions@missing" }, &refusal));
    try testing.expectEqualStrings("scripted/openai-completions@missing", refusal.model_id);
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

test "an open's oapx metadata sets the session's thinking level, window, permission mode and workspace, and anything else is ignored" {
    const base = tui_runtime.TuiRuntimeOptions{ .thinking_level = .low, .permission_mode = .bypass, .workspace_root = "/base" };
    var parsed = try std.json.parseFromSlice(std.json.Value, testing.allocator,
        \\{"oapx":{"thinking_level":"high","context_window":1000000,"permission_mode":"ask","workspace_root":"/work","unknown":1}}
    , .{});
    defer parsed.deinit();
    const applied = sessionOptions(base, parsed.value);
    try testing.expectEqual(ai_types.ThinkingLevel.high, applied.thinking_level);
    try testing.expectEqual(@as(?u32, 1_000_000), applied.context_window);
    try testing.expectEqual(tui_runtime.PermissionMode.ask, applied.permission_mode);
    try testing.expectEqualStrings("/work", applied.workspace_root);

    var wrong = try std.json.parseFromSlice(std.json.Value, testing.allocator,
        \\{"oapx":{"thinking_level":"loud","context_window":-1,"permission_mode":7}}
    , .{});
    defer wrong.deinit();
    const kept = sessionOptions(base, wrong.value);
    try testing.expectEqual(ai_types.ThinkingLevel.low, kept.thinking_level);
    try testing.expectEqual(@as(?u32, null), kept.context_window);
    try testing.expectEqual(tui_runtime.PermissionMode.bypass, kept.permission_mode);
    try testing.expectEqualStrings("/base", kept.workspace_root);
    try testing.expectEqual(tui_runtime.PermissionMode.bypass, sessionOptions(base, null).permission_mode);
}
