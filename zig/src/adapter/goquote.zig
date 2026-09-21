const std = @import("std");
const testing = std.testing;

const table = @import("goquote_table.zig");

pub const toolchain = table.toolchain;

pub fn printable(code: u21) bool {
    var low: usize = 0;
    var high: usize = table.printable.len;
    while (low < high) {
        const middle = low + (high - low) / 2;
        const span = table.printable[middle];
        if (code < span[0]) {
            high = middle;
        } else if (code > span[1]) {
            low = middle + 1;
        } else return true;
    }
    return false;
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
            if (printable(code)) {
                out.appendSlice(arena, value[index - width .. index]) catch return value;
            } else if (code < 0x10000) {
                const hex = std.fmt.allocPrint(arena, "\\u{x:0>4}", .{code}) catch return value;
                out.appendSlice(arena, hex) catch return value;
            } else {
                const hex = std.fmt.allocPrint(arena, "\\U{x:0>8}", .{code}) catch return value;
                out.appendSlice(arena, hex) catch return value;
            }
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

fn expectQuotedRune(code: u21, want: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var buffer: [4]u8 = undefined;
    const width = try std.unicode.utf8Encode(code, &buffer);
    try testing.expectEqualStrings(want, quote(arena.allocator(), buffer[0..width]));
}

test "printability follows the Go table, not an ASCII approximation of it" {
    try expectQuotedRune(0x20, "\" \"");
    try expectQuotedRune(0x7E, "\"~\"");
    try expectQuotedRune(0x7F, "\"\\x7f\"");
    try expectQuotedRune(0xA0, "\"\\u00a0\"");
    try expectQuotedRune(0xA1, "\"\xc2\xa1\"");
    try expectQuotedRune(0xAC, "\"\xc2\xac\"");
    try expectQuotedRune(0xAD, "\"\\u00ad\"");
    try expectQuotedRune(0xAE, "\"\xc2\xae\"");
    try expectQuotedRune(0x378, "\"\\u0378\"");
    try expectQuotedRune(0x37A, "\"\xcd\xba\"");
    try expectQuotedRune(0x200B, "\"\\u200b\"");
    try expectQuotedRune(0x2028, "\"\\u2028\"");
    try expectQuotedRune(0x202F, "\"\\u202f\"");
    try expectQuotedRune(0xE000, "\"\\ue000\"");
    try expectQuotedRune(0xFEFF, "\"\\ufeff\"");
    try expectQuotedRune(0xFFFD, "\"\xef\xbf\xbd\"");
    try expectQuotedRune(0x1F600, "\"\xf0\x9f\x98\x80\"");
    try expectQuotedRune(0xE0001, "\"\\U000e0001\"");
    try expectQuotedRune(0x10FFFD, "\"\\U0010fffd\"");
    try expectQuotedRune(0x10FFFF, "\"\\U0010ffff\"");
    try expectQuotedRune(0x59F0F, "\"\\U00059f0f\"");
    try expectQuotedRune(0xDAE0D, "\"\\U000dae0d\"");
    try expectQuotedRune(0x9A29D, "\"\\U0009a29d\"");
    try expectQuotedRune(0xA1230, "\"\\U000a1230\"");
    try expectQuotedRune(0x8E19F, "\"\\U0008e19f\"");
    try expectQuotedRune(0x34C4C, "\"\\U00034c4c\"");
    try expectQuotedRune(0xEE111, "\"\\U000ee111\"");
    try expectQuotedRune(0x1DA6D, "\"\xf0\x9d\xa9\xad\"");
    try expectQuotedRune(0xEEE3C, "\"\\U000eee3c\"");
    try expectQuotedRune(0xCC9A5, "\"\\U000cc9a5\"");
    try expectQuotedRune(0x70A28, "\"\\U00070a28\"");
    try expectQuotedRune(0x9D7D8, "\"\\U0009d7d8\"");
    try expectQuotedRune(0xB31B0, "\"\\U000b31b0\"");
    try expectQuotedRune(0xA030D, "\"\\U000a030d\"");
    try expectQuotedRune(0x971CD, "\"\\U000971cd\"");
    try expectQuotedRune(0x55955, "\"\\U00055955\"");
    try expectQuotedRune(0x18B62, "\"\xf0\x98\xad\xa2\"");
    try expectQuotedRune(0x4D876, "\"\\U0004d876\"");
    try expectQuotedRune(0xBFC13, "\"\\U000bfc13\"");
    try expectQuotedRune(0x10855E, "\"\\U0010855e\"");
    try expectQuotedRune(0x20862, "\"\xf0\xa0\xa1\xa2\"");
    try expectQuotedRune(0x4F04, "\"\xe4\xbc\x84\"");
    try expectQuotedRune(0xA95E8, "\"\\U000a95e8\"");
    try expectQuotedRune(0xA571A, "\"\\U000a571a\"");
}

test "the printable table covers the whole scalar range without overlap or disorder" {
    var previous: u21 = 0;
    for (table.printable, 0..) |span, index| {
        try testing.expect(span[0] <= span[1]);
        if (index > 0) try testing.expect(span[0] > previous + 1);
        previous = span[1];
    }
    try testing.expect(!printable(0x1F));
    try testing.expect(printable(0x20));
    try testing.expect(printable(0x7E));
    try testing.expect(!printable(0x7F));
    try testing.expect(!printable(0x10FFFF));
}
