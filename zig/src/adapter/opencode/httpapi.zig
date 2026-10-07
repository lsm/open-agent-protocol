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

pub const Batch = struct { events: []const native.Event, failure: ?[]const u8 = null, connected: bool = false };

pub const Stream = struct {
    decoder: Decoder,
    session: []const u8,
    last_seq: i64 = -1,
    failed: bool = false,
    connected: bool = false,

    pub fn init(gpa: std.mem.Allocator, limit: usize, session: []const u8) Stream {
        return .{ .decoder = Decoder.init(gpa, limit), .session = session };
    }

    pub fn deinit(self: *Stream) void {
        self.decoder.deinit();
        self.* = undefined;
    }

    pub fn feed(self: *Stream, arena: std.mem.Allocator, chunk: []const u8) std.mem.Allocator.Error!Batch {
        if (self.failed) return .{ .events = &.{}, .connected = self.connected };
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
            var decoded = native.Diagnostic{};
            const event = native.decodeEvent(arena, frame.data, &decoded) catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                error.InvalidWire, error.UnsupportedType => {
                    if (decoded.session_id.len == 0 or std.mem.eql(u8, decoded.session_id, self.session)) return self.fail(events.items, decoded.message);
                    continue;
                },
            };
            if (std.mem.eql(u8, event.type_name, native.server_connected)) {
                self.connected = true;
                continue;
            }
            if (!std.mem.eql(u8, event.session_id, self.session)) continue;
            if (event.durable) |position| {
                if (position.seq <= self.last_seq) {
                    return self.fail(events.items, try std.fmt.allocPrint(arena, subscription_failed ++ ": non-increasing durable sequence {d} after {d}", .{ position.seq, self.last_seq }));
                }
                self.last_seq = position.seq;
            }
            try events.append(arena, event);
        }
        if (framing) |message| return self.fail(events.items, message);
        return .{ .events = events.items, .connected = self.connected };
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
        return .{ .events = events, .failure = message, .connected = self.connected };
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

pub fn queryEscape(arena: std.mem.Allocator, value: []const u8) std.mem.Allocator.Error![]const u8 {
    var out = std.ArrayList(u8).empty;
    for (value) |byte| {
        if (std.ascii.isAlphanumeric(byte) or byte == '-' or byte == '_' or byte == '.' or byte == '~') {
            try out.append(arena, byte);
        } else if (byte == ' ') {
            try out.append(arena, '+');
        } else {
            try out.print(arena, "%{X:0>2}", .{byte});
        }
    }
    return out.items;
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
    return build(arena, endpoint, "POST", try sessionPath(arena, session, "/interrupt"), "", "application/json", null);
}

pub fn cancelInbox(arena: std.mem.Allocator, endpoint: Endpoint, session: []const u8, inbox: []const u8) std.mem.Allocator.Error!Request {
    const leaf = try std.mem.concat(arena, u8, &.{ "/inbox/", try pathEscape(arena, inbox) });
    return build(arena, endpoint, "DELETE", try sessionPath(arena, session, leaf), "", "application/json", null);
}

pub fn replyPermission(arena: std.mem.Allocator, endpoint: Endpoint, session: []const u8, request_id: []const u8, decision: []const u8, message: []const u8) std.mem.Allocator.Error!Request {
    const leaf = try std.mem.concat(arena, u8, &.{ "/permission/", try pathEscape(arena, request_id), "/reply" });
    const body = if (message.len > 0)
        try std.json.Stringify.valueAlloc(arena, .{ .decision = decision, .message = message }, .{})
    else
        try std.json.Stringify.valueAlloc(arena, .{ .decision = decision }, .{});
    return build(arena, endpoint, "POST", try sessionPath(arena, session, leaf), "", "application/json", body);
}

pub fn replyPermissionResult(arena: std.mem.Allocator, response: Response, session: []const u8, request_id: []const u8, limit: usize) std.mem.Allocator.Error!?Failure {
    if (response.status == 204) return null;
    const leaf = try std.mem.concat(arena, u8, &.{ "/permission/", try pathEscape(arena, request_id), "/reply" });
    return refusal(arena, response, try sessionPath(arena, session, leaf), limit);
}

