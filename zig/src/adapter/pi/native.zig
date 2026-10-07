const std = @import("std");
const contract = @import("contract");
const compat = @import("compat");

const head_bytes: usize = 256 * 1024;
const read_limit: u64 = 64 * 1024 * 1024;
const title_limit: usize = 120;

pub fn storeDirName(arena: std.mem.Allocator, directory: []const u8) ![]u8 {
    const trimmed = std.mem.trimStart(u8, directory, "/\\");
    const name = try std.fmt.allocPrint(arena, "--{s}--", .{trimmed});
    for (name[2 .. name.len - 2]) |*byte| {
        if (byte.* == '/' or byte.* == '\\' or byte.* == ':') byte.* = '-';
    }
    return name;
}

pub fn agentDir(arena: std.mem.Allocator, environment: []const []const u8, home: []const u8) ![]const u8 {
    for (environment) |entry| {
        const prefix = "PI_CODING_AGENT_DIR=";
        if (std.mem.startsWith(u8, entry, prefix) and entry.len > prefix.len) return entry[prefix.len..];
    }
    if (home.len == 0) return "";
    return std.fs.path.join(arena, &.{ home, ".pi", "agent" });
}

const Binding = struct {
    sessionId: []const u8,
    sessionFile: []const u8,
};

pub fn list(arena: std.mem.Allocator, agent_dir: []const u8, directory: []const u8, limit: usize) ![]const contract.NativeSession {
    if (agent_dir.len == 0 or directory.len == 0) return &.{};
    const store = try std.fs.path.join(arena, &.{ agent_dir, "sessions", try storeDirName(arena, directory) });
    var dir = std.Io.Dir.openDirAbsolute(compat.fs.defaultIo(), store, .{ .iterate = true }) catch return &.{};
    defer dir.close(compat.fs.defaultIo());
    var found: std.ArrayList(contract.NativeSession) = .empty;
    var iterator = dir.iterate();
    while (iterator.next(compat.fs.defaultIo()) catch null) |entry| {
        if (entry.kind != .file or !std.mem.endsWith(u8, entry.name, ".jsonl")) continue;
        const head = try readHead(arena, dir, entry.name) orelse continue;
        const header = firstLine(arena, head) orelse continue;
        if (!std.mem.eql(u8, text(header.get("type")) orelse "", "session")) continue;
        const id = text(header.get("id")) orelse continue;
        const path = try std.fs.path.join(arena, &.{ store, entry.name });
        try found.append(arena, .{
            .native_id = try std.json.Stringify.valueAlloc(arena, Binding{ .sessionId = id, .sessionFile = path }, .{}),
            .title = try titleOf(arena, head),
            .directory = text(header.get("cwd")) orelse directory,
            .updated_at_ms = compat.fs.modifiedMillis(dir, entry.name) catch 0,
        });
    }
    std.mem.sort(contract.NativeSession, found.items, {}, newer);
    return found.items[0..@min(found.items.len, limit)];
}

fn newer(_: void, left: contract.NativeSession, right: contract.NativeSession) bool {
    return left.updated_at_ms > right.updated_at_ms;
}

fn readHead(arena: std.mem.Allocator, dir: std.Io.Dir, name: []const u8) !?[]const u8 {
    var file = dir.openFile(compat.fs.defaultIo(), name, .{}) catch return null;
    defer file.close(compat.fs.defaultIo());
    const size = (file.stat(compat.fs.defaultIo()) catch return null).size;
    const bytes = try arena.alloc(u8, @intCast(@min(size, head_bytes)));
    const got = file.readPositionalAll(compat.fs.defaultIo(), bytes, 0) catch return null;
    return bytes[0..got];
}

fn firstLine(arena: std.mem.Allocator, bytes: []const u8) ?std.json.ObjectMap {
    const end = std.mem.indexOfScalar(u8, bytes, '\n') orelse bytes.len;
    const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, bytes[0..end], .{}) catch return null;
    return if (parsed == .object) parsed.object else null;
}

fn titleOf(arena: std.mem.Allocator, bytes: []const u8) ![]const u8 {
    var named: ?[]const u8 = null;
    var first_user: ?[]const u8 = null;
    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |line| {
        if (std.mem.indexOf(u8, line, "\"session_info\"") == null and (first_user != null or std.mem.indexOf(u8, line, "\"user\"") == null)) continue;
        const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, line, .{}) catch continue;
        if (parsed != .object) continue;
        const kind = text(parsed.object.get("type")) orelse continue;
        if (std.mem.eql(u8, kind, "session_info")) {
            if (text(parsed.object.get("name"))) |name| {
                if (name.len > 0) named = name;
            }
        } else if (std.mem.eql(u8, kind, "message") and first_user == null) {
            const message = parsed.object.get("message") orelse continue;
            if (message != .object or !std.mem.eql(u8, text(message.object.get("role")) orelse "", "user")) continue;
            first_user = try textParts(arena, message.object.get("content"), "\n");
        }
    }
    const chosen = named orelse first_user orelse return "";
    const trimmed = std.mem.trim(u8, chosen, " \t\r\n");
    const line = trimmed[0 .. std.mem.indexOfScalar(u8, trimmed, '\n') orelse trimmed.len];
    var cut = @min(line.len, title_limit);
    while (cut > 0 and cut < line.len and line[cut] & 0xC0 == 0x80) cut -= 1;
    return line[0..cut];
}

