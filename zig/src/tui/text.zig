const std = @import("std");
const zz = @import("zigzag");

const ellipsis = "…";

pub fn visibleWidth(text: []const u8) usize {
    return zz.width(text);
}

pub const RowRange = struct {
    start: usize,
    end: usize,
    width: usize,
};

pub const CursorPos = struct {
    row: usize,
    col: usize,
};

pub fn layoutRows(allocator: std.mem.Allocator, text: []const u8, width: usize) ![]RowRange {
    const limit = @max(width, 1);
    var rows: std.ArrayList(RowRange) = .empty;
    errdefer rows.deinit(allocator);
    var row_start: usize = 0;
    var row_width: usize = 0;
    var i: usize = 0;
    while (i < text.len) {
        if (text[i] == '\n') {
            try rows.append(allocator, .{ .start = row_start, .end = i, .width = row_width });
            i += 1;
            row_start = i;
            row_width = 0;
            continue;
        }
        const len = std.unicode.utf8ByteSequenceLength(text[i]) catch 1;
        const end = @min(text.len, i + len);
        const cell = displayCellWidth(text[i..end]);
        if (row_width > 0 and row_width + cell > limit) {
            try rows.append(allocator, .{ .start = row_start, .end = i, .width = row_width });
            row_start = i;
            row_width = 0;
            continue;
        }
        row_width += cell;
        i = end;
    }
    try rows.append(allocator, .{ .start = row_start, .end = text.len, .width = row_width });
    if (row_width == limit and text.len > 0 and text[text.len - 1] != '\n') {
        try rows.append(allocator, .{ .start = text.len, .end = text.len, .width = 0 });
    }
    return rows.toOwnedSlice(allocator);
}

pub fn cursorPos(rows: []const RowRange, text: []const u8, cursor: usize) CursorPos {
    if (rows.len == 0) return .{ .row = 0, .col = 0 };
    const at = boundaryAtOrBefore(text, @min(cursor, text.len));
    var row: usize = 0;
    for (rows, 0..) |r, i| {
        if (r.start <= at) row = i else break;
    }
    return .{ .row = row, .col = displayWidthOf(text[rows[row].start..at]) };
}

pub fn byteOffsetAtColumn(rows: []const RowRange, text: []const u8, row: usize, col: usize) usize {
    if (rows.len == 0) return 0;
    const r = rows[@min(row, rows.len - 1)];
    var acc: usize = 0;
    var i = r.start;
    while (i < r.end) {
        const len = std.unicode.utf8ByteSequenceLength(text[i]) catch 1;
        const end = @min(r.end, i + len);
        const cell = displayCellWidth(text[i..end]);
        if (acc + cell > col) break;
        acc += cell;
        i = end;
    }
    return i;
}

pub fn writeDisplayEscaped(writer: *std.Io.Writer, bytes: []const u8) !void {
    var i: usize = 0;
    while (i < bytes.len) {
        const len = std.unicode.utf8ByteSequenceLength(bytes[i]) catch 1;
        const end = @min(bytes.len, i + len);
        try writeDisplayCodepoint(writer, bytes[i..end]);
        i = end;
    }
}

fn writeDisplayCodepoint(writer: *std.Io.Writer, bytes: []const u8) !void {
    if (bytes.len == 1) {
        const b = bytes[0];
        if (b == '\t') return writer.writeAll("\u{2192}");
        if (b == 0x7f) return writer.writeAll("^?");
        if (b < 0x20) {
            try writer.writeByte('^');
            return writer.writeByte(b + 0x40);
        }
        if (b < 0x80) return writer.writeByte(b);
        return writer.writeByte('?');
    }
    const cp = std.unicode.utf8Decode(bytes) catch return writer.writeByte('?');
    if (cp >= 0x80 and cp <= 0x9f) return writer.writeByte('?');
    return writer.writeAll(bytes);
}

fn displayCellWidth(bytes: []const u8) usize {
    if (bytes.len == 1) {
        const b = bytes[0];
        if (b == '\t') return 1;
        if (b < 0x20 or b == 0x7f) return 2;
        return 1;
    }
    const cp = std.unicode.utf8Decode(bytes) catch return 1;
    if (cp >= 0x80 and cp <= 0x9f) return 1;
    return zz.measure.charWidth(cp);
}

