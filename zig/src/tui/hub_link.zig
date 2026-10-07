const std = @import("std");
const compat = @import("compat");

const protocol_name = "open-agent-protocol";
const protocol_version = "0.1";
const profile = "open-agent-protocol.agent-control-core";
const request_timeout_ms: u64 = 30_000;
const pollable = @import("builtin").os.tag != .windows;
const max_answer_bytes: usize = 16 << 20;
const max_idle_streams: u8 = 3;
const lost_stream = "{\"control\":\"stream.lost\",\"message\":\"the hub stopped this run's stream before its terminal event\"}";

const Followed = struct {
    id: []u8,
    highest: u64 = 0,
    settled: bool = false,
};

pub const HubLink = struct {
    allocator: std.mem.Allocator,
    base: []u8,
    adapter: []u8,
    session_id: []u8 = &.{},
    outbound: std.ArrayList([]u8) = .empty,
    outbound_mutex: std.atomic.Mutex = .unlocked,
    stream: ?RunStream = null,
    runs: std.ArrayList(Followed) = .empty,
    idle_streams: u8 = 0,
    stream_moved: bool = false,

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
        for (self.runs.items) |run| allocator.free(run.id);
        self.runs.deinit(allocator);
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
        if (std.mem.eql(u8, kind, "capabilities.request")) return self.call(a, .GET, try self.path(a, "/adapters/", self.adapter, "/capabilities"), null, id, false);
        if (std.mem.eql(u8, kind, "session.open.request")) return self.call(a, .POST, try self.path(a, "/adapters/", self.adapter, "/sessions"), line, id, false);
        if (std.mem.eql(u8, kind, "models.request")) return self.call(a, .GET, try self.consenting(a, try self.path(a, "/sessions/", self.session_id, "/models"), root), null, id, false);
        if (std.mem.eql(u8, kind, "session.message.submit.request") or std.mem.eql(u8, kind, "session.compact.request")) return self.call(a, .POST, try self.path(a, "/sessions/", self.session_id, "/submit"), line, id, std.mem.eql(u8, kind, "session.message.submit.request") and followsUp(root));
        if (std.mem.eql(u8, kind, "run.cancel.request")) return self.call(a, .POST, try self.path(a, "/sessions/", self.session_id, "/cancel"), line, id, false);
        if (std.mem.eql(u8, kind, "session.settings.update.request")) return self.call(a, .POST, try self.path(a, "/sessions/", self.session_id, "/settings"), line, id, false);
        if (std.mem.eql(u8, kind, "action.permission.resolve.request")) return self.call(a, .POST, try self.path(a, "/sessions/", self.session_id, "/resolve"), line, id, false);
        try self.refuse(a, id, "unsupported_feature", try std.fmt.allocPrint(a, "the hub has no route for {s}", .{kind}));
    }

    pub fn pump(self: *HubLink) !bool {
        const stream = if (self.stream) |*held| held else return false;
        const ended = try stream.receive(self.allocator);
        var moved = false;
        while (try stream.nextFrame(self.allocator)) |frame| {
            moved = true;
            self.stream_moved = true;
            if (stream.status_ok) {
                try self.relay(frame);
            } else {
                try self.push(frame);
            }
        }
        if (ended) {
            const refused = !stream.status_ok;
            const delivered = self.stream_moved;
            self.dropStream();
            try self.streamEnded(refused, delivered);
            moved = true;
        }
        return moved;
    }

    fn streamEnded(self: *HubLink, refused: bool, delivered: bool) !void {
        if (refused) return self.lose();
        const next = self.nextUnsettled() orelse return;
        self.idle_streams = if (delivered) 0 else self.idle_streams + 1;
        if (self.idle_streams >= max_idle_streams) return self.lose();
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        self.follow(scratch.allocator(), next.id, next.highest) catch return self.lose();
    }

    fn lose(self: *HubLink) !void {
        for (self.runs.items) |*run| run.settled = true;
        try self.push(lost_stream);
    }

    fn nextUnsettled(self: *HubLink) ?*Followed {
        for (self.runs.items) |*run| {
            if (!run.settled) return run;
        }
        return null;
    }

    fn findRun(self: *HubLink, run_id: []const u8) ?*Followed {
        for (self.runs.items) |*run| {
            if (std.mem.eql(u8, run.id, run_id)) return run;
        }
        return null;
    }

    fn track(self: *HubLink, run_id: []const u8) !void {
        if (self.findRun(run_id) != null) return;
        var index: usize = 0;
        while (index < self.runs.items.len) {
            if (self.runs.items[index].settled) {
                self.allocator.free(self.runs.orderedRemove(index).id);
            } else {
                index += 1;
            }
        }
        const owned = try self.allocator.dupe(u8, run_id);
        errdefer self.allocator.free(owned);
        try self.runs.append(self.allocator, .{ .id = owned });
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

    fn call(self: *HubLink, a: std.mem.Allocator, method: compat.http.Method, target: []const u8, body: ?[]const u8, id: []const u8, follow_up: bool) !void {
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
        if (try self.queuedBehind(a, answer.object, id, follow_up)) return;
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
        if (std.mem.eql(u8, kind, "session.message.submit.response") or std.mem.eql(u8, kind, "session.compact.response")) {
            const run_id = textOf(body, "run_id") orelse return;
            const admission = textOf(body, "admission") orelse "";
            if (std.mem.eql(u8, admission, "queued")) return self.track(run_id);
            if (!std.mem.eql(u8, admission, "started")) return;
            for (self.runs.items) |*run| run.settled = true;
            try self.track(run_id);
            self.idle_streams = 0;
            try self.follow(a, run_id, 0);
        }
    }

    fn queuedBehind(self: *HubLink, a: std.mem.Allocator, answer: std.json.ObjectMap, id: []const u8, follow_up: bool) !bool {
        const replied = textOf(answer, "type") orelse "";
        if (!std.mem.eql(u8, replied, "session.message.submit.response") and !std.mem.eql(u8, replied, "session.compact.response")) return false;
        const body = if (answer.get("payload")) |value| (if (value == .object) value.object else return false) else return false;
        if (!std.mem.eql(u8, textOf(body, "admission") orelse "", "queued")) return false;
        if (follow_up and self.nextUnsettled() != null) return false;
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

    fn follow(self: *HubLink, a: std.mem.Allocator, run_id: []const u8, after: u64) !void {
        self.dropStream();
        const escaped_run = try escapeSegment(a, run_id);
        const target = try self.path(a, "/sessions/", self.session_id, "/events");
        const request_target = try std.fmt.allocPrint(a, "{s}?after={d}&run_id={s}", .{ target, after, escaped_run });
        self.stream = try RunStream.open(a, self.base, request_target);
        self.stream_moved = false;
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
            const run_id = textOf(parsed.object, "run_id") orelse return self.push(data);
            const run = self.findRun(run_id) orelse return;
            const sequence: u64 = if (parsed.object.get("sequence")) |value| (if (value == .integer and value.integer > 0) @intCast(value.integer) else 0) else 0;
            if (sequence > 0 and sequence <= run.highest) return;
            if (sequence > run.highest) run.highest = sequence;
            if (std.mem.eql(u8, kind, "run.completed") or std.mem.eql(u8, kind, "run.failed") or std.mem.eql(u8, kind, "run.cancelled")) run.settled = true;
            return self.push(data);
        }
        if (parsed.object.get("last_sequence") != null or parsed.object.get("oldest_available") != null) try self.lose();
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

    fn consenting(self: *HubLink, a: std.mem.Allocator, target: []const u8, root: std.json.ObjectMap) ![]const u8 {
        _ = self;
        const payload = root.get("payload") orelse return target;
        if (payload != .object) return target;
        const features = payload.object.get("allow_degraded_features") orelse return target;
        if (features != .array) return target;
        var built: std.ArrayList(u8) = .empty;
        try built.appendSlice(a, target);
        for (features.array.items) |feature| {
            if (feature != .string) continue;
            try built.appendSlice(a, if (built.items.len == target.len) "?allow_degraded=" else "&allow_degraded=");
            try built.appendSlice(a, try escapeSegment(a, feature.string));
        }
        return built.items;
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
        const dialed = try dialTarget(authority);
        var socket = try compat.net.tcpConnectHost(a, dialed.host, dialed.port);
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

const Dialed = struct { host: []const u8, port: u16 };

fn dialTarget(authority: []const u8) !Dialed {
    const colon = std.mem.lastIndexOfScalar(u8, authority, ':') orelse return error.HubUrlNeedsPort;
    const port = std.fmt.parseInt(u16, authority[colon + 1 ..], 10) catch return error.HubUrlNeedsPort;
    const host = authority[0..colon];
    if (host.len >= 2 and host[0] == '[' and host[host.len - 1] == ']') return .{ .host = host[1 .. host.len - 1], .port = port };
    return .{ .host = host, .port = port };
}

fn followsUp(root: std.json.ObjectMap) bool {
    const payload = root.get("payload") orelse return false;
    if (payload != .object) return false;
    return std.mem.eql(u8, textOf(payload.object, "delivery") orelse "", "queue");
}

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

test "a bracketed IPv6 hub address is dialled without its brackets" {
    const v6 = try dialTarget("[::1]:4180");
    try testing.expectEqualStrings("::1", v6.host);
    try testing.expectEqual(@as(u16, 4180), v6.port);
    const v4 = try dialTarget("127.0.0.1:4180");
    try testing.expectEqualStrings("127.0.0.1", v4.host);
    try testing.expectError(error.HubUrlNeedsPort, dialTarget("[::1]"));
}

test "an events stream the hub refuses still ends the turn with a lost stream" {
    if (comptime !pollable) return error.SkipZigTest;
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const a = arena_state.allocator();
    var listener = try compat.net.tcpListen(try compat.net.resolveAddress(a, "127.0.0.1", 0), .{ .reuse_address = true });
    defer compat.net.closeServer(&listener);
    const base = try std.fmt.allocPrint(a, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&listener).getPort()});

    const link = try HubLink.create(testing.allocator, base, "memory");
    defer link.destroy();
    link.stream = try RunStream.open(a, base, "/sessions/gone/events");
    var served = try compat.net.accept(&listener);
    try served.stream.writeAll("HTTP/1.1 404 Not Found\r\nContent-Type: application/json\r\n\r\n{" ++ envelope_head ++ ",\"type\":\"error.response\",\"id\":\"hub-1\",\"in_reply_to\":\"hub-events\",\"payload\":{\"error\":{\"code\":\"unknown_session\",\"message\":\"gone\"}}}");
    served.stream.close();

    var rounds: usize = 0;
    while (link.stream != null and rounds < 400) : (rounds += 1) {
        _ = try link.pump();
        compat.time.sleepMs(5);
    }
    const refusal = link.popOutbound() orelse return error.TestNoRefusal;
    defer testing.allocator.free(refusal);
    try testing.expect(std.mem.indexOf(u8, refusal, "unknown_session") != null);
    const lost = link.popOutbound() orelse return error.TestTurnLeftOpen;
    defer testing.allocator.free(lost);
    try testing.expectEqualStrings(lost_stream, lost);
}

const FakeHub = struct {
    listener: compat.net.Server,
    targets: [2][128]u8 = undefined,
    lengths: [2]usize = .{ 0, 0 },
    answer: []const u8,
    connections: usize = 2,

    fn serve(self: *FakeHub) void {
        for (0..self.connections) |index| {
            var served = compat.net.accept(&self.listener) catch return;
            var buffer: [8192]u8 = undefined;
            var filled: usize = 0;
            while (std.mem.indexOf(u8, buffer[0..filled], "\r\n\r\n") == null) {
                const count = served.stream.readSome(buffer[filled..]) catch return;
                if (count == 0) break;
                filled += count;
            }
            if (std.mem.indexOf(u8, buffer[0..filled], "Content-Length: ")) |at| {
                const head_end = std.mem.indexOf(u8, buffer[0..filled], "\r\n\r\n").? + 4;
                const length_end = std.mem.indexOfScalarPos(u8, buffer[0..filled], at, '\r').?;
                const length = std.fmt.parseInt(usize, buffer[at + "Content-Length: ".len .. length_end], 10) catch 0;
                while (filled < head_end + length) {
                    const count = served.stream.readSome(buffer[filled..]) catch return;
                    if (count == 0) break;
                    filled += count;
                }
            }
            const line_end = std.mem.indexOf(u8, buffer[0..filled], "\r\n") orelse return;
            const request_line = buffer[0..line_end];
            @memcpy(self.targets[index][0..request_line.len], request_line);
            self.lengths[index] = request_line.len;
            if (index == 0) {
                var head: [128]u8 = undefined;
                served.stream.writeAll(std.fmt.bufPrint(&head, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {d}\r\nConnection: close\r\n\r\n", .{self.answer.len}) catch return) catch return;
                served.stream.writeAll(self.answer) catch return;
            } else {
                served.stream.writeAll("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n") catch return;
            }
            served.stream.close();
        }
    }

    fn target(self: *const FakeHub, index: usize) []const u8 {
        return self.targets[index][0..self.lengths[index]];
    }
};

test "a compaction goes to the hub's submit route and the run it starts is followed" {
    if (comptime !pollable) return error.SkipZigTest;
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const a = arena_state.allocator();
    var fake = FakeHub{
        .listener = try compat.net.tcpListen(try compat.net.resolveAddress(a, "127.0.0.1", 0), .{ .reuse_address = true }),
        .answer = "{" ++ envelope_head ++ ",\"type\":\"session.compact.response\",\"id\":\"hub-2\",\"payload\":{\"session_id\":\"s1\",\"accepted\":true,\"admission\":\"started\",\"run_id\":\"run-9\",\"status\":\"running\"}}",
    };
    defer compat.net.closeServer(&fake.listener);
    const base = try std.fmt.allocPrint(a, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&fake.listener).getPort()});
    const thread = try std.Thread.spawn(.{}, FakeHub.serve, .{&fake});
    const link = try HubLink.create(testing.allocator, base, "memory");
    defer link.destroy();
    link.session_id = try testing.allocator.dupe(u8, "s1");
    try link.handleLine("{" ++ envelope_head ++ ",\"type\":\"session.compact.request\",\"id\":\"compact-1\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\"}}");
    thread.join();
    try testing.expectEqualStrings("POST /sessions/s1/submit HTTP/1.1", fake.target(0));
    try testing.expectEqualStrings("GET /sessions/s1/events?after=0&run_id=run-9 HTTP/1.1", fake.target(1));
    const answer = link.popOutbound() orelse return error.TestNoAnswer;
    defer testing.allocator.free(answer);
    try testing.expect(std.mem.indexOf(u8, answer, "\"in_reply_to\":\"compact-1\"") != null);
    try testing.expect(std.mem.indexOf(u8, answer, "session.compact.response") != null);
}

test "a compaction the hub queues behind a run is withdrawn and refused, not left waiting" {
    if (comptime !pollable) return error.SkipZigTest;
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const a = arena_state.allocator();
    var fake = FakeHub{
        .listener = try compat.net.tcpListen(try compat.net.resolveAddress(a, "127.0.0.1", 0), .{ .reuse_address = true }),
        .answer = "{" ++ envelope_head ++ ",\"type\":\"session.compact.response\",\"id\":\"hub-2\",\"payload\":{\"session_id\":\"s1\",\"accepted\":true,\"admission\":\"queued\",\"run_id\":\"run-9\",\"status\":\"queued\"}}",
    };
    defer compat.net.closeServer(&fake.listener);
    const base = try std.fmt.allocPrint(a, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&fake.listener).getPort()});
    const thread = try std.Thread.spawn(.{}, FakeHub.serve, .{&fake});
    const link = try HubLink.create(testing.allocator, base, "memory");
    defer link.destroy();
    link.session_id = try testing.allocator.dupe(u8, "s1");
    try link.handleLine("{" ++ envelope_head ++ ",\"type\":\"session.compact.request\",\"id\":\"compact-1\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\"}}");
    thread.join();
    try testing.expectEqualStrings("POST /sessions/s1/cancel HTTP/1.1", fake.target(1));
    try testing.expect(link.stream == null);
    const answer = link.popOutbound() orelse return error.TestNoAnswer;
    defer testing.allocator.free(answer);
    try testing.expect(std.mem.indexOf(u8, answer, "session_busy") != null);
    try testing.expect(std.mem.indexOf(u8, answer, "\"in_reply_to\":\"compact-1\"") != null);
}

