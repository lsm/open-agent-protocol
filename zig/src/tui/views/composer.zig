const std = @import("std");
const zz = @import("zigzag");
const tui_state = @import("tui_state");
const tui_theme = @import("tui_theme");
const tui_text = @import("tui_text");

pub const Options = struct {
    width: usize = 80,
    max_rows: usize = max_content_rows,
};

pub const max_content_rows: usize = 12;
pub const min_panel_rows: usize = 3;
const prompt_width: usize = 2;
const cursor_blank = " ";
const placeholder_text = "Ask Makai…";

pub fn contentWidth(width: usize) usize {
    return @max(width, 20) -| 4 -| prompt_width;
}

pub fn rowCap(height: usize) usize {
    return @max(1, @min(max_content_rows, height / 3));
}

pub fn adjustScroll(allocator: std.mem.Allocator, state: *tui_state.AppState, width: usize, height: usize) !void {
    const composer = &state.composer;
    if (composer.text().len == 0 or (state.mode == .login_input and state.login_input_secret)) {
        composer.scroll_row = 0;
        return;
    }
    const content_width = contentWidth(width);
    const rows = try tui_text.layoutRows(allocator, composer.text(), content_width);
    defer allocator.free(rows);
    const pos = tui_text.cursorPos(rows, composer.text(), composer.cursor);
    const cap = rowCap(height);
    if (composer.scroll_row > pos.row) composer.scroll_row = pos.row;
    if (pos.row >= composer.scroll_row + cap) composer.scroll_row = pos.row + 1 - cap;
    composer.scroll_row = @min(composer.scroll_row, rows.len -| cap);
}

pub fn render(allocator: std.mem.Allocator, state: *const tui_state.AppState, options: Options) ![]const u8 {
    const width = @max(options.width, 20);
    const inner = width - 4;
    const block = try renderInput(allocator, state, inner, options.max_rows);
    defer allocator.free(block.text);
    const border = borderColor(state);
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    try writeBorderRow(writer, border, width, true, block.hidden_above);
    var lines = std.mem.splitScalar(u8, block.text, '\n');
    while (lines.next()) |line| {
        try writer.writeByte('\n');
        try writeBodyRow(writer, border, line, inner);
    }
    try writer.writeByte('\n');
    try writeBorderRow(writer, border, width, false, block.hidden_below);
    return out.toOwnedSlice();
}

pub fn borderColor(state: *const tui_state.AppState) zz.Color {
    if (state.mode == .approval) return tui_theme.palette.warning;
    if (state.mode == .login_input) return tui_theme.palette.thinking;
    if (state.composer.text().len > 0) return tui_theme.palette.accent;
    return tui_theme.palette.panel_border;
}

pub fn hintText(allocator: std.mem.Allocator, state: *const tui_state.AppState) ![]u8 {
    const text = state.composer.text();
    const k = tui_theme.key;
    switch (state.mode) {
        .approval => return allocator.dupe(u8, "y allow · a always · n deny · esc abort"),
        .login_input => return std.fmt.allocPrint(allocator, "{s} submit · esc cancel", .{k.enter}),
        .picker => return std.fmt.allocPrint(allocator, "type to filter · {s} move · {s} select · esc close", .{ k.up_down, k.enter }),
        .session_picker => return std.fmt.allocPrint(allocator, "{s} move · {s} select · esc close", .{ k.up_down, k.enter }),
        .normal => {},
    }
    if (std.mem.startsWith(u8, text, "/")) return std.fmt.allocPrint(allocator, "{s} select · {s} complete · {s} run · esc clear", .{ k.up_down, k.tab, k.enter });
    if (state.status.compacting) return std.fmt.allocPrint(allocator, "compacting · {s} queue · esc cancel", .{k.enter});
    if (state.status.streaming) {
        const queued = state.queue.total();
        if (queued > 0) return std.fmt.allocPrint(allocator, "{s} steer · {s} queue · queued {d} · esc abort", .{ k.enter, k.tab, queued });
        return std.fmt.allocPrint(allocator, "{s} steer · {s} queue · esc abort", .{ k.enter, k.tab });
    }
    if (std.mem.startsWith(u8, text, "!")) return std.fmt.allocPrint(allocator, "shell mode · {s} runs the command through the agent", .{k.enter});
    if (std.mem.startsWith(u8, text, "@")) return allocator.dupe(u8, "file picker · type a path or query");
    if (state.composer.history.items.len > 0) {
        return std.fmt.allocPrint(allocator, "{s} history · {s}{s} newline · / commands", .{ k.up_down, k.shift, k.enter });
    }
    return std.fmt.allocPrint(allocator, "{s} send · {s}{s} newline · / commands · {s}C quit", .{ k.enter, k.shift, k.enter, k.ctrl });
}

