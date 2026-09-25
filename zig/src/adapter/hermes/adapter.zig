const std = @import("std");
const harness_pins = @import("harness_pins");
const builtin = @import("builtin");
const contract = @import("contract");
const oap_types = @import("oap_types");
const process = @import("process");
const compat = @import("compat");
const json_encode = @import("json_encode");
const session = @import("session");
const rpc = @import("rpc");

pub const endpoint_id = session.endpoint_id;
pub const capability_revision = harness_pins.hermes_oapx_capability_revision;
const journal_reason = "oapx keeps no journal for this backend";

const features = [_]contract.Feature{
    .{ .key = "action.permissions", .level = .degraded, .reason = "approval gates surface as input interactions" },
    .{ .key = "action.tools", .level = .degraded, .reason = "tool.start/complete only; started synthesized; failures ride in result without a pinned discriminator" },
    .{ .key = "action.tools.execute", .level = .unavailable, .reason = "the gateway executes tools internally" },
    .{ .key = "capabilities", .level = .emulated, .reason = "conservative descriptor for the pinned gateway" },
    .{ .key = "protocol.initialize", .level = .native, .reason = "gateway.ready frame with replay epoch before any input" },
    .{ .key = "run.cancel", .level = .degraded, .reason = "session.interrupt intent; settlement via message.complete interrupted" },
    .{ .key = "run.reconciliation", .level = .degraded, .reason = "session.info running/turn_started_at corroboration" },
    .{ .key = "run.replay", .level = .unavailable, .reason = journal_reason },
    .{ .key = "run.resume", .level = .unavailable, .reason = journal_reason },
    .{ .key = "run.status", .level = .emulated },
    .{ .key = "run.streaming", .level = .native, .reason = "immediate frames on stdio; post-scrubber provenance disclosed" },
    .{ .key = "session.message.delivery.auto", .level = .degraded, .reason = "accepted only for known idle sessions" },
    .{ .key = "session.message.delivery.queue", .level = .unavailable, .reason = "busy statuses are rejected rather than guessed" },
    .{ .key = "session.message.delivery.steer", .level = .unavailable, .reason = "native session.steer not exposed in v1" },
    .{ .key = "session.message.submit", .level = .degraded, .reason = "status-only result; ownership by construction via message.start" },
    .{ .key = "session.open", .level = .native, .reason = "session.create mints the runtime session id" },
    .{ .key = "session.state", .level = .degraded, .reason = "reducer-owned live projection corroborated by session.info" },
    .{ .key = "user_input", .level = .native, .reason = "approval/clarify/sudo/secret gates with expire siblings" },
};

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = "Hermes Gateway Adapter", .version = harness_pins.hermes_endpoint_version, .adapter = "hermes-tui-gateway" },
    .capability_revision = capability_revision,
    .features = &features,
};

