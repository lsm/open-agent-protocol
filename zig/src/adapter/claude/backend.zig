const std = @import("std");
const process = @import("process");
const session = @import("session");
const rpc = @import("rpc");

pub const Error = error{
    ExecutableRequired,
    ToolPostureRequired,
    InvalidTurnUUID,
};

pub const ToolPosture = union(enum) {
    unrestricted,
    allowed: []const []const u8,
};

pub const Config = struct {
    executable: []const u8,
    args: []const []const u8 = &.{},
    environment: []const []const u8 = &.{},
    working_directory: ?[]const u8 = null,
    model: []const u8 = "",
    tools: ?ToolPosture = null,
    frame_limit: usize = process.default_frame_limit,
    exit_grace_ns: u64 = process.default_exit_grace_ns,
};

const fixed_argv = [_][]const u8{
    "--output-format",          "stream-json",
    "--verbose",                "--input-format",
    "stream-json",              "--system-prompt",
    "",                         "--include-partial-messages",
    "--permission-prompt-tool", "stdio",
    "--setting-sources=",
};

pub fn spawnFor(arena: std.mem.Allocator, config: Config) !process.Spawn {
    if (config.executable.len == 0) return Error.ExecutableRequired;
    const posture = config.tools orelse return Error.ToolPostureRequired;

    var argv = std.ArrayList([]const u8).empty;
    try argv.appendSlice(arena, &fixed_argv);
    if (config.model.len != 0) {
        try argv.append(arena, "--model");
        try argv.append(arena, config.model);
    }
    switch (posture) {
        .unrestricted => {},
        .allowed => |names| {
            try argv.append(arena, "--allowedTools");
            try argv.appendSlice(arena, names);
        },
    }
    try argv.appendSlice(arena, config.args);

    return .{
        .executable = config.executable,
        .args = try argv.toOwnedSlice(arena),
        .environment = config.environment,
        .working_directory = config.working_directory,
        .frame_limit = config.frame_limit,
        .exit_grace_ns = config.exit_grace_ns,
    };
}

pub fn validateTurnUUID(uuid: []const u8) !void {
    if (uuid.len == 0 or uuid.len > 128) return Error.InvalidTurnUUID;
    for (uuid) |char| {
        switch (char) {
            'a'...'z', 'A'...'Z', '0'...'9', '-' => {},
            else => return Error.InvalidTurnUUID,
        }
    }
}

pub fn userTurn(arena: std.mem.Allocator, uuid: []const u8, text: []const u8) ![]const u8 {
    try validateTurnUUID(uuid);
    const content = try std.json.Stringify.valueAlloc(arena, text, .{});
    const turn = try std.json.Stringify.valueAlloc(arena, uuid, .{});
    return std.mem.concat(arena, u8, &.{
        "{\"message\":{\"content\":",
        content,
        ",\"role\":\"user\"},\"origin\":{\"kind\":\"human\"},\"parent_tool_use_id\":null,\"session_id\":\"default\",\"type\":\"user\",\"uuid\":",
        turn,
        "}",
    });
}

pub const Backend = struct {
    arena: *std.heap.ArenaAllocator,
    transport: *process.Transport,
    reducer: session.Reducer,
    settled: bool = false,
    closed: bool = false,

    pub fn open(
        arena: *std.heap.ArenaAllocator,
        config: Config,
        options: session.Options,
    ) !Backend {
        return openWith(arena, try spawnFor(arena.allocator(), config), options);
    }

    pub fn openWith(
        arena: *std.heap.ArenaAllocator,
        spawn: process.Spawn,
        options: session.Options,
    ) !Backend {
        const transport = try process.Transport.open(arena.allocator(), spawn);
        errdefer transport.deinit();
        var reducer = session.Reducer.init(arena, options);
        reducer.open();
        return .{ .arena = arena, .transport = transport, .reducer = reducer };
    }

    pub fn submit(self: *Backend, uuid: []const u8, text: []const u8, identity: session.Identity) !void {
        const frame = try userTurn(self.arena.allocator(), uuid, text);
        try self.reducer.submitAs(uuid, identity);
        try self.transport.write(frame);
    }

    pub fn pump(self: *Backend) !bool {
        if (self.settled) return false;
        const line = self.transport.next() catch |err| {
            try self.settle(err);
            return false;
        };
        const bytes = line orelse {
            try self.settle(null);
            return false;
        };
        const arena = self.arena.allocator();
        const held = try arena.dupe(u8, bytes);
        var diagnostic = rpc.Diagnostic{};
        const message = rpc.parseMessage(arena, held, &diagnostic) catch {
            self.settled = true;
            self.reap();
            try self.reducer.transportFailed(diagnostic.message);
            return false;
        };
        try self.reducer.observe(message);
        return true;
    }

    fn settle(self: *Backend, err: ?anyerror) !void {
        self.settled = true;
        self.reap();
        const failure = self.transport.departed();
        if (err) |raised| {
            if (failure.departure == .running) {
                try self.reducer.transportFailed(@errorName(raised));
                return;
            }
        }
        try self.reducer.transportFailed(failure.text(self.arena.allocator()));
    }

    fn reap(self: *Backend) void {
        if (self.closed) return;
        self.closed = true;
        self.transport.close();
    }

    pub fn envelopes(self: *Backend) []std.json.Value {
        return self.reducer.envelopes.items;
    }

    pub fn close(self: *Backend) void {
        self.reap();
        self.transport.deinit();
    }
};

