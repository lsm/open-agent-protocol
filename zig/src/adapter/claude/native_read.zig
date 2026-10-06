const std = @import("std");
const contract = @import("contract");
const compat = @import("compat");
const native_list = @import("native_list.zig");

const read_limit: u64 = 64 * 1024 * 1024;

pub fn read(arena: std.mem.Allocator, home: []const u8, directory: []const u8, native_id: []const u8) ![]const contract.NativeTurn {
    if (home.len == 0 or !safeId(native_id)) return &.{};
    const name = try std.fmt.allocPrint(arena, "{s}.jsonl", .{native_id});
    const projects = try std.fs.path.join(arena, &.{ home, ".claude", "projects" });
    if (directory.len > 0) {
        const path = try std.fs.path.join(arena, &.{ projects, try native_list.projectDirName(arena, directory), name });
        if (try snapshot(arena, path)) |bytes| return turns(arena, bytes);
    }
    var root = std.Io.Dir.openDirAbsolute(compat.fs.defaultIo(), projects, .{ .iterate = true }) catch return &.{};
    defer root.close(compat.fs.defaultIo());
    var iterator = root.iterate();
    while (iterator.next(compat.fs.defaultIo()) catch null) |entry| {
        if (entry.kind != .directory) continue;
        const path = try std.fs.path.join(arena, &.{ projects, entry.name, name });
        if (try snapshot(arena, path)) |bytes| return turns(arena, bytes);
    }
    return &.{};
}

fn safeId(native_id: []const u8) bool {
    if (native_id.len == 0) return false;
    for (native_id) |byte| {
        if (!std.ascii.isAlphanumeric(byte) and byte != '-' and byte != '_') return false;
    }
    return true;
}

fn snapshot(arena: std.mem.Allocator, path: []const u8) !?[]const u8 {
    var dir = std.Io.Dir.openDirAbsolute(compat.fs.defaultIo(), std.fs.path.dirname(path) orelse return null, .{}) catch return null;
    defer dir.close(compat.fs.defaultIo());
    var file = dir.openFile(compat.fs.defaultIo(), std.fs.path.basename(path), .{}) catch return null;
    defer file.close(compat.fs.defaultIo());
    const size = (file.stat(compat.fs.defaultIo()) catch return null).size;
    const start = if (size > read_limit) size - read_limit else 0;
    const bytes = try arena.alloc(u8, @intCast(size - start));
    const got = file.readPositionalAll(compat.fs.defaultIo(), bytes, start) catch return null;
    var whole = bytes[0..got];
    if (start > 0) whole = whole[(std.mem.indexOfScalar(u8, whole, '\n') orelse whole.len)..];
    return whole[0..(std.mem.lastIndexOfScalar(u8, whole, '\n') orelse 0)];
}

pub fn turns(arena: std.mem.Allocator, bytes: []const u8) ![]const contract.NativeTurn {
    var found: std.ArrayList(contract.NativeTurn) = .empty;
    var reply: std.ArrayList(u8) = .empty;
    var reply_id: []const u8 = "";
    var reply_at: i64 = 0;
    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |line| {
        if (line.len == 0) continue;
        const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, line, .{}) catch continue;
        if (parsed != .object) continue;
        const kind = text(parsed.object.get("type")) orelse continue;
        if (flag(parsed.object.get("isSidechain")) or flag(parsed.object.get("isMeta"))) continue;
        const message = parsed.object.get("message") orelse continue;
        if (message != .object) continue;
        const at = isoMillis(text(parsed.object.get("timestamp")) orelse "") orelse 0;
        if (std.mem.eql(u8, kind, "user")) {
            const said = try userText(arena, message.object.get("content")) orelse continue;
            if (reply.items.len > 0) try found.append(arena, .{ .role = .assistant, .text = reply.items, .at_ms = reply_at });
            reply = .empty;
            reply_id = "";
            try found.append(arena, .{ .role = .user, .text = said, .at_ms = at });
        } else if (std.mem.eql(u8, kind, "assistant")) {
            const id = text(message.object.get("id")) orelse "";
            const piece = try assistantText(arena, message.object.get("content"));
            if (piece.len == 0) continue;
            if (!std.mem.eql(u8, id, reply_id) or id.len == 0) reply.clearRetainingCapacity();
            try reply.appendSlice(arena, piece);
            reply_id = id;
            reply_at = at;
        }
    }
    if (reply.items.len > 0) try found.append(arena, .{ .role = .assistant, .text = reply.items, .at_ms = reply_at });
    return found.items;
}

