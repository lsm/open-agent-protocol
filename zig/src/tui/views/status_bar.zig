const std = @import("std");
const zz = @import("zigzag");
const tui_state = @import("tui_state");
const tui_theme = @import("tui_theme");
const tui_text = @import("tui_text");

pub const Options = struct {
    width: usize = 80,
    hint: []const u8 = "",
};

const gauge_cells: usize = 8;
const hint_gap: usize = 3;

const SegmentKind = enum { model, context, queue, perm, cost, backpressure, drops, think, turns, state };

const Segment = struct {
    kind: SegmentKind,
    styled: []const u8,
    width: usize,
};

const SegmentList = std.ArrayList(Segment);

const DropStep = union(enum) {
    kind: SegmentKind,
    hint,
    perm_when_ask,
};

const drop_steps = [_]DropStep{
    .{ .kind = .turns },
    .{ .kind = .think },
    .{ .kind = .cost },
    .hint,
    .perm_when_ask,
    .{ .kind = .queue },
    .{ .kind = .drops },
    .{ .kind = .model },
    .{ .kind = .backpressure },
    .{ .kind = .context },
};

const max_segments: usize = 16;

pub fn render(allocator: std.mem.Allocator, state: *const tui_state.AppState, options: Options) ![]const u8 {
    var segments: SegmentList = .empty;
    defer {
        for (segments.items) |seg| allocator.free(seg.styled);
        segments.deinit(allocator);
    }

    const model = if (state.status.model.len > 0) state.status.model else "no-model";
    const provider = if (state.status.provider.len > 0) state.status.provider else "local";

    try pushOwnedValue(&segments, allocator, .model, try std.fmt.allocPrint(allocator, "{s}/{s}", .{ provider, model }), tui_theme.statusModel());
    try pushOwned(&segments, allocator, .context, try contextText(allocator, state, false));
    if (state.queue.total() > 0) {
        try pushOwnedSegment(&segments, allocator, .queue, "queue", try std.fmt.allocPrint(allocator, "{d}", .{state.queue.total()}));
    }
    if (state.mode == .approval) {
        try pushValue(&segments, allocator, .perm, "pending", tui_theme.warningText());
    } else if (state.permission_mode == .bypass) {
        try pushValue(&segments, allocator, .perm, "bypass", tui_theme.warningText());
    } else {
        try pushValue(&segments, allocator, .perm, @tagName(state.permission_mode), tui_theme.successText());
    }
    if (contextTokens(state) > 0 and state.telemetry.input_cost_per_million > 0) try pushOwnedValue(&segments, allocator, .cost, try estimatedCost(allocator, state), tui_theme.statusSegment());
    if (state.backpressure_active or state.dropped_event_count > 0) {
        const label: []const u8 = if (state.backpressure_active) "backpressure" else "drops";
        const value = try std.fmt.allocPrint(allocator, "{s}:{d}", .{ label, state.dropped_event_count });
        if (state.backpressure_active) {
            try pushOwnedValue(&segments, allocator, .backpressure, value, tui_theme.warningText());
        } else {
            try pushOwnedValue(&segments, allocator, .drops, value, tui_theme.statusSegment());
        }
    }
    if (state.thinking_level != .off) {
        try pushValue(&segments, allocator, .think, @tagName(state.thinking_level), tui_theme.statusSegment());
    }
    try pushOwnedSegment(&segments, allocator, .turns, "turns", try std.fmt.allocPrint(allocator, "{d}", .{state.status.turn_count}));
    try writeState(&segments, allocator, state);

    var dropped = [_]bool{false} ** max_segments;
    const mask = dropped[0..segments.items.len];

    if (try fitLayout(allocator, segments.items, mask, options.width, options.hint)) |text| return text;

    if (findKind(segments.items, mask, .context)) |idx| {
        const compact = try contextText(allocator, state, true);
        allocator.free(segments.items[idx].styled);
        segments.items[idx] = .{ .kind = .context, .styled = compact, .width = tui_text.visibleWidth(compact) };
        if (try fitLayout(allocator, segments.items, mask, options.width, options.hint)) |text| return text;
    }

    var hint_dropped = false;
    for (drop_steps) |step| {
        switch (step) {
            .hint => {
                if (options.hint.len == 0 or hint_dropped) continue;
                hint_dropped = true;
            },
            .perm_when_ask => {
                if (state.mode == .approval or state.permission_mode != .ask) continue;
                const idx = findKind(segments.items, mask, .perm) orelse continue;
                mask[idx] = true;
            },
            .kind => |kind| {
                const idx = findKind(segments.items, mask, kind) orelse continue;
                mask[idx] = true;
            },
        }
        const hint = if (hint_dropped) "" else options.hint;
        if (try fitLayout(allocator, segments.items, mask, options.width, hint)) |text| return text;
    }

    const left = try layoutKept(allocator, segments.items, mask, false);
    defer allocator.free(left);
    return tui_text.truncateToWidth(allocator, left, options.width);
}

