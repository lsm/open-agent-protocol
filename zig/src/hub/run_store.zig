const std = @import("std");
const compat = @import("compat");
const binding = @import("binding.zig");

pub const Action = enum { admitted, started, settled };

pub const Record = struct {
    action: Action,
    time_ms: i64,
    session_id: []const u8,
    run_id: []const u8,
    request: ?[]const u8 = null,
    replaces: ?[]const u8 = null,
    root: ?[]const u8 = null,
};

pub const Open = struct {
    run_id: []const u8,
    admitted_ms: i64,
    started: bool,
    request: ?[]const u8,
    root: []const u8,
};

pub const Runs = struct {
    ctx: *anyopaque,
    vtable: *const VTable,

    pub const VTable = struct {
        append: *const fn (ctx: *anyopaque, record: Record) anyerror!void,
        openRuns: *const fn (ctx: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) anyerror![]Open,
    };

    pub fn append(self: Runs, record: Record) !void {
        return self.vtable.append(self.ctx, record);
    }

    pub fn openRuns(self: Runs, arena: std.mem.Allocator, session_id: []const u8) ![]Open {
        return self.vtable.openRuns(self.ctx, arena, session_id);
    }
};

pub fn encode(allocator: std.mem.Allocator, record: Record) ![]u8 {
    const payload = try std.json.Stringify.valueAlloc(allocator, record, .{ .emit_null_optional_fields = false });
    defer allocator.free(payload);
    return binding.frame(allocator, payload);
}

pub fn decode(arena: std.mem.Allocator, line: []const u8) !Record {
    const payload = try binding.unframe(line);
    return std.json.parseFromSliceLeaky(Record, arena, payload, .{ .ignore_unknown_fields = true }) catch |err| {
        if (err == error.OutOfMemory) return error.OutOfMemory;
        return error.TornRecord;
    };
}

fn stamped(arena: std.mem.Allocator, held: []const Record, record: Record) !Record {
    if (record.action != .admitted or record.root != null) return record;
    const replaced = record.replaces orelse return record;
    for (try collectOpen(arena, held, record.session_id)) |run| {
        if (!std.mem.eql(u8, run.run_id, replaced)) continue;
        var rooted = record;
        rooted.root = run.root;
        return rooted;
    }
    return record;
}

fn own(arena: std.mem.Allocator, record: Record) !Record {
    var owned = record;
    owned.session_id = try arena.dupe(u8, record.session_id);
    owned.run_id = try arena.dupe(u8, record.run_id);
    if (record.request) |given| owned.request = try arena.dupe(u8, given);
    if (record.replaces) |given| owned.replaces = try arena.dupe(u8, given);
    if (record.root) |given| owned.root = try arena.dupe(u8, given);
    return owned;
}

fn compact(arena: std.mem.Allocator, all: []const Record) ![]Record {
    var sessions: std.ArrayList([]const u8) = .empty;
    for (all) |record| {
        for (sessions.items) |seen| {
            if (std.mem.eql(u8, seen, record.session_id)) break;
        } else try sessions.append(arena, record.session_id);
    }
    var kept: std.ArrayList(Record) = .empty;
    for (sessions.items) |session_id| {
        for (try collectOpen(arena, all, session_id)) |run| {
            for (all) |record| {
                if (record.action == .settled) continue;
                if (!std.mem.eql(u8, record.session_id, session_id) or !std.mem.eql(u8, record.run_id, run.run_id)) continue;
                try kept.append(arena, record);
            }
        }
    }
    return kept.items;
}

