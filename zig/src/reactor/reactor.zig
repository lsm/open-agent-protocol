const std = @import("std");
const c = std.c;
const poller = @import("poller.zig");
const source = @import("sources.zig");

pub const Event = union(enum) { io: poller.Ready, timer: usize, child: usize };
pub const Command = struct {
    run: *const fn (*anyopaque, *Reactor) void,
    context: *anyopaque,
};
pub const Handler = *const fn (*anyopaque, *Reactor, Event) void;
pub const Error = source.Error || error{ Closed, Full };

pub const Reactor = struct {
    allocator: std.mem.Allocator,
    poll: poller.Poller,
    sources: source.Sources,
    queue: []Command,
    budget: usize,
    handler: Handler,
    context: *anyopaque,
    thread: std.Thread = undefined,
    thread_id: std.atomic.Value(std.Thread.Id) = .init(0),
    mutex: c.pthread_mutex_t = .{},
    head: usize = 0,
    count: usize = 0,
    closing: bool = false,
    joined: bool = false,
    failure: ?Error = null,

    pub fn create(allocator: std.mem.Allocator, capacity: usize, budget: usize, handler: Handler, context: *anyopaque) !*Reactor {
        if (capacity == 0 or budget == 0) return error.InvalidConfiguration;
        const self = try allocator.create(Reactor);
        errdefer allocator.destroy(self);
        var poll = try poller.Poller.init();
        errdefer poll.deinit();
        const queue = try allocator.alloc(Command, capacity);
        errdefer allocator.free(queue);
        self.* = .{ .allocator = allocator, .poll = poll, .sources = undefined, .queue = queue, .budget = budget, .handler = handler, .context = context };
        self.sources = source.Sources.init(allocator, &self.poll);
        errdefer self.sources.deinit();
        errdefer check(c.pthread_mutex_destroy(&self.mutex));
        self.thread = try std.Thread.spawn(.{}, entry, .{self});
        return self;
    }

    pub fn post(self: *Reactor, command: Command) Error!void {
        self.lock();
        defer self.unlock();
        if (self.closing) return error.Closed;
        if (self.count == self.queue.len) return error.Full;
        if (self.count == 0) try self.poll.wake();
        self.queue[(self.head + self.count) % self.queue.len] = command;
        self.count += 1;
    }

    pub fn requestStop(self: *Reactor) Error!void {
        self.lock();
        defer self.unlock();
        if (self.closing) return;
        try self.poll.wake();
        self.closing = true;
    }

    pub fn stop(self: *Reactor) Error!void {
        if (!self.joined) {
            try self.requestStop();
            self.thread.join();
            self.joined = true;
        }
        if (self.failure) |failure| return failure;
    }

    pub fn destroy(self: *Reactor) Error!void {
        const stopped: Error!void = self.stop();
        if (!self.joined) return stopped;
        self.sources.deinit();
        self.poll.deinit();
        check(c.pthread_mutex_destroy(&self.mutex));
        const allocator = self.allocator;
        allocator.free(self.queue);
        allocator.destroy(self);
        return stopped;
    }

    pub fn threadId(self: *const Reactor) std.Thread.Id {
        return self.thread_id.load(.acquire);
    }

    fn entry(self: *Reactor) void {
        self.thread_id.store(std.Thread.getCurrentId(), .release);
        self.drive() catch |failure| {
            self.lock();
            self.failure = failure;
            self.closing = true;
            self.unlock();
            while (self.pop()) |command| command.run(command.context, self);
        };
    }

    fn pop(self: *Reactor) ?Command {
        self.lock();
        defer self.unlock();
        if (self.count == 0) return null;
        const command = self.queue[self.head];
        self.head = (self.head + 1) % self.queue.len;
        self.count -= 1;
        return command;
    }

    fn drive(self: *Reactor) Error!void {
        var ready: [64]poller.Ready = undefined;
        var events: [64]?Event = undefined;
        while (true) {
            for (0..self.budget) |_| {
                const command = self.pop() orelse break;
                command.run(command.context, self);
            }
            self.lock();
            const stop_now = self.closing and self.count == 0;
            const pending = self.count != 0;
            self.unlock();
            if (stop_now) return;
            if (pending) try self.poll.wake();
            const count = try self.poll.wait(&ready);
            for (ready[0..count], events[0..count]) |item, *event| {
                event.* = if (item.wake) null else if (try self.sources.take(item.token)) |kind| switch (kind) {
                    .timer => .{ .timer = item.token },
                    .child => .{ .child = item.token },
                } else .{ .io = item };
            }
            for (events[0..count]) |event| if (event) |item| self.handler(self.context, self, item);
        }
    }

    fn lock(self: *Reactor) void {
        check(c.pthread_mutex_lock(&self.mutex));
    }

    fn unlock(self: *Reactor) void {
        check(c.pthread_mutex_unlock(&self.mutex));
    }

    fn check(status: c.E) void {
        if (status != .SUCCESS) @panic("reactor synchronization failed");
    }
};
