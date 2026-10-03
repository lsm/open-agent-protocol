const std = @import("std");
const zz = @import("zigzag");
const tui_theme = @import("tui_theme");
const tui_text = @import("tui_text");

pub const glyph = "\u{7985}";

pub const Mood = enum { idle, thinking, tool, waiting };

const thinking_breath_ticks: f32 = 80;
const tool_breath_ticks: f32 = 32;
const dimmest: f32 = 70;
const brightest: f32 = 235;
const idle_level: u8 = 150;
const waiting_level: u8 = 210;
const farewell_rise_ticks: u64 = 6;
pub const farewell_ticks: u64 = 24;
const farewell_peak: f32 = 255;
const farewell_floor: f32 = 40;

pub fn breathStep(mood: Mood) f32 {
    return switch (mood) {
        .thinking => 1 / thinking_breath_ticks,
        .tool => 1 / tool_breath_ticks,
        .idle, .waiting => 0,
    };
}

pub fn glyphLevel(mood: Mood, phase: f32) u8 {
    return switch (mood) {
        .idle => idle_level,
        .waiting => waiting_level,
        .thinking, .tool => blk: {
            const wave = (1 - @cos(phase * std.math.tau)) / 2;
            break :blk @intFromFloat(@round(dimmest + (brightest - dimmest) * wave));
        },
    };
}

pub fn farewellLevel(since_end: u64) ?u8 {
    if (since_end >= farewell_ticks) return null;
    const start: f32 = @floatFromInt(idle_level);
    if (since_end < farewell_rise_ticks) {
        const t = @as(f32, @floatFromInt(since_end)) / @as(f32, @floatFromInt(farewell_rise_ticks));
        return @intFromFloat(@round(start + (farewell_peak - start) * t));
    }
    const t = @as(f32, @floatFromInt(since_end - farewell_rise_ticks)) / @as(f32, @floatFromInt(farewell_ticks - farewell_rise_ticks));
    return @intFromFloat(@round(farewell_peak + (farewell_floor - farewell_peak) * t));
}

pub const Counts = struct {
    thinking: usize = 0,
    tools: usize = 0,
    messages: usize = 0,
};

pub const Frame = struct {
    width: usize,
    height: usize,
    counts: Counts = .{},
    running: bool = false,
    elapsed_ms: u64 = 0,
    activity: []const u8 = "",
    final_block: []const u8 = "",
    failed: bool = false,
    mood: Mood = .idle,
    phase: f32 = 0,
    farewell: ?u8 = null,
    input: []const u8 = "",
    cursor: usize = 0,
    extra: []const u8 = "",
};

const max_column: usize = 76;
const max_art_rows: usize = 38;
const min_art_rows: usize = 6;
const enso_full_ms: f32 = 600_000;
const enso_rest: f32 = 0.93;
const enso_first: f32 = 0.03;
const ink_floor: f32 = 0.1;

pub fn ensoSweep(frame: Frame) f32 {
    if (!frame.running) return enso_rest;
    const done = @min(1, @as(f32, @floatFromInt(frame.elapsed_ms)) / enso_full_ms);
    return enso_first + (enso_rest - enso_first) * done;
}

const brush_size: usize = 128;

fn brushAt(x: f32, y: f32) f32 {
    if (x < 0 or y < 0 or x >= 1 or y >= 1) return 0;
    const fx = x * @as(f32, @floatFromInt(brush_size)) - 0.5;
    const fy = y * @as(f32, @floatFromInt(brush_size)) - 0.5;
    const x0: i32 = @intFromFloat(@floor(fx));
    const y0: i32 = @intFromFloat(@floor(fy));
    const tx = fx - @as(f32, @floatFromInt(x0));
    const ty = fy - @as(f32, @floatFromInt(y0));
    const a = brushTexel(x0, y0) * (1 - tx) + brushTexel(x0 + 1, y0) * tx;
    const b = brushTexel(x0, y0 + 1) * (1 - tx) + brushTexel(x0 + 1, y0 + 1) * tx;
    return a * (1 - ty) + b * ty;
}

fn brushTexel(x: i32, y: i32) f32 {
    if (x < 0 or y < 0 or x >= brush_size or y >= brush_size) return 0;
    const value = std.fmt.charToDigit(brush[@intCast(y)][@intCast(x)], 16) catch 0;
    return @as(f32, @floatFromInt(value)) / 15;
}

