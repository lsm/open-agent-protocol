const std = @import("std");

pub const Error = std.mem.Allocator.Error;

pub const meta_schemas = [_][]const u8{ "manifest.schema.json", "pack.schema.json" };

pub fn isMetaSchema(name: []const u8) bool {
    for (meta_schemas) |meta| {
        if (std.mem.eql(u8, meta, name)) return true;
    }
    return false;
}

pub fn document(allocator: std.mem.Allocator, root: std.json.Value) Error!std.json.Value {
    return walk(allocator, root, root, false);
}

fn walk(allocator: std.mem.Allocator, root: std.json.Value, node: std.json.Value, in_if: bool) Error!std.json.Value {
    switch (node) {
        .object => |object| return walkObject(allocator, root, object, in_if),
        .array => |items| {
            var out = std.json.Array.init(allocator);
            try out.ensureTotalCapacity(items.items.len);
            for (items.items) |item| out.appendAssumeCapacity(try walk(allocator, root, item, in_if));
            return .{ .array = out };
        },
        else => return node,
    }
}

fn walkObject(allocator: std.mem.Allocator, root: std.json.Value, object: std.json.ObjectMap, in_if: bool) Error!std.json.Value {
    var out: std.json.ObjectMap = .empty;
    var copy = object.iterator();
    while (copy.next()) |entry| try out.put(allocator, entry.key_ptr.*, entry.value_ptr.*);

    var rewrote_union = false;
    if (!in_if) {
        for ([_][]const u8{ "oneOf", "anyOf" }) |key| {
            const members = out.get(key) orelse continue;
            if (members != .array or members.array.items.len == 0) continue;
            const union_shape = try discriminated(allocator, root, members.array.items) orelse continue;
            var walked = std.json.Array.init(allocator);
            try walked.ensureTotalCapacity(members.array.items.len + 1);
            for (members.array.items) |member| walked.appendAssumeCapacity(try walk(allocator, root, member, false));
            walked.appendAssumeCapacity(try fallbackBranch(allocator, union_shape));
            _ = out.orderedRemove(key);
            try out.put(allocator, "anyOf", .{ .array = walked });
            rewrote_union = true;
        }
        if (out.get("enum")) |values| {
            if (values == .array and allStrings(values.array.items)) {
                _ = out.orderedRemove("enum");
                if (out.get("type") == null) try out.put(allocator, "type", .{ .string = "string" });
            }
        }
        if (out.get("additionalProperties")) |permitted| {
            if (permitted == .bool and permitted.bool == false) _ = out.orderedRemove("additionalProperties");
        }
    }

    var index: usize = 0;
    while (index < out.count()) : (index += 1) {
        const key = out.keys()[index];
        if (untouched(key)) continue;
        if (std.mem.eql(u8, key, "anyOf") and rewrote_union) continue;
        const nested = std.mem.eql(u8, key, "if") or in_if;
        const rewritten = try walk(allocator, root, out.values()[index], nested);
        out.values()[index] = rewritten;
    }
    return .{ .object = out };
}

fn untouched(key: []const u8) bool {
    for ([_][]const u8{ "enum", "const", "type", "required", "additionalProperties" }) |name| {
        if (std.mem.eql(u8, name, key)) return true;
    }
    return false;
}

const Union = struct {
    discriminator: []const u8,
    known: []const std.json.Value,
    common: []const []const u8,
};

fn discriminated(allocator: std.mem.Allocator, root: std.json.Value, members: []const std.json.Value) Error!?Union {
    if (members.len == 0) return null;
    var discriminator: ?[]const u8 = null;
    var known = std.ArrayList(std.json.Value).empty;
    var common = std.ArrayList([]const u8).empty;
    var seeded = false;

    for (members) |member| {
        const resolved = resolve(root, member) orelse return null;
        var found: ?[]const u8 = null;
        var declared: std.json.Value = .null;
        if (resolved.get("properties")) |properties| {
            if (properties == .object) {
                var property = properties.object.iterator();
                while (property.next()) |entry| {
                    if (entry.value_ptr.* != .object) continue;
                    const constant = entry.value_ptr.object.get("const") orelse continue;
                    if (found != null) return null;
                    found = entry.key_ptr.*;
                    declared = constant;
                }
            }
        }
        const name = found orelse return null;
        if (discriminator) |prior| {
            if (!std.mem.eql(u8, prior, name)) return null;
        }
        discriminator = name;
        try known.append(allocator, declared);

        var required: std.StringArrayHashMapUnmanaged(void) = .empty;
        if (resolved.get("required")) |list| {
            if (list == .array) {
                for (list.array.items) |entry| {
                    if (entry == .string) try required.put(allocator, entry.string, {});
                }
            }
        }
        if (!seeded) {
            for (required.keys()) |name_of| try common.append(allocator, name_of);
            seeded = true;
        } else {
            var kept: usize = 0;
            for (common.items) |name_of| {
                if (required.get(name_of) == null) continue;
                common.items[kept] = name_of;
                kept += 1;
            }
            common.shrinkRetainingCapacity(kept);
        }
    }

    std.mem.sort([]const u8, common.items, {}, lessThan);
    return .{ .discriminator = discriminator.?, .known = known.items, .common = common.items };
}

fn lessThan(_: void, a: []const u8, b: []const u8) bool {
    return std.mem.order(u8, a, b) == .lt;
}

