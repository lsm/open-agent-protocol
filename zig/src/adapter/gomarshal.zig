const std = @import("std");
const testing = std.testing;

pub const Error = std.mem.Allocator.Error || error{UnsupportedValue};

pub fn appendString(out: *std.ArrayList(u8), gpa: std.mem.Allocator, text: []const u8) std.mem.Allocator.Error!void {
    try out.append(gpa, '"');
    var at: usize = 0;
    while (at < text.len) {
        const byte = text[at];
        if (byte == 0xE2 and at + 2 < text.len and text[at + 1] == 0x80 and (text[at + 2] == 0xA8 or text[at + 2] == 0xA9)) {
            try out.appendSlice(gpa, if (text[at + 2] == 0xA8) "\\u2028" else "\\u2029");
            at += 3;
            continue;
        }
        at += 1;
        switch (byte) {
            '"' => try out.appendSlice(gpa, "\\\""),
            '\\' => try out.appendSlice(gpa, "\\\\"),
            '\n' => try out.appendSlice(gpa, "\\n"),
            '\r' => try out.appendSlice(gpa, "\\r"),
            '\t' => try out.appendSlice(gpa, "\\t"),
            0x08 => try out.appendSlice(gpa, "\\b"),
            0x0c => try out.appendSlice(gpa, "\\f"),
            '<' => try out.appendSlice(gpa, "\\u003c"),
            '>' => try out.appendSlice(gpa, "\\u003e"),
            '&' => try out.appendSlice(gpa, "\\u0026"),
            else => if (byte < 0x20) {
                const digits = "0123456789abcdef";
                try out.appendSlice(gpa, &.{ '\\', 'u', '0', '0', digits[byte >> 4], digits[byte & 0xf] });
            } else {
                try out.append(gpa, byte);
            },
        }
    }
    try out.append(gpa, '"');
}

pub fn appendFloat(out: *std.ArrayList(u8), gpa: std.mem.Allocator, value: f64) Error!void {
    if (!std.math.isFinite(value)) return error.UnsupportedValue;
    var buffer: [std.fmt.float.bufferSize(.decimal, f64)]u8 = undefined;
    const magnitude = @abs(value);
    const scientific = magnitude != 0 and (magnitude < 1e-6 or magnitude >= 1e21);
    const rendered = std.fmt.float.render(&buffer, value, .{ .mode = if (scientific) .scientific else .decimal }) catch |err| switch (err) {
        error.BufferTooSmall => return error.UnsupportedValue,
    };
    if (!scientific) return out.appendSlice(gpa, rendered);
    const mark = std.mem.indexOfScalar(u8, rendered, 'e') orelse return out.appendSlice(gpa, rendered);
    try out.appendSlice(gpa, rendered[0 .. mark + 1]);
    if (rendered[mark + 1] != '-') try out.append(gpa, '+');
    try out.appendSlice(gpa, rendered[mark + 1 ..]);
}

pub fn appendValue(out: *std.ArrayList(u8), gpa: std.mem.Allocator, value: std.json.Value) Error!void {
    switch (value) {
        .null => try out.appendSlice(gpa, "null"),
        .bool => |flag| try out.appendSlice(gpa, if (flag) "true" else "false"),
        .integer => |number| try out.print(gpa, "{d}", .{number}),
        .float => |number| try appendFloat(out, gpa, number),
        .number_string => |text| try out.appendSlice(gpa, text),
        .string => |text| try appendString(out, gpa, text),
        .array => |items| {
            try out.append(gpa, '[');
            for (items.items, 0..) |item, index| {
                if (index != 0) try out.append(gpa, ',');
                try appendValue(out, gpa, item);
            }
            try out.append(gpa, ']');
        },
        .object => |members| {
            try out.append(gpa, '{');
            var entries = members.iterator();
            var first = true;
            while (entries.next()) |entry| {
                if (!first) try out.append(gpa, ',');
                first = false;
                try appendString(out, gpa, entry.key_ptr.*);
                try out.append(gpa, ':');
                try appendValue(out, gpa, entry.value_ptr.*);
            }
            try out.append(gpa, '}');
        },
    }
}

pub fn marshal(arena: std.mem.Allocator, value: std.json.Value) Error![]const u8 {
    var out = std.ArrayList(u8).empty;
    try appendValue(&out, arena, value);
    return out.items;
}

fn keyLess(_: void, left: []const u8, right: []const u8) bool {
    return std.mem.order(u8, left, right) == .lt;
}

