const std = @import("std");
const builtin = @import("builtin");
const contract = @import("contract");
const oap_types = @import("oap_types");
const process = @import("process");
const compat = @import("compat");
const json_encode = @import("json_encode");
const native = @import("native");
const session = @import("copilot_session");

pub const endpoint_id = session.endpoint_id;
pub const endpoint_version = "1.0.98";
pub const capability_revision = "copilot-cli-1.0.98-oap-v1";
pub const protocol_version: i64 = 3;

const features = [_]contract.Feature{
    .{ .key = "protocol.initialize", .level = .emulated, .reason = "the adapter checks the server's protocol version with ping and creates the native session" },
    .{ .key = "capabilities", .level = .emulated, .reason = "a conservative descriptor for the pinned headless server" },
    .{ .key = "session.open", .level = .native, .reason = "session.create in one headless server per OAP session" },
    .{ .key = "session.state", .level = .emulated, .reason = "adapter-owned projection of the runs it admitted" },
    .{ .key = contract.feature_submit, .level = .emulated, .reason = "session.send answers a message id at once; the run starts at its own user.message" },
    .{ .key = "session.message.delivery.auto", .level = .emulated, .reason = "start when no run is open, otherwise queue behind it, as session.send enqueues" },
    .{ .key = "session.message.delivery.queue", .level = .emulated, .reason = "session.send enqueues behind an open run; an explicit queue on an idle session is admitted as a reservation that Copilot promotes at once" },
    .{ .key = "run.streaming", .level = .native },
    .{ .key = "run.status", .level = .emulated },
    .{ .key = "action.tools", .level = .native, .reason = "tool.execution_start and tool.execution_complete with progress between" },
    .{ .key = "action.tools.execute", .level = .unavailable, .reason = "Copilot executes its own tools" },
};

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = "GitHub Copilot CLI Adapter", .version = endpoint_version, .adapter = "copilot-headless-stdio" },
    .capability_revision = capability_revision,
    .features = &features,
    .limits = .{ .max_active_runs_per_session = session.default_max_queued + 1, .max_queued_runs_per_session = session.default_max_queued },
};

pub const Config = struct {
    executable: []const u8,
    args: []const []const u8 = &.{ "--headless", "--stdio", "--no-auto-update" },
    environment: []const []const u8 = &.{},
    working_directory: []const u8,
    model: []const u8 = "",
    frame_limit: usize = process.default_frame_limit,
    exit_grace_ns: u64 = process.default_exit_grace_ns,
    request_timeout_ns: u64 = 60 * std.time.ns_per_s,
    poll_ns: u64 = 5 * std.time.ns_per_ms,
};

fn wallClock() i64 {
    return compat.time.nowMillis();
}

fn monotonic() u64 {
    return compat.time.monotonicNanos() catch 0;
}

