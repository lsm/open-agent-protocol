const std = @import("std");
const builtin = @import("builtin");
const oap_types = @import("oap_types");
const oap_envelope = @import("oap_envelope");
const json_encode = @import("json_encode");
const contract = @import("contract");
const compat = @import("compat");
const hubmod = @import("hub");
const hub_http = @import("hub_http");
const routes = @import("hub_routes");
const hub_stdio = @import("hub_stdio");

pub const max_connections: usize = 64;
pub const io_cycle_ms: i32 = 50;
pub const stream_buffer_bytes: usize = 256 * 1024;
pub const read_chunk_bytes: usize = 16 * 1024;

const text_plain = "text/plain; charset=utf-8";
const ok_status = "200 OK";
const internal_status = "500 Internal Server Error";

pub const Answer = struct {
    status: []const u8,
    content_type: []const u8 = "application/json",
    body: []const u8 = "",
    allow: []const u8 = "",
};

pub const Reply = union(enum) {
    answer: Answer,
    no_content,
    stream: *hubmod.Subscription,
    gap: contract.Gap,
    deferred: Deferred,
};

pub const Deferred = struct {
    ticket: u64,
    correlation: Correlation,
};

const Correlation = struct {
    in_reply_to: ?[]const u8 = null,
    session_id: ?[]const u8 = null,
    run_id: ?[]const u8 = null,
};

const discard = struct {
    var context: u8 = 0;
    fn write(_: *anyopaque, _: []const u8) anyerror!void {}
};

const bad_request_codes = [_][]const u8{
    "malformed_json",
    "schema_invalid",
    "type_mismatch",
    "invalid_payload",
    "invalid_request",
    "invalid_steer_target",
};

pub fn statusFor(code: []const u8) []const u8 {
    if (hub_stdio.statusForRefusal(code)) |status| return status;
    for (bad_request_codes) |candidate| {
        if (std.mem.eql(u8, candidate, code)) return hub_http.bad_request;
    }
    return internal_status;
}

pub const Daemon = struct {
    allocator: std.mem.Allocator,
    frontend: hub_stdio.Frontend,
    allow: []const []const u8,
    outer: hub_http.KeepGoing,
    stopping: std.atomic.Value(bool) = .init(false),
    streams: std.atomic.Value(usize) = .init(0),

    pub fn init(allocator: std.mem.Allocator, core: *hubmod.Hub, allow: []const []const u8, outer: hub_http.KeepGoing) !Daemon {
        const frontend = try hub_stdio.Frontend.init(allocator, core, .{ .context = &discard.context, .write = discard.write }, .{});
        return .{ .allocator = allocator, .frontend = frontend, .allow = allow, .outer = outer };
    }

    pub fn deinit(self: *Daemon) void {
        self.frontend.deinit();
        self.* = undefined;
    }

    pub fn stop(self: *Daemon) void {
        self.stopping.store(true, .release);
    }

    fn goingCheck(context: *const anyopaque) bool {
        const self: *const Daemon = @ptrCast(@alignCast(context));
        if (self.stopping.load(.acquire)) return false;
        return self.outer.yes();
    }

    pub fn going(self: *const Daemon) hub_http.KeepGoing {
        return .{ .context = self, .check = goingCheck };
    }

    fn next(self: *Daemon) u64 {
        self.frontend.next_envelope += 1;
        return self.frontend.next_envelope;
    }

    pub fn mint(self: *Daemon) u64 {
        return self.next();
    }

    pub fn pump(self: *Daemon) !void {
        try self.frontend.hub.pump(self.allocator, 0);
        self.frontend.sweepAbandoned();
    }

    pub fn respond(self: *Daemon, arena: std.mem.Allocator, request: hub_http.Request) !Reply {
        const method = if (std.mem.eql(u8, request.method, "HEAD")) "GET" else request.method;
        const matched = routes.route(arena, method, request.split.path) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            error.BadEscape => return .{ .answer = .{ .status = hub_http.bad_request, .content_type = text_plain, .body = "bad request" } },
        };
        const found = switch (matched) {
            .not_found => return .{ .answer = .{ .status = "404 Not Found", .content_type = text_plain, .body = "not found" } },
            .method_not_allowed => |allowed| return .{ .answer = .{ .status = "405 Method Not Allowed", .content_type = text_plain, .body = "method not allowed", .allow = allowed } },
            .route => |value| value,
        };
        return switch (found) {
            .adapters => self.listing(arena, try self.frontend.adapters(arena)),
            .sessions => self.listing(arena, try self.frontend.sessions(arena)),
            .history => self.history(arena, request.split.query),
            .work_list => self.outcome(arena, try self.frontend.workList(arena, .{ .include_closed = try self.queryFlag(arena, request.split.query, "include_closed"), .include_native = try self.queryFlag(arena, request.split.query, "include_native") }), .{}),
            .work_status => |id| self.outcome(arena, try self.frontend.workStatus(arena, id), .{ .session_id = id }),
            .work_start => |name| self.workStart(arena, name, request.body),
            .work_send => |id| self.workSend(arena, id, request.body),
            .work_stop => |id| self.outcome(arena, try self.frontend.workStop(arena, id), .{ .session_id = id }),
            .work_read => |id| self.workRead(arena, id, request.split.query),
            .capabilities => |name| self.outcome(arena, try self.frontend.capabilities(arena, name), .{}),
            .state => |id| self.outcome(arena, try self.frontend.state(arena, id), .{ .session_id = id }),
            .models => |id| self.outcome(arena, try self.frontend.models(arena, id, try queryValues(arena, request.split.query, "allow_degraded")), .{ .session_id = id }),
            .tools => |id| self.outcome(arena, try self.frontend.tools(arena, id, try queryValues(arena, request.split.query, "allow_degraded")), .{ .session_id = id }),
            .close => |id| self.closing(arena, id),
            .open => |name| self.open(arena, name, request.body),
            .submit => |id| self.submit(arena, id, request.body),
            .resolve => |id| self.resolve(arena, id, request.body),
            .cancel => |id| self.cancel(arena, id, request.body),
            .settings => |id| self.settings(arena, id, request.body),
            .events => |id| self.events(arena, id, request),
        };
    }

    fn history(self: *Daemon, arena: std.mem.Allocator, query: []const u8) !Reply {
        var limit: ?i64 = null;
        if (try queryValue(arena, query, "limit")) |text| {
            limit = std.fmt.parseInt(i64, text, 10) catch 0;
        }
        return self.outcome(arena, try self.frontend.history(arena, try queryValue(arena, query, "cursor"), limit), .{});
    }

    fn listing(self: *Daemon, arena: std.mem.Allocator, value: std.json.Value) !Reply {
        _ = self;
        return .{ .answer = .{ .status = ok_status, .body = try json_encode.valueAlloc(arena, value) } };
    }

    fn outcome(self: *Daemon, arena: std.mem.Allocator, given: hub_stdio.Frontend.Outcome, correlation: Correlation) !Reply {
        return switch (given) {
            .answer => |value| .{ .answer = .{ .status = ok_status, .body = try json_encode.valueAlloc(arena, value) } },
            .answer_line => |line| .{ .answer = .{ .status = ok_status, .body = line } },
            .refused => |refused| self.refusal(arena, refused, correlation),
            .streaming => unreachable,
            .deferred => |ticket| .{ .deferred = .{ .ticket = ticket, .correlation = correlation } },
        };
    }

    pub fn settleDeferred(self: *Daemon, arena: std.mem.Allocator, deferred: Deferred) !?Reply {
        const job = self.frontend.finishedJob(deferred.ticket) orelse return null;
        defer self.frontend.releaseJob(job);
        return try self.outcome(arena, try self.frontend.finish(arena, job), deferred.correlation);
    }

    pub fn abandon(self: *Daemon, ticket: u64) void {
        self.frontend.abandon(ticket);
    }

    fn refusal(self: *Daemon, arena: std.mem.Allocator, refused: hub_stdio.Refusal, correlation: Correlation) !Reply {
        const id = try std.fmt.allocPrint(arena, "oap-error-{d}", .{self.next()});
        const in_reply_to = correlation.in_reply_to orelse try std.fmt.allocPrint(arena, "oap-request-{d}", .{self.next()});
        const message = try hub_stdio.trim(arena, refused.message);
        const body = try oap_envelope.serializeEnvelope(.{
            .id = id,
            .in_reply_to = in_reply_to,
            .session_id = correlation.session_id,
            .run_id = correlation.run_id,
            .payload = .{ .error_response = .{ .code = refused.code, .message = message, .details = refused.details } },
        }, arena);
        return .{ .answer = .{ .status = statusFor(refused.code), .body = body } };
    }

    fn control(self: *Daemon, arena: std.mem.Allocator, given: hub_stdio.Frontend.Control, correlation: Correlation) !Reply {
        switch (given) {
            .refused => |refused| {
                var correlated = correlation;
                if (refused.run_id) |run_id| correlated.run_id = run_id;
                return self.refusal(arena, refused.refusal, correlated);
            },
            .envelope => |envelope| return self.answered(arena, envelope),
        }
    }

    fn answered(self: *Daemon, arena: std.mem.Allocator, envelope: oap_types.Envelope) !Reply {
        _ = self;
        return .{ .answer = .{ .status = ok_status, .body = try oap_envelope.serializeEnvelope(envelope, arena) } };
    }

    fn closing(self: *Daemon, arena: std.mem.Allocator, id: []const u8) !Reply {
        const closed = try self.frontend.closeSession(arena, id);
        return switch (closed) {
            .refused => |refused| self.refusal(arena, refused, .{ .session_id = id }),
            else => .no_content,
        };
    }

    fn open(self: *Daemon, arena: std.mem.Allocator, name: []const u8, body: []const u8) !Reply {
        const value = parseBody(arena, body) orelse return self.refusal(arena, malformed, .{});
        const correlation = correlationOf(value);
        const request = hub_stdio.Request{
            .id = 0,
            .op = hub_stdio.op_open,
            .adapter = name,
            .payload = value,
            .supplied = .{ .adapter = true, .request = true },
        };
        return self.outcome(arena, try self.frontend.openSession(arena, name, request, true), correlation);
    }

    fn queryFlag(self: *Daemon, arena: std.mem.Allocator, query: []const u8, name: []const u8) !bool {
        _ = self;
        const given = (try queryValue(arena, query, name)) orelse return false;
        return std.mem.eql(u8, given, "true") or std.mem.eql(u8, given, "1");
    }

    fn workStart(self: *Daemon, arena: std.mem.Allocator, name: []const u8, body: []const u8) !Reply {
        const value = parseBody(arena, body) orelse return self.refusal(arena, malformed, .{});
        return self.outcome(arena, try self.frontend.workStart(arena, name, value), .{});
    }

    fn workSend(self: *Daemon, arena: std.mem.Allocator, id: []const u8, body: []const u8) !Reply {
        const value = parseBody(arena, body) orelse return self.refusal(arena, malformed, .{});
        return self.outcome(arena, try self.frontend.workSend(arena, id, value), .{ .session_id = id });
    }

    fn workRead(self: *Daemon, arena: std.mem.Allocator, id: []const u8, query: []const u8) !Reply {
        var after: ?u64 = null;
        if (try queryValue(arena, query, "after")) |text| after = std.fmt.parseInt(u64, text, 10) catch return self.refusal(arena, .{ .code = "invalid_request", .message = "after is a whole number" }, .{ .session_id = id });
        var limit: ?i64 = null;
        if (try queryValue(arena, query, "limit")) |text| limit = std.fmt.parseInt(i64, text, 10) catch 0;
        return self.outcome(arena, try self.frontend.workRead(arena, id, after, limit), .{ .session_id = id });
    }

    fn submit(self: *Daemon, arena: std.mem.Allocator, id: []const u8, body: []const u8) !Reply {
        const value = parseBody(arena, body) orelse return self.refusal(arena, malformed, .{});
        return self.control(arena, try self.frontend.submitControl(arena, id, value), correlationOf(value));
    }

    fn resolve(self: *Daemon, arena: std.mem.Allocator, id: []const u8, body: []const u8) !Reply {
        const value = parseBody(arena, body) orelse return self.refusal(arena, malformed, .{});
        return self.control(arena, try self.frontend.resolveControl(arena, id, value), correlationOf(value));
    }

    fn cancel(self: *Daemon, arena: std.mem.Allocator, id: []const u8, body: []const u8) !Reply {
        const value = parseBody(arena, body) orelse return self.refusal(arena, malformed, .{});
        return self.control(arena, try self.frontend.cancelControl(arena, id, value), correlationOf(value));
    }

    fn settings(self: *Daemon, arena: std.mem.Allocator, id: []const u8, body: []const u8) !Reply {
        const value = parseBody(arena, body) orelse return self.refusal(arena, malformed, .{});
        return self.control(arena, try self.frontend.settingsControl(arena, id, value), correlationOf(value));
    }

    fn events(self: *Daemon, arena: std.mem.Allocator, id: []const u8, request: hub_http.Request) !Reply {
        const scoped = Correlation{ .session_id = id };
        if (!self.frontend.hub.knows(id)) {
            return self.refusal(arena, .{ .code = "unknown_session", .message = try std.fmt.allocPrint(arena, "no session \"{s}\"", .{id}) }, scoped);
        }
        const named_after = try queryValue(arena, request.split.query, "after");
        const cursor = if (named_after) |text| (if (text.len > 0) text else request.last_event_id) else request.last_event_id;
        const run = (try queryValue(arena, request.split.query, "run_id")) orelse "";
        var after: ?u64 = null;
        if (cursor) |text| {
            if (text.len > 0) {
                after = std.fmt.parseInt(u64, text, 10) catch {
                    return self.refusal(arena, .{ .code = "invalid_cursor", .message = try std.fmt.allocPrint(arena, "cursor \"{s}\" is not an unsigned sequence", .{text}) }, scoped);
                };
            }
        }
        if (after == null and run.len > 0) {
            return self.refusal(arena, .{ .code = "invalid_cursor", .message = "run_id names the run a cursor belongs to; it has no meaning without after" }, scoped);
        }
        const subscription = self.frontend.hub.subscribe(arena, id, .{ .run_id = run, .after = after }) catch |err| {
            return self.refusal(arena, try subscribeRefusal(arena, err, id), scoped);
        };
        if (subscription.gap) |gap| {
            subscription.close();
            return .{ .gap = gap };
        }
        return .{ .stream = subscription };
    }

    pub fn leave(self: *Daemon, subscription: *hubmod.Subscription) void {
        _ = self;
        subscription.close();
    }

    pub fn joinedSignal(self: *Daemon, arena: std.mem.Allocator, subscription: *hubmod.Subscription) !?[]const u8 {
        _ = self;
        if (!subscription.joined) return null;
        return try signal(arena, "oap-subscribed", &.{
            .{ .key = "joined_after", .value = .{ .integer = @intCast(subscription.joined_after) } },
            .{ .key = "message", .value = .{ .string = "the subscription begins after this sequence; resubscribe with a cursor at or before it to replay what preceded this point" } },
            .{ .key = "run_id", .value = .{ .string = subscription.joined_run } },
        });
    }

    pub const Fill = enum { open, ended };

    pub fn fill(self: *Daemon, out: *std.ArrayList(u8), subscription: *hubmod.Subscription, budget: usize) !Fill {
        while (out.items.len < budget) {
            const delivered = subscription.next() orelse break;
            var counted: [32]u8 = undefined;
            try out.appendSlice(self.allocator, try std.fmt.bufPrint(&counted, "id: {d}\ndata: ", .{delivered.sequence}));
            try out.appendSlice(self.allocator, delivered.line);
            try out.appendSlice(self.allocator, "\n\n");
        }
        if (out.items.len >= budget) return .open;
        switch (subscription.ending) {
            .open => return .open,
            .overflow => {
                var scratch = std.heap.ArenaAllocator.init(self.allocator);
                defer scratch.deinit();
                const overflow = try signal(scratch.allocator(), "oap-overflow", &.{
                    .{ .key = "last_sequence", .value = .{ .integer = @intCast(subscription.overflow_sequence) } },
                    .{ .key = "message", .value = .{ .string = "event stream consumer fell behind; reconnect with a cursor after this sequence" } },
                    .{ .key = "run_id", .value = .{ .string = subscription.overflow_run } },
                });
                try out.appendSlice(self.allocator, overflow);
                return .ended;
            },
            else => return .ended,
        }
    }
};

