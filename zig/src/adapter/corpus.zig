const std = @import("std");
const build_options = @import("build_options");

pub const BlankExpectation = enum {
    zero_byte_or_empty_array,
    zero_byte_only,
    empty_array_only,
};

pub const Step = struct {
    action: []const u8,
    raw: std.json.Value,
    encoded: []const u8,
};

pub const Handled = enum { handled, unhandled };

pub const Replay = struct {
    expected: []std.json.Value,
    emitted: []std.json.Value,
};

pub const Outcome = struct {
    expected: usize,
    emitted: usize,
    first_mismatch: ?usize,
};

pub fn expectedEnvelopes(scratch: std.mem.Allocator, text: []const u8) ![]std.json.Value {
    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, text, .{});
    return switch (parsed) {
        .array => parsed.array.items,
        .null => &.{},
        else => error.InvalidExpectation,
    };
}

pub fn isBlank(policy: BlankExpectation, text: []const u8) bool {
    const trimmed = std.mem.trim(u8, text, " \t\r\n");
    return switch (policy) {
        .zero_byte_or_empty_array => trimmed.len == 0 or std.mem.eql(u8, trimmed, "[]"),
        .zero_byte_only => trimmed.len == 0,
        .empty_array_only => std.mem.eql(u8, trimmed, "[]"),
    };
}

pub fn equalValues(a: std.json.Value, b: std.json.Value) bool {
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

pub fn equalExcept(a: std.json.Value, b: std.json.Value, path: []const []const u8) bool {
    if (path.len == 0) return true;
    if (a != .object or b != .object) return false;
    if (a.object.count() != b.object.count()) return false;
    var it = a.object.iterator();
    while (it.next()) |entry| {
        const other = b.object.get(entry.key_ptr.*) orelse return false;
        if (std.mem.eql(u8, entry.key_ptr.*, path[0])) {
            if (!equalExcept(entry.value_ptr.*, other, path[1..])) return false;
        } else if (!equalValues(entry.value_ptr.*, other)) return false;
    }
    return true;
}

pub fn stringMember(map: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = map.get(key) orelse return null;
    return if (value == .string) value.string else null;
}

pub fn corpusRoot(allocator: std.mem.Allocator, relative: []const u8) ![]const u8 {
    return std.fs.path.join(allocator, &.{ build_options.repository_root, relative });
}

fn readFile(allocator: std.mem.Allocator, path: []const u8) ![]u8 {
    return std.Io.Dir.cwd().readFileAlloc(std.testing.io, path, allocator, .limited(16 * 1024 * 1024));
}

fn describe(allocator: std.mem.Allocator, value: std.json.Value) ![]const u8 {
    return std.json.Stringify.valueAlloc(allocator, value, .{ .whitespace = .indent_2 });
}

pub fn Harness(comptime Driver: type) type {
    return struct {
        const Self = @This();

        pub fn root(allocator: std.mem.Allocator) ![]const u8 {
            return corpusRoot(allocator, Driver.corpus_relative);
        }

        pub fn steps(scratch: std.mem.Allocator, native: []const u8, reducer: *Driver.Reducer, case: Driver.Case) !void {
            var lines = std.mem.splitScalar(u8, native, '\n');
            while (lines.next()) |line| {
                if (line.len == 0) continue;
                if (std.mem.indexOfScalar(u8, line, '\r') != null) return error.InvalidScriptLine;
                const script = try std.json.parseFromSliceLeaky(std.json.Value, scratch, line, .{});
                if (script != .object) return error.InvalidScriptLine;
                const action = stringMember(script.object, "action") orelse return error.InvalidScriptLine;
                const raw = script.object.get("raw") orelse return error.InvalidScriptLine;
                const step = Step{
                    .action = action,
                    .raw = raw,
                    .encoded = try std.json.Stringify.valueAlloc(scratch, raw, .{}),
                };
                if (try Driver.step(reducer, scratch, step, case) == .unhandled) {
                    return error.UnhandledScriptAction;
                }
            }
        }

        pub fn replay(arena: *std.heap.ArenaAllocator, corpus: []const u8, case: Driver.Case) !Replay {
            const scratch = arena.allocator();
            const dir = try std.fs.path.join(scratch, &.{ corpus, case.path });
            const native = try readFile(scratch, try std.fs.path.join(scratch, &.{ dir, "native.jsonl" }));
            const expected_text = try readFile(scratch, try std.fs.path.join(scratch, &.{ dir, "expected-oap.json" }));
            if (isBlank(Driver.blank_expectation, expected_text)) return error.BlankExpectation;

            var reducer = Driver.open(arena, case);
            try steps(scratch, native, &reducer, case);

            return .{ .expected = try expectedEnvelopes(scratch, expected_text), .emitted = Driver.envelopes(&reducer) };
        }

        pub fn runCase(allocator: std.mem.Allocator, corpus: []const u8, case: Driver.Case) !Outcome {
            var arena = std.heap.ArenaAllocator.init(allocator);
            defer arena.deinit();
            const played = try replay(&arena, corpus, case);

            var first_mismatch: ?usize = null;
            const shared = @min(played.expected.len, played.emitted.len);
            for (0..shared) |index| {
                if (equalValues(played.expected[index], played.emitted[index])) continue;
                first_mismatch = index;
                const want = try describe(arena.allocator(), played.expected[index]);
                const got = try describe(arena.allocator(), played.emitted[index]);
                std.debug.print("\n{s} envelope {d} mismatch\nwant: {s}\ngot:  {s}\n", .{ case.id, index, want, got });
                break;
            }
            return .{
                .expected = played.expected.len,
                .emitted = played.emitted.len,
                .first_mismatch = first_mismatch,
            };
        }

        pub fn expectEveryCase(allocator: std.mem.Allocator, cases: []const Driver.Case) !void {
            const corpus = try Self.root(allocator);
            defer allocator.free(corpus);

            var failures: usize = 0;
            for (cases) |case| {
                const outcome = try runCase(allocator, corpus, case);
                if (outcome.first_mismatch == null and outcome.expected == outcome.emitted) continue;
                failures += 1;
                std.debug.print("\n{s}: expected {d} envelopes, emitted {d}\n", .{ case.id, outcome.expected, outcome.emitted });
            }
            try std.testing.expectEqual(@as(usize, 0), failures);
        }
    };
}

const testing = std.testing;

test "a null expectation is a trace with no envelopes, never a blank one" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    for ([_]BlankExpectation{ .zero_byte_or_empty_array, .zero_byte_only, .empty_array_only }) |policy| {
        try testing.expect(!isBlank(policy, "null"));
        try testing.expect(!isBlank(policy, "null\n"));
    }

    try testing.expectEqual(@as(usize, 0), (try expectedEnvelopes(scratch, "null")).len);
    try testing.expectEqual(@as(usize, 0), (try expectedEnvelopes(scratch, "[]")).len);
    try testing.expectEqual(@as(usize, 1), (try expectedEnvelopes(scratch, "[{}]")).len);
    try testing.expectError(error.InvalidExpectation, expectedEnvelopes(scratch, "{}"));
    try testing.expectError(error.InvalidExpectation, expectedEnvelopes(scratch, "7"));
}