fn lift(err: anyerror) contract.Failure {
    if (err == error.OutOfMemory) return error.OutOfMemory;
    return error.BackendFailed;
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

const Answer = struct { result: ?std.json.Value, failure: []const u8 };

pub const Session = struct {
    owner: *Adapter,
    gpa: std.mem.Allocator,
    id: []u8,
    native_id: []const u8 = "",
    reducer_arena: *std.heap.ArenaAllocator,
    transport: *process.Transport,
    reducer: session.Reducer,
    calls: i64 = 0,
    awaited: i64 = 0,
    answer: ?Answer = null,
    ended: bool = false,
    sending: ?[]const u8 = null,
    held: std.ArrayList(native.Event) = .empty,

    fn open(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        if (request.reopen) return refusal.unsupported(contract.feature_open_reopen, contract.reason_unadvertised);
        if (request.participant.len == 0) return refusal.fail(error.InvalidSubmission, "open requires a non-empty participant id");
        if (!std.fs.path.isAbsolute(owner.config.working_directory)) return refusal.fail(error.BackendFailed, "the copilot adapter needs an absolute working directory");
        const self = try construct(owner, arena, request, refusal);
        errdefer self.destroy();
        try self.connect(arena, request, refusal);
        return self;
    }

    fn construct(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
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
        const participant = try reducer_arena.allocator().dupe(u8, request.participant);
        const transport = process.Transport.open(gpa, .{
            .executable = config.executable,
            .args = config.args,
            .environment = config.environment,
            .working_directory = config.working_directory,
            .frame_limit = config.frame_limit,
            .exit_grace_ns = config.exit_grace_ns,
            .framing = .content_length,
        }) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            const message = try std.fmt.allocPrint(arena, "the copilot server could not start: {s}", .{@errorName(err)});
            return refusal.fail(error.BackendFailed, message);
        };
        self.* = .{
            .owner = owner,
            .gpa = gpa,
            .id = id,
            .reducer_arena = reducer_arena,
            .transport = transport,
            .reducer = session.Reducer.init(reducer_arena, .{ .session_id = id, .native_id = "", .revision = capability_revision, .participant = participant, .counter = &owner.ids, .now_ms = wallClock }),
        };
        return self;
    }

    fn mint(owner: *Adapter, allocator: std.mem.Allocator) ![]u8 {
        owner.ids += 1;
        return std.fmt.allocPrint(allocator, "session-{d}", .{owner.ids});
    }

    fn connect(self: *Session, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!void {
        _ = request;
        const pong = try self.call(arena, "ping", .{ .object = .empty }, refusal);
        const spoken = if (pong == .object) pong.object.get("protocolVersion") else null;
        if (spoken == null or spoken.? != .integer or spoken.?.integer != protocol_version) {
            try self.fail("unexpected protocol version");
            return refusal.fail(error.BackendFailed, "the copilot server does not speak the pinned protocol version 3");
        }
        const status = try self.call(arena, "status.get", .{ .object = .empty }, refusal);
        const version = if (status == .object) native.text(status.object, "version") else "";
        if (!std.mem.eql(u8, version, endpoint_version)) {
            try self.fail("unexpected copilot version");
            const message = try std.fmt.allocPrint(arena, "the copilot server reports version \"{s}\"; this adapter is pinned to {s}", .{ version, endpoint_version });
            return refusal.fail(error.BackendFailed, message);
        }
        var params: std.json.ObjectMap = .empty;
        try params.put(self.owned(), "clientName", .{ .string = "open-agent-protocol" });
        if (self.owner.config.model.len > 0) try params.put(self.owned(), "model", .{ .string = self.owner.config.model });
        try params.put(self.owned(), "requestPermission", .{ .bool = false });
        try params.put(self.owned(), "requestUserInput", .{ .bool = false });
        try params.put(self.owned(), "workingDirectory", .{ .string = self.owner.config.working_directory });
        try params.put(self.owned(), "streaming", .{ .bool = true });
        try params.put(self.owned(), "envValueMode", .{ .string = "direct" });
        const created = try self.call(arena, "session.create", .{ .object = params }, refusal);
        const native_id = if (created == .object) native.text(created.object, "sessionId") else "";
        if (native_id.len == 0) {
            try self.fail("session.create answered no session id");
            return refusal.fail(error.BackendFailed, "the copilot server answered session.create with no session id");
        }
        self.native_id = try self.owned().dupe(u8, native_id);
        self.reducer.options.native_id = self.native_id;
    }

    fn handle(self: *Session) contract.Session {
        return .{ .ptr = self, .vtable = &.{
            .id = idOf,
            .state = state,
            .submit = submit,
            .resolve = resolve,
            .cancel = cancel,
            .readable = readable,
            .pump = pump,
            .drain = drain,
            .activity = activity,
            .close = close,
        } };
    }

    fn cast(ptr: *anyopaque) *Session {
        return @ptrCast(@alignCast(ptr));
    }

    fn owned(self: *Session) std.mem.Allocator {
        return self.reducer_arena.allocator();
    }

    fn destroy(self: *Session) void {
        self.transport.close();
        self.transport.deinit();
        self.reducer_arena.deinit();
        self.gpa.destroy(self.reducer_arena);
        self.gpa.free(self.id);
        self.gpa.destroy(self);
    }

    fn fail(self: *Session, detail: []const u8) contract.Failure!void {
        if (self.ended) return;
        self.ended = true;
        self.transport.close();
        self.reducer.closed = true;
        for (self.reducer.runs.items) |run| {
            if (run.terminal) continue;
            if (self.sending) |provisional| if (std.mem.eql(u8, provisional, run.id)) continue;
            self.reducer.refuse(run.id, "copilot_process_exit", detail) catch |err| return lift(err);
        }
    }

    fn call(self: *Session, arena: std.mem.Allocator, method: []const u8, params: std.json.Value, refusal: *contract.Refusal) contract.Failure!std.json.Value {
        self.calls += 1;
        self.awaited = self.calls;
        self.answer = null;
        defer self.awaited = 0;
        const frame = native.encodeRequest(self.owned(), self.calls, method, params) catch |err| return lift(err);
        self.transport.write(frame) catch {
            try self.fail("the copilot server stopped reading");
            const message = try std.fmt.allocPrint(arena, "the copilot server exited before answering {s}", .{method});
            return refusal.fail(error.BackendFailed, message);
        };
        const started = monotonic();
        while (self.answer == null) {
            if (self.ended) {
                const message = try std.fmt.allocPrint(arena, "the copilot server exited before answering {s}", .{method});
                return refusal.fail(error.BackendFailed, message);
            }
            if (monotonic() -| started > self.owner.config.request_timeout_ns) {
                try self.fail("the copilot server stopped answering");
                const message = try std.fmt.allocPrint(arena, "the copilot server did not answer {s} within {d} ms", .{ method, self.owner.config.request_timeout_ns / std.time.ns_per_ms });
                return refusal.fail(error.BackendFailed, message);
            }
            _ = try self.step(self.owner.config.poll_ns);
        }
        const answered = self.answer.?;
        self.answer = null;
        if (answered.result) |result| return result;
        const message = try std.fmt.allocPrint(arena, "copilot rpc error for {s}: {s}", .{ method, answered.failure });
        return refusal.fail(error.BackendFailed, message);
    }

    fn step(self: *Session, wait_ns: u64) contract.Failure!bool {
        if (self.ended) return false;
        const polled = self.transport.poll(wait_ns) catch |err| {
            try self.fail(@errorName(err));
            return true;
        };
        switch (polled) {
            .quiet => return false,
            .ended => {
                self.transport.close();
                try self.fail(self.transport.departed().text(self.owned()));
                return true;
            },
            .frame => |bytes| {
                const held = try self.owned().dupe(u8, bytes);
                const message = native.parseMessage(self.owned(), held) catch |err| {
                    if (err == error.OutOfMemory) return error.OutOfMemory;
                    try self.fail("the copilot server sent a frame that is not JSON-RPC 2.0");
                    return true;
                };
                try self.take(message);
                return true;
            },
        }
    }

    fn take(self: *Session, message: native.Message) contract.Failure!void {
        switch (message.kind) {
            .response, .failure => {
                const id = message.id orelse return;
                if (id != .integer or id.integer != self.awaited or self.awaited == 0) return;
                self.answer = if (message.kind == .response) .{ .result = message.result orelse .null, .failure = "" } else .{ .result = null, .failure = message.failure_message };
            },
            .notification => {
                const decoded = native.decodeEvent(message) catch |err| {
                    if (err == error.OutOfMemory) return error.OutOfMemory;
                    return self.fail("the copilot server sent a session.event that does not decode");
                };
                const event = decoded orelse return;
                if (self.sending != null and (self.held.items.len > 0 or self.unbound(event))) return self.held.append(self.owned(), event);
                self.reducer.observe(event) catch |err| return lift(err);
            },
            .request => {
                const id = message.id orelse return;
                const refused = native.encodeFailure(self.owned(), id, -32601, "method not supported") catch |err| return lift(err);
                self.transport.write(refused) catch try self.fail("the copilot server stopped reading");
            },
        }
    }

    fn unbound(self: *Session, event: native.Event) bool {
        if (event.kind != .user_message) return false;
        const message_id = native.text(event.data, "messageId");
        for (self.reducer.runs.items) |run| {
            if (std.mem.eql(u8, run.native_message_id, message_id)) return false;
        }
        return true;
    }

    fn release(self: *Session, run_id: []const u8, message_id: ?[]const u8) contract.Failure!void {
        self.sending = null;
        defer self.held.clearRetainingCapacity();
        if (message_id) |bound| {
            self.reducer.bind(run_id, bound) catch |err| return lift(err);
        } else {
            self.reducer.forget(run_id);
            if (self.ended) return;
        }
        for (self.held.items) |event| self.reducer.observe(event) catch |err| return lift(err);
    }

    fn idOf(ptr: *anyopaque) []const u8 {
        return cast(ptr).id;
    }

    fn live(self: *Session) ?*session.Run {
        for (self.reducer.runs.items) |run| {
            if (!run.terminal) return run;
        }
        return null;
    }

    fn state(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        _ = refusal;
        const self = cast(ptr);
        if (self.ended) return error.SessionClosed;
        var entries = std.ArrayList(oap_types.ActiveRun).empty;
        var executing: ?[]const u8 = null;
        var position: u64 = 0;
        for (self.reducer.runs.items) |run| {
            if (run.terminal) continue;
            const run_id = try arena.dupe(u8, run.id);
            if (run.status == .queued) {
                position += 1;
                try entries.append(arena, .{ .run_id = run_id, .status = .queued, .relationship = "primary", .queue_position = position });
                continue;
            }
            executing = run_id;
            try entries.append(arena, .{ .run_id = run_id, .status = .running, .relationship = "primary" });
        }
        const status: oap_types.SessionStatus = if (executing != null) .running else if (entries.items.len > 0) .queued else .idle;
        return .{ .session_id = self.id, .status = status, .active_run_id = executing, .active_runs = entries.items, .updated_at_ms = wallClock() };
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, envelope_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        _ = envelope_id;
        const self = cast(ptr);
        switch (request.delivery) {
            .auto, .queue => {},
            .steer, .btw => {
                refusal.* = .{ .feature = if (request.delivery == .steer) "session.message.delivery.steer" else "session.message.delivery.btw", .reason = contract.reason_unadvertised, .detail = "a Copilot prompt takes auto or queue delivery" };
                return error.UnsupportedFeature;
            },
        }
        if (request.messages.len == 0) return error.InvalidSubmission;
        var prompt: std.ArrayList(u8) = .empty;
        for (request.messages) |message| {
            if (message.role != .user) return error.InvalidSubmission;
            switch (message.content) {
                .text => |content| try prompt.appendSlice(arena, content),
                .parts => |parts| for (parts) |part| switch (part) {
                    .text => |content| try prompt.appendSlice(arena, content),
                    else => return error.InvalidSubmission,
                },
            }
        }
        if (prompt.items.len == 0) return error.InvalidSubmission;
        if (self.ended) return error.SessionClosed;
        const busy = self.reducer.live() > 0;
        const admitted = (if (request.delivery == .queue) self.reducer.reserve(request.session_id) else self.reducer.admit(request.session_id)) catch |err| return switch (err) {
            error.InvalidSubmission => error.InvalidSubmission,
            error.RunNotFound => error.RunNotFound,
            error.RunActive => error.RunActive,
            error.SessionClosed => error.SessionClosed,
            error.OutOfMemory => error.OutOfMemory,
        };
        var params: std.json.ObjectMap = .empty;
        try params.put(self.owned(), "sessionId", .{ .string = self.native_id });
        try params.put(self.owned(), "prompt", .{ .string = try self.owned().dupe(u8, prompt.items) });
        self.sending = admitted.run_id;
        const sent = self.call(arena, "session.send", .{ .object = params }, refusal) catch |err| {
            try self.release(admitted.run_id, null);
            return err;
        };
        const message_id = if (sent == .object) native.text(sent.object, "messageId") else "";
        if (message_id.len == 0) {
            try self.release(admitted.run_id, null);
            return refusal.fail(error.BackendFailed, "the copilot server answered session.send with no message id");
        }
        try self.release(admitted.run_id, message_id);
        const started = std.mem.eql(u8, admitted.admission, "started");
        const message_ids = try arena.alloc([]const u8, 1);
        message_ids[0] = try arena.dupe(u8, admitted.message_id);
        const submission_id = try arena.dupe(u8, message_id);
        const run_id = try arena.dupe(u8, admitted.run_id);
        return .{
            .session_id = self.id,
            .accepted = true,
            .submission_id = submission_id,
            .requested_delivery = request.delivery,
            .effective_delivery = if (started) .start else .queue,
            .delivery_resolution = if (busy) "session_busy" else "session_idle",
            .admission = if (started) .started else .queued,
            .run_id = run_id,
            .status = if (started) .running else .queued,
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
        _ = ptr;
        _ = arena;
        _ = run_id;
        return refusal.unsupported("run.cancel", contract.reason_unadvertised);
    }

    fn readable(ptr: *anyopaque) ?std.Io.File.Handle {
        return cast(ptr).transport.readable();
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
        try out.ensureUnusedCapacity(allocator, self.reducer.envelopes.items.len);
        const first = out.items.len;
        errdefer {
            for (out.items[first..]) |written| {
                allocator.free(written.line);
                allocator.free(written.run_id);
            }
            out.shrinkRetainingCapacity(first);
        }
        for (self.reducer.envelopes.items) |value| {
            const line = try json_encode.valueAlloc(allocator, value);
            errdefer allocator.free(line);
            const run_id = try allocator.dupe(u8, value.object.get("run_id").?.string);
            const sequence: u64 = @intCast(value.object.get("sequence").?.integer);
            out.appendAssumeCapacity(.{ .line = line, .run_id = run_id, .sequence = sequence });
        }
        self.reducer.envelopes.clearRetainingCapacity();
    }

    fn activity(ptr: *anyopaque) contract.Activity {
        return if (cast(ptr).live() != null) .running else .idle;
    }

    fn close(ptr: *anyopaque, force: bool) contract.Failure!void {
        _ = force;
        cast(ptr).destroy();
    }
};

const testing = std.testing;

const FakeServer = struct {
    tmp: std.testing.TmpDir,
    path: []u8,
    cwd: []u8,

    fn init(allocator: std.mem.Allocator, script: []const u8) !FakeServer {
        if (builtin.os.tag == .windows) return error.SkipZigTest;
        var tmp = std.testing.tmpDir(.{});
        errdefer tmp.cleanup();
        try tmp.dir.writeFile(testing.io, .{ .sub_path = "copilot", .data = script, .flags = .{ .permissions = .executable_file } });
        const cwd = try std.process.currentPathAlloc(testing.io, allocator);
        defer allocator.free(cwd);
        const dir = try std.Io.Dir.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..] });
        errdefer allocator.free(dir);
        const path = try std.Io.Dir.path.join(allocator, &.{ dir, "copilot" });
        return .{ .tmp = tmp, .path = path, .cwd = dir };
    }

    fn deinit(self: *FakeServer, allocator: std.mem.Allocator) void {
        allocator.free(self.path);
        allocator.free(self.cwd);
        self.tmp.cleanup();
    }

    fn written(self: *FakeServer, allocator: std.mem.Allocator) ![]u8 {
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

    fn config(self: *const FakeServer) Config {
        return .{ .executable = self.path, .args = &.{}, .environment = &.{"PATH=/usr/bin:/bin"}, .working_directory = self.cwd, .exit_grace_ns = 2 * std.time.ns_per_s, .request_timeout_ns = 10 * std.time.ns_per_s, .poll_ns = 2 * std.time.ns_per_ms };
    }
};

