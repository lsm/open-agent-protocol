const std = @import("std");
const builtin = @import("builtin");
const contract = @import("contract");
const oap_types = @import("oap_types");
const compat = @import("compat");
const json_encode = @import("json_encode");
const session = @import("session");
const native = @import("native");
const httpapi = @import("httpapi");
const client = @import("client");

pub const endpoint_id = session.endpoint_id;
pub const capability_revision = "opencode-v1.18.29-oapx-v1";
const journal_reason = "oapx keeps no journal for this backend";

const features = [_]contract.Feature{
    .{ .key = "action.permissions", .level = .unavailable, .reason = "durable stream carries no permission events; the polling surface is unexercised" },
    .{ .key = "action.tools", .level = .native, .reason = "tool.called/progress/success/failed lifecycle observed natively" },
    .{ .key = "action.tools.execute", .level = .unavailable, .reason = "tools execute server-side; no client-hosted execution surface" },
    .{ .key = "capabilities", .level = .emulated, .reason = "descriptor synthesized from the pinned route inventory" },
    .{ .key = "models.list", .level = .degraded, .reason = "the models this session is observed to run, projected from the native session record and durable step events; the server's own model.list route has no pinned response shape at this revision" },
    .{ .key = "protocol.initialize", .level = .emulated, .reason = "OpenCode has no initialize handshake; OpenAPI and catalogs describe the server" },
    .{ .key = "run.cancel", .level = .degraded, .reason = "interrupt is intent with idle no-op; settlement derived from durable evidence and the active set" },
    .{ .key = "run.reconciliation", .level = .emulated, .reason = "adapter-owned projection over active and durable sequence" },
    .{ .key = "run.replay", .level = .unavailable, .reason = journal_reason },
    .{ .key = "run.resume", .level = .unavailable, .reason = journal_reason },
    .{ .key = "run.status", .level = .native, .reason = "session.active and durable step events" },
    .{ .key = "run.streaming", .level = .degraded, .reason = "durable stream carries full-value text.ended boundaries, not live deltas" },
    .{ .key = "session.message.delivery.auto", .level = .emulated, .reason = "no native auto; maps to steer which starts immediately when idle" },
    .{ .key = "session.message.delivery.queue", .level = .native, .reason = "SessionInput.Admitted carries delivery=queue with promotedSeq; a reservation is admitted durably and promoted by session.next.prompted" },
    .{ .key = "session.message.delivery.steer", .level = .unavailable, .reason = "an explicit steer request is rejected as outside the v0.1 subset; the server's default delivery is exposed through an auto request" },
    .{ .key = "session.message.submit", .level = .native, .reason = "durable admission receipt with typed conflict rejection" },
    .{ .key = "session.open", .level = .native, .reason = "POST /api/session with server-assigned identity" },
    .{ .key = "session.state", .level = .emulated, .reason = "active set and adapter-owned projection" },
};

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = "OpenCode Server Adapter", .version = "v1.18.29", .adapter = "opencode-http-sse" },
    .capability_revision = capability_revision,
    .features = &features,
    .limits = .{ .max_active_runs_per_session = session.max_active_runs, .max_queued_runs_per_session = session.max_queued_runs },
};