const QueueHub = struct {
    listener: compat.net.Server,
    targets: [4][128]u8 = undefined,
    lengths: [4]usize = .{ 0, 0, 0, 0 },
    first_events: []const u8,
    second_events: []const u8,

    fn take(self: *QueueHub, index: usize) ?compat.net.Connection {
        var served = compat.net.accept(&self.listener) catch return null;
        var buffer: [8192]u8 = undefined;
        var filled: usize = 0;
        while (std.mem.indexOf(u8, buffer[0..filled], "\r\n\r\n") == null) {
            const count = served.stream.readSome(buffer[filled..]) catch return null;
            if (count == 0) break;
            filled += count;
        }
        if (std.mem.indexOf(u8, buffer[0..filled], "Content-Length: ")) |at| {
            const head_end = std.mem.indexOf(u8, buffer[0..filled], "\r\n\r\n").? + 4;
            const length_end = std.mem.indexOfScalarPos(u8, buffer[0..filled], at, '\r').?;
            const length = std.fmt.parseInt(usize, buffer[at + "Content-Length: ".len .. length_end], 10) catch 0;
            while (filled < head_end + length) {
                const count = served.stream.readSome(buffer[filled..]) catch return null;
                if (count == 0) break;
                filled += count;
            }
        }
        const line_end = std.mem.indexOf(u8, buffer[0..filled], "\r\n") orelse return null;
        @memcpy(self.targets[index][0..line_end], buffer[0..line_end]);
        self.lengths[index] = line_end;
        return served;
    }

    fn answer(served: *compat.net.Connection, body: []const u8) void {
        var head: [128]u8 = undefined;
        served.stream.writeAll(std.fmt.bufPrint(&head, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {d}\r\nConnection: close\r\n\r\n", .{body.len}) catch return) catch return;
        served.stream.writeAll(body) catch return;
        served.stream.close();
    }

    fn serve(self: *QueueHub) void {
        var started = self.take(0) orelse return;
        answer(&started, "{" ++ envelope_head ++ ",\"type\":\"session.message.submit.response\",\"id\":\"hub-1\",\"payload\":{\"session_id\":\"s1\",\"accepted\":true,\"admission\":\"started\",\"run_id\":\"run-1\",\"status\":\"running\"}}");
        var first = self.take(1) orelse return;
        first.stream.writeAll("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n") catch return;
        var queued = self.take(2) orelse return;
        answer(&queued, "{" ++ envelope_head ++ ",\"type\":\"session.message.submit.response\",\"id\":\"hub-2\",\"payload\":{\"session_id\":\"s1\",\"accepted\":true,\"admission\":\"queued\",\"run_id\":\"run-2\",\"status\":\"queued\"}}");
        first.stream.writeAll(self.first_events) catch return;
        first.stream.close();
        var second = self.take(3) orelse return;
        second.stream.writeAll("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n") catch return;
        second.stream.writeAll(self.second_events) catch return;
        second.stream.close();
    }

    fn target(self: *const QueueHub, index: usize) []const u8 {
        return self.targets[index][0..self.lengths[index]];
    }
};

fn runEvent(comptime kind: []const u8, comptime run_id: []const u8, comptime sequence: []const u8) []const u8 {
    return "data: {" ++ envelope_head ++ ",\"type\":\"" ++ kind ++ "\",\"id\":\"e-" ++ run_id ++ "-" ++ sequence ++ "\",\"session_id\":\"s1\",\"run_id\":\"" ++ run_id ++ "\",\"sequence\":" ++ sequence ++ ",\"payload\":{}}\n\n";
}

const queue_first_events = runEvent("run.started", "run-1", "1") ++ runEvent("run.started", "run-other", "1") ++ runEvent("run.completed", "run-1", "2");
const queue_second_events = runEvent("run.started", "run-2", "1") ++ runEvent("run.started", "run-2", "1") ++ runEvent("run.completed", "run-2", "2");

test "a follow-up the hub queues behind this terminal's run is followed once that run ends, without another run's events or a repeat" {
    if (comptime !pollable) return error.SkipZigTest;
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const a = arena_state.allocator();
    var fake = QueueHub{
        .listener = try compat.net.tcpListen(try compat.net.resolveAddress(a, "127.0.0.1", 0), .{ .reuse_address = true }),
        .first_events = queue_first_events,
        .second_events = queue_second_events,
    };
    defer compat.net.closeServer(&fake.listener);
    const base = try std.fmt.allocPrint(a, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&fake.listener).getPort()});
    const thread = try std.Thread.spawn(.{}, QueueHub.serve, .{&fake});
    const link = try HubLink.create(testing.allocator, base, "memory");
    defer link.destroy();
    link.session_id = try testing.allocator.dupe(u8, "s1");
    try link.handleLine("{" ++ envelope_head ++ ",\"type\":\"session.message.submit.request\",\"id\":\"submit-1\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\",\"delivery\":\"auto\"}}");
    try link.handleLine("{" ++ envelope_head ++ ",\"type\":\"session.message.submit.request\",\"id\":\"queue-1\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\",\"delivery\":\"queue\"}}");
    var rounds: usize = 0;
    while (link.nextUnsettled() != null and rounds < 400) : (rounds += 1) {
        _ = try link.pump();
        compat.time.sleepMs(5);
    }
    thread.join();
    try testing.expectEqualStrings("GET /sessions/s1/events?after=0&run_id=run-1 HTTP/1.1", fake.target(1));
    try testing.expectEqualStrings("POST /sessions/s1/submit HTTP/1.1", fake.target(2));
    try testing.expectEqualStrings("GET /sessions/s1/events?after=0&run_id=run-2 HTTP/1.1", fake.target(3));
    const expected = [_][]const u8{ "\"in_reply_to\":\"submit-1\"", "\"in_reply_to\":\"queue-1\"", "\"id\":\"e-run-1-1\"", "\"id\":\"e-run-1-2\"", "\"id\":\"e-run-2-1\"", "\"id\":\"e-run-2-2\"" };
    for (expected) |needle| {
        const line = link.popOutbound() orelse return error.TestLineMissing;
        defer testing.allocator.free(line);
        if (std.mem.indexOf(u8, line, needle) == null) {
            std.debug.print("wanted {s} in {s}\n", .{ needle, line });
            return error.TestLineOutOfOrder;
        }
    }
    try testing.expect(link.popOutbound() == null);
}

