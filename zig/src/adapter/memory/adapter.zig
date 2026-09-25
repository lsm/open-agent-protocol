const std = @import("std");
const contract = @import("contract");
const oap_types = @import("oap_types");
const compat = @import("compat");
const json_encode = @import("json_encode");
const jsonschema = @import("jsonschema");

pub const endpoint_id = "reference.memory";
pub const capability_revision = "reference-memory-v11";
pub const model_primary = "reference-model-a";
pub const model_secondary = "reference-model-b";
pub const journal_capacity = 64;

const protocol_name = "open-agent-protocol";
const protocol_version = "0.1";
const profile = "open-agent-protocol.agent-control-core";
const scripted_tool = "scripted_tool";
const scripted_owner = "reference-adapter";
const scripted_source = "reference-native";
const synthetic_mcp_source = "reference-mcp";
const fixed_result = "{\"ok\":true}";
const golden_arguments = "{\"operation\":\"golden\"}";
const scripted_schema = "{\"type\":\"object\",\"properties\":{\"operation\":{\"type\":\"string\"}}}";
const max_provided_tools = 2;
const max_attached_sources = 2;
const provided_name_pattern = "^[a-z][a-z0-9_]*$";
const provided_dialect = "https://json-schema.org/draft/2020-12/schema";
const attach_transports = [_][]const u8{ "process", "local" };

const features = [_]contract.Feature{
    .{ .key = "action.permissions", .level = .emulated, .reason = "the reference adapter exposes an interactive scripted gate" },
    .{
        .key = contract.feature_tool_sources_attach,
        .level = .emulated,
        .reason = "sources are described and published back; the reference adapter runs no client for them",
        .modes = &.{"session_open"},
        .limits_json = "{\"max_sources\":2,\"transports\":[\"process\",\"local\"]}",
    },
    .{ .key = "action.tools", .level = .emulated, .reason = "the reference adapter projects the scripted tool lifecycle" },
    .{ .key = "action.tools.execute", .level = .emulated, .reason = "the reference adapter executes a fixed deterministic script" },
    .{ .key = contract.feature_tools_list, .level = .emulated, .reason = "the reference catalog is the scripted tool plus the session's attached sources" },
    .{
        .key = contract.feature_tools_provide,
        .level = .emulated,
        .reason = "provided tools are called by the script and executed by the control layer through the resolve pair",
        .limits_json = "{\"max_tools\":2,\"name_pattern\":\"^[a-z][a-z0-9_]*$\",\"schema_dialect\":\"https://json-schema.org/draft/2020-12/schema\"}",
    },
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
    .{
        .key = "run.structured_output",
        .level = .emulated,
        .reason = "the scripted result is fixed, so only a schema that object satisfies is admitted",
        .constraints_json = "{\"fixed_result\":{\"ok\":true}}",
    },
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
    .{ .id = synthetic_mcp_source, .kind = "process", .display_name = "Reference Synthetic MCP Source", .protocol = "mcp", .endpoint = "stdio:reference-tool-source" },
};

var scripted_features = [_]oap_types.Feature{
    .{ .key = "action.permissions", .level = .emulated, .reason = "the scripted call is gated" },
    .{ .key = "action.tools.execute", .level = .emulated, .reason = "the reference adapter executes a fixed deterministic script" },
};

const scripted_catalog = [_]oap_types.ToolDefinition{.{
    .name = scripted_tool,
    .description = "The deterministic scripted tool the reference adapter calls.",
    .input_schema_json = scripted_schema,
    .execution_owner = scripted_owner,
    .source = scripted_source,
    .features = &scripted_features,
}};

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = "Deterministic In-Memory Reference Adapter", .version = protocol_version, .adapter = "process-memory-script" },
    .capability_revision = capability_revision,
    .features = &features,
    .tools = &scripted_catalog,
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
        const self: *Adapter = @ptrCast(@alignCast(ptr));
        if (request.participant.len == 0) return refusal.fail(error.InvalidSubmission, "open requires a non-empty participant id");
        const session = try Session.create(self, arena, request, refusal);
        return session.handle();
    }

    fn nextID(self: *Adapter, allocator: std.mem.Allocator, kind: []const u8) ![]u8 {
        self.ids += 1;
        return std.fmt.allocPrint(allocator, "{s}-{d}", .{ kind, self.ids });
    }
};

const Stage = enum { permission, call, input, terminal };

const Arm = enum { started, result, @"error" };

const Settled = struct {
    arm: Arm,
    request_id: []const u8,
    result_json: ?[]const u8 = null,
    code: []const u8 = "",
    message: []const u8 = "",
};

