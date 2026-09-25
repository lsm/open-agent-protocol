const std = @import("std");
const harness_pins = @import("harness_pins");
const builtin = @import("builtin");
const contract = @import("contract");
const oap_types = @import("oap_types");
const backend = @import("backend");
const session = @import("session");
const rpc = @import("rpc");
const compat = @import("compat");
const json_encode = @import("json_encode");

pub const endpoint_id = session.endpoint_id;
pub const capability_revision = harness_pins.claude_code_capability_revision;
pub const pinned_version = harness_pins.claude_code_endpoint_version;
pub const denied_message = "Denied by the operator";
const harness_owner = session.harness_owner;
const native_source = session.native_source;

const features = [_]contract.Feature{
    .{ .key = "protocol.initialize", .level = .emulated, .reason = "initialize control exchange at open; no capability negotiation" },
    .{ .key = "capabilities", .level = .emulated, .reason = "conservative descriptor; per-turn system/init refresh recorded as evidence" },
    .{ .key = "session.open", .level = .emulated, .reason = "process spawn + initialize; CLI session UUID observed on frames" },
    .{ .key = "session.state", .level = .degraded, .reason = "reducer-owned live projection" },
    .{ .key = "session.message.submit", .level = .degraded, .reason = "host-minted turn uuid correlated by the user_message_uuid echo" },
    .{ .key = "session.message.delivery.auto", .level = .degraded, .reason = "accepted only when the CLI session is idle" },
    .{ .key = "session.message.delivery.queue", .level = .unavailable, .reason = "queued continuation turns are not exposed in v1" },
    .{ .key = "session.message.delivery.steer", .level = .unavailable, .reason = "shouldQuery/priority are unexercised" },
    .{ .key = "run.streaming", .level = .native, .reason = "stream_event deltas with --include-partial-messages always on" },
    .{ .key = "run.status", .level = .emulated },
    .{ .key = "run.cancel", .level = .degraded, .reason = "interrupt intent; settlement only via terminal_reason aborted_*" },
    .{ .key = "run.resume", .level = .degraded, .reason = "native conversation resume is not exercised; OAP resume replays the adapter journal" },
    .{ .key = "run.replay", .level = .degraded, .reason = "bounded adapter journal; gaps are explicit and transcript persistence is not event replay" },
    .{ .key = "run.reconciliation", .level = .degraded, .reason = "system/init and session state frames corroborate" },
    .{ .key = "run.tool_selection", .level = .emulated, .scope = "run", .reason = "enforced per call: a PreToolUse hook, and the can_use_tool gate behind it, refuse an excluded tool before it runs and the call settles refused_by_policy; not retained past the run" },
    .{ .key = "action.tools", .level = .degraded, .reason = "tool_use/tool_result projection; started synthesized; tool_progress observed-only" },
    .{ .key = "action.tools.execute", .level = .unavailable, .reason = "the CLI executes tools internally" },
    .{ .key = contract.feature_tools_list, .level = .degraded, .reason = "system/init republishes the tool and MCP server lists per turn; there is none before the first" },
    .{ .key = "action.permissions", .level = .native, .reason = "can_use_tool reverse control requests" },
    .{ .key = "user_input", .level = .native, .reason = "permission gates over the control plane" },
};

const native_sources = [_]oap_types.ToolSourceDescriptor{
    .{ .id = native_source, .kind = "native", .display_name = "Claude Code built-in tools" },
};

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = "Claude Code Adapter", .version = pinned_version, .adapter = "claude-code-stream-json" },
    .capability_revision = capability_revision,
    .features = &features,
    .sources = &native_sources,
};

const decision_question = contract.Question{ .id = "decision", .kind = .single_choice, .options = &.{ "allow", "deny" } };

pub const Config = struct {
    backend: backend.Config,
    initialize_timeout_ns: u64 = 60 * std.time.ns_per_s,
    admission_timeout_ns: u64 = 10 * 60 * std.time.ns_per_s,
    control_timeout_ns: u64 = 60 * std.time.ns_per_s,
    poll_ns: u64 = 5 * std.time.ns_per_ms,
};

fn wallClock() i64 {
    return compat.time.nowMillis();
}

fn monotonic() u64 {
    return compat.time.monotonicNanos() catch 0;
}