pub fn renderCwdRow(allocator: std.mem.Allocator, display: []const u8, width: usize) ![]u8 {
    const clipped = try tui_text.takeTrailingWidth(allocator, display, width);
    defer allocator.free(clipped);
    const styled = try tui_theme.muted().render(allocator, clipped);
    defer allocator.free(styled);
    const pad = width -| tui_text.visibleWidth(clipped);
    if (pad == 0) return allocator.dupe(u8, styled);
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    for (0..pad) |_| try out.writer.writeByte(' ');
    try out.writer.writeAll(styled);
    return out.toOwnedSlice();
}

fn contextTokens(state: *const tui_state.AppState) u64 {
    return if (state.telemetry.estimated_tokens > 0) state.telemetry.estimated_tokens else state.status.context_used;
}

fn appendHint(allocator: std.mem.Allocator, left: []const u8, hint: []const u8, width: usize) ![]u8 {
    const left_width = tui_text.visibleWidth(left);
    const hint_width = tui_text.visibleWidth(hint);
    if (left_width + hint_gap + hint_width > width) return allocator.dupe(u8, left);
    const styled = try tui_theme.keyHint().render(allocator, hint);
    defer allocator.free(styled);
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    try out.writer.writeAll(left);
    const pad = width - left_width - hint_width;
    for (0..pad) |_| try out.writer.writeByte(' ');
    try out.writer.writeAll(styled);
    return out.toOwnedSlice();
}

fn fitLayout(allocator: std.mem.Allocator, segments: []const Segment, mask: []const bool, width: usize, hint: []const u8) !?[]u8 {
    var cut = false;
    for (mask) |dropped| cut = cut or dropped;
    const left = try layoutKept(allocator, segments, mask, cut);
    if (hint.len > 0) {
        if (tui_text.visibleWidth(left) + hint_gap + tui_text.visibleWidth(hint) <= width) {
            const with_hint = try appendHint(allocator, left, hint, width);
            allocator.free(left);
            return with_hint;
        }
        allocator.free(left);
        return null;
    }
    if (tui_text.visibleWidth(left) <= width) return left;
    allocator.free(left);
    return null;
}

fn layoutKept(allocator: std.mem.Allocator, segments: []const Segment, mask: []const bool, show_cut: bool) ![]u8 {
    const sep = try tui_theme.faint().render(allocator, " " ++ tui_theme.glyph.sep ++ " ");
    defer allocator.free(sep);
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    var written: usize = 0;
    for (segments, mask) |seg, dropped| {
        if (dropped) continue;
        if (written > 0) try out.writer.writeAll(sep);
        try out.writer.writeAll(seg.styled);
        written += 1;
    }
    if (show_cut and written > 0) {
        const ellipsis = try tui_theme.dim().render(allocator, "…");
        defer allocator.free(ellipsis);
        try out.writer.writeAll(sep);
        try out.writer.writeAll(ellipsis);
    }
    return out.toOwnedSlice();
}

fn findKind(segments: []const Segment, mask: []const bool, kind: SegmentKind) ?usize {
    for (segments, 0..) |seg, i| {
        if (!mask[i] and seg.kind == kind) return i;
    }
    return null;
}

