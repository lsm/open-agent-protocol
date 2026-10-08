const std = @import("std");
const agent = @import("agent");
const ai_types = @import("ai_types");
const session_runtime = @import("session_runtime");
const compat = @import("compat");
const tui_config = @import("tui_config");

pub const AutoCompactSetting = tui_config.AutoCompact;
pub const Verbosity = tui_config.Verbosity;
pub const VerbosityLevel = tui_config.VerbosityLevel;
pub const VerbosityPart = tui_config.VerbosityPart;

pub fn autoCompactAt(setting: AutoCompactSetting, model: ai_types.Model) ?u64 {
    if (model.context_window == 0) return null;
    return switch (setting) {
        .off => null,
        .percent => |percent| agent.compaction.shareAt(model.context_window, percent),
        .tokens => |count| count,
        .auto => agent.compaction.autoCompactAt(model.context_window, model.max_tokens),
    };
}

pub fn autoCompactPolicyJson(buffer: []u8, setting: AutoCompactSetting) ![]const u8 {
    return switch (setting) {
        .off => std.fmt.bufPrint(buffer, "{{\"kind\":\"off\"}}", .{}),
        .auto => std.fmt.bufPrint(buffer, "{{\"kind\":\"auto\"}}", .{}),
        .percent => |percent| std.fmt.bufPrint(buffer, "{{\"kind\":\"share\",\"share_percent\":{d}}}", .{percent}),
        .tokens => |count| std.fmt.bufPrint(buffer, "{{\"kind\":\"tokens\",\"tokens\":{d}}}", .{count}),
    };
}

test "autocompact settings become the compaction policies OAP names" {
    var buffer: [96]u8 = undefined;
    try std.testing.expectEqualStrings("{\"kind\":\"off\"}", try autoCompactPolicyJson(&buffer, .off));
    try std.testing.expectEqualStrings("{\"kind\":\"auto\"}", try autoCompactPolicyJson(&buffer, .auto));
    try std.testing.expectEqualStrings("{\"kind\":\"share\",\"share_percent\":70}", try autoCompactPolicyJson(&buffer, .{ .percent = 70 }));
    try std.testing.expectEqualStrings("{\"kind\":\"tokens\",\"tokens\":4000}", try autoCompactPolicyJson(&buffer, .{ .tokens = 4000 }));
}

pub const AppMode = enum {
    normal,
    approval,
    session_picker,
    picker,
    login_input,
};

pub const PickerKind = enum {
    model,
    login,
    permission,
    settings,
};

pub const TranscriptKind = enum {
    user,
    assistant,
    thinking,
    tool,
    system,
    welcome,
    @"error",
};

pub const ToolStatus = enum {
    pending,
    running,
    done,
    @"error",
    interrupted,
};

pub const TerminalEvidence = enum {
    none,
    execution,
    result,
    both,
};

pub const ToolEventClass = enum {
    live_intent,
    execution_outcome,
    result_outcome,
};

const OccurrenceResolution = struct {
    tool: *ToolEntry,
    merge_state: bool,
};

pub fn isTerminalToolStatus(status: ToolStatus) bool {
    return status != .pending and status != .running;
}

pub const ApprovalStatus = enum {
    none,
    pending,
    approved,
    rejected,
};

pub const ZenNote = enum { none, enter, leave };

pub const zen_enter_note = "The user switched to zen mode and will only see your final message. Work without narrating: no progress updates or explanations between tool calls. When you finish, reply once, concisely: what changed or what you found, and anything that needs the user.";

pub const zen_leave_note = "The user left zen mode; respond normally.";

pub const Zen = struct {
    on: bool = false,
    start_index: usize = 0,
    note: ZenNote = .none,
    phase: f32 = 0,
    was_running: bool = false,
    ended_tick: ?u64 = null,
    shown: ZenLine = .{},
    incoming: ?ZenLine = null,
    queue: [zen_queue_lines]ZenLine = undefined,
    queue_len: usize = 0,
    move_tick: u64 = 0,
    step: ?usize = null,
    step_started_ms: i64 = 0,

    pub fn activity(self: *const Zen) []const u8 {
        return self.shown.text();
    }

    pub fn incomingActivity(self: *const Zen) []const u8 {
        return if (self.incoming) |*line| line.text() else "";
    }

    pub fn settled(self: *const Zen) bool {
        return self.incoming == null and self.queue_len == 0;
    }

    pub fn rise(self: *const Zen, tick: u64, slide: u64) f32 {
        if (self.incoming == null or slide == 0) return 0;
        const since = tick -% self.move_tick;
        return @min(@as(f32, @floatFromInt(since)) / @as(f32, @floatFromInt(slide)), 1);
    }

    pub fn beginRun(self: *Zen, tick: u64) void {
        self.shown = .{};
        self.incoming = null;
        self.queue_len = 0;
        self.move_tick = tick;
        self.step = null;
    }

    pub fn advance(self: *Zen, tick: u64, slide: u64) void {
        if (self.incoming) |line| {
            if (tick -% self.move_tick < slide) return;
            self.shown = line;
            self.incoming = null;
            self.move_tick +%= slide;
        }
        if (self.queue_len == 0) return;
        self.incoming = self.queue[0];
        std.mem.copyForwards(ZenLine, self.queue[0 .. self.queue_len - 1], self.queue[1..self.queue_len]);
        self.queue_len -= 1;
        if (tick -% self.move_tick >= slide) self.move_tick = tick;
    }

    pub fn stepMs(self: *Zen, timed: ?usize, now_ms: i64) u64 {
        const key = timed orelse {
            self.step = null;
            return 0;
        };
        if (self.step != key) {
            self.step = key;
            self.step_started_ms = now_ms;
        }
        if (now_ms <= self.step_started_ms) return 0;
        return @intCast(now_ms - self.step_started_ms);
    }

    pub fn noteActivity(self: *Zen, text: []const u8) void {
        const latest = if (self.queue_len > 0) self.queue[self.queue_len - 1].text() else if (self.incoming) |*line| line.text() else self.activity();
        const kept = utf8Prefix(text, zen_activity_bytes);
        if (std.mem.eql(u8, kept, latest)) return;
        if (self.queue_len == zen_queue_lines) {
            std.mem.copyForwards(ZenLine, self.queue[0 .. zen_queue_lines - 1], self.queue[1..zen_queue_lines]);
            self.queue_len -= 1;
        }
        self.queue[self.queue_len] = ZenLine.of(kept);
        self.queue_len += 1;
    }

    pub fn enter(self: *Zen, transcript_len: usize) void {
        if (self.on) return;
        self.on = true;
        self.start_index = transcript_len;
        self.was_running = false;
        self.ended_tick = null;
        self.note = if (self.note == .leave) .none else .enter;
    }

    pub fn leave(self: *Zen) void {
        if (!self.on) return;
        self.on = false;
        self.note = if (self.note == .enter) .none else .leave;
    }

    pub fn noteText(self: *const Zen) ?[]const u8 {
        return switch (self.note) {
            .none => null,
            .enter => zen_enter_note,
            .leave => zen_leave_note,
        };
    }
};

pub const zen_activity_bytes: usize = 256;
pub const zen_queue_lines: usize = 8;

pub const ZenLine = struct {
    bytes: [zen_activity_bytes]u8 = undefined,
    len: usize = 0,

    pub fn of(source: []const u8) ZenLine {
        var line: ZenLine = .{};
        const kept = utf8Prefix(source, zen_activity_bytes);
        @memcpy(line.bytes[0..kept.len], kept);
        line.len = kept.len;
        return line;
    }

    pub fn text(self: *const ZenLine) []const u8 {
        return self.bytes[0..self.len];
    }
};

fn utf8Prefix(text: []const u8, limit: usize) []const u8 {
    if (text.len <= limit) return text;
    var end = limit;
    while (end > 0 and (text[end] & 0xc0) == 0x80) end -= 1;
    return text[0..end];
}

pub fn withoutZenNote(text: []const u8) []const u8 {
    inline for (.{ zen_enter_note, zen_leave_note }) |note| {
        if (std.mem.startsWith(u8, text, note ++ "\n\n")) return text[note.len + 2 ..];
    }
    return text;
}

pub fn zenStart(state: *const AppState) usize {
    var start = state.transcript.items.len;
    for ([_]?usize{ state.active_assistant_entry, state.active_thinking_entry, state.active_tool_summary_entry, state.active_tool_result_entry }) |active| {
        if (active) |index| start = @min(start, index);
    }
    return start;
}

pub const ZenCounts = struct {
    thinking: usize = 0,
    tools: usize = 0,
    messages: usize = 0,
    last_activity: ?usize = null,
    final: ?usize = null,
};

pub fn zenCounts(entries: []const TranscriptEntry, start: usize) ZenCounts {
    var counts: ZenCounts = .{};
    var index = @min(start, entries.len);
    while (index < entries.len) : (index += 1) {
        const entry = &entries[index];
        switch (entry.kind) {
            .thinking => {
                counts.thinking += 1;
                counts.last_activity = index;
            },
            .tool => if (entry.tool_summary) {
                counts.tools += 1;
                counts.last_activity = index;
            },
            .assistant => {
                counts.messages += 1;
                counts.final = index;
            },
            .@"error" => if (entry.run_failure) {
                counts.final = index;
            },
            .user => counts.final = null,
            else => {},
        }
    }
    return counts;
}

pub const TranscriptEntry = struct {
    kind: TranscriptKind,
    text: std.ArrayList(u8) = .empty,
    timestamp_ms: i64 = 0,
    tool_summary: bool = false,
    notice: bool = false,
    run_failure: bool = false,
    tool_call_id: []u8 = &.{},

    pub fn init(allocator: std.mem.Allocator, kind: TranscriptKind, text: []const u8) !TranscriptEntry {
        var entry = TranscriptEntry{ .kind = kind, .timestamp_ms = compat.time.nowMillis() };
        try entry.text.appendSlice(allocator, text);
        return entry;
    }

    pub fn deinit(self: *TranscriptEntry, allocator: std.mem.Allocator) void {
        self.text.deinit(allocator);
        if (self.tool_call_id.len > 0) allocator.free(self.tool_call_id);
        self.* = undefined;
    }
};

pub const RegisteredToolEntry = struct {
    name: []u8,
    label: []u8,
    short_description: []u8,

    pub fn init(allocator: std.mem.Allocator, tool: agent.AgentTool) !RegisteredToolEntry {
        const name = try allocator.dupe(u8, tool.name);
        errdefer allocator.free(name);
        const label = try allocator.dupe(u8, tool.label);
        errdefer allocator.free(label);
        const short_description = try allocator.dupe(u8, tool.short_description orelse "");
        errdefer allocator.free(short_description);
        return .{
            .name = name,
            .label = label,
            .short_description = short_description,
        };
    }

    pub fn deinit(self: *RegisteredToolEntry, allocator: std.mem.Allocator) void {
        allocator.free(self.name);
        allocator.free(self.label);
        allocator.free(self.short_description);
        self.* = undefined;
    }
};

pub const ToolEntry = struct {
    id: []u8,
    name: []u8,
    label: []u8,
    args_json: []u8,
    output: std.ArrayList(u8) = .empty,
    status: ToolStatus = .pending,
    occurrence: usize = 1,
    raw_total_bytes: u64 = 0,
    returned_total_bytes: u64 = 0,
    estimated_returned_tokens: u64 = 0,
    artifact_count: u32 = 0,
    artifact_refs: []u8 = &.{},
    truncated: bool = false,
    error_detail_readable: bool = false,
    terminal_evidence: TerminalEvidence = .none,
    error_card_emitted: bool = false,
    retired: bool = false,

    pub fn isFrozen(self: *const ToolEntry) bool {
        return self.terminal_evidence == .both or self.retired;
    }

    pub fn init(allocator: std.mem.Allocator, id: []const u8, name: []const u8, label: []const u8, args_json: []const u8, status: ToolStatus) !ToolEntry {
        return .{
            .id = try allocator.dupe(u8, id),
            .name = try allocator.dupe(u8, name),
            .label = try allocator.dupe(u8, label),
            .args_json = try allocator.dupe(u8, args_json),
            .status = status,
            .occurrence = 1,
        };
    }

    pub fn deinit(self: *ToolEntry, allocator: std.mem.Allocator) void {
        allocator.free(self.id);
        allocator.free(self.name);
        allocator.free(self.label);
        allocator.free(self.args_json);
        self.output.deinit(allocator);
        if (self.artifact_refs.len > 0) allocator.free(self.artifact_refs);
        self.* = undefined;
    }
};

pub const ApprovalState = struct {
    status: ApprovalStatus = .none,
    tool_call_id: []u8 = &.{},
    tool_name: []u8 = &.{},
    args_json: []u8 = &.{},
    scope_hint: []u8 = &.{},
    always: bool = false,

    pub fn deinit(self: *ApprovalState, allocator: std.mem.Allocator) void {
        if (self.tool_call_id.len > 0) allocator.free(self.tool_call_id);
        if (self.tool_name.len > 0) allocator.free(self.tool_name);
        if (self.args_json.len > 0) allocator.free(self.args_json);
        if (self.scope_hint.len > 0) allocator.free(self.scope_hint);
        self.* = .{};
    }

    pub fn setPending(self: *ApprovalState, allocator: std.mem.Allocator, tool_call_id: []const u8, tool_name: []const u8, display_name: []const u8, args_json: []const u8) !void {
        self.deinit(allocator);
        const scope_hint = try approvalScopeHint(allocator, display_name, args_json);
        errdefer allocator.free(scope_hint);
        self.* = .{
            .status = .pending,
            .tool_call_id = try allocator.dupe(u8, tool_call_id),
            .tool_name = try allocator.dupe(u8, tool_name),
            .args_json = try allocator.dupe(u8, args_json),
            .scope_hint = scope_hint,
        };
    }
};

fn eventTime(at_ms: i64) i64 {
    return if (at_ms > 0) at_ms else compat.time.nowMillis();
}

pub const TokenRate = struct {
    output_tokens: u64 = 0,
    stream_ms: u64 = 0,
    estimated: bool = false,

    pub fn hasFigure(self: TokenRate) bool {
        return self.output_tokens > 0 and self.stream_ms > 0;
    }

    pub fn perSecond(self: TokenRate) u64 {
        if (!self.hasFigure()) return 0;
        return @intCast((@as(u128, self.output_tokens) * 1000) / self.stream_ms);
    }
};

pub const TokenRateSet = struct {
    live: TokenRate = .{},
    previous: TokenRate = .{},
    average: TokenRate = .{},
    turn_measured: TokenRate = .{},
    turn_estimated: TokenRate = .{},
    measured_since_switch: TokenRate = .{},
    estimated_since_switch: TokenRate = .{},
    message_bytes: u64 = 0,
    message_first_ms: i64 = 0,
    run_active: bool = false,
    live_min_ms: i64 = 1_000,

    const min_measured_ms: u64 = 100;

    pub fn messageStarted(self: *TokenRateSet, now_ms: i64) void {
        self.message_bytes = 0;
        self.message_first_ms = now_ms;
        self.live = .{};
    }

    pub fn produced(self: *TokenRateSet, bytes: u64, now_ms: i64) void {
        if (self.message_first_ms == 0) self.message_first_ms = now_ms;
        self.message_bytes += bytes;
    }

    pub fn messageEnded(self: *TokenRateSet, now_ms: i64, output_tokens: u64) void {
        if (self.message_first_ms == 0) {
            self.message_bytes = 0;
            self.live = .{};
            return;
        }
        const span: u64 = if (now_ms > self.message_first_ms) @intCast(now_ms - self.message_first_ms) else 0;
        if (span >= min_measured_ms) {
            if (output_tokens > 0) {
                self.turn_measured.output_tokens += output_tokens;
                self.turn_measured.stream_ms += span;
            } else {
                self.turn_estimated.output_tokens += estimateTokenBytes(self.message_bytes);
                self.turn_estimated.stream_ms += span;
                self.turn_estimated.estimated = true;
            }
        }
        self.message_bytes = 0;
        self.message_first_ms = 0;
        self.live = .{};
    }

    pub fn turn(self: *const TokenRateSet) TokenRate {
        if (self.turn_estimated.hasFigure()) {
            if (self.turn_measured.hasFigure()) {
                return .{
                    .output_tokens = self.turn_measured.output_tokens + self.turn_estimated.output_tokens,
                    .stream_ms = self.turn_measured.stream_ms + self.turn_estimated.stream_ms,
                    .estimated = true,
                };
            }
            return self.turn_estimated;
        }
        return self.turn_measured;
    }

    pub fn runStarted(self: *TokenRateSet) void {
        self.run_active = true;
        self.previous = .{};
    }

    pub fn runEnded(self: *TokenRateSet) void {
        self.run_active = false;
    }

    pub fn messageAborted(self: *TokenRateSet) void {
        self.message_bytes = 0;
        self.message_first_ms = 0;
        self.live = .{};
    }

    pub fn resetForModel(self: *TokenRateSet) void {
        const open = self.turn();
        const clock = self.message_first_ms;
        const bytes = self.message_bytes;
        self.* = .{ .run_active = self.run_active, .message_first_ms = clock, .message_bytes = bytes };
        if (open.hasFigure()) self.previous = open;
    }

    pub fn turnEnded(self: *TokenRateSet) void {
        const finished = self.turn();
        self.message_bytes = 0;
        self.message_first_ms = 0;
        self.live = .{};
        if (self.turn_measured.hasFigure()) {
            self.measured_since_switch.output_tokens += self.turn_measured.output_tokens;
            self.measured_since_switch.stream_ms += self.turn_measured.stream_ms;
        }
        if (self.turn_estimated.hasFigure()) {
            self.estimated_since_switch.output_tokens += self.turn_estimated.output_tokens;
            self.estimated_since_switch.stream_ms += self.turn_estimated.stream_ms;
        }
        if (finished.hasFigure()) self.previous = finished;
        const measured = self.measured_since_switch.hasFigure();
        self.average = if (measured) self.measured_since_switch else self.estimated_since_switch;
        self.average.estimated = !measured;
        self.turn_measured = .{};
        self.turn_estimated = .{};
    }

    pub fn liveAt(self: *TokenRateSet, now_ms: i64) void {
        if (self.message_first_ms == 0 or now_ms <= self.message_first_ms) return;
        if (now_ms - self.message_first_ms < self.live_min_ms) return;
        self.live = .{
            .output_tokens = estimateTokenBytes(self.message_bytes),
            .stream_ms = @intCast(now_ms - self.message_first_ms),
            .estimated = true,
        };
    }

    pub fn turnShown(self: *const TokenRateSet) TokenRate {
        if (self.live.hasFigure()) return self.live;
        if (self.run_active) {
            const open = self.turn();
            if (open.hasFigure()) return open;
            if (self.previous.hasFigure()) return self.previous;
            return .{};
        }
        if (self.previous.hasFigure()) return self.previous;
        return .{};
    }
};

pub fn estimateTokenBytes(bytes: u64) u64 {
    if (bytes == 0) return 0;
    return (bytes + 3) / 4;
}

pub const UsageTotals = struct {
    input: u64 = 0,
    output: u64 = 0,
    cache_read: u64 = 0,

    pub fn add(self: *UsageTotals, other: UsageTotals) void {
        self.input += other.input;
        self.output += other.output;
        self.cache_read += other.cache_read;
    }

    pub fn reported(self: UsageTotals) bool {
        return self.input + self.output + self.cache_read > 0;
    }
};

pub const TelemetryState = struct {
    last_turn_usage: UsageTotals = .{},
    session_usage: UsageTotals = .{},
    estimated_tokens: u64 = 0,
    context_window: u64 = 0,
    input_cost_per_million: f64 = 0,
    rate: TokenRateSet = .{},
};

pub const QueueState = session_runtime.QueuedCounts;

pub const StatusState = struct {
    model: []u8 = &.{},
    provider: []u8 = &.{},
    session_id: []u8 = &.{},
    context_used: usize = 0,
    context_limit: usize = 0,
    turn_count: usize = 0,
    streaming: bool = false,
    compacting: bool = false,
    refreshing_models: bool = false,
    streaming_since_ms: i64 = 0,
    streaming_elapsed_ms: u64 = 0,
    last_error: []u8 = &.{},

    pub fn deinit(self: *StatusState, allocator: std.mem.Allocator) void {
        if (self.model.len > 0) allocator.free(self.model);
        if (self.provider.len > 0) allocator.free(self.provider);
        if (self.session_id.len > 0) allocator.free(self.session_id);
        if (self.last_error.len > 0) allocator.free(self.last_error);
        self.* = .{};
    }

    pub fn setModel(self: *StatusState, allocator: std.mem.Allocator, model: []const u8, provider: []const u8) !void {
        try self.setModelWithContext(allocator, model, provider, 0);
    }

    pub fn setModelWithContext(self: *StatusState, allocator: std.mem.Allocator, model: []const u8, provider: []const u8, context_limit: usize) !void {
        if (self.model.len > 0) allocator.free(self.model);
        if (self.provider.len > 0) allocator.free(self.provider);
        self.model = try allocator.dupe(u8, model);
        self.provider = try allocator.dupe(u8, provider);
        self.context_limit = context_limit;
    }

    pub fn setError(self: *StatusState, allocator: std.mem.Allocator, message: []const u8) !void {
        if (self.last_error.len > 0) allocator.free(self.last_error);
        self.last_error = try allocator.dupe(u8, message);
    }

    pub fn setSessionId(self: *StatusState, allocator: std.mem.Allocator, session_id: []const u8) !void {
        const new_session_id = try allocator.dupe(u8, session_id);
        if (self.session_id.len > 0) allocator.free(self.session_id);
        self.session_id = new_session_id;
    }
};

pub const PreviewState = struct {
    content: []u8 = &.{},

    pub fn deinit(self: *PreviewState, allocator: std.mem.Allocator) void {
        if (self.content.len > 0) allocator.free(self.content);
        self.* = .{};
    }

    pub fn set(self: *PreviewState, allocator: std.mem.Allocator, content: []const u8) !void {
        const owned = try allocator.dupe(u8, content);
        self.deinit(allocator);
        self.* = .{ .content = owned };
    }
};

pub const SessionEntry = struct {
    id: []u8,
    label: []u8,

    pub fn init(allocator: std.mem.Allocator, id: []const u8, label: []const u8) !SessionEntry {
        const owned_id = try allocator.dupe(u8, id);
        errdefer allocator.free(owned_id);
        const owned_label = try allocator.dupe(u8, label);
        return .{ .id = owned_id, .label = owned_label };
    }

    pub fn deinit(self: *SessionEntry, allocator: std.mem.Allocator) void {
        allocator.free(self.id);
        allocator.free(self.label);
        self.* = undefined;
    }
};