fn ensoAt(x: f32, y: f32, sweep: f32) f32 {
    const dx = x - 0.5;
    const dy = y - 0.5;
    const start = std.math.pi * 0.62;
    var u = std.math.atan2(dy, dx) - start;
    u = @mod(u, std.math.tau);
    const along = u / std.math.tau;
    if (along > sweep) return 0;
    const radius = 0.455 * (1 + 0.018 * @sin(3 * u + 1));
    const half = 0.032 * (1.25 - 0.6 * along) * (1 + 0.12 * @sin(7 * u));
    const off = @abs(@sqrt(dx * dx + dy * dy) - radius);
    const cap = std.math.clamp((sweep - along) * std.math.tau * radius / half, 0, 1);
    return std.math.clamp((half - off) / 0.012 + 0.5, 0, 1) * @max(cap, 0.35);
}

const samples = 3;

fn inkOver(x: f32, y: f32, pixel: f32, sweep: f32) f32 {
    var sum: f32 = 0;
    for (0..samples) |sy| {
        for (0..samples) |sx| {
            const ox = (@as(f32, @floatFromInt(sx)) + 0.5) / samples - 0.5;
            const oy = (@as(f32, @floatFromInt(sy)) + 0.5) / samples - 0.5;
            sum += inkAt(x + ox * pixel, y + oy * pixel, sweep);
        }
    }
    return sum / (samples * samples);
}

fn inkAt(x: f32, y: f32, sweep: f32) f32 {
    const glyph_scale: f32 = 0.72;
    const gx = (x - 0.5) / glyph_scale + 0.5;
    const gy = (y - 0.5) / glyph_scale + 0.5;
    return @max(brushAt(gx, gy), ensoAt(x, y, sweep));
}

fn brushArt(allocator: std.mem.Allocator, rows: usize, level: u8, sweep: f32) ![]const []const u8 {
    const cols = rows * 2;
    const pixels: f32 = @floatFromInt(rows * 2);
    const lines = try allocator.alloc([]const u8, rows);
    const peak: f32 = @floatFromInt(level);
    for (0..rows) |row| {
        var out: std.Io.Writer.Allocating = .init(allocator);
        const writer = &out.writer;
        var painted = false;
        for (0..cols) |col| {
            const x = (@as(f32, @floatFromInt(col)) + 0.5) / pixels;
            const top = inkOver(x, (@as(f32, @floatFromInt(row * 2)) + 0.5) / pixels, 1 / pixels, sweep);
            const bottom = inkOver(x, (@as(f32, @floatFromInt(row * 2 + 1)) + 0.5) / pixels, 1 / pixels, sweep);
            if (top < ink_floor and bottom < ink_floor) {
                if (painted) try writer.writeAll("\x1b[0m");
                painted = false;
                try writer.writeByte(' ');
                continue;
            }
            if (painted) try writer.writeAll("\x1b[0m");
            painted = true;
            const hi = @max(top, bottom);
            try inkColor(peak * hi).writeFg(writer);
            if (top >= ink_floor and bottom >= ink_floor and @abs(top - bottom) < 0.25) {
                try writer.writeAll("\u{2588}");
            } else if (top >= ink_floor and bottom >= ink_floor) {
                try inkColor(peak * @min(top, bottom)).writeBg(writer);
                try writer.writeAll(if (top > bottom) "\u{2580}" else "\u{2584}");
            } else {
                try writer.writeAll(if (top >= ink_floor) "\u{2580}" else "\u{2584}");
            }
        }
        if (painted) try writer.writeAll("\x1b[0m");
        lines[row] = try out.toOwnedSlice();
    }
    return lines;
}

fn inkColor(value: f32) zz.Color {
    const v: u8 = @intFromFloat(std.math.clamp(@round(value), 0, 255));
    return zz.Color.fromRgb(v, v, v);
}
const soft_level: u8 = 128;
const prompt = "\u{276f} ";
const placeholder = "type a prompt";

pub fn columnWidth(width: usize) usize {
    return std.math.clamp(width -| 4, 16, max_column);
}

