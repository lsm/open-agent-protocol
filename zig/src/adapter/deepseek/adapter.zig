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
pub const capability_revision = harness_pins.deepseek_harness_capability_revision;
pub const server_name = "deepseek-harness-sdk-runtime";
pub const server_version = harness_pins.deepseek_harness_admits[0];

const features = [_]contract.Feature{
    .{ .key = "action.permissions", .level = .unavailable, .reason = "selected SDK wire has no interaction channel" },
    .{ .key = "action.tools", .level = .degraded, .reason = "call/result only; started is synthesized" },
    .{ .key = "action.tools.execute", .level = .unavailable, .reason = "Harness executes tools internally" },
    .{ .key = "capabilities", .level = .emulated, .reason = "conservative descriptor for pinned SDK wire" },
    .{ .key = "protocol.initialize", .level = .emulated, .reason = "adapter-owned one-shot initialization freeze" },
    .{ .key = "run.cancel", .level = .unavailable, .reason = "selected SDK wire has no cancel request" },
    .{ .key = "run.reconciliation", .level = .degraded, .reason = "live status corroboration only" },
    .{ .key = "run.replay", .level = .degraded, .reason = "bounded adapter journal; gaps are explicit and there is no native replay request" },
    .{ .key = "run.resume", .level = .degraded, .reason = "selected SDK wire has no resume request; OAP resume replays the adapter journal" },
    .{ .key = "run.status", .level = .emulated },
    .{ .key = "run.streaming", .level = .native },
    .{ .key = "session.message.delivery.auto", .level = .degraded, .reason = "accepted only for known idle sessions and normalized to start" },
    .{ .key = "session.message.delivery.queue", .level = .unavailable, .reason = "overlapping native followups are outside the adapter contract" },
    .{ .key = "session.message.delivery.steer", .level = .unavailable, .reason = "selected SDK wire has no steer request" },
    .{ .key = "session.message.submit", .level = .degraded, .reason = "receipt plus entered direct-user message proves start" },
    .{ .key = "session.open", .level = .emulated, .reason = "one process and native session per OAP session" },
    .{ .key = "session.state", .level = .degraded, .reason = "reducer-owned live projection; no native query" },
};

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = "DeepSeek Harness SDK Adapter", .version = harness_pins.deepseek_harness_endpoint_version, .adapter = "deepseek-harness-jsonrpc" },
    .capability_revision = capability_revision,
    .features = &features,
};

