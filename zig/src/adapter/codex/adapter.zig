const std = @import("std");
const harness_pins = @import("harness_pins");
const builtin = @import("builtin");
const contract = @import("contract");
const oap_types = @import("oap_types");
const process = @import("process");
const session = @import("session");
const native = @import("native");
const rpc = @import("rpc");
const compat = @import("compat");
const json_encode = @import("json_encode");

pub const bridge = @import("bridge.zig");
pub const endpoint_id = session.endpoint_id;
pub const capability_revision = session.capability_revision;
const app_server_args = [_][]const u8{ "app-server", "--listen", "stdio://" };

const features = table: {
    @setEvalBranchQuota(20000);
    var built: [session.features.len]contract.Feature = undefined;
    for (session.features, &built) |feature, *slot| {
        slot.* = .{
            .key = feature.name,
            .level = std.meta.stringToEnum(oap_types.SupportLevel, feature.level).?,
            .reason = if (feature.reason.len > 0) feature.reason else null,
            .scope = if (feature.scope.len > 0) feature.scope else null,
            .modes = feature.modes,
        };
    }
    const done = built;
    break :table done;
};

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = session.endpoint_name, .version = session.codex_commit, .adapter = session.adapter_name },
    .capability_revision = capability_revision,
    .features = &features,
};

pub const default_compact_above: usize = 256 * 1024;

pub const Config = struct {
    executable: []const u8,
    args: []const []const u8 = &.{},
    environment: []const []const u8 = &.{},
    working_directory: ?[]const u8 = null,
    model: []const u8 = "",
    approval_policy: []const u8 = "",
    sandbox: []const u8 = "",
    control_socket: []const u8 = "",
    frame_limit: usize = rpc.frame_limit_default,
    exit_grace_ns: u64 = process.default_exit_grace_ns,
    request_timeout_ns: u64 = 60 * std.time.ns_per_s,
    poll_ns: u64 = 5 * std.time.ns_per_ms,
};

fn serverArgs(arena: std.mem.Allocator, config: Config) ![]const []const u8 {
    if (config.control_socket.len == 0) return &app_server_args;
    return try arena.dupe([]const u8, &.{ "codex-bridge", "--sock", config.control_socket });
}

test {
    _ = bridge;
}

