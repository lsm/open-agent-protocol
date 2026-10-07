const std = @import("std");
const harness_pins = @import("harness_pins");
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
pub const capability_revision = harness_pins.opencode_capability_revision;

const reopen_support_reason = "GET /api/session/:id attaches to the bound server session and follows its events from the attach on; an unknown or running session is refused";
const reopen_recovery_reason = "OpenCode attached to the bound server session and reports the model it records; OAP runs and cursors remain process-local";

const features = [_]contract.Feature{
    .{ .key = "action.permissions", .level = .unavailable, .reason = "permission.asked travels only on the volatile global event stream and is not served" },
    .{ .key = "action.tools", .level = .native, .reason = "tool.called/progress/success/failed lifecycle observed natively" },
    .{ .key = "action.tools.execute", .level = .unavailable, .reason = "tools execute server-side; no client-hosted execution surface" },
    .{ .key = "capabilities", .level = .emulated, .reason = "descriptor synthesized from the pinned route inventory" },
    .{ .key = "models.list", .level = .degraded, .reason = "the models this session is observed to run, projected from the native session record and durable step events; the server's own model.list route has no pinned response shape at this revision" },
    .{ .key = "protocol.initialize", .level = .emulated, .reason = "OpenCode has no initialize handshake; OpenAPI and catalogs describe the server" },
    .{ .key = "run.cancel", .level = .degraded, .reason = "interrupt is intent with an idle no-op; a running run settles at session.execution.interrupted and a queued one at session.inbox.cancelled" },
    .{ .key = "run.reconciliation", .level = .emulated, .reason = "adapter-owned projection over the session events; the event stream does not replay" },
    .{ .key = "run.replay", .level = .degraded, .reason = "bounded adapter journal; the native durable cursor is exposed as the transcript cursor" },
    .{ .key = "run.resume", .level = .degraded, .reason = "conversation resume exists natively but is not exercised; OAP resume replays the adapter journal" },
    .{ .key = "run.status", .level = .native, .reason = "session.inbox.delivered starts a run and session.execution.* settles it" },
    .{ .key = "run.streaming", .level = .degraded, .reason = "text and reasoning are forwarded whole at session.text.ended and session.reasoning.ended; the live deltas are not forwarded" },
    .{ .key = contract.feature_compaction_policy, .level = .unavailable, .reason = "compaction is the server's config, fixed when its operator starts it; the adapter attaches to a running server" },
    .{ .key = "session.message.delivery.auto", .level = .emulated, .reason = "no native auto; steer when the session is idle, queue behind an open run" },
    .{ .key = "session.message.delivery.queue", .level = .native, .reason = "a prompt with delivery=queue is admitted to the session inbox and starts its run at session.inbox.delivered" },
    .{ .key = "session.message.delivery.steer", .level = .unavailable, .reason = "an explicit steer request is rejected as outside the v0.1 subset; the server's default delivery is exposed through an auto request" },
    .{ .key = "session.message.submit", .level = .native, .reason = "durable admission receipt with typed conflict rejection" },
    .{ .key = "session.open", .level = .native, .reason = "POST /api/session with server-assigned identity" },
    .{ .key = contract.feature_open_reopen, .level = .native, .reason = reopen_support_reason },
    .{ .key = contract.feature_session_reasoning, .level = .native, .reason = "the session's model carries the level as its variant, which the runner sends on every step: set at create and between runs by switching the session to the same model with the new variant; it needs a model, and a variant the session record does not confirm is refused", .modes = &.{ contract.mode_session_open, contract.mode_session_live } },
    .{ .key = "session.state", .level = .emulated, .reason = "active set and adapter-owned projection" },
};

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = "OpenCode Server Adapter", .version = harness_pins.opencode_endpoint_version, .adapter = "opencode-http-sse" },
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
    request_timeout_ns: u64 = 60 * std.time.ns_per_s,
};

