
const std = @import("std");
const Writer = std.Io.Writer;
const ansi = @import("ansi.zig");
const measure = @import("../layout/measure.zig");

pub const Mode = enum {
    full,
    diff,
};

pub const Size = struct {
    width: u16,
    height: u16,
};

pub const Renderer = struct {
    mode: Mode,
    previous: std.array_list.Managed(u8),
    last_line_count: usize = 0,
    last_hash: u64 = 0,
    dirty: bool = true,

    pub fn init(allocator: std.mem.Allocator, mode: Mode) Renderer {
        return .{
            .mode = mode,
            .previous = std.array_list.Managed(u8).init(allocator),
        };
    }

    pub fn deinit(self: *Renderer) void {
        self.previous.deinit();
    }

    pub fn invalidate(self: *Renderer) void {
        self.dirty = true;
    }

    pub fn needsRepaint(self: *const Renderer) bool {
        return self.dirty;
    }

    pub fn render(self: *Renderer, writer: *Writer, view: []const u8, size: Size) !bool {
        const hash = std.hash.Wyhash.hash(0, view);
        if (!self.dirty and hash == self.last_hash) return false;

        const shape: Shape = if (self.mode == .diff)
            scan(view, size)
        else
            .{ .addressable = true, .styles_closed = true };

        const use_diff = self.mode == .diff and
            !self.dirty and
            self.previous.items.len > 0 and
            shape.addressable and
            shape.styles_closed;

        try writer.writeAll(ansi.sync_start);
        const line_count = if (use_diff)
            try self.writeDiff(writer, view, size)
        else
            try self.writeFull(writer, view, size);
        try writer.writeAll(ansi.sync_end);

        self.remember(view, line_count, hash);

        if (!shape.addressable or !shape.styles_closed) self.dirty = true;

        return true;
    }

    fn remember(self: *Renderer, view: []const u8, line_count: usize, hash: u64) void {
        self.last_line_count = line_count;
        self.last_hash = hash;

        self.previous.clearRetainingCapacity();
        self.previous.appendSlice(view) catch {
            self.previous.clearRetainingCapacity();
            self.dirty = true;
            return;
        };
        self.dirty = false;
    }

    fn writeFull(self: *Renderer, writer: *Writer, view: []const u8, size: Size) !usize {
        const max_row = addressableRows(size);

        var lines = std.mem.splitScalar(u8, view, '\n');
        var line_count: usize = 0;
        while (lines.next()) |line| {
            defer line_count += 1;
            if (line_count >= max_row) continue;

            try ansi.cursorTo0(writer, @intCast(line_count), 0);
            try writer.writeAll(line);
            if (!fillsRow(line, size)) try writer.writeAll(ansi.line_clear_right);
        }

        var row = line_count;
        while (row < self.last_line_count and row < max_row) : (row += 1) {
            try ansi.cursorTo0(writer, @intCast(row), 0);
            try writer.writeAll(ansi.line_clear);
        }

        return line_count;
    }

    fn writeDiff(self: *Renderer, writer: *Writer, view: []const u8, size: Size) !usize {
        var new_lines = std.mem.splitScalar(u8, view, '\n');
        var old_lines = std.mem.splitScalar(u8, self.previous.items, '\n');

        var line_count: usize = 0;
        var last_line: []const u8 = "";
        while (new_lines.next()) |line| {
            const row: u16 = @intCast(line_count);
            line_count += 1;
            last_line = line;

            if (old_lines.next()) |old_line| {
                if (std.mem.eql(u8, old_line, line)) continue;
            }

            try ansi.cursorTo0(writer, row, 0);
            try writer.writeAll(line);
            if (!fillsRow(line, size)) try writer.writeAll(ansi.line_clear_right);
        }

        var row = line_count;
        while (row < self.last_line_count and row < addressableRows(size)) : (row += 1) {
            try ansi.cursorTo0(writer, @intCast(row), 0);
            try writer.writeAll(ansi.line_clear);
        }

        if (row == line_count) {
            const col = @min(measure.width(last_line), size.width);
            try ansi.cursorTo0(writer, @intCast(line_count -| 1), @intCast(col));
        }

        return line_count;
    }
};

fn addressableRows(size: Size) usize {
    return if (size.height > 0) size.height else std.math.maxInt(u16);
}