pub const Config = struct {
    executable: []const u8,
    args: []const []const u8 = &.{},
    environment: []const []const u8 = &.{},
    working_directory: ?[]const u8 = null,
    model: []const u8 = "",
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

const Held = struct {
    message: rpc.Message,
    document: std.json.Value,
};

pub const Session = struct {
    owner: *Adapter,
    gpa: std.mem.Allocator,
    id: []u8,
    participant: []u8,
    reducer_arena: *std.heap.ArenaAllocator,
    transport: *process.Transport,
    reducer: ?session.Reducer = null,
    calls: i64 = 0,
    holding: bool = false,
    held: std.ArrayList(Held) = .empty,
    ended: bool = false,
    reaped: bool = false,

    fn open(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const self = try construct(owner, arena, request, refusal);
        errdefer self.destroy();
        try self.handshake(arena, refusal);
        var params: std.json.ObjectMap = .empty;
        if (owner.config.model.len > 0) try params.put(self.owned(), "model", .{ .string = owner.config.model });
        const created = try self.call(arena, "session.create", .{ .object = params }, refusal);
        const native_id = member(created, "session_id");
        if (native_id.len == 0 or member(created, "stored_session_id").len == 0) return refusal.fail(error.BackendFailed, "the hermes gateway answered session.create without a session_id and stored_session_id");
        self.reducer = session.Reducer.init(self.reducer_arena, .{
            .session_id = self.id,
            .native_id = native_id,
            .model = owner.config.model,
            .responder = self.participant,
            .revision = capability_revision,
            .counter = &owner.ids,
            .now_ms = wallClock,
        });
        self.reducer.?.open();
        return self;
    }

    fn construct(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const gpa = owner.allocator;
        const config = owner.config;
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
        const transport = process.Transport.open(gpa, .{
            .executable = config.executable,
            .args = config.args,
            .environment = config.environment,
            .working_directory = config.working_directory,
            .frame_limit = config.frame_limit,
            .exit_grace_ns = config.exit_grace_ns,
        }) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            const message = try std.fmt.allocPrint(arena, "the hermes gateway could not start: {s}", .{@errorName(err)});
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

    fn closed(self: *Session) bool {
        if (self.ended) return true;
        const reducer = self.reducer orelse return false;
        return reducer.unusable;
    }

    fn fail(self: *Session, detail: []const u8) contract.Failure!void {
        if (self.ended) return;
        self.ended = true;
        self.reap();
        try self.release();
        if (self.reducer) |*reducer| reducer.transportFailed(detail) catch |err| return lift(err);
    }

    fn release(self: *Session) contract.Failure!void {
        self.holding = false;
        var index: usize = 0;
        while (index < self.held.items.len) : (index += 1) {
            const entry = self.held.items[index];
            try self.apply(entry.message, entry.document);
        }
        self.held.clearRetainingCapacity();
    }

    fn apply(self: *Session, message: rpc.Message, document: std.json.Value) contract.Failure!void {
        const reducer = if (self.reducer) |*present| present else return;
        reducer.observe(message, document) catch |err| return lift(err);
    }

    fn write(self: *Session, arena: std.mem.Allocator, frame: []const u8, what: []const u8, refusal: *contract.Refusal) contract.Failure!void {
        self.transport.write(frame) catch |err| {
            self.reap();
            const departed = self.transport.departed().departure != .running;
            try self.fail(@errorName(err));
            const message = if (departed)
                try std.fmt.allocPrint(arena, "the hermes gateway exited before answering {s}", .{what})
            else
                try std.fmt.allocPrint(arena, "the hermes gateway did not take {s}: {s}", .{ what, @errorName(err) });
            return refusal.fail(error.BackendFailed, message);
        };
    }

    const Received = struct {
        message: rpc.Message,
        document: std.json.Value,
    };

    fn receive(self: *Session, wait_ns: u64) contract.Failure!?Received {
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
                var diagnostic = rpc.Diagnostic{};
                const message = rpc.parseMessage(self.owned(), held, &diagnostic) catch |err| {
                    if (err == error.OutOfMemory) return error.OutOfMemory;
                    try self.fail(diagnostic.message);
                    return null;
                };
                const document = std.json.parseFromSliceLeaky(std.json.Value, self.owned(), held, .{}) catch |err| {
                    if (err == error.OutOfMemory) return error.OutOfMemory;
                    try self.fail(rpc.invalid_message_prefix);
                    return null;
                };
                return .{ .message = message, .document = document };
            },
        }
    }

    fn step(self: *Session, wait_ns: u64) contract.Failure!bool {
        const was_ended = self.ended;
        const received = try self.receive(wait_ns) orelse return self.ended != was_ended;
        try self.observe(received);
        return true;
    }

    fn observe(self: *Session, received: Received) contract.Failure!void {
        if (self.holding) {
            try self.held.append(self.owned(), .{ .message = received.message, .document = received.document });
            return;
        }
        try self.apply(received.message, received.document);
    }

    fn call(self: *Session, arena: std.mem.Allocator, method: []const u8, params: std.json.Value, refusal: *contract.Refusal) contract.Failure!std.json.Value {
        self.calls += 1;
        const call_id = self.calls;
        var request: std.json.ObjectMap = .empty;
        try request.put(self.owned(), "id", .{ .integer = call_id });
        try request.put(self.owned(), "jsonrpc", .{ .string = "2.0" });
        try request.put(self.owned(), "method", .{ .string = method });
        try request.put(self.owned(), "params", params);
        const frame = json_encode.valueAlloc(self.owned(), .{ .object = request }) catch |err| return lift(err);
        try self.write(arena, frame, method, refusal);
        const started = monotonic();
        while (true) {
            if (self.ended) {
                const message = try std.fmt.allocPrint(arena, "the hermes gateway exited before answering {s}", .{method});
                return refusal.fail(error.BackendFailed, message);
            }
            if (monotonic() -| started > self.owner.config.request_timeout_ns) {
                try self.fail("the hermes gateway stopped answering");
                const message = try std.fmt.allocPrint(arena, "the hermes gateway did not answer {s} within {d} ms", .{ method, self.owner.config.request_timeout_ns / std.time.ns_per_ms });
                return refusal.fail(error.BackendFailed, message);
            }
            const received = try self.receive(self.owner.config.poll_ns) orelse continue;
            const answers = received.message.kind == .response or received.message.kind == .failure;
            const carried = received.document.object.get("id");
            if (!answers or carried == null or carried.? != .integer or carried.?.integer != call_id) {
                try self.observe(received);
                continue;
            }
            if (received.message.kind == .failure) {
                const failure = received.document.object.get("error").?;
                const code = failure.object.get("code");
                const text = member(failure, "message");
                const message = if (code != null and code.? == .integer)
                    try std.fmt.allocPrint(arena, "hermes rpc error for {s} ({d}): {s}", .{ method, code.?.integer, text })
                else
                    try std.fmt.allocPrint(arena, "hermes rpc error for {s}: {s}", .{ method, text });
                return refusal.fail(error.BackendFailed, message);
            }
            return received.document.object.get("result") orelse .null;
        }
    }

    fn handshake(self: *Session, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!void {
        const started = monotonic();
        while (true) {
            if (self.ended) return refusal.fail(error.BackendFailed, "the hermes gateway exited before gateway.ready");
            if (monotonic() -| started > self.owner.config.request_timeout_ns) {
                try self.fail("the hermes gateway never sent gateway.ready");
                const message = try std.fmt.allocPrint(arena, "the hermes gateway did not send gateway.ready within {d} ms", .{self.owner.config.request_timeout_ns / std.time.ns_per_ms});
                return refusal.fail(error.BackendFailed, message);
            }
            const received = try self.receive(self.owner.config.poll_ns) orelse continue;
            if (ready(received)) return;
            try self.fail("the hermes gateway spoke before gateway.ready");
            return refusal.fail(error.BackendFailed, "the hermes gateway's first frame was not a valid gateway.ready");
        }
    }

    fn live(self: *Session) ?*session.Reducer {
        const reducer = if (self.reducer) |*present| present else return null;
        const run = reducer.run orelse return null;
        if (run.terminal or !run.started) return null;
        return reducer;
    }

    fn waiting(self: *Session) bool {
        const reducer = self.live() orelse return false;
        for (reducer.interactions.items) |binding| {
            if (!binding.resolved and std.mem.eql(u8, binding.run_id, reducer.run.?.id)) return true;
        }
        return false;
    }

    fn idOf(ptr: *anyopaque) []const u8 {
        return cast(ptr).id;
    }

    fn state(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        _ = refusal;
        const self = cast(ptr);
        if (self.closed()) return error.SessionClosed;
        const reducer = self.live();
        const active_run_id: ?[]const u8 = if (reducer) |running| try arena.dupe(u8, running.run.?.id) else null;
        const model = self.owner.config.model;
        return .{
            .session_id = self.id,
            .status = if (reducer == null) .idle else if (self.waiting()) .waiting_for_input else .running,
            .active_run_id = active_run_id,
            .current_model_id = if (model.len > 0) try arena.dupe(u8, model) else null,
            .updated_at_ms = wallClock(),
        };
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const self = cast(ptr);
        if (!std.mem.eql(u8, request.session_id, self.id) or request.messages.len != 1 or request.delivery != .auto) return error.InvalidSubmission;
        const message = request.messages[0];
        if (message.role != .user) return error.InvalidSubmission;
        const text = switch (message.content) {
            .text => |content| try self.owned().dupe(u8, content),
            .parts => return error.InvalidSubmission,
        };
        if (self.closed()) return error.SessionClosed;
        const reducer = &self.reducer.?;
        reducer.submitAs(.{}) catch |err| return switch (err) {
            error.RunActive => error.RunActive,
            error.SessionUnusable => error.SessionClosed,
            else => lift(err),
        };
        var params: std.json.ObjectMap = .empty;
        try params.put(self.owned(), "session_id", .{ .string = reducer.options.native_id });
        try params.put(self.owned(), "text", .{ .string = text });
        const result = self.call(arena, "prompt.submit", .{ .object = params }, refusal) catch |err| {
            reducer.refuseSubmission();
            return err;
        };
        const status = member(result, "status");
        if (!std.mem.eql(u8, status, "streaming")) {
            reducer.refuseSubmission();
            const detail = try std.fmt.allocPrint(arena, "the hermes gateway answered prompt.submit with status \"{s}\"", .{status});
            return refusal.fail(error.BackendFailed, detail);
        }
        reducer.admit() catch |err| return lift(err);
        const started = monotonic();
        while (true) {
            if (reducer.run) |run| {
                if (run.started) {
                    const run_id = try arena.dupe(u8, run.id);
                    const message_id = try arena.dupe(u8, run.message_id);
                    const message_ids = try arena.dupe([]const u8, &.{message_id});
                    const model = self.owner.config.model;
                    return .{
                        .session_id = self.id,
                        .accepted = true,
                        .submission_id = message_id,
                        .requested_delivery = .auto,
                        .effective_delivery = .start,
                        .delivery_resolution = "session_idle",
                        .admission = .started,
                        .run_id = run_id,
                        .status = .running,
                        .model_id = if (model.len > 0) try arena.dupe(u8, model) else null,
                        .message_ids = message_ids,
                    };
                }
                if (run.terminal) return refusal.fail(error.BackendFailed, "the hermes gateway ended before the turn opened");
            } else return refusal.fail(error.BackendFailed, "the hermes gateway abandoned the turn before it opened");
            if (self.ended) return refusal.fail(error.BackendFailed, "the hermes gateway exited before the turn opened");
            if (monotonic() -| started > self.owner.config.admission_timeout_ns) {
                try self.fail("the hermes gateway never opened the turn");
                const detail = try std.fmt.allocPrint(arena, "the hermes gateway did not open the turn within {d} ms", .{self.owner.config.admission_timeout_ns / std.time.ns_per_ms});
                return refusal.fail(error.BackendFailed, detail);
            }
            _ = try self.step(self.owner.config.poll_ns);
        }
    }

    fn resolve(ptr: *anyopaque, arena: std.mem.Allocator, resolution: contract.Resolution, refusal: *contract.Refusal) contract.Failure!void {
        const self = cast(ptr);
        const request = switch (resolution) {
            .input => |input| input,
            .permission => return error.InteractionNotFound,
        };
        if (self.closed()) return error.SessionClosed;
        const reducer = &self.reducer.?;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.InvalidResolution;
        if (request.responded_by.len > 0 and !std.mem.eql(u8, request.responded_by, self.participant)) return error.InvalidResolution;
        if (request.requested_by.len > 0 and !std.mem.eql(u8, request.requested_by, endpoint_id)) return error.InvalidResolution;
        const answers = try self.owned().alloc(session.Answer, request.answers.len);
        for (request.answers, answers) |answer, *slot| {
            const selected = try self.owned().alloc([]const u8, answer.selected_option_ids.len);
            for (answer.selected_option_ids, selected) |option, *copy| copy.* = try self.owned().dupe(u8, option);
            const question_id = try self.owned().dupe(u8, answer.question_id);
            const text = try self.owned().dupe(u8, answer.text orelse "");
            slot.* = .{ .question_id = question_id, .selected_option_ids = selected, .text = text };
        }
        const binding = reducer.check(request.interaction_id, answers) catch |err| return switch (err) {
            error.InteractionNotFound => error.InteractionNotFound,
            error.SessionUnusable => error.SessionClosed,
            else => error.InvalidResolution,
        };
        if (request.run_id.len > 0 and !std.mem.eql(u8, request.run_id, binding.run_id)) return error.InvalidResolution;
        self.holding = true;
        defer self.holding = false;
        errdefer self.release() catch {};
        try self.respond(arena, binding, answers, refusal);
        reducer.resolve(request.interaction_id, answers) catch |err| switch (err) {
            error.InteractionNotFound, error.SessionUnusable => {},
            else => return lift(err),
        };
        try self.release();
    }

    fn respond(self: *Session, arena: std.mem.Allocator, binding: session.Interaction, answers: []const session.Answer, refusal: *contract.Refusal) contract.Failure!void {
        const own = self.owned();
        if (std.mem.eql(u8, binding.kind, "approval")) {
            var params: std.json.ObjectMap = .empty;
            try params.put(own, "session_id", .{ .string = self.reducer.?.options.native_id });
            try params.put(own, "choice", .{ .string = answers[0].selected_option_ids[0] });
            const result = try self.call(arena, "approval.respond", .{ .object = params }, refusal);
            const resolved = if (result == .object) result.object.get("resolved") else null;
            if (resolved == null or resolved.? != .bool or !resolved.?.bool) return refusal.fail(error.BackendFailed, "approval.respond did not resolve the gate");
            return;
        }
        const method = try std.fmt.allocPrint(own, "{s}.respond", .{binding.kind});
        for (binding.questions) |question| {
            const answer = for (answers) |candidate| {
                if (std.mem.eql(u8, candidate.question_id, question.id)) break candidate;
            } else return error.InvalidResolution;
            var params: std.json.ObjectMap = .empty;
            try params.put(own, "request_id", .{ .string = binding.request_id });
            if (std.mem.eql(u8, binding.kind, "sudo")) {
                try params.put(own, "password", .{ .string = answer.text });
            } else if (std.mem.eql(u8, binding.kind, "secret")) {
                try params.put(own, "value", .{ .string = answer.text });
            } else {
                if (binding.questions.len > 1) try params.put(own, "question_id", .{ .string = question.id });
                const value: []const u8 = if (std.mem.eql(u8, question.kind, "text"))
                    answer.text
                else if (std.mem.eql(u8, question.kind, "multi_choice"))
                    try encodeStrings(own, answer.selected_option_ids)
                else
                    answer.selected_option_ids[0];
                if (value.len > 0) try params.put(own, "answer", .{ .string = value });
            }
            const result = try self.call(arena, method, .{ .object = params }, refusal);
            const status = member(result, "status");
            if (!std.mem.eql(u8, status, "ok")) {
                const detail = try std.fmt.allocPrint(arena, "{s} returned status \"{s}\"", .{ method, status });
                return refusal.fail(error.BackendFailed, detail);
            }
        }
    }

    fn cancel(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
        const self = cast(ptr);
        if (self.closed()) return error.SessionClosed;
        const reducer = self.live() orelse return error.RunNotFound;
        if (!std.mem.eql(u8, reducer.run.?.id, run_id)) return error.RunNotFound;
        var params: std.json.ObjectMap = .empty;
        try params.put(self.owned(), "session_id", .{ .string = reducer.options.native_id });
        _ = try self.call(arena, "session.interrupt", .{ .object = params }, refusal);
        return .{
            .session_id = self.id,
            .run_id = try arena.dupe(u8, run_id),
            .accepted = true,
            .status = .cancelling,
        };
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
        const reducer = if (self.reducer) |*present| present else return;
        try appendEvents(allocator, reducer.envelopes.items, out);
        reducer.envelopes.clearRetainingCapacity();
    }

    fn activity(ptr: *anyopaque) contract.Activity {
        const self = cast(ptr);
        if (self.live() == null) return .idle;
        if (self.waiting()) return .waiting;
        return .running;
    }

    fn close(ptr: *anyopaque) void {
        cast(ptr).destroy();
    }
};

fn ready(received: Session.Received) bool {
    if (received.message.kind != .notification or !std.mem.eql(u8, received.message.method, "event")) return false;
    const params = received.document.object.get("params") orelse return false;
    if (!std.mem.eql(u8, member(params, "type"), "gateway.ready")) return false;
    if (params.object.get("session_id") != null) return false;
    const payload = params.object.get("payload") orelse return false;
    if (payload != .object) return false;
    const skin = payload.object.get("skin") orelse return false;
    if (skin == .null) return false;
    const changes = payload.object.get("change_events") orelse return false;
    if (changes != .bool or !changes.bool) return false;
    const epoch = member(payload, "replay_epoch");
    if (epoch.len != 32) return false;
    for (epoch) |char| if (!std.ascii.isHex(char)) return false;
    return true;
}

fn member(value: std.json.Value, key: []const u8) []const u8 {
    if (value != .object) return "";
    const found = value.object.get(key) orelse return "";
    return if (found == .string) found.string else "";
}

fn encodeStrings(allocator: std.mem.Allocator, values: []const []const u8) ![]const u8 {
    var array = std.json.Array.init(allocator);
    for (values) |value| try array.append(.{ .string = value });
    return json_encode.valueAlloc(allocator, .{ .array = array }) catch |err| lift(err);
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

pub const FakeGateway = struct {
    tmp: std.testing.TmpDir,
    path: []u8,

    pub fn init(allocator: std.mem.Allocator, script: []const u8) !FakeGateway {
        if (builtin.os.tag == .windows) return error.SkipZigTest;
        var tmp = std.testing.tmpDir(.{});
        errdefer tmp.cleanup();
        try tmp.dir.writeFile(testing.io, .{ .sub_path = "gateway", .data = script, .flags = .{ .permissions = .executable_file } });
        const cwd = try std.process.currentPathAlloc(testing.io, allocator);
        defer allocator.free(cwd);
        const path = try std.Io.Dir.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "gateway" });
        return .{ .tmp = tmp, .path = path };
    }

    pub fn deinit(self: *FakeGateway, allocator: std.mem.Allocator) void {
        allocator.free(self.path);
        self.tmp.cleanup();
    }

    pub fn written(self: *FakeGateway, allocator: std.mem.Allocator) ![]u8 {
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

    pub fn config(self: *const FakeGateway) Config {
        return .{
            .executable = self.path,
            .environment = &.{"PATH=/usr/bin:/bin"},
            .model = "hermes-test",
            .exit_grace_ns = 2 * std.time.ns_per_s,
            .request_timeout_ns = 10 * std.time.ns_per_s,
            .admission_timeout_ns = 10 * std.time.ns_per_s,
            .poll_ns = 2 * std.time.ns_per_ms,
        };
    }
};

pub const fake_prelude =
    \\#!/bin/sh
    \\exec 3>>"$(dirname "$0")/stdin.log"
    \\take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
    \\printf '{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"e3b0c44298fc1c149afbf4c8996fb924"}}}\n'
    \\take; printf '{"id":1,"jsonrpc":"2.0","result":{"session_id":"sess0001","stored_session_id":"key0001","message_count":0,"info":{}}}\n'
    \\
;

pub const fake_turn_admitted =
    \\take; printf '{"id":2,"jsonrpc":"2.0","result":{"status":"streaming"}}\n'
    \\printf '{"jsonrpc":"2.0","method":"event","params":{"type":"message.start","session_id":"sess0001","seq":1}}\n'
    \\
;

pub const fake_text_turn = fake_turn_admitted ++
    \\printf '{"jsonrpc":"2.0","method":"event","params":{"type":"message.delta","session_id":"sess0001","seq":2,"payload":{"text":"fixture-ok"}}}\n'
    \\printf '{"jsonrpc":"2.0","method":"event","params":{"type":"message.complete","session_id":"sess0001","seq":3,"payload":{"text":"fixture-ok","status":"complete","usage":{"input":1,"output":2,"total":3}}}}\n'
    \\
;

pub const fake_approval_turn = fake_turn_admitted ++
    \\printf '{"jsonrpc":"2.0","method":"event","params":{"type":"approval.request","session_id":"sess0001","seq":2,"payload":{"command":"rm -rf /tmp/x","choices":["once","session","always","deny"]}}}\n'
    \\take
    \\printf '{"jsonrpc":"2.0","method":"event","params":{"type":"message.complete","session_id":"sess0001","seq":3,"payload":{"text":"done","status":"complete","usage":{}}}}\n'
    \\printf '{"id":3,"jsonrpc":"2.0","result":{"resolved":true}}\n'
    \\
;

pub const fake_clarify_turn = fake_turn_admitted ++
    \\printf '{"jsonrpc":"2.0","method":"event","params":{"type":"clarify.request","session_id":"sess0001","seq":2,"payload":{"request_id":"aaaa1111","question":"which?","choices":["a","b"]}}}\n'
    \\take; printf '{"id":3,"jsonrpc":"2.0","result":{"status":"ok"}}\n'
    \\printf '{"jsonrpc":"2.0","method":"event","params":{"type":"message.complete","session_id":"sess0001","seq":3,"payload":{"text":"done","status":"complete","usage":{}}}}\n'
    \\
;

pub const fake_interrupted_turn = fake_turn_admitted ++
    \\take; printf '{"id":3,"jsonrpc":"2.0","result":{"status":"interrupted","interrupted":true}}\n'
    \\printf '{"jsonrpc":"2.0","method":"event","params":{"type":"message.complete","session_id":"sess0001","seq":2,"payload":{"text":"","status":"interrupted","usage":{}}}}\n'
    \\
;

pub const fake_idle = "while take; do :; done\n";

const Probe = struct {
    fake: FakeGateway,
    adapter: Adapter,
    arena: std.heap.ArenaAllocator,
    handle: ?contract.Session = null,

    fn init(self: *Probe, script: []const u8) !void {
        self.fake = try FakeGateway.init(testing.allocator, script);
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

    fn payloadOf(self: *Probe, event: contract.Event) !std.json.ObjectMap {
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, self.arena.allocator(), event.line, .{});
        return parsed.object.get("payload").?.object;
    }

    fn answer(self: *Probe, asked: contract.Event, question: []const u8, choice: []const u8, refusal: *contract.Refusal) !void {
        const payload = try self.payloadOf(asked);
        const scratch = self.arena.allocator();
        const answers = try scratch.alloc(oap_types.InputAnswer, 1);
        answers[0] = .{ .question_id = question, .selected_option_ids = try scratch.dupe([]const u8, &.{choice}) };
        const request = oap_types.UserInputResolveRequest{
            .interaction_id = payload.get("interaction_id").?.string,
            .requested_by = payload.get("requested_by").?.string,
            .responded_by = payload.get("responded_by").?.string,
            .session_id = payload.get("session_id").?.string,
            .run_id = payload.get("run_id").?.string,
            .answers = answers,
        };
        try self.handle.?.resolve(scratch, .{ .input = &request }, refusal);
    }
};

fn kinds(allocator: std.mem.Allocator, events: []const contract.Event) ![]const []const u8 {
    const names = try allocator.alloc([]const u8, events.len);
    for (events, names) |event, *name| {
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, allocator, event.line, .{});
        name.* = parsed.object.get("type").?.string;
    }
    return names;
}

