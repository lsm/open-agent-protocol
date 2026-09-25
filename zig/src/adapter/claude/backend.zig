const std = @import("std");
const process = @import("process");
const session = @import("session");
const rpc = @import("rpc");

pub const Error = error{
    ExecutableRequired,
    ToolPostureRequired,
    ToolAllowlistEmpty,
    ToolNameEmpty,
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
    expand_prompts: bool = false,
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
        .allowed => |rules| {
            if (rules.len == 0) return Error.ToolAllowlistEmpty;
            var surface = std.ArrayList([]const u8).empty;
            for (rules) |rule| {
                const tool = toolOf(rule);
                if (tool.len == 0) return Error.ToolNameEmpty;
                if (!containsTool(surface.items, tool)) try surface.append(arena, tool);
            }
            try argv.append(arena, "--tools");
            try argv.append(arena, try std.mem.join(arena, ",", surface.items));
            try argv.append(arena, "--allowedTools");
            try argv.appendSlice(arena, rules);
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

fn toolOf(rule: []const u8) []const u8 {
    const end = std.mem.indexOfScalar(u8, rule, '(') orelse rule.len;
    return std.mem.trim(u8, rule[0..end], &std.ascii.whitespace);
}

fn containsTool(tools: []const []const u8, tool: []const u8) bool {
    for (tools) |listed| if (std.mem.eql(u8, listed, tool)) return true;
    return false;
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

fn goJSONString(arena: std.mem.Allocator, value: []const u8) ![]const u8 {
    const encoded = try std.json.Stringify.valueAlloc(arena, value, .{});
    var out = std.ArrayList(u8).empty;
    var index: usize = 0;
    while (index < encoded.len) {
        if (index + 3 <= encoded.len and encoded[index] == 0xE2 and encoded[index + 1] == 0x80) {
            if (encoded[index + 2] == 0xA8) {
                try out.appendSlice(arena, "\\u2028");
                index += 3;
                continue;
            }
            if (encoded[index + 2] == 0xA9) {
                try out.appendSlice(arena, "\\u2029");
                index += 3;
                continue;
            }
        }
        switch (encoded[index]) {
            '<' => try out.appendSlice(arena, "\\u003c"),
            '>' => try out.appendSlice(arena, "\\u003e"),
            '&' => try out.appendSlice(arena, "\\u0026"),
            else => try out.append(arena, encoded[index]),
        }
        index += 1;
    }
    return out.toOwnedSlice(arena);
}

pub fn userTurn(arena: std.mem.Allocator, uuid: []const u8, text: []const u8, composed: bool) ![]const u8 {
    try validateTurnUUID(uuid);
    const content = try goJSONString(arena, text);
    const turn = try goJSONString(arena, uuid);
    return std.mem.concat(arena, u8, &.{
        if (composed) "{\"client_composed\":true,\"message\":{\"content\":" else "{\"message\":{\"content\":",
        content,
        ",\"role\":\"user\"},\"origin\":{\"kind\":\"human\"},\"parent_tool_use_id\":null,\"session_id\":\"default\",\"type\":\"user\",\"uuid\":",
        turn,
        "}",
    });
}

pub fn initializeRequest(arena: std.mem.Allocator, request_id: []const u8) ![]const u8 {
    const id = try goJSONString(arena, request_id);
    return std.mem.concat(arena, u8, &.{ "{\"request\":{\"hooks\":{\"PreToolUse\":[{\"matcher\":null,\"hookCallbackIds\":[\"" ++ tool_selection_hook ++ "\"]}]},\"subtype\":\"initialize\"},\"request_id\":", id, ",\"type\":\"control_request\"}" });
}

pub const tool_selection_hook = "oap_tool_selection";
pub const pre_tool_use = "PreToolUse";

pub fn hookContinue(arena: std.mem.Allocator, request_id: []const u8) ![]const u8 {
    const id = try goJSONString(arena, request_id);
    return std.mem.concat(arena, u8, &.{ "{\"response\":{\"request_id\":", id, ",\"response\":{},\"subtype\":\"success\"},\"type\":\"control_response\"}" });
}

pub fn hookDeny(arena: std.mem.Allocator, request_id: []const u8, reason: []const u8) ![]const u8 {
    const id = try goJSONString(arena, request_id);
    const why = try goJSONString(arena, reason);
    return std.mem.concat(arena, u8, &.{
        "{\"response\":{\"request_id\":",
        id,
        ",\"response\":{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":",
        why,
        "}},\"subtype\":\"success\"},\"type\":\"control_response\"}",
    });
}

pub fn interruptRequest(arena: std.mem.Allocator, request_id: []const u8) ![]const u8 {
    const id = try goJSONString(arena, request_id);
    return std.mem.concat(arena, u8, &.{ "{\"request\":{\"subtype\":\"interrupt\"},\"request_id\":", id, ",\"type\":\"control_request\"}" });
}

pub fn permissionAllow(arena: std.mem.Allocator, request_id: []const u8, input_json: []const u8) ![]const u8 {
    const id = try goJSONString(arena, request_id);
    return std.mem.concat(arena, u8, &.{
        "{\"response\":{\"request_id\":",
        id,
        ",\"response\":{\"behavior\":\"allow\",\"updatedInput\":",
        input_json,
        "},\"subtype\":\"success\"},\"type\":\"control_response\"}",
    });
}

pub fn permissionDeny(arena: std.mem.Allocator, request_id: []const u8, message: []const u8) ![]const u8 {
    const id = try goJSONString(arena, request_id);
    const reason = try goJSONString(arena, message);
    return std.mem.concat(arena, u8, &.{
        "{\"response\":{\"request_id\":",
        id,
        ",\"response\":{\"behavior\":\"deny\",\"message\":",
        reason,
        "},\"subtype\":\"success\"},\"type\":\"control_response\"}",
    });
}

pub fn controlError(arena: std.mem.Allocator, request_id: []const u8, message: []const u8) ![]const u8 {
    const id = try goJSONString(arena, request_id);
    const reason = try goJSONString(arena, message);
    return std.mem.concat(arena, u8, &.{ "{\"response\":{\"error\":", reason, ",\"request_id\":", id, ",\"subtype\":\"error\"},\"type\":\"control_response\"}" });
}

pub const default_compact_above: usize = 256 * 1024;

pub const Received = union(enum) {
    message: rpc.Message,
    quiet,
    settled,
};

pub const Backend = struct {
    arena: *std.heap.ArenaAllocator,
    transport: *process.Transport,
    reducer: session.Reducer,
    settled: bool = false,
    closed: bool = false,
    expand_prompts: bool = false,
    compact_above: usize = default_compact_above,
    retained: usize = 0,

    pub fn open(
        arena: *std.heap.ArenaAllocator,
        config: Config,
        options: session.Options,
    ) !Backend {
        var backend = try openWith(arena, try spawnFor(arena.allocator(), config), options);
        backend.expand_prompts = config.expand_prompts;
        return backend;
    }

    pub fn openWith(
        arena: *std.heap.ArenaAllocator,
        spawn: process.Spawn,
        options: session.Options,
    ) !Backend {
        const transport = try process.Transport.open(arena.child_allocator, spawn);
        errdefer transport.deinit();
        var reducer = session.Reducer.init(arena, options);
        reducer.open();
        return .{ .arena = arena, .transport = transport, .reducer = reducer };
    }

    pub fn submit(self: *Backend, uuid: []const u8, text: []const u8, identity: session.Identity) !void {
        const owned = try self.arena.allocator().dupe(u8, uuid);
        const frame = try userTurn(self.arena.allocator(), owned, text, !self.expand_prompts);
        try self.reducer.submitAs(owned, identity);
        self.transport.write(frame) catch |err| {
            self.settled = true;
            self.reap();
            try self.reducer.transportFailed(@errorName(err));
            return err;
        };
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
        const message = try self.admit(bytes) orelse return false;
        try self.reducer.observe(message);
        return true;
    }

    pub fn receive(self: *Backend, wait_ns: u64) !Received {
        if (self.settled) return .quiet;
        const polled = self.transport.poll(wait_ns) catch |err| {
            try self.settle(err);
            return .settled;
        };
        switch (polled) {
            .quiet => return .quiet,
            .ended => {
                try self.settle(null);
                return .settled;
            },
            .frame => |bytes| {
                const message = try self.admit(bytes) orelse return .settled;
                return .{ .message = message };
            },
        }
    }

    fn admit(self: *Backend, bytes: []const u8) !?rpc.Message {
        const arena = self.arena.allocator();
        const held = try arena.dupe(u8, bytes);
        var diagnostic = rpc.Diagnostic{};
        const message = rpc.parseMessage(arena, held, &diagnostic) catch {
            self.settled = true;
            self.reap();
            try self.reducer.transportFailed(diagnostic.message);
            return null;
        };
        return message;
    }

    pub fn abandon(self: *Backend, detail: []const u8) !void {
        if (self.settled) return;
        self.settled = true;
        self.reap();
        try self.reducer.transportFailed(detail);
    }

    pub fn writeControl(self: *Backend, frame: []const u8) !void {
        if (self.settled) return error.NotRunning;
        self.transport.write(frame) catch |err| {
            try self.settle(err);
            return err;
        };
    }

    pub fn compact(self: *Backend) !bool {
        if (self.reducer.run != null or self.reducer.envelopes.items.len != 0) return false;
        if (self.arena.queryCapacity() <= self.retained +| self.compact_above) return false;
        var fresh = std.heap.ArenaAllocator.init(self.arena.child_allocator);
        errdefer fresh.deinit();
        var kept = try self.reducer.compactInto(&fresh);
        self.arena.deinit();
        self.arena.* = fresh;
        kept.arena = self.arena;
        self.reducer = kept;
        self.retained = self.arena.queryCapacity();
        return true;
    }

    fn settle(self: *Backend, err: ?anyerror) !void {
        self.settled = true;
        self.reap();
        if (err) |raised| return self.reducer.transportFailed(@errorName(raised));
        try self.reducer.transportFailed(self.transport.departed().text(self.arena.allocator()));
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

test "the argv is exactly the fixed set the pinned CLI needs, in the pinned order" {
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
    const want = [_][]const u8{ "--model", "claude-opus-5", "--tools", "Read,Grep", "--allowedTools", "Read", "Grep", "--extra" };
    try std.testing.expectEqual(want.len, tail.len);
    for (want, tail) |expected, got| try std.testing.expectEqualStrings(expected, got);
}

test "a permission rule reaches --allowedTools whole and --tools as the tool it names, once" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const spawn = try spawnFor(arena.allocator(), .{
        .executable = "/bin/claude",
        .tools = .{ .allowed = &.{ "Bash(git diff:*)", "Read", "Bash(git log:*)" } },
    });
    const tail = spawn.args[fixed_argv.len..];
    const want = [_][]const u8{ "--tools", "Bash,Read", "--allowedTools", "Bash(git diff:*)", "Read", "Bash(git log:*)" };
    try std.testing.expectEqual(want.len, tail.len);
    for (want, tail) |expected, got| try std.testing.expectEqualStrings(expected, got);

    try std.testing.expectError(Error.ToolNameEmpty, spawnFor(arena.allocator(), .{
        .executable = "/bin/claude",
        .tools = .{ .allowed = &.{ "Read", "(git *)" } },
    }));
}

test "an unrestricted posture passes no allowlist, and an empty allowlist is refused rather than meaning one" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const unrestricted = try spawnFor(arena.allocator(), .{ .executable = "/bin/claude", .tools = .unrestricted });
    for (unrestricted.args) |arg| {
        try std.testing.expect(!std.mem.eql(u8, arg, "--allowedTools"));
        try std.testing.expect(!std.mem.eql(u8, arg, "--tools"));
    }

    try std.testing.expectError(Error.ToolAllowlistEmpty, spawnFor(arena.allocator(), .{
        .executable = "/bin/claude",
        .tools = .{ .allowed = &.{} },
    }));
    try std.testing.expectError(Error.ToolNameEmpty, spawnFor(arena.allocator(), .{
        .executable = "/bin/claude",
        .tools = .{ .allowed = &.{ "Read", "" } },
    }));
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
    const composed = try userTurn(arena.allocator(), "turn-1", "fix the test", true);
    try std.testing.expectEqualStrings(
        "{\"client_composed\":true,\"message\":{\"content\":\"fix the test\",\"role\":\"user\"},\"origin\":{\"kind\":\"human\"}," ++
            "\"parent_tool_use_id\":null,\"session_id\":\"default\",\"type\":\"user\",\"uuid\":\"turn-1\"}",
        composed,
    );
    const expanding = try userTurn(arena.allocator(), "turn-1", "fix the test", false);
    try std.testing.expectEqualStrings(
        "{\"message\":{\"content\":\"fix the test\",\"role\":\"user\"},\"origin\":{\"kind\":\"human\"}," ++
            "\"parent_tool_use_id\":null,\"session_id\":\"default\",\"type\":\"user\",\"uuid\":\"turn-1\"}",
        expanding,
    );
}

test "prompt text is escaped the way encoding/json escapes it, HTML and line separators included" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const frame = try userTurn(arena.allocator(), "turn-1", "a<b&c>d\u{2028}e\u{2029}f", false);
    try std.testing.expectEqualStrings(
        "{\"message\":{\"content\":\"a\\u003cb\\u0026c\\u003ed\\u2028e\\u2029f\",\"role\":\"user\"}," ++
            "\"origin\":{\"kind\":\"human\"},\"parent_tool_use_id\":null,\"session_id\":\"default\"," ++
            "\"type\":\"user\",\"uuid\":\"turn-1\"}",
        frame,
    );
}

test "a turn refuses its uuid before it builds a frame" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    try std.testing.expectError(Error.InvalidTurnUUID, userTurn(arena.allocator(), "turn_1", "hi", true));
}

test "prompt text is escaped by the encoder rather than concatenated" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const frame = try userTurn(arena.allocator(), "turn-1", "a\"b\\c\nd", true);
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

fn boundedBackend(arena: *std.heap.ArenaAllocator, comptime script: []const u8, limit: usize) !Backend {
    if (builtin.os.tag == .windows) return error.SkipZigTest;
    return Backend.openWith(arena, .{
        .executable = "/bin/sh",
        .args = &.{ "-c", "head -n 1 >/dev/null; " ++ script },
        .frame_limit = limit,
    }, .{}) catch return error.SkipZigTest;
}

fn failureMessage(backend: *Backend) ?[]const u8 {
    for (backend.envelopes()) |envelope| {
        if (envelope != .object) continue;
        const kind = envelope.object.get("type") orelse continue;
        if (kind != .string or !std.mem.eql(u8, kind.string, "run.failed")) continue;
        const payload = envelope.object.get("payload") orelse continue;
        if (payload != .object) continue;
        const failure = payload.object.get("error") orelse continue;
        if (failure != .object) continue;
        const message = failure.object.get("message") orelse continue;
        if (message != .string) continue;
        return message.string;
    }
    return null;
}

const echo_frame = "{\"type\":\"system\",\"user_message_uuid\":\"turn-1\"}";

test "a reader error names itself, not the status the child happened to exit on" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try boundedBackend(&arena, "printf '%s\\n' '" ++ echo_frame ++ "' '" ++ ("A" ** 400) ++ "'", 200);
    defer backend.close();
    try backend.submit("turn-1", "go", .{ .run_id = "run-1", .submission_id = "sub-1" });

    try std.testing.expect(try backend.pump());
    try std.testing.expect(!try backend.pump());
    try std.testing.expectEqual(process.Departure.exited, backend.transport.departed().departure);
    try std.testing.expectEqual(@as(u32, 0), backend.transport.departed().status);
    try std.testing.expectEqualStrings("FrameTooLarge", failureMessage(&backend) orelse return error.NoFailureEmitted);
}

