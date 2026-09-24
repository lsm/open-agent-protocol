const std = @import("std");
const native = @import("native");
const sse_parser = @import("sse_parser");
const goquote = @import("goquote");
const gomarshal = @import("gomarshal");

pub const default_frame_limit: usize = 8 << 20;

pub const invalid_frame = "opencode httpapi: invalid SSE frame";
pub const frame_too_large = "opencode httpapi: SSE event exceeds configured limit";
pub const subscription_failed = "opencode httpapi: subscription failed";

pub const Error = error{StreamFailed} || std.mem.Allocator.Error;

pub const Frame = struct { name: []const u8, data: []const u8 };

pub const Decoder = struct {
    gpa: std.mem.Allocator,
    parser: sse_parser.SSEParser,
    limit: usize,
    line: std.ArrayList(u8) = .empty,
    size: usize = 0,
    named: bool = false,
    identified: bool = false,
    data_len: usize = 0,

    pub fn init(gpa: std.mem.Allocator, limit: usize) Decoder {
        const bound = if (limit == 0) default_frame_limit else limit;
        return .{
            .gpa = gpa,
            .parser = sse_parser.SSEParser.initWithLimits(gpa, .{ .line_bytes = bound + 4, .event_bytes = bound + 4 }),
            .limit = bound,
        };
    }

    pub fn deinit(self: *Decoder) void {
        self.parser.deinit();
        self.line.deinit(self.gpa);
        self.* = undefined;
    }

    fn refuse(arena: std.mem.Allocator, diag: *native.Diagnostic, comptime reason: []const u8) Error {
        diag.message = try arena.dupe(u8, invalid_frame ++ ": " ++ reason);
        return error.StreamFailed;
    }

    pub fn feed(self: *Decoder, arena: std.mem.Allocator, chunk: []const u8, frames: *std.ArrayList(Frame), diag: *native.Diagnostic) Error!void {
        for (chunk) |byte| {
            try self.line.append(self.gpa, byte);
            if (self.line.items.len > self.limit + 2) {
                diag.message = frame_too_large;
                return error.StreamFailed;
            }
            if (byte != '\n') continue;
            var line = self.line.items[0 .. self.line.items.len - 1];
            if (line.len > 0 and line[line.len - 1] == '\r') line = line[0 .. line.len - 1];
            if (std.mem.indexOfScalar(u8, line, '\r') != null) return refuse(arena, diag, "bare CR inside line");
            if (try self.processLine(arena, line, diag)) |frame| try frames.append(arena, frame);
            self.line.clearRetainingCapacity();
        }
    }

    pub fn finish(self: *Decoder, arena: std.mem.Allocator, diag: *native.Diagnostic) Error!void {
        if (self.line.items.len > 0) return refuse(arena, diag, "unterminated line");
        diag.message = "EOF";
        return error.StreamFailed;
    }

    fn forward(self: *Decoder, line: []const u8) Error![]sse_parser.SSEEvent {
        const events = self.parser.feed(line) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return error.StreamFailed,
        };
        return events;
    }

    fn processLine(self: *Decoder, arena: std.mem.Allocator, line: []const u8, diag: *native.Diagnostic) Error!?Frame {
        self.size += line.len;
        if (self.size > self.limit) {
            diag.message = frame_too_large;
            return error.StreamFailed;
        }
        if (line.len == 0) {
            if (self.data_len == 0 and !self.named and !self.identified) {
                self.parser.reset();
                return null;
            }
            if (self.data_len == 0) return refuse(arena, diag, "event without data");
            const events = try self.forward("\n");
            const name = try arena.dupe(u8, events[0].event_type orelse "");
            const frame = Frame{ .name = name, .data = try arena.dupe(u8, events[0].data) };
            self.size = 0;
            self.named = false;
            self.identified = false;
            self.data_len = 0;
            return frame;
        }
        if (line[0] == ':') return null;
        const colon = std.mem.indexOfScalar(u8, line, ':');
        const field = if (colon) |at| line[0..at] else line;
        var value: []const u8 = if (colon) |at| line[at + 1 ..] else "";
        if (colon != null and value.len > 0 and value[0] == ' ') value = value[1..];
        if (std.mem.eql(u8, field, "data")) {
            self.data_len = if (self.data_len > 0) self.data_len + 1 + value.len else value.len;
        } else if (std.mem.eql(u8, field, "event")) {
            if (self.named) return refuse(arena, diag, "duplicate event field");
            self.named = value.len > 0;
        } else if (std.mem.eql(u8, field, "id")) {
            if (self.identified) return refuse(arena, diag, "duplicate id field");
            if (std.mem.indexOfScalar(u8, value, 0) != null) return refuse(arena, diag, "id contains NUL");
            self.identified = value.len > 0;
            return null;
        } else return null;
        const forwarded = try std.mem.concat(arena, u8, &.{ line, "\n" });
        _ = try self.forward(forwarded);
        return null;
    }
};

