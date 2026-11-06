const std = @import("std");
const c = std.c;
const pool = @import("pool.zig");

const Gate = struct {
    mutex: c.pthread_mutex_t = .{},
    changed: c.pthread_cond_t = .{},
    entered: usize = 0,
    released: bool = false,
    finished: std.atomic.Value(usize) = .init(0),

    fn deinit(self: *Gate) void {
        _ = c.pthread_cond_destroy(&self.changed);
        _ = c.pthread_mutex_destroy(&self.mutex);
    }

    fn run(context: *anyopaque) void {
        const self: *Gate = @ptrCast(@alignCast(context));
        _ = c.pthread_mutex_lock(&self.mutex);
        self.entered += 1;
        _ = c.pthread_cond_broadcast(&self.changed);
        while (!self.released) _ = c.pthread_cond_wait(&self.changed, &self.mutex);
        _ = c.pthread_mutex_unlock(&self.mutex);
        _ = self.finished.fetchAdd(1, .release);
    }

    fn wait(self: *Gate, count: usize) void {
        _ = c.pthread_mutex_lock(&self.mutex);
        defer _ = c.pthread_mutex_unlock(&self.mutex);
        while (self.entered < count) _ = c.pthread_cond_wait(&self.changed, &self.mutex);
    }

    fn release(self: *Gate) void {
        _ = c.pthread_mutex_lock(&self.mutex);
        self.released = true;
        _ = c.pthread_cond_broadcast(&self.changed);
        _ = c.pthread_mutex_unlock(&self.mutex);
    }
};

const Record = struct {
    values: [4]usize = undefined,
    count: usize = 0,

    const Item = struct {
        record: *Record,
        value: usize,

        fn run(context: *anyopaque) void {
            const self: *Item = @ptrCast(@alignCast(context));
            self.record.values[self.record.count] = self.value;
            self.record.count += 1;
        }
    };
};

test "a bounded queue rejects excess work and drains accepted jobs in order" {
    var gate: Gate = .{};
    defer gate.deinit();
    const workers = try pool.Pool.create(std.testing.allocator, 1, 2);
    defer workers.destroy();
    defer gate.release();
    try workers.post(.{ .run = Gate.run, .context = &gate });
    gate.wait(1);
    var record: Record = .{};
    var items = [_]Record.Item{
        .{ .record = &record, .value = 10 },
        .{ .record = &record, .value = 20 },
        .{ .record = &record, .value = 30 },
    };
    try workers.post(.{ .run = Record.Item.run, .context = &items[0] });
    try workers.post(.{ .run = Record.Item.run, .context = &items[1] });
    try std.testing.expectError(error.Full, workers.post(.{ .run = Record.Item.run, .context = &items[2] }));
    gate.release();
    workers.shutdown();
    try std.testing.expectEqualSlices(usize, &.{ 10, 20 }, record.values[0..record.count]);
    try std.testing.expectEqual(@as(usize, 1), gate.finished.load(.acquire));
    try std.testing.expectError(error.Closed, workers.post(.{ .run = Record.Item.run, .context = &items[2] }));
    workers.shutdown();
}

test "the configured workers run concurrently and remain bounded" {
    var gate: Gate = .{};
    defer gate.deinit();
    const workers = try pool.Pool.create(std.testing.allocator, 2, 3);
    defer workers.destroy();
    defer gate.release();
    for (0..2) |_| try workers.post(.{ .run = Gate.run, .context = &gate });
    gate.wait(2);
    for (0..3) |_| try workers.post(.{ .run = Gate.run, .context = &gate });
    try std.testing.expectError(error.Full, workers.post(.{ .run = Gate.run, .context = &gate }));
    gate.release();
    workers.shutdown();
    try std.testing.expectEqual(@as(usize, 5), gate.finished.load(.acquire));
}

fn allocationProbe(allocator: std.mem.Allocator) !void {
    const workers = try pool.Pool.create(allocator, 2, 2);
    defer workers.destroy();
}

test "allocation failures release pool storage" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, allocationProbe, .{});
}

test "empty configurations are rejected before workers start" {
    try std.testing.expectError(error.InvalidConfiguration, pool.Pool.create(std.testing.allocator, 0, 2));
    try std.testing.expectError(error.InvalidConfiguration, pool.Pool.create(std.testing.allocator, 2, 0));
}