pub fn serverInfo(arena: std.mem.Allocator, endpoint: Endpoint) std.mem.Allocator.Error!Request {
    return build(arena, endpoint, "GET", "/api/info", "", "application/json", null);
}

pub fn switchModel(arena: std.mem.Allocator, endpoint: Endpoint, session: []const u8, model: native.ModelRef) std.mem.Allocator.Error!Request {
    var body = std.ArrayList(u8).empty;
    try body.appendSlice(arena, "{\"model\":");
    try native.appendModelRef(&body, arena, model);
    try body.append(arena, '}');
    return build(arena, endpoint, "POST", try sessionPath(arena, session, "/model"), "", "application/json", body.items);
}

pub fn getSession(arena: std.mem.Allocator, endpoint: Endpoint, session: []const u8) std.mem.Allocator.Error!Request {
    return build(arena, endpoint, "GET", try sessionPath(arena, session, ""), "", "application/json", null);
}

pub fn sessions(arena: std.mem.Allocator, endpoint: Endpoint, directory: []const u8, cursor: []const u8, limit: usize) std.mem.Allocator.Error!Request {
    if (cursor.len > 0) return build(arena, endpoint, "GET", "/api/session", try std.fmt.allocPrint(arena, "cursor={s}&limit={d}", .{ try queryEscape(arena, cursor), limit }), "application/json", null);
    const scope = if (directory.len > 0) try std.mem.concat(arena, u8, &.{ "directory=", try queryEscape(arena, directory), "&" }) else "";
    const query = try std.fmt.allocPrint(arena, "{s}limit={d}&order=desc&parentID=null", .{ scope, limit });
    return build(arena, endpoint, "GET", "/api/session", query, "application/json", null);
}

pub fn messages(arena: std.mem.Allocator, endpoint: Endpoint, session: []const u8, cursor: []const u8, limit: usize, newest_first: bool) std.mem.Allocator.Error!Request {
    const query = if (cursor.len > 0)
        try std.fmt.allocPrint(arena, "cursor={s}&limit={d}", .{ try queryEscape(arena, cursor), limit })
    else
        try std.fmt.allocPrint(arena, "limit={d}&order={s}", .{ limit, if (newest_first) "desc" else "asc" });
    return build(arena, endpoint, "GET", try sessionPath(arena, session, "/message"), query, "application/json", null);
}

pub const MessagePage = struct { messages: []const std.json.Value, next: []const u8 };

pub fn messagesResult(arena: std.mem.Allocator, response: Response, session: []const u8, limit: usize) std.mem.Allocator.Error!Outcome(MessagePage) {
    const document = switch (try check(arena, response, try sessionPath(arena, session, "/message"), limit, &native.messages_response)) {
        .document => |value| value,
        .failed => |failure| return .{ .failed = failure },
    };
    if (document != .object) return .{ .ok = .{ .messages = &.{}, .next = "" } };
    const data = document.object.get("data") orelse std.json.Value.null;
    const cursor = document.object.get("cursor") orelse std.json.Value.null;
    const next = if (cursor == .object) cursor.object.get("next") orelse std.json.Value.null else std.json.Value.null;
    return .{ .ok = .{ .messages = if (data == .array) data.array.items else &.{}, .next = if (next == .string) next.string else "" } };
}

pub fn active(arena: std.mem.Allocator, endpoint: Endpoint) std.mem.Allocator.Error!Request {
    return build(arena, endpoint, "GET", "/api/session/active", "", "application/json", null);
}