test "a turn the transport refuses to write settles the run rather than leaving it open" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try boundedBackend(&arena, "cat >/dev/null", 64);
    defer backend.close();

    try std.testing.expectError(
        error.FrameTooLarge,
        backend.submit("turn-1", "go", .{ .run_id = "run-1", .submission_id = "sub-1" }),
    );
    try std.testing.expect(backend.reducer.unusable);
    try std.testing.expect(backend.reducer.run == null);
    try std.testing.expect(backend.closed);
    try std.testing.expect(!try backend.pump());
}

test "the turn the seam writes is the turn the child reads" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try shellBackend(&arena, "cat");
    defer backend.close();

    try backend.submit("turn-1", "hello", .{ .run_id = "run-1", .submission_id = "sub-1" });
    const echoed = try backend.transport.next();
    try std.testing.expect(echoed != null);
    try std.testing.expectEqualStrings(try userTurn(arena.allocator(), "turn-1", "hello", true), echoed.?);
}

test "a backend whose prompts expand writes the turn without client_composed" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try shellBackend(&arena, "cat");
    defer backend.close();
    backend.expand_prompts = true;

    try backend.submit("turn-1", "hello", .{ .run_id = "run-1", .submission_id = "sub-1" });
    const echoed = try backend.transport.next();
    try std.testing.expect(echoed != null);
    try std.testing.expectEqualStrings(try userTurn(arena.allocator(), "turn-1", "hello", false), echoed.?);
}

