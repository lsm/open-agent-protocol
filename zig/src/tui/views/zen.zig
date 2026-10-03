const std = @import("std");
const zz = @import("zigzag");
const tui_theme = @import("tui_theme");
const tui_text = @import("tui_text");

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

pub fn pulseLevel(mood: Mood, phase: f32) u8 {
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
    activity: []const u8 = "",
    incoming: []const u8 = "",
    rise: f32 = 0,
    light: f32 = 0,
    tool_ms: u64 = 0,
    final_block: []const u8 = "",
    failed: bool = false,
    mood: Mood = .idle,
    phase: f32 = 0,
    farewell: ?u8 = null,
    input: []const u8 = "",
    cursor: usize = 0,
    secret: bool = false,
    placeholder: []const u8 = placeholder,
    extra: []const u8 = "",
    scroll: usize = 0,
};

const max_column: usize = 76;
const max_reading: usize = 120;
const soft_level: u8 = 128;
const prompt = "\u{276f} ";
const placeholder = "type a prompt";

pub fn columnWidth(width: usize) usize {
    return std.math.clamp(width -| 4, 16, max_column);
}

pub fn readingWidth(width: usize) usize {
    return std.math.clamp(width -| 8, 16, max_reading);
}

const min_gap: usize = 2;

fn bottomMargin(height: usize) usize {
    return std.math.clamp(height / 6, 1, 6);
}

const Layout = struct {
    content: []const []const u8,
    lower: []const []const u8,
    lower_top: usize,
    area: usize,
    final: bool,
    column: usize,
    left: usize,
    lower_left: usize,
};

pub fn showsReply(frame: Frame) bool {
    return !frame.running and frame.final_block.len > 0 and frame.farewell == null;
}

fn layout(arena: std.mem.Allocator, frame: Frame) !Layout {
    const width = @max(frame.width, 20);
    const bar = columnWidth(width);
    const final = showsReply(frame);
    const column = if (final) readingWidth(width) else bar;

    var content: std.ArrayList([]const u8) = .empty;
    if (final) {
        try content.append(arena, try centered(arena, if (frame.failed) "\u{2718} stopped" else "\u{2713} done", column));
        try content.append(arena, "");
        var lines = std.mem.splitScalar(u8, frame.final_block, '\n');
        while (lines.next()) |line| try content.append(arena, line);
        try content.append(arena, "");
        try content.append(arena, try centered(arena, try gray(arena, soft_level, try trail(arena, frame.counts, column)), column));
    } else {
        const level = frame.farewell orelse pulseLevel(frame.mood, frame.phase);
        const marks = try trail(arena, frame.counts, column);
        try content.append(arena, try centered(arena, try gray(arena, level, if (marks.len == 0) dot else marks), column));
        if (frame.running) {
            const slot = try activitySlot(arena, frame, column);
            for (slot) |row| try content.append(arena, row);
        } else {
            var rest: [slot_rows][]const u8 = .{""} ** slot_rows;
            if (frame.farewell == null) rest[slot_centre] = try centered(arena, try gray(arena, soft_level, "zen \u{b7} only the final reply is shown"), column);
            for (rest) |row| try content.append(arena, row);
        }
    }

    var lower: std.ArrayList([]const u8) = .empty;
    if (frame.extra.len > 0) {
        var lines = std.mem.splitScalar(u8, frame.extra, '\n');
        while (lines.next()) |line| try lower.append(arena, line);
        try lower.append(arena, "");
    }
    try lower.append(arena, if (frame.secret) try secretBar(arena, frame.input, frame.placeholder, bar) else try inputBar(arena, frame.input, frame.cursor, bar, frame.placeholder));

    const lower_shown = if (lower.items.len > frame.height) lower.items[lower.items.len - frame.height ..] else lower.items;
    const lower_top = frame.height -| (bottomMargin(frame.height) + lower_shown.len);
    return .{
        .content = content.items,
        .lower = lower_shown,
        .lower_top = lower_top,
        .area = lower_top -| min_gap,
        .final = final,
        .column = column,
        .left = (width -| column) / 2,
        .lower_left = (width -| bar) / 2,
    };
}

const scroll_markers: usize = 2;

fn readingRoom(area: usize) usize {
    return @max(area -| scroll_markers, 1);
}

pub fn maxScroll(allocator: std.mem.Allocator, frame: Frame) !usize {
    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    const laid = try layout(arena_state.allocator(), frame);
    if (!laid.final or laid.content.len <= laid.area) return 0;
    return laid.content.len - readingRoom(laid.area);
}