fn displayWidthOf(bytes: []const u8) usize {
    var total: usize = 0;
    var i: usize = 0;
    while (i < bytes.len) {
        if (bytes[i] == '\n') break;
        const len = std.unicode.utf8ByteSequenceLength(bytes[i]) catch 1;
        const end = @min(bytes.len, i + len);
        total += displayCellWidth(bytes[i..end]);
        i = end;
    }
    return total;
}

fn boundaryAtOrBefore(text: []const u8, index: usize) usize {
    var idx = @min(index, text.len);
    while (idx > 0 and idx < text.len and (text[idx] & 0b1100_0000) == 0b1000_0000) idx -= 1;
    return idx;
}

pub fn lineCount(text: []const u8) usize {
    if (text.len == 0) return 0;
    var count: usize = 1;
    for (text) |c| {
        if (c == '\n') count += 1;
    }
    return count;
}

pub fn compactNumber(allocator: std.mem.Allocator, value: u64) ![]u8 {
    if (value >= 1_000_000) return std.fmt.allocPrint(allocator, "{d}M", .{value / 1_000_000});
    if (value >= 1_000) return std.fmt.allocPrint(allocator, "{d}k", .{value / 1_000});
    return std.fmt.allocPrint(allocator, "{d}", .{value});
}

pub fn sanitizeTerminalText(allocator: std.mem.Allocator, text: []const u8) ![]u8 {
    var out = std.ArrayList(u8).empty;
    errdefer out.deinit(allocator);
    var i: usize = 0;
    while (i < text.len) {
        const len = std.unicode.utf8ByteSequenceLength(text[i]) catch 1;
        const end = @min(text.len, i + len);
        const codepoint: u21 = std.unicode.utf8Decode(text[i..end]) catch 0xFFFD;
        const control = codepoint < 0x20 or codepoint == 0x7f or (codepoint >= 0x80 and codepoint <= 0x9f) or codepoint == 0xFFFD;
        if (control) try out.append(allocator, '?') else try out.appendSlice(allocator, text[i..end]);
        i = end;
    }
    return out.toOwnedSlice(allocator);
}

test "sanitizeTerminalText neutralises control bytes and escape sequences" {
    const cleaned = try sanitizeTerminalText(std.testing.allocator, "repo\x1b]0;evil\x07/dir\n\xc2\x9bx\x7fend");
    defer std.testing.allocator.free(cleaned);
    try std.testing.expectEqualStrings("repo?]0;evil?/dir??x?end", cleaned);
    const plain = try sanitizeTerminalText(std.testing.allocator, "/Users/me/projects/日本語");
    defer std.testing.allocator.free(plain);
    try std.testing.expectEqualStrings("/Users/me/projects/日本語", plain);
}

pub fn truncateToWidth(allocator: std.mem.Allocator, text: []const u8, max_width: usize) ![]u8 {
    return truncateLineToWidth(allocator, text, max_width);
}

pub fn truncateLineToWidth(allocator: std.mem.Allocator, text: []const u8, max_width: usize) ![]u8 {
    if (max_width == 0) return allocator.dupe(u8, "");
    if (visibleWidth(text) <= max_width and std.mem.indexOfScalar(u8, text, '\n') == null) return allocator.dupe(u8, text);
    if (max_width <= 1) return allocator.dupe(u8, ellipsis);

    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    const target = max_width - 1;
    var width: usize = 0;
    var i: usize = 0;
    var open_sgr = false;
    var truncated = false;

    while (i < text.len and width < target) {
        if (text[i] == 0x1b) {
            const start = i;
            try copyAnsiSequence(writer, text, &i);
            if (i > start and isSgrSequence(text[start..i])) open_sgr = true;
            continue;
        }
        if (text[i] == '\n') {
            truncated = true;
            break;
        }

        const len = std.unicode.utf8ByteSequenceLength(text[i]) catch 1;
        if (i + len > text.len) break;
        const codepoint = std.unicode.utf8Decode(text[i .. i + len]) catch text[i];
        const cw = zz.measure.charWidth(@intCast(codepoint));
        if (width + cw > target) {
            truncated = true;
            break;
        }
        try writer.writeAll(text[i .. i + len]);
        width += cw;
        i += len;
    }

    if (i < text.len or truncated) try writer.writeAll(ellipsis);
    if (open_sgr) try writer.writeAll(zz.ansi.reset);
    return out.toOwnedSlice();
}