test "the control frames are the ones the pinned marshal produces, keys sorted" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    try std.testing.expectEqualStrings(
        "{\"request\":{\"hooks\":{\"PreToolUse\":[{\"matcher\":null,\"hookCallbackIds\":[\"oap_tool_selection\"]}]},\"subtype\":\"initialize\"},\"request_id\":\"req_1\",\"type\":\"control_request\"}",
        try initializeRequest(scratch, "req_1"),
    );
    try std.testing.expectEqualStrings(
        "{\"request\":{\"subtype\":\"interrupt\"},\"request_id\":\"req_2\",\"type\":\"control_request\"}",
        try interruptRequest(scratch, "req_2"),
    );
    try std.testing.expectEqualStrings(
        "{\"response\":{\"request_id\":\"ask-1\",\"response\":{\"behavior\":\"allow\",\"updatedInput\":{\"command\":\"ls\"}},\"subtype\":\"success\"},\"type\":\"control_response\"}",
        try permissionAllow(scratch, "ask-1", "{\"command\":\"ls\"}"),
    );
    try std.testing.expectEqualStrings(
        "{\"response\":{\"request_id\":\"ask-2\",\"response\":{\"behavior\":\"deny\",\"message\":\"Denied by the operator\"},\"subtype\":\"success\"},\"type\":\"control_response\"}",
        try permissionDeny(scratch, "ask-2", "Denied by the operator"),
    );
    try std.testing.expectEqualStrings(
        "{\"response\":{\"error\":\"no \\u003cgate\\u003e\",\"request_id\":\"ask-3\",\"subtype\":\"error\"},\"type\":\"control_response\"}",
        try controlError(scratch, "ask-3", "no <gate>"),
    );
}