fn scrolledWindow(arena: std.mem.Allocator, laid: Layout, scroll: usize) ![]const []const u8 {
    const room = readingRoom(laid.area);
    const offset = @min(scroll, laid.content.len - room);
    const end = laid.content.len - offset;
    const start = end - room;
    var rows: std.ArrayList([]const u8) = .empty;
    try rows.append(arena, if (start > 0) try centered(arena, try gray(arena, soft_level, try std.fmt.allocPrint(arena, "\u{2191} {d} more \u{b7} PgUp", .{start})), laid.column) else "");
    try rows.appendSlice(arena, laid.content[start..end]);
    try rows.append(arena, if (offset > 0) try centered(arena, try gray(arena, soft_level, try std.fmt.allocPrint(arena, "\u{2193} {d} more \u{b7} PgDn", .{offset})), laid.column) else "");
    return rows.items;
}

pub fn render(allocator: std.mem.Allocator, frame: Frame) ![]u8 {
    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const laid = try layout(arena, frame);
    const shown = if (laid.content.len <= laid.area)
        laid.content
    else if (laid.final)
        try scrolledWindow(arena, laid, frame.scroll)
    else
        laid.content[laid.content.len - laid.area ..];
    const content_top = (laid.area -| shown.len) / 2;

    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    for (0..frame.height) |index| {
        if (index > 0) try writer.writeByte('\n');
        const in_lower = index >= laid.lower_top;
        const row: []const u8 = if (in_lower)
            (if (index - laid.lower_top < laid.lower.len) laid.lower[index - laid.lower_top] else "")
        else if (index >= content_top and index - content_top < shown.len)
            shown[index - content_top]
        else
            "";
        if (row.len == 0) continue;
        for (0..if (in_lower) laid.lower_left else laid.left) |_| try writer.writeByte(' ');
        try writer.writeAll(row);
    }
    return out.toOwnedSlice();
}

pub const change_ticks: u64 = 60;
pub const light_ticks: u64 = 80;
pub const slot_rows: usize = 3;
const slot_centre: usize = 1;
const glow_gain: f32 = 0.8;
const lit_peak: f32 = 230;
const timed_after_ms: u64 = 10_000;
const faded_level: f32 = 27;
const glow_band: f32 = 0.15;

fn activityLine(allocator: std.mem.Allocator, text: []const u8, tool_ms: u64, width: usize) ![]const u8 {
    if (tool_ms < timed_after_ms) return tui_text.truncateLineToWidth(allocator, text, width);
    const seconds = tool_ms / 1000;
    const clock = try std.fmt.allocPrint(allocator, "{d}:{d:0>2}", .{ seconds / 60, seconds % 60 });
    if (text.len == 0) return clock;
    const fitted = try tui_text.truncateLineToWidth(allocator, text, width -| (clock.len + 3));
    return std.fmt.allocPrint(allocator, "{s} \u{b7} {s}", .{ clock, fitted });
}

fn smoothstep(x: f32) f32 {
    const t = std.math.clamp(x, 0, 1);
    return t * t * (3 - 2 * t);
}

fn litLevel(strength: f32) u8 {
    const soft: f32 = @floatFromInt(soft_level);
    const level = if (strength <= 1) faded_level + (soft - faded_level) * @max(strength, 0) else soft + (lit_peak - soft) * @min(strength - 1, 1);
    return @intFromFloat(@round(level));
}

fn glow(progress: f32, col: usize, text_width: usize) f32 {
    const mid = @as(f32, @floatFromInt(text_width -| 1)) / 2;
    const reach = @abs(@as(f32, @floatFromInt(col)) - mid) / @max(mid, 1);
    const peak = glow_band + reach * (1 - 2 * glow_band);
    return @max(0, 1 - @abs(progress - peak) / glow_band);
}

const cell_share: f32 = 0.35;
const covered: []const u8 = "";

fn layCells(allocator: std.mem.Allocator, text: []const u8, width: usize) ![]?[]const u8 {
    const cells = try allocator.alloc(?[]const u8, width);
    @memset(cells, null);
    var col = (width -| tui_text.visibleWidth(text)) / 2;
    var view = std.unicode.Utf8View.initUnchecked(text).iterator();
    while (view.nextCodepointSlice()) |glyph| {
        const cell_width = tui_text.visibleWidth(glyph);
        if (col + cell_width > width) break;
        if (cell_width > 0) cells[col] = glyph;
        for (1..cell_width) |extra| cells[col + extra] = covered;
        col += cell_width;
    }
    return cells;
}