pub fn subscribe(arena: std.mem.Allocator, endpoint: Endpoint) std.mem.Allocator.Error!Request {
    return build(arena, endpoint, "GET", "/api/event", "", "text/event-stream", null);
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

pub fn interruptResult(arena: std.mem.Allocator, response: Response, session: []const u8, limit: usize) std.mem.Allocator.Error!Outcome(bool) {
    return switch (try check(arena, response, try sessionPath(arena, session, "/interrupt"), limit, &native.interrupt_response)) {
        .document => |document| .{ .ok = native.interruptedOf(document) },
        .failed => |failure| .{ .failed = failure },
    };
}

pub fn cancelInboxResult(arena: std.mem.Allocator, response: Response, session: []const u8, inbox: []const u8, limit: usize) std.mem.Allocator.Error!?Failure {
    if (response.status == 204) return null;
    const leaf = try std.mem.concat(arena, u8, &.{ "/inbox/", try pathEscape(arena, inbox) });
    return refusal(arena, response, try sessionPath(arena, session, leaf), limit);
}

pub fn infoResult(arena: std.mem.Allocator, response: Response, limit: usize) std.mem.Allocator.Error!?Failure {
    return switch (try check(arena, response, "/api/info", limit, &native.info_response)) {
        .document => null,
        .failed => |failure| failure,
    };
}

pub fn switchModelResult(arena: std.mem.Allocator, response: Response, session: []const u8, limit: usize) std.mem.Allocator.Error!?Failure {
    if (response.status == 204) return null;
    return refusal(arena, response, try sessionPath(arena, session, "/model"), limit);
}

pub fn getSessionResult(arena: std.mem.Allocator, response: Response, session: []const u8, limit: usize) std.mem.Allocator.Error!Outcome(native.SessionInfo) {
    const document = switch (try check(arena, response, try sessionPath(arena, session, ""), limit, &native.session_info_response)) {
        .document => |value| value,
        .failed => |failure| return .{ .failed = failure },
    };
    const info = native.sessionInfoOf(document);
    if (!native.validSessionInfo(info)) return .{ .failed = .{ .message = native.invalid_wire ++ ": invalid session info" } };
    if (!std.mem.eql(u8, info.id, session)) {
        return .{ .failed = .{ .message = try std.fmt.allocPrint(arena, subscription_failed ++ ": session record for foreign session {s}", .{info.id}) } };
    }
    return .{ .ok = info };
}

pub const SessionPage = struct { infos: []const native.SessionInfo, next: []const u8 };

pub fn sessionsResult(arena: std.mem.Allocator, response: Response, limit: usize) std.mem.Allocator.Error!Outcome(SessionPage) {
    const document = switch (try check(arena, response, "/api/session", limit, &native.sessions_response)) {
        .document => |value| value,
        .failed => |failure| return .{ .failed = failure },
    };
    const infos = try native.sessionsOf(arena, document);
    for (infos) |info| {
        if (!native.validSessionInfo(info)) return .{ .failed = .{ .message = native.invalid_wire ++ ": invalid session info" } };
    }
    var next: []const u8 = "";
    if (document == .object) {
        if (document.object.get("cursor")) |cursor| {
            if (cursor == .object) {
                if (cursor.object.get("next")) |value| {
                    if (value == .string) next = value.string;
                }
            }
        }
    }
    return .{ .ok = .{ .infos = infos, .next = next } };
}

pub fn activeResult(arena: std.mem.Allocator, response: Response, limit: usize) std.mem.Allocator.Error!Outcome([]const []const u8) {
    return switch (try check(arena, response, "/api/session/active", limit, &native.active_response)) {
        .document => |document| .{ .ok = try native.runningSessions(arena, document) },
        .failed => |failure| .{ .failed = failure },
    };
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

const frame_one = "event: message\ndata: {\"id\":\"evt_1\",\"type\":\"session.renamed\",\"durable\":{\"aggregateID\":\"ses_a\",\"seq\":1,\"version\":1},\"data\":{\"sessionID\":\"ses_a\",\"title\":\"t\"}}\n\n";
const frame_two = "event: message\ndata: {\"id\":\"evt_2\",\"type\":\"session.renamed\",\"durable\":{\"aggregateID\":\"ses_a\",\"seq\":2,\"version\":1},\"data\":{\"sessionID\":\"ses_a\",\"title\":\"t\"}}\n\n";
const connected_frame = "data: {\"id\":\"evt_connected\",\"type\":\"server.connected\",\"data\":{}}\n\n";

test "a subscription follows one session's events and fails on a sequence that does not advance" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var stream = Stream.init(testing.allocator, 0, "ses_a");
    defer stream.deinit();
    const batch = try stream.feed(arena.allocator(), frame_one ++ frame_two ++ frame_two);
    try testing.expectEqual(@as(usize, 2), batch.events.len);
    try testing.expectEqualStrings("evt_2", batch.events[1].id);
    try testing.expectEqualStrings(subscription_failed ++ ": non-increasing durable sequence 2 after 2", batch.failure.?);
    const after = try stream.feed(arena.allocator(), frame_one);
    try testing.expectEqual(@as(usize, 0), after.events.len);
    try testing.expectEqual(@as(?[]const u8, null), after.failure);
}

test "a subscription reports server.connected and passes over every other session and every unscoped event" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var stream = Stream.init(testing.allocator, 0, "ses_a");
    defer stream.deinit();
    const before = try stream.feed(arena.allocator(), ": heartbeat\n\n");
    try testing.expect(!before.connected);
    const batch = try stream.feed(arena.allocator(), connected_frame ++
        "data: {\"id\":\"evt_b1\",\"type\":\"session.renamed\",\"durable\":{\"aggregateID\":\"ses_b\",\"seq\":9,\"version\":1},\"data\":{\"sessionID\":\"ses_b\",\"title\":\"t\"}}\n\n" ++
        "data: {\"id\":\"evt_b2\",\"type\":\"session.brand.new\",\"data\":{\"sessionID\":\"ses_b\"}}\n\n" ++
        "data: {\"id\":\"evt_p\",\"created\":1,\"type\":\"project.updated\",\"data\":{\"id\":\"p\"}}\n\n" ++
        frame_one ++
        "data: {\"id\":\"evt_d\",\"type\":\"session.text.delta\",\"data\":{\"sessionID\":\"ses_a\",\"assistantMessageID\":\"msg_1\",\"ordinal\":0,\"delta\":\"h\"}}\n\n");
    try testing.expect(batch.connected);
    try testing.expectEqual(@as(?[]const u8, null), batch.failure);
    try testing.expectEqual(@as(usize, 2), batch.events.len);
    try testing.expectEqualStrings("evt_1", batch.events[0].id);
    try testing.expectEqualStrings("evt_d", batch.events[1].id);

    var own = Stream.init(testing.allocator, 0, "ses_a");
    defer own.deinit();
    const refused = try own.feed(arena.allocator(), "data: {\"id\":\"evt_n\",\"type\":\"session.brand.new\",\"data\":{\"sessionID\":\"ses_a\"}}\n\n");
    try testing.expectEqualStrings("opencode native: unsupported event type \"session.brand.new\"", refused.failure.?);
}

