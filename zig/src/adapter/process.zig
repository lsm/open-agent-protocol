const std = @import("std");
const builtin = @import("builtin");

pub const default_frame_limit: usize = 8 << 20;
pub const default_exit_grace_ns: u64 = 5 * std.time.ns_per_s;

pub const Error = error{
    ExecutableRequired,
    EnvironmentEntryWithoutValue,
    FrameTooLarge,
    InvalidFrame,
    UnterminatedFrame,
    EmbeddedNewline,
    NotRunning,
};

pub const Spawn = struct {
    executable: []const u8,
    args: []const []const u8 = &.{},
    environment: []const []const u8 = &.{},
    working_directory: ?[]const u8 = null,
    frame_limit: usize = default_frame_limit,
    exit_grace_ns: u64 = default_exit_grace_ns,
};

pub const Departure = enum { running, exited, signalled, stranded };

pub const Failure = struct {
    departure: Departure,
    status: u32 = 0,

    pub fn text(self: Failure, arena: std.mem.Allocator) []const u8 {
        return switch (self.departure) {
            .running => "child is running",
            .exited => std.fmt.allocPrint(arena, "child exited with status {d}", .{self.status}) catch "child exited",
            .signalled => std.fmt.allocPrint(arena, "child was terminated by signal {d}", .{self.status}) catch "child was terminated by a signal",
            .stranded => "child did not exit after stdin was closed",
        };
    }
};

fn io() std.Io {
    return if (builtin.is_test) std.testing.io else std.Io.Threaded.global_single_threaded.io();
}

pub const Transport = struct {
    allocator: std.mem.Allocator,
    limit: usize,
    grace: u64,
    child: ?std.process.Child = null,
    streams: std.Io.File.MultiReader.Buffer(1) = undefined,
    multi: std.Io.File.MultiReader = undefined,
    reading: bool = false,
    pending: std.ArrayList(u8) = .empty,
    line: std.ArrayList(u8) = .empty,
    ended: bool = false,
    failure: Failure = .{ .departure = .running },

    pub fn open(allocator: std.mem.Allocator, request: Spawn) !*Transport {
        if (request.executable.len == 0) return Error.ExecutableRequired;

        var argv = std.ArrayList([]const u8).empty;
        defer argv.deinit(allocator);
        try argv.append(allocator, request.executable);
        try argv.appendSlice(allocator, request.args);

        var environment = std.process.Environ.Map.init(allocator);
        defer environment.deinit();
        for (request.environment) |entry| {
            const split = std.mem.indexOfScalar(u8, entry, '=') orelse return Error.EnvironmentEntryWithoutValue;
            try environment.put(entry[0..split], entry[split + 1 ..]);
        }

        const self = try allocator.create(Transport);
        errdefer allocator.destroy(self);

        var child = try std.process.spawn(io(), .{
            .argv = argv.items,
            .environ_map = &environment,
            .cwd = if (request.working_directory) |path| .{ .path = path } else .inherit,
            .stdin = .pipe,
            .stdout = .pipe,
            .stderr = .inherit,
            .create_no_window = true,
        });
        errdefer child.kill(io());

        self.* = .{
            .allocator = allocator,
            .limit = request.frame_limit,
            .grace = request.exit_grace_ns,
            .child = child,
        };
        self.multi.init(allocator, io(), self.streams.toStreams(), &.{child.stdout.?});
        self.reading = true;
        return self;
    }

    pub fn deinit(self: *Transport) void {
        self.close();
        if (self.reading) {
            self.multi.deinit();
            self.reading = false;
        }
        self.pending.deinit(self.allocator);
        self.line.deinit(self.allocator);
        self.allocator.destroy(self);
    }

    pub fn write(self: *Transport, frame: []const u8) !void {
        if (std.mem.indexOfScalar(u8, frame, '\n') != null) return Error.EmbeddedNewline;
        if (frame.len > self.limit) return Error.FrameTooLarge;
        const child = &(self.child orelse return Error.NotRunning);
        const stdin = child.stdin orelse return Error.NotRunning;
        try stdin.writeStreamingAll(io(), frame);
        try stdin.writeStreamingAll(io(), "\n");
    }

    pub fn next(self: *Transport) !?[]const u8 {
        while (true) {
            if (try self.take()) |frame| return frame;
            if (self.ended) {
                if (self.pending.items.len > 0) return Error.UnterminatedFrame;
                return null;
            }
            try self.fill();
        }
    }

    fn take(self: *Transport) !?[]const u8 {
        const at = std.mem.indexOfScalar(u8, self.pending.items, '\n') orelse {
            if (self.pending.items.len > self.limit) return Error.FrameTooLarge;
            return null;
        };
        if (at > self.limit) return Error.FrameTooLarge;
        self.line.clearRetainingCapacity();
        try self.line.appendSlice(self.allocator, self.pending.items[0..at]);
        const remaining = self.pending.items.len - (at + 1);
        std.mem.copyForwards(u8, self.pending.items, self.pending.items[at + 1 ..]);
        self.pending.shrinkRetainingCapacity(remaining);
        const frame = self.line.items;
        if (frame.len == 0) return Error.InvalidFrame;
        if (std.mem.indexOfScalar(u8, frame, '\r') != null) return Error.InvalidFrame;
        if (!std.unicode.utf8ValidateSlice(frame)) return Error.InvalidFrame;
        return frame;
    }

    fn fill(self: *Transport) !void {
        if (!self.reading) {
            self.ended = true;
            return;
        }
        self.multi.fill(1, .none) catch |raised| switch (raised) {
            error.EndOfStream => {
                self.ended = true;
                return;
            },
            else => |leftover| return leftover,
        };
        const reader = self.multi.reader(0);
        const arrived = reader.buffered();
        if (arrived.len > 0) {
            try self.pending.appendSlice(self.allocator, arrived);
            reader.toss(arrived.len);
        }
    }

    pub fn close(self: *Transport) void {
        const child = &(self.child orelse return);
        if (child.stdin) |stdin| {
            stdin.close(io());
            child.stdin = null;
        }
        if (!self.settles()) return self.strand(child);
        const term = child.wait(io()) catch return self.strand(child);
        self.failure = switch (term) {
            .exited => |status| .{ .departure = .exited, .status = status },
            .signal, .stopped => |signal| .{ .departure = .signalled, .status = @intFromEnum(signal) },
            .unknown => |status| .{ .departure = .exited, .status = status },
        };
        self.child = null;
    }

    fn strand(self: *Transport, child: *std.process.Child) void {
        child.kill(io());
        self.failure = .{ .departure = .stranded };
        self.child = null;
    }

    fn settles(self: *Transport) bool {
        if (!self.reading or self.ended) return true;
        const budget: std.Io.Timeout = .{ .duration = .{ .clock = .awake, .raw = .fromNanoseconds(@intCast(self.grace)) } };
        const deadline = budget.toDeadline(io());
        while (true) {
            self.multi.fill(1, deadline) catch |raised| switch (raised) {
                error.EndOfStream => {
                    self.ended = true;
                    return true;
                },
                error.Timeout => return false,
                else => return true,
            };
            const reader = self.multi.reader(0);
            const arrived = reader.buffered();
            if (arrived.len > 0) reader.toss(arrived.len);
        }
    }

    pub fn departed(self: *const Transport) Failure {
        return self.failure;
    }
};