test "the argv is the fixed nine the pinned CLI needs, in the pinned order" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const spawn = try spawnFor(arena.allocator(), .{ .executable = "/bin/claude", .tools = .unrestricted });
    const want = [_][]const u8{
        "--output-format",          "stream-json",
        "--verbose",                "--input-format",
        "stream-json",              "--system-prompt",
        "",                         "--include-partial-messages",
        "--permission-prompt-tool", "stdio",
        "--setting-sources=",
    };
    try std.testing.expectEqual(want.len, spawn.args.len);
    for (want, spawn.args) |expected, got| try std.testing.expectEqualStrings(expected, got);
}

test "a model and a tool allowlist follow the fixed argv, in that order" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const spawn = try spawnFor(arena.allocator(), .{
        .executable = "/bin/claude",
        .model = "claude-opus-5",
        .tools = .{ .allowed = &.{ "Read", "Grep" } },
        .args = &.{"--extra"},
    });
    const tail = spawn.args[fixed_argv.len..];
    const want = [_][]const u8{ "--model", "claude-opus-5", "--allowedTools", "Read", "Grep", "--extra" };
    try std.testing.expectEqual(want.len, tail.len);
    for (want, tail) |expected, got| try std.testing.expectEqualStrings(expected, got);
}

test "an unrestricted posture passes no allowlist, which is not the same as an empty one" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const unrestricted = try spawnFor(arena.allocator(), .{ .executable = "/bin/claude", .tools = .unrestricted });
    for (unrestricted.args) |arg| try std.testing.expect(!std.mem.eql(u8, arg, "--allowedTools"));

    const empty = try spawnFor(arena.allocator(), .{ .executable = "/bin/claude", .tools = .{ .allowed = &.{} } });
    try std.testing.expectEqualStrings("--allowedTools", empty.args[empty.args.len - 1]);
}

test "a config that states no tool posture is refused rather than defaulted" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    try std.testing.expectError(Error.ToolPostureRequired, spawnFor(arena.allocator(), .{ .executable = "/bin/claude" }));
    try std.testing.expectError(Error.ExecutableRequired, spawnFor(arena.allocator(), .{ .executable = "", .tools = .unrestricted }));
}

test "a turn uuid is alphanumeric with hyphens and at most 128 bytes" {
    try validateTurnUUID("turn-1");
    try validateTurnUUID("a" ** 128);
    try std.testing.expectError(Error.InvalidTurnUUID, validateTurnUUID(""));
    try std.testing.expectError(Error.InvalidTurnUUID, validateTurnUUID("a" ** 129));
    try std.testing.expectError(Error.InvalidTurnUUID, validateTurnUUID("turn_1"));
    try std.testing.expectError(Error.InvalidTurnUUID, validateTurnUUID("turn 1"));
}

test "the user turn is the frame the pinned marshal produces, key order included" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const frame = try userTurn(arena.allocator(), "turn-1", "fix the test");
    try std.testing.expectEqualStrings(
        "{\"message\":{\"content\":\"fix the test\",\"role\":\"user\"},\"origin\":{\"kind\":\"human\"}," ++
            "\"parent_tool_use_id\":null,\"session_id\":\"default\",\"type\":\"user\",\"uuid\":\"turn-1\"}",
        frame,
    );
}

test "a turn refuses its uuid before it builds a frame" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    try std.testing.expectError(Error.InvalidTurnUUID, userTurn(arena.allocator(), "turn_1", "hi"));
}