const InputBlock = struct {
    text: []u8,
    hidden_above: usize = 0,
    hidden_below: usize = 0,
};

fn renderInput(allocator: std.mem.Allocator, state: *const tui_state.AppState, inner_width: usize, max_rows: usize) !InputBlock {
    const content_width = inner_width -| prompt_width;
    if (content_width == 0) return .{ .text = try allocator.dupe(u8, promptFor(state)) };
    if (state.mode == .login_input and state.login_input_secret and state.composer.text().len > 0) {
        const masked = try maskedSecretInput(allocator, state.composer.text());
        defer allocator.free(masked);
        const row = try renderMaskedRow(allocator, masked, content_width);
        defer allocator.free(row);
        return .{ .text = try prefixRow(allocator, state, row) };
    }
    if (state.composer.text().len == 0) {
        const row = try renderPlaceholderRow(allocator, state, content_width);
        defer allocator.free(row);
        return .{ .text = try prefixRow(allocator, state, row) };
    }
    const text = state.composer.text();
    const rows = try tui_text.layoutRows(allocator, text, content_width);
    defer allocator.free(rows);
    const pos = tui_text.cursorPos(rows, text, state.composer.cursor);
    const cap = @max(max_rows, 1);
    const max_scroll = rows.len -| cap;
    const scroll = @min(state.composer.scroll_row, max_scroll);
    const shown = @min(rows.len - scroll, cap);
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    var k: usize = 0;
    while (k < shown) : (k += 1) {
        const row_index = scroll + k;
        if (k > 0) try writer.writeByte('\n');
        if (k == 0) try writePrompt(writer, promptFor(state)) else try writer.writeAll("  ");
        const r = rows[row_index];
        if (row_index == pos.row) {
            const cursor = @min(state.composer.cursor, text.len);
            try tui_text.writeDisplayEscaped(writer, text[r.start..cursor]);
            try writeCursorAt(writer, text, cursor);
            if (cursor < r.end and text[cursor] != '\n') {
                const len = std.unicode.utf8ByteSequenceLength(text[cursor]) catch 1;
                try tui_text.writeDisplayEscaped(writer, text[@min(r.end, cursor + len)..r.end]);
            }
        } else {
            try tui_text.writeDisplayEscaped(writer, text[r.start..r.end]);
        }
    }
    return .{
        .text = try out.toOwnedSlice(),
        .hidden_above = scroll,
        .hidden_below = rows.len - (scroll + shown),
    };
}

fn prefixRow(allocator: std.mem.Allocator, state: *const tui_state.AppState, row: []const u8) ![]u8 {
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    try writePrompt(&out.writer, promptFor(state));
    try out.writer.writeAll(row);
    return out.toOwnedSlice();
}

fn renderPlaceholderRow(allocator: std.mem.Allocator, state: *const tui_state.AppState, content_width: usize) ![]u8 {
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    try writeCursorCell(writer, cursor_blank);
    const source = try placeholderFor(allocator, state);
    defer allocator.free(source);
    const clipped = try tui_text.truncateLineToWidth(allocator, source, content_width -| cursor_blank.len);
    defer allocator.free(clipped);
    try writeMuted(writer, clipped);
    return out.toOwnedSlice();
}