test "a control socket swaps the app-server for the bridge" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const own = try serverArgs(arena.allocator(), .{ .executable = "codex" });
    try std.testing.expectEqualStrings("app-server", own[0]);
    const shared = try serverArgs(arena.allocator(), .{ .executable = "oapx", .control_socket = "/tmp/codex.sock" });
    try std.testing.expectEqual(@as(usize, 3), shared.len);
    try std.testing.expectEqualStrings("codex-bridge", shared[0]);
    try std.testing.expectEqualStrings("/tmp/codex.sock", shared[2]);
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
    ids: usize = 0,

    pub fn init(allocator: std.mem.Allocator, config: Config) Adapter {
        return .{ .allocator = allocator, .config = config };
    }

    pub fn adapter(self: *Adapter) contract.Adapter {
        return .{ .ptr = self, .vtable = &.{ .probe = probe, .open = open, .native_list = nativeList, .native_link = nativeLink } };
    }

    fn nativeList(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.NativeListRequest, refusal: *contract.Refusal) contract.Failure![]const contract.NativeSession {
        const self: *Adapter = @ptrCast(@alignCast(ptr));
        const config = self.config;
        const argv = try std.mem.concat(arena, []const u8, &.{ config.args, try serverArgs(arena, config) });
        const transport = process.Transport.open(self.allocator, .{
            .executable = config.executable,
            .args = argv,
            .environment = config.environment,
            .working_directory = config.working_directory,
            .frame_limit = config.frame_limit,
            .exit_grace_ns = config.exit_grace_ns,
        }) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            return refusal.fail(error.BackendFailed, try std.fmt.allocPrint(arena, "the codex app-server could not start: {s}", .{@errorName(err)}));
        };
        defer transport.deinit();
        const directory = if (request.directory.len > 0) request.directory else config.working_directory orelse "";
        var params = std.json.ObjectMap.empty;
        try params.put(arena, "limit", .{ .integer = @intCast(request.limit) });
        if (directory.len > 0) try params.put(arena, "cwd", .{ .string = directory });
        const listing = try std.json.Stringify.valueAlloc(arena, std.json.Value{ .object = params }, .{});
        const lines = [_][]const u8{
            "{\"jsonrpc\":\"2.0\",\"id\":0,\"method\":\"initialize\",\"params\":{\"clientInfo\":{\"name\":\"oapx\",\"version\":\"0\"}}}",
            "{\"jsonrpc\":\"2.0\",\"method\":\"initialized\",\"params\":{}}",
            try std.fmt.allocPrint(arena, "{{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"thread/list\",\"params\":{s}}}", .{listing}),
        };
        for (lines) |line| transport.write(line) catch return refusal.fail(error.BackendFailed, "the codex app-server closed before answering thread/list");
        const started = monotonic();
        while (monotonic() -| started < config.request_timeout_ns) {
            const polled = transport.poll(config.poll_ns) catch return refusal.fail(error.BackendFailed, "the codex app-server's answer could not be read");
            const frame = switch (polled) {
                .frame => |frame| frame,
                .quiet => continue,
                .ended => return refusal.fail(error.BackendFailed, "the codex app-server exited before answering thread/list"),
            };
            const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, frame, .{}) catch continue;
            if (parsed != .object) continue;
            const id = parsed.object.get("id") orelse continue;
            if (id != .integer or id.integer != 1) continue;
            if (parsed.object.get("error")) |failure| {
                const text = if (failure == .object) (if (failure.object.get("message")) |m| (if (m == .string) m.string else "") else "") else "";
                return refusal.fail(error.BackendFailed, try std.fmt.allocPrint(arena, "codex thread/list refused: {s}", .{text}));
            }
            return threadsOf(arena, parsed.object.get("result"));
        }
        return refusal.fail(error.BackendFailed, "the codex app-server did not answer thread/list in time");
    }

    fn nativeLink(ptr: *anyopaque, arena: std.mem.Allocator, native_id: []const u8) std.mem.Allocator.Error![]const u8 {
        _ = ptr;
        return threadLink(arena, native_id);
    }

    fn threadLink(arena: std.mem.Allocator, native_id: []const u8) std.mem.Allocator.Error![]const u8 {
        return std.fmt.allocPrint(arena, "codex://threads/{s}", .{native_id});
    }

    fn threadsOf(arena: std.mem.Allocator, result: ?std.json.Value) ![]const contract.NativeSession {
        const body = result orelse return &.{};
        if (body != .object) return &.{};
        const data = body.object.get("data") orelse return &.{};
        if (data != .array) return &.{};
        var listed: std.ArrayList(contract.NativeSession) = .empty;
        for (data.array.items) |thread| {
            if (thread != .object) continue;
            const id = textOf(thread, "id");
            if (id.len == 0) continue;
            const name = textOf(thread, "name");
            const preview = textOf(thread, "preview");
            const first_line = preview[0 .. std.mem.indexOfScalar(u8, preview, '\n') orelse preview.len];
            const status = thread.object.get("status");
            const kind = if (status) |given| (if (given == .object) textOf(given, "type") else "") else "";
            const updated = thread.object.get("updatedAt");
            try listed.append(arena, .{
                .native_id = id,
                .title = if (name.len > 0) name else first_line[0..@min(first_line.len, 120)],
                .directory = textOf(thread, "cwd"),
                .updated_at_ms = if (updated) |seconds| (if (seconds == .integer) seconds.integer * std.time.ms_per_s else 0) else 0,
                .running = std.mem.eql(u8, kind, "active"),
                .link = try threadLink(arena, id),
            });
        }
        return listed.items;
    }

    fn textOf(value: std.json.Value, name: []const u8) []const u8 {
        if (value != .object) return "";
        const member = value.object.get(name) orelse return "";
        return if (member == .string) member.string else "";
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
    participant: []u8,
    reducer_arena: *std.heap.ArenaAllocator,
    transport: *process.Transport,
    reducer: session.Reducer,
    ended: bool = false,
    reaped: bool = false,
    pending_effort: []const u8 = "",
    compact_above: usize = default_compact_above,
    retained: usize = 0,

    fn open(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        if (request.reopen and request.native_session_id.len == 0) return refusal.unsupported(contract.feature_open_reopen, contract.reason_unsatisfiable);
        const self = try construct(owner, arena, request, refusal);
        errdefer self.destroy();
        try self.handshake(arena, refusal);
        self.reducer.open() catch |err| return lift(err);
        try self.flush();
        switch (try self.awaitSettled(arena, native.method_thread_start, refusal)) {
            .opened => return self,
            .refused => |refused| {
                if (request.reopen) {
                    refusal.* = .{ .feature = contract.feature_open_reopen, .reason = contract.reason_unsatisfiable, .detail = try describe(arena, refused) };
                    return error.UnsupportedFeature;
                }
                return refusal.fail(error.BackendFailed, try describe(arena, refused));
            },
            else => return refusal.fail(error.BackendFailed, "the codex app-server answered thread/start with an unrelated settlement"),
        }
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
        const resume_thread_id: []const u8 = if (request.reopen) try reducer_arena.allocator().dupe(u8, request.native_session_id) else "";
        const settings = try codexSettings(arena, request, refusal);
        const argv = try std.mem.concat(arena, []const u8, &.{ config.args, try serverArgs(arena, config) });
        const transport = process.Transport.open(gpa, .{
            .executable = config.executable,
            .args = argv,
            .environment = config.environment,
            .working_directory = config.working_directory,
            .frame_limit = config.frame_limit,
            .exit_grace_ns = config.exit_grace_ns,
        }) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            const message = try std.fmt.allocPrint(arena, "the codex app-server could not start: {s}", .{@errorName(err)});
            return refusal.fail(error.BackendFailed, message);
        };
        self.* = .{
            .owner = owner,
            .gpa = gpa,
            .id = id,
            .participant = participant,
            .reducer_arena = reducer_arena,
            .transport = transport,
            .reducer = session.Reducer.init(reducer_arena, .{
                .session_id = id,
                .participant = participant,
                .model = config.model,
                .working_directory = config.working_directory orelse "",
                .approval_policy = config.approval_policy,
                .sandbox = config.sandbox,
                .resume_thread_id = resume_thread_id,
                .reopen = request.reopen,
                .settings = settings.native,
                .compaction_auto = settings.auto,
                .revision = capability_revision,
                .counter = &owner.ids,
                .now_ms = wallClock,
            }),
        };
        return self;
    }

    const Chosen = struct { native: native.Settings = .{}, auto: bool = false };

    fn codexSettings(arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!Chosen {
        var chosen = Chosen{};
        if (request.reasoning_level) |level| {
            chosen.native.reasoning_effort = effortFor(level) orelse return refusal.unsupportedField(contract.feature_session_reasoning, contract.reason_unsatisfiable, "reasoning_level");
        }
        const raw = request.compaction_policy_json orelse return chosen;
        const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, raw, .{}) catch return refusal.unsupportedField(contract.feature_compaction_policy, contract.reason_unsatisfiable, "compaction_policy");
        const kind = if (parsed == .object) (if (parsed.object.get("kind")) |value| (if (value == .string) value.string else "") else "") else "";
        if (std.mem.eql(u8, kind, "auto")) {
            chosen.auto = true;
            return chosen;
        }
        if (std.mem.eql(u8, kind, "tokens")) {
            const tokens = parsed.object.get("tokens") orelse return refusal.unsupportedField(contract.feature_compaction_policy, contract.reason_unsatisfiable, "compaction_policy");
            if (tokens != .integer) return refusal.unsupportedField(contract.feature_compaction_policy, contract.reason_unsatisfiable, "compaction_policy");
            chosen.native.auto_compact_token_limit = tokens.integer;
            return chosen;
        }
        return refusal.unsupportedField(contract.feature_compaction_policy, contract.reason_unsatisfiable, "compaction_policy");
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
        .readable = readable,
        .activity = activity,
        .close = close,
        .native_id = nativeId,
        .update_settings = updateSettings,
    };

    fn updateSettings(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.SessionSettingsUpdateRequest, refusal: *contract.Refusal) contract.Failure!contract.Updated {
        const self = cast(ptr);
        try contract.refuseUnadvertisedLiveSettings(descriptor, request, refusal);
        if (self.reducer.closed or self.reducer.transport_closed) return error.SessionClosed;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        if (self.reducer.active != null) return error.RunActive;
        var response = oap_types.SessionSettingsUpdateResponse{ .session_id = self.id };
        if (request.reasoning_level) |level| {
            const effort = effortFor(level) orelse return refusal.unsupportedField(contract.feature_session_reasoning, contract.reason_unsatisfiable, "reasoning_level");
            response.previous_reasoning_level = reportedLevel(self.reducer.options.settings.reasoning_effort);
            self.reducer.options.settings.reasoning_effort = effort;
            self.pending_effort = effort;
            response.reasoning_level = reportedLevel(effort);
        }
        self.reducer.state.updated_at_ms = self.reducer.now();
        return .{ .response = response, .state = try state(ptr, arena, refusal) };
    }

    fn nativeId(ptr: *anyopaque) []const u8 {
        return cast(ptr).reducer.thread_id;
    }

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

    fn fail(self: *Session, detail: []const u8) contract.Failure!void {
        if (self.ended) return;
        self.ended = true;
        self.reap();
        self.reducer.transportFailed(detail) catch |err| return lift(err);
    }

    fn write(self: *Session, arena: std.mem.Allocator, frame: []const u8, what: []const u8, refusal: *contract.Refusal) contract.Failure!void {
        self.transport.write(frame) catch |err| {
            self.reap();
            const departed = self.transport.departed().departure != .running;
            try self.fail(@errorName(err));
            const message = if (departed)
                try std.fmt.allocPrint(arena, "the codex app-server exited before answering {s}", .{what})
            else
                try std.fmt.allocPrint(arena, "the codex app-server did not take {s}: {s}", .{ what, @errorName(err) });
            return refusal.fail(error.BackendFailed, message);
        };
    }

    fn flush(self: *Session) contract.Failure!void {
        var index: usize = 0;
        while (index < self.reducer.writes.items.len and !self.ended) : (index += 1) {
            self.transport.write(self.reducer.writes.items[index]) catch |err| try self.fail(@errorName(err));
        }
        self.reducer.writes.clearRetainingCapacity();
    }

    fn receive(self: *Session, wait_ns: u64) contract.Failure!?rpc.Message {
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
                return rpc.parseMessage(self.owned(), held) catch |err| {
                    if (err == error.OutOfMemory) return error.OutOfMemory;
                    const detail = try std.fmt.allocPrint(self.owned(), "codex app-server rpc: {s}", .{@errorName(err)});
                    try self.fail(detail);
                    return null;
                };
            },
        }
    }

    fn step(self: *Session, wait_ns: u64) contract.Failure!bool {
        const was_ended = self.ended;
        const message = try self.receive(wait_ns) orelse return self.ended != was_ended;
        self.reducer.observe(message) catch |err| return lift(err);
        try self.flush();
        return true;
    }

    fn handshake(self: *Session, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!void {
        const params = try native.initializeParams(self.owned(), session.client_name, session.protocol_version);
        const request = rpc.encode(self.owned(), .{ .request = .{ .id = 0, .method = native.method_initialize, .params = params } }) catch |err| return lift(err);
        try self.write(arena, request, native.method_initialize, refusal);
        const started = monotonic();
        while (true) {
            if (self.ended) return refusal.fail(error.BackendFailed, "the codex app-server exited before answering initialize");
            if (monotonic() -| started > self.owner.config.request_timeout_ns) {
                try self.fail("the codex app-server did not answer initialize in time");
                const message = try std.fmt.allocPrint(arena, "the codex app-server did not answer initialize within {d} ms", .{self.owner.config.request_timeout_ns / std.time.ns_per_ms});
                return refusal.fail(error.BackendFailed, message);
            }
            const message = try self.receive(self.owner.config.poll_ns) orelse continue;
            const answers_initialize = (message.kind == .response or message.kind == .failure) and message.id != null and message.id.?.eql(.{ .integer = 0 });
            if (!answers_initialize) {
                self.reducer.observe(message) catch |err| return lift(err);
                continue;
            }
            if (message.failure) |failure| {
                const detail = try std.fmt.allocPrint(arena, "codex rpc error for initialize: {s}", .{failure.message});
                return refusal.fail(error.BackendFailed, detail);
            }
            if (!native.decodes(message.result.?, native.initialize_response)) return refusal.fail(error.BackendFailed, "the codex app-server answered initialize with a response that does not decode into the pinned native type");
            break;
        }
        const initialized = rpc.encode(self.owned(), .{ .notification = .{ .method = native.method_initialized } }) catch |err| return lift(err);
        try self.write(arena, initialized, native.method_initialized, refusal);
    }

    fn awaitSettled(self: *Session, arena: std.mem.Allocator, method: []const u8, refusal: *contract.Refusal) contract.Failure!session.Settled {
        const started = monotonic();
        while (self.reducer.settled.items.len == 0) {
            if (self.ended) {
                const message = try std.fmt.allocPrint(arena, "the codex app-server exited before answering {s}", .{method});
                return refusal.fail(error.BackendFailed, message);
            }
            if (monotonic() -| started > self.owner.config.request_timeout_ns) {
                try self.fail("the codex app-server stopped answering");
                self.reducer.settled.clearRetainingCapacity();
                const message = try std.fmt.allocPrint(arena, "the codex app-server did not answer {s} within {d} ms", .{ method, self.owner.config.request_timeout_ns / std.time.ns_per_ms });
                return refusal.fail(error.BackendFailed, message);
            }
            _ = try self.step(self.owner.config.poll_ns);
        }
        return self.reducer.settled.orderedRemove(0);
    }

    fn idOf(ptr: *anyopaque) []const u8 {
        return cast(ptr).id;
    }

    fn state(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        _ = refusal;
        const self = cast(ptr);
        if (self.reducer.closed or self.reducer.transport_closed) return error.SessionClosed;
        const current = self.reducer.state;
        const active_run_id: ?[]const u8 = if (current.active_run_id.len > 0) try arena.dupe(u8, current.active_run_id) else null;
        const current_model_id: ?[]const u8 = if (current.current_model_id.len > 0) try arena.dupe(u8, current.current_model_id) else null;
        const transcript_cursor: ?[]const u8 = if (current.transcript_cursor.len > 0) try arena.dupe(u8, current.transcript_cursor) else null;
        return .{
            .session_id = self.id,
            .status = std.meta.stringToEnum(oap_types.SessionStatus, current.status) orelse .running,
            .active_run_id = active_run_id,
            .current_model_id = current_model_id,
            .transcript_cursor = transcript_cursor,
            .updated_at_ms = if (current.updated_at_ms > 0) current.updated_at_ms else null,
            .recovered = current.recovered,
            .reasoning_level = reportedLevel(self.reducer.options.settings.reasoning_effort),
            .compaction_policy_json = try reportedPolicy(arena, self.reducer.options),
        };
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, envelope_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        _ = envelope_id;
        const self = cast(ptr);
        if (!std.mem.eql(u8, request.session_id, self.id) or request.messages.len == 0) return error.InvalidSubmission;
        if (request.delivery != .auto or request.allow_degraded_features.len != 0) return error.InvalidSubmission;
        const inputs = try arena.alloc(session.InputMessage, request.messages.len);
        for (request.messages, inputs) |message, *input| {
            const id = try self.owned().dupe(u8, message.id orelse "");
            const text: ?[]const u8 = switch (message.content) {
                .text => |content| try self.owned().dupe(u8, content),
                .parts => null,
            };
            input.* = .{ .id = id, .role = @tagName(message.role), .text = text };
        }
        const model_id: ?[]const u8 = if (request.model_id) |model| try self.owned().dupe(u8, model) else null;
        self.reducer.submit(.{ .messages = inputs, .model_id = model_id, .effort = self.pending_effort }) catch |err| return switch (err) {
            error.ModelNotFound => refusal.missingModel(request.model_id orelse ""),
            error.InvalidSubmission, error.UnsupportedInput => error.InvalidSubmission,
            error.SessionClosed, error.NotOpen => error.SessionClosed,
            error.RunActive => error.RunActive,
            else => lift(err),
        };
        try self.flush();
        switch (try self.awaitSettled(arena, native.method_turn_start, refusal)) {
            .admitted => |admission| {
                self.pending_effort = "";
                const message_ids = try arena.alloc([]const u8, admission.message_ids.len);
                for (admission.message_ids, message_ids) |source, *slot| slot.* = try arena.dupe(u8, source);
                const submission_id = try arena.dupe(u8, admission.submission_id);
                const run_id = try arena.dupe(u8, admission.run_id);
                const admitted_model: ?[]const u8 = if (admission.model_id.len > 0) try arena.dupe(u8, admission.model_id) else null;
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
                    .model_id = admitted_model,
                    .message_ids = message_ids,
                };
            },
            .refused => |refused| return refusal.fail(error.BackendFailed, try describe(arena, refused)),
            else => return refusal.fail(error.BackendFailed, "the codex app-server answered turn/start with an unrelated settlement"),
        }
    }

    fn resolve(ptr: *anyopaque, arena: std.mem.Allocator, resolution: contract.Resolution, refusal: *contract.Refusal) contract.Failure!void {
        _ = arena;
        _ = refusal;
        const self = cast(ptr);
        const translated = switch (resolution) {
            .permission => |request| try ownPermission(self.owned(), request),
            .input => |request| try ownInput(self.owned(), request),
        };
        self.reducer.resolve(translated) catch |err| return switch (err) {
            error.InteractionNotFound, error.InteractionResolved => error.InteractionNotFound,
            error.WrongResponder, error.InvalidResolution => error.InvalidResolution,
            else => lift(err),
        };
        try self.flush();
    }

    fn cancel(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
        const self = cast(ptr);
        const immediate = self.reducer.cancel(run_id) catch |err| return switch (err) {
            error.RunNotFound => error.RunNotFound,
            error.SessionClosed => error.SessionClosed,
            else => lift(err),
        };
        const result = immediate orelse settled: {
            try self.flush();
            break :settled switch (try self.awaitSettled(arena, native.method_turn_interrupt, refusal)) {
                .cancel => |answered| answered,
                .refused => |refused| return refusal.fail(error.BackendFailed, try describe(arena, refused)),
                else => return refusal.fail(error.BackendFailed, "the codex app-server answered turn/interrupt with an unrelated settlement"),
            };
        };
        return .{
            .session_id = self.id,
            .run_id = try arena.dupe(u8, run_id),
            .accepted = result.accepted,
            .status = std.meta.stringToEnum(oap_types.RunStatus, result.status) orelse .cancelling,
        };
    }

    fn compact(self: *Session) contract.Failure!bool {
        if (self.ended or self.reducer.closed or self.reducer.transport_closed) return false;
        if (self.reducer.active != null) return false;
        if (self.reducer.envelopes.items.len != 0 or self.reducer.writes.items.len != 0) return false;
        if (self.reducer_arena.queryCapacity() <= self.retained +| self.compact_above) return false;
        var fresh = std.heap.ArenaAllocator.init(self.gpa);
        errdefer fresh.deinit();
        var kept = self.reducer.compactInto(&fresh) catch |err| return lift(err);
        self.reducer_arena.deinit();
        self.reducer_arena.* = fresh;
        kept.arena = self.reducer_arena;
        self.reducer = kept;
        self.retained = self.reducer_arena.queryCapacity();
        return true;
    }

    fn readable(ptr: *anyopaque) ?std.Io.File.Handle {
        const self = cast(ptr);
        return self.transport.readable();
    }

    fn pump(ptr: *anyopaque, wait_ns: u64) contract.Failure!bool {
        const self = cast(ptr);
        _ = try self.compact();
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
        try appendEvents(allocator, self.reducer.envelopes.items, out);
        self.reducer.envelopes.clearRetainingCapacity();
    }

    fn activity(ptr: *anyopaque) contract.Activity {
        const self = cast(ptr);
        if (self.reducer.active == null) return .idle;
        if (self.reducer.pendingInteraction() != null) return .waiting;
        return .running;
    }

    fn close(ptr: *anyopaque, force: bool) contract.Failure!void {
        _ = force;
        cast(ptr).destroy();
    }
};