fn gaugeColor(pct: u64) zz.Color {
    if (pct >= 85) return tui_theme.palette.gauge_red;
    if (pct >= 75) return tui_theme.palette.gauge_orange;
    if (pct >= 60) return tui_theme.palette.gauge_yellow;
    return tui_theme.palette.gauge_green;
}

fn renderGauge(allocator: std.mem.Allocator, pct: u64) ![]u8 {
    const filled: usize = @intCast(@min(gauge_cells, (pct * gauge_cells + 50) / 100));
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    try gaugeColor(pct).writeFg(writer);
    for (0..filled) |_| try writer.writeAll(tui_theme.glyph.gauge_on);
    try zz.ansi.sgr(writer, "0");
    try tui_theme.palette.faint.writeFg(writer);
    for (filled..gauge_cells) |_| try writer.writeAll(tui_theme.glyph.gauge_off);
    try writer.writeAll(zz.ansi.reset);
    return out.toOwnedSlice();
}

fn contextText(allocator: std.mem.Allocator, state: *const tui_state.AppState, compact: bool) ![]const u8 {
    const used: u64 = if (state.telemetry.estimated_tokens > 0) state.telemetry.estimated_tokens else state.status.context_used;
    const limit: u64 = if (state.telemetry.context_window > 0) state.telemetry.context_window else state.status.context_limit;
    const pct: u64 = if (limit > 0) @min(100, (used * 100) / limit) else 0;
    const used_text = try tui_text.compactNumber(allocator, used);
    defer allocator.free(used_text);
    if (limit == 0) {
        if (compact) return tui_theme.statusSegment().render(allocator, used_text);
        const gauge_text = try renderGauge(allocator, pct);
        defer allocator.free(gauge_text);
        const value = try std.fmt.allocPrint(allocator, "{s} {s} ctx", .{ gauge_text, used_text });
        defer allocator.free(value);
        return tui_theme.statusSegment().render(allocator, value);
    }
    const pct_text = try std.fmt.allocPrint(allocator, "{d}%", .{pct});
    defer allocator.free(pct_text);
    const styled_pct = try (zz.Style{}).fg(gaugeColor(pct)).inline_style(true).render(allocator, pct_text);
    if (compact) return styled_pct;
    defer allocator.free(styled_pct);
    const gauge_text = try renderGauge(allocator, pct);
    defer allocator.free(gauge_text);
    const limit_text = try tui_text.compactNumber(allocator, limit);
    defer allocator.free(limit_text);
    const tokens = try std.fmt.allocPrint(allocator, "{s}/{s}", .{ used_text, limit_text });
    defer allocator.free(tokens);
    const styled_tokens = try tui_theme.statusSegment().render(allocator, tokens);
    defer allocator.free(styled_tokens);
    return std.fmt.allocPrint(allocator, "{s} {s} {s}", .{ gauge_text, styled_pct, styled_tokens });
}

fn writeState(list: *SegmentList, allocator: std.mem.Allocator, state: *const tui_state.AppState) !void {
    if (state.status.streaming) {
        const elapsed = state.status.streaming_elapsed_ms;
        const activity = if (state.status.compacting) "compacting" else "streaming";
        var elapsed_buf: [12]u8 = undefined;
        const value = if (elapsed > 0)
            try std.fmt.allocPrint(allocator, "{s} {s} {s}", .{ tui_theme.spinnerFrame(state.anim_tick), activity, formatElapsed(&elapsed_buf, elapsed) })
        else
            try std.fmt.allocPrint(allocator, "{s} {s}", .{ tui_theme.spinnerFrame(state.anim_tick), activity });
        defer allocator.free(value);
        try pushValue(list, allocator, .state, value, tui_theme.runningText());
    } else {
        try pushValue(list, allocator, .state, "idle", tui_theme.muted());
    }
}

fn formatElapsed(buf: *[12]u8, ms: u64) []const u8 {
    const secs = ms / 1000;
    if (secs < 60) {
        return std.fmt.bufPrint(buf, "{d}.{d}s", .{ secs, (ms % 1000) / 100 }) catch "";
    }
    return std.fmt.bufPrint(buf, "{d}m{d:0>2}s", .{ secs / 60, secs % 60 }) catch "";
}