pub fn read(arena: std.mem.Allocator, native_id: []const u8) ![]const contract.NativeTurn {
    const binding = std.json.parseFromSliceLeaky(Binding, arena, native_id, .{ .ignore_unknown_fields = true }) catch return &.{};
    if (!std.fs.path.isAbsolute(binding.sessionFile) or !std.mem.endsWith(u8, binding.sessionFile, ".jsonl")) return &.{};
    var dir = std.Io.Dir.openDirAbsolute(compat.fs.defaultIo(), std.fs.path.dirname(binding.sessionFile) orelse return &.{}, .{}) catch return &.{};
    defer dir.close(compat.fs.defaultIo());
    var file = dir.openFile(compat.fs.defaultIo(), std.fs.path.basename(binding.sessionFile), .{}) catch return &.{};
    defer file.close(compat.fs.defaultIo());
    const size = (file.stat(compat.fs.defaultIo()) catch return &.{}).size;
    const start = if (size > read_limit) size - read_limit else 0;
    const bytes = try arena.alloc(u8, @intCast(size - start));
    const got = file.readPositionalAll(compat.fs.defaultIo(), bytes, start) catch return &.{};
    var whole = bytes[0..got];
    if (start > 0) {
        whole = whole[(std.mem.indexOfScalar(u8, whole, '\n') orelse return &.{}) + 1 ..];
    } else {
        const header = firstLine(arena, whole) orelse return &.{};
        if (!std.mem.eql(u8, text(header.get("id")) orelse "", binding.sessionId)) return &.{};
    }
    return turns(arena, whole[0..(std.mem.lastIndexOfScalar(u8, whole, '\n') orelse 0)]);
}

pub fn turns(arena: std.mem.Allocator, bytes: []const u8) ![]const contract.NativeTurn {
    var found: std.ArrayList(contract.NativeTurn) = .empty;
    var reply: []const u8 = "";
    var reply_at: i64 = 0;
    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |line| {
        if (line.len == 0) continue;
        const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, line, .{}) catch continue;
        if (parsed != .object or !std.mem.eql(u8, text(parsed.object.get("type")) orelse "", "message")) continue;
        const message = parsed.object.get("message") orelse continue;
        if (message != .object) continue;
        const role = text(message.object.get("role")) orelse continue;
        const at = compat.time.isoMillis(text(parsed.object.get("timestamp")) orelse "") orelse 0;
        if (std.mem.eql(u8, role, "user")) {
            const said = std.mem.trim(u8, try textParts(arena, message.object.get("content"), "\n"), " \t\r\n");
            if (said.len == 0) continue;
            if (reply.len > 0) try found.append(arena, .{ .role = .assistant, .text = reply, .at_ms = reply_at });
            reply = "";
            try found.append(arena, .{ .role = .user, .text = said, .at_ms = at });
        } else if (std.mem.eql(u8, role, "assistant")) {
            const piece = try textParts(arena, message.object.get("content"), "");
            if (piece.len == 0) continue;
            reply = piece;
            reply_at = at;
        }
    }
    if (reply.len > 0) try found.append(arena, .{ .role = .assistant, .text = reply, .at_ms = reply_at });
    return found.items;
}

fn text(value: ?std.json.Value) ?[]const u8 {
    const present = value orelse return null;
    return if (present == .string) present.string else null;
}

fn textParts(arena: std.mem.Allocator, content: ?std.json.Value, separator: []const u8) ![]const u8 {
    const body = content orelse return "";
    switch (body) {
        .string => |plain| return plain,
        .array => |parts| {
            var joined: std.ArrayList(u8) = .empty;
            for (parts.items) |part| {
                if (part != .object or !std.mem.eql(u8, text(part.object.get("type")) orelse "", "text")) continue;
                const piece = text(part.object.get("text")) orelse continue;
                if (joined.items.len > 0) try joined.appendSlice(arena, separator);
                try joined.appendSlice(arena, piece);
            }
            return joined.items;
        },
        else => return "",
    }
}

test "a working directory maps to the store folder Pi names after it" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    try std.testing.expectEqualStrings("--Users-me-work-a-b--", try storeDirName(arena.allocator(), "/Users/me/work/a:b"));
}