const Run = struct {
    id: []const u8,
    permission_id: []const u8,
    input_id: []const u8,
    tool_call_id: []const u8,
    call_id: []const u8 = "",
    provided: ?oap_types.ToolDefinition = null,
    status: oap_types.RunStatus = .running,
    stage: Stage = .permission,
    started: bool = false,
    queued_admission: bool = false,
    terminal: bool = false,
    pending: []const u8 = "",
    acknowledged: bool = false,
    settled: ?Settled = null,
    settlement_id: []const u8 = "",
    next_sequence: u64 = 1,
    model: []const u8 = "",
    instructions: ?[]const u8 = null,
    structured: bool = false,
    calls_tool: bool = true,

    fn live(self: *const Run) bool {
        return !self.terminal;
    }

    fn reservation(self: *const Run) bool {
        return self.queued_admission and !self.started;
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
    elected: ?oap_types.ToolDefinition = null,
};

pub const Session = struct {
    owner: *Adapter,
    gpa: std.mem.Allocator,
    keep: std.heap.ArenaAllocator,
    id: []const u8,
    participant: []const u8,
    current_model: []const u8 = "",
    updated_at_ms: i64,
    transcript_cursor: u64 = 0,
    attached: []oap_types.ToolSourceDescriptor = &.{},
    provided: []oap_types.ToolDefinition = &.{},
    active: ?*Run = null,
    reserved: ?*Run = null,
    runs: std.ArrayList(*Run) = .empty,
    settled: std.ArrayList(oap_types.RunPosition) = .empty,
    journal: std.ArrayList(Journaled) = .empty,
    outbox: std.ArrayList(Journaled) = .empty,

    fn create(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const gpa = owner.allocator;
        const self = try gpa.create(Session);
        errdefer gpa.destroy(self);
        self.* = .{ .owner = owner, .gpa = gpa, .keep = std.heap.ArenaAllocator.init(gpa), .id = "", .participant = "", .updated_at_ms = owner.now_ms() };
        errdefer self.keep.deinit();
        const keep = self.keep.allocator();
        self.participant = try keep.dupe(u8, request.participant);
        self.attached = try admitToolSources(keep, arena, request.tool_sources_json, refusal);
        self.provided = try self.admitProvidedTools(arena, request.tools_json, refusal);
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
        .resolve_call = resolveCall,
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
        for (self.journal.items) |entry| gpa.free(entry.line);
        self.journal.deinit(gpa);
        for (self.outbox.items) |entry| gpa.free(entry.line);
        self.outbox.deinit(gpa);
        self.keep.deinit();
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

    fn sessionSources(self: *Session, arena: std.mem.Allocator) ![]oap_types.ToolSourceDescriptor {
        const sources = try arena.alloc(oap_types.ToolSourceDescriptor, declared_sources.len + self.attached.len);
        @memcpy(sources[0..declared_sources.len], &declared_sources);
        @memcpy(sources[declared_sources.len..], self.attached);
        return sources;
    }

    fn state(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        _ = refusal;
        return cast(ptr).snapshot(arena);
    }

    fn snapshot(self: *Session, arena: std.mem.Allocator) contract.Failure!oap_types.SessionState {
        var entries = std.ArrayList(oap_types.ActiveRun).empty;
        var started: ?*Run = null;
        var position: u64 = 0;
        for ([_]?*Run{ self.active, self.reserved }) |candidate| {
            const run = candidate orelse continue;
            if (!run.live()) continue;
            if (!run.reservation()) {
                try entries.append(arena, try self.activeEntry(arena, run, null));
                started = run;
                continue;
            }
            position += 1;
            try entries.append(arena, try self.activeEntry(arena, run, position));
        }
        var result = oap_types.SessionState{
            .session_id = self.id,
            .status = .idle,
            .active_runs = entries.items,
            .current_model_id = if (self.current_model.len > 0) self.current_model else null,
            .transcript_cursor = if (self.transcript_cursor > 0) try std.fmt.allocPrint(arena, "{d}", .{self.transcript_cursor}) else null,
            .updated_at_ms = self.updated_at_ms,
            .sources = try self.sessionSources(arena),
            .as_of = if (self.settled.items.len > 0) .{ .settled = self.settled.items } else null,
        };
        if (started) |run| {
            result.status = if (run.status == .waiting_for_input or run.pending.len > 0) .waiting_for_input else .running;
            result.active_run_id = run.id;
        } else if (entries.items.len > 0) {
            result.status = .queued;
        }
        return result;
    }

    fn activeEntry(self: *Session, arena: std.mem.Allocator, run: *Run, position: ?u64) !oap_types.ActiveRun {
        _ = self;
        const pending: []const []const u8 = if (run.pending.len > 0) try arena.dupe([]const u8, &.{run.pending}) else &.{};
        const acknowledged: []const []const u8 = if (run.acknowledged and run.pending.len > 0 and std.mem.eql(u8, run.pending, run.call_id)) try arena.dupe([]const u8, &.{run.call_id}) else &.{};
        return .{
            .run_id = run.id,
            .status = if (run.reservation()) .queued else run.status,
            .relationship = "primary",
            .queue_position = position,
            .as_of_sequence = run.next_sequence - 1,
            .pending_interactions = pending,
            .acknowledged_interactions = acknowledged,
        };
    }

    fn callable(self: *Session, arena: std.mem.Allocator) ![]const []const u8 {
        const names = try arena.alloc([]const u8, 1 + self.provided.len);
        names[0] = scripted_tool;
        for (self.provided, names[1..]) |tool, *slot| slot.* = tool.name;
        return names;
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const self = cast(ptr);
        var controls = try self.admitControls(arena, request, refusal);
        if (request.session_id.len == 0 or request.messages.len == 0) return error.InvalidSubmission;
        if (request.delivery != .auto and request.delivery != .queue) return error.InvalidSubmission;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        const is_busy = self.busy();
        if (is_busy) {
            if (self.reserved) |run| {
                if (run.live()) return error.RunActive;
            }
        }
        if (!is_busy and request.delivery != .queue and controls.model.len == 0) controls.model = self.current_model;

        const keep = self.keep.allocator();
        const reservation = is_busy or request.delivery == .queue;
        try self.runs.ensureUnusedCapacity(keep, 1);
        const run = try keep.create(Run);
        run.* = .{
            .id = try self.owner.nextID(keep, "run"),
            .permission_id = try self.owner.nextID(keep, "permission"),
            .input_id = try self.owner.nextID(keep, "input"),
            .tool_call_id = try self.owner.nextID(keep, "tool-call"),
            .model = controls.model,
            .instructions = if (controls.instructions) |text| try keep.dupe(u8, text) else null,
            .structured = controls.structured,
            .calls_tool = controls.calls_tool,
            .queued_admission = reservation,
            .status = if (reservation) .queued else .running,
        };
        if (!controls.calls_tool) {
            run.stage = .input;
        } else if (self.provided.len > 0) {
            run.stage = .call;
            run.call_id = try self.owner.nextID(keep, "call");
            run.provided = controls.elected;
        }
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
            .run_id = run.id,
            .status = .running,
            .model_id = if (controls.model.len > 0) controls.model else null,
            .message_ids = message_ids,
        };
        if (reservation) {
            admission.effective_delivery = .queue;
            admission.admission = .queued;
            admission.status = .queued;
            if (is_busy) admission.delivery_resolution = "session_busy";
        }
        if (!is_busy) try self.emitInitial(run);
        return admission;
    }

    fn admitControls(self: *Session, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *contract.Refusal) contract.Failure!Controls {
        var controls = Controls{ .instructions = request.instructions };
        if (request.model_id) |model| {
            if (!std.mem.eql(u8, model, model_primary) and !std.mem.eql(u8, model, model_secondary)) return refusal.missingModel(model);
            controls.model = if (std.mem.eql(u8, model, model_primary)) model_primary else model_secondary;
        }
        if (request.output_schema_json) |schema| {
            if (try outputSchemaDefect(arena, schema)) |detail| {
                refusal.* = .{ .feature = "run.structured_output", .reason = contract.reason_unsatisfiable, .field = "output_schema", .detail = detail };
                return error.UnsupportedFeature;
            }
            controls.structured = true;
        }
        const catalog = try self.callable(arena);
        var choice: ?contract.ToolChoice = null;
        if (request.tool_choice_json) |text| {
            const parsed = contract.parseToolChoice(arena, text) catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                error.InvalidPolicy => {
                    refusal.* = .{ .feature = "run.tool_selection", .reason = contract.reason_unsatisfiable, .detail = "tool_choice is not the typed policy" };
                    return error.UnsupportedFeature;
                },
            };
            if (parsed.allowed) |allowed| {
                for (allowed) |name| {
                    if (!listed(catalog, name)) {
                        refusal.* = .{ .feature = "run.tool_selection", .reason = contract.reason_unsatisfiable, .tool = name, .detail = "allowed names a tool outside the catalog" };
                        return error.UnsupportedFeature;
                    }
                }
            }
            choice = parsed;
        }
        controls.calls_tool = false;
        if (self.provided.len > 0) {
            for (self.provided) |tool| {
                if (choice == null or choice.?.permits(tool.name)) {
                    controls.elected = tool;
                    controls.calls_tool = true;
                    break;
                }
            }
        } else {
            controls.calls_tool = choice == null or choice.?.permits(scripted_tool);
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
        const text = if (run.instructions) |instructions| if (instructions.len == 0) lead else try std.fmt.allocPrint(a, "{s} {s}", .{ instructions, lead }) else lead;
        try self.emitDelta(a, run, text);
        if (!run.calls_tool) return self.requestInput(run);

        if (run.call_id.len > 0) {
            var provided = try self.providedCall(a, run, "");
            try provided.put("arguments_json", try parseValue(a, golden_arguments));
            return self.emit(run, "action.call.requested", provided.value(), false);
        }

        var call = try self.scriptedCall(a, run, false);
        try call.put("arguments_json", try parseValue(a, golden_arguments));
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

    fn scriptedCall(self: *Session, a: std.mem.Allocator, run: *Run, answered: bool) contract.Failure!Payload {
        var call = Payload.init(a);
        try call.run(self, run);
        try call.put("tool_call_id", .{ .string = run.tool_call_id });
        try call.put("requested_by", .{ .string = endpoint_id });
        if (answered) try call.put("responded_by", .{ .string = self.participant });
        try call.put("execution_owner", .{ .string = scripted_owner });
        try call.put("source", .{ .string = scripted_source });
        try call.put("name", .{ .string = scripted_tool });
        return call;
    }

    fn providedCall(self: *Session, a: std.mem.Allocator, run: *Run, request_id: []const u8) contract.Failure!Payload {
        const tool = run.provided.?;
        var call = Payload.init(a);
        try call.put("interaction_id", .{ .string = run.call_id });
        if (request_id.len > 0) try call.put("request_id", .{ .string = request_id });
        try call.run(self, run);
        try call.put("tool_call_id", .{ .string = run.tool_call_id });
        try call.put("requested_by", .{ .string = endpoint_id });
        try call.put("responded_by", .{ .string = self.participant });
        try call.put("execution_owner", .{ .string = tool.execution_owner });
        if (tool.source) |source| try call.put("source", .{ .string = source });
        try call.put("name", .{ .string = tool.name });
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
        const run_id, const responded_by = switch (resolution) {
            .permission => |request| .{ request.run_id, request.responded_by },
            .input => |request| .{ request.run_id, request.responded_by },
        };
        const run = self.findRun(run_id) orelse return error.RunNotFound;
        if (run.terminal) return error.InteractionNotFound;
        if (!run.started) return error.InteractionNotFound;
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
            .call, .terminal => return error.InteractionNotFound,
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
            var call = try self.scriptedCall(a, run, true);
            try self.emit(run, "action.call.cancelled", call.value(), false);
            var failure = Payload.init(a);
            try failure.run(self, run);
            try failure.put("error", try parseValue(a, "{\"code\":\"permission_denied\",\"message\":\"scripted tool permission denied\"}"));
            return self.emit(run, "run.failed", failure.value(), true);
        }
        var started = try self.scriptedCall(a, run, false);
        try started.put("arguments_json", try parseValue(a, golden_arguments));
        try self.emit(run, "action.call.started", started.value(), false);
        var completed = try self.scriptedCall(a, run, false);
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
        var listed_answers = std.json.Array.init(a);
        for (answers) |answer| {
            var answered = Payload.init(a);
            try answered.put("question_id", .{ .string = answer.question_id });
            if (answer.text) |text| try answered.put("text", .{ .string = text });
            if (answer.selected_option_ids.len > 0) {
                var selected = std.json.Array.init(a);
                for (answer.selected_option_ids) |option| try selected.append(.{ .string = option });
                try answered.put("selected_option_ids", .{ .array = selected });
            }
            try listed_answers.append(answered.value());
        }
        try resolved.put("answers", .{ .array = listed_answers });
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

    fn resolveCall(ptr: *anyopaque, arena: std.mem.Allocator, request_id: []const u8, request: *const oap_types.CallResolveRequest, refusal: *contract.Refusal) contract.Failure!oap_types.CallResolveResponse {
        _ = refusal;
        const self = cast(ptr);
        var answer = oap_types.CallResolveResponse{
            .interaction_id = request.interaction_id,
            .session_id = request.session_id,
            .run_id = request.run_id,
            .tool_call_id = request.tool_call_id,
            .accepted = false,
        };
        const run = self.findRun(request.run_id) orelse return refused(answer, "unknown_interaction", null);
        if (run.call_id.len == 0 or !std.mem.eql(u8, request.interaction_id, run.call_id) or !std.mem.eql(u8, request.session_id, self.id)) return refused(answer, "unknown_interaction", null);
        if (!std.mem.eql(u8, request.responded_by, self.participant) or !std.mem.eql(u8, request.requested_by, endpoint_id) or !std.mem.eql(u8, request.tool_call_id, run.tool_call_id)) return refused(answer, "wrong_responder", null);
        var arms: usize = 0;
        var arm: Arm = .started;
        if (request.started) {
            arms += 1;
            arm = .started;
        }
        if (request.result_json != null) {
            arms += 1;
            arm = .result;
        }
        if (request.err != null) {
            arms += 1;
            arm = .@"error";
        }
        if (arms != 1) return refused(answer, "unknown_interaction", null);
        if (run.settled) |settled| {
            const settlement = if (run.settlement_id.len > 0) run.settlement_id else settled.request_id;
            if (arm == .started and run.settlement_id.len == 0) return refused(answer, "late_acknowledgement", null);
            return refused(answer, "already_resolved", settlement);
        }
        if (run.terminal or run.stage != .call) return refused(answer, "already_resolved", run.settlement_id);
        if (arm == .started and run.acknowledged) return refused(answer, "repeated_acknowledgement", null);

        answer.accepted = true;
        const keep = self.keep.allocator();
        if (arm == .started) {
            run.acknowledged = true;
            var scratch = std.heap.ArenaAllocator.init(self.gpa);
            defer scratch.deinit();
            var call = try self.providedCall(scratch.allocator(), run, request_id);
            try call.put("arguments_json", try parseValue(scratch.allocator(), golden_arguments));
            try self.emit(run, "action.call.started", call.value(), false);
            return answer;
        }
        var settled = Settled{ .arm = arm, .request_id = try keep.dupe(u8, request_id) };
        if (request.result_json) |result| settled.result_json = try keep.dupe(u8, result);
        if (request.err) |failure| {
            settled.code = try keep.dupe(u8, failure.code);
            settled.message = try keep.dupe(u8, failure.message);
        }
        run.settled = settled;
        run.stage = .input;
        try self.settleCall(run);
        _ = arena;
        return answer;
    }

    fn refused(answer: oap_types.CallResolveResponse, reason: []const u8, settlement: ?[]const u8) oap_types.CallResolveResponse {
        var refusal = answer;
        refusal.accepted = false;
        refusal.reason = reason;
        if (std.mem.eql(u8, reason, "already_resolved")) refusal.settlement_id = settlement;
        return refusal;
    }

    fn settleCall(self: *Session, run: *Run) contract.Failure!void {
        const settled = run.settled.?;
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        if (!run.acknowledged) {
            var started = try self.providedCall(a, run, settled.request_id);
            try started.put("arguments_json", try parseValue(a, golden_arguments));
            try self.emit(run, "action.call.started", started.value(), false);
        }
        var terminal = try self.providedCall(a, run, settled.request_id);
        if (settled.arm == .@"error") {
            var failure = Payload.init(a);
            try failure.put("code", .{ .string = settled.code });
            try failure.put("message", .{ .string = settled.message });
            try terminal.put("error", failure.value());
            try self.emit(run, "action.call.failed", terminal.value(), false);
        } else {
            try terminal.put("result", try parseValue(a, settled.result_json orelse "null"));
            try self.emit(run, "action.call.completed", terminal.value(), false);
        }
        return self.requestInput(run);
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
        if (run.stage == .call) {
            var call = try self.providedCall(a, run, "");
            try self.emit(run, "action.call.cancelled", call.value(), false);
        }
        switch (run.stage) {
            .permission => {
                var resolved = Payload.init(a);
                try resolved.interaction(self, run.permission_id);
                try resolved.run(self, run);
                try resolved.put("tool_call_id", .{ .string = run.tool_call_id });
                try resolved.put("outcome", .{ .string = "cancelled" });
                try resolved.put("reason", try parseValue(a, "{\"code\":\"run_cancelled\",\"message\":\"run cancellation closed the permission request\"}"));
                try self.emit(run, "action.permission.resolved", resolved.value(), false);
                var call = try self.scriptedCall(a, run, true);
                try self.emit(run, "action.call.cancelled", call.value(), false);
            },
            .input => {
                var resolved = Payload.init(a);
                try resolved.interaction(self, run.input_id);
                try resolved.run(self, run);
                try resolved.put("status", .{ .string = "cancelled" });
                try self.emit(run, "user.input.resolved", resolved.value(), false);
            },
            .call, .terminal => {},
        }
        var cancelled = Payload.init(a);
        try cancelled.run(self, run);
        try cancelled.put("reason", .{ .string = "cancel confirmed" });
        try self.emit(run, "run.cancelled", cancelled.value(), true);
        return .{ .session_id = self.id, .run_id = owned_run_id, .accepted = true, .status = .cancelling };
    }

    fn emit(self: *Session, run: *Run, kind: []const u8, payload: std.json.Value, terminal: bool) contract.Failure!void {
        if (run.terminal) return;
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
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
        try self.store(run, try json_encode.valueAlloc(self.gpa, envelope.value()), sequence);
        run.next_sequence += 1;
        self.transcript_cursor = sequence;
        self.updated_at_ms = now;

        if (std.mem.eql(u8, kind, "action.permission.requested")) run.pending = run.permission_id;
        if (std.mem.eql(u8, kind, "user.input.requested")) run.pending = run.input_id;
        if (std.mem.eql(u8, kind, "action.permission.resolved") or std.mem.eql(u8, kind, "user.input.resolved")) run.pending = "";
        if (std.mem.eql(u8, kind, "action.call.requested") and run.call_id.len > 0) run.pending = run.call_id;
        if (run.call_id.len > 0 and std.mem.eql(u8, run.pending, run.call_id) and
            (std.mem.eql(u8, kind, "action.call.completed") or std.mem.eql(u8, kind, "action.call.failed") or std.mem.eql(u8, kind, "action.call.cancelled")))
        {
            run.pending = "";
            run.settlement_id = try self.keep.allocator().dupe(u8, event_id);
        }
        if (!terminal) return;
        run.terminal = true;
        run.pending = "";
        if (std.mem.eql(u8, kind, "run.completed")) run.status = .completed;
        if (std.mem.eql(u8, kind, "run.failed")) run.status = .failed;
        if (std.mem.eql(u8, kind, "run.cancelled")) run.status = .cancelled;
        try self.settled.append(self.keep.allocator(), .{ .run_id = run.id, .sequence = sequence });
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

    fn store(self: *Session, run: *Run, line: []u8, sequence: u64) !void {
        const gpa = self.gpa;
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
    }

    fn carriesToolCall(kind: []const u8) bool {
        return std.mem.startsWith(u8, kind, "action.call.") or std.mem.eql(u8, kind, "action.permission.requested") or std.mem.eql(u8, kind, "user.input.requested");
    }

    fn pump(ptr: *anyopaque, wait_ns: u64) contract.Failure!bool {
        _ = ptr;
        _ = wait_ns;
        return false;
    }

    fn copyEvent(allocator: std.mem.Allocator, entry: Journaled) !contract.Event {
        const line = try allocator.dupe(u8, entry.line);
        errdefer allocator.free(line);
        const run_id = try allocator.dupe(u8, entry.run_id);
        return .{ .line = line, .run_id = run_id, .sequence = entry.sequence };
    }

    fn drain(ptr: *anyopaque, allocator: std.mem.Allocator, out: *std.ArrayList(contract.Event)) contract.Failure!void {
        const self = cast(ptr);
        try out.ensureUnusedCapacity(allocator, self.outbox.items.len);
        for (self.outbox.items) |queued| {
            out.appendAssumeCapacity(try copyEvent(allocator, queued));
        }
        for (self.outbox.items) |queued| self.gpa.free(queued.line);
        self.outbox.clearRetainingCapacity();
    }

    fn activity(ptr: *anyopaque) contract.Activity {
        const self = cast(ptr);
        const run = self.active orelse return .idle;
        if (!run.live()) return .idle;
        return if (run.pending.len > 0 or run.status == .waiting_for_input) .waiting else .running;
    }

    fn close(ptr: *anyopaque) void {
        cast(ptr).destroy();
    }

    fn tools(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ToolsListRequest, refusal: *contract.Refusal) contract.Failure!oap_types.ToolsListResponse {
        _ = refusal;
        const self = cast(ptr);
        const named = request.session_id orelse "";
        if (named.len > 0 and !std.mem.eql(u8, named, self.id)) return error.RunNotFound;
        const sources = if (named.len > 0) try self.sessionSources(arena) else try arena.dupe(oap_types.ToolSourceDescriptor, &declared_sources);
        const definitions = try arena.alloc(oap_types.ToolDefinition, 1 + if (named.len > 0) self.provided.len else 0);
        definitions[0] = scripted_catalog[0];
        if (named.len > 0) @memcpy(definitions[1..], self.provided);
        return .{ .session_id = request.session_id, .sources = sources, .tools = definitions };
    }

    fn models(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ModelsRequest, refusal: *contract.Refusal) contract.Failure!oap_types.ModelsResponse {
        _ = refusal;
        const self = cast(ptr);
        if (request.session_id.len > 0 and !std.mem.eql(u8, request.session_id, self.id)) return error.InvalidSubmission;
        const catalog = try arena.dupe(oap_types.ModelDescriptor, &.{
            .{ .id = model_primary, .display_name = "Reference Model A", .provider_id = "reference", .context_window = 8192, .default = true },
            .{ .id = model_secondary, .display_name = "Reference Model B", .provider_id = "reference", .context_window = 8192 },
        });
        const providers = try arena.dupe(oap_types.ProviderDescriptor, &.{
            .{ .id = "reference", .display_name = "Reference Provider", .wire = "openai-chat-completions", .kind = "direct" },
        });
        return .{ .session_id = self.id, .current_model_id = if (self.current_model.len > 0) self.current_model else null, .models = catalog, .providers = providers };
    }

    fn switchModel(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.SessionModelSwitchRequest, refusal: *contract.Refusal) contract.Failure!contract.Switched {
        const self = cast(ptr);
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
        const run = self.findRun(run_id) orelse return error.RunNotFound;
        const latest = run.next_sequence - 1;
        if (after > latest) return error.ReplayCursorFuture;
        var oldest: u64 = 0;
        var suffix = std.ArrayList(contract.Event).empty;
        for (self.journal.items) |kept| {
            if (!std.mem.eql(u8, kept.run_id, run.id)) continue;
            if (oldest == 0) oldest = kept.sequence;
            if (kept.sequence > after) {
                try suffix.ensureUnusedCapacity(allocator, 1);
                suffix.appendAssumeCapacity(try copyEvent(allocator, kept));
            }
        }
        if (after < latest and (oldest == 0 or after + 1 < oldest)) {
            return .{ .gap = .{ .requested_after = after, .oldest_available = oldest, .latest_available = latest } };
        }
        return .{ .events = suffix.items };
    }

    fn admitProvidedTools(self: *Session, arena: std.mem.Allocator, carried: ?[]const u8, refusal: *contract.Refusal) contract.Failure![]oap_types.ToolDefinition {
        const text = carried orelse return &.{};
        const keep = self.keep.allocator();
        const document = std.json.parseFromSliceLeaky(std.json.Value, keep, text, .{ .allocate = .alloc_always }) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            return refuseTool(arena, refusal, "", "the provided tools are not a list of tool definitions");
        };
        if (document != .array) return refuseTool(arena, refusal, "", "the provided tools are not a list of tool definitions");
        const items = document.array.items;
        if (items.len == 0) return &.{};
        if (items.len > max_provided_tools) {
            return refuseTool(arena, refusal, try keep.dupe(u8, stringOf(items[max_provided_tools], "name")), try std.fmt.allocPrint(keep, "at most {d} tools may be provided", .{max_provided_tools}));
        }
        const sources = try self.sessionSources(arena);
        const provided = try keep.alloc(oap_types.ToolDefinition, items.len);
        for (items, provided, 0..) |item, *slot, index| {
            if (item != .object) return refuseTool(arena, refusal, "", "a provided tool needs a name");
            const name = stringOf(item, "name");
            const owner = stringOf(item, "execution_owner");
            const source = stringOf(item, "source");
            if (name.len == 0) return refuseTool(arena, refusal, "", "a provided tool needs a name");
            if (!std.mem.eql(u8, owner, self.participant)) return refuseTool(arena, refusal, name, "execution_owner must be the opening participant");
            if (source.len > 0 and !sourceListed(sources, source)) return refuseTool(arena, refusal, name, try std.fmt.allocPrint(keep, "source {s} resolves to no declared or attached source", .{source}));
            if (std.mem.eql(u8, name, scripted_tool) or providedNamed(provided[0..index], name)) return refuseTool(arena, refusal, name, "the name already resolves to a catalog entry");
            if (!namePatternMatches(name)) return refuseTool(arena, refusal, name, "the name is outside the disclosed name_pattern " ++ provided_name_pattern);
            const schema = item.object.get("input_schema");
            if (!admissibleDialect(schema)) return refuseTool(arena, refusal, name, "the input schema declares a dialect outside the disclosed " ++ provided_dialect);
            slot.* = .{
                .name = name,
                .description = if (stringOf(item, "description").len > 0) stringOf(item, "description") else null,
                .input_schema_json = if (schema) |value| try json_encode.valueAlloc(keep, value) else "null",
                .execution_owner = owner,
                .source = if (source.len > 0) source else null,
                .features = try toolFeatures(keep, item.object.get("features")),
            };
        }
        return provided;
    }
};

fn refuseTool(arena: std.mem.Allocator, refusal: *contract.Refusal, tool: []const u8, detail: []const u8) contract.Failure {
    refusal.* = .{ .feature = contract.feature_tools_provide, .reason = contract.reason_unsatisfiable, .tool = try arena.dupe(u8, tool), .detail = try arena.dupe(u8, detail) };
    return error.UnsupportedFeature;
}

fn refuseSource(arena: std.mem.Allocator, refusal: *contract.Refusal, source: []const u8, detail: []const u8) contract.Failure {
    refusal.* = .{ .feature = contract.feature_tool_sources_attach, .reason = contract.reason_unsatisfiable, .source = try arena.dupe(u8, source), .detail = try arena.dupe(u8, detail) };
    return error.UnsupportedFeature;
}

fn admitToolSources(keep: std.mem.Allocator, arena: std.mem.Allocator, carried: ?[]const u8, refusal: *contract.Refusal) contract.Failure![]oap_types.ToolSourceDescriptor {
    const text = carried orelse return &.{};
    const document = std.json.parseFromSliceLeaky(std.json.Value, keep, text, .{ .allocate = .alloc_always }) catch |err| {
        if (err == error.OutOfMemory) return error.OutOfMemory;
        return refuseSource(arena, refusal, "", "the tool sources are not a list of attachments");
    };
    if (document != .array) return refuseSource(arena, refusal, "", "the tool sources are not a list of attachments");
    const items = document.array.items;
    if (items.len == 0) return &.{};
    if (items.len > max_attached_sources) {
        return refuseSource(arena, refusal, stringOf(items[max_attached_sources], "id"), try std.fmt.allocPrint(keep, "at most {d} sources may be attached", .{max_attached_sources}));
    }
    const attached = try keep.alloc(oap_types.ToolSourceDescriptor, items.len);
    for (items, attached, 0..) |item, *slot, index| {
        const id = stringOf(item, "id");
        const kind = stringOf(item, "kind");
        if (id.len == 0 or kind.len == 0) return refuseSource(arena, refusal, id, "an attachment needs an id and a kind");
        if (sourceListed(&declared_sources, id) or sourceListed(attached[0..index], id)) return refuseSource(arena, refusal, id, "the id already resolves to a declared or attached source");
        if (!listed(&attach_transports, kind)) return refuseSource(arena, refusal, id, try std.fmt.allocPrint(keep, "kind {s} is outside the disclosed transports", .{kind}));
        if (try duplicateEnvironmentName(keep, item)) |name| return refuseSource(arena, refusal, id, try std.fmt.allocPrint(keep, "environment names {s} twice", .{name}));
        slot.* = .{
            .id = id,
            .kind = kind,
            .display_name = optionalString(item, "display_name"),
            .protocol = optionalString(item, "protocol"),
            .endpoint = optionalString(item, "endpoint"),
        };
    }
    return attached;
}

fn duplicateEnvironmentName(keep: std.mem.Allocator, item: std.json.Value) !?[]const u8 {
    _ = keep;
    if (item != .object) return null;
    const environment = item.object.get("environment") orelse return null;
    if (environment != .array or environment.array.items.len < 2) return null;
    for (environment.array.items, 0..) |entry, index| {
        if (entry != .string) continue;
        const name = envName(entry.string);
        for (environment.array.items[0..index]) |earlier| {
            if (earlier == .string and std.mem.eql(u8, envName(earlier.string), name)) return name;
        }
    }
    return null;
}

fn envName(entry: []const u8) []const u8 {
    const cut = std.mem.indexOfScalar(u8, entry, '=') orelse return entry;
    return entry[0..cut];
}

fn toolFeatures(keep: std.mem.Allocator, carried: ?std.json.Value) ![]oap_types.Feature {
    const value = carried orelse return &.{};
    if (value != .object) return &.{};
    const result = try keep.alloc(oap_types.Feature, value.object.count());
    var filled: usize = 0;
    var it = value.object.iterator();
    while (it.next()) |declared| {
        const support = declared.value_ptr.*;
        const level = std.meta.stringToEnum(oap_types.SupportLevel, stringOf(support, "level")) orelse continue;
        const modes = try featureModes(keep, support);
        const constraints_json = try featureObjectJson(keep, support, "constraints");
        const limits_json = try featureObjectJson(keep, support, "limits");
        result[filled] = .{ .key = declared.key_ptr.*, .level = level, .reason = optionalString(support, "reason"), .scope = optionalString(support, "scope"), .modes = modes, .constraints_json = constraints_json, .limits_json = limits_json };
        filled += 1;
    }
    return result[0..filled];
}

fn featureModes(keep: std.mem.Allocator, support: std.json.Value) ![]const []const u8 {
    if (support != .object) return &.{};
    const carried = support.object.get("modes") orelse return &.{};
    if (carried != .array) return &.{};
    const modes = try keep.alloc([]const u8, carried.array.items.len);
    var filled: usize = 0;
    for (carried.array.items) |mode| {
        if (mode != .string) continue;
        modes[filled] = mode.string;
        filled += 1;
    }
    return modes[0..filled];
}

fn featureObjectJson(keep: std.mem.Allocator, support: std.json.Value, key: []const u8) !?[]const u8 {
    if (support != .object) return null;
    const carried = support.object.get(key) orelse return null;
    if (carried != .object) return null;
    return try std.json.Stringify.valueAlloc(keep, carried, .{});
}

fn stringOf(value: std.json.Value, key: []const u8) []const u8 {
    if (value != .object) return "";
    const carried = value.object.get(key) orelse return "";
    return if (carried == .string) carried.string else "";
}

fn optionalString(value: std.json.Value, key: []const u8) ?[]const u8 {
    const text = stringOf(value, key);
    return if (text.len > 0) text else null;
}

fn sourceListed(sources: []const oap_types.ToolSourceDescriptor, id: []const u8) bool {
    for (sources) |source| {
        if (std.mem.eql(u8, source.id, id)) return true;
    }
    return false;
}

fn providedNamed(tools: []const oap_types.ToolDefinition, name: []const u8) bool {
    for (tools) |tool| {
        if (std.mem.eql(u8, tool.name, name)) return true;
    }
    return false;
}

fn namePatternMatches(name: []const u8) bool {
    if (name.len == 0 or name[0] < 'a' or name[0] > 'z') return false;
    for (name[1..]) |c| {
        const ok = (c >= 'a' and c <= 'z') or (c >= '0' and c <= '9') or c == '_';
        if (!ok) return false;
    }
    return true;
}

fn admissibleDialect(schema: ?std.json.Value) bool {
    const value = schema orelse return true;
    if (value == .null) return true;
    if (value != .object) return false;
    const declared = value.object.get("$schema") orelse return true;
    if (declared == .null) return true;
    if (declared != .string) return false;
    return declared.string.len == 0 or std.mem.eql(u8, declared.string, provided_dialect);
}

fn listed(names: []const []const u8, name: []const u8) bool {
    for (names) |candidate| {
        if (std.mem.eql(u8, candidate, name)) return true;
    }
    return false;
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

    fn interaction(self: *Payload, session: *Session, interaction_id: []const u8) !void {
        try self.put("interaction_id", .{ .string = interaction_id });
        try self.put("requested_by", .{ .string = endpoint_id });
        try self.put("responded_by", .{ .string = session.participant });
    }
};

fn parseValue(allocator: std.mem.Allocator, text: []const u8) !std.json.Value {
    return std.json.parseFromSliceLeaky(std.json.Value, allocator, text, .{}) catch |err| switch (err) {
        error.OutOfMemory => error.OutOfMemory,
        else => error.InvalidResolution,
    };
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
    const result = parseValue(arena, fixed_result) catch return error.OutOfMemory;
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
        return self.initWith(null, null);
    }

    fn initWith(self: *Probe, tools_json: ?[]const u8, sources_json: ?[]const u8) !void {
        self.adapter = Adapter.init(testing.allocator);
        self.adapter.now_ms = fixedClock;
        self.arena = std.heap.ArenaAllocator.init(testing.allocator);
        self.seen = .empty;
        var refusal = contract.Refusal{};
        self.session = self.adapter.adapter().open(self.arena.allocator(), .{ .session_id = "s1", .participant = "user", .tools_json = tools_json, .tool_sources_json = sources_json }, &refusal) catch |err| {
            self.arena.deinit();
            return err;
        };
    }

    fn resolveCall(self: *Probe, run_id: []const u8, call_id: []const u8, tool_call_id: []const u8, arm: []const u8) contract.Failure!oap_types.CallResolveResponse {
        var refusal = contract.Refusal{};
        var request = oap_types.CallResolveRequest{ .interaction_id = call_id, .session_id = "s1", .run_id = run_id, .tool_call_id = tool_call_id, .requested_by = endpoint_id, .responded_by = "user" };
        if (std.mem.eql(u8, arm, "started")) request.started = true;
        if (std.mem.eql(u8, arm, "result")) request.result_json = "{\"hits\":1}";
        if (std.mem.eql(u8, arm, "error")) request.err = .{ .code = "tool_failed", .message = "no hits" };
        return self.session.vtable.resolve_call.?(self.session.ptr, self.a(), "req-9", &request, &refusal);
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

test "a queued run's gates cannot be resolved before it starts" {
    var probe: Probe = undefined;
    try probe.init();
    defer probe.deinit();
    _ = try probe.submit();
    const queued = try probe.submitWith(.auto, .{ .session_id = "", .messages = &.{}, .delivery = .auto });
    const minted = try std.fmt.parseInt(u64, queued.run_id.?["run-".len..], 10);
    const permission = try std.fmt.allocPrint(probe.a(), "permission-{d}", .{minted + 1});
    const input = try std.fmt.allocPrint(probe.a(), "input-{d}", .{minted + 2});
    try testing.expectError(error.InteractionNotFound, probe.approve(queued.run_id.?, permission, "approve"));
    try testing.expectError(error.InteractionNotFound, probe.answer(queued.run_id.?, input));
}

test "empty instructions leave the scripted text unprefixed" {
    var probe: Probe = undefined;
    try probe.init();
    defer probe.deinit();
    _ = try probe.submitWith(.auto, .{ .session_id = "", .messages = &.{}, .delivery = .auto, .instructions = "" });
    try probe.session.drain(probe.a(), &probe.seen);
    try testing.expect(std.mem.indexOf(u8, probe.seen.items[1].line, "\"text\":\"I will use the scripted tool.\"") != null);
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

fn submitAndSettle(allocator: std.mem.Allocator) !void {
    var adapter = Adapter.init(allocator);
    adapter.now_ms = fixedClock;
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    var refusal = contract.Refusal{};
    const session = try adapter.adapter().open(arena.allocator(), .{ .session_id = "s1", .participant = "user" }, &refusal);
    defer session.close();
    const messages = try arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = "run" } }});
    const request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = messages, .delivery = .auto, .instructions = "Be brief." };
    _ = try session.submit(arena.allocator(), &request, &refusal);
    _ = try session.submit(arena.allocator(), &request, &refusal);
    _ = try session.cancel(arena.allocator(), "run-1", &refusal);
    var drained = std.ArrayList(contract.Event).empty;
    try session.drain(arena.allocator(), &drained);
}