pub const Config = struct {
    executable: []const u8,
    args: []const []const u8 = &.{},
    environment: []const []const u8 = &.{},
    working_directory: []const u8,
    provider: []const u8,
    model: []const u8,
    max_tokens: ?i64 = null,
    frame_limit: usize = rpc.frame_limit_default,
    exit_grace_ns: u64 = process.default_exit_grace_ns,
    request_timeout_ns: u64 = 60 * std.time.ns_per_s,
    admission_timeout_ns: u64 = 10 * 60 * std.time.ns_per_s,
    shutdown_timeout_ns: u64 = 5 * std.time.ns_per_s,
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

const Reply = struct {
    result: ?std.json.Value,
    failure: []const u8,
};

pub const Session = struct {
    owner: *Adapter,
    gpa: std.mem.Allocator,
    id: []u8,
    reducer_arena: *std.heap.ArenaAllocator,
    transport: *process.Transport,
    reducer: session.Reducer,
    calls: i64 = 0,
    awaited: i64 = 0,
    reply: ?Reply = null,
    ended: bool = false,
    reaped: bool = false,

    fn open(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const config = owner.config;
        if (config.provider.len == 0 or config.model.len == 0 or !std.fs.path.isAbsolute(config.working_directory)) {
            return refusal.fail(error.BackendFailed, "the deepseek harness needs an absolute working directory, a provider and a model");
        }
        const self = try construct(owner, arena, request, refusal);
        errdefer self.destroy();
        try self.handshake(arena, refusal);
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
        const transport = process.Transport.open(gpa, .{
            .executable = config.executable,
            .args = config.args,
            .environment = config.environment,
            .working_directory = config.working_directory,
            .frame_limit = config.frame_limit,
            .exit_grace_ns = config.exit_grace_ns,
        }) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            const message = try std.fmt.allocPrint(arena, "the deepseek harness could not start: {s}", .{@errorName(err)});
            return refusal.fail(error.BackendFailed, message);
        };
        var reducer = session.Reducer.init(reducer_arena.allocator());
        reducer.session_id = id;
        reducer.revision = capability_revision;
        reducer.counters.shared = &owner.ids;
        reducer.counters.now_ms = wallClock;
        session.openSession(&reducer);
        self.* = .{
            .owner = owner,
            .gpa = gpa,
            .id = id,
            .reducer_arena = reducer_arena,
            .transport = transport,
            .reducer = reducer,
        };
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
        session.transportFailed(&self.reducer, detail) catch |err| return lift(err);
    }

    fn closed(self: *Session) bool {
        return self.ended or self.reducer.unusable or self.reducer.closed;
    }

    fn send(self: *Session, method: []const u8, params: ?std.json.Value) contract.Failure!bool {
        self.calls += 1;
        self.awaited = self.calls;
        self.reply = null;
        var frame: std.json.ObjectMap = .empty;
        try frame.put(self.owned(), "id", .{ .integer = self.calls });
        try frame.put(self.owned(), "jsonrpc", .{ .string = "2.0" });
        try frame.put(self.owned(), "method", .{ .string = method });
        if (params) |carried| try frame.put(self.owned(), "params", carried);
        const line = json_encode.valueAlloc(self.owned(), .{ .object = frame }) catch |err| return lift(err);
        self.transport.write(line) catch |err| {
            self.reap();
            const departed = self.transport.departed();
            try self.fail(if (departed.departure != .running) departed.text(self.owned()) else @errorName(err));
            return false;
        };
        return true;
    }

    fn awaitReply(self: *Session, arena: std.mem.Allocator, method: []const u8, refusal: *contract.Refusal) contract.Failure!std.json.Value {
        const started = monotonic();
        while (self.reply == null) {
            if (self.ended) {
                const message = try std.fmt.allocPrint(arena, "the deepseek harness exited before answering {s}", .{method});
                return refusal.fail(error.BackendFailed, message);
            }
            if (monotonic() -| started > self.owner.config.request_timeout_ns) {
                try self.fail("the deepseek harness stopped answering");
                const message = try std.fmt.allocPrint(arena, "the deepseek harness did not answer {s} within {d} ms", .{ method, self.owner.config.request_timeout_ns / std.time.ns_per_ms });
                return refusal.fail(error.BackendFailed, message);
            }
            _ = try self.step(self.owner.config.poll_ns);
        }
        const answered = self.reply.?;
        self.reply = null;
        self.awaited = 0;
        if (answered.result) |result| return result;
        const message = try std.fmt.allocPrint(arena, "deepseek rpc error for {s}: {s}", .{ method, answered.failure });
        return refusal.fail(error.BackendFailed, message);
    }

    fn handshake(self: *Session, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!void {
        const config = self.owner.config;
        var params: std.json.ObjectMap = .empty;
        try params.put(self.owned(), "cwd", .{ .string = config.working_directory });
        try params.put(self.owned(), "provider", .{ .string = config.provider });
        try params.put(self.owned(), "model", .{ .string = config.model });
        if (config.max_tokens) |limit| try params.put(self.owned(), "maxTokens", .{ .integer = limit });
        if (!try self.send("initialize", .{ .object = params })) return refusal.fail(error.BackendFailed, "the deepseek harness exited before answering initialize");
        const started = monotonic();
        while (self.reply == null) {
            if (self.ended) return refusal.fail(error.BackendFailed, "the deepseek harness exited before answering initialize");
            if (monotonic() -| started > config.request_timeout_ns) {
                try self.fail("the deepseek harness never answered initialize");
                const message = try std.fmt.allocPrint(arena, "the deepseek harness did not answer initialize within {d} ms", .{config.request_timeout_ns / std.time.ns_per_ms});
                return refusal.fail(error.BackendFailed, message);
            }
            const received = try self.receive(config.poll_ns) orelse continue;
            if (!self.answers(received)) {
                try self.fail("native observation preceded initialize response");
                return refusal.fail(error.BackendFailed, "the deepseek harness spoke before answering initialize");
            }
            self.take(received);
        }
        const result = try self.awaitReply(arena, "initialize", refusal);
        const info = member(result, "serverInfo") orelse std.json.Value.null;
        if (!std.mem.eql(u8, text(info, "name"), server_name) or !std.mem.eql(u8, text(info, "version"), server_version)) {
            try self.fail("unexpected serverInfo");
            const message = try std.fmt.allocPrint(arena, "the deepseek harness identified as \"{s}\"/\"{s}\", not the pinned {s}/{s}", .{ text(info, "name"), text(info, "version"), server_name, server_version });
            return refusal.fail(error.BackendFailed, message);
        }
        session.initialize(&self.reducer, config.model) catch |err| return lift(err);
    }

    const Received = struct { message: rpc.Message, document: std.json.Value };

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
                const message = rpc.parseMessage(self.owned(), held) catch |err| {
                    if (err == error.OutOfMemory) return error.OutOfMemory;
                    const detail = try std.fmt.allocPrint(self.owned(), "deepseek rpc: {s}", .{@errorName(err)});
                    try self.fail(detail);
                    return null;
                };
                const document = std.json.parseFromSliceLeaky(std.json.Value, self.owned(), held, .{}) catch |err| return lift(err);
                return .{ .message = message, .document = document };
            },
        }
    }

    fn answers(self: *Session, received: Received) bool {
        if (received.message.kind != .response and received.message.kind != .failure) return false;
        const carried = received.document.object.get("id") orelse return false;
        return carried == .integer and carried.integer == self.awaited and self.awaited != 0;
    }

    fn take(self: *Session, received: Received) void {
        if (received.message.kind == .failure) {
            const failure = received.document.object.get("error") orelse std.json.Value.null;
            self.reply = .{ .result = null, .failure = text(failure, "message") };
            return;
        }
        self.reply = .{ .result = received.document.object.get("result") orelse std.json.Value.null, .failure = "" };
    }

    fn step(self: *Session, wait_ns: u64) contract.Failure!bool {
        const was_ended = self.ended;
        const received = try self.receive(wait_ns) orelse return self.ended != was_ended;
        if (self.answers(received)) {
            self.take(received);
            return true;
        }
        switch (received.message.kind) {
            .request => session.externalActivity(&self.reducer, "reverse request") catch |err| return lift(err),
            .notification => session.observeNotification(&self.reducer, received.message.method, received.document.object.get("params")) catch |err| return lift(err),
            .response, .failure => {},
        }
        return true;
    }

    fn idOf(ptr: *anyopaque) []const u8 {
        return cast(ptr).id;
    }

    fn live(self: *Session) bool {
        return self.reducer.reserved and self.reducer.started and !self.reducer.terminal;
    }

    fn state(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        _ = refusal;
        const self = cast(ptr);
        if (self.closed()) return error.SessionClosed;
        const running = self.live();
        const active_run_id: ?[]const u8 = if (running) try arena.dupe(u8, self.reducer.run_id) else null;
        const current_model_id = try arena.dupe(u8, self.reducer.model);
        return .{
            .session_id = self.id,
            .status = if (running) .running else .idle,
            .active_run_id = active_run_id,
            .current_model_id = current_model_id,
            .transcript_cursor = if (self.reducer.cursor > 0) try std.fmt.allocPrint(arena, "{d}", .{self.reducer.cursor}) else null,
            .updated_at_ms = wallClock(),
        };
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const self = cast(ptr);
        if (request.session_id.len == 0 or request.messages.len == 0 or request.delivery != .auto) return error.InvalidSubmission;
        var blocks = std.json.Array.init(self.owned());
        const message_ids = try arena.alloc([]const u8, request.messages.len);
        for (request.messages, message_ids) |message, *slot| {
            if (message.role != .user) return error.InvalidSubmission;
            slot.* = if (message.id) |carried| carried else try self.reducer.counters.nextID(arena, "message");
            switch (message.content) {
                .text => |content| try blocks.append(try textBlock(self.owned(), content)),
                .parts => |parts| for (parts) |part| switch (part) {
                    .text => |content| try blocks.append(try textBlock(self.owned(), content)),
                    else => return error.InvalidSubmission,
                },
            }
        }
        if (self.closed()) return error.SessionClosed;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        if (self.reducer.reserved and !self.reducer.terminal) return error.RunActive;
        session.submitAs(&self.reducer, .{ .mint_request_id = false }) catch |err| return switch (err) {
            error.RunActive => error.RunActive,
            error.SessionClosed, error.SessionUnusable => error.SessionClosed,
            else => lift(err),
        };
        var params: std.json.ObjectMap = .empty;
        try params.put(self.owned(), "sessionId", .{ .string = self.id });
        try params.put(self.owned(), "contentBlocks", .{ .array = blocks });
        if (!try self.send("session/prompt", .{ .object = params })) {
            session.abortSubmission(&self.reducer);
            return refusal.fail(error.BackendFailed, "the deepseek harness exited before answering session/prompt");
        }
        const result = self.awaitReply(arena, "session/prompt", refusal) catch |err| {
            if (!self.reducer.started) session.abortSubmission(&self.reducer);
            return err;
        };
        const receipt = try arena.dupe(u8, text(result, "messageId"));
        session.receipt(&self.reducer, try self.owned().dupe(u8, receipt)) catch |err| return lift(err);
        const started = monotonic();
        while (!self.reducer.started) {
            if (self.reducer.terminal or self.ended or self.reducer.unusable) return refusal.fail(error.BackendFailed, "the deepseek harness did not start the turn");
            if (monotonic() -| started > self.owner.config.admission_timeout_ns) {
                try self.fail("the deepseek harness never started the turn");
                return refusal.fail(error.BackendFailed, "the deepseek harness did not start the turn in time");
            }
            _ = try self.step(self.owner.config.poll_ns);
        }
        const run_id = try arena.dupe(u8, self.reducer.run_id);
        const model_id = try arena.dupe(u8, self.reducer.model);
        return .{
            .session_id = self.id,
            .accepted = true,
            .submission_id = receipt,
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
        return refusal.unsupported("run.cancel", "selected SDK wire has no cancel request");
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
        try appendEvents(allocator, self.reducer.emitted.items, out);
        self.reducer.emitted.clearRetainingCapacity();
    }

    fn activity(ptr: *anyopaque) contract.Activity {
        return if (cast(ptr).live()) .running else .idle;
    }

    fn close(ptr: *anyopaque) void {
        const self = cast(ptr);
        self.shutdown();
        self.destroy();
    }

    fn shutdown(self: *Session) void {
        if (self.ended) return;
        const sent = self.send("shutdown", null) catch false;
        if (!sent) return;
        const started = monotonic();
        while (self.reply == null and !self.ended) {
            if (monotonic() -| started > self.owner.config.shutdown_timeout_ns) return;
            _ = self.step(self.owner.config.poll_ns) catch return;
        }
    }
};