test "the descriptor carries resume and replay unavailable under its own revision" {
    try testing.expect(!std.mem.eql(u8, capability_revision, session.capability_revision));
    try testing.expectEqual(oap_types.SupportLevel.unavailable, descriptor.level("run.replay"));
    try testing.expectEqual(oap_types.SupportLevel.unavailable, descriptor.level("run.resume"));
    for (features[1..], features[0 .. features.len - 1]) |later, earlier| try testing.expect(std.mem.lessThan(u8, earlier.key, later.key));
}

test "an open writes session.create, and a turn is admitted on message.start and settles on message.complete" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);

    const admitted = try probe.submit("hello", &refusal);
    try testing.expect(admitted.accepted);
    try testing.expectEqualStrings("hermes-test", admitted.model_id.?);
    try testing.expectEqualStrings(admitted.submission_id, admitted.message_ids[0]);

    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);
    const names = try kinds(probe.arena.allocator(), seen.items);
    try testing.expectEqualStrings("run.started", names[0]);
    try testing.expectEqualStrings("content.delta", names[1]);
    for (seen.items, 1..) |event, sequence| {
        try testing.expectEqualStrings(admitted.run_id.?, event.run_id);
        try testing.expectEqual(@as(u64, sequence), event.sequence);
        try testing.expect(std.mem.indexOf(u8, event.line, "\"capability_revision\":\"" ++ capability_revision ++ "\"") != null);
    }
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
    try testing.expectEqualStrings(
        \\{"id":1,"jsonrpc":"2.0","method":"session.create","params":{"model":"hermes-test"}}
        \\{"id":2,"jsonrpc":"2.0","method":"prompt.submit","params":{"session_id":"sess0001","text":"hello"}}
        \\
    , try probe.fake.written(probe.arena.allocator()));
}