pub fn truncateLinesToWidth(allocator: std.mem.Allocator, text: []const u8, line_width: usize, max_lines: usize) ![]u8 {
    if (line_width == 0 or max_lines == 0) return allocator.dupe(u8, "");
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    var lines = std.mem.splitScalar(u8, text, '\n');
    var row: usize = 0;
    while (row < max_lines) : (row += 1) {
        const line = lines.next() orelse break;
        if (row > 0) try writer.writeByte('\n');
        const has_more_lines = row + 1 == max_lines and lines.peek() != null;
        const width = if (has_more_lines) line_width -| 1 else line_width;
        const clipped = try truncateLineToWidth(allocator, line, width);
        defer allocator.free(clipped);
        try writer.writeAll(clipped);
        if (has_more_lines) try writer.writeAll(ellipsis);
    }
    return out.toOwnedSlice();
}

pub fn wrapTextWithAnsi(allocator: std.mem.Allocator, text: []const u8, max_width: usize) ![]u8 {
    if (max_width == 0 or text.len == 0) return allocator.dupe(u8, text);

    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    var line_width: usize = 0;
    var word = std.ArrayList(u8).empty;
    defer word.deinit(allocator);
    var word_width: usize = 0;
    var pending_space = false;

    var i: usize = 0;
    while (i < text.len) {
        if (text[i] == 0x1b) {
            const start = i;
            var sink: std.Io.Writer.Allocating = .init(allocator);
            defer sink.deinit();
            try copyAnsiSequence(&sink.writer, text, &i);
            try word.appendSlice(allocator, sink.written());
            _ = start;
            continue;
        }

        const c = text[i];
        if (c == '\n') {
            try flushWord(writer, &word, word_width, &line_width, max_width, pending_space);
            word_width = 0;
            pending_space = false;
            try writer.writeByte('\n');
            line_width = 0;
            i += 1;
            continue;
        }
        if (c == ' ' or c == '\t' or c == '\r') {
            try flushWord(writer, &word, word_width, &line_width, max_width, pending_space);
            word_width = 0;
            pending_space = line_width > 0;
            i += 1;
            continue;
        }

        const len = std.unicode.utf8ByteSequenceLength(c) catch 1;
        if (i + len > text.len) break;
        const codepoint = std.unicode.utf8Decode(text[i .. i + len]) catch c;
        const cw = zz.measure.charWidth(@intCast(codepoint));
        try word.appendSlice(allocator, text[i .. i + len]);
        word_width += cw;
        i += len;
    }

    try flushWord(writer, &word, word_width, &line_width, max_width, pending_space);
    return out.toOwnedSlice();
}

fn flushWord(writer: *std.Io.Writer, word: *std.ArrayList(u8), word_width: usize, line_width: *usize, max_width: usize, pending_space: bool) !void {
    if (word.items.len == 0) return;
    const sep: usize = if (pending_space and line_width.* > 0) 1 else 0;
    if (line_width.* > 0 and line_width.* + sep + word_width > max_width) {
        try writer.writeByte('\n');
        line_width.* = 0;
    } else if (sep == 1) {
        try writer.writeByte(' ');
        line_width.* += 1;
    }
    try writer.writeAll(word.items);
    line_width.* += word_width;
    word.clearRetainingCapacity();
}

fn copyAnsiSequence(writer: *std.Io.Writer, text: []const u8, index: *usize) !void {
    const start = index.*;
    try writer.writeByte(text[index.*]);
    index.* += 1;
    if (index.* >= text.len) return;
    try writer.writeByte(text[index.*]);
    const second = text[index.*];
    index.* += 1;

    if (second == '[') {
        while (index.* < text.len) {
            const c = text[index.*];
            try writer.writeByte(c);
            index.* += 1;
            if (c >= 0x40 and c <= 0x7e) return;
        }
        return;
    }
    if (second == ']') {
        while (index.* < text.len) {
            const c = text[index.*];
            try writer.writeByte(c);
            index.* += 1;
            if (c == 0x07) return;
            if (c == 0x1b and index.* < text.len and text[index.*] == '\\') {
                try writer.writeByte(text[index.*]);
                index.* += 1;
                return;
            }
        }
        return;
    }
    if (second >= '(' and second <= '+') {
        if (index.* < text.len) {
            try writer.writeByte(text[index.*]);
            index.* += 1;
        }
        return;
    }
    if (second == 'P') {
        while (index.* < text.len) {
            const c = text[index.*];
            try writer.writeByte(c);
            index.* += 1;
            if (c == 0x07) return;
            if (c == 0x1b and index.* < text.len and text[index.*] == '\\') {
                try writer.writeByte(text[index.*]);
                index.* += 1;
                return;
            }
        }
        return;
    }
    _ = start;
}