fn collectOpen(arena: std.mem.Allocator, held: []const Record, session_id: []const u8) ![]Open {
    var open_runs: std.ArrayList(Open) = .empty;
    var ended: std.StringHashMapUnmanaged(void) = .empty;
    for (held) |record| {
        if (!std.mem.eql(u8, record.session_id, session_id)) continue;
        if (ended.contains(record.run_id)) continue;
        if (record.action == .settled) try ended.put(arena, record.run_id, {});
        if (record.action == .admitted) if (record.replaces) |replaced| try ended.put(arena, replaced, {});
        if (record.action == .admitted) if (record.replaces) |replaced| {
            const slot = for (open_runs.items, 0..) |run, index| {
                if (std.mem.eql(u8, run.run_id, replaced)) break index;
            } else null;
            if (slot) |index| {
                const root = record.root orelse open_runs.items[index].root;
                open_runs.items[index] = .{ .run_id = record.run_id, .admitted_ms = record.time_ms, .started = false, .request = record.request, .root = root };
                continue;
            }
        };
        const found = for (open_runs.items, 0..) |run, index| {
            if (std.mem.eql(u8, run.run_id, record.run_id)) break index;
        } else null;
        if (record.action == .settled) {
            if (found) |index| _ = open_runs.orderedRemove(index);
            continue;
        }
        const index = found orelse blk: {
            try open_runs.append(arena, .{ .run_id = record.run_id, .admitted_ms = record.time_ms, .started = false, .request = null, .root = record.root orelse record.run_id });
            break :blk open_runs.items.len - 1;
        };
        switch (record.action) {
            .admitted => open_runs.items[index].request = record.request,
            .started => open_runs.items[index].started = true,
            .settled => unreachable,
        }
    }
    return open_runs.items;
}

pub const compact_slack: usize = 64;

pub const Store = struct {
    allocator: std.mem.Allocator,
    path: []u8,
    staging: []u8,
    held: ?compat.fs.File = null,
    log: ?Log = null,

    const Log = struct {
        arena: std.heap.ArenaAllocator,
        records: std.ArrayList(Record),
        length: u64,
        kept: usize,
    };

    pub fn open(allocator: std.mem.Allocator, path: []const u8) !Store {
        if (path.len == 0) return error.RunStoreNeedsAPath;
        if (std.fs.path.dirname(path)) |dir| try compat.fs.createDir(compat.fs.getCwd(), dir);
        const owned = try allocator.dupe(u8, path);
        errdefer allocator.free(owned);
        const staging = try std.fmt.allocPrint(allocator, "{s}.writing", .{path});
        errdefer allocator.free(staging);
        const lock_path = try std.fmt.allocPrint(allocator, "{s}.lock", .{path});
        defer allocator.free(lock_path);
        const held = compat.fs.getCwd().createFile(compat.fs.defaultIo(), lock_path, .{ .truncate = false, .lock = .exclusive, .lock_nonblocking = true, .permissions = compat.fs.default_file_mode }) catch |err| switch (err) {
            error.WouldBlock => return error.RunStoreInUse,
            else => return err,
        };
        errdefer held.close(compat.fs.defaultIo());
        return .{ .allocator = allocator, .path = owned, .staging = staging, .held = held };
    }

    pub fn deinit(self: *Store) void {
        if (self.held) |held| held.close(compat.fs.defaultIo());
        if (self.log) |*log| log.arena.deinit();
        self.allocator.free(self.path);
        self.allocator.free(self.staging);
        self.* = undefined;
    }

    fn records(self: *Store, arena: std.mem.Allocator) !struct { std.ArrayList(Record), u64 } {
        const bytes = compat.fs.readFileAlloc(arena, compat.fs.getCwd(), self.path, binding.max_store_bytes) catch |err| switch (err) {
            error.FileNotFound => return .{ .empty, 0 },
            else => return err,
        };
        var kept: std.ArrayList(Record) = .empty;
        var start: usize = 0;
        while (std.mem.indexOfScalarPos(u8, bytes, start, '\n')) |end| : (start = end + 1) {
            const record = decode(arena, bytes[start .. end + 1]) catch |err| {
                if (err == error.OutOfMemory) return error.OutOfMemory;
                break;
            };
            try kept.append(arena, record);
        }
        return .{ kept, start };
    }

    fn load(self: *Store) !*Log {
        if (self.log) |*log| return log;
        var arena = std.heap.ArenaAllocator.init(self.allocator);
        errdefer arena.deinit();
        const held, const length = try self.records(arena.allocator());
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const kept = (try compact(scratch.allocator(), held.items)).len;
        self.log = .{ .arena = arena, .records = held, .length = length, .kept = kept };
        return &self.log.?;
    }

    pub fn append(self: *Store, record: Record) !void {
        const reopened = self.log == null;
        const log = try self.load();
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const arena = scratch.allocator();
        const next = try stamped(arena, log.records.items, record);
        const line = try encode(arena, next);
        if (reopened or log.records.items.len + 1 > 2 * log.kept + compact_slack or log.length + line.len > binding.max_store_bytes / 2) return self.compactWith(arena, log, next);
        const owned = try own(log.arena.allocator(), next);
        try log.records.ensureUnusedCapacity(log.arena.allocator(), 1);
        const io = compat.fs.defaultIo();
        var file = try compat.fs.getCwd().createFile(io, self.path, .{ .truncate = false, .permissions = compat.fs.default_file_mode });
        defer file.close(io);
        try file.setLength(io, log.length);
        try file.writePositionalAll(io, line, log.length);
        try file.sync(io);
        log.records.appendAssumeCapacity(owned);
        log.length += line.len;
    }

    fn compactWith(self: *Store, arena: std.mem.Allocator, log: *Log, next: Record) !void {
        const all = try std.mem.concat(arena, Record, &.{ log.records.items, &.{next} });
        var fresh = std.heap.ArenaAllocator.init(self.allocator);
        errdefer fresh.deinit();
        var kept: std.ArrayList(Record) = .empty;
        var bytes: std.ArrayList(u8) = .empty;
        for (try compact(arena, all)) |held| {
            try kept.append(fresh.allocator(), try own(fresh.allocator(), held));
            try bytes.appendSlice(arena, try encode(arena, held));
        }
        try compat.fs.atomicReplaceDurable(compat.fs.getCwd(), self.path, self.staging, bytes.items);
        log.arena.deinit();
        log.* = .{ .arena = fresh, .records = kept, .length = bytes.items.len, .kept = kept.items.len };
    }

    pub fn openRuns(self: *Store, arena: std.mem.Allocator, session_id: []const u8) ![]Open {
        const held, _ = try self.records(arena);
        return collectOpen(arena, held.items, session_id);
    }

    pub fn runs(self: *Store) Runs {
        return .{ .ctx = self, .vtable = &store_vtable };
    }

    const store_vtable = Runs.VTable{ .append = storeAppend, .openRuns = storeOpenRuns };

    fn storeAppend(ctx: *anyopaque, record: Record) anyerror!void {
        const self: *Store = @ptrCast(@alignCast(ctx));
        return self.append(record);
    }

    fn storeOpenRuns(ctx: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) anyerror![]Open {
        const self: *Store = @ptrCast(@alignCast(ctx));
        return self.openRuns(arena, session_id);
    }
};