test "an approval answer reaches the gateway before a settlement that raced it is applied" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_approval_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("run it", &refusal);

    var seen = std.ArrayList(contract.Event).empty;
    const asked = try probe.pumpUntil("user.input.requested", &seen);
    try testing.expectEqual(contract.Activity.waiting, probe.handle.?.activity());
    try probe.answer(asked, "choice", "once", &refusal);
    const settled = try probe.pumpUntil("run.completed", &seen);
    _ = settled;
    const names = try kinds(probe.arena.allocator(), seen.items);
    var resolved: ?[]const u8 = null;
    for (seen.items, names) |event, name| {
        if (std.mem.eql(u8, name, "user.input.resolved")) resolved = event.line;
    }
    try testing.expect(std.mem.indexOf(u8, resolved.?, "\"status\":\"submitted\"") != null);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.endsWith(u8, written, "{\"id\":3,\"jsonrpc\":\"2.0\",\"method\":\"approval.respond\",\"params\":{\"session_id\":\"sess0001\",\"choice\":\"once\"}}\n"));
}

test "a clarify answer is written with its request id and the chosen option" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_clarify_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("ask me", &refusal);

    var seen = std.ArrayList(contract.Event).empty;
    const asked = try probe.pumpUntil("user.input.requested", &seen);
    try testing.expectError(error.InvalidResolution, probe.answer(asked, "answer", "c", &refusal));
    try probe.answer(asked, "answer", "a", &refusal);
    _ = try probe.pumpUntil("run.completed", &seen);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.endsWith(u8, written, "{\"id\":3,\"jsonrpc\":\"2.0\",\"method\":\"clarify.respond\",\"params\":{\"request_id\":\"aaaa1111\",\"answer\":\"a\"}}\n"));
}