pub const ComposerState = struct {
    buffer: std.ArrayList(u8) = .empty,
    cursor: usize = 0,
    history: std.ArrayList([]u8) = .empty,
    history_index: ?usize = null,
    history_draft: std.ArrayList(u8) = .empty,
    scroll_row: usize = 0,
    goal_column: ?usize = null,

    pub fn deinit(self: *ComposerState, allocator: std.mem.Allocator) void {
        self.buffer.deinit(allocator);
        for (self.history.items) |item| allocator.free(item);
        self.history.deinit(allocator);
        self.history_draft.deinit(allocator);
        self.* = undefined;
    }

    pub fn clear(self: *ComposerState) void {
        self.buffer.clearRetainingCapacity();
        self.cursor = 0;
        self.history_index = null;
        self.history_draft.clearRetainingCapacity();
        self.scroll_row = 0;
        self.goal_column = null;
    }

    pub fn text(self: ComposerState) []const u8 {
        return self.buffer.items;
    }

    pub fn normalizeCursor(self: *ComposerState) void {
        self.cursor = utf8BoundaryAtOrBefore(self.buffer.items, @min(self.cursor, self.buffer.items.len));
    }

    pub fn insertSlice(self: *ComposerState, allocator: std.mem.Allocator, bytes: []const u8) !void {
        self.normalizeCursor();
        try self.buffer.insertSlice(allocator, self.cursor, bytes);
        self.cursor += bytes.len;
    }

    pub fn insertPaste(self: *ComposerState, allocator: std.mem.Allocator, bytes: []const u8) !void {
        if (bytes.len > 0 and bytes[0] == '\n') {
            self.normalizeCursor();
            if (self.cursor > 0 and self.buffer.items[self.cursor - 1] == '\r') {
                _ = self.deleteBeforeCursor();
            }
        }
        if (std.mem.indexOf(u8, bytes, "\r\n") == null) return self.insertSlice(allocator, bytes);
        const normalized = try std.mem.replaceOwned(u8, allocator, bytes, "\r\n", "\n");
        defer allocator.free(normalized);
        return self.insertSlice(allocator, normalized);
    }

    pub fn deleteBeforeCursor(self: *ComposerState) bool {
        self.normalizeCursor();
        if (self.cursor == 0) return false;
        const start = previousCodepointStart(self.buffer.items, self.cursor);
        const removed = self.cursor - start;
        std.mem.copyForwards(u8, self.buffer.items[start..], self.buffer.items[self.cursor..]);
        self.buffer.shrinkRetainingCapacity(self.buffer.items.len - removed);
        self.cursor = start;
        return true;
    }

    pub fn moveCursorPrev(self: *ComposerState) bool {
        self.normalizeCursor();
        if (self.cursor == 0) return false;
        self.cursor = previousCodepointStart(self.buffer.items, self.cursor);
        return true;
    }

    pub fn moveCursorNext(self: *ComposerState) bool {
        self.normalizeCursor();
        if (self.cursor >= self.buffer.items.len) return false;
        self.cursor = nextCodepointEnd(self.buffer.items, self.cursor);
        return true;
    }

    pub fn moveCursorHome(self: *ComposerState) void {
        self.normalizeCursor();
        const before = self.buffer.items[0..self.cursor];
        self.cursor = if (std.mem.lastIndexOfScalar(u8, before, '\n')) |nl| nl + 1 else 0;
    }

    pub fn moveCursorEnd(self: *ComposerState) void {
        self.normalizeCursor();
        self.cursor = std.mem.indexOfScalarPos(u8, self.buffer.items, self.cursor, '\n') orelse self.buffer.items.len;
    }

    pub fn moveCursorWordPrev(self: *ComposerState) void {
        self.normalizeCursor();
        self.cursor = wordStartBefore(self.buffer.items, self.cursor);
    }

    pub fn moveCursorWordNext(self: *ComposerState) void {
        self.normalizeCursor();
        self.cursor = wordEndAfter(self.buffer.items, self.cursor);
    }

    pub fn deleteWordBeforeCursor(self: *ComposerState) bool {
        self.normalizeCursor();
        if (self.cursor == 0) return false;
        const start = wordStartBefore(self.buffer.items, self.cursor);
        self.removeRange(start, self.cursor);
        return true;
    }

    pub fn deleteToLineEnd(self: *ComposerState) bool {
        self.normalizeCursor();
        if (self.cursor >= self.buffer.items.len) return false;
        const end = std.mem.indexOfScalarPos(u8, self.buffer.items, self.cursor, '\n') orelse self.buffer.items.len;
        if (end == self.cursor) {
            self.removeRange(self.cursor, self.cursor + 1);
            return true;
        }
        self.removeRange(self.cursor, end);
        return true;
    }

    pub fn deleteToLineStart(self: *ComposerState) bool {
        self.normalizeCursor();
        if (self.cursor == 0) return false;
        const start = if (std.mem.lastIndexOfScalar(u8, self.buffer.items[0..self.cursor], '\n')) |nl| nl + 1 else 0;
        if (start == self.cursor) {
            self.removeRange(self.cursor - 1, self.cursor);
            return true;
        }
        self.removeRange(start, self.cursor);
        return true;
    }

    pub fn deleteAtCursor(self: *ComposerState) bool {
        self.normalizeCursor();
        if (self.cursor >= self.buffer.items.len) return false;
        const end = nextCodepointEnd(self.buffer.items, self.cursor);
        self.removeRange(self.cursor, end);
        return true;
    }

    fn removeRange(self: *ComposerState, start: usize, end: usize) void {
        const removed = end - start;
        std.mem.copyForwards(u8, self.buffer.items[start..], self.buffer.items[end..]);
        self.buffer.shrinkRetainingCapacity(self.buffer.items.len - removed);
        self.cursor = start;
    }
};

fn isWordByte(c: u8) bool {
    return !(c == ' ' or c == '\t' or c == '\n' or c == '/' or c == '.' or c == ',' or c == ';' or c == ':' or c == '-' or c == '_' or c == '(' or c == ')' or c == '"' or c == '\'');
}

fn wordStartBefore(text: []const u8, cursor: usize) usize {
    var idx = @min(cursor, text.len);
    while (idx > 0 and !isWordByte(text[idx - 1])) idx -= 1;
    while (idx > 0 and isWordByte(text[idx - 1])) idx -= 1;
    return idx;
}

fn wordEndAfter(text: []const u8, cursor: usize) usize {
    var idx = @min(cursor, text.len);
    while (idx < text.len and !isWordByte(text[idx])) idx += 1;
    while (idx < text.len and isWordByte(text[idx])) idx += 1;
    return idx;
}

fn previousCodepointStart(text: []const u8, cursor: usize) usize {
    if (cursor == 0) return 0;
    var idx = @min(cursor, text.len) - 1;
    while (idx > 0 and (text[idx] & 0b1100_0000) == 0b1000_0000) idx -= 1;
    return idx;
}

fn nextCodepointEnd(text: []const u8, cursor: usize) usize {
    const idx = utf8BoundaryAtOrBefore(text, @min(cursor, text.len));
    if (idx >= text.len) return text.len;
    const len = std.unicode.utf8ByteSequenceLength(text[idx]) catch 1;
    return @min(text.len, idx + len);
}

fn utf8BoundaryAtOrBefore(text: []const u8, index: usize) usize {
    var idx = @min(index, text.len);
    while (idx > 0 and idx < text.len and (text[idx] & 0b1100_0000) == 0b1000_0000) idx -= 1;
    return idx;
}

const max_hashline_preview_bytes: usize = 20 * 1024;
const hashline_preview_truncated_marker = "\n... preview truncated ...\n";