fn estimatedCost(allocator: std.mem.Allocator, state: *const tui_state.AppState) ![]u8 {
    const tokens: f64 = @floatFromInt(if (state.telemetry.estimated_tokens > 0) state.telemetry.estimated_tokens else state.status.context_used);
    const rate: f64 = state.telemetry.input_cost_per_million;
    const dollars = (tokens / 1_000_000.0) * rate;
    return std.fmt.allocPrint(allocator, "${d:.4}", .{dollars});
}

fn pushSegment(list: *SegmentList, allocator: std.mem.Allocator, kind: SegmentKind, key: []const u8, value: []const u8) !void {
    try pushStyledValue(list, allocator, kind, key, value, tui_theme.statusSegment());
}

fn pushOwnedSegment(list: *SegmentList, allocator: std.mem.Allocator, kind: SegmentKind, key: []const u8, value: []u8) !void {
    defer allocator.free(value);
    try pushSegment(list, allocator, kind, key, value);
}

fn pushStyledValue(list: *SegmentList, allocator: std.mem.Allocator, kind: SegmentKind, key: []const u8, value: []const u8, value_style: zz.Style) !void {
    const styled_key = try tui_theme.statusKey().render(allocator, key);
    defer allocator.free(styled_key);
    const styled_value = try value_style.render(allocator, value);
    defer allocator.free(styled_value);
    const styled = try std.fmt.allocPrint(allocator, "{s}:{s}", .{ styled_key, styled_value });
    try pushOwned(list, allocator, kind, styled);
}

fn pushValue(list: *SegmentList, allocator: std.mem.Allocator, kind: SegmentKind, value: []const u8, value_style: zz.Style) !void {
    try pushOwned(list, allocator, kind, try value_style.render(allocator, value));
}

fn pushOwnedValue(list: *SegmentList, allocator: std.mem.Allocator, kind: SegmentKind, value: []u8, value_style: zz.Style) !void {
    defer allocator.free(value);
    try pushValue(list, allocator, kind, value, value_style);
}

fn pushOwned(list: *SegmentList, allocator: std.mem.Allocator, kind: SegmentKind, styled: []const u8) !void {
    errdefer allocator.free(styled);
    try list.append(allocator, .{ .kind = kind, .styled = styled, .width = tui_text.visibleWidth(styled) });
}

test "status bar renders model and clips width" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModel(std.testing.allocator, "claude", "anthropic");
    state.status.streaming = true;

    const text = try render(std.testing.allocator, &state, .{ .width = 24 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(tui_text.visibleWidth(text) <= 24);
    try std.testing.expect(std.mem.indexOf(u8, text, "streaming") != null);
}

test "status bar renders queue count when queued" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.queue.steering = 1;
    state.queue.follow_up = 2;

    const text = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(std.mem.indexOf(u8, text, "queue") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "3") != null);
}

test "status bar renders context gauge cost and bare segment values" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModelWithContext(std.testing.allocator, "claude", "anthropic", 200_000);
    state.telemetry.estimated_tokens = 10_000;
    state.telemetry.context_window = 200_000;
    state.telemetry.input_cost_per_million = 3.0;

    const text = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(std.mem.indexOf(u8, text, "10k/200k") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, tui_theme.glyph.gauge_off) != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "5%") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "$") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "bypass") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "low") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "perm:") == null);
    try std.testing.expect(std.mem.indexOf(u8, text, "think:") == null);
}

test "status bar colors the context gauge by usage band" {
    const Case = struct { pct: u64, sgr: []const u8 };
    const cases = [_]Case{
        .{ .pct = 59, .sgr = "38;5;34" },
        .{ .pct = 60, .sgr = "38;5;178" },
        .{ .pct = 75, .sgr = "38;5;208" },
        .{ .pct = 85, .sgr = "38;5;196" },
    };
    for (cases) |case| {
        var state = tui_state.AppState.init(std.testing.allocator);
        defer state.deinit();
        state.telemetry.estimated_tokens = case.pct * 1_000;
        state.telemetry.context_window = 100_000;

        const text = try render(std.testing.allocator, &state, .{ .width = 160 });
        defer std.testing.allocator.free(text);

        try std.testing.expect(std.mem.indexOf(u8, text, case.sgr) != null);
    }
}

