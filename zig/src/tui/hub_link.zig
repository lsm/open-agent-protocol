const std = @import("std");
const compat = @import("compat");

const protocol_name = "open-agent-protocol";
const protocol_version = "0.1";
const profile = "open-agent-protocol.agent-control-core";
const request_timeout_ms: u64 = 30_000;
const pollable = @import("builtin").os.tag != .windows;
const max_answer_bytes: usize = 16 << 20;
const lost_stream = "{\"control\":\"stream.lost\",\"message\":\"the hub stopped this run's stream before its terminal event\"}";

pub const HubLink = struct {
    allocator: std.mem.Allocator,
    base: []u8,
    adapter: []u8,
    session_id: []u8 = &.{},
    outbound: std.ArrayList([]u8) = .empty,
    outbound_mutex: std.atomic.Mutex = .unlocked,
    stream: ?RunStream = null,
    settled: bool = false,

    pub fn create(allocator: std.mem.Allocator, base: []const u8, adapter: []const u8) !*HubLink {
        const trimmed = std.mem.trimEnd(u8, base, "/");
        const kept_base = try allocator.dupe(u8, trimmed);
        errdefer allocator.free(kept_base);
        const kept_adapter = try allocator.dupe(u8, adapter);
        errdefer allocator.free(kept_adapter);
        const self = try allocator.create(HubLink);
        self.* = .{ .allocator = allocator, .base = kept_base, .adapter = kept_adapter };
        return self;
    }

    pub fn destroy(self: *HubLink) void {
        const allocator = self.allocator;
        self.dropStream();
        for (self.outbound.items) |line| allocator.free(line);
        self.outbound.deinit(allocator);
        allocator.free(self.base);
        allocator.free(self.adapter);
        allocator.free(self.session_id);
        allocator.destroy(self);
    }

    pub fn handleLine(self: *HubLink, line: []const u8) !void {
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        const parsed = std.json.parseFromSliceLeaky(std.json.Value, a, line, .{}) catch return;
        if (parsed != .object) return;
        const root = parsed.object;
        const kind = textOf(root, "type") orelse return;
        const id = textOf(root, "id") orelse "";
        if (std.mem.eql(u8, kind, "capabilities.request")) return self.call(a, .GET, try self.path(a, "/adapters/", self.adapter, "/capabilities"), null, id);
        if (std.mem.eql(u8, kind, "session.open.request")) return self.call(a, .POST, try self.path(a, "/adapters/", self.adapter, "/sessions"), line, id);
        if (std.mem.eql(u8, kind, "models.request")) return self.call(a, .GET, try self.path(a, "/sessions/", self.session_id, "/models"), null, id);
        if (std.mem.eql(u8, kind, "session.message.submit.request")) return self.call(a, .POST, try self.path(a, "/sessions/", self.session_id, "/submit"), line, id);
        if (std.mem.eql(u8, kind, "run.cancel.request")) return self.call(a, .POST, try self.path(a, "/sessions/", self.session_id, "/cancel"), line, id);
        if (std.mem.eql(u8, kind, "action.permission.resolve.request")) return self.call(a, .POST, try self.path(a, "/sessions/", self.session_id, "/resolve"), line, id);
        try self.refuse(a, id, "unsupported_feature", try std.fmt.allocPrint(a, "the hub has no route for {s}", .{kind}));
    }

    pub fn pump(self: *HubLink) !bool {
        const stream = if (self.stream) |*held| held else return false;
        const ended = try stream.receive(self.allocator);
        var moved = false;
        while (try stream.nextFrame(self.allocator)) |frame| {
            moved = true;
            if (stream.status_ok) {
                try self.relay(frame);
            } else {
                try self.push(frame);
                self.settled = true;
            }
        }
        if (ended) {
            self.dropStream();
            if (!self.settled) try self.push(lost_stream);
            moved = true;
        }
        return moved;
    }

    pub fn closeSession(self: *HubLink) void {
        self.dropStream();
        if (self.session_id.len == 0) return;
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        const target = self.path(a, "/sessions/", self.session_id, "/close") catch return;
        const url = std.fmt.allocPrint(a, "{s}{s}", .{ self.base, target }) catch return;
        var fetched = compat.http.fetch(self.allocator, url, .{ .method = .POST, .timeout_ms = request_timeout_ms }) catch return;
        fetched.deinit(self.allocator);
    }

    pub fn popOutbound(self: *HubLink) ?[]u8 {
        self.lock();
        defer self.outbound_mutex.unlock();
        if (self.outbound.items.len == 0) return null;
        return self.outbound.orderedRemove(0);
    }

    fn call(self: *HubLink, a: std.mem.Allocator, method: compat.http.Method, target: []const u8, body: ?[]const u8, id: []const u8) !void {
        const url = try std.fmt.allocPrint(a, "{s}{s}", .{ self.base, target });
        const headers = [_]std.http.Header{ .{ .name = "Content-Type", .value = "application/json" }, .{ .name = "Accept", .value = "application/json" } };
        var fetched = compat.http.fetch(self.allocator, url, .{
            .method = method,
            .body = body,
            .extra_headers = if (body != null) &headers else headers[1..],
            .max_response_bytes = max_answer_bytes,
            .timeout_ms = request_timeout_ms,
        }) catch |err| return self.refuse(a, id, "hub_unreachable", try std.fmt.allocPrint(a, "the hub at {s} did not answer: {s}", .{ self.base, @errorName(err) }));
        defer fetched.deinit(self.allocator);
        const answer = std.json.parseFromSliceLeaky(std.json.Value, a, fetched.body, .{}) catch {
            return self.refuse(a, id, "hub_unreadable", try std.fmt.allocPrint(a, "the hub answered {d} with a body that is not an envelope", .{fetched.status}));
        };
        if (answer != .object) return self.refuse(a, id, "hub_unreadable", "the hub answered with a body that is not an envelope");
        if (try self.queuedBehind(a, answer.object, id)) return;
        try self.observe(a, answer.object);
        var correlated = answer.object;
        try correlated.put(a, "in_reply_to", .{ .string = id });
        try self.push(try std.json.Stringify.valueAlloc(a, std.json.Value{ .object = correlated }, .{}));
    }

    fn observe(self: *HubLink, a: std.mem.Allocator, answer: std.json.ObjectMap) !void {
        const kind = textOf(answer, "type") orelse return;
        const body = if (answer.get("payload")) |value| (if (value == .object) value.object else return) else return;
        if (std.mem.eql(u8, kind, "session.open.response")) {
            const named = textOf(body, "session_id") orelse return;
            const kept = try self.allocator.dupe(u8, named);
            self.allocator.free(self.session_id);
            self.session_id = kept;
            return;
        }
        if (std.mem.eql(u8, kind, "session.message.submit.response")) {
            const run_id = textOf(body, "run_id") orelse return;
            if (!std.mem.eql(u8, textOf(body, "admission") orelse "", "started")) return;
            try self.follow(a, run_id);
        }
    }

    fn queuedBehind(self: *HubLink, a: std.mem.Allocator, answer: std.json.ObjectMap, id: []const u8) !bool {
        if (!std.mem.eql(u8, textOf(answer, "type") orelse "", "session.message.submit.response")) return false;
        const body = if (answer.get("payload")) |value| (if (value == .object) value.object else return false) else return false;
        if (!std.mem.eql(u8, textOf(body, "admission") orelse "", "queued")) return false;
        if (textOf(body, "run_id")) |run_id| {
            var cancel_payload: std.json.ObjectMap = .empty;
            try cancel_payload.put(a, "session_id", .{ .string = self.session_id });
            try cancel_payload.put(a, "run_id", .{ .string = run_id });
            var cancel: std.json.ObjectMap = .empty;
            try cancel.put(a, "protocol", .{ .string = protocol_name });
            try cancel.put(a, "version", .{ .string = protocol_version });
            try cancel.put(a, "profile", .{ .string = profile });
            try cancel.put(a, "type", .{ .string = "run.cancel.request" });
            try cancel.put(a, "id", .{ .string = "hub-link-withdraw" });
            try cancel.put(a, "session_id", .{ .string = self.session_id });
            try cancel.put(a, "run_id", .{ .string = run_id });
            try cancel.put(a, "payload", .{ .object = cancel_payload });
            const line = try std.json.Stringify.valueAlloc(a, std.json.Value{ .object = cancel }, .{});
            const target = try self.path(a, "/sessions/", self.session_id, "/cancel");
            const url = try std.fmt.allocPrint(a, "{s}{s}", .{ self.base, target });
            const headers = [_]std.http.Header{.{ .name = "Content-Type", .value = "application/json" }};
            if (compat.http.fetch(self.allocator, url, .{ .method = .POST, .body = line, .extra_headers = &headers, .timeout_ms = request_timeout_ms })) |answered| {
                var owned = answered;
                owned.deinit(self.allocator);
            } else |_| {}
        }
        try self.refuse(a, id, "session_busy", "the hub queued this turn behind a run of this session still in flight, which this terminal no longer follows; the reservation was withdrawn, so wait for that run or cancel it from another client");
        return true;
    }

    fn follow(self: *HubLink, a: std.mem.Allocator, run_id: []const u8) !void {
        self.dropStream();
        const escaped_run = try escapeSegment(a, run_id);
        const target = try self.path(a, "/sessions/", self.session_id, "/events");
        const request_target = try std.fmt.allocPrint(a, "{s}?after=0&run_id={s}", .{ target, escaped_run });
        self.stream = try RunStream.open(a, self.base, request_target);
        self.settled = false;
    }

    fn dropStream(self: *HubLink) void {
        if (self.stream) |*stream| stream.close(self.allocator);
        self.stream = null;
    }

    fn relay(self: *HubLink, data: []const u8) !void {
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        const parsed = std.json.parseFromSliceLeaky(std.json.Value, a, data, .{}) catch return;
        if (parsed != .object) return;
        if (textOf(parsed.object, "type")) |kind| {
            if (std.mem.eql(u8, kind, "run.completed") or std.mem.eql(u8, kind, "run.failed") or std.mem.eql(u8, kind, "run.cancelled")) self.settled = true;
            return self.push(data);
        }
        if (parsed.object.get("last_sequence") != null or parsed.object.get("oldest_available") != null) {
            self.settled = true;
            try self.push(lost_stream);
        }
    }

    fn refuse(self: *HubLink, a: std.mem.Allocator, id: []const u8, code: []const u8, message: []const u8) !void {
        var error_object: std.json.ObjectMap = .empty;
        try error_object.put(a, "code", .{ .string = code });
        try error_object.put(a, "message", .{ .string = message });
        var payload: std.json.ObjectMap = .empty;
        try payload.put(a, "error", .{ .object = error_object });
        var envelope: std.json.ObjectMap = .empty;
        try envelope.put(a, "protocol", .{ .string = protocol_name });
        try envelope.put(a, "version", .{ .string = protocol_version });
        try envelope.put(a, "profile", .{ .string = profile });
        try envelope.put(a, "type", .{ .string = "error.response" });
        try envelope.put(a, "id", .{ .string = "hub-link-refusal" });
        try envelope.put(a, "in_reply_to", .{ .string = id });
        try envelope.put(a, "payload", .{ .object = payload });
        try self.push(try std.json.Stringify.valueAlloc(a, std.json.Value{ .object = envelope }, .{}));
    }

    fn push(self: *HubLink, line: []const u8) !void {
        const owned = try self.allocator.dupe(u8, line);
        errdefer self.allocator.free(owned);
        self.lock();
        defer self.outbound_mutex.unlock();
        try self.outbound.append(self.allocator, owned);
    }

    fn lock(self: *HubLink) void {
        while (!self.outbound_mutex.tryLock()) std.atomic.spinLoopHint();
    }

    fn path(self: *HubLink, a: std.mem.Allocator, head: []const u8, segment: []const u8, tail: []const u8) ![]const u8 {
        _ = self;
        return std.fmt.allocPrint(a, "{s}{s}{s}", .{ head, try escapeSegment(a, segment), tail });
    }
};

