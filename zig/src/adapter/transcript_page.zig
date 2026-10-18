const std = @import("std");
const contract = @import("contract.zig");

pub const Item = struct {
    offset: u64,
    at_ms: i64 = 0,
    message: ?std.json.Value = null,
    compaction: []const u8 = "",
};

const Error = contract.Failure;

pub fn page(arena: std.mem.Allocator, request: contract.TranscriptRequest, items: []const Item) Error!contract.TranscriptPage {
    const ids = try entryIds(arena, items);
    var start: usize = 0;
    var gap: ?contract.TranscriptGap = null;
    if (request.cursor) |cursor| {
        const position = try decode(arena, request.native_id, cursor);
        if (position.entry_id.len == 0) {
            start = 0;
        } else if (indexOf(ids, position.entry_id)) |found| {
            start = found + 1;
        } else {
            const pruned = items.len == 0 or items[0].offset > position.offset;
            gap = .{ .reason = if (pruned) "pruned" else "rewritten", .resumed_at = if (items.len > 0) ids[0] else "" };
        }
    }
    const limit = @max(request.limit, 1);
    var entries: std.ArrayList(std.json.Value) = .empty;
    var bytes: usize = 0;
    var next = start;
    while (next < items.len and entries.items.len < limit) : (next += 1) {
        var value = try entryValue(arena, ids[next], items[next], null);
        var size = try encodedLen(arena, value);
        if (bytes + size > contract.transcript_page_bytes) {
            if (entries.items.len > 0) break;
            const parts = partCount(items[next]);
            var plan = Cut{ .bound = contract.transcript_page_bytes / (parts * 2), .keep = parts };
            value = try entryValue(arena, ids[next], items[next], plan);
            size = try encodedLen(arena, value);
            while (size > contract.transcript_page_bytes and plan.bound > 0) {
                plan.bound /= 2;
                value = try entryValue(arena, ids[next], items[next], plan);
                size = try encodedLen(arena, value);
            }
            if (size > contract.transcript_page_bytes) {
                plan.reduce = true;
                value = try entryValue(arena, ids[next], items[next], plan);
                size = try encodedLen(arena, value);
            }
            while (size > contract.transcript_page_bytes and plan.keep > 1) {
                plan.keep /= 2;
                value = try entryValue(arena, ids[next], items[next], plan);
                size = try encodedLen(arena, value);
            }
        }
        bytes += size;
        try entries.append(arena, value);
    }
    const last: ?usize = if (next > start) next - 1 else if (start > 0) start - 1 else null;
    const next_cursor = if (last) |index| try encode(arena, request.native_id, ids[index], items[index].offset) else try encode(arena, request.native_id, "", 0);
    return .{ .entries = entries.items, .next_cursor = next_cursor, .complete = next >= items.len, .gap = gap };
}

pub fn tail(arena: std.mem.Allocator, native_id: []const u8, items: []const Item) Error![]const u8 {
    const ids = try entryIds(arena, items);
    if (items.len == 0) return encode(arena, native_id, "", 0);
    return encode(arena, native_id, ids[items.len - 1], items[items.len - 1].offset);
}

fn entryIds(arena: std.mem.Allocator, items: []const Item) ![]const []const u8 {
    const ids = try arena.alloc([]const u8, items.len);
    for (items, ids, 0..) |entry, *id, index| {
        var repeat: usize = 0;
        while (repeat < index and items[index - repeat - 1].offset == entry.offset) repeat += 1;
        id.* = if (repeat == 0) try std.fmt.allocPrint(arena, "e{d}", .{entry.offset}) else try std.fmt.allocPrint(arena, "e{d}.{d}", .{ entry.offset, repeat });
    }
    return ids;
}

const Position = struct { entry_id: []const u8, offset: u64 };

fn encode(arena: std.mem.Allocator, native_id: []const u8, entry_id: []const u8, offset: u64) ![]const u8 {
    const raw = try std.fmt.allocPrint(arena, "1\n{s}\n{s}\n{d}", .{ native_id, entry_id, offset });
    const coder = std.base64.url_safe_no_pad.Encoder;
    const out = try arena.alloc(u8, coder.calcSize(raw.len));
    return coder.encode(out, raw);
}

fn decode(arena: std.mem.Allocator, native_id: []const u8, cursor: []const u8) Error!Position {
    const coder = std.base64.url_safe_no_pad.Decoder;
    const size = coder.calcSizeForSlice(cursor) catch return error.InvalidCursor;
    const raw = try arena.alloc(u8, size);
    coder.decode(raw, cursor) catch return error.InvalidCursor;
    var fields = std.mem.splitScalar(u8, raw, '\n');
    if (!std.mem.eql(u8, fields.next() orelse "", "1")) return error.InvalidCursor;
    if (!std.mem.eql(u8, fields.next() orelse "", native_id)) return error.InvalidCursor;
    const entry_id = fields.next() orelse return error.InvalidCursor;
    const offset = std.fmt.parseInt(u64, fields.next() orelse "", 10) catch return error.InvalidCursor;
    if (fields.next() != null) return error.InvalidCursor;
    return .{ .entry_id = entry_id, .offset = offset };
}

