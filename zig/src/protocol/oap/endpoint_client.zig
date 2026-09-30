const std = @import("std");
const builtin = @import("builtin");
const compat = @import("compat");

pub const max_line_bytes: usize = 1 << 20;

pub const Error = error{
    FrameTooLong,
    EndpointClosed,
    NotRunning,
    EmbeddedNewline,
    ExitGraceElapsed,
    UnclassifiedFrame,
};

pub const Spawn = struct {
    command: []const u8,
    args: []const []const u8 = &.{},
    environment: []const []const u8 = &.{},
};

pub const Budget = struct {
    io: std.Io,
    limit: ?std.Io.Clock.Timestamp,

    pub fn leftOf(limit: std.Io.Clock.Timestamp, now: std.Io.Clock.Timestamp) i96 {
        return now.durationTo(limit).raw.nanoseconds;
    }

    fn fromTimeout(given: std.Io.Timeout, io: std.Io) Budget {
        return .{ .io = io, .limit = given.toTimestamp(io) };
    }

    fn until(io: std.Io, budget_ms: i64) Budget {
        return .{
            .io = io,
            .limit = std.Io.Clock.Timestamp.now(io, .awake).addDuration(.{
                .raw = .fromMilliseconds(@max(budget_ms, 0)),
                .clock = .awake,
            }),
        };
    }

    pub fn unbounded() Budget {
        return .{ .io = undefined, .limit = null };
    }

    fn leftNanoseconds(self: Budget) ?i96 {
        const limit = self.limit orelse return null;
        return leftOf(limit, std.Io.Clock.Timestamp.now(self.io, limit.clock));
    }

    pub fn expired(self: Budget) bool {
        const left = self.leftNanoseconds() orelse return false;
        return left <= 0;
    }

    pub fn timeout(self: Budget) std.Io.Timeout {
        const limit = self.limit orelse return .none;
        const spend = @max(leftOf(limit, std.Io.Clock.Timestamp.now(self.io, limit.clock)), 1);
        return .{ .duration = .{ .raw = .fromNanoseconds(spend), .clock = limit.clock } };
    }
};

pub const Frame = union(enum) {
    envelope: []const u8,
    control: []const u8,
};

