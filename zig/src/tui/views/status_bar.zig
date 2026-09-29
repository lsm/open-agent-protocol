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

const SegmentKind = enum { model, context, queue, perm, cost, backpressure, drops, think, turns, state, rate };

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
    .{ .kind = .rate },
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
    try pushValue(&segments, allocator, .think, @tagName(state.thinking_level), tui_theme.statusSegment());
    try pushOwnedSegment(&segments, allocator, .turns, "turns", try std.fmt.allocPrint(allocator, "{d}", .{state.status.turn_count}));
    try writeState(&segments, allocator, state);
    try writeRate(&segments, allocator, state);

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
    return tui_text.truncateToWidth(allocator, left, options.width) catch |err| switch (err) {
        error.WriteFailed => error.OutOfMemory,
        else => err,
    };
}

pub fn renderCwdRow(allocator: std.mem.Allocator, display: []const u8, branch: []const u8, width: usize) ![]u8 {
    return renderCwdRowImpl(allocator, display, branch, width) catch |err| switch (err) {
        error.WriteFailed => error.OutOfMemory,
        else => |e| e,
    };
}

fn renderCwdRowImpl(allocator: std.mem.Allocator, display: []const u8, branch: []const u8, width: usize) ![]u8 {
    const path_width = tui_text.visibleWidth(display);
    const branch_width = tui_text.visibleWidth(branch);
    const show_branch = branch_width > 0 and path_width +| hint_gap +| branch_width <= width;
    const clipped = try tui_text.takeTrailingWidth(allocator, display, width);
    defer allocator.free(clipped);
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    try tui_theme.palette.muted.writeFg(&out.writer);
    try zz.ansi.sgr(&out.writer, "2");
    try out.writer.writeAll(clipped);
    const left = tui_text.visibleWidth(clipped);
    if (show_branch) {
        const gap = width - left -| branch_width;
        for (0..gap) |_| try out.writer.writeByte(' ');
        try out.writer.writeAll(branch);
    } else {
        for (left..width) |_| try out.writer.writeByte(' ');
    }
    try out.writer.writeAll(zz.ansi.reset);
    return out.toOwnedSlice();
}

fn contextTokens(state: *const tui_state.AppState) u64 {
    return if (state.telemetry.estimated_tokens > 0) state.telemetry.estimated_tokens else state.status.context_used;
}

fn appendHint(allocator: std.mem.Allocator, left: []const u8, hint: []const u8, width: usize) ![]u8 {
    const left_width = tui_text.visibleWidth(left);
    const hint_width = tui_text.visibleWidth(hint);
    if (left_width + hint_gap + hint_width > width) return allocator.dupe(u8, left);
    const styled = try renderStyled(allocator, tui_theme.keyHint(), hint);
    defer allocator.free(styled);
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    out.writer.writeAll(left) catch return error.OutOfMemory;
    const pad = width - left_width - hint_width;
    for (0..pad) |_| out.writer.writeByte(' ') catch return error.OutOfMemory;
    out.writer.writeAll(styled) catch return error.OutOfMemory;
    return out.toOwnedSlice();
}