fn renderMaskedRow(allocator: std.mem.Allocator, masked: []const u8, width: usize) ![]u8 {
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    if (tui_text.visibleWidth(masked) + cursor_blank.len <= width) {
        try writer.writeAll(masked);
    } else {
        const before = try takeTrailingWidth(allocator, masked, width -| cursor_blank.len);
        defer allocator.free(before);
        try writer.writeAll(before);
    }
    try writeCursorCell(writer, cursor_blank);
    return out.toOwnedSlice();
}

fn placeholderFor(allocator: std.mem.Allocator, state: *const tui_state.AppState) ![]u8 {
    if (state.mode == .login_input) return allocator.dupe(u8, if (state.login_input_secret) "paste the secret and press Enter" else "type your answer and press Enter");
    if (state.mode == .approval) {
        const queued = state.queue.total();
        if (queued > 0) return std.fmt.allocPrint(allocator, "{d} queued · y / a / n to decide", .{queued});
        return allocator.dupe(u8, "y / a / n to decide, or type /abort");
    }
    if (state.status.compacting) return allocator.dupe(u8, "compacting the conversation · type now, it sends when done…");
    if (state.status.streaming) {
        const queued = state.queue.total();
        if (queued > 0) return std.fmt.allocPrint(allocator, "{d} queued · type to steer or queue more…", .{queued});
        return std.fmt.allocPrint(allocator, "type to steer the running turn · {s} queues a follow-up…", .{tui_theme.key.tab});
    }
    return allocator.dupe(u8, placeholder_text);
}

fn maskedSecretInput(allocator: std.mem.Allocator, text: []const u8) ![]u8 {
    if (text.len == 0) return allocator.dupe(u8, "");
    const mask = try allocator.alloc(u8, text.len);
    @memset(mask, '*');
    return mask;
}

fn promptFor(state: *const tui_state.AppState) []const u8 {
    const text = state.composer.text();
    if (std.mem.startsWith(u8, text, "!")) return "! ";
    if (std.mem.startsWith(u8, text, "@")) return "@ ";
    return tui_theme.glyph.prompt ++ " ";
}

fn writePrompt(writer: *std.Io.Writer, prompt: []const u8) !void {
    try tui_theme.palette.accent.writeFg(writer);
    try zz.ansi.sgr(writer, "1");
    try writer.writeAll(prompt);
    try writer.writeAll(zz.ansi.reset);
}

fn writeMuted(writer: *std.Io.Writer, text: []const u8) !void {
    try tui_theme.palette.muted.writeFg(writer);
    try zz.ansi.sgr(writer, "2");
    try writer.writeAll(text);
    try writer.writeAll(zz.ansi.reset);
}

fn writeCursorCell(writer: *std.Io.Writer, cell: []const u8) !void {
    try writer.writeAll("\x1b[7m");
    try writer.writeAll(cell);
    try writer.writeAll("\x1b[27m");
}

fn writeCursorAt(writer: *std.Io.Writer, text: []const u8, cursor: usize) !void {
    if (cursor >= text.len or text[cursor] == '\n') return writeCursorCell(writer, cursor_blank);
    const len = std.unicode.utf8ByteSequenceLength(text[cursor]) catch 1;
    try writer.writeAll("\x1b[7m");
    try tui_text.writeDisplayEscaped(writer, text[cursor..@min(text.len, cursor + len)]);
    try writer.writeAll("\x1b[27m");
}

