const std = @import("std");
const contract = @import("contract");
const oap_types = @import("oap_types");
const compat = @import("compat");
const json_encode = @import("json_encode");
const jsonschema = @import("jsonschema");

pub const endpoint_id = "reference.memory";
pub const capability_revision = "reference-memory-oapx-v1";
pub const model_primary = "reference-model-a";
pub const model_secondary = "reference-model-b";
pub const journal_capacity = 64;

const protocol_name = "open-agent-protocol";
const protocol_version = "0.1";
const profile = "open-agent-protocol.agent-control-core";
const scripted_tool = "scripted_tool";
const scripted_owner = "reference-adapter";
const scripted_source = "reference-native";
const fixed_result = "{\"ok\":true}";
const golden_arguments = "{\"operation\":\"golden\"}";
const scripted_schema = "{\"type\":\"object\",\"properties\":{\"operation\":{\"type\":\"string\"}}}";
const open_time_reason = "the oapx adapter contract carries no open-time tool or source list";

const features = [_]contract.Feature{
    .{ .key = "action.permissions", .level = .emulated, .reason = "the reference adapter exposes an interactive scripted gate" },
    .{ .key = contract.feature_tool_sources_attach, .level = .unavailable, .reason = open_time_reason },
    .{ .key = "action.tools", .level = .emulated, .reason = "the reference adapter projects the scripted tool lifecycle" },
    .{ .key = "action.tools.execute", .level = .emulated, .reason = "the reference adapter executes a fixed deterministic script" },
    .{ .key = contract.feature_tools_list, .level = .emulated, .reason = "the reference catalog is the scripted tool" },
    .{ .key = contract.feature_tools_provide, .level = .unavailable, .reason = open_time_reason },
    .{ .key = "capabilities", .level = .native },
    .{ .key = contract.feature_models_list, .level = .native, .reason = "the reference adapter serves its fixed catalog, which is exactly the set its model gate admits" },
    .{ .key = "protocol.initialize", .level = .native },
    .{ .key = "run.cancel", .level = .emulated, .reason = "run-target API is implemented over a one-active-run session" },
    .{ .key = "run.instructions", .level = .emulated, .reason = "instructions are prepended to the scripted text so their effect is observable" },
    .{ .key = "run.model_selection", .level = .emulated, .scope = "run", .reason = "the reference adapter runs no model; it echoes a selection from a fixed catalog for one run" },
    .{ .key = "run.reconciliation", .level = .native },
    .{ .key = "run.replay", .level = .degraded, .reason = "older cursors can expire and no cross-process replay is claimed" },
    .{ .key = "run.resume", .level = .degraded, .reason = "reattachment and replay use a bounded process-memory journal" },
    .{ .key = "run.status", .level = .native },
    .{ .key = "run.streaming", .level = .native },
    .{ .key = "run.structured_output", .level = .emulated, .reason = "the scripted result is fixed, so only a schema that object satisfies is admitted" },
    .{ .key = "run.tool_selection", .level = .emulated, .scope = "run", .reason = "the policy filters the scripted tool and is not retained past the run" },
    .{ .key = "session.message.delivery.auto", .level = .native },
    .{ .key = "session.message.delivery.queue", .level = .emulated, .reason = "a busy session reserves one second run and promotes it when the started run settles" },
    .{ .key = contract.feature_submit, .level = .native },
    .{ .key = contract.feature_model_switch, .level = .emulated, .reason = "the reference adapter changes the session default within its fixed catalog" },
    .{ .key = "session.open", .level = .native },
    .{ .key = contract.feature_open_subscribe, .level = .native, .reason = "the journal exists from the open, so a subscription registered there misses nothing" },
    .{ .key = "session.state", .level = .native },
    .{ .key = "user_input", .level = .emulated, .reason = "the reference adapter exposes an interactive scripted gate" },
};

const declared_sources = [_]oap_types.ToolSourceDescriptor{
    .{ .id = scripted_source, .kind = "native", .display_name = "Reference Adapter Script" },
    .{ .id = "reference-mcp", .kind = "process", .display_name = "Reference Synthetic MCP Source", .protocol = "mcp", .endpoint = "stdio:reference-tool-source" },
};

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = "Deterministic In-Memory Reference Adapter", .version = protocol_version, .adapter = "process-memory-script" },
    .capability_revision = capability_revision,
    .features = &features,
    .sources = &declared_sources,
    .limits = .{ .max_active_runs_per_session = 2, .max_queued_runs_per_session = 1 },
};

const golden_question = contract.Question{ .id = "choice", .kind = .single_choice, .options = &.{"yes"} };

fn wallClock() i64 {
    return compat.time.nowMillis();
}

pub const Adapter = struct {
    allocator: std.mem.Allocator,
    ids: u64 = 0,
    now_ms: *const fn () i64 = wallClock,

    pub fn init(allocator: std.mem.Allocator) Adapter {
        return .{ .allocator = allocator };
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
        const session = try Session.create(self, request);
        return session.handle();
    }

    fn nextID(self: *Adapter, allocator: std.mem.Allocator, kind: []const u8) ![]u8 {
        self.ids += 1;
        return std.fmt.allocPrint(allocator, "{s}-{d}", .{ kind, self.ids });
    }
};

const Stage = enum { permission, input, terminal };

const Run = struct {
    id: []u8,
    permission_id: []u8,
    input_id: []u8,
    tool_call_id: []u8,
    status: oap_types.RunStatus = .running,
    stage: Stage = .permission,
    started: bool = false,
    queued_admission: bool = false,
    terminal: bool = false,
    pending: bool = false,
    next_sequence: u64 = 1,
    model: []const u8 = "",
    instructions: ?[]u8 = null,
    structured: bool = false,
    calls_tool: bool = true,

    fn destroy(self: *Run, gpa: std.mem.Allocator) void {
        gpa.free(self.id);
        gpa.free(self.permission_id);
        gpa.free(self.input_id);
        gpa.free(self.tool_call_id);
        if (self.instructions) |owned| gpa.free(owned);
        gpa.destroy(self);
    }

    fn live(self: *const Run) bool {
        return !self.terminal;
    }
};

const Journaled = struct {
    line: []u8,
    run_id: []const u8,
    sequence: u64,
};

const Controls = struct {
    model: []const u8 = "",
    instructions: ?[]const u8 = null,
    structured: bool = false,
    calls_tool: bool = true,
};