pub fn render(allocator: std.mem.Allocator, frame: Frame) ![]u8 {
    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const width = @max(frame.width, 20);
    const column = columnWidth(width);

    var rows: std.ArrayList([]const u8) = .empty;
    if (!frame.running and frame.final_block.len > 0 and frame.farewell == null) {
        try rows.append(arena, try centered(arena, if (frame.failed) "\u{2718} stopped" else "\u{2713} done", column));
        try rows.append(arena, "");
        var lines = std.mem.splitScalar(u8, frame.final_block, '\n');
        while (lines.next()) |line| try rows.append(arena, line);
        try rows.append(arena, "");
        try rows.append(arena, try centered(arena, try trail(arena, frame.counts, column), column));
    } else {
        const level = frame.farewell orelse glyphLevel(frame.mood, frame.phase);
        const extra_rows = if (frame.extra.len > 0) std.mem.count(u8, frame.extra, "\n") + 1 else 0;
        const art_rows = @min(frame.height -| (7 + extra_rows), max_art_rows, column / 2);
        if (art_rows >= min_art_rows) {
            const art = try brushArt(arena, art_rows, level, ensoSweep(frame));
            for (art) |line| try rows.append(arena, try centered(arena, line, column));
        } else {
            try rows.append(arena, try centered(arena, try gray(arena, level, glyph), column));
        }
        try rows.append(arena, "");
        try rows.append(arena, try centered(arena, try trail(arena, frame.counts, column), column));
        const line = if (frame.running) try activityLine(arena, frame, column) else if (frame.farewell != null) "" else "zen \u{b7} only the final reply is shown";
        try rows.append(arena, try centered(arena, try gray(arena, soft_level, line), column));
    }
    try rows.append(arena, "");
    if (frame.extra.len > 0) {
        var lines = std.mem.splitScalar(u8, frame.extra, '\n');
        while (lines.next()) |line| try rows.append(arena, line);
    }
    try rows.append(arena, try inputBar(arena, frame.input, frame.cursor, column));

    const shown = if (rows.items.len > frame.height) rows.items[rows.items.len - frame.height ..] else rows.items;
    const top = (frame.height -| shown.len) / 2;
    const left = (width -| column) / 2;
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    for (0..frame.height) |index| {
        if (index > 0) try writer.writeByte('\n');
        if (index < top or index - top >= shown.len) continue;
        const row = shown[index - top];
        if (row.len == 0) continue;
        for (0..left) |_| try writer.writeByte(' ');
        try writer.writeAll(row);
    }
    return out.toOwnedSlice();
}

fn activityLine(allocator: std.mem.Allocator, frame: Frame, width: usize) ![]const u8 {
    const seconds = frame.elapsed_ms / 1000;
    const clock = try std.fmt.allocPrint(allocator, "{d}:{d:0>2}", .{ seconds / 60, seconds % 60 });
    if (frame.activity.len == 0) return clock;
    const fitted = try tui_text.truncateLineToWidth(allocator, frame.activity, width -| (clock.len + 3));
    return std.fmt.allocPrint(allocator, "{s} \u{b7} {s}", .{ clock, fitted });
}

const dot = "\u{b7}";

fn trail(allocator: std.mem.Allocator, counts: Counts, column: usize) ![]const u8 {
    const steps = counts.thinking + counts.tools + counts.messages;
    if (steps == 0) return "";
    const room = @max(column / 2, 8) - 4;
    const shown = @min(steps, room);
    var out: std.Io.Writer.Allocating = .init(allocator);
    if (steps > shown) try out.writer.print("{d} ", .{steps - shown});
    for (0..shown) |i| {
        if (i > 0) try out.writer.writeByte(' ');
        try out.writer.writeAll(dot);
    }
    return out.toOwnedSlice();
}

fn inputBar(allocator: std.mem.Allocator, input: []const u8, cursor: usize, column: usize) ![]const u8 {
    const flat = try allocator.dupe(u8, input);
    for (flat) |*byte| {
        if (byte.* == '\n' or byte.* == '\r' or byte.* == '\t') byte.* = ' ';
    }
    const at = @min(cursor, flat.len);
    const budget = column -| (tui_text.visibleWidth(prompt) + 1);
    if (flat.len == 0) {
        return std.fmt.allocPrint(allocator, "{s}\x1b[7m \x1b[0m\x1b[2m{s}\x1b[0m", .{ prompt, placeholder });
    }
    const before = try tui_text.takeTrailingWidth(allocator, flat[0..at], budget);
    const char_len = if (at < flat.len) std.unicode.utf8ByteSequenceLength(flat[at]) catch 1 else 0;
    const under = if (char_len > 0) flat[at..@min(flat.len, at + char_len)] else " ";
    const rest_budget = budget -| (tui_text.visibleWidth(before) + tui_text.visibleWidth(under));
    const after = try tui_text.truncateLineToWidth(allocator, flat[@min(flat.len, at + char_len)..], rest_budget);
    return std.fmt.allocPrint(allocator, "{s}{s}\x1b[7m{s}\x1b[0m{s}", .{ prompt, before, under, after });
}