test "a session file reads as its user messages and the last reply before each, leaving out thinking and tool results" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const lines =
        \\{"type":"session","version":3,"id":"abc","timestamp":"2026-03-28T17:06:36.479Z","cwd":"/w"}
        \\{"type":"model_change","id":"m1","parentId":null}
        \\{"type":"message","id":"1","timestamp":"2026-03-28T17:06:52.478Z","message":{"role":"user","content":[{"type":"text","text":"what tools?"}]}}
        \\{"type":"message","id":"2","timestamp":"2026-03-28T17:06:57.760Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"Let me look."}]}}
        \\{"type":"message","id":"3","message":{"role":"toolResult","content":[{"type":"text","text":"ls output"}]}}
        \\{"type":"message","id":"4","timestamp":"2026-03-28T17:07:11.954Z","message":{"role":"assistant","content":[{"type":"text","text":"None are "},{"type":"text","text":"installed."}]}}
        \\{"type":"message","id":"5","message":{"role":"user","content":"thanks"}}
    ;
    const read_turns = try turns(arena.allocator(), lines);
    try std.testing.expectEqual(@as(usize, 3), read_turns.len);
    try std.testing.expectEqualStrings("what tools?", read_turns[0].text);
    try std.testing.expectEqual(@as(i64, 1774717612478), read_turns[0].at_ms);
    try std.testing.expectEqual(contract.NativeTurn.Role.assistant, read_turns[1].role);
    try std.testing.expectEqualStrings("None are installed.", read_turns[1].text);
    try std.testing.expectEqualStrings("thanks", read_turns[2].text);
}

test "a project's session files list newest first, named by session_info or the first user message, with an id a reopen accepts, and read back through it" {
    var arena_state = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const io = std.testing.io;
    try tmp.dir.createDirPath(io, "agent/sessions/--w--");
    var store = try tmp.dir.openDir(io, "agent/sessions/--w--", .{});
    defer store.close(io);
    try store.writeFile(io, .{ .sub_path = "a.jsonl", .data = "{\"type\":\"session\",\"id\":\"aaa\",\"cwd\":\"/w\"}\n{\"type\":\"message\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"fix the parser\\nplease\"}]}}\n" });
    try store.writeFile(io, .{ .sub_path = "b.jsonl", .data = "{\"type\":\"session\",\"id\":\"bbb\",\"cwd\":\"/w\"}\n{\"type\":\"session_info\",\"name\":\"Named work\"}\n" });
    try store.writeFile(io, .{ .sub_path = "c.jsonl", .data = "not a session\n" });
    const cwd = try std.process.currentPathAlloc(io, arena);
    const agent = try std.fs.path.join(arena, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "agent" });
    const listed = try list(arena, agent, "/w", 10);
    try std.testing.expectEqual(@as(usize, 2), listed.len);
    var titles = [_][]const u8{ listed[0].title, listed[1].title };
    std.mem.sort([]const u8, &titles, {}, struct {
        fn less(_: void, left: []const u8, right: []const u8) bool {
            return std.mem.lessThan(u8, left, right);
        }
    }.less);
    try std.testing.expectEqualStrings("Named work", titles[0]);
    try std.testing.expectEqualStrings("fix the parser", titles[1]);
    const parser = if (std.mem.eql(u8, listed[0].title, "fix the parser")) listed[0] else listed[1];
    const binding = try std.json.parseFromSliceLeaky(Binding, arena, parser.native_id, .{});
    try std.testing.expectEqualStrings("aaa", binding.sessionId);
    try std.testing.expect(std.mem.endsWith(u8, binding.sessionFile, "/agent/sessions/--w--/a.jsonl"));
    const read_back = try read(arena, parser.native_id);
    try std.testing.expectEqual(@as(usize, 1), read_back.len);
    try std.testing.expectEqualStrings("fix the parser\nplease", read_back[0].text);
    try std.testing.expectEqual(@as(usize, 0), (try read(arena, "{\"sessionId\":\"other\",\"sessionFile\":\"" ++ "/nowhere/x.jsonl\"}")).len);
    try std.testing.expectEqual(@as(usize, 0), (try list(arena, agent, "/elsewhere", 10)).len);
}

test "a session file larger than the read limit reads its tail" {
    var arena_state = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const io = std.testing.io;
    const filler = "{\"type\":\"message\",\"message\":{\"role\":\"toolResult\",\"content\":\"" ++ "x" ** 1024 ++ "\"}}\n";
    var data: std.ArrayList(u8) = .empty;
    try data.appendSlice(arena, "{\"type\":\"session\",\"id\":\"big\"}\n");
    while (data.items.len < read_limit + 4096) try data.appendSlice(arena, filler);
    try data.appendSlice(arena, "{\"type\":\"message\",\"message\":{\"role\":\"user\",\"content\":\"the last word\"}}\n");
    try tmp.dir.writeFile(io, .{ .sub_path = "big.jsonl", .data = data.items });
    const cwd = try std.process.currentPathAlloc(io, arena);
    const path = try std.fs.path.join(arena, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "big.jsonl" });
    const read_back = try read(arena, try std.json.Stringify.valueAlloc(arena, Binding{ .sessionId = "big", .sessionFile = path }, .{}));
    try std.testing.expectEqual(@as(usize, 1), read_back.len);
    try std.testing.expectEqualStrings("the last word", read_back[0].text);
}
