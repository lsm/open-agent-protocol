const std = @import("std");

pub const Error = std.mem.Allocator.Error || std.Io.Writer.Error;

const Frame = union(enum) {
    array: struct { items: []const std.json.Value, next: usize },
    object: struct { keys: []const []const u8, values: []const std.json.Value, next: usize },
};

pub fn write(allocator: std.mem.Allocator, value: std.json.Value, writer: *std.Io.Writer) Error!void {
    var stack: std.ArrayList(Frame) = .empty;
    defer stack.deinit(allocator);
    var pending: ?std.json.Value = value;
    while (true) {
        if (pending) |item| {
            pending = null;
            switch (item) {
                .array => |array| {
                    try stack.append(allocator, .{ .array = .{ .items = array.items, .next = 0 } });
                    try writer.writeByte('[');
                },
                .object => |object| {
                    try stack.append(allocator, .{ .object = .{ .keys = object.keys(), .values = object.values(), .next = 0 } });
                    try writer.writeByte('{');
                },
                else => try writeScalar(item, writer),
            }
        }
        if (stack.items.len == 0) return;
        switch (stack.items[stack.items.len - 1]) {
            .array => |*array| {
                if (array.next == array.items.len) {
                    _ = stack.pop();
                    try writer.writeByte(']');
                    continue;
                }
                if (array.next > 0) try writer.writeByte(',');
                pending = array.items[array.next];
                array.next += 1;
            },
            .object => |*object| {
                if (object.next == object.keys.len) {
                    _ = stack.pop();
                    try writer.writeByte('}');
                    continue;
                }
                if (object.next > 0) try writer.writeByte(',');
                try std.json.Stringify.encodeJsonString(object.keys[object.next], .{}, writer);
                try writer.writeByte(':');
                pending = object.values[object.next];
                object.next += 1;
            },
        }
    }
}

fn writeScalar(value: std.json.Value, writer: *std.Io.Writer) std.Io.Writer.Error!void {
    switch (value) {
        .null => try writer.writeAll("null"),
        .bool => |inner| try writer.writeAll(if (inner) "true" else "false"),
        .integer => |inner| try writer.print("{}", .{inner}),
        .float => |inner| try writer.print("{}", .{inner}),
        .number_string => |inner| try writer.writeAll(inner),
        .string => |inner| try std.json.Stringify.encodeJsonString(inner, .{}, writer),
        .array, .object => unreachable,
    }
}

pub fn valueAlloc(allocator: std.mem.Allocator, value: std.json.Value) std.mem.Allocator.Error![]u8 {
    var out: std.Io.Writer.Allocating = .init(allocator);
    defer out.deinit();
    write(allocator, value, &out.writer) catch |err| switch (err) {
        error.WriteFailed, error.OutOfMemory => return error.OutOfMemory,
    };
    return out.toOwnedSlice();
}

const testing = std.testing;

fn nested(arena: std.mem.Allocator, depth: usize, leaf: std.json.Value) !std.json.Value {
    var value = leaf;
    for (0..depth) |index| {
        if (index % 2 == 0) {
            var array = std.json.Array.init(arena);
            try array.append(value);
            value = .{ .array = array };
        } else {
            var object: std.json.ObjectMap = .empty;
            try object.put(arena, "k", value);
            value = .{ .object = object };
        }
    }
    return value;
}

test "the encoding is byte-identical to std.json.Stringify's minified output" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const documents = [_][]const u8{
        "null",
        "true",
        "false",
        "0",
        "-9223372036854775808",
        "18446744073709551616",
        "1.5",
        "-0.0",
        "1e300",
        "\"\"",
        "\"quote\\\" slash\\\\ solidus/ \\b\\f\\n\\r\\t \\u0001 \\u001f \\u007f\"",
        "\"caf\\u00e9 \\u2028 \\ud83d\\ude00 raw\"",
        "[]",
        "{}",
        "[[],{},[[]],{\"a\":{}}]",
        "{\"z\":1,\"a\":[1,2.5,\"x\",null,true],\"m\":{\"n\\n\":{\"o\":[{}]}}}",
        "[1,[2,[3,[4,{\"five\":[6]}]]],7]",
    };
    for (documents) |document| {
        const value = try std.json.parseFromSliceLeaky(std.json.Value, arena.allocator(), document, .{});
        const want = try std.json.Stringify.valueAlloc(testing.allocator, value, .{});
        defer testing.allocator.free(want);
        const got = try valueAlloc(testing.allocator, value);
        defer testing.allocator.free(got);
        try testing.expectEqualStrings(want, got);
    }
}

test "a value nested past std.json.Stringify's 256 levels encodes in full" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const depth = 10000;
    const value = try nested(arena.allocator(), depth, .{ .integer = 7 });
    const got = try valueAlloc(testing.allocator, value);
    defer testing.allocator.free(got);

    const reparsed = try std.json.parseFromSliceLeaky(std.json.Value, arena.allocator(), got, .{});
    var cursor = reparsed;
    for (0..depth) |index| {
        const level = depth - 1 - index;
        if (level % 2 == 0) {
            try testing.expectEqual(@as(usize, 1), cursor.array.items.len);
            cursor = cursor.array.items[0];
        } else {
            try testing.expectEqual(@as(usize, 1), cursor.object.count());
            cursor = cursor.object.get("k").?;
        }
    }
    try testing.expectEqual(@as(i64, 7), cursor.integer);
}

fn encodeUnderFailure(allocator: std.mem.Allocator, value: std.json.Value) !void {
    const got = try valueAlloc(allocator, value);
    allocator.free(got);
}

test "every allocation failure while encoding is reported and leaks nothing" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const value = try std.json.parseFromSliceLeaky(std.json.Value, arena.allocator(),
        \\{"a":[1,{"b":[2,3,{"c":"long enough to grow the buffer more than once over"}]}],"d":[[[[[]]]]]}
    , .{});
    try testing.checkAllAllocationFailures(testing.allocator, encodeUnderFailure, .{value});
}