pub const sse_head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nCache-Control: no-cache\r\nConnection: close\r\n\r\n";

const Member = struct {
    key: []const u8,
    value: std.json.Value,
};

fn signal(arena: std.mem.Allocator, name: []const u8, members: []const Member) ![]const u8 {
    var object = std.json.ObjectMap.init(arena, &.{}, &.{}) catch return error.OutOfMemory;
    for (members) |member| try object.put(arena, member.key, member.value);
    const data = try json_encode.valueAlloc(arena, .{ .object = object });
    return std.fmt.allocPrint(arena, "event: {s}\ndata: {s}\n\n", .{ name, data });
}

pub fn gapSignal(arena: std.mem.Allocator, gap: contract.Gap) ![]const u8 {
    return signal(arena, "oap-replay-gap", &.{
        .{ .key = "latest_available", .value = .{ .integer = @intCast(gap.latest_available) } },
        .{ .key = "message", .value = .{ .string = "requested replay cursor is no longer retained; reconnect with a cursor at or after oldest_available - 1" } },
        .{ .key = "oldest_available", .value = .{ .integer = @intCast(gap.oldest_available) } },
        .{ .key = "requested_after", .value = .{ .integer = @intCast(gap.requested_after) } },
    });
}

const malformed = hub_stdio.Refusal{ .code = "malformed_json", .message = "the request body is not a JSON envelope" };

fn parseBody(arena: std.mem.Allocator, body: []const u8) ?std.json.Value {
    if (body.len == 0) return null;
    const value = std.json.parseFromSliceLeaky(std.json.Value, arena, body, .{}) catch return null;
    if (value != .object) return null;
    return value;
}

fn stringIn(object: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = object.get(key) orelse return null;
    if (value != .string or value.string.len == 0) return null;
    return value.string;
}

fn correlationOf(value: std.json.Value) Correlation {
    if (value != .object) return .{};
    return .{
        .in_reply_to = stringIn(value.object, "id"),
        .session_id = stringIn(value.object, "session_id"),
        .run_id = stringIn(value.object, "run_id"),
    };
}

fn subscribeRefusal(arena: std.mem.Allocator, err: hubmod.Failure, id: []const u8) !hub_stdio.Refusal {
    return switch (err) {
        error.UnknownSession => .{ .code = "unknown_session", .message = try std.fmt.allocPrint(arena, "no session \"{s}\"", .{id}) },
        error.SessionClosed => .{ .code = "session_closed", .message = "the session is closed" },
        error.NoRunToResume => .{ .code = "no_run_to_resume", .message = "the session has no run to resume a cursor on" },
        error.ReplayCursorFuture => .{ .code = "replay_cursor_future", .message = "the cursor is past the latest sequence the run has emitted" },
        error.RunNotFound => .{ .code = "run_not_found", .message = "the session never had the run the cursor names" },
        error.InvalidCursor => .{ .code = "invalid_cursor", .message = "run_id names the run a cursor belongs to; it has no meaning without after" },
        error.OutOfMemory => error.OutOfMemory,
        else => .{ .code = "internal", .message = @errorName(err) },
    };
}

