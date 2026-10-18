const std = @import("std");
const ai_types = @import("ai_types");
const contract = @import("contract");

pub const SavedEntry = struct {
    offset: u64,
    at_ms: i64 = 0,
    message: ?ai_types.Message = null,
    compaction: []const u8 = "",
};

const Error = contract.Failure;

pub fn page(arena: std.mem.Allocator, request: contract.TranscriptRequest, saved: []const SavedEntry) Error!contract.TranscriptPage {
    const ids = try entryIds(arena, saved);
    var start: usize = 0;
    var gap: ?contract.TranscriptGap = null;
    if (request.cursor) |cursor| {
        const position = try decode(arena, request.native_id, cursor);
        if (position.entry_id.len == 0) {
            start = 0;
        } else if (indexOf(ids, position.entry_id)) |found| {
            start = found + 1;
        } else {
            const pruned = saved.len == 0 or saved[0].offset > position.offset;
            gap = .{ .reason = if (pruned) "pruned" else "rewritten", .resumed_at = if (saved.len > 0) ids[0] else "" };
        }
    }
    const limit = @max(request.limit, 1);
    var entries: std.ArrayList(std.json.Value) = .empty;
    var bytes: usize = 0;
    var next = start;
    while (next < saved.len and entries.items.len < limit) : (next += 1) {
        var value = try entryValue(arena, ids[next], saved[next], null);
        var size = try encodedLen(arena, value);
        if (bytes + size > contract.transcript_page_bytes) {
            if (entries.items.len > 0) break;
            var bound = contract.transcript_page_bytes / (partCount(saved[next]) * 2);
            value = try entryValue(arena, ids[next], saved[next], bound);
            size = try encodedLen(arena, value);
            while (size > contract.transcript_page_bytes and bound > 0) {
                bound /= 2;
                value = try entryValue(arena, ids[next], saved[next], bound);
                size = try encodedLen(arena, value);
            }
        }
        bytes += size;
        try entries.append(arena, value);
    }
    const last: ?usize = if (next > start) next - 1 else if (start > 0) start - 1 else null;
    const next_cursor = if (last) |index| try encode(arena, request.native_id, ids[index], saved[index].offset) else try encode(arena, request.native_id, "", 0);
    return .{ .entries = entries.items, .next_cursor = next_cursor, .complete = next >= saved.len, .gap = gap };
}

pub fn tail(arena: std.mem.Allocator, native_id: []const u8, saved: []const SavedEntry) Error![]const u8 {
    const ids = try entryIds(arena, saved);
    if (saved.len == 0) return encode(arena, native_id, "", 0);
    return encode(arena, native_id, ids[saved.len - 1], saved[saved.len - 1].offset);
}

