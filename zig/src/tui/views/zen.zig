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
    previous: []const u8 = "",
    since_change: u64 = slide_ticks,
    stuck_ms: u64 = 0,
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
};

const max_column: usize = 76;
const soft_level: u8 = 128;
const prompt = "\u{276f} ";
const placeholder = "type a prompt";

pub fn columnWidth(width: usize) usize {
    return std.math.clamp(width -| 4, 16, max_column);
}

const min_gap: usize = 2;

fn bottomMargin(height: usize) usize {
    return std.math.clamp(height / 6, 1, 6);
}

pub fn render(allocator: std.mem.Allocator, frame: Frame) ![]u8 {
    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const width = @max(frame.width, 20);
    const column = columnWidth(width);

    var content: std.ArrayList([]const u8) = .empty;
    if (!frame.running and frame.final_block.len > 0 and frame.farewell == null) {
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
            try content.append(arena, "");
            try content.append(arena, try centered(arena, try gray(arena, soft_level, if (frame.farewell != null) "" else "zen \u{b7} only the final reply is shown"), column));
            try content.append(arena, "");
        }
    }

    var lower: std.ArrayList([]const u8) = .empty;
    if (frame.extra.len > 0) {
        var lines = std.mem.splitScalar(u8, frame.extra, '\n');
        while (lines.next()) |line| try lower.append(arena, line);
        try lower.append(arena, "");
    }
    try lower.append(arena, if (frame.secret) try secretBar(arena, frame.input, frame.placeholder, column) else try inputBar(arena, frame.input, frame.cursor, column, frame.placeholder));

    const lower_shown = if (lower.items.len > frame.height) lower.items[lower.items.len - frame.height ..] else lower.items;
    const lower_top = frame.height -| (bottomMargin(frame.height) + lower_shown.len);
    const area = lower_top -| min_gap;
    const shown = if (content.items.len > area) content.items[content.items.len - area ..] else content.items;
    const content_top = (area -| shown.len) / 2;
    const left = (width -| column) / 2;

    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    for (0..frame.height) |index| {
        if (index > 0) try writer.writeByte('\n');
        const row: []const u8 = if (index >= lower_top)
            (if (index - lower_top < lower_shown.len) lower_shown[index - lower_top] else "")
        else if (index >= content_top and index - content_top < shown.len)
            shown[index - content_top]
        else
            "";
        if (row.len == 0) continue;
        for (0..left) |_| try writer.writeByte(' ');
        try writer.writeAll(row);
    }
    return out.toOwnedSlice();
}

pub const slide_ticks: u64 = 12;
const stuck_after_ms: u64 = 10_000;
const faded_level: f32 = 40;

fn activityLine(allocator: std.mem.Allocator, text: []const u8, stuck_ms: u64, width: usize) ![]const u8 {
    if (stuck_ms < stuck_after_ms) return tui_text.truncateLineToWidth(allocator, text, width);
    const seconds = stuck_ms / 1000;
    const clock = try std.fmt.allocPrint(allocator, "{d}:{d:0>2}", .{ seconds / 60, seconds % 60 });
    if (text.len == 0) return clock;
    const fitted = try tui_text.truncateLineToWidth(allocator, text, width -| (clock.len + 3));
    return std.fmt.allocPrint(allocator, "{s} \u{b7} {s}", .{ clock, fitted });
}

fn fadedRow(allocator: std.mem.Allocator, text: []const u8, strength: f32, width: usize) ![]const u8 {
    if (text.len == 0 or strength <= 0) return "";
    const level: u8 = @intFromFloat(@round(faded_level + (@as(f32, @floatFromInt(soft_level)) - faded_level) * @min(strength, 1)));
    return centered(allocator, try gray(allocator, level, text), width);
}

fn activitySlot(allocator: std.mem.Allocator, frame: Frame, width: usize) ![3][]const u8 {
    const current = try activityLine(allocator, frame.activity, frame.stuck_ms, width);
    if (frame.since_change >= slide_ticks) return .{ "", try fadedRow(allocator, current, 1, width), "" };
    const half = slide_ticks / 2;
    if (frame.since_change < half) {
        const p = @as(f32, @floatFromInt(frame.since_change)) / @as(f32, @floatFromInt(half));
        const previous = try tui_text.truncateLineToWidth(allocator, frame.previous, width);
        return .{ try fadedRow(allocator, previous, 1 - p, width), "", try fadedRow(allocator, current, p, width) };
    }
    return .{ "", try fadedRow(allocator, current, 1, width), "" };
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

test "a changed activity slides up: the old line rises and fades as the new one rises into its place" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const base: Frame = .{ .width = 80, .height = 24, .running = true, .activity = "Shell Execute  ls", .previous = "thinking" };

    var frame = base;
    frame.since_change = 2;
    const rising = try activitySlot(a, frame, 60);
    try std.testing.expect(std.mem.indexOf(u8, rising[0], "thinking") != null);
    try std.testing.expectEqualStrings("", rising[1]);
    try std.testing.expect(std.mem.indexOf(u8, rising[2], "Shell Execute") != null);

    frame.since_change = 5;
    const later = try activitySlot(a, frame, 60);
    try std.testing.expect(levelOf(later[0]) < levelOf(rising[0]));
    try std.testing.expect(levelOf(later[2]) > levelOf(rising[2]));

    frame.since_change = slide_ticks / 2;
    const landed = try activitySlot(a, frame, 60);
    try std.testing.expectEqualStrings("", landed[0]);
    try std.testing.expect(std.mem.indexOf(u8, landed[1], "Shell Execute") != null);
    try std.testing.expectEqualStrings("", landed[2]);

    frame.since_change = slide_ticks;
    const settled = try activitySlot(a, frame, 60);
    try std.testing.expect(std.mem.indexOf(u8, settled[1], "thinking") == null);
    try std.testing.expectEqual(levelOf(landed[1]), levelOf(settled[1]));
}

fn levelOf(row: []const u8) u32 {
    const at = std.mem.indexOf(u8, row, "\x1b[38;2;") orelse return 0;
    const rest = row[at + 7 ..];
    const end = std.mem.indexOfScalar(u8, rest, ';') orelse return 0;
    return std.fmt.parseInt(u32, rest[0..end], 10) catch 0;
}

test "the activity line shows how long it has been stuck only after ten seconds" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    try std.testing.expectEqualStrings("Read  a.zig", try activityLine(a, "Read  a.zig", 9_999, 60));
    try std.testing.expectEqualStrings("0:12 \u{b7} Read  a.zig", try activityLine(a, "Read  a.zig", 12_000, 60));
    try std.testing.expectEqualStrings("1:05 \u{b7} Read  a.zig", try activityLine(a, "Read  a.zig", 65_000, 60));
}
