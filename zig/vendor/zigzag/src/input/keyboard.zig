
const std = @import("std");
const keys = @import("keys.zig");
const mouse = @import("mouse.zig");

pub const Key = keys.Key;
pub const KeyEvent = keys.KeyEvent;
pub const Modifiers = keys.Modifiers;
pub const MouseEvent = mouse.MouseEvent;

pub const ParseResult = union(enum) {
    key: KeyEvent,
    mouse: MouseEvent,
    none,
};

pub const ParseReturn = struct { result: ParseResult, consumed: usize };

pub const StreamStep = union(enum) {
    event: ParseReturn,
    incomplete,
};

const paste_start = "\x1b[200~";
const paste_end = "\x1b[201~";

pub fn parse(data: []const u8) ParseReturn {
    return switch (parseStream(data)) {
        .event => |e| e,
        .incomplete => parseFlush(data),
    };
}

pub fn parseStream(data: []const u8) StreamStep {
    if (data.len == 0) return .incomplete;

    if (data[0] == 0x1b) {
        const frame_len = switch (frameEscape(data)) {
            .complete => |n| n,
            .incomplete => return .incomplete,
        };
        return parseFramed(data, frame_len);
    }

    if (data[0] < 32) {
        return .{ .event = .{ .result = .{ .key = parseControl(data[0]) }, .consumed = 1 } };
    }

    if (data[0] == 127) {
        return .{ .event = .{ .result = .{ .key = .{ .key = .backspace } }, .consumed = 1 } };
    }

    const len = std.unicode.utf8ByteSequenceLength(data[0]) catch {
        return .{ .event = .{ .result = .{ .key = .{ .key = .{ .char = data[0] } } }, .consumed = 1 } };
    };
    if (len > data.len) return .incomplete;
    const codepoint = std.unicode.utf8Decode(data[0..len]) catch data[0];
    return .{ .event = .{ .result = .{ .key = .{ .key = .{ .char = codepoint } } }, .consumed = len } };
}

pub fn parseFlush(data: []const u8) ParseReturn {
    if (data.len == 0) return .{ .result = .none, .consumed = 0 };

    if (data[0] == 0x1b) {
        if (std.mem.startsWith(u8, data, paste_start)) {
            if (parseBracketedPaste(data)) |result| return result;
        }

        if (data.len >= 2 and data[1] == '[') {
            if (parseCsi(data)) |result| return result;
        }

        if (data.len >= 2 and data[1] == 'O') {
            if (parseSs3(data)) |result| return result;
        }

        if (data.len >= 2 and data[1] != '[' and data[1] != 'O') {
            const inner = parse(data[1..]);
            if (inner.result == .key) {
                var key_event = inner.result.key;
                key_event.modifiers.alt = true;
                return .{ .result = .{ .key = key_event }, .consumed = 1 + inner.consumed };
            }
        }

        return .{ .result = .{ .key = .{ .key = .escape } }, .consumed = 1 };
    }

    if (data[0] < 32) {
        const key_event = parseControl(data[0]);
        return .{ .result = .{ .key = key_event }, .consumed = 1 };
    }

    if (data[0] == 127) {
        return .{ .result = .{ .key = .{ .key = .backspace } }, .consumed = 1 };
    }

    const len = std.unicode.utf8ByteSequenceLength(data[0]) catch 1;
    if (len <= data.len) {
        const codepoint = std.unicode.utf8Decode(data[0..len]) catch data[0];
        return .{ .result = .{ .key = .{ .key = .{ .char = codepoint } } }, .consumed = len };
    }

    return .{ .result = .{ .key = .{ .key = .{ .char = data[0] } } }, .consumed = 1 };
}

const Frame = union(enum) {
    complete: usize,
    incomplete,
};