fn receiveMessage(backend: *Backend) !?rpc.Message {
    var attempts: usize = 0;
    while (attempts < 400) : (attempts += 1) {
        switch (try backend.receive(25 * std.time.ns_per_ms)) {
            .message => |message| return message,
            .quiet => continue,
            .settled => return null,
        }
    }
    return error.ReceiveNeverSettled;
}

test "a received frame is handed over parsed and not yet reduced" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try shellBackend(&arena, "printf '%s\\n' '" ++ init_frame ++ "'");
    defer backend.close();

    const message = (try receiveMessage(&backend)).?;
    try std.testing.expectEqualStrings("system", message.type);
    try std.testing.expectEqualStrings("init", message.subtype);
    try std.testing.expect(!backend.reducer.catalog_known);
    try backend.reducer.observe(message);
    try std.testing.expect(backend.reducer.catalog_known);
}

test "a receive past the child's exit settles once and then stays quiet" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try started(&arena, "printf '%s\\n' '" ++ echo_frame ++ "'; exit 4");
    defer backend.close();

    try backend.reducer.observe((try receiveMessage(&backend)).?);
    try std.testing.expect(try receiveMessage(&backend) == null);
    try std.testing.expect(backend.settled);
    try std.testing.expect(backend.reducer.unusable);
    try std.testing.expectEqualStrings("child exited with status 4", failureMessage(&backend) orelse return error.NoFailureEmitted);
    try std.testing.expect(try backend.receive(std.time.ns_per_ms) == .quiet);
}