fn entryIds(arena: std.mem.Allocator, saved: []const SavedEntry) ![]const []const u8 {
    const ids = try arena.alloc([]const u8, saved.len);
    for (saved, ids, 0..) |entry, *id, index| {
        var repeat: usize = 0;
        while (repeat < index and saved[index - repeat - 1].offset == entry.offset) repeat += 1;
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

fn partCount(entry: SavedEntry) usize {
    const message = entry.message orelse return 1;
    return @max(1, switch (message) {
        .user => |user| switch (user.content) {
            .text => 1,
            .parts => |parts| parts.len,
        },
        .assistant => |assistant| assistant.content.len,
        .tool_result => |result| result.content.len,
    });
}

fn cut(text: []const u8, bound: ?usize) []const u8 {
    const max = bound orelse return text;
    if (text.len <= max) return text;
    var len = max;
    while (len > 0 and (text[len] & 0xc0) == 0x80) len -= 1;
    return text[0..len];
}

fn entryValue(arena: std.mem.Allocator, id: []const u8, entry: SavedEntry, bound: ?usize) !std.json.Value {
    var object: std.json.ObjectMap = .empty;
    try object.put(arena, "entry_id", .{ .string = id });
    if (entry.at_ms > 0) try object.put(arena, "at_ms", .{ .integer = entry.at_ms });
    if (entry.message) |message| {
        try object.put(arena, "kind", .{ .string = "message" });
        try object.put(arena, "message", try messageValue(arena, message, bound));
    } else {
        var compaction: std.json.ObjectMap = .empty;
        try compaction.put(arena, "summary", .{ .string = cut(entry.compaction, bound) });
        try object.put(arena, "kind", .{ .string = "compaction" });
        try object.put(arena, "compaction", .{ .object = compaction });
    }
    if (bound != null) try object.put(arena, "truncated", .{ .bool = true });
    return .{ .object = object };
}

fn messageValue(arena: std.mem.Allocator, message: ai_types.Message, bound: ?usize) !std.json.Value {
    var parts = std.json.Array.init(arena);
    const role: []const u8 = switch (message) {
        .user => |user| role: {
            switch (user.content) {
                .text => |text| try parts.append(try textPart(arena, "text", cut(text, bound))),
                .parts => |held| for (held) |part| try parts.append(try userPart(arena, part, bound)),
            }
            break :role "user";
        },
        .assistant => |assistant| role: {
            for (assistant.content) |part| try parts.append(switch (part) {
                .text => |text| try textPart(arena, "text", cut(text.text, bound)),
                .thinking => |thinking| try textPart(arena, "reasoning", cut(thinking.thinking, bound)),
                .image => |image| try imagePart(arena, image, bound),
                .tool_call => |call| try toolCallPart(arena, call, bound),
            });
            break :role "assistant";
        },
        .tool_result => |result| role: {
            var text: std.ArrayList(u8) = .empty;
            for (result.content) |part| switch (part) {
                .text => |held| try text.appendSlice(arena, held.text),
                .image => {},
            };
            var object: std.json.ObjectMap = .empty;
            try object.put(arena, "type", .{ .string = "tool_result" });
            try object.put(arena, "tool_call_id", .{ .string = if (result.tool_call_id.len > 0) result.tool_call_id else "unknown" });
            try object.put(arena, "result", .{ .string = cut(text.items, bound) });
            if (result.is_error) try object.put(arena, "is_error", .{ .bool = true });
            try parts.append(.{ .object = object });
            break :role "tool";
        },
    };
    var object: std.json.ObjectMap = .empty;
    try object.put(arena, "role", .{ .string = role });
    try object.put(arena, "content", if (parts.items.len > 0) .{ .array = parts } else .{ .string = "" });
    return .{ .object = object };
}

fn textPart(arena: std.mem.Allocator, kind: []const u8, text: []const u8) !std.json.Value {
    var object: std.json.ObjectMap = .empty;
    try object.put(arena, "type", .{ .string = kind });
    try object.put(arena, kind, .{ .string = text });
    return .{ .object = object };
}

fn userPart(arena: std.mem.Allocator, part: ai_types.UserContentPart, bound: ?usize) !std.json.Value {
    return switch (part) {
        .text => |text| textPart(arena, "text", cut(text.text, bound)),
        .image => |image| imagePart(arena, image, bound),
    };
}

fn imagePart(arena: std.mem.Allocator, image: ai_types.ImageContent, bound: ?usize) !std.json.Value {
    if (bound != null) return textPart(arena, "text", "image");
    var source: std.json.ObjectMap = .empty;
    try source.put(arena, "data", .{ .string = image.data });
    try source.put(arena, "media_type", .{ .string = image.mime_type });
    var object: std.json.ObjectMap = .empty;
    try object.put(arena, "type", .{ .string = "image" });
    try object.put(arena, "image", .{ .object = source });
    return .{ .object = object };
}

fn toolCallPart(arena: std.mem.Allocator, call: ai_types.ToolCall, bound: ?usize) !std.json.Value {
    const parsed: ?std.json.Value = if (bound == null) std.json.parseFromSliceLeaky(std.json.Value, arena, call.arguments_json, .{}) catch null else null;
    var object: std.json.ObjectMap = .empty;
    try object.put(arena, "type", .{ .string = "tool_call" });
    try object.put(arena, "tool_call_id", .{ .string = if (call.id.len > 0) call.id else "unknown" });
    try object.put(arena, "name", .{ .string = if (call.name.len > 0) call.name else "unknown" });
    try object.put(arena, "arguments_json", parsed orelse .{ .string = cut(call.arguments_json, bound) });
    return .{ .object = object };
}

const testing = std.testing;

fn userEntry(offset: u64, text: []const u8) SavedEntry {
    return .{ .offset = offset, .at_ms = 1000, .message = .{ .user = .{ .content = .{ .text = text }, .timestamp = 0 } } };
}

fn idOf(value: std.json.Value) []const u8 {
    return value.object.get("entry_id").?.string;
}

test "a page holds at most its limit, and its cursor reads on from the entry after the last one returned" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const saved = [_]SavedEntry{ userEntry(0, "one"), userEntry(40, "two"), .{ .offset = 80, .compaction = "kept" } };
    const first = try page(a, .{ .native_id = "s1", .limit = 2 }, &saved);
    try testing.expectEqual(@as(usize, 2), first.entries.len);
    try testing.expect(!first.complete);
    try testing.expectEqualStrings("e0", idOf(first.entries[0]));
    try testing.expectEqualStrings("message", first.entries[0].object.get("kind").?.string);
    try testing.expectEqualStrings("one", first.entries[0].object.get("message").?.object.get("content").?.array.items[0].object.get("text").?.string);
    const second = try page(a, .{ .native_id = "s1", .cursor = first.next_cursor }, &saved);
    try testing.expect(second.complete);
    try testing.expect(second.gap == null);
    try testing.expectEqual(@as(usize, 1), second.entries.len);
    try testing.expectEqualStrings("kept", second.entries[0].object.get("compaction").?.object.get("summary").?.string);
    const caught_up = try page(a, .{ .native_id = "s1", .cursor = second.next_cursor }, &saved);
    try testing.expectEqual(@as(usize, 0), caught_up.entries.len);
    try testing.expect(caught_up.complete);
    try testing.expectEqualStrings(second.next_cursor, caught_up.next_cursor);
}

test "a cursor whose entry is gone resumes at the oldest entry held, pruned when the store's front moved past it and rewritten otherwise" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const before = [_]SavedEntry{ userEntry(0, "one"), userEntry(40, "two"), userEntry(80, "three") };
    const read = try page(a, .{ .native_id = "s1", .limit = 2 }, &before);
    const pruned = [_]SavedEntry{userEntry(120, "four")};
    const after_prune = try page(a, .{ .native_id = "s1", .cursor = read.next_cursor }, &pruned);
    try testing.expectEqualStrings("pruned", after_prune.gap.?.reason);
    try testing.expectEqualStrings("e120", after_prune.gap.?.resumed_at);
    try testing.expectEqualStrings("e120", idOf(after_prune.entries[0]));
    const rewritten = [_]SavedEntry{ userEntry(0, "one"), userEntry(50, "two, rewritten") };
    const after_rewrite = try page(a, .{ .native_id = "s1", .cursor = read.next_cursor }, &rewritten);
    try testing.expectEqualStrings("rewritten", after_rewrite.gap.?.reason);
    try testing.expectEqualStrings("e0", after_rewrite.gap.?.resumed_at);
    try testing.expectEqual(@as(usize, 2), after_rewrite.entries.len);
}

