const std = @import("std");
const compat = @import("compat");

fn defaultIo() std.Io {
    return if (@import("builtin").is_test)
        std.testing.io
    else
        std.Io.Threaded.global_single_threaded.io();
}

pub const max_line_bytes: usize = 1 << 20;

pub const Error = error{
    FrameTooLong,
    EndpointClosed,
    NotRunning,
    EmbeddedNewline,
};

pub const Spawn = struct {
    command: []const u8,
    args: []const []const u8 = &.{},
    environment: []const []const u8 = &.{},
};

pub const Frame = union(enum) {
    envelope: []const u8,
    control: []const u8,
};

pub const Client = struct {
    allocator: std.mem.Allocator,
    child: ?std.process.Child = null,
    pending: std.ArrayList(u8) = .empty,
    line: std.ArrayList(u8) = .empty,
    closed: bool = false,

    pub fn spawn(allocator: std.mem.Allocator, request: Spawn) !Client {
        var argv = std.ArrayList([]const u8).empty;
        defer argv.deinit(allocator);
        try argv.append(allocator, request.command);
        try argv.appendSlice(allocator, request.args);

        var environment = std.process.Environ.Map.init(allocator);
        defer environment.deinit();
        for (request.environment) |name| {
            const value = compat.getEnvVarOwned(allocator, name) catch continue;
            defer allocator.free(value);
            try environment.put(name, value);
        }

        const child = try std.process.spawn(defaultIo(), .{
            .argv = argv.items,
            .environ_map = &environment,
            .stdin = .pipe,
            .stdout = .pipe,
            .stderr = .inherit,
            .create_no_window = true,
        });
        return .{ .allocator = allocator, .child = child };
    }

    pub fn deinit(self: *Client) void {
        self.close();
        self.pending.deinit(self.allocator);
        self.line.deinit(self.allocator);
    }

    pub fn close(self: *Client) void {
        if (self.child) |*child| {
            if (child.stdin) |stdin| {
                stdin.close(defaultIo());
                child.stdin = null;
            }
            _ = child.wait(defaultIo()) catch {};
            self.child = null;
        }
        self.closed = true;
    }

    pub fn write(self: *Client, line: []const u8) !void {
        if (std.mem.indexOfScalar(u8, line, '\n') != null) return Error.EmbeddedNewline;
        if (line.len + 1 > max_line_bytes) return Error.FrameTooLong;
        const child = &(self.child orelse return Error.NotRunning);
        const stdin = child.stdin orelse return Error.NotRunning;
        try stdin.writeStreamingAll(defaultIo(), line);
        try stdin.writeStreamingAll(defaultIo(), "\n");
    }

    pub fn next(self: *Client, timeout: std.Io.Timeout) !?Frame {
        while (true) {
            if (try self.takeLine()) |line| {
                const trimmed = std.mem.trim(u8, line, " \t\r");
                if (trimmed.len == 0) continue;
                return classify(trimmed);
            }
            const filled = try self.fill(timeout);
            if (!filled) return null;
        }
    }

    fn takeLine(self: *Client) !?[]const u8 {
        const at = std.mem.indexOfScalar(u8, self.pending.items, '\n') orelse {
            if (self.pending.items.len >= max_line_bytes) return Error.FrameTooLong;
            return null;
        };
        if (at + 1 > max_line_bytes) return Error.FrameTooLong;
        self.line.clearRetainingCapacity();
        try self.line.appendSlice(self.allocator, self.pending.items[0..at]);
        const remaining = self.pending.items.len - (at + 1);
        std.mem.copyForwards(u8, self.pending.items, self.pending.items[at + 1 ..]);
        self.pending.shrinkRetainingCapacity(remaining);
        return self.line.items;
    }

    fn fill(self: *Client, timeout: std.Io.Timeout) !bool {
        const child = &(self.child orelse return Error.NotRunning);
        const stdout = child.stdout orelse return Error.NotRunning;
        var buffer: std.Io.File.MultiReader.Buffer(1) = undefined;
        var multi: std.Io.File.MultiReader = undefined;
        multi.init(self.allocator, defaultIo(), buffer.toStreams(), &.{stdout});
        defer multi.deinit();
        const reader = multi.reader(0);
        multi.fill(1, timeout) catch |raised| switch (raised) {
            error.Timeout => return false,
            error.EndOfStream => return Error.EndpointClosed,
            else => |e| return e,
        };
        try self.pending.appendSlice(self.allocator, reader.buffered());
        return true;
    }
};

fn classify(line: []const u8) Frame {
    if (hasTopLevelMember(line, "protocol")) return .{ .envelope = line };
    return .{ .control = line };
}