fn frameEscape(data: []const u8) Frame {
    if (data.len < 2) return .incomplete;

    return switch (data[1]) {
        '[' => frameCsi(data),
        'O' => if (data.len >= 3) Frame{ .complete = 3 } else .incomplete,
        ']' => frameString(data, .bel_or_st),
        'P', '_', '^', 'X' => frameString(data, .st_only),
        0x1b => switch (frameEscape(data[1..])) {
            .complete => |n| Frame{ .complete = 1 + n },
            .incomplete => .incomplete,
        },
        else => frameAltKey(data),
    };
}

fn frameCsi(data: []const u8) Frame {
    var i: usize = 2;
    while (i < data.len and data[i] >= 0x30 and data[i] <= 0x3f) : (i += 1) {}
    while (i < data.len and data[i] >= 0x20 and data[i] <= 0x2f) : (i += 1) {}
    if (i >= data.len) return .incomplete;
    if (data[i] < 0x40 or data[i] > 0x7e) return .{ .complete = i };
    return .{ .complete = i + 1 };
}

const StringTerm = enum { bel_or_st, st_only };

fn discardKind(data: []const u8) ?InputParser.Discard {
    const at = wrappedStart(data) + 1;
    if (at >= data.len) return null;
    return switch (data[at]) {
        '[' => blk: {
            var i = at + 1;
            while (i < data.len and data[i] >= 0x30 and data[i] <= 0x3f) : (i += 1) {}
            break :blk if (i < data.len and data[i] >= 0x20 and data[i] <= 0x2f) .csi_intermediate else .csi;
        },
        ']' => .string_bel_or_st,
        'P', '_', '^', 'X' => .string_st,
        else => null,
    };
}

fn wrappedStart(data: []const u8) usize {
    var i: usize = 0;
    while (i + 1 < data.len and data[i + 1] == 0x1b) : (i += 1) {}
    return i;
}

fn skipDiscarded(state: *InputParser.Discard, data: []const u8) usize {
    switch (state.*) {
        .none => return 0,
        .csi, .csi_intermediate => {
            var i: usize = 0;
            if (state.* == .csi) {
                while (i < data.len and data[i] >= 0x30 and data[i] <= 0x3f) : (i += 1) {}
                if (i < data.len and data[i] >= 0x20 and data[i] <= 0x2f) state.* = .csi_intermediate;
            }
            while (i < data.len and data[i] >= 0x20 and data[i] <= 0x2f) : (i += 1) {}
            if (i == data.len) return i;
            state.* = .none;
            return if (data[i] >= 0x40 and data[i] <= 0x7e) i + 1 else i;
        },
        .string_bel_or_st, .string_st => {
            var i: usize = 0;
            while (i < data.len) : (i += 1) {
                if (data[i] == 0x07 and state.* == .string_bel_or_st) {
                    state.* = .none;
                    return i + 1;
                }
                if (data[i] == 0x1b) {
                    if (i + 1 == data.len) return i;
                    state.* = .none;
                    return if (data[i + 1] == '\\') i + 2 else i;
                }
            }
            return i;
        },
    }
}

fn frameString(data: []const u8, term: StringTerm) Frame {
    var i: usize = 2;
    while (i < data.len) : (i += 1) {
        if (term == .bel_or_st and data[i] == 0x07) return .{ .complete = i + 1 };
        if (data[i] == 0x1b) {
            if (i + 1 >= data.len) return .incomplete;
            if (data[i + 1] == '\\') return .{ .complete = i + 2 };
            return .{ .complete = i };
        }
    }
    return .incomplete;
}

fn frameAltKey(data: []const u8) Frame {
    const b = data[1];
    if (b < 0x80) return .{ .complete = 2 };
    const len = std.unicode.utf8ByteSequenceLength(b) catch return .{ .complete = 2 };
    if (1 + len > data.len) return .incomplete;
    return .{ .complete = 1 + len };
}

