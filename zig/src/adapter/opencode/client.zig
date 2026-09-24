const std = @import("std");
const builtin = @import("builtin");
const compat = @import("compat");
const httpapi = @import("httpapi");

pub const Error = error{
    UnsupportedScheme,
    InvalidEndpoint,
    MalformedResponse,
    ResponseTooLarge,
    Timeout,
    ConnectionClosed,
} || std.mem.Allocator.Error;

pub const Target = struct {
    host: []const u8,
    port: u16,
    base_path: []const u8,
};

pub fn parseEndpoint(arena: std.mem.Allocator, url: []const u8) Error!Target {
    const uri = std.Uri.parse(url) catch return error.InvalidEndpoint;
    if (!std.ascii.eqlIgnoreCase(uri.scheme, "http")) return error.UnsupportedScheme;
    const host_component = uri.host orelse return error.InvalidEndpoint;
    const host = try componentText(arena, host_component);
    if (host.len == 0) return error.InvalidEndpoint;
    const path = try componentText(arena, uri.path);
    return .{ .host = host, .port = uri.port orelse 80, .base_path = path };
}

fn componentText(arena: std.mem.Allocator, component: std.Uri.Component) Error![]const u8 {
    return switch (component) {
        .raw => |text| text,
        .percent_encoded => |text| blk: {
            const copy = try arena.dupe(u8, text);
            break :blk std.Uri.percentDecodeInPlace(copy);
        },
    };
}

pub fn encode(arena: std.mem.Allocator, target: Target, request: httpapi.Request) std.mem.Allocator.Error![]const u8 {
    var out = std.ArrayList(u8).empty;
    try out.print(arena, "{s} {s} HTTP/1.1\r\nHost: {s}:{d}\r\n", .{ request.method, request.target, target.host, target.port });
    for (request.headers) |header| try out.print(arena, "{s}: {s}\r\n", .{ header.name, header.value });
    if (request.body) |body| try out.print(arena, "Content-Length: {d}\r\n", .{body.len});
    try out.appendSlice(arena, "Connection: close\r\n\r\n");
    if (request.body) |body| try out.appendSlice(arena, body);
    return out.items;
}

const line_limit = 4 * 1024;

const Framing = enum { chunk_size, chunk_data, chunk_end, trailer, identity, done };