pub const Batch = struct { events: []const native.Event, failure: ?[]const u8 = null };

pub const Stream = struct {
    decoder: Decoder,
    last_seq: i64 = -1,
    failed: bool = false,

    pub fn init(gpa: std.mem.Allocator, limit: usize) Stream {
        return .{ .decoder = Decoder.init(gpa, limit) };
    }

    pub fn deinit(self: *Stream) void {
        self.decoder.deinit();
        self.* = undefined;
    }

    pub fn feed(self: *Stream, arena: std.mem.Allocator, chunk: []const u8) std.mem.Allocator.Error!Batch {
        if (self.failed) return .{ .events = &.{} };
        var diag = native.Diagnostic{};
        var events = std.ArrayList(native.Event).empty;
        var frames = std.ArrayList(Frame).empty;
        var framing: ?[]const u8 = null;
        self.decoder.feed(arena, chunk, &frames, &diag) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            error.StreamFailed => framing = diag.message,
        };
        for (frames.items) |frame| {
            if (frame.name.len > 0 and !std.mem.eql(u8, frame.name, "message")) {
                return self.fail(events.items, try std.fmt.allocPrint(arena, invalid_frame ++ ": unexpected SSE event name {s}", .{goquote.quote(arena, frame.name)}));
            }
            const event = native.decodeEvent(arena, frame.data, &diag) catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                error.InvalidWire, error.UnsupportedType => return self.fail(events.items, diag.message),
            };
            if (event.durable.seq <= self.last_seq) {
                return self.fail(events.items, try std.fmt.allocPrint(arena, subscription_failed ++ ": non-increasing durable sequence {d} after {d}", .{ event.durable.seq, self.last_seq }));
            }
            self.last_seq = event.durable.seq;
            try events.append(arena, event);
        }
        if (framing) |message| return self.fail(events.items, message);
        return .{ .events = events.items };
    }

    pub fn finish(self: *Stream, arena: std.mem.Allocator) std.mem.Allocator.Error![]const u8 {
        if (self.failed) return "";
        var diag = native.Diagnostic{};
        self.decoder.finish(arena, &diag) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            error.StreamFailed => {},
        };
        self.failed = true;
        return diag.message;
    }

    fn fail(self: *Stream, events: []const native.Event, message: []const u8) Batch {
        self.failed = true;
        return .{ .events = events, .failure = message };
    }
};

pub const Header = struct { name: []const u8, value: []const u8 };

pub const Request = struct {
    method: []const u8,
    target: []const u8,
    headers: []const Header,
    body: ?[]const u8 = null,
};

pub const Endpoint = struct {
    base_path: []const u8 = "",
    username: []const u8 = "",
    password: []const u8 = "",
};

pub const CreateSession = struct { agent: []const u8 = "", model: ?native.ModelRef = null };

fn unescapedInSegment(byte: u8) bool {
    return switch (byte) {
        'A'...'Z', 'a'...'z', '0'...'9', '-', '_', '.', '~', '$', '&', '+', ':', '=', '@' => true,
        else => false,
    };
}

