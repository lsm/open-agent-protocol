const std = @import("std");
const harness_pins = @import("harness_pins");
const builtin = @import("builtin");
const contract = @import("contract");
const oap_types = @import("oap_types");
const process = @import("process");
const compat = @import("compat");
const json_encode = @import("json_encode");
const session = @import("session.zig");
const rpc = @import("rpc.zig");

pub const endpoint_id = session.endpoint_id;
pub const capability_revision = harness_pins.pi_capability_revision;

const features = [_]contract.Feature{
    .{ .key = "action.permissions", .level = .unavailable, .reason = "extension dialogs are generic user input, not permissions" },
    .{ .key = "action.tools", .level = .degraded, .reason = "observed tool lifecycle only; no portable catalog" },
    .{ .key = "action.tools.execute", .level = .unavailable, .reason = "Pi executes tools internally" },
    .{ .key = "capabilities", .level = .emulated, .reason = "conservative descriptor synthesized for the pinned RPC vocabulary" },
    .{ .key = "protocol.initialize", .level = .emulated, .reason = "Pi has no negotiation; readiness is a get_state handshake" },
    .{ .key = "run.cancel", .level = .degraded, .reason = "abort intent is local; agent_settled remains terminal authority" },
    .{ .key = "run.reconciliation", .level = .emulated, .reason = "get_state reconciles streaming state" },
    .{ .key = "run.replay", .level = .degraded, .reason = "bounded adapter journal; gaps explicit" },
    .{ .key = "run.resume", .level = .degraded, .reason = "bounded process-memory replay" },
    .{ .key = "run.status", .level = .emulated },
    .{ .key = "run.streaming", .level = .native },
    .{ .key = "session.message.delivery.auto", .level = .emulated, .reason = "idle auto is normalized to native prompt/start" },
    .{ .key = "session.message.delivery.queue", .level = .unavailable, .reason = "v0.1 admission cannot expose Pi queued prompt semantics safely" },
    .{ .key = "session.message.delivery.steer", .level = .unavailable, .reason = "v0.1 admission cannot expose Pi steering semantics safely" },
    .{ .key = "session.message.submit", .level = .emulated, .reason = "successful prompt response proves admission only" },
    .{ .key = "session.open", .level = .emulated, .reason = "one ready Pi process is associated with one OAP session" },
    .{ .key = "session.state", .level = .emulated, .reason = "adapter projection reconciled with get_state" },
};

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = "Pi RPC Adapter", .version = harness_pins.pi_endpoint_version, .adapter = "pi-rpc-stdio" },
    .capability_revision = capability_revision,
    .features = &features,
};