fn textBlock(allocator: std.mem.Allocator, content: []const u8) !std.json.Value {
    var block: std.json.ObjectMap = .empty;
    try block.put(allocator, "type", .{ .string = "text" });
    try block.put(allocator, "text", .{ .string = try allocator.dupe(u8, content) });
    return .{ .object = block };
}

fn member(value: std.json.Value, key: []const u8) ?std.json.Value {
    if (value != .object) return null;
    return value.object.get(key);
}

fn text(value: std.json.Value, key: []const u8) []const u8 {
    const found = member(value, key) orelse return "";
    return if (found == .string) found.string else "";
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

pub const FakeHarness = struct {
    tmp: std.testing.TmpDir,
    path: []u8,
    cwd: []u8,

    pub fn init(allocator: std.mem.Allocator, script: []const u8) !FakeHarness {
        if (builtin.os.tag == .windows) return error.SkipZigTest;
        var tmp = std.testing.tmpDir(.{});
        errdefer tmp.cleanup();
        try tmp.dir.writeFile(testing.io, .{ .sub_path = "harness", .data = script, .flags = .{ .permissions = .executable_file } });
        const cwd = try std.process.currentPathAlloc(testing.io, allocator);
        defer allocator.free(cwd);
        const dir = try std.Io.Dir.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..] });
        errdefer allocator.free(dir);
        const path = try std.Io.Dir.path.join(allocator, &.{ dir, "harness" });
        return .{ .tmp = tmp, .path = path, .cwd = dir };
    }

    pub fn deinit(self: *FakeHarness, allocator: std.mem.Allocator) void {
        allocator.free(self.path);
        allocator.free(self.cwd);
        self.tmp.cleanup();
    }

    pub fn written(self: *FakeHarness, allocator: std.mem.Allocator) ![]u8 {
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

    pub fn config(self: *const FakeHarness) Config {
        return .{
            .executable = self.path,
            .environment = &.{"PATH=/usr/bin:/bin"},
            .working_directory = self.cwd,
            .provider = "fixture-provider",
            .model = "deepseek-chat",
            .exit_grace_ns = 2 * std.time.ns_per_s,
            .request_timeout_ns = 10 * std.time.ns_per_s,
            .admission_timeout_ns = 10 * std.time.ns_per_s,
            .shutdown_timeout_ns = 2 * std.time.ns_per_s,
            .poll_ns = 2 * std.time.ns_per_ms,
        };
    }
};

