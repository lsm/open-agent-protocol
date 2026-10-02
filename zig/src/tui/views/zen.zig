const std = @import("std");
const zz = @import("zigzag");
const tui_theme = @import("tui_theme");
const tui_text = @import("tui_text");

pub const glyph = "\u{7985}";

const breath_ticks: u64 = 80;
const dimmest: f32 = 70;
const brightest: f32 = 235;
const idle_level: u8 = 150;

pub fn glyphLevel(running: bool, tick: u64) u8 {
    if (!running) return idle_level;
    const phase = @as(f32, @floatFromInt(tick % breath_ticks)) / @as(f32, @floatFromInt(breath_ticks));
    const wave = (1 - @cos(phase * std.math.tau)) / 2;
    return @intFromFloat(@round(dimmest + (brightest - dimmest) * wave));
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
    tick: u64 = 0,
};

pub fn render(allocator: std.mem.Allocator, frame: Frame) ![]u8 {
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    const width = @max(frame.width, 20);
    const counts = try countsRow(allocator, frame.counts, width);
    defer allocator.free(counts);

    if (!frame.running and frame.final_block.len > 0) {
        const badge = if (frame.failed) tui_theme.glyph.err ++ " stopped" else tui_theme.glyph.check ++ " done";
        try writeCentered(writer, allocator, try styled(allocator, if (frame.failed) tui_theme.palette.danger else tui_theme.palette.success, badge), width);
        try writer.writeAll("\n\n");
        try writer.writeAll(frame.final_block);
        try writer.writeAll("\n\n");
        try writer.writeAll(counts);
        return out.toOwnedSlice();
    }

    const above = (frame.height -| 4) / 2;
    for (0..above) |_| try writer.writeByte('\n');
    const level = glyphLevel(frame.running, frame.tick);
    try writeCentered(writer, allocator, try styled(allocator, zz.Color.fromRgb(level, level, level), glyph), width);
    try writer.writeAll("\n\n");
    try writer.writeAll(counts);
    try writer.writeByte('\n');
    const line = if (frame.running) try activityLine(allocator, frame, width) else try allocator.dupe(u8, "zen \u{b7} send a prompt; only the final reply is shown");
    defer allocator.free(line);
    try writeCentered(writer, allocator, try styled(allocator, tui_theme.palette.dim, line), width);
    return out.toOwnedSlice();
}

fn activityLine(allocator: std.mem.Allocator, frame: Frame, width: usize) ![]u8 {
    const seconds = frame.elapsed_ms / 1000;
    const clock = try std.fmt.allocPrint(allocator, "{d}:{d:0>2}", .{ seconds / 60, seconds % 60 });
    defer allocator.free(clock);
    if (frame.activity.len == 0) return std.fmt.allocPrint(allocator, "zen · {s}", .{clock});
    const budget = width -| (clock.len + 12);
    const fitted = try tui_text.truncateLineToWidth(allocator, frame.activity, budget);
    defer allocator.free(fitted);
    return std.fmt.allocPrint(allocator, "zen · {s} · {s}", .{ clock, fitted });
}

fn countsRow(allocator: std.mem.Allocator, counts: Counts, width: usize) ![]u8 {
    const thinking = try std.fmt.allocPrint(allocator, "{s} {d} thinking", .{ tui_theme.glyph.thinking, counts.thinking });
    defer allocator.free(thinking);
    const tools = try std.fmt.allocPrint(allocator, "{s} {d} tool{s}", .{ tui_theme.glyph.tool, counts.tools, if (counts.tools == 1) "" else "s" });
    defer allocator.free(tools);
    const messages = try std.fmt.allocPrint(allocator, "{s} {d} message{s}", .{ tui_theme.glyph.assistant, counts.messages, if (counts.messages == 1) "" else "s" });
    defer allocator.free(messages);
    const a = try styled(allocator, tui_theme.palette.thinking, thinking);
    defer allocator.free(a);
    const b = try styled(allocator, tui_theme.palette.tool_shell, tools);
    defer allocator.free(b);
    const c = try styled(allocator, tui_theme.palette.assistant, messages);
    defer allocator.free(c);
    const row = try std.fmt.allocPrint(allocator, "{s}   {s}   {s}", .{ a, b, c });
    defer allocator.free(row);
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    try writeCentered(&out.writer, allocator, try allocator.dupe(u8, row), width);
    return out.toOwnedSlice();
}

fn styled(allocator: std.mem.Allocator, color: zz.Color, text: []const u8) ![]u8 {
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    try color.writeFg(&out.writer);
    try out.writer.writeAll(text);
    try out.writer.writeAll("\x1b[0m");
    return out.toOwnedSlice();
}

fn writeCentered(writer: *std.Io.Writer, allocator: std.mem.Allocator, owned: []u8, width: usize) !void {
    defer allocator.free(owned);
    const visible = tui_text.visibleWidth(owned);
    for (0..(width -| visible) / 2) |_| try writer.writeByte(' ');
    try writer.writeAll(owned);
}

test "the glyph breathes while running and rests when idle" {
    try std.testing.expectEqual(@as(u8, 70), glyphLevel(true, 0));
    try std.testing.expectEqual(@as(u8, 235), glyphLevel(true, 40));
    try std.testing.expectEqual(glyphLevel(true, 10), glyphLevel(true, 90));
    try std.testing.expectEqual(idle_level, glyphLevel(false, 40));
}

test "a running frame centres the glyph and shows the counts and activity" {
    const text = try render(std.testing.allocator, .{ .width = 80, .height = 20, .running = true, .elapsed_ms = 75_000, .counts = .{ .thinking = 3, .tools = 1, .messages = 0 }, .activity = "Shell Execute  go test ./...", .tick = 40 });
    defer std.testing.allocator.free(text);
    var lines = std.mem.splitScalar(u8, text, '\n');
    var count: usize = 0;
    var glyph_row: ?usize = null;
    while (lines.next()) |line| : (count += 1) {
        try std.testing.expect(tui_text.visibleWidth(line) <= 80);
        if (std.mem.indexOf(u8, line, glyph) != null) glyph_row = count;
    }
    try std.testing.expectEqual(@as(usize, 8), glyph_row.?);
    try std.testing.expect(std.mem.indexOf(u8, text, "3 thinking") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "1 tool") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "1 tools") == null);
    try std.testing.expect(std.mem.indexOf(u8, text, "1:15") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "go test") != null);
}

test "a stopped frame shows the final block instead of the glyph" {
    const text = try render(std.testing.allocator, .{ .width = 60, .height = 20, .final_block = "the final reply", .counts = .{ .messages = 1 } });
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "the final reply") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "done") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, glyph) == null);

    const failed = try render(std.testing.allocator, .{ .width = 60, .height = 20, .final_block = "boom", .failed = true });
    defer std.testing.allocator.free(failed);
    try std.testing.expect(std.mem.indexOf(u8, failed, "stopped") != null);
}