fn ownPermission(own: std.mem.Allocator, request: *const oap_types.PermissionResolveRequest) std.mem.Allocator.Error!session.Resolution {
    const interaction_id = try own.dupe(u8, request.interaction_id);
    const requested_by = try own.dupe(u8, request.requested_by);
    const responded_by = try own.dupe(u8, request.responded_by);
    const session_id = try own.dupe(u8, request.session_id);
    const run_id = try own.dupe(u8, request.run_id);
    const choice_id = try own.dupe(u8, request.choice_id orelse "");
    return .{ .run_id = run_id, .responded_by = responded_by, .permission = .{
        .interaction_id = interaction_id,
        .requested_by = requested_by,
        .responded_by = responded_by,
        .session_id = session_id,
        .run_id = run_id,
        .choice_id = choice_id,
        .granted = request.granted,
        .updates_arguments = request.updated_arguments_json != null,
    } };
}

fn ownInput(own: std.mem.Allocator, request: *const oap_types.UserInputResolveRequest) std.mem.Allocator.Error!session.Resolution {
    const answers = try own.alloc(session.Answer, request.answers.len);
    for (request.answers, answers) |answer, *slot| {
        const selected = try own.alloc([]const u8, answer.selected_option_ids.len);
        for (answer.selected_option_ids, selected) |option, *copy| copy.* = try own.dupe(u8, option);
        const question_id = try own.dupe(u8, answer.question_id);
        const text = try own.dupe(u8, answer.text orelse "");
        slot.* = .{ .question_id = question_id, .text = text, .selected_option_ids = selected };
    }
    const interaction_id = try own.dupe(u8, request.interaction_id);
    const requested_by = try own.dupe(u8, request.requested_by);
    const responded_by = try own.dupe(u8, request.responded_by);
    const session_id = try own.dupe(u8, request.session_id);
    const run_id = try own.dupe(u8, request.run_id);
    return .{ .run_id = run_id, .responded_by = responded_by, .input = .{
        .interaction_id = interaction_id,
        .requested_by = requested_by,
        .responded_by = responded_by,
        .session_id = session_id,
        .run_id = run_id,
        .answers = answers,
    } };
}