pub const Client = struct {
    allocator: std.mem.Allocator,
    threaded: std.Io.Threaded,
    child: ?std.process.Child = null,
    pending: std.ArrayList(u8) = .empty,
    line: std.ArrayList(u8) = .empty,
    closed: bool = false,

    pub fn init(allocator: std.mem.Allocator) Client {
        return .{ .allocator = allocator, .threaded = std.Io.Threaded.init(allocator, .{}) };
    }

    fn io(self: *Client) std.Io {
        return self.threaded.io();
    }

    pub fn spawn(allocator: std.mem.Allocator, request: Spawn) !Client {
        var argv = std.ArrayList([]const u8).empty;
        defer argv.deinit(allocator);
        try argv.append(allocator, request.command);
        try argv.appendSlice(allocator, request.args);

        var environment = if (request.environment.len == 0)
            try compat.runtimeEnviron().createMap(allocator)
        else
            std.process.Environ.Map.init(allocator);
        defer environment.deinit();
        for (request.environment) |name| {
            const value = compat.getEnvVarOwned(allocator, name) catch continue;
            defer allocator.free(value);
            try environment.put(name, value);
        }

        var client = Client.init(allocator);
        errdefer client.deinit();
        client.child = try std.process.spawn(client.io(), .{
            .argv = argv.items,
            .environ_map = &environment,
            .stdin = .pipe,
            .stdout = .pipe,
            .stderr = .inherit,
            .create_no_window = true,
        });
        return client;
    }

    pub fn deinit(self: *Client) void {
        self.close();
        self.pending.deinit(self.allocator);
        self.line.deinit(self.allocator);
        self.threaded.deinit();
    }

    pub fn close(self: *Client) void {
        self.closeStdin();
        if (self.child) |*child| {
            _ = child.wait(self.io()) catch {};
            self.child = null;
        }
        self.closed = true;
    }

    pub fn closeStdin(self: *Client) void {
        if (self.child) |*child| {
            if (child.stdin) |stdin| {
                stdin.close(self.io());
                child.stdin = null;
            }
        }
    }

    pub fn waitExit(self: *Client, grace_ms: i64) !u8 {
        const child = &(self.child orelse return Error.NotRunning);
        var waited: i64 = 0;
        while (waited < grace_ms) {
            if (tryExitPosix(child, self.io())) |term| {
                self.child = null;
                return exitCodeOf(term);
            }
            std.Io.sleep(self.io(), .fromMilliseconds(exit_poll_ms), .boot) catch {};
            waited += exit_poll_ms;
        }
        if (self.child) |*pending| {
            pending.kill(self.io());
            self.child = null;
        }
        return Error.ExitGraceElapsed;
    }

    pub fn write(self: *Client, line: []const u8) !void {
        if (std.mem.indexOfScalar(u8, line, '\n') != null) return Error.EmbeddedNewline;
        if (line.len + 1 > max_line_bytes) return Error.FrameTooLong;
        const child = &(self.child orelse return Error.NotRunning);
        const stdin = child.stdin orelse return Error.NotRunning;
        try stdin.writeStreamingAll(self.io(), line);
        try stdin.writeStreamingAll(self.io(), "\n");
    }

    pub fn budget(self: *Client, budget_ms: i64) Budget {
        return Budget.until(self.io(), budget_ms);
    }

    pub fn budgetFor(self: *Client, timeout: std.Io.Timeout) Budget {
        return Budget.fromTimeout(timeout, self.io());
    }

    pub fn next(self: *Client, timeout: std.Io.Timeout) !?Frame {
        return self.nextBounded(Budget.fromTimeout(timeout, self.io()));
    }

    pub fn nextBounded(self: *Client, allowance: Budget) !?Frame {
        while (true) {
            if (try self.takeLine()) |line| {
                const trimmed = std.mem.trim(u8, line, " \t\r");
                if (trimmed.len == 0) {
                    if (allowance.expired()) return null;
                    continue;
                }
                return try classify(trimmed);
            }
            if (allowance.expired()) return null;
            const filled = try self.fill(allowance.timeout());
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
        multi.init(self.allocator, self.io(), buffer.toStreams(), &.{stdout});
        defer multi.deinit();
        const reader = multi.reader(0);
        multi.fill(1, timeout) catch |raised| switch (raised) {
            error.Timeout => return false,
            error.EndOfStream => return Error.EndpointClosed,
            else => |e| return e,
        };
        if (reader.buffered().len == 0) return false;
        try self.pending.appendSlice(self.allocator, reader.buffered());
        return true;
    }
};

const exit_poll_ms: i64 = 25;

fn tryExitPosix(child: *std.process.Child, io: std.Io) ?std.process.Child.Term {
    if (builtin.os.tag == .windows) return tryExitWindows(child, io);
    const id = child.id orelse return null;
    var status: if (builtin.link_libc) c_int else u32 = undefined;
    while (true) {
        const result = std.posix.system.waitpid(id, &status, std.posix.W.NOHANG);
        switch (std.posix.errno(result)) {
            .SUCCESS => {
                if (result == 0) return null;
                child.id = null;
                closePipes(child, io);
                return termOf(status);
            },
            .INTR => continue,
            else => return null,
        }
    }
}

fn tryExitWindows(child: *std.process.Child, io: std.Io) ?std.process.Child.Term {
    const windows = std.os.windows;
    const handle = child.id orelse return null;
    const poll: windows.LARGE_INTEGER = -(exit_poll_ms * std.time.ns_per_ms / 100);
    switch (windows.ntdll.NtWaitForSingleObject(handle, windows.BOOLEAN.FALSE, &poll)) {
        .WAIT_0 => {},
        .USER_APC, .ALERTED, .TIMEOUT => return null,
        else => |status| {
            std.debug.assert(status == .TIMEOUT);
            return null;
        },
    }
    return child.wait(io) catch null;
}

fn closePipes(child: *std.process.Child, io: std.Io) void {
    if (child.stdin) |stdin| {
        stdin.close(io);
        child.stdin = null;
    }
    if (child.stdout) |stdout| {
        stdout.close(io);
        child.stdout = null;
    }
}

fn termOf(status: anytype) std.process.Child.Term {
    const raw: u32 = @bitCast(status);
    return if (std.posix.W.IFEXITED(raw))
        .{ .exited = std.posix.W.EXITSTATUS(raw) }
    else if (std.posix.W.IFSIGNALED(raw))
        .{ .signal = std.posix.W.TERMSIG(raw) }
    else
        .{ .unknown = raw };
}

fn exitCodeOf(term: std.process.Child.Term) !u8 {
    return switch (term) {
        .exited => |code| code,
        .signal => |number| 128 +% @as(u8, @intCast(@intFromEnum(number))),
        else => Error.NotRunning,
    };
}

fn classify(line: []const u8) Error!Frame {
    if (hasTopLevelMember(line, "protocol")) return .{ .envelope = line };
    if (hasTopLevelMember(line, "control")) return .{ .control = line };
    return Error.UnclassifiedFrame;
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
    const envelope = try classify("{\"protocol\":\"open-agent-protocol\",\"id\":\"q1\"}");
    try std.testing.expect(envelope == .envelope);
    const control = try classify("{\"control\":\"replay\",\"cursor\":\"7\"}");
    try std.testing.expect(control == .control);
}

test "a nested protocol member does not make a control frame an envelope" {
    const control = try classify("{\"control\":\"replay\",\"detail\":{\"protocol\":\"x\"}}");
    try std.testing.expect(control == .control);
}

test "a protocol string that is a value rather than a key is not a member" {
    const control = try classify("{\"control\":\"protocol\"}");
    try std.testing.expect(control == .control);
}

test "an escaped quote inside a key does not end the key early" {
    const control = try classify("{\"control\":\"replay\",\"a\\\"protocol\":1}");
    try std.testing.expect(control == .control);
}

test "a line written to an endpoint comes back framed, and the buffer is the client's" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var client = Client.spawn(std.testing.allocator, .{ .command = "/bin/cat" }) catch return error.SkipZigTest;
    defer client.deinit();

    const sent = "{\"protocol\":\"open-agent-protocol\",\"id\":\"q1\",\"type\":\"capabilities.request\"}";
    try client.write(sent);

    const deadline: std.Io.Timeout = .{ .duration = .{ .raw = std.Io.Duration.fromMilliseconds(5000), .clock = .awake } };
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
    var client = Client.init(std.testing.allocator);
    defer client.deinit();
    try std.testing.expectError(Error.EmbeddedNewline, client.write("{\"a\":1}\n{\"b\":2}"));
}