fn fitLayout(allocator: std.mem.Allocator, segments: []const Segment, mask: []const bool, width: usize, hint: []const u8) !?[]u8 {
    var cut = false;
    for (mask) |dropped| cut = cut or dropped;
    const left = try layoutKept(allocator, segments, mask, cut);
    errdefer allocator.free(left);
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
    const sep = try renderStyled(allocator, tui_theme.faint(), " " ++ tui_theme.glyph.sep ++ " ");
    defer allocator.free(sep);
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    var written: usize = 0;
    for (segments, mask) |seg, dropped| {
        if (dropped) continue;
        if (written > 0) out.writer.writeAll(sep) catch return error.OutOfMemory;
        out.writer.writeAll(seg.styled) catch return error.OutOfMemory;
        written += 1;
    }
    if (show_cut and written > 0) {
        const ellipsis = try renderStyled(allocator, tui_theme.dim(), "…");
        defer allocator.free(ellipsis);
        out.writer.writeAll(sep) catch return error.OutOfMemory;
        out.writer.writeAll(ellipsis) catch return error.OutOfMemory;
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

fn renderStyled(allocator: std.mem.Allocator, style: zz.Style, text: []const u8) ![]u8 {
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    writeStyled(&out.writer, style, text) catch return error.OutOfMemory;
    return out.toOwnedSlice();
}

fn writeStyled(writer: *std.Io.Writer, style: zz.Style, text: []const u8) !void {
    try style.foreground.writeFg(writer);
    if (style.bold_attr orelse false) try zz.ansi.sgr(writer, "1");
    if (style.dim_attr orelse false) try zz.ansi.sgr(writer, "2");
    try writer.writeAll(text);
    try writer.writeAll(zz.ansi.reset);
}

fn renderGauge(allocator: std.mem.Allocator, pct: u64) ![]u8 {
    const filled: usize = @intCast(@min(gauge_cells, (pct * gauge_cells + 50) / 100));
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    gaugeColor(pct).writeFg(writer) catch return error.OutOfMemory;
    for (0..filled) |_| writer.writeAll(tui_theme.glyph.gauge_on) catch return error.OutOfMemory;
    zz.ansi.sgr(writer, "0") catch return error.OutOfMemory;
    tui_theme.palette.faint.writeFg(writer) catch return error.OutOfMemory;
    for (filled..gauge_cells) |_| writer.writeAll(tui_theme.glyph.gauge_off) catch return error.OutOfMemory;
    writer.writeAll(zz.ansi.reset) catch return error.OutOfMemory;
    return out.toOwnedSlice();
}

fn contextText(allocator: std.mem.Allocator, state: *const tui_state.AppState, compact: bool) ![]const u8 {
    const used: u64 = if (state.telemetry.estimated_tokens > 0) state.telemetry.estimated_tokens else state.status.context_used;
    const limit: u64 = if (state.telemetry.context_window > 0) state.telemetry.context_window else state.status.context_limit;
    const pct: u64 = if (limit > 0) @min(100, (used * 100) / limit) else 0;
    const used_text = try tui_text.compactNumber(allocator, used);
    defer allocator.free(used_text);
    if (limit == 0) {
        if (compact) return renderStyled(allocator, tui_theme.statusSegment(), used_text);
        const gauge_text = try renderGauge(allocator, pct);
        defer allocator.free(gauge_text);
        const value = try std.fmt.allocPrint(allocator, "{s} {s} ctx", .{ gauge_text, used_text });
        defer allocator.free(value);
        return renderStyled(allocator, tui_theme.statusSegment(), value);
    }
    const pct_text = try std.fmt.allocPrint(allocator, "{d}%", .{pct});
    defer allocator.free(pct_text);
    const styled_pct = try renderStyled(allocator, (zz.Style{}).fg(gaugeColor(pct)).inline_style(true), pct_text);
    if (compact) return styled_pct;
    defer allocator.free(styled_pct);
    const gauge_text = try renderGauge(allocator, pct);
    defer allocator.free(gauge_text);
    const limit_text = try tui_text.compactNumber(allocator, limit);
    defer allocator.free(limit_text);
    const tokens = try std.fmt.allocPrint(allocator, "{s}/{s}", .{ used_text, limit_text });
    defer allocator.free(tokens);
    const styled_tokens = try renderStyled(allocator, tui_theme.statusSegment(), tokens);
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

fn writeRate(list: *SegmentList, allocator: std.mem.Allocator, state: *const tui_state.AppState) !void {
    const rate = &state.telemetry.rate;
    const shown = rate.shown();
    if (!shown.measured()) return;
    var buf: [16]u8 = undefined;
    const mark = if (shown.estimated) "~" else "";
    const value = std.fmt.bufPrint(&buf, "{s}{d} tok/s", .{ mark, shown.perSecond() }) catch return;
    try pushValue(list, allocator, .rate, value, tui_theme.statusSegment());
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
    const styled_key = try renderStyled(allocator, tui_theme.statusKey(), key);
    defer allocator.free(styled_key);
    const styled_value = try renderStyled(allocator, value_style, value);
    defer allocator.free(styled_value);
    const styled = try std.fmt.allocPrint(allocator, "{s}:{s}", .{ styled_key, styled_value });
    try pushOwned(list, allocator, kind, styled);
}

fn pushValue(list: *SegmentList, allocator: std.mem.Allocator, kind: SegmentKind, value: []const u8, value_style: zz.Style) !void {
    try pushOwned(list, allocator, kind, try renderStyled(allocator, value_style, value));
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

fn rateProbe(measured: bool) !tui_state.TokenRateSet {
    var rate = tui_state.TokenRateSet{};
    rate.produced(400, 1_000);
    rate.messageEnded(2_000, 100);
    rate.turnEnded();
    rate.previous.estimated = !measured;
    rate.average = rate.previous;
    return rate;
}

test "the rate shows the last turn's measured figure unmarked" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModelWithContext(std.testing.allocator, "claude-sonnet-4-5", "anthropic", 200_000);
    state.telemetry.rate = try rateProbe(true);

    const text = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "100 tok/s") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "~100 tok/s") == null);
}