fn centered(allocator: std.mem.Allocator, text: []const u8, width: usize) ![]const u8 {
    const pad = (width -| tui_text.visibleWidth(text)) / 2;
    const out = try allocator.alloc(u8, pad + text.len);
    @memset(out[0..pad], ' ');
    @memcpy(out[pad..], text);
    return out;
}

fn gray(allocator: std.mem.Allocator, level: u8, text: []const u8) ![]const u8 {
    var out: std.Io.Writer.Allocating = .init(allocator);
    try zz.Color.fromRgb(level, level, level).writeFg(&out.writer);
    try out.writer.writeAll(text);
    try out.writer.writeAll("\x1b[0m");
    return out.toOwnedSlice();
}

fn monochrome(allocator: std.mem.Allocator, text: []const u8) ![]u8 {
    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(allocator);
    var i: usize = 0;
    while (i < text.len) {
        if (text[i] == 0x1b and i + 1 < text.len and text[i + 1] == '[') {
            var end = i + 2;
            while (end < text.len and !(text[end] >= 0x40 and text[end] <= 0x7e)) end += 1;
            i = @min(text.len, end + 1);
            continue;
        }
        try out.append(allocator, text[i]);
        i += 1;
    }
    return out.toOwnedSlice(allocator);
}

const brush = [brush_size][]const u8{
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000005765100000000000000000000000000000000000",
    "0000000000000000000000000000000000000000000000000000000000000000000000000000000000000001fffffc6000000000000000000000000000000000",
    "0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000fffffffc30000000000000000000000000000000",
    "0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000afffffffe4000000000000000000000000000000",
    "0000000000000000000000000000000000000000039bba8641000000000000000000000000000000000110005fffffffff400000000000000000000000000000",
    "00000000000000000000000000000000000000000bffffffffc72000000000000000000000000000003e70001effffffffe20000000000000000000000000000",
    "000000000000000000000000000000000000000006fffffffffff92000000000000000000000000001df400009fffffffffc0000000000000000000000000000",
    "000000000000000000000000000000000000000000bfffffffffffe60000000000000000000000000afd000002ffffffffff5000000000000000000000000000",
    "0000000000000000000000000000000000000000001effffffffffff8000000000000000000000007ff7000000bfffffffffb000000000000000000000000000",
    "00000000000000000000000000000000000000000005fffffffffffff80000000000085000000006fff2000000cffffffffff100000000000000000000000000",
    "00000000000000000000000000000000000000000000afffffffffffff40000000004ff80000006fffc0000002fffffffffff300000000000000000000000000",
    "000000000000000000000000000000000000000000002effffffffffffd1000000001effb20008ffff60000009ffffffffffb000000000000000000000000000",
    "0000000000000000000000000000000000000000000007fffffffffffff70000000009fffe88dfffff1000002ffffffffffe2000000000000000000000000000",
    "0000000000000000000000000000000000000000000008fffffffffffffe0000000008fffffffffffb000000afffffffffe30000000000000000000000000000",
    "00000000000000000000000000000000000000000001bfffffffffffffff500000000afffffffffff6000004ffffffffff500000000000000000000000000000",
    "0000000000000000000000000000000000000000004dffffffffffffffff800000000dfffffffffff200000dfffffffff5000000000000000000000000000000",
    "000000000000000000000000000000000000000008ffffffffffffffffff600000001fffffffffffc000007fffffffff60000000000000000000000000000000",
    "0000000000000000000000000000000000000003cfffffffffffffffffd5000000001fffffffffff700003fffffffff600000000000000000000000000000000",
    "000000000000000000000000000000000000007fffffffffffffffffb500000000001fffffffffff20000cffffffff6000000000000000000000000000000000",
    "0000000000000000000000000000000000003cffffffffffffeca7410000000000000dfffffffffc00009ffffffff60000000000000000000000000000000000",
    "000000000000000000000000000000000019fffffffec9753100000000000000000009fffffffff70006ffffffff600000000000000000000000000000000000",
    "0000000000000000000000000000000005effec9641000000000000000000000000004fffffffff2003ffffffff5000000000000000000000000000000000000",
    "000000000000000000000000000000000a963000000000000000000000000000000000cfffffffc002efffffff50000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000005fffffff700cffffffe400000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000affffff30affffffe4001345677620000000000000000000000000000",
    "000000000000000000000000000000000000000000000000000000000000000000000001dffffd0afffffffbacefffffffffc500000000000000000000000000",
    "0000000000000000000000000000000000000000000000000000000000000000000000002cfff6afffffffffffffffffffffffc4000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000696cffffffffffffffffffffffffff900000000000000000000000",
    "0000000000000000000000000000000000000000000000000000000000000000000000000003dffffffffffffffffffffffffffffc1000000000000000000000",
    "000000000000000000000000000000000000000000000002688620000000000000000000006fffffffffffffffffffffffffffffffc000000000000000000000",
    "0000000000000000000000000000000000000000000001affffff80000000000000000003bffffffffffffffffffa634cffffffffff600000000000000000000",
    "000000000000000000000000000000000000000000001dffffffff700000000000000029ffffffffffffffffffa100002ffffffffffb00000000000000000000",
    "000000000000000000000000000000000000000000005ffffffffff2000000009b1039efffffffffffffffffff7000000efffffffffe00000000000000000000",
    "00000000000000000000000000000000000000000002dffffffffff900000006ffffdefffffffffffeffffffff7000000effffffffff10000000000000000000",
    "0000000000000000000000000000000000000000007efffffffffffe0000003ffffd1bffffffda7411ffffffff6000000effffffffff10000000000000000000",
    "00000000000000000000000000000000000000006dffffffffffffff300001dffffa1fffc841000001ffffffff6100001fffffffffff10000000000000000000",
    "000000000000000004500000000000000000017effffffffffffffff60000aefffffafe30000000001ffffffffffd8102ffffffffffd00000000000000000000",
    "00000000000000000bf20000000000000005bfffffffffffffffffff70007f8fffffff400000000016ffffffffffffb04ffffffffff600000000000000000000",
    "00000000000000000bf900000000000059efffffffffffffffffffff6005fe1dfffffe000000038cfffffffffffffff37fffffffffc000000000000000000000",
    "000000000000000009ff3000000015aefffffffffffffffffffffffd003ef707fffffc000028dffffffffffffffffff7bfffffffff4000000000000000000000",
    "000000000000000006ffe732358bfffffffffffffffffffffffffff702dfd000dffffc002afffffffffffffffffffffbfffffffffb0000000000000000000000",
    "000000000000000002fffffffffffffffffffffffffffffffffffff11cff6000affffd07fffffffffffffffffffffffffffffffff20000000000000000000000",
    "000000000000000000bffffffffffffffffffffffffffbfffffffff1bffc00008fffffbffffffffffffffffffffffe9cffffffff900000000000000000000000",
    "0000000000000000004fffffffffffffffffffffffffb0effffffffbfff400006ffffffffffffffffffffffffffe7107fffffffe100000000000000000000000",
    "0000000000000000000bfffffffffffffffffffffffe13ffffffffffffb000006fffffffffffffffffffffffffe2000bfffffff8000000000000000000000000",
    "00000000000000000002effffffffffffffffffffff405ffffffffffff3000005fffff8bffffffffffffffffff50003fffffffe1000000000000000000000000",
    "000000000000000000005fffffffffffffffffffff8008fffffffffffa0000004fffff4016aeffffffffffffff1000cfffffff90000000000000000000000000",
    "0000000000000000000007fffffffffffffffffffb000afffffffffff20000003fffff50000037bffffffffffe000affffffff20000000000000000000000000",
    "00000000000000000000007fffffffffffffffffd1000cffffffffff800000002fffff5000000008fffffffffd01affffffffa00000000000000000000000000",
    "000000000000000000000005effffffffffffffe20000ffffffffffe100000000fffff6000000006fffffffffc4dfffffffff300000000000000000000000000",
    "0000000000000000000000003cffffffffffffe400002ffffffffff6000000000effff700000002cfffffffffeffffffffff9000000000000000000000000000",
    "000000000000000000000000018ffffffffffe4000005fffffffffc0000000000bffff80000018effffffffffffffffffffd1000000000000000000000000000",
    "0000000000000000000000000003bfffffffd3000001dfffffffff400000000008ffff900028efffffffffffffffffffffe30000000000000000000000000000",
    "000000000000000000000000000004bfffe81000001cfffffffffa000000000004ffffb029ffffffffffffffffffffffff500000000000000000000000000000",
    "000000000000000000000000000000013200000001bfffffffffe1000000000000afffe8ffffffffffffffffffffffffe4000000000000000000000000000000",
    "00000000000000000000000000000000000000000bffffffffff5000000000000009fffffffffffffffffffffffffffb10000000000000000000000000000000",
    "0000000000000000000000000000000000000001bffffffffffa0000000000000001fffffffffffffffffffffffffb4000000000000000000000000000000000",
    "000000000000000000000000000000000000001cfffffffffff70000000000000000cfffffffffffffffffffffc7300000000000000000000000000000000000",
    "00000000000000000000000000000000000003dffffffffffff50000000000000000bffffffffffffffffffffa00000000000000000000000000000000000000",
    "0000000000000000000000000000000000017efffffffffffff400000000000000008ffffffffffffffffffffa00000000000000000000000000000000000000",
    "00000000000000000000000000000005a9aefffffffffffffff3000000000000000006cffffffecafffffffffa12345666666655310000000000000000000000",
    "0000000000000000000000000000008ffffffffffffffffffff2000000000000000000013443206dffffffffffffffffffffffffffb300000000000000000000",
    "000000000000000000000000000001fffffffffffffffffffff10000000000000000000000028effffffffffffffffffffffffffffff50000000000000000000",
    "000000000000000000000000000003fffffffffffffffffffff00000000000000000000004bffffffffffffffffffffffffffffffffff4000000000000000000",
    "000000000000000000000000000004fffffffffffffffffffff000000000000000000006dffffffffffffffffffffffffffffffffffffc000000000000000000",
    "000000000000000000000000000006fffffffffffffcbfffffe0000000000000000017efffffffffffffffffffffffffffffffffffffff200000000000000000",
    "000000000000000000000000000008ffffffffffffd1afffffe00000000000000148efffffffffffffffffffffffffffffffffffffffff500000000000000000",
    "00000000000000000000000000000afffffffffffe209fffffe000000000269bdffffffffffffffffffffffffedcddefffffffffffffff600000000000000000",
    "00000000000000000000000000000dffffffffffe300afffffd000000008fffffffffffffffffffffffffffff7000000247bffffffffff600000000000000000",
    "00000000000000000000000000001ffffffffffe3000afffffd00000004ffffffffffffffffffffc7ffffffff7000000000017effffffe200000000000000000",
    "00000000000000000000000000002fffffffffe40000bfffffd0000000afffffffffffffffffe7200efffffff700000000000029efffc3000000000000000000",
    "00000000000000000000000000001ffffffffe400000cfffffd0000000cfffffffffffffffc500000efffffff700000000000000023200000000000000000000",
    "00000000000000000000000000000effffffe3000000efffffd00000009fffffffffffffd50000000efffffff700000000000000000000000000000000000000",
    "00000000000000000000000000000bfffffd30000002ffffffd00000002fffffffffffe6000000000efffffff700000000000000000000000000000000000000",
    "000000000000000000000000000007ffffc200000005ffffffd000000006fffffffff810000000000dfffffff600000000000000000000000000000000000000",
    "000000000000000000000000000002fff9000000000affffffd0000000006ffffffa2000000000000dfffffff500000000000000000000000000000000000000",
    "0000000000000000000000000000003630000000002fffffffd000000000029bc9300000000000000cfffffff400000000000000000000000000000000000000",
    "0000000000000000000000000000000000000000005fffffffd000000000000000000000000000000cfffffff300000000000000000000000000000000000000",
    "0000000000000000000000000000000000000000006fffffffd000000000000000000000000000000cfffffff200000000000000000000000000000000000000",
    "0000000000000000000000000000000000000000006fffffffd000000000000000000000000000000bfffffff100000000000000000000000000000000000000",
    "0000000000000000000000000000000000000000005fffffffd000000000000000000000000000000affffffe000000000000000000000000000000000000000",
    "0000000000000000000000000000000000000000004fffffffe0000000000000000000000000000009ffffffd000000000000000000000000000000000000000",
    "0000000000000000000000000000000000000000003fffffffe0000000000000000000000000000009ffffffc000000000000000000000000000000000000000",
    "0000000000000000000000000000000000000000001fffffffe0000000000000000000000000000008ffffffa000000000000000000000000000000000000000",
    "0000000000000000000000000000000000000000000effffffe0000000000000000000000000000007ffffff8000000000000000000000000000000000000000",
    "0000000000000000000000000000000000000000000cfffffff0000000000000000000000000000006ffffff6000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000007fffffff0000000000000000000000000000005ffffff4000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000002fffffff0000000000000000000000000000004ffffff2000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000afffffd0000000000000000000000000000002ffffff0000000000000000000000000000000000000000",
    "000000000000000000000000000000000000000000003fffffa0000000000000000000000000000001fffffd0000000000000000000000000000000000000000",
    "000000000000000000000000000000000000000000000affff60000000000000000000000000000000fffffb0000000000000000000000000000000000000000",
    "0000000000000000000000000000000000000000000003ffff20000000000000000000000000000000effff80000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000008ffd00000000000000000000000000000000dffff50000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000001cf800000000000000000000000000000000bffff30000000000000000000000000000000000000000",
    "000000000000000000000000000000000000000000000002a200000000000000000000000000000000afffe00000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000009fffc00000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000007fff800000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000006fff500000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000004fff100000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000003ffd000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000001ff9000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000ef5000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000de1000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000760000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
};