pub const Session = struct {
    owner: *Adapter,
    gpa: std.mem.Allocator,
    id: []u8,
    participant: []u8,
    current_model: []const u8 = "",
    updated_at_ms: i64,
    closed: bool = false,
    active: ?*Run = null,
    reserved: ?*Run = null,
    runs: std.ArrayList(*Run) = .empty,
    journal: std.ArrayList(Journaled) = .empty,
    outbox: std.ArrayList(Journaled) = .empty,

    fn create(owner: *Adapter, request: contract.OpenRequest) contract.Failure!*Session {
        const gpa = owner.allocator;
        const self = try gpa.create(Session);
        errdefer gpa.destroy(self);
        const id = if (request.session_id.len > 0) try gpa.dupe(u8, request.session_id) else try owner.nextID(gpa, "session");
        errdefer gpa.free(id);
        const participant = try gpa.dupe(u8, request.participant);
        self.* = .{ .owner = owner, .gpa = gpa, .id = id, .participant = participant, .updated_at_ms = owner.now_ms() };
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

    fn destroy(self: *Session) void {
        const gpa = self.gpa;
        for (self.runs.items) |run| run.destroy(gpa);
        self.runs.deinit(gpa);
        for (self.journal.items) |entry| gpa.free(entry.line);
        self.journal.deinit(gpa);
        for (self.outbox.items) |entry| gpa.free(entry.line);
        self.outbox.deinit(gpa);
        gpa.free(self.participant);
        gpa.free(self.id);
        gpa.destroy(self);
    }

    fn findRun(self: *Session, run_id: []const u8) ?*Run {
        for (self.runs.items) |run| {
            if (std.mem.eql(u8, run.id, run_id)) return run;
        }
        return null;
    }

    fn busy(self: *Session) bool {
        return if (self.active) |run| run.live() else false;
    }

    fn state(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        _ = refusal;
        const self = cast(ptr);
        if (self.closed) return error.SessionClosed;
        return self.snapshot(arena);
    }

    fn snapshot(self: *Session, arena: std.mem.Allocator) contract.Failure!oap_types.SessionState {
        var status: oap_types.SessionStatus = .idle;
        var active_run_id: ?[]const u8 = null;
        if (self.active) |run| {
            if (run.live() and run.started) {
                status = if (run.status == .waiting_for_input or run.pending) .waiting_for_input else .running;
                active_run_id = try arena.dupe(u8, run.id);
            }
        }
        if (active_run_id == null) {
            const queued = (if (self.active) |run| run.live() else false) or (if (self.reserved) |run| run.live() else false);
            if (queued) status = .queued;
        }
        return .{
            .session_id = self.id,
            .status = status,
            .active_run_id = active_run_id,
            .current_model_id = if (self.current_model.len > 0) self.current_model else null,
            .updated_at_ms = self.updated_at_ms,
        };
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const self = cast(ptr);
        var controls = try admitControls(arena, request, refusal);
        if (request.session_id.len == 0 or request.messages.len == 0) return error.InvalidSubmission;
        if (request.delivery != .auto and request.delivery != .queue) return error.InvalidSubmission;
        if (self.closed) return error.SessionClosed;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        const is_busy = self.busy();
        if (is_busy) {
            if (self.reserved) |run| {
                if (run.live()) return error.RunActive;
            }
        }
        if (!is_busy and request.delivery != .queue and controls.model.len == 0) controls.model = self.current_model;

        const gpa = self.gpa;
        const run = try gpa.create(Run);
        errdefer gpa.destroy(run);
        const run_id = try self.owner.nextID(gpa, "run");
        errdefer gpa.free(run_id);
        const permission_id = try self.owner.nextID(gpa, "permission");
        errdefer gpa.free(permission_id);
        const input_id = try self.owner.nextID(gpa, "input");
        errdefer gpa.free(input_id);
        const tool_call_id = try self.owner.nextID(gpa, "tool-call");
        errdefer gpa.free(tool_call_id);
        const instructions = if (controls.instructions) |text| try gpa.dupe(u8, text) else null;
        errdefer if (instructions) |owned| gpa.free(owned);
        try self.runs.ensureUnusedCapacity(gpa, 1);
        const reservation = is_busy or request.delivery == .queue;
        run.* = .{
            .id = run_id,
            .permission_id = permission_id,
            .input_id = input_id,
            .tool_call_id = tool_call_id,
            .stage = if (controls.calls_tool) .permission else .input,
            .model = controls.model,
            .instructions = instructions,
            .structured = controls.structured,
            .calls_tool = controls.calls_tool,
            .queued_admission = reservation,
            .status = if (reservation) .queued else .running,
        };
        self.runs.appendAssumeCapacity(run);
        if (is_busy) self.reserved = run else self.active = run;
        self.updated_at_ms = self.owner.now_ms();

        const message_ids = try arena.alloc([]const u8, request.messages.len);
        for (request.messages, message_ids) |message, *slot| {
            slot.* = if (message.id) |carried| carried else try self.owner.nextID(arena, "message");
        }
        const submission_id = try self.owner.nextID(arena, "submission");
        var admission = oap_types.MessageSubmitResponse{
            .session_id = self.id,
            .accepted = true,
            .submission_id = submission_id,
            .requested_delivery = request.delivery,
            .effective_delivery = .start,
            .delivery_resolution = "session_idle",
            .admission = .started,
            .run_id = try arena.dupe(u8, run.id),
            .status = .running,
            .model_id = if (controls.model.len > 0) controls.model else null,
            .message_ids = message_ids,
        };
        if (reservation) {
            admission.effective_delivery = .queue;
            admission.admission = .queued;
            admission.status = .queued;
            admission.delivery_resolution = if (is_busy) "session_busy" else "session_idle";
        }
        if (!is_busy) try self.emitInitial(run);
        return admission;
    }

    fn admitControls(arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *contract.Refusal) contract.Failure!Controls {
        var controls = Controls{ .instructions = request.instructions };
        if (request.model_id) |model| {
            if (!std.mem.eql(u8, model, model_primary) and !std.mem.eql(u8, model, model_secondary)) return refusal.missingModel(model);
            controls.model = if (std.mem.eql(u8, model, model_primary)) model_primary else model_secondary;
        }
        if (request.output_schema_json) |schema| {
            if (try outputSchemaDefect(arena, schema)) |detail| {
                refusal.* = .{ .feature = "run.structured_output", .reason = contract.reason_unsatisfiable, .field = "output_schema", .message = detail };
                return error.UnsupportedFeature;
            }
            controls.structured = true;
        }
        if (request.tool_choice_json) |choice| {
            const policy = parseToolChoice(arena, choice) catch |err| {
                if (err == error.OutOfMemory) return error.OutOfMemory;
                refusal.* = .{ .feature = "run.tool_selection", .reason = contract.reason_unsatisfiable, .message = "tool_choice is not the typed policy" };
                return error.UnsupportedFeature;
            };
            if (policy.allowed) |allowed| {
                for (allowed) |name| {
                    if (!std.mem.eql(u8, name, scripted_tool)) {
                        const detail = try std.fmt.allocPrint(arena, "allowed names a tool outside the catalog: {s}", .{name});
                        refusal.* = .{ .feature = "run.tool_selection", .reason = contract.reason_unsatisfiable, .message = detail };
                        return error.UnsupportedFeature;
                    }
                }
            }
            controls.calls_tool = policy.permits(scripted_tool);
        }
        return controls;
    }

    fn emitInitial(self: *Session, run: *Run) contract.Failure!void {
        if (run.model.len == 0) run.model = self.current_model;
        run.started = true;
        run.status = .running;
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();

        var started = Payload.init(a);
        try started.run(self, run);
        try started.put("status", .{ .string = "running" });
        if (run.model.len > 0) try started.put("model_id", .{ .string = run.model });
        try started.put("started_at_ms", .{ .integer = self.owner.now_ms() });
        try self.emit(run, "run.started", started.value(), false);

        const lead = if (run.calls_tool) "I will use the scripted tool." else "I will answer without the scripted tool.";
        const text = if (run.instructions) |instructions| try std.fmt.allocPrint(a, "{s} {s}", .{ instructions, lead }) else lead;
        try self.emitDelta(a, run, text);
        if (!run.calls_tool) return self.requestInput(run);

        var call = try self.callPayload(a, run, true);
        try self.emit(run, "action.call.requested", call.value(), false);

        var permission = Payload.init(a);
        try permission.interaction(self, run.permission_id);
        try permission.run(self, run);
        try permission.put("tool_call_id", .{ .string = run.tool_call_id });
        try permission.put("title", .{ .string = "Allow scripted tool" });
        try permission.put("description", .{ .string = "The golden script requires approval." });
        try permission.put("choices", try parseValue(a, "[{\"id\":\"approve\",\"label\":\"Approve\"},{\"id\":\"deny\",\"label\":\"Deny\"}]"));
        try permission.put("arguments_json", try parseValue(a, golden_arguments));
        try self.emit(run, "action.permission.requested", permission.value(), false);
    }

    fn emitDelta(self: *Session, a: std.mem.Allocator, run: *Run, text: []const u8) contract.Failure!void {
        const message_id = try self.owner.nextID(a, "message");
        var delta = Payload.init(a);
        try delta.run(self, run);
        try delta.put("message_id", .{ .string = message_id });
        var part = Payload.init(a);
        try part.put("type", .{ .string = "text" });
        try part.put("text", .{ .string = text });
        try delta.put("part", part.value());
        try self.emit(run, "content.delta", delta.value(), false);
    }

    fn callPayload(self: *Session, a: std.mem.Allocator, run: *Run, with_arguments: bool) contract.Failure!Payload {
        var call = Payload.init(a);
        try call.run(self, run);
        try call.put("tool_call_id", .{ .string = run.tool_call_id });
        try call.put("requested_by", .{ .string = endpoint_id });
        try call.put("execution_owner", .{ .string = scripted_owner });
        try call.put("source", .{ .string = scripted_source });
        try call.put("name", .{ .string = scripted_tool });
        if (with_arguments) try call.put("arguments_json", try parseValue(a, golden_arguments));
        return call;
    }

    fn cancelledCall(self: *Session, a: std.mem.Allocator, run: *Run) contract.Failure!Payload {
        var call = Payload.init(a);
        try call.run(self, run);
        try call.put("tool_call_id", .{ .string = run.tool_call_id });
        try call.put("requested_by", .{ .string = endpoint_id });
        try call.put("responded_by", .{ .string = self.participant });
        try call.put("execution_owner", .{ .string = scripted_owner });
        try call.put("source", .{ .string = scripted_source });
        try call.put("name", .{ .string = scripted_tool });
        return call;
    }

    fn requestInput(self: *Session, run: *Run) contract.Failure!void {
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        var input = Payload.init(a);
        try input.interaction(self, run.input_id);
        try input.run(self, run);
        if (run.calls_tool) try input.put("tool_call_id", .{ .string = run.tool_call_id });
        try input.put("title", .{ .string = "Golden input" });
        try input.put("description", .{ .string = "Choose the deterministic answer." });
        try input.put("questions", try parseValue(a, "[{\"id\":\"choice\",\"prompt\":\"Continue?\",\"kind\":\"single_choice\",\"required\":true,\"options\":[{\"id\":\"yes\",\"label\":\"Yes\"}]}]"));
        try self.emit(run, "user.input.requested", input.value(), false);

        const now = self.owner.now_ms();
        var status = Payload.init(a);
        try status.run(self, run);
        try status.put("status", .{ .string = "waiting_for_input" });
        try status.put("pending_user_input_id", .{ .string = run.input_id });
        try status.put("updated_at_ms", .{ .integer = now });
        if (!run.terminal) {
            run.status = .waiting_for_input;
            self.updated_at_ms = now;
        }
        try self.emit(run, "run.status.updated", status.value(), false);
    }

    fn resolve(ptr: *anyopaque, arena: std.mem.Allocator, resolution: contract.Resolution, refusal: *contract.Refusal) contract.Failure!void {
        _ = arena;
        _ = refusal;
        const self = cast(ptr);
        if (self.closed) return error.SessionClosed;
        const run_id, const responded_by = switch (resolution) {
            .permission => |request| .{ request.run_id, request.responded_by },
            .input => |request| .{ request.run_id, request.responded_by },
        };
        const run = self.findRun(run_id) orelse return error.RunNotFound;
        if (run.terminal) return error.InteractionNotFound;
        if (!std.mem.eql(u8, responded_by, self.participant)) return error.InvalidResolution;
        switch (run.stage) {
            .permission => {
                const request = switch (resolution) {
                    .permission => |carried| carried,
                    .input => return error.InvalidResolution,
                };
                if (!std.mem.eql(u8, request.interaction_id, run.permission_id) or !std.mem.eql(u8, request.session_id, self.id)) return error.InvalidResolution;
                if (!std.mem.eql(u8, request.requested_by, endpoint_id)) return error.InvalidResolution;
                const choice = request.choice_id orelse return error.InvalidResolution;
                const granted = if (std.mem.eql(u8, choice, "approve")) true else if (std.mem.eql(u8, choice, "deny")) false else return error.InvalidResolution;
                if (granted != request.granted) return error.InvalidResolution;
                run.stage = .input;
                return self.resolvePermission(run, choice, granted);
            },
            .input => {
                const request = switch (resolution) {
                    .input => |carried| carried,
                    .permission => return error.InvalidResolution,
                };
                if (!std.mem.eql(u8, request.interaction_id, run.input_id) or !std.mem.eql(u8, request.session_id, self.id)) return error.InvalidResolution;
                if (!std.mem.eql(u8, request.requested_by, endpoint_id)) return error.InvalidResolution;
                if (request.answers.len != 1 or !contract.validInputAnswer(golden_question, request.answers[0])) return error.InvalidResolution;
                run.stage = .terminal;
                return self.resolveInput(run, request.answers);
            },
            .terminal => return error.InteractionNotFound,
        }
    }

    fn resolvePermission(self: *Session, run: *Run, choice: []const u8, granted: bool) contract.Failure!void {
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        var resolved = Payload.init(a);
        try resolved.interaction(self, run.permission_id);
        try resolved.run(self, run);
        try resolved.put("tool_call_id", .{ .string = run.tool_call_id });
        try resolved.put("outcome", .{ .string = "resolved" });
        try resolved.put("choice_id", .{ .string = choice });
        try resolved.put("granted", .{ .bool = granted });
        try self.emit(run, "action.permission.resolved", resolved.value(), false);
        if (!granted) {
            var call = try self.cancelledCall(a, run);
            try self.emit(run, "action.call.cancelled", call.value(), false);
            var failure = Payload.init(a);
            try failure.run(self, run);
            try failure.put("error", try parseValue(a, "{\"code\":\"permission_denied\",\"message\":\"scripted tool permission denied\"}"));
            return self.emit(run, "run.failed", failure.value(), true);
        }
        var started = try self.callPayload(a, run, true);
        try self.emit(run, "action.call.started", started.value(), false);
        var completed = try self.callPayload(a, run, false);
        try completed.put("result", try parseValue(a, fixed_result));
        try self.emit(run, "action.call.completed", completed.value(), false);
        return self.requestInput(run);
    }

    fn resolveInput(self: *Session, run: *Run, answers: []const oap_types.InputAnswer) contract.Failure!void {
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        var resolved = Payload.init(a);
        try resolved.interaction(self, run.input_id);
        try resolved.run(self, run);
        try resolved.put("status", .{ .string = "submitted" });
        var listed = std.json.Array.init(a);
        for (answers) |answer| {
            var entry = Payload.init(a);
            try entry.put("question_id", .{ .string = answer.question_id });
            if (answer.text) |text| try entry.put("text", .{ .string = text });
            if (answer.selected_option_ids.len > 0) {
                var selected = std.json.Array.init(a);
                for (answer.selected_option_ids) |option| try selected.append(.{ .string = option });
                try entry.put("selected_option_ids", .{ .array = selected });
            }
            try listed.append(entry.value());
        }
        try resolved.put("answers", .{ .array = listed });
        try self.emit(run, "user.input.resolved", resolved.value(), false);

        const final_text = "The golden script completed.";
        const message_id = try self.owner.nextID(a, "message");
        var delta = Payload.init(a);
        try delta.run(self, run);
        try delta.put("message_id", .{ .string = message_id });
        var part = Payload.init(a);
        try part.put("type", .{ .string = "text" });
        try part.put("text", .{ .string = final_text });
        try delta.put("part", part.value());
        try self.emit(run, "content.delta", delta.value(), false);

        var completed = Payload.init(a);
        try completed.run(self, run);
        var final = Payload.init(a);
        try final.put("id", .{ .string = message_id });
        try final.put("role", .{ .string = "assistant" });
        try final.put("content", .{ .string = final_text });
        try completed.put("final_response", final.value());
        try completed.put("stop_reason", .{ .string = "end_turn" });
        if (run.model.len > 0) try completed.put("model_id", .{ .string = run.model });
        if (run.structured) try completed.put("result", try parseValue(a, fixed_result));
        try self.emit(run, "run.completed", completed.value(), true);
    }

    fn cancel(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
        _ = refusal;
        const self = cast(ptr);
        if (self.closed) return error.SessionClosed;
        const run = self.findRun(run_id) orelse return error.RunNotFound;
        const owned_run_id = try arena.dupe(u8, run.id);
        if (run.terminal) {
            if (run.status == .cancelled) return .{ .session_id = self.id, .run_id = owned_run_id, .accepted = true, .status = .cancelled };
            return error.RunTerminal;
        }
        if (run.status == .cancelling) return .{ .session_id = self.id, .run_id = owned_run_id, .accepted = true, .status = .cancelling };
        const reservation = !run.started;
        run.status = .cancelling;

        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        if (reservation) {
            var cancelled = Payload.init(a);
            try cancelled.run(self, run);
            try cancelled.put("reason", .{ .string = "reservation cancelled before promotion" });
            try self.emit(run, "run.cancelled", cancelled.value(), true);
            return .{ .session_id = self.id, .run_id = owned_run_id, .accepted = true, .status = .cancelling };
        }
        var status = Payload.init(a);
        try status.run(self, run);
        try status.put("status", .{ .string = "cancelling" });
        try status.put("updated_at_ms", .{ .integer = self.owner.now_ms() });
        try self.emit(run, "run.status.updated", status.value(), false);
        switch (run.stage) {
            .permission => {
                var resolved = Payload.init(a);
                try resolved.interaction(self, run.permission_id);
                try resolved.run(self, run);
                try resolved.put("tool_call_id", .{ .string = run.tool_call_id });
                try resolved.put("outcome", .{ .string = "cancelled" });
                try resolved.put("reason", try parseValue(a, "{\"code\":\"run_cancelled\",\"message\":\"run cancellation closed the permission request\"}"));
                try self.emit(run, "action.permission.resolved", resolved.value(), false);
                var call = try self.cancelledCall(a, run);
                try self.emit(run, "action.call.cancelled", call.value(), false);
            },
            .input => {
                var resolved = Payload.init(a);
                try resolved.interaction(self, run.input_id);
                try resolved.run(self, run);
                try resolved.put("status", .{ .string = "cancelled" });
                try self.emit(run, "user.input.resolved", resolved.value(), false);
            },
            .terminal => {},
        }
        var cancelled = Payload.init(a);
        try cancelled.run(self, run);
        try cancelled.put("reason", .{ .string = "cancel confirmed" });
        try self.emit(run, "run.cancelled", cancelled.value(), true);
        return .{ .session_id = self.id, .run_id = owned_run_id, .accepted = true, .status = .cancelling };
    }

    fn emit(self: *Session, run: *Run, kind: []const u8, payload: std.json.Value, terminal: bool) contract.Failure!void {
        if (run.terminal) return;
        const gpa = self.gpa;
        var scratch = std.heap.ArenaAllocator.init(gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        const event_id = try self.owner.nextID(a, "event");
        const now = self.owner.now_ms();
        const sequence = run.next_sequence;
        var envelope = Payload.init(a);
        try envelope.put("protocol", .{ .string = protocol_name });
        try envelope.put("version", .{ .string = protocol_version });
        try envelope.put("profile", .{ .string = profile });
        try envelope.put("type", .{ .string = kind });
        try envelope.put("id", .{ .string = event_id });
        try envelope.put("payload", payload);
        try envelope.put("sequence", .{ .integer = @intCast(sequence) });
        try envelope.put("timestamp_ms", .{ .integer = now });
        try envelope.put("session_id", .{ .string = self.id });
        try envelope.put("run_id", .{ .string = run.id });
        if (run.calls_tool and carriesToolCall(kind)) try envelope.put("tool_call_id", .{ .string = run.tool_call_id });
        try envelope.put("capability_revision", .{ .string = capability_revision });
        const line = try json_encode.valueAlloc(gpa, envelope.value());
        errdefer gpa.free(line);
        const copy = try gpa.dupe(u8, line);
        errdefer gpa.free(copy);
        try self.outbox.ensureUnusedCapacity(gpa, 1);
        try self.journal.ensureUnusedCapacity(gpa, 1);
        self.outbox.appendAssumeCapacity(.{ .line = copy, .run_id = run.id, .sequence = sequence });
        if (self.journal.items.len == journal_capacity) {
            gpa.free(self.journal.orderedRemove(0).line);
        }
        self.journal.appendAssumeCapacity(.{ .line = line, .run_id = run.id, .sequence = sequence });
        run.next_sequence += 1;
        self.updated_at_ms = now;

        if (std.mem.eql(u8, kind, "action.permission.requested") or std.mem.eql(u8, kind, "user.input.requested")) run.pending = true;
        if (std.mem.eql(u8, kind, "action.permission.resolved") or std.mem.eql(u8, kind, "user.input.resolved")) run.pending = false;
        if (!terminal) return;
        run.terminal = true;
        run.pending = false;
        if (std.mem.eql(u8, kind, "run.completed")) run.status = .completed;
        if (std.mem.eql(u8, kind, "run.failed")) run.status = .failed;
        if (std.mem.eql(u8, kind, "run.cancelled")) run.status = .cancelled;
        if (self.active == run) self.active = null;
        if (self.reserved == run) self.reserved = null;
        if (self.active == null) {
            if (self.reserved) |promoted| {
                if (promoted.live()) {
                    self.reserved = null;
                    self.active = promoted;
                    try self.emitInitial(promoted);
                }
            }
        }
    }

    fn carriesToolCall(kind: []const u8) bool {
        return std.mem.startsWith(u8, kind, "action.call.") or std.mem.eql(u8, kind, "action.permission.requested") or std.mem.eql(u8, kind, "user.input.requested");
    }

    fn pump(ptr: *anyopaque, wait_ns: u64) contract.Failure!bool {
        _ = ptr;
        _ = wait_ns;
        return false;
    }

    fn drain(ptr: *anyopaque, allocator: std.mem.Allocator, out: *std.ArrayList(contract.Event)) contract.Failure!void {
        const self = cast(ptr);
        try out.ensureUnusedCapacity(allocator, self.outbox.items.len);
        for (self.outbox.items) |entry| {
            out.appendAssumeCapacity(.{ .line = try allocator.dupe(u8, entry.line), .run_id = try allocator.dupe(u8, entry.run_id), .sequence = entry.sequence });
        }
        for (self.outbox.items) |entry| self.gpa.free(entry.line);
        self.outbox.clearRetainingCapacity();
    }

    fn activity(ptr: *anyopaque) contract.Activity {
        const self = cast(ptr);
        const run = self.active orelse return .idle;
        if (!run.live()) return .idle;
        return if (run.pending or run.status == .waiting_for_input) .waiting else .running;
    }

    fn close(ptr: *anyopaque) void {
        cast(ptr).destroy();
    }

    fn tools(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ToolsListRequest, refusal: *contract.Refusal) contract.Failure!oap_types.ToolsListResponse {
        _ = refusal;
        const self = cast(ptr);
        if (self.closed) return error.SessionClosed;
        if (request.session_id) |named| {
            if (named.len > 0 and !std.mem.eql(u8, named, self.id)) return error.RunNotFound;
        }
        const tool_features = try arena.dupe(oap_types.Feature, &.{
            .{ .key = "action.permissions", .level = .emulated, .reason = "the scripted call is gated" },
            .{ .key = "action.tools.execute", .level = .emulated, .reason = "the reference adapter executes a fixed deterministic script" },
        });
        const definitions = try arena.dupe(oap_types.ToolDefinition, &.{.{
            .name = scripted_tool,
            .description = "The deterministic scripted tool the reference adapter calls.",
            .input_schema_json = scripted_schema,
            .execution_owner = scripted_owner,
            .source = scripted_source,
            .features = tool_features,
        }});
        return .{ .session_id = request.session_id, .sources = try arena.dupe(oap_types.ToolSourceDescriptor, &declared_sources), .tools = definitions };
    }

    fn models(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ModelsRequest, refusal: *contract.Refusal) contract.Failure!oap_types.ModelsResponse {
        _ = refusal;
        const self = cast(ptr);
        if (self.closed) return error.SessionClosed;
        if (request.session_id.len > 0 and !std.mem.eql(u8, request.session_id, self.id)) return error.InvalidSubmission;
        const listed = try arena.dupe(oap_types.ModelDescriptor, &.{
            .{ .id = model_primary, .display_name = "Reference Model A", .provider_id = "reference", .default = true },
            .{ .id = model_secondary, .display_name = "Reference Model B", .provider_id = "reference" },
        });
        return .{ .session_id = self.id, .current_model_id = if (self.current_model.len > 0) self.current_model else null, .models = listed };
    }

    fn switchModel(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.SessionModelSwitchRequest, refusal: *contract.Refusal) contract.Failure!contract.Switched {
        const self = cast(ptr);
        if (self.closed) return error.SessionClosed;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.InvalidSubmission;
        const chosen = if (std.mem.eql(u8, request.model_id, model_primary)) model_primary else if (std.mem.eql(u8, request.model_id, model_secondary)) model_secondary else return refusal.missingModel(request.model_id);
        const previous = self.current_model;
        self.current_model = chosen;
        self.updated_at_ms = self.owner.now_ms();
        return .{
            .response = .{ .session_id = self.id, .model_id = chosen, .previous_model_id = if (previous.len > 0) previous else null },
            .state = try self.snapshot(arena),
        };
    }

    fn replay(ptr: *anyopaque, allocator: std.mem.Allocator, run_id: []const u8, after: u64, refusal: *contract.Refusal) contract.Failure!contract.Replay {
        _ = refusal;
        const self = cast(ptr);
        if (self.closed) return error.SessionClosed;
        const run = self.findRun(run_id) orelse return error.RunNotFound;
        const latest = run.next_sequence - 1;
        if (after > latest) return error.ReplayCursorFuture;
        var oldest: u64 = 0;
        var suffix = std.ArrayList(contract.Event).empty;
        for (self.journal.items) |entry| {
            if (!std.mem.eql(u8, entry.run_id, run.id)) continue;
            if (oldest == 0) oldest = entry.sequence;
            if (entry.sequence > after) {
                try suffix.append(allocator, .{ .line = try allocator.dupe(u8, entry.line), .run_id = try allocator.dupe(u8, entry.run_id), .sequence = entry.sequence });
            }
        }
        if (after < latest and (oldest == 0 or after + 1 < oldest)) {
            return .{ .gap = .{ .requested_after = after, .oldest_available = oldest, .latest_available = latest } };
        }
        return .{ .events = suffix.items };
    }
};

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

    fn interaction(self: *Payload, session: *Session, interaction_id: []const u8) !void {
        try self.put("interaction_id", .{ .string = interaction_id });
        try self.put("requested_by", .{ .string = endpoint_id });
        try self.put("responded_by", .{ .string = session.participant });
    }
};

fn parseValue(allocator: std.mem.Allocator, text: []const u8) !std.json.Value {
    return std.json.parseFromSliceLeaky(std.json.Value, allocator, text, .{}) catch |err| switch (err) {
        error.OutOfMemory => error.OutOfMemory,
        else => unreachable,
    };
}

const ToolChoice = struct {
    allowed: ?[]const []const u8 = null,
    disallowed: []const []const u8 = &.{},

    fn permits(self: ToolChoice, name: []const u8) bool {
        if (self.allowed) |allowed| {
            if (!contains(allowed, name)) return false;
        }
        return !contains(self.disallowed, name);
    }
};

fn contains(names: []const []const u8, name: []const u8) bool {
    for (names) |candidate| {
        if (std.mem.eql(u8, candidate, name)) return true;
    }
    return false;
}

fn parseToolChoice(arena: std.mem.Allocator, text: []const u8) !ToolChoice {
    const document = std.json.parseFromSliceLeaky(std.json.Value, arena, text, .{}) catch |err| {
        if (err == error.OutOfMemory) return error.OutOfMemory;
        return error.InvalidPolicy;
    };
    if (document != .object) return error.InvalidPolicy;
    var choice = ToolChoice{};
    var carried: usize = 0;
    var it = document.object.iterator();
    while (it.next()) |entry| {
        const names = try stringList(arena, entry.value_ptr.*);
        if (std.mem.eql(u8, entry.key_ptr.*, "allowed")) {
            choice.allowed = names;
        } else if (std.mem.eql(u8, entry.key_ptr.*, "disallowed")) {
            choice.disallowed = names;
        } else return error.InvalidPolicy;
        carried += 1;
    }
    if (carried != 1) return error.InvalidPolicy;
    return choice;
}

fn stringList(arena: std.mem.Allocator, value: std.json.Value) ![]const []const u8 {
    if (value != .array) return error.InvalidPolicy;
    const names = try arena.alloc([]const u8, value.array.items.len);
    for (value.array.items, names) |item, *slot| {
        if (item != .string) return error.InvalidPolicy;
        slot.* = item.string;
    }
    return names;
}

fn outputSchemaDefect(arena: std.mem.Allocator, text: []const u8) error{OutOfMemory}!?[]const u8 {
    const document = std.json.parseFromSliceLeaky(std.json.Value, arena, text, .{}) catch |err| {
        if (err == error.OutOfMemory) return error.OutOfMemory;
        return "output_schema is not valid JSON";
    };
    if (document != .object) return "output_schema must be a JSON Schema object";
    if (document.object.get("type")) |declared| {
        switch (declared) {
            .string => |named| if (!std.mem.eql(u8, named, "object")) return "output_schema root type is not object; a structured result is an object",
            .array => |named| {
                if (named.items.len == 0) return "output_schema root type list is empty";
                for (named.items) |entry| {
                    if (entry != .string or !std.mem.eql(u8, entry.string, "object")) return "output_schema root type list names a type other than object";
                }
            },
            else => return "output_schema root type is not a string or a list of strings",
        }
    }
    var registry = jsonschema.Registry{ .allocator = arena };
    registry.addDocument("output_schema", text) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return "output_schema is not valid JSON",
    };
    var validator = jsonschema.Validator.init(arena, &registry);
    const result = try parseValue(arena, fixed_result);
    const failure = validator.validate("output_schema", result) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return "output_schema uses a construct oapx's schema engine does not evaluate",
    };
    if (failure != null) return "the fixed result does not satisfy the requested schema";
    return null;
}

