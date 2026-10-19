const std = @import("std");

pub const code_malformed_json = "malformed_json";
pub const code_duplicate_key = "duplicate_key";

pub const Item = struct {
    raw: []const u8,
    value: std.json.Value,
    line: usize,
};

pub const Malformed = struct {
    index: usize,
    line: usize,
};

pub const Trace = union(enum) {
    items: []const Item,
    malformed: Malformed,
};

pub fn traceItems(arena: std.mem.Allocator, source: []const u8) !Trace {
    const text = std.mem.trim(u8, source, " \t\r\n");
    if (text.len == 0) return .{ .items = &.{} };
    if (text[0] == '[') {
        const document = try parseValue(arena, text) orelse return .{ .malformed = .{ .index = 0, .line = 0 } };
        const elements = try traceElements(arena, text);
        const items = try arena.alloc(Item, elements.len);
        for (items, elements, document.array.items) |*item, raw, value| item.* = .{ .raw = raw, .value = value, .line = 0 };
        return .{ .items = items };
    }
    if (try parseValue(arena, text)) |value| {
        const items = try arena.alloc(Item, 1);
        items[0] = .{ .raw = text, .value = value, .line = 1 };
        return .{ .items = items };
    }
    var items = std.ArrayList(Item).empty;
    var lines = std.mem.splitScalar(u8, text, '\n');
    var number: usize = 0;
    while (lines.next()) |untrimmed| {
        number += 1;
        const line = std.mem.trim(u8, untrimmed, " \t\r\n");
        if (line.len == 0) continue;
        const value = try parseValue(arena, line) orelse return .{ .malformed = .{ .index = items.items.len, .line = number } };
        try items.append(arena, .{ .raw = line, .value = value, .line = number });
    }
    return .{ .items = items.items };
}

pub fn repeatsAKey(allocator: std.mem.Allocator, element: []const u8) !bool {
    var strict = std.json.parseFromSlice(std.json.Value, allocator, element, .{}) catch |err| switch (err) {
        error.OutOfMemory => return err,
        error.DuplicateField => return true,
        else => return false,
    };
    strict.deinit();
    return false;
}

pub fn codes(arena: std.mem.Allocator, source: []const u8) ![]const []const u8 {
    var found = std.ArrayList([]const u8).empty;
    switch (try traceItems(arena, source)) {
        .malformed => try found.append(arena, code_malformed_json),
        .items => |items| for (items) |item| {
            if (try repeatsAKey(arena, item.raw)) try found.append(arena, code_duplicate_key);
        },
    }
    return found.items;
}

fn traceElements(allocator: std.mem.Allocator, source: []const u8) ![]const []const u8 {
    var scanner = std.json.Scanner.initCompleteInput(allocator, source);
    defer scanner.deinit();
    if (try scanner.next() != .array_begin) return error.TraceIsNotAnArray;
    var elements = std.ArrayList([]const u8).empty;
    errdefer elements.deinit(allocator);
    while (try scanner.peekNextTokenType() != .array_end) {
        const start = scanner.cursor;
        try scanner.skipValue();
        try elements.append(allocator, std.mem.trimStart(u8, source[start..scanner.cursor], " \t\r\n,"));
    }
    return elements.toOwnedSlice(allocator);
}

fn parseValue(arena: std.mem.Allocator, text: []const u8) !?std.json.Value {
    return std.json.parseFromSliceLeaky(std.json.Value, arena, text, .{ .duplicate_field_behavior = .use_last }) catch |err| switch (err) {
        error.OutOfMemory => return err,
        else => return null,
    };
}

const testing = std.testing;

test "a trace that does not parse is malformed once, and a repeated key is named per envelope that repeats it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const malformed = try codes(a, "[{\"type\":\"x\"},{\"type\":");
    try testing.expectEqual(@as(usize, 1), malformed.len);
    try testing.expectEqualStrings(code_malformed_json, malformed[0]);
    const repeated = try codes(a, "[{\"type\":\"x\",\"type\":\"y\"},{\"id\":\"a\"},{\"payload\":{\"k\":1,\"k\":2}}]");
    try testing.expectEqual(@as(usize, 2), repeated.len);
    try testing.expectEqualStrings(code_duplicate_key, repeated[0]);
    const lines = try codes(a, "{\"id\":\"a\"}\n{\"id\":\n");
    try testing.expectEqual(@as(usize, 1), lines.len);
    try testing.expectEqual(@as(usize, 0), (try codes(a, "[{\"id\":\"a\"},{\"id\":\"b\"}]")).len);
}