test "the glyph breathes slowly while thinking, faster on a tool, and holds while waiting or idle" {
    try std.testing.expectEqual(@as(u8, 70), glyphLevel(.thinking, 0));
    try std.testing.expectEqual(@as(u8, 235), glyphLevel(.thinking, 0.5));
    try std.testing.expectEqual(glyphLevel(.tool, 0.25), glyphLevel(.thinking, 0.25));
    try std.testing.expect(breathStep(.tool) > breathStep(.thinking) * 2);
    try std.testing.expectEqual(@as(f32, 0), breathStep(.waiting));
    try std.testing.expectEqual(@as(f32, 0), breathStep(.idle));
    try std.testing.expectEqual(waiting_level, glyphLevel(.waiting, 0.5));
    try std.testing.expectEqual(idle_level, glyphLevel(.idle, 0.5));
}

test "the trail grows a dot per step and counts what it cannot fit" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    try std.testing.expectEqualStrings("", try trail(arena.allocator(), .{}, 40));
    try std.testing.expectEqualStrings(dot ++ " " ++ dot ++ " " ++ dot, try trail(arena.allocator(), .{ .thinking = 1, .tools = 1, .messages = 1 }, 40));
    const long = try trail(arena.allocator(), .{ .tools = 100 }, 40);
    try std.testing.expect(std.mem.startsWith(u8, long, "84 "));
    try std.testing.expectEqual(@as(usize, 16), std.mem.count(u8, long, dot));
    try std.testing.expect(tui_text.visibleWidth(long) <= 40);
}

