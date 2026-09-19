const std = @import("std");

pub const code_credential_in_trace = "credential_in_trace";
pub const code_credential_in_headers = "credential_in_headers";
pub const code_terminal_not_assembly = "terminal_not_assembly";
pub const code_sequence_gap = "sequence_gap";
pub const code_sequence_regression = "sequence_regression";
pub const code_event_after_terminal = "event_after_terminal";

pub const implemented = [_][]const u8{
    code_credential_in_trace,
    code_credential_in_headers,
    code_terminal_not_assembly,
    code_sequence_gap,
    code_sequence_regression,
    code_event_after_terminal,
};

pub fn isImplemented(code: []const u8) bool {
    for (implemented) |name| {
        if (std.mem.eql(u8, name, code)) return true;
    }
    return false;
}

pub const Diagnostic = struct {
    code: []const u8,
    index: usize,
};

const credential_header_names = [_][]const u8{
    "authorization",
    "proxy-authorization",
    "x-api-key",
    "api-key",
};

fn namesACredential(name: []const u8) bool {
    var lowered: [32]u8 = undefined;
    if (name.len > lowered.len) return false;
    for (name, 0..) |character, at| lowered[at] = std.ascii.toLower(character);
    for (credential_header_names) |known| {
        if (std.mem.eql(u8, known, lowered[0..name.len])) return true;
    }
    return false;
}

fn bearerShaped(value: std.json.Value) bool {
    if (value != .string) return false;
    const text = std.mem.trimStart(u8, value.string, " \t");
    if (text.len < "bearer ".len) return false;
    return std.ascii.eqlIgnoreCase(text[0.."bearer ".len], "bearer ");
}

fn scopedEvent(declared: []const u8) bool {
    const scoped = [_][]const u8{
        "inference.started",    "inference.part.started", "inference.part.delta",
        "inference.part.ended", "inference.completed",    "inference.failed",
    };
    for (scoped) |name| {
        if (std.mem.eql(u8, name, declared)) return true;
    }
    return false;
}

fn field(envelope: std.json.Value, name: []const u8) []const u8 {
    if (envelope != .object) return "";
    const value = envelope.object.get(name) orelse return "";
    if (value != .string) return "";
    return value.string;
}

fn member(container: std.json.Value, name: []const u8) ?std.json.Value {
    if (container != .object) return null;
    return container.object.get(name);
}

fn memberString(container: std.json.Value, name: []const u8) []const u8 {
    const value = member(container, name) orelse return "";
    if (value != .string) return "";
    return value.string;
}

fn sequenceOf(envelope: std.json.Value) i64 {
    const value = member(envelope, "sequence") orelse return 0;
    return switch (value) {
        .integer => |n| n,
        else => 0,
    };
}

const Ended = struct {
    part_index: i64,
    part_kind: []const u8,
    text: []const u8,
    carry: []const u8,
    tool_call: ?std.json.Value,
};

const Terminal = struct {
    index: usize,
    inference: []const u8,
    content: ?std.json.Value,
};