pub const fake_prelude =
    \\#!/bin/sh
    \\exec 3>>"$(dirname "$0")/stdin.log"
    \\take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
    \\take; printf '{"id":1,"jsonrpc":"2.0","result":{"serverInfo":{"name":"deepseek-harness-sdk-runtime","version":"0.0.1"}}}\n'
    \\
;

pub const fake_turn_admitted =
    \\take
    \\printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"inserted":[{"content":[{"text":"hello","type":"text"}],"id":"m-1","role":"user","source":{"kind":"user"}}],"start":0,"target":"next-turn"},"seq":1,"time":1,"type":"agent/inbox/spliced"},"sessionId":"s1"}}'
    \\printf '%s\n' '{"id":2,"jsonrpc":"2.0","result":{"messageId":"m-1"}}'
    \\printf '%s\n' '{"jsonrpc":"2.0","method":"session.status","params":{"sessionId":"s1","status":"running"}}'
    \\printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"turn":1},"seq":2,"time":2,"type":"turn/start"},"sessionId":"s1"}}'
    \\printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"step":1,"turn":1},"seq":3,"time":3,"type":"step/start"},"sessionId":"s1"}}'
    \\printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"content":[{"text":"hello","type":"text"}],"id":"m-1","role":"user","source":{"kind":"user"}},"seq":4,"surfaceOp":"append","time":4,"type":"user/message"},"sessionId":"s1"}}'
    \\