pub const Config = struct {
    executable: []const u8,
    args: []const []const u8 = &.{},
    environment: []const []const u8 = &.{},
    working_directory: ?[]const u8 = null,
    frame_limit: usize = rpc.frame_limit_default,
    exit_grace_ns: u64 = process.default_exit_grace_ns,
    request_timeout_ns: u64 = 60 * std.time.ns_per_s,
    admission_timeout_ns: u64 = 10 * 60 * std.time.ns_per_s,
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

const Dialog = enum { select, confirm, text };

const Ask = struct {
    native_id: []const u8,
    interaction_id: []const u8,
    dialog: Dialog,
    labels: []const []const u8,
};

const Reply = struct {
    success: bool,
    data: ?std.json.Value = null,
    message: []const u8 = "",
};

pub const Session = struct {
    owner: *Adapter,
    gpa: std.mem.Allocator,
    id: []u8,
    participant: []u8,
    reducer_arena: *std.heap.ArenaAllocator,
    transport: *process.Transport,
    reducer: ?*session.Reducer = null,
    current_model: []const u8 = "",
    native_session: []const u8 = "",
    next_request: usize = 0,
    awaited: []const u8 = "",
    reply: ?Reply = null,
    asks: std.ArrayList(Ask) = .empty,
    unbound: std.ArrayList(Ask) = .empty,
    bound: usize = 0,
    statuses: std.StringHashMapUnmanaged([]const u8) = .empty,
    unusable: bool = false,
    ended: bool = false,
    reaped: bool = false,

    fn open(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const self = try construct(owner, arena, request, refusal);
        errdefer self.destroy();
        const state_data = try self.command(arena, "get_state", null, refusal);
        const native_session = memberOf(state_data, "sessionId") orelse std.json.Value.null;
        if (invalidState(state_data)) |reason| return refusal.fail(error.BackendFailed, reason);
        const streaming = memberOf(state_data, "isStreaming") orelse std.json.Value.null;
        if (streaming == .bool and streaming.bool) return refusal.fail(error.BackendFailed, "the Pi agent was already streaming when the session opened");
        self.current_model = modelOf(self.owned(), memberOf(state_data, "model")) catch |err| return lift(err);
        self.native_session = try self.owned().dupe(u8, native_session.string);
        return self;
    }

    fn construct(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const gpa = owner.allocator;
        const config = owner.config;
        for (config.args) |arg| {
            if (std.mem.eql(u8, arg, "--")) return refusal.fail(error.BackendFailed, "a standalone -- in the Pi args prevents enforced extension disabling");
            if (std.mem.eql(u8, arg, "--extension") or std.mem.eql(u8, arg, "-e") or std.mem.startsWith(u8, arg, "--extension=")) return refusal.fail(error.BackendFailed, "explicit Pi extensions are incompatible with canonical prompt admission");
        }
        const self = try gpa.create(Session);
        errdefer gpa.destroy(self);
        const id = if (request.session_id.len > 0) try gpa.dupe(u8, request.session_id) else try mint(owner, gpa, "session");
        errdefer gpa.free(id);
        const participant = try gpa.dupe(u8, request.participant);
        errdefer gpa.free(participant);
        const reducer_arena = try gpa.create(std.heap.ArenaAllocator);
        errdefer gpa.destroy(reducer_arena);
        reducer_arena.* = std.heap.ArenaAllocator.init(gpa);
        errdefer reducer_arena.deinit();
        const argv = try std.mem.concat(arena, []const u8, &.{ config.args, &.{ "--mode", "rpc", "--no-extensions" } });
        const transport = process.Transport.open(gpa, .{
            .executable = config.executable,
            .args = argv,
            .environment = config.environment,
            .working_directory = config.working_directory,
            .frame_limit = config.frame_limit,
            .exit_grace_ns = config.exit_grace_ns,
        }) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            const message = try std.fmt.allocPrint(arena, "the Pi agent could not start: {s}", .{@errorName(err)});
            return refusal.fail(error.BackendFailed, message);
        };
        self.* = .{
            .owner = owner,
            .gpa = gpa,
            .id = id,
            .participant = participant,
            .reducer_arena = reducer_arena,
            .transport = transport,
        };
        return self;
    }

    fn mint(owner: *Adapter, allocator: std.mem.Allocator, kind: []const u8) ![]u8 {
        owner.ids += 1;
        return std.fmt.allocPrint(allocator, "{s}-{d}", .{ kind, owner.ids });
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
    };

    fn cast(ptr: *anyopaque) *Session {
        return @ptrCast(@alignCast(ptr));
    }

    fn owned(self: *Session) std.mem.Allocator {
        return self.reducer_arena.allocator();
    }

    fn destroy(self: *Session) void {
        const gpa = self.gpa;
        self.reap();
        self.transport.deinit();
        self.reducer_arena.deinit();
        gpa.destroy(self.reducer_arena);
        gpa.free(self.id);
        gpa.free(self.participant);
        gpa.destroy(self);
    }

    fn reap(self: *Session) void {
        if (self.reaped) return;
        self.reaped = true;
        self.transport.close();
    }

    fn live(self: *Session) ?*session.Reducer {
        const reducer = self.reducer orelse return null;
        if (reducer.terminal) return null;
        return reducer;
    }

    fn fail(self: *Session, detail: []const u8) contract.Failure!void {
        if (self.ended) return;
        self.ended = true;
        self.reap();
        if (self.live()) |reducer| session.transportFailed(reducer, detail) catch |err| return lift(err);
        try self.recordTerminal();
    }

    fn send(self: *Session, value: std.json.Value) contract.Failure!bool {
        if (self.ended) return false;
        const frame = json_encode.valueAlloc(self.owned(), value) catch |err| return lift(err);
        self.transport.write(frame) catch |err| {
            try self.fail(@errorName(err));
            return false;
        };
        return true;
    }

    fn commandFrame(self: *Session, kind: []const u8, message: ?[]const u8) !struct { id: []const u8, value: std.json.Value } {
        self.next_request += 1;
        const id = try std.fmt.allocPrint(self.owned(), "req_{d}", .{self.next_request});
        var frame: std.json.ObjectMap = .empty;
        try frame.put(self.owned(), "id", .{ .string = id });
        try frame.put(self.owned(), "type", .{ .string = kind });
        if (message) |text| {
            try frame.put(self.owned(), "message", .{ .string = text });
            try frame.put(self.owned(), "streamingBehavior", .{ .string = "steer" });
        }
        return .{ .id = id, .value = .{ .object = frame } };
    }

    fn command(self: *Session, arena: std.mem.Allocator, kind: []const u8, message: ?[]const u8, refusal: *contract.Refusal) contract.Failure!std.json.Value {
        const built = self.commandFrame(kind, message) catch |err| return lift(err);
        self.awaited = built.id;
        self.reply = null;
        defer self.awaited = "";
        if (!try self.send(built.value)) {
            const text = try std.fmt.allocPrint(arena, "the Pi agent exited before answering {s}", .{kind});
            return refusal.fail(error.BackendFailed, text);
        }
        const started = monotonic();
        while (self.reply == null) {
            if (self.ended) {
                const text = try std.fmt.allocPrint(arena, "the Pi agent exited before answering {s}", .{kind});
                return refusal.fail(error.BackendFailed, text);
            }
            if (monotonic() -| started > self.owner.config.request_timeout_ns) {
                try self.fail("the Pi agent stopped answering");
                const text = try std.fmt.allocPrint(arena, "the Pi agent did not answer {s} within {d} ms", .{ kind, self.owner.config.request_timeout_ns / std.time.ns_per_ms });
                return refusal.fail(error.BackendFailed, text);
            }
            _ = try self.step(self.owner.config.poll_ns);
        }
        const answered = self.reply.?;
        self.reply = null;
        if (!answered.success) {
            const text = try std.fmt.allocPrint(arena, "pi {s} failed: {s}", .{ kind, answered.message });
            return refusal.fail(error.BackendFailed, text);
        }
        return answered.data orelse std.json.Value.null;
    }

    fn receive(self: *Session, wait_ns: u64) contract.Failure!?struct { frame: rpc.Frame, value: std.json.Value } {
        if (self.ended) return null;
        const polled = self.transport.poll(wait_ns) catch |err| {
            try self.fail(@errorName(err));
            return null;
        };
        switch (polled) {
            .quiet => return null,
            .ended => {
                self.reap();
                try self.fail(self.transport.departed().text(self.owned()));
                return null;
            },
            .frame => |bytes| {
                const held = try self.owned().dupe(u8, bytes);
                const frame = rpc.classify(self.owned(), held) catch |err| {
                    if (err == error.OutOfMemory) return error.OutOfMemory;
                    const detail = try std.fmt.allocPrint(self.owned(), "pi rpc: {s}", .{@errorName(err)});
                    try self.fail(detail);
                    return null;
                };
                const value = std.json.parseFromSliceLeaky(std.json.Value, self.owned(), held, .{}) catch |err| return lift(err);
                return .{ .frame = frame, .value = value };
            },
        }
    }

    fn step(self: *Session, wait_ns: u64) contract.Failure!bool {
        const was_ended = self.ended;
        const received = try self.receive(wait_ns) orelse {
            if (self.ended != was_ended) try self.settleAsks();
            return self.ended != was_ended;
        };
        switch (received.frame.kind) {
            .response => {
                const id = textOf(received.value, "id");
                const success = memberOf(received.value, "success") orelse std.json.Value.null;
                const reply = Reply{
                    .success = success == .bool and success.bool,
                    .data = memberOf(received.value, "data"),
                    .message = textOf(received.value, "error"),
                };
                if (self.awaited.len > 0 and std.mem.eql(u8, id, self.awaited)) self.reply = reply;
            },
            .event => if (self.reducer) |reducer| {
                session.apply(reducer, received.value) catch |err| return lift(err);
                try self.bindAsks();
            },
            .extension_ui_request => try self.extensionRequest(received.value),
        }
        try self.recordTerminal();
        try self.settleAsks();
        return true;
    }

    fn extensionRequest(self: *Session, request: std.json.Value) contract.Failure!void {
        const native_id = textOf(request, "id");
        const method = textOf(request, "method");
        const dialog: ?Dialog = if (std.mem.eql(u8, method, "select"))
            .select
        else if (std.mem.eql(u8, method, "confirm"))
            .confirm
        else if (std.mem.eql(u8, method, "input") or std.mem.eql(u8, method, "editor"))
            .text
        else
            null;
        const reducer = self.live() orelse return;
        const kind = dialog orelse return;
        var labels = std.ArrayList([]const u8).empty;
        if (memberOf(request, "options")) |options| {
            if (options == .array) {
                for (options.array.items) |option| {
                    if (option == .string) try labels.append(self.owned(), option.string);
                }
            }
        }
        try self.unbound.append(self.owned(), .{
            .native_id = try self.owned().dupe(u8, native_id),
            .interaction_id = "",
            .dialog = kind,
            .labels = labels.items,
        });
        session.applyExtension(reducer, request) catch |err| return lift(err);
        try self.bindAsks();
    }

    fn bindAsks(self: *Session) contract.Failure!void {
        const reducer = self.reducer orelse return;
        while (self.bound < reducer.interactions.items.len and self.unbound.items.len > 0) : (self.bound += 1) {
            var ask = self.unbound.orderedRemove(0);
            ask.interaction_id = reducer.interactions.items[self.bound].id;
            try self.asks.append(self.owned(), ask);
        }
    }

    const Outcome = union(enum) { value: []const u8, confirmed: bool, cancelled };

    fn extensionAnswer(self: *Session, native_id: []const u8, outcome: Outcome) contract.Failure!std.json.Value {
        var frame: std.json.ObjectMap = .empty;
        try frame.put(self.owned(), "type", .{ .string = "extension_ui_response" });
        try frame.put(self.owned(), "id", .{ .string = native_id });
        switch (outcome) {
            .value => |text| try frame.put(self.owned(), "value", .{ .string = text }),
            .confirmed => |flag| try frame.put(self.owned(), "confirmed", .{ .bool = flag }),
            .cancelled => try frame.put(self.owned(), "cancelled", .{ .bool = true }),
        }
        return .{ .object = frame };
    }

    fn settleAsks(self: *Session) contract.Failure!void {
        if (self.live() != null and !self.ended) return;
        self.asks.clearRetainingCapacity();
        self.unbound.clearRetainingCapacity();
    }

    fn recordTerminal(self: *Session) contract.Failure!void {
        const reducer = self.reducer orelse return;
        if (!reducer.terminal or self.statuses.contains(reducer.run_id)) return;
        var status: []const u8 = "failed";
        for (reducer.emitted.items) |envelope| {
            const kind = envelope.object.get("type").?.string;
            if (std.mem.eql(u8, kind, "run.completed")) status = "completed";
            if (std.mem.eql(u8, kind, "run.cancelled")) status = "cancelled";
        }
        try self.statuses.put(self.owned(), reducer.run_id, status);
    }

    fn idOf(ptr: *anyopaque) []const u8 {
        return cast(ptr).id;
    }

    fn state(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        const self = cast(ptr);
        if (self.ended or self.unusable) return error.SessionClosed;
        const state_data = try self.command(arena, "get_state", null, refusal);
        const native_session = memberOf(state_data, "sessionId") orelse std.json.Value.null;
        if (invalidState(state_data)) |reason| return refusal.fail(error.BackendFailed, reason);
        if (!std.mem.eql(u8, native_session.string, self.native_session)) {
            self.unusable = true;
            return refusal.fail(error.BackendFailed, "the Pi agent's native session changed");
        }
        if (self.ended or self.unusable) return error.SessionClosed;
        const reducer = self.live();
        const active_run_id: ?[]const u8 = if (reducer) |running| try arena.dupe(u8, running.run_id) else null;
        const current_model_id: ?[]const u8 = if (self.current_model.len > 0) try arena.dupe(u8, self.current_model) else null;
        return .{
            .session_id = self.id,
            .status = if (reducer == null) .idle else if (self.asks.items.len > 0) .waiting_for_input else .running,
            .active_run_id = active_run_id,
            .current_model_id = current_model_id,
            .updated_at_ms = wallClock(),
        };
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const self = cast(ptr);
        if (request.session_id.len == 0 or request.messages.len == 0 or request.delivery != .auto) return error.InvalidSubmission;
        var texts = std.ArrayList([]const u8).empty;
        for (request.messages) |message| {
            if (message.role != .user) return error.InvalidSubmission;
            switch (message.content) {
                .text => |text| try texts.append(arena, text),
                .parts => |parts| for (parts) |part| switch (part) {
                    .text => |text| try texts.append(arena, text),
                    else => return error.InvalidSubmission,
                },
            }
        }
        if (texts.items.len == 0) return error.InvalidSubmission;
        const joined = try std.mem.join(self.owned(), "\n\n", texts.items);
        if (std.mem.startsWith(u8, joined, "/")) return error.InvalidSubmission;
        if (self.ended or self.unusable) return error.SessionClosed;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        if (self.live() != null) return error.RunActive;

        const reducer = try self.owned().create(session.Reducer);
        reducer.* = session.Reducer.init(self.owned());
        reducer.session_id = self.id;
        reducer.responder = self.participant;
        reducer.revision = capability_revision;
        reducer.model_id = self.current_model;
        self.bound = 0;
        self.unbound.clearRetainingCapacity();
        reducer.counters.shared = &self.owner.ids;
        reducer.counters.now_ms = wallClock;
        const message_ids = try arena.alloc([]const u8, request.messages.len);
        for (request.messages, message_ids) |message, *slot| {
            const given = message.id orelse "";
            slot.* = if (given.len > 0) try arena.dupe(u8, given) else try arena.dupe(u8, try reducer.counters.nextID(self.owned(), "message"));
        }
        reducer.run_id = try reducer.counters.nextID(self.owned(), "run");
        reducer.message_id = try reducer.counters.nextID(self.owned(), "message");
        self.reducer = reducer;

        _ = self.command(arena, "prompt", joined, refusal) catch |err| {
            self.reducer = null;
            self.unusable = true;
            return err;
        };
        const started = monotonic();
        while (!reducer.started) {
            if (reducer.terminal or self.ended) return refusal.fail(error.BackendFailed, "the Pi agent ended the turn before starting it");
            if (monotonic() -| started > self.owner.config.admission_timeout_ns) {
                try self.fail("the Pi agent did not start the turn in time");
                return refusal.fail(error.BackendFailed, "the Pi agent did not start the turn in time");
            }
            _ = try self.step(self.owner.config.poll_ns);
        }
        const submission_id = try arena.dupe(u8, try reducer.counters.nextID(self.owned(), "submission"));
        const run_id = try arena.dupe(u8, reducer.run_id);
        const model_id: ?[]const u8 = if (self.current_model.len > 0) try arena.dupe(u8, self.current_model) else null;
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
            .model_id = model_id,
            .message_ids = message_ids,
        };
    }

    fn resolve(ptr: *anyopaque, arena: std.mem.Allocator, resolution: contract.Resolution, refusal: *contract.Refusal) contract.Failure!void {
        _ = refusal;
        const self = cast(ptr);
        const request = switch (resolution) {
            .input => |input| input,
            .permission => return error.InteractionNotFound,
        };
        if (self.ended or self.unusable) return error.SessionClosed;
        const at = self.askFor(request.interaction_id) orelse return error.InteractionNotFound;
        const reducer = self.live() orelse return error.InteractionNotFound;
        if (!std.mem.eql(u8, request.run_id, reducer.run_id) or !std.mem.eql(u8, request.session_id, self.id)) return error.InvalidResolution;
        if (!std.mem.eql(u8, request.responded_by, self.participant) or !std.mem.eql(u8, request.requested_by, endpoint_id)) return error.InvalidResolution;
        if (request.answers.len != 1) return error.InvalidResolution;
        const answer = request.answers[0];
        const ask = self.asks.items[at];
        if (!contract.validInputAnswer(try posed(arena, ask), answer)) return error.InvalidResolution;
        const outcome: Outcome = switch (ask.dialog) {
            .text => .{ .value = try self.owned().dupe(u8, answer.text orelse return error.InvalidResolution) },
            .confirm => confirmed: {
                if (answer.selected_option_ids.len != 1) return error.InvalidResolution;
                const chosen = answer.selected_option_ids[0];
                if (!std.mem.eql(u8, chosen, "yes") and !std.mem.eql(u8, chosen, "no")) return error.InvalidResolution;
                break :confirmed .{ .confirmed = std.mem.eql(u8, chosen, "yes") };
            },
            .select => selected: {
                if (answer.selected_option_ids.len != 1) return error.InvalidResolution;
                const chosen = answer.selected_option_ids[0];
                if (!std.mem.startsWith(u8, chosen, "option-")) return error.InvalidResolution;
                const index = std.fmt.parseInt(usize, chosen["option-".len..], 10) catch return error.InvalidResolution;
                if (index < 1 or index > ask.labels.len) return error.InvalidResolution;
                break :selected .{ .value = ask.labels[index - 1] };
            },
        };
        const reducer_answer = switch (ask.dialog) {
            .text => outcome.value,
            else => try self.owned().dupe(u8, answer.selected_option_ids[0]),
        };
        session.resolveExtension(reducer, ask.interaction_id, reducer_answer) catch |err| return switch (err) {
            error.InteractionNotFound, error.InteractionResolved => error.InteractionNotFound,
            error.InvalidResolution => error.InvalidResolution,
            else => lift(err),
        };
        _ = self.asks.orderedRemove(at);
        _ = try self.send(try self.extensionAnswer(ask.native_id, outcome));
    }

    fn posed(arena: std.mem.Allocator, ask: Ask) std.mem.Allocator.Error!contract.Question {
        return switch (ask.dialog) {
            .text => .{ .id = "value", .kind = .text },
            .confirm => .{ .id = "value", .kind = .single_choice, .options = &.{ "yes", "no" } },
            .select => select: {
                const options = try arena.alloc([]const u8, ask.labels.len);
                for (options, 1..) |*slot, position| slot.* = try std.fmt.allocPrint(arena, "option-{d}", .{position});
                break :select .{ .id = "value", .kind = .single_choice, .options = options };
            },
        };
    }

    fn askFor(self: *Session, interaction_id: []const u8) ?usize {
        for (self.asks.items, 0..) |ask, index| {
            if (std.mem.eql(u8, ask.interaction_id, interaction_id)) return index;
        }
        return null;
    }

    fn cancel(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
        const self = cast(ptr);
        if (self.ended or self.unusable) return error.SessionClosed;
        if (self.statuses.contains(run_id)) return error.RunTerminal;
        const reducer = self.live() orelse return error.RunNotFound;
        if (!std.mem.eql(u8, reducer.run_id, run_id)) return error.RunNotFound;
        if (!reducer.cancel_intent) {
            session.cancel(reducer) catch |err| return lift(err);
            _ = self.command(arena, "abort", null, refusal) catch |err| {
                if (err == error.BackendFailed and !self.ended) {
                    session.abortFailed(reducer, refusal.message) catch |failure| return lift(failure);
                    try self.recordTerminal();
                    try self.settleAsks();
                }
                return err;
            };
        }
        try self.recordTerminal();
        const status: oap_types.RunStatus = if (self.statuses.get(run_id)) |settled| std.meta.stringToEnum(oap_types.RunStatus, settled) orelse .cancelling else .cancelling;
        return .{ .session_id = self.id, .run_id = try arena.dupe(u8, run_id), .accepted = true, .status = status };
    }

    fn pump(ptr: *anyopaque, wait_ns: u64) contract.Failure!bool {
        const self = cast(ptr);
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
        const reducer = self.reducer orelse return;
        try self.recordTerminal();
        try appendEvents(allocator, reducer.emitted.items, out);
        reducer.emitted.clearRetainingCapacity();
    }

    fn activity(ptr: *anyopaque) contract.Activity {
        const self = cast(ptr);
        if (self.live() == null) return .idle;
        if (self.asks.items.len > 0) return .waiting;
        return .running;
    }

    fn close(ptr: *anyopaque) void {
        cast(ptr).destroy();
    }
};