test "a frame the codec refuses on receive settles the run rather than surfacing" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try started(&arena, "printf '%s\\n' '" ++ echo_frame ++ "' 'not json'");
    defer backend.close();

    try backend.reducer.observe((try receiveMessage(&backend)).?);
    try std.testing.expect(try receiveMessage(&backend) == null);
    try std.testing.expect(backend.reducer.unusable);
    try std.testing.expect(failureMessage(&backend) != null);
}

test "a control frame reaches the child whole, and one written after the child is gone is refused" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try shellBackend(&arena, "cat");
    defer backend.close();

    const frame = try initializeRequest(arena.allocator(), "req_1");
    try backend.writeControl(frame);
    const echoed = (try backend.transport.next()).?;
    try std.testing.expectEqualStrings(frame, echoed);

    backend.settled = true;
    try std.testing.expectError(error.NotRunning, backend.writeControl(frame));
}

const turn_frames = "printf '%s\\n' " ++
    "'{\"type\":\"stream_event\",\"session_id\":\"s\",\"event\":{\"type\":\"message_start\"},\"uuid\":\"e1\",\"user_message_uuid\":\"turn-1\"}' " ++
    "'{\"type\":\"stream_event\",\"session_id\":\"s\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"" ++ ("w" ** 2048) ++ "\"}},\"uuid\":\"e2\"}' " ++
    "'{\"type\":\"result\",\"session_id\":\"s\",\"subtype\":\"success\",\"result\":\"one\",\"user_message_uuid\":\"turn-1\",\"uuid\":\"r1\"}'; " ++
    "read -r second; printf '%s\\n' " ++
    "'{\"type\":\"result\",\"session_id\":\"s\",\"subtype\":\"success\",\"result\":\"two\",\"user_message_uuid\":\"turn-2\",\"uuid\":\"r2\"}'";

