const std = @import("std");

pub const Set = std.StaticBitSet(256);

const max_repeat = 1000;
const max_program = 10_000;

const Node = union(enum) {
    empty,
    set: Set,
    begin,
    end,
    concat: []const Node,
    alt: []const Node,
    repeat: Repeat,

    const Repeat = struct { node: *const Node, min: u32, max: ?u32 };
};

const Inst = union(enum) {
    set: Set,
    split: [2]u32,
    jmp: u32,
    begin,
    end,
    match,
};

pub const Regex = struct {
    arena: std.heap.ArenaAllocator,
    program: []const Inst,

    pub fn deinit(self: *Regex) void {
        self.arena.deinit();
        self.* = undefined;
    }

    pub fn fullMatch(self: *const Regex, allocator: std.mem.Allocator, text: []const u8) !?bool {
        for (text) |byte| if (byte >= 0x80) return null;
        var now = try std.DynamicBitSetUnmanaged.initEmpty(allocator, self.program.len);
        defer now.deinit(allocator);
        var next = try std.DynamicBitSetUnmanaged.initEmpty(allocator, self.program.len);
        defer next.deinit(allocator);
        self.close(&now, 0, 0, text.len);
        for (text, 0..) |byte, at| {
            next.unsetAll();
            var live = now.iterator(.{});
            while (live.next()) |pc| switch (self.program[pc]) {
                .set => |set| if (set.isSet(byte)) self.close(&next, @intCast(pc + 1), at + 1, text.len),
                else => {},
            };
            std.mem.swap(std.DynamicBitSetUnmanaged, &now, &next);
        }
        var live = now.iterator(.{});
        while (live.next()) |pc| if (self.program[pc] == .match) return true;
        return false;
    }

    fn close(self: *const Regex, threads: *std.DynamicBitSetUnmanaged, pc: u32, at: usize, len: usize) void {
        if (threads.isSet(pc)) return;
        threads.set(pc);
        switch (self.program[pc]) {
            .jmp => |to| self.close(threads, to, at, len),
            .split => |to| {
                self.close(threads, to[0], at, len);
                self.close(threads, to[1], at, len);
            },
            .begin => if (at == 0) self.close(threads, pc + 1, at, len),
            .end => if (at == len) self.close(threads, pc + 1, at, len),
            else => {},
        }
    }
};

pub const Compiled = union(enum) {
    regex: Regex,
    invalid,
    unsupported,
};

pub fn compile(allocator: std.mem.Allocator, pattern: []const u8) !Compiled {
    var arena = std.heap.ArenaAllocator.init(allocator);
    errdefer arena.deinit();
    var parser = Parser{ .arena = arena.allocator(), .source = pattern };
    const tree = parser.alternation() catch |err| switch (err) {
        error.Invalid => {
            arena.deinit();
            return .invalid;
        },
        error.Unsupported => {
            arena.deinit();
            return .unsupported;
        },
        error.OutOfMemory => return error.OutOfMemory,
    };
    if (parser.at < pattern.len) {
        arena.deinit();
        return .invalid;
    }
    if (parser.soft) {
        arena.deinit();
        return .unsupported;
    }
    var emitter = Emitter{ .arena = arena.allocator() };
    emitter.emit(tree) catch |err| switch (err) {
        error.Unsupported => {
            arena.deinit();
            return .unsupported;
        },
        error.OutOfMemory => return error.OutOfMemory,
    };
    try emitter.program.append(arena.allocator(), .match);
    return .{ .regex = .{ .arena = arena, .program = emitter.program.items } };
}

const Failure = error{ Invalid, Unsupported, OutOfMemory };

const posix_classes = [_][]const u8{ "alnum", "alpha", "ascii", "blank", "cntrl", "digit", "graph", "lower", "print", "punct", "space", "upper", "word", "xdigit" };