fn parseFramed(data: []const u8, frame_len: usize) StreamStep {
    const dropped = StreamStep{ .event = .{ .result = .none, .consumed = frame_len } };
    if (frame_len < 2) return .{ .event = .{ .result = .{ .key = .{ .key = .escape } }, .consumed = 1 } };

    const seq = data[0..frame_len];

    switch (data[1]) {
        '[' => {
            if (std.mem.eql(u8, seq, "\x1b[M")) {
                if (data.len < mouse.x10_report_len) return .incomplete;
                if (mouse.parseX10(data)) |m| {
                    return .{ .event = .{ .result = .{ .mouse = m }, .consumed = mouse.x10_report_len } };
                }
                return .{ .event = .{ .result = .none, .consumed = mouse.x10_report_len } };
            }

            if (std.mem.eql(u8, seq, paste_start)) {
                const content = data[paste_start.len..];
                const end = std.mem.indexOf(u8, content, paste_end) orelse return .incomplete;
                return .{ .event = .{
                    .result = .{ .key = .{ .key = .{ .paste = content[0..end] } } },
                    .consumed = paste_start.len + end + paste_end.len,
                } };
            }
            if (parseCsi(seq)) |result| return .{ .event = result };
            return dropped;
        },
        'O' => {
            if (parseSs3(seq)) |result| return .{ .event = result };
            return dropped;
        },
        ']', 'P', '_', '^', 'X' => return dropped,
        else => {},
    }

    switch (parseStream(data[1..])) {
        .event => |inner| switch (inner.result) {
            .key => |k| {
                var key_event = k;
                key_event.modifiers.alt = true;
                return .{ .event = .{ .result = .{ .key = key_event }, .consumed = 1 + inner.consumed } };
            },
            .mouse => |m| return .{ .event = .{ .result = .{ .mouse = m }, .consumed = 1 + inner.consumed } },
            .none => return .{ .event = .{ .result = .none, .consumed = 1 + inner.consumed } },
        },
        .incomplete => return .incomplete,
    }
}

fn parseControl(c: u8) KeyEvent {
    return switch (c) {
        0 => .{ .key = .null_key, .modifiers = .{ .ctrl = true } },
        8 => .{ .key = .backspace },
        9 => .{ .key = .tab },
        10 => .{ .key = .enter, .modifiers = .{ .shift = true } },
        13 => .{ .key = .enter },
        27 => .{ .key = .escape },
        1...7, 11, 12, 14...26 => .{
            .key = .{ .char = 'a' + c - 1 },
            .modifiers = .{ .ctrl = true },
        },
        else => .{ .key = .{ .char = c } },
    };
}

fn accumulateParam(value: u16, digit: u8) u16 {
    const scaled = std.math.mul(u16, value, 10) catch return std.math.maxInt(u16);
    return std.math.add(u16, scaled, digit - '0') catch std.math.maxInt(u16);
}