test "the brush art is square, keeps its glyph's ink, and its ensō grows with time" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const art = try brushArt(arena.allocator(), 16, 255, enso_rest);
    try std.testing.expectEqual(@as(usize, 16), art.len);
    var ink: usize = 0;
    for (art) |line| {
        const plain = try monochrome(arena.allocator(), line);
        try std.testing.expectEqual(@as(usize, 32), tui_text.visibleWidth(plain));
        ink += std.mem.count(u8, plain, "\u{2588}") + std.mem.count(u8, plain, "\u{2580}") + std.mem.count(u8, plain, "\u{2584}");
    }
    try std.testing.expect(ink > 80);
    try std.testing.expect(brushAt(0.5, 0.5) >= 0 and brushAt(-0.1, 0.5) == 0);

    try std.testing.expect(ensoAt(0.5 + 0.455 * @cos(std.math.pi * 0.62 + 0.1), 0.5 + 0.455 * @sin(std.math.pi * 0.62 + 0.1), enso_rest) > 0.5);
    try std.testing.expectEqual(@as(f32, 0), ensoAt(0.5 + 0.455 * @cos(std.math.pi * 0.62 - 0.1), 0.5 + 0.455 * @sin(std.math.pi * 0.62 - 0.1), enso_rest));
    try std.testing.expectEqual(@as(f32, 0), ensoAt(0.5 + 0.455 * @cos(std.math.pi * 0.62 + 3), 0.5 + 0.455 * @sin(std.math.pi * 0.62 + 3), 0.1));
    try std.testing.expect(ensoSweep(.{ .width = 80, .height = 40, .running = true }) < ensoSweep(.{ .width = 80, .height = 40, .running = true, .elapsed_ms = 300_000 }));
    try std.testing.expectEqual(enso_rest, ensoSweep(.{ .width = 80, .height = 40, .running = true, .elapsed_ms = 900_000 }));
    try std.testing.expectEqual(enso_rest, ensoSweep(.{ .width = 80, .height = 40 }));
}