pub const Config = struct {
    endpoint: []const u8,
    username: []const u8 = "",
    password: []const u8 = "",
    agent: []const u8 = "",
    frame_limit: usize = httpapi.default_frame_limit,
    history_limit: usize = session.default_history_limit,
    request_timeout_ns: u64 = 60 * std.time.ns_per_s,
    settle_poll_min_ns: u64 = 10 * std.time.ns_per_ms,
    settle_poll_max_ns: u64 = 500 * std.time.ns_per_ms,
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
    ids: u64 = 0,

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

pub const Session = struct {
    owner: *Adapter,
    gpa: std.mem.Allocator,
    id: []u8,
    reducer_arena: *std.heap.ArenaAllocator,
    target: client.Target,
    endpoint: httpapi.Endpoint,
    subscription: ?*client.Connection = null,
    stream: httpapi.Stream,
    reducer: session.Reducer = undefined,
    ended: bool = false,
    next_poll_ns: u64 = 0,
    poll_delay_ns: u64 = 0,

    fn open(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const gpa = owner.allocator;
        const config = owner.config;
        const self = try gpa.create(Session);
        errdefer gpa.destroy(self);
        const id = if (request.session_id.len > 0) try gpa.dupe(u8, request.session_id) else try mint(owner, gpa);
        errdefer gpa.free(id);
        const reducer_arena = try gpa.create(std.heap.ArenaAllocator);
        errdefer gpa.destroy(reducer_arena);
        reducer_arena.* = std.heap.ArenaAllocator.init(gpa);
        errdefer reducer_arena.deinit();
        const target = client.parseEndpoint(reducer_arena.allocator(), config.endpoint) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            const message = try std.fmt.allocPrint(arena, "the OpenCode endpoint \"{s}\" is not a plain http URL", .{config.endpoint});
            return refusal.fail(error.BackendFailed, message);
        };
        self.* = .{
            .owner = owner,
            .gpa = gpa,
            .id = id,
            .reducer_arena = reducer_arena,
            .target = target,
            .endpoint = .{ .base_path = target.base_path, .username = config.username, .password = config.password },
            .stream = httpapi.Stream.init(gpa, config.frame_limit),
        };
        errdefer self.stream.deinit();

        const own = self.owned();
        const created_request = try httpapi.createSession(own, self.endpoint, .{ .agent = config.agent });
        const created_response = self.exchange(created_request) catch |err| return refusal.fail(error.BackendFailed, try describe(arena, "create OpenCode session", err));
        const info = switch (try httpapi.createSessionResult(own, created_response, config.frame_limit)) {
            .ok => |value| value,
            .failed => |failure| return refusal.fail(error.BackendFailed, try std.fmt.allocPrint(arena, "create OpenCode session: {s}", .{failure.message})),
        };

        const subscribe_request = try httpapi.subscribe(own, self.endpoint, info.id, -1);
        const subscription = client.Connection.start(gpa, target, try client.encode(own, target, subscribe_request), config.frame_limit) catch |err| return refusal.fail(error.BackendFailed, try describe(arena, "subscribe OpenCode session events", err));
        self.subscription = subscription;
        errdefer {
            subscription.destroy(gpa);
            self.subscription = null;
        }
        const started = monotonic();
        while (!subscription.reader.head_done) {
            if (!subscription.open) return refusal.fail(error.BackendFailed, "subscribe OpenCode session events: the server closed the stream before answering");
            if (monotonic() -| started > config.request_timeout_ns) return refusal.fail(error.BackendFailed, "subscribe OpenCode session events: no answer in time");
            _ = subscription.poll(20) catch |err| return refusal.fail(error.BackendFailed, try describe(arena, "subscribe OpenCode session events", err));
        }
        if (subscription.reader.status != 200) {
            const message = try std.fmt.allocPrint(arena, "subscribe OpenCode session events: HTTP {d}", .{subscription.reader.status});
            return refusal.fail(error.BackendFailed, message);
        }

        self.reducer = session.Reducer.init(reducer_arena, .{
            .session_id = id,
            .native_id = info.id,
            .model = try session.normalizeModel(own, info.model),
            .history_limit = config.history_limit,
            .revision = capability_revision,
            .counter = &owner.ids,
            .now_ms = wallClock,
        }, .{ .context = self, .prompt = prompt, .interrupt = interrupt, .active = active, .history = history });
        self.reducer.open() catch |err| return lift(err);
        try self.feed();
        return self;
    }

    fn mint(owner: *Adapter, allocator: std.mem.Allocator) ![]u8 {
        owner.ids += 1;
        return std.fmt.allocPrint(allocator, "session-{d}", .{owner.ids});
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
        .models = models,
    };

    fn cast(ptr: *anyopaque) *Session {
        return @ptrCast(@alignCast(ptr));
    }

    fn owned(self: *Session) std.mem.Allocator {
        return self.reducer_arena.allocator();
    }

    fn destroy(self: *Session) void {
        const gpa = self.gpa;
        if (self.subscription) |subscription| subscription.destroy(gpa);
        self.stream.deinit();
        self.reducer_arena.deinit();
        gpa.destroy(self.reducer_arena);
        gpa.free(self.id);
        gpa.destroy(self);
    }

    fn exchange(self: *Session, request: httpapi.Request) !httpapi.Response {
        return client.roundTrip(self.gpa, self.owned(), self.target, request, self.owner.config.frame_limit, self.owner.config.request_timeout_ns);
    }

    fn transportFailure(self: *Session, err: anyerror) std.mem.Allocator.Error!session.Failure {
        if (err == error.OutOfMemory) return error.OutOfMemory;
        return .{ .message = try std.fmt.allocPrint(self.owned(), "opencode httpapi: {s}", .{@errorName(err)}) };
    }

    fn prompt(context: *anyopaque, arena: std.mem.Allocator, native_session: []const u8, request: native.PromptRequest) std.mem.Allocator.Error!session.PromptOutcome {
        const self = cast(context);
        const response = self.exchange(try httpapi.prompt(arena, self.endpoint, native_session, request)) catch |err| return .{ .failed = try self.transportFailure(err) };
        return switch (try httpapi.promptResult(arena, response, native_session, self.owner.config.frame_limit)) {
            .ok => |admitted| .{ .admitted = admitted },
            .failed => |failure| .{ .failed = .{ .message = failure.message, .api = failure.api } },
        };
    }

    fn interrupt(context: *anyopaque, arena: std.mem.Allocator, native_session: []const u8) std.mem.Allocator.Error!?session.Failure {
        const self = cast(context);
        const response = self.exchange(try httpapi.interrupt(arena, self.endpoint, native_session)) catch |err| return try self.transportFailure(err);
        const failure = try httpapi.interruptResult(arena, response, native_session, self.owner.config.frame_limit) orelse return null;
        return .{ .message = failure.message, .api = failure.api };
    }

    fn active(context: *anyopaque, arena: std.mem.Allocator, native_session: []const u8) std.mem.Allocator.Error!session.ActiveOutcome {
        const self = cast(context);
        const response = self.exchange(try httpapi.active(arena, self.endpoint)) catch |err| return .{ .failed = try self.transportFailure(err) };
        return switch (try httpapi.activeResult(arena, response, self.owner.config.frame_limit)) {
            .ok => |running| .{ .listed = for (running) |candidate| {
                if (std.mem.eql(u8, candidate, native_session)) break true;
            } else false },
            .failed => |failure| .{ .failed = .{ .message = failure.message, .api = failure.api } },
        };
    }

    fn history(context: *anyopaque, arena: std.mem.Allocator, native_session: []const u8, after: i64, limit: usize) std.mem.Allocator.Error!session.HistoryOutcome {
        const self = cast(context);
        const response = self.exchange(try httpapi.history(arena, self.endpoint, native_session, after, limit)) catch |err| return .{ .failed = try self.transportFailure(err) };
        return switch (try httpapi.historyResult(arena, response, native_session, self.owner.config.frame_limit)) {
            .ok => |page| .{ .page = page },
            .failed => |failure| .{ .failed = .{ .message = failure.message, .api = failure.api } },
        };
    }

    fn feed(self: *Session) contract.Failure!void {
        const subscription = self.subscription orelse return;
        const chunk = try subscription.reader.take(self.owned());
        if (chunk.len > 0) {
            const batch = try self.stream.feed(self.owned(), chunk);
            for (batch.events) |observed| self.reducer.observe(observed) catch |err| return lift(err);
            if (batch.failure) |message| return self.end(message);
        }
        if (!subscription.open and !self.ended) {
            const message = try self.stream.finish(self.owned());
            try self.end(message);
        }
    }

    fn end(self: *Session, message: []const u8) contract.Failure!void {
        if (self.ended) return;
        self.ended = true;
        if (self.subscription) |subscription| subscription.shut();
        self.reducer.transportFailed(message) catch |err| return lift(err);
    }

    fn settle(self: *Session) contract.Failure!void {
        if (self.reducer.settlements.items.len == 0) {
            self.poll_delay_ns = 0;
            return;
        }
        const now = monotonic();
        if (self.poll_delay_ns != 0 and now < self.next_poll_ns) return;
        self.reducer.poll() catch |err| return lift(err);
        const config = self.owner.config;
        self.poll_delay_ns = if (self.poll_delay_ns == 0) config.settle_poll_min_ns else @min(self.poll_delay_ns * 2, config.settle_poll_max_ns);
        self.next_poll_ns = monotonic() + self.poll_delay_ns;
    }

    fn idOf(ptr: *anyopaque) []const u8 {
        return cast(ptr).id;
    }

    fn state(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        _ = refusal;
        const self = cast(ptr);
        if (self.reducer.unusable) return error.SessionClosed;
        const current = self.reducer.active;
        const running = current != null and !current.?.terminal;
        const model = self.reducer.options.model;
        const active_run_id: ?[]const u8 = if (running) try arena.dupe(u8, current.?.id) else null;
        const current_model_id: ?[]const u8 = if (model.len > 0) try arena.dupe(u8, model) else null;
        return .{
            .session_id = self.id,
            .status = if (running) .running else if (self.reducer.reserved != null) .queued else .idle,
            .active_run_id = active_run_id,
            .current_model_id = current_model_id,
            .updated_at_ms = wallClock(),
        };
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        _ = refusal;
        const self = cast(ptr);
        if (request.messages.len != 1) return error.InvalidSubmission;
        const message = request.messages[0];
        if (message.role != .user) return error.InvalidSubmission;
        const text = switch (message.content) {
            .text => |content| try self.owned().dupe(u8, content),
            .parts => return error.InvalidSubmission,
        };
        const delivery: []const u8 = switch (request.delivery) {
            .auto => "auto",
            .queue => "queue",
            else => return error.InvalidSubmission,
        };
        const admission = self.reducer.submit(request.session_id, text, delivery) catch |err| return switch (err) {
            error.InvalidSubmission, error.Unsupported => error.InvalidSubmission,
            error.SessionClosed => error.SessionClosed,
            error.RunNotFound => error.RunNotFound,
            error.RunActive => error.RunActive,
            else => lift(err),
        };
        const message_ids = try arena.alloc([]const u8, admission.message_ids.len);
        for (admission.message_ids, message_ids) |source, *slot| slot.* = try arena.dupe(u8, source);
        const submission_id = try arena.dupe(u8, admission.submission_id);
        const delivery_resolution: ?[]const u8 = if (admission.delivery_resolution.len > 0) try arena.dupe(u8, admission.delivery_resolution) else null;
        const run_id = try arena.dupe(u8, admission.run_id);
        const model_id: ?[]const u8 = if (admission.model_id.len > 0) try arena.dupe(u8, admission.model_id) else null;
        return .{
            .session_id = self.id,
            .accepted = true,
            .submission_id = submission_id,
            .requested_delivery = if (std.mem.eql(u8, admission.requested_delivery, "queue")) .queue else .auto,
            .effective_delivery = if (std.mem.eql(u8, admission.effective_delivery, "start")) .start else .queue,
            .delivery_resolution = delivery_resolution,
            .admission = if (std.mem.eql(u8, admission.admission, "started")) .started else .queued,
            .run_id = run_id,
            .status = if (admission.status == .running) .running else .queued,
            .model_id = model_id,
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
        const self = cast(ptr);
        const answered = self.reducer.cancel(run_id) catch |err| return switch (err) {
            error.RunNotFound => error.RunNotFound,
            error.RunTerminal => error.RunTerminal,
            error.SessionClosed => error.SessionClosed,
            error.CancellationAmbiguous => refusal.fail(error.BackendFailed, "the OpenCode server did not take the interrupt"),
            else => lift(err),
        };
        return .{
            .session_id = self.id,
            .run_id = try arena.dupe(u8, answered.run_id),
            .accepted = true,
            .status = switch (answered.status) {
                .cancelled => .cancelled,
                .queued => .queued,
                .running => .running,
                else => .cancelling,
            },
        };
    }

    fn models(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ModelsRequest, refusal: *contract.Refusal) contract.Failure!oap_types.ModelsResponse {
        const self = cast(ptr);
        if (!request.allowsDegraded(contract.feature_models_list)) return refusal.degraded(contract.feature_models_list);
        const catalog = self.reducer.models(request.session_id, true) catch |err| return switch (err) {
            error.SessionClosed => error.SessionClosed,
            error.RunNotFound => error.RunNotFound,
            else => lift(err),
        };
        const listed = catalog.object.get("models").?.array.items;
        const out = try arena.alloc(oap_types.ModelDescriptor, listed.len);
        for (listed, out) |entry, *slot| {
            const provider = entry.object.get("provider_id");
            const default = entry.object.get("default");
            const model_id = try arena.dupe(u8, entry.object.get("id").?.string);
            const provider_id: ?[]const u8 = if (provider) |value| try arena.dupe(u8, value.string) else null;
            slot.* = .{ .id = model_id, .provider_id = provider_id, .default = default != null and default.?.bool };
        }
        const current = catalog.object.get("current_model_id");
        return .{
            .session_id = self.id,
            .current_model_id = if (current) |value| try arena.dupe(u8, value.string) else null,
            .models = out,
        };
    }

    fn pump(ptr: *anyopaque, wait_ns: u64) contract.Failure!bool {
        const self = cast(ptr);
        const before = self.reducer.envelopes.items.len;
        if (self.subscription) |subscription| {
            const wait_ms: i32 = @intCast(@min(wait_ns / std.time.ns_per_ms, 1000));
            var reads: usize = 0;
            while (reads < 64 and subscription.open) : (reads += 1) {
                const got = subscription.poll(if (reads == 0) wait_ms else 0) catch |err| {
                    try self.end(try std.fmt.allocPrint(self.owned(), "opencode httpapi: {s}", .{@errorName(err)}));
                    break;
                };
                if (!got) break;
            }
            try self.feed();
        }
        if (!self.ended) try self.settle();
        return self.reducer.envelopes.items.len != before;
    }

    fn drain(ptr: *anyopaque, allocator: std.mem.Allocator, out: *std.ArrayList(contract.Event)) contract.Failure!void {
        const self = cast(ptr);
        try appendEvents(allocator, self.reducer.envelopes.items, out);
        self.reducer.envelopes.clearRetainingCapacity();
    }

    fn activity(ptr: *anyopaque) contract.Activity {
        const self = cast(ptr);
        const current = self.reducer.active;
        if (current != null and !current.?.terminal) return .running;
        if (self.reducer.reserved != null) return .running;
        return .idle;
    }

    fn close(ptr: *anyopaque) void {
        cast(ptr).destroy();
    }
};

fn describe(arena: std.mem.Allocator, what: []const u8, err: anyerror) contract.Failure![]const u8 {
    if (err == error.OutOfMemory) return error.OutOfMemory;
    return std.fmt.allocPrint(arena, "{s}: {s}", .{ what, @errorName(err) });
}

fn appendEvents(allocator: std.mem.Allocator, emitted: []const std.json.Value, out: *std.ArrayList(contract.Event)) contract.Failure!void {
    try out.ensureUnusedCapacity(allocator, emitted.len);
    const first = out.items.len;
    errdefer {
        for (out.items[first..]) |written| {
            allocator.free(written.line);
            allocator.free(written.run_id);
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

pub const fake_session = "ses_fake00000000000000";

pub const FakeServer = struct {
    server: compat.net.Server = undefined,
    port: u16 = 0,
    thread: ?std.Thread = null,
    stop: std.atomic.Value(bool) = .init(false),
    turns: []const []const []const u8 = &.{},
    after_interrupt: []const []const u8 = &.{},
    refuse_prompts: bool = false,
    busy_polls: usize = 0,
    prompts: std.atomic.Value(usize) = .init(0),
    interrupts: std.atomic.Value(usize) = .init(0),
    actives: std.atomic.Value(usize) = .init(0),
    histories: std.atomic.Value(usize) = .init(0),
    failure: ?anyerror = null,

    pub fn start(self: *FakeServer) !void {
        if (builtin.os.tag == .windows) return error.SkipZigTest;
        const address = try compat.net.resolveAddress(testing.allocator, "127.0.0.1", 0);
        self.server = try compat.net.tcpListen(address, .{ .reuse_address = true });
        self.port = compat.net.listenAddress(&self.server).getPort();
        self.thread = try std.Thread.spawn(.{}, run, .{self});
    }

    pub fn finish(self: *FakeServer) void {
        self.stop.store(true, .release);
        if (self.thread) |thread| thread.join();
        compat.net.closeServer(&self.server);
        if (self.failure) |err| std.debug.print("fake OpenCode server failed: {s}\n", .{@errorName(err)});
    }

    pub fn config(self: *const FakeServer, arena: std.mem.Allocator) !Config {
        return .{
            .endpoint = try std.fmt.allocPrint(arena, "http://127.0.0.1:{d}", .{self.port}),
            .request_timeout_ns = 10 * std.time.ns_per_s,
            .settle_poll_min_ns = std.time.ns_per_ms,
            .settle_poll_max_ns = 20 * std.time.ns_per_ms,
        };
    }

    const Conn = struct {
        stream: compat.net.Stream,
        received: std.ArrayList(u8) = .empty,
        open: bool = true,
    };

    fn run(self: *FakeServer) void {
        self.serve() catch |err| {
            self.failure = err;
        };
    }

    fn serve(self: *FakeServer) !void {
        var arena_state = std.heap.ArenaAllocator.init(std.heap.page_allocator);
        defer arena_state.deinit();
        const arena = arena_state.allocator();
        var conns = std.ArrayList(*Conn).empty;
        defer for (conns.items) |conn| {
            if (conn.open) conn.stream.close();
        };
        var sse: ?*Conn = null;
        var seq: i64 = 0;
        while (!self.stop.load(.acquire)) {
            if (try compat.net.readableWithin(compat.net.serverHandle(&self.server), 2)) {
                const conn = try arena.create(Conn);
                conn.* = .{ .stream = try compat.net.acceptStream(&self.server) };
                try conns.append(arena, conn);
            }
            for (conns.items) |conn| {
                if (!conn.open or conn == sse) continue;
                if (!try compat.net.readableWithin(compat.net.streamHandle(&conn.stream), 0)) continue;
                var buffer: [16 * 1024]u8 = undefined;
                const count = client.readSome(&conn.stream, &buffer) catch 0;
                if (count == 0) {
                    conn.open = false;
                    conn.stream.close();
                    continue;
                }
                try conn.received.appendSlice(arena, buffer[0..count]);
                const head_end = std.mem.indexOf(u8, conn.received.items, "\r\n\r\n") orelse continue;
                const head = conn.received.items[0..head_end];
                var length: usize = 0;
                var lines = std.mem.splitSequence(u8, head, "\r\n");
                const request_line = lines.next().?;
                while (lines.next()) |line| {
                    if (std.ascii.startsWithIgnoreCase(line, "content-length:")) length = try std.fmt.parseInt(usize, std.mem.trim(u8, line["content-length:".len..], " "), 10);
                }
                if (conn.received.items.len < head_end + 4 + length) continue;
                const body = conn.received.items[head_end + 4 .. head_end + 4 + length];
                var words = std.mem.splitScalar(u8, request_line, ' ');
                const method = words.next().?;
                const target = words.next().?;
                if (std.mem.startsWith(u8, target, "/api/session/" ++ fake_session ++ "/event")) {
                    try conn.stream.writeAll("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n");
                    sse = conn;
                    continue;
                }
                if (std.mem.eql(u8, method, "POST") and std.mem.eql(u8, target, "/api/session")) {
                    try respond(conn, "200 OK", "{\"data\":{\"id\":\"" ++ fake_session ++ "\",\"projectID\":\"prj_fake\",\"model\":{\"id\":\"fixture\",\"providerID\":\"fixture\"},\"time\":{\"created\":1,\"updated\":1}}}");
                } else if (std.mem.endsWith(u8, target, "/prompt")) {
                    const turn = self.prompts.fetchAdd(1, .acq_rel);
                    if (self.refuse_prompts) {
                        try respond(conn, "409 Conflict", "{\"name\":\"ConflictError\",\"data\":{\"message\":\"busy\"}}");
                        continue;
                    }
                    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, arena, body, .{});
                    const message_id = parsed.object.get("id").?.string;
                    const delivery = parsed.object.get("delivery").?.string;
                    const text = parsed.object.get("prompt").?.object.get("text").?.string;
                    const receipt = try std.fmt.allocPrint(arena, "{{\"data\":{{\"admittedSeq\":{d},\"id\":\"{s}\",\"sessionID\":\"" ++ fake_session ++ "\",\"prompt\":{{\"text\":\"{s}\"}},\"delivery\":\"{s}\",\"timeCreated\":1,\"promotedSeq\":{d}}}}}", .{ turn + 1, message_id, text, delivery, seq + 1 });
                    try respond(conn, "200 OK", receipt);
                    if (turn < self.turns.len) try emit(arena, sse, self.turns[turn], message_id, &seq);
                } else if (std.mem.endsWith(u8, target, "/interrupt")) {
                    _ = self.interrupts.fetchAdd(1, .acq_rel);
                    try conn.stream.writeAll("HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n");
                    conn.open = false;
                    conn.stream.close();
                    try emit(arena, sse, self.after_interrupt, "", &seq);
                } else if (std.mem.eql(u8, target, "/api/session/active")) {
                    const polled = self.actives.fetchAdd(1, .acq_rel);
                    if (polled < self.busy_polls) {
                        try respond(conn, "200 OK", "{\"data\":{\"" ++ fake_session ++ "\":{\"type\":\"running\"}}}");
                    } else {
                        try respond(conn, "200 OK", "{\"data\":{}}");
                    }
                } else if (std.mem.indexOf(u8, target, "/history") != null) {
                    _ = self.histories.fetchAdd(1, .acq_rel);
                    try respond(conn, "200 OK", "{\"data\":[],\"hasMore\":false}");
                } else {
                    try respond(conn, "404 Not Found", "{}");
                }
            }
        }
    }

    fn respond(conn: *Conn, status: []const u8, body: []const u8) !void {
        var head: [256]u8 = undefined;
        try conn.stream.writeAll(try std.fmt.bufPrint(&head, "HTTP/1.1 {s}\r\nContent-Type: application/json\r\nContent-Length: {d}\r\nConnection: close\r\n\r\n", .{ status, body.len }));
        try conn.stream.writeAll(body);
        conn.open = false;
        conn.stream.close();
    }

    fn emit(arena: std.mem.Allocator, sse: ?*Conn, templates: []const []const u8, message_id: []const u8, seq: *i64) !void {
        const conn = sse orelse return error.NoSubscription;
        for (templates) |template| {
            seq.* += 1;
            const numbered = try std.mem.replaceOwned(u8, arena, template, "%SEQ%", try std.fmt.allocPrint(arena, "{d}", .{seq.*}));
            const line = try std.mem.replaceOwned(u8, arena, numbered, "%MSG%", message_id);
            try conn.stream.writeAll(try std.mem.concat(arena, u8, &.{ "data: ", line, "\n\n" }));
        }
    }
};

fn fakeEvent(comptime kind: []const u8, comptime data: []const u8) []const u8 {
    return "{\"id\":\"evt_%SEQ%\",\"type\":\"session.next." ++ kind ++ "\",\"durable\":{\"aggregateID\":\"" ++ fake_session ++ "\",\"seq\":%SEQ%,\"version\":1},\"data\":{\"timestamp\":%SEQ%,\"sessionID\":\"" ++ fake_session ++ "\"" ++ data ++ "}}";
}

pub const prompted = fakeEvent("prompted", ",\"messageID\":\"%MSG%\",\"prompt\":{\"text\":\"hello\"},\"delivery\":\"steer\"");
pub const step_started = fakeEvent("step.started", ",\"assistantMessageID\":\"msg_a1\",\"agent\":\"build\",\"model\":{\"id\":\"fixture\",\"providerID\":\"fixture\"}");
pub const text_ended = fakeEvent("text.ended", ",\"assistantMessageID\":\"msg_a1\",\"textID\":\"t1\",\"text\":\"done\"");
pub const step_ended = fakeEvent("step.ended", ",\"assistantMessageID\":\"msg_a1\",\"finish\":\"stop\",\"cost\":0,\"tokens\":{\"input\":2,\"output\":5,\"reasoning\":0,\"cache\":{\"read\":0,\"write\":0}}");
pub const step_aborted = fakeEvent("step.ended", ",\"assistantMessageID\":\"msg_a1\",\"finish\":\"aborted\",\"cost\":0,\"tokens\":{\"input\":2,\"output\":5,\"reasoning\":0,\"cache\":{\"read\":0,\"write\":0}}");

pub const text_turn = [_][]const u8{ prompted, step_started, text_ended, step_ended };
pub const open_turn = [_][]const u8{ prompted, step_started };

const Probe = struct {
    fake: FakeServer,
    adapter: Adapter,
    arena: std.heap.ArenaAllocator,
    handle: ?contract.Session = null,

    fn init(self: *Probe, turns: []const []const []const u8, after_interrupt: []const []const u8) !void {
        self.fake = .{ .turns = turns, .after_interrupt = after_interrupt };
        try self.fake.start();
        self.arena = std.heap.ArenaAllocator.init(testing.allocator);
        self.adapter = Adapter.init(testing.allocator, try self.fake.config(self.arena.allocator()));
        self.handle = null;
    }

    fn deinit(self: *Probe) void {
        if (self.handle) |opened| opened.close();
        self.fake.finish();
        self.arena.deinit();
    }

    fn open(self: *Probe, refusal: *contract.Refusal) !contract.Session {
        const opened = try self.adapter.adapter().vtable.open(&self.adapter, self.arena.allocator(), .{ .session_id = "s1", .participant = "user" }, refusal);
        self.handle = opened;
        return opened;
    }

    fn submit(self: *Probe, delivery: oap_types.RequestedDelivery, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const messages = try self.arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = "hello" } }});
        const request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = messages, .delivery = delivery };
        return self.handle.?.submit(self.arena.allocator(), &request, refusal);
    }

    fn pumpUntil(self: *Probe, comptime kind: []const u8, seen: *std.ArrayList(contract.Event)) !contract.Event {
        var rounds: usize = 0;
        while (rounds < 2000) : (rounds += 1) {
            var drained = std.ArrayList(contract.Event).empty;
            try self.handle.?.drain(self.arena.allocator(), &drained);
            try seen.appendSlice(self.arena.allocator(), drained.items);
            for (drained.items) |candidate| {
                if (std.mem.indexOf(u8, candidate.line, "\"type\":\"" ++ kind ++ "\"") != null) return candidate;
            }
            _ = try self.handle.?.pump(5 * std.time.ns_per_ms);
        }
        return error.EventNeverArrived;
    }
};