fn parseCsi(data: []const u8) ?ParseReturn {
    if (data.len < 3) return null;
    if (data[0] != 0x1b or data[1] != '[') return null;

    if (data.len >= 3 and data[2] == '<') {
        if (mouse.parseSgr(data)) |m| {
            return .{ .result = .{ .mouse = m.event }, .consumed = m.consumed };
        }
    }

    if (data[2] == 'M') {
        if (mouse.parseX10(data)) |m| {
            return .{ .result = .{ .mouse = m }, .consumed = mouse.x10_report_len };
        }
    }

    var idx: usize = 2;
    var params: [8]u16 = @splat(0);
    var param_count: usize = 0;
    var has_colon = false;
    var sub_params: [8]u16 = @splat(0);

    while (idx < data.len and param_count < params.len) {
        const c = data[idx];
        if (c >= '0' and c <= '9') {
            params[param_count] = accumulateParam(params[param_count], c);
            idx += 1;
        } else if (c == ';') {
            param_count += 1;
            idx += 1;
        } else if (c == ':') {
            has_colon = true;
            sub_params[param_count] = 0;
            idx += 1;
            while (idx < data.len and data[idx] >= '0' and data[idx] <= '9') {
                sub_params[param_count] = accumulateParam(sub_params[param_count], data[idx]);
                idx += 1;
            }
        } else {
            break;
        }
    }
    if (param_count == params.len) return null;
    param_count += 1;

    if (idx >= data.len) return null;

    const final_byte = data[idx];
    idx += 1;

    if (final_byte == 'u') {
        return parseKittyCsi(params[0..param_count], sub_params[0..param_count], has_colon, idx);
    }

    var modifiers = Modifiers{};
    if (param_count >= 2 and params[1] > 1) {
        const mod_param = params[1] - 1;
        modifiers.shift = (mod_param & 1) != 0;
        modifiers.alt = (mod_param & 2) != 0;
        modifiers.ctrl = (mod_param & 4) != 0;
    }

    const key: Key = switch (final_byte) {
        'A' => .up,
        'B' => .down,
        'C' => .right,
        'D' => .left,
        'H' => .home,
        'F' => .end,
        'Z' => {
            modifiers.shift = true;
            return .{ .result = .{ .key = .{ .key = .tab, .modifiers = modifiers } }, .consumed = idx };
        },
        '~' => switch (params[0]) {
            1 => .home,
            2 => .insert,
            3 => .delete,
            4 => .end,
            5 => .page_up,
            6 => .page_down,
            7 => .home,
            8 => .end,
            11 => .f1,
            12 => .f2,
            13 => .f3,
            14 => .f4,
            15 => .f5,
            17 => .f6,
            18 => .f7,
            19 => .f8,
            20 => .f9,
            21 => .f10,
            23 => .f11,
            24 => .f12,
            else => return null,
        },
        else => return null,
    };

    return .{ .result = .{ .key = .{ .key = key, .modifiers = modifiers } }, .consumed = idx };
}

fn parseKittyCsi(params: []const u16, sub_params: []const u16, has_colon: bool, consumed: usize) ?ParseReturn {
    if (params.len == 0) return null;

    const keycode = params[0];

    var modifiers = Modifiers{};
    if (params.len >= 2 and params[1] > 1) {
        const mod_param = params[1] - 1;
        modifiers.shift = (mod_param & 1) != 0;
        modifiers.alt = (mod_param & 2) != 0;
        modifiers.ctrl = (mod_param & 4) != 0;
        modifiers.super = (mod_param & 8) != 0;
    }

    var event_type: keys.KeyEventType = .press;
    if (has_colon and params.len >= 2) {
        event_type = switch (sub_params[1]) {
            2 => .repeat,
            3 => .release,
            else => .press,
        };
    }

    const key: Key = switch (keycode) {
        8 => .backspace,
        9 => .tab,
        13 => .enter,
        27 => .escape,
        32 => .space,
        127 => .backspace,
        57358 => .{ .char = 0 },
        else => blk: {
            if (keycode >= 32 and keycode < 127) {
                break :blk .{ .char = @intCast(keycode) };
            }
            if (keycode > 127 and keycode <= 0x10FFFF) {
                break :blk .{ .char = @intCast(keycode) };
            }
            break :blk .null_key;
        },
    };

    return .{
        .result = .{ .key = .{
            .key = key,
            .modifiers = modifiers,
            .event_type = event_type,
        } },
        .consumed = consumed,
    };
}

fn parseBracketedPaste(data: []const u8) ?ParseReturn {
    const content = data[paste_start.len..];

    if (std.mem.indexOf(u8, content, paste_end)) |end_offset| {
        return .{
            .result = .{ .key = .{
                .key = .{ .paste = content[0..end_offset] },
            } },
            .consumed = paste_start.len + end_offset + paste_end.len,
        };
    }

    return .{
        .result = .{ .key = .{
            .key = .{ .paste = content },
        } },
        .consumed = data.len,
    };
}