pub const Adapter = struct {
    allocator: std.mem.Allocator,
    config: Config,
    ids: usize = 0,

    pub fn init(allocator: std.mem.Allocator, config: Config) Adapter {
        return .{ .allocator = allocator, .config = config };
    }

    pub fn adapter(self: *Adapter) contract.Adapter {
        return .{ .ptr = self, .vtable = &.{ .probe = probe, .open = open } };
    }

    fn mint(self: *Adapter, allocator: std.mem.Allocator, kind: []const u8) ![]u8 {
        self.ids += 1;
        return std.fmt.allocPrint(allocator, "{s}-{d}", .{ kind, self.ids });
    }

    fn probe(ptr: *anyopaque, refusal: *contract.Refusal) contract.Failure!contract.Descriptor {
        _ = ptr;
        _ = refusal;
        return descriptor;
    }

    fn open(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!contract.Session {
        const self: *Adapter = @ptrCast(@alignCast(ptr));
        const opened = try Session.open(self, arena, request, refusal);
        return opened.handle();
    }
};

const Ask = struct {
    request_id: []u8,
    input_json: []u8,
    interaction_id: []u8 = &.{},

    fn deinit(self: Ask, allocator: std.mem.Allocator) void {
        allocator.free(self.request_id);
        allocator.free(self.input_json);
        allocator.free(self.interaction_id);
    }
};

const Call = struct {
    request_id: []u8,
    answered: bool = false,
    failure: ?[]u8 = null,
};

const ControlKind = enum { initialize, interrupt };

pub const Session = struct {
    owner: *Adapter,
    gpa: std.mem.Allocator,
    id: []u8,
    participant: []u8,
    reducer_arena: *std.heap.ArenaAllocator,
    engine: backend.Backend,
    requests: usize = 0,
    asks: std.ArrayList(Ask) = .empty,
    call: ?Call = null,

    fn open(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const self = try construct(owner, arena, request, refusal);
        errdefer self.destroy();
        try self.control(arena, .initialize, owner.config.initialize_timeout_ns, refusal);
        return self;
    }

    fn construct(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const gpa = owner.allocator;
        const self = try gpa.create(Session);
        errdefer gpa.destroy(self);
        const id = if (request.session_id.len > 0) try gpa.dupe(u8, request.session_id) else try owner.mint(gpa, "session");
        errdefer gpa.free(id);
        const participant = try gpa.dupe(u8, request.participant);
        errdefer gpa.free(participant);
        const reducer_arena = try gpa.create(std.heap.ArenaAllocator);
        errdefer gpa.destroy(reducer_arena);
        reducer_arena.* = std.heap.ArenaAllocator.init(gpa);
        errdefer reducer_arena.deinit();
        const engine = backend.Backend.open(reducer_arena, owner.config.backend, .{
            .session_id = id,
            .model = owner.config.backend.model,
            .responder = participant,
            .endpoint = endpoint_id,
            .revision = capability_revision,
            .counter = &owner.ids,
            .now_ms = wallClock,
        }) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            const message = try std.fmt.allocPrint(arena, "the claude child could not start: {s}", .{@errorName(err)});
            return refusal.fail(error.BackendFailed, message);
        };
        self.* = .{
            .owner = owner,
            .gpa = gpa,
            .id = id,
            .participant = participant,
            .reducer_arena = reducer_arena,
            .engine = engine,
        };
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
    };

    fn cast(ptr: *anyopaque) *Session {
        return @ptrCast(@alignCast(ptr));
    }

    fn destroy(self: *Session) void {
        const gpa = self.gpa;
        self.engine.close();
        for (self.asks.items) |ask| ask.deinit(gpa);
        self.asks.deinit(gpa);
        self.clearCall();
        self.reducer_arena.deinit();
        gpa.destroy(self.reducer_arena);
        gpa.free(self.id);
        gpa.free(self.participant);
        gpa.destroy(self);
    }

    fn clearCall(self: *Session) void {
        const call = self.call orelse return;
        self.gpa.free(call.request_id);
        if (call.failure) |failure| self.gpa.free(failure);
        self.call = null;
    }

    fn step(self: *Session, wait_ns: u64) contract.Failure!bool {
        const received = self.engine.receive(wait_ns) catch |err| return lift(err);
        switch (received) {
            .quiet => return false,
            .settled => {
                try self.releaseOrphans();
                return true;
            },
            .message => |message| {
                self.route(message) catch |err| return lift(err);
                try self.releaseOrphans();
                return true;
            },
        }
    }

    fn route(self: *Session, message: rpc.Message) !void {
        switch (message.kind) {
            .control_response => {
                const response = message.response orelse return;
                const call = if (self.call) |*pending| pending else return;
                if (!std.mem.eql(u8, call.request_id, response.request_id) or call.answered) return;
                if (!response.success) call.failure = try self.gpa.dupe(u8, response.err);
                call.answered = true;
            },
            .control_cancel => {
                if (self.askIndex(message.request_id)) |index| {
                    const withdrawn = self.asks.orderedRemove(index);
                    withdrawn.deinit(self.gpa);
                }
                try self.engine.reducer.observe(message);
            },
            .control_request => {
                if (std.mem.eql(u8, message.subtype, "hook_callback")) return self.answerHook(message);
                if (!std.mem.eql(u8, message.subtype, "can_use_tool")) return self.engine.reducer.observe(message);
                if (try self.refuseExcludedAsk(message)) return;
                try self.recordAsk(message);
                try self.engine.reducer.observe(message);
                try self.bindAsk(message.request_id);
            },
            .observation => try self.engine.reducer.observe(message),
        }
    }

    fn requestOf(message: rpc.Message) ?std.json.ObjectMap {
        const request = message.object.object.get("request") orelse return null;
        return if (request == .object) request.object else null;
    }

    fn member(map: std.json.ObjectMap, key: []const u8) []const u8 {
        const value = map.get(key) orelse return "";
        return if (value == .string) value.string else "";
    }

    fn policyRefusal(arena: std.mem.Allocator, tool_name: []const u8) ![]const u8 {
        return std.fmt.allocPrint(arena, "{s} is excluded by this run's tool_choice", .{tool_name});
    }

    fn answerHook(self: *Session, message: rpc.Message) !void {
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        const request = requestOf(message) orelse std.json.ObjectMap.empty;
        const callback_id = member(request, "callback_id");
        const input = if (request.get("input")) |carried| (if (carried == .object) carried.object else std.json.ObjectMap.empty) else std.json.ObjectMap.empty;
        if (!std.mem.eql(u8, callback_id, backend.tool_selection_hook) or !std.mem.eql(u8, member(input, "hook_event_name"), backend.pre_tool_use)) {
            try self.writeFrame(try backend.controlError(a, message.request_id, "claude adapter: unregistered hook callback"));
            return self.engine.reducer.hookCallback(callback_id);
        }
        const tool_name = member(input, "tool_name");
        if (!self.engine.reducer.excludes(tool_name)) return self.writeFrame(try backend.hookContinue(a, message.request_id));
        try self.writeFrame(try backend.hookDeny(a, message.request_id, try policyRefusal(a, tool_name)));
        try self.engine.reducer.refusedByPolicy(member(input, "tool_use_id"));
    }

    fn refuseExcludedAsk(self: *Session, message: rpc.Message) !bool {
        const request = requestOf(message) orelse return false;
        const tool_name = member(request, "tool_name");
        if (!self.engine.reducer.excludes(tool_name)) return false;
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const a = scratch.allocator();
        try self.writeFrame(try backend.permissionDeny(a, message.request_id, try policyRefusal(a, tool_name)));
        try self.engine.reducer.refusedByPolicy(member(request, "tool_use_id"));
        return true;
    }

    fn writeFrame(self: *Session, frame: []const u8) !void {
        if (self.engine.settled) return;
        self.engine.writeControl(frame) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
        };
    }

    fn recordAsk(self: *Session, message: rpc.Message) !void {
        const request = message.object.object.get("request") orelse return;
        if (request != .object) return;
        const input = rpc.lookupRaw(request.object, "input") orelse std.json.Value.null;
        try self.asks.ensureUnusedCapacity(self.gpa, 1);
        const input_json = try json_encode.valueAlloc(self.gpa, input);
        errdefer self.gpa.free(input_json);
        const request_id = try self.gpa.dupe(u8, message.request_id);
        self.asks.appendAssumeCapacity(.{ .request_id = request_id, .input_json = input_json });
    }

    fn bindAsk(self: *Session, request_id: []const u8) !void {
        const index = self.askIndex(request_id) orelse return;
        if (self.engine.reducer.gateFor(request_id)) |interaction_id| {
            self.asks.items[index].interaction_id = try self.gpa.dupe(u8, interaction_id);
            return;
        }
        try self.refuseAsk(index, "claude adapter: permission ask outside an owned run");
    }

    fn askIndex(self: *Session, request_id: []const u8) ?usize {
        for (self.asks.items, 0..) |ask, index| {
            if (std.mem.eql(u8, ask.request_id, request_id)) return index;
        }
        return null;
    }

    fn askFor(self: *Session, interaction_id: []const u8) ?usize {
        for (self.asks.items, 0..) |ask, index| {
            if (std.mem.eql(u8, ask.interaction_id, interaction_id)) return index;
        }
        return null;
    }

    fn refuseAsk(self: *Session, index: usize, message: []const u8) contract.Failure!void {
        const ask = self.asks.orderedRemove(index);
        defer ask.deinit(self.gpa);
        if (self.engine.settled) return;
        var scratch = std.heap.ArenaAllocator.init(self.gpa);
        defer scratch.deinit();
        const frame = try backend.controlError(scratch.allocator(), ask.request_id, message);
        self.engine.writeControl(frame) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
        };
    }

    fn releaseOrphans(self: *Session) contract.Failure!void {
        var index: usize = 0;
        while (index < self.asks.items.len) {
            const ask = self.asks.items[index];
            if (ask.interaction_id.len > 0 and !self.engine.reducer.gatePending(ask.interaction_id)) {
                try self.refuseAsk(index, "claude adapter: run settled while the permission ask was open");
                continue;
            }
            index += 1;
        }
    }

    fn control(self: *Session, arena: std.mem.Allocator, kind: ControlKind, timeout_ns: u64, refusal: *contract.Refusal) contract.Failure!void {
        self.requests += 1;
        const request_id = try std.fmt.allocPrint(self.gpa, "req_{d}", .{self.requests});
        self.call = .{ .request_id = request_id };
        defer self.clearCall();
        const frame = switch (kind) {
            .initialize => try backend.initializeRequest(arena, request_id),
            .interrupt => try backend.interruptRequest(arena, request_id),
        };
        self.engine.writeControl(frame) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            const message = try std.fmt.allocPrint(arena, "the claude child did not take the {s} request: {s}", .{ @tagName(kind), @errorName(err) });
            return refusal.fail(error.BackendFailed, message);
        };
        const started = monotonic();
        while (!self.call.?.answered) {
            if (self.engine.settled) {
                const message = try std.fmt.allocPrint(arena, "the claude child exited before answering {s}", .{@tagName(kind)});
                return refusal.fail(error.BackendFailed, message);
            }
            if (monotonic() -| started > timeout_ns) {
                const message = try std.fmt.allocPrint(arena, "the claude child did not answer {s} within {d} ms", .{ @tagName(kind), timeout_ns / std.time.ns_per_ms });
                return refusal.fail(error.BackendFailed, message);
            }
            _ = try self.step(self.owner.config.poll_ns);
        }
        if (self.call.?.failure) |failure| {
            const message = try std.fmt.allocPrint(arena, "claude rpc error for request {s}: {s}", .{ request_id, failure });
            return refusal.fail(error.BackendFailed, message);
        }
    }

    fn idOf(ptr: *anyopaque) []const u8 {
        return cast(ptr).id;
    }

    fn state(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        _ = refusal;
        const self = cast(ptr);
        if (self.engine.reducer.unusable) return error.SessionClosed;
        const reducer = &self.engine.reducer;
        const active = if (reducer.run) |run| run.started else false;
        const active_run_id: ?[]const u8 = if (active) try arena.dupe(u8, reducer.run.?.id) else null;
        const current_model_id: ?[]const u8 = if (reducer.current_model.len > 0) try arena.dupe(u8, reducer.current_model) else null;
        return .{
            .session_id = self.id,
            .status = if (active) .running else .idle,
            .active_run_id = active_run_id,
            .current_model_id = current_model_id,
            .updated_at_ms = wallClock(),
        };
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const self = cast(ptr);
        const text = try submissionText(arena, request);
        if (self.engine.settled or self.engine.reducer.unusable) return error.SessionClosed;
        if (self.engine.reducer.run != null) return error.RunActive;
        const policy: session.ToolPolicy = if (request.tool_choice_json) |choice_json| chosen: {
            const choice = contract.parseToolChoice(self.engine.reducer.arena.allocator(), choice_json) catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                error.InvalidPolicy => {
                    refusal.* = .{ .feature = "run.tool_selection", .reason = contract.reason_unsatisfiable, .message = "tool_choice is not the typed policy" };
                    return error.UnsupportedFeature;
                },
            };
            break :chosen .{ .allowed = choice.allowed, .disallowed = choice.disallowed };
        } else .{};
        const uuid = try self.owner.mint(arena, "turn");
        self.engine.reducer.policy = policy;
        self.engine.reducer.started = null;
        self.engine.submit(uuid, text, .{}) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            error.SessionClosed => return error.SessionClosed,
            error.RunActive => return error.RunActive,
            else => {
                const message = try std.fmt.allocPrint(arena, "the claude child did not take the turn: {s}", .{@errorName(err)});
                return refusal.fail(error.BackendFailed, message);
            },
        };
        const pending = self.engine.reducer.run orelse return refusal.fail(error.BackendFailed, "the claude session dropped the turn before it was written");
        const submission_id = try arena.dupe(u8, pending.submission_id);
        const message_id = try arena.dupe(u8, pending.message_id);
        const model = try arena.dupe(u8, pending.model);

        const started = monotonic();
        while (true) {
            if (self.engine.reducer.started) |admitted| {
                if (std.mem.eql(u8, admitted.submission_uuid, uuid)) break;
            }
            if (self.engine.reducer.run == null) return refusal.fail(error.BackendFailed, "the claude child ended the turn before acknowledging it");
            if (monotonic() -| started > self.owner.config.admission_timeout_ns) {
                self.engine.abandon("the claude child did not acknowledge the turn in time") catch |err| return lift(err);
                return refusal.fail(error.BackendFailed, "the claude child did not acknowledge the turn in time");
            }
            _ = try self.step(self.owner.config.poll_ns);
        }
        const run_id = try arena.dupe(u8, self.engine.reducer.started.?.run_id);
        const message_ids = try arena.dupe([]const u8, &.{message_id});
        return .{
            .session_id = self.id,
            .accepted = true,
            .submission_id = submission_id,
            .requested_delivery = .auto,
            .effective_delivery = .start,
            .delivery_resolution = "session_idle",
            .admission = .started,
            .run_id = run_id,
            .status = .running,
            .model_id = if (model.len > 0) model else null,
            .message_ids = message_ids,
        };
    }

    fn resolve(ptr: *anyopaque, arena: std.mem.Allocator, resolution: contract.Resolution, refusal: *contract.Refusal) contract.Failure!void {
        const self = cast(ptr);
        const request = switch (resolution) {
            .input => |input| input,
            .permission => return refusal.fail(error.InteractionNotFound, "the claude backend raises its asks as user.input gates; no permission interaction is open"),
        };
        const index = self.askFor(request.interaction_id) orelse return error.InteractionNotFound;
        if (!self.engine.reducer.gatePending(request.interaction_id)) return error.InteractionNotFound;
        const run_id = self.engine.reducer.run.?.id;
        if (!std.mem.eql(u8, request.run_id, run_id)) return error.InvalidResolution;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.InvalidResolution;
        if (!std.mem.eql(u8, request.responded_by, self.participant)) return error.InvalidResolution;
        if (!std.mem.eql(u8, request.requested_by, endpoint_id)) return error.InvalidResolution;
        if (request.answers.len != 1 or !contract.validInputAnswer(decision_question, request.answers[0])) return error.InvalidResolution;
        const decision: session.Decision = if (std.mem.eql(u8, request.answers[0].selected_option_ids[0], "allow")) .allow else .deny;

        const ask = self.asks.items[index];
        const frame = switch (decision) {
            .allow => try backend.permissionAllow(arena, ask.request_id, ask.input_json),
            .deny => try backend.permissionDeny(arena, ask.request_id, denied_message),
        };
        self.engine.writeControl(frame) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            const message = try std.fmt.allocPrint(arena, "the claude child did not take the answer: {s}", .{@errorName(err)});
            return refusal.fail(error.BackendFailed, message);
        };
        const answered = self.asks.orderedRemove(index);
        answered.deinit(self.gpa);
        self.engine.reducer.resolve(request.interaction_id, decision) catch |err| return lift(err);
    }

    fn cancel(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
        const self = cast(ptr);
        const run = self.engine.reducer.run orelse return error.RunNotFound;
        if (!run.started or !std.mem.eql(u8, run.id, run_id)) return error.RunNotFound;
        const cancelled = try arena.dupe(u8, run_id);
        try self.control(arena, .interrupt, self.owner.config.control_timeout_ns, refusal);
        return .{ .session_id = self.id, .run_id = cancelled, .accepted = true, .status = .cancelling };
    }

    fn pump(ptr: *anyopaque, wait_ns: u64) contract.Failure!bool {
        const self = cast(ptr);
        _ = self.engine.compact() catch |err| return lift(err);
        var progressed = false;
        var wait = wait_ns;
        var frames: usize = 0;
        while (frames < 256) : (frames += 1) {
            if (!try self.step(wait)) break;
            progressed = true;
            wait = std.time.ns_per_ms;
        }
        return progressed;
    }

    fn drain(ptr: *anyopaque, allocator: std.mem.Allocator, out: *std.ArrayList(contract.Event)) contract.Failure!void {
        const self = cast(ptr);
        try appendEvents(allocator, self.engine.reducer.envelopes.items, out);
        self.engine.reducer.envelopes.clearRetainingCapacity();
    }

    fn activity(ptr: *anyopaque) contract.Activity {
        const self = cast(ptr);
        const run = self.engine.reducer.run orelse return .idle;
        if (run.started and self.engine.reducer.pendingInteraction() != null) return .waiting;
        return .running;
    }

    fn close(ptr: *anyopaque) void {
        cast(ptr).destroy();
    }

    fn tools(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ToolsListRequest, refusal: *contract.Refusal) contract.Failure!oap_types.ToolsListResponse {
        const self = cast(ptr);
        if (!request.allowsDegraded(contract.feature_tools_list)) return refusal.degraded(contract.feature_tools_list);
        const reducer = &self.engine.reducer;
        if (request.session_id == null or !reducer.catalog_known) {
            const sources = try arena.dupe(oap_types.ToolSourceDescriptor, &native_sources);
            return .{ .session_id = request.session_id, .sources = sources };
        }
        const served = (reducer.listTools() catch |err| return lift(err)) orelse &.{};
        const sources = try arena.alloc(oap_types.ToolSourceDescriptor, 1 + reducer.servers.items.len);
        sources[0] = native_sources[0];
        for (reducer.servers.items, sources[1..]) |server, *source| {
            const id = try std.fmt.allocPrint(arena, "{s}{s}", .{ session.mcp_source_prefix, server });
            const display_name = try arena.dupe(u8, server);
            source.* = .{ .id = id, .kind = "process", .protocol = "mcp", .display_name = display_name };
        }
        const unexecuted = try arena.dupe(oap_types.Feature, &.{.{ .key = "action.tools.execute", .level = .unavailable, .reason = "the CLI executes its own tools" }});
        const definitions = try arena.alloc(oap_types.ToolDefinition, served.len);
        for (served, definitions) |entry, *definition| {
            const name = try arena.dupe(u8, entry.name);
            const source = try arena.dupe(u8, entry.source);
            definition.* = .{
                .name = name,
                .input_schema_json = "{\"type\":\"object\"}",
                .execution_owner = harness_owner,
                .source = source,
                .features = unexecuted,
            };
        }
        return .{ .session_id = request.session_id, .sources = sources, .tools = definitions };
    }
};

