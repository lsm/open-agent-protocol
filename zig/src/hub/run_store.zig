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
};

pub const Open = struct {
    run_id: []const u8,
    admitted_ms: i64,
    started: bool,
    request: ?[]const u8,
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

fn sameRun(a: Record, b: Record) bool {
    return std.mem.eql(u8, a.session_id, b.session_id) and std.mem.eql(u8, a.run_id, b.run_id);
}

pub const Store = struct {
    allocator: std.mem.Allocator,
    path: []u8,
    staging: []u8,
    held: ?compat.fs.File = null,

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
        self.allocator.free(self.path);
        self.allocator.free(self.staging);
        self.* = undefined;
    }

    fn records(self: *Store, arena: std.mem.Allocator) ![]Record {
        const bytes = compat.fs.readFileAlloc(arena, compat.fs.getCwd(), self.path, binding.max_store_bytes) catch |err| switch (err) {
            error.FileNotFound => return &.{},
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
        return kept.items;
    }

    pub fn append(self: *Store, record: Record) !void {
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const arena = scratch.allocator();
        var next: std.ArrayList(u8) = .empty;
        for (try self.records(arena)) |held| {
            if (record.action == .settled and sameRun(held, record)) continue;
            try next.appendSlice(arena, try encode(arena, held));
        }
        if (record.action != .settled) try next.appendSlice(arena, try encode(arena, record));
        try compat.fs.atomicReplace(compat.fs.getCwd(), self.path, self.staging, next.items);
    }

    pub fn openRuns(self: *Store, arena: std.mem.Allocator, session_id: []const u8) ![]Open {
        var open_runs: std.ArrayList(Open) = .empty;
        for (try self.records(arena)) |record| {
            if (!std.mem.eql(u8, record.session_id, session_id)) continue;
            const found = for (open_runs.items, 0..) |run, index| {
                if (std.mem.eql(u8, run.run_id, record.run_id)) break index;
            } else null;
            if (record.action == .settled) {
                if (found) |index| _ = open_runs.orderedRemove(index);
                continue;
            }
            const index = found orelse blk: {
                try open_runs.append(arena, .{ .run_id = record.run_id, .admitted_ms = record.time_ms, .started = false, .request = null });
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
    const bytes = try compat.fs.readFileAlloc(arena.allocator(), compat.fs.getCwd(), path, binding.max_store_bytes);
    try testing.expect(std.mem.indexOf(u8, bytes, "r3") == null);
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