pub fn pathEscape(arena: std.mem.Allocator, segment: []const u8) std.mem.Allocator.Error![]const u8 {
    var out = std.ArrayList(u8).empty;
    for (segment) |byte| {
        if (unescapedInSegment(byte)) {
            try out.append(arena, byte);
        } else {
            try out.print(arena, "%{X:0>2}", .{byte});
        }
    }
    return out.items;
}

fn cleanBase(arena: std.mem.Allocator, base: []const u8) std.mem.Allocator.Error![]const u8 {
    var kept = std.ArrayList([]const u8).empty;
    var segments = std.mem.splitScalar(u8, base, '/');
    while (segments.next()) |segment| {
        if (segment.len == 0 or std.mem.eql(u8, segment, ".")) continue;
        if (std.mem.eql(u8, segment, "..")) {
            _ = kept.pop();
            continue;
        }
        try kept.append(arena, segment);
    }
    var out = std.ArrayList(u8).empty;
    for (kept.items) |segment| {
        try out.append(arena, '/');
        try out.appendSlice(arena, segment);
    }
    return out.items;
}

fn headers(arena: std.mem.Allocator, endpoint: Endpoint, accept: []const u8, body: bool) std.mem.Allocator.Error![]const Header {
    var out = std.ArrayList(Header).empty;
    if (body) try out.append(arena, .{ .name = "Content-Type", .value = "application/json" });
    try out.append(arena, .{ .name = "Accept", .value = accept });
    if (endpoint.password.len > 0) {
        const credentials = try std.mem.concat(arena, u8, &.{ endpoint.username, ":", endpoint.password });
        const encoder = std.base64.standard.Encoder;
        const encoded = try arena.alloc(u8, encoder.calcSize(credentials.len));
        const value = try std.mem.concat(arena, u8, &.{ "Basic ", encoder.encode(encoded, credentials) });
        try out.append(arena, .{ .name = "Authorization", .value = value });
    }
    return out.items;
}

fn build(arena: std.mem.Allocator, endpoint: Endpoint, method: []const u8, path: []const u8, query: []const u8, accept: []const u8, body: ?[]const u8) std.mem.Allocator.Error!Request {
    const base = try cleanBase(arena, endpoint.base_path);
    const target = try std.mem.concat(arena, u8, &.{ base, path, if (query.len > 0) "?" else "", query });
    const carried = try headers(arena, endpoint, accept, body != null);
    return .{ .method = method, .target = target, .headers = carried, .body = body };
}

fn sessionPath(arena: std.mem.Allocator, session: []const u8, leaf: []const u8) std.mem.Allocator.Error![]const u8 {
    return std.mem.concat(arena, u8, &.{ "/api/session/", try pathEscape(arena, session), leaf });
}

pub fn createSession(arena: std.mem.Allocator, endpoint: Endpoint, request: CreateSession) std.mem.Allocator.Error!Request {
    var body = std.ArrayList(u8).empty;
    try body.append(arena, '{');
    if (request.agent.len > 0) {
        try body.appendSlice(arena, "\"agent\":");
        try gomarshal.appendString(&body, arena, request.agent);
    }
    if (request.model) |model| {
        if (request.agent.len > 0) try body.append(arena, ',');
        try body.appendSlice(arena, "\"model\":");
        try native.appendModelRef(&body, arena, model);
    }
    try body.append(arena, '}');
    return build(arena, endpoint, "POST", "/api/session", "", "application/json", body.items);
}

pub fn prompt(arena: std.mem.Allocator, endpoint: Endpoint, session: []const u8, request: native.PromptRequest) std.mem.Allocator.Error!Request {
    const body = try native.marshalPromptRequest(arena, request);
    return build(arena, endpoint, "POST", try sessionPath(arena, session, "/prompt"), "", "application/json", body);
}

pub fn interrupt(arena: std.mem.Allocator, endpoint: Endpoint, session: []const u8) std.mem.Allocator.Error!Request {
    return build(arena, endpoint, "POST", try sessionPath(arena, session, "/interrupt"), "", "application/json", "{}");
}