test "a cancel sends session.interrupt for the native session and answers cancelling" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_interrupted_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("long", &refusal);
    try testing.expectError(error.RunNotFound, probe.handle.?.cancel(probe.arena.allocator(), "run-unknown", &refusal));

    const cancelled = try probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expect(cancelled.accepted);
    try testing.expectEqual(oap_types.RunStatus.cancelling, cancelled.status);
    var seen = std.ArrayList(contract.Event).empty;
    const failed = try probe.pumpUntil("run.failed", &seen);
    try testing.expectEqualStrings("hermes_interrupted", (try probe.payloadOf(failed)).get("error").?.object.get("code").?.string);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.endsWith(u8, written, "{\"id\":3,\"jsonrpc\":\"2.0\",\"method\":\"session.interrupt\",\"params\":{\"session_id\":\"sess0001\"}}\n"));
}

test "a prompt.submit the gateway does not stream is refused and leaves the session idle for the next" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"id":2,"jsonrpc":"2.0","result":{"status":"steered"}}\n'
        \\take; printf '{"id":3,"jsonrpc":"2.0","result":{"status":"streaming"}}\n'
        \\printf '{"jsonrpc":"2.0","method":"event","params":{"type":"message.start","session_id":"sess0001","seq":1}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    try testing.expectError(error.BackendFailed, probe.submit("busy", &refusal));
    try testing.expectEqualStrings("the hermes gateway answered prompt.submit with status \"steered\"", refusal.message);
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
    try testing.expectEqual(oap_types.SessionStatus.idle, (try probe.handle.?.state(probe.arena.allocator(), &refusal)).status);
    try testing.expect((try probe.submit("again", &refusal)).accepted);
}