test "what counts as a blank expectation differs per adapter" {
    try testing.expect(isBlank(.zero_byte_or_empty_array, ""));
    try testing.expect(isBlank(.zero_byte_or_empty_array, "[]"));
    try testing.expect(!isBlank(.zero_byte_or_empty_array, "[{}]"));

    try testing.expect(isBlank(.zero_byte_only, ""));
    try testing.expect(!isBlank(.zero_byte_only, "[]"));

    try testing.expect(isBlank(.empty_array_only, "[]"));
    try testing.expect(!isBlank(.empty_array_only, ""));
}

fn parse(arena: std.mem.Allocator, text: []const u8) !std.json.Value {
    return std.json.parseFromSliceLeaky(std.json.Value, arena, text, .{});
}

test "comparison is structural, not textual" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    try testing.expect(equalValues(try parse(scratch, "{\"a\":1,\"b\":[2,3]}"), try parse(scratch, "{\"b\":[2,3],\"a\":1}")));
    try testing.expect(equalValues(try parse(scratch, "{\"a\":1}"), try parse(scratch, "{\"a\":1.0}")));
    try testing.expect(!equalValues(try parse(scratch, "{\"a\":1}"), try parse(scratch, "{\"a\":1,\"b\":2}")));
    try testing.expect(!equalValues(try parse(scratch, "{\"a\":1,\"b\":2}"), try parse(scratch, "{\"a\":1}")));
    try testing.expect(!equalValues(try parse(scratch, "[1,2]"), try parse(scratch, "[2,1]")));
    try testing.expect(!equalValues(try parse(scratch, "[1]"), try parse(scratch, "[1,2]")));
    try testing.expect(!equalValues(try parse(scratch, "[1,2]"), try parse(scratch, "[1]")));
}

