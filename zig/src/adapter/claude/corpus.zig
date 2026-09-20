const std = @import("std");
const rpc = @import("rpc");
const session = @import("session");
const build_options = @import("build_options");

const corpus_relative = "fixtures/adapters/claude-code-2.1.263";

const Case = struct {
    id: []const u8,
    path: []const u8,
};

fn readFile(allocator: std.mem.Allocator, path: []const u8) ![]u8 {
    return std.Io.Dir.cwd().readFileAlloc(std.testing.io, path, allocator, .limited(16 * 1024 * 1024));
}

fn corpusRoot(allocator: std.mem.Allocator) ![]const u8 {
    return std.fs.path.join(allocator, &.{ build_options.repository_root, corpus_relative });
}

fn stringMember(map: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = map.get(key) orelse return null;
    return if (value == .string) value.string else null;
}

fn equalValues(a: std.json.Value, b: std.json.Value) bool {
    if (@intFromEnum(a) != @intFromEnum(b)) {
        if (a == .integer and b == .float) return @as(f64, @floatFromInt(a.integer)) == b.float;
        if (a == .float and b == .integer) return a.float == @as(f64, @floatFromInt(b.integer));
        return false;
    }
    return switch (a) {
        .null => true,
        .bool => a.bool == b.bool,
        .integer => a.integer == b.integer,
        .float => a.float == b.float,
        .number_string => std.mem.eql(u8, a.number_string, b.number_string),
        .string => std.mem.eql(u8, a.string, b.string),
        .array => blk: {
            if (a.array.items.len != b.array.items.len) break :blk false;
            for (a.array.items, b.array.items) |left, right| {
                if (!equalValues(left, right)) break :blk false;
            }
            break :blk true;
        },
        .object => blk: {
            if (a.object.count() != b.object.count()) break :blk false;
            var it = a.object.iterator();
            while (it.next()) |entry| {
                const other = b.object.get(entry.key_ptr.*) orelse break :blk false;
                if (!equalValues(entry.value_ptr.*, other)) break :blk false;
            }
            break :blk true;
        },
    };
}

fn describe(allocator: std.mem.Allocator, value: std.json.Value) ![]const u8 {
    return std.json.Stringify.valueAlloc(allocator, value, .{ .whitespace = .indent_2 });
}

const Outcome = struct {
    emitted: usize,
    expected: usize,
    first_mismatch: ?usize,
};

fn runCase(allocator: std.mem.Allocator, root: []const u8, case: Case) !Outcome {
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    const dir = try std.fs.path.join(scratch, &.{ root, case.path });
    const native_path = try std.fs.path.join(scratch, &.{ dir, "native.jsonl" });
    const expected_path = try std.fs.path.join(scratch, &.{ dir, "expected-oap.json" });

    const native = try readFile(scratch, native_path);
    const expected_text = try readFile(scratch, expected_path);

    var reducer = session.Reducer.init(&arena, .{});
    reducer.open();

    var reader = rpc.FrameReader{ .source = native };
    while (try reader.next()) |line| {
        const script = try std.json.parseFromSliceLeaky(std.json.Value, scratch, line, .{});
        if (script != .object) return error.InvalidScriptLine;
        const action = stringMember(script.object, "action") orelse return error.InvalidScriptLine;
        const raw = script.object.get("raw") orelse return error.InvalidScriptLine;
        const encoded = try std.json.Stringify.valueAlloc(scratch, raw, .{});

        if (std.mem.eql(u8, action, "submit")) {
            const uuid = if (raw == .object) stringMember(raw.object, "uuid") orelse "" else "";
            try reducer.submit(uuid);
            continue;
        }
        if (std.mem.eql(u8, action, "observe")) {
            const message = try rpc.parseMessage(scratch, encoded);
            try reducer.observe(message);
            continue;
        }
        if (std.mem.eql(u8, action, "oap-control")) {
            if (raw != .object) return error.InvalidScriptLine;
            const op = stringMember(raw.object, "op") orelse return error.InvalidScriptLine;
            if (!std.mem.eql(u8, op, "resolve")) continue;
            const decision = stringMember(raw.object, "decision") orelse return error.InvalidScriptLine;
            const pending = reducer.pendingInteraction() orelse return error.NoPendingInteraction;
            if (std.mem.eql(u8, decision, "allow")) {
                try reducer.resolve(pending, .allow);
            } else if (std.mem.eql(u8, decision, "deny")) {
                try reducer.resolve(pending, .deny);
            } else return error.InvalidScriptLine;
            continue;
        }
    }

    const expected = try std.json.parseFromSliceLeaky(std.json.Value, scratch, expected_text, .{});
    if (expected != .array) return error.InvalidExpectation;

    var first_mismatch: ?usize = null;
    const shared = @min(expected.array.items.len, reducer.envelopes.items.len);
    for (0..shared) |index| {
        if (!equalValues(expected.array.items[index], reducer.envelopes.items[index])) {
            first_mismatch = index;
            const want = try describe(scratch, expected.array.items[index]);
            const got = try describe(scratch, reducer.envelopes.items[index]);
            std.debug.print("\n{s} envelope {d} mismatch\nwant: {s}\ngot:  {s}\n", .{ case.id, index, want, got });
            break;
        }
    }
    return .{
        .emitted = reducer.envelopes.items.len,
        .expected = expected.array.items.len,
        .first_mismatch = first_mismatch,
    };
}

const passing_cases = [_]Case{
    .{ .id = "initialize-lifecycle", .path = "initialize-lifecycle" },
    .{ .id = "tool-lifecycle", .path = "tool-lifecycle" },
    .{ .id = "permission-gates", .path = "permission-gates" },
    .{ .id = "interrupt-cancel", .path = "interrupt-cancel" },
    .{ .id = "settlement-statuses", .path = "settlement-statuses" },
    .{ .id = "streaming-provenance", .path = "streaming-provenance" },
    .{ .id = "admission-corroboration", .path = "admission-corroboration" },
};

test "the Zig reducer reproduces every expectation it claims" {
    const allocator = std.testing.allocator;
    const root = try corpusRoot(allocator);
    defer allocator.free(root);

    for (passing_cases) |case| {
        const outcome = try runCase(allocator, root, case);
        try std.testing.expectEqual(@as(?usize, null), outcome.first_mismatch);
        try std.testing.expectEqual(outcome.expected, outcome.emitted);
    }
}