test "a gateway whose first frame is not gateway.ready refuses the open before session.create" {
    var probe: Probe = undefined;
    try probe.init(
        \\#!/bin/sh
        \\exec 3>>"$(dirname "$0")/stdin.log"
        \\printf '{"jsonrpc":"2.0","method":"event","params":{"type":"message.delta","session_id":"sess0001","seq":1,"payload":{"text":"x"}}}\n'
        \\while IFS= read -r line; do printf '%s\n' "$line" >&3; done
        \\
    );
    defer probe.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, probe.open(&refusal));
    try testing.expectEqualStrings("the hermes gateway's first frame was not a valid gateway.ready", refusal.message);
    try testing.expectEqualStrings("", try probe.fake.written(probe.arena.allocator()));
}

test "a session.create the gateway refuses fails the open with its error" {
    var probe: Probe = undefined;
    try probe.init(
        \\#!/bin/sh
        \\printf '{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"e3b0c44298fc1c149afbf4c8996fb924"}}}\n'
        \\IFS= read -r line
        \\printf '{"id":1,"jsonrpc":"2.0","error":{"code":-32000,"message":"no such model"}}\n'
        \\while IFS= read -r line; do :; done
        \\
    );
    defer probe.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, probe.open(&refusal));
    try testing.expectEqualStrings("hermes rpc error for session.create (-32000): no such model", refusal.message);
}

test "a gateway that dies mid-run fails the run with its exit and closes the session" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_turn_admitted ++ "exit 5\n");
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("doomed", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const failed = try probe.pumpUntil("run.failed", &seen);
    const failure = (try probe.payloadOf(failed)).get("error").?.object;
    try testing.expectEqualStrings("hermes_process_exit", failure.get("code").?.string);
    try testing.expectEqualStrings("child exited with status 5", failure.get("message").?.string);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
    try testing.expectError(error.SessionClosed, probe.submit("after", &refusal));
}