pub const AppState = struct {
    allocator: std.mem.Allocator,
    mode: AppMode = .normal,
    transcript: std.ArrayList(TranscriptEntry) = .empty,
    registered_tools: std.ArrayList(RegisteredToolEntry) = .empty,
    tools: std.ArrayList(ToolEntry) = .empty,
    sessions: std.ArrayList(SessionEntry) = .empty,
    composer: ComposerState = .{},
    approval: ApprovalState = .{},
    permission_mode: session_runtime.PermissionMode = .bypass,
    status: StatusState = .{},
    queue: QueueState = .{},
    telemetry: TelemetryState = .{},
    preview: PreviewState = .{},
    thinking_level: ai_types.ThinkingLevel = .low,
    autocompact: AutoCompactSetting = .auto,
    verbosity: Verbosity = .{},
    zen: Zen = .{},
    login_input_secret: bool = false,
    anim_tick: u64 = 0,
    transcript_scroll: usize = 0,
    session_index: usize = 0,
    session_scroll: usize = 0,
    confirm_session_delete: bool = false,
    confirm_session_force_delete: bool = false,
    menu_index: usize = 0,
    menu_scroll: usize = 0,
    picker_kind: PickerKind = .model,
    cwd_display: []u8 = &.{},
    git_branch: []u8 = &.{},
    session_title: []u8 = &.{},
    agent_cwd_display: []u8 = &.{},
    agent_cwd_raw: []u8 = &.{},
    session_root_raw: []u8 = &.{},
    follow_agent_cwd: bool = true,
    active_user_entry: ?usize = null,
    active_assistant_entry: ?usize = null,
    active_thinking_entry: ?usize = null,
    active_tool_result_entry: ?usize = null,
    active_tool_summary_entry: ?usize = null,
    tool_families: std.StringHashMapUnmanaged(usize) = .empty,
    unfrozen_occurrence_ids: std.StringHashMapUnmanaged(void) = .empty,
    retire_candidates: std.ArrayListUnmanaged(usize) = .empty,
    finalized_tool_count: usize = 0,
    summary_scan_floor: usize = 0,
    summary_floor_tool: usize = 0,
    last_tool_calls_json: []u8 = &.{},
    stream_aborted: bool = false,
    dropped_event_count: u64 = 0,
    backpressure_active: bool = false,
    pending_steers: std.ArrayList([]u8) = .empty,
    steers_reconciled: u64 = 0,
    pending_follow_ups: std.ArrayList([]u8) = .empty,
    held_after_abort: std.ArrayList([]u8) = .empty,
    held_after_abort_echoed: usize = 0,
    picker_filter: std.ArrayList(u8) = .empty,

    pub fn init(allocator: std.mem.Allocator) AppState {
        return .{ .allocator = allocator };
    }

    pub fn deinit(self: *AppState) void {
        for (self.transcript.items) |*entry| entry.deinit(self.allocator);
        self.transcript.deinit(self.allocator);
        for (self.registered_tools.items) |*tool| tool.deinit(self.allocator);
        self.registered_tools.deinit(self.allocator);
        self.unfrozen_occurrence_ids.deinit(self.allocator);
        self.retire_candidates.deinit(self.allocator);
        for (self.tools.items) |*tool| tool.deinit(self.allocator);
        self.tools.deinit(self.allocator);
        self.clearToolFamilies();
        self.tool_families.deinit(self.allocator);
        for (self.sessions.items) |*session| session.deinit(self.allocator);
        self.sessions.deinit(self.allocator);
        self.composer.deinit(self.allocator);
        self.approval.deinit(self.allocator);
        self.status.deinit(self.allocator);
        self.preview.deinit(self.allocator);
        self.clearPendingSteers();
        self.pending_steers.deinit(self.allocator);
        self.clearPendingFollowUps();
        self.pending_follow_ups.deinit(self.allocator);
        self.clearHeldAfterAbort();
        self.held_after_abort.deinit(self.allocator);
        self.picker_filter.deinit(self.allocator);
        if (self.last_tool_calls_json.len > 0) self.allocator.free(self.last_tool_calls_json);
        if (self.cwd_display.len > 0) self.allocator.free(self.cwd_display);
        if (self.git_branch.len > 0) self.allocator.free(self.git_branch);
        if (self.session_title.len > 0) self.allocator.free(self.session_title);
        if (self.agent_cwd_display.len > 0) self.allocator.free(self.agent_cwd_display);
        if (self.agent_cwd_raw.len > 0) self.allocator.free(self.agent_cwd_raw);
        if (self.session_root_raw.len > 0) self.allocator.free(self.session_root_raw);
        self.* = undefined;
    }

    pub fn setCwdDisplay(self: *AppState, allocator: std.mem.Allocator, display: []const u8) !void {
        const owned = try allocator.dupe(u8, display);
        if (self.cwd_display.len > 0) self.allocator.free(self.cwd_display);
        self.cwd_display = owned;
    }

    pub fn setSessionRoot(self: *AppState, allocator: std.mem.Allocator, raw: []const u8) !void {
        const owned = try allocator.dupe(u8, raw);
        if (self.session_root_raw.len > 0) self.allocator.free(self.session_root_raw);
        self.session_root_raw = owned;
    }

    pub fn setGitBranch(self: *AppState, allocator: std.mem.Allocator, branch: []const u8) !void {
        const owned = try allocator.dupe(u8, branch);
        if (self.git_branch.len > 0) self.allocator.free(self.git_branch);
        self.git_branch = owned;
    }

    pub fn setSessionTitle(self: *AppState, allocator: std.mem.Allocator, raw: []const u8) !void {
        const cleaned = try sanitizeTerminalText(allocator, raw);
        defer allocator.free(cleaned);
        const owned = try allocator.dupe(u8, std.mem.trim(u8, cleaned, " "));
        if (self.session_title.len > 0) self.allocator.free(self.session_title);
        self.session_title = owned;
    }

    pub fn setAgentCwd(self: *AppState, allocator: std.mem.Allocator, raw: []const u8, display: []const u8) !void {
        const owned_raw = try allocator.dupe(u8, raw);
        errdefer allocator.free(owned_raw);
        const owned_display = try allocator.dupe(u8, display);
        if (self.agent_cwd_raw.len > 0) self.allocator.free(self.agent_cwd_raw);
        if (self.agent_cwd_display.len > 0) self.allocator.free(self.agent_cwd_display);
        self.agent_cwd_raw = owned_raw;
        self.agent_cwd_display = owned_display;
    }

    pub fn cwdRowPath(self: *const AppState) []const u8 {
        if (self.agent_cwd_display.len > 0) return self.agent_cwd_display;
        return self.cwd_display;
    }

    pub fn resetAgentCwd(self: *AppState) void {
        if (self.agent_cwd_raw.len > 0) self.allocator.free(self.agent_cwd_raw);
        if (self.agent_cwd_display.len > 0) self.allocator.free(self.agent_cwd_display);
        self.agent_cwd_raw = &.{};
        self.agent_cwd_display = &.{};
    }

    pub fn setFollowingAgentCwd(self: *AppState, following: bool) void {
        self.follow_agent_cwd = following;
    }

    pub fn followAgentCwd(self: *AppState, args_json: []const u8) !void {
        const raw = (try agentCwdFromArgs(self.allocator, args_json)) orelse return;
        defer self.allocator.free(raw);
        if (std.mem.eql(u8, raw, self.agent_cwd_raw)) return;
        const home = compat.getEnvVarOwned(self.allocator, "HOME") catch null;
        defer if (home) |value| self.allocator.free(value);
        const display = try collapseHomePath(self.allocator, raw, home);
        defer self.allocator.free(display);
        try self.setAgentCwd(self.allocator, raw, display);
    }

    pub fn agentCwdIsOutsideSession(self: *const AppState) bool {
        if (self.agent_cwd_raw.len == 0) return false;
        const within = pathWithin(self.allocator, self.agent_cwd_raw, self.session_root_raw) catch return true;
        return !within;
    }

    pub fn appendTranscript(self: *AppState, kind: TranscriptKind, text: []const u8) !void {
        try self.transcript.append(self.allocator, try TranscriptEntry.init(self.allocator, kind, text));
    }

    pub fn appendNotice(self: *AppState, text: []const u8) !void {
        var entry = try TranscriptEntry.init(self.allocator, .system, text);
        errdefer entry.deinit(self.allocator);
        entry.notice = true;
        try self.transcript.append(self.allocator, entry);
    }

    pub fn appendToolSummaryTranscript(self: *AppState, text: []const u8, tool_call_id: []const u8) !void {
        var entry = try TranscriptEntry.init(self.allocator, .tool, text);
        errdefer entry.deinit(self.allocator);
        entry.tool_summary = true;
        if (tool_call_id.len > 0) entry.tool_call_id = try self.allocator.dupe(u8, tool_call_id);
        try self.transcript.append(self.allocator, entry);
    }

    pub fn setRegisteredTools(self: *AppState, tools: []const agent.AgentTool) !void {
        for (self.registered_tools.items) |*tool| tool.deinit(self.allocator);
        self.registered_tools.clearRetainingCapacity();
        try self.registered_tools.ensureTotalCapacity(self.allocator, tools.len);
        for (tools) |tool| self.registered_tools.appendAssumeCapacity(try RegisteredToolEntry.init(self.allocator, tool));
    }

    pub fn toolLabel(self: *const AppState, name: []const u8) []const u8 {
        for (self.registered_tools.items) |tool| {
            if (std.mem.eql(u8, tool.name, name)) return tool.label;
        }
        return name;
    }

    pub fn clearTranscript(self: *AppState) void {
        for (self.transcript.items) |*entry| entry.deinit(self.allocator);
        self.transcript.clearRetainingCapacity();
        self.transcript_scroll = 0;
        self.zen.start_index = 0;
        self.clearActiveTranscriptEntries();
        self.clearPendingSteers();
    }

    pub fn lastAssistantText(self: *const AppState) ?[]const u8 {
        var i = self.transcript.items.len;
        while (i > 0) {
            i -= 1;
            if (self.transcript.items[i].kind == .assistant) {
                return self.transcript.items[i].text.items;
            }
        }
        return null;
    }

    pub fn clearTools(self: *AppState) void {
        self.unfrozen_occurrence_ids.clearRetainingCapacity();
        self.retire_candidates.clearRetainingCapacity();
        for (self.tools.items) |*tool| tool.deinit(self.allocator);
        self.tools.clearRetainingCapacity();
        self.clearToolFamilies();
        self.finalized_tool_count = 0;
        self.summary_scan_floor = 0;
        self.summary_floor_tool = 0;
    }

    pub fn resetReplayState(self: *AppState) void {
        self.clearTranscript();
        self.clearTools();
        self.telemetry = .{};
        self.queue = .{};
        self.clearPendingFollowUps();
        self.status.context_used = 0;
        self.status.turn_count = 0;
        self.status.streaming = false;
        self.status.compacting = false;
        self.stream_aborted = false;
        if (self.status.last_error.len > 0) {
            self.allocator.free(self.status.last_error);
            self.status.last_error = &.{};
        }
        self.dropped_event_count = 0;
        self.backpressure_active = false;
        self.resetAgentCwd();
        if (self.last_tool_calls_json.len > 0) {
            self.allocator.free(self.last_tool_calls_json);
            self.last_tool_calls_json = &.{};
        }
    }

    pub fn appendUserMessage(self: *AppState, text: []const u8) !void {
        try self.appendTranscript(.user, text);
    }

    pub fn appendSteeredMessage(self: *AppState, text: []const u8) !void {
        return self.appendSteeredMessageEchoing(text, text);
    }

    pub fn appendSteeredMessageEchoing(self: *AppState, text: []const u8, echo: []const u8) !void {
        const owned = try self.allocator.dupe(u8, text);
        errdefer self.allocator.free(owned);
        try self.pending_steers.append(self.allocator, owned);
        errdefer _ = self.pending_steers.pop();
        if (echo.len == 0) return;
        try self.appendUserMessage(echo);
        if (self.active_user_entry) |index| {
            if (index < self.transcript.items.len and self.transcript.items[index].kind == .user) return;
        }
        self.active_user_entry = self.transcript.items.len - 1;
    }

    pub fn reconcileSteers(self: *AppState, consumed_total: u64) void {
        while (self.steers_reconciled < consumed_total and self.pending_steers.items.len > 0) {
            const matched = self.pending_steers.orderedRemove(0);
            self.allocator.free(matched);
            self.steers_reconciled += 1;
        }
        self.steers_reconciled = consumed_total;
    }

    pub fn clearPendingSteers(self: *AppState) void {
        for (self.pending_steers.items) |pending| self.allocator.free(pending);
        self.pending_steers.clearRetainingCapacity();
    }

    pub fn appendQueuedFollowUp(self: *AppState, text: []const u8) !void {
        const owned = try self.allocator.dupe(u8, text);
        errdefer self.allocator.free(owned);
        try self.pending_follow_ups.append(self.allocator, owned);
    }

    pub fn clearPendingFollowUps(self: *AppState) void {
        for (self.pending_follow_ups.items) |pending| self.allocator.free(pending);
        self.pending_follow_ups.clearRetainingCapacity();
    }

    pub fn holdQueuedAfterAbort(self: *AppState) !void {
        try self.held_after_abort.ensureUnusedCapacity(self.allocator, self.pending_steers.items.len + self.pending_follow_ups.items.len);
        if (self.held_after_abort.items.len == 0) self.held_after_abort_echoed = self.pending_steers.items.len;
        self.held_after_abort.appendSliceAssumeCapacity(self.pending_steers.items);
        self.held_after_abort.appendSliceAssumeCapacity(self.pending_follow_ups.items);
        self.pending_steers.clearRetainingCapacity();
        self.pending_follow_ups.clearRetainingCapacity();
    }

    pub fn clearHeldAfterAbort(self: *AppState) void {
        for (self.held_after_abort.items) |held| self.allocator.free(held);
        self.held_after_abort.clearRetainingCapacity();
        self.held_after_abort_echoed = 0;
    }

    pub fn toolById(self: *const AppState, id: []const u8) ?*const ToolEntry {
        var i = self.tools.items.len;
        while (i > 0) {
            i -= 1;
            if (std.mem.eql(u8, self.tools.items[i].id, id)) return &self.tools.items[i];
        }
        return null;
    }

    pub fn pickerFilter(self: *const AppState) []const u8 {
        return self.picker_filter.items;
    }

    pub fn appendPickerFilter(self: *AppState, text: []const u8) !void {
        for (text) |c| {
            if (c < 0x20 or c == 0x7f) continue;
            try self.picker_filter.append(self.allocator, c);
        }
        self.menu_index = 0;
        self.menu_scroll = 0;
    }

    pub fn popPickerFilter(self: *AppState) bool {
        const items = self.picker_filter.items;
        if (items.len == 0) return false;
        var start = items.len - 1;
        while (start > 0 and (items[start] & 0b1100_0000) == 0b1000_0000) start -= 1;
        self.picker_filter.shrinkRetainingCapacity(start);
        self.menu_index = 0;
        self.menu_scroll = 0;
        return true;
    }

    pub fn clearPickerFilter(self: *AppState) void {
        self.picker_filter.clearRetainingCapacity();
    }

    pub fn submitComposer(self: *AppState) !?[]u8 {
        const raw = std.mem.trim(u8, self.composer.buffer.items, " \t\r\n");
        if (raw.len == 0) {
            self.composer.clear();
            return null;
        }
        const submitted = try self.allocator.dupe(u8, raw);
        errdefer self.allocator.free(submitted);
        try self.recordComposerHistory(submitted);
        self.composer.clear();
        try self.appendUserMessage(submitted);
        return submitted;
    }

    pub fn recordComposerHistory(self: *AppState, text: []const u8) !void {
        const raw = std.mem.trim(u8, text, " \t\r\n");
        if (raw.len == 0) return;
        try self.composer.history.append(self.allocator, try self.allocator.dupe(u8, raw));
        self.composer.history_index = null;
        self.composer.history_draft.clearRetainingCapacity();
    }

    pub fn replaceComposerBuffer(self: *AppState, text: []const u8) !void {
        self.composer.buffer.clearRetainingCapacity();
        try self.composer.buffer.appendSlice(self.allocator, text);
        self.composer.cursor = self.composer.buffer.items.len;
    }

    pub fn composerHistoryPrev(self: *AppState) !bool {
        if (self.composer.history.items.len == 0) return false;
        if (self.composer.history_index) |index| {
            if (index == 0) return false;
            const next_index = index - 1;
            self.composer.history_index = next_index;
            try self.replaceComposerBuffer(self.composer.history.items[next_index]);
            return true;
        }
        self.composer.history_draft.clearRetainingCapacity();
        try self.composer.history_draft.appendSlice(self.allocator, self.composer.buffer.items);
        const next_index = self.composer.history.items.len - 1;
        self.composer.history_index = next_index;
        try self.replaceComposerBuffer(self.composer.history.items[next_index]);
        return true;
    }

    pub fn composerHistoryNext(self: *AppState) !bool {
        const current = self.composer.history_index orelse return false;
        if (current + 1 >= self.composer.history.items.len) {
            self.composer.history_index = null;
            try self.replaceComposerBuffer(self.composer.history_draft.items);
            self.composer.history_draft.clearRetainingCapacity();
            return true;
        }
        const next_index = current + 1;
        self.composer.history_index = next_index;
        try self.replaceComposerBuffer(self.composer.history.items[next_index]);
        return true;
    }

    pub fn cycleThinkingLevel(self: *AppState) ai_types.ThinkingLevel {
        self.thinking_level = switch (self.thinking_level) {
            .off, .minimal => .low,
            .low => .medium,
            .medium => .high,
            .high => .xhigh,
            .xhigh => .max,
            .max => .off,
        };
        return self.thinking_level;
    }

    pub fn setQueuedCounts(self: *AppState, counts: session_runtime.QueuedCounts) void {
        self.queue = counts;
        while (self.pending_follow_ups.items.len > counts.follow_up) {
            self.allocator.free(self.pending_follow_ups.orderedRemove(0));
        }
    }

    pub fn applyEvent(self: *AppState, event: session_runtime.SessionEvent) !void {
        if (self.stream_aborted) switch (event) {
            .turn_end, .agent_end, .@"error", .system_warning, .backpressure_status, .compaction_end => {},
            else => return,
        };
        switch (event) {
            .agent_start => {
                self.status.streaming = true;
                self.markStreamingStarted();
                self.telemetry.rate.runStarted();
            },
            .turn_start => {
                self.status.streaming = true;
                self.markStreamingStarted();
                self.status.turn_count += 1;
                self.cleanupActiveTranscriptEntries();
                self.retireToolOccurrences();
            },
            .message_start => |payload| switch (payload.role) {
                .assistant => {
                    self.telemetry.rate.messageStarted(eventTime(payload.at_ms));
                    self.active_assistant_entry = try self.appendEmptyTranscript(.assistant);
                },
                .user => self.active_user_entry = try self.ensureTrailingEntry(.user),
                .tool_result => self.active_tool_result_entry = try self.appendEmptyTranscript(.tool),
            },
            .text_delta => |payload| {
                self.telemetry.rate.produced(payload.delta.slice().len, eventTime(payload.at_ms));
                try self.appendDelta(.assistant, payload.delta.slice());
            },
            .thinking_delta => |payload| {
                self.telemetry.rate.produced(payload.delta.slice().len, eventTime(payload.at_ms));
                try self.appendThinkingDelta(payload.delta.slice());
            },
            .tool_call_delta => |payload| self.telemetry.rate.produced(payload.delta.slice().len, eventTime(payload.at_ms)),
            .provider_event => {},
            .message_end => |payload| switch (payload.role) {
                .assistant => {
                    self.telemetry.rate.messageEnded(eventTime(payload.at_ms), payload.output_tokens);
                    const usage = UsageTotals{ .input = payload.input_tokens, .output = payload.output_tokens, .cache_read = payload.cache_read_tokens };
                    if (usage.reported()) {
                        self.telemetry.last_turn_usage = usage;
                        self.telemetry.session_usage.add(usage);
                    }
                    self.active_thinking_entry = null;
                    try self.finishTranscriptEntry(.assistant, payload.text.slice(), &self.active_assistant_entry);
                    try self.rememberToolCalls(payload.tool_calls_json.slice());
                },
                .user => try self.finishTranscriptEntryWithOptions(.user, payload.text.slice(), &self.active_user_entry, true),
                .tool_result => {
                    if (payload.tool_call_id.slice().len == 0) {
                        try self.finishToolResultEntry(payload.text.slice(), "");
                        return;
                    }
                    const status: ToolStatus = if (payload.is_error) .@"error" else .done;
                    const resolution = try self.resolveToolOccurrence(payload.tool_call_id.slice(), payload.tool_name.slice(), "", .result_outcome, status);
                    const tool = resolution.tool;
                    const upgrade_failure = payload.is_error and tool.status == .done;
                    if (resolution.merge_state or upgrade_failure) {
                        const detail_source = if (payload.details_json.slice().len > 0) payload.details_json.slice() else payload.text.slice();
                        try self.recoverToolArgsInto(tool, payload.tool_call_id.slice());
                        try self.terminalizeToolOccurrence(tool, status, .result);
                        if (resolution.merge_state) {
                            try self.mergeTerminalOutput(tool, detail_source);
                            const artifact_count = countArtifactsJson(self.allocator, payload.artifacts_json.slice());
                            if (artifact_count > tool.artifact_count) tool.artifact_count = artifact_count;
                            refreshTruncated(tool);
                        }
                        const summary = try toolResultSummary(self.allocator, tool.label, tool.args_json, detail_source, payload.is_error, tool.raw_total_bytes, tool.returned_total_bytes, tool.estimated_returned_tokens, tool.artifact_count);
                        defer self.allocator.free(summary);
                        try self.writeToolSummaryRow(summary, tool.id);
                        if (payload.is_error) {
                            const unwrapped = try toolErrorMessage(self.allocator, detail_source);
                            defer if (unwrapped) |message| self.allocator.free(message);
                            tool.error_detail_readable = unwrapped != null or plainTextErrorDetail(self.allocator, detail_source);
                            try self.emitToolErrorCard(tool, if (unwrapped) |message| message else detail_source, tool.error_detail_readable);
                        }
                    } else if (tool.terminal_evidence == .execution) {
                        tool.terminal_evidence = .both;
                        _ = self.unfrozen_occurrence_ids.remove(tool.id);
                    }
                    const suppress_text = tool.status == .@"error" and tool.error_detail_readable;
                    try self.finishToolResultEntry(if (suppress_text) "" else payload.text.slice(), tool.id);
                    self.advanceSummaryScanFloor();
                },
            },
            .tool_approval_requested => |payload| {
                const label = self.toolLabel(payload.tool_name.slice());
                try self.approval.setPending(self.allocator, payload.tool_call_id.slice(), payload.tool_name.slice(), label, payload.args_json.slice());
                if (std.mem.eql(u8, payload.tool_name.slice(), "Edit")) try self.setHashlinePreview(payload.args_json.slice());
                self.mode = .approval;
                _ = try self.resolveToolOccurrence(payload.tool_call_id.slice(), payload.tool_name.slice(), payload.args_json.slice(), .live_intent, .pending);
            },
            .tool_execution_start => |payload| {
                if (self.follow_agent_cwd) try self.followAgentCwd(payload.args_json.slice());
                const resolution = try self.resolveToolOccurrence(payload.tool_call_id.slice(), payload.tool_name.slice(), payload.args_json.slice(), .live_intent, .running);
                const summary = try toolInvocation(self.allocator, resolution.tool.label, payload.args_json.slice());
                defer self.allocator.free(summary);
                try self.appendToolSummaryTranscript(summary, resolution.tool.id);
                self.active_tool_summary_entry = self.transcript.items.len - 1;
            },
            .tool_execution_update => |payload| {
                const resolution = try self.resolveToolOccurrence(payload.tool_call_id.slice(), payload.tool_name.slice(), payload.args_json.slice(), .live_intent, .running);
                const tool = resolution.tool;
                if (tool.output.items.len > 0) try tool.output.append(self.allocator, '\n');
                try tool.output.appendSlice(self.allocator, payload.partial_result_json.slice());
            },
            .tool_execution_end => |payload| {
                const status: ToolStatus = if (payload.is_error) .@"error" else .done;
                const resolution = try self.resolveToolOccurrence(payload.tool_call_id.slice(), payload.tool_name.slice(), "", .execution_outcome, status);
                const tool = resolution.tool;
                if (tool.args_json.len == 0) try self.recoverToolArgsInto(tool, payload.tool_call_id.slice());
                try self.mergeTerminalOutput(tool, payload.result_json.slice());
                try self.applyToolTelemetry(tool, payload.raw_total_bytes, payload.returned_total_bytes, payload.estimated_returned_tokens, payload.artifact_count, payload.artifact_refs.slice());
                try self.terminalizeToolOccurrence(tool, status, .execution);
                const effective_is_error = tool.status == .@"error";
                const summary = try toolResultSummary(self.allocator, tool.label, tool.args_json, payload.result_json.slice(), effective_is_error, tool.raw_total_bytes, tool.returned_total_bytes, tool.estimated_returned_tokens, tool.artifact_count);
                defer self.allocator.free(summary);
                try self.finalizeToolSummaryEntry(summary, tool.id);
                tool.error_detail_readable = false;
                if (effective_is_error) {
                    const unwrapped = try toolErrorMessage(self.allocator, payload.result_json.slice());
                    defer if (unwrapped) |message| self.allocator.free(message);
                    tool.error_detail_readable = unwrapped != null or plainTextErrorDetail(self.allocator, payload.result_json.slice());
                    try self.emitToolErrorCard(tool, if (unwrapped) |message| message else payload.result_json.slice(), tool.error_detail_readable);
                    if (tool.error_detail_readable) try self.removeLinkedResultRows(tool.id);
                }
                self.advanceSummaryScanFloor();
            },
            .context_usage => |payload| self.applyContextUsage(payload),
            .prompt_segment_usage => {},
            .system_warning => |payload| try self.appendTranscript(.@"error", payload.message.slice()),
            .backpressure_status => |payload| {
                self.backpressure_active = payload.active;
                self.dropped_event_count = payload.dropped_count;
            },
            .compaction_start => {
                self.status.compacting = true;
                self.status.streaming = true;
                self.markStreamingStarted();
            },
            .compaction_end => |payload| {
                self.status.compacting = false;
                if (!payload.in_run) {
                    self.status.streaming = false;
                    self.markStreamingStopped();
                    self.stream_aborted = false;
                }
                switch (payload.outcome) {
                    .completed => {
                        self.telemetry.estimated_tokens = payload.tokens_after;
                        self.status.context_used = @intCast(payload.tokens_after);
                        const notice = try compactionNotice(self.allocator, payload);
                        defer self.allocator.free(notice);
                        try self.appendTranscript(.system, notice);
                    },
                    .cancelled => try self.appendTranscript(.system, "compaction cancelled; the conversation is unchanged"),
                    .failed => {
                        const message = try std.fmt.allocPrint(self.allocator, "compaction failed: {s}; the conversation is unchanged", .{payload.message.slice()});
                        defer self.allocator.free(message);
                        try self.status.setError(self.allocator, message);
                        try self.appendTranscript(.@"error", message);
                    },
                }
            },
            .turn_end => {
                self.telemetry.rate.turnEnded();
                self.status.streaming = false;
                self.markStreamingStopped();
                self.stream_aborted = false;
                try self.finalizeInterruptedTools();
            },
            .agent_end => |payload| {
                self.telemetry.rate.turnEnded();
                self.telemetry.rate.runEnded();
                self.status.streaming = false;
                self.status.compacting = false;
                self.markStreamingStopped();
                self.stream_aborted = false;
                try self.finalizeInterruptedTools();
                self.retireToolOccurrences();
                switch (payload.reason) {
                    .completed => {},
                    .cancelled => try self.appendTranscript(.system, "agent cancelled"),
                    .@"error" => {
                        if (self.status.last_error.len == 0) {
                            try self.status.setError(self.allocator, "agent ended with error, but no error details were provided");
                            try self.appendTranscript(.@"error", self.status.last_error);
                            self.transcript.items[self.transcript.items.len - 1].run_failure = true;
                        }
                    },
                }
            },
            .@"error" => |payload| {
                self.cleanupActiveTranscriptEntries();
                self.markStreamingStopped();
                self.stream_aborted = false;
                try self.status.setError(self.allocator, payload.message.slice());
                try self.appendTranscript(.@"error", payload.message.slice());
                self.transcript.items[self.transcript.items.len - 1].run_failure = true;
            },
        }
    }

    pub fn setApprovalDecision(self: *AppState, approved: bool, always: bool) void {
        self.approval.status = if (approved) .approved else .rejected;
        self.approval.always = always;
        self.mode = .normal;
    }

    pub fn addSession(self: *AppState, id: []const u8, label: []const u8) !void {
        var entry = try SessionEntry.init(self.allocator, id, label);
        errdefer entry.deinit(self.allocator);
        try self.sessions.append(self.allocator, entry);
    }

    fn setHashlinePreview(self: *AppState, args_json: []const u8) !void {
        var parsed = std.json.parseFromSlice(std.json.Value, self.allocator, args_json, .{}) catch return;
        defer parsed.deinit();
        if (parsed.value != .object) return;
        const obj = parsed.value.object;
        const operation = jsonString(obj, "operation") orelse "edit";
        const start_line = jsonUsize(obj, "start_line") orelse 0;
        const end_line = jsonUsize(obj, "end_line") orelse start_line;
        const start_hash = jsonString(obj, "start_hash") orelse jsonString(obj, "line_hash") orelse "";
        const end_hash = jsonString(obj, "end_hash") orelse start_hash;

        var out = std.ArrayList(u8).empty;
        defer out.deinit(self.allocator);
        if (std.mem.eql(u8, operation, "find_replace")) {
            const header = try std.fmt.allocPrint(self.allocator, "edit preview\noperation: {s}\n", .{operation});
            defer self.allocator.free(header);
            try appendHashlinePreview(&out, self.allocator, header);
            try self.appendPreviewLines(&out, "- ", 0, jsonString(obj, "find") orelse "");
            try self.appendPreviewLines(&out, "+ ", 0, jsonString(obj, "replace") orelse "");
        } else {
            const header = try std.fmt.allocPrint(self.allocator, "edit preview\noperation: {s}\nrange: {d}:{s}..{d}:{s}\n", .{ operation, start_line, start_hash, end_line, end_hash });
            defer self.allocator.free(header);
            try appendHashlinePreview(&out, self.allocator, header);
            if (std.mem.eql(u8, operation, "delete")) {
                const row = try std.fmt.allocPrint(self.allocator, "- lines {d}..{d}\n", .{ start_line, end_line });
                defer self.allocator.free(row);
                try appendHashlinePreview(&out, self.allocator, row);
            } else {
                try self.appendPreviewLines(&out, "+ ", start_line, jsonString(obj, "content") orelse "");
            }
        }
        try self.preview.set(self.allocator, out.items);
    }

    fn appendPreviewLines(self: *AppState, out: *std.ArrayList(u8), marker: []const u8, first_line: usize, text: []const u8) !void {
        var line_no = first_line;
        var lines = std.mem.splitScalar(u8, text, '\n');
        while (lines.next()) |line| {
            if (line.len == 0 and line.ptr == text.ptr + text.len) break;
            const row = if (line_no > 0)
                try std.fmt.allocPrint(self.allocator, "{s}{d}|{s}\n", .{ marker, line_no, line })
            else
                try std.fmt.allocPrint(self.allocator, "{s}{s}\n", .{ marker, line });
            defer self.allocator.free(row);
            try appendHashlinePreview(out, self.allocator, row);
            if (line_no > 0) line_no += 1;
            if (out.items.len >= max_hashline_preview_bytes) {
                try markHashlinePreviewTruncated(out);
                return;
            }
        }
    }

    fn compactionNotice(allocator: std.mem.Allocator, payload: @TypeOf(@as(session_runtime.SessionEvent, undefined).compaction_end)) ![]u8 {
        var out: std.Io.Writer.Allocating = .init(allocator);
        defer out.deinit();
        const writer = &out.writer;
        if (payload.messages_before == 0 and payload.tokens_before == 0) {
            try writer.writeAll("conversation compacted");
            if (payload.tokens_after > 0) {
                try writer.writeAll(" · ~");
                try writeApproxTokens(writer, payload.tokens_after);
                try writer.writeAll(" tokens");
            }
        } else {
            try writer.print("conversation compacted · {d} messages · ~", .{payload.messages_before});
            try writeApproxTokens(writer, payload.tokens_before);
            try writer.writeAll(" → ~");
            try writeApproxTokens(writer, payload.tokens_after);
            try writer.writeAll(" tokens");
        }
        if (payload.transcript.slice().len > 0) try writer.print("\ntranscript: {s}", .{payload.transcript.slice()});
        try writer.print("\n\n{s}", .{agent.compaction.summaryOf(payload.text.slice())});
        return out.toOwnedSlice();
    }

    fn writeApproxTokens(writer: *std.Io.Writer, tokens: u64) !void {
        if (tokens < 1000) return writer.print("{d}", .{tokens});
        try writer.print("{d}.{d}k", .{ tokens / 1000, (tokens % 1000) / 100 });
    }

    fn markStreamingStarted(self: *AppState) void {
        if (self.status.streaming_since_ms == 0) self.status.streaming_since_ms = compat.time.nowMillis();
    }

    fn markStreamingStopped(self: *AppState) void {
        self.status.streaming_since_ms = 0;
        self.status.streaming_elapsed_ms = 0;
    }

    pub fn refreshStreamingElapsed(self: *AppState, now_ms: i64) void {
        if (self.status.streaming_since_ms == 0 or now_ms < self.status.streaming_since_ms) {
            self.status.streaming_elapsed_ms = 0;
            return;
        }
        self.status.streaming_elapsed_ms = @intCast(now_ms - self.status.streaming_since_ms);
    }

    fn appendThinkingDelta(self: *AppState, delta: []const u8) !void {
        const index = try self.thinkingEntryIndex();
        try self.transcript.items[index].text.appendSlice(self.allocator, delta);
    }

    fn thinkingEntryIndex(self: *AppState) !usize {
        if (self.active_thinking_entry) |index| {
            if (index < self.transcript.items.len and self.transcript.items[index].kind == .thinking) return index;
            self.active_thinking_entry = null;
        }
        const len = self.transcript.items.len;
        if (len > 0 and self.transcript.items[len - 1].kind == .thinking) {
            self.active_thinking_entry = len - 1;
            return len - 1;
        }
        if (self.active_assistant_entry) |index| {
            if (index + 1 == len and self.transcript.items[index].kind == .assistant and self.transcript.items[index].text.items.len == 0) {
                try self.transcript.insert(self.allocator, index, try TranscriptEntry.init(self.allocator, .thinking, ""));
                self.active_assistant_entry = index + 1;
                self.active_thinking_entry = index;
                return index;
            }
        }
        const index = try self.appendEmptyTranscript(.thinking);
        self.active_thinking_entry = index;
        return index;
    }

    fn ensureTrailingEntry(self: *AppState, kind: TranscriptKind) !usize {
        if (self.transcript.items.len == 0 or self.transcript.items[self.transcript.items.len - 1].kind != kind) {
            return try self.appendEmptyTranscript(kind);
        }
        return self.transcript.items.len - 1;
    }

    fn appendEmptyTranscript(self: *AppState, kind: TranscriptKind) !usize {
        try self.appendTranscript(kind, "");
        return self.transcript.items.len - 1;
    }

    fn appendDelta(self: *AppState, kind: TranscriptKind, delta: []const u8) !void {
        const index = switch (kind) {
            .assistant => try self.activeOrTrailingEntry(kind, &self.active_assistant_entry),
            else => try self.ensureTrailingEntry(kind),
        };
        try self.transcript.items[index].text.appendSlice(self.allocator, delta);
    }

    fn activeOrTrailingEntry(self: *AppState, kind: TranscriptKind, active_entry: *?usize) !usize {
        if (active_entry.*) |index| {
            if (index < self.transcript.items.len and self.transcript.items[index].kind == kind) return index;
            active_entry.* = null;
        }
        const index = try self.ensureTrailingEntry(kind);
        active_entry.* = index;
        return index;
    }

    fn finishTranscriptEntry(self: *AppState, kind: TranscriptKind, text: []const u8, active_entry: ?*?usize) !void {
        return self.finishTranscriptEntryWithOptions(kind, text, active_entry, false);
    }

    fn finishTranscriptEntryWithOptions(self: *AppState, kind: TranscriptKind, text: []const u8, active_entry: ?*?usize, dedupe_trailing: bool) !void {
        if (text.len == 0) {
            if (active_entry) |entry| {
                if (entry.*) |index| {
                    if (index < self.transcript.items.len and self.transcript.items[index].kind == kind and self.transcript.items[index].text.items.len == 0) {
                        self.removeTranscriptEntry(index);
                    }
                }
                entry.* = null;
            }
            return;
        }

        if (active_entry) |entry| {
            if (entry.*) |index| {
                if (index < self.transcript.items.len and self.transcript.items[index].kind == kind) {
                    try self.replaceEntryText(index, text);
                    entry.* = null;
                    return;
                }
            }
            entry.* = null;
        }

        if (dedupe_trailing and self.transcript.items.len > 0 and self.transcript.items[self.transcript.items.len - 1].kind == kind and std.mem.eql(u8, self.transcript.items[self.transcript.items.len - 1].text.items, text)) return;
        try self.appendTranscript(kind, text);
    }

    fn clearActiveTranscriptEntries(self: *AppState) void {
        self.active_user_entry = null;
        self.active_assistant_entry = null;
        self.active_thinking_entry = null;
        self.active_tool_result_entry = null;
        self.active_tool_summary_entry = null;
    }

    fn cleanupActiveTranscriptEntries(self: *AppState) void {
        self.removeEmptyActiveTranscriptEntry(&self.active_user_entry, .user);
        self.removeEmptyActiveTranscriptEntry(&self.active_assistant_entry, .assistant);
        self.removeEmptyActiveTranscriptEntry(&self.active_thinking_entry, .thinking);
        self.removeEmptyActiveTranscriptEntry(&self.active_tool_result_entry, .tool);
        self.removeEmptyActiveTranscriptEntry(&self.active_tool_summary_entry, .tool);
        self.clearActiveTranscriptEntries();
    }

    fn finalizeToolSummaryEntry(self: *AppState, summary: []const u8, tool_call_id: []const u8) !void {
        if (self.active_tool_summary_entry) |index| {
            if (index < self.transcript.items.len and self.transcript.items[index].kind == .tool) {
                const active_id = self.transcript.items[index].tool_call_id;
                if (active_id.len == 0 or std.mem.eql(u8, active_id, tool_call_id)) {
                    self.active_tool_summary_entry = null;
                    try self.replaceEntryText(index, summary);
                    try self.setEntryToolId(index, tool_call_id);
                    return;
                }
            } else {
                self.active_tool_summary_entry = null;
            }
        }
        try self.writeToolSummaryRow(summary, tool_call_id);
    }

    fn writeToolSummaryRow(self: *AppState, summary: []const u8, tool_call_id: []const u8) !void {
        if (try self.replaceLinkedSummaryRow(summary, tool_call_id)) return;
        if (try self.insertBeforeLinkedResultRow(summary, tool_call_id)) return;
        try self.appendToolSummaryTranscript(summary, tool_call_id);
    }

    fn insertBeforeLinkedResultRow(self: *AppState, summary: []const u8, tool_call_id: []const u8) !bool {
        if (self.active_tool_result_entry) |index| {
            if (index < self.transcript.items.len and self.transcript.items[index].kind == .tool and !self.transcript.items[index].tool_summary) {
                const active_id = self.transcript.items[index].tool_call_id;
                if (active_id.len == 0 or std.mem.eql(u8, active_id, tool_call_id)) {
                    try self.insertToolSummaryRowAt(index, summary, tool_call_id);
                    return true;
                }
            }
        }
        var i = self.summary_scan_floor;
        while (i < self.transcript.items.len) : (i += 1) {
            const entry = &self.transcript.items[i];
            if (entry.kind != .tool or entry.tool_summary) continue;
            if (!std.mem.eql(u8, entry.tool_call_id, tool_call_id)) continue;
            try self.insertToolSummaryRowAt(i, summary, tool_call_id);
            return true;
        }
        return false;
    }

    fn insertToolSummaryRowAt(self: *AppState, index: usize, summary: []const u8, tool_call_id: []const u8) !void {
        var row = try TranscriptEntry.init(self.allocator, .tool, summary);
        errdefer row.deinit(self.allocator);
        row.tool_summary = true;
        row.tool_call_id = try self.allocator.dupe(u8, tool_call_id);
        try self.transcript.insert(self.allocator, index, row);
        if (index < self.summary_scan_floor) self.summary_scan_floor = index;
        if (index < self.zen.start_index) self.zen.start_index += 1;
        self.adjustActiveTranscriptEntryAfterInsert(&self.active_user_entry, index);
        self.adjustActiveTranscriptEntryAfterInsert(&self.active_assistant_entry, index);
        self.adjustActiveTranscriptEntryAfterInsert(&self.active_tool_result_entry, index);
        self.adjustActiveTranscriptEntryAfterInsert(&self.active_tool_summary_entry, index);
    }

    fn replaceLinkedSummaryRow(self: *AppState, summary: []const u8, tool_call_id: []const u8) !bool {
        var i = self.transcript.items.len;
        while (i > self.summary_scan_floor) {
            i -= 1;
            const entry = &self.transcript.items[i];
            if (entry.kind != .tool or !entry.tool_summary) continue;
            if (!std.mem.eql(u8, entry.tool_call_id, tool_call_id)) continue;
            try self.replaceEntryText(i, summary);
            return true;
        }
        return false;
    }

    fn removeLinkedResultRows(self: *AppState, tool_call_id: []const u8) !void {
        var i = self.transcript.items.len;
        while (i > self.summary_scan_floor) {
            i -= 1;
            const entry = &self.transcript.items[i];
            if (entry.kind != .tool or entry.tool_summary) continue;
            if (!std.mem.eql(u8, entry.tool_call_id, tool_call_id)) continue;
            self.removeTranscriptEntry(i);
        }
    }

    fn removeEmptyActiveTranscriptEntry(self: *AppState, active_entry: *?usize, kind: TranscriptKind) void {
        if (active_entry.*) |index| {
            if (index < self.transcript.items.len and self.transcript.items[index].kind == kind and self.transcript.items[index].text.items.len == 0) {
                self.removeTranscriptEntry(index);
            }
        }
    }

    fn removeTranscriptEntry(self: *AppState, index: usize) void {
        if (index < self.summary_scan_floor) self.summary_scan_floor -= 1;
        if (index < self.zen.start_index) self.zen.start_index -= 1;
        var entry = self.transcript.orderedRemove(index);
        entry.deinit(self.allocator);
        self.adjustActiveTranscriptEntryAfterRemove(&self.active_user_entry, index);
        self.adjustActiveTranscriptEntryAfterRemove(&self.active_assistant_entry, index);
        self.adjustActiveTranscriptEntryAfterRemove(&self.active_thinking_entry, index);
        self.adjustActiveTranscriptEntryAfterRemove(&self.active_tool_result_entry, index);
        self.adjustActiveTranscriptEntryAfterRemove(&self.active_tool_summary_entry, index);
    }

    fn adjustActiveTranscriptEntryAfterRemove(self: *AppState, active_entry: *?usize, removed_index: usize) void {
        _ = self;
        if (active_entry.*) |index| {
            active_entry.* = if (index == removed_index) null else if (index > removed_index) index - 1 else index;
        }
    }

    fn adjustActiveTranscriptEntryAfterInsert(self: *AppState, active_entry: *?usize, inserted_index: usize) void {
        _ = self;
        if (active_entry.*) |index| {
            if (index >= inserted_index) active_entry.* = index + 1;
        }
    }

    fn replaceEntryText(self: *AppState, index: usize, text: []const u8) !void {
        const entry = &self.transcript.items[index];
        if (std.mem.eql(u8, entry.text.items, text)) return;
        entry.text.clearRetainingCapacity();
        try entry.text.appendSlice(self.allocator, text);
    }

    fn applyContextUsage(self: *AppState, payload: anytype) void {
        self.telemetry.estimated_tokens = payload.estimated_tokens;
        self.telemetry.context_window = self.status.context_limit;
        self.status.context_used = @intCast(payload.estimated_tokens);
    }

    fn applyToolTelemetry(self: *AppState, tool: *ToolEntry, raw_total_bytes: u64, returned_total_bytes: u64, estimated_returned_tokens: u64, artifact_count: u32, artifact_refs: []const u8) !void {
        if (raw_total_bytes > 0) tool.raw_total_bytes = raw_total_bytes;
        if (returned_total_bytes > 0) tool.returned_total_bytes = returned_total_bytes;
        if (estimated_returned_tokens > 0) tool.estimated_returned_tokens = estimated_returned_tokens;
        if (artifact_count > tool.artifact_count or (artifact_count == tool.artifact_count and artifact_refs.len > 0 and tool.artifact_refs.len == 0)) {
            tool.artifact_count = @max(tool.artifact_count, artifact_count);
            const owned_refs = try self.allocator.dupe(u8, artifact_refs);
            if (tool.artifact_refs.len > 0) self.allocator.free(tool.artifact_refs);
            tool.artifact_refs = owned_refs;
        }
        refreshTruncated(tool);
    }

    fn mergeTerminalOutput(self: *AppState, tool: *ToolEntry, payload: []const u8) !void {
        if (payload.len == 0) return;
        if (std.mem.eql(u8, tool.output.items, payload)) return;
        if (tool.output.items.len > 0) try tool.output.append(self.allocator, '\n');
        try tool.output.appendSlice(self.allocator, payload);
    }

    fn clearToolFamilies(self: *AppState) void {
        var it = self.tool_families.iterator();
        while (it.next()) |entry| self.allocator.free(entry.key_ptr.*);
        self.tool_families.clearRetainingCapacity();
    }

    fn liveFamilyTool(self: *AppState, provider_id: []const u8) ?*ToolEntry {
        const index = self.tool_families.get(provider_id) orelse return null;
        const tool = &self.tools.items[index];
        if (tool.status == .pending or tool.status == .running) return tool;
        return null;
    }

    fn latestFamilyTool(self: *AppState, provider_id: []const u8) ?*ToolEntry {
        const index = self.tool_families.get(provider_id) orelse return null;
        return &self.tools.items[index];
    }

    fn allocateToolOccurrence(self: *AppState, provider_id: []const u8, name: []const u8, args_json: []const u8, label: []const u8, status: ToolStatus) !*ToolEntry {
        const occurrence = if (self.tool_families.get(provider_id)) |index| self.tools.items[index].occurrence + 1 else 1;
        const key = if (occurrence == 1) try self.allocator.dupe(u8, provider_id) else try std.fmt.allocPrint(self.allocator, "{s}\x1f{d}", .{ provider_id, occurrence });
        defer self.allocator.free(key);
        var entry = try ToolEntry.init(self.allocator, key, name, label, args_json, status);
        entry.occurrence = occurrence;
        errdefer entry.deinit(self.allocator);
        try self.unfrozen_occurrence_ids.put(self.allocator, entry.id, {});
        errdefer _ = self.unfrozen_occurrence_ids.remove(entry.id);
        if (self.tool_families.getPtr(provider_id)) |latest| {
            if (isTerminalToolStatus(status)) try self.retire_candidates.append(self.allocator, self.tools.items.len);
            errdefer {
                if (isTerminalToolStatus(status)) _ = self.retire_candidates.pop();
            }
            try self.tools.append(self.allocator, entry);
            latest.* = self.tools.items.len - 1;
            return &self.tools.items[self.tools.items.len - 1];
        }
        const family_key = try self.allocator.dupe(u8, provider_id);
        errdefer self.allocator.free(family_key);
        const gop = try self.tool_families.getOrPut(self.allocator, family_key);
        gop.key_ptr.* = family_key;
        gop.value_ptr.* = self.tools.items.len;
        errdefer _ = self.tool_families.remove(provider_id);
        if (isTerminalToolStatus(status)) try self.retire_candidates.append(self.allocator, self.tools.items.len);
        errdefer {
            if (isTerminalToolStatus(status)) _ = self.retire_candidates.pop();
        }
        try self.tools.append(self.allocator, entry);
        return &self.tools.items[self.tools.items.len - 1];
    }

    pub fn finalizeInterruptedTools(self: *AppState) !void {
        self.cleanupActiveTranscriptEntries();
        var i = self.finalized_tool_count;
        while (i < self.tools.items.len) : (i += 1) {
            const tool = &self.tools.items[i];
            if (isTerminalToolStatus(tool.status)) continue;
            try self.retire_candidates.append(self.allocator, i);
            tool.status = .interrupted;
            const invocation = try toolInvocation(self.allocator, tool.label, tool.args_json);
            defer self.allocator.free(invocation);
            const message = try std.fmt.allocPrint(self.allocator, "{s} interrupted", .{invocation});
            defer self.allocator.free(message);
            try self.writeToolSummaryRow(message, tool.id);
        }
        while (self.finalized_tool_count < self.tools.items.len and isTerminalToolStatus(self.tools.items[self.finalized_tool_count].status)) self.finalized_tool_count += 1;
        self.advanceSummaryScanFloor();
    }

    pub fn retireToolOccurrences(self: *AppState) void {
        for (self.retire_candidates.items) |index| {
            const tool = &self.tools.items[index];
            tool.retired = true;
            _ = self.unfrozen_occurrence_ids.remove(tool.id);
        }
        self.retire_candidates.clearRetainingCapacity();
        self.advanceSummaryScanFloor();
    }

    pub fn advanceSummaryScanFloor(self: *AppState) void {
        while (self.summary_floor_tool < self.tools.items.len and self.tools.items[self.summary_floor_tool].isFrozen()) self.summary_floor_tool += 1;
        if (self.summary_floor_tool >= self.tools.items.len) {
            self.summary_scan_floor = self.transcript.items.len;
            return;
        }
        var i = self.summary_scan_floor;
        while (i < self.transcript.items.len) : (i += 1) {
            const entry = &self.transcript.items[i];
            if (entry.kind != .tool or entry.tool_call_id.len == 0) continue;
            if (self.unfrozen_occurrence_ids.contains(entry.tool_call_id)) {
                self.summary_scan_floor = i;
                return;
            }
        }
        self.summary_scan_floor = self.transcript.items.len;
    }

    fn rememberToolCalls(self: *AppState, tool_calls_json: []const u8) !void {
        if (tool_calls_json.len == 0) return;
        const owned = try self.allocator.dupe(u8, tool_calls_json);
        if (self.last_tool_calls_json.len > 0) self.allocator.free(self.last_tool_calls_json);
        self.last_tool_calls_json = owned;
    }

    fn recoverToolArgs(self: *AppState, tool_call_id: []const u8) !?[]u8 {
        if (self.last_tool_calls_json.len == 0 or tool_call_id.len == 0) return null;
        var parsed = std.json.parseFromSlice(std.json.Value, self.allocator, self.last_tool_calls_json, .{}) catch return null;
        defer parsed.deinit();
        if (parsed.value != .array) return null;
        for (parsed.value.array.items) |item| {
            if (item != .object) continue;
            const id = jsonString(item.object, "id") orelse continue;
            if (!std.mem.eql(u8, id, tool_call_id)) continue;
            const args = jsonString(item.object, "arguments_json") orelse return null;
            if (args.len == 0) return null;
            return try self.allocator.dupe(u8, args);
        }
        return null;
    }

    fn recoverToolArgsInto(self: *AppState, tool: *ToolEntry, provider_id: []const u8) !void {
        if (tool.args_json.len != 0) return;
        if (try self.recoverToolArgs(provider_id)) |args| {
            self.allocator.free(tool.args_json);
            tool.args_json = args;
        }
    }

    fn toolIndexOf(self: *const AppState, tool: *const ToolEntry) usize {
        return (@intFromPtr(tool) - @intFromPtr(self.tools.items.ptr)) / @sizeOf(ToolEntry);
    }

    fn terminalizeToolOccurrence(self: *AppState, tool: *ToolEntry, status: ToolStatus, half: TerminalEvidence) !void {
        if (!isTerminalToolStatus(tool.status)) try self.retire_candidates.append(self.allocator, self.toolIndexOf(tool));
        if (tool.terminal_evidence == .none) {
            tool.status = status;
            tool.terminal_evidence = half;
        } else if (tool.terminal_evidence != half and tool.terminal_evidence != .both) {
            tool.terminal_evidence = .both;
            if (status == .@"error") tool.status = .@"error";
        }
        if (tool.isFrozen()) _ = self.unfrozen_occurrence_ids.remove(tool.id);
    }

    fn emitToolErrorCard(self: *AppState, tool: *ToolEntry, raw_detail: []const u8, readable: bool) !void {
        const detail = try sanitizeTerminalText(self.allocator, raw_detail);
        defer self.allocator.free(detail);
        const message = try std.fmt.allocPrint(self.allocator, "{s} failed: {s}", .{ tool.label, detail });
        defer self.allocator.free(message);
        if (tool.error_card_emitted) {
            if (!readable) return;
            var i = self.transcript.items.len;
            while (i > self.summary_scan_floor) {
                i -= 1;
                const entry = &self.transcript.items[i];
                if (entry.kind != .@"error" or !std.mem.eql(u8, entry.tool_call_id, tool.id)) continue;
                try self.replaceEntryText(i, message);
                break;
            }
            try self.status.setError(self.allocator, message);
            return;
        }
        try self.status.setError(self.allocator, message);
        try self.appendTranscript(.@"error", message);
        try self.setEntryToolId(self.transcript.items.len - 1, tool.id);
        tool.error_card_emitted = true;
    }

    fn finishToolResultEntry(self: *AppState, text: []const u8, tool_call_id: []const u8) !void {
        if (text.len == 0) {
            if (self.active_tool_result_entry) |index| {
                if (index < self.transcript.items.len and self.transcript.items[index].kind == .tool and self.transcript.items[index].text.items.len == 0) {
                    self.removeTranscriptEntry(index);
                }
                self.active_tool_result_entry = null;
            }
            return;
        }
        if (self.active_tool_result_entry) |index| {
            if (index < self.transcript.items.len and self.transcript.items[index].kind == .tool) {
                try self.replaceEntryText(index, text);
                try self.setEntryToolId(index, tool_call_id);
                self.active_tool_result_entry = null;
                return;
            }
            self.active_tool_result_entry = null;
        }
        try self.appendTranscript(.tool, text);
        try self.setEntryToolId(self.transcript.items.len - 1, tool_call_id);
    }

    fn setEntryToolId(self: *AppState, index: usize, tool_call_id: []const u8) !void {
        if (tool_call_id.len == 0) return;
        const entry = &self.transcript.items[index];
        if (std.mem.eql(u8, entry.tool_call_id, tool_call_id)) return;
        const owned = try self.allocator.dupe(u8, tool_call_id);
        if (entry.tool_call_id.len > 0) self.allocator.free(entry.tool_call_id);
        entry.tool_call_id = owned;
    }

    pub fn resolveToolOccurrenceForTest(self: *AppState, id: []const u8, name: []const u8, args_json: []const u8, class: ToolEventClass, status: ToolStatus) !OccurrenceResolution {
        return try self.resolveToolOccurrence(id, name, args_json, class, status);
    }

    fn resolveToolOccurrence(self: *AppState, provider_id: []const u8, name: []const u8, args_json: []const u8, class: ToolEventClass, status: ToolStatus) !OccurrenceResolution {
        const label = self.toolLabel(name);
        if (self.liveFamilyTool(provider_id)) |tool| {
            if (class == .live_intent) tool.status = status;
            try self.refreshToolIdentity(tool, label, args_json);
            return .{ .tool = tool, .merge_state = true };
        }
        if (class != .live_intent) {
            if (self.latestFamilyTool(provider_id)) |tool| {
                if (!tool.retired) switch (class) {
                    .execution_outcome => if (tool.terminal_evidence == .none or tool.terminal_evidence == .result) {
                        try self.refreshToolIdentity(tool, label, args_json);
                        return .{ .tool = tool, .merge_state = true };
                    },
                    .result_outcome => switch (tool.terminal_evidence) {
                        .none => {
                            try self.refreshToolIdentity(tool, label, args_json);
                            return .{ .tool = tool, .merge_state = true };
                        },
                        .execution => return .{ .tool = tool, .merge_state = false },
                        .result, .both => {},
                    },
                    .live_intent => {},
                };
            }
        }
        const tool = try self.allocateToolOccurrence(provider_id, name, args_json, label, status);
        return .{ .tool = tool, .merge_state = true };
    }

    fn refreshToolIdentity(self: *AppState, tool: *ToolEntry, label: []const u8, args_json: []const u8) !void {
        if (label.len > 0 and !std.mem.eql(u8, tool.label, label)) {
            const owned = try self.allocator.dupe(u8, label);
            self.allocator.free(tool.label);
            tool.label = owned;
        }
        if (args_json.len > 0 and !std.mem.eql(u8, tool.args_json, args_json)) {
            const owned = try self.allocator.dupe(u8, args_json);
            self.allocator.free(tool.args_json);
            tool.args_json = owned;
        }
    }
};

