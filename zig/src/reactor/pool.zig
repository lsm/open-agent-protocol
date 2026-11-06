const std = @import("std");
const c = std.c;

pub const Job = struct {
    run: *const fn (*anyopaque) void,
    context: *anyopaque,
};

pub const Pool = struct {
    allocator: std.mem.Allocator,
    workers: []std.Thread,
    queue: []Job,
    started: usize = 0,
    head: usize = 0,
    count: usize = 0,
    closing: bool = false,
    joined: bool = false,
    mutex: c.pthread_mutex_t = .{},
    ready: c.pthread_cond_t = .{},

    pub fn create(allocator: std.mem.Allocator, worker_count: usize, capacity: usize) !*Pool {
        if (worker_count == 0 or capacity == 0) return error.InvalidConfiguration;
        const self = try allocator.create(Pool);
        errdefer allocator.destroy(self);
        const queue = try allocator.alloc(Job, capacity);
        errdefer allocator.free(queue);
        const workers = try allocator.alloc(std.Thread, worker_count);
        errdefer allocator.free(workers);
        self.* = .{ .allocator = allocator, .workers = workers, .queue = queue };
        errdefer {
            self.shutdown();
            self.releaseSync();
        }
        for (workers) |*thread| {
            thread.* = try std.Thread.spawn(.{}, worker, .{self});
            self.started += 1;
        }
        return self;
    }

    pub fn destroy(self: *Pool) void {
        self.shutdown();
        self.releaseSync();
        const allocator = self.allocator;
        allocator.free(self.queue);
        allocator.free(self.workers);
        allocator.destroy(self);
    }

    pub fn post(self: *Pool, job: Job) error{ Closed, Full }!void {
        check(c.pthread_mutex_lock(&self.mutex));
        defer check(c.pthread_mutex_unlock(&self.mutex));
        if (self.closing) return error.Closed;
        if (self.count == self.queue.len) return error.Full;
        self.queue[(self.head + self.count) % self.queue.len] = job;
        self.count += 1;
        check(c.pthread_cond_signal(&self.ready));
    }

    pub fn shutdown(self: *Pool) void {
        if (self.joined) return;
        check(c.pthread_mutex_lock(&self.mutex));
        self.closing = true;
        check(c.pthread_cond_broadcast(&self.ready));
        check(c.pthread_mutex_unlock(&self.mutex));
        for (self.workers[0..self.started]) |thread| thread.join();
        self.joined = true;
    }

    fn worker(self: *Pool) void {
        while (true) {
            check(c.pthread_mutex_lock(&self.mutex));
            while (self.count == 0 and !self.closing) check(c.pthread_cond_wait(&self.ready, &self.mutex));
            if (self.count == 0) {
                check(c.pthread_mutex_unlock(&self.mutex));
                return;
            }
            const job = self.queue[self.head];
            self.head = (self.head + 1) % self.queue.len;
            self.count -= 1;
            check(c.pthread_mutex_unlock(&self.mutex));
            job.run(job.context);
        }
    }

    fn releaseSync(self: *Pool) void {
        check(c.pthread_cond_destroy(&self.ready));
        check(c.pthread_mutex_destroy(&self.mutex));
    }

    fn check(status: c.E) void {
        if (status != .SUCCESS) @panic("pool synchronization failed");
    }
};