test "a framing failure still delivers the events decoded before it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var stream = Stream.init(testing.allocator, 0, "ses_a");
    defer stream.deinit();
    const batch = try stream.feed(arena.allocator(), frame_one ++ "data: a\rb\n\n" ++ frame_two);
    try testing.expectEqual(@as(usize, 1), batch.events.len);
    try testing.expectEqualStrings("evt_1", batch.events[0].id);
    try testing.expectEqualStrings(invalid_frame ++ ": bare CR inside line", batch.failure.?);
}

test "a subscription fails on a frame name other than message and on a malformed payload" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var named = Stream.init(testing.allocator, 0, "ses_a");
    defer named.deinit();
    const renamed = try named.feed(arena.allocator(), "event: ping\ndata: {}\n\n");
    try testing.expectEqualStrings(invalid_frame ++ ": unexpected SSE event name \"ping\"", renamed.failure.?);

    var malformed = Stream.init(testing.allocator, 0, "ses_a");
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

    const located = try createSessionResult(scratch, .{ .status = 200, .body = "{\"data\":{\"id\":\"ses_a\",\"projectID\":\"p\",\"outcome\":\"succeeded\",\"time\":{\"created\":1,\"updated\":1,\"idle\":2},\"location\":{\"directory\":\"/w\"}}}" }, 0);
    try testing.expectEqualStrings("/w", located.ok.directory);

    const foreign = try promptResult(scratch, .{ .status = 200, .body = "{\"data\":{\"id\":\"msg_a\",\"sessionID\":\"ses_b\",\"time\":{\"created\":1},\"type\":\"user\",\"payload\":{\"text\":\"x\"},\"delivery\":\"steer\"}}" }, "ses_a", 0);
    try testing.expectEqualStrings(subscription_failed ++ ": admitted receipt for foreign session ses_b", foreign.failed.message);

    const receipt = try promptResult(scratch, .{ .status = 200, .body = "{\"data\":{\"id\":\"msg_a\",\"sessionID\":\"ses_a\",\"time\":{\"created\":1},\"type\":\"user\",\"payload\":{\"text\":\"x\"},\"delivery\":\"queue\"}}" }, "ses_a", 0);
    try testing.expectEqualStrings("queue", receipt.ok.delivery);

    const invalid = try promptResult(scratch, .{ .status = 200, .body = "{\"data\":{\"id\":\"msg_a\",\"sessionID\":\"ses_a\",\"time\":{\"created\":1},\"type\":\"user\",\"payload\":{},\"delivery\":\"now\"}}" }, "ses_a", 0);
    try testing.expectEqualStrings(native.invalid_wire ++ ": invalid admitted receipt", invalid.failed.message);
}

