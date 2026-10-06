const std = @import("std");
const builtin = @import("builtin");
const contract = @import("contract");
const compat = @import("compat");

const head_bytes: usize = 256 * 1024;
const tail_bytes: usize = 64 * 1024;
const title_limit: usize = 120;

pub fn projectDirName(arena: std.mem.Allocator, directory: []const u8) ![]u8 {
    const name = try arena.dupe(u8, directory);
    for (name) |*byte| {
        if (!std.ascii.isAlphanumeric(byte.*) and byte.* != '-') byte.* = '-';
    }
    return name;
}

const Desktop = struct {
    title: []const u8 = "",
    archived: bool = false,
    local_id: []const u8 = "",
};

pub const Paths = struct {
    home: []const u8,
    desktop_sessions: []const u8 = "",
};

pub fn list(arena: std.mem.Allocator, paths: Paths, directory: []const u8, limit: usize) ![]const contract.NativeSession {
    if (directory.len == 0 or paths.home.len == 0) return &.{};
    const project = try std.fs.path.join(arena, &.{ paths.home, ".claude", "projects", try projectDirName(arena, directory) });
    var dir = std.Io.Dir.openDirAbsolute(compat.fs.defaultIo(), project, .{ .iterate = true }) catch return &.{};
    defer dir.close(compat.fs.defaultIo());

    const live = try liveSessions(arena, paths.home);
    const desktop = try desktopRecords(arena, paths.desktop_sessions);

    var found: std.ArrayList(contract.NativeSession) = .empty;
    var iterator = dir.iterate();
    while (iterator.next(compat.fs.defaultIo()) catch null) |entry| {
        if (entry.kind != .file or !std.mem.endsWith(u8, entry.name, ".jsonl")) continue;
        const id = try arena.dupe(u8, entry.name[0 .. entry.name.len - ".jsonl".len]);
        const known = desktop.get(id);
        if (known) |record| {
            if (record.archived) continue;
        }
        const updated = compat.fs.modifiedMillis(dir, entry.name) catch 0;
        const title = if (known != null and known.?.title.len > 0) known.?.title else try transcriptTitle(arena, dir, entry.name);
        try found.append(arena, .{
            .native_id = id,
            .title = title,
            .directory = directory,
            .updated_at_ms = updated,
            .running = live.contains(id),
            .link = if (known != null and known.?.local_id.len > 0) try appLink(arena, known.?.local_id) else "",
        });
    }
    std.mem.sort(contract.NativeSession, found.items, {}, newer);
    return found.items[0..@min(found.items.len, limit)];
}

pub fn appLink(arena: std.mem.Allocator, local_id: []const u8) ![]const u8 {
    return std.fmt.allocPrint(arena, "claude://claude.ai/epitaxy/{s}", .{local_id});
}

pub fn linkFor(arena: std.mem.Allocator, desktop_sessions: []const u8, native_id: []const u8) ![]const u8 {
    const records = try desktopRecords(arena, desktop_sessions);
    const known = records.get(native_id) orelse return "";
    if (known.local_id.len == 0) return "";
    return appLink(arena, known.local_id);
}

fn newer(_: void, left: contract.NativeSession, right: contract.NativeSession) bool {
    return left.updated_at_ms > right.updated_at_ms;
}

fn liveSessions(arena: std.mem.Allocator, home: []const u8) !std.StringHashMapUnmanaged(void) {
    var live: std.StringHashMapUnmanaged(void) = .empty;
    const registry = try std.fs.path.join(arena, &.{ home, ".claude", "sessions" });
    var dir = std.Io.Dir.openDirAbsolute(compat.fs.defaultIo(), registry, .{ .iterate = true }) catch return live;
    defer dir.close(compat.fs.defaultIo());
    var iterator = dir.iterate();
    while (iterator.next(compat.fs.defaultIo()) catch null) |entry| {
        if (entry.kind != .file or !std.mem.endsWith(u8, entry.name, ".json")) continue;
        const bytes = compat.fs.readFileAlloc(arena, dir, entry.name, 64 * 1024) catch continue;
        const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, bytes, .{}) catch continue;
        if (parsed != .object) continue;
        const session = parsed.object.get("sessionId") orelse continue;
        const pid = parsed.object.get("pid") orelse continue;
        if (session != .string or pid != .integer) continue;
        if (!alive(pid.integer)) continue;
        try live.put(arena, session.string, {});
    }
    return live;
}