test "a submit, a queued submit and the promoting cancel free everything they built when an allocation fails" {
    try testing.checkAllAllocationFailures(testing.allocator, submitAndSettle, .{});
}

const provided_lookup = "[{\"name\":\"lookup\",\"input_schema\":{\"type\":\"object\"},\"execution_owner\":\"user\",\"source\":\"att1\"}]";
const attached_local = "[{\"id\":\"att1\",\"kind\":\"local\",\"display_name\":\"Attached\"}]";

test "a provided tool is called through the resolve pair: acknowledge, settle, then the input gate" {
    var probe: Probe = undefined;
    try probe.initWith(provided_lookup, attached_local);
    defer probe.deinit();
    const admitted = try probe.submit();
    const run_id = admitted.run_id.?;
    try testing.expectEqual(contract.Activity.waiting, probe.session.activity());
    const wrong = try probe.resolveCall(run_id, "call-9", "tool-call-4", "started");
    try testing.expectEqualStrings("unknown_interaction", wrong.reason.?);
    const acknowledged = try probe.resolveCall(run_id, "call-5", "tool-call-4", "started");
    try testing.expect(acknowledged.accepted);
    const again = try probe.resolveCall(run_id, "call-5", "tool-call-4", "started");
    try testing.expectEqualStrings("repeated_acknowledgement", again.reason.?);
    const settled = try probe.resolveCall(run_id, "call-5", "tool-call-4", "result");
    try testing.expect(settled.accepted);
    const late = try probe.resolveCall(run_id, "call-5", "tool-call-4", "error");
    try testing.expectEqualStrings("already_resolved", late.reason.?);
    try testing.expect(late.settlement_id != null);
    const kinds = try probe.types();
    try expectTypes(&.{ "run.started", "content.delta", "action.call.requested", "action.call.started", "action.call.completed", "user.input.requested", "run.status.updated" }, kinds);
    try testing.expect(std.mem.indexOf(u8, probe.seen.items[4].line, "\"request_id\":\"req-9\"") != null);
    try testing.expect(std.mem.indexOf(u8, probe.seen.items[4].line, "\"result\":{\"hits\":1}") != null);
}