fn appendEvents(allocator: std.mem.Allocator, emitted: []const std.json.Value, out: *std.ArrayList(contract.Event)) contract.Failure!void {
    try out.ensureUnusedCapacity(allocator, emitted.len);
    const first = out.items.len;
    errdefer {
        for (out.items[first..]) |event| {
            allocator.free(event.line);
            allocator.free(event.run_id);
        }
        out.shrinkRetainingCapacity(first);
    }
    for (emitted) |value| {
        const line = try json_encode.valueAlloc(allocator, value);
        errdefer allocator.free(line);
        const run_id = try allocator.dupe(u8, value.object.get("run_id").?.string);
        const sequence: u64 = @intCast(value.object.get("sequence").?.integer);
        out.appendAssumeCapacity(.{ .line = line, .run_id = run_id, .sequence = sequence });
    }
}

fn lift(err: anyerror) contract.Failure {
    if (err == error.OutOfMemory) return error.OutOfMemory;
    return error.BackendFailed;
}

fn submissionText(arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest) contract.Failure![]const u8 {
    if (request.session_id.len == 0 or request.messages.len != 1 or request.delivery != .auto) return error.InvalidSubmission;
    const message = request.messages[0];
    if (message.role != .user) return error.InvalidSubmission;
    switch (message.content) {
        .text => |text| return text,
        .parts => |parts| {
            var joined = std.ArrayList(u8).empty;
            for (parts, 0..) |part, index| {
                const text = switch (part) {
                    .text => |value| value,
                    else => return error.InvalidSubmission,
                };
                if (index > 0) try joined.append(arena, '\n');
                try joined.appendSlice(arena, text);
            }
            return joined.items;
        },
    }
}