fn text(value: ?std.json.Value) ?[]const u8 {
    const present = value orelse return null;
    return if (present == .string) present.string else null;
}

fn flag(value: ?std.json.Value) bool {
    const present = value orelse return false;
    return present == .bool and present.bool;
}

fn userText(arena: std.mem.Allocator, content: ?std.json.Value) !?[]const u8 {
    const body = content orelse return null;
    var joined: std.ArrayList(u8) = .empty;
    switch (body) {
        .string => |plain| try joined.appendSlice(arena, plain),
        .array => |parts| for (parts.items) |part| {
            if (part != .object) continue;
            if (!std.mem.eql(u8, text(part.object.get("type")) orelse "", "text")) continue;
            const piece = text(part.object.get("text")) orelse continue;
            if (joined.items.len > 0) try joined.append(arena, '\n');
            try joined.appendSlice(arena, piece);
        },
        else => return null,
    }
    const said = withoutWrappers(joined.items);
    if (said.len == 0) return null;
    return said;
}

const wrappers = [_][]const u8{ "command-name", "command-message", "command-args", "local-command-stdout", "local-command-stderr", "local-command-caveat", "system-reminder", "bash-input", "bash-stdout", "bash-stderr" };

fn withoutWrappers(said: []const u8) []const u8 {
    var rest = std.mem.trim(u8, said, " \t\r\n");
    outer: while (rest.len > 0 and rest[0] == '<') {
        for (wrappers) |name| {
            if (rest.len < name.len + 2 or !std.mem.eql(u8, rest[1 .. name.len + 1], name) or rest[name.len + 1] != '>') continue;
            var closing_buffer: [64]u8 = undefined;
            const closing = std.fmt.bufPrint(&closing_buffer, "</{s}>", .{name}) catch return rest;
            const at = std.mem.indexOf(u8, rest, closing) orelse return rest;
            rest = std.mem.trim(u8, rest[at + closing.len ..], " \t\r\n");
            continue :outer;
        }
        break;
    }
    return rest;
}

fn assistantText(arena: std.mem.Allocator, content: ?std.json.Value) ![]const u8 {
    const body = content orelse return "";
    if (body != .array) return if (body == .string) body.string else "";
    var joined: std.ArrayList(u8) = .empty;
    for (body.array.items) |part| {
        if (part != .object) continue;
        if (!std.mem.eql(u8, text(part.object.get("type")) orelse "", "text")) continue;
        try joined.appendSlice(arena, text(part.object.get("text")) orelse "");
    }
    return joined.items;
}

pub fn isoMillis(stamp: []const u8) ?i64 {
    if (stamp.len < 20 or stamp[4] != '-' or stamp[7] != '-' or stamp[10] != 'T' or stamp[13] != ':' or stamp[16] != ':') return null;
    const year = std.fmt.parseInt(i64, stamp[0..4], 10) catch return null;
    const month = std.fmt.parseInt(i64, stamp[5..7], 10) catch return null;
    const day = std.fmt.parseInt(i64, stamp[8..10], 10) catch return null;
    const hour = std.fmt.parseInt(i64, stamp[11..13], 10) catch return null;
    const minute = std.fmt.parseInt(i64, stamp[14..16], 10) catch return null;
    const second = std.fmt.parseInt(i64, stamp[17..19], 10) catch return null;
    var millis: i64 = 0;
    if (stamp[19] == '.') {
        var index: usize = 20;
        var scale: i64 = 100;
        while (index < stamp.len and std.ascii.isDigit(stamp[index])) : (index += 1) {
            millis += (stamp[index] - '0') * scale;
            scale = @divTrunc(scale, 10);
        }
    }
    const shifted = if (month <= 2) year - 1 else year;
    const era = @divFloor(shifted, 400);
    const of_era = shifted - era * 400;
    const day_of_year = @divFloor(153 * (month + (if (month > 2) @as(i64, -3) else 9)) + 2, 5) + day - 1;
    const day_of_era = of_era * 365 + @divFloor(of_era, 4) - @divFloor(of_era, 100) + day_of_year;
    const days = era * 146097 + day_of_era - 719468;
    return ((days * 24 + hour) * 60 + minute) * 60_000 + second * 1000 + millis;
}