pub const Memory = struct {
    arena: std.heap.ArenaAllocator,
    records: std.ArrayList(Record) = .empty,

    pub fn init(allocator: std.mem.Allocator) Memory {
        return .{ .arena = std.heap.ArenaAllocator.init(allocator) };
    }

    pub fn deinit(self: *Memory) void {
        self.arena.deinit();
        self.* = undefined;
    }

    pub fn runs(self: *Memory) Runs {
        return .{ .ctx = self, .vtable = &memory_vtable };
    }

    const memory_vtable = Runs.VTable{ .append = memoryAppend, .openRuns = memoryOpenRuns };

    fn memoryAppend(ctx: *anyopaque, record: Record) anyerror!void {
        const self: *Memory = @ptrCast(@alignCast(ctx));
        const arena = self.arena.allocator();
        var scratch = std.heap.ArenaAllocator.init(self.arena.child_allocator);
        defer scratch.deinit();
        const owned = try stamped(scratch.allocator(), self.records.items, try own(arena, record));
        const all = try std.mem.concat(scratch.allocator(), Record, &.{ self.records.items, &.{owned} });
        const kept = try compact(scratch.allocator(), all);
        var next: std.ArrayList(Record) = .empty;
        try next.appendSlice(arena, kept);
        self.records = next;
    }

    fn memoryOpenRuns(ctx: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) anyerror![]Open {
        const self: *Memory = @ptrCast(@alignCast(ctx));
        return collectOpen(arena, self.records.items, session_id);
    }
};