const fake_prelude =
    \\#!/bin/sh
    \\exec 3>>"$(dirname "$0")/stdin.log"
    \\take() { IFS= read -r header || exit 0; len=$(printf '%s' "${header#Content-Length: }" | tr -d '\r'); IFS= read -r blank; body=$(dd bs=1 count="$len" 2>/dev/null); printf '%s\n' "$body" >&3; }
    \\say() { printf 'Content-Length: %s\r\n\r\n%s' "${#1}" "$1"; }
    \\
;

const fake_connect = fake_prelude ++
    \\take; say '{"jsonrpc":"2.0","id":1,"result":{"message":"pong","timestamp":1,"protocolVersion":3}}'
    \\take; say '{"jsonrpc":"2.0","id":2,"result":{"version":"1.0.98","protocolVersion":3}}'
    \\take; say '{"jsonrpc":"2.0","id":3,"result":{"sessionId":"ses-1","workspacePath":"/w"}}'
    \\
;

const fake_idle = "while take; do :; done\n";

inline fn eventFrame(comptime kind: []const u8, comptime data: []const u8) []const u8 {
    return "say '{\"jsonrpc\":\"2.0\",\"method\":\"session.event\",\"params\":{\"sessionId\":\"ses-1\",\"event\":{\"type\":\"" ++ kind ++ "\",\"data\":" ++ data ++ "}}}'\n";
}