fn isSgrSequence(seq: []const u8) bool {
    return seq.len >= 3 and seq[0] == 0x1b and seq[1] == '[' and seq[seq.len - 1] == 'm';
}

test "compactNumber formats suffixes" {
    const small = try compactNumber(std.testing.allocator, 42);
    defer std.testing.allocator.free(small);
    try std.testing.expectEqualStrings("42", small);
    const thousands = try compactNumber(std.testing.allocator, 12_345);
    defer std.testing.allocator.free(thousands);
    try std.testing.expectEqualStrings("12k", thousands);
    const millions = try compactNumber(std.testing.allocator, 2_000_000);
    defer std.testing.allocator.free(millions);
    try std.testing.expectEqualStrings("2M", millions);
}

test "visibleWidth ignores ANSI" {
    try std.testing.expectEqual(@as(usize, 5), visibleWidth("\x1b[31mhello\x1b[0m"));
}

test "truncateToWidth preserves ANSI and width" {
    const text = try truncateToWidth(std.testing.allocator, "\x1b[31mhello world\x1b[0m", 6);
    defer std.testing.allocator.free(text);
    try std.testing.expect(visibleWidth(text) <= 6);
    try std.testing.expect(std.mem.indexOf(u8, text, ellipsis) != null);
}

test "truncateLinesToWidth caps output to max lines" {
    const text = try truncateLinesToWidth(std.testing.allocator, "one\ntwo\nthree\nfour", 10, 3);
    defer std.testing.allocator.free(text);
    try std.testing.expectEqual(@as(usize, 3), lineCount(text));
    try std.testing.expect(std.mem.indexOf(u8, text, "three") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, ellipsis) != null);
}

test "truncateToWidth accepts CSI tilde terminator" {
    const text = try truncateToWidth(std.testing.allocator, "\x1b[1~hello", 5);
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "hello") != null);
}

test "wrapTextWithAnsi accepts CSI tilde terminator" {
    const text = try wrapTextWithAnsi(std.testing.allocator, "\x1b[1~alpha beta", 20);
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "alpha") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "beta") != null);
}

test "wrapTextWithAnsi wraps words" {
    const text = try wrapTextWithAnsi(std.testing.allocator, "alpha beta gamma", 10);
    defer std.testing.allocator.free(text);
    try std.testing.expectEqualStrings("alpha beta\ngamma", text);
}

test "layoutRows wraps at the width and breaks on newlines" {
    const rows = try layoutRows(std.testing.allocator, "abcdefgh\nij", 4);
    defer std.testing.allocator.free(rows);
    try std.testing.expectEqual(@as(usize, 3), rows.len);
    try std.testing.expectEqual(RowRange{ .start = 0, .end = 4, .width = 4 }, rows[0]);
    try std.testing.expectEqual(RowRange{ .start = 4, .end = 8, .width = 4 }, rows[1]);
    try std.testing.expectEqual(RowRange{ .start = 9, .end = 11, .width = 2 }, rows[2]);
}

test "layoutRows appends an empty row after a full final row" {
    const rows = try layoutRows(std.testing.allocator, "abcdefgh", 4);
    defer std.testing.allocator.free(rows);
    try std.testing.expectEqual(@as(usize, 3), rows.len);
    try std.testing.expectEqual(RowRange{ .start = 8, .end = 8, .width = 0 }, rows[2]);
}

test "layoutRows keeps one empty row for empty text" {
    const rows = try layoutRows(std.testing.allocator, "", 8);
    defer std.testing.allocator.free(rows);
    try std.testing.expectEqual(@as(usize, 1), rows.len);
    try std.testing.expectEqual(RowRange{ .start = 0, .end = 0, .width = 0 }, rows[0]);
}