pub const Machine = struct {
    allocator: std.mem.Allocator,
    diagnostics: std.ArrayList(Diagnostic) = .empty,
    parts: std.StringArrayHashMapUnmanaged(std.ArrayList(Ended)) = .empty,
    settled: std.StringArrayHashMapUnmanaged(bool) = .empty,
    sequences: std.StringArrayHashMapUnmanaged(i64) = .empty,
    terminals: std.ArrayList(Terminal) = .empty,

    pub fn init(allocator: std.mem.Allocator) Machine {
        return .{ .allocator = allocator };
    }

    pub fn deinit(self: *Machine) void {
        self.diagnostics.deinit(self.allocator);
        for (self.parts.values()) |*list| list.deinit(self.allocator);
        self.parts.deinit(self.allocator);
        self.settled.deinit(self.allocator);
        self.sequences.deinit(self.allocator);
        self.terminals.deinit(self.allocator);
    }

    fn add(self: *Machine, code: []const u8, index: usize) !void {
        try self.diagnostics.append(self.allocator, .{ .code = code, .index = index });
    }

    pub fn apply(self: *Machine, index: usize, envelope: std.json.Value) !void {
        const declared = field(envelope, "type");
        const payload = member(envelope, "payload") orelse std.json.Value{ .null = {} };

        if (std.mem.eql(u8, declared, "provider.credential.grant.request")) {
            if (member(payload, "value") != null) {
                try self.add(code_credential_in_trace, index);
                return;
            }
        }
        try self.credentialHeaders(index, declared, payload);

        const inference = field(envelope, "inference_id");
        if (inference.len != 0 and scopedEvent(declared)) {
            const sequence = sequenceOf(envelope);
            if (self.sequences.get(inference)) |last| {
                if (sequence <= last) {
                    try self.add(code_sequence_regression, index);
                } else if (sequence != last + 1) {
                    try self.add(code_sequence_gap, index);
                }
                if (sequence > last) try self.sequences.put(self.allocator, inference, sequence);
            } else {
                if (sequence != 1) try self.add(code_sequence_gap, index);
                try self.sequences.put(self.allocator, inference, sequence);
            }
            if (self.settled.get(inference) != null) try self.add(code_event_after_terminal, index);
        }

        if (std.mem.eql(u8, declared, "inference.part.ended")) {
            const entry = self.parts.getPtr(inference) orelse blk: {
                try self.parts.put(self.allocator, inference, .empty);
                break :blk self.parts.getPtr(inference).?;
            };
            try entry.append(self.allocator, .{
                .part_index = if (member(payload, "part_index")) |v| (if (v == .integer) v.integer else 0) else 0,
                .part_kind = memberString(payload, "part_kind"),
                .text = memberString(payload, "text"),
                .carry = memberString(payload, "carry"),
                .tool_call = member(payload, "tool_call"),
            });
            return;
        }
        if (std.mem.eql(u8, declared, "inference.completed")) {
            const message = member(payload, "message") orelse std.json.Value{ .null = {} };
            try self.terminals.append(self.allocator, .{
                .index = index,
                .inference = inference,
                .content = member(message, "content"),
            });
            try self.settled.put(self.allocator, inference, true);
            return;
        }
        if (std.mem.eql(u8, declared, "inference.failed")) {
            try self.settled.put(self.allocator, inference, true);
        }
    }

    fn credentialHeaders(self: *Machine, index: usize, declared: []const u8, payload: std.json.Value) !void {
        if (std.mem.eql(u8, declared, "inference.create.request")) {
            try self.scanHeaders(index, member(payload, "headers"));
            return;
        }
        if (!std.mem.eql(u8, declared, "provider.describe.response")) return;
        const descriptors = member(payload, "providers") orelse return;
        if (descriptors != .array) return;
        for (descriptors.array.items) |descriptor| {
            try self.scanHeaders(index, member(descriptor, "headers"));
        }
    }

    fn scanHeaders(self: *Machine, index: usize, declared: ?std.json.Value) !void {
        const headers = declared orelse return;
        if (headers != .object) return;
        var found: usize = 0;
        var it = headers.object.iterator();
        while (it.next()) |entry| {
            if (namesACredential(entry.key_ptr.*) or bearerShaped(entry.value_ptr.*)) found += 1;
        }
        var raised: usize = 0;
        while (raised < found) : (raised += 1) try self.add(code_credential_in_headers, index);
    }

    pub fn close(self: *Machine) !void {
        for (self.terminals.items) |terminal| {
            const list = self.parts.get(terminal.inference) orelse continue;
            if (list.items.len == 0) continue;
            if (try self.assemblyDefect(list.items, terminal.content)) {
                try self.add(code_terminal_not_assembly, terminal.index);
            }
        }
    }

    fn assemblyDefect(self: *Machine, ended: []Ended, content: ?std.json.Value) !bool {
        const assembled = content orelse return true;
        if (assembled != .array) return true;

        const ordered = try self.allocator.alloc(Ended, ended.len);
        defer self.allocator.free(ordered);
        @memcpy(ordered, ended);
        std.mem.sort(Ended, ordered, {}, byPartIndex);

        if (assembled.array.items.len != ordered.len) return true;
        for (ordered, assembled.array.items) |part, carried| {
            if (partMismatch(part, carried)) return true;
        }
        return false;
    }
};

fn byPartIndex(_: void, a: Ended, b: Ended) bool {
    return a.part_index < b.part_index;
}