test "a transcript reads as its user messages and the replies between them, leaving out tool results, thinking, meta, side chains and Claude Code's own wrappers but not a message that opens with markup" {
    var arena_state = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const lines =
        \\{"type":"queue-operation"}
        \\{"type":"user","timestamp":"2026-10-06T20:12:33.250Z","message":{"role":"user","content":"Remember the word mango."}}
        \\{"type":"user","isMeta":true,"message":{"role":"user","content":"meta noise"}}
        \\{"type":"assistant","timestamp":"2026-10-06T20:12:34.000Z","message":{"id":"m1","content":[{"type":"thinking","thinking":"hm"}]}}
        \\{"type":"assistant","timestamp":"2026-10-06T20:12:35.000Z","message":{"id":"m1","content":[{"type":"text","text":"ok"}]}}
        \\{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}}
        \\{"type":"user","isSidechain":true,"message":{"role":"user","content":"side"}}
        \\{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name>"}}
        \\{"type":"user","message":{"role":"user","content":"<div>pasted markup</div> what is this?"}}
        \\{"type":"user","message":{"role":"user","content":"<system-reminder>noise</system-reminder>\nafter the reminder"}}
        \\{"type":"user","timestamp":"2026-10-06T20:13:00.000Z","message":{"role":"user","content":[{"type":"text","text":"Which word?"}]}}
        \\{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"Checking."},{"type":"tool_use","id":"t2","name":"x","input":{}}]}}
        \\{"type":"assistant","message":{"id":"m3","content":[{"type":"text","text":"mango"}]}}
    ;
    const read_turns = try turns(arena, lines);
    try std.testing.expectEqual(@as(usize, 6), read_turns.len);
    try std.testing.expectEqual(contract.NativeTurn.Role.user, read_turns[0].role);
    try std.testing.expectEqualStrings("Remember the word mango.", read_turns[0].text);
    try std.testing.expectEqual(@as(i64, 1791317553250), read_turns[0].at_ms);
    try std.testing.expectEqual(contract.NativeTurn.Role.assistant, read_turns[1].role);
    try std.testing.expectEqualStrings("ok", read_turns[1].text);
    try std.testing.expectEqualStrings("<div>pasted markup</div> what is this?", read_turns[2].text);
    try std.testing.expectEqualStrings("after the reminder", read_turns[3].text);
    try std.testing.expectEqualStrings("Which word?", read_turns[4].text);
    try std.testing.expectEqualStrings("mango", read_turns[5].text);
}

test "a transcript is found under its directory's project folder, or under any project when the directory is unknown, and an id that could leave the folder reads nothing" {
    var arena_state = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const io = std.testing.io;
    try tmp.dir.createDirPath(io, ".claude/projects/-w");
    var project = try tmp.dir.openDir(io, ".claude/projects/-w", .{});
    defer project.close(io);
    try project.writeFile(io, .{ .sub_path = "abc.jsonl", .data = "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"half writ" });
    const cwd = try std.process.currentPathAlloc(io, arena);
    const home = try std.fs.path.join(arena, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..] });
    const placed = try read(arena, home, "/w", "abc");
    try std.testing.expectEqual(@as(usize, 1), placed.len);
    try std.testing.expectEqualStrings("hello", placed[0].text);
    try std.testing.expectEqual(@as(usize, 1), (try read(arena, home, "", "abc")).len);
    try std.testing.expectEqual(@as(usize, 0), (try read(arena, home, "/w", "../-w/abc")).len);
    try std.testing.expectEqual(@as(usize, 0), (try read(arena, home, "/w", "missing")).len);
}