pub fn active(arena: std.mem.Allocator, endpoint: Endpoint) std.mem.Allocator.Error!Request {
    return build(arena, endpoint, "GET", "/api/session/active", "", "application/json", null);
}

fn cursorQuery(arena: std.mem.Allocator, after: i64, limit: usize) std.mem.Allocator.Error![]const u8 {
    var out = std.ArrayList(u8).empty;
    if (after >= 0) try out.print(arena, "after={d}", .{after});
    if (limit > 0) try out.print(arena, "{s}limit={d}", .{ if (out.items.len > 0) "&" else "", limit });
    return out.items;
}

pub fn history(arena: std.mem.Allocator, endpoint: Endpoint, session: []const u8, after: i64, limit: usize) std.mem.Allocator.Error!Request {
    return build(arena, endpoint, "GET", try sessionPath(arena, session, "/history"), try cursorQuery(arena, after, limit), "application/json", null);
}

pub fn subscribe(arena: std.mem.Allocator, endpoint: Endpoint, session: []const u8, after: i64) std.mem.Allocator.Error!Request {
    return build(arena, endpoint, "GET", try sessionPath(arena, session, "/event"), try cursorQuery(arena, after, 0), "text/event-stream", null);
}

pub const Response = struct { status: u16, body: []const u8 = "" };

pub const Failure = struct { message: []const u8, api: bool = false };

pub fn Outcome(comptime T: type) type {
    return union(enum) { ok: T, failed: Failure };
}

const Checked = union(enum) { document: std.json.Value, failed: Failure };

fn refusal(arena: std.mem.Allocator, response: Response, path: []const u8, limit: usize) std.mem.Allocator.Error!?Failure {
    const bound = if (limit == 0) default_frame_limit else limit;
    if (response.body.len > bound) return .{ .message = try std.fmt.allocPrint(arena, "opencode httpapi: {s} response exceeds {d} bytes", .{ path, bound }) };
    if (response.status < 200 or response.status >= 300) {
        const api = try native.decodeApiError(arena, response.status, response.body);
        return .{ .message = try api.message(arena), .api = true };
    }
    return null;
}

fn check(arena: std.mem.Allocator, response: Response, path: []const u8, limit: usize, fields: []const native.Field) std.mem.Allocator.Error!Checked {
    if (response.status == 204) return .{ .failed = .{ .message = try std.fmt.allocPrint(arena, "opencode httpapi: unexpected 204 for {s}", .{path}) } };
    if (try refusal(arena, response, path, limit)) |failure| return .{ .failed = failure };
    var diag = native.Diagnostic{};
    const document = native.decodeStrict(arena, response.body, fields, &diag) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        error.InvalidWire, error.UnsupportedType => return .{ .failed = .{ .message = try std.fmt.allocPrint(arena, "opencode httpapi: decode {s} response: {s}", .{ path, diag.message }) } },
    };
    return .{ .document = document };
}

pub fn createSessionResult(arena: std.mem.Allocator, response: Response, limit: usize) std.mem.Allocator.Error!Outcome(native.SessionInfo) {
    const document = switch (try check(arena, response, "/api/session", limit, &native.session_info_response)) {
        .document => |value| value,
        .failed => |failure| return .{ .failed = failure },
    };
    const info = native.sessionInfoOf(document);
    if (!native.validSessionInfo(info)) return .{ .failed = .{ .message = native.invalid_wire ++ ": invalid session info" } };
    return .{ .ok = info };
}

pub fn promptResult(arena: std.mem.Allocator, response: Response, session: []const u8, limit: usize) std.mem.Allocator.Error!Outcome(native.Admitted) {
    const document = switch (try check(arena, response, try sessionPath(arena, session, "/prompt"), limit, &native.admitted_response)) {
        .document => |value| value,
        .failed => |failure| return .{ .failed = failure },
    };
    const admitted = native.admittedOf(document);
    if (!native.validAdmitted(admitted)) return .{ .failed = .{ .message = native.invalid_wire ++ ": invalid admitted receipt" } };
    if (!std.mem.eql(u8, admitted.session_id, session)) {
        return .{ .failed = .{ .message = try std.fmt.allocPrint(arena, subscription_failed ++ ": admitted receipt for foreign session {s}", .{admitted.session_id}) } };
    }
    return .{ .ok = admitted };
}