fn fillsRow(line: []const u8, size: Size) bool {
    if (size.width == 0) return false;

    const w = measure.width(line);
    return w > 0 and w % size.width == 0;
}

const Shape = struct {
    addressable: bool,
    styles_closed: bool,
};

fn scan(view: []const u8, size: Size) Shape {
    var addressable = size.width > 0 and size.height > 0;
    var styles_closed = true;

    var lines = std.mem.splitScalar(u8, view, '\n');
    var count: usize = 0;
    while (lines.next()) |line| {
        count += 1;
        if (addressable and (count > size.height or measure.width(line) > size.width)) {
            addressable = false;
        }
        if (styles_closed and leavesStyleOpen(line)) styles_closed = false;
        if (!addressable and !styles_closed) break;
    }

    return .{ .addressable = addressable, .styles_closed = styles_closed };
}

const Attr = struct {
    const fg: u16 = 1 << 0;
    const bg: u16 = 1 << 1;
    const underline_color: u16 = 1 << 2;
    const bold_dim: u16 = 1 << 3;
    const italic: u16 = 1 << 4;
    const underline: u16 = 1 << 5;
    const blink: u16 = 1 << 6;
    const reverse: u16 = 1 << 7;
    const hidden: u16 = 1 << 8;
    const strike: u16 = 1 << 9;
    const other: u16 = 1 << 10;
};

fn leavesStyleOpen(line: []const u8) bool {
    var active: u16 = 0;
    var link_open = false;

    var i: usize = 0;
    while (i < line.len) {
        if (line[i] != 0x1b or i + 1 >= line.len) {
            i += 1;
            continue;
        }

        switch (line[i + 1]) {
            '[' => {
                const params_start = i + 2;
                var end = params_start;
                while (end < line.len and line[end] >= 0x20 and line[end] <= 0x3f) : (end += 1) {}
                if (end >= line.len) break;
                if (line[end] == 'm') active = applySgr(line[params_start..end], active);
                i = end + 1;
            },
            ']' => {
                const payload_start = i + 2;
                var end = payload_start;
                while (end < line.len and line[end] != 0x07 and line[end] != 0x1b) : (end += 1) {}

                const payload = line[payload_start..end];
                if (std.mem.startsWith(u8, payload, "8;")) {
                    const uri_start = (std.mem.indexOfScalarPos(u8, payload, 2, ';') orelse
                        payload.len -| 1) + 1;
                    link_open = uri_start < payload.len;
                }

                i = if (end >= line.len)
                    line.len
                else if (line[end] == 0x07)
                    end + 1
                else
                    end + 2;
            },
            else => i += 2,
        }
    }

    return active != 0 or link_open;
}

fn applySgr(params: []const u8, current: u16) u16 {
    if (params.len == 0) return 0;

    var state = current;
    var it = std.mem.splitScalar(u8, params, ';');
    while (it.next()) |param| {
        const colon = std.mem.indexOfScalar(u8, param, ':');
        const head = if (colon) |c| param[0..c] else param;
        const code = std.fmt.parseInt(u16, head, 10) catch {
            state |= Attr.other;
            continue;
        };

        switch (code) {
            0 => state = 0,
            1, 2 => state |= Attr.bold_dim,
            3 => state |= Attr.italic,
            4, 21 => state |= Attr.underline,
            5, 6 => state |= Attr.blink,
            7 => state |= Attr.reverse,
            8 => state |= Attr.hidden,
            9 => state |= Attr.strike,
            22 => state &= ~Attr.bold_dim,
            23 => state &= ~Attr.italic,
            24 => state &= ~Attr.underline,
            25 => state &= ~Attr.blink,
            27 => state &= ~Attr.reverse,
            28 => state &= ~Attr.hidden,
            29 => state &= ~Attr.strike,
            30...37, 90...97 => state |= Attr.fg,
            39 => state &= ~Attr.fg,
            40...47, 100...107 => state |= Attr.bg,
            49 => state &= ~Attr.bg,
            59 => state &= ~Attr.underline_color,
            38, 48, 58 => {
                state |= switch (code) {
                    38 => Attr.fg,
                    48 => Attr.bg,
                    else => Attr.underline_color,
                };
                if (colon != null) continue;
                const kind = it.next() orelse break;
                const arg_count: usize = if (std.mem.eql(u8, kind, "2"))
                    3
                else if (std.mem.eql(u8, kind, "5"))
                    1
                else
                    0;
                for (0..arg_count) |_| {
                    _ = it.next() orelse break;
                }
            },
            else => state |= Attr.other,
        }
    }

    return state;
}