test "an unacknowledged error settlement starts the call before failing it" {
    var probe: Probe = undefined;
    try probe.initWith(provided_lookup, attached_local);
    defer probe.deinit();
    const admitted = try probe.submit();
    const settled = try probe.resolveCall(admitted.run_id.?, "call-5", "tool-call-4", "error");
    try testing.expect(settled.accepted);
    const kinds = try probe.types();
    try expectTypes(&.{ "action.call.started", "action.call.failed", "user.input.requested" }, kinds[3..6]);
    const late_ack = try probe.resolveCall(admitted.run_id.?, "call-5", "tool-call-4", "started");
    try testing.expectEqualStrings("already_resolved", late_ack.reason.?);
}

test "state reports the queued run, the pending call, the cursor, the sources and what settled" {
    var probe: Probe = undefined;
    try probe.initWith(provided_lookup, attached_local);
    defer probe.deinit();
    _ = try probe.submit();
    _ = try probe.submit();
    var refusal = contract.Refusal{};
    const busy = try probe.session.state(probe.a(), &refusal);
    try testing.expectEqual(@as(usize, 2), busy.active_runs.len);
    try testing.expectEqualStrings("call-5", busy.active_runs[0].pending_interactions[0]);
    try testing.expectEqual(@as(?u64, 1), busy.active_runs[1].queue_position);
    try testing.expectEqual(oap_types.RunStatus.queued, busy.active_runs[1].status);
    try testing.expectEqualStrings("3", busy.transcript_cursor.?);
    try testing.expectEqual(@as(usize, 3), busy.sources.len);
    _ = try probe.session.cancel(probe.a(), "run-1", &refusal);
    const after = try probe.session.state(probe.a(), &refusal);
    try testing.expectEqualStrings("run-1", after.as_of.?.settled[0].run_id.?);
    try testing.expectEqualStrings(busy.active_runs[1].run_id, after.active_run_id.?);
}