const Parser = struct {
    arena: std.mem.Allocator,
    source: []const u8,
    at: usize = 0,
    depth: u32 = 0,
    soft: bool = false,

    fn peek(self: *Parser) ?u8 {
        return if (self.at < self.source.len) self.source[self.at] else null;
    }

    fn standDown(self: *Parser) Node {
        self.soft = true;
        return .empty;
    }

    fn skipRune(self: *Parser) Failure!void {
        const length = std.unicode.utf8ByteSequenceLength(self.source[self.at]) catch return error.Invalid;
        if (self.at + length > self.source.len) return error.Invalid;
        self.at += length;
    }

    fn alternation(self: *Parser) Failure!Node {
        var branches = std.ArrayList(Node).empty;
        try branches.append(self.arena, try self.concatenation());
        while (self.peek() == '|') {
            self.at += 1;
            try branches.append(self.arena, try self.concatenation());
        }
        return if (branches.items.len == 1) branches.items[0] else .{ .alt = branches.items };
    }

    fn concatenation(self: *Parser) Failure!Node {
        var parts = std.ArrayList(Node).empty;
        while (self.peek()) |c| {
            if (c == '|' or c == ')') break;
            const piece = try self.atom();
            try parts.append(self.arena, try self.quantified(piece));
        }
        return switch (parts.items.len) {
            0 => .empty,
            1 => parts.items[0],
            else => .{ .concat = parts.items },
        };
    }

    fn quantified(self: *Parser, piece: Node) Failure!Node {
        const c = self.peek() orelse return piece;
        var min: u32 = 0;
        var max: ?u32 = null;
        switch (c) {
            '*' => self.at += 1,
            '+' => {
                self.at += 1;
                min = 1;
            },
            '?' => {
                self.at += 1;
                max = 1;
            },
            '{' => {
                const bounds = try self.braces() orelse return piece;
                min = bounds.min;
                max = bounds.max;
            },
            else => return piece,
        }
        if (self.peek() == '?') self.at += 1;
        if (self.peek()) |after| if (after == '*' or after == '+' or after == '?') return error.Invalid;
        if (piece == .begin or piece == .end) return self.standDown();
        const held = try self.arena.create(Node);
        held.* = piece;
        return .{ .repeat = .{ .node = held, .min = min, .max = max } };
    }

    fn braces(self: *Parser) Failure!?struct { min: u32, max: ?u32 } {
        var at = self.at + 1;
        const min = number(self.source, &at) orelse return null;
        var max: ?u32 = min;
        if (at < self.source.len and self.source[at] == ',') {
            at += 1;
            max = if (at < self.source.len and self.source[at] == '}') null else number(self.source, &at) orelse return null;
        }
        if (at >= self.source.len or self.source[at] != '}') return null;
        if (min > max_repeat or (max != null and (max.? > max_repeat or max.? < min))) return error.Invalid;
        self.at = at + 1;
        return .{ .min = min, .max = max };
    }

    fn number(source: []const u8, at: *usize) ?u32 {
        const start = at.*;
        var value: u32 = 0;
        while (at.* < source.len and std.ascii.isDigit(source[at.*])) : (at.* += 1) {
            value = @min(value * 10 + (source[at.*] - '0'), max_repeat + 1);
        }
        if (at.* == start) return null;
        if (at.* - start >= 2 and source[start] == '0') return null;
        return value;
    }

    fn atom(self: *Parser) Failure!Node {
        const c = self.source[self.at];
        if (c >= 0x80) {
            try self.skipRune();
            return self.standDown();
        }
        self.at += 1;
        switch (c) {
            '*', '+', '?' => return error.Invalid,
            '(' => return self.group(),
            '[' => return if (try self.class()) |set| .{ .set = set } else self.standDown(),
            '.' => {
                var set = Set.full;
                set.unset('\n');
                return .{ .set = set };
            },
            '^' => return .begin,
            '$' => return .end,
            '\\' => return if (try self.escape(false)) |set| .{ .set = set } else self.standDown(),
            else => return .{ .set = single(c) },
        }
    }

    fn group(self: *Parser) Failure!Node {
        if (self.peek() == '?') {
            const rest = self.source[self.at..];
            if (std.mem.startsWith(u8, rest, "?:")) {
                self.at += 2;
            } else if (std.mem.startsWith(u8, rest, "?P<") or std.mem.startsWith(u8, rest, "?<")) {
                const open = self.at + @as(usize, if (rest[1] == 'P') 3 else 2);
                if (rest[1] == '<' and rest.len > 2 and (rest[2] == '=' or rest[2] == '!')) return error.Invalid;
                const close = std.mem.indexOfScalarPos(u8, self.source, open, '>') orelse return error.Invalid;
                if (close == open) return error.Invalid;
                for (self.source[open..close]) |name| if (!std.ascii.isAlphanumeric(name) and name != '_') return error.Invalid;
                self.at = close + 1;
            } else {
                self.at += 1;
                var letters: usize = 0;
                var negated = false;
                while (self.peek()) |flag| : (self.at += 1) switch (flag) {
                    'i', 'm', 's', 'U' => letters += 1,
                    '-' => {
                        if (negated) return error.Invalid;
                        negated = true;
                    },
                    else => break,
                };
                const end = self.peek() orelse return error.Invalid;
                if (letters == 0 or self.source[self.at - 1] == '-' or (end != ')' and end != ':')) return error.Invalid;
                self.at += 1;
                self.soft = true;
                if (end == ')') return .empty;
            }
        }
        self.depth += 1;
        if (self.depth > 1000) return error.Unsupported;
        const inner = try self.alternation();
        if (self.peek() != ')') return error.Invalid;
        self.at += 1;
        self.depth -= 1;
        return inner;
    }

    fn class(self: *Parser) Failure!?Set {
        var set = Set.empty;
        var known = true;
        const negated = self.peek() == '^';
        if (negated) self.at += 1;
        var first = true;
        while (true) {
            const c = self.peek() orelse return error.Invalid;
            if (c == ']' and !first) break;
            first = false;
            if (c >= 0x80) {
                try self.skipRune();
                known = false;
                continue;
            }
            if (c == '[' and std.mem.startsWith(u8, self.source[self.at..], "[:")) {
                if (std.mem.indexOfPos(u8, self.source, self.at + 2, ":]")) |close| {
                    var name = self.source[self.at + 2 .. close];
                    if (name.len > 0 and name[0] == '^') name = name[1..];
                    var named = false;
                    for (posix_classes) |posix| named = named or std.mem.eql(u8, posix, name);
                    if (!named) return error.Invalid;
                    self.at = close + 2;
                    known = false;
                    continue;
                }
            }
            self.at += 1;
            var low: u8 = c;
            if (c == '\\') {
                const escaped = try self.escape(true) orelse {
                    known = false;
                    continue;
                };
                if (escaped.count() != 1) {
                    set.setUnion(escaped);
                    continue;
                }
                low = @intCast(escaped.findFirstSet().?);
            }
            if (self.peek() == '-' and self.at + 1 < self.source.len and self.source[self.at + 1] != ']') {
                self.at += 1;
                var high = self.source[self.at];
                if (high >= 0x80) {
                    try self.skipRune();
                    known = false;
                    continue;
                }
                self.at += 1;
                if (high == '\\') {
                    const escaped = try self.escape(true) orelse {
                        known = false;
                        continue;
                    };
                    if (escaped.count() != 1) return error.Invalid;
                    high = @intCast(escaped.findFirstSet().?);
                }
                if (high < low) return error.Invalid;
                set.setRangeValue(.{ .start = low, .end = @as(usize, high) + 1 }, true);
            } else set.set(low);
        }
        self.at += 1;
        if (!known) return null;
        if (negated) set.toggleAll();
        return set;
    }

    fn escape(self: *Parser, in_class: bool) Failure!?Set {
        const c = self.peek() orelse return error.Invalid;
        self.at += 1;
        return switch (c) {
            'd' => digits(),
            'D' => inverse(digits()),
            'w' => word(),
            'W' => inverse(word()),
            's' => space(),
            'S' => inverse(space()),
            'a' => single(0x07),
            'n' => single('\n'),
            't' => single('\t'),
            'r' => single('\r'),
            'f' => single(0x0c),
            'v' => single(0x0b),
            'b', 'B', 'A', 'z' => if (in_class) error.Invalid else null,
            'p', 'P' => {
                if (self.peek() == '{') {
                    const close = std.mem.indexOfScalarPos(u8, self.source, self.at, '}') orelse return error.Invalid;
                    if (close == self.at + 1) return error.Invalid;
                    self.at = close + 1;
                } else {
                    if (self.peek() == null) return error.Invalid;
                    try self.skipRune();
                }
                return null;
            },
            'Q' => {
                if (in_class) return error.Invalid;
                self.at = if (std.mem.indexOfPos(u8, self.source, self.at, "\\E")) |close| close + 2 else self.source.len;
                return null;
            },
            'x' => {
                var value: u32 = 0;
                if (self.peek() == '{') {
                    const close = std.mem.indexOfScalarPos(u8, self.source, self.at, '}') orelse return error.Invalid;
                    const digits_text = self.source[self.at + 1 .. close];
                    if (digits_text.len == 0 or digits_text.len > 8) return error.Invalid;
                    value = std.fmt.parseUnsigned(u32, digits_text, 16) catch return error.Invalid;
                    if (value > 0x10FFFF) return error.Invalid;
                    self.at = close + 1;
                } else {
                    if (self.at + 2 > self.source.len) return error.Invalid;
                    value = std.fmt.parseUnsigned(u32, self.source[self.at .. self.at + 2], 16) catch return error.Invalid;
                    self.at += 2;
                }
                return if (value < 0x80) single(@intCast(value)) else null;
            },
            '0'...'7' => {
                if (c != '0' and !(self.peek() != null and self.peek().? >= '0' and self.peek().? <= '7')) return error.Invalid;
                var value: u32 = c - '0';
                var taken: usize = 1;
                while (taken < 3) : (taken += 1) {
                    const next = self.peek() orelse break;
                    if (next < '0' or next > '7') break;
                    value = value * 8 + (next - '0');
                    self.at += 1;
                }
                return if (value < 0x80) single(@intCast(value)) else null;
            },
            else => if (std.ascii.isAlphanumeric(c) or c >= 0x80) error.Invalid else single(c),
        };
    }
};

