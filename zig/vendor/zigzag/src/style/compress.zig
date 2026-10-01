
const std = @import("std");
const Writer = std.Io.Writer;
const ansi = @import("../terminal/ansi.zig");

pub const StyleState = struct {
    bold: bool = false,
    dim: bool = false,
    italic: bool = false,
    underline: bool = false,
    blink: bool = false,
    reverse: bool = false,
    strikethrough: bool = false,
    fg_set: bool = false,
    bg_set: bool = false,
    fg_r: u8 = 0,
    fg_g: u8 = 0,
    fg_b: u8 = 0,
    bg_r: u8 = 0,
    bg_g: u8 = 0,
    bg_b: u8 = 0,
    fg_ansi: ?u8 = null,
    bg_ansi: ?u8 = null,

    pub fn reset(self: *StyleState) void {
        self.* = .{};
    }

    pub fn transitionTo(self: *StyleState, writer: *Writer, target: StyleState) !void {
        if (!target.bold and !target.dim and !target.italic and !target.underline and
            !target.blink and !target.reverse and !target.strikethrough and
            !target.fg_set and !target.bg_set)
        {
            if (self.bold or self.dim or self.italic or self.underline or
                self.blink or self.reverse or self.strikethrough or
                self.fg_set or self.bg_set)
            {
                try writer.writeAll(ansi.reset);
                self.reset();
            }
            return;
        }

        const needs_reset = (self.bold and !target.bold) or
            (self.dim and !target.dim) or
            (self.italic and !target.italic) or
            (self.underline and !target.underline) or
            (self.blink and !target.blink) or
            (self.reverse and !target.reverse) or
            (self.strikethrough and !target.strikethrough);

        if (needs_reset) {
            try writer.writeAll(ansi.reset);
            self.reset();
        }

        if (target.bold and !self.bold) try writer.print(ansi.CSI ++ "{d}m", .{ansi.SGR.bold});
        if (target.dim and !self.dim) try writer.print(ansi.CSI ++ "{d}m", .{ansi.SGR.dim});
        if (target.italic and !self.italic) try writer.print(ansi.CSI ++ "{d}m", .{ansi.SGR.italic});
        if (target.underline and !self.underline) try writer.print(ansi.CSI ++ "{d}m", .{ansi.SGR.underline});
        if (target.blink and !self.blink) try writer.print(ansi.CSI ++ "{d}m", .{ansi.SGR.blink});
        if (target.reverse and !self.reverse) try writer.print(ansi.CSI ++ "{d}m", .{ansi.SGR.reverse});
        if (target.strikethrough and !self.strikethrough) try writer.print(ansi.CSI ++ "{d}m", .{ansi.SGR.strikethrough});

        if (target.fg_set) {
            const fg_changed = !self.fg_set or
                self.fg_r != target.fg_r or self.fg_g != target.fg_g or self.fg_b != target.fg_b or
                (self.fg_ansi != target.fg_ansi);
            if (fg_changed) {
                if (target.fg_ansi) |a| {
                    try writer.print(ansi.CSI ++ "{d}m", .{a});
                } else {
                    try writer.print(ansi.CSI ++ "38;2;{d};{d};{d}m", .{ target.fg_r, target.fg_g, target.fg_b });
                }
            }
        } else if (self.fg_set) {
            try writer.print(ansi.CSI ++ "{d}m", .{ansi.SGR.fg_default});
        }

        if (target.bg_set) {
            const bg_changed = !self.bg_set or
                self.bg_r != target.bg_r or self.bg_g != target.bg_g or self.bg_b != target.bg_b or
                (self.bg_ansi != target.bg_ansi);
            if (bg_changed) {
                if (target.bg_ansi) |a| {
                    try writer.print(ansi.CSI ++ "{d}m", .{a});
                } else {
                    try writer.print(ansi.CSI ++ "48;2;{d};{d};{d}m", .{ target.bg_r, target.bg_g, target.bg_b });
                }
            }
        } else if (self.bg_set) {
            try writer.print(ansi.CSI ++ "{d}m", .{ansi.SGR.bg_default});
        }

        self.* = target;
    }
};

pub fn compressAnsi(allocator: std.mem.Allocator, input: []const u8) ![]const u8 {
    var result: Writer.Allocating = .init(allocator);
    const writer = &result.writer;

    var i: usize = 0;
    var last_was_reset = false;

    while (i < input.len) {
        const seq_len = ansi.escapeSequenceLen(input, i);
        if (seq_len == 0) {
            try writer.writeByte(input[i]);
            last_was_reset = false;
            i += 1;
            continue;
        }

        const seq = input[i..][0..seq_len];
        i += seq_len;

        if (std.mem.eql(u8, seq, ansi.reset)) {
            if (!last_was_reset) {
                try writer.writeAll(seq);
                last_was_reset = true;
            }
            continue;
        }

        try writer.writeAll(seq);
        last_was_reset = false;
    }

    return result.toOwnedSlice();
}