fn alive(pid: i64) bool {
    if (builtin.os.tag == .windows) return true;
    if (pid <= 0 or pid > std.math.maxInt(i32)) return false;
    const result = std.posix.system.kill(@intCast(pid), @enumFromInt(0));
    return switch (std.posix.errno(result)) {
        .SUCCESS, .PERM => true,
        else => false,
    };
}

fn desktopRecords(arena: std.mem.Allocator, root: []const u8) !std.StringHashMapUnmanaged(Desktop) {
    var records: std.StringHashMapUnmanaged(Desktop) = .empty;
    if (root.len == 0) return records;
    var accounts = std.Io.Dir.openDirAbsolute(compat.fs.defaultIo(), root, .{ .iterate = true }) catch return records;
    defer accounts.close(compat.fs.defaultIo());
    var account_iterator = accounts.iterate();
    while (account_iterator.next(compat.fs.defaultIo()) catch null) |account| {
        if (account.kind != .directory) continue;
        var workspaces = accounts.openDir(compat.fs.defaultIo(), account.name, .{ .iterate = true }) catch continue;
        defer workspaces.close(compat.fs.defaultIo());
        var workspace_iterator = workspaces.iterate();
        while (workspace_iterator.next(compat.fs.defaultIo()) catch null) |workspace| {
            if (workspace.kind != .directory) continue;
            var sessions = workspaces.openDir(compat.fs.defaultIo(), workspace.name, .{ .iterate = true }) catch continue;
            defer sessions.close(compat.fs.defaultIo());
            var session_iterator = sessions.iterate();
            while (session_iterator.next(compat.fs.defaultIo()) catch null) |file| {
                if (file.kind != .file or !std.mem.endsWith(u8, file.name, ".json")) continue;
                const bytes = compat.fs.readFileAlloc(arena, sessions, file.name, 4 * 1024 * 1024) catch continue;
                const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, bytes, .{}) catch continue;
                if (parsed != .object) continue;
                const cli = parsed.object.get("cliSessionId") orelse continue;
                if (cli != .string) continue;
                const title = if (parsed.object.get("title")) |given| (if (given == .string) given.string else "") else "";
                const archived = if (parsed.object.get("isArchived")) |given| (given == .bool and given.bool) else false;
                const local_id = if (parsed.object.get("sessionId")) |given| (if (given == .string) given.string else "") else "";
                try records.put(arena, cli.string, .{ .title = title, .archived = archived, .local_id = local_id });
            }
        }
    }
    return records;
}

fn transcriptTitle(arena: std.mem.Allocator, dir: std.Io.Dir, name: []const u8) ![]const u8 {
    var file = dir.openFile(compat.fs.defaultIo(), name, .{}) catch return "";
    defer file.close(compat.fs.defaultIo());
    const size = (file.stat(compat.fs.defaultIo()) catch return "").size;
    if (size > head_bytes) {
        const tail = try arena.alloc(u8, tail_bytes);
        const got = file.readPositionalAll(compat.fs.defaultIo(), tail, size - tail_bytes) catch 0;
        if (customTitle(arena, tail[0..got])) |title| return title;
    }
    const head = try arena.alloc(u8, @min(size, head_bytes));
    const got = file.readPositionalAll(compat.fs.defaultIo(), head, 0) catch 0;
    if (size <= head_bytes) {
        if (customTitle(arena, head[0..got])) |title| return title;
    }
    return firstUserLine(arena, head[0..got]);
}

fn customTitle(arena: std.mem.Allocator, bytes: []const u8) ?[]const u8 {
    var latest: ?[]const u8 = null;
    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |line| {
        if (std.mem.indexOf(u8, line, "\"custom-title\"") == null) continue;
        const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, line, .{}) catch continue;
        if (parsed != .object) continue;
        const title = parsed.object.get("customTitle") orelse continue;
        if (title != .string or title.string.len == 0 or std.mem.eql(u8, title.string, "New session")) continue;
        latest = title.string;
    }
    return latest;
}

fn withoutLeadingTags(text: []const u8) []const u8 {
    var rest = std.mem.trim(u8, text, " \t\r\n");
    while (rest.len > 1 and rest[0] == '<') {
        const name_end = std.mem.indexOfAny(u8, rest, "> \n") orelse break;
        const name = rest[1..name_end];
        if (name.len == 0 or name[0] == '/') break;
        var closing_buffer: [128]u8 = undefined;
        const closing = std.fmt.bufPrint(&closing_buffer, "</{s}>", .{name}) catch break;
        const at = std.mem.indexOf(u8, rest, closing) orelse break;
        rest = std.mem.trim(u8, rest[at + closing.len ..], " \t\r\n");
    }
    return rest;
}

