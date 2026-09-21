const std = @import("std");
const testing = std.testing;

pub fn unprintable(code: u21) bool {
    return code <= 0x9f or code == 0xa0 or code == 0xad;
}

pub fn quote(arena: std.mem.Allocator, value: []const u8) []const u8 {
    var out = std.ArrayList(u8).empty;
    out.append(arena, '"') catch return value;
    var index: usize = 0;
    while (index < value.len) {
        const byte = value[index];
        if (byte < 0x80) {
            index += 1;
            switch (byte) {
                '"' => out.appendSlice(arena, "\\\"") catch return value,
                '\\' => out.appendSlice(arena, "\\\\") catch return value,
                '\n' => out.appendSlice(arena, "\\n") catch return value,
                '\r' => out.appendSlice(arena, "\\r") catch return value,
                '\t' => out.appendSlice(arena, "\\t") catch return value,
                0x07 => out.appendSlice(arena, "\\a") catch return value,
                0x08 => out.appendSlice(arena, "\\b") catch return value,
                0x0b => out.appendSlice(arena, "\\v") catch return value,
                0x0c => out.appendSlice(arena, "\\f") catch return value,
                0x00...0x06, 0x0e...0x1f, 0x7f => {
                    const hex = std.fmt.allocPrint(arena, "\\x{x:0>2}", .{byte}) catch return value;
                    out.appendSlice(arena, hex) catch return value;
                },
                else => out.append(arena, byte) catch return value,
            }
            continue;
        }
        const width = std.unicode.utf8ByteSequenceLength(byte) catch {
            index += 1;
            const hex = std.fmt.allocPrint(arena, "\\x{x:0>2}", .{byte}) catch return value;
            out.appendSlice(arena, hex) catch return value;
            continue;
        };
        const decoded = if (index + width <= value.len) std.unicode.utf8Decode(value[index .. index + width]) catch null else null;
        if (decoded) |code| {
            index += width;
            if (unprintable(code)) {
                const hex = std.fmt.allocPrint(arena, "\\u{x:0>4}", .{code}) catch return value;
                out.appendSlice(arena, hex) catch return value;
            } else out.appendSlice(arena, value[index - width .. index]) catch return value;
            continue;
        }
        index += 1;
        const hex = std.fmt.allocPrint(arena, "\\x{x:0>2}", .{byte}) catch return value;
        out.appendSlice(arena, hex) catch return value;
    }
    out.append(arena, '"') catch return value;
    return out.items;
}

test "a value is escaped the way strconv.Quote escapes it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    try testing.expectEqualStrings("\"plain\"", quote(scratch, "plain"));
    try testing.expectEqualStrings("\"we\\\"ird\"", quote(scratch, "we\"ird"));
    try testing.expectEqualStrings("\"back\\\\slash\"", quote(scratch, "back\\slash"));
    try testing.expectEqualStrings("\"a\\nb\"", quote(scratch, "a\nb"));
    try testing.expectEqualStrings("\"a\\x00b\"", quote(scratch, "a\x00b"));
    try testing.expectEqualStrings("\"\\a\\b\\v\\f\"", quote(scratch, "\x07\x08\x0b\x0c"));
    try testing.expectEqualStrings("\"\\r\\t\\x1f\"", quote(scratch, "\r\t\x1f"));
    try testing.expectEqualStrings("\"\\u0080\\u009f\"", quote(scratch, "\u{80}\u{9f}"));
    try testing.expectEqualStrings("\"\\u00a0\\u00ad\"", quote(scratch, "\u{a0}\u{ad}"));
    try testing.expectEqualStrings("\"\u{b0}\u{bf}\u{ab}\u{a9}\"", quote(scratch, "\u{b0}\u{bf}\u{ab}\u{a9}"));
    try testing.expectEqualStrings("\"\\xc2A\"", quote(scratch, "\xc2A"));
    try testing.expectEqualStrings("\"a\\xc2b\"", quote(scratch, "a\xc2b"));
    try testing.expectEqualStrings("\"\\xff\"", quote(scratch, "\xff"));
    try testing.expectEqualStrings("\"\\xc2\"", quote(scratch, "\xc2"));
    try testing.expectEqualStrings("\"\\xe2\\x80\"", quote(scratch, "\xe2\x80"));
    try testing.expectEqualStrings("\"\u{1f600}\"", quote(scratch, "\u{1f600}"));
    try testing.expectEqualStrings("\"caf\u{e9} na\u{ef}ve\"", quote(scratch, "caf\u{e9} na\u{ef}ve"));
}