const testing = std.testing;

fn shell(script: []const u8, request: Spawn) !*Transport {
    if (builtin.os.tag == .windows) return error.SkipZigTest;
    var spawn = request;
    spawn.executable = "/bin/sh";
    spawn.args = &.{ "-c", script };
    return Transport.open(testing.allocator, spawn) catch return error.SkipZigTest;
}

fn drain(transport: *Transport, arena: std.mem.Allocator) ![]const []const u8 {
    var frames = std.ArrayList([]const u8).empty;
    while (try transport.next()) |frame| {
        try frames.append(arena, try arena.dupe(u8, frame));
    }
    return frames.items;
}

test "frames arrive whole and in the order the child wrote them" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const transport = try shell("printf 'a\\nbb\\nccc\\n'", .{ .executable = "" });
    defer transport.deinit();

    const frames = try drain(transport, arena.allocator());
    try testing.expectEqual(@as(usize, 3), frames.len);
    try testing.expectEqualStrings("a", frames[0]);
    try testing.expectEqualStrings("bb", frames[1]);
    try testing.expectEqualStrings("ccc", frames[2]);
}

test "a child that writes nothing ends the stream rather than stalling it" {
    const transport = try shell("true", .{ .executable = "" });
    defer transport.deinit();

    try testing.expect(try transport.next() == null);
}

test "bytes left over when the child goes are an unterminated frame, not a frame" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const transport = try shell("printf 'a\\nbb'", .{ .executable = "" });
    defer transport.deinit();

    try testing.expectEqualStrings("a", (try transport.next()).?);
    try testing.expectError(Error.UnterminatedFrame, transport.next());
}

test "an empty line, a carriage return and invalid UTF-8 are framing defects" {
    const empty = try shell("printf '\\n'", .{ .executable = "" });
    defer empty.deinit();
    try testing.expectError(Error.InvalidFrame, empty.next());

    const carriage = try shell("printf 'a\\r\\n'", .{ .executable = "" });
    defer carriage.deinit();
    try testing.expectError(Error.InvalidFrame, carriage.next());

    const mangled = try shell("printf '\\377\\n'", .{ .executable = "" });
    defer mangled.deinit();
    try testing.expectError(Error.InvalidFrame, mangled.next());
}