fn partMismatch(ended: Ended, carried: std.json.Value) bool {
    if (!std.mem.eql(u8, memberString(carried, "type"), ended.part_kind)) return true;
    if (!std.mem.eql(u8, memberString(carried, "carry"), ended.carry)) return true;
    if (std.mem.eql(u8, ended.part_kind, "text") or std.mem.eql(u8, ended.part_kind, "reasoning")) {
        return !std.mem.eql(u8, memberString(carried, ended.part_kind), ended.text);
    }
    if (std.mem.eql(u8, ended.part_kind, "tool_call")) {
        const call = ended.tool_call orelse return false;
        if (!std.mem.eql(u8, memberString(carried, "tool_call_id"), memberString(call, "tool_call_id"))) return true;
        if (!std.mem.eql(u8, memberString(carried, "name"), memberString(call, "name"))) return true;
        return !sameJson(member(carried, "arguments_json"), member(call, "arguments_json"));
    }
    return false;
}

fn sameJson(a: ?std.json.Value, b: ?std.json.Value) bool {
    if (a == null and b == null) return true;
    const left = a orelse return false;
    const right = b orelse return false;
    return valueEql(left, right);
}

fn valueEql(a: std.json.Value, b: std.json.Value) bool {
    return switch (a) {
        .null => b == .null,
        .bool => |x| b == .bool and b.bool == x,
        .integer => |x| b == .integer and b.integer == x,
        .float => |x| b == .float and b.float == x,
        .number_string => |x| b == .number_string and std.mem.eql(u8, b.number_string, x),
        .string => |x| b == .string and std.mem.eql(u8, b.string, x),
        .array => |x| blk: {
            if (b != .array or b.array.items.len != x.items.len) break :blk false;
            for (x.items, b.array.items) |left, right| {
                if (!valueEql(left, right)) break :blk false;
            }
            break :blk true;
        },
        .object => |x| blk: {
            if (b != .object or b.object.count() != x.count()) break :blk false;
            var it = x.iterator();
            while (it.next()) |entry| {
                const other = b.object.get(entry.key_ptr.*) orelse break :blk false;
                if (!valueEql(entry.value_ptr.*, other)) break :blk false;
            }
            break :blk true;
        },
    };
}

fn expectCodes(trace: []const u8, want: []const []const u8) !void {
    const allocator = std.testing.allocator;
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, trace, .{});
    defer parsed.deinit();

    var machine = Machine.init(allocator);
    defer machine.deinit();
    for (parsed.value.array.items, 0..) |envelope, index| try machine.apply(index, envelope);
    try machine.close();

    try std.testing.expectEqual(want.len, machine.diagnostics.items.len);
    for (want, machine.diagnostics.items) |expected, actual| {
        try std.testing.expectEqualStrings(expected, actual.code);
    }
}

test "a terminal that is the assembly of the parts that ended raises nothing" {
    try expectCodes(
        \\[{"type":"inference.started","id":"a","inference_id":"i","sequence":1,"payload":{}},
        \\{"type":"inference.part.ended","id":"b","inference_id":"i","sequence":2,"payload":{"part_index":0,"part_kind":"text","text":"hello"}},
        \\{"type":"inference.part.ended","id":"c","inference_id":"i","sequence":3,"payload":{"part_index":1,"part_kind":"text","text":"world"}},
        \\{"type":"inference.completed","id":"d","inference_id":"i","sequence":4,"payload":{"message":{"content":[
        \\{"type":"text","text":"hello"},{"type":"text","text":"world"}]}}}]
    , &.{});
}

test "the terminal is judged against the parts in index order, not arrival order" {
    try expectCodes(
        \\[{"type":"inference.started","id":"a","inference_id":"i","sequence":1,"payload":{}},
        \\{"type":"inference.part.ended","id":"b","inference_id":"i","sequence":2,"payload":{"part_index":1,"part_kind":"text","text":"world"}},
        \\{"type":"inference.part.ended","id":"c","inference_id":"i","sequence":3,"payload":{"part_index":0,"part_kind":"text","text":"hello"}},
        \\{"type":"inference.completed","id":"d","inference_id":"i","sequence":4,"payload":{"message":{"content":[
        \\{"type":"text","text":"hello"},{"type":"text","text":"world"}]}}}]
    , &.{});

    try expectCodes(
        \\[{"type":"inference.started","id":"a","inference_id":"i","sequence":1,"payload":{}},
        \\{"type":"inference.part.ended","id":"b","inference_id":"i","sequence":2,"payload":{"part_index":1,"part_kind":"text","text":"world"}},
        \\{"type":"inference.part.ended","id":"c","inference_id":"i","sequence":3,"payload":{"part_index":0,"part_kind":"text","text":"hello"}},
        \\{"type":"inference.completed","id":"d","inference_id":"i","sequence":4,"payload":{"message":{"content":[
        \\{"type":"text","text":"world"},{"type":"text","text":"hello"}]}}}]
    , &.{"terminal_not_assembly"});
}