fn writeBodyRow(writer: *std.Io.Writer, border: zz.Color, content: []const u8, inner: usize) !void {
    try border.writeFg(writer);
    try writer.writeAll(zz.Border.rounded.vertical);
    try writer.writeAll(zz.ansi.reset);
    try writer.writeByte(' ');
    try writer.writeAll(content);
    const pad = inner -| tui_text.visibleWidth(content);
    for (0..pad) |_| try writer.writeByte(' ');
    try writer.writeByte(' ');
    try border.writeFg(writer);
    try writer.writeAll(zz.Border.rounded.vertical);
    try writer.writeAll(zz.ansi.reset);
}

fn writeBorderRow(writer: *std.Io.Writer, border: zz.Color, width: usize, top: bool, hidden: usize) !void {
    const glyphs = zz.Border.rounded;
    const left = if (top) glyphs.top_left else glyphs.bottom_left;
    const right = if (top) glyphs.top_right else glyphs.bottom_right;
    const arrow: []const u8 = if (top) "\u{25b2}" else "\u{25bc}";
    var marker_buf: [24]u8 = undefined;
    const marker = if (hidden > 0) std.fmt.bufPrint(&marker_buf, "{s} {d}", .{ arrow, hidden }) catch arrow else "";
    const marker_width = tui_text.visibleWidth(marker);
    try border.writeFg(writer);
    try writer.writeAll(left);
    if (marker_width == 0 or width < marker_width + 7) {
        for (0..width -| 2) |_| try writer.writeAll(glyphs.horizontal);
        try writer.writeAll(right);
        return writer.writeAll(zz.ansi.reset);
    }
    try writer.writeAll(glyphs.horizontal);
    try writer.writeAll(zz.ansi.reset);
    try writer.writeByte(' ');
    try writeMuted(writer, marker);
    try border.writeFg(writer);
    try writer.writeByte(' ');
    for (0..width - 5 - marker_width) |_| try writer.writeAll(glyphs.horizontal);
    try writer.writeAll(right);
    try writer.writeAll(zz.ansi.reset);
}

fn takeTrailingWidth(allocator: std.mem.Allocator, text: []const u8, width: usize) ![]u8 {
    if (width == 0 or text.len == 0) return allocator.dupe(u8, "");
    if (tui_text.visibleWidth(text) <= width and std.mem.indexOfScalar(u8, text, '\n') == null) return allocator.dupe(u8, text);
    var start = text.len;
    var visible: usize = 0;
    while (start > 0 and visible < width -| 1) {
        const cp_start = previousCodepointStart(text, start);
        const cp = text[cp_start..start];
        if (cp.len == 1 and cp[0] == '\n') break;
        visible += tui_text.visibleWidth(cp);
        if (visible > width -| 1) break;
        start = cp_start;
    }
    return std.fmt.allocPrint(allocator, "…{s}", .{text[start..]});
}

fn previousCodepointStart(text: []const u8, cursor: usize) usize {
    if (cursor == 0) return 0;
    var idx = @min(cursor, text.len) - 1;
    while (idx > 0 and (text[idx] & 0b1100_0000) == 0b1000_0000) idx -= 1;
    return idx;
}

test "composer renders placeholder and text" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();

    const placeholder = try render(std.testing.allocator, &state, .{ .width = 80 });
    defer std.testing.allocator.free(placeholder);
    try std.testing.expect(std.mem.indexOf(u8, placeholder, "Ask Makai") != null);
    try std.testing.expect(std.mem.indexOf(u8, placeholder, cursor_blank) != null);
    try std.testing.expect(std.mem.indexOf(u8, placeholder, "\u{2588}") == null);
    try std.testing.expect(std.mem.indexOf(u8, placeholder, "╭") != null);
    try std.testing.expect((std.mem.indexOf(u8, placeholder, "\x1b[7m") orelse return error.MissingCursor) < (std.mem.indexOf(u8, placeholder, "Ask Makai") orelse return error.MissingPlaceholder));
    var lines = std.mem.splitScalar(u8, placeholder, '\n');
    while (lines.next()) |line| try std.testing.expectEqual(@as(usize, 80), tui_text.visibleWidth(line));

    try state.composer.buffer.appendSlice(std.testing.allocator, "hello world");
    state.composer.cursor = state.composer.buffer.items.len;
    const text = try render(std.testing.allocator, &state, .{ .width = 30 });
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, tui_theme.glyph.prompt) != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "hello world") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "\u{2588}") == null);
}