fn decodeComponent(arena: std.mem.Allocator, raw: []const u8) !?[]const u8 {
    var out = std.ArrayList(u8).empty;
    var index: usize = 0;
    while (index < raw.len) {
        const byte = raw[index];
        if (byte == '+') {
            try out.append(arena, ' ');
            index += 1;
            continue;
        }
        if (byte == '%') {
            if (index + 2 >= raw.len) return null;
            const high = std.fmt.charToDigit(raw[index + 1], 16) catch return null;
            const low = std.fmt.charToDigit(raw[index + 2], 16) catch return null;
            try out.append(arena, high * 16 + low);
            index += 3;
            continue;
        }
        try out.append(arena, byte);
        index += 1;
    }
    return out.items;
}

pub fn queryValues(arena: std.mem.Allocator, query: []const u8, name: []const u8) ![]const []const u8 {
    var found = std.ArrayList([]const u8).empty;
    var pairs = std.mem.splitScalar(u8, query, '&');
    while (pairs.next()) |pair| {
        if (pair.len == 0 or std.mem.indexOfScalar(u8, pair, ';') != null) continue;
        const at = std.mem.indexOfScalar(u8, pair, '=');
        const key = (try decodeComponent(arena, if (at) |cut| pair[0..cut] else pair)) orelse continue;
        if (!std.mem.eql(u8, key, name)) continue;
        const value = (try decodeComponent(arena, if (at) |cut| pair[cut + 1 ..] else "")) orelse continue;
        try found.append(arena, value);
    }
    return found.items;
}

pub fn queryValue(arena: std.mem.Allocator, query: []const u8, name: []const u8) !?[]const u8 {
    const found = try queryValues(arena, query, name);
    if (found.len == 0) return null;
    return found[0];
}

pub fn render(arena: std.mem.Allocator, answer: Answer, body_allowed: bool) ![]const u8 {
    const allow = if (answer.allow.len > 0) try std.fmt.allocPrint(arena, "Allow: {s}\r\n", .{answer.allow}) else "";
    return std.fmt.allocPrint(arena, "HTTP/1.1 {s}\r\nContent-Type: {s}\r\n{s}Connection: close\r\nContent-Length: {d}\r\n\r\n{s}", .{
        answer.status,
        answer.content_type,
        allow,
        answer.body.len,
        if (body_allowed) answer.body else "",
    });
}

pub const no_content_head = "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n";

fn nowMs() u64 {
    return @intCast(@max(compat.time.monotonicMillis() catch 0, 0));
}

const Socket = std.Io.net.Socket.Handle;

const Read = union(enum) {
    bytes: usize,
    waiting,
    closed,
};

fn readSome(handle: Socket, into: []u8) Read {
    const got = std.posix.system.read(handle, into.ptr, into.len);
    return switch (std.posix.errno(got)) {
        .SUCCESS => if (got == 0) .closed else .{ .bytes = @intCast(got) },
        .AGAIN, .INTR => .waiting,
        else => .closed,
    };
}

const Wrote = union(enum) {
    bytes: usize,
    waiting,
    closed,
};

const no_signal: u32 = if (@hasDecl(std.posix.MSG, "NOSIGNAL")) std.posix.MSG.NOSIGNAL else 0;

fn writeSome(handle: Socket, from: []const u8) Wrote {
    const sent = std.posix.system.sendto(handle, from.ptr, from.len, no_signal, null, 0);
    return switch (std.posix.errno(sent)) {
        .SUCCESS => .{ .bytes = @intCast(sent) },
        .AGAIN, .INTR => .waiting,
        else => .closed,
    };
}

fn prepare(handle: Socket) bool {
    const flags = std.posix.system.fcntl(handle, std.posix.F.GETFL, @as(usize, 0));
    if (std.posix.errno(flags) != .SUCCESS) return false;
    const nonblocking: @TypeOf(flags) = 1 << @bitOffsetOf(std.posix.O, "NONBLOCK");
    if (std.posix.errno(std.posix.system.fcntl(handle, std.posix.F.SETFL, flags | nonblocking)) != .SUCCESS) return false;
    if (comptime @hasDecl(std.posix.SO, "NOSIGPIPE")) {
        const on: c_int = 1;
        std.posix.setsockopt(handle, std.posix.SOL.SOCKET, std.posix.SO.NOSIGPIPE, std.mem.asBytes(&on)) catch return false;
    }
    return true;
}

const Phase = enum {
    head,
    body,
    flushing,
    refusing,
    streaming,
    awaiting,
    done,
};

pub const Connection = struct {
    daemon: *Daemon,
    stream: compat.net.Stream,
    scratch: std.heap.ArenaAllocator,
    inbox: std.ArrayList(u8) = .empty,
    request: hub_http.Request = .{},
    body_allowed: bool = true,
    phase: Phase = .head,
    out: std.ArrayList(u8) = .empty,
    sent: usize = 0,
    started_ms: u64,
    progress_ms: u64,
    drain_left: usize = 0,
    drain_started_ms: u64 = 0,
    peer_closed: bool = false,
    subscription: ?*hubmod.Subscription = null,
    stream_ended: bool = false,
    awaiting: ?Deferred = null,

    fn create(daemon: *Daemon, stream: compat.net.Stream) !*Connection {
        const connection = try daemon.allocator.create(Connection);
        const now = nowMs();
        connection.* = .{
            .daemon = daemon,
            .stream = stream,
            .scratch = std.heap.ArenaAllocator.init(daemon.allocator),
            .started_ms = now,
            .progress_ms = now,
        };
        return connection;
    }

    fn destroy(self: *Connection) void {
        const allocator = self.daemon.allocator;
        if (self.subscription) |subscription| self.daemon.leave(subscription);
        if (self.awaiting) |deferred| self.daemon.abandon(deferred.ticket);
        self.stream.close();
        self.inbox.deinit(allocator);
        self.out.deinit(allocator);
        self.scratch.deinit();
        allocator.destroy(self);
    }

    fn handle(self: *const Connection) Socket {
        return compat.net.streamHandle(&self.stream);
    }

    fn pending(self: *const Connection) bool {
        return self.sent < self.out.items.len;
    }

    fn events(self: *const Connection) i16 {
        var wanted: i16 = 0;
        switch (self.phase) {
            .head, .body, .refusing, .streaming, .flushing => if (!self.peer_closed) {
                wanted |= std.posix.POLL.IN;
            },
            .awaiting, .done => {},
        }
        if (self.pending()) wanted |= std.posix.POLL.OUT;
        return wanted;
    }

    fn queue(self: *Connection, bytes: []const u8) void {
        self.out.appendSlice(self.daemon.allocator, bytes) catch {
            self.phase = .done;
        };
    }

    fn refuse(self: *Connection, bytes: []const u8, owed: usize) void {
        self.queue(bytes);
        if (self.phase == .done) return;
        self.phase = .refusing;
        self.drain_left = @min(owed, hub_http.drain_total_cap_bytes);
        self.drain_started_ms = nowMs();
    }

    fn transportFailure(self: *Connection, failure: hub_http.Failure, owed: usize) void {
        const arena = self.scratch.allocator();
        const bytes = hub_http.transportFailureBytes(arena, self.daemon.mint(), failure, self.body_allowed) catch {
            self.phase = .done;
            return;
        };
        self.refuse(bytes, owed);
    }

    fn readable(self: *Connection) void {
        var chunk: [read_chunk_bytes]u8 = undefined;
        switch (self.phase) {
            .head => {
                const room = @min(chunk.len, hub_http.max_header_bytes + 1 - @min(self.inbox.items.len, hub_http.max_header_bytes));
                switch (readSome(self.handle(), chunk[0..room])) {
                    .waiting => return,
                    .closed => {
                        self.peer_closed = true;
                        self.transportFailure(error.Truncated, 0);
                    },
                    .bytes => |count| {
                        self.inbox.appendSlice(self.daemon.allocator, chunk[0..count]) catch {
                            self.phase = .done;
                            return;
                        };
                        self.progress_ms = nowMs();
                        self.takeHead();
                    },
                }
            },
            .body => switch (readSome(self.handle(), self.request.body[self.request.filled..])) {
                .waiting => return,
                .closed => {
                    self.peer_closed = true;
                    self.transportFailure(error.BodyTruncated, 0);
                },
                .bytes => |count| {
                    self.request.filled += count;
                    self.progress_ms = nowMs();
                    if (self.request.filled == self.request.body.len) self.dispatch();
                },
            },
            .refusing => {
                const room = @min(chunk.len, self.drain_left);
                if (room == 0) return;
                switch (readSome(self.handle(), chunk[0..room])) {
                    .waiting => return,
                    .closed => self.peer_closed = true,
                    .bytes => |count| {
                        self.drain_left -= count;
                    },
                }
            },
            .streaming, .flushing, .awaiting => switch (readSome(self.handle(), &chunk)) {
                .waiting, .bytes => return,
                .closed => {
                    self.peer_closed = true;
                    if (self.phase == .streaming or self.phase == .awaiting) self.phase = .done;
                },
            },
            .done => {},
        }
    }

    fn takeHead(self: *Connection) void {
        const at = std.mem.indexOf(u8, self.inbox.items, "\r\n\r\n") orelse {
            if (self.inbox.items.len >= hub_http.max_header_bytes) self.transportFailure(error.HeaderTooLarge, 0);
            return;
        };
        const arena = self.scratch.allocator();
        const head = self.inbox.items[0 .. at + 4];
        const leftover = self.inbox.items[at + 4 ..];
        var declared: usize = 0;
        self.request = hub_http.parseHead(arena, head, &self.body_allowed, &declared) catch |failure| {
            self.transportFailure(failure, declared -| leftover.len);
            return;
        };
        const gate = hub_http.answer(self.daemon.allow, self.request);
        if (gate != .not_found) {
            const bytes = hub_http.answerBytes(arena, self.daemon.mint(), gate, self.body_allowed) catch {
                self.phase = .done;
                return;
            };
            self.refuse(bytes, self.request.content_length -| leftover.len);
            return;
        }
        if (self.request.content_length == 0) return self.dispatch();
        self.request.body = arena.alloc(u8, self.request.content_length) catch {
            self.phase = .done;
            return;
        };
        const taken = @min(leftover.len, self.request.body.len);
        @memcpy(self.request.body[0..taken], leftover[0..taken]);
        self.request.filled = taken;
        if (self.request.filled == self.request.body.len) return self.dispatch();
        self.phase = .body;
    }

    fn dispatch(self: *Connection) void {
        const arena = self.scratch.allocator();
        self.phase = .flushing;
        const reply = self.daemon.respond(arena, self.request) catch {
            self.queue(render(arena, .{ .status = internal_status, .content_type = text_plain, .body = "internal error" }, self.body_allowed) catch "");
            return;
        };
        self.deliver(reply);
    }

    fn deliver(self: *Connection, reply: Reply) void {
        const arena = self.scratch.allocator();
        switch (reply) {
            .deferred => |deferred| {
                self.awaiting = deferred;
                self.phase = .awaiting;
            },
            .answer => |given| self.queue(render(arena, given, self.body_allowed) catch ""),
            .no_content => self.queue(no_content_head),
            .gap => |gap| {
                self.queue(sse_head);
                if (self.phase == .done) return;
                if (self.body_allowed) self.queue(gapSignal(arena, gap) catch "");
            },
            .stream => |subscription| {
                self.queue(sse_head);
                if (self.phase == .done or !self.body_allowed) {
                    self.daemon.leave(subscription);
                    return;
                }
                self.subscription = subscription;
                self.phase = .streaming;
                if (self.daemon.joinedSignal(arena, subscription) catch null) |joined| self.queue(joined);
            },
        }
    }

    fn feed(self: *Connection) void {
        if (self.phase != .streaming or self.stream_ended) return;
        const subscription = self.subscription orelse return;
        if (self.out.items.len - self.sent >= stream_buffer_bytes) return;
        self.compact();
        const filled = self.daemon.fill(&self.out, subscription, self.sent + stream_buffer_bytes) catch {
            self.phase = .done;
            return;
        };
        if (filled == .ended) self.stream_ended = true;
    }

    fn compact(self: *Connection) void {
        if (self.sent == 0) return;
        const rest = self.out.items.len - self.sent;
        std.mem.copyForwards(u8, self.out.items[0..rest], self.out.items[self.sent..]);
        self.out.shrinkRetainingCapacity(rest);
        self.sent = 0;
    }

    fn writable(self: *Connection) void {
        while (self.pending()) {
            switch (writeSome(self.handle(), self.out.items[self.sent..])) {
                .waiting => return,
                .closed => {
                    self.phase = .done;
                    return;
                },
                .bytes => |count| {
                    self.sent += count;
                    self.progress_ms = nowMs();
                },
            }
        }
    }

    fn settle(self: *Connection, now: u64) void {
        switch (self.phase) {
            .head => if (now -| self.started_ms >= @as(u64, @intCast(hub_http.header_read_ms))) self.transportFailure(error.Timeout, 0),
            .body => if (now -| self.progress_ms >= @as(u64, @intCast(hub_http.idle_read_ms))) self.transportFailure(error.Timeout, 0),
            .flushing => {
                if (!self.pending()) self.phase = .done;
            },
            .refusing => {
                if (self.pending()) return;
                if (self.drain_left == 0 or self.peer_closed or now -| self.drain_started_ms >= @as(u64, @intCast(hub_http.drain_total_ms))) self.phase = .done;
            },
            .streaming => {
                if (self.stream_ended and !self.pending()) self.phase = .done;
            },
            .awaiting => {
                const deferred = self.awaiting orelse return;
                const reply = (self.daemon.settleDeferred(self.scratch.allocator(), deferred) catch {
                    self.awaiting = null;
                    self.phase = .flushing;
                    self.queue(render(self.scratch.allocator(), .{ .status = internal_status, .content_type = text_plain, .body = "internal error" }, self.body_allowed) catch "");
                    return;
                }) orelse return;
                self.awaiting = null;
                self.phase = .flushing;
                self.deliver(reply);
            },
            .done => {},
        }
        if (self.phase != .done and self.pending() and now -| self.progress_ms >= @as(u64, @intCast(hub_http.idle_read_ms))) self.phase = .done;
    }
};