fn describe(arena: std.mem.Allocator, refused: session.Refusal) ![]const u8 {
    if (refused.code) |code| return std.fmt.allocPrint(arena, "codex rpc error for {s} ({d}): {s}", .{ refused.method, code, refused.message });
    return std.fmt.allocPrint(arena, "codex {s}: {s}", .{ refused.method, refused.message });
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

const efforts = [_]struct { level: []const u8, effort: []const u8 }{
    .{ .level = "off", .effort = "none" },
    .{ .level = "minimal", .effort = "minimal" },
    .{ .level = "low", .effort = "low" },
    .{ .level = "medium", .effort = "medium" },
    .{ .level = "high", .effort = "high" },
    .{ .level = "xhigh", .effort = "xhigh" },
    .{ .level = "max", .effort = "max" },
};

fn effortFor(level: []const u8) ?[]const u8 {
    for (efforts) |entry| {
        if (std.mem.eql(u8, entry.level, level)) return entry.effort;
    }
    return null;
}

fn reportedLevel(effort: []const u8) ?[]const u8 {
    for (efforts) |entry| {
        if (std.mem.eql(u8, entry.effort, effort)) return entry.level;
    }
    return null;
}

fn reportedPolicy(arena: std.mem.Allocator, options: session.Options) !?[]const u8 {
    if (options.settings.auto_compact_token_limit) |limit| return try std.fmt.allocPrint(arena, "{{\"kind\":\"tokens\",\"tokens\":{d}}}", .{limit});
    if (options.compaction_auto) return "{\"kind\":\"auto\"}";
    return null;
}

const testing = std.testing;

pub const FakeCodex = struct {
    tmp: std.testing.TmpDir,
    path: []u8,

    pub fn init(allocator: std.mem.Allocator, script: []const u8) !FakeCodex {
        if (builtin.os.tag == .windows) return error.SkipZigTest;
        var tmp = std.testing.tmpDir(.{});
        errdefer tmp.cleanup();
        try tmp.dir.writeFile(testing.io, .{ .sub_path = "codex", .data = script, .flags = .{ .permissions = .executable_file } });
        const cwd = try std.process.currentPathAlloc(testing.io, allocator);
        defer allocator.free(cwd);
        const path = try std.Io.Dir.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "codex" });
        return .{ .tmp = tmp, .path = path };
    }

    pub fn deinit(self: *FakeCodex, allocator: std.mem.Allocator) void {
        allocator.free(self.path);
        self.tmp.cleanup();
    }

    pub fn written(self: *FakeCodex, allocator: std.mem.Allocator) ![]u8 {
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

    pub fn config(self: *const FakeCodex) Config {
        return .{
            .executable = self.path,
            .environment = &.{"PATH=/usr/bin:/bin"},
            .model = "glm-test",
            .approval_policy = "on-request",
            .sandbox = "workspace-write",
            .exit_grace_ns = 2 * std.time.ns_per_s,
            .request_timeout_ns = 10 * std.time.ns_per_s,
            .poll_ns = 2 * std.time.ns_per_ms,
        };
    }
};