test "composer hint follows the interaction state" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();

    const idle = try hintText(std.testing.allocator, &state);
    defer std.testing.allocator.free(idle);
    try std.testing.expect(std.mem.indexOf(u8, idle, "send") != null);
    try std.testing.expect(std.mem.indexOf(u8, idle, "newline") != null);
    try std.testing.expect(std.mem.indexOf(u8, idle, "commands") != null);
    try std.testing.expect(std.mem.indexOf(u8, idle, "Ctrl+R") == null);

    try state.recordComposerHistory("earlier");
    const recall = try hintText(std.testing.allocator, &state);
    defer std.testing.allocator.free(recall);
    try std.testing.expect(std.mem.indexOf(u8, recall, "history") != null);

    state.status.streaming = true;
    state.queue.steering = 2;
    const streaming = try hintText(std.testing.allocator, &state);
    defer std.testing.allocator.free(streaming);
    try std.testing.expect(std.mem.indexOf(u8, streaming, tui_theme.key.enter ++ " steer") != null);
    try std.testing.expect(std.mem.indexOf(u8, streaming, tui_theme.key.tab ++ " queue") != null);
    try std.testing.expect(std.mem.indexOf(u8, streaming, "queued 2") != null);
    try std.testing.expect(std.mem.indexOf(u8, streaming, "Alt+Enter") == null);

    try state.replaceComposerBuffer("/ab");
    const streaming_slash = try hintText(std.testing.allocator, &state);
    defer std.testing.allocator.free(streaming_slash);
    try std.testing.expect(std.mem.indexOf(u8, streaming_slash, tui_theme.key.tab ++ " complete") != null);
    try std.testing.expect(std.mem.indexOf(u8, streaming_slash, "queue") == null);
    state.composer.clear();
    state.status.streaming = false;
    state.queue.steering = 0;

    try state.replaceComposerBuffer("!ls");
    const shell = try hintText(std.testing.allocator, &state);
    defer std.testing.allocator.free(shell);
    try std.testing.expect(std.mem.indexOf(u8, shell, "shell mode") != null);

    try state.replaceComposerBuffer("@src");
    const file = try hintText(std.testing.allocator, &state);
    defer std.testing.allocator.free(file);
    try std.testing.expect(std.mem.indexOf(u8, file, "file picker") != null);

    try state.replaceComposerBuffer("/mo");
    const slash = try hintText(std.testing.allocator, &state);
    defer std.testing.allocator.free(slash);
    try std.testing.expect(std.mem.indexOf(u8, slash, tui_theme.key.up_down ++ " select") != null);
    try std.testing.expect(std.mem.indexOf(u8, slash, "complete") != null);

    state.mode = .picker;
    const picker = try hintText(std.testing.allocator, &state);
    defer std.testing.allocator.free(picker);
    try std.testing.expect(std.mem.indexOf(u8, picker, "type to filter") != null);

    state.mode = .session_picker;
    const sessions = try hintText(std.testing.allocator, &state);
    defer std.testing.allocator.free(sessions);
    try std.testing.expect(std.mem.indexOf(u8, sessions, "filter") == null);

    state.mode = .approval;
    const approval = try hintText(std.testing.allocator, &state);
    defer std.testing.allocator.free(approval);
    try std.testing.expect(std.mem.indexOf(u8, approval, "allow") != null);
    try std.testing.expect(std.mem.indexOf(u8, approval, "deny") != null);
}