pub const Reader = struct {
    gpa: std.mem.Allocator,
    limit: usize,
    raw: std.ArrayList(u8) = .empty,
    body: std.ArrayList(u8) = .empty,
    status: u16 = 0,
    head_done: bool = false,
    framing: Framing = .identity,
    remaining: ?usize = null,
    eof: bool = false,

    pub fn init(gpa: std.mem.Allocator, limit: usize) Reader {
        return .{ .gpa = gpa, .limit = limit };
    }

    pub fn deinit(self: *Reader) void {
        self.raw.deinit(self.gpa);
        self.body.deinit(self.gpa);
        self.* = undefined;
    }

    pub fn complete(self: *const Reader) bool {
        return self.head_done and self.framing == .done;
    }

    pub fn push(self: *Reader, bytes: []const u8) Error!void {
        try self.raw.appendSlice(self.gpa, bytes);
        if (!self.head_done) {
            const end = std.mem.indexOf(u8, self.raw.items, "\r\n\r\n") orelse {
                if (self.raw.items.len > 64 * 1024) return error.MalformedResponse;
                return;
            };
            try self.head(self.raw.items[0..end]);
            self.consume(end + 4);
        }
        try self.decode();
    }

    pub fn finish(self: *Reader) Error!void {
        self.eof = true;
        if (!self.head_done) return error.ConnectionClosed;
        if (self.framing == .identity and self.remaining == null) {
            self.framing = .done;
            return;
        }
        if (self.framing != .done) return error.ConnectionClosed;
    }

    fn consume(self: *Reader, count: usize) void {
        const rest = self.raw.items.len - count;
        std.mem.copyForwards(u8, self.raw.items[0..rest], self.raw.items[count..]);
        self.raw.shrinkRetainingCapacity(rest);
    }

    fn head(self: *Reader, text: []const u8) Error!void {
        var lines = std.mem.splitSequence(u8, text, "\r\n");
        const status_line = lines.next() orelse return error.MalformedResponse;
        if (!std.mem.startsWith(u8, status_line, "HTTP/1.")) return error.MalformedResponse;
        var parts = std.mem.splitScalar(u8, status_line, ' ');
        _ = parts.next();
        const code = parts.next() orelse return error.MalformedResponse;
        self.status = std.fmt.parseInt(u16, code, 10) catch return error.MalformedResponse;
        while (lines.next()) |line| {
            const colon = std.mem.indexOfScalar(u8, line, ':') orelse return error.MalformedResponse;
            const name = std.mem.trim(u8, line[0..colon], " \t");
            const value = std.mem.trim(u8, line[colon + 1 ..], " \t");
            if (std.ascii.eqlIgnoreCase(name, "transfer-encoding") and std.ascii.indexOfIgnoreCase(value, "chunked") != null) {
                self.framing = .chunk_size;
            } else if (std.ascii.eqlIgnoreCase(name, "content-length") and self.framing == .identity) {
                self.remaining = std.fmt.parseInt(usize, value, 10) catch return error.MalformedResponse;
            }
        }
        if (self.framing == .chunk_size) self.remaining = null;
        if (self.framing == .identity) {
            if (self.remaining) |length| {
                if (length > self.limit) return error.ResponseTooLarge;
                if (length == 0) self.framing = .done;
            }
        }
        self.head_done = true;
    }

    fn keep(self: *Reader, bytes: []const u8) Error!void {
        if (self.body.items.len + bytes.len > self.limit) return error.ResponseTooLarge;
        try self.body.appendSlice(self.gpa, bytes);
    }

    fn decode(self: *Reader) Error!void {
        while (true) {
            switch (self.framing) {
                .done => return,
                .identity => {
                    const available = self.raw.items;
                    const count = if (self.remaining) |left| @min(left, available.len) else available.len;
                    try self.keep(available[0..count]);
                    self.consume(count);
                    if (self.remaining) |left| {
                        self.remaining = left - count;
                        if (self.remaining.? == 0) self.framing = .done;
                    }
                    return;
                },
                .chunk_size => {
                    const end = std.mem.indexOf(u8, self.raw.items, "\r\n") orelse return self.boundLine();
                    const line = self.raw.items[0..end];
                    const digits = if (std.mem.indexOfScalar(u8, line, ';')) |semi| line[0..semi] else line;
                    const size = std.fmt.parseInt(usize, std.mem.trim(u8, digits, " \t"), 16) catch return error.MalformedResponse;
                    self.consume(end + 2);
                    if (size == 0) {
                        self.framing = .trailer;
                    } else {
                        self.remaining = size;
                        self.framing = .chunk_data;
                    }
                },
                .chunk_data => {
                    const left = self.remaining.?;
                    const count = @min(left, self.raw.items.len);
                    if (count == 0) return;
                    try self.keep(self.raw.items[0..count]);
                    self.consume(count);
                    self.remaining = left - count;
                    if (self.remaining.? == 0) self.framing = .chunk_end;
                },
                .chunk_end => {
                    if (self.raw.items.len < 2) return;
                    if (!std.mem.eql(u8, self.raw.items[0..2], "\r\n")) return error.MalformedResponse;
                    self.consume(2);
                    self.framing = .chunk_size;
                },
                .trailer => {
                    const end = std.mem.indexOf(u8, self.raw.items, "\r\n") orelse return self.boundLine();
                    self.consume(end + 2);
                    if (end == 0) self.framing = .done;
                },
            }
        }
    }

    fn boundLine(self: *const Reader) Error!void {
        if (self.raw.items.len > line_limit) return error.MalformedResponse;
    }

    pub fn take(self: *Reader, arena: std.mem.Allocator) std.mem.Allocator.Error![]const u8 {
        const copy = try arena.dupe(u8, self.body.items);
        self.body.clearRetainingCapacity();
        return copy;
    }
};

pub const Connection = struct {
    stream: compat.net.Stream,
    reader: Reader,
    open: bool = true,

    pub fn start(gpa: std.mem.Allocator, target: Target, bytes: []const u8, limit: usize) !*Connection {
        const self = try gpa.create(Connection);
        errdefer gpa.destroy(self);
        var stream = try compat.net.tcpConnectHost(gpa, target.host, target.port);
        errdefer stream.close();
        try stream.writeAll(bytes);
        self.* = .{ .stream = stream, .reader = Reader.init(gpa, limit) };
        return self;
    }

    pub fn destroy(self: *Connection, gpa: std.mem.Allocator) void {
        self.shut();
        self.reader.deinit();
        gpa.destroy(self);
    }

    pub fn shut(self: *Connection) void {
        if (!self.open) return;
        self.open = false;
        self.stream.close();
    }

    pub fn poll(self: *Connection, wait_ms: i32) !bool {
        if (!self.open) return false;
        if (!try compat.net.readableWithin(compat.net.streamHandle(&self.stream), wait_ms)) return false;
        var buffer: [64 * 1024]u8 = undefined;
        const count = try readSome(&self.stream, &buffer);
        if (count == 0) {
            self.shut();
            try self.reader.finish();
            return true;
        }
        try self.reader.push(buffer[0..count]);
        return true;
    }
};