pub const fake_prelude =
    \\#!/bin/sh
    \\exec 3>>"$(dirname "$0")/stdin.log"
    \\take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
    \\take; printf '{"id":0,"result":{"userAgent":"codex-fake","codexHome":"/codex","platformFamily":"unix","platformOs":"linux"}}\n'
    \\take
    \\take; printf '{"id":1,"result":{"thread":{"id":"native-thread"}}}\n'
    \\
;

const fake_turn_admitted =
    \\take; printf '{"id":2,"result":{"turn":{"id":"native-turn","status":"inProgress"}}}\n'
    \\printf '{"method":"turn/started","params":{"threadId":"native-thread","turn":{"id":"native-turn","status":"inProgress"}}}\n'
    \\
;

pub const fake_text_turn = fake_turn_admitted ++
    \\printf '{"method":"item/agentMessage/delta","params":{"delta":"fixture-ok","itemId":"message-native","threadId":"native-thread","turnId":"native-turn"}}\n'
    \\printf '{"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"native-turn","status":"completed"}}}\n'
    \\
;

pub const fake_approval_turn = fake_turn_admitted ++
    \\printf '{"method":"item/started","params":{"threadId":"native-thread","turnId":"native-turn","item":{"type":"commandExecution","id":"native-item","command":"true","status":"inProgress"}}}\n'
    \\printf '{"id":7,"method":"item/commandExecution/requestApproval","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"native-item","kind":"command","startedAtMs":10,"reason":"needs approval","availableDecisions":["accept","decline","cancel"]}}\n'
    \\take
    \\printf '{"method":"item/completed","params":{"threadId":"native-thread","turnId":"native-turn","item":{"type":"commandExecution","id":"native-item","command":"true","status":"completed","aggregatedOutput":"ok"}}}\n'
    \\printf '{"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"native-turn","status":"completed"}}}\n'
    \\
;

pub const fake_interrupted_turn = fake_turn_admitted ++
    \\take; printf '{"id":3,"result":{}}\n'
    \\printf '{"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"native-turn","status":"interrupted"}}}\n'
    \\
;

pub const fake_idle = "while take; do :; done\n";

const Probe = struct {
    fake: FakeCodex,
    adapter: Adapter,
    arena: std.heap.ArenaAllocator,
    handle: ?contract.Session = null,

    fn init(self: *Probe, script: []const u8) !void {
        self.fake = try FakeCodex.init(testing.allocator, script);
        self.adapter = Adapter.init(testing.allocator, self.fake.config());
        self.arena = std.heap.ArenaAllocator.init(testing.allocator);
        self.handle = null;
    }

    fn deinit(self: *Probe) void {
        if (self.handle) |opened| opened.teardown();
        self.arena.deinit();
        self.fake.deinit(testing.allocator);
    }

    fn open(self: *Probe, refusal: *contract.Refusal) !contract.Session {
        return self.openWith(.{ .session_id = "s1", .participant = "user" }, refusal);
    }

    fn openWith(self: *Probe, request: contract.OpenRequest, refusal: *contract.Refusal) !contract.Session {
        const opened = try self.adapter.adapter().vtable.open(&self.adapter, self.arena.allocator(), request, refusal);
        self.handle = opened;
        return opened;
    }

    fn submit(self: *Probe, text: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const messages = try self.arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = text } }});
        const request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = messages, .delivery = .auto };
        return self.handle.?.submit(self.arena.allocator(), &request, "", refusal);
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

fn kinds(allocator: std.mem.Allocator, events: []const contract.Event) ![]const []const u8 {
    const names = try allocator.alloc([]const u8, events.len);
    for (events, names) |event, *name| {
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, allocator, event.line, .{});
        name.* = parsed.object.get("type").?.string;
    }
    return names;
}

test "the descriptor is the Go adapter's, under its revision" {
    try testing.expectEqualStrings(session.capability_revision, capability_revision);
    try testing.expectEqual(session.features.len, descriptor.features.len);
    for (session.features, descriptor.features) |pinned, served| {
        try testing.expectEqualStrings(pinned.name, served.key);
        try testing.expectEqualStrings(pinned.level, @tagName(served.level));
        try testing.expectEqualStrings(pinned.reason, served.reason orelse "");
        try testing.expectEqualStrings(pinned.scope, served.scope orelse "");
    }
}

test "an open writes the pinned initialize, initialized and thread/start frames before handing the session out" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);

    const scratch = probe.arena.allocator();
    const conversation = try std.json.parseFromSliceLeaky(std.json.Value, scratch, @embedFile("codex_conversation"), .{});
    var pinned = std.ArrayList([]const u8).empty;
    for (conversation.object.get("frames").?.array.items) |frame| {
        if (std.mem.eql(u8, frame.object.get("direction").?.string, "client_to_server")) try pinned.append(scratch, frame.object.get("frame").?.string);
    }
    const written = try probe.fake.written(scratch);
    var lines = std.mem.splitScalar(u8, std.mem.trimEnd(u8, written, "\n"), '\n');
    for (pinned.items[0..3]) |want| try testing.expectEqualStrings(want, lines.next().?);
    try testing.expect(lines.next() == null);
}