test "streaming placeholder carries the queued count at any width" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();

    state.status.streaming = true;
    const steering = try placeholderFor(std.testing.allocator, &state);
    defer std.testing.allocator.free(steering);
    try std.testing.expectEqualStrings("type to steer the running turn · " ++ tui_theme.key.tab ++ " queues a follow-up…", steering);

    state.queue.steering = 1;
    const queued = try placeholderFor(std.testing.allocator, &state);
    defer std.testing.allocator.free(queued);
    try std.testing.expect(std.mem.indexOf(u8, queued, "1 queued") != null);
    try std.testing.expect(std.mem.indexOf(u8, queued, "steer or queue more") != null);

    const rendered = try render(std.testing.allocator, &state, .{ .width = 30 });
    defer std.testing.allocator.free(rendered);
    try std.testing.expect(std.mem.indexOf(u8, rendered, "1 queued") != null);

    state.mode = .approval;
    const approval = try placeholderFor(std.testing.allocator, &state);
    defer std.testing.allocator.free(approval);
    try std.testing.expect(std.mem.indexOf(u8, approval, "y / a / n to decide") != null);
    try std.testing.expect(std.mem.indexOf(u8, approval, "1 queued") != null);

    const approval_rendered = try render(std.testing.allocator, &state, .{ .width = 30 });
    defer std.testing.allocator.free(approval_rendered);
    try std.testing.expect(std.mem.indexOf(u8, approval_rendered, "1 queued") != null);
}

test "composer border follows the mode and the draft but holds still while streaming" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try std.testing.expect(std.meta.eql(borderColor(&state), tui_theme.palette.panel_border));
    try state.replaceComposerBuffer("draft");
    try std.testing.expect(std.meta.eql(borderColor(&state), tui_theme.palette.accent));
    state.mode = .approval;
    try std.testing.expect(std.meta.eql(borderColor(&state), tui_theme.palette.warning));
    state.mode = .normal;
    state.status.streaming = true;
    try std.testing.expect(std.meta.eql(borderColor(&state), tui_theme.palette.accent));
    state.composer.clear();
    try std.testing.expect(std.meta.eql(borderColor(&state), tui_theme.palette.panel_border));

    const first = try render(std.testing.allocator, &state, .{ .width = 40 });
    defer std.testing.allocator.free(first);
    state.anim_tick += 7;
    const later = try render(std.testing.allocator, &state, .{ .width = 40 });
    defer std.testing.allocator.free(later);
    try std.testing.expectEqualStrings(first, later);
}

test "composer renders multiline draft content" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.composer.buffer.appendSlice(std.testing.allocator, "first line\nsecond line");
    state.composer.cursor = state.composer.buffer.items.len;

    const text = try render(std.testing.allocator, &state, .{ .width = 40 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(std.mem.indexOf(u8, text, "first line") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "second line") != null);
    var lines = std.mem.splitScalar(u8, text, '\n');
    while (lines.next()) |line| try std.testing.expectEqual(@as(usize, 40), tui_text.visibleWidth(line));
}

test "composer keeps a block cursor on a newline boundary" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.composer.buffer.appendSlice(std.testing.allocator, "first\nsecond");
    state.composer.cursor = 5;

    const block = try renderInput(std.testing.allocator, &state, 30, 6);
    defer std.testing.allocator.free(block.text);

    try std.testing.expect(std.mem.indexOf(u8, block.text, "first\x1b[7m \x1b[27m\n") != null);
    try std.testing.expect(std.mem.indexOf(u8, block.text, "second") != null);
}

test "composer masks secret login input" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.mode = .login_input;
    state.login_input_secret = true;
    try state.composer.buffer.appendSlice(std.testing.allocator, "sk-secret-value");
    state.composer.cursor = state.composer.buffer.items.len;

    const text = try render(std.testing.allocator, &state, .{ .width = 80 });
    defer std.testing.allocator.free(text);

    try std.testing.expect(std.mem.indexOf(u8, text, "sk-secret-value") == null);
    try std.testing.expect(std.mem.indexOf(u8, text, "***************") != null);
}