pub fn interruptResult(arena: std.mem.Allocator, response: Response, session: []const u8, limit: usize) std.mem.Allocator.Error!?Failure {
    if (response.status == 204) return null;
    return refusal(arena, response, try sessionPath(arena, session, "/interrupt"), limit);
}

pub fn activeResult(arena: std.mem.Allocator, response: Response, limit: usize) std.mem.Allocator.Error!Outcome([]const []const u8) {
    return switch (try check(arena, response, "/api/session/active", limit, &native.active_response)) {
        .document => |document| .{ .ok = try native.runningSessions(arena, document) },
        .failed => |failure| .{ .failed = failure },
    };
}

pub fn historyResult(arena: std.mem.Allocator, response: Response, session: []const u8, limit: usize) std.mem.Allocator.Error!Outcome(native.HistoryPage) {
    const document = switch (try check(arena, response, try sessionPath(arena, session, "/history"), limit, &native.history_response)) {
        .document => |value| value,
        .failed => |failure| return .{ .failed = failure },
    };
    var diag = native.Diagnostic{};
    const page = native.historyOf(arena, document, &diag) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        error.InvalidWire, error.UnsupportedType => return .{ .failed = .{ .message = diag.message } },
    };
    return .{ .ok = page };
}

const testing = std.testing;

fn decodeFrames(arena: std.mem.Allocator, wire: []const u8) ![]const Frame {
    var decoder = Decoder.init(testing.allocator, 256);
    defer decoder.deinit();
    var diag = native.Diagnostic{};
    var frames = std.ArrayList(Frame).empty;
    try decoder.feed(arena, wire, &frames, &diag);
    return frames.items;
}

fn expectFrameRefusal(wire: []const u8, want: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var decoder = Decoder.init(testing.allocator, 64);
    defer decoder.deinit();
    var diag = native.Diagnostic{};
    var frames = std.ArrayList(Frame).empty;
    try testing.expectError(error.StreamFailed, decoder.feed(arena.allocator(), wire, &frames, &diag));
    try testing.expectEqualStrings(want, diag.message);
}

test "an SSE message frame decodes to its name and data" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const frames = try decodeFrames(arena.allocator(), ": hello\n\nevent: message\nid: 7\nretry: 5\nunknown: x\ndata: {\"a\":\ndata: 1}\n\ndata:x\r\n\r\n");
    try testing.expectEqual(@as(usize, 2), frames.len);
    try testing.expectEqualStrings("message", frames[0].name);
    try testing.expectEqualStrings("{\"a\":\n1}", frames[0].data);
    try testing.expectEqualStrings("", frames[1].name);
    try testing.expectEqualStrings("x", frames[1].data);
}

test "an SSE frame is refused where the oracle's decoder refuses it" {
    try expectFrameRefusal("data: a\rb\n\n", invalid_frame ++ ": bare CR inside line");
    try expectFrameRefusal("data: a\r\r\n\n", invalid_frame ++ ": bare CR inside line");
    try expectFrameRefusal("event: message\n\n", invalid_frame ++ ": event without data");
    try expectFrameRefusal("id: 1\n\n", invalid_frame ++ ": event without data");
    try expectFrameRefusal("event: a\nevent: b\ndata: x\n\n", invalid_frame ++ ": duplicate event field");
    try expectFrameRefusal("id: 1\nid: 2\ndata: x\n\n", invalid_frame ++ ": duplicate id field");
    try expectFrameRefusal("id: a\x00b\ndata: x\n\n", invalid_frame ++ ": id contains NUL");
    try expectFrameRefusal("data: " ++ "x" ** 70 ++ "\n\n", frame_too_large);
    try expectFrameRefusal(": " ++ "c" ** 40 ++ "\n: " ++ "c" ** 40 ++ "\n", frame_too_large);
}