pub fn serveListener(daemon: *Daemon, listener: *compat.net.Server, poll_ms: i32) ?std.Io.net.Server.AcceptError {
    if (comptime hub_http.pollable) return serveLoop(daemon, listener, poll_ms) else return serveSerially(daemon, listener);
}

fn serveSerially(daemon: *Daemon, listener: *compat.net.Server) ?std.Io.net.Server.AcceptError {
    defer daemon.stop();
    while (daemon.going().yes()) {
        daemon.pump() catch {};
        const accepted = compat.net.accept(listener) catch |failure| switch (hub_http.classifyAccept(failure)) {
            .serve_again => continue,
            .back_off => {
                compat.time.sleepMs(hub_http.accept_backoff_ms);
                continue;
            },
            .stop => return failure,
        };
        var stream = accepted.stream;
        defer stream.close();
        serveBlocking(daemon, &stream);
    }
    return null;
}

fn serveBlocking(daemon: *Daemon, stream: *compat.net.Stream) void {
    const keep_going = daemon.going();
    var scratch_state = std.heap.ArenaAllocator.init(daemon.allocator);
    defer scratch_state.deinit();
    const scratch = scratch_state.allocator();
    var body_allowed = true;
    var declared: usize = 0;
    var request = hub_http.readHead(scratch, stream, hub_http.header_read_ms, io_cycle_ms, keep_going, &body_allowed, &declared) catch |failure| {
        if (failure == error.Stopped) return;
        hub_http.writeTransportFailure(stream, scratch, daemon.mint(), failure, body_allowed) catch {};
        _ = hub_http.drain(stream, declared, keep_going);
        return;
    };
    const gate = hub_http.answer(daemon.allow, request);
    if (gate != .not_found) {
        hub_http.writeAnswer(stream, scratch, daemon.mint(), gate, body_allowed) catch {};
        _ = hub_http.drain(stream, request.content_length, keep_going);
        return;
    }
    hub_http.readBody(scratch, stream, &request, hub_http.idle_read_ms, io_cycle_ms, keep_going) catch |failure| {
        if (failure == error.Stopped) return;
        hub_http.writeTransportFailure(stream, scratch, daemon.mint(), failure, body_allowed) catch {};
        return;
    };
    const reply = daemon.respond(scratch, request) catch {
        stream.writeAll(render(scratch, .{ .status = internal_status, .content_type = text_plain, .body = "internal error" }, body_allowed) catch return) catch {};
        return;
    };
    var settled = reply;
    while (settled == .deferred) {
        settled = (daemon.settleDeferred(scratch, settled.deferred) catch return) orelse {
            compat.time.sleepMs(2);
            continue;
        };
    }
    switch (settled) {
        .deferred => unreachable,
        .answer => |given| stream.writeAll(render(scratch, given, body_allowed) catch return) catch {},
        .no_content => stream.writeAll(no_content_head) catch {},
        .gap => |gap| {
            stream.writeAll(sse_head) catch return;
            if (body_allowed) stream.writeAll(gapSignal(scratch, gap) catch return) catch {};
        },
        .stream => |subscription| {
            defer daemon.leave(subscription);
            var out = std.ArrayList(u8).empty;
            defer out.deinit(daemon.allocator);
            out.appendSlice(daemon.allocator, sse_head) catch return;
            if (body_allowed) _ = daemon.fill(&out, subscription, stream_buffer_bytes) catch {};
            stream.writeAll(out.items) catch {};
        },
    }
}

fn serveLoop(daemon: *Daemon, listener: *compat.net.Server, poll_ms: i32) ?std.Io.net.Server.AcceptError {
    var connections = std.ArrayList(*Connection).empty;
    defer {
        daemon.stop();
        for (connections.items) |connection| connection.destroy();
        connections.deinit(daemon.allocator);
    }
    var watched = std.ArrayList(std.posix.pollfd).empty;
    defer watched.deinit(daemon.allocator);
    var scratch = std.heap.ArenaAllocator.init(daemon.allocator);
    defer scratch.deinit();
    while (daemon.going().yes()) {
        _ = scratch.reset(.retain_capacity);
        watched.clearRetainingCapacity();
        const accepting = connections.items.len < max_connections;
        watched.append(daemon.allocator, .{ .fd = compat.net.serverHandle(listener), .events = if (accepting) std.posix.POLL.IN else 0, .revents = 0 }) catch return null;
        for (connections.items) |connection| {
            watched.append(daemon.allocator, .{ .fd = connection.handle(), .events = connection.events(), .revents = 0 }) catch return null;
        }
        const children = daemon.frontend.hub.readableHandles(scratch.allocator()) catch &.{};
        for (children) |child| {
            watched.append(daemon.allocator, .{ .fd = child, .events = std.posix.POLL.IN, .revents = 0 }) catch return null;
        }
        _ = std.posix.poll(watched.items, poll_ms) catch 0;
        if (!daemon.going().yes()) break;

        for (connections.items, watched.items[1 .. 1 + connections.items.len]) |connection, polled| {
            if (polled.revents & (std.posix.POLL.IN | std.posix.POLL.HUP | std.posix.POLL.ERR) != 0) connection.readable();
            if (polled.revents & std.posix.POLL.OUT != 0) connection.writable();
        }
        daemon.pump() catch {};
        const now = nowMs();
        for (connections.items) |connection| {
            connection.feed();
            connection.writable();
            connection.settle(now);
        }
        var index: usize = 0;
        var streaming: usize = 0;
        while (index < connections.items.len) {
            if (connections.items[index].phase != .done) {
                streaming += @intFromBool(connections.items[index].phase == .streaming);
                index += 1;
                continue;
            }
            connections.orderedRemove(index).destroy();
        }
        daemon.streams.store(streaming, .release);
        if (accepting and watched.items[0].revents & std.posix.POLL.IN != 0) {
            const accepted = compat.net.accept(listener) catch |failure| switch (hub_http.classifyAccept(failure)) {
                .serve_again => continue,
                .back_off => {
                    compat.time.sleepMs(hub_http.accept_backoff_ms);
                    continue;
                },
                .stop => return failure,
            };
            var stream = accepted.stream;
            if (!prepare(compat.net.streamHandle(&stream))) {
                stream.close();
                continue;
            }
            const connection = Connection.create(daemon, stream) catch {
                stream.close();
                continue;
            };
            connections.append(daemon.allocator, connection) catch {
                connection.destroy();
                continue;
            };
        }
    }
    return null;
}