test "a frame over the bound fails closed rather than truncating" {
    var client = Client.init(std.testing.allocator);
    defer client.deinit();
    const oversized = try std.testing.allocator.alloc(u8, max_line_bytes);
    defer std.testing.allocator.free(oversized);
    @memset(oversized, 'x');
    try std.testing.expectError(Error.FrameTooLong, client.write(oversized));

    try client.pending.appendNTimes(std.testing.allocator, 'y', max_line_bytes);
    try std.testing.expectError(Error.FrameTooLong, client.takeLine());
}

fn probeHome(allowlist: []const []const u8, inherit: bool) ![]const u8 {
    var names = std.ArrayList([]const u8).empty;
    defer names.deinit(std.testing.allocator);
    for (allowlist) |name| try names.append(std.testing.allocator, name);
    if (!inherit) try names.append(std.testing.allocator, "__oapx_unset__");
    var client = try Client.spawn(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", "printf '{\"protocol\":\"p\",\"home\":\"%s\"}\\n' \"$HOME\"" },
        .environment = names.items,
    });
    defer client.deinit();
    const deadline: std.Io.Timeout = .{ .duration = .{ .raw = std.Io.Duration.fromMilliseconds(5000), .clock = .awake } };
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

    const withheld = probeHome(&.{}, false) catch return error.SkipZigTest;
    defer std.testing.allocator.free(withheld);
    try std.testing.expectEqualStrings("", withheld);

    const granted = try probeHome(&.{"HOME"}, false);
    defer std.testing.allocator.free(granted);
    try std.testing.expectEqualStrings(ambient, granted);
}