const testing = std.testing;

fn fixedClock() i64 {
    return 1000;
}

const Probe = struct {
    adapter: Adapter,
    arena: std.heap.ArenaAllocator,
    session: contract.Session = undefined,
    seen: std.ArrayList(contract.Event) = .empty,

    fn init(self: *Probe) !void {
        self.adapter = Adapter.init(testing.allocator);
        self.adapter.now_ms = fixedClock;
        self.arena = std.heap.ArenaAllocator.init(testing.allocator);
        self.seen = .empty;
        var refusal = contract.Refusal{};
        self.session = try self.adapter.adapter().open(self.arena.allocator(), .{ .session_id = "s1", .participant = "user" }, &refusal);
    }

    fn deinit(self: *Probe) void {
        self.session.close();
        self.arena.deinit();
    }

    fn a(self: *Probe) std.mem.Allocator {
        return self.arena.allocator();
    }

    fn submitWith(self: *Probe, delivery: oap_types.RequestedDelivery, controls: oap_types.MessageSubmitRequest) contract.Failure!oap_types.MessageSubmitResponse {
        var request = controls;
        request.session_id = "s1";
        request.delivery = delivery;
        request.messages = try self.a().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = "run" } }});
        var refusal = contract.Refusal{};
        return self.session.submit(self.a(), &request, &refusal);
    }

    fn submit(self: *Probe) !oap_types.MessageSubmitResponse {
        return self.submitWith(.auto, .{ .session_id = "", .messages = &.{}, .delivery = .auto });
    }

    fn types(self: *Probe) ![]const []const u8 {
        try self.session.drain(self.a(), &self.seen);
        var kinds = std.ArrayList([]const u8).empty;
        for (self.seen.items) |event| {
            const parsed = try std.json.parseFromSliceLeaky(std.json.Value, self.a(), event.line, .{});
            try kinds.append(self.a(), parsed.object.get("type").?.string);
        }
        return kinds.items;
    }

    fn approve(self: *Probe, run_id: []const u8, interaction: []const u8, choice: []const u8) contract.Failure!void {
        var refusal = contract.Refusal{};
        const request = oap_types.PermissionResolveRequest{ .interaction_id = interaction, .requested_by = endpoint_id, .responded_by = "user", .session_id = "s1", .run_id = run_id, .granted = std.mem.eql(u8, choice, "approve"), .choice_id = choice };
        return self.session.resolve(self.a(), .{ .permission = &request }, &refusal);
    }

    fn answer(self: *Probe, run_id: []const u8, interaction: []const u8) contract.Failure!void {
        var refusal = contract.Refusal{};
        const answers = try self.a().dupe(oap_types.InputAnswer, &.{.{ .question_id = "choice", .selected_option_ids = &.{"yes"} }});
        const request = oap_types.UserInputResolveRequest{ .interaction_id = interaction, .requested_by = endpoint_id, .responded_by = "user", .session_id = "s1", .run_id = run_id, .answers = answers };
        return self.session.resolve(self.a(), .{ .input = &request }, &refusal);
    }
};

