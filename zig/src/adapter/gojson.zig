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

pub const nesting_limit: usize = 10000;

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
