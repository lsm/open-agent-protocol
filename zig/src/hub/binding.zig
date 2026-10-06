const std = @import("std");
const compat = @import("compat");

pub const Action = enum { opened, reopened, closed, refused };

pub const CompactionPolicy = struct {
    kind: []const u8,
    share_percent: ?i64 = null,
    tokens: ?i64 = null,
};

pub const Record = struct {
    session_id: []const u8,
    adapter: []const u8,
    harness_version: []const u8 = "",
    native_session_id: []const u8 = "",
    home: []const u8 = "",
    directory: []const u8 = "",
    model: []const u8 = "",
    reasoning_level: []const u8 = "",
    compaction_policy: ?CompactionPolicy = null,
    tool_source_ids: []const []const u8 = &.{},
    adopted: bool = false,
};

pub const Entry = struct {
    action: Action,
    time_ms: i64,
    record: Record,
};

pub const max_store_bytes: usize = 16 * 1024 * 1024;

const WireRecord = struct {
    session_id: []const u8,
    adapter: []const u8,
    harness_version: ?[]const u8 = null,
    native_session_id: ?[]const u8 = null,
    home: ?[]const u8 = null,
    directory: ?[]const u8 = null,
    model: ?[]const u8 = null,
    reasoning_level: ?[]const u8 = null,
    compaction_policy: ?CompactionPolicy = null,
    tool_source_ids: ?[]const []const u8 = null,
    adopted: ?bool = null,
};

const WireEntry = struct {
    action: Action,
    time_ms: i64,
    record: WireRecord,
};

fn present(value: []const u8) ?[]const u8 {
    return if (value.len > 0) value else null;
}

pub fn encode(allocator: std.mem.Allocator, entry: Entry) ![]u8 {
    const wire = WireEntry{
        .action = entry.action,
        .time_ms = entry.time_ms,
        .record = .{
            .session_id = entry.record.session_id,
            .adapter = entry.record.adapter,
            .harness_version = present(entry.record.harness_version),
            .native_session_id = present(entry.record.native_session_id),
            .home = present(entry.record.home),
            .directory = present(entry.record.directory),
            .model = present(entry.record.model),
            .reasoning_level = present(entry.record.reasoning_level),
            .compaction_policy = entry.record.compaction_policy,
            .tool_source_ids = if (entry.record.tool_source_ids.len > 0) entry.record.tool_source_ids else null,
            .adopted = if (entry.record.adopted) true else null,
        },
    };
    const payload = try std.json.Stringify.valueAlloc(allocator, wire, .{ .emit_null_optional_fields = false });
    defer allocator.free(payload);
    return std.fmt.allocPrint(allocator, "{x:0>8} {s}\n", .{ std.hash.Crc32.hash(payload), payload });
}

pub fn decode(arena: std.mem.Allocator, line: []const u8) !Entry {
    const body = std.mem.trimEnd(u8, line, "\n");
    const space = std.mem.indexOfScalar(u8, body, ' ') orelse return error.TornRecord;
    const sum = body[0..space];
    const payload = body[space + 1 ..];
    if (sum.len != 8) return error.TornRecord;
    for (sum) |digit| {
        if (!std.ascii.isDigit(digit) and (digit < 'a' or digit > 'f')) return error.TornRecord;
    }
    const stated = std.fmt.parseInt(u32, sum, 16) catch return error.TornRecord;
    if (stated != std.hash.Crc32.hash(payload)) return error.TornRecord;
    const wire = std.json.parseFromSliceLeaky(WireEntry, arena, payload, .{ .ignore_unknown_fields = true }) catch |err| {
        if (err == error.OutOfMemory) return error.OutOfMemory;
        return error.TornRecord;
    };
    return .{
        .action = wire.action,
        .time_ms = wire.time_ms,
        .record = .{
            .session_id = wire.record.session_id,
            .adapter = wire.record.adapter,
            .harness_version = wire.record.harness_version orelse "",
            .native_session_id = wire.record.native_session_id orelse "",
            .home = wire.record.home orelse "",
            .directory = wire.record.directory orelse "",
            .model = wire.record.model orelse "",
            .reasoning_level = wire.record.reasoning_level orelse "",
            .compaction_policy = wire.record.compaction_policy,
            .tool_source_ids = wire.record.tool_source_ids orelse &.{},
            .adopted = wire.record.adopted orelse false,
        },
    };
}