const testing = std.testing;
const memory = @import("memory");

var test_now_ns: u64 = 1_000 * std.time.ns_per_s;

fn testClock() u64 {
    return test_now_ns;
}

const envelope_head = "\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\"";
const open_demo = "{" ++ envelope_head ++ ",\"type\":\"session.open.request\",\"id\":\"open-1\",\"payload\":{\"session_id\":\"demo\"}}";
const open_held = "{" ++ envelope_head ++ ",\"type\":\"session.open.request\",\"id\":\"open-2\",\"payload\":{\"session_id\":\"held\",\"subscribe\":true}}";
const submit_demo = "{" ++ envelope_head ++ ",\"type\":\"session.message.submit.request\",\"id\":\"submit-1\",\"session_id\":\"demo\",\"payload\":{\"session_id\":\"demo\",\"delivery\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"run\"}]}]}}";
const submit_held = "{" ++ envelope_head ++ ",\"type\":\"session.message.submit.request\",\"id\":\"submit-2\",\"session_id\":\"held\",\"payload\":{\"session_id\":\"held\",\"delivery\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"run\"}]}]}}";
const submit_missing_model = "{" ++ envelope_head ++ ",\"type\":\"session.message.submit.request\",\"id\":\"submit-3\",\"session_id\":\"demo\",\"payload\":{\"session_id\":\"demo\",\"delivery\":\"auto\",\"model_id\":\"model-the-catalog-lacks\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"run\"}]}]}}";
const resolve_permission = "{" ++ envelope_head ++ ",\"type\":\"action.permission.resolve.request\",\"id\":\"resolve-permission-1\",\"session_id\":\"demo\",\"run_id\":\"run-1\",\"payload\":{\"interaction_id\":\"permission-2\",\"session_id\":\"demo\",\"run_id\":\"run-1\",\"requested_by\":\"reference.memory\",\"responded_by\":\"user\",\"choice_id\":\"approve\",\"granted\":true}}";
const resolve_input = "{" ++ envelope_head ++ ",\"type\":\"user.input.resolve.request\",\"id\":\"resolve-input-1\",\"session_id\":\"demo\",\"run_id\":\"run-1\",\"payload\":{\"interaction_id\":\"input-3\",\"session_id\":\"demo\",\"run_id\":\"run-1\",\"requested_by\":\"reference.memory\",\"responded_by\":\"user\",\"answers\":[{\"question_id\":\"choice\",\"selected_option_ids\":[\"yes\"]}]}}";
const cancel_demo = "{" ++ envelope_head ++ ",\"type\":\"run.cancel.request\",\"id\":\"cancel-1\",\"session_id\":\"demo\",\"run_id\":\"run-1\",\"payload\":{\"session_id\":\"demo\",\"run_id\":\"run-1\"}}";
const compact_demo = "{" ++ envelope_head ++ ",\"type\":\"session.compact.request\",\"id\":\"compact-1\",\"session_id\":\"demo\",\"payload\":{\"session_id\":\"demo\",\"focus\":\"the release plan\"}}";
const compact_steer = "{" ++ envelope_head ++ ",\"type\":\"session.compact.request\",\"id\":\"compact-2\",\"session_id\":\"demo\",\"payload\":{\"session_id\":\"demo\",\"delivery\":\"steer\"}}";
const compact_elsewhere = "{" ++ envelope_head ++ ",\"type\":\"session.compact.request\",\"id\":\"compact-3\",\"session_id\":\"demo\",\"payload\":{\"session_id\":\"other\"}}";
const cancel_elsewhere = "{" ++ envelope_head ++ ",\"type\":\"run.cancel.request\",\"id\":\"cancel-2\",\"session_id\":\"other\",\"run_id\":\"run-1\",\"payload\":{\"session_id\":\"other\",\"run_id\":\"run-1\"}}";

const Fixture = struct {
    adapter: memory.Adapter,
    core: hubmod.Hub,
    daemon: Daemon,
    scratch: std.heap.ArenaAllocator,

    fn init(self: *Fixture, options: hubmod.Options) !void {
        self.adapter = memory.Adapter.init(testing.allocator);
        self.core = hubmod.Hub.init(testing.allocator, testClock, options);
        errdefer self.core.deinit();
        try self.core.register("memory", self.adapter.adapter());
        self.daemon = try Daemon.init(testing.allocator, &self.core, &.{"127.0.0.1"}, hub_http.always_going);
        self.scratch = std.heap.ArenaAllocator.init(testing.allocator);
    }

    fn deinit(self: *Fixture) void {
        self.scratch.deinit();
        self.daemon.deinit();
        self.core.deinit();
        self.adapter.deinit();
    }

    fn ask(self: *Fixture, method: []const u8, target: []const u8, body: []const u8) !Reply {
        const arena = self.scratch.allocator();
        const split = try hub_http.splitTarget(arena, target);
        const request = hub_http.Request{
            .method = method,
            .target = target,
            .split = split,
            .host = "127.0.0.1",
            .content_type = "application/json",
            .content_length = body.len,
            .filled = body.len,
            .body = try arena.dupe(u8, body),
        };
        return self.daemon.respond(arena, request);
    }

    fn answer(self: *Fixture, method: []const u8, target: []const u8, body: []const u8) !Answer {
        const reply = try self.ask(method, target, body);
        return reply.answer;
    }

    fn json(self: *Fixture, given: Answer) !std.json.ObjectMap {
        const value = try std.json.parseFromSliceLeaky(std.json.Value, self.scratch.allocator(), given.body, .{});
        return value.object;
    }

    fn code(self: *Fixture, given: Answer) ![]const u8 {
        const root = try self.json(given);
        return root.get("payload").?.object.get("error").?.object.get("code").?.string;
    }

    fn kind(self: *Fixture, given: Answer) ![]const u8 {
        const root = try self.json(given);
        return root.get("type").?.string;
    }

    fn pump(self: *Fixture) !void {
        try self.core.pump(testing.allocator, 0);
    }
};

test "the two listings answer plain JSON, and a probe answers an envelope correlated to a minted request" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();

    const adapters = try fixture.answer("GET", "/adapters", "");
    try testing.expectEqualStrings("200 OK", adapters.status);
    const listed = (try fixture.json(adapters)).get("adapters").?.array.items;
    try testing.expectEqualStrings("memory", listed[0].object.get("name").?.string);

    const sessions = try fixture.answer("GET", "/sessions", "");
    try testing.expectEqualStrings("200 OK", sessions.status);
    try testing.expectEqual(@as(usize, 0), (try fixture.json(sessions)).get("sessions").?.array.items.len);

    const probed = try fixture.answer("GET", "/adapters/memory/capabilities", "");
    try testing.expectEqualStrings("200 OK", probed.status);
    try testing.expectEqualStrings("capabilities.response", try fixture.kind(probed));
    try testing.expect(std.mem.startsWith(u8, (try fixture.json(probed)).get("in_reply_to").?.string, "oap-request-"));

    const unknown = try fixture.answer("GET", "/adapters/nope/capabilities", "");
    try testing.expectEqualStrings("404 Not Found", unknown.status);
    try testing.expectEqualStrings("unknown_adapter", try fixture.code(unknown));
}

test "an open answers with the request's own id, and its refusal is an error envelope correlated to that id" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();

    const opened = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);
    try testing.expectEqualStrings("200 OK", opened.status);
    try testing.expectEqualStrings("session.open.response", try fixture.kind(opened));
    try testing.expectEqualStrings("open-1", (try fixture.json(opened)).get("in_reply_to").?.string);

    const refused = try fixture.answer("POST", "/adapters/nope/sessions", open_demo);
    try testing.expectEqualStrings("404 Not Found", refused.status);
    try testing.expectEqualStrings("unknown_adapter", try fixture.code(refused));
    try testing.expectEqualStrings("open-1", (try fixture.json(refused)).get("in_reply_to").?.string);

    const again = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);
    try testing.expectEqualStrings("409 Conflict", again.status);
    try testing.expectEqualStrings("session_exists", try fixture.code(again));
}

test "a body that is not a JSON envelope is malformed_json, and an envelope of the wrong type is type_mismatch" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();
    _ = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);

    const garbled = try fixture.answer("POST", "/sessions/demo/submit", "{not json");
    try testing.expectEqualStrings("400 Bad Request", garbled.status);
    try testing.expectEqualStrings("malformed_json", try fixture.code(garbled));

    const empty = try fixture.answer("POST", "/sessions/demo/submit", "");
    try testing.expectEqualStrings("malformed_json", try fixture.code(empty));

    const mistyped = try fixture.answer("POST", "/sessions/demo/submit", cancel_demo);
    try testing.expectEqualStrings("400 Bad Request", mistyped.status);
    try testing.expectEqualStrings("type_mismatch", try fixture.code(mistyped));
    try testing.expectEqualStrings("cancel-1", (try fixture.json(mistyped)).get("in_reply_to").?.string);
}

