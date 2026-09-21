const std = @import("std");
const testing = std.testing;

pub fn hexEscapeAt(data: []const u8, index: usize) ?u21 {
    if (index + 6 > data.len) return null;
    if (data[index] != '\\' or data[index + 1] != 'u') return null;
    var code: u21 = 0;
    for (data[index + 2 .. index + 6]) |digit| {
        const nibble: u21 = switch (digit) {
            '0'...'9' => digit - '0',
            'a'...'f' => digit - 'a' + 10,
            'A'...'F' => digit - 'A' + 10,
            else => return null,
        };
        code = code * 16 + nibble;
    }
    return code;
}

pub fn isSurrogate(code: u21) bool {
    return code >= 0xd800 and code <= 0xdfff;
}

pub fn carriesSurrogateEscape(data: []const u8) bool {
    var index: usize = 0;
    while (index + 6 <= data.len) : (index += 1) {
        const code = hexEscapeAt(data, index) orelse continue;
        if (isSurrogate(code)) return true;
    }
    return false;
}

pub fn replaceLoneSurrogates(arena: std.mem.Allocator, data: []const u8) []const u8 {
    if (!carriesSurrogateEscape(data)) return data;
    var out = std.ArrayList(u8).empty;
    out.ensureTotalCapacity(arena, data.len) catch return data;
    var index: usize = 0;
    var in_string = false;
    while (index < data.len) {
        const byte = data[index];
        if (!in_string) {
            if (byte == '"') in_string = true;
            out.append(arena, byte) catch return data;
            index += 1;
            continue;
        }
        if (byte == '\\') {
            if (hexEscapeAt(data, index)) |code| {
                if (code >= 0xd800 and code <= 0xdbff) {
                    if (hexEscapeAt(data, index + 6)) |trailing| {
                        if (trailing >= 0xdc00 and trailing <= 0xdfff) {
                            out.appendSlice(arena, data[index .. index + 12]) catch return data;
                            index += 12;
                            continue;
                        }
                    }
                }
                if (isSurrogate(code)) {
                    out.appendSlice(arena, "\\ufffd") catch return data;
                    index += 6;
                    continue;
                }
                out.appendSlice(arena, data[index .. index + 6]) catch return data;
                index += 6;
                continue;
            }
            if (index + 2 > data.len) {
                out.append(arena, byte) catch return data;
                index += 1;
                continue;
            }
            out.appendSlice(arena, data[index .. index + 2]) catch return data;
            index += 2;
            continue;
        }
        if (byte == '"') in_string = false;
        out.append(arena, byte) catch return data;
        index += 1;
    }
    return out.items;
}

test "an escape is four hex digits and nothing JSON does not spell" {
    try testing.expectEqual(@as(?u21, 0xd800), hexEscapeAt("\\ud800", 0));
    try testing.expectEqual(@as(?u21, 0xd800), hexEscapeAt("\\uD800", 0));
    try testing.expectEqual(@as(?u21, 0x0041), hexEscapeAt("\\u0041", 0));

    try testing.expectEqual(@as(?u21, null), hexEscapeAt("\\U0041", 0));
    try testing.expectEqual(@as(?u21, null), hexEscapeAt("\\u+d80", 0));
    try testing.expectEqual(@as(?u21, null), hexEscapeAt("\\ud_80", 0));
    try testing.expectEqual(@as(?u21, null), hexEscapeAt("\\u 800", 0));
    try testing.expectEqual(@as(?u21, null), hexEscapeAt("\\ud80", 0));
    try testing.expectEqual(@as(?u21, null), hexEscapeAt("x\\ud800", 0));
}

pub fn finiteNumbers(value: std.json.Value) bool {
    return switch (value) {
        .float => |number| std.math.isFinite(number),
        .number_string => |text| blk: {
            const parsed = std.fmt.parseFloat(f64, text) catch break :blk false;
            break :blk std.math.isFinite(parsed);
        },
        .array => |items| blk: {
            for (items.items) |item| {
                if (!finiteNumbers(item)) break :blk false;
            }
            break :blk true;
        },
        .object => |members| blk: {
            var entries = members.iterator();
            while (entries.next()) |entry| {
                if (!finiteNumbers(entry.value_ptr.*)) break :blk false;
            }
            break :blk true;
        },
        else => true,
    };
}

test "a number the oracle cannot hold in a float64 is refused wherever it sits" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    for ([_][]const u8{ "1e400", "-1e400", "[1e400]", "{\"a\":{\"b\":[1e400]}}" }) |text| {
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, text, .{});
        try testing.expect(!finiteNumbers(parsed));
    }
    for ([_][]const u8{ "1e308", "-1e308", "9223372036854775808", "0", "[1,2.5]", "{\"a\":1e308}" }) |text| {
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, text, .{});
        try testing.expect(finiteNumbers(parsed));
    }
}

