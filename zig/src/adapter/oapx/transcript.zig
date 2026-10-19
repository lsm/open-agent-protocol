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
    return contract.transcript_page.page(arena, request, try items(arena, saved));
}

pub fn tail(arena: std.mem.Allocator, native_id: []const u8, saved: []const SavedEntry) Error![]const u8 {
    return contract.transcript_page.tail(arena, native_id, try items(arena, saved));
}

fn items(arena: std.mem.Allocator, saved: []const SavedEntry) ![]const contract.transcript_page.Item {
    const listed = try arena.alloc(contract.transcript_page.Item, saved.len);
    for (saved, listed) |*entry, *slot| slot.* = .{
        .offset = entry.offset,
        .at_ms = entry.at_ms,
        .lazy = if (entry.message != null) .{ .ctx = entry, .build = encodeSaved } else null,
        .compaction = entry.compaction,
    };
    return listed;
}

fn encodeSaved(ctx: *const anyopaque, arena: std.mem.Allocator) std.mem.Allocator.Error!std.json.Value {
    const entry: *const SavedEntry = @ptrCast(@alignCast(ctx));
    return messageValue(arena, entry.message.?);
}

pub fn messageValue(arena: std.mem.Allocator, message: ai_types.Message) !std.json.Value {
    var parts = std.json.Array.init(arena);
    const role: []const u8 = switch (message) {
        .user => |user| role: {
            switch (user.content) {
                .text => |text| try parts.append(try textPart(arena, "text", text)),
                .parts => |held| for (held) |part| try parts.append(try userPart(arena, part)),
            }
            break :role "user";
        },
        .assistant => |assistant| role: {
            for (assistant.content) |part| try parts.append(switch (part) {
                .text => |text| try textPart(arena, "text", text.text),
                .thinking => |thinking| try textPart(arena, "reasoning", thinking.thinking),
                .image => |image| try imagePart(arena, image),
                .tool_call => |call| try toolCallPart(arena, call),
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
            try object.put(arena, "result", .{ .string = text.items });
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

fn userPart(arena: std.mem.Allocator, part: ai_types.UserContentPart) !std.json.Value {
    return switch (part) {
        .text => |text| textPart(arena, "text", text.text),
        .image => |image| imagePart(arena, image),
    };
}

fn imagePart(arena: std.mem.Allocator, image: ai_types.ImageContent) !std.json.Value {
    var source: std.json.ObjectMap = .empty;
    try source.put(arena, "data", .{ .string = image.data });
    try source.put(arena, "media_type", .{ .string = image.mime_type });
    var object: std.json.ObjectMap = .empty;
    try object.put(arena, "type", .{ .string = "image" });
    try object.put(arena, "image", .{ .object = source });
    return .{ .object = object };
}

fn toolCallPart(arena: std.mem.Allocator, call: ai_types.ToolCall) !std.json.Value {
    const parsed: ?std.json.Value = std.json.parseFromSliceLeaky(std.json.Value, arena, call.arguments_json, .{}) catch null;
    var object: std.json.ObjectMap = .empty;
    try object.put(arena, "type", .{ .string = "tool_call" });
    try object.put(arena, "tool_call_id", .{ .string = if (call.id.len > 0) call.id else "unknown" });
    try object.put(arena, "name", .{ .string = if (call.name.len > 0) call.name else "unknown" });
    try object.put(arena, "arguments_json", parsed orelse .{ .string = call.arguments_json });
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
    try testing.expect((try std.json.Stringify.valueAlloc(a, cut_page.entries[0], .{})).len <= contract.transcript_page_bytes);
}

test "an oversize entry cuts only its text-bearing fields, so a tool call keeps its type, id and name" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const long = try a.alloc(u8, contract.transcript_page_bytes);
    @memset(long, 'z');
    const parts = try a.dupe(ai_types.AssistantContent, &.{ .{ .text = .{ .text = long } }, .{ .tool_call = .{ .id = "call-identifier", .name = "a_tool_name", .arguments_json = "\"raw arguments\"" } } });
    const message = ai_types.Message{ .assistant = .{ .content = parts, .api = "", .provider = "", .model = "", .usage = .{}, .stop_reason = .stop, .timestamp = 0 } };
    const cut_page = try page(a, .{ .native_id = "s1" }, &.{.{ .offset = 0, .message = message }});
    try testing.expect(cut_page.entries[0].object.get("truncated").?.bool);
    try testing.expect((try std.json.Stringify.valueAlloc(a, cut_page.entries[0], .{})).len <= contract.transcript_page_bytes);
    const listed = cut_page.entries[0].object.get("message").?.object.get("content").?.array.items;
    const call = listed[1].object;
    try testing.expectEqualStrings("tool_call", call.get("type").?.string);
    try testing.expectEqualStrings("call-identifier", call.get("tool_call_id").?.string);
    try testing.expectEqualStrings("a_tool_name", call.get("name").?.string);
    try testing.expectEqualStrings("raw arguments", call.get("arguments_json").?.string);
}

test "an entry too large even with every text emptied reduces its parts to their kind, then keeps the leading parts that fit" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const parts = try a.alloc(ai_types.AssistantContent, 40001);
    parts[0] = .{ .text = .{ .text = "the first words" } };
    for (parts[1..]) |*part| part.* = .{ .tool_call = .{ .id = "call-identifier", .name = "a_tool_name", .arguments_json = "{}" } };
    const message = ai_types.Message{ .assistant = .{ .content = parts, .api = "", .provider = "", .model = "", .usage = .{}, .stop_reason = .stop, .timestamp = 0 } };
    const cut_page = try page(a, .{ .native_id = "s1" }, &.{.{ .offset = 0, .message = message }});
    try testing.expect(cut_page.entries[0].object.get("truncated").?.bool);
    try testing.expect((try std.json.Stringify.valueAlloc(a, cut_page.entries[0], .{})).len <= contract.transcript_page_bytes);
    const listed = cut_page.entries[0].object.get("message").?.object.get("content").?.array.items;
    try testing.expect(listed.len >= 2 and listed.len < parts.len);
    try testing.expectEqualStrings("assistant", cut_page.entries[0].object.get("message").?.object.get("role").?.string);
    try testing.expectEqualStrings("text", listed[1].object.get("type").?.string);
    try testing.expectEqualStrings("tool_call", listed[1].object.get("text").?.string);
}

const Counted = struct {
    var built: usize = 0;

    fn build(ctx: *const anyopaque, arena: std.mem.Allocator) std.mem.Allocator.Error!std.json.Value {
        _ = ctx;
        built += 1;
        return messageValue(arena, .{ .user = .{ .content = .{ .text = "said" }, .timestamp = 0 } });
    }
};

test "the pager encodes a message only for an entry it returns, and the tail cursor encodes none" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const listed = try a.alloc(contract.transcript_page.Item, 50);
    for (listed, 0..) |*item, index| item.* = .{ .offset = index, .lazy = .{ .ctx = item, .build = Counted.build } };
    Counted.built = 0;
    _ = try contract.transcript_page.tail(a, "s1", listed);
    try testing.expectEqual(@as(usize, 0), Counted.built);
    const two = try contract.transcript_page.page(a, .{ .native_id = "s1", .limit = 2 }, listed);
    try testing.expectEqual(@as(usize, 2), two.entries.len);
    try testing.expectEqual(@as(usize, 2), Counted.built);
}