pub fn conform(arena: std.mem.Allocator, runs: Runs) !void {
    const request = "{\"message\":\"<next & then>\"}";
    for ([_]Record{
        .{ .action = .admitted, .time_ms = 1, .session_id = "conform-a", .run_id = "r1" },
        .{ .action = .started, .time_ms = 2, .session_id = "conform-a", .run_id = "r1" },
        .{ .action = .admitted, .time_ms = 3, .session_id = "conform-a", .run_id = "r2", .request = request },
        .{ .action = .admitted, .time_ms = 4, .session_id = "conform-b", .run_id = "r1" },
        .{ .action = .admitted, .time_ms = 5, .session_id = "conform-a", .run_id = "r3" },
        .{ .action = .settled, .time_ms = 6, .session_id = "conform-a", .run_id = "r3" },
        .{ .action = .settled, .time_ms = 7, .session_id = "conform-b", .run_id = "r1" },
        .{ .action = .admitted, .time_ms = 8, .session_id = "conform-c", .run_id = "held", .request = request },
        .{ .action = .admitted, .time_ms = 9, .session_id = "conform-c", .run_id = "after", .request = request },
        .{ .action = .admitted, .time_ms = 10, .session_id = "conform-c", .run_id = "redelivered", .request = request, .replaces = "held" },
        .{ .action = .admitted, .time_ms = 11, .session_id = "conform-c", .run_id = "again", .request = request, .replaces = "redelivered" },
    }) |record| try runs.append(record);
    const open_a = try runs.openRuns(arena, "conform-a");
    if (open_a.len != 2) return error.RunStoreNonconforming;
    if (!std.mem.eql(u8, open_a[0].run_id, "r1") or !open_a[0].started or open_a[0].admitted_ms != 1) return error.RunStoreNonconforming;
    if (!std.mem.eql(u8, open_a[1].run_id, "r2") or open_a[1].started) return error.RunStoreNonconforming;
    if (!std.mem.eql(u8, open_a[1].request orelse "", request)) return error.RunStoreNonconforming;
    if ((try runs.openRuns(arena, "conform-b")).len != 0) return error.RunStoreNonconforming;
    if ((try runs.openRuns(arena, "conform-absent")).len != 0) return error.RunStoreNonconforming;
    const open_c = try runs.openRuns(arena, "conform-c");
    if (open_c.len != 2) return error.RunStoreNonconforming;
    if (!std.mem.eql(u8, open_c[0].run_id, "again") or !std.mem.eql(u8, open_c[1].run_id, "after")) return error.RunStoreNonconforming;
    if (!std.mem.eql(u8, open_c[0].root, "held") or !std.mem.eql(u8, open_c[1].root, "after")) return error.RunStoreNonconforming;
    if ((try runs.openRuns(arena, "conform-c")).len != 2) return error.RunStoreNonconforming;
}

const testing = std.testing;

fn scratchPath(allocator: std.mem.Allocator, tmp: *std.testing.TmpDir, name: []const u8) ![]u8 {
    const cwd = try std.process.currentPathAlloc(testing.io, allocator);
    defer allocator.free(cwd);
    return std.fs.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], name });
}

test "a run admitted and started before a restart is read after it, and a settled one is gone" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "state/runs.jsonl");
    defer testing.allocator.free(path);
    {
        var store = try Store.open(testing.allocator, path);
        defer store.deinit();
        const runs_store = store.runs();
        try runs_store.append(.{ .action = .admitted, .time_ms = 1, .session_id = "s", .run_id = "r1" });
        try runs_store.append(.{ .action = .started, .time_ms = 2, .session_id = "s", .run_id = "r1" });
        try runs_store.append(.{ .action = .admitted, .time_ms = 3, .session_id = "s", .run_id = "r2", .request = "{\"message\":\"next\"}" });
        try runs_store.append(.{ .action = .admitted, .time_ms = 4, .session_id = "other", .run_id = "r3" });
        try runs_store.append(.{ .action = .settled, .time_ms = 5, .session_id = "other", .run_id = "r3" });
    }
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const open_runs = try store.runs().openRuns(arena.allocator(), "s");
    try testing.expectEqual(@as(usize, 2), open_runs.len);
    try testing.expectEqualStrings("r1", open_runs[0].run_id);
    try testing.expect(open_runs[0].started);
    try testing.expectEqualStrings("r2", open_runs[1].run_id);
    try testing.expect(!open_runs[1].started);
    try testing.expectEqualStrings("{\"message\":\"next\"}", open_runs[1].request.?);
    try testing.expectEqual(@as(usize, 0), (try store.openRuns(arena.allocator(), "other")).len);
}