test "a dropped part, a changed kind and a dropped carry are each not the assembly" {
    try expectCodes(
        \\[{"type":"inference.part.ended","id":"b","inference_id":"i","sequence":1,"payload":{"part_index":0,"part_kind":"text","text":"hello"}},
        \\{"type":"inference.part.ended","id":"c","inference_id":"i","sequence":2,"payload":{"part_index":1,"part_kind":"text","text":"world"}},
        \\{"type":"inference.completed","id":"d","inference_id":"i","sequence":3,"payload":{"message":{"content":[{"type":"text","text":"hello"}]}}}]
    , &.{"terminal_not_assembly"});

    try expectCodes(
        \\[{"type":"inference.part.ended","id":"b","inference_id":"i","sequence":1,"payload":{"part_index":0,"part_kind":"reasoning","text":"hello"}},
        \\{"type":"inference.completed","id":"d","inference_id":"i","sequence":2,"payload":{"message":{"content":[{"type":"text","text":"hello"}]}}}]
    , &.{"terminal_not_assembly"});

    try expectCodes(
        \\[{"type":"inference.part.ended","id":"b","inference_id":"i","sequence":1,"payload":{"part_index":0,"part_kind":"text","text":"hello","carry":"opaque"}},
        \\{"type":"inference.completed","id":"d","inference_id":"i","sequence":2,"payload":{"message":{"content":[{"type":"text","text":"hello"}]}}}]
    , &.{"terminal_not_assembly"});
}

test "a credential never travels on the wire, named or bearer shaped" {
    try expectCodes(
        \\[{"type":"provider.credential.grant.request","id":"a","payload":{"nonce":"n","value":"sk-live"}}]
    , &.{"credential_in_trace"});

    try expectCodes(
        \\[{"type":"provider.credential.grant.request","id":"a","payload":{"nonce":"n"}}]
    , &.{});

    try expectCodes(
        \\[{"type":"inference.create.request","id":"a","payload":{"headers":{"X-Api-Key":"k"}}}]
    , &.{"credential_in_headers"});

    try expectCodes(
        \\[{"type":"inference.create.request","id":"a","payload":{"headers":{"x-tenant-token":"Bearer abc"}}}]
    , &.{"credential_in_headers"});

    try expectCodes(
        \\[{"type":"inference.create.request","id":"a","payload":{"headers":{"x-tenant":"acme"}}}]
    , &.{});

    try expectCodes(
        \\[{"type":"provider.describe.response","id":"a","payload":{"providers":[
        \\{"headers":{"x-region":"eu"}},{"headers":{"Authorization":"x"}}]}}]
    , &.{"credential_in_headers"});
}

test "an inference sequence opens at one, advances by one, and closes at the terminal" {
    try expectCodes(
        \\[{"type":"inference.started","id":"a","inference_id":"i","sequence":2,"payload":{}}]
    , &.{"sequence_gap"});

    try expectCodes(
        \\[{"type":"inference.started","id":"a","inference_id":"i","sequence":1,"payload":{}},
        \\{"type":"inference.part.delta","id":"b","inference_id":"i","sequence":3,"payload":{}}]
    , &.{"sequence_gap"});

    try expectCodes(
        \\[{"type":"inference.started","id":"a","inference_id":"i","sequence":1,"payload":{}},
        \\{"type":"inference.part.delta","id":"b","inference_id":"i","sequence":1,"payload":{}}]
    , &.{"sequence_regression"});

    try expectCodes(
        \\[{"type":"inference.failed","id":"a","inference_id":"i","sequence":1,"payload":{}},
        \\{"type":"inference.part.delta","id":"b","inference_id":"i","sequence":2,"payload":{}}]
    , &.{"event_after_terminal"});
}