test "a submit admits a run, and a model the catalog lacks is refused naming that model" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();
    _ = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);

    const admitted = try fixture.answer("POST", "/sessions/demo/submit", submit_demo);
    try testing.expectEqualStrings("200 OK", admitted.status);
    try testing.expectEqualStrings("session.message.submit.response", try fixture.kind(admitted));
    const root = try fixture.json(admitted);
    try testing.expectEqualStrings("submit-1", root.get("in_reply_to").?.string);
    try testing.expectEqualStrings("run-1", root.get("run_id").?.string);

    const missing = try fixture.answer("POST", "/sessions/demo/submit", submit_missing_model);
    try testing.expectEqualStrings("400 Bad Request", missing.status);
    try testing.expectEqualStrings("model_not_found", try fixture.code(missing));
    const details = (try fixture.json(missing)).get("payload").?.object.get("error").?.object.get("details").?.object;
    try testing.expectEqualStrings("model-the-catalog-lacks", details.get("model_id").?.string);

    const unknown = try fixture.answer("POST", "/sessions/absent/submit", submit_demo);
    try testing.expectEqualStrings("404 Not Found", unknown.status);
    try testing.expectEqualStrings("unknown_session", try fixture.code(unknown));
}

test "a compaction is admitted on the submit route and answered as a session.compact.response" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();
    _ = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);

    const admitted = try fixture.answer("POST", "/sessions/demo/submit", compact_demo);
    try testing.expectEqualStrings("200 OK", admitted.status);
    try testing.expectEqualStrings("session.compact.response", try fixture.kind(admitted));
    const root = try fixture.json(admitted);
    try testing.expectEqualStrings("compact-1", root.get("in_reply_to").?.string);
    try testing.expectEqualStrings("run-1", root.get("run_id").?.string);
    try testing.expectEqualStrings("started", root.get("payload").?.object.get("admission").?.string);

    const elsewhere = try fixture.answer("POST", "/sessions/demo/submit", compact_elsewhere);
    try testing.expectEqualStrings("400 Bad Request", elsewhere.status);
    try testing.expectEqualStrings("scope_mismatch", try fixture.code(elsewhere));
}

test "a compaction the adapter refuses answers with the submit route's refusal" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();
    _ = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);
    _ = try fixture.answer("POST", "/sessions/demo/submit", submit_demo);

    const steered = try fixture.answer("POST", "/sessions/demo/submit", compact_steer);
    try testing.expectEqualStrings("400 Bad Request", steered.status);
    try testing.expectEqualStrings("unsupported_feature", try fixture.code(steered));
    try testing.expectEqualStrings("compact-2", (try fixture.json(steered)).get("in_reply_to").?.string);
}

test "a resolve answers the response its request's type selects, and a second answer to one gate is rejected" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();
    _ = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);
    _ = try fixture.answer("POST", "/sessions/demo/submit", submit_demo);
    try fixture.pump();

    const permitted = try fixture.answer("POST", "/sessions/demo/resolve", resolve_permission);
    try testing.expectEqualStrings("200 OK", permitted.status);
    try testing.expectEqualStrings("action.permission.resolve.response", try fixture.kind(permitted));
    try testing.expectEqualStrings("run-1", (try fixture.json(permitted)).get("run_id").?.string);
    try fixture.pump();

    const answered = try fixture.answer("POST", "/sessions/demo/resolve", resolve_input);
    try testing.expectEqualStrings("200 OK", answered.status);
    try testing.expectEqualStrings("user.input.resolve.response", try fixture.kind(answered));

    const twice = try fixture.answer("POST", "/sessions/demo/resolve", resolve_input);
    try testing.expectEqualStrings("409 Conflict", twice.status);
    try testing.expectEqualStrings("resolution_rejected", try fixture.code(twice));
    try testing.expectEqualStrings("run-1", (try fixture.json(twice)).get("run_id").?.string);
}

test "a settings update is answered on its own route, and one naming nothing, an unadvertised setting or an absent session is refused" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();
    _ = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);

    const updated = try fixture.answer("POST", "/sessions/demo/settings", "{" ++ envelope_head ++ ",\"type\":\"session.settings.update.request\",\"id\":\"set-1\",\"session_id\":\"demo\",\"payload\":{\"session_id\":\"demo\",\"compaction_policy\":{\"kind\":\"share\",\"share_percent\":60}}}");
    try testing.expectEqualStrings("200 OK", updated.status);
    try testing.expectEqualStrings("session.settings.update.response", try fixture.kind(updated));
    const policy = (try fixture.json(updated)).get("payload").?.object.get("compaction_policy").?.object;
    try testing.expectEqualStrings("share", policy.get("kind").?.string);
    try testing.expectEqual(@as(i64, 60), policy.get("share_percent").?.integer);

    const empty = try fixture.answer("POST", "/sessions/demo/settings", "{" ++ envelope_head ++ ",\"type\":\"session.settings.update.request\",\"id\":\"set-2\",\"session_id\":\"demo\",\"payload\":{\"session_id\":\"demo\"}}");
    try testing.expectEqualStrings("400 Bad Request", empty.status);
    try testing.expectEqualStrings("schema_invalid", try fixture.code(empty));

    const level = try fixture.answer("POST", "/sessions/demo/settings", "{" ++ envelope_head ++ ",\"type\":\"session.settings.update.request\",\"id\":\"set-3\",\"session_id\":\"demo\",\"payload\":{\"session_id\":\"demo\",\"reasoning_level\":\"high\"}}");
    try testing.expectEqualStrings("400 Bad Request", level.status);
    try testing.expectEqualStrings("unsupported_feature", try fixture.code(level));

    const absent = try fixture.answer("POST", "/sessions/absent/settings", "{" ++ envelope_head ++ ",\"type\":\"session.settings.update.request\",\"id\":\"set-4\",\"session_id\":\"absent\",\"payload\":{\"session_id\":\"absent\",\"compaction_policy\":{\"kind\":\"off\"}}}");
    try testing.expectEqualStrings("404 Not Found", absent.status);
    try testing.expectEqualStrings("unknown_session", try fixture.code(absent));
}

test "a cancel naming another session is scope_mismatch, a live run's is accepted, and a completed run's is run_terminal" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();
    _ = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);
    _ = try fixture.answer("POST", "/sessions/demo/submit", submit_demo);

    const elsewhere = try fixture.answer("POST", "/sessions/demo/cancel", cancel_elsewhere);
    try testing.expectEqualStrings("400 Bad Request", elsewhere.status);
    try testing.expectEqualStrings("scope_mismatch", try fixture.code(elsewhere));

    try fixture.pump();
    _ = try fixture.answer("POST", "/sessions/demo/resolve", resolve_permission);
    try fixture.pump();
    _ = try fixture.answer("POST", "/sessions/demo/resolve", resolve_input);
    try fixture.pump();

    const settled = try fixture.answer("POST", "/sessions/demo/cancel", cancel_demo);
    try testing.expectEqualStrings("409 Conflict", settled.status);
    try testing.expectEqualStrings("run_terminal", try fixture.code(settled));
    try testing.expectEqualStrings("run-1", (try fixture.json(settled)).get("run_id").?.string);

    const second = try fixture.answer("POST", "/sessions/demo/submit", submit_demo);
    const run = (try fixture.json(second)).get("run_id").?.string;
    const cancel_live = try std.fmt.allocPrint(fixture.scratch.allocator(), "{{" ++ envelope_head ++ ",\"type\":\"run.cancel.request\",\"id\":\"cancel-3\",\"session_id\":\"demo\",\"run_id\":\"{s}\",\"payload\":{{\"session_id\":\"demo\",\"run_id\":\"{s}\"}}}}", .{ run, run });
    const live = try fixture.answer("POST", "/sessions/demo/cancel", cancel_live);
    try testing.expectEqualStrings("200 OK", live.status);
    try testing.expectEqualStrings("run.cancel.response", try fixture.kind(live));

    const absent = try fixture.answer("POST", "/sessions/absent/cancel", cancel_demo);
    try testing.expectEqualStrings("404 Not Found", absent.status);
    try testing.expectEqualStrings("unknown_session", try fixture.code(absent));
}

test "a close answers no content, and the session is unknown to every route after it" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();
    _ = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);

    const state = try fixture.answer("GET", "/sessions/demo/state", "");
    try testing.expectEqualStrings("session.state.response", try fixture.kind(state));
    const tools = try fixture.answer("GET", "/sessions/demo/tools", "");
    try testing.expectEqualStrings("action.tools.list.response", try fixture.kind(tools));
    const models = try fixture.answer("GET", "/sessions/demo/models?allow_degraded=models.list", "");
    try testing.expectEqualStrings("models.response", try fixture.kind(models));

    const closed = try fixture.ask("POST", "/sessions/demo/close", "");
    try testing.expect(closed == .no_content);
    for ([_][]const u8{ "/sessions/demo/state", "/sessions/demo/tools", "/sessions/demo/models", "/sessions/demo/events" }) |target| {
        const gone = try fixture.answer("GET", target, "");
        try testing.expectEqualStrings("404 Not Found", gone.status);
        try testing.expectEqualStrings("unknown_session", try fixture.code(gone));
    }
    const twice = try fixture.answer("POST", "/sessions/demo/close", "");
    try testing.expectEqualStrings("unknown_session", try fixture.code(twice));
}

test "a close of a session with a run in flight is run_active" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();
    _ = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);
    _ = try fixture.answer("POST", "/sessions/demo/submit", submit_demo);

    const busy = try fixture.answer("POST", "/sessions/demo/close", "");
    try testing.expectEqualStrings("409 Conflict", busy.status);
    try testing.expectEqualStrings("run_active", try fixture.code(busy));
}