const Probe = struct {
    fake: FakeServer,
    adapter: Adapter,
    arena: std.heap.ArenaAllocator,
    handle: ?contract.Session = null,

    fn init(self: *Probe, script: []const u8) !void {
        self.fake = try FakeServer.init(testing.allocator, script);
        self.adapter = Adapter.init(testing.allocator, self.fake.config());
        self.arena = std.heap.ArenaAllocator.init(testing.allocator);
        self.handle = null;
    }

    fn deinit(self: *Probe) void {
        if (self.handle) |held| held.teardown();
        self.arena.deinit();
        self.fake.deinit(testing.allocator);
    }

    fn open(self: *Probe, refusal: *contract.Refusal) !contract.Session {
        const opened = try self.adapter.adapter().open(self.arena.allocator(), .{ .session_id = "s1", .participant = "user" }, refusal);
        self.handle = opened;
        return opened;
    }

    fn submit(self: *Probe, text: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        return self.deliver(text, .auto, refusal);
    }

    fn deliver(self: *Probe, text: []const u8, delivery: oap_types.RequestedDelivery, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const messages = try self.arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = text } }});
        return self.handle.?.submit(self.arena.allocator(), &.{ .session_id = "s1", .messages = messages, .delivery = delivery }, "", refusal);
    }

    fn pumpUntil(self: *Probe, comptime kind: []const u8, seen: *std.ArrayList(contract.Event)) !contract.Event {
        var rounds: usize = 0;
        while (rounds < 2000) : (rounds += 1) {
            _ = try self.handle.?.pump(5 * std.time.ns_per_ms);
            var drained = std.ArrayList(contract.Event).empty;
            try self.handle.?.drain(self.arena.allocator(), &drained);
            try seen.appendSlice(self.arena.allocator(), drained.items);
            for (seen.items) |event| {
                if (std.mem.indexOf(u8, event.line, "\"type\":\"" ++ kind ++ "\"") != null) return event;
            }
        }
        return error.EventNeverArrived;
    }
};