test "one exempt member does not exempt its siblings" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const path = [_][]const u8{ "payload", "error", "message" };

    const base = try parse(scratch, "{\"id\":\"e1\",\"payload\":{\"run_id\":\"r\",\"error\":{\"code\":\"c\",\"message\":\"left\"}}}");
    const differs_at_leaf = try parse(scratch, "{\"id\":\"e1\",\"payload\":{\"run_id\":\"r\",\"error\":{\"code\":\"c\",\"message\":\"right\"}}}");
    const differs_at_sibling = try parse(scratch, "{\"id\":\"e1\",\"payload\":{\"run_id\":\"r\",\"error\":{\"code\":\"d\",\"message\":\"left\"}}}");
    const differs_above = try parse(scratch, "{\"id\":\"e2\",\"payload\":{\"run_id\":\"r\",\"error\":{\"code\":\"c\",\"message\":\"left\"}}}");
    const missing_sibling = try parse(scratch, "{\"id\":\"e1\",\"payload\":{\"error\":{\"code\":\"c\",\"message\":\"left\"}}}");

    try testing.expect(equalExcept(base, differs_at_leaf, &path));
    try testing.expect(!equalExcept(base, differs_at_sibling, &path));
    try testing.expect(!equalExcept(base, differs_above, &path));
    try testing.expect(!equalExcept(base, missing_sibling, &path));
}

const StubDriver = struct {
    pub const corpus_relative = "";
    pub const blank_expectation = BlankExpectation.zero_byte_only;

    pub const Case = struct {
        id: []const u8,
        path: []const u8,
    };

    pub const Reducer = struct {
        arena: *std.heap.ArenaAllocator,
        seen: std.ArrayList(std.json.Value) = .empty,
    };

    pub fn open(arena: *std.heap.ArenaAllocator, case: Case) Reducer {
        _ = case;
        return .{ .arena = arena };
    }

    pub fn envelopes(reducer: *Reducer) []std.json.Value {
        return reducer.seen.items;
    }

    pub fn step(reducer: *Reducer, scratch: std.mem.Allocator, s: Step, case: Case) !Handled {
        _ = scratch;
        _ = case;
        if (!std.mem.eql(u8, s.action, "emit")) return .unhandled;
        try reducer.seen.append(reducer.arena.allocator(), s.raw);
        return .handled;
    }
};

const StubHarness = Harness(StubDriver);

fn stubCorpus(tmp: *std.testing.TmpDir, allocator: std.mem.Allocator) ![]const u8 {
    const cwd = try std.process.currentPathAlloc(std.testing.io, allocator);
    defer allocator.free(cwd);
    return std.Io.Dir.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..] });
}

fn writeCase(tmp: *std.testing.TmpDir, native: []const u8, expected: []const u8) !void {
    try tmp.dir.createDirPath(std.testing.io, "case");
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "case/native.jsonl", .data = native });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "case/expected-oap.json", .data = expected });
}

test "a blank expectation is refused rather than compared against nothing" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const root = try stubCorpus(&tmp, allocator);
    defer allocator.free(root);
    const case = StubDriver.Case{ .id = "case", .path = "case" };

    try writeCase(&tmp, "", "");
    var blank = std.heap.ArenaAllocator.init(allocator);
    defer blank.deinit();
    try testing.expectError(error.BlankExpectation, StubHarness.replay(&blank, root, case));

    try writeCase(&tmp, "", "[]");
    var empty = std.heap.ArenaAllocator.init(allocator);
    defer empty.deinit();
    const played = try StubHarness.replay(&empty, root, case);
    try testing.expectEqual(@as(usize, 0), played.expected.len);
    try testing.expectEqual(@as(usize, 0), played.emitted.len);

    try writeCase(&tmp,
        \\{"action":"emit","raw":{"n":1}}
    , "null");
    var emitted_anyway = std.heap.ArenaAllocator.init(allocator);
    defer emitted_anyway.deinit();
    const refused = try StubHarness.runCase(allocator, root, case);
    try testing.expectEqual(@as(usize, 0), refused.expected);
    try testing.expectEqual(@as(usize, 1), refused.emitted);

    try writeCase(&tmp, "", "null");
    var silent = std.heap.ArenaAllocator.init(allocator);
    defer silent.deinit();
    const agreed = try StubHarness.replay(&silent, root, case);
    try testing.expectEqual(@as(usize, 0), agreed.expected.len);
    try testing.expectEqual(@as(usize, 0), agreed.emitted.len);
}

test "the harness drives a script through the driver in order" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const root = try stubCorpus(&tmp, allocator);
    defer allocator.free(root);
    const case = StubDriver.Case{ .id = "case", .path = "case" };

    try writeCase(&tmp,
        \\{"action":"emit","raw":{"n":1}}
        \\{"action":"emit","raw":{"n":2}}
    ,
        \\[{"n":1},{"n":2}]
    );
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    const outcome = try StubHarness.runCase(allocator, root, case);
    try testing.expectEqual(@as(?usize, null), outcome.first_mismatch);
    try testing.expectEqual(@as(usize, 2), outcome.emitted);

    try writeCase(&tmp,
        \\{"action":"emit","raw":{"n":1}}
        \\{"action":"nothing-drives-this","raw":{}}
    ,
        \\[{"n":1}]
    );
    var refused = std.heap.ArenaAllocator.init(allocator);
    defer refused.deinit();
    try testing.expectError(error.UnhandledScriptAction, StubHarness.replay(&refused, root, case));
}