fn kinds(allocator: std.mem.Allocator, events: []const contract.Event) ![]const []const u8 {
    const names = try allocator.alloc([]const u8, events.len);
    for (events, names) |emitted, *name| {
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, allocator, emitted.line, .{});
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

test "a steered turn is admitted started, streams its text, and settles once the server reports it idle" {
    var probe: Probe = undefined;
    try probe.init(&.{&text_turn}, &.{});
    defer probe.deinit();
    probe.fake.busy_polls = 3;
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);

    const admitted = try probe.submit(.auto, &refusal);
    try testing.expectEqual(oap_types.Admission.started, admitted.admission);
    try testing.expectEqualStrings("fixture/fixture", admitted.model_id.?);
    try testing.expectEqual(@as(usize, 1), admitted.message_ids.len);
    try testing.expect(std.mem.startsWith(u8, admitted.message_ids[0], "msg_oap"));

    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);
    const names = try kinds(probe.arena.allocator(), seen.items);
    try testing.expectEqualStrings("run.started", names[0]);
    for (seen.items, 1..) |emitted, sequence| {
        try testing.expectEqualStrings(admitted.run_id.?, emitted.run_id);
        try testing.expectEqual(@as(u64, sequence), emitted.sequence);
        try testing.expect(std.mem.indexOf(u8, emitted.line, "\"capability_revision\":\"" ++ capability_revision ++ "\"") != null);
    }
    try testing.expect(probe.fake.actives.load(.acquire) > 3);
    try testing.expect(probe.fake.histories.load(.acquire) >= 1);
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
}