test "an open checks the protocol version and creates the native session, and a prompt streams to completion at idle" {
    var probe: Probe = undefined;
    try probe.init(fake_connect ++
        \\take; say '{"jsonrpc":"2.0","id":4,"result":{"messageId":"m-1"}}'
        \\
    ++ eventFrame("user.message", "{\"messageId\":\"m-1\",\"interactionId\":\"i-1\",\"content\":\"hi\"}") ++
        eventFrame("assistant.message_delta", "{\"messageId\":\"a-1\",\"deltaContent\":\"pong\"}") ++
        eventFrame("assistant.message", "{\"messageId\":\"a-1\",\"content\":\"pong\"}") ++
        eventFrame("session.idle", "{}") ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.open(&refusal);
    const admitted = try probe.submit("hi", &refusal);
    try testing.expectEqual(oap_types.Admission.started, admitted.admission);
    try testing.expectEqualStrings("m-1", admitted.submission_id);
    var seen = std.ArrayList(contract.Event).empty;
    const completed = try probe.pumpUntil("run.completed", &seen);
    try testing.expectEqualStrings(admitted.run_id.?, completed.run_id);
    try testing.expect(std.mem.indexOf(u8, completed.line, "\"text\":\"pong\"") != null);
    try testing.expect(std.mem.indexOf(u8, seen.items[1].line, "content.delta") != null);
    try testing.expectEqual(contract.Activity.idle, opened.activity());
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "\"method\":\"ping\"") != null);
    try testing.expect(std.mem.indexOf(u8, written, "\"method\":\"session.create\"") != null);
    try testing.expect(std.mem.indexOf(u8, written, probe.fake.cwd) != null);
    try testing.expect(std.mem.indexOf(u8, written, "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"session.send\",\"params\":{\"sessionId\":\"ses-1\",\"prompt\":\"hi\"}}") != null);
}