fn runStatus(status: session.Status) oap_types.RunStatus {
    return switch (status) {
        .queued => .queued,
        .running => .running,
        .cancelling => .cancelling,
        .completed => .completed,
        .failed => .failed,
        .cancelled => .cancelled,
    };
}

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
        return .{ .ptr = self, .vtable = &.{ .probe = probe, .open = open, .native_list = nativeList } };
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

    fn nativeList(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.NativeListRequest, refusal: *contract.Refusal) contract.Failure![]const contract.NativeSession {
        const self: *Adapter = @ptrCast(@alignCast(ptr));
        const config = self.config;
        const target = client.parseEndpoint(arena, config.endpoint) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            return refusal.fail(error.BackendFailed, try std.fmt.allocPrint(arena, "the OpenCode endpoint \"{s}\" is not a plain http URL", .{config.endpoint}));
        };
        const endpoint = httpapi.Endpoint{ .base_path = target.base_path, .username = config.username, .password = config.password };
        const listed_response = client.roundTrip(self.allocator, arena, target, try httpapi.sessions(arena, endpoint, request.directory, request.limit), config.frame_limit, config.request_timeout_ns) catch |err| return refusal.fail(error.BackendFailed, try describe(arena, "list OpenCode sessions", err));
        const infos = switch (try httpapi.sessionsResult(arena, listed_response, config.frame_limit)) {
            .ok => |value| value,
            .failed => |failure| return refusal.fail(error.BackendFailed, try std.fmt.allocPrint(arena, "list OpenCode sessions: {s}", .{failure.message})),
        };
        const active_response = client.roundTrip(self.allocator, arena, target, try httpapi.active(arena, endpoint), config.frame_limit, config.request_timeout_ns) catch |err| return refusal.fail(error.BackendFailed, try describe(arena, "list running OpenCode sessions", err));
        const running = switch (try httpapi.activeResult(arena, active_response, config.frame_limit)) {
            .ok => |value| value,
            .failed => |failure| return refusal.fail(error.BackendFailed, try std.fmt.allocPrint(arena, "list running OpenCode sessions: {s}", .{failure.message})),
        };
        const listed = try arena.alloc(contract.NativeSession, infos.len);
        for (infos, listed) |info, *entry| {
            var busy = false;
            for (running) |id| busy = busy or std.mem.eql(u8, id, info.id);
            entry.* = .{ .native_id = info.id, .title = info.title, .directory = info.directory, .updated_at_ms = info.updated, .running = busy };
        }
        return listed;
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
    model: ?native.ModelRef = null,
    reported_level: ?[]const u8 = null,
    native_id: []const u8 = "",
    recovered: bool = false,

    fn open(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        if (request.reasoning_level != null and !request.reopen) return refusal.unsupportedField(contract.feature_session_reasoning, contract.reason_unsatisfiable, "reasoning_level");
        if (request.reopen and std.mem.trim(u8, request.native_session_id, " \t\r\n").len == 0) return refusal.unsupported(contract.feature_open_reopen, contract.reason_unsatisfiable);
        if (request.compaction_policy_json != null) return refusal.unsupportedField(contract.feature_compaction_policy, contract.reason_unadvertised, "compaction_policy");
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
            .stream = httpapi.Stream.init(gpa, config.frame_limit, ""),
        };
        errdefer self.stream.deinit();

        const own = self.owned();
        const info = if (request.reopen) attached: {
            const bound = self.attach(request.native_session_id) catch |err| {
                if (err == error.OutOfMemory) return error.OutOfMemory;
                return refusal.unsupported(contract.feature_open_reopen, contract.reason_unsatisfiable);
            };
            self.recovered = true;
            break :attached bound;
        } else created: {
            const created_request = try httpapi.createSession(own, self.endpoint, .{ .agent = config.agent });
            const created_response = self.exchange(created_request) catch |err| return refusal.fail(error.BackendFailed, try describe(arena, "create OpenCode session", err));
            break :created switch (try httpapi.createSessionResult(own, created_response, config.frame_limit)) {
                .ok => |value| value,
                .failed => |failure| return refusal.fail(error.BackendFailed, try std.fmt.allocPrint(arena, "create OpenCode session: {s}", .{failure.message})),
            };
        };
        self.native_id = info.id;
        self.stream.session = info.id;

        const subscribe_request = try httpapi.subscribe(own, self.endpoint);
        const subscription = client.Connection.start(gpa, target, try client.encode(own, target, subscribe_request), config.frame_limit) catch |err| return refusal.fail(error.BackendFailed, try describe(arena, "subscribe OpenCode session events", err));
        self.subscription = subscription;
        errdefer {
            subscription.destroy(gpa);
            self.subscription = null;
        }

        self.model = info.model;
        self.reducer = session.Reducer.init(reducer_arena, .{
            .session_id = id,
            .native_id = info.id,
            .model = try session.normalizeModel(own, info.model),
            .revision = capability_revision,
            .counter = &owner.ids,
            .now_ms = wallClock,
        }, .{ .context = self, .prompt = prompt, .interrupt = interrupt, .cancel_inbox = cancelInbox });
        self.reducer.open() catch |err| return lift(err);
        const started = monotonic();
        while (!self.stream.connected) {
            if (subscription.reader.head_done and subscription.reader.status != 200) {
                const message = try std.fmt.allocPrint(arena, "subscribe OpenCode session events: opencode native: HTTP {d}", .{subscription.reader.status});
                return refusal.fail(error.BackendFailed, message);
            }
            if (self.ended or !subscription.open) return refusal.fail(error.BackendFailed, "subscribe OpenCode session events: the event stream ended before server.connected");
            if (monotonic() -| started > config.request_timeout_ns) return refusal.fail(error.BackendFailed, "subscribe OpenCode session events: no server.connected within the request timeout");
            _ = subscription.poll(20) catch |err| return refusal.fail(error.BackendFailed, try describe(arena, "subscribe OpenCode session events", err));
            try self.feed();
        }
        if (request.reasoning_level) |level| try self.switchLevel(arena, level, refusal);
        return self;
    }

    fn attach(self: *Session, bound: []const u8) !native.SessionInfo {
        const own = self.owned();
        const limit = self.owner.config.frame_limit;
        const info = switch (try httpapi.getSessionResult(own, try self.exchange(try httpapi.getSession(own, self.endpoint, bound)), bound, limit)) {
            .ok => |value| value,
            .failed => return error.SessionUnattachable,
        };
        const running = switch (try httpapi.activeResult(own, try self.exchange(try httpapi.active(own, self.endpoint)), limit)) {
            .ok => |value| value,
            .failed => return error.SessionUnattachable,
        };
        for (running) |id| {
            if (std.mem.eql(u8, id, bound)) return error.SessionUnattachable;
        }
        return info;
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
        .native_id = nativeId,
        .state = state,
        .submit = submit,
        .resolve = resolve,
        .cancel = cancel,
        .pump = pump,
        .drain = drain,
        .readable = readable,
        .activity = activity,
        .close = close,
        .models = models,
        .update_settings = updateSettings,
    };

    fn switchLevel(self: *Session, arena: std.mem.Allocator, level: []const u8, refusal: *contract.Refusal) contract.Failure!void {
        const current = self.model orelse return refusal.unsupportedField(contract.feature_session_reasoning, contract.reason_unsatisfiable, "reasoning_level");
        const own = self.owned();
        const next = native.ModelRef{ .id = current.id, .provider_id = current.provider_id, .variant = try own.dupe(u8, level) };
        const native_id = self.reducer.options.native_id;
        const limit = self.owner.config.frame_limit;
        const switched = self.exchange(try httpapi.switchModel(arena, self.endpoint, native_id, next)) catch |err| return refusal.fail(error.BackendFailed, try describe(arena, "switch OpenCode session variant", err));
        if (try httpapi.switchModelResult(arena, switched, native_id, limit)) |failure| return refusal.fail(error.BackendFailed, try std.fmt.allocPrint(arena, "switch OpenCode session variant: {s}", .{failure.message}));
        const read = self.exchange(try httpapi.getSession(own, self.endpoint, native_id)) catch |err| return refusal.fail(error.BackendFailed, try describe(arena, "read OpenCode session", err));
        const info = switch (try httpapi.getSessionResult(own, read, native_id, limit)) {
            .ok => |value| value,
            .failed => |failure| return refusal.fail(error.BackendFailed, try std.fmt.allocPrint(arena, "read OpenCode session: {s}", .{failure.message})),
        };
        const recorded = info.model orelse return refusal.unsupportedField(contract.feature_session_reasoning, contract.reason_unsatisfiable, "reasoning_level");
        if (!std.mem.eql(u8, recorded.id, next.id) or !std.mem.eql(u8, recorded.provider_id, next.provider_id) or !std.mem.eql(u8, recorded.variant, next.variant)) return refusal.unsupportedField(contract.feature_session_reasoning, contract.reason_unsatisfiable, "reasoning_level");
        self.model = recorded;
        self.reported_level = next.variant;
    }

    fn updateSettings(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.SessionSettingsUpdateRequest, refusal: *contract.Refusal) contract.Failure!contract.Updated {
        const self = cast(ptr);
        try contract.refuseUnadvertisedLiveSettings(descriptor, request, refusal);
        if (self.reducer.unusable or self.ended) return error.SessionClosed;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        for ([_]?*session.Run{ self.reducer.active, self.reducer.reserved }) |candidate| {
            const run = candidate orelse continue;
            if (!run.terminal or run.holding) return error.RunActive;
        }
        var response = oap_types.SessionSettingsUpdateResponse{ .session_id = self.id };
        if (request.reasoning_level) |level| {
            const previous = self.reported_level;
            try self.switchLevel(arena, level, refusal);
            response.previous_reasoning_level = previous;
            response.reasoning_level = self.reported_level;
        }
        return .{ .response = response, .state = try state(ptr, arena, refusal) };
    }

    fn readable(ptr: *anyopaque) ?std.Io.File.Handle {
        if (builtin.os.tag == .windows) return null;
        const self = cast(ptr);
        const subscription = self.subscription orelse return null;
        if (!subscription.open) return null;
        return @intCast(compat.net.streamHandle(&subscription.stream));
    }

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

    fn interrupt(context: *anyopaque, arena: std.mem.Allocator, native_session: []const u8) std.mem.Allocator.Error!session.InterruptOutcome {
        const self = cast(context);
        const response = self.exchange(try httpapi.interrupt(arena, self.endpoint, native_session)) catch |err| return .{ .failed = try self.transportFailure(err) };
        return switch (try httpapi.interruptResult(arena, response, native_session, self.owner.config.frame_limit)) {
            .ok => |interrupted| .{ .interrupted = interrupted },
            .failed => |failure| .{ .failed = .{ .message = failure.message, .api = failure.api } },
        };
    }

    fn cancelInbox(context: *anyopaque, arena: std.mem.Allocator, native_session: []const u8, inbox: []const u8) std.mem.Allocator.Error!?session.Failure {
        const self = cast(context);
        const response = self.exchange(try httpapi.cancelInbox(arena, self.endpoint, native_session, inbox)) catch |err| return try self.transportFailure(err);
        const failure = try httpapi.cancelInboxResult(arena, response, native_session, inbox, self.owner.config.frame_limit) orelse return null;
        return .{ .message = failure.message, .api = failure.api };
    }

    fn feed(self: *Session) contract.Failure!void {
        const subscription = self.subscription orelse return;
        if (subscription.reader.head_done and subscription.reader.status != 200) {
            return self.end(try std.fmt.allocPrint(self.owned(), "subscribe OpenCode session events: opencode native: HTTP {d}", .{subscription.reader.status}));
        }
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

    fn idOf(ptr: *anyopaque) []const u8 {
        return cast(ptr).id;
    }

    fn nativeId(ptr: *anyopaque) []const u8 {
        return cast(ptr).native_id;
    }

    fn state(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        _ = refusal;
        const self = cast(ptr);
        if (self.reducer.unusable) return error.SessionClosed;
        const model = self.reducer.options.model;
        const current_model_id: ?[]const u8 = if (model.len > 0) try arena.dupe(u8, model) else null;
        var entries = std.ArrayList(oap_types.ActiveRun).empty;
        var started: ?[]const u8 = null;
        var position: u64 = 0;
        for ([_]?*session.Run{ self.reducer.active, self.reducer.reserved }) |candidate| {
            const run = candidate orelse continue;
            if (run.terminal and !run.holding) continue;
            const reservation = run.queued_admission and !run.start_published;
            if (!reservation) started = run.id else position += 1;
            try entries.append(arena, .{
                .run_id = try arena.dupe(u8, run.id),
                .status = if (reservation) .queued else runStatus(run.status),
                .relationship = "primary",
                .queue_position = if (reservation) position else null,
                .as_of_sequence = if (reservation) run.published_seq else run.next - 1,
            });
        }
        const settled = try arena.alloc(oap_types.RunPosition, self.reducer.settled.items.len);
        for (self.reducer.settled.items, settled) |entry, *slot| slot.* = .{ .run_id = try arena.dupe(u8, entry.run_id), .sequence = entry.sequence };
        const active_run_id: ?[]const u8 = if (started) |id| try arena.dupe(u8, id) else null;
        const transcript_cursor: ?[]const u8 = if (self.reducer.last_seq > 0) try std.fmt.allocPrint(arena, "{d}", .{self.reducer.last_seq}) else null;
        return .{
            .session_id = self.id,
            .status = if (started != null) .running else if (entries.items.len > 0) .queued else .idle,
            .active_run_id = active_run_id,
            .active_runs = entries.items,
            .current_model_id = current_model_id,
            .transcript_cursor = transcript_cursor,
            .updated_at_ms = wallClock(),
            .as_of = if (settled.len > 0) .{ .settled = settled } else null,
            .reasoning_level = if (self.reported_level) |level| try arena.dupe(u8, level) else null,
            .recovered = self.recovered,
            .recovery_reason = if (self.recovered) reopen_recovery_reason else null,
        };
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, envelope_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        _ = envelope_id;
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

    fn models(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ModelsRequest, refusal: *contract.Refusal) contract.Failure!contract.Catalog {
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
            .revision = capability_revision,
            .response = .{
                .session_id = self.id,
                .current_model_id = if (current) |value| try arena.dupe(u8, value.string) else null,
                .models = out,
            },
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

    fn close(ptr: *anyopaque, force: bool) contract.Failure!void {
        _ = force;
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
    refuse_events: bool = false,
    busy_polls: usize = 0,
    prompts: std.atomic.Value(usize) = .init(0),
    interrupts: std.atomic.Value(usize) = .init(0),
    cancels: std.atomic.Value(usize) = .init(0),
    actives: std.atomic.Value(usize) = .init(0),
    switches: std.atomic.Value(usize) = .init(0),
    keep_variant: bool = false,
    variant: [32]u8 = undefined,
    variant_len: usize = 0,
    failure: ?anyerror = null,
    missing_record: bool = false,
    event_target: [256]u8 = undefined,
    event_target_len: usize = 0,
    list_target: [256]u8 = undefined,
    list_target_len: usize = 0,

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
                if (std.mem.eql(u8, target, "/api/event")) {
                    @memcpy(self.event_target[0..target.len], target);
                    self.event_target_len = target.len;
                    if (self.refuse_events) {
                        try respond(conn, "500 Internal Server Error", "{}");
                        continue;
                    }
                    try conn.stream.writeAll("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\ndata: {\"id\":\"evt_connected\",\"type\":\"server.connected\",\"data\":{}}\n\n");
                    sse = conn;
                    continue;
                }
                if (std.mem.eql(u8, method, "GET") and std.mem.startsWith(u8, target, "/api/session?")) {
                    @memcpy(self.list_target[0..target.len], target);
                    self.list_target_len = target.len;
                    try respond(conn, "200 OK", "{\"data\":[{\"id\":\"" ++ fake_session ++ "\",\"projectID\":\"prj_fake\",\"title\":\"busy one\",\"time\":{\"created\":1,\"updated\":9},\"location\":{\"directory\":\"/w\"}},{\"id\":\"ses_idle0000000000000000\",\"projectID\":\"prj_fake\",\"title\":\"\",\"time\":{\"created\":1,\"updated\":4},\"location\":{\"directory\":\"/w\"}}],\"cursor\":{}}");
                    continue;
                }
                if (std.mem.eql(u8, method, "POST") and std.mem.eql(u8, target, "/api/session")) {
                    try respond(conn, "200 OK", "{\"data\":{\"id\":\"" ++ fake_session ++ "\",\"projectID\":\"prj_fake\",\"model\":{\"id\":\"fixture\",\"providerID\":\"fixture\"},\"time\":{\"created\":1,\"updated\":1},\"location\":{\"directory\":\"/w\"}}}");
                } else if (std.mem.endsWith(u8, target, "/prompt")) {
                    const turn = self.prompts.fetchAdd(1, .acq_rel);
                    if (self.refuse_prompts) {
                        try respond(conn, "409 Conflict", "{\"_tag\":\"ConflictError\",\"message\":\"busy\"}");
                        continue;
                    }
                    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, arena, body, .{});
                    const message_id = parsed.object.get("id").?.string;
                    const delivery = parsed.object.get("delivery").?.string;
                    const text = parsed.object.get("text").?.string;
                    const receipt = try std.fmt.allocPrint(arena, "{{\"data\":{{\"id\":\"{s}\",\"sessionID\":\"" ++ fake_session ++ "\",\"time\":{{\"created\":1}},\"type\":\"user\",\"payload\":{{\"text\":\"{s}\"}},\"delivery\":\"{s}\"}}}}", .{ message_id, text, delivery });
                    try respond(conn, "200 OK", receipt);
                    if (turn < self.turns.len) try emit(arena, sse, self.turns[turn], message_id, &seq);
                } else if (std.mem.eql(u8, method, "POST") and std.mem.endsWith(u8, target, "/model")) {
                    _ = self.switches.fetchAdd(1, .acq_rel);
                    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, arena, body, .{});
                    const variant = parsed.object.get("model").?.object.get("variant").?.string;
                    if (!self.keep_variant) {
                        @memcpy(self.variant[0..variant.len], variant);
                        self.variant_len = variant.len;
                    }
                    try conn.stream.writeAll("HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n");
                    conn.open = false;
                    conn.stream.close();
                } else if (std.mem.eql(u8, method, "GET") and std.mem.eql(u8, target, "/api/session/" ++ fake_session) and self.missing_record) {
                    try respond(conn, "404 Not Found", "{\"_tag\":\"SessionNotFoundError\",\"sessionID\":\"" ++ fake_session ++ "\",\"message\":\"Session not found\"}");
                } else if (std.mem.eql(u8, method, "GET") and std.mem.eql(u8, target, "/api/session/" ++ fake_session)) {
                    const record = try std.fmt.allocPrint(arena, "{{\"data\":{{\"id\":\"" ++ fake_session ++ "\",\"projectID\":\"prj_fake\",\"model\":{{\"id\":\"fixture\",\"providerID\":\"fixture\",\"variant\":\"{s}\"}},\"time\":{{\"created\":1,\"updated\":2}}}}}}", .{self.variant[0..self.variant_len]});
                    try respond(conn, "200 OK", record);
                } else if (std.mem.endsWith(u8, target, "/interrupt")) {
                    _ = self.interrupts.fetchAdd(1, .acq_rel);
                    try respond(conn, "200 OK", "{\"interrupted\":true}");
                    try emit(arena, sse, self.after_interrupt, "", &seq);
                } else if (std.mem.eql(u8, method, "DELETE") and std.mem.indexOf(u8, target, "/inbox/") != null) {
                    _ = self.cancels.fetchAdd(1, .acq_rel);
                    try conn.stream.writeAll("HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n");
                    conn.open = false;
                    conn.stream.close();
                } else if (std.mem.eql(u8, target, "/api/session/active")) {
                    const polled = self.actives.fetchAdd(1, .acq_rel);
                    if (polled < self.busy_polls) {
                        try respond(conn, "200 OK", "{\"data\":{\"" ++ fake_session ++ "\":{\"type\":\"running\"}}}");
                    } else {
                        try respond(conn, "200 OK", "{\"data\":{}}");
                    }
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
    return "{\"id\":\"evt_%SEQ%\",\"created\":%SEQ%,\"type\":\"session." ++ kind ++ "\",\"location\":{\"directory\":\"/w\"},\"durable\":{\"aggregateID\":\"" ++ fake_session ++ "\",\"seq\":%SEQ%,\"version\":1},\"data\":{\"sessionID\":\"" ++ fake_session ++ "\"" ++ data ++ "}}";
}

pub const delivered = fakeEvent("inbox.delivered", ",\"inboxID\":\"%MSG%\"");
pub const step_started = fakeEvent("step.started", ",\"assistantMessageID\":\"msg_a1\",\"agent\":\"build\",\"model\":{\"id\":\"fixture\",\"providerID\":\"fixture\"},\"started\":1");
pub const text_ended = fakeEvent("text.ended", ",\"assistantMessageID\":\"msg_a1\",\"ordinal\":0,\"text\":\"done\"");
pub const step_ended = fakeEvent("step.ended", ",\"assistantMessageID\":\"msg_a1\",\"finish\":\"stop\",\"cost\":0,\"tokens\":{\"input\":2,\"output\":5,\"reasoning\":0,\"cache\":{\"read\":0,\"write\":0}}");
pub const step_aborted = fakeEvent("step.failed", ",\"assistantMessageID\":\"msg_a1\",\"error\":{\"type\":\"aborted\",\"message\":\"Step interrupted\"}");
pub const execution_succeeded = fakeEvent("execution.succeeded", "");
pub const execution_interrupted = fakeEvent("execution.interrupted", ",\"reason\":\"user\"");

pub const text_turn = [_][]const u8{ delivered, step_started, text_ended, step_ended, execution_succeeded };
pub const open_turn = [_][]const u8{ delivered, step_started };
pub const interrupted_turn = [_][]const u8{ step_aborted, execution_interrupted };

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
        if (self.handle) |opened| opened.teardown();
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
        return self.handle.?.submit(self.arena.allocator(), &request, "", refusal);
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

test "the descriptor serves resume and replay from the endpoint journal under the Go adapter's revision" {
    try testing.expectEqualStrings(session.capability_revision, capability_revision);
    try testing.expectEqual(oap_types.SupportLevel.degraded, descriptor.level("run.replay"));
    try testing.expectEqual(oap_types.SupportLevel.degraded, descriptor.level("run.resume"));
    for (features[1..], features[0 .. features.len - 1]) |later, earlier| try testing.expect(std.mem.lessThan(u8, earlier.key, later.key));
}

test "a steered turn is admitted started, streams its text, and settles when the execution succeeds" {
    var probe: Probe = undefined;
    try probe.init(&.{&text_turn}, &.{});
    defer probe.deinit();
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
    try testing.expectEqual(@as(usize, 0), probe.fake.actives.load(.acquire));
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
}

test "a cancel interrupts the server and the run settles cancelled when the execution is interrupted" {
    var probe: Probe = undefined;
    try probe.init(&.{&open_turn}, &interrupted_turn);
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

test "a cancel before the input is delivered deletes it from the session inbox instead of interrupting" {
    var probe: Probe = undefined;
    try probe.init(&.{}, &.{});
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit(.auto, &refusal);
    const cancelled = try probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expect(cancelled.accepted);
    try testing.expectEqual(@as(usize, 1), probe.fake.cancels.load(.acquire));
    try testing.expectEqual(@as(usize, 0), probe.fake.interrupts.load(.acquire));
}

test "a live level switches the session to its model with the new variant and reports what the record confirms" {
    var probe: Probe = undefined;
    try probe.init(&.{}, &.{});
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.open(&refusal);
    const updater = opened.vtable.update_settings.?;
    const updated = try updater(opened.ptr, probe.arena.allocator(), &.{ .session_id = "s1", .reasoning_level = "high" }, &refusal);
    try testing.expectEqualStrings("high", updated.response.reasoning_level.?);
    try testing.expect(updated.response.previous_reasoning_level == null);
    try testing.expectEqualStrings("high", updated.state.reasoning_level.?);
    try testing.expectEqual(@as(usize, 1), probe.fake.switches.load(.acquire));
    try testing.expectEqualStrings("high", (try opened.state(probe.arena.allocator(), &refusal)).reasoning_level.?);
}

test "a live update is refused for what OpenCode cannot take between runs" {
    var probe: Probe = undefined;
    try probe.init(&.{&open_turn}, &.{});
    defer probe.deinit();
    probe.fake.keep_variant = true;
    var refusal = contract.Refusal{};
    const opened = try probe.open(&refusal);
    const updater = opened.vtable.update_settings.?;
    try testing.expectError(error.UnsupportedFeature, updater(opened.ptr, probe.arena.allocator(), &.{ .session_id = "s1", .reasoning_level = "max" }, &refusal));
    try testing.expectEqualStrings(contract.feature_session_reasoning, refusal.feature);
    try testing.expectEqualStrings(contract.reason_unsatisfiable, refusal.reason);
    try testing.expect((try opened.state(probe.arena.allocator(), &refusal)).reasoning_level == null);
    refusal = .{};
    try testing.expectError(error.UnsupportedFeature, updater(opened.ptr, probe.arena.allocator(), &.{ .session_id = "s1", .compaction_policy_json = "{\"kind\":\"off\"}" }, &refusal));
    try testing.expectEqualStrings(contract.feature_compaction_policy, refusal.feature);
    try testing.expectEqualStrings(contract.reason_unadvertised, refusal.reason);
    try testing.expectError(error.RunNotFound, updater(opened.ptr, probe.arena.allocator(), &.{ .session_id = "other", .reasoning_level = "high" }, &refusal));
    _ = try probe.submit(.auto, &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.started", &seen);
    try testing.expectError(error.RunActive, updater(opened.ptr, probe.arena.allocator(), &.{ .session_id = "s1", .reasoning_level = "high" }, &refusal));
    try testing.expectEqual(@as(usize, 1), probe.fake.switches.load(.acquire));
}

test "an event stream the server refuses refuses the open" {
    var probe: Probe = undefined;
    try probe.init(&.{}, &.{});
    defer probe.deinit();
    probe.fake.refuse_events = true;
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, probe.open(&refusal));
    try testing.expectEqualStrings("subscribe OpenCode session events: opencode native: HTTP 500", refusal.message);
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
    try testing.expectEqualStrings("fixture/fixture", catalog.response.current_model_id.?);
    try testing.expectEqualStrings("fixture/fixture", catalog.response.models[0].id);
    try testing.expect(catalog.response.models[0].default);
    try testing.expectEqualStrings(capability_revision, catalog.revision);
}

test "a submission carrying a degraded-feature consent list is admitted, as Go's is" {
    var probe: Probe = undefined;
    try probe.init(&.{&text_turn}, &.{});
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const messages = try probe.arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = "hello" } }});
    const request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = messages, .delivery = .auto, .allow_degraded_features = &.{"run.streaming"} };
    const admitted = try probe.handle.?.submit(probe.arena.allocator(), &request, "", &refusal);
    try testing.expect(admitted.accepted);
}