test "a known path asked with the wrong method is 405 naming the method it takes, and an unknown path is a plain 404" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();

    const wrong = try fixture.answer("POST", "/adapters", "");
    try testing.expectEqualStrings("405 Method Not Allowed", wrong.status);
    try testing.expectEqualStrings("GET", wrong.allow);

    const nowhere = try fixture.answer("GET", "/nowhere", "");
    try testing.expectEqualStrings("404 Not Found", nowhere.status);
    try testing.expectEqualStrings("not found", nowhere.body);

    const headed = try fixture.answer("HEAD", "/adapters", "");
    try testing.expectEqualStrings("200 OK", headed.status);
}

test "a work list that asks the adapters for their own sessions is deferred, and settles into its answer once the listing is done" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();
    const reply = try fixture.ask("GET", "/work?include_native=true", "");
    try testing.expect(reply == .deferred);
    const arena = fixture.scratch.allocator();
    var settled: ?Reply = null;
    var waits: usize = 0;
    while (settled == null and waits < 5000) : (waits += 1) {
        settled = try fixture.daemon.settleDeferred(arena, reply.deferred);
        if (settled == null) std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
    try testing.expectEqualStrings("200 OK", settled.?.answer.status);
    try testing.expectEqualStrings("{\"groups\":[]}", settled.?.answer.body);
    try testing.expectEqual(@as(usize, 0), fixture.daemon.frontend.jobs.items.len);
}

test "events refuse an unknown session, a cursor that is not a sequence, a run with no cursor, and a cursor on a session with no run" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();
    _ = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);

    const cases = [_]struct { target: []const u8, status: []const u8, code: []const u8 }{
        .{ .target = "/sessions/absent/events", .status = "404 Not Found", .code = "unknown_session" },
        .{ .target = "/sessions/demo/events?after=ten", .status = "400 Bad Request", .code = "invalid_cursor" },
        .{ .target = "/sessions/demo/events?run_id=run-1", .status = "400 Bad Request", .code = "invalid_cursor" },
        .{ .target = "/sessions/demo/events?after=0", .status = "409 Conflict", .code = "no_run_to_resume" },
    };
    for (cases) |case| {
        const refused = try fixture.answer("GET", case.target, "");
        try testing.expectEqualStrings(case.status, refused.status);
        try testing.expectEqualStrings(case.code, try fixture.code(refused));
    }
    _ = try fixture.answer("POST", "/sessions/demo/submit", submit_demo);
    try fixture.pump();
    const never = try fixture.answer("GET", "/sessions/demo/events?after=0&run_id=run-9", "");
    try testing.expectEqualStrings("404 Not Found", never.status);
    try testing.expectEqualStrings("run_not_found", try fixture.code(never));
    const future = try fixture.answer("GET", "/sessions/demo/events?after=999", "");
    try testing.expectEqualStrings("400 Bad Request", future.status);
    try testing.expectEqualStrings("replay_cursor_future", try fixture.code(future));
}

test "a cursor reads the query first and Last-Event-ID only when the query names none" {
    var fixture: Fixture = undefined;
    try fixture.init(.{ .journal_capacity = 256 });
    defer fixture.deinit();
    _ = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);
    _ = try fixture.answer("POST", "/sessions/demo/submit", submit_demo);
    try fixture.pump();

    const arena = fixture.scratch.allocator();
    var request = hub_http.Request{ .method = "GET", .split = try hub_http.splitTarget(arena, "/sessions/demo/events?after=2"), .last_event_id = "nonsense" };
    const by_query = try fixture.daemon.respond(arena, request);
    try testing.expectEqual(@as(u64, 3), by_query.stream.next().?.sequence);
    fixture.daemon.leave(by_query.stream);

    request.split = try hub_http.splitTarget(arena, "/sessions/demo/events");
    request.last_event_id = "1";
    const by_header = try fixture.daemon.respond(arena, request);
    try testing.expectEqual(@as(u64, 2), by_header.stream.next().?.sequence);
    fixture.daemon.leave(by_header.stream);
}

test "a cursor the journal no longer holds is answered as a replay gap naming what is kept" {
    var fixture: Fixture = undefined;
    try fixture.init(.{ .journal_capacity = 2 });
    defer fixture.deinit();
    _ = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);
    _ = try fixture.answer("POST", "/sessions/demo/submit", submit_demo);
    try fixture.pump();

    const reply = try fixture.ask("GET", "/sessions/demo/events?after=0", "");
    const gap = reply.gap;
    try testing.expectEqual(@as(u64, 0), gap.requested_after);
    try testing.expect(gap.oldest_available > 1);
    const framed = try gapSignal(fixture.scratch.allocator(), gap);
    try testing.expect(std.mem.startsWith(u8, framed, "event: oap-replay-gap\ndata: {"));
    try testing.expect(std.mem.endsWith(u8, framed, "}\n\n"));
}

test "a subscribing open holds its subscription, and the events request with no cursor adopts it from the first envelope" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();

    const opened = try fixture.answer("POST", "/adapters/memory/sessions", open_held);
    try testing.expectEqualStrings("200 OK", opened.status);
    try testing.expectEqual(@as(usize, 1), fixture.core.holds.items.len);
    _ = try fixture.answer("POST", "/sessions/held/submit", submit_held);
    try fixture.pump();

    const reply = try fixture.ask("GET", "/sessions/held/events", "");
    try testing.expectEqual(@as(usize, 0), fixture.core.holds.items.len);
    const first = reply.stream.next().?;
    try testing.expectEqual(@as(u64, 1), first.sequence);
    try testing.expect(std.mem.indexOf(u8, first.line, "run.started") != null);
    fixture.daemon.leave(reply.stream);
}

test "a query is split on ampersands alone, every repetition is read decoded, and a pair with a semicolon or a bad escape is dropped, as Go reads it" {
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const found = try queryValues(arena, "allow_degraded=models.list&x=1&allow_degraded=a%2Fb+c", "allow_degraded");
    try testing.expectEqual(@as(usize, 2), found.len);
    try testing.expectEqualStrings("models.list", found[0]);
    try testing.expectEqualStrings("a/b c", found[1]);
    try testing.expectEqual(@as(?[]const u8, null), try queryValue(arena, "", "after"));
    try testing.expectEqual(@as(?[]const u8, null), try queryValue(arena, "after=1;run_id=x", "after"));
    try testing.expectEqual(@as(?[]const u8, null), try queryValue(arena, "after=1;run_id=x", "run_id"));
    try testing.expectEqualStrings("3", (try queryValue(arena, "after=1;x&after=3", "after")).?);
    try testing.expectEqual(@as(?[]const u8, null), try queryValue(arena, "after=%zz", "after"));
    try testing.expectEqualStrings("2", (try queryValue(arena, "after=%zz&after=2", "after")).?);
    try testing.expectEqualStrings("", (try queryValue(arena, "after", "after")).?);
}

test "reading a query survives every allocation failing, and leaks nothing" {
    try testing.checkAllAllocationFailures(testing.allocator, struct {
        fn drive(allocator: std.mem.Allocator) !void {
            var arena_state = std.heap.ArenaAllocator.init(allocator);
            defer arena_state.deinit();
            _ = try queryValues(arena_state.allocator(), "allow_degraded=a%2Fb&allow_degraded=c", "allow_degraded");
        }
    }.drive, .{});
}

const Served = struct {
    fixture: Fixture,
    listener: compat.net.Server,
    thread: std.Thread,
    address: compat.net.Address,

    fn prepare(self: *Served) !void {
        try self.fixture.init(.{ .journal_capacity = 256 });
        errdefer self.fixture.deinit();
        const local = try compat.net.resolveAddress(testing.allocator, "127.0.0.1", 0);
        self.listener = try compat.net.tcpListen(local, .{ .reuse_address = true });
        self.address = compat.net.listenAddress(&self.listener);
    }

    fn serve(self: *Served) !void {
        self.thread = try std.Thread.spawn(.{}, loop, .{self});
    }

    fn loop(self: *Served) void {
        _ = serveListener(&self.fixture.daemon, &self.listener, 5);
    }

    fn stop(self: *Served) void {
        self.fixture.daemon.stop();
        self.thread.join();
        compat.net.closeServer(&self.listener);
        self.fixture.deinit();
    }

    fn dial(self: *Served) !compat.net.Stream {
        return compat.net.tcpConnect(self.address);
    }

    fn streams(self: *Served) usize {
        return self.fixture.daemon.streams.load(.acquire);
    }
};

fn readFor(stream: *compat.net.Stream, into: *std.ArrayList(u8), wanted: []const u8, budget_ms: i64) !bool {
    const until = compat.time.nowMillis() + budget_ms;
    var buffer: [4096]u8 = undefined;
    while (compat.time.nowMillis() < until) {
        if (std.mem.indexOf(u8, into.items, wanted) != null) return true;
        const ready = try compat.net.readableWithin(compat.net.streamHandle(stream), 20);
        if (!ready) continue;
        const n = try stream.readSome(&buffer);
        if (n == 0) return std.mem.indexOf(u8, into.items, wanted) != null;
        try into.appendSlice(testing.allocator, buffer[0..n]);
    }
    return std.mem.indexOf(u8, into.items, wanted) != null;
}

fn waitForStreams(served: *Served, wanted: usize, budget_ms: i64) bool {
    const until = compat.time.nowMillis() + budget_ms;
    while (compat.time.nowMillis() < until) {
        if (served.streams() == wanted) return true;
        compat.time.sleepMs(5);
    }
    return served.streams() == wanted;
}

fn post(path: []const u8, body: []const u8) ![]const u8 {
    return std.fmt.allocPrint(testing.allocator, "POST {s} HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: {d}\r\n\r\n{s}", .{ path, body.len, body });
}