fn invalidState(data: std.json.Value) ?[]const u8 {
    const native_session = memberOf(data, "sessionId") orelse std.json.Value.null;
    if (native_session != .string or native_session.string.len == 0) return "the Pi agent's get_state named no session";
    for ([_][]const u8{ "messageCount", "pendingMessageCount" }) |key| {
        const count = memberOf(data, key) orelse continue;
        if (count != .integer or count.integer < 0) return "the Pi agent's get_state reported a negative count";
    }
    for ([_][]const u8{ "steeringMode", "followUpMode" }) |key| {
        if (!oneOf(textOf(data, key), &.{ "all", "one-at-a-time" })) return "the Pi agent's get_state reported an invalid queue mode";
    }
    if (!oneOf(textOf(data, "thinkingLevel"), &.{ "off", "minimal", "low", "medium", "high", "xhigh", "max" })) return "the Pi agent's get_state reported an invalid thinking level";
    return null;
}

fn oneOf(text: []const u8, allowed: []const []const u8) bool {
    for (allowed) |candidate| if (std.mem.eql(u8, text, candidate)) return true;
    return false;
}

fn memberOf(value: std.json.Value, key: []const u8) ?std.json.Value {
    if (value != .object) return null;
    return value.object.get(key);
}

fn textOf(value: std.json.Value, key: []const u8) []const u8 {
    const member = memberOf(value, key) orelse return "";
    return if (member == .string) member.string else "";
}