const CellFade = struct {
    arrived: bool,
    strength: f32,
};

fn cellFade(rise: f32, col: usize, lo: usize, hi: usize) CellFade {
    const mid = @as(f32, @floatFromInt(lo + hi -| 1)) / 2;
    const half = @max(@as(f32, @floatFromInt(hi -| lo)) / 2, 1);
    const delay = @abs(@as(f32, @floatFromInt(col)) - mid) / half * (1 - cell_share);
    const t = std.math.clamp((rise - delay) / cell_share, 0, 1);
    if (t < 0.5) return .{ .arrived = false, .strength = 1 - smoothstep(t * 2) };
    return .{ .arrived = true, .strength = smoothstep(t * 2 - 1) };
}

fn activityRow(allocator: std.mem.Allocator, frame: Frame, width: usize) ![]const u8 {
    const changing = frame.incoming.len > 0;
    const shown = if (changing) try tui_text.truncateLineToWidth(allocator, frame.incoming, width) else try activityLine(allocator, frame.activity, frame.tool_ms, width);
    const fresh = try layCells(allocator, shown, width);
    const old = try layCells(allocator, if (changing) try tui_text.truncateLineToWidth(allocator, frame.activity, width) else "", width);
    var lo: usize = width;
    var hi: usize = 0;
    for (0..width) |col| if (fresh[col] != null or old[col] != null) {
        lo = @min(lo, col);
        hi = col + 1;
    };
    if (hi <= lo) return "";
    const floor: u8 = @intFromFloat(faded_level);
    var out: std.Io.Writer.Allocating = .init(allocator);
    var last: ?u8 = null;
    var pending: usize = lo;
    var drawn = false;
    for (lo..hi) |col| {
        const fade: CellFade = if (changing) cellFade(frame.rise, col, lo, hi) else .{ .arrived = true, .strength = 1 };
        const glyph = (if (fade.arrived) fresh[col] else old[col]) orelse {
            pending += 1;
            continue;
        };
        if (glyph.len == 0) continue;
        const level = litLevel(fade.strength * (1 + glow_gain * glow(frame.light, col - lo, hi - lo)));
        if (level <= floor) {
            pending += tui_text.visibleWidth(glyph);
            continue;
        }
        for (0..pending) |_| try out.writer.writeByte(' ');
        pending = 0;
        if (last != level) try zz.Color.fromRgb(level, level, level).writeFg(&out.writer);
        last = level;
        try out.writer.writeAll(glyph);
        drawn = true;
    }
    if (!drawn) {
        out.deinit();
        return "";
    }
    try out.writer.writeAll("\x1b[0m");
    return out.toOwnedSlice();
}

fn activitySlot(allocator: std.mem.Allocator, frame: Frame, width: usize) ![slot_rows][]const u8 {
    var rows: [slot_rows][]const u8 = .{""} ** slot_rows;
    rows[slot_centre] = try activityRow(allocator, frame, width);
    return rows;
}