fn openReopen(probe: *Probe, binding: []const u8, refusal: *contract.Refusal) !contract.Session {
    const opened = try probe.adapter.adapter().vtable.open(&probe.adapter, probe.arena.allocator(), .{ .session_id = "s1", .participant = "user", .reopen = true, .native_session_id = binding }, refusal);
    probe.handle = opened;
    return opened;
}

test "a reopen attaches to the bound server session and follows its events without creating one" {
    var probe: Probe = undefined;
    try probe.init(&.{}, &.{});
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try openReopen(&probe, fake_session, &refusal);
    const state_value = try opened.state(probe.arena.allocator(), &refusal);
    try testing.expect(state_value.recovered);
    try testing.expectEqualStrings(reopen_recovery_reason, state_value.recovery_reason.?);
    try testing.expect(state_value.status == .idle and state_value.active_run_id == null);
    try testing.expectEqualStrings(fake_session, opened.nativeId());
    try testing.expectEqualStrings("/api/event", probe.fake.event_target[0..probe.fake.event_target_len]);
    try testing.expectEqual(@as(usize, 0), probe.fake.prompts.load(.acquire));
}

test "a reopen carrying a level switches the recorded model's variant once attached" {
    var probe: Probe = undefined;
    try probe.init(&.{}, &.{});
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.adapter.adapter().vtable.open(&probe.adapter, probe.arena.allocator(), .{ .session_id = "s1", .participant = "user", .reopen = true, .native_session_id = fake_session, .reasoning_level = "high" }, &refusal);
    probe.handle = opened;
    try testing.expectEqual(@as(usize, 1), probe.fake.switches.load(.acquire));
    try testing.expectEqualStrings("high", (try opened.state(probe.arena.allocator(), &refusal)).reasoning_level.?);
}