const testing = std.testing;

pub const FakeClaude = struct {
    tmp: std.testing.TmpDir,
    path: []u8,

    pub fn init(allocator: std.mem.Allocator, script: []const u8) !FakeClaude {
        if (builtin.os.tag == .windows) return error.SkipZigTest;
        var tmp = std.testing.tmpDir(.{});
        errdefer tmp.cleanup();
        try tmp.dir.writeFile(testing.io, .{ .sub_path = "claude", .data = script, .flags = .{ .permissions = .executable_file } });
        const cwd = try std.process.currentPathAlloc(testing.io, allocator);
        defer allocator.free(cwd);
        const path = try std.Io.Dir.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "claude" });
        return .{ .tmp = tmp, .path = path };
    }

    pub fn deinit(self: *FakeClaude, allocator: std.mem.Allocator) void {
        allocator.free(self.path);
        self.tmp.cleanup();
    }

    pub fn written(self: *FakeClaude, allocator: std.mem.Allocator) ![]u8 {
        const file = try self.tmp.dir.openFile(testing.io, "stdin.log", .{});
        defer file.close(testing.io);
        var out = std.ArrayList(u8).empty;
        errdefer out.deinit(allocator);
        var buffer: [4096]u8 = undefined;
        while (true) {
            const count = file.readStreaming(testing.io, &.{&buffer}) catch |err| switch (err) {
                error.EndOfStream => break,
                else => |failure| return failure,
            };
            if (count == 0) break;
            try out.appendSlice(allocator, buffer[0..count]);
        }
        return out.toOwnedSlice(allocator);
    }

    pub fn config(self: *const FakeClaude) Config {
        return .{
            .backend = .{
                .executable = self.path,
                .environment = &.{"PATH=/usr/bin:/bin"},
                .tools = .unrestricted,
                .exit_grace_ns = 2 * std.time.ns_per_s,
            },
            .initialize_timeout_ns = 10 * std.time.ns_per_s,
            .admission_timeout_ns = 10 * std.time.ns_per_s,
            .control_timeout_ns = 10 * std.time.ns_per_s,
            .poll_ns = 2 * std.time.ns_per_ms,
        };
    }
};

pub const fake_prelude =
    \\#!/bin/sh
    \\exec 3>>"$(dirname "$0")/stdin.log"
    \\take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
    \\field() { printf '%s' "$line" | sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p"; }
    \\take; printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{}}}\n' "$(field request_id)"
    \\
;

pub fn fakeGatedTurn(comptime tool_id: []const u8, comptime ask_id: []const u8) []const u8 {
    return fakeGatedTurnWithInput(tool_id, ask_id, "{\"command\":\"touch /tmp/x\"}");
}

fn fakeGatedTurnWithInput(comptime tool_id: []const u8, comptime ask_id: []const u8, comptime input: []const u8) []const u8 {
    return "take; uuid=$(field uuid)\n" ++
        "printf '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"native-1\",\"tools\":[\"Bash\",\"Read\",\"mcp__files__read\"],\"mcp_servers\":[{\"name\":\"files\",\"status\":\"connected\"}],\"model\":\"claude-fake\",\"uuid\":\"i1\"}\\n'\n" ++
        "printf '{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\"},\"session_id\":\"native-1\",\"parent_tool_use_id\":null,\"uuid\":\"e1\",\"user_message_uuid\":\"%s\"}\\n' \"$uuid\"\n" ++
        "printf '{\"type\":\"assistant\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-fake\",\"content\":[{\"type\":\"tool_use\",\"id\":\"" ++ tool_id ++ "\",\"name\":\"Bash\",\"input\":" ++ input ++ "}]},\"parent_tool_use_id\":null,\"session_id\":\"native-1\",\"uuid\":\"a1\"}\\n'\n" ++
        "printf '{\"type\":\"control_request\",\"request_id\":\"" ++ ask_id ++ "\",\"request\":{\"subtype\":\"can_use_tool\",\"tool_name\":\"Bash\",\"input\":" ++ input ++ ",\"tool_use_id\":\"" ++ tool_id ++ "\"}}\\n'\n" ++
        "take; behavior=$(field behavior)\n" ++
        "printf '{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"tool_use_id\":\"" ++ tool_id ++ "\",\"type\":\"tool_result\",\"content\":\"%s\",\"is_error\":false}]},\"parent_tool_use_id\":null,\"session_id\":\"native-1\",\"uuid\":\"u1\"}\\n' \"$behavior\"\n" ++
        "printf '{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}},\"session_id\":\"native-1\",\"parent_tool_use_id\":null,\"uuid\":\"e2\"}\\n'\n" ++
        "printf '{\"type\":\"result\",\"subtype\":\"success\",\"duration_ms\":12,\"is_error\":false,\"num_turns\":1,\"session_id\":\"native-1\",\"stop_reason\":\"end_turn\",\"usage\":{\"input_tokens\":7,\"output_tokens\":5},\"terminal_reason\":\"completed\",\"result\":\"answered %s\",\"user_message_uuid\":\"%s\",\"queued_turn_count\":0,\"uuid\":\"r1\"}\\n' \"$behavior\" \"$uuid\"\n";
}