test "a torn tail is not read as a run, and the next append drops it" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    try store.append(.{ .action = .admitted, .time_ms = 1, .session_id = "s", .run_id = "r1" });
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const whole = try compat.fs.readFileAlloc(arena.allocator(), compat.fs.getCwd(), path, binding.max_store_bytes);
    const torn = try std.mem.concat(arena.allocator(), u8, &.{ whole, whole[0 .. whole.len / 2] });
    try compat.fs.atomicReplace(compat.fs.getCwd(), path, store.staging, torn);
    try testing.expectEqual(@as(usize, 1), (try store.openRuns(arena.allocator(), "s")).len);
    try store.append(.{ .action = .admitted, .time_ms = 2, .session_id = "s", .run_id = "r2" });
    const open_runs = try store.openRuns(arena.allocator(), "s");
    try testing.expectEqual(@as(usize, 2), open_runs.len);
    try testing.expectEqualStrings("r2", open_runs[1].run_id);
}

test "a second store on the same path is refused rather than waiting" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    try testing.expectError(error.RunStoreInUse, Store.open(testing.allocator, path));
}

test "a run record line is the checksum of its JSON and the JSON, in fixed member order" {
    const line = try encode(testing.allocator, .{ .action = .admitted, .time_ms = 3, .session_id = "s", .run_id = "r2", .request = "{\"message\":\"next\"}" });
    defer testing.allocator.free(line);
    try testing.expectEqualStrings("34771ed2 {\"action\":\"admitted\",\"time_ms\":3,\"session_id\":\"s\",\"run_id\":\"r2\",\"request\":\"{\\\"message\\\":\\\"next\\\"}\"}\n", line);
    const raw = try encode(testing.allocator, .{ .action = .admitted, .time_ms = 3, .session_id = "s", .run_id = "r2", .request = "{\"message\":\"<a & b>\"}" });
    defer testing.allocator.free(raw);
    try testing.expectEqualStrings("cb0983f9 {\"action\":\"admitted\",\"time_ms\":3,\"session_id\":\"s\",\"run_id\":\"r2\",\"request\":\"{\\\"message\\\":\\\"<a & b>\\\"}\"}\n", raw);
}

test "a settled record a log kept is read as the end of its run" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var log: std.ArrayList(u8) = .empty;
    for ([_]Record{
        .{ .action = .admitted, .time_ms = 1, .session_id = "s", .run_id = "r1" },
        .{ .action = .admitted, .time_ms = 2, .session_id = "s", .run_id = "r2" },
        .{ .action = .started, .time_ms = 3, .session_id = "s", .run_id = "r1" },
        .{ .action = .settled, .time_ms = 4, .session_id = "s", .run_id = "r1" },
    }) |record| try log.appendSlice(arena.allocator(), try encode(arena.allocator(), record));
    try compat.fs.atomicReplace(compat.fs.getCwd(), path, store.staging, log.items);
    const open_runs = try store.openRuns(arena.allocator(), "s");
    try testing.expectEqual(@as(usize, 1), open_runs.len);
    try testing.expectEqualStrings("r2", open_runs[0].run_id);
}

test "the JSONL store and the memory store both conform" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var file = try Store.open(testing.allocator, path);
    defer file.deinit();
    try conform(arena.allocator(), file.runs());
    var memory = Memory.init(testing.allocator);
    defer memory.deinit();
    try conform(arena.allocator(), memory.runs());
}

test "a store that keeps a settled run open does not conform" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var memory = Memory.init(testing.allocator);
    defer memory.deinit();
    var forgetful = Forgetful{ .inner = &memory };
    try testing.expectError(error.RunStoreNonconforming, conform(arena.allocator(), forgetful.runs()));
}