test "the client drives a real endpoint binary when the operator names one" {
    const named = compat.getEnvVarOwned(std.testing.allocator, "OAPX_ENDPOINT_BIN") catch return error.SkipZigTest;
    defer std.testing.allocator.free(named);
    if (named.len == 0 or !std.fs.path.isAbsolute(named)) return error.SkipZigTest;

    var client = try Client.spawn(std.testing.allocator, .{
        .command = named,
        .args = &.{ "endpoint", "--adapter", "memory" },
        .environment = &.{ "HOME", "PATH" },
    });
    defer client.deinit();

    try client.write("{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"capabilities.request\",\"id\":\"q1\",\"payload\":{}}");

    const deadline: std.Io.Timeout = .{ .duration = .{ .raw = std.Io.Duration.fromMilliseconds(10000), .clock = .awake } };
    var attempts: usize = 0;
    while (attempts < 60) : (attempts += 1) {
        const frame = try client.next(deadline) orelse continue;
        try std.testing.expect(frame == .envelope);
        try std.testing.expect(std.mem.indexOf(u8, frame.envelope, "\"type\":\"capabilities.response\"") != null);
        try std.testing.expect(std.mem.indexOf(u8, frame.envelope, "\"in_reply_to\":\"q1\"") != null);
        return;
    }
    return error.EndpointAnsweredNothing;
}


test "a child inherits this process's environment unless the allowlist says otherwise" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const ambient = compat.getEnvVarOwned(std.testing.allocator, "HOME") catch return error.SkipZigTest;
    defer std.testing.allocator.free(ambient);
    if (ambient.len == 0) return error.SkipZigTest;

    const inherited = try probeHome(&.{}, true);
    defer std.testing.allocator.free(inherited);
    try std.testing.expectEqualStrings(ambient, inherited);
}

test "a budget keeps the shape and clock of the timeout it was given" {
    var client = Client.init(std.testing.allocator);
    defer client.deinit();
    const io = client.io();

    const unbounded = Budget.fromTimeout(.none, io);
    try std.testing.expect(unbounded.limit == null);
    try std.testing.expect(!unbounded.expired());
    try std.testing.expect(unbounded.timeout() == .none);

    const by_duration = Budget.fromTimeout(
        .{ .duration = .{ .raw = std.Io.Duration.fromMilliseconds(60000), .clock = .awake } },
        io,
    );
    try std.testing.expect(by_duration.limit != null);
    try std.testing.expect(!by_duration.expired());
    try std.testing.expect(by_duration.timeout() == .duration);

    const by_deadline = Budget.fromTimeout(
        .{ .deadline = std.Io.Clock.Timestamp.now(io, .awake).addDuration(.{ .raw = std.Io.Duration.fromMilliseconds(60000), .clock = .awake }) },
        io,
    );
    try std.testing.expect(by_deadline.limit != null);
    try std.testing.expect(!by_deadline.expired());
    try std.testing.expect(by_deadline.timeout() == .duration);
    try std.testing.expect(by_deadline.limit.?.clock == .awake);
}