fn single(c: u8) Set {
    var set = Set.empty;
    set.set(c);
    return set;
}

fn inverse(set: Set) Set {
    var flipped = set;
    flipped.toggleAll();
    return flipped;
}

fn digits() Set {
    var set = Set.empty;
    set.setRangeValue(.{ .start = '0', .end = '9' + 1 }, true);
    return set;
}

fn word() Set {
    var set = digits();
    set.setRangeValue(.{ .start = 'a', .end = 'z' + 1 }, true);
    set.setRangeValue(.{ .start = 'A', .end = 'Z' + 1 }, true);
    set.set('_');
    return set;
}

fn space() Set {
    var set = Set.empty;
    for ("\t\n\x0c\r ") |c| set.set(c);
    return set;
}

const Emitter = struct {
    arena: std.mem.Allocator,
    program: std.ArrayList(Inst) = .empty,

    fn here(self: *Emitter) u32 {
        return @intCast(self.program.items.len);
    }

    fn push(self: *Emitter, inst: Inst) error{ Unsupported, OutOfMemory }!u32 {
        if (self.program.items.len >= max_program) return error.Unsupported;
        try self.program.append(self.arena, inst);
        return self.here() - 1;
    }

    fn emit(self: *Emitter, node: Node) error{ Unsupported, OutOfMemory }!void {
        switch (node) {
            .empty => {},
            .set => |set| _ = try self.push(.{ .set = set }),
            .begin => _ = try self.push(.begin),
            .end => _ = try self.push(.end),
            .concat => |parts| for (parts) |part| try self.emit(part),
            .alt => |branches| {
                var exits = std.ArrayList(u32).empty;
                for (branches[0 .. branches.len - 1]) |branch| {
                    const split = try self.push(.{ .split = .{ 0, 0 } });
                    self.program.items[split].split[0] = self.here();
                    try self.emit(branch);
                    try exits.append(self.arena, try self.push(.{ .jmp = 0 }));
                    self.program.items[split].split[1] = self.here();
                }
                try self.emit(branches[branches.len - 1]);
                for (exits.items) |exit| self.program.items[exit].jmp = self.here();
            },
            .repeat => |repeat| {
                var count: u32 = 0;
                while (count < repeat.min) : (count += 1) try self.emit(repeat.node.*);
                if (repeat.max) |max| {
                    var exits = std.ArrayList(u32).empty;
                    while (count < max) : (count += 1) {
                        try exits.append(self.arena, try self.push(.{ .split = .{ self.here() + 1, 0 } }));
                        try self.emit(repeat.node.*);
                    }
                    for (exits.items) |exit| self.program.items[exit].split[1] = self.here();
                } else {
                    const loop = try self.push(.{ .split = .{ self.here() + 1, 0 } });
                    try self.emit(repeat.node.*);
                    _ = try self.push(.{ .jmp = loop });
                    self.program.items[loop].split[1] = self.here();
                }
            },
        }
    }
};