test "an open refuses a server on another protocol version, and one that answers no session id" {
    var refusal = contract.Refusal{};
    var wrong: Probe = undefined;
    try wrong.init(fake_prelude ++
        \\take; say '{"jsonrpc":"2.0","id":1,"result":{"message":"pong","protocolVersion":4}}'
        \\
    ++ fake_idle);
    defer wrong.deinit();
    try testing.expectError(error.BackendFailed, wrong.open(&refusal));
    try testing.expect(std.mem.indexOf(u8, refusal.message, "protocol version 3") != null);

    var anonymous: Probe = undefined;
    try anonymous.init(fake_prelude ++
        \\take; say '{"jsonrpc":"2.0","id":1,"result":{"message":"pong","protocolVersion":3}}'
        \\take; say '{"jsonrpc":"2.0","id":2,"result":{"version":"1.0.98","protocolVersion":3}}'
        \\take; say '{"jsonrpc":"2.0","id":3,"result":{}}'
        \\
    ++ fake_idle);
    defer anonymous.deinit();
    try testing.expectError(error.BackendFailed, anonymous.open(&refusal));
    try testing.expect(std.mem.indexOf(u8, refusal.message, "no session id") != null);
}

test "a second prompt queues behind the open run, and a server that exits fails every open run" {
    var probe: Probe = undefined;
    try probe.init(fake_connect ++
        \\take; say '{"jsonrpc":"2.0","id":4,"result":{"messageId":"m-1"}}'
        \\take; say '{"jsonrpc":"2.0","id":5,"result":{"messageId":"m-2"}}'
        \\
    ++ eventFrame("user.message", "{\"messageId\":\"m-1\"}") ++ "exit 0\n");
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const first = try probe.submit("one", &refusal);
    const second = try probe.submit("two", &refusal);
    try testing.expectEqual(oap_types.Admission.started, first.admission);
    try testing.expectEqual(oap_types.Admission.queued, second.admission);
    try testing.expectEqual(oap_types.EffectiveDelivery.queue, second.effective_delivery);
    const held = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqual(oap_types.SessionStatus.running, held.status);
    try testing.expectEqualStrings(first.run_id.?, held.active_run_id.?);
    try testing.expectEqual(@as(usize, 2), held.active_runs.len);
    try testing.expectEqualStrings(second.run_id.?, held.active_runs[1].run_id);
    try testing.expectEqual(@as(?u64, 1), held.active_runs[1].queue_position);
    try testing.expect(held.active_runs[0].queue_position == null);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.failed", &seen);
    var failed: usize = 0;
    for (seen.items) |event| {
        if (std.mem.indexOf(u8, event.line, "\"type\":\"run.failed\"") == null) continue;
        failed += 1;
        try testing.expect(std.mem.indexOf(u8, event.line, "child exited with status 0") != null);
    }
    try testing.expectEqual(@as(usize, 2), failed);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
}