fn approvalScopeHint(allocator: std.mem.Allocator, tool_name: []const u8, args_json: []const u8) ![]u8 {
    const safe_tool_name = try sanitizeTerminalText(allocator, tool_name);
    defer allocator.free(safe_tool_name);

    var parsed = std.json.parseFromSlice(std.json.Value, allocator, args_json, .{}) catch return std.fmt.allocPrint(allocator, "{s} (one tool call)", .{safe_tool_name});
    defer parsed.deinit();
    if (parsed.value != .object) return std.fmt.allocPrint(allocator, "{s} (one tool call)", .{safe_tool_name});
    const obj = parsed.value.object;
    if (firstJsonString(obj, &.{ "path", "file_path", "target_path", "cwd" })) |path| {
        const safe_path = try sanitizeTerminalText(allocator, path);
        defer allocator.free(safe_path);
        return std.fmt.allocPrint(allocator, "{s} path {s}", .{ safe_tool_name, safe_path });
    }
    if (firstJsonString(obj, &.{ "command", "cmd", "script" })) |command| {
        const safe_command = try sanitizeTerminalText(allocator, command);
        defer allocator.free(safe_command);
        return std.fmt.allocPrint(allocator, "{s} command {s}", .{ safe_tool_name, safe_command });
    }
    return std.fmt.allocPrint(allocator, "{s} (one tool call)", .{safe_tool_name});
}

fn sanitizeTerminalText(allocator: std.mem.Allocator, text: []const u8) ![]u8 {
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    var i: usize = 0;
    while (i < text.len) {
        const c = text[i];
        switch (c) {
            '\n', '\r', '\t' => {
                try writer.writeByte(' ');
                i += 1;
                continue;
            },
            0x00...0x08, 0x0b, 0x0c, 0x0e...0x1f, 0x7f => {
                i += 1;
                continue;
            },
            else => {},
        }
        const len = std.unicode.utf8ByteSequenceLength(c) catch {
            i += 1;
            continue;
        };
        if (i + len > text.len) break;
        const codepoint = std.unicode.utf8Decode(text[i .. i + len]) catch {
            i += 1;
            continue;
        };
        if (codepoint < 0x20 or codepoint == 0x7f or (codepoint >= 0x80 and codepoint <= 0x9f)) {
            i += len;
            continue;
        }
        try writer.writeAll(text[i .. i + len]);
        i += len;
    }
    return out.toOwnedSlice();
}

fn collapseHomePath(allocator: std.mem.Allocator, path: []const u8, home: ?[]const u8) ![]u8 {
    const value = home orelse return allocator.dupe(u8, path);
    if (value.len <= 1) return allocator.dupe(u8, path);
    if (std.mem.eql(u8, path, value)) return allocator.dupe(u8, "~");
    if (std.mem.startsWith(u8, path, value) and path.len > value.len and path[value.len] == std.fs.path.sep) {
        return std.fmt.allocPrint(allocator, "~{s}", .{path[value.len..]});
    }
    return allocator.dupe(u8, path);
}

pub fn agentCwdFromArgs(allocator: std.mem.Allocator, args_json: []const u8) !?[]u8 {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, args_json, .{}) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return null,
    };
    defer parsed.deinit();
    if (parsed.value != .object) return null;
    const root = firstJsonString(parsed.value.object, &.{"workspace_root"}) orelse return null;
    if (!std.fs.path.isAbsolute(root)) return null;
    return sanitizeTerminalText(allocator, root) catch |err| switch (err) {
        error.WriteFailed => error.OutOfMemory,
        else => |e| e,
    };
}

pub fn pathWithinForTest(allocator: std.mem.Allocator, path: []const u8, root: []const u8) !bool {
    return pathWithin(allocator, path, root);
}

fn pathWithin(allocator: std.mem.Allocator, path: []const u8, root: []const u8) !bool {
    if (root.len == 0) return false;
    const resolved_path = try std.fs.path.resolve(allocator, &.{path});
    defer allocator.free(resolved_path);
    const resolved_root = try std.fs.path.resolve(allocator, &.{root});
    defer allocator.free(resolved_root);
    if (std.mem.eql(u8, resolved_path, resolved_root)) return true;
    if (!std.mem.startsWith(u8, resolved_path, resolved_root)) return false;
    if (resolved_root[resolved_root.len - 1] == std.fs.path.sep) return true;
    return resolved_path.len > resolved_root.len and resolved_path[resolved_root.len] == std.fs.path.sep;
}

fn firstJsonString(obj: std.json.ObjectMap, keys: []const []const u8) ?[]const u8 {
    for (keys) |key| {
        if (jsonString(obj, key)) |value| return value;
    }
    return null;
}

fn appendHashlinePreview(out: *std.ArrayList(u8), allocator: std.mem.Allocator, text: []const u8) !void {
    if (out.items.len >= max_hashline_preview_bytes) return;
    const remaining = max_hashline_preview_bytes - out.items.len;
    if (text.len <= remaining) {
        try out.appendSlice(allocator, text);
        return;
    }
    if (remaining > 0) try out.appendSlice(allocator, text[0..remaining]);
    try markHashlinePreviewTruncated(out);
}

fn markHashlinePreviewTruncated(out: *std.ArrayList(u8)) !void {
    if (out.items.len < hashline_preview_truncated_marker.len) return;
    const marker_start = out.items.len - hashline_preview_truncated_marker.len;
    @memcpy(out.items[marker_start..], hashline_preview_truncated_marker);
}

fn jsonString(obj: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = obj.get(key) orelse return null;
    if (value != .string) return null;
    return value.string;
}

fn jsonUsize(obj: std.json.ObjectMap, key: []const u8) ?usize {
    const value = obj.get(key) orelse return null;
    return switch (value) {
        .integer => |i| if (i < 0) null else @intCast(i),
        .number_string => |s| std.fmt.parseUnsigned(usize, s, 10) catch null,
        else => null,
    };
}

fn toolInvocation(allocator: std.mem.Allocator, name: []const u8, args_json: []const u8) ![]u8 {
    const primary = primaryToolArg(allocator, args_json) catch null;
    defer if (primary) |value| allocator.free(value);
    if (primary) |value| {
        const clipped = try clipSummaryArg(allocator, value);
        defer allocator.free(clipped);
        return std.fmt.allocPrint(allocator, "◈ {s} \"{s}\"", .{ name, clipped });
    }
    return std.fmt.allocPrint(allocator, "◈ {s}", .{name});
}

fn toolResultSummary(allocator: std.mem.Allocator, name: []const u8, args_json: []const u8, result_json: []const u8, is_error: bool, raw_total_bytes: u64, returned_total_bytes: u64, estimated_tokens: u64, artifact_count: u32) ![]u8 {
    const invocation = try toolInvocation(allocator, name, args_json);
    defer allocator.free(invocation);
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    try writer.print("{s} {s}", .{ invocation, if (is_error) "failed" else "ok" });
    if (raw_total_bytes > 0 or returned_total_bytes > 0) {
        try writer.print(" raw={d}B returned={d}B", .{ raw_total_bytes, returned_total_bytes });
    } else {
        try writer.print(" output={d}B", .{result_json.len});
    }
    if (estimated_tokens > 0) try writer.print(" ~{d} tok", .{estimated_tokens});
    if (artifact_count > 0) try writer.print(" artifacts={d} on disk", .{artifact_count});
    if (raw_total_bytes > returned_total_bytes or artifact_count > 0) try writer.writeAll(" preview-capped");
    if (is_error and result_json.len > 0) {
        const unwrapped = try toolErrorMessage(allocator, result_json);
        defer if (unwrapped) |message| allocator.free(message);
        const preview_source = if (unwrapped) |message| message else result_json;
        const preview = try clipSummaryArg(allocator, preview_source);
        defer allocator.free(preview);
        try writer.print(" \"{s}\"", .{preview});
    }
    return out.toOwnedSlice();
}

fn toolErrorMessage(allocator: std.mem.Allocator, result_json: []const u8) !?[]u8 {
    if (result_json.len == 0) return null;
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, result_json, .{}) catch return null;
    defer parsed.deinit();
    if (parsed.value != .object) return null;
    const message = jsonString(parsed.value.object, "err") orelse return null;
    if (message.len == 0) return null;
    return try allocator.dupe(u8, message);
}

fn plainTextErrorDetail(allocator: std.mem.Allocator, result_json: []const u8) bool {
    if (result_json.len == 0) return false;
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, result_json, .{}) catch return true;
    defer parsed.deinit();
    return parsed.value != .object and parsed.value != .null;
}

fn countArtifactsJson(allocator: std.mem.Allocator, artifacts_json: []const u8) u32 {
    if (artifacts_json.len == 0) return 0;
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, artifacts_json, .{}) catch return 0;
    defer parsed.deinit();
    if (parsed.value != .array) return 0;
    return @intCast(parsed.value.array.items.len);
}

fn refreshTruncated(tool: *ToolEntry) void {
    tool.truncated = tool.raw_total_bytes > tool.returned_total_bytes or tool.artifact_count > 0;
}

fn primaryToolArg(allocator: std.mem.Allocator, args_json: []const u8) !?[]u8 {
    if (args_json.len == 0) return null;
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, args_json, .{}) catch return null;
    defer parsed.deinit();
    if (parsed.value != .object) return null;
    const keys = [_][]const u8{ "description", "command", "path", "query", "pattern", "file", "operation" };
    for (keys) |key| {
        if (jsonString(parsed.value.object, key)) |value| {
            if (value.len > 0) return try allocator.dupe(u8, value);
        }
    }
    return null;
}

const max_summary_arg_width: usize = 512;

fn clipSummaryArg(allocator: std.mem.Allocator, value: []const u8) ![]u8 {
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    var width: usize = 0;
    var i: usize = 0;
    while (i < value.len and width < max_summary_arg_width) {
        const c = value[i];
        if (c == '\n' or c == '\r' or c == '\t') {
            try writer.writeByte(' ');
            width += 1;
            i += 1;
            continue;
        }
        if (c < 0x20 or c == 0x7f) {
            i += 1;
            continue;
        }
        const len = std.unicode.utf8ByteSequenceLength(c) catch 1;
        if (i + len > value.len) break;
        if (len == 1) {
            try writer.writeByte(c);
        } else {
            const codepoint = std.unicode.utf8Decode(value[i .. i + len]) catch {
                i += 1;
                continue;
            };
            if (codepoint < 0x20 or codepoint == 0x7f or (codepoint >= 0x80 and codepoint <= 0x9f)) {
                i += len;
                continue;
            }
            try writer.writeAll(value[i .. i + len]);
        }
        width += 1;
        i += len;
    }
    if (i < value.len) try writer.writeAll("…");
    return out.toOwnedSlice();
}

fn ownedText(text: []const u8) !@import("owned_slice").OwnedSlice(u8) {
    return @import("owned_slice").OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, text));
}

fn toolStartEvent(id: []const u8, name: []const u8, args_json: []const u8) !session_runtime.SessionEvent {
    return .{ .tool_execution_start = .{
        .tool_call_id = try ownedText(id),
        .tool_name = try ownedText(name),
        .args_json = try ownedText(args_json),
    } };
}

fn toolEndEvent(id: []const u8, name: []const u8, result_json: []const u8, is_error: bool) !session_runtime.SessionEvent {
    return .{ .tool_execution_end = .{
        .tool_call_id = try ownedText(id),
        .tool_name = try ownedText(name),
        .result_json = try ownedText(result_json),
        .is_error = is_error,
    } };
}

fn toolResultMessageEvent(id: []const u8, name: []const u8, text: []const u8, details_json: []const u8, is_error: bool) !session_runtime.SessionEvent {
    return .{ .message_end = .{
        .role = .tool_result,
        .tool_call_id = try ownedText(id),
        .tool_name = try ownedText(name),
        .text = try ownedText(text),
        .details_json = try ownedText(details_json),
        .is_error = is_error,
    } };
}

fn countRows(state: *const AppState, summary: bool, id: []const u8) usize {
    var count: usize = 0;
    for (state.transcript.items) |*entry| {
        if (entry.kind != .tool or entry.tool_summary != summary) continue;
        if (!std.mem.eql(u8, entry.tool_call_id, id)) continue;
        count += 1;
    }
    return count;
}