test "composer accounts for prompt width when wrapping text" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.composer.buffer.appendSlice(std.testing.allocator, "1234567890");
    state.composer.cursor = state.composer.buffer.items.len;

    const block = try renderInput(std.testing.allocator, &state, 8, 6);
    defer std.testing.allocator.free(block.text);

    var lines = std.mem.splitScalar(u8, block.text, '\n');
    while (lines.next()) |line| {
        try std.testing.expect(tui_text.visibleWidth(line) <= 8);
    }
}

test "composer renders block cursor at current position" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.replaceComposerBuffer("abc");
    state.composer.cursor = 1;

    const block = try renderInput(std.testing.allocator, &state, 20, 6);
    defer std.testing.allocator.free(block.text);

    try std.testing.expect(std.mem.indexOf(u8, block.text, "a\x1b[7mb\x1b[27mc") != null);
    try std.testing.expect(std.mem.indexOf(u8, block.text, "\u{2588}") == null);
}

test "composer puts the cursor on its own row after a full row" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.replaceComposerBuffer("123456");
    state.composer.cursor = 6;

    const block = try renderInput(std.testing.allocator, &state, 8, 6);
    defer std.testing.allocator.free(block.text);

    try std.testing.expectEqual(@as(usize, 2), tui_text.lineCount(block.text));
    try std.testing.expect(std.mem.indexOf(u8, block.text, "123456\n  \x1b[7m \x1b[27m") != null);
}

test "composer renders control bytes visibly instead of leaking them" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.replaceComposerBuffer("a\x1b[Hb\x07c\td");

    const block = try renderInput(std.testing.allocator, &state, 40, 6);
    defer std.testing.allocator.free(block.text);

    try std.testing.expect(std.mem.indexOf(u8, block.text, "a^[[Hb^Gc\u{2192}d\x1b[7m \x1b[27m") != null);
}

test "composer windows a long draft around the scroll row with border markers" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.replaceComposerBuffer("aaaa\nbbbb\ncccc\ndddd\neeee");
    state.composer.cursor = 0;
    state.composer.scroll_row = 1;

    const text = try render(std.testing.allocator, &state, .{ .width = 20, .max_rows = 2 });
    defer std.testing.allocator.free(text);

    try std.testing.expectEqual(@as(usize, 4), tui_text.lineCount(text));
    try std.testing.expect(std.mem.indexOf(u8, text, "\u{25b2} 1") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "\u{25bc} 2") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "bbbb") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "cccc") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "dddd") == null);
    var lines = std.mem.splitScalar(u8, text, '\n');
    while (lines.next()) |line| try std.testing.expectEqual(@as(usize, 20), tui_text.visibleWidth(line));
}

test "adjustScroll keeps the cursor row inside the window" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.replaceComposerBuffer("aaaa\nbbbb\ncccc\ndddd\neeee");

    try adjustScroll(std.testing.allocator, &state, 20, 12);
    try std.testing.expectEqual(@as(usize, 1), state.composer.scroll_row);

    state.composer.cursor = 0;
    try adjustScroll(std.testing.allocator, &state, 20, 12);
    try std.testing.expectEqual(@as(usize, 0), state.composer.scroll_row);

    state.composer.clear();
    try adjustScroll(std.testing.allocator, &state, 20, 12);
    try std.testing.expectEqual(@as(usize, 0), state.composer.scroll_row);
}

test "rowCap follows the terminal height with a floor of one" {
    try std.testing.expectEqual(@as(usize, 1), rowCap(0));
    try std.testing.expectEqual(@as(usize, 1), rowCap(5));
    try std.testing.expectEqual(@as(usize, 8), rowCap(24));
    try std.testing.expectEqual(@as(usize, 12), rowCap(60));
}