test "a frame past the limit is refused rather than delivered in pieces" {
    const transport = try shell("printf 'aaaaaaaaaa\\n'", .{ .executable = "", .frame_limit = 4 });
    defer transport.deinit();

    try testing.expectError(Error.FrameTooLarge, transport.next());
}

test "the child sees exactly the environment it was given and nothing ambient" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const transport = try shell("env", .{ .executable = "", .environment = &.{"OAP_TRANSPORT_PROBE=kept"} });
    defer transport.deinit();

    const frames = try drain(transport, arena.allocator());
    var saw_probe = false;
    for (frames) |frame| {
        if (std.mem.startsWith(u8, frame, "OAP_TRANSPORT_PROBE=")) saw_probe = true;
        try testing.expect(!std.mem.startsWith(u8, frame, "PATH="));
    }
    try testing.expect(saw_probe);
}

test "an environment entry that names no value is refused before the child starts" {
    try testing.expectError(Error.EnvironmentEntryWithoutValue, Transport.open(testing.allocator, .{
        .executable = "/bin/sh",
        .args = &.{ "-c", "true" },
        .environment = &.{"BARE"},
    }));
}

test "a transport with no executable is refused before anything is spawned" {
    try testing.expectError(Error.ExecutableRequired, Transport.open(testing.allocator, .{ .executable = "" }));
}

test "the child runs where it was told to, not where its parent happens to be" {
    if (builtin.os.tag == .windows) return error.SkipZigTest;
    const transport = try shell("pwd", .{ .executable = "", .working_directory = "/" });
    defer transport.deinit();

    try testing.expectEqualStrings("/", (try transport.next()).?);
}

test "a frame written to the child comes back through a child that echoes it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const transport = try shell("cat", .{ .executable = "" });
    defer transport.deinit();

    try transport.write("{\"a\":1}");
    try testing.expectEqualStrings("{\"a\":1}", (try transport.next()).?);
}

test "a frame carrying its own newline is refused rather than split in two" {
    const transport = try shell("cat", .{ .executable = "" });
    defer transport.deinit();

    try testing.expectError(Error.EmbeddedNewline, transport.write("{\"a\":1}\n{\"b\":2}"));
}

test "a frame past the limit is refused on the way out as well as on the way in" {
    const transport = try shell("cat", .{ .executable = "", .frame_limit = 4 });
    defer transport.deinit();

    try transport.write("abcd");
    try testing.expectError(Error.FrameTooLarge, transport.write("abcde"));
    try testing.expectEqualStrings("abcd", (try transport.next()).?);
}

test "how the child left is reported, and by what" {
    const exited = try shell("exit 3", .{ .executable = "" });
    defer exited.deinit();
    try testing.expect(try exited.next() == null);
    exited.close();
    try testing.expectEqual(Departure.exited, exited.departed().departure);
    try testing.expectEqual(@as(u32, 3), exited.departed().status);

    const signalled = try shell("kill -TERM $$", .{ .executable = "" });
    defer signalled.deinit();
    try testing.expect(try signalled.next() == null);
    signalled.close();
    try testing.expectEqual(Departure.signalled, signalled.departed().departure);
}

test "a running child has not departed" {
    const transport = try shell("cat", .{ .executable = "" });
    defer transport.deinit();

    try testing.expectEqual(Departure.running, transport.departed().departure);
}

test "a child that ignores the closed stdin is killed rather than waited on forever" {
    const transport = try shell("exec sleep 30", .{ .executable = "", .exit_grace_ns = 50 * std.time.ns_per_ms });
    defer transport.deinit();

    transport.close();
    try testing.expectEqual(Departure.stranded, transport.departed().departure);
}

test "a child that goes when stdin closes is waited on, not killed" {
    const transport = try shell("cat", .{ .executable = "", .exit_grace_ns = 5 * std.time.ns_per_s });
    defer transport.deinit();

    transport.close();
    try testing.expectEqual(Departure.exited, transport.departed().departure);
    try testing.expectEqual(@as(u32, 0), transport.departed().status);
}

test "closing the transport releases the child's pipes, not only its stdin" {
    const gone = try shell("printf 'a\\n'", .{ .executable = "" });
    defer gone.deinit();
    try testing.expectEqualStrings("a", (try gone.next()).?);
    gone.close();
    try testing.expect(gone.child == null);

    const killed = try shell("exec sleep 30", .{ .executable = "", .exit_grace_ns = 50 * std.time.ns_per_ms });
    defer killed.deinit();
    killed.close();
    try testing.expectEqual(Departure.stranded, killed.departed().departure);
    try testing.expect(killed.child == null);
}

test "a chatty child that will not go is killed on one budget, not one per read" {
    const transport = try shell("while :; do printf 'x\\n'; sleep 0.01; done", .{ .executable = "", .exit_grace_ns = 150 * std.time.ns_per_ms });
    defer transport.deinit();

    _ = try transport.next();
    transport.close();

    try testing.expectEqual(Departure.stranded, transport.departed().departure);
}