test "status bar hides the cost when the model price is unknown" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModelWithContext(std.testing.allocator, "claude-opus-5", "anthropic", 200_000);
    state.telemetry.estimated_tokens = 10_000;
    state.telemetry.context_window = 200_000;
    state.telemetry.input_cost_per_million = 0;

    const text = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(std.mem.indexOf(u8, text, "10k/200k") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "$") == null);
}

test "status bar prices context with the active model rate" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.telemetry.estimated_tokens = 1_000_000;
    state.telemetry.input_cost_per_million = 15.0;

    const text = try render(std.testing.allocator, &state, .{ .width = 200 });
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "$15.0000") != null);
}

test "status bar shows streaming elapsed time" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.status.streaming = true;
    state.status.streaming_elapsed_ms = 4_200;

    const text = try render(std.testing.allocator, &state, .{ .width = 200 });
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "streaming 4.2s") != null);

    state.status.streaming_elapsed_ms = 75_000;
    const long = try render(std.testing.allocator, &state, .{ .width = 200 });
    defer std.testing.allocator.free(long);
    try std.testing.expect(std.mem.indexOf(u8, long, "streaming 1m15s") != null);
}

test "status bar puts the state segment last" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModelWithContext(std.testing.allocator, "claude-sonnet-4-5", "anthropic", 200_000);
    state.status.turn_count = 4;

    const text = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(text);

    const idle_pos = std.mem.indexOf(u8, text, "idle") orelse return error.MissingStateSegment;
    const turns_pos = std.mem.indexOf(u8, text, "turns") orelse return error.MissingTurnsSegment;
    try std.testing.expect(idle_pos > turns_pos);
}

test "status bar appends the hint right-aligned and drops segments by priority to fit it" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModelWithContext(std.testing.allocator, "claude-sonnet-4-5", "anthropic", 200_000);
    state.status.turn_count = 4;

    const text = try render(std.testing.allocator, &state, .{ .width = 160, .hint = "⏎ send · esc clear" });
    defer std.testing.allocator.free(text);
    try std.testing.expectEqual(@as(usize, 160), tui_text.visibleWidth(text));
    try std.testing.expect(std.mem.endsWith(u8, text, "esc clear" ++ zz.ansi.reset));
    try std.testing.expect(std.mem.indexOf(u8, text, "turns:4") != null);

    const mid = try render(std.testing.allocator, &state, .{ .width = 90, .hint = "⏎ send · esc clear" });
    defer std.testing.allocator.free(mid);
    try std.testing.expectEqual(@as(usize, 90), tui_text.visibleWidth(mid));
    try std.testing.expect(std.mem.indexOf(u8, mid, "esc clear") != null);
    try std.testing.expect(std.mem.indexOf(u8, mid, "anthropic/claude-sonnet-4-5") != null);
    try std.testing.expect(std.mem.indexOf(u8, mid, "turns:4") != null);

    const narrow = try render(std.testing.allocator, &state, .{ .width = 60, .hint = "⏎ send · esc clear" });
    defer std.testing.allocator.free(narrow);
    try std.testing.expect(tui_text.visibleWidth(narrow) <= 60);
    try std.testing.expect(std.mem.indexOf(u8, narrow, "esc clear") == null);
    try std.testing.expect(std.mem.indexOf(u8, narrow, "turns") == null);
    try std.testing.expect(std.mem.indexOf(u8, narrow, "low") == null);
    try std.testing.expect(std.mem.indexOf(u8, narrow, "anthropic/claude-sonnet-4-5") != null);
    try std.testing.expect(std.mem.indexOf(u8, narrow, "…") != null);

    const tight = try render(std.testing.allocator, &state, .{ .width = 40, .hint = "⏎ send · esc clear" });
    defer std.testing.allocator.free(tight);
    try std.testing.expect(tui_text.visibleWidth(tight) <= 40);
    try std.testing.expect(std.mem.indexOf(u8, tight, "idle") != null);
    try std.testing.expect(std.mem.indexOf(u8, tight, "esc clear") == null);

    const crowded = try render(std.testing.allocator, &state, .{ .width = 62, .hint = "a very long hint that can never fit beside the segments at all" });
    defer std.testing.allocator.free(crowded);
    try std.testing.expect(std.mem.indexOf(u8, crowded, "never fit") == null);
    try std.testing.expect(std.mem.indexOf(u8, crowded, "idle") != null);
}