test "an open whose tools or sources break a disclosed limit is refused naming the offender" {
    const cases = [_]struct { tools: ?[]const u8, sources: ?[]const u8, feature: []const u8, offender: []const u8 }{
        .{ .tools = "[{\"name\":\"Bad\",\"execution_owner\":\"user\"}]", .sources = null, .feature = contract.feature_tools_provide, .offender = "Bad" },
        .{ .tools = "[{\"name\":\"x\",\"execution_owner\":\"someone\"}]", .sources = null, .feature = contract.feature_tools_provide, .offender = "x" },
        .{ .tools = "[{\"name\":\"scripted_tool\",\"execution_owner\":\"user\"}]", .sources = null, .feature = contract.feature_tools_provide, .offender = "scripted_tool" },
        .{ .tools = "[{\"name\":\"x\",\"execution_owner\":\"user\",\"source\":\"nowhere\"}]", .sources = null, .feature = contract.feature_tools_provide, .offender = "x" },
        .{ .tools = "[{\"name\":\"x\",\"execution_owner\":\"user\",\"input_schema\":{\"$schema\":\"http://json-schema.org/draft-07/schema#\"}}]", .sources = null, .feature = contract.feature_tools_provide, .offender = "x" },
        .{ .tools = "[{\"name\":\"a\",\"execution_owner\":\"user\"},{\"name\":\"b\",\"execution_owner\":\"user\"},{\"name\":\"c\",\"execution_owner\":\"user\"}]", .sources = null, .feature = contract.feature_tools_provide, .offender = "c" },
        .{ .tools = null, .sources = "[{\"id\":\"reference-mcp\",\"kind\":\"local\"}]", .feature = contract.feature_tool_sources_attach, .offender = "reference-mcp" },
        .{ .tools = null, .sources = "[{\"id\":\"s\",\"kind\":\"http\"}]", .feature = contract.feature_tool_sources_attach, .offender = "s" },
        .{ .tools = null, .sources = "[{\"id\":\"s\",\"kind\":\"local\",\"environment\":[\"A\",\"A\"]}]", .feature = contract.feature_tool_sources_attach, .offender = "s" },
        .{ .tools = null, .sources = "[{\"id\":\"a\",\"kind\":\"local\"},{\"id\":\"b\",\"kind\":\"local\"},{\"id\":\"c\",\"kind\":\"local\"}]", .feature = contract.feature_tool_sources_attach, .offender = "c" },
    };
    for (cases) |case| {
        var adapter = Adapter.init(testing.allocator);
        var arena = std.heap.ArenaAllocator.init(testing.allocator);
        defer arena.deinit();
        var refusal = contract.Refusal{};
        try testing.expectError(error.UnsupportedFeature, adapter.adapter().open(arena.allocator(), .{ .session_id = "s1", .participant = "user", .tools_json = case.tools, .tool_sources_json = case.sources }, &refusal));
        try testing.expectEqualStrings(case.feature, refusal.feature);
        try testing.expectEqualStrings(case.offender, if (refusal.tool.len > 0) refusal.tool else refusal.source);
    }
}