pub fn validPrefix(allocator: std.mem.Allocator, bytes: []const u8) !usize {
    var scratch = std.heap.ArenaAllocator.init(allocator);
    defer scratch.deinit();
    var keep: usize = 0;
    while (keep < bytes.len) {
        const end = std.mem.indexOfScalarPos(u8, bytes, keep, '\n') orelse return keep;
        _ = decode(scratch.allocator(), bytes[keep .. end + 1]) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            return keep;
        };
        keep = end + 1;
    }
    return keep;
}

pub const Store = struct {
    allocator: std.mem.Allocator,
    path: []u8,
    staging: []u8,
    last_failure: ?anyerror = null,
    held: ?compat.fs.File = null,

    pub fn open(allocator: std.mem.Allocator, path: []const u8) !Store {
        if (path.len == 0) return error.BindingStoreNeedsAPath;
        if (std.fs.path.dirname(path)) |dir| try compat.fs.createDir(compat.fs.getCwd(), dir);
        const owned = try allocator.dupe(u8, path);
        errdefer allocator.free(owned);
        const staging = try std.fmt.allocPrint(allocator, "{s}.writing", .{path});
        errdefer allocator.free(staging);
        const lock_path = try std.fmt.allocPrint(allocator, "{s}.lock", .{path});
        defer allocator.free(lock_path);
        const held = compat.fs.getCwd().createFile(compat.fs.defaultIo(), lock_path, .{ .truncate = false, .lock = .exclusive, .lock_nonblocking = true, .permissions = compat.fs.default_file_mode }) catch |err| switch (err) {
            error.WouldBlock => return error.SessionHistoryInUse,
            else => return err,
        };
        errdefer held.close(compat.fs.defaultIo());
        var store = Store{ .allocator = allocator, .path = owned, .staging = staging, .held = held };
        try store.rewrite(null);
        return store;
    }

    pub fn deinit(self: *Store) void {
        if (self.held) |held| held.close(compat.fs.defaultIo());
        self.allocator.free(self.path);
        self.allocator.free(self.staging);
        self.* = undefined;
    }

    fn contents(self: *Store) ![]u8 {
        return compat.fs.readFileAlloc(self.allocator, compat.fs.getCwd(), self.path, max_store_bytes) catch |err| switch (err) {
            error.FileNotFound => try self.allocator.alloc(u8, 0),
            else => return err,
        };
    }

    fn rewrite(self: *Store, line: ?[]const u8) !void {
        const bytes = try self.contents();
        defer self.allocator.free(bytes);
        const keep = try validPrefix(self.allocator, bytes);
        const exists = compat.fs.fileKind(compat.fs.getCwd(), self.path) == .file;
        if (line == null and keep == bytes.len and exists) return;
        const next = try std.mem.concat(self.allocator, u8, &.{ bytes[0..keep], line orelse "" });
        defer self.allocator.free(next);
        try compat.fs.atomicReplace(compat.fs.getCwd(), self.path, self.staging, next);
    }

    pub fn append(self: *Store, entry: Entry) !void {
        self.write(entry) catch |err| {
            self.last_failure = err;
            return err;
        };
        self.last_failure = null;
    }

    fn write(self: *Store, entry: Entry) !void {
        const line = try encode(self.allocator, entry);
        defer self.allocator.free(line);
        try self.rewrite(line);
    }

    pub fn latest(self: *Store, arena: std.mem.Allocator, session_id: []const u8) !?Entry {
        const bytes = compat.fs.readFileAlloc(arena, compat.fs.getCwd(), self.path, max_store_bytes) catch |err| switch (err) {
            error.FileNotFound => return null,
            else => return err,
        };
        var found: ?Entry = null;
        var start: usize = 0;
        while (start < bytes.len) {
            const end = std.mem.indexOfScalarPos(u8, bytes, start, '\n') orelse return error.TornRecord;
            const entry = try decode(arena, bytes[start .. end + 1]);
            if (std.mem.eql(u8, entry.record.session_id, session_id)) found = entry;
            start = end + 1;
        }
        return found;
    }

    pub fn sessions(self: *Store, arena: std.mem.Allocator) ![]Entry {
        const bytes = compat.fs.readFileAlloc(arena, compat.fs.getCwd(), self.path, max_store_bytes) catch |err| switch (err) {
            error.FileNotFound => return &.{},
            else => return err,
        };
        var entries: std.ArrayList(Entry) = .empty;
        var start: usize = 0;
        while (start < bytes.len) {
            const end = std.mem.indexOfScalarPos(u8, bytes, start, '\n') orelse return error.TornRecord;
            try entries.append(arena, try decode(arena, bytes[start .. end + 1]));
            start = end + 1;
        }
        return latestEach(arena, entries.items);
    }
};