test "a final reply reads wider than the input bar when the screen allows" {
    try std.testing.expectEqual(@as(usize, 76), columnWidth(200));
    try std.testing.expectEqual(@as(usize, 120), readingWidth(200));
    try std.testing.expectEqual(@as(usize, 92), readingWidth(100));
    const wide = "w" ** 110;
    const text = try render(std.testing.allocator, .{ .width = 200, .height = 20, .final_block = wide, .input = "next", .cursor = 4 });
    defer std.testing.allocator.free(text);
    const plain = try plainRows(std.testing.allocator, text);
    defer std.testing.allocator.free(plain);
    var lines = std.mem.splitScalar(u8, plain, '\n');
    var reply_left: ?usize = null;
    var bar_left: ?usize = null;
    while (lines.next()) |line| {
        if (std.mem.indexOf(u8, line, wide)) |at| reply_left = at;
        if (std.mem.indexOf(u8, line, prompt ++ "next")) |at| bar_left = at;
    }
    try std.testing.expectEqual(@as(usize, 40), reply_left.?);
    try std.testing.expectEqual(@as(usize, 62), bar_left.?);
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

fn secretBar(allocator: std.mem.Allocator, input: []const u8, hint: []const u8, column: usize) ![]const u8 {
    if (input.len == 0) return std.fmt.allocPrint(allocator, "{s}\x1b[7m \x1b[0m\x1b[2m{s}\x1b[0m", .{ prompt, hint });
    const stars = try allocator.alloc(u8, @min(input.len, column -| (tui_text.visibleWidth(prompt) + 1)));
    @memset(stars, '*');
    return std.fmt.allocPrint(allocator, "{s}{s}\x1b[7m \x1b[0m", .{ prompt, stars });
}

fn inputBar(allocator: std.mem.Allocator, input: []const u8, cursor: usize, column: usize, hint: []const u8) ![]const u8 {
    const flat = try allocator.dupe(u8, input);
    for (flat) |*byte| {
        if (byte.* == '\n' or byte.* == '\r' or byte.* == '\t') byte.* = ' ';
    }
    const at = @min(cursor, flat.len);
    const budget = column -| (tui_text.visibleWidth(prompt) + 1);
    if (flat.len == 0) {
        return std.fmt.allocPrint(allocator, "{s}\x1b[7m \x1b[0m\x1b[2m{s}\x1b[0m", .{ prompt, hint });
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

test "the pulse breathes slowly while thinking, faster on a tool, and holds while waiting or idle" {
    try std.testing.expectEqual(@as(u8, 70), pulseLevel(.thinking, 0));
    try std.testing.expectEqual(@as(u8, 235), pulseLevel(.thinking, 0.5));
    try std.testing.expectEqual(pulseLevel(.tool, 0.25), pulseLevel(.thinking, 0.25));
    try std.testing.expect(breathStep(.tool) > breathStep(.thinking) * 2);
    try std.testing.expectEqual(@as(f32, 0), breathStep(.waiting));
    try std.testing.expectEqual(@as(f32, 0), breathStep(.idle));
    try std.testing.expectEqual(waiting_level, pulseLevel(.waiting, 0.5));
    try std.testing.expectEqual(idle_level, pulseLevel(.idle, 0.5));
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

test "a secret never reaches the input bar in the clear" {
    const text = try render(std.testing.allocator, .{ .width = 80, .height = 24, .input = "sk-live-123", .cursor = 11, .secret = true });
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "sk-live") == null);
    try std.testing.expect(std.mem.indexOf(u8, text, prompt ++ "***********") != null);
    const empty = try render(std.testing.allocator, .{ .width = 80, .height = 24, .secret = true, .placeholder = "paste the secret" });
    defer std.testing.allocator.free(empty);
    try std.testing.expect(std.mem.indexOf(u8, empty, "paste the secret") != null);
}

test "the farewell rises to full brightness, fades out, then ends" {
    try std.testing.expectEqual(idle_level, farewellLevel(0).?);
    try std.testing.expectEqual(@as(u8, 255), farewellLevel(farewell_rise_ticks).?);
    try std.testing.expect(farewellLevel(farewell_ticks - 1).? < 60);
    try std.testing.expect(farewellLevel(farewell_ticks) == null);
}

test "a frame in its farewell shows the bright trail, not yet the reply" {
    const text = try render(std.testing.allocator, .{ .width = 60, .height = 20, .final_block = "the final reply", .farewell = 255, .counts = .{ .tools = 1 } });
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "\x1b[38;2;255;255;255m" ++ dot) != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "the final reply") == null);
}

fn plainRows(allocator: std.mem.Allocator, text: []const u8) ![]u8 {
    return monochrome(allocator, text);
}