fn expectMatch(pattern: []const u8, text: []const u8, want: bool) !void {
    var compiled = try compile(std.testing.allocator, pattern);
    switch (compiled) {
        .regex => |*regex| {
            defer regex.deinit();
            try std.testing.expectEqual(@as(?bool, want), try regex.fullMatch(std.testing.allocator, text));
        },
        else => return error.TestUnexpectedResult,
    }
}

test "a pattern matches only the whole name, as Go's anchored RE2 does" {
    try expectMatch("[a-z]+", "echo", true);
    try expectMatch("[a-z]+", "echo-1", false);
    try expectMatch("^[a-z][a-z0-9_]*$", "look_up2", true);
    try expectMatch("^[a-zA-Z0-9_-]{1,64}$", "look up", false);
    try expectMatch("ab|cd", "cd", true);
    try expectMatch("a(?:b|c){2,3}d", "abcbd", true);
    try expectMatch("a(?:b|c){2,3}d", "abd", false);
    try expectMatch("(?P<head>[a-z])\\w*", "x_9", true);
    try expectMatch("[^0-9]\\d?", "x7", true);
    try expectMatch("a.c", "a\nc", false);
}

test "a pattern Go's RE2 rejects is invalid, and valid syntax this matcher does not implement stands down" {
    for ([_][]const u8{ "[a-z", "(ab", "ab)", "*a", "a**", "[z-a]", "\\q", "(?P<>x)", "a{2,1}", "x{1001}", "x{12345}", "(?i)[a-", "(?=a)", "(?<!a)b", "[[:foo:]]", "\\1", "\\8", "\\C", "[\\b]", "(?i-)a", "(?)a" }) |pattern| {
        try std.testing.expectEqual(Compiled.invalid, std.meta.activeTag(try compile(std.testing.allocator, pattern)));
    }
    for ([_][]const u8{ "(?i)abc", "(?i:ab)c", "\\pL+", "\\p{Greek}", "[[:alpha:]]", "\\bword", "^*a", "caf\xc3\xa9", "\\x{100}", "\\Qa.b\\E" }) |pattern| {
        try std.testing.expectEqual(Compiled.unsupported, std.meta.activeTag(try compile(std.testing.allocator, pattern)));
    }
}

test "a brace that is not a repeat in Go's syntax is a literal" {
    try expectMatch("a{01}", "a{01}", true);
    try expectMatch("a{,3}", "a{,3}", true);
    try expectMatch("a{2,}", "aaa", true);
    try expectMatch("\\101\\x42", "AB", true);
}

test "a name outside ASCII is not judged" {
    var compiled = try compile(std.testing.allocator, ".+");
    defer compiled.regex.deinit();
    try std.testing.expectEqual(@as(?bool, null), try compiled.regex.fullMatch(std.testing.allocator, "caf\xc3\xa9"));
}