test "status bar hides the cost until context tokens are known" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    const idle = try render(std.testing.allocator, &state, .{ .width = 200 });
    defer std.testing.allocator.free(idle);
    try std.testing.expect(std.mem.indexOf(u8, idle, "$") == null);
    state.telemetry.estimated_tokens = 500;
    state.telemetry.input_cost_per_million = 3.0;
    const priced = try render(std.testing.allocator, &state, .{ .width = 200 });
    defer std.testing.allocator.free(priced);
    try std.testing.expect(std.mem.indexOf(u8, priced, "$0.0015") != null);
}

test "status bar renders bypass permission mode as a bare value" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.permission_mode = .bypass;

    const text = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(std.mem.indexOf(u8, text, "bypass") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "perm:") == null);
}

test "status bar hides the thinking segment when thinking is off" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.thinking_level = .off;

    const text = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(std.mem.indexOf(u8, text, "off") == null);
    try std.testing.expect(std.mem.indexOf(u8, text, "low") == null);
}

test "status bar renders backpressure indicator when active" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.backpressure_active = true;
    state.dropped_event_count = 5;

    const text = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(std.mem.indexOf(u8, text, "backpressure") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, ":5") != null);
}

test "status bar renders drop count after backpressure clears" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.dropped_event_count = 12;

    const text = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(std.mem.indexOf(u8, text, "drops") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, ":12") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "drops:drops") == null);
    try std.testing.expect(std.mem.indexOf(u8, text, "backpressure") == null);
}

test "status bar does not render last error" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setError(std.testing.allocator, "agent error");
    state.status.turn_count = 7;

    const text = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(std.mem.indexOf(u8, text, "turns") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "7") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "agent error") == null);
}

test "status bar drops turns first at narrow width" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModelWithContext(std.testing.allocator, "claude-sonnet-4-5", "anthropic", 200_000);
    state.thinking_level = .medium;
    state.status.turn_count = 13;

    const full = try render(std.testing.allocator, &state, .{ .width = 200 });
    defer std.testing.allocator.free(full);
    try std.testing.expect(tui_text.visibleWidth(full) > 80);

    const text = try render(std.testing.allocator, &state, .{ .width = 80 });
    defer std.testing.allocator.free(text);
    try std.testing.expect(tui_text.visibleWidth(text) <= 80);
    try std.testing.expect(std.mem.indexOf(u8, text, "anthropic/claude-sonnet-4-5") != null);

    const dropped = try render(std.testing.allocator, &state, .{ .width = 60 });
    defer std.testing.allocator.free(dropped);
    try std.testing.expect(tui_text.visibleWidth(dropped) <= 60);
    try std.testing.expect(std.mem.indexOf(u8, dropped, "turns") == null);
    try std.testing.expect(std.mem.indexOf(u8, dropped, "medium") != null);
    try std.testing.expect(std.mem.indexOf(u8, dropped, "…") != null);
}

test "status bar shrinks the context segment before dropping other segments" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModelWithContext(std.testing.allocator, "claude-sonnet-4-5", "anthropic", 200_000);
    state.telemetry.estimated_tokens = 10_000;
    state.telemetry.context_window = 200_000;

    const text = try render(std.testing.allocator, &state, .{ .width = 70 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(tui_text.visibleWidth(text) <= 70);
    try std.testing.expect(std.mem.indexOf(u8, text, "10k/200k") == null);
    try std.testing.expect(std.mem.indexOf(u8, text, "5%") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "turns:0") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "…") == null);
}