test "layoutRows never splits a wide codepoint" {
    const rows = try layoutRows(std.testing.allocator, "ab日本c", 3);
    defer std.testing.allocator.free(rows);
    try std.testing.expectEqual(@as(usize, 4), rows.len);
    try std.testing.expectEqual(RowRange{ .start = 0, .end = 2, .width = 2 }, rows[0]);
    try std.testing.expectEqual(RowRange{ .start = 2, .end = 5, .width = 2 }, rows[1]);
    try std.testing.expectEqual(RowRange{ .start = 5, .end = 9, .width = 3 }, rows[2]);
    try std.testing.expectEqual(RowRange{ .start = 9, .end = 9, .width = 0 }, rows[3]);
}

test "layoutRows counts control bytes by their visible form" {
    const rows = try layoutRows(std.testing.allocator, "a\tb\x01\x7fc", 9);
    defer std.testing.allocator.free(rows);
    try std.testing.expectEqual(@as(usize, 1), rows.len);
    try std.testing.expectEqual(@as(usize, 8), rows[0].width);
}

test "layoutRows survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            const rows = try layoutRows(allocator, "ab\ncdef\n\ngh", 3);
            defer allocator.free(rows);
            try std.testing.expect(rows.len >= 4);
        }
    }.run, .{});
}

test "cursorPos maps byte offsets to rows and columns" {
    const text = "abcdefgh\nij";
    const rows = try layoutRows(std.testing.allocator, text, 4);
    defer std.testing.allocator.free(rows);
    try std.testing.expectEqual(CursorPos{ .row = 0, .col = 0 }, cursorPos(rows, text, 0));
    try std.testing.expectEqual(CursorPos{ .row = 1, .col = 0 }, cursorPos(rows, text, 4));
    try std.testing.expectEqual(CursorPos{ .row = 1, .col = 4 }, cursorPos(rows, text, 8));
    try std.testing.expectEqual(CursorPos{ .row = 2, .col = 0 }, cursorPos(rows, text, 9));
    try std.testing.expectEqual(CursorPos{ .row = 2, .col = 1 }, cursorPos(rows, text, 10));
}

test "cursorPos puts the cursor on its own row after a full final row" {
    const text = "abcdefgh";
    const rows = try layoutRows(std.testing.allocator, text, 4);
    defer std.testing.allocator.free(rows);
    try std.testing.expectEqual(CursorPos{ .row = 2, .col = 0 }, cursorPos(rows, text, 8));
    try std.testing.expectEqual(CursorPos{ .row = 1, .col = 0 }, cursorPos(rows, text, 4));
}

test "cursorPos keeps the cursor on the row when it sits on a newline" {
    const text = "ab\ncd";
    const rows = try layoutRows(std.testing.allocator, text, 8);
    defer std.testing.allocator.free(rows);
    try std.testing.expectEqual(CursorPos{ .row = 0, .col = 2 }, cursorPos(rows, text, 2));
    try std.testing.expectEqual(CursorPos{ .row = 1, .col = 0 }, cursorPos(rows, text, 3));
}

test "byteOffsetAtColumn snaps a wide codepoint to its start" {
    const text = "ab日cd";
    const rows = try layoutRows(std.testing.allocator, text, 10);
    defer std.testing.allocator.free(rows);
    try std.testing.expectEqual(@as(usize, 0), byteOffsetAtColumn(rows, text, 0, 0));
    try std.testing.expectEqual(@as(usize, 2), byteOffsetAtColumn(rows, text, 0, 3));
    try std.testing.expectEqual(@as(usize, 5), byteOffsetAtColumn(rows, text, 0, 4));
    try std.testing.expectEqual(@as(usize, 7), byteOffsetAtColumn(rows, text, 0, 99));
}

test "writeDisplayEscaped renders control bytes visibly" {
    var out: std.Io.Writer.Allocating = .init(std.testing.allocator);
    defer out.deinit();
    try writeDisplayEscaped(&out.writer, "a\tb\x01\x1b\x7fc");
    try std.testing.expectEqualStrings("a\u{2192}b^A^[^?c", out.written());
}

test "writeDisplayEscaped replaces invalid UTF-8 and C1 codepoints" {
    var out: std.Io.Writer.Allocating = .init(std.testing.allocator);
    defer out.deinit();
    try writeDisplayEscaped(&out.writer, "a\x80b\xc2\x85c");
    try std.testing.expectEqualStrings("a?b?c", out.written());
}