test "a cancel interrupts the server and the run settles cancelled once it goes idle" {
    var probe: Probe = undefined;
    try probe.init(&.{&open_turn}, &.{step_aborted});
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit(.auto, &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.started", &seen);

    const cancelled = try probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expect(cancelled.accepted);
    try testing.expectEqual(oap_types.RunStatus.cancelling, cancelled.status);
    try testing.expectEqual(@as(usize, 1), probe.fake.interrupts.load(.acquire));
    _ = try probe.pumpUntil("run.cancelled", &seen);
}

test "a prompt the server refuses is admitted and then failed, closing the session as Go does" {
    var probe: Probe = undefined;
    try probe.init(&.{}, &.{});
    defer probe.deinit();
    probe.fake.refuse_prompts = true;
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit(.auto, &refusal);
    try testing.expectEqual(oap_types.Admission.queued, admitted.admission);
    var seen = std.ArrayList(contract.Event).empty;
    const failed = try probe.pumpUntil("run.failed", &seen);
    try testing.expect(std.mem.indexOf(u8, failed.line, "opencode_admission_ambiguous") != null);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
}

test "an endpoint that is not plain http refuses the open, naming it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var adapter_state = Adapter.init(testing.allocator, .{ .endpoint = "https://127.0.0.1:1" });
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, adapter_state.adapter().vtable.open(&adapter_state, arena.allocator(), .{ .session_id = "s1", .participant = "user" }, &refusal));
    try testing.expectEqualStrings("the OpenCode endpoint \"https://127.0.0.1:1\" is not a plain http URL", refusal.message);
}