fn modelOf(arena: std.mem.Allocator, value: ?std.json.Value) ![]const u8 {
    const model = value orelse return "";
    const id = textOf(model, "id");
    const provider = textOf(model, "provider");
    if (id.len == 0) return textOf(model, "name");
    if (provider.len == 0) return id;
    return std.fmt.allocPrint(arena, "{s}/{s}", .{ provider, id });
}

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

const testing = std.testing;

pub const FakePi = struct {
    tmp: std.testing.TmpDir,
    path: []u8,

    pub fn init(allocator: std.mem.Allocator, script: []const u8) !FakePi {
        if (builtin.os.tag == .windows) return error.SkipZigTest;
        var tmp = std.testing.tmpDir(.{});
        errdefer tmp.cleanup();
        try tmp.dir.writeFile(testing.io, .{ .sub_path = "pi", .data = script, .flags = .{ .permissions = .executable_file } });
        const cwd = try std.process.currentPathAlloc(testing.io, allocator);
        defer allocator.free(cwd);
        const path = try std.Io.Dir.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "pi" });
        return .{ .tmp = tmp, .path = path };
    }

    pub fn deinit(self: *FakePi, allocator: std.mem.Allocator) void {
        allocator.free(self.path);
        self.tmp.cleanup();
    }

    pub fn written(self: *FakePi, allocator: std.mem.Allocator) ![]u8 {
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

    pub fn config(self: *const FakePi) Config {
        return .{
            .executable = self.path,
            .environment = &.{"PATH=/usr/bin:/bin"},
            .exit_grace_ns = 2 * std.time.ns_per_s,
            .request_timeout_ns = 10 * std.time.ns_per_s,
            .admission_timeout_ns = 10 * std.time.ns_per_s,
            .poll_ns = 2 * std.time.ns_per_ms,
        };
    }
};