pub fn readSome(stream: *compat.net.Stream, buffer: []u8) !usize {
    if (builtin.os.tag == .windows) return error.UnsupportedPlatform;
    return std.posix.read(compat.net.streamHandle(stream), buffer) catch |err| switch (err) {
        error.ConnectionResetByPeer => 0,
        else => |failure| return failure,
    };
}

pub fn roundTrip(gpa: std.mem.Allocator, arena: std.mem.Allocator, target: Target, request: httpapi.Request, limit: usize, timeout_ns: u64) !httpapi.Response {
    const bytes = try encode(arena, target, request);
    const connection = try Connection.start(gpa, target, bytes, limit);
    defer connection.destroy(gpa);
    const started = compat.time.monotonicNanos() catch 0;
    while (!connection.reader.complete()) {
        if (!connection.open) return error.ConnectionClosed;
        const now = compat.time.monotonicNanos() catch 0;
        if (now -| started > timeout_ns) return error.Timeout;
        _ = try connection.poll(20);
    }
    return .{ .status = connection.reader.status, .body = try connection.reader.take(arena) };
}

const testing = std.testing;

fn pushAll(reader: *Reader, wire: []const u8, step: usize) !void {
    var at: usize = 0;
    while (at < wire.len) {
        const end = @min(wire.len, at + step);
        try reader.push(wire[at..end]);
        at = end;
    }
}

test "a chunked body decodes whole whatever the read boundaries" {
    const wire = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nContent-Type: text/event-stream\r\n\r\n5\r\nhello\r\n7;ext=1\r\n, world\r\n0\r\n\r\n";
    for ([_]usize{ 1, 2, 3, 7, wire.len }) |step| {
        var reader = Reader.init(testing.allocator, 1024);
        defer reader.deinit();
        try pushAll(&reader, wire, step);
        try testing.expect(reader.complete());
        try testing.expectEqual(@as(u16, 200), reader.status);
        try testing.expectEqualStrings("hello, world", reader.body.items);
    }
}

test "a content-length body completes at its length and a close-delimited one at end of stream" {
    var sized = Reader.init(testing.allocator, 1024);
    defer sized.deinit();
    try sized.push("HTTP/1.1 201 Created\r\nContent-Length: 4\r\n\r\n{}");
    try testing.expect(!sized.complete());
    try sized.push("  ");
    try testing.expect(sized.complete());
    try testing.expectEqualStrings("{}  ", sized.body.items);

    var open = Reader.init(testing.allocator, 1024);
    defer open.deinit();
    try open.push("HTTP/1.1 200 OK\r\n\r\nabc");
    try testing.expect(!open.complete());
    try open.finish();
    try testing.expect(open.complete());
    try testing.expectEqualStrings("abc", open.body.items);
}

test "a truncated chunked body, an oversized body and a non-HTTP head are refused" {
    var truncated = Reader.init(testing.allocator, 1024);
    defer truncated.deinit();
    try truncated.push("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhel");
    try testing.expectError(error.ConnectionClosed, truncated.finish());

    var oversized = Reader.init(testing.allocator, 3);
    defer oversized.deinit();
    try testing.expectError(error.ResponseTooLarge, oversized.push("HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\n"));

    var garbage = Reader.init(testing.allocator, 1024);
    defer garbage.deinit();
    try testing.expectError(error.MalformedResponse, garbage.push("SSH-2.0-OpenSSH\r\n\r\n"));
}

test "a chunk-size or trailer line that never ends is refused once it passes the line bound" {
    var sized = Reader.init(testing.allocator, 1 << 20);
    defer sized.deinit();
    try sized.push("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n");
    const endless = "f" ** 1024;
    var refused: ?Error = null;
    var pushed: usize = 0;
    while (pushed < 8 and refused == null) : (pushed += 1) {
        sized.push(endless) catch |err| {
            refused = err;
        };
    }
    try testing.expectEqual(@as(?Error, error.MalformedResponse), refused);

    var trailed = Reader.init(testing.allocator, 1 << 20);
    defer trailed.deinit();
    try trailed.push("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n");
    try testing.expectError(error.MalformedResponse, trailed.push("x" ** (5 * 1024)));
}

test "an endpoint must be plain http, and its path becomes the base path" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const target = try parseEndpoint(arena.allocator(), "http://127.0.0.1:4096/base");
    try testing.expectEqualStrings("127.0.0.1", target.host);
    try testing.expectEqual(@as(u16, 4096), target.port);
    try testing.expectEqualStrings("/base", target.base_path);
    try testing.expectError(error.UnsupportedScheme, parseEndpoint(arena.allocator(), "https://example.com"));
}