fn expectTypes(expected: []const []const u8, actual: []const []const u8) !void {
    try testing.expectEqual(expected.len, actual.len);
    for (expected, actual) |want, got| try testing.expectEqualStrings(want, got);
}

test "the golden script runs permission, tool, input and completion with contiguous sequences" {
    var probe: Probe = undefined;
    try probe.init();
    defer probe.deinit();
    const admitted = try probe.submit();
    try testing.expectEqualStrings("run-1", admitted.run_id.?);
    try testing.expectEqual(contract.Activity.waiting, probe.session.activity());
    try probe.approve("run-1", "permission-2", "approve");
    try probe.answer("run-1", "input-3");
    try expectTypes(&.{ "run.started", "content.delta", "action.call.requested", "action.permission.requested", "action.permission.resolved", "action.call.started", "action.call.completed", "user.input.requested", "run.status.updated", "user.input.resolved", "content.delta", "run.completed" }, try probe.types());
    for (probe.seen.items, 1..) |event, sequence| try testing.expectEqual(@as(u64, sequence), event.sequence);
    try testing.expectEqual(contract.Activity.idle, probe.session.activity());
}

test "a denied permission cancels the call and fails the run" {
    var probe: Probe = undefined;
    try probe.init();
    defer probe.deinit();
    _ = try probe.submit();
    try probe.approve("run-1", "permission-2", "deny");
    const kinds = try probe.types();
    try expectTypes(&.{ "action.permission.resolved", "action.call.cancelled", "run.failed" }, kinds[4..]);
}