fn firstUserLine(arena: std.mem.Allocator, bytes: []const u8) []const u8 {
    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |line| {
        if (std.mem.indexOf(u8, line, "\"type\":\"user\"") == null) continue;
        const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, line, .{}) catch continue;
        if (parsed != .object) continue;
        const message = parsed.object.get("message") orelse continue;
        if (message != .object) continue;
        const content = message.object.get("content") orelse continue;
        const text = switch (content) {
            .string => |plain| plain,
            .array => |parts| blk: {
                for (parts.items) |part| {
                    if (part != .object) continue;
                    const piece = part.object.get("text") orelse continue;
                    if (piece == .string) break :blk piece.string;
                }
                continue;
            },
            else => continue,
        };
        const trimmed = withoutLeadingTags(text);
        if (trimmed.len == 0 or trimmed[0] == '<') continue;
        const first = trimmed[0 .. std.mem.indexOfScalar(u8, trimmed, '\n') orelse trimmed.len];
        var cut = @min(first.len, title_limit);
        while (cut > 0 and cut < first.len and first[cut] & 0xC0 == 0x80) cut -= 1;
        return first[0..cut];
    }
    return "";
}

test "a working directory maps to the project folder Claude Code names after it" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    try std.testing.expectEqualStrings("-Users-me-work--claude-worktrees-a-b", try projectDirName(arena.allocator(), "/Users/me/work/.claude/worktrees/a-b"));
}

test "a project's transcripts list newest first, titled by the latest custom title or the first user line, and an archived one is left out" {
    var arena_state = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const io = std.testing.io;
    try tmp.dir.createDirPath(io, ".claude/projects/-w");
    try tmp.dir.createDirPath(io, "desktop/account/workspace");
    var project = try tmp.dir.openDir(io, ".claude/projects/-w", .{});
    defer project.close(io);
    try project.writeFile(io, .{ .sub_path = "aaa.jsonl", .data = "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"<command-name>/init</command-name>\"}}\n{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"<system-reminder>\\nnoise\\n</system-reminder>\\nfix the parser\\nplease\"}]}}\n" });
    try project.writeFile(io, .{ .sub_path = "bbb.jsonl", .data = "{\"type\":\"custom-title\",\"customTitle\":\"New session\"}\n{\"type\":\"custom-title\",\"customTitle\":\"Named work\"}\n" });
    try project.writeFile(io, .{ .sub_path = "ccc.jsonl", .data = "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"archived\"}}\n" });
    try project.writeFile(io, .{ .sub_path = "notes.txt", .data = "ignored" });
    var workspace = try tmp.dir.openDir(io, "desktop/account/workspace", .{});
    defer workspace.close(io);
    try workspace.writeFile(io, .{ .sub_path = "local_c.json", .data = "{\"cliSessionId\":\"ccc\",\"title\":\"gone\",\"isArchived\":true}" });
    try workspace.writeFile(io, .{ .sub_path = "local_b.json", .data = "{\"sessionId\":\"local_b\",\"cliSessionId\":\"bbb\",\"title\":\"\",\"isArchived\":false}" });

    const root = try tmp.dir.realPathFileAlloc(io, ".", arena);
    const listed = try list(arena, .{ .home = root, .desktop_sessions = try std.fs.path.join(arena, &.{ root, "desktop" }) }, "/w", 10);
    try std.testing.expectEqual(@as(usize, 2), listed.len);
    var titles: [2][]const u8 = undefined;
    for (listed, 0..) |session, index| {
        titles[index] = session.title;
        if (std.mem.eql(u8, session.native_id, "bbb")) try std.testing.expectEqualStrings("claude://claude.ai/epitaxy/local_b", session.link);
        if (std.mem.eql(u8, session.native_id, "aaa")) try std.testing.expectEqualStrings("", session.link);
        try std.testing.expectEqualStrings("/w", session.directory);
        try std.testing.expect(!session.running);
    }
    std.mem.sort([]const u8, &titles, {}, struct {
        fn less(_: void, a: []const u8, b: []const u8) bool {
            return std.mem.lessThan(u8, a, b);
        }
    }.less);
    try std.testing.expectEqualStrings("Named work", titles[0]);
    try std.testing.expectEqualStrings("fix the parser", titles[1]);
}