pub const fake_gated_turn = fakeGatedTurn("toolu_1", "ask-1");
pub const fake_second_gated_turn = fakeGatedTurn("toolu_2", "ask-2");

pub const fake_interrupted_turn =
    \\take; uuid=$(field uuid)
    \\printf '{"type":"stream_event","event":{"type":"message_start"},"session_id":"native-1","parent_tool_use_id":null,"uuid":"e3","user_message_uuid":"%s"}\n' "$uuid"
    \\printf '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}},"session_id":"native-1","parent_tool_use_id":null,"uuid":"e4"}\n'
    \\take; printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{"still_queued":[]}}}\n' "$(field request_id)"
    \\printf '{"type":"result","subtype":"error_during_execution","duration_ms":66,"is_error":true,"num_turns":2,"session_id":"native-1","stop_reason":null,"usage":{"input_tokens":7,"output_tokens":5},"terminal_reason":"aborted_streaming","errors":["interrupted"],"user_message_uuid":"%s","queued_turn_count":0,"uuid":"r2"}\n' "$uuid"
    \\
;

pub const fake_idle = "while take; do :; done\n";

const Probe = struct {
    fake: FakeClaude,
    adapter: Adapter,
    arena: std.heap.ArenaAllocator,
    handle: ?contract.Session = null,

    fn init(self: *Probe, script: []const u8) !void {
        self.fake = try FakeClaude.init(testing.allocator, script);
        self.adapter = Adapter.init(testing.allocator, self.fake.config());
        self.arena = std.heap.ArenaAllocator.init(testing.allocator);
        self.handle = null;
    }

    fn deinit(self: *Probe) void {
        if (self.handle) |opened| opened.close();
        self.arena.deinit();
        self.fake.deinit(testing.allocator);
    }

    fn open(self: *Probe, refusal: *contract.Refusal) !contract.Session {
        const opened = try self.adapter.adapter().open(self.arena.allocator(), .{ .session_id = "s1", .participant = "user" }, refusal);
        self.handle = opened;
        return opened;
    }

    fn submit(self: *Probe, text: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const messages = try self.arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = text } }});
        const request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = messages, .delivery = .auto };
        return self.handle.?.submit(self.arena.allocator(), &request, refusal);
    }

    fn submitChoosing(self: *Probe, text: []const u8, choice: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const messages = try self.arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = text } }});
        const request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = messages, .delivery = .auto, .tool_choice_json = choice };
        return self.handle.?.submit(self.arena.allocator(), &request, refusal);
    }

    fn events(self: *Probe) ![]contract.Event {
        var drained = std.ArrayList(contract.Event).empty;
        try self.handle.?.drain(self.arena.allocator(), &drained);
        return drained.items;
    }

    fn pumpUntil(self: *Probe, comptime kind: []const u8, seen: *std.ArrayList(contract.Event)) !contract.Event {
        var rounds: usize = 0;
        while (rounds < 2000) : (rounds += 1) {
            _ = try self.handle.?.pump(5 * std.time.ns_per_ms);
            const batch = try self.events();
            try seen.appendSlice(self.arena.allocator(), batch);
            for (batch) |event| {
                if (std.mem.indexOf(u8, event.line, "\"type\":\"" ++ kind ++ "\"") != null) return event;
            }
        }
        return error.EventNeverArrived;
    }

    fn answer(self: *Probe, event: contract.Event, decision: []const u8, refusal: *contract.Refusal) !void {
        const scratch = self.arena.allocator();
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, event.line, .{});
        const payload = parsed.object.get("payload").?.object;
        const selected = try scratch.dupe([]const u8, &.{decision});
        const answers = try scratch.dupe(oap_types.InputAnswer, &.{.{ .question_id = "decision", .selected_option_ids = selected }});
        const request = oap_types.UserInputResolveRequest{
            .interaction_id = payload.get("interaction_id").?.string,
            .requested_by = payload.get("requested_by").?.string,
            .responded_by = payload.get("responded_by").?.string,
            .session_id = payload.get("session_id").?.string,
            .run_id = payload.get("run_id").?.string,
            .answers = answers,
        };
        return self.handle.?.resolve(scratch, .{ .input = &request }, refusal);
    }

    fn waitWritten(self: *Probe, needle: []const u8) ![]u8 {
        var rounds: usize = 0;
        while (rounds < 400) : (rounds += 1) {
            const written = try self.fake.written(self.arena.allocator());
            if (std.mem.indexOf(u8, written, needle) != null) return written;
            _ = try self.handle.?.pump(5 * std.time.ns_per_ms);
        }
        return error.FrameNeverWritten;
    }
};

test "an open runs the initialize exchange before handing the session out" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.open(&refusal);
    try testing.expectEqualStrings("s1", opened.id());
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expectEqualStrings("{\"request\":{\"hooks\":{\"PreToolUse\":[{\"matcher\":null,\"hookCallbackIds\":[\"oap_tool_selection\"]}]},\"subtype\":\"initialize\"},\"request_id\":\"req_1\",\"type\":\"control_request\"}\n", written);
}

test "an open whose initialize the child refuses, or leaves unanswered at exit, is refused naming why" {
    var refused: Probe = undefined;
    try refused.init(
        \\#!/bin/sh
        \\read -r line; printf '{"type":"control_response","response":{"subtype":"error","request_id":"req_1","error":"hooks are off"}}\n'; read -r rest
        \\
    );
    defer refused.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, refused.open(&refusal));
    try testing.expectEqualStrings("claude rpc error for request req_1: hooks are off", refusal.message);

    var gone: Probe = undefined;
    try gone.init("#!/bin/sh\nread -r line; exit 3\n");
    defer gone.deinit();
    refusal = .{};
    try testing.expectError(error.BackendFailed, gone.open(&refusal));
    try testing.expectEqualStrings("the claude child exited before answering initialize", refusal.message);
}

test "an admission waits for the turn's echo and names the run it started" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_gated_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);

    const admission = try probe.submit("fix it", &refusal);
    try testing.expect(admission.accepted);
    try testing.expectEqual(oap_types.Admission.started, admission.admission);
    try testing.expectEqual(oap_types.EffectiveDelivery.start, admission.effective_delivery);
    try testing.expectEqual(oap_types.RunStatus.running, admission.status.?);
    try testing.expectEqual(@as(usize, 1), admission.message_ids.len);
    const events = try probe.events();
    try testing.expect(events.len >= 1);
    try testing.expect(std.mem.indexOf(u8, events[0].line, "\"type\":\"run.started\"") != null);
    try testing.expectEqualStrings(admission.run_id.?, events[0].run_id);
    try testing.expectEqual(@as(u64, 1), events[0].sequence);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "\"content\":\"fix it\"") != null);
}

test "an allowed ask reaches the child with the tool's own input, and the run completes on the stream" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_gated_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("fix it", &refusal);

    var seen = std.ArrayList(contract.Event).empty;
    const asked = try probe.pumpUntil("user.input.requested", &seen);
    try testing.expectEqual(contract.Activity.waiting, probe.handle.?.activity());
    try probe.answer(asked, "allow", &refusal);
    const resolved = try probe.events();
    try testing.expect(std.mem.indexOf(u8, resolved[0].line, "\"type\":\"user.input.resolved\"") != null);

    const completed = try probe.pumpUntil("run.completed", &seen);
    try testing.expect(std.mem.indexOf(u8, completed.line, "answered allow") != null);
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());

    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "{\"response\":{\"request_id\":\"ask-1\",\"response\":{\"behavior\":\"allow\",\"updatedInput\":{\"command\":\"touch /tmp/x\"}},\"subtype\":\"success\"},\"type\":\"control_response\"}") != null);
}