fn expectRewritten(want: []const u8, source: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    try testing.expectEqualStrings(want, replaceLoneSurrogates(arena.allocator(), source));
}

test "a lone surrogate escape is rewritten and a valid pair is left alone" {
    try expectRewritten("{\"a\":\"\\ufffd\"}", "{\"a\":\"\\ud800\"}");
    try expectRewritten("{\"a\":\"\\ud83d\\ude00\"}", "{\"a\":\"\\ud83d\\ude00\"}");
    try expectRewritten("{\"a\":\"\\ufffd\\ufffd\"}", "{\"a\":\"\\ud800\\ud800\"}");
    try expectRewritten("{\"a\":\"\\ufffd\\ud83d\\ude00\"}", "{\"a\":\"\\ud83d\\ud83d\\ude00\"}");
    try expectRewritten("{\"a\":\"plain\"}", "{\"a\":\"plain\"}");
    try expectRewritten("{\"\\ufffd\":1}", "{\"\\ud800\":1}");
}

fn foldNext(text: []const u8, index: *usize) ?u21 {
    if (index.* >= text.len) return null;
    const byte = text[index.*];
    if (byte < 0x80) {
        index.* += 1;
        return std.ascii.toLower(byte);
    }
    const width = std.unicode.utf8ByteSequenceLength(byte) catch {
        index.* += 1;
        return byte;
    };
    if (index.* + width > text.len) {
        index.* += 1;
        return byte;
    }
    const code = std.unicode.utf8Decode(text[index.* .. index.* + width]) catch {
        index.* += 1;
        return byte;
    };
    index.* += width;
    return switch (code) {
        0x17f => 's',
        0x212a => 'k',
        else => code,
    };
}

pub fn foldEql(left: []const u8, right: []const u8) bool {
    var at_left: usize = 0;
    var at_right: usize = 0;
    while (true) {
        const a = foldNext(left, &at_left);
        const b = foldNext(right, &at_right);
        if (a == null and b == null) return true;
        if (a == null or b == null) return false;
        if (a.? != b.?) return false;
    }
}

pub fn foldedSet(object: std.json.ObjectMap, path: []const []const u8) ?std.json.Value {
    var found: ?std.json.Value = null;
    var entries = object.iterator();
    while (entries.next()) |entry| {
        if (!foldEql(entry.key_ptr.*, path[0])) continue;
        if (path.len == 1) {
            if (entry.value_ptr.* == .null) continue;
            found = entry.value_ptr.*;
            continue;
        }
        if (entry.value_ptr.* != .object) continue;
        if (foldedSet(entry.value_ptr.object, path[1..])) |nested| found = nested;
    }
    return found;
}

pub fn foldedLast(object: std.json.ObjectMap, path: []const []const u8) ?std.json.Value {
    var found: ?std.json.Value = null;
    var entries = object.iterator();
    while (entries.next()) |entry| {
        if (!foldEql(entry.key_ptr.*, path[0])) continue;
        if (path.len == 1) {
            found = entry.value_ptr.*;
            continue;
        }
        if (entry.value_ptr.* != .object) continue;
        if (foldedLast(entry.value_ptr.object, path[1..])) |nested| found = nested;
    }
    return found;
}

pub fn foldedWrongType(object: std.json.ObjectMap, key: []const u8, want: std.meta.Tag(std.json.Value)) bool {
    var entries = object.iterator();
    while (entries.next()) |entry| {
        if (!foldEql(entry.key_ptr.*, key)) continue;
        if (entry.value_ptr.* == .null) continue;
        if (entry.value_ptr.* != want) return true;
    }
    return false;
}

pub const nesting_limit: usize = 10000;

const WalkFrame = struct {
    is_object: bool,
    expect_key: bool = false,
    seen: std.StringHashMapUnmanaged(void) = .empty,
};

pub const Walk = union(enum) { ok, duplicate: []const u8, trailing, too_deep };

pub fn walkFrame(arena: std.mem.Allocator, data: []const u8) !Walk {
    var scanner = std.json.Scanner.initCompleteInput(arena, data);
    defer scanner.deinit();
    var stack = std.ArrayList(WalkFrame).empty;
    var settled = false;
    while (true) {
        const token = scanner.nextAlloc(arena, .alloc_always) catch return if (settled) Walk.trailing else Walk.ok;
        if (settled) {
            if (token == .end_of_document) break;
            return .trailing;
        }
        var closed = false;
        switch (token) {
            .object_begin, .array_begin => {
                if (stack.items.len >= nesting_limit) return .too_deep;
                try stack.append(arena, .{ .is_object = token == .object_begin, .expect_key = token == .object_begin });
            },
            .object_end, .array_end => {
                _ = stack.pop();
                if (stack.items.len == 0) settled = true;
                closed = true;
            },
            .end_of_document => break,
            .allocated_string => |text| {
                if (stack.items.len == 0) {
                    settled = true;
                    continue;
                }
                const top = &stack.items[stack.items.len - 1];
                if (top.is_object and top.expect_key) {
                    if (top.seen.contains(text)) return Walk{ .duplicate = text };
                    try top.seen.put(arena, text, {});
                    top.expect_key = false;
                    continue;
                }
                closed = true;
            },
            else => {
                if (stack.items.len == 0) {
                    settled = true;
                    continue;
                }
                closed = true;
            },
        }
        if (!closed or stack.items.len == 0) continue;
        const top = &stack.items[stack.items.len - 1];
        if (top.is_object) top.expect_key = true;
    }
    return .ok;
}