test "the farewell rises to full brightness, fades out, then ends" {
    try std.testing.expectEqual(idle_level, farewellLevel(0).?);
    try std.testing.expectEqual(@as(u8, 255), farewellLevel(farewell_rise_ticks).?);
    try std.testing.expect(farewellLevel(farewell_ticks - 1).? < 60);
    try std.testing.expect(farewellLevel(farewell_ticks) == null);
}

test "a frame in its farewell shows the glyph, not yet the reply" {
    const text = try render(std.testing.allocator, .{ .width = 60, .height = 20, .final_block = "the final reply", .farewell = 255 });
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "\x1b[38;2;255;255;255m\u{2588}") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "the final reply") == null);
}

fn plainRows(allocator: std.mem.Allocator, text: []const u8) ![]u8 {
    return monochrome(allocator, text);
}

test "a running frame fills the screen, centres its column and ends in the input bar" {
    const text = try render(std.testing.allocator, .{ .width = 100, .height = 30, .running = true, .elapsed_ms = 75_000, .counts = .{ .thinking = 3, .tools = 1 }, .activity = "Shell Execute  go test ./...", .mood = .tool, .phase = 0.5, .input = "hello", .cursor = 5 });
    defer std.testing.allocator.free(text);
    const plain = try plainRows(std.testing.allocator, text);
    defer std.testing.allocator.free(plain);
    var lines = std.mem.splitScalar(u8, plain, '\n');
    var count: usize = 0;
    var glyph_row: ?usize = null;
    var input_row: ?usize = null;
    while (lines.next()) |line| : (count += 1) {
        try std.testing.expect(tui_text.visibleWidth(line) <= 100);
        if (glyph_row == null and std.mem.indexOf(u8, line, "\u{2588}") != null) glyph_row = count;
        if (std.mem.indexOf(u8, line, prompt ++ "hello") != null) {
            input_row = count;
            try std.testing.expectEqual(@as(usize, 12), std.mem.indexOf(u8, line, prompt).?);
        }
    }
    try std.testing.expectEqual(@as(usize, 30), count);
    try std.testing.expect(glyph_row.? < 10);
    try std.testing.expectEqual(@as(usize, 28), input_row.?);
    try std.testing.expect(std.mem.indexOf(u8, plain, " " ++ dot ++ " " ++ dot ++ " " ++ dot ++ " " ++ dot ++ "\n") != null);
    try std.testing.expect(std.mem.indexOf(u8, plain, dot ++ " " ++ dot ++ " " ++ dot ++ " " ++ dot ++ " " ++ dot) == null);
    try std.testing.expect(std.mem.indexOf(u8, plain, "1:15 \u{b7} Shell Execute  go test") != null);
}