test "a resolution that is misaddressed, self-contradictory, or late is refused" {
    var probe: Probe = undefined;
    try probe.init();
    defer probe.deinit();
    _ = try probe.submit();
    try testing.expectError(error.InvalidResolution, probe.answer("run-1", "input-3"));
    try testing.expectError(error.InvalidResolution, probe.approve("run-1", "permission-9", "approve"));
    try testing.expectError(error.RunNotFound, probe.approve("run-9", "permission-2", "approve"));
    var refusal = contract.Refusal{};
    const stranger = oap_types.PermissionResolveRequest{ .interaction_id = "permission-2", .requested_by = endpoint_id, .responded_by = "someone", .session_id = "s1", .run_id = "run-1", .granted = true, .choice_id = "approve" };
    try testing.expectError(error.InvalidResolution, probe.session.resolve(probe.a(), .{ .permission = &stranger }, &refusal));
    const contradicted = oap_types.PermissionResolveRequest{ .interaction_id = "permission-2", .requested_by = endpoint_id, .responded_by = "user", .session_id = "s1", .run_id = "run-1", .granted = false, .choice_id = "approve" };
    try testing.expectError(error.InvalidResolution, probe.session.resolve(probe.a(), .{ .permission = &contradicted }, &refusal));
    try probe.approve("run-1", "permission-2", "deny");
    try testing.expectError(error.InteractionNotFound, probe.answer("run-1", "input-3"));
}