pub fn noopToolForTest(
    tool_call_id: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?agent.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!agent.AgentToolResult {
    _ = tool_call_id;
    _ = args_json;
    _ = cancel_token;
    _ = on_update_ctx;
    _ = on_update;
    _ = allocator;
    return error.NotImplemented;
}

test "a reply drained in one pass is timed from when its events were queued, not when they were applied" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .turn_start = .{} });
    try state.applyEvent(.{ .message_start = .{ .at_ms = 1_000, .role = .assistant } });
    try state.applyEvent(.{ .text_delta = .{ .at_ms = 1_500, .content_index = 0, .delta = ai_types.OwnedSlice(u8).initBorrowed("hello") } });
    try state.applyEvent(.{ .message_end = .{ .at_ms = 5_000, .role = .assistant, .text = ai_types.OwnedSlice(u8).initBorrowed("hello"), .output_tokens = 400 } });
    try state.applyEvent(.{ .turn_end = .{ .stop_reason = .stop } });

    try std.testing.expectEqual(@as(u64, 100), state.telemetry.rate.previous.perSecond());
}

test "a reply measured over less than a tenth of a second is left out rather than reported" {
    var rate = TokenRateSet{};
    rate.messageStarted(1_000);
    rate.produced(400, 1_000);
    rate.messageEnded(1_001, 1_199);
    rate.turnEnded();

    try std.testing.expect(!rate.previous.hasFigure());
    try std.testing.expect(!rate.average.hasFigure());
}

test "a measured turn reports the provider's own output tokens" {
    var rate = TokenRateSet{};
    rate.produced(400, 1_000);
    rate.messageEnded(3_000, 150);
    rate.turnEnded();

    try std.testing.expectEqual(@as(u64, 150), rate.previous.output_tokens);
    try std.testing.expectEqual(@as(u64, 2_000), rate.previous.stream_ms);
    try std.testing.expectEqual(@as(u64, 75), rate.previous.perSecond());
    try std.testing.expect(!rate.previous.estimated);
    try std.testing.expectEqual(@as(u64, 75), rate.average.perSecond());
    try std.testing.expect(!rate.average.estimated);
}

test "a turn with no reported usage is estimated from the bytes that streamed" {
    var rate = TokenRateSet{};
    rate.produced(400, 1_000);
    rate.messageEnded(3_000, 0);
    rate.turnEnded();

    try std.testing.expectEqual(@as(u64, 100), rate.previous.output_tokens);
    try std.testing.expect(rate.previous.estimated);
    try std.testing.expectEqual(@as(u64, 50), rate.previous.perSecond());
}

test "a turn's stream time is its messages' spans, so tool time between them never counts" {
    var rate = TokenRateSet{};
    rate.produced(400, 1_000);
    rate.messageEnded(2_000, 100);
    rate.produced(400, 60_000);
    rate.messageEnded(61_000, 100);
    rate.turnEnded();

    try std.testing.expectEqual(@as(u64, 200), rate.previous.output_tokens);
    try std.testing.expectEqual(@as(u64, 2_000), rate.previous.stream_ms);
    try std.testing.expectEqual(@as(u64, 100), rate.previous.perSecond());
}

test "the average never mixes a measured turn with an estimated one" {
    var rate = TokenRateSet{};
    rate.produced(400, 1_000);
    rate.messageEnded(2_000, 0);
    rate.turnEnded();
    try std.testing.expect(rate.previous.estimated);

    rate.produced(400, 3_000);
    rate.messageEnded(5_000, 400);
    rate.turnEnded();

    try std.testing.expect(!rate.previous.estimated);
    try std.testing.expectEqual(@as(u64, 400), rate.average.output_tokens);
    try std.testing.expectEqual(@as(u64, 2_000), rate.average.stream_ms);
    try std.testing.expect(!rate.average.estimated);
}

test "with no measured sample at all the average is of the estimates, and says so" {
    var rate = TokenRateSet{};
    rate.produced(400, 1_000);
    rate.messageEnded(2_000, 0);
    rate.turnEnded();
    rate.produced(800, 3_000);
    rate.messageEnded(5_000, 0);
    rate.turnEnded();

    try std.testing.expectEqual(@as(u64, 300), rate.average.output_tokens);
    try std.testing.expectEqual(@as(u64, 3_000), rate.average.stream_ms);
    try std.testing.expect(rate.average.estimated);
}

test "the shown figure is the live one while a message streams, then the turn's" {
    var rate = TokenRateSet{};
    rate.runStarted();
    rate.produced(400, 1_000);
    rate.liveAt(2_000);
    try std.testing.expect(rate.live.hasFigure());
    try std.testing.expect(rate.live.estimated);
    try std.testing.expectEqual(@as(u64, 100), rate.turnShown().output_tokens);
    try std.testing.expectEqual(@as(u64, 1_000), rate.turnShown().stream_ms);

    rate.messageEnded(2_000, 100);
    rate.turnEnded();
    try std.testing.expect(!rate.live.hasFigure());
    try std.testing.expectEqual(@as(u64, 100), rate.turnShown().output_tokens);
    try std.testing.expect(!rate.turnShown().estimated);
}

test "an idle rate carries the last turn and the average since the model switch" {
    var rate = TokenRateSet{};
    rate.runStarted();
    rate.produced(400, 1_000);
    rate.messageEnded(2_000, 100);
    rate.turnEnded();
    rate.runEnded();

    rate.produced(400, 10_000_000);
    rate.messageEnded(10_000_500, 100);
    rate.turnEnded();
    rate.runEnded();

    try std.testing.expectEqual(@as(u64, 200), rate.average.output_tokens);
    try std.testing.expectEqual(@as(u64, 1_500), rate.average.stream_ms);
    try std.testing.expectEqual(@as(u64, 100), rate.turnShown().output_tokens);
    try std.testing.expectEqual(@as(u64, 500), rate.turnShown().stream_ms);
}

test "a run in progress shows its own last turn, ahead of the average" {
    var rate = TokenRateSet{};
    rate.runStarted();
    rate.produced(400, 1_000);
    rate.messageEnded(2_000, 100);
    rate.turnEnded();
    rate.runEnded();
    try std.testing.expectEqual(@as(u64, 100), rate.average.output_tokens);

    rate.runStarted();
    rate.produced(400, 10_000_000);
    rate.messageEnded(10_000_500, 900);
    rate.turnEnded();

    try std.testing.expectEqual(@as(u64, 900), rate.turnShown().output_tokens);
    try std.testing.expectEqual(@as(u64, 1_000), rate.average.output_tokens);
}

test "a run that measured nothing yet falls back to the average rather than nothing" {
    var rate = TokenRateSet{};
    rate.produced(400, 1_000);
    rate.messageEnded(2_000, 100);
    rate.turnEnded();
    rate.runEnded();

    rate.runStarted();
    try std.testing.expect(!rate.previous.hasFigure());
    try std.testing.expect(!rate.turnShown().hasFigure());
    try std.testing.expectEqual(@as(u64, 100), rate.average.output_tokens);
    try std.testing.expectEqual(@as(u64, 1_000), rate.average.stream_ms);
}

test "a new run does not open showing the run before it" {
    var rate = TokenRateSet{};
    rate.runStarted();
    rate.produced(400, 1_000);
    rate.messageEnded(2_000, 900);
    rate.turnEnded();
    rate.runEnded();
    try std.testing.expectEqual(@as(u64, 900), rate.previous.output_tokens);

    rate.runStarted();
    try std.testing.expect(!rate.previous.hasFigure());
    try std.testing.expect(!rate.turnShown().hasFigure());
    try std.testing.expectEqual(@as(u64, 900), rate.average.output_tokens);
    try std.testing.expectEqual(@as(u64, 1_000), rate.average.stream_ms);
}

test "a turn that produced nothing reports nothing" {
    var rate = TokenRateSet{};
    rate.turnEnded();
    try std.testing.expect(!rate.previous.hasFigure());
    try std.testing.expect(!rate.turnShown().hasFigure());
    try std.testing.expectEqual(@as(u64, 0), rate.turnShown().perSecond());
}

test "a turn aborted mid-stream leaves no live figure and no clock for the next turn" {
    var rate = TokenRateSet{};
    rate.produced(400, 1_000);
    rate.liveAt(2_000);
    try std.testing.expect(rate.live.hasFigure());

    rate.turnEnded();

    try std.testing.expect(!rate.live.hasFigure());
    try std.testing.expect(!rate.turnShown().hasFigure());
    rate.liveAt(9_000_000);
    try std.testing.expect(!rate.live.hasFigure());

    rate.produced(400, 10_000_000);
    rate.messageEnded(10_000_500, 100);
    rate.turnEnded();
    try std.testing.expectEqual(@as(u64, 500), rate.previous.stream_ms);
    try std.testing.expectEqual(@as(u64, 100), rate.previous.output_tokens);
}

test "a message that arrives whole, with no stream, contributes neither tokens nor time" {
    var rate = TokenRateSet{};
    rate.messageEnded(2_000, 400);

    try std.testing.expect(!rate.turn().hasFigure());
    rate.turnEnded();
    try std.testing.expect(!rate.previous.hasFigure());
    try std.testing.expect(!rate.measured_since_switch.hasFigure());
    try std.testing.expect(!rate.estimated_since_switch.hasFigure());
    try std.testing.expect(!rate.average.hasFigure());
}

test "a tool-call-only message is production, so its usage is measured over a real span" {
    var rate = TokenRateSet{};
    rate.produced(120, 1_000);
    rate.messageEnded(3_000, 60);
    rate.turnEnded();

    try std.testing.expectEqual(@as(u64, 60), rate.previous.output_tokens);
    try std.testing.expectEqual(@as(u64, 2_000), rate.previous.stream_ms);
    try std.testing.expect(!rate.previous.estimated);
    try std.testing.expectEqual(@as(u64, 30), rate.previous.perSecond());
}

test "a turn with one measured and one estimated message is marked, and pools them apart" {
    var rate = TokenRateSet{};
    rate.produced(400, 1_000);
    rate.messageEnded(2_000, 0);
    rate.produced(400, 3_000);
    rate.messageEnded(4_000, 200);
    rate.turnEnded();

    try std.testing.expect(rate.previous.estimated);
    try std.testing.expectEqual(@as(u64, 300), rate.previous.output_tokens);
    try std.testing.expectEqual(@as(u64, 2_000), rate.previous.stream_ms);

    try std.testing.expectEqual(@as(u64, 200), rate.measured_since_switch.output_tokens);
    try std.testing.expectEqual(@as(u64, 1_000), rate.measured_since_switch.stream_ms);
    try std.testing.expectEqual(@as(u64, 100), rate.estimated_since_switch.output_tokens);
    try std.testing.expectEqual(@as(u64, 1_000), rate.estimated_since_switch.stream_ms);

    try std.testing.expectEqual(@as(u64, 200), rate.average.output_tokens);
    try std.testing.expectEqual(@as(u64, 1_000), rate.average.stream_ms);
    try std.testing.expect(!rate.average.estimated);
}

test "the agent_end that follows every turn_end leaves the previous turn's figure standing" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    state.telemetry.rate.turn_measured = .{ .output_tokens = 200, .stream_ms = 2_000 };
    state.telemetry.rate.turn_estimated = .{};

    try state.applyEvent(.{ .turn_end = .{ .stop_reason = .stop } });
    try std.testing.expect(state.telemetry.rate.previous.hasFigure());
    try std.testing.expectEqual(@as(u64, 100), state.telemetry.rate.previous.perSecond());

    try state.applyEvent(.{ .agent_end = .{ .reason = .completed } });
    try std.testing.expect(state.telemetry.rate.previous.hasFigure());
    try std.testing.expectEqual(@as(u64, 200), state.telemetry.rate.previous.output_tokens);
    try std.testing.expectEqual(@as(u64, 2_000), state.telemetry.rate.previous.stream_ms);
    try std.testing.expectEqual(@as(u64, 100), state.telemetry.rate.average.perSecond());
}

test "a turn that produced nothing leaves the last real figure standing" {
    var rate = TokenRateSet{};
    rate.produced(400, 1_000);
    rate.messageEnded(2_000, 100);
    rate.turnEnded();
    try std.testing.expectEqual(@as(u64, 100), rate.previous.perSecond());

    rate.turnEnded();
    try std.testing.expectEqual(@as(u64, 100), rate.previous.perSecond());
    try std.testing.expectEqual(@as(u64, 100), rate.measured_since_switch.output_tokens);
    try std.testing.expectEqual(@as(u64, 1_000), rate.measured_since_switch.stream_ms);
}

test "a turn's tool phase shows the turn's own figure, not a lagging average" {
    var rate = TokenRateSet{};
    rate.runStarted();

    rate.produced(400, 1_000);
    rate.messageEnded(2_000, 400);
    try std.testing.expectEqual(@as(u64, 400), rate.turn().output_tokens);
    try std.testing.expectEqual(@as(u64, 400), rate.turnShown().output_tokens);
    try std.testing.expect(!rate.turnShown().estimated);

    rate.turnEnded();
    try std.testing.expectEqual(@as(u64, 400), rate.previous.output_tokens);

    rate.runStarted();
    rate.produced(400, 10_000_000);
    rate.messageEnded(10_000_500, 100);
    rate.turnEnded();

    try std.testing.expectEqual(@as(u64, 100), rate.turnShown().output_tokens);
    try std.testing.expectEqual(@as(u64, 500), rate.average.output_tokens);
}

test "a model switch mid-message keeps that message's clock, so its tokens divide by the real span" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .agent_start = .{} });
    try state.applyEvent(.{ .turn_start = .{} });
    try state.applyEvent(.{ .message_start = .{ .role = .assistant } });
    const began = state.telemetry.rate.message_first_ms;
    state.telemetry.rate.produced(400, began + 1_000);

    state.telemetry.rate.resetForModel();

    try std.testing.expect(state.telemetry.rate.message_first_ms == began);
    try std.testing.expectEqual(@as(u64, 400), state.telemetry.rate.message_bytes);
    try std.testing.expect(!state.telemetry.rate.average.hasFigure());
    try std.testing.expect(!state.telemetry.rate.turn_measured.hasFigure());

    state.telemetry.rate.produced(400, began + 4_000);
    state.telemetry.rate.messageEnded(began + 4_000, 800);
    state.telemetry.rate.turnEnded();

    try std.testing.expectEqual(@as(u64, 800), state.telemetry.rate.previous.output_tokens);
    try std.testing.expectEqual(@as(u64, 4_000), state.telemetry.rate.previous.stream_ms);
    try std.testing.expectEqual(@as(u64, 200), state.telemetry.rate.previous.perSecond());
}

test "a second turn's thinking still shows the first turn's figure" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .agent_start = .{} });
    try state.applyEvent(.{ .turn_start = .{} });
    try state.applyEvent(.{ .message_start = .{ .role = .assistant } });
    const began = state.telemetry.rate.message_first_ms;
    state.telemetry.rate.produced(400, began + 2_000);
    state.telemetry.rate.messageEnded(began + 2_000, 400);
    try state.applyEvent(.{ .turn_end = .{ .stop_reason = .stop } });
    try std.testing.expectEqual(@as(u64, 200), state.telemetry.rate.previous.perSecond());

    try state.applyEvent(.{ .turn_start = .{} });
    try state.applyEvent(.{ .message_start = .{ .role = .assistant } });
    state.telemetry.rate.produced(80, state.telemetry.rate.message_first_ms + 40);
    state.telemetry.rate.liveAt(state.telemetry.rate.message_first_ms + 200);

    try std.testing.expect(!state.telemetry.rate.live.hasFigure());
    try std.testing.expectEqual(@as(u64, 400), state.telemetry.rate.turnShown().output_tokens);
    try std.testing.expectEqual(@as(u64, 2_000), state.telemetry.rate.turnShown().stream_ms);
}

test "a message that thinks for half a minute is measured from when it began" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.applyEvent(.{ .turn_start = .{} });
    try state.applyEvent(.{ .message_start = .{ .role = .assistant } });

    const began = state.telemetry.rate.message_first_ms;
    try std.testing.expect(began != 0);
    state.telemetry.rate.produced(400, began + 30_000);
    state.telemetry.rate.messageEnded(began + 34_000, 20_000);
    state.telemetry.rate.turnEnded();

    try std.testing.expectEqual(@as(u64, 20_000), state.telemetry.rate.previous.output_tokens);
    try std.testing.expectEqual(@as(u64, 34_000), state.telemetry.rate.previous.stream_ms);
    try std.testing.expect(!state.telemetry.rate.previous.estimated);
    try std.testing.expectEqual(@as(u64, 588), state.telemetry.rate.previous.perSecond());
}

test "the live figure waits for a second of streaming, then reports" {
    var rate = TokenRateSet{};
    rate.runStarted();
    rate.produced(400, 1_000);
    rate.produced(400, 1_200);
    rate.liveAt(1_400);
    try std.testing.expect(!rate.live.hasFigure());

    rate.liveAt(2_000);
    try std.testing.expect(rate.live.hasFigure());
    try std.testing.expect(rate.live.estimated);
}

test "the turn figure stands in while the live figure is still too young" {
    var rate = TokenRateSet{};
    rate.runStarted();
    rate.produced(400, 1_000);
    rate.messageEnded(3_000, 400);
    rate.turnEnded();

    rate.produced(400, 10_000);
    rate.liveAt(10_200);
    try std.testing.expect(!rate.live.hasFigure());
    try std.testing.expectEqual(@as(u64, 400), rate.turnShown().output_tokens);
    try std.testing.expectEqual(@as(u64, 200), rate.turnShown().perSecond());
}

test "a rate with no time or no tokens never reads as speed" {
    const no_time = TokenRate{ .output_tokens = 500, .stream_ms = 0 };
    try std.testing.expect(!no_time.hasFigure());
    try std.testing.expectEqual(@as(u64, 0), no_time.perSecond());
    const no_tokens = TokenRate{ .output_tokens = 0, .stream_ms = 500 };
    try std.testing.expect(!no_tokens.hasFigure());
    try std.testing.expectEqual(@as(u64, 0), no_tokens.perSecond());
}

test "bytes convert at the agent's own divisor" {
    try std.testing.expectEqual(@as(u64, 0), estimateTokenBytes(0));
    try std.testing.expectEqual(@as(u64, 1), estimateTokenBytes(1));
    try std.testing.expectEqual(@as(u64, 1), estimateTokenBytes(4));
    try std.testing.expectEqual(@as(u64, 2), estimateTokenBytes(5));
    try std.testing.expectEqual(@as(u64, 100), estimateTokenBytes(400));
}

test "AppState applies transcript and tool events" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();
    const tools = [_]agent.AgentTool{.{
        .label = "Shell Execute",
        .name = "shell_command",
        .description = "Run shell command",
        .short_description = "Run shell",
        .parameters_schema_json = "{}",
        .execute = noopToolForTest,
    }};
    try state.setRegisteredTools(&tools);

    var text_event = session_runtime.SessionEvent{ .text_delta = .{ .content_index = 0, .delta = try ownedText("hello") } };
    defer text_event.deinit(std.testing.allocator);
    try state.applyEvent(text_event);

    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.assistant, state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("hello", state.transcript.items[0].text.items);

    var final_text_event = session_runtime.SessionEvent{ .message_end = .{ .role = .assistant, .text = try ownedText("hello world") } };
    defer final_text_event.deinit(std.testing.allocator);
    try state.applyEvent(final_text_event);

    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.assistant, state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("hello world", state.transcript.items[0].text.items);

    var start_event = try toolStartEvent("call-1", "shell_command", "{\"description\":\"Check the current workspace directory\",\"command\":\"pwd\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);

    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.tool, state.transcript.items[1].kind);
    try std.testing.expect(state.transcript.items[1].tool_summary);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[1].text.items, "◈ Shell Execute \"Check the current workspace directory\"") != null);
    try std.testing.expectEqual(@as(usize, 1), state.active_tool_summary_entry.?);

    var end_event = try toolEndEvent("call-1", "shell_command", "{\"ok\":true}", false);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try std.testing.expectEqual(@as(usize, 1), state.tools.items.len);
    try std.testing.expectEqual(ToolStatus.done, state.tools.items[0].status);
    try std.testing.expectEqualStrings("Shell Execute", state.tools.items[0].label);
    try std.testing.expect(std.mem.indexOf(u8, state.tools.items[0].output.items, "ok") != null);
    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[1].text.items, "◈ Shell Execute \"Check the current workspace directory\" ok") != null);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[1].text.items, "shell_command") == null);
    try std.testing.expect(state.active_tool_summary_entry == null);
}

test "AppState strips control bytes from tool summaries" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-1", "shell_command", "{\"command\":\"before\\u001b[2Jafter\\u0007\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);

    try std.testing.expect(std.mem.indexOfScalar(u8, state.transcript.items[0].text.items, 0x1b) == null);
    try std.testing.expect(std.mem.indexOfScalar(u8, state.transcript.items[0].text.items, 0x07) == null);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "before[2Jafter") != null);
}

test "AppState finalizes transcript from message_end text" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var assistant_end = session_runtime.SessionEvent{ .message_end = .{ .role = .assistant, .text = try ownedText("final response") } };
    defer assistant_end.deinit(std.testing.allocator);
    try state.applyEvent(assistant_end);

    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.assistant, state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("final response", state.transcript.items[0].text.items);
}

test "AppState message_end does not duplicate streamed transcript" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var delta_a = session_runtime.SessionEvent{ .text_delta = .{ .content_index = 0, .delta = try ownedText("hel") } };
    defer delta_a.deinit(std.testing.allocator);
    try state.applyEvent(delta_a);

    var delta_b = session_runtime.SessionEvent{ .text_delta = .{ .content_index = 0, .delta = try ownedText("lo") } };
    defer delta_b.deinit(std.testing.allocator);
    try state.applyEvent(delta_b);

    var assistant_end = session_runtime.SessionEvent{ .message_end = .{ .role = .assistant, .text = try ownedText("hello") } };
    defer assistant_end.deinit(std.testing.allocator);
    try state.applyEvent(assistant_end);

    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqualStrings("hello", state.transcript.items[0].text.items);
}

test "AppState message_end user text avoids duplicate submitted message" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.appendUserMessage("hello");
    var user_end = session_runtime.SessionEvent{ .message_end = .{ .role = .user, .text = try ownedText("hello") } };
    defer user_end.deinit(std.testing.allocator);
    try state.applyEvent(user_end);

    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.user, state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("hello", state.transcript.items[0].text.items);
}

test "AppState user message_start and message_end do not leave empty transcript row" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .message_start = .{ .role = .user } });
    var user_end = session_runtime.SessionEvent{ .message_end = .{ .role = .user, .text = try ownedText("queued prompt") } };
    defer user_end.deinit(std.testing.allocator);
    try state.applyEvent(user_end);

    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.user, state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("queued prompt", state.transcript.items[0].text.items);
}

test "AppState appendSteeredMessage echoes and tracks pending steer" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.appendSteeredMessage("steer mid turn");
    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.user, state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("steer mid turn", state.transcript.items[0].text.items);
    try std.testing.expectEqual(@as(usize, 1), state.pending_steers.items.len);
    try std.testing.expectEqual(@as(usize, 0), state.active_user_entry.?);

    try state.appendSteeredMessage("steer again");
    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expectEqual(@as(usize, 0), state.active_user_entry.?);

    state.reconcileSteers(1);
    try std.testing.expectEqual(@as(usize, 1), state.pending_steers.items.len);
    try std.testing.expectEqualStrings("steer again", state.pending_steers.items[0]);
    state.reconcileSteers(2);
    try std.testing.expectEqual(@as(usize, 0), state.pending_steers.items.len);
}

test "AppState reconcileSteers pops queued heads without text matching" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.appendSteeredMessage("first steer");
    try state.appendSteeredMessage("second steer");
    state.reconcileSteers(1);
    try std.testing.expectEqual(@as(usize, 1), state.pending_steers.items.len);
    try std.testing.expectEqualStrings("second steer", state.pending_steers.items[0]);
    state.reconcileSteers(1);
    try std.testing.expectEqual(@as(usize, 1), state.pending_steers.items.len);
    state.reconcileSteers(2);
    try std.testing.expectEqual(@as(usize, 0), state.pending_steers.items.len);

    try state.appendSteeredMessage("same text steer");
    state.reconcileSteers(3);
    try std.testing.expectEqual(@as(usize, 0), state.pending_steers.items.len);
}

test "AppState tracks queued follow-ups until the agent consumes them" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.appendQueuedFollowUp("first follow-up");
    try state.appendQueuedFollowUp("second follow-up");
    state.setQueuedCounts(.{ .follow_up = 2 });
    try std.testing.expectEqual(@as(usize, 2), state.pending_follow_ups.items.len);
    try std.testing.expectEqual(@as(usize, 0), state.transcript.items.len);

    state.setQueuedCounts(.{ .steering = 3, .follow_up = 1 });
    try std.testing.expectEqual(@as(usize, 1), state.pending_follow_ups.items.len);
    try std.testing.expectEqualStrings("second follow-up", state.pending_follow_ups.items[0]);

    state.setQueuedCounts(.{});
    try std.testing.expectEqual(@as(usize, 0), state.pending_follow_ups.items.len);
}