test "a turn is admitted on its turn/start answer and settles on turn/completed" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);

    const admitted = try probe.submit("hello", &refusal);
    try testing.expect(admitted.accepted);
    try testing.expectEqualStrings("s1", admitted.session_id);
    try testing.expectEqualStrings("glm-test", admitted.model_id.?);
    try testing.expectEqual(@as(usize, 1), admitted.message_ids.len);

    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);
    const names = try kinds(probe.arena.allocator(), seen.items);
    try testing.expectEqualStrings("run.started", names[0]);
    try testing.expectEqualStrings("run.completed", names[names.len - 1]);
    for (seen.items, 1..) |event, sequence| {
        try testing.expectEqualStrings(admitted.run_id.?, event.run_id);
        try testing.expectEqual(@as(u64, sequence), event.sequence);
        try testing.expect(std.mem.indexOf(u8, event.line, "\"capability_revision\":\"" ++ capability_revision ++ "\"") != null);
    }
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
}

test "state reports the settled run's last sequence as its transcript cursor" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const fresh = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expect(fresh.transcript_cursor == null);
    _ = try probe.submit("hello", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const completed = try probe.pumpUntil("run.completed", &seen);
    const settled = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqualStrings(try std.fmt.allocPrint(probe.arena.allocator(), "{d}", .{completed.sequence}), settled.transcript_cursor.?);
}

test "a second submission while a run is live is refused run_active" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_interrupted_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("first", &refusal);
    try testing.expectError(error.RunActive, probe.submit("second", &refusal));
}

test "an approval raises a permission interaction and the operator's choice reaches the child" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_approval_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("run true", &refusal);

    var seen = std.ArrayList(contract.Event).empty;
    const asked = try probe.pumpUntil("action.permission.requested", &seen);
    try testing.expectEqual(contract.Activity.waiting, probe.handle.?.activity());
    const payload = try probe.payloadOf(asked);
    const request = oap_types.PermissionResolveRequest{
        .interaction_id = payload.get("interaction_id").?.string,
        .requested_by = payload.get("requested_by").?.string,
        .responded_by = payload.get("responded_by").?.string,
        .session_id = payload.get("session_id").?.string,
        .run_id = payload.get("run_id").?.string,
        .granted = true,
        .choice_id = "accept",
    };
    try probe.handle.?.resolve(probe.arena.allocator(), .{ .permission = &request }, &refusal);
    _ = try probe.waitWritten("{\"id\":7,\"result\":{\"decision\":\"accept\"}}");
    _ = try probe.pumpUntil("run.completed", &seen);
    try testing.expectError(error.InteractionNotFound, probe.handle.?.resolve(probe.arena.allocator(), .{ .permission = &request }, &refusal));
}

test "a cancel interrupts the exact turn, answers cancelling, and the run settles cancelled" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_interrupted_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("long", &refusal);

    const cancelled = try probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expect(cancelled.accepted);
    try testing.expectEqual(oap_types.RunStatus.cancelling, cancelled.status);
    _ = try probe.waitWritten("{\"id\":3,\"method\":\"turn/interrupt\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\"}}");
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.cancelled", &seen);
    try testing.expectError(error.RunNotFound, probe.handle.?.cancel(probe.arena.allocator(), "run-unknown", &refusal));
}

const fake_resume_prelude =
    \\#!/bin/sh
    \\exec 3>>"$(dirname "$0")/stdin.log"
    \\take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
    \\take; printf '{"id":0,"result":{"userAgent":"codex-fake","codexHome":"/codex","platformFamily":"unix","platformOs":"linux"}}\n'
    \\take
    \\
;