test "leavesStyleOpen: plain text closes nothing" {
    try std.testing.expect(!leavesStyleOpen(""));
    try std.testing.expect(!leavesStyleOpen("just text"));
}

test "leavesStyleOpen: a reset closes the line" {
    try std.testing.expect(!leavesStyleOpen("\x1b[31mred\x1b[0m"));
    try std.testing.expect(!leavesStyleOpen("\x1b[1;4;31mfancy\x1b[m tail"));
    try std.testing.expect(!leavesStyleOpen("\x1b[38;2;255;0;0mred\x1b[0m"));
}

test "leavesStyleOpen: an unclosed attribute leaves the line open" {
    try std.testing.expect(leavesStyleOpen("\x1b[31mred"));
    try std.testing.expect(leavesStyleOpen("\x1b[41mbackground"));
    try std.testing.expect(leavesStyleOpen("\x1b[1mbold\x1b[0m\x1b[4munderline"));
}

test "leavesStyleOpen: extended colour arguments are not read as codes" {
    try std.testing.expect(leavesStyleOpen("\x1b[38;2;255;0;0mred"));
    try std.testing.expect(leavesStyleOpen("\x1b[48;5;0mblack background"));
    try std.testing.expect(leavesStyleOpen("\x1b[38:2::255:0:0mcolon form"));
}

test "leavesStyleOpen: attributes turned off individually" {
    try std.testing.expect(!leavesStyleOpen("\x1b[1mbold\x1b[22m"));
    try std.testing.expect(!leavesStyleOpen("\x1b[31mred\x1b[39m"));
    try std.testing.expect(!leavesStyleOpen("\x1b[41mbg\x1b[49m"));
    try std.testing.expect(leavesStyleOpen("\x1b[1;31mboth\x1b[22m"));
}

test "leavesStyleOpen: non-SGR sequences are ignored" {
    try std.testing.expect(!leavesStyleOpen("\x1b[2Ktext"));
    try std.testing.expect(!leavesStyleOpen("\x1b[10;5Htext"));
}

test "leavesStyleOpen: hyperlinks" {
    try std.testing.expect(leavesStyleOpen("\x1b]8;;https://example.com\x07link text"));
    try std.testing.expect(!leavesStyleOpen("\x1b]8;;https://example.com\x07link\x1b]8;;\x07"));
    try std.testing.expect(!leavesStyleOpen("\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\"));
}

test "fillsRow: a line that ends on the row edge" {
    const size = Size{ .width = 5, .height = 4 };

    try std.testing.expect(!fillsRow("", size));
    try std.testing.expect(!fillsRow("abc", size));
    try std.testing.expect(fillsRow("abcde", size));

    try std.testing.expect(!fillsRow("abcdefgh", size));
    try std.testing.expect(fillsRow("abcdefghij", size));

    try std.testing.expect(fillsRow("\x1b[31mabcde\x1b[0m", size));
    try std.testing.expect(!fillsRow("日本", size));
    try std.testing.expect(fillsRow("日本語だよ", size));

    try std.testing.expect(!fillsRow("abcde", .{ .width = 0, .height = 0 }));
}

test "addressableRows: an unknown height falls back to the sequence limit" {
    try std.testing.expectEqual(@as(usize, 24), addressableRows(.{ .width = 80, .height = 24 }));
    try std.testing.expectEqual(
        @as(usize, std.math.maxInt(u16)),
        addressableRows(.{ .width = 80, .height = 0 }),
    );
}

test "scan: frame geometry" {
    const size = Size{ .width = 10, .height = 3 };

    try std.testing.expect(scan("abc\ndef", size).addressable);
    try std.testing.expect(!scan("abc\ndef\nghi\njkl", size).addressable);
    try std.testing.expect(!scan("this line is too wide", size).addressable);
    try std.testing.expect(scan("\x1b[31mabc\x1b[0m", size).addressable);
    try std.testing.expect(scan("日本語だ", size).addressable);
    try std.testing.expect(!scan("日本語だよね", size).addressable);
}