const Forgetful = struct {
    inner: *Memory,

    fn runs(self: *Forgetful) Runs {
        return .{ .ctx = self, .vtable = &.{ .append = append, .openRuns = openRunsOf } };
    }

    fn append(ctx: *anyopaque, record: Record) anyerror!void {
        const self: *Forgetful = @ptrCast(@alignCast(ctx));
        if (record.action == .settled) return;
        return self.inner.runs().append(record);
    }

    fn openRunsOf(ctx: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) anyerror![]Open {
        const self: *Forgetful = @ptrCast(@alignCast(ctx));
        return self.inner.runs().openRuns(arena, session_id);
    }
};

test "a store that keeps a replaced run open does not conform" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var memory = Memory.init(testing.allocator);
    defer memory.deinit();
    var unreplacing = Unreplacing{ .inner = &memory };
    try testing.expectError(error.RunStoreNonconforming, conform(arena.allocator(), unreplacing.runs()));
}

const Unreplacing = struct {
    inner: *Memory,

    fn runs(self: *Unreplacing) Runs {
        return .{ .ctx = self, .vtable = &.{ .append = append, .openRuns = openRunsOf } };
    }

    fn append(ctx: *anyopaque, record: Record) anyerror!void {
        const self: *Unreplacing = @ptrCast(@alignCast(ctx));
        var kept = record;
        kept.replaces = null;
        return self.inner.runs().append(kept);
    }

    fn openRunsOf(ctx: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) anyerror![]Open {
        const self: *Unreplacing = @ptrCast(@alignCast(ctx));
        return self.inner.runs().openRuns(arena, session_id);
    }
};

test "a re-delivery replaces its held run" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    try store.append(.{ .action = .admitted, .time_ms = 1, .session_id = "s", .run_id = "held", .request = "{}" });
    try store.append(.{ .action = .admitted, .time_ms = 2, .session_id = "s", .run_id = "new", .request = "{}", .replaces = "held" });
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const open_runs = try store.openRuns(arena.allocator(), "s");
    try testing.expectEqual(@as(usize, 1), open_runs.len);
    try testing.expectEqualStrings("new", open_runs[0].run_id);
}

test "settling the replacing run over a kept log does not revive the replaced one" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var log: std.ArrayList(u8) = .empty;
    for ([_]Record{
        .{ .action = .admitted, .time_ms = 1, .session_id = "s", .run_id = "held" },
        .{ .action = .admitted, .time_ms = 2, .session_id = "s", .run_id = "redelivered", .replaces = "held" },
    }) |record| try log.appendSlice(arena.allocator(), try encode(arena.allocator(), record));
    try compat.fs.atomicReplace(compat.fs.getCwd(), path, store.staging, log.items);
    try store.append(.{ .action = .settled, .time_ms = 3, .session_id = "s", .run_id = "redelivered" });
    try testing.expectEqual(@as(usize, 0), (try store.openRuns(arena.allocator(), "s")).len);
}

test "a re-delivered run keeps its place ahead of a run still held behind it, across a reread" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    try store.append(.{ .action = .admitted, .time_ms = 1, .session_id = "s", .run_id = "a" });
    try store.append(.{ .action = .admitted, .time_ms = 2, .session_id = "s", .run_id = "b" });
    try store.append(.{ .action = .admitted, .time_ms = 3, .session_id = "s", .run_id = "a2", .replaces = "a" });
    try store.append(.{ .action = .started, .time_ms = 4, .session_id = "other", .run_id = "x" });
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const open_runs = try store.openRuns(arena.allocator(), "s");
    try testing.expectEqual(@as(usize, 2), open_runs.len);
    try testing.expectEqualStrings("a2", open_runs[0].run_id);
    try testing.expectEqualStrings("b", open_runs[1].run_id);
}

test "a record after a run was replaced or settled does not reopen it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const open_runs = try collectOpen(arena.allocator(), &.{
        .{ .action = .admitted, .time_ms = 1, .session_id = "s", .run_id = "held" },
        .{ .action = .admitted, .time_ms = 2, .session_id = "s", .run_id = "redelivered", .replaces = "held" },
        .{ .action = .started, .time_ms = 3, .session_id = "s", .run_id = "held" },
        .{ .action = .admitted, .time_ms = 4, .session_id = "s", .run_id = "done" },
        .{ .action = .settled, .time_ms = 5, .session_id = "s", .run_id = "done" },
        .{ .action = .started, .time_ms = 6, .session_id = "s", .run_id = "done" },
    }, "s");
    try testing.expectEqual(@as(usize, 1), open_runs.len);
    try testing.expectEqualStrings("redelivered", open_runs[0].run_id);
}