fn indexOf(ids: []const []const u8, wanted: []const u8) ?usize {
    for (ids, 0..) |id, index| {
        if (std.mem.eql(u8, id, wanted)) return index;
    }
    return null;
}

fn encodedLen(arena: std.mem.Allocator, value: std.json.Value) !usize {
    const encoded = try std.json.Stringify.valueAlloc(arena, value, .{});
    return encoded.len;
}

fn partCount(entry: Item) usize {
    const message = entry.message orelse return 1;
    if (message != .object) return 1;
    const content = message.object.get("content") orelse return 1;
    return if (content == .array) @max(1, content.array.items.len) else 1;
}

fn cut(text: []const u8, bound: usize) []const u8 {
    if (text.len <= bound) return text;
    var len = bound;
    while (len > 0 and (text[len] & 0xc0) == 0x80) len -= 1;
    return text[0..len];
}

const Cut = struct { bound: usize, reduce: bool = false, keep: usize };

fn entryValue(arena: std.mem.Allocator, id: []const u8, entry: Item, plan: ?Cut) !std.json.Value {
    var object: std.json.ObjectMap = .empty;
    try object.put(arena, "entry_id", .{ .string = id });
    if (entry.at_ms > 0) try object.put(arena, "at_ms", .{ .integer = entry.at_ms });
    if (entry.message) |message| {
        try object.put(arena, "kind", .{ .string = "message" });
        try object.put(arena, "message", if (plan) |held| try cutMessage(arena, message, held) else message);
    } else {
        var compaction: std.json.ObjectMap = .empty;
        try compaction.put(arena, "summary", .{ .string = if (plan) |held| cut(entry.compaction, held.bound) else entry.compaction });
        try object.put(arena, "kind", .{ .string = "compaction" });
        try object.put(arena, "compaction", .{ .object = compaction });
    }
    if (plan != null) try object.put(arena, "truncated", .{ .bool = true });
    return .{ .object = object };
}

fn cutField(key: []const u8) bool {
    for ([_][]const u8{ "text", "reasoning", "result", "arguments_json" }) |name| {
        if (std.mem.eql(u8, key, name)) return true;
    }
    return false;
}

fn cutMessage(arena: std.mem.Allocator, message: std.json.Value, plan: Cut) !std.json.Value {
    if (message != .object) return cutValue(arena, message, plan);
    var out: std.json.ObjectMap = .empty;
    var members = message.object.iterator();
    while (members.next()) |member| {
        const held = member.value_ptr.*;
        if (std.mem.eql(u8, member.key_ptr.*, "content") and held == .array) {
            const kept = held.array.items[0..@min(held.array.items.len, plan.keep)];
            var parts = try std.json.Array.initCapacity(arena, kept.len);
            for (kept) |part| parts.appendAssumeCapacity(try cutValue(arena, part, plan));
            try out.put(arena, member.key_ptr.*, .{ .array = parts });
        } else {
            try out.put(arena, member.key_ptr.*, try cutValue(arena, held, plan));
        }
    }
    return .{ .object = out };
}

fn cutValue(arena: std.mem.Allocator, value: std.json.Value, plan: Cut) !std.json.Value {
    const bound = plan.bound;
    switch (value) {
        .string => |text| return .{ .string = cut(text, bound) },
        .array => |held| {
            var out = try std.json.Array.initCapacity(arena, held.items.len);
            for (held.items) |item| out.appendAssumeCapacity(try cutValue(arena, item, plan));
            return .{ .array = out };
        },
        .object => |held| {
            const kind = if (held.get("type")) |named| if (named == .string) named.string else "" else "";
            const reduced = plan.reduce and kind.len > 0 and !std.mem.eql(u8, kind, "text") and !std.mem.eql(u8, kind, "reasoning");
            if (std.mem.eql(u8, kind, "image") or reduced) {
                var text: std.json.ObjectMap = .empty;
                try text.put(arena, "type", .{ .string = "text" });
                try text.put(arena, "text", .{ .string = kind });
                return .{ .object = text };
            }
            var out: std.json.ObjectMap = .empty;
            var members = held.iterator();
            while (members.next()) |member| {
                const value_held = member.value_ptr.*;
                const kept: std.json.Value = if (!cutField(member.key_ptr.*))
                    (if (value_held == .string) value_held else try cutValue(arena, value_held, plan))
                else if (value_held == .string)
                    .{ .string = cut(value_held.string, bound) }
                else
                    .{ .string = cut(try std.json.Stringify.valueAlloc(arena, value_held, .{}), bound) };
                try out.put(arena, member.key_ptr.*, kept);
            }
            return .{ .object = out };
        },
        else => return value,
    }
}