test "a server that closes the event stream mid-run fails the run and closes the session" {
    var probe: Probe = undefined;
    try probe.init(&.{&open_turn}, &.{});
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit(.auto, &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.started", &seen);
    probe.fake.stop.store(true, .release);
    if (probe.fake.thread) |thread| thread.join();
    probe.fake.thread = null;
    const failed = try probe.pumpUntil("run.failed", &seen);
    try testing.expect(std.mem.indexOf(u8, failed.line, "opencode_stream_failed") != null);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
}

test "the degraded models catalog is served only to a caller that opts into it" {
    var probe: Probe = undefined;
    try probe.init(&.{}, &.{});
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.open(&refusal);
    const lister = opened.vtable.models.?;
    try testing.expectError(error.CapabilityDegraded, lister(opened.ptr, probe.arena.allocator(), &.{ .session_id = "s1" }, &refusal));
    try testing.expectEqualStrings("models.list", refusal.feature);
    const catalog = try lister(opened.ptr, probe.arena.allocator(), &.{ .session_id = "s1", .allow_degraded_features = &.{"models.list"} }, &refusal);
    try testing.expectEqualStrings("fixture/fixture", catalog.current_model_id.?);
    try testing.expectEqualStrings("fixture/fixture", catalog.models[0].id);
    try testing.expect(catalog.models[0].default);
}

test "a submission carrying a degraded-feature consent list is admitted, as Go's is" {
    var probe: Probe = undefined;
    try probe.init(&.{&text_turn}, &.{});
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const messages = try probe.arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = "hello" } }});
    const request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = messages, .delivery = .auto, .allow_degraded_features = &.{"run.streaming"} };
    const admitted = try probe.handle.?.submit(probe.arena.allocator(), &request, &refusal);
    try testing.expect(admitted.accepted);
}