fn parseSs3(data: []const u8) ?ParseReturn {
    if (data.len < 3) return null;
    if (data[0] != 0x1b or data[1] != 'O') return null;

    const key: Key = switch (data[2]) {
        'P' => .f1,
        'Q' => .f2,
        'R' => .f3,
        'S' => .f4,
        'A' => .up,
        'B' => .down,
        'C' => .right,
        'D' => .left,
        'H' => .home,
        'F' => .end,
        else => return null,
    };

    return .{ .result = .{ .key = .{ .key = key } }, .consumed = 3 };
}

pub fn parseAll(allocator: std.mem.Allocator, data: []const u8) ![]ParseResult {
    var results = std.array_list.Managed(ParseResult).init(allocator);
    errdefer results.deinit();

    var offset: usize = 0;
    while (offset < data.len) {
        const parsed = parse(data[offset..]);
        if (parsed.consumed == 0) break;

        if (parsed.result != .none) {
            try results.append(parsed.result);
        }
        offset += parsed.consumed;
    }

    return results.toOwnedSlice();
}

pub const InputParser = struct {
    pub const capacity = 4096;

    pub const paste_chunk_threshold = capacity / 2;

    pub const default_escape_timeout_ns: u64 = 50 * std.time.ns_per_ms;

    buf: [capacity]u8 = undefined,
    len: usize = 0,
    in_paste: bool = false,
    holding: bool = false,
    holding_since_ns: u64 = 0,
    escape_timeout_ns: u64 = default_escape_timeout_ns,
    discarding: Discard = .none,

    const Discard = enum { none, csi, csi_intermediate, string_bel_or_st, string_st };

    pub fn feed(
        self: *InputParser,
        allocator: std.mem.Allocator,
        data: []const u8,
        now_ns: u64,
    ) ![]ParseResult {
        var results = std.array_list.Managed(ParseResult).init(allocator);
        errdefer results.deinit();

        var rest = data;
        while (true) {
            const take = @min(self.buf.len - self.len, rest.len);
            @memcpy(self.buf[self.len..][0..take], rest[0..take]);
            self.len += take;
            rest = rest[take..];

            try self.drain(allocator, &results, now_ns, rest.len > 0);
            if (rest.len == 0) break;

            if (self.len == self.buf.len) self.clearBuffer();
        }

        return results.toOwnedSlice();
    }

    pub fn pending(self: *const InputParser) []const u8 {
        return self.buf[0..self.len];
    }

    pub fn reset(self: *InputParser) void {
        self.clearBuffer();
        self.in_paste = false;
        self.discarding = .none;
    }

    fn clearBuffer(self: *InputParser) void {
        self.len = 0;
        self.holding = false;
    }

    fn drain(
        self: *InputParser,
        allocator: std.mem.Allocator,
        results: *std.array_list.Managed(ParseResult),
        now_ns: u64,
        force: bool,
    ) !void {
        var offset: usize = 0;

        while (offset < self.len) {
            const chunk = self.buf[offset..self.len];

            if (self.discarding != .none) {
                const string = self.discarding == .string_bel_or_st or self.discarding == .string_st;
                const waited = now_ns -| self.holding_since_ns;
                if (string and chunk.len >= 2 and chunk[0] == 0x1b and chunk[1] != '\\' and self.holding and waited >= self.escape_timeout_ns) {
                    try appendResult(allocator, results, .{ .key = .{ .key = .escape } });
                    offset += 1;
                    self.discarding = .none;
                    self.holding = false;
                    continue;
                }
                offset += skipDiscarded(&self.discarding, chunk);
                if (self.discarding != .none) break;
                self.holding = false;
                continue;
            }

            if (self.in_paste) {
                const consumed = try self.drainPaste(allocator, results, chunk, force);
                if (consumed == 0) break;
                offset += consumed;
                continue;
            }

            if (std.mem.startsWith(u8, chunk, paste_start)) {
                self.in_paste = true;
                offset += paste_start.len;
                continue;
            }

            switch (parseStream(chunk)) {
                .event => |parsed| {
                    if (parsed.consumed == 0) break;
                    try appendResult(allocator, results, parsed.result);
                    offset += parsed.consumed;
                },
                .incomplete => {
                    if (offset == 0 and self.len == self.buf.len and chunk[0] == 0x1b) {
                        const wrapped = wrappedStart(chunk);
                        if (wrapped > 0 and std.mem.startsWith(u8, chunk[wrapped..], paste_start)) {
                            offset = wrapped;
                            continue;
                        }
                        if (discardKind(chunk)) |kind| {
                            self.discarding = kind;
                            offset = self.len;
                            if (kind != .csi and chunk[chunk.len - 1] == 0x1b) offset -= 1;
                            break;
                        }
                    }

                    const waited = now_ns -| self.holding_since_ns;
                    const timed_out = self.holding and waited >= self.escape_timeout_ns;
                    if (!force and !timed_out) break;

                    if (force and offset > 0 and chunk[0] == 0x1b) break;

                    const parsed = parseFlush(chunk);
                    if (parsed.consumed == 0) break;
                    try appendResult(allocator, results, parsed.result);
                    offset += parsed.consumed;
                },
            }
        }

        if (offset > 0) {
            if (offset < self.len) {
                std.mem.copyForwards(u8, self.buf[0 .. self.len - offset], self.buf[offset..self.len]);
            }
            self.len -= offset;
        }

        if (self.len == 0 or self.in_paste) {
            self.holding = false;
        } else if (!self.holding or offset > 0) {
            self.holding = true;
            self.holding_since_ns = now_ns;
        }
    }

    fn drainPaste(
        self: *InputParser,
        allocator: std.mem.Allocator,
        results: *std.array_list.Managed(ParseResult),
        chunk: []const u8,
        force: bool,
    ) !usize {
        if (std.mem.indexOf(u8, chunk, paste_end)) |end| {
            try appendPaste(allocator, results, chunk[0..end]);
            self.in_paste = false;
            return end + paste_end.len;
        }

        const holdback = @min(paste_end.len - 1, chunk.len);
        const ready = chunk.len - holdback;
        if (!force and ready < paste_chunk_threshold) return 0;

        const cut = utf8BoundaryFloor(chunk[0..ready]);
        if (cut == 0) return 0;
        try appendPaste(allocator, results, chunk[0..cut]);
        return cut;
    }

    fn appendResult(
        allocator: std.mem.Allocator,
        results: *std.array_list.Managed(ParseResult),
        result: ParseResult,
    ) !void {
        switch (result) {
            .none => return,
            .key => |k| switch (k.key) {
                .paste => |content| return appendPaste(allocator, results, content),
                else => {},
            },
            else => {},
        }
        try results.append(result);
    }

    fn appendPaste(
        allocator: std.mem.Allocator,
        results: *std.array_list.Managed(ParseResult),
        content: []const u8,
    ) !void {
        const owned = try allocator.dupe(u8, content);
        try results.append(.{ .key = .{ .key = .{ .paste = owned } } });
    }
};

fn utf8BoundaryFloor(bytes: []const u8) usize {
    var i = bytes.len;
    var back: usize = 0;
    while (i > 0 and back < 4) : (back += 1) {
        i -= 1;
        if (bytes[i] & 0xc0 == 0x80) continue;
        const need = std.unicode.utf8ByteSequenceLength(bytes[i]) catch return bytes.len;
        return if (i + need <= bytes.len) bytes.len else i;
    }
    return bytes.len;
}

test "parseCsi rejects more parameters than it can hold" {
    try std.testing.expect(parseCsi("\x1b[1;2;3;4;5;6;7;8;u") == null);
    try std.testing.expect(parseCsi("\x1b[1;2;3;4;5;6;7;8;9u") == null);
}
