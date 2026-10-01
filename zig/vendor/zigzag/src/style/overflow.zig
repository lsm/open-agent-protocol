
const std = @import("std");
const Writer = std.Io.Writer;
const measure = @import("../layout/measure.zig");
const ansi = @import("../terminal/ansi.zig");
const unicode = @import("../unicode.zig");

pub const Overflow = enum {
    visible,
    hidden,
    ellipsis,
    word_wrap,
    char_wrap,
};

pub fn applyOverflow(
    allocator: std.mem.Allocator,
    text: []const u8,
    max_width: u16,
    policy: Overflow,
) ![]const u8 {
    if (policy == .visible or max_width == 0) return text;

    var result: std.array_list.Managed(u8) = .init(allocator);

    var lines = std.mem.splitScalar(u8, text, '\n');
    var first_line = true;
    while (lines.next()) |line| {
        if (!first_line) try result.append('\n');
        first_line = false;

        switch (policy) {
            .hidden => try applyClip(&result, line, max_width),
            .ellipsis => try applyEllipsis(&result, line, max_width),
            .word_wrap => try applyWordWrap(&result, line, max_width),
            .char_wrap => try applyCharWrap(&result, line, max_width),
            .visible => unreachable,
        }
    }

    return result.toOwnedSlice();
}

fn applyClip(result: *std.array_list.Managed(u8), line: []const u8, max_width: u16) !void {
    var visible_width: usize = 0;
    var i: usize = 0;
    while (i < line.len) {
        const esc_len = ansi.escapeSequenceLen(line, i);
        if (esc_len > 0) {
            try result.appendSlice(line[i..][0..esc_len]);
            i += esc_len;
            continue;
        }

        const char_width = charDisplayWidth(line, i);
        if (visible_width + char_width > max_width) break;

        const byte_len = charByteLen(line[i]);
        try result.appendSlice(line[i .. i + byte_len]);
        visible_width += char_width;
        i += byte_len;
    }
}

fn applyEllipsis(result: *std.array_list.Managed(u8), line: []const u8, max_width: u16) !void {
    const line_width = measure.width(line);
    if (line_width <= max_width) {
        try result.appendSlice(line);
        return;
    }

    if (max_width <= 1) {
        if (max_width == 1) try result.appendSlice("\xe2\x80\xa6");
        return;
    }

    var visible_width: usize = 0;
    var i: usize = 0;
    const target_width = max_width - 1;
    while (i < line.len) {
        const esc_len = ansi.escapeSequenceLen(line, i);
        if (esc_len > 0) {
            try result.appendSlice(line[i..][0..esc_len]);
            i += esc_len;
            continue;
        }

        const char_width = charDisplayWidth(line, i);
        if (visible_width + char_width > target_width) break;

        const byte_len = charByteLen(line[i]);
        try result.appendSlice(line[i .. i + byte_len]);
        visible_width += char_width;
        i += byte_len;
    }

    try result.appendSlice("\xe2\x80\xa6");
}

fn applyWordWrap(result: *std.array_list.Managed(u8), line: []const u8, max_width: u16) !void {
    if (measure.width(line) <= max_width) {
        try result.appendSlice(line);
        return;
    }

    var visible_width: usize = 0;
    var i: usize = 0;
    var line_start = true;

    while (i < line.len) {
        const esc_len = ansi.escapeSequenceLen(line, i);
        if (esc_len > 0) {
            try result.appendSlice(line[i..][0..esc_len]);
            i += esc_len;
            continue;
        }

        if (line[i] == ' ') {
            if (visible_width + 1 > max_width) {
                try result.append('\n');
                visible_width = 0;
                line_start = true;
                i += 1;
                while (i < line.len and line[i] == ' ') : (i += 1) {}
                continue;
            }
            if (!line_start) {
                try result.append(' ');
                visible_width += 1;
            }
            i += 1;
            continue;
        }

        const word_start = i;
        var word_width: usize = 0;
        while (i < line.len and line[i] != ' ') {
            const inner_esc = ansi.escapeSequenceLen(line, i);
            if (inner_esc > 0) {
                i += inner_esc;
                continue;
            }
            word_width += charDisplayWidth(line, i);
            i += charByteLen(line[i]);
        }
        const word = line[word_start..i];

        if (!line_start and visible_width + word_width > max_width) {
            try result.append('\n');
            visible_width = 0;
            line_start = true;
        }

        try result.appendSlice(word);
        visible_width += word_width;
        line_start = false;
    }
}

fn applyCharWrap(result: *std.array_list.Managed(u8), line: []const u8, max_width: u16) !void {
    if (measure.width(line) <= max_width) {
        try result.appendSlice(line);
        return;
    }

    var visible_width: usize = 0;
    var i: usize = 0;
    var first_on_line = true;

    while (i < line.len) {
        const esc_len = ansi.escapeSequenceLen(line, i);
        if (esc_len > 0) {
            try result.appendSlice(line[i..][0..esc_len]);
            i += esc_len;
            continue;
        }

        const char_width = charDisplayWidth(line, i);
        if (visible_width + char_width > max_width and !first_on_line) {
            try result.append('\n');
            visible_width = 0;
            first_on_line = true;
        }

        const byte_len = charByteLen(line[i]);
        try result.appendSlice(line[i .. i + byte_len]);
        visible_width += char_width;
        first_on_line = false;
        i += byte_len;
    }
}

fn charDisplayWidth(text: []const u8, pos: usize) usize {
    const byte = text[pos];
    if (byte < 0x80) return 1;

    const byte_len = charByteLen(byte);
    if (pos + byte_len > text.len) return 1;
    const cp = std.unicode.utf8Decode(text[pos..][0..byte_len]) catch return 1;
    return unicode.charWidth(cp);
}

fn charByteLen(first_byte: u8) usize {
    if (first_byte < 0x80) return 1;
    if (first_byte & 0xE0 == 0xC0) return 2;
    if (first_byte & 0xF0 == 0xE0) return 3;
    if (first_byte & 0xF8 == 0xF0) return 4;
    return 1;
}

test "clip truncates at max width" {
    const allocator = std.testing.allocator;
    const result = try applyOverflow(allocator, "Hello, World!", 5, .hidden);
    defer allocator.free(result);
    try std.testing.expectEqualStrings("Hello", result);
}

test "ellipsis adds character" {
    const allocator = std.testing.allocator;
    const result = try applyOverflow(allocator, "Hello, World!", 6, .ellipsis);
    defer allocator.free(result);
    try std.testing.expectEqualStrings("Hello\xe2\x80\xa6", result);
}

test "visible returns original" {
    const allocator = std.testing.allocator;
    const result = try applyOverflow(allocator, "Hello", 3, .visible);
    try std.testing.expectEqualStrings("Hello", result);
}

test "short text unchanged with ellipsis" {
    const allocator = std.testing.allocator;
    const result = try applyOverflow(allocator, "Hi", 10, .ellipsis);
    defer allocator.free(result);
    try std.testing.expectEqualStrings("Hi", result);
}