test "a reopen refuses a level the session record does not confirm" {
    var probe: Probe = undefined;
    try probe.init(&.{}, &.{});
    defer probe.deinit();
    probe.fake.keep_variant = true;
    var refusal = contract.Refusal{};
    try testing.expectError(error.UnsupportedFeature, probe.adapter.adapter().vtable.open(&probe.adapter, probe.arena.allocator(), .{ .session_id = "s1", .participant = "user", .reopen = true, .native_session_id = fake_session, .reasoning_level = "max" }, &refusal));
    try testing.expectEqualStrings(contract.feature_session_reasoning, refusal.feature);
}

test "a fresh open still refuses a level" {
    var probe: Probe = undefined;
    try probe.init(&.{}, &.{});
    defer probe.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.UnsupportedFeature, probe.adapter.adapter().vtable.open(&probe.adapter, probe.arena.allocator(), .{ .session_id = "s1", .participant = "user", .reasoning_level = "high" }, &refusal));
    try testing.expectEqual(@as(usize, 0), probe.fake.switches.load(.acquire));
}

test "a fresh open binds the session the server created and follows the global event stream" {
    var probe: Probe = undefined;
    try probe.init(&.{}, &.{});
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.open(&refusal);
    try testing.expectEqualStrings(fake_session, opened.nativeId());
    try testing.expectEqualStrings("/api/event", probe.fake.event_target[0..probe.fake.event_target_len]);
}