test "a cancel at the permission gate closes the gate and the call before the terminal" {
    var probe: Probe = undefined;
    try probe.init();
    defer probe.deinit();
    _ = try probe.submit();
    var refusal = contract.Refusal{};
    const answered = try probe.session.cancel(probe.a(), "run-1", &refusal);
    try testing.expectEqual(oap_types.RunStatus.cancelling, answered.status);
    const kinds = try probe.types();
    try expectTypes(&.{ "run.status.updated", "action.permission.resolved", "action.call.cancelled", "run.cancelled" }, kinds[4..]);
    const again = try probe.session.cancel(probe.a(), "run-1", &refusal);
    try testing.expectEqual(oap_types.RunStatus.cancelled, again.status);
}

test "a busy session queues one run, refuses a third, and promotes the queued run when the first settles" {
    var probe: Probe = undefined;
    try probe.init();
    defer probe.deinit();
    _ = try probe.submit();
    const queued = try probe.submitWith(.auto, .{ .session_id = "", .messages = &.{}, .delivery = .auto });
    try testing.expectEqual(oap_types.Admission.queued, queued.admission);
    try testing.expectEqualStrings("session_busy", queued.delivery_resolution.?);
    try testing.expectError(error.RunActive, probe.submit());
    var refusal = contract.Refusal{};
    const state = try probe.session.state(probe.a(), &refusal);
    try testing.expectEqualStrings("run-1", state.active_run_id.?);
    try probe.approve("run-1", "permission-2", "deny");
    try probe.session.drain(probe.a(), &probe.seen);
    const last = probe.seen.items[probe.seen.items.len - 1];
    try testing.expectEqualStrings(queued.run_id.?, last.run_id);
    try testing.expect(std.mem.indexOf(u8, probe.seen.items[probe.seen.items.len - 4].line, "\"run.started\"") != null);
}