fn resolve(root: std.json.Value, member: std.json.Value) ?std.json.ObjectMap {
    if (member != .object) return null;
    const ref = member.object.get("$ref") orelse return member.object;
    if (ref != .string) return member.object;
    if (!std.mem.startsWith(u8, ref.string, "#/")) return null;
    var node = root;
    var parts = std.mem.splitScalar(u8, ref.string[2..], '/');
    while (parts.next()) |part| {
        if (node != .object) return null;
        node = node.object.get(part) orelse return null;
    }
    if (node != .object) return null;
    return node.object;
}

fn fallbackBranch(allocator: std.mem.Allocator, shape: Union) Error!std.json.Value {
    var required = std.json.Array.init(allocator);
    var seen = false;
    for (shape.common) |name| {
        if (std.mem.eql(u8, name, shape.discriminator)) seen = true;
        try required.append(.{ .string = name });
    }
    if (!seen) try required.append(.{ .string = shape.discriminator });

    var rejected = std.json.Array.init(allocator);
    for (shape.known) |value| try rejected.append(value);

    var negation: std.json.ObjectMap = .empty;
    try negation.put(allocator, "enum", .{ .array = rejected });

    var discriminator: std.json.ObjectMap = .empty;
    try discriminator.put(allocator, "type", .{ .string = "string" });
    try discriminator.put(allocator, "not", .{ .object = negation });

    var properties: std.json.ObjectMap = .empty;
    try properties.put(allocator, shape.discriminator, .{ .object = discriminator });

    var branch: std.json.ObjectMap = .empty;
    try branch.put(allocator, "type", .{ .string = "object" });
    try branch.put(allocator, "required", .{ .array = required });
    try branch.put(allocator, "properties", .{ .object = properties });
    return .{ .object = branch };
}

fn allStrings(values: []const std.json.Value) bool {
    if (values.len == 0) return false;
    for (values) |value| {
        if (value != .string) return false;
    }
    return true;
}

fn tolerated(arena: *std.heap.ArenaAllocator, source: []const u8) !std.json.Value {
    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, arena.allocator(), source, .{});
    return document(arena.allocator(), parsed);
}

test "a discriminated union gains a branch for every type it does not name" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();

    const out = try tolerated(&arena,
        \\{"oneOf":[
        \\{"type":"object","required":["type","id","seq"],"properties":{"type":{"const":"a"}}},
        \\{"type":"object","required":["type","id"],"properties":{"type":{"const":"b"}}}]}
    );

    try std.testing.expect(out.object.get("oneOf") == null);
    const branches = out.object.get("anyOf").?.array.items;
    try std.testing.expectEqual(@as(usize, 3), branches.len);

    const fallback = branches[2].object;
    try std.testing.expectEqualStrings("object", fallback.get("type").?.string);

    const required = fallback.get("required").?.array.items;
    try std.testing.expectEqual(@as(usize, 2), required.len);
    try std.testing.expectEqualStrings("id", required[0].string);
    try std.testing.expectEqualStrings("type", required[1].string);

    const rejected = fallback.get("properties").?.object.get("type").?.object.get("not").?.object.get("enum").?.array.items;
    try std.testing.expectEqual(@as(usize, 2), rejected.len);
    try std.testing.expectEqualStrings("a", rejected[0].string);
    try std.testing.expectEqualStrings("b", rejected[1].string);
}

test "tolerance opens the closed shapes and leaves the conditions that discriminate" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();

    const out = try tolerated(&arena,
        \\{"additionalProperties":false,
        \\"properties":{"status":{"enum":["running","done"]}},
        \\"if":{"additionalProperties":false,"properties":{"kind":{"enum":["x"]}}},
        \\"then":{"additionalProperties":false}}
    );

    try std.testing.expect(out.object.get("additionalProperties") == null);

    const status = out.object.get("properties").?.object.get("status").?.object;
    try std.testing.expect(status.get("enum") == null);
    try std.testing.expectEqualStrings("string", status.get("type").?.string);

    const condition = out.object.get("if").?.object;
    try std.testing.expectEqual(false, condition.get("additionalProperties").?.bool);
    try std.testing.expectEqual(@as(usize, 1), condition.get("properties").?.object.get("kind").?.object.get("enum").?.array.items.len);

    try std.testing.expect(out.object.get("then").?.object.get("additionalProperties") == null);
}

test "a union no single constant discriminates keeps the shape it had" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();

    const out = try tolerated(&arena,
        \\{"oneOf":[{"type":"object","properties":{"type":{"const":"a"},"kind":{"const":"k"}}},
        \\{"type":"object","properties":{"type":{"const":"b"}}}]}
    );

    try std.testing.expect(out.object.get("anyOf") == null);
    try std.testing.expectEqual(@as(usize, 2), out.object.get("oneOf").?.array.items.len);
}

test "a union whose members are local references is discriminated through them" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();

    const out = try tolerated(&arena,
        \\{"oneOf":[{"$ref":"#/$defs/a"},{"$ref":"#/$defs/b"}],
        \\"$defs":{"a":{"required":["type"],"properties":{"type":{"const":"a"}}},
        \\"b":{"required":["type"],"properties":{"type":{"const":"b"}}}}}
    );

    const branches = out.object.get("anyOf").?.array.items;
    try std.testing.expectEqual(@as(usize, 3), branches.len);
    try std.testing.expectEqualStrings("#/$defs/a", branches[0].object.get("$ref").?.string);
    try std.testing.expectEqualStrings("a", branches[2].object.get("properties").?.object.get("type").?.object.get("not").?.object.get("enum").?.array.items[0].string);
}