test "a reopen refuses a session it cannot attach" {
    for ([_]enum { blank, missing, running }{ .blank, .missing, .running }) |case| {
        var probe: Probe = undefined;
        try probe.init(&.{}, &.{});
        defer probe.deinit();
        probe.fake.missing_record = case == .missing;
        probe.fake.busy_polls = if (case == .running) 1 else 0;
        var refusal = contract.Refusal{};
        try testing.expectError(error.UnsupportedFeature, openReopen(&probe, if (case == .blank) " " else fake_session, &refusal));
        try testing.expectEqualStrings(contract.feature_open_reopen, refusal.feature);
        try testing.expectEqualStrings(contract.reason_unsatisfiable, refusal.reason);
        try testing.expectEqual(@as(usize, 0), probe.fake.event_target_len);
    }
}

test "the native list asks OpenCode for the directory's root sessions newest first and marks the ones it runs" {
    var probe: Probe = undefined;
    try probe.init(&.{}, &.{});
    defer probe.deinit();
    probe.fake.busy_polls = 1;
    var refusal = contract.Refusal{};
    const listed = try probe.adapter.adapter().nativeList(probe.arena.allocator(), .{ .directory = "/w ork", .limit = 7 }, &refusal).?;
    try testing.expectEqualStrings("/api/session?directory=%2Fw+ork&limit=7&order=desc&parentID=null", probe.fake.list_target[0..probe.fake.list_target_len]);
    try testing.expectEqual(@as(usize, 2), listed.len);
    try testing.expectEqualStrings(fake_session, listed[0].native_id);
    try testing.expectEqualStrings("busy one", listed[0].title);
    try testing.expectEqualStrings("/w", listed[0].directory);
    try testing.expectEqual(@as(i64, 9), listed[0].updated_at_ms);
    try testing.expect(listed[0].running);
    try testing.expectEqualStrings("ses_idle0000000000000000", listed[1].native_id);
    try testing.expect(!listed[1].running);
}