pub const default_limit: usize = 50;
pub const max_limit: usize = 100;

pub fn latestEach(arena: std.mem.Allocator, entries: []const Entry) ![]Entry {
    var states: std.ArrayList(Entry) = .empty;
    var at: std.StringHashMapUnmanaged(usize) = .empty;
    for (entries) |entry| {
        if (entry.action == .refused) continue;
        if (at.get(entry.record.session_id)) |index| {
            states.items[index] = entry;
            continue;
        }
        try at.put(arena, entry.record.session_id, states.items.len);
        try states.append(arena, entry);
    }
    return states.items;
}

pub const Page = struct {
    entries: []const Entry,
    next_cursor: ?[]const u8 = null,
};

pub fn list(arena: std.mem.Allocator, sessions: []const Entry, cursor: ?[]const u8, limit: usize) !Page {
    const bound = if (limit == 0) default_limit else limit;
    if (bound > max_limit) return error.InvalidLimit;
    const ordered = try arena.dupe(Entry, sessions);
    std.mem.sort(Entry, ordered, {}, before);
    var start: usize = 0;
    if (cursor) |given| if (given.len > 0) {
        const after = try decodeCursor(arena, given);
        while (start < ordered.len and !before({}, after, ordered[start])) start += 1;
    };
    const end = @min(start + bound, ordered.len);
    var page = Page{ .entries = ordered[start..end] };
    if (end < ordered.len) page.next_cursor = try encodeCursor(arena, ordered[end - 1]);
    return page;
}

fn before(_: void, a: Entry, b: Entry) bool {
    if (a.time_ms != b.time_ms) return a.time_ms > b.time_ms;
    return std.mem.lessThan(u8, a.record.session_id, b.record.session_id);
}

const cursor_codec = std.base64.url_safe_no_pad;

fn encodeCursor(arena: std.mem.Allocator, entry: Entry) ![]const u8 {
    const raw = try std.fmt.allocPrint(arena, "{d}:{s}", .{ entry.time_ms, entry.record.session_id });
    const out = try arena.alloc(u8, cursor_codec.Encoder.calcSize(raw.len));
    return cursor_codec.Encoder.encode(out, raw);
}

fn decodeCursor(arena: std.mem.Allocator, cursor: []const u8) !Entry {
    const size = cursor_codec.Decoder.calcSizeForSlice(cursor) catch return error.InvalidCursor;
    const raw = try arena.alloc(u8, size);
    cursor_codec.Decoder.decode(raw, cursor) catch return error.InvalidCursor;
    const colon = std.mem.indexOfScalar(u8, raw, ':') orelse return error.InvalidCursor;
    if (colon + 1 == raw.len) return error.InvalidCursor;
    const time_ms = std.fmt.parseInt(i64, raw[0..colon], 10) catch return error.InvalidCursor;
    return .{ .action = .opened, .time_ms = time_ms, .record = .{ .session_id = raw[colon + 1 ..], .adapter = "" } };
}