pub const fake_prelude =
    \\#!/bin/sh
    \\printf '%s\n' "$*" >"$(dirname "$0")/argv.log"
    \\exec 3>>"$(dirname "$0")/stdin.log"
    \\take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
    \\take; printf '{"type":"response","id":"req_1","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false,"model":{"id":"model","provider":"fixture"}}}\n'
    \\
;

const assistant_hello = "{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"hello\"}],\"api\":\"messages\",\"provider\":\"fixture\",\"model\":\"model\",\"usage\":{\"input\":1,\"output\":1,\"cacheRead\":0,\"cacheWrite\":0,\"totalTokens\":2,\"cost\":{\"input\":0,\"output\":0,\"cacheRead\":0,\"cacheWrite\":0,\"total\":0}},\"stopReason\":\"stop\",\"timestamp\":1}";

const fake_prompt_accepted =
    \\take; printf '{"type":"response","id":"req_2","command":"prompt","success":true}\n'
    \\printf '{"type":"agent_start"}\n'
    \\
;

pub const fake_text_turn = fake_prompt_accepted ++
    "printf '%s\\n' '{\"type\":\"message_end\",\"message\":" ++ assistant_hello ++ "}'\n" ++
    "printf '%s\\n' '{\"type\":\"agent_end\",\"messages\":[" ++ assistant_hello ++ "],\"willRetry\":false}'\n" ++
    "printf '{\"type\":\"agent_settled\"}\\n'\n";