test "a running frame floats its input bar above the bottom, apart from the trail" {
    const text = try render(std.testing.allocator, .{ .width = 100, .height = 30, .running = true, .counts = .{ .thinking = 3, .tools = 1 }, .activity = "Shell Execute  go test ./...", .mood = .tool, .phase = 0.5, .input = "hello", .cursor = 5 });
    defer std.testing.allocator.free(text);
    const plain = try plainRows(std.testing.allocator, text);
    defer std.testing.allocator.free(plain);
    var lines = std.mem.splitScalar(u8, plain, '\n');
    var count: usize = 0;
    var trail_row: ?usize = null;
    var input_row: ?usize = null;
    while (lines.next()) |line| : (count += 1) {
        try std.testing.expect(tui_text.visibleWidth(line) <= 100);
        if (std.mem.indexOf(u8, line, dot ++ " " ++ dot) != null) trail_row = count;
        if (std.mem.indexOf(u8, line, prompt ++ "hello") != null) {
            input_row = count;
            try std.testing.expectEqual(@as(usize, 12), std.mem.indexOf(u8, line, prompt).?);
        }
    }
    try std.testing.expectEqual(@as(usize, 30), count);
    try std.testing.expectEqual(@as(usize, 24), input_row.?);
    try std.testing.expectEqual(@as(usize, 9), trail_row.?);
    try std.testing.expect(std.mem.indexOf(u8, plain, dot ++ " " ++ dot ++ " " ++ dot ++ " " ++ dot ++ "\n") != null);
    try std.testing.expect(std.mem.indexOf(u8, plain, "Shell Execute  go test") != null);
    try std.testing.expect(std.mem.indexOf(u8, plain, "1:15") == null);
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

test "a stopped frame shows the final reply with its markdown colours" {
    const text = try render(std.testing.allocator, .{ .width = 60, .height = 20, .final_block = "\x1b[36mthe final reply\x1b[0m", .counts = .{ .messages = 1 } });
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "\x1b[36mthe final reply") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "done") != null);

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

test "a final reply taller than its room scrolls with markers for what is hidden" {
    var reply: std.Io.Writer.Allocating = .init(std.testing.allocator);
    defer reply.deinit();
    for (0..40) |i| try reply.writer.print("row {d}\n", .{i});
    const base: Frame = .{ .width = 60, .height = 20, .final_block = reply.written(), .input = "next", .cursor = 4 };
    const most = try maxScroll(std.testing.allocator, base);
    try std.testing.expect(most > 0);

    var frame = base;
    frame.scroll = most;
    const top = try render(std.testing.allocator, frame);
    defer std.testing.allocator.free(top);
    try std.testing.expect(std.mem.indexOf(u8, top, "done") != null);
    try std.testing.expect(std.mem.indexOf(u8, top, "row 0\n") != null);
    try std.testing.expect(std.mem.indexOf(u8, top, "PgDn") != null);
    try std.testing.expect(std.mem.indexOf(u8, top, "PgUp") == null);

    frame.scroll = 0;
    const bottom = try render(std.testing.allocator, frame);
    defer std.testing.allocator.free(bottom);
    try std.testing.expect(std.mem.indexOf(u8, bottom, "row 39") != null);
    try std.testing.expect(std.mem.indexOf(u8, bottom, "PgUp") != null);
    try std.testing.expect(std.mem.indexOf(u8, bottom, "PgDn") == null);

    frame.scroll = most / 2;
    const middle = try render(std.testing.allocator, frame);
    defer std.testing.allocator.free(middle);
    try std.testing.expect(std.mem.indexOf(u8, middle, "PgUp") != null and std.mem.indexOf(u8, middle, "PgDn") != null);
    try std.testing.expectEqual(@as(usize, 20), std.mem.count(u8, middle, "\n") + 1);
    try std.testing.expect(std.mem.indexOf(u8, middle, prompt ++ "next") != null);

    try std.testing.expectEqual(@as(usize, 0), try maxScroll(std.testing.allocator, .{ .width = 60, .height = 20, .final_block = "short" }));
}

test "the input bar keeps the cursor in view and shows a placeholder when empty" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const empty = try inputBar(arena.allocator(), "", 0, 40, placeholder);
    try std.testing.expect(std.mem.indexOf(u8, empty, placeholder) != null);
    const long = "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnop";
    const bar = try inputBar(arena.allocator(), long, long.len, 20, placeholder);
    const plain = try monochrome(arena.allocator(), bar);
    try std.testing.expect(tui_text.visibleWidth(plain) <= 20);
    try std.testing.expect(std.mem.endsWith(u8, std.mem.trimEnd(u8, plain, " "), "mnop"));
}

fn levelsOf(allocator: std.mem.Allocator, row: []const u8) ![]u32 {
    var levels: std.ArrayList(u32) = .empty;
    var rest = row;
    while (std.mem.indexOf(u8, rest, "\x1b[38;2;")) |at| {
        rest = rest[at + 7 ..];
        const end = std.mem.indexOfScalar(u8, rest, ';') orelse break;
        try levels.append(allocator, try std.fmt.parseInt(u32, rest[0..end], 10));
    }
    return levels.toOwnedSlice(allocator);
}