const testing = std.testing;

fn scratchPath(allocator: std.mem.Allocator, tmp: *std.testing.TmpDir, name: []const u8) ![]u8 {
    const cwd = try std.process.currentPathAlloc(testing.io, allocator);
    defer allocator.free(cwd);
    return std.fs.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], name });
}

test "a record line is the checksum of its JSON and the JSON, as the Go store writes it" {
    const line = try encode(testing.allocator, .{ .action = .opened, .time_ms = 7, .record = .{ .session_id = "s", .adapter = "codex", .native_session_id = "thread-1" } });
    defer testing.allocator.free(line);
    try testing.expectEqualStrings("c66c4573 {\"action\":\"opened\",\"time_ms\":7,\"record\":{\"session_id\":\"s\",\"adapter\":\"codex\",\"native_session_id\":\"thread-1\"}}\n", line);
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const decoded = try decode(arena.allocator(), line);
    try testing.expectEqualStrings("thread-1", decoded.record.native_session_id);
    try testing.expectEqual(Action.opened, decoded.action);
}

test "the open's settings are written as the Go store writes them" {
    const line = try encode(testing.allocator, .{ .action = .opened, .time_ms = 7, .record = .{ .session_id = "s", .adapter = "pi", .model = "m", .reasoning_level = "high", .compaction_policy = .{ .kind = "share", .share_percent = 80 } } });
    defer testing.allocator.free(line);
    try testing.expectEqualStrings("b3827233 {\"action\":\"opened\",\"time_ms\":7,\"record\":{\"session_id\":\"s\",\"adapter\":\"pi\",\"model\":\"m\",\"reasoning_level\":\"high\",\"compaction_policy\":{\"kind\":\"share\",\"share_percent\":80}}}\n", line);
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const decoded = try decode(arena.allocator(), line);
    try testing.expectEqualStrings("high", decoded.record.reasoning_level);
    try testing.expectEqual(@as(?i64, 80), decoded.record.compaction_policy.?.share_percent);
}

test "a binding recorded before a restart is read after it, the latest entry winning" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "state/bindings.jsonl");
    defer testing.allocator.free(path);
    {
        var store = try Store.open(testing.allocator, path);
        defer store.deinit();
        try store.append(.{ .action = .opened, .time_ms = 1, .record = .{ .session_id = "s", .adapter = "pi", .native_session_id = "old" } });
        try store.append(.{ .action = .opened, .time_ms = 2, .record = .{ .session_id = "other", .adapter = "pi" } });
        try store.append(.{ .action = .closed, .time_ms = 3, .record = .{ .session_id = "s", .adapter = "pi", .native_session_id = "kept" } });
    }
    var reopened = try Store.open(testing.allocator, path);
    defer reopened.deinit();
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const entry = (try reopened.latest(arena.allocator(), "s")).?;
    try testing.expectEqual(Action.closed, entry.action);
    try testing.expectEqualStrings("kept", entry.record.native_session_id);
    try testing.expect((try reopened.latest(arena.allocator(), "absent")) == null);
}