test "AppState picker filter drops control bytes, pops whole codepoints and resets the selection" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();
    state.menu_index = 4;
    state.menu_scroll = 2;

    try state.appendPickerFilter("gpt\n-é");
    try std.testing.expectEqualStrings("gpt-é", state.pickerFilter());
    try std.testing.expectEqual(@as(usize, 0), state.menu_index);
    try std.testing.expectEqual(@as(usize, 0), state.menu_scroll);

    state.menu_index = 3;
    try std.testing.expect(state.popPickerFilter());
    try std.testing.expectEqualStrings("gpt-", state.pickerFilter());
    try std.testing.expectEqual(@as(usize, 0), state.menu_index);
    state.clearPickerFilter();
    try std.testing.expect(!state.popPickerFilter());
}

test "tool invocation keeps a long description whole for the row to fit" {
    const description = "Check the Exa env var name in ~/.zshrc without exposing the value itself";
    const summary = try toolInvocation(std.testing.allocator, "Shell Execute", "{\"description\":\"" ++ description ++ "\",\"command\":\"ls\"}");
    defer std.testing.allocator.free(summary);
    try std.testing.expectEqualStrings("◈ Shell Execute \"" ++ description ++ "\"", summary);
}

test "AppState reconcileSteers fast-forwards when no pending steer remains" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.appendSteeredMessage("consumed steer");
    state.clearPendingSteers();
    state.reconcileSteers(4);
    try std.testing.expectEqual(@as(usize, 0), state.pending_steers.items.len);

    try state.appendSteeredMessage("later steer");
    state.reconcileSteers(4);
    try std.testing.expectEqual(@as(usize, 1), state.pending_steers.items.len);
    state.reconcileSteers(5);
    try std.testing.expectEqual(@as(usize, 0), state.pending_steers.items.len);
}

test "AppState clearTranscript drops pending steers" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.appendSteeredMessage("aborted steer");
    state.clearTranscript();
    try std.testing.expectEqual(@as(usize, 0), state.transcript.items.len);
    try std.testing.expectEqual(@as(usize, 0), state.pending_steers.items.len);
    try std.testing.expect(state.active_user_entry == null);

    try state.appendUserMessage("aborted steer");
    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqualStrings("aborted steer", state.transcript.items[0].text.items);
}

test "AppState message_end updates active assistant before trailing tool" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .message_start = .{ .role = .assistant } });
    var text_delta = session_runtime.SessionEvent{ .text_delta = .{ .content_index = 0, .delta = try ownedText("partial") } };
    defer text_delta.deinit(std.testing.allocator);
    try state.applyEvent(text_delta);

    var tool_start = try toolStartEvent("call-1", "shell", "{\"command\":\"ls\"}");
    defer tool_start.deinit(std.testing.allocator);
    try state.applyEvent(tool_start);

    var assistant_end = session_runtime.SessionEvent{ .message_end = .{ .role = .assistant, .text = try ownedText("final assistant") } };
    defer assistant_end.deinit(std.testing.allocator);
    try state.applyEvent(assistant_end);

    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.assistant, state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("final assistant", state.transcript.items[0].text.items);
    try std.testing.expectEqual(TranscriptKind.tool, state.transcript.items[1].kind);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[1].text.items, "shell") != null);
}

test "AppState message_end-only assistant appends after prior assistant" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.appendTranscript(.assistant, "previous response");
    var assistant_end = session_runtime.SessionEvent{ .message_end = .{ .role = .assistant, .text = try ownedText("next response") } };
    defer assistant_end.deinit(std.testing.allocator);
    try state.applyEvent(assistant_end);

    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expectEqualStrings("previous response", state.transcript.items[0].text.items);
    try std.testing.expectEqualStrings("next response", state.transcript.items[1].text.items);
}

test "AppState message_start opens fresh assistant row after prior assistant" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.appendTranscript(.assistant, "previous response");
    try state.applyEvent(.{ .message_start = .{ .role = .assistant } });
    var assistant_end = session_runtime.SessionEvent{ .message_end = .{ .role = .assistant, .text = try ownedText("next response") } };
    defer assistant_end.deinit(std.testing.allocator);
    try state.applyEvent(assistant_end);

    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expectEqualStrings("previous response", state.transcript.items[0].text.items);
    try std.testing.expectEqualStrings("next response", state.transcript.items[1].text.items);
}

test "AppState removes empty assistant placeholder on empty message_end" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.appendTranscript(.assistant, "previous response");
    try state.applyEvent(.{ .message_start = .{ .role = .assistant } });
    var assistant_end = session_runtime.SessionEvent{ .message_end = .{ .role = .assistant, .text = try ownedText("") } };
    defer assistant_end.deinit(std.testing.allocator);
    try state.applyEvent(assistant_end);

    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.assistant, state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("previous response", state.transcript.items[0].text.items);
}

test "AppState removes empty assistant placeholder on aborted turn" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.appendTranscript(.assistant, "previous response");
    try state.applyEvent(.{ .message_start = .{ .role = .assistant } });
    try state.applyEvent(session_runtime.SessionEvent{ .agent_end = .{ .reason = .cancelled } });

    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.assistant, state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("previous response", state.transcript.items[0].text.items);
    try std.testing.expectEqual(TranscriptKind.system, state.transcript.items[1].kind);
    try std.testing.expectEqualStrings("agent cancelled", state.transcript.items[1].text.items);
}

test "AppState finalizes active assistant after reasoning and tool deltas" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .message_start = .{ .role = .assistant } });
    var thinking_delta = session_runtime.SessionEvent{ .thinking_delta = .{ .content_index = 0, .delta = try ownedText("plan") } };
    defer thinking_delta.deinit(std.testing.allocator);
    try state.applyEvent(thinking_delta);
    var tool_delta = session_runtime.SessionEvent{ .tool_call_delta = .{ .content_index = 1, .delta = try ownedText("{\"name\":\"shell\"}") } };
    defer tool_delta.deinit(std.testing.allocator);
    try state.applyEvent(tool_delta);
    var text_delta = session_runtime.SessionEvent{ .text_delta = .{ .content_index = 2, .delta = try ownedText("partial") } };
    defer text_delta.deinit(std.testing.allocator);
    try state.applyEvent(text_delta);
    var assistant_end = session_runtime.SessionEvent{ .message_end = .{ .role = .assistant, .text = try ownedText("final") } };
    defer assistant_end.deinit(std.testing.allocator);
    try state.applyEvent(assistant_end);

    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.thinking, state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("plan", state.transcript.items[0].text.items);
    try std.testing.expectEqual(TranscriptKind.assistant, state.transcript.items[1].kind);
    try std.testing.expectEqualStrings("final", state.transcript.items[1].text.items);
    try std.testing.expect(state.active_thinking_entry == null);
    try std.testing.expect(state.active_assistant_entry == null);
}

test "AppState keeps identical inline assistant message_end turns" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var first_end = session_runtime.SessionEvent{ .message_end = .{ .role = .assistant, .text = try ownedText("Done") } };
    defer first_end.deinit(std.testing.allocator);
    try state.applyEvent(first_end);
    var second_end = session_runtime.SessionEvent{ .message_end = .{ .role = .assistant, .text = try ownedText("Done") } };
    defer second_end.deinit(std.testing.allocator);
    try state.applyEvent(second_end);

    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expectEqualStrings("Done", state.transcript.items[0].text.items);
    try std.testing.expectEqualStrings("Done", state.transcript.items[1].text.items);
}

test "AppState clears stale active assistant before next inline message_end" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .message_start = .{ .role = .assistant } });
    var partial = session_runtime.SessionEvent{ .text_delta = .{ .content_index = 0, .delta = try ownedText("interrupted") } };
    defer partial.deinit(std.testing.allocator);
    try state.applyEvent(partial);
    try state.applyEvent(.{ .agent_end = .{ .reason = .@"error" } });

    var assistant_end = session_runtime.SessionEvent{ .message_end = .{ .role = .assistant, .text = try ownedText("next response") } };
    defer assistant_end.deinit(std.testing.allocator);
    try state.applyEvent(assistant_end);

    try std.testing.expectEqual(@as(usize, 3), state.transcript.items.len);
    try std.testing.expectEqualStrings("interrupted", state.transcript.items[0].text.items);
    try std.testing.expectEqual(TranscriptKind.@"error", state.transcript.items[1].kind);
    try std.testing.expectEqualStrings("agent ended with error, but no error details were provided", state.transcript.items[1].text.items);
    try std.testing.expectEqualStrings("next response", state.transcript.items[2].text.items);
}

test "AppState does not append generic agent error after detailed error event" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var error_event = session_runtime.SessionEvent{ .@"error" = .{ .message = try ownedText("provider failed: bad request") } };
    defer error_event.deinit(std.testing.allocator);
    try state.applyEvent(error_event);
    try state.applyEvent(.{ .agent_end = .{ .reason = .@"error" } });

    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.@"error", state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("provider failed: bad request", state.transcript.items[0].text.items);
}

test "AppState clones registered tool metadata" {
    const tools = [_]agent.AgentTool{
        .{
            .label = "Shell Execute",
            .name = "shell_execute",
            .description = "Run command",
            .short_description = "Run shell commands",
            .parameters_schema_json = "{}",
            .execute = noopToolForTest,
        },
        .{
            .label = "Workspace Info",
            .name = "workspace_info",
            .description = "Show workspace",
            .parameters_schema_json = "{}",
            .execute = noopToolForTest,
        },
    };
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.setRegisteredTools(&tools);
    try std.testing.expectEqual(@as(usize, 2), state.registered_tools.items.len);
    try std.testing.expectEqualStrings("shell_execute", state.registered_tools.items[0].name);
    try std.testing.expectEqualStrings("Shell Execute", state.registered_tools.items[0].label);
    try std.testing.expectEqualStrings("Run shell commands", state.registered_tools.items[0].short_description);
    try std.testing.expectEqualStrings("", state.registered_tools.items[1].short_description);

    const replacement = [_]agent.AgentTool{.{
        .label = "File Read",
        .name = "file_read",
        .description = "Read file",
        .short_description = "Read files",
        .parameters_schema_json = "{}",
        .execute = noopToolForTest,
    }};
    try state.setRegisteredTools(&replacement);
    try std.testing.expectEqual(@as(usize, 1), state.registered_tools.items.len);
    try std.testing.expectEqualStrings("file_read", state.registered_tools.items[0].name);
}

test "AppState tool_result message_end updates active tool entry only" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.appendTranscript(.tool, "shell_execute");
    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var tool_result_a = session_runtime.SessionEvent{ .message_end = .{ .role = .tool_result, .text = try ownedText("first result") } };
    defer tool_result_a.deinit(std.testing.allocator);
    try state.applyEvent(tool_result_a);

    try state.appendTranscript(.tool, "file_read");
    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var tool_result_b = session_runtime.SessionEvent{ .message_end = .{ .role = .tool_result, .text = try ownedText("second result") } };
    defer tool_result_b.deinit(std.testing.allocator);
    try state.applyEvent(tool_result_b);

    try std.testing.expectEqual(@as(usize, 4), state.transcript.items.len);
    try std.testing.expectEqualStrings("shell_execute", state.transcript.items[0].text.items);
    try std.testing.expectEqualStrings("first result", state.transcript.items[1].text.items);
    try std.testing.expectEqualStrings("file_read", state.transcript.items[2].text.items);
    try std.testing.expectEqualStrings("second result", state.transcript.items[3].text.items);
}

test "AppState approval flow transitions pending to approved and rejected" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var approval_event = session_runtime.SessionEvent{ .tool_approval_requested = .{
        .tool_call_id = try ownedText("call-2"),
        .tool_name = try ownedText("edit_file"),
        .args_json = try ownedText("{\"path\":\"README.md\"}"),
    } };
    defer approval_event.deinit(std.testing.allocator);
    try state.applyEvent(approval_event);

    var hashline_event = session_runtime.SessionEvent{ .tool_approval_requested = .{
        .tool_call_id = try ownedText("call-hash"),
        .tool_name = try ownedText("Edit"),
        .args_json = try ownedText("{\"path\":\"src/main.zig\",\"operation\":\"hash_range_replace\",\"start_line\":2,\"start_hash\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"content\":\"new line\"}"),
    } };
    defer hashline_event.deinit(std.testing.allocator);
    try state.applyEvent(hashline_event);
    try std.testing.expect(std.mem.indexOf(u8, state.preview.content, "edit preview") != null);
    try std.testing.expect(std.mem.indexOf(u8, state.preview.content, "+ 2|new line") != null);

    var insert_after_event = session_runtime.SessionEvent{ .tool_approval_requested = .{
        .tool_call_id = try ownedText("call-hash-insert-after"),
        .tool_name = try ownedText("Edit"),
        .args_json = try ownedText("{\"path\":\"src/main.zig\",\"operation\":\"insert\",\"start_line\":11,\"start_hash\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"content\":\"inserted\"}"),
    } };
    defer insert_after_event.deinit(std.testing.allocator);
    try state.applyEvent(insert_after_event);
    try std.testing.expect(std.mem.indexOf(u8, state.preview.content, "+ 11|inserted") != null);
    try std.testing.expect(std.mem.indexOf(u8, state.preview.content, "+ 10|inserted") == null);

    var blank_line_event = session_runtime.SessionEvent{ .tool_approval_requested = .{
        .tool_call_id = try ownedText("call-hash-blank"),
        .tool_name = try ownedText("Edit"),
        .args_json = try ownedText("{\"path\":\"src/main.zig\",\"operation\":\"hash_range_replace\",\"start_line\":2,\"start_hash\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"content\":\"line1\\n\\nline3\"}"),
    } };
    defer blank_line_event.deinit(std.testing.allocator);
    try state.applyEvent(blank_line_event);
    try std.testing.expect(std.mem.indexOf(u8, state.preview.content, "+ 3|") != null);
    try std.testing.expect(std.mem.indexOf(u8, state.preview.content, "+ 4|line3") != null);

    var large_replacement = try std.ArrayList(u8).initCapacity(std.testing.allocator, max_hashline_preview_bytes + 4096);
    defer large_replacement.deinit(std.testing.allocator);
    while (large_replacement.items.len < max_hashline_preview_bytes + 4096) {
        try large_replacement.appendSlice(std.testing.allocator, "large replacement line\n");
    }
    const large_args = try std.fmt.allocPrint(std.testing.allocator, "{{\"path\":\"src/main.zig\",\"operation\":\"hash_range_replace\",\"start_line\":2,\"start_hash\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"content\":{f}}}", .{std.json.fmt(large_replacement.items, .{})});
    defer std.testing.allocator.free(large_args);
    var large_event = session_runtime.SessionEvent{ .tool_approval_requested = .{
        .tool_call_id = try ownedText("call-hash-large"),
        .tool_name = try ownedText("Edit"),
        .args_json = try ownedText(large_args),
    } };
    defer large_event.deinit(std.testing.allocator);
    try state.applyEvent(large_event);
    try std.testing.expect(state.preview.content.len <= max_hashline_preview_bytes);
    try std.testing.expect(std.mem.indexOf(u8, state.preview.content, "preview truncated") != null);

    try std.testing.expectEqual(AppMode.approval, state.mode);
    try std.testing.expectEqual(ApprovalStatus.pending, state.approval.status);
    try std.testing.expectEqualStrings("Edit", state.approval.tool_name);
    try std.testing.expectEqualStrings("call-hash-large", state.approval.tool_call_id);

    state.setApprovalDecision(true, true);
    try std.testing.expectEqual(AppMode.normal, state.mode);
    try std.testing.expectEqual(ApprovalStatus.approved, state.approval.status);
    try std.testing.expect(state.approval.always);

    state.setApprovalDecision(false, false);
    try std.testing.expectEqual(ApprovalStatus.rejected, state.approval.status);
    try std.testing.expect(!state.approval.always);
}

test "Composer submission stores history and user transcript" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.composer.buffer.appendSlice(std.testing.allocator, " hello makai ");
    const submitted = (try state.submitComposer()).?;
    defer std.testing.allocator.free(submitted);

    try std.testing.expectEqualStrings("hello makai", submitted);
    try std.testing.expectEqual(@as(usize, 1), state.composer.history.items.len);
    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.user, state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("hello makai", state.transcript.items[0].text.items);
}

test "Composer history navigation recalls entries and restores draft" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.composer.history.append(std.testing.allocator, try std.testing.allocator.dupe(u8, "first"));
    try state.composer.history.append(std.testing.allocator, try std.testing.allocator.dupe(u8, "second"));
    try state.composer.buffer.appendSlice(std.testing.allocator, "draft");

    try std.testing.expect(try state.composerHistoryPrev());
    try std.testing.expectEqualStrings("second", state.composer.text());
    try std.testing.expect(try state.composerHistoryPrev());
    try std.testing.expectEqualStrings("first", state.composer.text());
    try std.testing.expect(!try state.composerHistoryPrev());
    try std.testing.expectEqualStrings("first", state.composer.text());
    try std.testing.expect(try state.composerHistoryNext());
    try std.testing.expectEqualStrings("second", state.composer.text());
    try std.testing.expect(try state.composerHistoryNext());
    try std.testing.expectEqualStrings("draft", state.composer.text());
}

test "Composer cursor edits within the draft" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.composer.insertSlice(std.testing.allocator, "abc");
    try std.testing.expectEqual(@as(usize, 3), state.composer.cursor);
    try std.testing.expect(state.composer.moveCursorPrev());
    try state.composer.insertSlice(std.testing.allocator, "X");
    try std.testing.expectEqualStrings("abXc", state.composer.text());
    try std.testing.expect(state.composer.deleteBeforeCursor());
    try std.testing.expectEqualStrings("abc", state.composer.text());
    state.composer.moveCursorHome();
    try std.testing.expect(!state.composer.deleteBeforeCursor());
    try state.composer.insertSlice(std.testing.allocator, "λ");
    try std.testing.expectEqualStrings("λabc", state.composer.text());
    try std.testing.expectEqual(@as(usize, "λ".len), state.composer.cursor);
}

test "Composer Home and End jump within the current line" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.composer.insertSlice(std.testing.allocator, "first\nsecond\nthird");
    state.composer.cursor = 9;
    state.composer.moveCursorHome();
    try std.testing.expectEqual(@as(usize, 6), state.composer.cursor);
    state.composer.moveCursorEnd();
    try std.testing.expectEqual(@as(usize, 12), state.composer.cursor);
    state.composer.moveCursorEnd();
    try std.testing.expectEqual(@as(usize, 12), state.composer.cursor);
    state.composer.cursor = 2;
    state.composer.moveCursorEnd();
    try std.testing.expectEqual(@as(usize, 5), state.composer.cursor);
    state.composer.cursor = 20;
    state.composer.moveCursorHome();
    try std.testing.expectEqual(@as(usize, 13), state.composer.cursor);
}

test "Composer paste normalises CRLF into LF" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.composer.insertPaste(std.testing.allocator, "one\r\ntwo\r\nthree");
    try std.testing.expectEqualStrings("one\ntwo\nthree", state.composer.text());
    try std.testing.expectEqual(@as(usize, 13), state.composer.cursor);
    try state.composer.insertPaste(std.testing.allocator, "\r\nfour");
    try std.testing.expectEqualStrings("one\ntwo\nthree\nfour", state.composer.text());
}

test "Composer paste drops a carriage return split across two events" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.composer.insertPaste(std.testing.allocator, "one\r");
    try state.composer.insertPaste(std.testing.allocator, "\ntwo");
    try std.testing.expectEqualStrings("one\ntwo", state.composer.text());
    try std.testing.expectEqual(@as(usize, 7), state.composer.cursor);
}

test "Composer clear resets the scroll row and goal column" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.composer.insertSlice(std.testing.allocator, "draft");
    state.composer.scroll_row = 4;
    state.composer.goal_column = 9;
    state.composer.clear();
    try std.testing.expectEqual(@as(usize, 0), state.composer.scroll_row);
    try std.testing.expectEqual(@as(?usize, null), state.composer.goal_column);
}

test "AppState cycles thinking levels for TUI shortcut" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();
    try std.testing.expectEqual(ai_types.ThinkingLevel.low, state.thinking_level);
    try std.testing.expectEqual(ai_types.ThinkingLevel.medium, state.cycleThinkingLevel());
    try std.testing.expectEqual(ai_types.ThinkingLevel.high, state.cycleThinkingLevel());
    try std.testing.expectEqual(ai_types.ThinkingLevel.xhigh, state.cycleThinkingLevel());
    try std.testing.expectEqual(ai_types.ThinkingLevel.max, state.cycleThinkingLevel());
    try std.testing.expectEqual(ai_types.ThinkingLevel.off, state.cycleThinkingLevel());
    try std.testing.expectEqual(ai_types.ThinkingLevel.low, state.cycleThinkingLevel());
}

test "AppState reset replay clears stale queue counts" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();
    state.queue = .{ .steering = 1, .follow_up = 2 };

    state.resetReplayState();

    try std.testing.expectEqual(@as(usize, 0), state.queue.total());
}

test "AppState reset replay clears backpressure state" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();
    state.dropped_event_count = 5;
    state.backpressure_active = true;

    state.resetReplayState();

    try std.testing.expectEqual(@as(u64, 0), state.dropped_event_count);
    try std.testing.expect(!state.backpressure_active);
}

test "AppState stream_aborted still applies backpressure status and warning" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();
    state.stream_aborted = true;

    try state.applyEvent(.{ .backpressure_status = .{ .active = true, .dropped_count = 4 } });
    try std.testing.expect(state.backpressure_active);
    try std.testing.expectEqual(@as(u64, 4), state.dropped_event_count);

    var warning = session_runtime.SessionEvent{ .system_warning = .{ .message = try ownedText("warn") } };
    defer warning.deinit(std.testing.allocator);
    try state.applyEvent(warning);

    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqualStrings("warn", state.transcript.items[0].text.items);
}

test "AppState applies thinking tool call and lifecycle events" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .agent_start = .{} });
    try std.testing.expect(state.status.streaming);
    try std.testing.expectEqual(@as(usize, 0), state.transcript.items.len);

    var thinking_event = session_runtime.SessionEvent{ .thinking_delta = .{ .content_index = 0, .delta = try ownedText("plan") } };
    defer thinking_event.deinit(std.testing.allocator);
    try state.applyEvent(thinking_event);

    var call_event = session_runtime.SessionEvent{ .tool_call_delta = .{ .content_index = 1, .delta = try ownedText("{\"name\":\"shell\"}") } };
    defer call_event.deinit(std.testing.allocator);
    try state.applyEvent(call_event);

    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.thinking, state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("plan", state.transcript.items[0].text.items);

    try state.applyEvent(session_runtime.SessionEvent{ .agent_end = .{ .reason = .cancelled } });
    try std.testing.expect(!state.status.streaming);
    try std.testing.expectEqualStrings("agent cancelled", state.transcript.items[state.transcript.items.len - 1].text.items);
}

test "AppState appends tool execution updates" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var update_event = session_runtime.SessionEvent{ .tool_execution_update = .{
        .tool_call_id = try ownedText("call-3"),
        .tool_name = try ownedText("search"),
        .args_json = try ownedText("{\"query\":\"tui\"}"),
        .partial_result_json = try ownedText("{\"match\":1}"),
    } };
    defer update_event.deinit(std.testing.allocator);
    try state.applyEvent(update_event);

    try std.testing.expectEqual(@as(usize, 1), state.tools.items.len);
    try std.testing.expectEqual(ToolStatus.running, state.tools.items[0].status);
    try std.testing.expectEqualStrings("{\"match\":1}", state.tools.items[0].output.items);
}

test "AppState token counters update from context usage events" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();
    state.status.context_limit = 2000;

    try state.applyEvent(.{ .context_usage = .{
        .system_prompt_bytes = 100,
        .message_bytes = 300,
        .tool_definition_bytes = 200,
        .total_bytes = 600,
        .estimated_tokens = 150,
        .message_count = 4,
        .tool_count = 2,
    } });

    try std.testing.expectEqual(@as(usize, 150), state.status.context_used);
    try std.testing.expectEqual(@as(u64, 150), state.telemetry.estimated_tokens);
    try std.testing.expectEqual(@as(u64, 2000), state.telemetry.context_window);
}

test "AppState detects truncated tool execution end events" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var end_event = session_runtime.SessionEvent{ .tool_execution_end = .{
        .tool_call_id = try ownedText("call-trunc"),
        .tool_name = try ownedText("shell_command"),
        .result_json = try ownedText("{\"summary\":true}"),
        .is_error = false,
        .raw_total_bytes = 4096,
        .returned_total_bytes = 512,
        .estimated_returned_tokens = 128,
        .artifact_count = 1,
        .artifact_refs = try ownedText("artifact://tool-output/1"),
    } };
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try std.testing.expectEqual(@as(usize, 1), state.tools.items.len);
    try std.testing.expect(state.tools.items[0].truncated);
    try std.testing.expectEqual(@as(u64, 4096), state.tools.items[0].raw_total_bytes);
    try std.testing.expectEqualStrings("artifact://tool-output/1", state.tools.items[0].artifact_refs);
    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "artifacts=1 on disk") != null);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "◈ shell_command ok") != null);
}