const RunStream = struct {
    socket: compat.net.Stream,
    buffer: std.ArrayList(u8) = .empty,
    consumed: usize = 0,
    head_read: bool = false,
    status_ok: bool = true,
    frame: std.ArrayList(u8) = .empty,

    fn open(a: std.mem.Allocator, base: []const u8, target: []const u8) !RunStream {
        if (comptime !pollable) return error.HubStreamNeedsPoll;
        const authority = if (std.mem.indexOf(u8, base, "://")) |at| base[at + 3 ..] else base;
        const colon = std.mem.lastIndexOfScalar(u8, authority, ':') orelse return error.HubUrlNeedsPort;
        const port = std.fmt.parseInt(u16, authority[colon + 1 ..], 10) catch return error.HubUrlNeedsPort;
        var socket = try compat.net.tcpConnectHost(a, authority[0..colon], port);
        errdefer socket.close();
        const request = try std.fmt.allocPrint(a, "GET {s} HTTP/1.1\r\nHost: {s}\r\nAccept: text/event-stream\r\nConnection: close\r\n\r\n", .{ target, authority });
        try socket.writeAll(request);
        return .{ .socket = socket };
    }

    fn close(self: *RunStream, allocator: std.mem.Allocator) void {
        self.socket.close();
        self.buffer.deinit(allocator);
        self.frame.deinit(allocator);
        self.* = undefined;
    }

    fn receive(self: *RunStream, allocator: std.mem.Allocator) !bool {
        if (comptime !pollable) return true;
        const handle = compat.net.streamHandle(&self.socket);
        var chunk: [16 * 1024]u8 = undefined;
        if (self.consumed > 0) {
            const left = self.buffer.items.len - self.consumed;
            std.mem.copyForwards(u8, self.buffer.items[0..left], self.buffer.items[self.consumed..]);
            self.buffer.shrinkRetainingCapacity(left);
            self.consumed = 0;
        }
        while (true) {
            var watched = [_]std.posix.pollfd{.{ .fd = handle, .events = std.posix.POLL.IN, .revents = 0 }};
            const ready = std.posix.poll(&watched, 0) catch return true;
            if (ready == 0) return false;
            const count = std.posix.read(handle, &chunk) catch return true;
            if (count == 0) return true;
            try self.buffer.appendSlice(allocator, chunk[0..count]);
        }
    }

    fn nextFrame(self: *RunStream, allocator: std.mem.Allocator) !?[]const u8 {
        const pending = self.buffer.items[self.consumed..];
        if (!self.head_read) {
            const end = std.mem.indexOf(u8, pending, "\r\n\r\n") orelse return null;
            self.status_ok = std.mem.startsWith(u8, pending, "HTTP/1.1 2") or std.mem.startsWith(u8, pending, "HTTP/1.0 2");
            self.consumed += end + 4;
            self.head_read = true;
            if (!self.status_ok) {
                const body = self.buffer.items[self.consumed..];
                self.consumed = self.buffer.items.len;
                return if (body.len > 0) body else null;
            }
            return self.nextFrame(allocator);
        }
        const end = std.mem.indexOf(u8, pending, "\n\n") orelse return null;
        const block = pending[0..end];
        self.consumed += end + 2;
        self.frame.clearRetainingCapacity();
        var lines = std.mem.splitScalar(u8, block, '\n');
        while (lines.next()) |raw| {
            const line = std.mem.trimEnd(u8, raw, "\r");
            if (!std.mem.startsWith(u8, line, "data:")) continue;
            const value = std.mem.trimStart(u8, line["data:".len..], " ");
            if (self.frame.items.len > 0) try self.frame.append(allocator, '\n');
            try self.frame.appendSlice(allocator, value);
        }
        if (self.frame.items.len == 0) return self.nextFrame(allocator);
        return self.frame.items;
    }
};