test "a new line opens out of the old one from the centre, letter by letter" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var frame: Frame = .{ .width = 80, .height = 24, .running = true, .activity = "aaaaaaaaaaaaaaa", .incoming = "bbbbbbbbbbbbbbb" };

    const start = try plainRows(a, (try activitySlot(a, frame, 60))[1]);
    try std.testing.expectEqualStrings("aaaaaaaaaaaaaaa", std.mem.trim(u8, start, " "));

    frame.rise = 0.5;
    const slot = try activitySlot(a, frame, 60);
    try std.testing.expectEqualStrings("", slot[0]);
    try std.testing.expectEqualStrings("", slot[2]);
    const middle = std.mem.trim(u8, try plainRows(a, slot[1]), " ");
    try std.testing.expectEqual(@as(u8, 'a'), middle[0]);
    try std.testing.expectEqual(@as(u8, 'a'), middle[middle.len - 1]);
    try std.testing.expect(std.mem.indexOfScalar(u8, middle, 'b') != null);
    try std.testing.expectEqual(@as(u8, 'b'), middle[middle.len / 2]);

    frame.rise = 1;
    const landed = try plainRows(a, (try activitySlot(a, frame, 60))[1]);
    try std.testing.expectEqualStrings("bbbbbbbbbbbbbbb", std.mem.trim(u8, landed, " "));
}

test "every letter fades out to the background and its successor fades in, without a step" {
    for ([_]usize{ 10, 17, 24 }) |col| {
        var previous: f32 = 1;
        var darkest: f32 = 1;
        var arrived = false;
        var tick: u64 = 0;
        while (tick <= change_ticks) : (tick += 1) {
            const fade = cellFade(@as(f32, @floatFromInt(tick)) / @as(f32, @floatFromInt(change_ticks)), col, 10, 25);
            const signed: f32 = if (fade.arrived) fade.strength else -fade.strength;
            const was: f32 = if (arrived) previous else -previous;
            try std.testing.expect(@abs(signed - was) <= 0.25);
            if (fade.arrived and !arrived) try std.testing.expect(previous < 0.08 and fade.strength < 0.08);
            darkest = @min(darkest, fade.strength);
            previous = fade.strength;
            arrived = fade.arrived;
        }
        try std.testing.expect(arrived);
        try std.testing.expectEqual(@as(f32, 1), previous);
        try std.testing.expect(darkest < 0.08);
    }
    try std.testing.expect(cellFade(0.3, 17, 10, 25).arrived);
    try std.testing.expect(!cellFade(0.3, 10, 10, 25).arrived);
}

test "the light keeps opening from the centre to both ends while a line rests" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const soft: u32 = soft_level;
    var frame: Frame = .{ .width = 80, .height = 24, .running = true, .activity = "Shell Execute  ls" };

    for ([_]f32{ 0, 0.999 }) |at| {
        frame.light = at;
        for (try levelsOf(a, (try activitySlot(a, frame, 60))[1])) |level| try std.testing.expect(level <= soft + 2);
    }
    frame.light = 0.22;
    const early = try levelsOf(a, (try activitySlot(a, frame, 60))[1]);
    try std.testing.expect(early[early.len / 2] > early[0]);
    frame.light = 0.8;
    const late = try levelsOf(a, (try activitySlot(a, frame, 60))[1]);
    try std.testing.expect(late[0] > soft);
    try std.testing.expect(late[0] > late[late.len / 2]);

    frame.incoming = "Read  a.zig";
    frame.rise = 0.1;
    const fading = try levelsOf(a, (try activitySlot(a, frame, 60))[1]);
    try std.testing.expect(fading[0] > fading[fading.len / 2]);
}

fn levelOf(row: []const u8) u32 {
    const at = std.mem.indexOf(u8, row, "\x1b[38;2;") orelse return 0;
    const rest = row[at + 7 ..];
    const end = std.mem.indexOfScalar(u8, rest, ';') orelse return 0;
    return std.fmt.parseInt(u32, rest[0..end], 10) catch 0;
}

test "the activity line shows only the title until a tool call passes ten seconds, then both" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    try std.testing.expectEqualStrings("Read  a.zig", try activityLine(a, "Read  a.zig", 9_999, 60));
    try std.testing.expectEqualStrings("0:12 \u{b7} Read  a.zig", try activityLine(a, "Read  a.zig", 12_000, 60));
    try std.testing.expectEqualStrings("1:05 \u{b7} Read  a.zig", try activityLine(a, "Read  a.zig", 65_000, 60));
}