;

pub const fake_text_turn = fake_turn_admitted ++
    \\printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"message":{"content":[{"text":"Hello","type":"text"}],"id":"a-6","role":"assistant","source":{"kind":"model","model":"deepseek-chat","provider":"fixture-provider"}},"stream":[{"type":"chunk","time":6,"chunk":{"type":"text-delta","index":0,"text":"Hel"}}],"step":1,"turn":1,"usage":{"inputTokens":1,"outputTokens":2}},"seq":6,"surfaceOp":"append","time":6,"type":"assistant/message"},"sessionId":"s1"}}'
    \\printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"step":1,"turn":1},"seq":7,"time":7,"type":"step/end"},"sessionId":"s1"}}'
    \\printf '%s\n' '{"jsonrpc":"2.0","method":"session.event","params":{"event":{"data":{"reason":{"kind":"completed"},"turn":1},"seq":8,"time":8,"type":"turn/end"},"sessionId":"s1"}}'
    \\printf '%s\n' '{"jsonrpc":"2.0","method":"session.status","params":{"sessionId":"s1","status":"idle"}}'
    \\
;

pub const fake_idle =
    \\while take; do
    \\  id=$(printf '%s' "$line" | sed -n 's/^{"id":\([0-9]*\),"jsonrpc":"2.0","method":"shutdown".*/\1/p')
    \\  if [ -n "$id" ]; then printf '{"id":%s,"jsonrpc":"2.0","result":{}}\n' "$id"; printf 'shutdown answered\n' >&3; exit 0; fi
    \\done
    \\
;

const Probe = struct {
    fake: FakeHarness,
    adapter: Adapter,
    arena: std.heap.ArenaAllocator,
    handle: ?contract.Session = null,

    fn init(self: *Probe, script: []const u8) !void {
        self.fake = try FakeHarness.init(testing.allocator, script);
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

    fn submit(self: *Probe, prompt: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const messages = try self.arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = prompt } }});
        const request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = messages, .delivery = .auto };
        return self.handle.?.submit(self.arena.allocator(), &request, refusal);
    }

    fn pumpUntil(self: *Probe, comptime kind: []const u8, seen: *std.ArrayList(contract.Event)) !contract.Event {
        var rounds: usize = 0;
        while (rounds < 2000) : (rounds += 1) {
            var drained = std.ArrayList(contract.Event).empty;
            try self.handle.?.drain(self.arena.allocator(), &drained);
            try seen.appendSlice(self.arena.allocator(), drained.items);
            for (drained.items) |emitted| {
                if (std.mem.indexOf(u8, emitted.line, "\"type\":\"" ++ kind ++ "\"") != null) return emitted;
            }
            _ = try self.handle.?.pump(5 * std.time.ns_per_ms);
        }
        return error.EventNeverArrived;
    }

    fn payloadOf(self: *Probe, emitted: contract.Event) !std.json.ObjectMap {
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, self.arena.allocator(), emitted.line, .{});
        return parsed.object.get("payload").?.object;
    }
};