test "prompt text is escaped by the encoder rather than concatenated" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const frame = try userTurn(arena.allocator(), "turn-1", "a\"b\\c\nd");
    try std.testing.expect(std.mem.indexOf(u8, frame, "\"content\":\"a\\\"b\\\\c\\nd\"") != null);
}

const builtin = @import("builtin");

fn shellBackend(arena: *std.heap.ArenaAllocator, script: []const u8) !Backend {
    if (builtin.os.tag == .windows) return error.SkipZigTest;
    return Backend.openWith(arena, .{
        .executable = "/bin/sh",
        .args = &.{ "-c", script },
    }, .{}) catch return error.SkipZigTest;
}

fn pumpToEnd(backend: *Backend) !void {
    var guard: usize = 0;
    while (try backend.pump()) {
        guard += 1;
        if (guard > 64) return error.PumpDidNotSettle;
    }
}

fn started(arena: *std.heap.ArenaAllocator, comptime script: []const u8) !Backend {
    var backend = try shellBackend(arena, "head -n 1 >/dev/null; " ++ script);
    try backend.submit("turn-1", "go", .{ .run_id = "run-1", .submission_id = "sub-1" });
    return backend;
}

const init_frame = "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\",\"tools\":[\"Read\"],\"mcp_servers\":[],\"model\":\"claude-test\"}";

test "a frame the child writes reaches the reducer rather than being refused" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try started(&arena, "printf '%s\\n' '" ++ init_frame ++ "'");
    defer backend.close();

    try std.testing.expect(try backend.pump());
    try std.testing.expect(!backend.reducer.unusable);
    try pumpToEnd(&backend);
    try std.testing.expect(backend.settled);
}

test "settling reaps the child, so the failure names the status it exited on" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try started(&arena, "exit 3");
    defer backend.close();

    try pumpToEnd(&backend);
    try std.testing.expect(backend.settled);
    try std.testing.expect(backend.reducer.unusable);
    try std.testing.expectEqual(process.Departure.exited, backend.transport.departed().departure);
    try std.testing.expectEqual(@as(u32, 3), backend.transport.departed().status);
}

test "a line the codec refuses settles the run and reaps the child rather than being skipped" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try started(&arena, "printf 'not json\\n'");
    defer backend.close();

    try std.testing.expect(!try backend.pump());
    try std.testing.expect(backend.settled);
    try std.testing.expect(backend.reducer.unusable);
    try std.testing.expect(backend.closed);
    try std.testing.expectEqual(process.Departure.exited, backend.transport.departed().departure);
}

test "a backend settled by a refused line reports no more work, though a frame is still buffered" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try started(&arena, "printf '%s\\n' 'not json' '" ++ init_frame ++ "'");
    defer backend.close();

    try std.testing.expect(!try backend.pump());
    try std.testing.expect(backend.settled);
    const settled_count = backend.envelopes().len;

    try std.testing.expect(!try backend.pump());
    try std.testing.expectEqual(settled_count, backend.envelopes().len);

    const buffered = try backend.transport.next();
    try std.testing.expect(buffered != null);
    try std.testing.expectEqualStrings(init_frame, buffered.?);
}

test "a settled backend stops pumping rather than reading a closed child again" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try started(&arena, "true");
    defer backend.close();

    try std.testing.expect(!try backend.pump());
    const settled_count = backend.envelopes().len;
    try std.testing.expect(!try backend.pump());
    try std.testing.expectEqual(settled_count, backend.envelopes().len);
    try std.testing.expect(backend.reducer.unusable);
}

const filler_frame = "{\"type\":\"" ++ ("Z" ** 100) ++ "\"}";

test "what the reducer keeps from a frame outlives the next frame" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try started(&arena, "printf '%s\\n' '" ++ init_frame ++ "' '" ++ filler_frame ++ "'");
    defer backend.close();

    try std.testing.expect(try backend.pump());
    try std.testing.expectEqual(@as(usize, 1), backend.reducer.run.?.buffered.items.len);
    try std.testing.expect(try backend.pump());

    const held = backend.reducer.run.?.buffered.items[0];
    try std.testing.expectEqualStrings("system", held.type);
    try std.testing.expectEqualStrings(init_frame, held.raw);
}

test "the turn the seam writes is the turn the child reads" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try shellBackend(&arena, "cat");
    defer backend.close();

    try backend.submit("turn-1", "hello", .{ .run_id = "run-1", .submission_id = "sub-1" });
    const echoed = try backend.transport.next();
    try std.testing.expect(echoed != null);
    try std.testing.expectEqualStrings(try userTurn(arena.allocator(), "turn-1", "hello"), echoed.?);
}