fn hasTopLevelMember(line: []const u8, name: []const u8) bool {
    var at: usize = 0;
    var depth: usize = 0;
    var in_string = false;
    var escaped = false;
    var key_start: ?usize = null;
    while (at < line.len) : (at += 1) {
        const c = line[at];
        if (in_string) {
            if (escaped) {
                escaped = false;
            } else if (c == '\\') {
                escaped = true;
            } else if (c == '"') {
                in_string = false;
                if (depth == 1 and key_start != null) {
                    const key = line[key_start.?..at];
                    if (std.mem.eql(u8, key, name) and followedByColon(line, at + 1)) return true;
                }
                key_start = null;
            }
            continue;
        }
        switch (c) {
            '"' => {
                in_string = true;
                escaped = false;
                key_start = at + 1;
            },
            '{', '[' => depth += 1,
            '}', ']' => if (depth > 0) {
                depth -= 1;
            },
            else => {},
        }
    }
    return false;
}

fn followedByColon(line: []const u8, from: usize) bool {
    var at = from;
    while (at < line.len) : (at += 1) {
        switch (line[at]) {
            ' ', '\t' => continue,
            ':' => return true,
            else => return false,
        }
    }
    return false;
}

test "a line carrying protocol is an envelope and one carrying control is not" {
    const envelope = classify("{\"protocol\":\"open-agent-protocol\",\"id\":\"q1\"}");
    try std.testing.expect(envelope == .envelope);
    const control = classify("{\"control\":\"replay\",\"cursor\":\"7\"}");
    try std.testing.expect(control == .control);
}

test "a nested protocol member does not make a control frame an envelope" {
    const control = classify("{\"control\":\"replay\",\"detail\":{\"protocol\":\"x\"}}");
    try std.testing.expect(control == .control);
}

test "a protocol string that is a value rather than a key is not a member" {
    const control = classify("{\"control\":\"protocol\"}");
    try std.testing.expect(control == .control);
}

test "an escaped quote inside a key does not end the key early" {
    const control = classify("{\"a\\\"protocol\":1}");
    try std.testing.expect(control == .control);
}

test "a line written to an endpoint comes back framed, and the buffer is the client's" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var client = Client.spawn(std.testing.allocator, .{ .command = "/bin/cat" }) catch return error.SkipZigTest;
    defer client.deinit();

    const sent = "{\"protocol\":\"open-agent-protocol\",\"id\":\"q1\",\"type\":\"capabilities.request\"}";
    try client.write(sent);

    const deadline: std.Io.Timeout = .{ .duration = .{ .raw = std.Io.Duration.fromMilliseconds(5000), .clock = .boot } };
    var seen: ?Frame = null;
    var attempts: usize = 0;
    while (seen == null and attempts < 20) : (attempts += 1) {
        seen = try client.next(deadline);
    }
    try std.testing.expect(seen != null);
    try std.testing.expect(seen.? == .envelope);
    try std.testing.expectEqualStrings(sent, seen.?.envelope);
}

test "a line carrying a newline is refused rather than split into two frames" {
    var client = Client{ .allocator = std.testing.allocator };
    defer client.deinit();
    try std.testing.expectError(Error.EmbeddedNewline, client.write("{\"a\":1}\n{\"b\":2}"));
}

test "a frame over the bound fails closed rather than truncating" {
    var client = Client{ .allocator = std.testing.allocator };
    defer client.deinit();
    const oversized = try std.testing.allocator.alloc(u8, max_line_bytes);
    defer std.testing.allocator.free(oversized);
    @memset(oversized, 'x');
    try std.testing.expectError(Error.FrameTooLong, client.write(oversized));

    try client.pending.appendNTimes(std.testing.allocator, 'y', max_line_bytes);
    try std.testing.expectError(Error.FrameTooLong, client.takeLine());
}

fn probeHome(allowlist: []const []const u8) ![]const u8 {
    var client = try Client.spawn(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", "printf '{\"protocol\":\"p\",\"home\":\"%s\"}\\n' \"$HOME\"" },
        .environment = allowlist,
    });
    defer client.deinit();
    const deadline: std.Io.Timeout = .{ .duration = .{ .raw = std.Io.Duration.fromMilliseconds(5000), .clock = .boot } };
    var attempts: usize = 0;
    while (attempts < 20) : (attempts += 1) {
        const frame = try client.next(deadline) orelse continue;
        const opened = std.mem.indexOf(u8, frame.envelope, "\"home\":\"") orelse return error.Unexpected;
        const from = opened + "\"home\":\"".len;
        const closed = std.mem.indexOfScalarPos(u8, frame.envelope, from, '"') orelse return error.Unexpected;
        return std.testing.allocator.dupe(u8, frame.envelope[from..closed]);
    }
    return error.Unexpected;
}

test "a child sees only the names the operator allowlisted, never the ambient environment" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const ambient = compat.getEnvVarOwned(std.testing.allocator, "HOME") catch return error.SkipZigTest;
    defer std.testing.allocator.free(ambient);
    if (ambient.len == 0) return error.SkipZigTest;

    const withheld = probeHome(&.{}) catch return error.SkipZigTest;
    defer std.testing.allocator.free(withheld);
    try std.testing.expectEqualStrings("", withheld);

    const granted = try probeHome(&.{"HOME"});
    defer std.testing.allocator.free(granted);
    try std.testing.expectEqualStrings(ambient, granted);
}