test "a rate that is an estimate is marked, so a mark always means an estimate" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModelWithContext(std.testing.allocator, "claude-sonnet-4-5", "anthropic", 200_000);
    state.telemetry.rate = try rateProbe(false);

    const text = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "~100 tok/s") != null);
}

test "the rate drops before any other segment when the row overflows" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModelWithContext(std.testing.allocator, "claude-sonnet-4-5", "anthropic", 200_000);
    state.status.turn_count = 4;
    state.telemetry.rate = try rateProbe(true);

    const narrow = try render(std.testing.allocator, &state, .{ .width = 30 });
    defer std.testing.allocator.free(narrow);
    try std.testing.expect(std.mem.indexOf(u8, narrow, "tok/s") == null);

    const wide = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(wide);
    try std.testing.expect(std.mem.indexOf(u8, wide, "tok/s") != null);
}

test "a session with no figure yet shows no rate segment" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModelWithContext(std.testing.allocator, "claude-sonnet-4-5", "anthropic", 200_000);

    const text = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "tok/s") == null);
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
    try std.testing.expect(std.mem.indexOf(u8, text, "turns") != null);

    const mid = try render(std.testing.allocator, &state, .{ .width = 90, .hint = "⏎ send · esc clear" });
    defer std.testing.allocator.free(mid);
    try std.testing.expectEqual(@as(usize, 90), tui_text.visibleWidth(mid));
    try std.testing.expect(std.mem.indexOf(u8, mid, "esc clear") != null);
    try std.testing.expect(std.mem.indexOf(u8, mid, "anthropic/claude-sonnet-4-5") != null);
    try std.testing.expect(std.mem.indexOf(u8, mid, "turns") != null);

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