test "the descriptor serves resume and replay from the endpoint journal under the Go adapter's revision" {
    try testing.expectEqualStrings(session.capability_revision, capability_revision);
    try testing.expectEqual(oap_types.SupportLevel.degraded, descriptor.level("run.replay"));
    try testing.expectEqual(oap_types.SupportLevel.degraded, descriptor.level("run.resume"));
    for (features[1..], features[0 .. features.len - 1]) |later, earlier| try testing.expect(std.mem.lessThan(u8, earlier.key, later.key));
}

test "an open writes the pinned initialize, and a prompt is admitted once the harness enters it and settles on turn end" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("hello", &refusal);
    try testing.expectEqualStrings("deepseek-chat", admitted.model_id.?);

    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);
    for (seen.items, 1..) |emitted, sequence| {
        try testing.expectEqualStrings(admitted.run_id.?, emitted.run_id);
        try testing.expectEqual(@as(u64, sequence), emitted.sequence);
        try testing.expect(std.mem.indexOf(u8, emitted.line, "\"capability_revision\":\"" ++ capability_revision ++ "\"") != null);
    }
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
    const written = try probe.fake.written(probe.arena.allocator());
    const initialize = try std.fmt.allocPrint(probe.arena.allocator(), "{{\"id\":1,\"jsonrpc\":\"2.0\",\"method\":\"initialize\",\"params\":{{\"cwd\":\"{s}\",\"provider\":\"fixture-provider\",\"model\":\"deepseek-chat\"}}}}\n", .{probe.fake.cwd});
    try testing.expect(std.mem.startsWith(u8, written, initialize));
    try testing.expect(std.mem.endsWith(u8, written, "{\"id\":2,\"jsonrpc\":\"2.0\",\"method\":\"session/prompt\",\"params\":{\"sessionId\":\"s1\",\"contentBlocks\":[{\"type\":\"text\",\"text\":\"hello\"}]}}\n"));
}

test "a close asks the harness to shut down before stopping it" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.open(&refusal);
    opened.close();
    probe.handle = null;
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.endsWith(u8, written, "{\"id\":2,\"jsonrpc\":\"2.0\",\"method\":\"shutdown\"}\nshutdown answered\n"));
}

test "a harness naming another server refuses the open" {
    var probe: Probe = undefined;
    try probe.init(
        \\#!/bin/sh
        \\IFS= read -r line
        \\printf '{"id":1,"jsonrpc":"2.0","result":{"serverInfo":{"name":"imposter","version":"9"}}}\n'
        \\while IFS= read -r line; do :; done
        \\
    );
    defer probe.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, probe.open(&refusal));
    try testing.expectEqualStrings("the deepseek harness identified as \"imposter\"/\"9\", not the pinned deepseek-harness-sdk-runtime/0.0.1", refusal.message);
}

test "a harness that speaks before answering initialize refuses the open" {
    var probe: Probe = undefined;
    try probe.init(
        \\#!/bin/sh
        \\IFS= read -r line
        \\printf '{"jsonrpc":"2.0","method":"session.status","params":{"sessionId":"s1","status":"idle"}}\n'
        \\while IFS= read -r line; do :; done
        \\
    );
    defer probe.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, probe.open(&refusal));
    try testing.expectEqualStrings("the deepseek harness spoke before answering initialize", refusal.message);
}

test "a cancel is refused as unsupported, since the wire has no cancel request" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_turn_admitted ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("hello", &refusal);
    try testing.expectError(error.UnsupportedFeature, probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal));
    try testing.expectEqualStrings("run.cancel", refusal.feature);
    try testing.expectError(error.RunActive, probe.submit("again", &refusal));
}

test "a harness that dies mid-run fails the run with its exit and closes the session" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_turn_admitted ++ "exit 4\n");
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("hello", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const failed = try probe.pumpUntil("run.failed", &seen);
    const failure = (try probe.payloadOf(failed)).get("error").?.object;
    try testing.expectEqualStrings("deepseek_process_exit", failure.get("code").?.string);
    try testing.expectEqualStrings("child exited with status 4", failure.get("message").?.string);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
}