pub fn canonicalAny(arena: std.mem.Allocator, value: std.json.Value) std.mem.Allocator.Error!std.json.Value {
    return switch (value) {
        .null, .bool, .string => value,
        .integer => |number| .{ .float = @floatFromInt(number) },
        .float => |number| .{ .float = number },
        .number_string => |text| if (std.fmt.parseFloat(f64, text)) |number| .{ .float = number } else |_| value,
        .array => |items| blk: {
            var copied = try std.json.Array.initCapacity(arena, items.items.len);
            for (items.items) |item| copied.appendAssumeCapacity(try canonicalAny(arena, item));
            break :blk .{ .array = copied };
        },
        .object => |members| blk: {
            const keys = try arena.alloc([]const u8, members.count());
            for (members.keys(), keys) |key, *slot| slot.* = key;
            std.mem.sort([]const u8, keys, {}, keyLess);
            var sorted: std.json.ObjectMap = .empty;
            try sorted.ensureTotalCapacity(arena, keys.len);
            for (keys) |key| sorted.putAssumeCapacity(key, try canonicalAny(arena, members.get(key).?));
            break :blk .{ .object = sorted };
        },
    };
}

fn expectFloat(value: f64, want: []const u8) !void {
    var out = std.ArrayList(u8).empty;
    defer out.deinit(testing.allocator);
    try appendFloat(&out, testing.allocator, value);
    try testing.expectEqualStrings(want, out.items);
}

test "a float is written the way encoding/json writes a float64" {
    try expectFloat(0, "0");
    try expectFloat(-0.0, "-0");
    try expectFloat(1, "1");
    try expectFloat(1.5, "1.5");
    try expectFloat(0.3, "0.3");
    try expectFloat(1e20, "100000000000000000000");
    try expectFloat(1e21, "1e+21");
    try expectFloat(1.2345678901234568e17, "123456789012345680");
    try expectFloat(1.2345678901234567e19, "12345678901234567000");
    try expectFloat(1e-6, "0.000001");
    try expectFloat(0.000001234, "0.000001234");
    try expectFloat(2.5e-5, "0.000025");
    try expectFloat(1e-7, "1e-7");
    try expectFloat(1.5e-7, "1.5e-7");
    try expectFloat(1e-10, "1e-10");
    try expectFloat(1e100, "1e+100");
    try expectFloat(5e-324, "5e-324");
    try expectFloat(std.math.floatMax(f64), "1.7976931348623157e+308");
}

test "a float encoding/json cannot write is refused rather than invented" {
    var out = std.ArrayList(u8).empty;
    defer out.deinit(testing.allocator);
    try testing.expectError(error.UnsupportedValue, appendFloat(&out, testing.allocator, std.math.inf(f64)));
    try testing.expectError(error.UnsupportedValue, appendFloat(&out, testing.allocator, -std.math.inf(f64)));
    try testing.expectError(error.UnsupportedValue, appendFloat(&out, testing.allocator, std.math.nan(f64)));
    try testing.expectEqual(@as(usize, 0), out.items.len);
}

test "a string is escaped the way encoding/json escapes it, HTML-safe" {
    var out = std.ArrayList(u8).empty;
    defer out.deinit(testing.allocator);
    try appendString(&out, testing.allocator, "a<b>&c \"q\" \\ \u{2028}\u{2029}\n\t\r\x08\x0c\x01\x1f\x7f \u{e9}");
    try testing.expectEqualStrings("\"a\\u003cb\\u003e\\u0026c \\\"q\\\" \\\\ \\u2028\\u2029\\n\\t\\r\\b\\f\\u0001\\u001f\x7f \u{e9}\"", out.items);
}

test "a decoded any is re-encoded with sorted keys and float64 numbers" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, "{\"b\":1,\"a\":{\"z\":[1.0,-0,12345678901234567890]},\"B\":\"<\",\"_\":null,\"\u{e9}\":true}", .{ .parse_numbers = false });
    const encoded = try marshal(scratch, try canonicalAny(scratch, parsed));
    try testing.expectEqualStrings("{\"B\":\"\\u003c\",\"_\":null,\"a\":{\"z\":[1,-0,12345678901234567000]},\"b\":1,\"\u{e9}\":true}", encoded);
}

test "an object keeps the order it was built in" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var object: std.json.ObjectMap = .empty;
    try object.put(scratch, "z", .{ .integer = 1 });
    try object.put(scratch, "a", .{ .float = 0.5 });
    try object.put(scratch, "m", .{ .number_string = "18446744073709551615" });
    try testing.expectEqualStrings("{\"z\":1,\"a\":0.5,\"m\":18446744073709551615}", try marshal(scratch, .{ .object = object }));
}