test "zen's own rows draw no colour but grey" {
    const text = try render(std.testing.allocator, .{ .width = 80, .height = 24, .running = true, .mood = .thinking, .phase = 0.3, .counts = .{ .tools = 2 }, .activity = "Read  a.zig" });
    defer std.testing.allocator.free(text);
    var i: usize = 0;
    while (std.mem.indexOfPos(u8, text, i, "\x1b[38;2;")) |at| {
        const rest = text[at + 7 ..];
        const end = std.mem.indexOfScalar(u8, rest, 'm').?;
        var parts = std.mem.splitScalar(u8, rest[0..end], ';');
        const r = parts.next().?;
        try std.testing.expectEqualStrings(r, parts.next().?);
        try std.testing.expectEqualStrings(r, parts.next().?);
        i = at + 1;
    }
}

test "a stopped frame shows the final reply with its markdown colours, and no glyph" {
    const text = try render(std.testing.allocator, .{ .width = 60, .height = 20, .final_block = "\x1b[36mthe final reply\x1b[0m", .counts = .{ .messages = 1 } });
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "\x1b[36mthe final reply") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "done") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, glyph) == null);

    const failed = try render(std.testing.allocator, .{ .width = 60, .height = 20, .final_block = "boom", .failed = true });
    defer std.testing.allocator.free(failed);
    try std.testing.expect(std.mem.indexOf(u8, failed, "stopped") != null);
}

test "a long final reply keeps the input bar on screen" {
    const reply = "line\n" ** 40;
    const text = try render(std.testing.allocator, .{ .width = 60, .height = 12, .final_block = reply, .input = "next", .cursor = 4 });
    defer std.testing.allocator.free(text);
    try std.testing.expectEqual(@as(usize, 12), std.mem.count(u8, text, "\n") + 1);
    try std.testing.expect(std.mem.indexOf(u8, text, prompt ++ "next") != null);
}

test "the input bar keeps the cursor in view and shows a placeholder when empty" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const empty = try inputBar(arena.allocator(), "", 0, 40);
    try std.testing.expect(std.mem.indexOf(u8, empty, placeholder) != null);
    const long = "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnop";
    const bar = try inputBar(arena.allocator(), long, long.len, 20);
    const plain = try monochrome(arena.allocator(), bar);
    try std.testing.expect(tui_text.visibleWidth(plain) <= 20);
    try std.testing.expect(std.mem.endsWith(u8, std.mem.trimEnd(u8, plain, " "), "mnop"));
}