test "an empty event is skipped and an empty field does not claim the event" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const frames = try decodeFrames(arena.allocator(), "data:\n\nevent:\nevent: message\ndata: y\n\n");
    try testing.expectEqual(@as(usize, 1), frames.len);
    try testing.expectEqualStrings("message", frames[0].name);
    try testing.expectEqualStrings("y", frames[0].data);
}

test "a stream that ends mid-line is unterminated, and one that ends cleanly is EOF" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var diag = native.Diagnostic{};
    var partial = Decoder.init(testing.allocator, 0);
    defer partial.deinit();
    var frames = std.ArrayList(Frame).empty;
    try partial.feed(arena.allocator(), "data: x", &frames, &diag);
    try testing.expectError(error.StreamFailed, partial.finish(arena.allocator(), &diag));
    try testing.expectEqualStrings(invalid_frame ++ ": unterminated line", diag.message);

    var clean = Decoder.init(testing.allocator, 0);
    defer clean.deinit();
    try clean.feed(arena.allocator(), "data: x\n\n", &frames, &diag);
    try testing.expectError(error.StreamFailed, clean.finish(arena.allocator(), &diag));
    try testing.expectEqualStrings("EOF", diag.message);
}

const frame_one = "event: message\ndata: {\"id\":\"evt_1\",\"type\":\"session.next.moved\",\"durable\":{\"aggregateID\":\"ses_a\",\"seq\":1,\"version\":1},\"data\":{}}\n\n";
const frame_two = "event: message\ndata: {\"id\":\"evt_2\",\"type\":\"session.next.moved\",\"durable\":{\"aggregateID\":\"ses_a\",\"seq\":2,\"version\":1},\"data\":{}}\n\n";

test "a subscription replays durable events and fails on a sequence that does not advance" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var stream = Stream.init(testing.allocator, 0);
    defer stream.deinit();
    const batch = try stream.feed(arena.allocator(), frame_one ++ frame_two ++ frame_two);
    try testing.expectEqual(@as(usize, 2), batch.events.len);
    try testing.expectEqualStrings("evt_2", batch.events[1].id);
    try testing.expectEqualStrings(subscription_failed ++ ": non-increasing durable sequence 2 after 2", batch.failure.?);
    const after = try stream.feed(arena.allocator(), frame_one);
    try testing.expectEqual(@as(usize, 0), after.events.len);
    try testing.expectEqual(@as(?[]const u8, null), after.failure);
}

test "a framing failure still delivers the events decoded before it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var stream = Stream.init(testing.allocator, 0);
    defer stream.deinit();
    const batch = try stream.feed(arena.allocator(), frame_one ++ "data: a\rb\n\n" ++ frame_two);
    try testing.expectEqual(@as(usize, 1), batch.events.len);
    try testing.expectEqualStrings("evt_1", batch.events[0].id);
    try testing.expectEqualStrings(invalid_frame ++ ": bare CR inside line", batch.failure.?);
}

test "a subscription fails on a frame name other than message and on a malformed payload" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var named = Stream.init(testing.allocator, 0);
    defer named.deinit();
    const renamed = try named.feed(arena.allocator(), "event: ping\ndata: {}\n\n");
    try testing.expectEqualStrings(invalid_frame ++ ": unexpected SSE event name \"ping\"", renamed.failure.?);

    var malformed = Stream.init(testing.allocator, 0);
    defer malformed.deinit();
    const refused = try malformed.feed(arena.allocator(), frame_one ++ "data: {\"id\":\"bad\"}\n\n");
    try testing.expectEqual(@as(usize, 1), refused.events.len);
    try testing.expectEqualStrings(native.invalid_wire ++ ": invalid event id", refused.failure.?);
}