test "a reopen resumes the bound thread and reports the model it resumed under" {
    var probe: Probe = undefined;
    try probe.init(fake_resume_prelude ++
        \\take; printf '{"id":1,"result":{"thread":{"id":"native-thread"},"model":"gpt-resumed","modelProvider":"openai","cwd":"/work","approvalPolicy":"on-request","approvalsReviewer":"user","sandbox":{"type":"workspaceWrite"}}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.openWith(.{ .session_id = "s1", .participant = "user", .reopen = true, .native_session_id = "native-thread" }, &refusal);
    try testing.expectEqualStrings("native-thread", opened.nativeId());
    _ = try probe.waitWritten("{\"id\":1,\"method\":\"thread/resume\",\"params\":{\"threadId\":\"native-thread\",\"approvalPolicy\":\"on-request\",\"sandbox\":\"workspace-write\"}}");
    const reopened = try opened.state(probe.arena.allocator(), &refusal);
    try testing.expect(reopened.recovered);
    try testing.expectEqualStrings("gpt-resumed", reopened.current_model_id.?);
}

fn reopenedLevel(comptime effort: []const u8, requested: ?[]const u8) !?[]const u8 {
    var probe: Probe = undefined;
    try probe.init(fake_resume_prelude ++
        \\take; printf '{"id":1,"result":{"thread":{"id":"native-thread"},"model":"gpt-resumed","modelProvider":"openai","cwd":"/work","approvalPolicy":"on-request","approvalsReviewer":"user","sandbox":{"type":"workspaceWrite"},"reasoningEffort":"
    ++ effort ++
        \\"}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.openWith(.{ .session_id = "s1", .participant = "user", .reopen = true, .native_session_id = "native-thread", .reasoning_level = requested }, &refusal);
    _ = try probe.waitWritten("\"method\":\"thread/resume\"");
    const level = (try opened.state(probe.arena.allocator(), &refusal)).reasoning_level orelse return null;
    return try testing.allocator.dupe(u8, level);
}

test "a reopen reports the reasoning level the thread resumed under" {
    const shared = (try reopenedLevel("high", null)).?;
    defer testing.allocator.free(shared);
    try testing.expectEqualStrings("high", shared);
    const off = (try reopenedLevel("none", null)).?;
    defer testing.allocator.free(off);
    try testing.expectEqualStrings("off", off);
    try testing.expectEqual(@as(?[]const u8, null), try reopenedLevel("ultra", null));
    const requested = (try reopenedLevel("high", "low")).?;
    defer testing.allocator.free(requested);
    try testing.expectEqualStrings("low", requested);
}

test "a reopen Codex resumes under wider permissions than the host configured is refused" {
    const answers = [_][]const u8{
        "{\"id\":1,\"result\":{\"thread\":{\"id\":\"native-thread\"},\"model\":\"m\",\"modelProvider\":\"openai\",\"cwd\":\"/work\",\"approvalPolicy\":\"on-request\",\"approvalsReviewer\":\"user\",\"sandbox\":{\"type\":\"dangerFullAccess\"}}}",
        "{\"id\":1,\"result\":{\"thread\":{\"id\":\"native-thread\"},\"model\":\"m\",\"modelProvider\":\"openai\",\"cwd\":\"/work\",\"approvalPolicy\":\"never\",\"approvalsReviewer\":\"user\",\"sandbox\":{\"type\":\"workspaceWrite\"}}}",
    };
    for (answers) |answer| {
        var probe: Probe = undefined;
        const script = try std.fmt.allocPrint(testing.allocator, "{s}take; printf '%s\\n' '{s}'\n{s}", .{ fake_resume_prelude, answer, fake_idle });
        defer testing.allocator.free(script);
        try probe.init(script);
        defer probe.deinit();
        var refusal = contract.Refusal{};
        try testing.expectError(error.UnsupportedFeature, probe.openWith(.{ .session_id = "s1", .participant = "user", .reopen = true, .native_session_id = "native-thread" }, &refusal));
        try testing.expectEqualStrings(contract.feature_open_reopen, refusal.feature);
        try testing.expectEqualStrings(contract.reason_unsatisfiable, refusal.reason);
    }
}

test "a reopen Codex cannot load is unsupported_feature" {
    var probe: Probe = undefined;
    try probe.init(fake_resume_prelude ++
        \\take; printf '{"id":1,"error":{"code":-32602,"message":"no rollout found for thread id native-thread"}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.UnsupportedFeature, probe.openWith(.{ .session_id = "s1", .participant = "user", .reopen = true, .native_session_id = "native-thread" }, &refusal));
    try testing.expect(std.mem.indexOf(u8, refusal.detail, "no rollout found") != null);

    var unbound = contract.Refusal{};
    try testing.expectError(error.UnsupportedFeature, probe.openWith(.{ .session_id = "s1", .participant = "user", .reopen = true }, &unbound));
}

test "an open carries its reasoning level and token limit into thread/start and reports them" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.openWith(.{ .session_id = "s1", .participant = "user", .reasoning_level = "off", .compaction_policy_json = "{\"kind\":\"tokens\",\"tokens\":180000}" }, &refusal);
    _ = try probe.waitWritten("\"config\":{\"model_reasoning_effort\":\"none\",\"model_auto_compact_token_limit\":180000}");
    const reported = try opened.state(probe.arena.allocator(), &refusal);
    try testing.expectEqualStrings("off", reported.reasoning_level.?);
    try testing.expectEqualStrings("{\"kind\":\"tokens\",\"tokens\":180000}", reported.compaction_policy_json.?);
}

test "a compaction form Codex lacks is refused before the app-server starts" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_idle);
    defer probe.deinit();
    for ([_][]const u8{ "{\"kind\":\"off\"}", "{\"kind\":\"share\",\"share_percent\":80}" }) |policy| {
        var refusal = contract.Refusal{};
        try testing.expectError(error.UnsupportedFeature, probe.openWith(.{ .session_id = "s1", .participant = "user", .compaction_policy_json = policy }, &refusal));
        try testing.expectEqualStrings(contract.feature_compaction_policy, refusal.feature);
        try testing.expectEqualStrings("compaction_policy", refusal.field);
    }
    try testing.expectError(error.FileNotFound, probe.fake.written(probe.arena.allocator()));
}

test "a child that exits before answering initialize refuses the open, naming why" {
    var probe: Probe = undefined;
    try probe.init("#!/bin/sh\nexit 3\n");
    defer probe.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, probe.open(&refusal));
    try testing.expectEqualStrings("the codex app-server exited before answering initialize", refusal.message);
}

test "a thread/start the child refuses fails the open with the child's error" {
    var probe: Probe = undefined;
    try probe.init(
        \\#!/bin/sh
        \\exec 3>>"$(dirname "$0")/stdin.log"
        \\take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
        \\take; printf '{"id":0,"result":{"userAgent":"codex-fake","codexHome":"/codex","platformFamily":"unix","platformOs":"linux"}}\n'
        \\take
        \\take; printf '{"id":1,"error":{"code":-32000,"message":"no such model"}}\n'
        \\while take; do :; done
        \\
    );
    defer probe.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, probe.open(&refusal));
    try testing.expectEqualStrings("codex rpc error for thread/start (-32000): no such model", refusal.message);
}

test "a child that dies mid-run fails the run and closes the session" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_turn_admitted ++ "exit 0\n");
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("doomed", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const failed = try probe.pumpUntil("run.failed", &seen);
    try testing.expectEqualStrings("child exited with status 0", (try probe.payloadOf(failed)).get("error").?.object.get("message").?.string);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
    try testing.expectError(error.SessionClosed, probe.submit("after", &refusal));
}

test "a thread/start the child never answers refuses the open once the request bound passes" {
    var probe: Probe = undefined;
    try probe.init(
        \\#!/bin/sh
        \\exec 3>>"$(dirname "$0")/stdin.log"
        \\take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
        \\take; printf '{"id":0,"result":{"userAgent":"codex-fake","codexHome":"/codex","platformFamily":"unix","platformOs":"linux"}}\n'
        \\take
        \\while take; do :; done
        \\
    );
    defer probe.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, probe.open(&refusal));
    try testing.expectEqualStrings("the codex app-server did not answer thread/start within 10000 ms", refusal.message);
}

test "a turn/start the child never answers is refused once the request bound passes, stopping the child and closing the session" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take
        \\while IFS= read -r line; do :; done
        \\printf 'stdin closed\n' >&3
        \\
    );
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    probe.adapter.config.request_timeout_ns = 3 * std.time.ns_per_s;
    try testing.expectError(error.BackendFailed, probe.submit("unanswered", &refusal));
    try testing.expectEqualStrings("the codex app-server did not answer turn/start within 3000 ms", refusal.message);

    var rounds: usize = 0;
    while (rounds < 400) : (rounds += 1) {
        const written = try probe.fake.written(probe.arena.allocator());
        if (std.mem.endsWith(u8, written, "stdin closed\n")) break;
        compat.time.sleepNs(5 * std.time.ns_per_ms);
    } else return error.ChildNeverStopped;
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
    try testing.expectError(error.SessionClosed, probe.submit("after", &refusal));
}