pub const fake_dialog_turn = fake_prompt_accepted ++
    \\printf '{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"Proceed?","message":"Continue"}\n'
    \\take
    \\
++
    "printf '%s\\n' '{\"type\":\"agent_end\",\"messages\":[" ++ assistant_hello ++ "],\"willRetry\":false}'\n" ++
    "printf '{\"type\":\"agent_settled\"}\\n'\n";

pub const fake_idle = "while take; do :; done\n";

const Probe = struct {
    fake: FakePi,
    adapter: Adapter,
    arena: std.heap.ArenaAllocator,
    handle: ?contract.Session = null,

    fn init(self: *Probe, script: []const u8) !void {
        self.fake = try FakePi.init(testing.allocator, script);
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
        const opened = try self.adapter.adapter().vtable.open(&self.adapter, self.arena.allocator(), .{ .session_id = "s1", .participant = "user" }, refusal);
        self.handle = opened;
        return opened;
    }

    fn submit(self: *Probe, text: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const messages = try self.arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = text } }});
        const request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = messages, .delivery = .auto };
        return self.handle.?.submit(self.arena.allocator(), &request, refusal);
    }

    fn pumpUntil(self: *Probe, comptime kind: []const u8, seen: *std.ArrayList(contract.Event)) !contract.Event {
        var rounds: usize = 0;
        while (rounds < 2000) : (rounds += 1) {
            var drained = std.ArrayList(contract.Event).empty;
            try self.handle.?.drain(self.arena.allocator(), &drained);
            try seen.appendSlice(self.arena.allocator(), drained.items);
            for (drained.items) |event| {
                if (std.mem.indexOf(u8, event.line, "\"type\":\"" ++ kind ++ "\"") != null) return event;
            }
            _ = try self.handle.?.pump(5 * std.time.ns_per_ms);
        }
        return error.EventNeverArrived;
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

    fn payloadOf(self: *Probe, event: contract.Event) !std.json.ObjectMap {
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, self.arena.allocator(), event.line, .{});
        return parsed.object.get("payload").?.object;
    }
};

test "an open asks get_state and takes the model it reports, and the prompt reaches Pi in Go's command form" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("hello", &refusal);
    try testing.expectEqualStrings("fixture/model", admitted.model_id.?);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expectEqualStrings(
        \\{"id":"req_1","type":"get_state"}
        \\{"id":"req_2","type":"prompt","message":"hello","streamingBehavior":"steer"}
        \\
    , written);
}

test "run.started names the model get_state reported, as Go does" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("hello", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const started = try probe.pumpUntil("run.started", &seen);
    try testing.expectEqualStrings("fixture/model", (try probe.payloadOf(started)).get("model_id").?.string);
}

test "a turn is admitted on agent_start and settles on agent_end, citing the served revision" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("hello", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);
    try testing.expect(std.mem.indexOf(u8, seen.items[0].line, "\"type\":\"run.started\"") != null);
    for (seen.items, 1..) |event, sequence| {
        try testing.expectEqualStrings(admitted.run_id.?, event.run_id);
        try testing.expectEqual(@as(u64, sequence), event.sequence);
        try testing.expect(std.mem.indexOf(u8, event.line, "\"capability_revision\":\"" ++ capability_revision ++ "\"") != null);
    }
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
    try testing.expectError(error.RunTerminal, probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal));
}

test "a confirm dialog becomes a user input interaction, a malformed answer is refused unwritten, and a valid one reaches Pi as confirmed" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_dialog_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("go", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const asked = try probe.pumpUntil("user.input.requested", &seen);
    try testing.expectEqual(contract.Activity.waiting, probe.handle.?.activity());
    const payload = try probe.payloadOf(asked);
    const selected = try probe.arena.allocator().dupe([]const u8, &.{"yes"});
    const answers = try probe.arena.allocator().dupe(oap_types.InputAnswer, &.{.{ .question_id = "value", .selected_option_ids = selected }});
    const request = oap_types.UserInputResolveRequest{
        .interaction_id = payload.get("interaction_id").?.string,
        .requested_by = payload.get("requested_by").?.string,
        .responded_by = payload.get("responded_by").?.string,
        .session_id = payload.get("session_id").?.string,
        .run_id = payload.get("run_id").?.string,
        .answers = answers,
    };
    for ([_]oap_types.InputAnswer{
        .{ .question_id = "other", .selected_option_ids = selected },
        .{ .question_id = "value", .text = "yes", .selected_option_ids = selected },
        .{ .question_id = "value", .selected_option_ids = try probe.arena.allocator().dupe([]const u8, &.{"maybe"}) },
    }) |wrong| {
        var refused = request;
        refused.answers = try probe.arena.allocator().dupe(oap_types.InputAnswer, &.{wrong});
        try testing.expectError(error.InvalidResolution, probe.handle.?.resolve(probe.arena.allocator(), .{ .input = &refused }, &refusal));
    }
    try testing.expect(std.mem.indexOf(u8, try probe.fake.written(probe.arena.allocator()), "extension_ui_response") == null);
    try probe.handle.?.resolve(probe.arena.allocator(), .{ .input = &request }, &refusal);
    _ = try probe.waitWritten("{\"type\":\"extension_ui_response\",\"id\":\"ui-1\",\"confirmed\":true}");
    _ = try probe.pumpUntil("run.completed", &seen);
    try testing.expectError(error.InteractionNotFound, probe.handle.?.resolve(probe.arena.allocator(), .{ .input = &request }, &refusal));
}