test "a queued reservation cancelled before promotion settles with its own first event" {
    var probe: Probe = undefined;
    try probe.init();
    defer probe.deinit();
    _ = try probe.submit();
    const queued = try probe.submitWith(.queue, .{ .session_id = "", .messages = &.{}, .delivery = .queue });
    var refusal = contract.Refusal{};
    _ = try probe.session.cancel(probe.a(), queued.run_id.?, &refusal);
    try probe.session.drain(probe.a(), &probe.seen);
    const last = probe.seen.items[probe.seen.items.len - 1];
    try testing.expectEqualStrings(queued.run_id.?, last.run_id);
    try testing.expectEqual(@as(u64, 1), last.sequence);
    try testing.expect(std.mem.indexOf(u8, last.line, "\"run.cancelled\"") != null);
}

test "a replay re-delivers the suffix after a cursor, refuses a future cursor, and reports a gap past the journal" {
    var probe: Probe = undefined;
    try probe.init();
    defer probe.deinit();
    _ = try probe.submit();
    var refusal = contract.Refusal{};
    const replayed = try probe.session.vtable.replay.?(probe.session.ptr, probe.a(), "run-1", 2, &refusal);
    try testing.expectEqual(@as(usize, 2), replayed.events.len);
    try testing.expectEqual(@as(u64, 3), replayed.events[0].sequence);
    try testing.expectError(error.ReplayCursorFuture, probe.session.vtable.replay.?(probe.session.ptr, probe.a(), "run-1", 9, &refusal));

    _ = try probe.session.cancel(probe.a(), "run-1", &refusal);
    var runs: usize = 0;
    while (runs < 10) : (runs += 1) {
        const admitted = try probe.submit();
        _ = try probe.session.cancel(probe.a(), admitted.run_id.?, &refusal);
    }
    const gap = try probe.session.vtable.replay.?(probe.session.ptr, probe.a(), "run-1", 0, &refusal);
    try testing.expectEqual(@as(u64, 8), gap.gap.latest_available);
    try testing.expectEqual(@as(u64, 0), gap.gap.oldest_available);
}