fn lineCount(arena: std.mem.Allocator, path: []const u8) !usize {
    const bytes = try compat.fs.readFileAlloc(arena, compat.fs.getCwd(), path, binding.max_store_bytes);
    return std.mem.count(u8, bytes, "\n");
}

fn churn(store: *Store, arena: std.mem.Allocator, from: usize, count: usize) !void {
    for (from..from + count) |index| {
        const run_id = try std.fmt.allocPrint(arena, "done-{d}", .{index});
        try store.append(.{ .action = .admitted, .time_ms = @intCast(index), .session_id = "s", .run_id = run_id });
        try store.append(.{ .action = .settled, .time_ms = @intCast(index), .session_id = "s", .run_id = run_id });
    }
}

test "an append adds its line after the lines already written, settled ones included" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const records = [_]Record{
        .{ .action = .admitted, .time_ms = 1, .session_id = "s", .run_id = "done" },
        .{ .action = .settled, .time_ms = 2, .session_id = "s", .run_id = "done" },
        .{ .action = .admitted, .time_ms = 3, .session_id = "s", .run_id = "open" },
    };
    var expected: std.ArrayList(u8) = .empty;
    for (records) |record| {
        try store.append(record);
        try expected.appendSlice(arena.allocator(), try encode(arena.allocator(), record));
    }
    try testing.expectEqualStrings(expected.items, try compat.fs.readFileAlloc(arena.allocator(), compat.fs.getCwd(), path, binding.max_store_bytes));
}

test "the log is compacted to its open runs once dead records outgrow the slack" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    try store.append(.{ .action = .admitted, .time_ms = 0, .session_id = "s", .run_id = "open" });
    try churn(&store, arena.allocator(), 0, compact_slack / 2);
    try store.append(.{ .action = .started, .time_ms = 1, .session_id = "s", .run_id = "open" });
    try testing.expectEqual(@as(usize, compact_slack + 2), try lineCount(arena.allocator(), path));
    try store.append(.{ .action = .admitted, .time_ms = 2, .session_id = "s", .run_id = "late" });
    try testing.expectEqual(@as(usize, 3), try lineCount(arena.allocator(), path));
    for (0..4 * compact_slack) |index| {
        try churn(&store, arena.allocator(), compact_slack + index, 1);
        try testing.expect(try lineCount(arena.allocator(), path) <= 2 * 4 + compact_slack);
    }
    const open_runs = try store.openRuns(arena.allocator(), "s");
    try testing.expectEqual(@as(usize, 2), open_runs.len);
    try testing.expectEqualStrings("open", open_runs[0].run_id);
    try testing.expect(open_runs[0].started);
    try testing.expectEqualStrings("late", open_runs[1].run_id);
}

test "a store reopened after compaction reads the same runs and keeps appending" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    {
        var store = try Store.open(testing.allocator, path);
        defer store.deinit();
        try store.append(.{ .action = .admitted, .time_ms = 0, .session_id = "s", .run_id = "held", .request = "{}" });
        try churn(&store, arena.allocator(), 0, compact_slack);
        try store.append(.{ .action = .admitted, .time_ms = 1, .session_id = "s", .run_id = "next" });
        try testing.expect(try lineCount(arena.allocator(), path) < 2 * compact_slack);
    }
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    try store.append(.{ .action = .admitted, .time_ms = 2, .session_id = "s", .run_id = "again", .request = "{}", .replaces = "held" });
    var reopened = try store.openRuns(arena.allocator(), "s");
    try testing.expectEqual(@as(usize, 2), reopened.len);
    try testing.expectEqualStrings("again", reopened[0].run_id);
    try testing.expectEqualStrings("held", reopened[0].root);
    try testing.expectEqualStrings("next", reopened[1].run_id);
    store.deinit();
    store = try Store.open(testing.allocator, path);
    reopened = try store.openRuns(arena.allocator(), "s");
    try testing.expectEqualStrings("held", reopened[0].root);
}