test "AppState appends visible transcript row for tool execution errors" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var end_event = session_runtime.SessionEvent{ .tool_execution_end = .{
        .tool_call_id = try ownedText("call-error"),
        .tool_name = try ownedText("shell_command"),
        .result_json = try ownedText("OutOfMemory"),
        .is_error = true,
    } };
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try std.testing.expectEqual(@as(usize, 1), state.tools.items.len);
    try std.testing.expectEqual(ToolStatus.@"error", state.tools.items[0].status);
    try std.testing.expectEqual(TranscriptKind.@"error", state.transcript.items[state.transcript.items.len - 1].kind);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[state.transcript.items.len - 1].text.items, "shell_command failed: OutOfMemory") != null);
}

test "AppState unwraps tool error envelope for display" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-err", "workspace_list", "{\"workspace_root\":\"/tmp\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);

    var end_event = try toolEndEvent("call-err", "workspace_list", "{\"ok\":false,\"err\":\"FileNotFound\",\"duration_ms\":2}", true);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "◈ workspace_list failed") != null);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "\"FileNotFound\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "{\"ok\":false") == null);
    try std.testing.expectEqual(TranscriptKind.@"error", state.transcript.items[1].kind);
    try std.testing.expectEqualStrings("workspace_list failed: FileNotFound", state.transcript.items[1].text.items);
}

test "AppState renders one summary line per tool call across a turn" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.appendUserMessage("run two tools");

    const calls = [_]struct { id: []const u8, name: []const u8 }{
        .{ .id = "call-a", .name = "workspace_info" },
        .{ .id = "call-b", .name = "workspace_list" },
    };
    for (calls) |call| {
        var start_event = try toolStartEvent(call.id, call.name, "{\"workspace_root\":\"/tmp\"}");
        defer start_event.deinit(std.testing.allocator);
        try state.applyEvent(start_event);
        var end_event = try toolEndEvent(call.id, call.name, "{\"ok\":true}", false);
        defer end_event.deinit(std.testing.allocator);
        try state.applyEvent(end_event);
    }

    var tool_rows: usize = 0;
    for (state.transcript.items) |entry| {
        if (entry.kind == .tool) tool_rows += 1;
    }
    try std.testing.expectEqual(@as(usize, 2), tool_rows);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[1].text.items, "◈ workspace_info ok") != null);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[2].text.items, "◈ workspace_list ok") != null);
}

test "AppState sanitizes unwrapped tool error messages" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var end_event = try toolEndEvent("call-esc", "shell_command", "{\"ok\":false,\"err\":\"before\\u001b[2Jafter\\u0007\"}", true);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    for (state.transcript.items) |entry| {
        try std.testing.expect(std.mem.indexOfScalar(u8, entry.text.items, 0x1b) == null);
        try std.testing.expect(std.mem.indexOfScalar(u8, entry.text.items, 0x07) == null);
    }
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "before[2Jafter") != null);
    try std.testing.expectEqual(TranscriptKind.@"error", state.transcript.items[1].kind);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[1].text.items, "shell_command failed: before[2Jafter") != null);
}

test "AppState drops redundant result text for failed tool calls" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-f", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    var end_event = try toolEndEvent("call-f", "shell", "{\"ok\":false,\"err\":\"FileNotFound\"}", true);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_end = session_runtime.SessionEvent{ .message_end = .{ .role = .tool_result, .tool_call_id = try ownedText("call-f"), .text = try ownedText("Tool execution failed: FileNotFound") } };
    defer result_end.deinit(std.testing.allocator);
    try state.applyEvent(result_end);

    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "◈ shell \"ls\" failed") != null);
    try std.testing.expectEqual(TranscriptKind.@"error", state.transcript.items[1].kind);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[1].text.items, "Tool execution failed") == null);
    try std.testing.expect(state.active_tool_result_entry == null);
}

test "AppState keeps readable text for rejected tool calls" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-r", "shell", "{\"command\":\"true\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    var end_event = try toolEndEvent("call-r", "shell", "{\"rejected\":true}", true);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_end = session_runtime.SessionEvent{ .message_end = .{ .role = .tool_result, .tool_call_id = try ownedText("call-r"), .text = try ownedText("Tool execution rejected by user") } };
    defer result_end.deinit(std.testing.allocator);
    try state.applyEvent(result_end);

    try std.testing.expectEqual(@as(usize, 3), state.transcript.items.len);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "failed") != null);
    try std.testing.expectEqual(TranscriptKind.@"error", state.transcript.items[1].kind);
    try std.testing.expectEqual(TranscriptKind.tool, state.transcript.items[2].kind);
    try std.testing.expectEqualStrings("Tool execution rejected by user", state.transcript.items[2].text.items);
    try std.testing.expectEqualStrings("call-r", state.transcript.items[2].tool_call_id);
    try std.testing.expect(!state.tools.items[0].error_detail_readable);
}

test "AppState drops duplicate text for plain-error tool results" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-s", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    var end_event = try toolEndEvent("call-s", "shell", "Skipped due to queued user message.", true);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_end = session_runtime.SessionEvent{ .message_end = .{ .role = .tool_result, .tool_call_id = try ownedText("call-s"), .text = try ownedText("Skipped due to queued user message.") } };
    defer result_end.deinit(std.testing.allocator);
    try state.applyEvent(result_end);

    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "◈ shell \"ls\" failed") != null);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "Skipped due to queued user message.") != null);
    try std.testing.expectEqual(TranscriptKind.@"error", state.transcript.items[1].kind);
    try std.testing.expect(state.tools.items[0].error_detail_readable);
}

test "AppState keeps the prior summary when an end arrives for another call" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-a", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    var end_event = try toolEndEvent("call-b", "shell", "{\"ok\":true}", false);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "◈ shell \"ls\"") != null);
    try std.testing.expectEqualStrings("call-a", state.transcript.items[0].tool_call_id);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[1].text.items, "◈ shell ok") != null);
    try std.testing.expectEqualStrings("call-b", state.transcript.items[1].tool_call_id);
    try std.testing.expectEqual(@as(usize, 0), state.active_tool_summary_entry.?);
}

test "AppState finalizes running tools as interrupted on aborted turn end" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-a", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);

    state.stream_aborted = true;
    try state.applyEvent(.{ .turn_end = .{ .stop_reason = .stop } });

    try std.testing.expectEqual(ToolStatus.interrupted, state.tools.items[0].status);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "interrupted") != null);
    try std.testing.expect(state.active_tool_summary_entry == null);
}

test "AppState reconciles result rows when an end arrives after the result" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_end = session_runtime.SessionEvent{ .message_end = .{ .role = .tool_result, .tool_call_id = try ownedText("call-r2"), .text = try ownedText("Tool execution failed: FileNotFound") } };
    defer result_end.deinit(std.testing.allocator);
    try state.applyEvent(result_end);

    var end_event = try toolEndEvent("call-r2", "shell", "{\"ok\":false,\"err\":\"FileNotFound\"}", true);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "◈ shell failed") != null);
    try std.testing.expectEqual(TranscriptKind.@"error", state.transcript.items[1].kind);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[1].text.items, "Tool execution failed") == null);
}

test "AppState keeps result text when error details are absent" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-n", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    var end_event = try toolEndEvent("call-n", "shell", "null", true);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_end = session_runtime.SessionEvent{ .message_end = .{ .role = .tool_result, .tool_call_id = try ownedText("call-n"), .text = try ownedText("connection reset by peer") } };
    defer result_end.deinit(std.testing.allocator);
    try state.applyEvent(result_end);

    try std.testing.expectEqual(@as(usize, 3), state.transcript.items.len);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "failed") != null);
    try std.testing.expectEqual(TranscriptKind.@"error", state.transcript.items[1].kind);
    try std.testing.expectEqual(TranscriptKind.tool, state.transcript.items[2].kind);
    try std.testing.expectEqualStrings("connection reset by peer", state.transcript.items[2].text.items);
    try std.testing.expect(!state.tools.items[0].error_detail_readable);
}

test "AppState recovers replayed tool arguments from assistant tool calls" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .message_start = .{ .role = .assistant } });
    var assistant_end = session_runtime.SessionEvent{ .message_end = .{ .role = .assistant, .text = try ownedText(""), .tool_calls_json = try ownedText("[{\"type\":\"tool_call\",\"id\":\"call-old\",\"name\":\"shell\",\"arguments_json\":\"{\\\"command\\\":\\\"pwd\\\"}\"}]") } };
    defer assistant_end.deinit(std.testing.allocator);
    try state.applyEvent(assistant_end);

    var end_event = try toolEndEvent("call-old", "shell", "{\"ok\":true}", false);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqualStrings("{\"command\":\"pwd\"}", state.tools.items[0].args_json);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "◈ shell \"pwd\" ok") != null);
    try std.testing.expectEqualStrings("call-old", state.transcript.items[0].tool_call_id);
}

test "AppState finalizes unmatched replayed tool starts as interrupted" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-i", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    try std.testing.expect(state.active_tool_summary_entry != null);

    try state.finalizeInterruptedTools();

    try std.testing.expect(state.active_tool_summary_entry == null);
    try std.testing.expectEqual(ToolStatus.interrupted, state.tools.items[0].status);
    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqualStrings("◈ shell \"ls\" interrupted", state.transcript.items[0].text.items);
}

test "AppState finalizes running tools as interrupted on cancelled agent end" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-x", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);

    try state.applyEvent(session_runtime.SessionEvent{ .agent_end = .{ .reason = .cancelled } });

    try std.testing.expectEqual(ToolStatus.interrupted, state.tools.items[0].status);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "interrupted") != null);
    try std.testing.expectEqual(TranscriptKind.system, state.transcript.items[1].kind);
}

test "AppState scopes reused tool call ids per occurrence" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var first_start = try toolStartEvent("call-x", "shell", "{\"command\":\"ls\"}");
    defer first_start.deinit(std.testing.allocator);
    try state.applyEvent(first_start);
    var first_end = try toolEndEvent("call-x", "shell", "{\"ok\":true}", false);
    defer first_end.deinit(std.testing.allocator);
    try state.applyEvent(first_end);

    var second_start = try toolStartEvent("call-x", "shell", "{\"command\":\"pwd\"}");
    defer second_start.deinit(std.testing.allocator);
    try state.applyEvent(second_start);
    var second_end = try toolEndEvent("call-x", "shell", "{\"ok\":true}", false);
    defer second_end.deinit(std.testing.allocator);
    try state.applyEvent(second_end);

    try std.testing.expectEqual(@as(usize, 2), state.tools.items.len);
    try std.testing.expectEqualStrings("call-x", state.tools.items[0].id);
    try std.testing.expectEqualStrings("call-x\x1f2", state.tools.items[1].id);
    try std.testing.expectEqual(@as(usize, 1), state.tools.items[0].occurrence);
    try std.testing.expectEqual(@as(usize, 2), state.tools.items[1].occurrence);
    try std.testing.expectEqual(ToolStatus.done, state.tools.items[0].status);
    try std.testing.expectEqual(ToolStatus.done, state.tools.items[1].status);
    try std.testing.expectEqualStrings("{\"command\":\"ls\"}", state.tools.items[0].args_json);
    try std.testing.expectEqualStrings("{\"command\":\"pwd\"}", state.tools.items[1].args_json);
    try std.testing.expectEqual(@as(usize, 2), state.transcript.items.len);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "\"ls\" ok") != null);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[1].text.items, "\"pwd\" ok") != null);
    try std.testing.expectEqualStrings("call-x", state.transcript.items[0].tool_call_id);
    try std.testing.expectEqualStrings("call-x\x1f2", state.transcript.items[1].tool_call_id);

    var third_start = try toolStartEvent("call-x", "shell", "{\"command\":\"id\"}");
    defer third_start.deinit(std.testing.allocator);
    try state.applyEvent(third_start);
    try std.testing.expectEqual(@as(usize, 3), state.tools.items.len);
    try std.testing.expectEqualStrings("call-x\x1f3", state.tools.items[2].id);
    try std.testing.expectEqual(ToolStatus.running, state.tools.items[2].status);
}

test "AppState allocates the next occurrence when only ends are replayed" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .message_start = .{ .role = .assistant } });
    var assistant_end = session_runtime.SessionEvent{ .message_end = .{ .role = .assistant, .text = try ownedText(""), .tool_calls_json = try ownedText("[{\"type\":\"tool_call\",\"id\":\"call-x\",\"name\":\"shell\",\"arguments_json\":\"{\\\"command\\\":\\\"ls\\\"}\"}]") } };
    defer assistant_end.deinit(std.testing.allocator);
    try state.applyEvent(assistant_end);

    var first_end = try toolEndEvent("call-x", "shell", "{\"ok\":true}", false);
    defer first_end.deinit(std.testing.allocator);
    try state.applyEvent(first_end);
    var second_end = try toolEndEvent("call-x", "shell", "{\"ok\":false,\"err\":\"Boom\"}", true);
    defer second_end.deinit(std.testing.allocator);
    try state.applyEvent(second_end);

    try std.testing.expectEqual(@as(usize, 2), state.tools.items.len);
    try std.testing.expectEqualStrings("call-x", state.tools.items[0].id);
    try std.testing.expectEqualStrings("call-x\x1f2", state.tools.items[1].id);
    try std.testing.expectEqualStrings("{\"command\":\"ls\"}", state.tools.items[0].args_json);
    try std.testing.expectEqualStrings("{\"command\":\"ls\"}", state.tools.items[1].args_json);
    try std.testing.expectEqual(ToolStatus.done, state.tools.items[0].status);
    try std.testing.expectEqual(ToolStatus.@"error", state.tools.items[1].status);
    try std.testing.expectEqual(@as(usize, 3), state.transcript.items.len);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "ok") != null);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "Boom") == null);
    try std.testing.expectEqualStrings("call-x", state.transcript.items[0].tool_call_id);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[1].text.items, "failed") != null);
    try std.testing.expectEqualStrings("call-x\x1f2", state.transcript.items[1].tool_call_id);
    try std.testing.expectEqual(TranscriptKind.@"error", state.transcript.items[2].kind);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[2].text.items, "Boom") != null);
}

test "AppState links result rows to the resolved occurrence" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var first_start = try toolStartEvent("call-x", "shell", "{\"command\":\"ls\"}");
    defer first_start.deinit(std.testing.allocator);
    try state.applyEvent(first_start);
    var first_end = try toolEndEvent("call-x", "shell", "{\"ok\":true}", false);
    defer first_end.deinit(std.testing.allocator);
    try state.applyEvent(first_end);
    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var first_result = session_runtime.SessionEvent{ .message_end = .{ .role = .tool_result, .tool_call_id = try ownedText("call-x"), .text = try ownedText("all good") } };
    defer first_result.deinit(std.testing.allocator);
    try state.applyEvent(first_result);

    var second_start = try toolStartEvent("call-x", "shell", "{\"command\":\"pwd\"}");
    defer second_start.deinit(std.testing.allocator);
    try state.applyEvent(second_start);
    var second_end = try toolEndEvent("call-x", "shell", "{\"ok\":false,\"err\":\"Boom\"}", true);
    defer second_end.deinit(std.testing.allocator);
    try state.applyEvent(second_end);
    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var second_result = session_runtime.SessionEvent{ .message_end = .{ .role = .tool_result, .tool_call_id = try ownedText("call-x"), .text = try ownedText("Tool execution failed: Boom"), .details_json = try ownedText("{\"ok\":false,\"err\":\"Boom\"}"), .is_error = true } };
    defer second_result.deinit(std.testing.allocator);
    try state.applyEvent(second_result);

    try std.testing.expectEqualStrings("call-x", state.tools.items[0].id);
    try std.testing.expectEqualStrings("call-x\x1f2", state.tools.items[1].id);
    try std.testing.expectEqual(@as(usize, 4), state.transcript.items.len);
    try std.testing.expectEqualStrings("call-x", state.transcript.items[1].tool_call_id);
    try std.testing.expectEqualStrings("all good", state.transcript.items[1].text.items);
    try std.testing.expect(state.transcript.items[2].tool_summary);
    try std.testing.expectEqualStrings("call-x\x1f2", state.transcript.items[2].tool_call_id);
    try std.testing.expectEqual(TranscriptKind.@"error", state.transcript.items[3].kind);
    for (state.transcript.items) |*entry| {
        if (entry.kind != .tool or entry.tool_summary) continue;
        try std.testing.expectEqualStrings("call-x", entry.tool_call_id);
        try std.testing.expectEqualStrings("all good", entry.text.items);
    }
}

test "AppState resets occurrence numbering when tools are cleared" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-x", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    var end_event = try toolEndEvent("call-x", "shell", "{\"ok\":true}", false);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);
    try std.testing.expectEqualStrings("call-x", state.tools.items[0].id);

    state.clearTools();

    var restart = try toolStartEvent("call-x", "shell", "{\"command\":\"pwd\"}");
    defer restart.deinit(std.testing.allocator);
    try state.applyEvent(restart);
    try std.testing.expectEqual(@as(usize, 1), state.tools.items.len);
    try std.testing.expectEqualStrings("call-x", state.tools.items[0].id);
    try std.testing.expectEqual(@as(usize, 1), state.tools.items[0].occurrence);
}

test "AppState reconciles an interrupted occurrence from a retained result" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-r", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    try state.applyEvent(.{ .turn_end = .{ .stop_reason = .stop } });
    try std.testing.expectEqual(ToolStatus.interrupted, state.tools.items[0].status);
    try std.testing.expectEqual(TerminalEvidence.none, state.tools.items[0].terminal_evidence);
    try std.testing.expectEqual(@as(usize, 1), countRows(&state, true, state.tools.items[0].id));
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "interrupted") != null);

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_event = try toolResultMessageEvent("call-r", "shell", "all good", "{\"ok\":true}", false);
    defer result_event.deinit(std.testing.allocator);
    try state.applyEvent(result_event);

    try std.testing.expectEqual(@as(usize, 1), state.tools.items.len);
    try std.testing.expectEqual(ToolStatus.done, state.tools.items[0].status);
    try std.testing.expectEqual(TerminalEvidence.result, state.tools.items[0].terminal_evidence);
    try std.testing.expectEqual(@as(usize, 1), countRows(&state, true, "call-r"));
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "interrupted") == null);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "ok") != null);
    try std.testing.expectEqual(@as(usize, 1), countRows(&state, false, "call-r"));

    var late_end = try toolEndEvent("call-r", "shell", "{\"ok\":true}", false);
    defer late_end.deinit(std.testing.allocator);
    try state.applyEvent(late_end);
    try std.testing.expectEqual(@as(usize, 1), state.tools.items.len);
    try std.testing.expectEqual(TerminalEvidence.both, state.tools.items[0].terminal_evidence);
    try std.testing.expectEqual(@as(usize, 1), countRows(&state, true, "call-r"));
}

test "AppState merges a reversed replay into one occurrence and one summary row" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_event = try toolResultMessageEvent("call-v", "shell", "fine", "{\"ok\":true}", false);
    defer result_event.deinit(std.testing.allocator);
    try state.applyEvent(result_event);
    try std.testing.expectEqual(@as(usize, 1), state.tools.items.len);
    try std.testing.expectEqual(ToolStatus.done, state.tools.items[0].status);
    try std.testing.expectEqual(TerminalEvidence.result, state.tools.items[0].terminal_evidence);

    var end_event = try toolEndEvent("call-v", "shell", "{\"ok\":true,\"turn\":2}", false);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try std.testing.expectEqual(@as(usize, 1), state.tools.items.len);
    try std.testing.expectEqual(TerminalEvidence.both, state.tools.items[0].terminal_evidence);
    try std.testing.expectEqual(ToolStatus.done, state.tools.items[0].status);
    try std.testing.expectEqual(@as(usize, 1), countRows(&state, true, "call-v"));
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "output=11B") == null);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "ok") != null);
    try std.testing.expectEqual(@as(usize, 1), countRows(&state, false, "call-v"));
    try std.testing.expectEqual(state.transcript.items.len, state.summary_scan_floor);
}

test "AppState emits one error card when a failing result precedes its end" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_event = try toolResultMessageEvent("call-e", "shell", "Tool execution failed: Boom", "{\"ok\":false,\"err\":\"Boom\"}", true);
    defer result_event.deinit(std.testing.allocator);
    try state.applyEvent(result_event);

    try std.testing.expectEqual(@as(usize, 1), state.tools.items.len);
    try std.testing.expectEqual(ToolStatus.@"error", state.tools.items[0].status);
    try std.testing.expect(state.tools.items[0].error_card_emitted);
    try std.testing.expectEqual(@as(usize, 1), countRows(&state, true, "call-e"));
    try std.testing.expectEqual(@as(usize, 0), countRows(&state, false, "call-e"));
    var error_rows: usize = 0;
    for (state.transcript.items) |*entry| {
        if (entry.kind == .@"error") error_rows += 1;
    }
    try std.testing.expectEqual(@as(usize, 1), error_rows);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[1].text.items, "Boom") != null);

    var end_event = try toolEndEvent("call-e", "shell", "{\"ok\":false,\"err\":\"Boom\"}", true);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try std.testing.expectEqual(@as(usize, 1), state.tools.items.len);
    try std.testing.expectEqual(TerminalEvidence.both, state.tools.items[0].terminal_evidence);
    try std.testing.expectEqual(ToolStatus.@"error", state.tools.items[0].status);
    try std.testing.expectEqual(@as(usize, 1), countRows(&state, true, "call-e"));
    error_rows = 0;
    for (state.transcript.items) |*entry| {
        if (entry.kind == .@"error") error_rows += 1;
    }
    try std.testing.expectEqual(@as(usize, 1), error_rows);
}

test "AppState inserts a summary row for an interrupted occurrence without one" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var approval = session_runtime.SessionEvent{ .tool_approval_requested = .{
        .tool_call_id = try ownedText("call-a"),
        .tool_name = try ownedText("shell"),
        .args_json = try ownedText("{\"command\":\"ls\"}"),
    } };
    defer approval.deinit(std.testing.allocator);
    try state.applyEvent(approval);
    try std.testing.expectEqual(@as(usize, 1), state.tools.items.len);
    try std.testing.expectEqual(@as(usize, 0), state.transcript.items.len);

    try state.applyEvent(.{ .turn_end = .{ .stop_reason = .stop } });

    try std.testing.expectEqual(ToolStatus.interrupted, state.tools.items[0].status);
    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expect(state.transcript.items[0].tool_summary);
    try std.testing.expectEqualStrings("call-a", state.transcript.items[0].tool_call_id);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "interrupted") != null);
    try std.testing.expectEqual(@as(usize, 1), state.finalized_tool_count);
}

test "AppState render-links a result after its end without new rows or state flips" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-n", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    var end_event = try toolEndEvent("call-n", "shell", "{\"ok\":true}", false);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);
    const rows_after_end = state.transcript.items.len;

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_event = try toolResultMessageEvent("call-n", "shell", "all good", "{\"ok\":true}", false);
    defer result_event.deinit(std.testing.allocator);
    try state.applyEvent(result_event);

    try std.testing.expectEqual(@as(usize, 1), state.tools.items.len);
    try std.testing.expectEqual(TerminalEvidence.both, state.tools.items[0].terminal_evidence);
    try std.testing.expectEqual(ToolStatus.done, state.tools.items[0].status);
    try std.testing.expectEqual(@as(usize, 1), countRows(&state, true, "call-n"));
    try std.testing.expectEqual(@as(usize, 1), countRows(&state, false, "call-n"));
    try std.testing.expectEqual(rows_after_end + 1, state.transcript.items.len);
    try std.testing.expectEqual(state.transcript.items.len, state.summary_scan_floor);
    try std.testing.expectEqual(@as(usize, 1), state.summary_floor_tool);
}

test "AppState upgrades a done occurrence when its retained result reports failure" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-u", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    var end_event = try toolEndEvent("call-u", "shell", "{\"ok\":true}", false);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);
    try std.testing.expectEqual(ToolStatus.done, state.tools.items[0].status);

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_event = try toolResultMessageEvent("call-u", "shell", "Tool execution failed: Boom", "{\"ok\":false,\"err\":\"Boom\"}", true);
    defer result_event.deinit(std.testing.allocator);
    try state.applyEvent(result_event);

    try std.testing.expectEqual(ToolStatus.@"error", state.tools.items[0].status);
    try std.testing.expectEqual(TerminalEvidence.both, state.tools.items[0].terminal_evidence);
    try std.testing.expect(state.tools.items[0].error_card_emitted);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "failed") != null);
    try std.testing.expectEqual(@as(usize, 0), countRows(&state, false, "call-u"));
}