test "the budget arithmetic keeps sub-millisecond precision without a live clock" {
    var client = Client.init(std.testing.allocator);
    defer client.deinit();
    const io = client.io();
    const base = std.Io.Clock.Timestamp.now(io, .awake);

    var at = base;
    try std.testing.expectEqual(@as(i64, 0), @as(i64, @intCast(Budget.leftOf(at, at))));
    at = at.addDuration(.{ .raw = .fromNanoseconds(500_000), .clock = .awake });
    try std.testing.expectEqual(@as(i64, 500_000), @as(i64, @intCast(Budget.leftOf(at, base))));
    const one_nanosecond = base.addDuration(.{ .raw = .fromNanoseconds(1), .clock = .awake });
    try std.testing.expectEqual(@as(i64, 1), @as(i64, @intCast(Budget.leftOf(one_nanosecond, base))));
    const past = base.subDuration(.{ .raw = .fromMilliseconds(5), .clock = .awake });
    try std.testing.expect(Budget.leftOf(past, base) < 0);

    const spent: Budget = .{ .io = io, .limit = past };
    try std.testing.expect(spent.expired());
    try std.testing.expect(std.meta.activeTag(spent.timeout()) == .duration);
    try std.testing.expect(std.meta.activeTag(Budget.unbounded().timeout()) == .none);
}

const partial_tail =
    \\printf 'partial'; sleep 1.5; echo '{"control":"late"}'
;

const flood_tail =
    \\i=0; while [ $i -lt 100 ]; do echo; i=$((i+1)); sleep 0.02; done; echo '{"control":"after"}'
;

const short_budget_ms: i64 = 400;
const open_budget_ms: i64 = 15_000;
const deadline_slack_ns: i64 = 50_000_000;
const bounded_call_limit_ns: i64 = 30_000_000_000;

const OwnedFrame = struct {
    control: bool,
    text: []u8,
};

const Reading = struct {
    elapsed_ns: i64,
    frame: ?OwnedFrame,

    fn deinit(self: Reading, allocator: std.mem.Allocator) void {
        if (self.frame) |frame| allocator.free(frame.text);
    }
};

fn own(allocator: std.mem.Allocator, frame: ?Frame) !?OwnedFrame {
    const seen = frame orelse return null;
    return switch (seen) {
        .control => |text| .{ .control = true, .text = try allocator.dupe(u8, text) },
        .envelope => |text| .{ .control = false, .text = try allocator.dupe(u8, text) },
    };
}

fn readUnder(client: *Client, budget: Budget) !Reading {
    const started = std.Io.Clock.Timestamp.now(std.testing.io, .awake);
    const frame = try client.nextBounded(budget);
    const finished = std.Io.Clock.Timestamp.now(std.testing.io, .awake);
    return .{
        .elapsed_ns = @intCast(started.durationTo(finished).raw.nanoseconds),
        .frame = try own(std.testing.allocator, frame),
    };
}

fn stopChild(client: *Client) void {
    _ = client.waitExit(300) catch {};
}

fn remainingMs(budget: Budget) i64 {
    const limit = budget.limit orelse return 0;
    const now = std.Io.Clock.Timestamp.now(budget.io, limit.clock);
    return @intCast(@max(Budget.leftOf(limit, now), 0) / std.time.ns_per_ms);
}

test "a spent budget stops on the first buffered blank instead of draining the buffer" {
    var client = Client.init(std.testing.allocator);
    defer client.deinit();
    try client.pending.appendNTimes(std.testing.allocator, '\n', 4096);

    const spent: Budget = .{ .io = client.io(), .limit = std.Io.Clock.Timestamp.now(client.io(), .awake).subDuration(.{ .raw = .fromMilliseconds(5), .clock = .awake }) };
    try std.testing.expect(spent.expired());

    const frame = try client.nextBounded(spent);
    try std.testing.expect(frame == null);
    try std.testing.expect(client.pending.items.len > 0);
}