test "a prompt the server refuses or answers without a message id is not admitted, and leaves no run behind" {
    var probe: Probe = undefined;
    try probe.init(fake_connect ++
        \\take; say '{"jsonrpc":"2.0","id":4,"error":{"code":-32000,"message":"busy"}}'
        \\take; say '{"jsonrpc":"2.0","id":5,"result":{}}'
        \\take; say '{"jsonrpc":"2.0","id":6,"result":{"messageId":"m-3"}}'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    try testing.expectError(error.BackendFailed, probe.submit("one", &refusal));
    try testing.expectError(error.BackendFailed, probe.submit("two", &refusal));
    var drained = std.ArrayList(contract.Event).empty;
    try probe.handle.?.drain(probe.arena.allocator(), &drained);
    try testing.expectEqual(@as(usize, 0), drained.items.len);
    const idle = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqual(oap_types.SessionStatus.idle, idle.status);
    try testing.expectEqual(@as(usize, 0), idle.active_runs.len);
    const third = try probe.submit("three", &refusal);
    try testing.expectEqual(oap_types.Admission.started, third.admission);
}

test "an explicit queue on an idle session is a reservation Copilot promotes at once, and steer is refused" {
    var probe: Probe = undefined;
    try probe.init(fake_connect ++
        \\take; say '{"jsonrpc":"2.0","id":4,"result":{"messageId":"m-1"}}'
        \\
    ++ eventFrame("user.message", "{\"messageId\":\"m-1\"}") ++
        eventFrame("assistant.message", "{\"messageId\":\"a-1\",\"content\":\"done\"}") ++
        eventFrame("session.idle", "{}") ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const reserved = try probe.deliver("one", .queue, &refusal);
    try testing.expectEqual(oap_types.Admission.queued, reserved.admission);
    try testing.expectEqual(oap_types.EffectiveDelivery.queue, reserved.effective_delivery);
    try testing.expectEqual(oap_types.RequestedDelivery.queue, reserved.requested_delivery);
    try testing.expectEqualStrings("session_idle", reserved.delivery_resolution.?);
    var seen = std.ArrayList(contract.Event).empty;
    const started = try probe.pumpUntil("run.started", &seen);
    try testing.expectEqualStrings(reserved.run_id.?, started.run_id);
    _ = try probe.pumpUntil("run.completed", &seen);
    try testing.expectError(error.UnsupportedFeature, probe.deliver("two", .steer, &refusal));
    try testing.expectEqualStrings("session.message.delivery.steer", refusal.feature);
}

test "an open refuses a server that reports another Copilot version" {
    var refusal = contract.Refusal{};
    var other: Probe = undefined;
    try other.init(fake_prelude ++
        \\take; say '{"jsonrpc":"2.0","id":1,"result":{"message":"pong","protocolVersion":3}}'
        \\take; say '{"jsonrpc":"2.0","id":2,"result":{"version":"1.0.97","protocolVersion":3}}'
        \\
    ++ fake_idle);
    defer other.deinit();
    try testing.expectError(error.BackendFailed, other.open(&refusal));
    try testing.expect(std.mem.indexOf(u8, refusal.message, "reports version \"1.0.97\"") != null);
    const written = try other.fake.written(other.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "session.create") == null);
}

test "a run whose user.message arrives before session.send answers still starts and completes" {
    var probe: Probe = undefined;
    try probe.init(fake_connect ++ "take\n" ++
        eventFrame("user.message", "{\"messageId\":\"m-1\"}") ++
        eventFrame("assistant.message", "{\"messageId\":\"a-1\",\"content\":\"early\"}") ++
        \\say '{"jsonrpc":"2.0","id":4,"result":{"messageId":"m-1"}}'
        \\
    ++ eventFrame("session.idle", "{}") ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("hi", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const completed = try probe.pumpUntil("run.completed", &seen);
    try testing.expectEqualStrings(admitted.run_id.?, completed.run_id);
    try testing.expect(std.mem.indexOf(u8, completed.line, "early") != null);
    try testing.expect(std.mem.indexOf(u8, seen.items[0].line, "\"type\":\"run.started\"") != null);
}

test "a server that exits before answering session.send fails the open run and admits nothing" {
    var probe: Probe = undefined;
    try probe.init(fake_connect ++
        \\take; say '{"jsonrpc":"2.0","id":4,"result":{"messageId":"m-1"}}'
        \\
    ++ eventFrame("user.message", "{\"messageId\":\"m-1\"}") ++ "take\nexit 0\n");
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const first = try probe.submit("one", &refusal);
    try testing.expectError(error.BackendFailed, probe.submit("two", &refusal));
    var drained = std.ArrayList(contract.Event).empty;
    try probe.handle.?.drain(probe.arena.allocator(), &drained);
    var failed: usize = 0;
    for (drained.items) |event| {
        try testing.expectEqualStrings(first.run_id.?, event.run_id);
        if (std.mem.indexOf(u8, event.line, "\"type\":\"run.failed\"") != null) failed += 1;
    }
    try testing.expectEqual(@as(usize, 1), failed);
}

test "a refused send still delivers the events held while it was outstanding to the open run" {
    var probe: Probe = undefined;
    try probe.init(fake_connect ++
        \\take; say '{"jsonrpc":"2.0","id":4,"result":{"messageId":"m-1"}}'
        \\
    ++ eventFrame("user.message", "{\"messageId\":\"m-1\"}") ++ "take\n" ++
        eventFrame("user.message", "{\"messageId\":\"m-9\"}") ++
        eventFrame("assistant.message", "{\"messageId\":\"a-1\",\"content\":\"first\"}") ++
        eventFrame("session.idle", "{}") ++
        \\say '{"jsonrpc":"2.0","id":5,"error":{"code":-32000,"message":"busy"}}'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const first = try probe.submit("one", &refusal);
    try testing.expectError(error.BackendFailed, probe.submit("two", &refusal));
    var seen = std.ArrayList(contract.Event).empty;
    const completed = try probe.pumpUntil("run.completed", &seen);
    try testing.expectEqualStrings(first.run_id.?, completed.run_id);
    try testing.expect(std.mem.indexOf(u8, completed.line, "first") != null);
}