test "a live reasoning level rides the next turn/start's effort only, since Codex keeps it" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++
        \\take; printf '{"id":3,"result":{"turn":{"id":"second-turn","status":"inProgress"}}}\n'
        \\printf '{"method":"turn/started","params":{"threadId":"native-thread","turn":{"id":"second-turn","status":"inProgress"}}}\n'
        \\printf '{"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"second-turn","status":"completed"}}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.open(&refusal);
    const updater = opened.vtable.update_settings.?;
    const at_open = (try opened.state(probe.arena.allocator(), &refusal)).updated_at_ms orelse 0;
    compat.time.sleepNs(3 * std.time.ns_per_ms);
    const updated = try updater(opened.ptr, probe.arena.allocator(), &.{ .session_id = "s1", .reasoning_level = "off" }, &refusal);
    try testing.expectEqualStrings("off", updated.response.reasoning_level.?);
    try testing.expect(updated.response.previous_reasoning_level == null);
    try testing.expectEqualStrings("off", updated.state.reasoning_level.?);
    try testing.expect((updated.state.updated_at_ms orelse 0) > at_open);

    _ = try probe.submit("one", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);
    _ = try probe.submit("two", &refusal);
    _ = try probe.pumpUntil("run.completed", &seen);
    const written = try probe.waitWritten("\"id\":3");
    try testing.expect(std.mem.indexOf(u8, written, "{\"id\":2,\"method\":\"turn/start\",\"params\":{\"threadId\":\"native-thread\",\"input\":[{\"type\":\"text\",\"text\":\"one\"}],\"model\":\"glm-test\",\"effort\":\"none\"}}") != null);
    try testing.expect(std.mem.indexOf(u8, written, "{\"id\":3,\"method\":\"turn/start\",\"params\":{\"threadId\":\"native-thread\",\"input\":[{\"type\":\"text\",\"text\":\"two\"}],\"model\":\"glm-test\"}}") != null);
    try testing.expectEqualStrings("off", (try opened.state(probe.arena.allocator(), &refusal)).reasoning_level.?);
}

test "a live update is refused for what Codex cannot take between runs" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_turn_admitted ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.open(&refusal);
    const updater = opened.vtable.update_settings.?;
    try testing.expectError(error.UnsupportedFeature, updater(opened.ptr, probe.arena.allocator(), &.{ .session_id = "s1", .compaction_policy_json = "{\"kind\":\"tokens\",\"tokens\":1000}" }, &refusal));
    try testing.expectEqualStrings(contract.feature_compaction_policy, refusal.feature);
    try testing.expectEqualStrings(contract.reason_unadvertised, refusal.reason);
    try testing.expectError(error.RunNotFound, updater(opened.ptr, probe.arena.allocator(), &.{ .session_id = "other", .reasoning_level = "high" }, &refusal));
    _ = try probe.submit("busy", &refusal);
    try testing.expectError(error.RunActive, updater(opened.ptr, probe.arena.allocator(), &.{ .session_id = "s1", .reasoning_level = "high" }, &refusal));
    const live: *Session = @ptrCast(@alignCast(opened.ptr));
    try testing.expectEqualStrings("", live.pending_effort);
}

test "a settled session's arena is compacted, keeping the thread, its state and what each settled run reported" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++
        \\take; printf '{"id":3,"result":{"turn":{"id":"second-turn","status":"inProgress"}}}\n'
        \\printf '{"method":"turn/started","params":{"threadId":"native-thread","turn":{"id":"second-turn","status":"inProgress"}}}\n'
        \\printf '{"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"second-turn","status":"completed"}}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const scratch = probe.arena.allocator();

    const first = try probe.submit("one", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);
    const settled = try probe.handle.?.state(scratch, &refusal);

    const live: *Session = @ptrCast(@alignCast(probe.handle.?.ptr));
    live.compact_above = 0;
    const before = live.reducer_arena.queryCapacity();
    try testing.expect(try live.compact());
    try testing.expect(live.reducer_arena.queryCapacity() < before);
    try testing.expect(!try live.compact());

    const kept = try probe.handle.?.state(scratch, &refusal);
    try testing.expectEqualStrings("native-thread", live.reducer.thread_id);
    try testing.expectEqual(settled.status, kept.status);
    try testing.expectEqual(oap_types.SessionStatus.idle, kept.status);
    try testing.expectEqualStrings(settled.session_id, kept.session_id);
    try testing.expectEqualStrings(settled.transcript_cursor.?, kept.transcript_cursor.?);
    try testing.expectEqualStrings(settled.current_model_id.?, kept.current_model_id.?);
    const late = try probe.handle.?.cancel(scratch, first.run_id.?, &refusal);
    try testing.expect(!late.accepted);
    try testing.expectEqual(oap_types.RunStatus.completed, late.status);

    seen.clearRetainingCapacity();
    const second = try probe.submit("two", &refusal);
    try testing.expect(!std.mem.eql(u8, first.run_id.?, second.run_id.?));
    const completed = try probe.pumpUntil("run.completed", &seen);
    try testing.expectEqualStrings(second.run_id.?, completed.run_id);
    try testing.expectEqual(@as(u64, 1), seen.items[0].sequence);
}

test "a session compacts only once no run is active and its events are drained" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_approval_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const scratch = probe.arena.allocator();
    const live: *Session = @ptrCast(@alignCast(probe.handle.?.ptr));

    try testing.expect(!try live.compact());
    live.compact_above = 0;

    const admitted = try probe.submit("gated", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const gate = try probe.pumpUntil("action.permission.requested", &seen);
    var drained = std.ArrayList(contract.Event).empty;
    try probe.handle.?.drain(scratch, &drained);
    try testing.expect(live.reducer.active != null);
    try testing.expect(!try live.compact());

    const answer = oap_types.PermissionResolveRequest{ .interaction_id = (try probe.payloadOf(gate)).get("interaction_id").?.string, .requested_by = endpoint_id, .responded_by = "user", .session_id = "s1", .run_id = admitted.run_id.?, .granted = true, .choice_id = "accept" };
    try probe.handle.?.resolve(scratch, .{ .permission = &answer }, &refusal);
    var rounds: usize = 0;
    while (rounds < 2000) : (rounds += 1) {
        _ = try probe.handle.?.pump(5 * std.time.ns_per_ms);
        if (live.reducer.active == null) break;
    }
    try testing.expect(live.reducer.active == null);
    try testing.expect(live.reducer.envelopes.items.len > 0);
    try testing.expect(!try live.compact());

    try probe.handle.?.drain(scratch, &drained);
    try testing.expect(try live.compact());
}

test "a thread/list answer becomes native sessions, titled by name or the preview's first line" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const answer = try std.json.parseFromSliceLeaky(std.json.Value, arena.allocator(),
        \\{"data":[{"id":"t1","name":"named","preview":"ignored","cwd":"/w","updatedAt":1700000000,"status":{"type":"active"}},
        \\{"id":"t2","name":null,"preview":"first line\nsecond","cwd":"/w","updatedAt":1700000001,"status":{"type":"notLoaded"}},
        \\{"name":"no id"}]}
    , .{});
    const listed = try Adapter.threadsOf(arena.allocator(), answer);
    try std.testing.expectEqual(@as(usize, 2), listed.len);
    try std.testing.expectEqualStrings("named", listed[0].title);
    try std.testing.expect(listed[0].running);
    try std.testing.expectEqual(@as(i64, 1700000000000), listed[0].updated_at_ms);
    try std.testing.expectEqualStrings("first line", listed[1].title);
    try std.testing.expect(!listed[1].running);
    try std.testing.expectEqualStrings("/w", listed[1].directory);
    try std.testing.expectEqualStrings("codex://threads/t2", listed[1].link);
}
