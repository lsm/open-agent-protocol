
const std = @import("std");
const Writer = std.Io.Writer;
const measure = @import("measure.zig");
const unicode = @import("../unicode.zig");

pub const Layer = struct {
    content: []const u8,
    x: u16 = 0,
    y: u16 = 0,
    z: i16 = 0,
    transparent: bool = true,
};

pub const LayerStack = struct {
    allocator: std.mem.Allocator,
    layers: std.array_list.Managed(Layer),
    width: u16 = 80,
    height: u16 = 24,
    background: u8 = ' ',

    pub fn init(allocator: std.mem.Allocator) LayerStack {
        return .{
            .allocator = allocator,
            .layers = std.array_list.Managed(Layer).init(allocator),
        };
    }

    pub fn deinit(self: *LayerStack) void {
        self.layers.deinit();
    }

    pub fn setSize(self: *LayerStack, w: u16, h: u16) void {
        self.width = w;
        self.height = h;
    }

    pub fn push(self: *LayerStack, layer: Layer) !void {
        try self.layers.append(layer);
    }

    pub fn clear(self: *LayerStack) void {
        self.layers.clearRetainingCapacity();
    }

    pub fn render(self: *const LayerStack, allocator: std.mem.Allocator) ![]const u8 {
        const w: usize = self.width;
        const h: usize = self.height;

        const bg = [1]u8{self.background};
        const background: Cell = .{ .content = &bg, .ansi_prefix = "" };

        const sorted = try allocator.alloc(Layer, self.layers.items.len);
        @memcpy(sorted, self.layers.items);
        std.mem.sort(Layer, sorted, {}, struct {
            fn lessThan(_: void, a: Layer, b: Layer) bool {
                return a.z < b.z;
            }
        }.lessThan);

        const cells = w * h;
        const planes = try allocator.alloc(?Cell, sorted.len * cells);
        @memset(planes, null);
        for (sorted, 0..) |layer, index| {
            try paintLayer(allocator, planes[index * cells ..][0..cells], w, h, layer);
        }

        const grid = try allocator.alloc(Cell, cells);
        @memset(grid, background);
        const covered = try allocator.alloc(bool, cells);
        @memset(covered, false);
        var index = sorted.len;
        while (index > 0) {
            index -= 1;
            const plane = planes[index * cells ..][0..cells];
            for (0..h) |row| {
                for (0..w) |col| {
                    const at = row * w + col;
                    const cell = plane[at] orelse continue;
                    if (cell.content.len == 0) continue;
                    const wide = col + 1 < w and plane[at + 1] != null and plane[at + 1].?.content.len == 0;
                    if (covered[at] or (wide and covered[at + 1])) continue;
                    grid[at] = cell;
                    covered[at] = true;
                    if (wide) {
                        grid[at + 1] = .{ .content = "" };
                        covered[at + 1] = true;
                    }
                }
            }
        }

        var result: Writer.Allocating = .init(allocator);
        const writer = &result.writer;

        for (0..h) |row| {
            if (row > 0) try writer.writeByte('\n');
            for (0..w) |col| {
                const cell = grid[row * w + col];
                if (cell.content.len == 0) continue;
                if (cell.ansi_prefix.len > 0 or std.mem.indexOfScalar(u8, cell.content, 0x1b) != null) {
                    try writer.writeAll(cell.ansi_prefix);
                    try writer.writeAll(cell.content);
                    try writer.writeAll("\x1b[0m");
                } else {
                    try writer.writeAll(cell.content);
                }
            }
        }

        return result.toArrayList().items;
    }

    fn paintLayer(allocator: std.mem.Allocator, plane: []?Cell, w: usize, h: usize, layer: Layer) !void {
        const content = layer.content;
        var row: usize = layer.y;
        var col: usize = layer.x;
        var i: usize = 0;
        var current_ansi: []const u8 = "";
        var last_cell: ?usize = null;
        var last_start: usize = 0;
        var last_end: usize = 0;
        var last_sliced = true;
        var last_style: []const u8 = "";

        while (i < content.len and row < h) {
            if (content[i] == '\n') {
                row += 1;
                col = layer.x;
                i += 1;
                last_cell = null;
                continue;
            }

            if (content[i] == 0x1b and i + 1 < content.len and content[i + 1] == '[') {
                const seq_start = i;
                i += 2;
                while (i < content.len and content[i] != 'm' and content[i] != 'H' and content[i] != 'J' and content[i] != 'K') : (i += 1) {}
                if (i < content.len) {
                    i += 1;
                    if (content[seq_start + 2 .. i - 1].len == 1 and content[seq_start + 2] == '0') {
                        current_ansi = "";
                    } else {
                        current_ansi = content[seq_start..i];
                    }
                }
                continue;
            }

            const start = i;
            const char_len = std.unicode.utf8ByteSequenceLength(content[i]) catch 1;
            const end = @min(i + char_len, content.len);
            const char = content[i..end];
            const codepoint: u21 = std.unicode.utf8Decode(char) catch content[i];
            const char_width = measure.charWidth(codepoint);
            i = end;

            if (char_width == 0) {
                if (attachesToPrevious(codepoint)) {
                    if (last_cell) |at| {
                        const restyle = !std.mem.eql(u8, current_ansi, last_style);
                        if (last_sliced and last_end == start and !restyle) {
                            plane[at].?.content = content[last_start..end];
                        } else if (restyle) {
                            plane[at].?.content = try std.mem.concat(allocator, u8, &.{ plane[at].?.content, "\x1b[0m", current_ansi, char });
                            last_style = current_ansi;
                            last_sliced = false;
                        } else {
                            plane[at].?.content = try std.mem.concat(allocator, u8, &.{ plane[at].?.content, char });
                            last_sliced = false;
                        }
                        last_end = end;
                    }
                }
                continue;
            }

            const is_transparent = layer.transparent and codepoint == ' ' and current_ansi.len == 0;
            if (!is_transparent and col + char_width <= w) {
                const at = row * w + col;
                plane[at] = .{
                    .content = char,
                    .ansi_prefix = current_ansi,
                };
                last_cell = at;
                last_start = start;
                last_end = end;
                last_sliced = true;
                last_style = current_ansi;
                if (char_width == 2) {
                    plane[at + 1] = .{ .content = "" };
                }
            } else {
                last_cell = null;
            }
            col += char_width;
        }
    }
};

fn attachesToPrevious(codepoint: u21) bool {
    if (unicode.codepointWidth(codepoint) != 0) return false;
    return switch (codepoint) {
        0x200D, 0xFE0E, 0xFE0F, 0x20E3 => false,
        else => true,
    };
}

const Cell = struct {
    content: []const u8 = " ",
    ansi_prefix: []const u8 = "",
};