test "a path segment is escaped the way url.PathEscape escapes it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    try testing.expectEqualStrings("a%20b%2Fc%3Fd", try pathEscape(scratch, "a b/c?d"));
    try testing.expectEqualStrings("ses_x-Y.~", try pathEscape(scratch, "ses_x-Y.~"));
    try testing.expectEqualStrings("$&+%2C:%3B=@%21%2A%27%28%29", try pathEscape(scratch, "$&+,:;=@!*'()"));
    try testing.expectEqualStrings("%C3%A9%25%23%5B%5D", try pathEscape(scratch, "\u{e9}%#[]"));
}

test "the endpoint's base path is joined and cleaned the way url.JoinPath does it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    for ([_][2][]const u8{
        .{ "", "/api/session/a%20b/prompt" },
        .{ "/", "/api/session/a%20b/prompt" },
        .{ "/base", "/base/api/session/a%20b/prompt" },
        .{ "/base/", "/base/api/session/a%20b/prompt" },
        .{ "//x/../y", "/y/api/session/a%20b/prompt" },
    }) |case| {
        const request = try prompt(scratch, .{ .base_path = case[0] }, "a b", .{});
        try testing.expectEqualStrings(case[1], request.target);
    }
}

test "a response is refused the way the oracle's client refuses it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const conflict = try promptResult(scratch, .{ .status = 409, .body = "{\"_tag\":\"ConflictError\"}" }, "ses_a", 0);
    try testing.expectEqualStrings("opencode native: HTTP 409 ConflictError", conflict.failed.message);
    try testing.expect(conflict.failed.api);

    const surprise = try createSessionResult(scratch, .{ .status = 200, .body = "{\"data\":{\"id\":\"ses_a\",\"projectID\":\"p\",\"time\":{\"created\":1,\"updated\":1}},\"surprise\":1}" }, 0);
    try testing.expectEqualStrings("opencode httpapi: decode /api/session response: json: unknown field \"surprise\"", surprise.failed.message);
    try testing.expect(!surprise.failed.api);

    const duplicated = try createSessionResult(scratch, .{ .status = 200, .body = "{\"data\":{\"id\":\"ses_a\",\"id\":\"ses_b\"}}" }, 0);
    try testing.expectEqualStrings("opencode httpapi: decode /api/session response: duplicate object key \"id\"", duplicated.failed.message);

    const oversized = try createSessionResult(scratch, .{ .status = 200, .body = "{\"data\":{}}" }, 4);
    try testing.expectEqualStrings("opencode httpapi: /api/session response exceeds 4 bytes", oversized.failed.message);

    const unvalidated = try createSessionResult(scratch, .{ .status = 200, .body = "{\"data\":{\"id\":\"ses_a\",\"projectID\":\"\"}}" }, 0);
    try testing.expectEqualStrings(native.invalid_wire ++ ": invalid session info", unvalidated.failed.message);

    const bodiless = try createSessionResult(scratch, .{ .status = 204 }, 0);
    try testing.expectEqualStrings("opencode httpapi: unexpected 204 for /api/session", bodiless.failed.message);

    const foreign = try promptResult(scratch, .{ .status = 200, .body = "{\"data\":{\"admittedSeq\":1,\"id\":\"msg_a\",\"sessionID\":\"ses_b\",\"prompt\":{\"text\":\"x\"},\"delivery\":\"steer\",\"timeCreated\":1}}" }, "ses_a", 0);
    try testing.expectEqualStrings(subscription_failed ++ ": admitted receipt for foreign session ses_b", foreign.failed.message);

    const receipt = try promptResult(scratch, .{ .status = 200, .body = "{\"data\":{\"admittedSeq\":1,\"id\":\"msg_a\",\"sessionID\":\"ses_a\",\"prompt\":{\"text\":\"x\"},\"delivery\":\"steer\",\"timeCreated\":1}}" }, "ses_a", 0);
    try testing.expectEqual(@as(?i64, null), receipt.ok.promoted_seq);
    const promoted = try promptResult(scratch, .{ .status = 200, .body = "{\"data\":{\"admittedSeq\":1,\"id\":\"msg_a\",\"sessionID\":\"ses_a\",\"prompt\":{\"text\":\"x\"},\"delivery\":\"queue\",\"timeCreated\":1,\"promotedSeq\":3}}" }, "ses_a", 0);
    try testing.expectEqual(@as(?i64, 3), promoted.ok.promoted_seq);

    const invalid = try promptResult(scratch, .{ .status = 200, .body = "{\"data\":{\"admittedSeq\":1,\"id\":\"msg_a\",\"sessionID\":\"ses_a\",\"prompt\":{\"text\":\"x\"},\"delivery\":\"now\",\"timeCreated\":1}}" }, "ses_a", 0);
    try testing.expectEqualStrings(native.invalid_wire ++ ": invalid admitted receipt", invalid.failed.message);
}