fn reduceUntilIdle(backend: *Backend) !void {
    while (backend.reducer.run != null) {
        const message = (try receiveMessage(backend)) orelse return error.ChildLeftEarly;
        try backend.reducer.observe(message);
    }
}

test "the turn uuid a submission names is held by the session, not borrowed from the caller" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try shellBackend(&arena, "head -n 1 >/dev/null; printf '%s\\n' '" ++ echo_frame ++ "'");
    defer backend.close();

    const lent = try std.testing.allocator.dupe(u8, "turn-1");
    try backend.submit(lent, "go", .{});
    @memset(lent, 'x');
    std.testing.allocator.free(lent);

    try backend.reducer.observe((try receiveMessage(&backend)).?);
    try std.testing.expect(backend.reducer.run.?.started);
    try std.testing.expectEqualStrings("turn-1", backend.reducer.started.?.submission_uuid);
}

test "compaction releases a settled run's frames once the session is idle and keeps the session answering" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var backend = try started(&arena, turn_frames);
    defer backend.close();
    backend.compact_above = 0;

    try std.testing.expect(!try backend.compact());
    try reduceUntilIdle(&backend);
    try std.testing.expect(!try backend.compact());

    const grown = backend.arena.queryCapacity();
    backend.reducer.envelopes.clearRetainingCapacity();
    try std.testing.expect(try backend.compact());
    try std.testing.expect(backend.arena.queryCapacity() < grown);
    try std.testing.expect(backend.reducer.arena == backend.arena);

    try backend.submit("turn-2", "again", .{});
    try reduceUntilIdle(&backend);
    const kinds = backend.reducer.envelopes.items;
    try std.testing.expectEqualStrings("run.completed", kinds[kinds.len - 1].object.get("type").?.string);
    try std.testing.expectEqualStrings("two", kinds[kinds.len - 1].object.get("payload").?.object.get("final_response").?.object.get("content").?.string);
}