test "none, duration and deadline each read the same frame from one child" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var client = try Client.spawn(std.testing.allocator, .{ .command = "/bin/cat" });
    defer client.deinit();
    defer stopChild(&client);
    const io = client.io();
    const sent = "{\"protocol\":\"open-agent-protocol\",\"id\":\"q1\"}";

    const shapes = [_]Budget{
        Budget.unbounded(),
        Budget.fromTimeout(
            .{ .duration = .{ .raw = std.Io.Duration.fromMilliseconds(60000), .clock = .awake } },
            io,
        ),
        Budget.fromTimeout(
            .{ .deadline = std.Io.Clock.Timestamp.now(io, .awake).addDuration(.{ .raw = std.Io.Duration.fromMilliseconds(60000), .clock = .awake }) },
            io,
        ),
    };

    for (shapes) |budget| {
        try client.write(sent);
        var attempts: usize = 0;
        var seen: ?Frame = null;
        while (seen == null and attempts < 20) : (attempts += 1) {
            seen = try client.nextBounded(budget);
        }
        try std.testing.expect(seen != null);
        try std.testing.expect(seen.? == .envelope);
        try std.testing.expectEqualStrings(sent, seen.?.envelope);
    }

    try std.testing.expect(shapes[0].limit == null);
    try std.testing.expect(shapes[0].timeout() == .none);
    try std.testing.expect(shapes[0].expired() == false);
    try std.testing.expect(shapes[2].limit.?.clock == .awake);
    try std.testing.expect(shapes[2].timeout() == .duration);
}

test "a complete frame already buffered is returned even when the budget is spent" {
    var client = Client.init(std.testing.allocator);
    defer client.deinit();
    const sent = "{\"protocol\":\"open-agent-protocol\",\"id\":\"q1\"}";
    try client.pending.appendSlice(std.testing.allocator, sent);
    try client.pending.append(std.testing.allocator, '\n');

    const spent: Budget = .{ .io = client.io(), .limit = std.Io.Clock.Timestamp.now(client.io(), .awake).subDuration(.{ .raw = .fromMilliseconds(5), .clock = .awake }) };
    try std.testing.expect(spent.expired());

    const frame = try client.nextBounded(spent);
    try std.testing.expect(frame != null);
    try std.testing.expect(frame.? == .envelope);
    try std.testing.expectEqualStrings(sent, frame.?.envelope);
}

test "a short budget returns nothing on a partial line, and the same child then yields its frame" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const allocator = std.testing.allocator;
    var client = try Client.spawn(allocator, .{ .command = "/bin/sh", .args = &.{ "-c", partial_tail } });
    defer client.deinit();
    defer stopChild(&client);

    const short = client.budget(short_budget_ms);
    const allowed = remainingMs(short);
    try std.testing.expect(allowed > 0);

    const first = try readUnder(&client, short);
    defer first.deinit(allocator);
    try std.testing.expect(first.frame == null);
    try std.testing.expect(first.elapsed_ns >= allowed * std.time.ns_per_ms - deadline_slack_ns);
    try std.testing.expect(first.elapsed_ns < bounded_call_limit_ns);
    try std.testing.expect(client.pending.items.len > 0);

    const second = try readUnder(&client, client.budget(open_budget_ms));
    defer second.deinit(allocator);
    try std.testing.expect(second.frame != null);
    try std.testing.expect(second.frame.?.control);
    try std.testing.expectEqualStrings("partial{\"control\":\"late\"}", second.frame.?.text);
    try std.testing.expect(client.pending.items.len == 0);
}

test "a short budget returns nothing on a blank flood, and the same child then yields its frame" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const allocator = std.testing.allocator;
    var client = try Client.spawn(allocator, .{ .command = "/bin/sh", .args = &.{ "-c", flood_tail } });
    defer client.deinit();
    defer stopChild(&client);

    const short = client.budget(short_budget_ms);
    const allowed = remainingMs(short);
    try std.testing.expect(allowed > 0);

    const first = try readUnder(&client, short);
    defer first.deinit(allocator);
    try std.testing.expect(first.frame == null);
    try std.testing.expect(first.elapsed_ns >= allowed * std.time.ns_per_ms - deadline_slack_ns);
    try std.testing.expect(first.elapsed_ns < bounded_call_limit_ns);

    const second = try readUnder(&client, client.budget(open_budget_ms));
    defer second.deinit(allocator);
    try std.testing.expect(second.frame != null);
    try std.testing.expect(second.frame.?.control);
    try std.testing.expectEqualStrings("{\"control\":\"after\"}", second.frame.?.text);
}