test "a torn tail left by a crash is cut before the first append after a reopen" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const first = try encode(arena.allocator(), .{ .action = .admitted, .time_ms = 1, .session_id = "s", .run_id = "r1" });
    const lost = try encode(arena.allocator(), .{ .action = .admitted, .time_ms = 2, .session_id = "s", .run_id = "lost" });
    const after = try encode(arena.allocator(), .{ .action = .admitted, .time_ms = 3, .session_id = "s", .run_id = "after" });
    const seed = try std.fmt.allocPrint(arena.allocator(), "{s}.seed", .{path});
    try compat.fs.atomicReplace(compat.fs.getCwd(), path, seed, try std.mem.concat(arena.allocator(), u8, &.{ first, lost[0 .. lost.len / 2], after }));
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    try store.append(.{ .action = .admitted, .time_ms = 4, .session_id = "s", .run_id = "r2" });
    const written = try encode(arena.allocator(), .{ .action = .admitted, .time_ms = 4, .session_id = "s", .run_id = "r2" });
    try testing.expectEqualStrings(try std.mem.concat(arena.allocator(), u8, &.{ first, written }), try compat.fs.readFileAlloc(arena.allocator(), compat.fs.getCwd(), path, binding.max_store_bytes));
    const open_runs = try store.openRuns(arena.allocator(), "s");
    try testing.expectEqual(@as(usize, 2), open_runs.len);
    try testing.expectEqualStrings("r2", open_runs[1].run_id);
}

test "a compaction a crash left half-staged does not reach the log, and the next one replaces it" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    {
        var store = try Store.open(testing.allocator, path);
        defer store.deinit();
        try store.append(.{ .action = .admitted, .time_ms = 1, .session_id = "s", .run_id = "r1" });
        try compat.fs.writeFile(compat.fs.getCwd(), store.staging, "0000 half a compac");
    }
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    try testing.expectEqual(@as(usize, 1), (try store.openRuns(arena.allocator(), "s")).len);
    try churn(&store, arena.allocator(), 0, compact_slack);
    try testing.expectEqual(compat.fs.FileKind.absent, compat.fs.fileKind(compat.fs.getCwd(), store.staging));
    try testing.expect(try lineCount(arena.allocator(), path) < 2 * compact_slack);
    try testing.expectEqualStrings("r1", (try store.openRuns(arena.allocator(), "s"))[0].run_id);
}

test "dead records never push the log past what a read accepts" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const request = try arena.allocator().alloc(u8, binding.max_store_bytes / 4);
    @memset(request, 'x');
    for (0..5) |index| {
        const run_id = try std.fmt.allocPrint(arena.allocator(), "big-{d}", .{index});
        try store.append(.{ .action = .admitted, .time_ms = @intCast(index), .session_id = "s", .run_id = run_id, .request = request });
        try store.append(.{ .action = .settled, .time_ms = @intCast(index), .session_id = "s", .run_id = run_id });
    }
    try store.append(.{ .action = .admitted, .time_ms = 9, .session_id = "s", .run_id = "small" });
    const open_runs = try store.openRuns(arena.allocator(), "s");
    try testing.expectEqual(@as(usize, 1), open_runs.len);
    try testing.expectEqualStrings("small", open_runs[0].run_id);
}

test "the first append after an open leaves the log readable by its owner alone" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const path = try scratchPath(testing.allocator, &tmp, "runs.jsonl");
    defer testing.allocator.free(path);
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const io = compat.fs.defaultIo();
    {
        var file = try compat.fs.getCwd().createFile(io, path, .{ .permissions = @fromBackingInt(@intCast(0o644)) });
        defer file.close(io);
        try file.writeStreamingAll(io, try encode(arena.allocator(), .{ .action = .admitted, .time_ms = 1, .session_id = "s", .run_id = "r1", .request = "{}" }));
    }
    var store = try Store.open(testing.allocator, path);
    defer store.deinit();
    try store.append(.{ .action = .started, .time_ms = 2, .session_id = "s", .run_id = "r1" });
    const stat = try compat.fs.getCwd().statFile(io, path, .{});
    try testing.expectEqual(@as(u32, 0o600), @as(u32, @intCast(@backingInt(stat.permissions))) & 0o777);
}