test "status bar shows off when thinking is off" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.thinking_level = .off;

    const text = try render(std.testing.allocator, &state, .{ .width = 160 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(std.mem.indexOf(u8, text, "off") != null);
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

    const turns_dropped = try render(std.testing.allocator, &state, .{ .width = 62 });
    defer std.testing.allocator.free(turns_dropped);
    try std.testing.expect(tui_text.visibleWidth(turns_dropped) <= 62);
    try std.testing.expect(std.mem.indexOf(u8, turns_dropped, "turns") == null);
    try std.testing.expect(std.mem.indexOf(u8, turns_dropped, "medium") != null);
    try std.testing.expect(std.mem.indexOf(u8, turns_dropped, "…") != null);

    const think_dropped = try render(std.testing.allocator, &state, .{ .width = 60 });
    defer std.testing.allocator.free(think_dropped);
    try std.testing.expect(tui_text.visibleWidth(think_dropped) <= 60);
    try std.testing.expect(std.mem.indexOf(u8, think_dropped, "turns") == null);
    try std.testing.expect(std.mem.indexOf(u8, think_dropped, "medium") == null);
    try std.testing.expect(std.mem.indexOf(u8, think_dropped, "anthropic/claude-sonnet-4-5") != null);
    try std.testing.expect(std.mem.indexOf(u8, think_dropped, "…") != null);
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
    try std.testing.expect(std.mem.indexOf(u8, text, "turns") != null);
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
        const has_turns = std.mem.indexOf(u8, text, "turns") != null;
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

test "cwd row left-aligns the path and right-aligns the branch" {
    const row = try renderCwdRow(std.testing.allocator, "~/focus/open-agent-protocol", "main", 60);
    defer std.testing.allocator.free(row);

    try std.testing.expectEqual(@as(usize, 60), tui_text.visibleWidth(row));
    try std.testing.expect(std.mem.endsWith(u8, row, "main" ++ zz.ansi.reset));
    const path_at = std.mem.indexOf(u8, row, "~/focus/open-agent-protocol").?;
    const branch_at = std.mem.indexOf(u8, row, "main").?;
    try std.testing.expect(branch_at > path_at + "~/focus/open-agent-protocol".len);
    try std.testing.expect(tui_text.visibleWidth(row[0..path_at]) == 0);
}

test "cwd row drops the branch before it drops the path" {
    const fits = try renderCwdRow(std.testing.allocator, "~/work", "main", 20);
    defer std.testing.allocator.free(fits);
    try std.testing.expect(tui_text.visibleWidth(fits) == 20);
    try std.testing.expect(std.mem.endsWith(u8, fits, "main" ++ zz.ansi.reset));

    const narrow = try renderCwdRow(std.testing.allocator, "~/work", "main", 8);
    defer std.testing.allocator.free(narrow);
    try std.testing.expect(tui_text.visibleWidth(narrow) == 8);
    try std.testing.expect(std.mem.indexOf(u8, narrow, "main") == null);
    try std.testing.expect(std.mem.indexOf(u8, narrow, "~/work") != null);
}

test "cwd row left-aligns the path when there is no branch" {
    const row = try renderCwdRow(std.testing.allocator, "~/focus/open-agent-protocol", "", 60);
    defer std.testing.allocator.free(row);

    try std.testing.expectEqual(@as(usize, 60), tui_text.visibleWidth(row));
    try std.testing.expect(std.mem.endsWith(u8, row, zz.ansi.reset));
    const path_at = std.mem.indexOf(u8, row, "~/focus/open-agent-protocol").?;
    try std.testing.expect(tui_text.visibleWidth(row[0..path_at]) == 0);
}

test "cwd row left-truncates a directory longer than the width and drops the branch" {
    const row = try renderCwdRow(std.testing.allocator, "/Users/lsm/focus/open-agent-protocol", "main", 24);
    defer std.testing.allocator.free(row);

    try std.testing.expect(tui_text.visibleWidth(row) == 24);
    try std.testing.expect(std.mem.indexOf(u8, row, "…") != null);
    try std.testing.expect(std.mem.indexOf(u8, row, "main") == null);
    try std.testing.expect(std.mem.endsWith(u8, row, "open-agent-protocol" ++ zz.ansi.reset));
}

test "cwd row keeps a path that only just fits beside the branch" {
    const row = try renderCwdRow(std.testing.allocator, "~/a/b", "main", 12);
    defer std.testing.allocator.free(row);

    try std.testing.expect(tui_text.visibleWidth(row) == 12);
    try std.testing.expect(std.mem.indexOf(u8, row, "…") == null);
    try std.testing.expect(std.mem.endsWith(u8, row, "main" ++ zz.ansi.reset));
}

fn renderCwdRowProbe(allocator: std.mem.Allocator) !void {
    const row = try renderCwdRow(allocator, "~/focus/open-agent-protocol", "main", 24);
    allocator.free(row);
}

test "renderCwdRow survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, renderCwdRowProbe, .{});
}

fn renderProbe(allocator: std.mem.Allocator) !void {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModelWithContext(std.testing.allocator, "claude-sonnet-4-5", "anthropic", 200_000);
    state.thinking_level = .medium;
    state.status.turn_count = 13;
    state.status.context_used = 90_000;

    const text = try render(allocator, &state, .{ .width = 60, .hint = "? help" });
    allocator.free(text);
}

test "status bar render survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, renderProbe, .{});
}