test "interrupt says whether it interrupted, an inbox cancel accepts no content, and the active set keeps only running sessions" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    try testing.expect((try interruptResult(scratch, .{ .status = 200, .body = "{\"interrupted\":true}" }, "ses_a", 0)).ok);
    try testing.expect(!(try interruptResult(scratch, .{ .status = 200, .body = "{\"interrupted\":false}" }, "ses_a", 0)).ok);
    const unavailable = try interruptResult(scratch, .{ .status = 503, .body = "{\"_tag\":\"ServiceUnavailableError\"}" }, "ses_a", 0);
    try testing.expectEqualStrings("opencode native: HTTP 503 ServiceUnavailableError", unavailable.failed.message);
    try testing.expectEqual(@as(?Failure, null), try cancelInboxResult(scratch, .{ .status = 204 }, "ses_a", "msg_1", 0));
    const missing = (try cancelInboxResult(scratch, .{ .status = 404, .body = "{\"_tag\":\"SessionNotFoundError\"}" }, "ses_a", "msg_1", 0)).?;
    try testing.expectEqualStrings("opencode native: HTTP 404 SessionNotFoundError", missing.message);

    const listed = try activeResult(scratch, .{ .status = 200, .body = "{\"data\":{\"ses_a\":{\"type\":\"running\"},\"ses_b\":{\"type\":\"idle\"},\"ses_c\":null}}" }, 0);
    try testing.expectEqual(@as(usize, 1), listed.ok.len);
    try testing.expectEqualStrings("ses_a", listed.ok[0]);
    const strict = try activeResult(scratch, .{ .status = 200, .body = "{\"data\":{\"ses_a\":{\"type\":\"running\",\"since\":1}}}" }, 0);
    try testing.expectEqualStrings("opencode httpapi: decode /api/session/active response: json: unknown field \"since\"", strict.failed.message);
    try testing.expectEqual(@as(?Failure, null), try infoResult(scratch, .{ .status = 200, .body = "{\"version\":\"2.0.24\",\"pid\":1,\"urls\":[],\"paths\":{},\"capabilities\":{}}" }, 0));
}