test "interrupt accepts no content and the active set keeps only running sessions" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    try testing.expectEqual(@as(?Failure, null), try interruptResult(scratch, .{ .status = 204 }, "ses_a", 0));
    const unavailable = (try interruptResult(scratch, .{ .status = 503, .body = "{\"_tag\":\"ServiceUnavailableError\"}" }, "ses_a", 0)).?;
    try testing.expectEqualStrings("opencode native: HTTP 503 ServiceUnavailableError", unavailable.message);

    const listed = try activeResult(scratch, .{ .status = 200, .body = "{\"data\":{\"ses_a\":{\"type\":\"running\"},\"ses_b\":{\"type\":\"idle\"},\"ses_c\":null}}" }, 0);
    try testing.expectEqual(@as(usize, 1), listed.ok.len);
    try testing.expectEqualStrings("ses_a", listed.ok[0]);
    const strict = try activeResult(scratch, .{ .status = 200, .body = "{\"data\":{\"ses_a\":{\"type\":\"running\",\"since\":1}}}" }, 0);
    try testing.expectEqualStrings("opencode httpapi: decode /api/session/active response: json: unknown field \"since\"", strict.failed.message);
}

test "a history page decodes every durable event it carries" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const page = try historyResult(scratch, .{ .status = 200, .body = "{\"data\":[{\"id\":\"evt_4\",\"type\":\"session.next.text.ended\",\"durable\":{\"aggregateID\":\"ses_a\",\"seq\":4,\"version\":1},\"data\":{\"text\":\"a<b\"}}],\"hasMore\":true}" }, "ses_a", 0);
    try testing.expect(page.ok.has_more);
    try testing.expectEqual(@as(i64, 4), page.ok.events[0].durable.seq);
    var diag = native.Diagnostic{};
    try testing.expectEqualStrings("a<b", (try native.decodeTextEnded(scratch, page.ok.events[0], &diag)).text);

    const refused = try historyResult(scratch, .{ .status = 200, .body = "{\"data\":[{\"id\":\"nope\"}],\"hasMore\":false}" }, "ses_a", 0);
    try testing.expectEqualStrings(native.invalid_wire ++ ": invalid event id", refused.failed.message);
}

fn streamEveryShape(allocator: std.mem.Allocator) !void {
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    var stream = Stream.init(allocator, 0);
    defer stream.deinit();
    const batch = try stream.feed(arena.allocator(), ": keepalive\n\n" ++ frame_one ++ frame_two);
    if (batch.events.len != 2) return error.TestUnexpectedResult;
    _ = try stream.finish(arena.allocator());
    _ = try historyResult(arena.allocator(), .{ .status = 200, .body = "{\"data\":[{\"id\":\"evt_4\",\"type\":\"session.next.moved\",\"durable\":{\"aggregateID\":\"ses_a\",\"seq\":4,\"version\":1},\"data\":{}}],\"hasMore\":false}" }, "ses_a", 0);
    _ = try prompt(arena.allocator(), .{ .base_path = "/base", .username = "u", .password = "p" }, "ses_a", .{ .id = "msg_a", .prompt = .{ .text = "hi" }, .delivery = "steer" });
}

test "the SSE decoder and the codec propagate every allocation failure and leak nothing" {
    try testing.checkAllAllocationFailures(testing.allocator, streamEveryShape, .{});
}