test "a tool input nested past 256 levels reaches the call, the prompt and the child whole" {
    const input = "{\"command\":\"touch /tmp/x\",\"nested\":" ++ ("[" ** 300) ++ "1" ++ ("]" ** 300) ++ "}";
    var probe: Probe = undefined;
    try probe.init(comptime (fake_prelude ++ fakeGatedTurnWithInput("toolu_1", "ask-1", input) ++ fake_idle));
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("fix it", &refusal);

    var seen = std.ArrayList(contract.Event).empty;
    const asked = try probe.pumpUntil("user.input.requested", &seen);
    const scratch = probe.arena.allocator();
    var requested: ?std.json.Value = null;
    for (seen.items) |event| {
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, event.line, .{});
        if (std.mem.eql(u8, parsed.object.get("type").?.string, "action.call.requested")) requested = parsed;
    }
    const arguments = requested.?.object.get("payload").?.object.get("arguments_json").?;
    try testing.expectEqualStrings(input, try json_encode.valueAlloc(scratch, arguments));

    const prompt = (try std.json.parseFromSliceLeaky(std.json.Value, scratch, asked.line, .{})).object.get("payload").?.object.get("questions").?.array.items[0].object.get("prompt").?.string;
    try testing.expectEqualStrings("Bash " ++ input, prompt);

    try probe.answer(asked, "allow", &refusal);
    _ = try probe.waitWritten("\"updatedInput\":" ++ input ++ "}");
    _ = try probe.pumpUntil("run.completed", &seen);
}

test "a denied ask reaches the child as the operator's refusal" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_gated_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("fix it", &refusal);

    var seen = std.ArrayList(contract.Event).empty;
    try probe.answer(try probe.pumpUntil("user.input.requested", &seen), "deny", &refusal);
    const completed = try probe.pumpUntil("run.completed", &seen);
    try testing.expect(std.mem.indexOf(u8, completed.line, "answered deny") != null);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "{\"response\":{\"request_id\":\"ask-1\",\"response\":{\"behavior\":\"deny\",\"message\":\"Denied by the operator\"},\"subtype\":\"success\"},\"type\":\"control_response\"}") != null);
}

test "a resolution that misnames its gate, run, session, responder, requester or answer is refused before anything is written" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_gated_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("fix it", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const asked = try probe.pumpUntil("user.input.requested", &seen);
    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, probe.arena.allocator(), asked.line, .{});
    const payload = parsed.object.get("payload").?.object;
    const interaction = payload.get("interaction_id").?.string;
    const run_id = payload.get("run_id").?.string;

    var allow = [_]oap_types.InputAnswer{.{ .question_id = "decision", .selected_option_ids = &.{"allow"} }};
    var maybe = [_]oap_types.InputAnswer{.{ .question_id = "decision", .selected_option_ids = &.{"maybe"} }};
    var typed = [_]oap_types.InputAnswer{.{ .question_id = "decision", .text = "allow" }};
    var twice = [_]oap_types.InputAnswer{ .{ .question_id = "decision", .selected_option_ids = &.{"allow"} }, .{ .question_id = "decision", .selected_option_ids = &.{"allow"} } };
    const Case = struct { request: oap_types.UserInputResolveRequest, failure: contract.Failure };
    const cases = [_]Case{
        .{ .request = .{ .interaction_id = "interaction-0", .requested_by = endpoint_id, .responded_by = "user", .session_id = "s1", .run_id = run_id, .answers = &allow }, .failure = error.InteractionNotFound },
        .{ .request = .{ .interaction_id = interaction, .requested_by = endpoint_id, .responded_by = "user", .session_id = "s1", .run_id = "run-0", .answers = &allow }, .failure = error.InvalidResolution },
        .{ .request = .{ .interaction_id = interaction, .requested_by = endpoint_id, .responded_by = "user", .session_id = "s0", .run_id = run_id, .answers = &allow }, .failure = error.InvalidResolution },
        .{ .request = .{ .interaction_id = interaction, .requested_by = endpoint_id, .responded_by = "someone", .session_id = "s1", .run_id = run_id, .answers = &allow }, .failure = error.InvalidResolution },
        .{ .request = .{ .interaction_id = interaction, .requested_by = "other.endpoint", .responded_by = "user", .session_id = "s1", .run_id = run_id, .answers = &allow }, .failure = error.InvalidResolution },
        .{ .request = .{ .interaction_id = interaction, .requested_by = endpoint_id, .responded_by = "user", .session_id = "s1", .run_id = run_id, .answers = &maybe }, .failure = error.InvalidResolution },
        .{ .request = .{ .interaction_id = interaction, .requested_by = endpoint_id, .responded_by = "user", .session_id = "s1", .run_id = run_id, .answers = &typed }, .failure = error.InvalidResolution },
        .{ .request = .{ .interaction_id = interaction, .requested_by = endpoint_id, .responded_by = "user", .session_id = "s1", .run_id = run_id, .answers = &twice }, .failure = error.InvalidResolution },
    };
    for (cases) |case| {
        try testing.expectError(case.failure, probe.handle.?.resolve(probe.arena.allocator(), .{ .input = &case.request }, &refusal));
    }
    const permission = oap_types.PermissionResolveRequest{ .interaction_id = interaction, .requested_by = endpoint_id, .responded_by = "user", .session_id = "s1", .run_id = run_id, .granted = true };
    try testing.expectError(error.InteractionNotFound, probe.handle.?.resolve(probe.arena.allocator(), .{ .permission = &permission }, &refusal));

    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "control_response") == null);
    try testing.expectEqual(contract.Activity.waiting, probe.handle.?.activity());
}

test "a cancel interrupts the child and is acknowledged once the child receipts it" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_interrupted_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admission = try probe.submit("start", &refusal);

    try testing.expectError(error.RunNotFound, probe.handle.?.cancel(probe.arena.allocator(), "run-0", &refusal));
    const acknowledged = try probe.handle.?.cancel(probe.arena.allocator(), admission.run_id.?, &refusal);
    try testing.expect(acknowledged.accepted);
    try testing.expectEqual(oap_types.RunStatus.cancelling, acknowledged.status);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "{\"request\":{\"subtype\":\"interrupt\"},\"request_id\":\"req_2\",\"type\":\"control_request\"}") != null);

    var seen = std.ArrayList(contract.Event).empty;
    const cancelled = try probe.pumpUntil("run.cancelled", &seen);
    try testing.expect(std.mem.indexOf(u8, cancelled.line, "aborted_streaming") != null);
    try testing.expectError(error.RunNotFound, probe.handle.?.cancel(probe.arena.allocator(), admission.run_id.?, &refusal));
}

test "a cancel the child never receipts is refused once its bound passes, and the run stays live" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"stream_event","event":{"type":"message_start"},"session_id":"n","uuid":"e1","user_message_uuid":"%s"}\n' "$(field uuid)"
        \\
    ++ fake_idle);
    defer probe.deinit();
    probe.adapter.config.control_timeout_ns = 150 * std.time.ns_per_ms;
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admission = try probe.submit("start", &refusal);

    try testing.expectError(error.BackendFailed, probe.handle.?.cancel(probe.arena.allocator(), admission.run_id.?, &refusal));
    try testing.expectEqualStrings("the claude child did not answer interrupt within 150 ms", refusal.message);
    try testing.expectEqual(contract.Activity.running, probe.handle.?.activity());
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "\"subtype\":\"interrupt\"") != null);
}

test "a child that dies before echoing the turn refuses the admission and closes the session" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ "take; exit 1\n");
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);

    try testing.expectError(error.BackendFailed, probe.submit("go", &refusal));
    try testing.expectEqualStrings("the claude child ended the turn before acknowledging it", refusal.message);
    try testing.expectEqual(@as(usize, 0), (try probe.events()).len);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
    try testing.expectError(error.SessionClosed, probe.submit("again", &refusal));
}