fn provideAndSettle(allocator: std.mem.Allocator) !void {
    var adapter = Adapter.init(allocator);
    adapter.now_ms = fixedClock;
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    var refusal = contract.Refusal{};
    const session = try adapter.adapter().open(arena.allocator(), .{ .session_id = "s1", .participant = "user", .tools_json = provided_lookup, .tool_sources_json = attached_local }, &refusal);
    defer session.close();
    const messages = try arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = "run" } }});
    const request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = messages, .delivery = .auto };
    const admitted = try session.submit(arena.allocator(), &request, &refusal);
    var call = oap_types.CallResolveRequest{ .interaction_id = "call-5", .session_id = "s1", .run_id = admitted.run_id.?, .tool_call_id = "tool-call-4", .requested_by = endpoint_id, .responded_by = "user", .result_json = "{}" };
    _ = try session.vtable.resolve_call.?(session.ptr, arena.allocator(), "req-1", &call, &refusal);
    var state_refusal = contract.Refusal{};
    _ = try session.state(arena.allocator(), &state_refusal);
    var drained = std.ArrayList(contract.Event).empty;
    try session.drain(arena.allocator(), &drained);
}

test "an open with provided tools, a submit and a settled call free everything they built when an allocation fails" {
    try testing.checkAllAllocationFailures(testing.allocator, provideAndSettle, .{});
}

test "a provided tool keeps its declared feature modes, constraints and limits, and a null $schema is admitted" {
    var probe: Probe = undefined;
    try probe.initWith("[{\"name\":\"lookup\",\"input_schema\":{\"$schema\":null,\"type\":\"object\"},\"execution_owner\":\"user\",\"source\":\"att1\",\"features\":{\"x\":{\"level\":\"degraded\",\"modes\":[\"session_open\"],\"constraints\":{\"b\":1,\"a\":2},\"limits\":{\"max\":3}}}}]", attached_local);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const listed_tools = try probe.session.vtable.tools.?(probe.session.ptr, probe.a(), &.{ .session_id = "s1" }, &refusal);
    const feature = listed_tools.tools[1].features[0];
    try testing.expectEqualStrings("x", feature.key);
    try testing.expectEqual(@as(usize, 1), feature.modes.len);
    try testing.expectEqualStrings("session_open", feature.modes[0]);
    try testing.expectEqualStrings("{\"b\":1,\"a\":2}", feature.constraints_json.?);
    try testing.expectEqualStrings("{\"max\":3}", feature.limits_json.?);
}