test "a models request carries its degraded consent to the hub as the query the hub reads" {
    if (comptime !pollable) return error.SkipZigTest;
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const a = arena_state.allocator();
    var fake = FakeHub{
        .listener = try compat.net.tcpListen(try compat.net.resolveAddress(a, "127.0.0.1", 0), .{ .reuse_address = true }),
        .answer = "{" ++ envelope_head ++ ",\"type\":\"models.response\",\"id\":\"hub-4\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\",\"models\":[]}}",
        .connections = 1,
    };
    defer compat.net.closeServer(&fake.listener);
    const base = try std.fmt.allocPrint(a, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&fake.listener).getPort()});
    const thread = try std.Thread.spawn(.{}, FakeHub.serve, .{&fake});
    const link = try HubLink.create(testing.allocator, base, "oapx");
    defer link.destroy();
    link.session_id = try testing.allocator.dupe(u8, "s1");
    try link.handleLine("{" ++ envelope_head ++ ",\"type\":\"models.request\",\"id\":\"models-1\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\",\"allow_degraded_features\":[\"models.list\"]}}");
    thread.join();
    try testing.expectEqualStrings("GET /sessions/s1/models?allow_degraded=models.list HTTP/1.1", fake.target(0));
    const answer = link.popOutbound() orelse return error.TestNoAnswer;
    defer testing.allocator.free(answer);
    try testing.expect(std.mem.indexOf(u8, answer, "\"in_reply_to\":\"models-1\"") != null);
}