pub fn withinNestingLimit(data: []const u8) bool {
    var depth: usize = 0;
    var index: usize = 0;
    var in_string = false;
    while (index < data.len) : (index += 1) {
        const byte = data[index];
        if (in_string) {
            if (byte == '\\') {
                index += 1;
                continue;
            }
            if (byte == '"') in_string = false;
            continue;
        }
        switch (byte) {
            '"' => in_string = true,
            '{', '[' => {
                depth += 1;
                if (depth > nesting_limit) return false;
            },
            '}', ']' => depth -|= 1,
            else => {},
        }
    }
    return true;
}

test "nesting is bounded where the oracle bounds it, and braces in strings do not count" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    for ([_]usize{ 9998, 9999 }) |arrays| {
        const opens = try scratch.alloc(u8, arrays);
        @memset(opens, '[');
        const closes = try scratch.alloc(u8, arrays);
        @memset(closes, ']');
        const frame = try std.mem.concat(scratch, u8, &.{ "{\"a\":{\"b\":", opens, closes, "}}" });
        try testing.expectEqual(arrays == 9998, withinNestingLimit(frame));
    }

    const brackets = try scratch.alloc(u8, 10001);
    @memset(brackets, '[');
    try testing.expect(withinNestingLimit(try std.mem.concat(scratch, u8, &.{ "{\"a\":\"", brackets, "\"}" })));
    try testing.expect(withinNestingLimit(try std.mem.concat(scratch, u8, &.{ "{\"a\":\"\\\"", brackets, "\"}" })));
    try testing.expect(withinNestingLimit("[]"));
    try testing.expect(withinNestingLimit("]]]]["));
}

fn nestedArrays(allocator: std.mem.Allocator, containers: usize) ![]const u8 {
    var body = std.ArrayList(u8).empty;
    try body.appendSlice(allocator, "{\"a\":");
    try body.appendNTimes(allocator, '[', containers - 1);
    try body.appendNTimes(allocator, ']', containers - 1);
    try body.appendSlice(allocator, "}");
    return body.items;
}

test "the cheap depth scan and the walking one put the limit in the same place" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    const at_cap = try nestedArrays(allocator, nesting_limit);
    try testing.expect(withinNestingLimit(at_cap));
    try testing.expectEqual(Walk.ok, try walkFrame(allocator, at_cap));

    const past_cap = try nestedArrays(allocator, nesting_limit + 1);
    try testing.expect(!withinNestingLimit(past_cap));
    try testing.expectEqual(Walk.too_deep, try walkFrame(allocator, past_cap));
}

const WalkCase = struct {
    frame: []const u8,
    verdict: []const u8,
};

const walk_oracle = [_]WalkCase{
    .{ .frame = "\"x\"", .verdict = "ok" },
    .{ .frame = "7", .verdict = "ok" },
    .{ .frame = "true", .verdict = "ok" },
    .{ .frame = "null", .verdict = "ok" },
    .{ .frame = "\"x\" \"y\"", .verdict = "trailing" },
    .{ .frame = "7 8", .verdict = "trailing" },
    .{ .frame = "{}", .verdict = "ok" },
    .{ .frame = "{} {}", .verdict = "trailing" },
    .{ .frame = "{\"a\":1}", .verdict = "ok" },
    .{ .frame = "{\"a\":1,\"a\":2}", .verdict = "duplicate" },
    .{ .frame = "[1,2]", .verdict = "ok" },
};

test "a frame the walk was never guarded against is answered, not indexed off the end" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    for (walk_oracle) |case| {
        const outcome = try walkFrame(allocator, case.frame);
        const verdict = switch (outcome) {
            .ok => "ok",
            .trailing => "trailing",
            .too_deep => "too_deep",
            .duplicate => "duplicate",
        };
        try testing.expectEqualStrings(case.verdict, verdict);
    }
}

test "a top-level value settles the frame, so a second one is trailing" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const allocator = arena.allocator();

    try testing.expectEqual(Walk.ok, try walkFrame(allocator, "\"only\""));
    try testing.expectEqual(Walk.trailing, try walkFrame(allocator, "\"one\" \"two\""));
    try testing.expectEqual(Walk.trailing, try walkFrame(allocator, "1 \"two\""));
}