test "a cancel sends abort once and answers cancelling" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_prompt_accepted ++
        \\take; printf '{"type":"response","id":"req_3","command":"abort","success":true}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("long", &refusal);
    const first = try probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expectEqual(oap_types.RunStatus.cancelling, first.status);
    const again = try probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expectEqual(oap_types.RunStatus.cancelling, again.status);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expectEqual(@as(usize, 1), std.mem.count(u8, written, "\"type\":\"abort\""));
    try testing.expectError(error.RunNotFound, probe.handle.?.cancel(probe.arena.allocator(), "run-unknown", &refusal));
}

test "a Pi agent already streaming at open refuses the session" {
    var probe: Probe = undefined;
    try probe.init(
        \\#!/bin/sh
        \\IFS= read -r line; printf '{"type":"response","id":"req_1","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":true}}\n'
        \\while IFS= read -r line; do :; done
        \\
    );
    defer probe.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, probe.open(&refusal));
    try testing.expectEqualStrings("the Pi agent was already streaming when the session opened", refusal.message);
}

test "a Pi agent that dies mid-run fails the run with its exit and closes the session" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_prompt_accepted ++ "exit 5\n");
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("doomed", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const failed = try probe.pumpUntil("run.failed", &seen);
    const failure = (try probe.payloadOf(failed)).get("error").?.object;
    try testing.expectEqualStrings("pi_process_exit", failure.get("code").?.string);
    try testing.expectEqualStrings("child exited with status 5", failure.get("message").?.string);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
}

test "a dialog still open when its run settles resolves cancelled and writes Pi nothing, as Go does" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_prompt_accepted ++
        \\printf '{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"Proceed?","message":"Continue"}\n'
        \\
    ++
        "printf '%s\\n' '{\"type\":\"agent_end\",\"messages\":[" ++ assistant_hello ++ "],\"willRetry\":false}'\n" ++
        "printf '{\"type\":\"agent_settled\"}\\n'\n" ++
        \\take; printf '{"type":"response","id":"req_3","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("go", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);
    const resolved = seen.items[seen.items.len - 2];
    try testing.expect(std.mem.indexOf(u8, resolved.line, "\"type\":\"user.input.resolved\"") != null);
    try testing.expectEqualStrings("cancelled", (try probe.payloadOf(resolved)).get("status").?.string);
    _ = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqualStrings(
        \\{"id":"req_1","type":"get_state"}
        \\{"id":"req_2","type":"prompt","message":"go","streamingBehavior":"steer"}
        \\{"id":"req_3","type":"get_state"}
        \\
    , try probe.fake.written(probe.arena.allocator()));
}

test "a dialog raised outside a run is ignored and writes Pi nothing, as Go does" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"extension_ui_request","id":"ui-9","method":"confirm","title":"Idle?","message":"No run"}\n'
        \\printf '{"type":"response","id":"req_2","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\take; printf '{"type":"response","id":"req_3","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    _ = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqualStrings(
        \\{"id":"req_1","type":"get_state"}
        \\{"id":"req_2","type":"get_state"}
        \\{"id":"req_3","type":"get_state"}
        \\
    , try probe.fake.written(probe.arena.allocator()));
}

test "the child runs with extensions disabled, and an explicit extension argument refuses the open" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const argv = try probe.fake.tmp.dir.readFileAlloc(testing.io, "argv.log", probe.arena.allocator(), .limited(4096));
    try testing.expectEqualStrings("--mode rpc --no-extensions\n", argv);
    try testing.expectEqual(oap_types.SupportLevel.unavailable, descriptor.level("action.permissions"));

    var refused: Probe = undefined;
    try refused.init(fake_prelude ++ fake_idle);
    defer refused.deinit();
    refused.adapter.config.args = &.{ "--extension", "x.ts" };
    try testing.expectError(error.BackendFailed, refused.open(&refusal));
    try testing.expectEqualStrings("explicit Pi extensions are incompatible with canonical prompt admission", refusal.message);
}

test "a prompt Pi refuses closes the session rather than leaving an unstarted run behind" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"response","id":"req_2","command":"prompt","success":false,"error":"no model"}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    try testing.expectError(error.BackendFailed, probe.submit("hello", &refusal));
    try testing.expectEqualStrings("pi prompt failed: no model", refusal.message);
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
    try testing.expectError(error.SessionClosed, probe.submit("again", &refusal));
}

test "a dialog raised before agent_start surfaces once the run starts and its answer reaches Pi, as Go does" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"response","id":"req_2","command":"prompt","success":true}\n'
        \\printf '{"type":"extension_ui_request","id":"ui-0","method":"confirm","title":"Early?","message":"Before start"}\n'
        \\printf '{"type":"agent_start"}\n'
        \\take
        \\
    ++ "printf '%s\\n' '{\"type\":\"agent_end\",\"messages\":[" ++ assistant_hello ++ "],\"willRetry\":false}'\n" ++
        "printf '{\"type\":\"agent_settled\"}\\n'\n" ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("hello", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const asked = try probe.pumpUntil("user.input.requested", &seen);
    const payload = try probe.payloadOf(asked);
    const answers = try probe.arena.allocator().dupe(oap_types.InputAnswer, &.{.{ .question_id = "value", .selected_option_ids = try probe.arena.allocator().dupe([]const u8, &.{"no"}) }});
    const request = oap_types.UserInputResolveRequest{
        .interaction_id = payload.get("interaction_id").?.string,
        .requested_by = payload.get("requested_by").?.string,
        .responded_by = payload.get("responded_by").?.string,
        .session_id = payload.get("session_id").?.string,
        .run_id = payload.get("run_id").?.string,
        .answers = answers,
    };
    try probe.handle.?.resolve(probe.arena.allocator(), .{ .input = &request }, &refusal);
    _ = try probe.waitWritten("{\"type\":\"extension_ui_response\",\"id\":\"ui-0\",\"confirmed\":false}");
    _ = try probe.pumpUntil("run.completed", &seen);
}