test "an open event stream does not hold the daemon: another connection is answered while it streams" {
    if (comptime !hub_http.pollable) return error.SkipZigTest;
    var served: Served = undefined;
    try served.prepare();
    _ = try served.fixture.answer("POST", "/adapters/memory/sessions", open_demo);
    try served.serve();
    defer served.stop();

    var stream = try served.dial();
    defer stream.close();
    try stream.writeAll("GET /sessions/demo/events HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n");
    var streamed = std.ArrayList(u8).empty;
    defer streamed.deinit(testing.allocator);
    try testing.expect(try readFor(&stream, &streamed, "text/event-stream", 2000));

    var other = try served.dial();
    defer other.close();
    try other.writeAll("GET /adapters HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n");
    var listed = std.ArrayList(u8).empty;
    defer listed.deinit(testing.allocator);
    try testing.expect(try readFor(&other, &listed, "\"name\":\"memory\"", 2000));
    try testing.expect(std.mem.startsWith(u8, listed.items, "HTTP/1.1 200 OK\r\n"));

    var submitter = try served.dial();
    defer submitter.close();
    const submit = try post("/sessions/demo/submit", submit_demo);
    defer testing.allocator.free(submit);
    try submitter.writeAll(submit);
    var admitted = std.ArrayList(u8).empty;
    defer admitted.deinit(testing.allocator);
    try testing.expect(try readFor(&submitter, &admitted, "session.message.submit.response", 2000));
    try testing.expect(try readFor(&stream, &streamed, "id: 1\ndata: {", 2000));
    try testing.expect(std.mem.indexOf(u8, streamed.items, "run.started") != null);
}

test "a client that hangs up mid-stream releases its stream without an event to write" {
    if (comptime !hub_http.pollable) return error.SkipZigTest;
    var served: Served = undefined;
    try served.prepare();
    _ = try served.fixture.answer("POST", "/adapters/memory/sessions", open_demo);
    try served.serve();
    defer served.stop();

    var stream = try served.dial();
    try stream.writeAll("GET /sessions/demo/events HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n");
    var streamed = std.ArrayList(u8).empty;
    defer streamed.deinit(testing.allocator);
    try testing.expect(try readFor(&stream, &streamed, "text/event-stream", 2000));
    try testing.expect(waitForStreams(&served, 1, 2000));
    stream.close();
    try testing.expect(waitForStreams(&served, 0, 2000));
}

test "a body cut short by a half-close is refused request_read by the loop" {
    if (comptime !hub_http.pollable) return error.SkipZigTest;
    var served: Served = undefined;
    try served.prepare();
    try served.serve();
    defer served.stop();

    var stream = try served.dial();
    defer stream.close();
    try stream.writeAll("POST /adapters/memory/sessions HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: 64\r\n\r\n{\"a\":");
    stream.shutdownHow(.send);
    var answered = std.ArrayList(u8).empty;
    defer answered.deinit(testing.allocator);
    try testing.expect(try readFor(&stream, &answered, "request_read", 2000));
    try testing.expect(std.mem.startsWith(u8, answered.items, "HTTP/1.1 400 Bad Request\r\n"));
}

test "stopping the daemon ends an open stream rather than waiting it out" {
    if (comptime !hub_http.pollable) return error.SkipZigTest;
    var served: Served = undefined;
    try served.prepare();
    _ = try served.fixture.answer("POST", "/adapters/memory/sessions", open_demo);
    try served.serve();

    var stream = try served.dial();
    defer stream.close();
    try stream.writeAll("GET /sessions/demo/events HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n");
    var streamed = std.ArrayList(u8).empty;
    defer streamed.deinit(testing.allocator);
    try testing.expect(try readFor(&stream, &streamed, "text/event-stream", 2000));
    try testing.expect(waitForStreams(&served, 1, 2000));

    const before = compat.time.nowMillis();
    served.stop();
    const took = compat.time.nowMillis() - before;
    try testing.expect(took < 1000);
}

test "the connection bound is the number the draft's G13 row records" {
    try testing.expectEqual(@as(usize, 64), max_connections);
}

test "a reopen is answered with the revision it was gated under, though it cited none" {
    var fixture: Fixture = undefined;
    try fixture.init(.{});
    defer fixture.deinit();
    _ = try fixture.answer("POST", "/adapters/memory/sessions", open_demo);
    const closed = try fixture.ask("POST", "/sessions/demo/close", "");
    try testing.expect(closed == .no_content);
    const reopen = "{" ++ envelope_head ++ ",\"type\":\"session.open.request\",\"id\":\"open-2\",\"payload\":{\"session_id\":\"demo\",\"reopen\":true}}";
    const reopened = try fixture.answer("POST", "/adapters/memory/sessions", reopen);
    try testing.expectEqualStrings("session.open.response", try fixture.kind(reopened));
    const root = try fixture.json(reopened);
    try testing.expectEqualStrings(memory.capability_revision, root.get("capability_revision").?.string);
    try testing.expect(root.get("payload").?.object.get("recovery").?.object.get("recovered").?.bool);
}

fn openNamed(arena: std.mem.Allocator, session_id: []const u8) ![]const u8 {
    return std.fmt.allocPrint(arena, "{{" ++ envelope_head ++ ",\"type\":\"session.open.request\",\"id\":\"open-{s}\",\"payload\":{{\"session_id\":\"{s}\"}}}}", .{ session_id, session_id });
}

test "the session history lists what the store recorded, newest first, and pages on its cursor" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const cwd = try std.process.currentPathAlloc(testing.io, testing.allocator);
    defer testing.allocator.free(cwd);
    const path = try std.fs.path.join(testing.allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "sessions.jsonl" });
    defer testing.allocator.free(path);
    var store = try hubmod.binding.Store.open(testing.allocator, path);
    defer store.deinit();
    var fixture: Fixture = undefined;
    try fixture.init(.{ .bindings = &store });
    defer fixture.deinit();
    const arena = fixture.scratch.allocator();

    for ([_][]const u8{ "history-a", "history-b", "history-c" }) |id| {
        const opened = try fixture.answer("POST", "/adapters/memory/sessions", try openNamed(arena, id));
        try testing.expectEqualStrings("200 OK", opened.status);
    }
    const closed = try fixture.ask("POST", "/sessions/history-b/close", "");
    try testing.expect(closed == .no_content);

    const whole = try fixture.answer("GET", "/sessions/history", "");
    try testing.expectEqualStrings("200 OK", whole.status);
    const listed = (try fixture.json(whole)).get("sessions").?.array.items;
    try testing.expectEqual(@as(usize, 3), listed.len);
    var ids: [3][]const u8 = undefined;
    for (listed, 0..) |entry, index| {
        const id = entry.object.get("session_id").?.string;
        ids[index] = id;
        const want: []const u8 = if (std.mem.eql(u8, id, "history-b")) "closed" else "live";
        try testing.expectEqualStrings(want, entry.object.get("state").?.string);
        try testing.expectEqualStrings("memory", entry.object.get("adapter").?.string);
        if (index == 0) continue;
        const earlier = listed[index - 1].object;
        const was = earlier.get("updated_at_ms").?.integer;
        const now = entry.object.get("updated_at_ms").?.integer;
        try testing.expect(was > now or (was == now and std.mem.lessThan(u8, earlier.get("session_id").?.string, id)));
    }
    try testing.expect((try fixture.json(whole)).get("next_cursor") == null);

    var cursor: ?[]const u8 = null;
    var paged: usize = 0;
    while (paged < 4) {
        const target = if (cursor) |given| try std.fmt.allocPrint(arena, "/sessions/history?limit=1&cursor={s}", .{given}) else "/sessions/history?limit=1";
        const page = try fixture.json(try fixture.answer("GET", target, ""));
        const entries = page.get("sessions").?.array.items;
        try testing.expectEqual(@as(usize, 1), entries.len);
        try testing.expectEqualStrings(ids[paged], entries[0].object.get("session_id").?.string);
        paged += 1;
        cursor = if (page.get("next_cursor")) |next| next.string else break;
    }
    try testing.expectEqual(@as(usize, 3), paged);
}

test "the session history refuses a cursor, a limit and a missing store as Go does" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const cwd = try std.process.currentPathAlloc(testing.io, testing.allocator);
    defer testing.allocator.free(cwd);
    const path = try std.fs.path.join(testing.allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "sessions.jsonl" });
    defer testing.allocator.free(path);
    var store = try hubmod.binding.Store.open(testing.allocator, path);
    defer store.deinit();
    var fixture: Fixture = undefined;
    try fixture.init(.{ .bindings = &store });
    defer fixture.deinit();
    const cases = [_]struct { target: []const u8, code: []const u8 }{
        .{ .target = "/sessions/history?cursor=not-a-cursor", .code = "invalid_cursor" },
        .{ .target = "/sessions/history?limit=0", .code = "invalid_request" },
        .{ .target = "/sessions/history?limit=101", .code = "invalid_request" },
        .{ .target = "/sessions/history?limit=many", .code = "invalid_request" },
    };
    for (cases) |each| {
        const refused = try fixture.answer("GET", each.target, "");
        try testing.expectEqualStrings("400 Bad Request", refused.status);
        try testing.expectEqualStrings(each.code, try fixture.code(refused));
    }

    var bare: Fixture = undefined;
    try bare.init(.{});
    defer bare.deinit();
    const unadvertised = try bare.answer("GET", "/sessions/history", "");
    try testing.expectEqualStrings("400 Bad Request", unadvertised.status);
    try testing.expectEqualStrings("unsupported_feature", try bare.code(unadvertised));
    const details = (try bare.json(unadvertised)).get("payload").?.object.get("error").?.object.get("details").?.object;
    try testing.expectEqualStrings("session.list", details.get("feature").?.string);
    try testing.expectEqualStrings("unadvertised", details.get("reason").?.string);
}