test "AppState refreshes an opaque error card when the end half recovers detail" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_event = try toolResultMessageEvent("call-c", "shell", "Tool execution failed", "null", true);
    defer result_event.deinit(std.testing.allocator);
    try state.applyEvent(result_event);
    try std.testing.expect(state.tools.items[0].error_card_emitted);
    try std.testing.expect(!state.tools.items[0].error_detail_readable);
    const card_row = state.transcript.items[2];
    try std.testing.expectEqual(TranscriptKind.@"error", card_row.kind);
    try std.testing.expect(std.mem.indexOf(u8, card_row.text.items, "null") != null);

    var end_event = try toolEndEvent("call-c", "shell", "{\"ok\":false,\"err\":\"Boom\"}", true);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try std.testing.expect(state.tools.items[0].error_detail_readable);
    var error_rows: usize = 0;
    for (state.transcript.items) |*entry| {
        if (entry.kind != .@"error") continue;
        error_rows += 1;
        try std.testing.expect(std.mem.indexOf(u8, entry.text.items, "Boom") != null);
        try std.testing.expect(std.mem.indexOf(u8, entry.text.items, "null") == null);
    }
    try std.testing.expectEqual(@as(usize, 1), error_rows);
    try std.testing.expect(std.mem.indexOf(u8, state.status.last_error, "Boom") != null);
}

test "AppState treats results after a completed or retired occurrence as new occurrences" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-p", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    var end_event = try toolEndEvent("call-p", "shell", "{\"ok\":true}", false);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);
    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_event = try toolResultMessageEvent("call-p", "shell", "first", "{\"ok\":true}", false);
    defer result_event.deinit(std.testing.allocator);
    try state.applyEvent(result_event);
    try std.testing.expectEqual(TerminalEvidence.both, state.tools.items[0].terminal_evidence);

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var repeat_result = try toolResultMessageEvent("call-p", "shell", "second", "{\"ok\":false,\"err\":\"Boom\"}", true);
    defer repeat_result.deinit(std.testing.allocator);
    try state.applyEvent(repeat_result);

    try std.testing.expectEqual(@as(usize, 2), state.tools.items.len);
    try std.testing.expectEqualStrings("call-p", state.tools.items[0].id);
    try std.testing.expectEqualStrings("call-p\x1f2", state.tools.items[1].id);
    try std.testing.expectEqual(TerminalEvidence.result, state.tools.items[1].terminal_evidence);
    try std.testing.expectEqual(ToolStatus.@"error", state.tools.items[1].status);
    try std.testing.expectEqual(@as(usize, 1), countRows(&state, true, "call-p\x1f2"));

    try state.applyEvent(.{ .turn_start = .{} });
    try std.testing.expect(state.tools.items[0].retired);
    try std.testing.expect(state.tools.items[1].retired);
    try std.testing.expect(state.summary_scan_floor > 0);
    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var late_result = try toolResultMessageEvent("call-p", "shell", "third", "{\"ok\":true}", false);
    defer late_result.deinit(std.testing.allocator);
    try state.applyEvent(late_result);
    try std.testing.expectEqual(@as(usize, 3), state.tools.items.len);
    try std.testing.expectEqualStrings("call-p\x1f3", state.tools.items[2].id);
    try std.testing.expectEqual(@as(usize, 1), countRows(&state, true, "call-p\x1f3"));
}

test "AppState merges the retained result over accumulated preview output" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-w", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    var update_event = session_runtime.SessionEvent{ .tool_execution_update = .{
        .tool_call_id = try ownedText("call-w"),
        .tool_name = try ownedText("shell"),
        .args_json = try ownedText(""),
        .partial_result_json = try ownedText("{\"partial\":1}"),
    } };
    defer update_event.deinit(std.testing.allocator);
    try state.applyEvent(update_event);
    try state.applyEvent(.{ .turn_end = .{ .stop_reason = .stop } });

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_event = try toolResultMessageEvent("call-w", "shell", "final output", "{\"ok\":true,\"final\":1}", false);
    defer result_event.deinit(std.testing.allocator);
    try state.applyEvent(result_event);

    try std.testing.expect(std.mem.indexOf(u8, state.tools.items[0].output.items, "{\"partial\":1}") != null);
    try std.testing.expect(std.mem.indexOf(u8, state.tools.items[0].output.items, "{\"ok\":true,\"final\":1}") != null);
    try std.testing.expectEqual(TerminalEvidence.result, state.tools.items[0].terminal_evidence);
}

test "AppState keeps result-recovered artifacts against a legacy telemetry-less end" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-o", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    try state.applyEvent(.{ .turn_end = .{ .stop_reason = .stop } });

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_event = session_runtime.SessionEvent{ .message_end = .{
        .role = .tool_result,
        .tool_call_id = try ownedText("call-o"),
        .tool_name = try ownedText("shell"),
        .text = try ownedText("artifact written"),
        .details_json = try ownedText("{\"ok\":true,\"artifact\":\"out.txt\"}"),
        .artifacts_json = try ownedText("[{\"uri\":\"artifact://out\"},{\"uri\":\"artifact://err\"}]"),
    } };
    defer result_event.deinit(std.testing.allocator);
    try state.applyEvent(result_event);

    try std.testing.expectEqualStrings("{\"ok\":true,\"artifact\":\"out.txt\"}", state.tools.items[0].output.items);
    try std.testing.expectEqual(@as(u32, 2), state.tools.items[0].artifact_count);
    try std.testing.expect(state.tools.items[0].truncated);
    var summary_text: ?[]const u8 = null;
    for (state.transcript.items) |*entry| {
        if (entry.kind == .tool and entry.tool_summary and std.mem.eql(u8, entry.tool_call_id, "call-o")) summary_text = entry.text.items;
    }
    try std.testing.expect(summary_text != null);
    try std.testing.expect(std.mem.indexOf(u8, summary_text.?, "artifacts=2 on disk") != null);
    const output_len = state.tools.items[0].output.items.len;

    var late_end = try toolEndEvent("call-o", "shell", "{\"ok\":true,\"artifact\":\"out.txt\"}", false);
    defer late_end.deinit(std.testing.allocator);
    try state.applyEvent(late_end);
    try std.testing.expectEqual(output_len, state.tools.items[0].output.items.len);
    try std.testing.expectEqual(@as(u32, 2), state.tools.items[0].artifact_count);
    try std.testing.expect(state.tools.items[0].truncated);
    try std.testing.expectEqual(TerminalEvidence.both, state.tools.items[0].terminal_evidence);
}

test "AppState merges end telemetry into result-recovered state" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var result_event = session_runtime.SessionEvent{ .message_end = .{
        .role = .tool_result,
        .tool_call_id = try ownedText("call-t"),
        .tool_name = try ownedText("shell"),
        .text = try ownedText("kept"),
        .details_json = try ownedText("{\"ok\":true}"),
        .artifacts_json = try ownedText("[{\"uri\":\"artifact://one\"}]"),
    } };
    defer result_event.deinit(std.testing.allocator);
    try state.applyEvent(result_event);

    var end_event = session_runtime.SessionEvent{ .tool_execution_end = .{
        .tool_call_id = try ownedText("call-t"),
        .tool_name = try ownedText("shell"),
        .result_json = try ownedText("{\"ok\":true}"),
        .is_error = false,
        .raw_total_bytes = 120,
        .returned_total_bytes = 80,
        .estimated_returned_tokens = 9,
        .artifact_count = 1,
        .artifact_refs = try ownedText("artifact://one"),
    } };
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);

    try std.testing.expectEqual(@as(u64, 120), state.tools.items[0].raw_total_bytes);
    try std.testing.expectEqual(@as(u64, 80), state.tools.items[0].returned_total_bytes);
    try std.testing.expectEqual(@as(u64, 9), state.tools.items[0].estimated_returned_tokens);
    try std.testing.expectEqual(@as(u32, 1), state.tools.items[0].artifact_count);
    try std.testing.expectEqualStrings("artifact://one", state.tools.items[0].artifact_refs);
    try std.testing.expectEqual(TerminalEvidence.both, state.tools.items[0].terminal_evidence);
    try std.testing.expect(std.mem.indexOf(u8, state.transcript.items[0].text.items, "raw=120B returned=80B") != null);
}

test "AppState retires terminal occurrences at agent_end and releases the floor" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var start_event = try toolStartEvent("call-g", "shell", "{\"command\":\"ls\"}");
    defer start_event.deinit(std.testing.allocator);
    try state.applyEvent(start_event);
    var end_event = try toolEndEvent("call-g", "shell", "{\"ok\":true}", false);
    defer end_event.deinit(std.testing.allocator);
    try state.applyEvent(end_event);
    try std.testing.expectEqual(TerminalEvidence.execution, state.tools.items[0].terminal_evidence);
    try std.testing.expect(!state.tools.items[0].retired);
    try std.testing.expectEqual(@as(usize, 0), state.summary_scan_floor);

    try state.applyEvent(.{ .agent_end = .{ .reason = .completed } });
    try std.testing.expect(state.tools.items[0].retired);
    try std.testing.expectEqual(state.transcript.items.len, state.summary_scan_floor);

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var late_result = try toolResultMessageEvent("call-g", "shell", "late", "{\"ok\":true}", false);
    defer late_result.deinit(std.testing.allocator);
    try state.applyEvent(late_result);
    try std.testing.expectEqual(@as(usize, 2), state.tools.items.len);
    try std.testing.expectEqualStrings("call-g\x1f2", state.tools.items[1].id);
}

test "AppState retires terminal occurrences behind a live gap without rescanning" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var gap_start = try toolStartEvent("call-gap", "shell", "{\"command\":\"watch\"}");
    defer gap_start.deinit(std.testing.allocator);
    try state.applyEvent(gap_start);
    for (0..4) |i| {
        var buf: [24]u8 = undefined;
        const id = try std.fmt.bufPrint(&buf, "call-{d}", .{i});
        var start_event = try toolStartEvent(id, "shell", "{\"command\":\"ls\"}");
        defer start_event.deinit(std.testing.allocator);
        try state.applyEvent(start_event);
        var end_event = try toolEndEvent(id, "shell", "{\"ok\":true}", false);
        defer end_event.deinit(std.testing.allocator);
        try state.applyEvent(end_event);
    }

    try state.applyEvent(.{ .turn_start = .{} });
    try std.testing.expect(!state.tools.items[0].retired);
    for (1..5) |i| try std.testing.expect(state.tools.items[i].retired);
    try std.testing.expectEqual(@as(usize, 0), state.retire_candidates.items.len);
    try std.testing.expectEqual(@as(usize, 0), state.summary_scan_floor);

    var late_start = try toolStartEvent("call-late", "shell", "{\"command\":\"id\"}");
    defer late_start.deinit(std.testing.allocator);
    try state.applyEvent(late_start);
    var late_end = try toolEndEvent("call-late", "shell", "{\"ok\":true}", false);
    defer late_end.deinit(std.testing.allocator);
    try state.applyEvent(late_end);
    try state.applyEvent(.{ .turn_start = .{} });
    try std.testing.expect(state.tools.items[5].retired);
    try std.testing.expectEqual(@as(usize, 0), state.retire_candidates.items.len);

    try state.applyEvent(.{ .turn_end = .{ .stop_reason = .stop } });
    try std.testing.expectEqual(ToolStatus.interrupted, state.tools.items[0].status);
    try state.applyEvent(.{ .turn_start = .{} });
    try std.testing.expect(state.tools.items[0].retired);
    try std.testing.expectEqual(state.transcript.items.len, state.summary_scan_floor);
}

test "AppState scans below the floor for delayed halves of interleaved occurrences" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var b_start = try toolStartEvent("call-b", "shell", "{\"command\":\"pwd\"}");
    defer b_start.deinit(std.testing.allocator);
    try state.applyEvent(b_start);
    var b_end = try toolEndEvent("call-b", "shell", "{\"ok\":true}", false);
    defer b_end.deinit(std.testing.allocator);
    try state.applyEvent(b_end);
    var x_start = try toolStartEvent("call-x", "shell", "{\"command\":\"ls\"}");
    defer x_start.deinit(std.testing.allocator);
    try state.applyEvent(x_start);

    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var x_result = try toolResultMessageEvent("call-x", "shell", "x fine", "{\"ok\":true}", false);
    defer x_result.deinit(std.testing.allocator);
    try state.applyEvent(x_result);
    try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
    var b_result = try toolResultMessageEvent("call-b", "shell", "b fine", "{\"ok\":true}", false);
    defer b_result.deinit(std.testing.allocator);
    try state.applyEvent(b_result);
    try state.applyEvent(.{ .turn_end = .{ .stop_reason = .stop } });
    try std.testing.expectEqual(TerminalEvidence.both, state.tools.items[0].terminal_evidence);
    try std.testing.expectEqual(TerminalEvidence.result, state.tools.items[1].terminal_evidence);

    var z_start = try toolStartEvent("call-z", "shell", "{\"command\":\"id\"}");
    defer z_start.deinit(std.testing.allocator);
    try state.applyEvent(z_start);
    var z_end = try toolEndEvent("call-z", "shell", "{\"ok\":true}", false);
    defer z_end.deinit(std.testing.allocator);
    try state.applyEvent(z_end);

    var x_end = try toolEndEvent("call-x", "shell", "{\"ok\":true,\"v\":2}", false);
    defer x_end.deinit(std.testing.allocator);
    try state.applyEvent(x_end);

    try std.testing.expectEqual(TerminalEvidence.both, state.tools.items[1].terminal_evidence);
    try std.testing.expectEqual(@as(usize, 1), countRows(&state, true, "call-x"));
    var x_summary: ?[]const u8 = null;
    for (state.transcript.items) |*entry| {
        if (entry.kind == .tool and entry.tool_summary and std.mem.eql(u8, entry.tool_call_id, "call-x")) x_summary = entry.text.items;
    }
    try std.testing.expect(x_summary != null);
    try std.testing.expect(std.mem.indexOf(u8, x_summary.?, "output=11B") == null);
}

test "AppState occurrence watermarks advance over completed families" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    for (0..6) |i| {
        var buf: [24]u8 = undefined;
        const id = try std.fmt.bufPrint(&buf, "call-{d}", .{i});
        var start_event = try toolStartEvent(id, "shell", "{\"command\":\"ls\"}");
        defer start_event.deinit(std.testing.allocator);
        try state.applyEvent(start_event);
        var end_event = try toolEndEvent(id, "shell", "{\"ok\":true}", false);
        defer end_event.deinit(std.testing.allocator);
        try state.applyEvent(end_event);
        try state.applyEvent(.{ .message_start = .{ .role = .tool_result } });
        var result_event = try toolResultMessageEvent(id, "shell", "fine", "{\"ok\":true}", false);
        defer result_event.deinit(std.testing.allocator);
        try state.applyEvent(result_event);
        try state.applyEvent(.{ .turn_end = .{ .stop_reason = .stop } });
    }

    try std.testing.expectEqual(@as(usize, 6), state.tools.items.len);
    try std.testing.expectEqual(@as(usize, 6), state.finalized_tool_count);
    try std.testing.expectEqual(@as(usize, 6), state.summary_floor_tool);
    try std.testing.expectEqual(state.transcript.items.len, state.summary_scan_floor);
}

test "lastAssistantText returns the most recent assistant reply" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try std.testing.expect(state.lastAssistantText() == null);

    try state.appendTranscript(.user, "hello");
    try state.appendTranscript(.assistant, "first reply");
    try state.appendTranscript(.user, "again");
    try state.appendTranscript(.assistant, "second reply");
    try state.appendTranscript(.system, "noise");

    try std.testing.expectEqualStrings("second reply", state.lastAssistantText().?);
}

test "AppState stream_aborted ignores stale lifecycle events" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    state.stream_aborted = true;
    try state.applyEvent(.{ .agent_start = .{} });
    try std.testing.expect(!state.status.streaming);

    try state.applyEvent(.{ .turn_start = .{} });
    try std.testing.expect(!state.status.streaming);

    try state.applyEvent(.{ .agent_end = .{ .reason = .cancelled } });
    try std.testing.expect(!state.status.streaming);
    try std.testing.expect(!state.stream_aborted);
}

test "AppState surfaces system_warning as visible warning transcript entry" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    var warning = session_runtime.SessionEvent{ .system_warning = .{ .message = try ownedText("Warning: 5 events dropped due to backpressure") } };
    defer warning.deinit(std.testing.allocator);
    try state.applyEvent(warning);

    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqual(TranscriptKind.@"error", state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("Warning: 5 events dropped due to backpressure", state.transcript.items[0].text.items);
}

test "AppState updates backpressure status fields" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .backpressure_status = .{ .active = true, .dropped_count = 3 } });
    try std.testing.expect(state.backpressure_active);
    try std.testing.expectEqual(@as(u64, 3), state.dropped_event_count);

    try state.applyEvent(.{ .backpressure_status = .{ .active = false, .dropped_count = 3 } });
    try std.testing.expect(!state.backpressure_active);
    try std.testing.expectEqual(@as(u64, 3), state.dropped_event_count);
}

test "a steer sent with a narrower echo is tracked whole but shown only as its echo" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.appendSteeredMessageEchoing("shown before\n\nnew part", "new part");
    try state.appendSteeredMessageEchoing("shown before", "");
    try std.testing.expectEqual(@as(usize, 2), state.pending_steers.items.len);
    try std.testing.expectEqualStrings("shown before\n\nnew part", state.pending_steers.items[0]);
    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqualStrings("new part", state.transcript.items[0].text.items);
}

test "zen notes the agent once per switch and cancels a switch it never sent" {
    var zen: Zen = .{};
    zen.enter(4);
    try std.testing.expectEqualStrings(zen_enter_note, zen.noteText().?);
    zen.leave();
    try std.testing.expect(zen.noteText() == null);
    zen.enter(4);
    zen.note = .none;
    zen.leave();
    try std.testing.expectEqualStrings(zen_leave_note, zen.noteText().?);
    zen.enter(9);
    try std.testing.expect(zen.noteText() == null);
    try std.testing.expectEqual(@as(usize, 9), zen.start_index);
}

test "zen counts thinking, tool rows and replies since it began, and finds the final reply" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.appendTranscript(.assistant, "before zen");
    const start = state.transcript.items.len;
    try state.appendTranscript(.user, "go");
    try state.appendTranscript(.thinking, "hmm");
    try state.appendToolSummaryTranscript("\u{25c8} Shell Execute \"ls\" ok", "call-1");
    try state.appendTranscript(.assistant, "first");
    try state.appendTranscript(.thinking, "again");
    try state.appendTranscript(.assistant, "last");
    const counts = zenCounts(state.transcript.items, start);
    try std.testing.expectEqual(@as(usize, 2), counts.thinking);
    try std.testing.expectEqual(@as(usize, 1), counts.tools);
    try std.testing.expectEqual(@as(usize, 2), counts.messages);
    try std.testing.expectEqualStrings("last", state.transcript.items[counts.final.?].text.items);
    try std.testing.expectEqual(@as(usize, start + 4), counts.last_activity.?);

    try state.appendTranscript(.user, "next");
    try std.testing.expect(zenCounts(state.transcript.items, start).final == null);
    try state.appendTranscript(.@"error", "unknown command");
    try std.testing.expect(zenCounts(state.transcript.items, start).final == null);
    try state.appendTranscript(.@"error", "HTTP 500");
    state.transcript.items[state.transcript.items.len - 1].run_failure = true;
    try std.testing.expectEqualStrings("HTTP 500", state.transcript.items[zenCounts(state.transcript.items, start).final.?].text.items);
    try std.testing.expectEqual(@as(usize, 0), zenCounts(state.transcript.items, 99).messages);

    state.zen.enter(state.transcript.items.len);
    state.clearTranscript();
    try std.testing.expectEqual(@as(usize, 0), state.zen.start_index);
    try state.appendTranscript(.assistant, "after clear");
    try std.testing.expectEqual(@as(usize, 1), zenCounts(state.transcript.items, state.zen.start_index).messages);
}

test "the zen note comes off a user message, and nothing else does" {
    try std.testing.expectEqualStrings("hi", withoutZenNote(zen_enter_note ++ "\n\nhi"));
    try std.testing.expectEqualStrings("hi", withoutZenNote(zen_leave_note ++ "\n\nhi"));
    try std.testing.expectEqualStrings(zen_enter_note, withoutZenNote(zen_enter_note));
    try std.testing.expectEqualStrings("hi", withoutZenNote("hi"));
}

test "zen's starting point follows rows inserted or removed above it" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.appendTranscript(.user, "a");
    try state.appendTranscript(.thinking, "b");
    state.zen.enter(state.transcript.items.len);
    try state.insertToolSummaryRowAt(0, "\u{25c8} Shell Execute \"ls\"", "call-1");
    try std.testing.expectEqual(@as(usize, 3), state.zen.start_index);
    state.removeTranscriptEntry(1);
    try std.testing.expectEqual(@as(usize, 2), state.zen.start_index);
    state.removeTranscriptEntry(1);
    try std.testing.expectEqual(@as(usize, 1), state.zen.start_index);
}

test "zen entered mid-reply counts the reply already streaming" {
    var state = AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.appendTranscript(.user, "go");
    try state.appendTranscript(.assistant, "half a rep");
    state.active_assistant_entry = 1;
    try state.appendNotice("zen on");
    state.zen.enter(zenStart(&state));
    try std.testing.expectEqual(@as(usize, 1), state.zen.start_index);
    try std.testing.expectEqual(@as(usize, 1), zenCounts(state.transcript.items, state.zen.start_index).final.?);
    state.active_assistant_entry = null;
    try std.testing.expectEqual(state.transcript.items.len, zenStart(&state));
}

test "zen queues each new line and lets every slide run its full length" {
    var zen: Zen = .{};
    zen.beginRun(0);
    zen.noteActivity("thinking");
    zen.noteActivity("thinking");
    zen.noteActivity("Read  a.zig");
    zen.noteActivity("Shell Execute  ls");
    try std.testing.expectEqual(@as(usize, 3), zen.queue_len);
    zen.advance(0, 40);
    try std.testing.expectEqualStrings("", zen.activity());
    try std.testing.expectEqualStrings("thinking", zen.incomingActivity());
    try std.testing.expectEqual(@as(f32, 0.5), zen.rise(20, 40));
    zen.advance(39, 40);
    try std.testing.expectEqualStrings("thinking", zen.incomingActivity());
    zen.advance(40, 40);
    try std.testing.expectEqualStrings("thinking", zen.activity());
    try std.testing.expectEqualStrings("Read  a.zig", zen.incomingActivity());
    try std.testing.expectEqual(@as(f32, 0), zen.rise(40, 40));
    zen.advance(85, 40);
    try std.testing.expectEqualStrings("Read  a.zig", zen.activity());
    try std.testing.expectEqualStrings("Shell Execute  ls", zen.incomingActivity());
    try std.testing.expectEqual(@as(f32, 0.125), zen.rise(85, 40));
    try std.testing.expect(!zen.settled());
    zen.advance(120, 40);
    try std.testing.expectEqualStrings("Shell Execute  ls", zen.activity());
    try std.testing.expect(zen.settled());
    zen.advance(500, 40);
    zen.noteActivity("thinking");
    zen.advance(501, 40);
    try std.testing.expectEqual(@as(f32, 0), zen.rise(501, 40));
    try std.testing.expectEqual(@as(f32, 0.5), zen.rise(521, 40));
}

test "zen's queue keeps the newest lines when it overflows, and clips each line on a character" {
    var zen: Zen = .{};
    for (0..zen_queue_lines + 3) |i| {
        var buf: [16]u8 = undefined;
        zen.noteActivity(try std.fmt.bufPrint(&buf, "step {d}", .{i}));
    }
    try std.testing.expectEqual(zen_queue_lines, zen.queue_len);
    try std.testing.expectEqualStrings("step 3", zen.queue[0].text());
    const long = "\u{2026}" ** 100;
    const line = ZenLine.of(long);
    try std.testing.expect(line.text().len <= zen_activity_bytes);
    try std.testing.expect(std.unicode.utf8ValidateSlice(line.text()));
}

test "the step clock times only a timed step, from its own start" {
    var zen: Zen = .{};
    try std.testing.expectEqual(@as(u64, 0), zen.stepMs(null, 1_790_000_000_000));
    try std.testing.expectEqual(@as(u64, 0), zen.stepMs(4, 100_000));
    try std.testing.expectEqual(@as(u64, 12_000), zen.stepMs(4, 112_000));
    try std.testing.expectEqual(@as(u64, 0), zen.stepMs(7, 113_000));
    try std.testing.expectEqual(@as(u64, 3_000), zen.stepMs(7, 116_000));
    try std.testing.expectEqual(@as(u64, 0), zen.stepMs(null, 130_000));
    try std.testing.expectEqual(@as(u64, 0), zen.stepMs(7, 131_000));
    zen.noteActivity("thinking");
    zen.beginRun(40);
    try std.testing.expectEqualStrings("", zen.activity());
    try std.testing.expect(zen.settled());
    try std.testing.expectEqual(@as(u64, 0), zen.stepMs(7, 140_000));
}

test "a compaction notice leaves out the counts a remote compaction does not report" {
    const notice = try AppState.compactionNotice(std.testing.allocator, .{ .outcome = .completed, .text = ai_types.OwnedSlice(u8).initBorrowed("the session so far"), .tokens_after = 1200 });
    defer std.testing.allocator.free(notice);
    try std.testing.expect(std.mem.startsWith(u8, notice, "conversation compacted · ~1.2k tokens\n"));
    try std.testing.expect(std.mem.indexOf(u8, notice, "messages") == null);
}