test "status bar drops the ask permission segment but never bypass" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModel(std.testing.allocator, "claude", "anthropic");
    state.permission_mode = .ask;

    const ask = try render(std.testing.allocator, &state, .{ .width = 24 });
    defer std.testing.allocator.free(ask);
    try std.testing.expect(std.mem.indexOf(u8, ask, "ask") == null);
    try std.testing.expect(std.mem.indexOf(u8, ask, "idle") != null);

    state.permission_mode = .bypass;
    const bypass = try render(std.testing.allocator, &state, .{ .width = 24 });
    defer std.testing.allocator.free(bypass);
    try std.testing.expect(std.mem.indexOf(u8, bypass, "bypass") != null);
    try std.testing.expect(std.mem.indexOf(u8, bypass, "idle") != null);
}

test "status bar keeps whole segments monotonically as width grows" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModelWithContext(std.testing.allocator, "claude-sonnet-4-5", "anthropic", 200_000);
    state.thinking_level = .medium;
    state.status.turn_count = 13;

    var had_think = false;
    var had_turns = false;
    for ([_]usize{ 30, 60, 80, 100, 120, 160, 200 }) |width| {
        const text = try render(std.testing.allocator, &state, .{ .width = width });
        defer std.testing.allocator.free(text);
        try std.testing.expect(tui_text.visibleWidth(text) <= width);
        try std.testing.expect(std.mem.indexOf(u8, text, "idle") != null);
        const has_think = std.mem.indexOf(u8, text, "medium") != null;
        const has_turns = std.mem.indexOf(u8, text, "turns:13") != null;
        try std.testing.expect(has_think or !had_think);
        try std.testing.expect(has_turns or !had_turns);
        had_think = has_think;
        had_turns = has_turns;
    }
    try std.testing.expect(had_think);
    try std.testing.expect(had_turns);
}

test "status bar drops the model segment before the state segment" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModel(std.testing.allocator, "claude-opus-4-6-with-a-very-long-name", "anthropic");

    const text = try render(std.testing.allocator, &state, .{ .width = 20 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(tui_text.visibleWidth(text) <= 20);
    try std.testing.expect(std.mem.indexOf(u8, text, "…") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "claude-opus") == null);
    try std.testing.expect(std.mem.indexOf(u8, text, "idle") != null);
}

test "status bar keeps the state and bypass segments when nothing else fits" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModel(std.testing.allocator, "claude-opus", "claude");

    const text = try render(std.testing.allocator, &state, .{ .width = 20 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(tui_text.visibleWidth(text) <= 20);
    try std.testing.expect(std.mem.indexOf(u8, text, "…") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "bypass") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "idle") != null);
}

test "status bar clips to the width when even the kept segments overflow" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.permission_mode = .bypass;

    const text = try render(std.testing.allocator, &state, .{ .width = 8 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(tui_text.visibleWidth(text) <= 8);
    try std.testing.expect(std.mem.indexOf(u8, text, "…") != null);
}

test "cwd row right-aligns the working directory" {
    const row = try renderCwdRow(std.testing.allocator, "~/focus/open-agent-protocol", 60);
    defer std.testing.allocator.free(row);

    try std.testing.expectEqual(@as(usize, 60), tui_text.visibleWidth(row));
    try std.testing.expect(std.mem.endsWith(u8, row, "~/focus/open-agent-protocol" ++ zz.ansi.reset));
}

test "cwd row left-truncates a directory longer than the width" {
    const row = try renderCwdRow(std.testing.allocator, "/Users/lsm/focus/open-agent-protocol", 24);
    defer std.testing.allocator.free(row);

    try std.testing.expect(tui_text.visibleWidth(row) <= 24);
    try std.testing.expect(std.mem.indexOf(u8, row, "…") != null);
    try std.testing.expect(std.mem.endsWith(u8, row, "open-agent-protocol" ++ zz.ansi.reset));
}

fn renderCwdRowProbe(allocator: std.mem.Allocator) !void {
    const row = try renderCwdRow(allocator, "~/focus/open-agent-protocol", 24);
    allocator.free(row);
}

test "renderCwdRow survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, renderCwdRowProbe, .{});
}