test "an abort Pi refuses fails the run with pi_abort_failed, as Go does" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_prompt_accepted ++
        \\take; printf '{"type":"response","id":"req_3","command":"abort","success":false,"error":"busy"}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("long", &refusal);
    try testing.expectError(error.BackendFailed, probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal));
    var seen = std.ArrayList(contract.Event).empty;
    const failed = try probe.pumpUntil("run.failed", &seen);
    const failure = (try probe.payloadOf(failed)).get("error").?.object;
    try testing.expectEqualStrings("pi_abort_failed", failure.get("code").?.string);
    try testing.expectEqualStrings("pi abort failed: busy", failure.get("message").?.string);
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
}

test "a state request reads get_state again, idle and mid-run, and answers the adapter projection" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"response","id":"req_2","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\take; printf '{"type":"response","id":"req_3","command":"prompt","success":true}\n'
        \\printf '{"type":"agent_start"}\n'
        \\take; printf '{"type":"response","id":"req_4","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":true}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const idle = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqual(oap_types.SessionStatus.idle, idle.status);
    try testing.expectEqualStrings("fixture/model", idle.current_model_id.?);
    const admitted = try probe.submit("long", &refusal);
    const running = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqual(oap_types.SessionStatus.running, running.status);
    try testing.expectEqualStrings(admitted.run_id.?, running.active_run_id.?);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expectEqualStrings(
        \\{"id":"req_1","type":"get_state"}
        \\{"id":"req_2","type":"get_state"}
        \\{"id":"req_3","type":"prompt","message":"long","streamingBehavior":"steer"}
        \\{"id":"req_4","type":"get_state"}
        \\
    , written);
}

test "a get_state naming another native session makes the session unusable" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"response","id":"req_2","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"other-session","isStreaming":false}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    try testing.expectError(error.BackendFailed, probe.handle.?.state(probe.arena.allocator(), &refusal));
    try testing.expectEqualStrings("the Pi agent's native session changed", refusal.message);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
    try testing.expectError(error.SessionClosed, probe.submit("again", &refusal));
}

fn stateData(comptime session_id: []const u8, comptime counts: []const u8, comptime steering: []const u8, comptime follow_up: []const u8, comptime thinking: []const u8) []const u8 {
    return "\"thinkingLevel\":\"" ++ thinking ++ "\",\"steeringMode\":\"" ++ steering ++ "\",\"followUpMode\":\"" ++ follow_up ++ "\"," ++ counts ++ ",\"sessionId\":\"" ++ session_id ++ "\"";
}

const valid_counts = "\"messageCount\":0,\"pendingMessageCount\":0";

fn expectRefusedGetState(data: []const u8, message: []const u8) !void {
    const at_open = try std.fmt.allocPrint(testing.allocator, "#!/bin/sh\nIFS= read -r line; printf '%s\\n' '{{\"type\":\"response\",\"id\":\"req_1\",\"command\":\"get_state\",\"success\":true,\"data\":{{{s}}}}}'\nwhile IFS= read -r line; do :; done\n", .{data});
    defer testing.allocator.free(at_open);
    var opening: Probe = undefined;
    try opening.init(at_open);
    defer opening.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, opening.open(&refusal));
    try testing.expectEqualStrings(message, refusal.message);

    const on_state = try std.fmt.allocPrint(testing.allocator, "{s}take; printf '%s\\n' '{{\"type\":\"response\",\"id\":\"req_2\",\"command\":\"get_state\",\"success\":true,\"data\":{{{s}}}}}'\n" ++
        "take; printf '%s\\n' '{{\"type\":\"response\",\"id\":\"req_3\",\"command\":\"get_state\",\"success\":true,\"data\":{{\"thinkingLevel\":\"max\",\"steeringMode\":\"one-at-a-time\",\"followUpMode\":\"all\",\"sessionId\":\"native-session\"}}}}'\n{s}", .{ fake_prelude, data, fake_idle });
    defer testing.allocator.free(on_state);
    var probe: Probe = undefined;
    try probe.init(on_state);
    defer probe.deinit();
    _ = try probe.open(&refusal);
    try testing.expectError(error.BackendFailed, probe.handle.?.state(probe.arena.allocator(), &refusal));
    try testing.expectEqualStrings(message, refusal.message);
    const after = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqual(oap_types.SessionStatus.idle, after.status);
}

test "a get_state naming no session is refused at open and on a state request, which leaves the session usable" {
    try expectRefusedGetState(stateData("", valid_counts, "all", "all", "off"), "the Pi agent's get_state named no session");
}

test "a get_state with a negative messageCount is refused at open and on a state request" {
    try expectRefusedGetState(stateData("native-session", "\"messageCount\":-1,\"pendingMessageCount\":0", "all", "all", "off"), "the Pi agent's get_state reported a negative count");
}

test "a get_state with a negative pendingMessageCount is refused at open and on a state request" {
    try expectRefusedGetState(stateData("native-session", "\"messageCount\":0,\"pendingMessageCount\":-1", "all", "all", "off"), "the Pi agent's get_state reported a negative count");
}

test "a get_state with an unknown steeringMode is refused at open and on a state request" {
    try expectRefusedGetState(stateData("native-session", valid_counts, "some", "all", "off"), "the Pi agent's get_state reported an invalid queue mode");
}

test "a get_state with an unknown followUpMode is refused at open and on a state request" {
    try expectRefusedGetState(stateData("native-session", valid_counts, "all", "some", "off"), "the Pi agent's get_state reported an invalid queue mode");
}

test "a get_state with an unknown thinkingLevel is refused at open and on a state request" {
    try expectRefusedGetState(stateData("native-session", valid_counts, "all", "all", "extreme"), "the Pi agent's get_state reported an invalid thinking level");
}