test "model selection, instructions and a satisfiable output schema shape the run; a disallowed tool skips the gate" {
    var probe: Probe = undefined;
    try probe.init();
    defer probe.deinit();
    const admitted = try probe.submitWith(.auto, .{ .session_id = "", .messages = &.{}, .delivery = .auto, .model_id = model_secondary, .instructions = "Be brief.", .output_schema_json = "{\"type\":\"object\",\"required\":[\"ok\"]}", .tool_choice_json = "{\"disallowed\":[\"scripted_tool\"]}" });
    try testing.expectEqualStrings(model_secondary, admitted.model_id.?);
    const kinds = try probe.types();
    try expectTypes(&.{ "run.started", "content.delta", "user.input.requested", "run.status.updated" }, kinds);
    try testing.expect(std.mem.indexOf(u8, probe.seen.items[1].line, "Be brief. I will answer without the scripted tool.") != null);
    try probe.answer(admitted.run_id.?, "input-3");
    try probe.session.drain(probe.a(), &probe.seen);
    const completed = probe.seen.items[probe.seen.items.len - 1].line;
    try testing.expect(std.mem.indexOf(u8, completed, "\"result\":{\"ok\":true}") != null);
    try testing.expect(std.mem.indexOf(u8, completed, "\"model_id\":\"reference-model-b\"") != null);
}

test "an unknown model, an unsatisfiable schema and a policy naming an unknown tool are each refused under their key" {
    var probe: Probe = undefined;
    try probe.init();
    defer probe.deinit();
    const base = oap_types.MessageSubmitRequest{ .session_id = "", .messages = &.{}, .delivery = .auto };
    var request = base;
    request.model_id = "nope";
    try testing.expectError(error.ModelNotFound, probe.submitWith(.auto, request));
    request = base;
    request.output_schema_json = "{\"type\":\"object\",\"required\":[\"missing\"]}";
    try testing.expectError(error.UnsupportedFeature, probe.submitWith(.auto, request));
    request = base;
    request.output_schema_json = "{\"type\":\"string\"}";
    try testing.expectError(error.UnsupportedFeature, probe.submitWith(.auto, request));
    request = base;
    request.tool_choice_json = "{\"allowed\":[\"other\"]}";
    try testing.expectError(error.UnsupportedFeature, probe.submitWith(.auto, request));
    request = base;
    request.tool_choice_json = "{\"allowed\":[],\"disallowed\":[]}";
    try testing.expectError(error.UnsupportedFeature, probe.submitWith(.auto, request));
    try testing.expectEqual(contract.Activity.idle, probe.session.activity());
}

test "a model switch changes the session default the next run starts with" {
    var probe: Probe = undefined;
    try probe.init();
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const switched = try probe.session.vtable.switch_model.?(probe.session.ptr, probe.a(), &.{ .session_id = "s1", .model_id = model_secondary }, &refusal);
    try testing.expect(switched.response.previous_model_id == null);
    try testing.expectError(error.ModelNotFound, probe.session.vtable.switch_model.?(probe.session.ptr, probe.a(), &.{ .session_id = "s1", .model_id = "nope" }, &refusal));
    const admitted = try probe.submit();
    try testing.expectEqualStrings(model_secondary, admitted.model_id.?);
}