fn textOf(object: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = object.get(key) orelse return null;
    return if (value == .string) value.string else null;
}

fn escapeSegment(a: std.mem.Allocator, segment: []const u8) ![]const u8 {
    var out: std.ArrayList(u8) = .empty;
    for (segment) |byte| {
        const plain = std.ascii.isAlphanumeric(byte) or byte == '-' or byte == '_' or byte == '.' or byte == '~';
        if (plain) {
            try out.append(a, byte);
        } else {
            try out.print(a, "%{X:0>2}", .{byte});
        }
    }
    return out.items;
}

const testing = std.testing;

const envelope_head = "\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\"";
test "a request the hub has no route for is refused to its own id, not dropped" {
    const link = try HubLink.create(testing.allocator, "http://127.0.0.1:1", "memory");
    defer link.destroy();
    try link.handleLine("{" ++ envelope_head ++ ",\"type\":\"session.model.switch.request\",\"id\":\"m1\",\"payload\":{}}");
    const answer = link.popOutbound() orelse return error.TestNoRefusal;
    defer testing.allocator.free(answer);
    try testing.expect(std.mem.indexOf(u8, answer, "\"in_reply_to\":\"m1\"") != null);
    try testing.expect(std.mem.indexOf(u8, answer, "unsupported_feature") != null);
}