test "a turn the child never acknowledges is refused once its bound passes, and the child is reaped" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ "take; exec sleep 30\n");
    defer probe.deinit();
    probe.adapter.config.admission_timeout_ns = 150 * std.time.ns_per_ms;
    probe.adapter.config.backend.exit_grace_ns = 50 * std.time.ns_per_ms;
    var refusal = contract.Refusal{};
    const opened = try probe.open(&refusal);

    try testing.expectError(error.BackendFailed, probe.submit("go", &refusal));
    try testing.expectEqualStrings("the claude child did not acknowledge the turn in time", refusal.message);
    const live: *Session = @ptrCast(@alignCast(opened.ptr));
    try testing.expect(live.engine.closed);
    try testing.expectError(error.SessionClosed, probe.submit("again", &refusal));
}

test "a second submission while a run is live is refused run_active" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_gated_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("one", &refusal);
    try testing.expectError(error.RunActive, probe.submit("two", &refusal));
}

test "a submission the child cannot take as one user turn is refused invalid_submission" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const arena = probe.arena.allocator();

    var two = [_]oap_types.Message{ .{ .role = .user, .content = .{ .text = "a" } }, .{ .role = .user, .content = .{ .text = "b" } } };
    const doubled = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = &two, .delivery = .auto };
    try testing.expectError(error.InvalidSubmission, probe.handle.?.submit(arena, &doubled, &refusal));

    var assistant = [_]oap_types.Message{.{ .role = .assistant, .content = .{ .text = "a" } }};
    const spoken = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = &assistant, .delivery = .auto };
    try testing.expectError(error.InvalidSubmission, probe.handle.?.submit(arena, &spoken, &refusal));

    var reasoning = [_]oap_types.ContentPart{.{ .reasoning = .{ .text = "hm" } }};
    var mixed = [_]oap_types.Message{.{ .role = .user, .content = .{ .parts = &reasoning } }};
    const thought = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = &mixed, .delivery = .auto };
    try testing.expectError(error.InvalidSubmission, probe.handle.?.submit(arena, &thought, &refusal));

    var one = [_]oap_types.Message{.{ .role = .user, .content = .{ .text = "a" } }};
    const queued = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = &one, .delivery = .queue };
    try testing.expectError(error.InvalidSubmission, probe.handle.?.submit(arena, &queued, &refusal));

    const written = try probe.fake.written(arena);
    try testing.expect(std.mem.indexOf(u8, written, "\"type\":\"user\"") == null);
}

test "text parts are joined with newlines into the one turn the child reads" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ "take; printf '{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"n\",\"result\":\"ok\",\"user_message_uuid\":\"%s\",\"uuid\":\"r\"}\\n' \"$(field uuid)\"\n" ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);

    var parts = [_]oap_types.ContentPart{ .{ .text = "first" }, .{ .text = "second" } };
    var messages = [_]oap_types.Message{.{ .role = .user, .content = .{ .parts = &parts } }};
    const request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = &messages, .delivery = .auto };
    _ = try probe.handle.?.submit(probe.arena.allocator(), &request, &refusal);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "\"content\":\"first\\nsecond\"") != null);
}

test "an ask the child raises outside any run is refused back to the child and ends the session" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\printf '{"type":"control_request","request_id":"stray-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"t"}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);

    const written = try probe.waitWritten("stray-1");
    try testing.expect(std.mem.indexOf(u8, written, "{\"response\":{\"error\":\"claude adapter: permission ask outside an owned run\",\"request_id\":\"stray-1\",\"subtype\":\"error\"},\"type\":\"control_response\"}") != null);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
}

test "an ask its run settles around is refused back to the child rather than left waiting" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; uuid=$(field uuid)
        \\printf '{"type":"stream_event","event":{"type":"message_start"},"session_id":"n","uuid":"e1","user_message_uuid":"%s"}\n' "$uuid"
        \\printf '{"type":"control_request","request_id":"ask-9","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"t9"}}\n'
        \\printf '{"type":"result","subtype":"success","session_id":"n","result":"moved on","user_message_uuid":"%s","uuid":"r9"}\n' "$uuid"
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("go", &refusal);

    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);
    const written = try probe.waitWritten("ask-9");
    try testing.expect(std.mem.indexOf(u8, written, "{\"response\":{\"error\":\"claude adapter: run settled while the permission ask was open\",\"request_id\":\"ask-9\",\"subtype\":\"error\"},\"type\":\"control_response\"}") != null);
    const live: *Session = @ptrCast(@alignCast(probe.handle.?.ptr));
    try testing.expectEqual(@as(usize, 0), live.asks.items.len);
}

test "the tool catalog needs the degraded opt-in and lists the latest init once one has arrived" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_gated_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const lister = probe.handle.?.vtable.tools.?;

    const bare = oap_types.ToolsListRequest{ .session_id = "s1" };
    try testing.expectError(error.CapabilityDegraded, lister(probe.handle.?.ptr, probe.arena.allocator(), &bare, &refusal));
    try testing.expectEqualStrings(contract.feature_tools_list, refusal.feature);

    const consented = oap_types.ToolsListRequest{ .session_id = "s1", .allow_degraded_features = &.{contract.feature_tools_list} };
    const before = try lister(probe.handle.?.ptr, probe.arena.allocator(), &consented, &refusal);
    try testing.expectEqual(@as(usize, 0), before.tools.len);
    try testing.expectEqual(@as(usize, 1), before.sources.len);
    try testing.expectEqualStrings(native_source, before.sources[0].id);

    _ = try probe.submit("fix it", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("user.input.requested", &seen);
    const after = try lister(probe.handle.?.ptr, probe.arena.allocator(), &consented, &refusal);
    try testing.expectEqual(@as(usize, 3), after.tools.len);
    try testing.expectEqualStrings("Bash", after.tools[0].name);
    try testing.expectEqualStrings(native_source, after.tools[0].source.?);
    try testing.expectEqualStrings("mcp__files__read", after.tools[2].name);
    try testing.expectEqualStrings("mcp:files", after.tools[2].source.?);
    try testing.expectEqual(oap_types.SupportLevel.unavailable, after.tools[2].features[0].level);
    try testing.expectEqual(@as(usize, 2), after.sources.len);
    try testing.expectEqualStrings("mcp:files", after.sources[1].id);
    try testing.expectEqualStrings("mcp", after.sources[1].protocol.?);
}

test "state reports the live run and the model the latest init named" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_gated_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);

    const idle = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqual(oap_types.SessionStatus.idle, idle.status);
    try testing.expect(idle.active_run_id == null);
    try testing.expect(idle.current_model_id == null);

    const admission = try probe.submit("fix it", &refusal);
    const running = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqual(oap_types.SessionStatus.running, running.status);
    try testing.expectEqualStrings(admission.run_id.?, running.active_run_id.?);
    try testing.expectEqualStrings("claude-fake", running.current_model_id.?);
}

test "each event is drained once, a settled session's arena is compacted, and the next run counts from one" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_gated_turn ++ fake_second_gated_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const live: *Session = @ptrCast(@alignCast(probe.handle.?.ptr));
    live.engine.compact_above = 0;

    var seen = std.ArrayList(contract.Event).empty;
    const first = try probe.submit("one", &refusal);
    try probe.answer(try probe.pumpUntil("user.input.requested", &seen), "allow", &refusal);
    _ = try probe.pumpUntil("run.completed", &seen);
    try testing.expectEqual(@as(usize, 0), (try probe.events()).len);
    try testing.expectEqual(@as(usize, 0), live.engine.reducer.envelopes.items.len);
    try testing.expectEqual(@as(usize, 0), live.engine.retained);
    _ = try probe.handle.?.pump(std.time.ns_per_ms);
    try testing.expect(live.engine.retained > 0);
    try testing.expectEqual(live.engine.retained, live.reducer_arena.queryCapacity());

    const second = try probe.submit("two", &refusal);
    try probe.answer(try probe.pumpUntil("user.input.requested", &seen), "allow", &refusal);
    const completed = try probe.pumpUntil("run.completed", &seen);
    try testing.expectEqualStrings(second.run_id.?, completed.run_id);
    try testing.expect(!std.mem.eql(u8, first.run_id.?, second.run_id.?));

    for ([_][]const u8{ first.run_id.?, second.run_id.? }) |run_id| {
        var expected: u64 = 1;
        for (seen.items) |event| {
            if (!std.mem.eql(u8, event.run_id, run_id)) continue;
            try testing.expectEqual(expected, event.sequence);
            expected += 1;
        }
        try testing.expect(expected > 4);
    }
}