test "a torn record is detected rather than read, and the next open keeps only the whole prefix" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "bindings.jsonl");
    defer testing.allocator.free(path);
    const whole = try encode(testing.allocator, .{ .action = .opened, .time_ms = 1, .record = .{ .session_id = "s", .adapter = "pi", .native_session_id = "n" } });
    defer testing.allocator.free(whole);
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    const torn = [_][]const u8{
        "{\"action\":\"opened\",\"time_ms\":2",
        "00000000 {\"action\":\"opened\",\"time_ms\":2,\"record\":{\"session_id\":\"s\",\"adapter\":\"pi\"}}\n",
    };
    for (torn) |tail| {
        const bytes = try std.mem.concat(testing.allocator, u8, &.{ whole, tail });
        defer testing.allocator.free(bytes);
        try compat.fs.writeFile(compat.fs.getCwd(), path, bytes);
        var reader = Store{ .allocator = testing.allocator, .path = path, .staging = "" };
        try testing.expectError(error.TornRecord, reader.latest(arena.allocator(), "s"));

        var repaired = try Store.open(testing.allocator, path);
        defer repaired.deinit();
        const kept = try compat.fs.readFileAlloc(testing.allocator, compat.fs.getCwd(), path, max_store_bytes);
        defer testing.allocator.free(kept);
        try testing.expectEqualStrings(whole, kept);
        try testing.expectEqualStrings("n", (try repaired.latest(arena.allocator(), "s")).?.record.native_session_id);
    }
}

test "a second store on one session history is refused while the first holds it, and taken once it closes" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "sessions.jsonl");
    defer testing.allocator.free(path);
    var first = try Store.open(testing.allocator, path);
    try testing.expectError(error.SessionHistoryInUse, Store.open(testing.allocator, path));
    first.deinit();
    var second = try Store.open(testing.allocator, path);
    second.deinit();
}

fn opened(time_ms: i64, session_id: []const u8) Entry {
    return .{ .action = .opened, .time_ms = time_ms, .record = .{ .session_id = session_id, .adapter = "memory" } };
}

test "the session list keeps each session's latest entry that is not refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var closed = opened(4, "a");
    closed.action = .closed;
    var refused = opened(5, "a");
    refused.action = .refused;
    var only_refused = opened(2, "only-refused");
    only_refused.action = .refused;
    const states = try latestEach(arena.allocator(), &.{ opened(1, "a"), only_refused, opened(3, "b"), closed, refused });
    try testing.expectEqual(@as(usize, 2), states.len);
    try testing.expectEqualStrings("a", states[0].record.session_id);
    try testing.expectEqual(Action.closed, states[0].action);
    try testing.expectEqual(@as(i64, 4), states[0].time_ms);
    try testing.expectEqualStrings("b", states[1].record.session_id);
}

test "a session list is newest first, ties broken by session id, and pages on the cursor Go issues" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const sessions = [_]Entry{ opened(1, "old"), opened(5, "tie-b"), opened(9, "new"), opened(5, "tie-a") };
    const whole = try list(a, &sessions, null, 0);
    try testing.expectEqual(@as(usize, 4), whole.entries.len);
    try testing.expect(whole.next_cursor == null);
    const order = [_][]const u8{ "new", "tie-a", "tie-b", "old" };
    for (order, whole.entries) |want, got| try testing.expectEqualStrings(want, got.record.session_id);

    const first = try list(a, &sessions, null, 2);
    try testing.expectEqualStrings("NTp0aWUtYQ", first.next_cursor.?);
    const second = try list(a, &sessions, first.next_cursor, 2);
    try testing.expectEqualStrings("tie-b", second.entries[0].record.session_id);
    try testing.expectEqualStrings("old", second.entries[1].record.session_id);
    try testing.expect(second.next_cursor == null);
}

test "a session list defaults its limit, refuses one past the ceiling, and refuses a cursor it did not issue" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const sessions = try a.alloc(Entry, default_limit + 1);
    for (sessions, 0..) |*slot, index| slot.* = opened(@intCast(index), try std.fmt.allocPrint(a, "s{d}", .{index}));
    const page = try list(a, sessions, null, 0);
    try testing.expectEqual(default_limit, page.entries.len);
    try testing.expect(page.next_cursor != null);
    try testing.expectError(error.InvalidLimit, list(a, sessions, null, max_limit + 1));
    _ = try list(a, sessions, null, max_limit);
    for ([_][]const u8{ "not base64!", "bm8tY29sb24", "eDpzZXNzaW9u", "MTI6" }) |cursor| {
        try testing.expectError(error.InvalidCursor, list(a, sessions, cursor, 0));
    }
}