fn streamEveryShape(allocator: std.mem.Allocator) !void {
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    var stream = Stream.init(allocator, 0, "ses_a");
    defer stream.deinit();
    const batch = try stream.feed(arena.allocator(), ": keepalive\n\n" ++ connected_frame ++ frame_one ++ frame_two);
    if (batch.events.len != 2) return error.TestUnexpectedResult;
    _ = try stream.finish(arena.allocator());
    _ = try prompt(arena.allocator(), .{ .base_path = "/base", .username = "u", .password = "p" }, "ses_a", .{ .id = "msg_a", .text = "hi", .delivery = "steer" });
    _ = try cancelInbox(arena.allocator(), .{ .base_path = "/base" }, "ses_a", "msg_a");
}

test "the SSE decoder and the codec propagate every allocation failure and leak nothing" {
    try testing.checkAllAllocationFailures(testing.allocator, streamEveryShape, .{});
}

test "a session list decodes every row strictly and refuses one that is not a session" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const listed = try sessionsResult(scratch, .{ .status = 200, .body = "{\"data\":[{\"id\":\"ses_a\",\"projectID\":\"p\",\"title\":\"t\",\"time\":{\"created\":1,\"updated\":2}}],\"cursor\":{\"next\":\"x\"}}" }, 0);
    try testing.expectEqual(@as(usize, 1), listed.ok.infos.len);
    try testing.expectEqualStrings("t", listed.ok.infos[0].title);
    try testing.expectEqual(@as(i64, 2), listed.ok.infos[0].updated);
    try testing.expectEqualStrings("x", listed.ok.next);
    const strict = try sessionsResult(scratch, .{ .status = 200, .body = "{\"data\":[{\"id\":\"ses_a\",\"projectID\":\"p\",\"surprise\":1}],\"cursor\":{}}" }, 0);
    try testing.expect(strict == .failed);
    const invalid = try sessionsResult(scratch, .{ .status = 200, .body = "{\"data\":[{\"id\":\"ses_a\",\"projectID\":\"\"}],\"cursor\":{}}" }, 0);
    try testing.expectEqualStrings(native.invalid_wire ++ ": invalid session info", invalid.failed.message);
    const request = try sessions(scratch, .{}, "", "", 3);
    try testing.expectEqualStrings("/api/session?limit=3&order=desc&parentID=null", request.target);
    try testing.expectEqualStrings("/api/session?cursor=n%2B1&limit=3", (try sessions(scratch, .{}, "/x", "n+1", 3)).target);
    const scoped = try sessions(scratch, .{}, "/x/R&D+a=b c~", "", 3);
    try testing.expectEqualStrings("/api/session?directory=%2Fx%2FR%26D%2Ba%3Db+c~&limit=3&order=desc&parentID=null", scoped.target);
}

test "a message page is asked oldest first, then by its cursor alone, and keeps every message raw" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    try testing.expectEqualStrings("/api/session/ses%20a/message?limit=200&order=asc", (try messages(scratch, .{}, "ses a", "", 200, false)).target);
    try testing.expectEqualStrings("/api/session/ses%20a/message?limit=200&order=desc", (try messages(scratch, .{}, "ses a", "", 200, true)).target);
    try testing.expectEqualStrings("/api/session/ses%20a/message?cursor=n%2B1&limit=200", (try messages(scratch, .{}, "ses a", "n+1", 200, true)).target);
    const page = try messagesResult(scratch, .{ .status = 200, .body = "{\"data\":[{\"type\":\"user\",\"text\":\"x\",\"anything\":1},7],\"cursor\":{\"next\":\"n1\"}}" }, "ses_a", 0);
    try testing.expectEqual(@as(usize, 2), page.ok.messages.len);
    try testing.expectEqualStrings("n1", page.ok.next);
    const last = try messagesResult(scratch, .{ .status = 200, .body = "{\"data\":[],\"cursor\":{}}" }, "ses_a", 0);
    try testing.expectEqualStrings("", last.ok.next);
    const strict = try messagesResult(scratch, .{ .status = 200, .body = "{\"data\":[],\"cursor\":{},\"surprise\":1}" }, "ses_a", 0);
    try testing.expect(strict == .failed);
    const empty = try messagesResult(scratch, .{ .status = 200, .body = "null" }, "ses_a", 0);
    try testing.expectEqual(@as(usize, 0), empty.ok.messages.len);
}