fn drainProbe(allocator: std.mem.Allocator, emitted: []const std.json.Value) !void {
    var out = std.ArrayList(contract.Event).empty;
    defer {
        for (out.items) |event| {
            allocator.free(event.line);
            allocator.free(event.run_id);
        }
        out.deinit(allocator);
    }
    try appendEvents(allocator, emitted[0..1], &out);
    try appendEvents(allocator, emitted[1..], &out);
    if (out.items.len != emitted.len) return error.EventsLost;
}

test "draining hands every event over whole, and frees what it built when any allocation fails" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const emitted = try std.json.parseFromSliceLeaky([]std.json.Value, arena.allocator(),
        \\[{"type":"run.started","run_id":"run-1","sequence":1,"payload":{"status":"running"}},
        \\ {"type":"content.delta","run_id":"run-1","sequence":2,"payload":{"part":{"type":"text","text":"hi"}}}]
    , .{});
    try testing.checkAllAllocationFailures(testing.allocator, drainProbe, .{emitted});

    var out = std.ArrayList(contract.Event).empty;
    try appendEvents(arena.allocator(), emitted, &out);
    try testing.expectEqualStrings("{\"type\":\"content.delta\",\"run_id\":\"run-1\",\"sequence\":2,\"payload\":{\"part\":{\"type\":\"text\",\"text\":\"hi\"}}}", out.items[1].line);
    try testing.expectEqualStrings("run-1", out.items[1].run_id);
    try testing.expectEqual(@as(u64, 2), out.items[1].sequence);
}

fn constructProbe(allocator: std.mem.Allocator, config: Config) !void {
    var owner = Adapter.init(allocator, config);
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    var refusal = contract.Refusal{};
    const built = try Session.construct(&owner, arena.allocator(), .{ .session_id = "s1", .participant = "user" }, &refusal);
    built.destroy();
}

test "building a session and its child leaks nothing, and fails only for want of memory, when any allocation fails" {
    var fake = try FakeClaude.init(testing.allocator, fake_prelude ++ fake_idle);
    defer fake.deinit(testing.allocator);
    var config = fake.config();
    config.backend.exit_grace_ns = 500 * std.time.ns_per_ms;

    var counting = std.testing.FailingAllocator.init(testing.allocator, .{});
    try constructProbe(counting.allocator(), config);
    try testing.expect(counting.alloc_index > 0);
    for (0..counting.alloc_index) |index| {
        var failing = std.testing.FailingAllocator.init(testing.allocator, .{ .fail_index = index });
        constructProbe(failing.allocator(), config) catch |err| try testing.expectEqual(error.OutOfMemory, err);
        try testing.expect(failing.has_induced_failure);
        try testing.expectEqual(failing.allocated_bytes, failing.freed_bytes);
    }
}

fn fakeHookedTurn(comptime callback: []const u8) []const u8 {
    return "take; uuid=$(field uuid)\n" ++
        "printf '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"native-1\",\"tools\":[\"Bash\"],\"mcp_servers\":[],\"model\":\"claude-fake\",\"uuid\":\"i1\"}\\n'\n" ++
        "printf '{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\"},\"session_id\":\"native-1\",\"parent_tool_use_id\":null,\"uuid\":\"e1\",\"user_message_uuid\":\"%s\"}\\n' \"$uuid\"\n" ++
        "printf '{\"type\":\"assistant\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-fake\",\"content\":[{\"type\":\"tool_use\",\"id\":\"toolu_h\",\"name\":\"Bash\",\"input\":{}}]},\"parent_tool_use_id\":null,\"session_id\":\"native-1\",\"uuid\":\"a1\"}\\n'\n" ++
        "printf '{\"type\":\"control_request\",\"request_id\":\"hook-1\",\"request\":{\"subtype\":\"hook_callback\",\"callback_id\":\"" ++ callback ++ "\",\"tool_use_id\":\"toolu_h\",\"input\":{\"hook_event_name\":\"PreToolUse\",\"tool_name\":\"Bash\",\"tool_use_id\":\"toolu_h\"}}}\\n'\n" ++
        "take; case \"$line\" in *'\"permissionDecision\":\"deny\"'*) refused=true ;; *) refused=false ;; esac\n" ++
        "printf '{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"tool_use_id\":\"toolu_h\",\"type\":\"tool_result\",\"content\":\"hook said %s\",\"is_error\":%s}]},\"parent_tool_use_id\":null,\"session_id\":\"native-1\",\"uuid\":\"u1\"}\\n' \"$refused\" \"$refused\"\n" ++
        "printf '{\"type\":\"result\",\"subtype\":\"success\",\"duration_ms\":12,\"is_error\":false,\"num_turns\":1,\"session_id\":\"native-1\",\"stop_reason\":\"end_turn\",\"usage\":{\"input_tokens\":7,\"output_tokens\":5},\"terminal_reason\":\"completed\",\"result\":\"ok\",\"user_message_uuid\":\"%s\",\"queued_turn_count\":0,\"uuid\":\"r1\"}\\n' \"$uuid\"\n";
}

const fake_hooked_turn = fakeHookedTurn("oap_tool_selection");
const fake_stray_hook_turn = fakeHookedTurn("someone_else");

test "a PreToolUse hook for a tool the run's tool_choice excludes is denied and the call settles refused_by_policy" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_hooked_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submitChoosing("go", "{\"disallowed\":[\"Bash\"]}", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const failed = try probe.pumpUntil("action.call.failed", &seen);
    try testing.expect(std.mem.indexOf(u8, failed.line, "\"code\":\"refused_by_policy\"") != null);
    try testing.expect(std.mem.indexOf(u8, failed.line, "hook said true") != null);
    _ = try probe.pumpUntil("run.completed", &seen);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "{\"response\":{\"request_id\":\"hook-1\",\"response\":{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"Bash is excluded by this run's tool_choice\"}},\"subtype\":\"success\"},\"type\":\"control_response\"}") != null);
}

test "a PreToolUse hook for a permitted tool continues and the call completes" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_hooked_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submitChoosing("go", "{\"allowed\":[\"Bash\"]}", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("action.call.completed", &seen);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "{\"response\":{\"request_id\":\"hook-1\",\"response\":{},\"subtype\":\"success\"},\"type\":\"control_response\"}") != null);
}

test "a hook callback the adapter did not register is refused and ends the run as external activity" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_stray_hook_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("go", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const failed = try probe.pumpUntil("run.failed", &seen);
    try testing.expect(std.mem.indexOf(u8, failed.line, "hook callback \\\"someone_else\\\"") != null);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "\"error\":\"claude adapter: unregistered hook callback\"") != null);
}

test "a permission ask for an excluded tool is denied without opening a gate" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_gated_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submitChoosing("go", "{\"allowed\":[\"Read\"]}", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);
    for (seen.items) |event| try testing.expect(std.mem.indexOf(u8, event.line, "\"user.input.requested\"") == null);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "\"behavior\":\"deny\",\"message\":\"Bash is excluded by this run's tool_choice\"") != null);
}

test "a tool_choice that is not the typed policy is refused under run.tool_selection" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    try testing.expectError(error.UnsupportedFeature, probe.submitChoosing("go", "{\"allowed\":[\"Bash\"],\"disallowed\":[]}", &refusal));
    try testing.expectEqualStrings("run.tool_selection", refusal.feature);
}