test "a cursor that does not decode or that names another session is invalid, never a gap" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const saved = [_]SavedEntry{ userEntry(0, "one"), userEntry(40, "two") };
    const read = try page(a, .{ .native_id = "s1", .limit = 1 }, &saved);
    try testing.expectError(error.InvalidCursor, page(a, .{ .native_id = "s2", .cursor = read.next_cursor }, &saved));
    try testing.expectError(error.InvalidCursor, page(a, .{ .native_id = "s1", .cursor = "not a cursor!" }, &saved));
    try testing.expectError(error.InvalidCursor, page(a, .{ .native_id = "s1", .cursor = "Mgpz" }, &saved));
}

test "a page stops before its byte bound, and an entry larger than the bound comes alone, cut on a code point and marked truncated" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const half = try a.alloc(u8, contract.transcript_page_bytes / 2);
    @memset(half, 'x');
    const huge = try a.alloc(u8, contract.transcript_page_bytes + 2);
    @memset(huge, 'y');
    @memcpy(huge[contract.transcript_page_bytes / 2 - 1 ..][0..2], "é");
    const saved = [_]SavedEntry{ userEntry(0, half), userEntry(1, half), userEntry(2, huge) };
    const first = try page(a, .{ .native_id = "s1" }, &saved);
    try testing.expectEqual(@as(usize, 1), first.entries.len);
    try testing.expect(!first.complete);
    try testing.expect(first.entries[0].object.get("truncated") == null);
    const second = try page(a, .{ .native_id = "s1", .cursor = first.next_cursor }, &saved);
    try testing.expectEqual(@as(usize, 1), second.entries.len);
    const third = try page(a, .{ .native_id = "s1", .cursor = second.next_cursor }, &saved);
    try testing.expectEqual(@as(usize, 1), third.entries.len);
    try testing.expect(third.complete);
    try testing.expect(third.entries[0].object.get("truncated").?.bool);
    const text = third.entries[0].object.get("message").?.object.get("content").?.array.items[0].object.get("text").?.string;
    try testing.expect(text.len < contract.transcript_page_bytes);
    try testing.expect(std.unicode.utf8ValidateSlice(text));
}

test "an empty transcript still mints a cursor, which reads from the start once entries arrive and is never a gap" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const empty = try page(a, .{ .native_id = "s1" }, &.{});
    try testing.expect(empty.complete);
    try testing.expect(empty.next_cursor.len > 0);
    const again = try page(a, .{ .native_id = "s1", .cursor = empty.next_cursor }, &.{});
    try testing.expect(again.gap == null);
    try testing.expectEqual(@as(usize, 0), again.entries.len);
    const saved = [_]SavedEntry{userEntry(0, "one")};
    const arrived = try page(a, .{ .native_id = "s1", .cursor = empty.next_cursor }, &saved);
    try testing.expect(arrived.gap == null);
    try testing.expectEqualStrings("e0", idOf(arrived.entries[0]));
}

test "an entry whose text escapes to several times its length is still cut until its encoding fits the page" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const controls = try a.alloc(u8, contract.transcript_page_bytes);
    @memset(controls, 1);
    const saved = [_]SavedEntry{userEntry(0, controls)};
    const cut_page = try page(a, .{ .native_id = "s1" }, &saved);
    try testing.expect(cut_page.entries[0].object.get("truncated").?.bool);
    try testing.expect(try encodedLen(a, cut_page.entries[0]) <= contract.transcript_page_bytes);
}