test "a settings update goes to the hub's settings route and its answer is correlated to the request" {
    if (comptime !pollable) return error.SkipZigTest;
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const a = arena_state.allocator();
    var fake = FakeHub{
        .listener = try compat.net.tcpListen(try compat.net.resolveAddress(a, "127.0.0.1", 0), .{ .reuse_address = true }),
        .answer = "{" ++ envelope_head ++ ",\"type\":\"session.settings.update.response\",\"id\":\"hub-3\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\",\"compaction_policy\":{\"kind\":\"off\"}}}",
        .connections = 1,
    };
    defer compat.net.closeServer(&fake.listener);
    const base = try std.fmt.allocPrint(a, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&fake.listener).getPort()});
    const thread = try std.Thread.spawn(.{}, FakeHub.serve, .{&fake});
    const link = try HubLink.create(testing.allocator, base, "memory");
    defer link.destroy();
    link.session_id = try testing.allocator.dupe(u8, "s1");
    try link.handleLine("{" ++ envelope_head ++ ",\"type\":\"session.settings.update.request\",\"id\":\"settings-1\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\",\"compaction_policy\":{\"kind\":\"off\"}}}");
    thread.join();
    try testing.expectEqualStrings("POST /sessions/s1/settings HTTP/1.1", fake.target(0));
    const answer = link.popOutbound() orelse return error.TestNoAnswer;
    defer testing.allocator.free(answer);
    try testing.expect(std.mem.indexOf(u8, answer, "\"in_reply_to\":\"settings-1\"") != null);
    try testing.expect(std.mem.indexOf(u8, answer, "session.settings.update.response") != null);
    try testing.expect(link.stream == null);
}
