const std = @import("std");
const builtin = @import("builtin");
const ai_types = @import("ai_types");
const agent = @import("agent");
const compat = @import("compat");
const common = @import("tools/common");
const process_runner = @import("tools/process_runner");

pub const schema_execute =
    \\{"type":"object","properties":{"description":{"type":"string","description":"Why this tool call is needed and what information or change it is intended to produce."},"workspace_root":{"type":"string"},"command":{"type":"string"},"timeout_ms":{"type":"integer","minimum":1},"run_in_background":{"type":"boolean","description":"Start the command as a background job that outlives this call. It ends when the session closes or at its time limit: timeout_ms, at most 1 hour. Returns task_id and output_path at once; read output_path with Read and check or stop the job with ShellJob."}},"required":["description","workspace_root","command"],"additionalProperties":false}
;

pub const schema_job =
    \\{"type":"object","properties":{"description":{"type":"string","description":"Why this tool call is needed and what information or change it is intended to produce."},"workspace_root":{"type":"string"},"path":{"type":"string","description":"The output_path the background Shell call returned."},"kill":{"type":"boolean"}},"required":["description","workspace_root","path"],"additionalProperties":false}
;

const jobs_dir = ".oapx/jobs";

pub const job_tool_name = "ShellJob";

pub const Jobs = struct {
    allocator: std.mem.Allocator,
    mutex: std.atomic.Mutex = .unlocked,
    next: u64 = 0,
    next_end: u64 = 0,
    closing: bool = false,
    ready: ?Ready = null,
    list: std.ArrayList(*Job) = .empty,
    foreground_budget_ms: u64 = default_foreground_budget_ms,
    limit_ms: u64 = command_limit_ms,
    run_generation: u64 = 0,
    cancelled_run: ?u64 = null,
    limiter: ?std.Thread = null,
    limiter_wake: [2]std.posix.fd_t = undefined,

    const Job = struct {
        id: []u8,
        output: []u8,
        workspace_root: []u8,
        tool_call_id: []u8,
        leader: std.posix.pid_t,
        term: ?std.process.Child.Term = null,
        ended_order: u64 = 0,
        killed: bool = false,
        cause: ?Cause = null,
        noticed: bool = false,
        watcher: ?std.Thread = null,
        moved_in: ?u64 = null,
        limit_ms: u64 = 0,
        deadline_ms: i64 = 0,
        limited: bool = false,

        fn owesNotice(self: *const Job) bool {
            return self.term != null and !self.killed and !self.noticed;
        }
    };

    pub const Ready = struct {
        ctx: *anyopaque,
        notify: *const fn (*anyopaque) void,
    };

    pub fn setReady(self: *Jobs, ready: ?Ready) void {
        self.lock();
        defer self.mutex.unlock();
        self.ready = ready;
    }

    pub const Cause = enum { finished, expired, cancelled_by_host, cancelled_by_model, session_closed };
    pub const Status = enum { running, completed, failed, cancelled };
    pub const Task = struct {
        task_id: []const u8,
        tool_call_id: []const u8,
        status: Status,
        cause: ?Cause,
        ended_order: u64,
    };

    pub const Notice = struct {
        task_id: []const u8,
        text: []u8,

        pub fn deinit(self: Notice, allocator: std.mem.Allocator) void {
            allocator.free(self.text);
        }
    };

    pub const Adopted = struct { task_id: []const u8, output_path: []const u8, bytes: usize };
    pub const AdoptOptions = struct { tool_call_id: []const u8, limit_ms: u64, started_ms: i64, ends_with_run: bool };

    pub const Report = struct {
        task_id: []const u8,
        status: []const u8,
        exit_code: ?u8 = null,
        signal: ?u32 = null,
        reason: ?[]const u8 = null,
        bytes: u64,
        output_path: []const u8,
    };

    pub fn init(allocator: std.mem.Allocator) Jobs {
        return .{ .allocator = allocator };
    }

    pub fn deinit(self: *Jobs) void {
        self.seal();
        self.endAll();
        if (comptime builtin.os.tag != .windows) if (self.limiter) |thread| {
            _ = std.posix.system.close(self.limiter_wake[1]);
            thread.join();
            _ = std.posix.system.close(self.limiter_wake[0]);
        };
        for (self.list.items) |job| self.freeJob(job);
        self.list.deinit(self.allocator);
        self.* = undefined;
    }

    fn lock(self: *Jobs) void {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
    }

    pub fn seal(self: *Jobs) void {
        self.lock();
        defer self.mutex.unlock();
        self.closing = true;
    }

    fn newJob(self: *Jobs, workspace_root: []const u8, tool_call_id: []const u8) !*Job {
        self.lock();
        self.next += 1;
        const number = self.next;
        self.mutex.unlock();
        var entropy: [6]u8 = undefined;
        compat.random.fillSecureBytes(&entropy);
        const id = try std.fmt.allocPrint(self.allocator, "job-{d}-{x}", .{ number, entropy });
        errdefer self.allocator.free(id);
        const output = try std.fmt.allocPrint(self.allocator, jobs_dir ++ "/{s}.log", .{id});
        errdefer self.allocator.free(output);
        const root = try self.allocator.dupe(u8, workspace_root);
        errdefer self.allocator.free(root);
        const call_id = try self.allocator.dupe(u8, tool_call_id);
        errdefer self.allocator.free(call_id);
        const job = try self.allocator.create(Job);
        job.* = .{ .id = id, .output = output, .workspace_root = root, .tool_call_id = call_id, .leader = 0 };
        return job;
    }

    fn freeJob(self: *Jobs, job: *Job) void {
        self.allocator.free(job.id);
        self.allocator.free(job.output);
        self.allocator.free(job.workspace_root);
        self.allocator.free(job.tool_call_id);
        self.allocator.destroy(job);
    }

    fn watch(self: *Jobs, job: *Job, copier: std.Thread, stop: std.posix.fd_t) void {
        const term = process_runner.waitLeader(job.leader) catch std.process.Child.Term{ .unknown = 0 };
        process_runner.endGroup(job.leader);
        _ = std.posix.system.close(stop);
        copier.join();
        self.lock();
        job.term = term;
        self.next_end += 1;
        job.ended_order = self.next_end;
        if (job.cause == null) job.cause = .finished;
        if (self.ready) |ready| ready.notify(ready.ctx);
        self.mutex.unlock();
    }

    pub fn adopt(self: *Jobs, workspace_root: []const u8, moved: process_runner.Moved, options: AdoptOptions) !Adopted {
        if (comptime builtin.os.tag == .windows) return error.Unsupported;
        var taken = false;
        defer if (!taken) {
            process_runner.endGroup(moved.leader);
            _ = process_runner.waitLeader(moved.leader) catch {};
            _ = std.posix.system.close(moved.stdout);
            _ = std.posix.system.close(moved.stderr);
        };
        var dir = try common.openWorkspace(workspace_root, false);
        defer dir.close(common.defaultIo());
        try dir.createDirPath(common.defaultIo(), jobs_dir);
        self.reap();
        const job = try self.newJob(workspace_root, options.tool_call_id);
        errdefer self.freeJob(job);
        const file = try dir.createFile(common.defaultIo(), job.output, .{});
        var file_open = true;
        defer if (file_open) file.close(common.defaultIo());
        try file.writeStreamingAll(common.defaultIo(), moved.stdout_head);
        try file.writeStreamingAll(common.defaultIo(), moved.stderr_head);
        var stop: [2]std.posix.fd_t = undefined;
        try closedOnExecPipe(&stop);
        var stop_open = true;
        defer if (stop_open) {
            _ = std.posix.system.close(stop[0]);
            _ = std.posix.system.close(stop[1]);
        };
        self.lock();
        var locked = true;
        defer if (locked) self.mutex.unlock();
        if (self.closing) return error.SessionClosed;
        try self.startLimiter();
        try self.list.ensureUnusedCapacity(self.allocator, 1);
        const copier = try std.Thread.spawn(.{}, copyOutput, .{ moved.stdout, moved.stderr, stop[0], file });
        job.leader = moved.leader;
        job.limit_ms = options.limit_ms;
        job.deadline_ms = options.started_ms + @as(i64, @intCast(options.limit_ms));
        if (options.ends_with_run) job.moved_in = self.run_generation;
        job.killed = options.ends_with_run and self.cancelled_run == self.run_generation;
        if (job.killed) job.cause = .cancelled_by_host;
        file_open = false;
        taken = true;
        stop_open = false;
        job.watcher = std.Thread.spawn(.{}, watch, .{ self, job, copier, stop[1] }) catch |err| {
            process_runner.endGroup(moved.leader);
            _ = process_runner.waitLeader(moved.leader) catch {};
            _ = std.posix.system.close(stop[1]);
            copier.join();
            return err;
        };
        self.list.appendAssumeCapacity(job);
        _ = std.posix.system.write(self.limiter_wake[1], "x", 1);
        const killed = job.killed;
        self.mutex.unlock();
        locked = false;
        if (killed) process_runner.endGroup(moved.leader);
        return .{ .task_id = job.id, .output_path = job.output, .bytes = moved.stdout_head.len + moved.stderr_head.len };
    }

    fn startLimiter(self: *Jobs) !void {
        if (self.limiter != null) return;
        var wake: [2]std.posix.fd_t = undefined;
        try closedOnExecPipe(&wake);
        errdefer {
            _ = std.posix.system.close(wake[0]);
            _ = std.posix.system.close(wake[1]);
        }
        self.limiter = try std.Thread.spawn(.{}, watchLimits, .{ self, wake[0] });
        self.limiter_wake = wake;
    }

    fn watchLimits(self: *Jobs, wake: std.posix.fd_t) void {
        var watched = [_]std.posix.pollfd{.{ .fd = wake, .events = std.posix.POLL.IN, .revents = 0 }};
        while (true) {
            const wait_ms = self.endExpired();
            _ = std.posix.poll(&watched, wait_ms) catch {
                compat.time.sleepMs(limiter_retry_ms);
                continue;
            };
            if (watched[0].revents == 0) continue;
            var drained: [64]u8 = undefined;
            const count = std.posix.read(wake, &drained) catch {
                compat.time.sleepMs(limiter_retry_ms);
                continue;
            };
            if (count == 0) return;
        }
    }

    fn endExpired(self: *Jobs) i32 {
        var nearest: ?i64 = null;
        var index: usize = 0;
        self.lock();
        while (index < self.list.items.len) : (index += 1) {
            const job = self.list.items[index];
            if (job.term != null or job.cause != null) continue;
            const now = common.nowMs();
            if (job.deadline_ms > now) {
                nearest = @min(nearest orelse job.deadline_ms, job.deadline_ms);
                continue;
            }
            job.limited = true;
            job.cause = .expired;
            self.mutex.unlock();
            process_runner.endGroup(job.leader);
            self.lock();
        }
        self.mutex.unlock();
        const until = nearest orelse return -1;
        return @intCast(@min(@max(until - common.nowMs(), 0), std.math.maxInt(i32)));
    }

    fn copyOutput(stdout: std.posix.fd_t, stderr: std.posix.fd_t, stop: std.posix.fd_t, file: std.Io.File) void {
        defer file.close(common.defaultIo());
        defer _ = std.posix.system.close(stop);
        var watched = [_]std.posix.pollfd{
            .{ .fd = stdout, .events = std.posix.POLL.IN, .revents = 0 },
            .{ .fd = stderr, .events = std.posix.POLL.IN, .revents = 0 },
            .{ .fd = stop, .events = std.posix.POLL.IN, .revents = 0 },
        };
        defer for (watched[0..2]) |entry| {
            if (entry.fd >= 0) _ = std.posix.system.close(entry.fd);
        };
        var buffer: [16 * 1024]u8 = undefined;
        var stopping = false;
        var drained: usize = 0;
        while (watched[0].fd >= 0 or watched[1].fd >= 0) {
            const ready = std.posix.poll(&watched, if (stopping) 0 else -1) catch return;
            if (stopping and ready == 0) return;
            if (watched[2].revents != 0) {
                stopping = true;
                watched[2].fd = -1;
            }
            for (watched[0..2]) |*entry| {
                if (entry.fd < 0 or entry.revents == 0) continue;
                const count = std.posix.read(entry.fd, &buffer) catch 0;
                if (count == 0) {
                    _ = std.posix.system.close(entry.fd);
                    entry.fd = -1;
                    continue;
                }
                file.writeStreamingAll(common.defaultIo(), buffer[0..count]) catch return;
                if (stopping) drained += count;
                if (drained >= stop_drain_bytes) return;
            }
        }
    }

    fn limitFor(self: *const Jobs, obj: std.json.ObjectMap) u64 {
        return @min(common.optionalU64(obj, "timeout_ms", self.limit_ms), self.limit_ms);
    }

    pub fn beginRun(self: *Jobs) void {
        self.lock();
        defer self.mutex.unlock();
        self.run_generation += 1;
    }

    pub fn endMoved(self: *Jobs) void {
        self.lock();
        const generation = self.run_generation;
        self.cancelled_run = generation;
        var index: usize = 0;
        while (index < self.list.items.len) : (index += 1) {
            const job = self.list.items[index];
            if (job.moved_in != generation or job.term != null or job.cause != null) continue;
            job.killed = true;
            job.cause = .cancelled_by_host;
            self.mutex.unlock();
            process_runner.endGroup(job.leader);
            self.lock();
        }
        self.mutex.unlock();
    }

    pub fn report(self: *Jobs, output_path: []const u8, kill: bool) !Report {
        self.reap();
        self.lock();
        const job = self.find(output_path) orelse {
            self.mutex.unlock();
            return error.UnknownJob;
        };
        if (kill and job.term == null and job.cause == null) {
            job.killed = true;
            job.cause = .cancelled_by_model;
        }
        self.mutex.unlock();
        if (kill) self.end(job);
        self.lock();
        defer self.mutex.unlock();
        return .{
            .task_id = job.id,
            .status = if (job.term == null) "running" else if (job.killed or job.limited) "killed" else switch (job.term.?) {
                .exited => "exited",
                else => "signalled",
            },
            .exit_code = if (job.term) |term| switch (term) {
                .exited => |code| code,
                else => null,
            } else null,
            .signal = if (job.term) |term| switch (term) {
                .signal => |sig| @backingInt(sig),
                else => null,
            } else null,
            .reason = if (job.limited) "time_limit" else null,
            .bytes = outputBytes(job.*),
            .output_path = job.output,
        };
    }

    pub fn tasks(self: *Jobs, allocator: std.mem.Allocator) ![]Task {
        return self.tasksForCall(allocator, null);
    }

    pub fn tasksForCall(self: *Jobs, allocator: std.mem.Allocator, call_id: ?[]const u8) ![]Task {
        self.lock();
        defer self.mutex.unlock();
        var count: usize = 0;
        for (self.list.items) |job| {
            if (call_id) |named| if (!std.mem.eql(u8, named, job.tool_call_id)) continue;
            count += 1;
        }
        const result = try allocator.alloc(Task, count);
        var filled: usize = 0;
        errdefer {
            for (result[0..filled]) |task| {
                allocator.free(task.task_id);
                allocator.free(task.tool_call_id);
            }
            allocator.free(result);
        }
        for (self.list.items) |job| {
            if (call_id) |named| if (!std.mem.eql(u8, named, job.tool_call_id)) continue;
            const task = &result[filled];
            const id = try allocator.dupe(u8, job.id);
            errdefer allocator.free(id);
            task.* = .{
                .task_id = id,
                .tool_call_id = try allocator.dupe(u8, job.tool_call_id),
                .status = taskStatus(job),
                .cause = if (job.term == null) null else job.cause,
                .ended_order = job.ended_order,
            };
            filled += 1;
        }
        return result;
    }

    fn taskStatus(job: *const Job) Status {
        const term = job.term orelse return .running;
        return switch (job.cause orelse .finished) {
            .finished => switch (term) {
                .exited => |code| if (code == 0) .completed else .failed,
                else => .failed,
            },
            .expired => .failed,
            .cancelled_by_host, .cancelled_by_model, .session_closed => .cancelled,
        };
    }

    pub fn cancelTask(self: *Jobs, task_id: []const u8) !bool {
        self.lock();
        const job = for (self.list.items) |candidate| {
            if (std.mem.eql(u8, candidate.id, task_id)) break candidate;
        } else {
            self.mutex.unlock();
            return error.UnknownJob;
        };
        if (job.term != null or job.cause != null) {
            const settling = job.term == null;
            self.mutex.unlock();
            if (settling) {
                self.end(job);
                while (true) {
                    self.lock();
                    const finished = job.term != null;
                    self.mutex.unlock();
                    if (finished) break;
                    compat.time.sleepMs(1);
                }
            }
            return false;
        }
        job.killed = true;
        job.cause = .cancelled_by_host;
        self.mutex.unlock();
        self.end(job);
        return true;
    }

    pub const Output = struct {
        text: []u8,
        next: u64,
        truncated: bool,
        ended: bool,
    };

    const DecodedOutput = struct { text: []u8, consumed: usize };
    const output_limit = 32 * 1024;

    fn sequencePrefix(bytes: []const u8) struct { length: usize, complete: bool, valid: bool } {
        const first = bytes[0];
        const length: usize = if (first < 0x80) 1 else if (first >= 0xc2 and first <= 0xdf) 2 else if (first >= 0xe0 and first <= 0xef) 3 else if (first >= 0xf0 and first <= 0xf4) 4 else return .{ .length = 1, .complete = true, .valid = false };
        var i: usize = 1;
        while (i < length and i < bytes.len) : (i += 1) {
            const c = bytes[i];
            if (c < 0x80 or c > 0xbf or (i == 1 and ((first == 0xe0 and c < 0xa0) or (first == 0xed and c > 0x9f) or (first == 0xf0 and c < 0x90) or (first == 0xf4 and c > 0x8f)))) {
                return .{ .length = i, .complete = true, .valid = false };
            }
        }
        return .{ .length = i, .complete = i == length, .valid = true };
    }

    fn decodeOutput(allocator: std.mem.Allocator, bytes: []const u8, ended: bool, limit: usize) !DecodedOutput {
        var text = std.ArrayList(u8).empty;
        errdefer text.deinit(allocator);
        var pos: usize = 0;
        while (pos < bytes.len) {
            const seq = sequencePrefix(bytes[pos..]);
            if (!seq.complete and !ended) break;
            if (pos + seq.length > limit) break;
            if (seq.valid and seq.complete) {
                try text.appendSlice(allocator, bytes[pos..][0..seq.length]);
            } else {
                try text.appendSlice(allocator, "�");
            }
            pos += seq.length;
        }
        return .{ .text = try text.toOwnedSlice(allocator), .consumed = pos };
    }

    pub fn outputTask(self: *Jobs, allocator: std.mem.Allocator, task_id: []const u8, from: u64) !Output {
        self.lock();
        const job = for (self.list.items) |candidate| {
            if (std.mem.eql(u8, candidate.id, task_id)) break candidate;
        } else {
            self.mutex.unlock();
            return error.UnknownJob;
        };
        const root = allocator.dupe(u8, job.workspace_root) catch |err| {
            self.mutex.unlock();
            return err;
        };
        const path = allocator.dupe(u8, job.output) catch |err| {
            self.mutex.unlock();
            allocator.free(root);
            return err;
        };
        const ended = job.term != null;
        self.mutex.unlock();
        defer allocator.free(root);
        defer allocator.free(path);
        var dir = try common.openWorkspace(root, false);
        defer dir.close(common.defaultIo());
        const file = dir.openFile(common.defaultIo(), path, .{ .follow_symlinks = false, .resolve_beneath = true }) catch |err| switch (err) {
            error.FileNotFound => if (!ended and from == 0) return .{ .text = try allocator.dupe(u8, ""), .next = 0, .truncated = false, .ended = false } else return err,
            else => return err,
        };
        defer file.close(common.defaultIo());
        const size = (try file.stat(common.defaultIo())).size;
        if (from > size) return error.InvalidOffset;
        const prefix: usize = @intCast(@min(from, 3));
        const available = size - from;
        const wanted: usize = @intCast(@min(available, output_limit + 3));
        const buf = try allocator.alloc(u8, prefix + wanted);
        defer allocator.free(buf);
        const got = try file.readPositionalAll(common.defaultIo(), buf, from - prefix);
        if (got < prefix) return error.InvalidOffset;
        for (0..prefix) |i| {
            const seq = sequencePrefix(buf[i..got]);
            if (i + seq.length > prefix or (seq.valid and !seq.complete and !ended and i + seq.length == prefix)) return error.InvalidOffset;
        }
        const result = try decodeOutput(allocator, buf[prefix..got], ended and got == prefix + available, output_limit);
        return .{ .text = result.text, .next = from + result.consumed, .truncated = false, .ended = ended and from + result.consumed == size };
    }

    pub fn noticeWaiting(self: *Jobs) bool {
        self.lock();
        defer self.mutex.unlock();
        for (self.list.items) |job| {
            if (job.owesNotice()) return true;
        }
        return false;
    }

    pub fn takeNotice(self: *Jobs, allocator: std.mem.Allocator) !?Notice {
        self.lock();
        defer self.mutex.unlock();
        var text = std.ArrayList(u8).empty;
        errdefer text.deinit(allocator);
        var first: ?[]const u8 = null;
        for (self.list.items) |job| {
            if (!job.owesNotice()) continue;
            try appendNotice(allocator, &text, job);
            first = first orelse job.id;
        }
        const task_id = first orelse return null;
        const owned = try text.toOwnedSlice(allocator);
        for (self.list.items) |job| {
            if (job.owesNotice()) job.noticed = true;
        }
        return .{ .task_id = task_id, .text = owned };
    }

    pub fn anyRunning(self: *Jobs) bool {
        self.reap();
        self.lock();
        defer self.mutex.unlock();
        for (self.list.items) |job| {
            if (job.term == null) return true;
        }
        return false;
    }

    pub fn endAll(self: *Jobs) void {
        self.lock();
        const jobs = self.list.items;
        self.mutex.unlock();
        for (jobs) |job| {
            self.lock();
            if (job.term == null and job.cause == null) {
                job.killed = true;
                job.cause = .session_closed;
            }
            self.mutex.unlock();
            self.end(job);
        }
    }

    fn find(self: *Jobs, output_path: []const u8) ?*Job {
        for (self.list.items) |job| if (std.mem.eql(u8, job.output, output_path)) return job;
        return null;
    }

    fn reap(self: *Jobs) void {
        while (true) {
            self.lock();
            const watcher = for (self.list.items) |job| {
                if (job.term == null) continue;
                const thread = job.watcher orelse continue;
                job.watcher = null;
                break thread;
            } else null;
            self.mutex.unlock();
            (watcher orelse return).join();
        }
    }

    fn end(self: *Jobs, job: *Job) void {
        self.lock();
        const settled = job.term != null;
        const watcher = job.watcher;
        job.watcher = null;
        self.mutex.unlock();
        if (!settled) process_runner.endGroup(job.leader);
        if (watcher) |thread| thread.join();
    }

    fn appendNotice(allocator: std.mem.Allocator, text: *std.ArrayList(u8), job: *const Job) !void {
        if (text.items.len > 0) try text.appendSlice(allocator, "\n\n");
        const ended = if (job.limited) try std.fmt.allocPrint(allocator, limited_format, .{(job.limit_ms + std.time.ms_per_s - 1) / std.time.ms_per_s}) else switch (job.term.?) {
            .exited => |code| try std.fmt.allocPrint(allocator, "exited with code {d}", .{code}),
            .signal => |sig| try std.fmt.allocPrint(allocator, "ended by signal {d}", .{@backingInt(sig)}),
            else => try allocator.dupe(u8, "ended"),
        };
        defer allocator.free(ended);
        const tail = try outputTail(allocator, job.*);
        defer allocator.free(tail);
        const line = try std.fmt.allocPrint(allocator, notice_format, .{ job.id, ended, job.output, tail });
        defer allocator.free(line);
        try text.appendSlice(allocator, line);
    }

    fn outputTail(allocator: std.mem.Allocator, job: Job) ![]u8 {
        var dir = common.openWorkspace(job.workspace_root, false) catch return allocator.dupe(u8, "");
        defer dir.close(common.defaultIo());
        const file = dir.openFile(common.defaultIo(), job.output, .{}) catch return allocator.dupe(u8, "");
        defer file.close(common.defaultIo());
        const size = (file.stat(common.defaultIo()) catch return allocator.dupe(u8, "")).size;
        const length: usize = @intCast(@min(size, notice_tail_bytes));
        const tail = try allocator.alloc(u8, length);
        defer allocator.free(tail);
        const read = file.readPositionalAll(common.defaultIo(), tail, size - length) catch 0;
        return allocator.dupe(u8, wholeCharacters(tail[0..read]));
    }

    fn outputBytes(job: Job) u64 {
        var dir = common.openWorkspace(job.workspace_root, false) catch return 0;
        defer dir.close(common.defaultIo());
        const file = dir.openFile(common.defaultIo(), job.output, .{}) catch return 0;
        defer file.close(common.defaultIo());
        const st = file.stat(common.defaultIo()) catch return 0;
        return st.size;
    }
};

const notice_tail_bytes = 2048;
const commands_move = builtin.os.tag != .windows;
const limiter_retry_ms = 1_000;
const stop_drain_bytes = 1024 * 1024;
const default_foreground_budget_ms: u64 = 5_000;
const command_limit_ms: u64 = 60 * 60 * 1_000;
const limited_format = "was ended because it reached its time limit of {d} seconds; no command may run longer than 1 hour";
const moved_tail_bytes = 4096;
const moved_format =
    \\The command is still running in the background as job {s}. It has not failed.
    \\It has written {d} bytes so far. The last of them are at the end of this message, and all of its output goes to {s}.
    \\You will be told when the command ends, so keep working.
    \\
    \\{s}
;
const notice_format =
    \\[SYSTEM NOTIFICATION - NOT USER INPUT]
    \\Background job {s} {s}. Its output is in {s}; the end of it:
    \\{s}
;

const end_directory_script =
    \\__oap_cmd=$1
    \\__oap_path=$2
    \\set --
    \\eval "$__oap_cmd"
    \\__oap_rc=$?
    \\set +x
    \\{ pwd > "$__oap_path"; } 2>/dev/null
    \\exit "$__oap_rc"
;

const MarkerDir = struct {
    path: []u8,
    name: []u8,

    fn create(allocator: std.mem.Allocator) !MarkerDir {
        const base = compat.getEnvVarOwned(allocator, "TMPDIR") catch null;
        defer if (base) |value| allocator.free(value);
        const candidate = std.mem.trimEnd(u8, base orelse "/tmp", "/");
        const root = if (candidate.len == 0 or !std.Io.Dir.path.isAbsolute(candidate)) "/tmp" else candidate;
        var bytes: [16]u8 = undefined;
        compat.random.fillSecureBytes(&bytes);
        const name = try std.fmt.allocPrint(allocator, "oap-cwd-{x:0>16}{x:0>16}", .{
            std.mem.readInt(u64, bytes[0..8], .little),
            std.mem.readInt(u64, bytes[8..16], .little),
        });
        errdefer allocator.free(name);
        const path = try std.Io.Dir.path.join(allocator, &.{ root, name });
        errdefer allocator.free(path);

        try compat.fs.createPrivateDir(path);
        return .{ .path = path, .name = name };
    }

    fn deinit(self: MarkerDir, allocator: std.mem.Allocator) void {
        allocator.free(self.path);
        allocator.free(self.name);
    }

    fn markerPath(self: MarkerDir, allocator: std.mem.Allocator) ![]u8 {
        return std.Io.Dir.path.join(allocator, &.{ self.path, self.name });
    }

    fn read(self: MarkerDir, io: std.Io, allocator: std.mem.Allocator) !?[]u8 {
        var dir = std.Io.Dir.openDirAbsolute(io, self.path, .{}) catch return null;
        defer dir.close(io);
        const raw = dir.readFileAlloc(io, self.name, allocator, .limited(4096)) catch return null;
        const data = if (raw.len > 0 and raw[raw.len - 1] == '\n') raw[0 .. raw.len - 1] else raw;
        if (data.len == 0) {
            allocator.free(raw);
            return null;
        }
        if (data.ptr != raw.ptr or data.len != raw.len) {
            const holding = allocator.alloc(u8, data.len) catch {
                allocator.free(raw);
                return null;
            };
            @memcpy(holding, data);
            allocator.free(raw);
            return holding;
        }
        return raw;
    }

    fn remove(self: MarkerDir, io: std.Io) void {
        var dir = std.Io.Dir.openDirAbsolute(io, self.path, .{}) catch return;
        dir.deleteFile(io, self.name) catch {};
        dir.close(io);
        compat.fs.removeDir(self.path);
    }
};

fn prepareMarker(allocator: std.mem.Allocator, marker: *?MarkerDir, path: *[]u8) !?[]u8 {
    const created = MarkerDir.create(allocator) catch |err| switch (err) {
        error.OutOfMemory => return err,
        else => return null,
    };
    const made = created.markerPath(allocator) catch |err| {
        created.remove(common.defaultIo());
        created.deinit(allocator);
        return err;
    };
    marker.* = created;
    path.* = made;
    return made;
}

const ReportDirectory = struct {
    directory: []const u8,
    observed: bool,
};

fn reportDirectory(term: std.process.Child.Term, parsed: ?[]const u8, start: []const u8) ReportDirectory {
    if (term != .exited) return .{ .directory = start, .observed = false };
    const found = parsed orelse return .{ .directory = start, .observed = false };
    return .{ .directory = found, .observed = true };
}

pub fn sessionTools(jobs: *Jobs) [2]agent.AgentTool {
    var shell_tool = execute_tool;
    shell_tool.runtime_ctx = jobs;
    shell_tool.runtime_execute = executeIn;
    return .{ shell_tool, .{
        .label = "Shell job",
        .name = job_tool_name,
        .description = "Report a background job started with Shell run_in_background, named by the output_path that call returned: running, or its exit code or signal, with the bytes written to its output file. kill: true ends the job and everything it started.",
        .short_description = "Check or stop a background shell job.",
        .parameters_schema_json = schema_job,
        .execute = unavailableJob,
        .runtime_ctx = jobs,
        .runtime_execute = executeJob,
        .operation = .read,
    } };
}

fn unavailableJob(tool_call_id: []const u8, args_json: []const u8, cancel_token: ?ai_types.CancelToken, on_update_ctx: ?*anyopaque, on_update: ?agent.ToolUpdateCallback, allocator: std.mem.Allocator) anyerror!agent.AgentToolResult {
    return executeJob(null, tool_call_id, args_json, cancel_token, on_update_ctx, on_update, allocator);
}

fn executeJob(ctx: ?*anyopaque, tool_call_id: []const u8, args_json: []const u8, cancel_token: ?ai_types.CancelToken, on_update_ctx: ?*anyopaque, on_update: ?agent.ToolUpdateCallback, allocator: std.mem.Allocator) anyerror!agent.AgentToolResult {
    _ = tool_call_id;
    _ = cancel_token;
    _ = on_update_ctx;
    _ = on_update;
    const jobs: *Jobs = @ptrCast(@alignCast(ctx orelse return error.BackgroundJobsUnavailable));
    var parsed = try common.parseArgs(allocator, args_json);
    defer parsed.deinit();
    const obj = parsed.value.object;
    const reported = try jobs.report(try common.requiredString(obj, "path"), common.optionalBool(obj, "kill", false));
    const details = try common.jsonString(allocator, reported);
    errdefer allocator.free(details);
    const text = try allocator.dupe(u8, details);
    errdefer allocator.free(text);
    return common.makeTextResultOwned(allocator, text, details);
}

fn executeIn(ctx: ?*anyopaque, tool_call_id: []const u8, args_json: []const u8, cancel_token: ?ai_types.CancelToken, on_update_ctx: ?*anyopaque, on_update: ?agent.ToolUpdateCallback, allocator: std.mem.Allocator) anyerror!agent.AgentToolResult {
    var parsed = try common.parseArgs(allocator, args_json);
    defer parsed.deinit();
    const obj = parsed.value.object;
    const held: ?*Jobs = if (ctx) |given| @ptrCast(@alignCast(given)) else null;
    if (!common.optionalBool(obj, "run_in_background", false)) return runForeground(held, tool_call_id, args_json, cancel_token, on_update_ctx, on_update, allocator);
    if (common.isCancelled(cancel_token)) return error.Cancelled;
    const jobs = held orelse return error.BackgroundJobsUnavailable;
    const started = try startBackground(jobs, obj, tool_call_id, cancel_token, allocator);
    const details = try common.jsonString(allocator, .{ .ok = true, .task_id = started.task_id, .output_path = started.output_path });
    errdefer allocator.free(details);
    const text = try std.fmt.allocPrint(allocator, "started background job {s}; its stdout and stderr go to {s}. Read that file for output, and pass its path to ShellJob to check or stop the job. It ends when the session closes or at its time limit of at most 1 hour.", .{ started.task_id, started.output_path });
    errdefer allocator.free(text);
    return common.makeTextResultOwned(allocator, text, details);
}

pub const execute_tool = agent.AgentTool{
    .label = "Shell",
    .name = "Shell",
    .description = "Run a shell command in the workspace and return stdout, stderr, exit status, duration, and byte counts. Large output is stored as a retrievable artifact.",
    .short_description = "Run shell command; past 20 KiB, shows head and tail and saves the rest.",
    .parameters_schema_json = schema_execute,
    .execute = execute,
    .operation = .shell,
};

pub fn execute(
    tool_call_id: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?agent.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!agent.AgentToolResult {
    return executeIn(null, tool_call_id, args_json, cancel_token, on_update_ctx, on_update, allocator);
}

fn startBackground(jobs: *Jobs, obj: std.json.ObjectMap, tool_call_id: []const u8, cancel_token: ?ai_types.CancelToken, allocator: std.mem.Allocator) !Jobs.Adopted {
    if (comptime !commands_move) return error.Unsupported;
    const workspace_root = try common.requiredString(obj, "workspace_root");
    const command = try common.requiredString(obj, "command");
    var dir = try common.openWorkspace(workspace_root, false);
    defer dir.close(common.defaultIo());
    const started_ms = common.nowMs();
    const limit_ms = jobs.limitFor(obj);
    const outcome = try process_runner.runWithBudget(allocator, &.{ "/bin/sh", "-c", command }, .{ .dir = dir }, limit_ms, 0, cancel_token);
    defer allocator.free(outcome.moved.stdout_head);
    defer allocator.free(outcome.moved.stderr_head);
    return jobs.adopt(workspace_root, outcome.moved, .{ .tool_call_id = tool_call_id, .limit_ms = limit_ms, .started_ms = started_ms, .ends_with_run = false });
}

fn closedOnExecPipe(fds: *[2]std.posix.fd_t) !void {
    if (std.posix.errno(std.posix.system.pipe(fds)) != .SUCCESS) return error.SystemResources;
    for (fds) |fd| _ = std.posix.system.fcntl(fd, std.posix.F.SETFD, @as(c_int, std.posix.FD_CLOEXEC));
}

fn wholeCharacters(bytes: []const u8) []const u8 {
    var first: usize = 0;
    while (first < @min(bytes.len, 3) and bytes[first] & 0xC0 == 0x80) first += 1;
    var end = bytes.len;
    var last = end;
    while (last > first and end - last < 4) {
        last -= 1;
        if (bytes[last] & 0xC0 == 0x80) continue;
        const length = std.unicode.utf8ByteSequenceLength(bytes[last]) catch break;
        if (length > end - last) end = last;
        break;
    }
    const text = bytes[first..end];
    return if (std.unicode.utf8ValidateSlice(text)) text else "(not text)";
}

fn movedResult(allocator: std.mem.Allocator, jobs: *Jobs, workspace_root: []const u8, tool_call_id: []const u8, moved: process_runner.Moved, limit_ms: u64, started_ms: i64) !agent.AgentToolResult {
    defer allocator.free(moved.stdout_head);
    defer allocator.free(moved.stderr_head);
    const adopted = try jobs.adopt(workspace_root, moved, .{ .tool_call_id = tool_call_id, .limit_ms = limit_ms, .started_ms = started_ms, .ends_with_run = true });
    const head = try std.mem.concat(allocator, u8, &.{ moved.stdout_head, moved.stderr_head });
    defer allocator.free(head);
    const shown = wholeCharacters(head[head.len - @min(head.len, moved_tail_bytes) ..]);
    const details = try common.jsonString(allocator, .{ .ok = true, .running = true, .task_id = adopted.task_id, .output_path = adopted.output_path, .bytes = adopted.bytes });
    errdefer allocator.free(details);
    const text = try std.fmt.allocPrint(allocator, moved_format, .{ adopted.task_id, adopted.bytes, adopted.output_path, shown });
    errdefer allocator.free(text);
    return common.makeTextResultOwned(allocator, text, details);
}

fn runForeground(
    jobs: ?*Jobs,
    tool_call_id: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?agent.ToolUpdateCallback,
    allocator: std.mem.Allocator,
) anyerror!agent.AgentToolResult {
    _ = on_update_ctx;
    _ = on_update;
    if (common.isCancelled(cancel_token)) return error.Cancelled;

    const start_ms = common.nowMs();
    var parsed = try common.parseArgs(allocator, args_json);
    defer parsed.deinit();
    const obj = parsed.value.object;
    const workspace_root = try common.requiredString(obj, "workspace_root");
    const command = try common.requiredString(obj, "command");
    const timeout_ms = if (commands_move and jobs != null) jobs.?.limitFor(obj) else @min(common.optionalU64(obj, "timeout_ms", 30_000), @as(u64, std.math.maxInt(i64)));

    var dir = try common.openWorkspace(workspace_root, false);
    defer dir.close(common.defaultIo());

    const start_directory = try std.Io.Dir.path.resolve(allocator, &.{workspace_root});
    defer allocator.free(start_directory);
    var marker: ?MarkerDir = null;
    defer if (marker) |created| created.deinit(allocator);
    defer if (marker) |created| created.remove(common.defaultIo());
    const windows = @import("builtin").os.tag == .windows;
    var marker_path: []u8 = &.{};
    defer allocator.free(marker_path);
    const prepared: ?[]u8 = if (windows) null else try prepareMarker(allocator, &marker, &marker_path);
    const argv: []const []const u8 = if (prepared) |marker_file|
        &.{ "/bin/sh", "-c", end_directory_script, "/bin/sh", command, marker_file }
    else if (windows)
        &.{ "cmd.exe", "/C", command }
    else
        &.{ "/bin/sh", "-c", command };

    const budget_ms: ?u64 = if (jobs) |held| (if (timeout_ms > held.foreground_budget_ms) held.foreground_budget_ms else null) else null;
    const outcome = process_runner.runWithBudget(allocator, argv, .{ .dir = dir }, timeout_ms, budget_ms, cancel_token) catch |err| {
        if (err == error.Cancelled) return err;
        const duration_ms = common.durationMs(start_ms);
        const owned_directory = try allocator.dupe(u8, start_directory);
        errdefer allocator.free(owned_directory);
        const details = try common.jsonString(allocator, .{
            .ok = false,
            .err = @errorName(err),
            .duration_ms = duration_ms,
            .stdout_bytes = 0,
            .stderr_bytes = 0,
            .raw_bytes = 0,
            .working_directory = start_directory,
            .working_directory_observed = false,
        });
        var details_here = true;
        defer if (details_here) allocator.free(details);
        const text = try std.fmt.allocPrint(allocator, "shell command failed: {s}", .{@errorName(err)});
        var text_here = true;
        defer if (text_here) allocator.free(text);
        var failed = try common.makeTextResultOwned(allocator, text, details);
        errdefer failed.deinit(allocator);
        failed.working_directory = ai_types.OwnedSlice(u8).initOwned(owned_directory);
        failed.working_directory_observed = false;
        text_here = false;
        details_here = false;
        return failed;
    };
    const result = switch (outcome) {
        .finished => |finished| finished,
        .moved => |moved| return movedResult(allocator, jobs.?, workspace_root, tool_call_id, moved, timeout_ms, start_ms),
    };
    defer allocator.free(result.stdout);
    defer allocator.free(result.stderr);

    const observed = if (marker) |created| created.read(common.defaultIo(), allocator) catch null else null;
    defer if (observed) |owned| allocator.free(owned);
    const report = reportDirectory(result.term, observed, start_directory);
    const end_directory = report.directory;
    const owned_directory = try allocator.dupe(u8, end_directory);
    errdefer allocator.free(owned_directory);

    const exit_code: ?u8 = switch (result.term) {
        .exited => |code| code,
        else => null,
    };
    const signal: ?u32 = switch (result.term) {
        .signal => |sig| @backingInt(sig),
        else => null,
    };
    const duration_ms = common.durationMs(start_ms);
    const raw_bytes = result.stdout.len + result.stderr.len;
    const details = try common.jsonString(allocator, .{
        .ok = exit_code == 0,
        .exit_code = exit_code,
        .signal = signal,
        .duration_ms = duration_ms,
        .stdout_bytes = result.stdout.len,
        .stderr_bytes = result.stderr.len,
        .raw_bytes = raw_bytes,
        .working_directory = end_directory,
        .working_directory_observed = report.observed,
    });
    defer allocator.free(details);

    const text = try std.fmt.allocPrint(allocator,
        \\stdout:
        \\{s}
        \\stderr:
        \\{s}
    , .{ result.stdout, result.stderr });
    defer allocator.free(text);
    var made = try common.makeTextResultWithArtifact(allocator, .{ .tool_name = "Shell", .call_id = tool_call_id, .text = text, .details_json = details });
    defer if (made.artifact_path) |path| allocator.free(path);
    made.result.working_directory = ai_types.OwnedSlice(u8).initOwned(owned_directory);
    made.result.working_directory_observed = report.observed;
    return made.result;
}

const Case = struct {
    cwd: [:0]u8,
    root: [:0]u8,
    tmp: std.testing.TmpDir,

    fn init() !Case {
        var self = Case{ .cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator), .root = undefined, .tmp = std.testing.tmpDir(.{}) };
        errdefer std.testing.allocator.free(self.cwd);
        const joined = try std.Io.Dir.path.join(std.testing.allocator, &.{ self.cwd, ".zig-cache", "tmp", self.tmp.sub_path[0..] });
        defer std.testing.allocator.free(joined);
        self.root = try std.testing.allocator.dupeSentinel(u8, joined, 0);
        return self;
    }

    fn deinit(self: *Case) void {
        std.testing.allocator.free(self.root);
        std.testing.allocator.free(self.cwd);
        self.tmp.cleanup();
    }

    fn args(self: *const Case, command: []const u8) ![:0]u8 {
        return std.fmt.allocPrintSentinel(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"{s}\"}}", .{ self.root, command }, 0);
    }

    fn run(self: *const Case, call_id: []const u8, command: []const u8) !agent.AgentToolResult {
        const argv_json = try self.args(command);
        defer std.testing.allocator.free(argv_json);
        return execute(call_id, argv_json, null, null, null, std.testing.allocator);
    }
};

fn movedCase(jobs: *Jobs, case: *const Case, command: []const u8) !agent.AgentToolResult {
    const args = try case.args(command);
    defer std.testing.allocator.free(args);
    const tools = sessionTools(jobs);
    return tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-moved", args, null, null, null, std.testing.allocator);
}

fn waitForNotice(jobs: *Jobs) !void {
    const by = common.nowMs() + 5_000;
    while (!jobs.noticeWaiting() and common.nowMs() < by) compat.time.sleepMs(5);
    try std.testing.expect(jobs.noticeWaiting());
}

fn expectTask(jobs: *Jobs, status: Jobs.Status, cause: Jobs.Cause) !void {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const tasks = try jobs.tasks(arena.allocator());
    try std.testing.expectEqual(status, tasks[0].status);
    try std.testing.expectEqual(@as(?Jobs.Cause, cause), tasks[0].cause);
}

test "a command past its budget returns while it keeps running, and its end is reported" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    jobs.foreground_budget_ms = 1_000;

    const began = common.nowMs();
    var result = try movedCase(&jobs, &case, "echo early; sleep 4; echo late");
    defer result.deinit(std.testing.allocator);
    try std.testing.expect(common.nowMs() - began < 3_500);
    const text = result.content.slice()[0].text.text;
    try std.testing.expect(std.mem.indexOf(u8, text, "still running") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "early") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "late") == null);
    try std.testing.expect(jobs.anyRunning());

    try waitForNotice(&jobs);
    const notice = (try jobs.takeNotice(std.testing.allocator)).?;
    defer notice.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.indexOf(u8, notice.text, "exited with code 0") != null);
    try std.testing.expect(std.mem.indexOf(u8, notice.text, "early\nlate") != null);
    try expectTask(&jobs, .completed, .finished);
}

test "a moved result shows text whose last bytes cut a character at either end" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    jobs.foreground_budget_ms = 1_000;

    var result = try movedCase(&jobs, &case, "awk 'BEGIN { for (i = 0; i < 3000; i++) printf \\\"\\\\303\\\\251\\\"; printf \\\"tail\\\\303\\\" }'; sleep 30");
    defer result.deinit(std.testing.allocator);
    jobs.endAll();
    const text = result.content.slice()[0].text.text;
    try std.testing.expect(std.mem.indexOf(u8, text, "(not text)") == null);
    try std.testing.expect(std.mem.indexOf(u8, text, "tail") != null);
}

test "a command started while a job runs inherits none of the job's pipes" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();

    var before = try movedCase(&jobs, &case, "ls /dev/fd | wc -l");
    defer before.deinit(std.testing.allocator);
    jobs.foreground_budget_ms = 50;
    var moved = try movedCase(&jobs, &case, "sleep 30");
    defer moved.deinit(std.testing.allocator);
    jobs.foreground_budget_ms = default_foreground_budget_ms;
    var after = try movedCase(&jobs, &case, "ls /dev/fd | wc -l");
    defer after.deinit(std.testing.allocator);
    jobs.endAll();
    try std.testing.expectEqualStrings(before.content.slice()[0].text.text, after.content.slice()[0].text.text);
}

test "a command within its budget answers as it always did and leaves no job" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();

    var result = try movedCase(&jobs, &case, "echo out; echo err 1>&2");
    defer result.deinit(std.testing.allocator);
    const text = result.content.slice()[0].text.text;
    try std.testing.expect(std.mem.indexOf(u8, text, "stdout:\nout") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "stderr:\nerr") != null);
    try std.testing.expectEqual(@as(usize, 0), jobs.list.items.len);
}

test "a command that reaches its limit is ended and the model is told why" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    jobs.foreground_budget_ms = 50;
    jobs.limit_ms = 300;

    var result = try movedCase(&jobs, &case, "sleep 30");
    defer result.deinit(std.testing.allocator);
    try waitForNotice(&jobs);
    const notice = (try jobs.takeNotice(std.testing.allocator)).?;
    defer notice.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.indexOf(u8, notice.text, "reached its time limit") != null);
    const report = try jobs.report(jobs.list.items[0].output, false);
    try std.testing.expectEqualStrings("time_limit", report.reason.?);
    try expectTask(&jobs, .failed, .expired);
}

const EndlessCopy = struct {
    done: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),

    fn run(self: *EndlessCopy, stdout: std.posix.fd_t, stderr: std.posix.fd_t, stop: std.posix.fd_t, file: std.Io.File) void {
        Jobs.copyOutput(stdout, stderr, stop, file);
        self.done.store(true, .release);
    }
};

test "the copier stops once the job has ended, however much a surviving writer still sends" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const io = common.defaultIo();
    const stdout = try std.Io.Dir.openFileAbsolute(io, "/dev/zero", .{});
    const stderr = try std.Io.Dir.openFileAbsolute(io, "/dev/zero", .{});
    const sink = try std.Io.Dir.openFileAbsolute(io, "/dev/null", .{ .mode = .write_only });
    var stop: [2]std.posix.fd_t = undefined;
    try closedOnExecPipe(&stop);
    var copy = EndlessCopy{};
    const thread = try std.Thread.spawn(.{}, EndlessCopy.run, .{ &copy, stdout.handle, stderr.handle, stop[0], sink });
    _ = std.posix.system.close(stop[1]);
    const by = common.nowMs() + 3_000;
    while (!copy.done.load(.acquire) and common.nowMs() < by) compat.time.sleepMs(5);
    if (!copy.done.load(.acquire)) {
        thread.detach();
        return error.CopierNeverStopped;
    }
    thread.join();
}

test "a moved command's task names the tool call that started it" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    jobs.foreground_budget_ms = 50;
    var result = try movedCase(&jobs, &case, "sleep 30");
    defer result.deinit(std.testing.allocator);
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const tasks = try jobs.tasks(arena.allocator());
    try std.testing.expectEqualStrings("call-moved", tasks[0].tool_call_id);
    try std.testing.expectEqual(Jobs.Status.running, tasks[0].status);
}

test "a timeout above the limit is held to the limit" {
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    var parsed = try common.parseArgs(std.testing.allocator, "{\"timeout_ms\":7200000}");
    defer parsed.deinit();
    try std.testing.expectEqual(@as(u64, 3_600_000), jobs.limitFor(parsed.value.object));
    var none = try common.parseArgs(std.testing.allocator, "{}");
    defer none.deinit();
    try std.testing.expectEqual(@as(u64, 3_600_000), jobs.limitFor(none.value.object));
}

test "a background job reaches its limit too, and a run's cancel leaves it alone" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    jobs.limit_ms = 300;
    jobs.beginRun();
    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"sleep 30\",\"run_in_background\":true}}", .{case.root});
    defer std.testing.allocator.free(args);
    const tools = sessionTools(&jobs);
    var result = try tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-bg", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);

    jobs.endMoved();
    compat.time.sleepMs(50);
    try std.testing.expect(jobs.anyRunning());
    try waitForNotice(&jobs);
    try std.testing.expect(jobs.list.items[0].limited);
    try expectTask(&jobs, .failed, .expired);
}

test "a limit reached by a job already being cancelled leaves the cancel as its reason" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    try startJob(&jobs, &case, "sleep 30", "call-cancelled");
    const job = jobs.list.items[0];
    jobs.lock();
    job.killed = true;
    job.cause = .cancelled_by_model;
    job.deadline_ms = 0;
    jobs.mutex.unlock();

    _ = jobs.endExpired();

    const reported = try jobs.report(job.output, false);
    try std.testing.expectEqual(@as(?[]const u8, null), reported.reason);
    try std.testing.expectEqual(Jobs.Cause.cancelled_by_model, job.cause.?);
}

test "cancelling the run ends the command it moved and reports nothing" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    jobs.foreground_budget_ms = 50;
    jobs.beginRun();

    var result = try movedCase(&jobs, &case, "sleep 30");
    defer result.deinit(std.testing.allocator);
    jobs.endMoved();
    const by = common.nowMs() + 5_000;
    while (jobs.anyRunning() and common.nowMs() < by) compat.time.sleepMs(5);
    try std.testing.expect(!jobs.anyRunning());
    try std.testing.expect(!jobs.noticeWaiting());
    try expectTask(&jobs, .cancelled, .cancelled_by_host);
}

test "a command that moves just after its run's cancel is ended too" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    jobs.foreground_budget_ms = 50;
    jobs.beginRun();
    jobs.endMoved();

    var result = try movedCase(&jobs, &case, "sleep 30");
    defer result.deinit(std.testing.allocator);
    const by = common.nowMs() + 5_000;
    while (jobs.anyRunning() and common.nowMs() < by) compat.time.sleepMs(5);
    try std.testing.expect(!jobs.anyRunning());
    try std.testing.expect(!jobs.noticeWaiting());
    try expectTask(&jobs, .cancelled, .cancelled_by_host);
}

test "the next run's commands survive the last run's cancel" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    jobs.foreground_budget_ms = 50;
    jobs.beginRun();
    jobs.endMoved();
    jobs.beginRun();

    var result = try movedCase(&jobs, &case, "sleep 30");
    defer result.deinit(std.testing.allocator);
    compat.time.sleepMs(100);
    try std.testing.expect(jobs.anyRunning());
    jobs.endAll();
}

test "a background command given a timeout shorter than its start is still a job, and the limit ends it" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();

    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"sleep 30\",\"run_in_background\":true,\"timeout_ms\":1}}", .{case.root});
    defer std.testing.allocator.free(args);
    const tools = sessionTools(&jobs);
    var result = try tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-tiny", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    try waitForNotice(&jobs);
    const notice = (try jobs.takeNotice(std.testing.allocator)).?;
    defer notice.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.indexOf(u8, notice.text, "time limit of 1 seconds") != null);
}

test "a later run's cancel leaves a job an earlier run moved alone" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    jobs.foreground_budget_ms = 50;
    jobs.beginRun();
    var result = try movedCase(&jobs, &case, "sleep 30");
    defer result.deinit(std.testing.allocator);

    jobs.beginRun();
    jobs.endMoved();
    compat.time.sleepMs(100);
    try std.testing.expect(jobs.anyRunning());
}

test "shell execute reports the directory the command ended in" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var result = try case.run("call-cd", "mkdir -p sub && cd sub && pwd");
    defer result.deinit(std.testing.allocator);
    const expected = try std.Io.Dir.path.resolve(std.testing.allocator, &.{ case.root, "sub" });
    defer std.testing.allocator.free(expected);
    try std.testing.expectEqualStrings(expected, result.workingDirectory().?);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, expected) != null);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, "call-cd") == null);
}

test "the reported directory is the shell's logical path, not a resolved one" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    try case.tmp.dir.createDir(common.defaultIo(), "nested", .default_dir);
    try std.Io.Dir.symLink(case.tmp.dir, common.defaultIo(), "nested", "alias", .{});
    var result = try case.run("call-logical", "cd alias && pwd");
    defer result.deinit(std.testing.allocator);
    const expected = try std.Io.Dir.path.join(std.testing.allocator, &.{ case.root, "alias" });
    defer std.testing.allocator.free(expected);
    try std.testing.expectEqualStrings(expected, result.workingDirectory().?);
}

test "shell execute reports the start directory when the command does not move" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var result = try case.run("call-still", "true");
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings(case.root, result.workingDirectory().?);
}

test "shell execute reports the start directory when the command replaces the shell" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var result = try case.run("call-exec", "exec pwd");
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings(case.root, result.workingDirectory().?);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, case.root) != null);
}

test "an exit trap's output reaches the model" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var result = try case.run("call-trap", "trap 'echo from-the-trap' EXIT");
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings(case.root, result.workingDirectory().?);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, "from-the-trap") != null);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, "call-trap") == null);
}

test "the marker is gone and the reported byte count matches the command's own output" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var result = try case.run("call-nl", "echo ok");
    defer result.deinit(std.testing.allocator);
    const text = result.content.slice()[0].text.text;
    try std.testing.expect(std.mem.indexOf(u8, text, "call-nl") == null);
    try std.testing.expect(std.mem.indexOf(u8, text, "ok") != null);
    try std.testing.expect(std.mem.indexOf(u8, result.getDetailsJson().?, "\"stdout_bytes\":3") != null);
}

test "the command sees no positional parameters, as it did before the wrapper" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"printf '[%s][%s]' \\\"$1\\\" \\\"$2\\\"\"}}", .{cwd});
    defer std.testing.allocator.free(args);
    var result = try execute("call-args", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, "[]") != null);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, "call-args") == null);
}

test "a command that reassigns the positional parameters still reports its directory" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var result = try case.run("call-clobber", "set -- clobbered; pwd");
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings(case.root, result.workingDirectory().?);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, case.root) != null);
}

test "a marker is only read when the wrapper exited normally" {
    const start = "/start";
    const exited_ok = reportDirectory(.{ .exited = 0 }, "/parsed", start);
    try std.testing.expectEqualStrings("/parsed", exited_ok.directory);
    try std.testing.expect(exited_ok.observed);
    const exited_failed = reportDirectory(.{ .exited = 7 }, "/parsed", start);
    try std.testing.expectEqualStrings("/parsed", exited_failed.directory);
    try std.testing.expect(exited_failed.observed);
    const no_marker = reportDirectory(.{ .exited = 0 }, null, start);
    try std.testing.expectEqualStrings(start, no_marker.directory);
    try std.testing.expect(!no_marker.observed);
    for ([_]std.process.Child.Term{ .{ .signal = .KILL }, .{ .stopped = .KILL }, .{ .unknown = 0 } }) |term| {
        const untrusted = reportDirectory(term, "/parsed", start);
        try std.testing.expectEqualStrings(start, untrusted.directory);
        try std.testing.expect(!untrusted.observed);
    }
}

test "a command that exits early is reported as unobserved, not as the start directory" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    for ([_][]const u8{ "mkdir -p sub && cd sub && set -e && false", "mkdir -p sub && cd sub && exit 0" }) |command| {
        var result = try case.run("call-early", command);
        defer result.deinit(std.testing.allocator);
        try std.testing.expectEqualStrings(case.root, result.workingDirectory().?);
        try std.testing.expect(std.mem.indexOf(u8, result.getDetailsJson().?, "\"working_directory_observed\":false") != null);
    }
}

test "a killed command reports the start directory as unobserved" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var result = try case.run("call-killed", "kill -9 $$");
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings(case.root, result.workingDirectory().?);
    try std.testing.expect(std.mem.indexOf(u8, result.getDetailsJson().?, "\"working_directory_observed\":false") != null);
}

test "a command cannot mask its failure by leaving IFS set" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    for ([_]struct { command: []const u8, code: []const u8 }{
        .{ .command = "IFS=1; false", .code = "1" },
        .{ .command = "IFS=12; exit 10", .code = "10" },
    }) |c| {
        var result = try case.run("call-ifs", c.command);
        defer result.deinit(std.testing.allocator);
        const details = result.getDetailsJson().?;
        const expected = try std.fmt.allocPrint(std.testing.allocator, "\"exit_code\":{s}", .{c.code});
        defer std.testing.allocator.free(expected);
        try std.testing.expect(std.mem.indexOf(u8, details, expected) != null);
        try std.testing.expect(std.mem.indexOf(u8, details, "\"ok\":true") == null);
    }
}

test "a command whose output exceeds the cap fails promptly, unobserved, without hanging" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    const argv_json = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"head -c 20000000 /dev/zero | tr '\\\\0' x\",\"timeout_ms\":60000}}", .{case.root});
    defer std.testing.allocator.free(argv_json);
    const started = common.nowMs();
    var result = try execute("call-cap", argv_json, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    const elapsed_ms: u64 = @intCast(@max(0, common.nowMs() - started));
    try std.testing.expectEqualStrings(case.root, result.workingDirectory().?);
    try std.testing.expect(!result.working_directory_observed);
    try std.testing.expect(result.observedWorkingDirectory() == null);
    try std.testing.expect(std.mem.indexOf(u8, result.getDetailsJson().?, "StreamTooLong") != null);
    try std.testing.expect(elapsed_ms < 20_000);
}
const MarkerCase = struct {
    fn run(failing: std.mem.Allocator) !void {
        var created = try MarkerDir.create(std.testing.allocator);
        errdefer {
            created.remove(common.defaultIo());
            created.deinit(std.testing.allocator);
        }
        var marker_path: []u8 = &.{};
        defer failing.free(marker_path);
        marker_path = try created.markerPath(failing);
        created.remove(common.defaultIo());
        created.deinit(std.testing.allocator);
        created = undefined;
    }
};

fn countMarkers(root: []const u8, out: *[64][64]u8) usize {
    var dir = std.Io.Dir.openDirAbsolute(common.defaultIo(), root, .{ .iterate = true }) catch return 0;
    defer dir.close(common.defaultIo());
    var it: std.Io.Dir.Iterator = dir.iterate();
    var n: usize = 0;
    while (it.next(common.defaultIo()) catch null) |entry| {
        if (!std.mem.startsWith(u8, entry.name, "oap-cwd-")) continue;
        if (n >= out.len or entry.name.len > out[n].len) continue;
        @memset(&out[n], 0);
        @memcpy(out[n][0..entry.name.len], entry.name);
        n += 1;
    }
    return n;
}

test "an exhausted allocator leaves no private directory behind" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const base = compat.getEnvVarOwned(std.testing.allocator, "TMPDIR") catch null;
    defer if (base) |value| std.testing.allocator.free(value);
    const candidate = std.mem.trimEnd(u8, base orelse "/tmp", "/");
    const root = if (candidate.len == 0 or !std.Io.Dir.path.isAbsolute(candidate)) "/tmp" else candidate;
    var before: [64][64]u8 = undefined;
    const seen_before = countMarkers(root, &before);
    try std.testing.checkAllAllocationFailures(std.testing.allocator, MarkerCase.run, .{});
    var after: [64][64]u8 = undefined;
    const seen_after = countMarkers(root, &after);
    for (after[0..seen_after]) |name| {
        var existed = false;
        for (before[0..seen_before]) |prior| {
            if (std.mem.eql(u8, &prior, &name)) existed = true;
        }
        if (!existed) return error.TestUnexpectedResult;
    }
}

test "shell execute captures stdout" {
    const cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"echo hello\"}}", .{cwd});
    defer std.testing.allocator.free(args);
    var result = try execute("call", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, "hello") != null);
    try std.testing.expect(result.getDetailsJson().?.len > 0);
}

test "shell execute supports filesystem root workspace" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var artifact_root = common.TestArtifactRoot.init();
    defer artifact_root.deinit();
    var result = try execute("call-root", "{\"workspace_root\":\"/\",\"command\":\"pwd\",\"timeout_ms\":10000}", null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.startsWith(u8, result.content.slice()[0].text.text, "stdout:\n/\n"));
}

test "shell execute stores output over 32 KiB as an artifact, regardless of compact_output" {
    var artifact_root = common.TestArtifactRoot.init();
    defer artifact_root.deinit();
    const cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const large_args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"python3 - <<'PY'\\nimport sys\\nsys.stdout.write('x' * 40000)\\nPY\"}}", .{cwd});
    defer std.testing.allocator.free(large_args);
    var large = try execute("call-large", large_args, null, null, null, std.testing.allocator);
    defer large.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.indexOf(u8, large.content.slice()[0].text.text, "lines omitted") != null);
    try std.testing.expect(std.mem.indexOf(u8, large.content.slice()[0].text.text, "read the rest with Shell") != null);
    try std.testing.expect(std.mem.indexOf(u8, large.getDetailsJson().?, "\"compressed\":true") != null);
    try std.testing.expectEqual(@as(usize, 1), large.artifacts.slice().len);
    const small_args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"echo ok\",\"compact_output\":true}}", .{cwd});
    defer std.testing.allocator.free(small_args);
    var small = try execute("call-small", small_args, null, null, null, std.testing.allocator);
    defer small.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.startsWith(u8, small.content.slice()[0].text.text, "stdout:\nok\n"));
    try std.testing.expectEqual(@as(usize, 0), small.artifacts.slice().len);
}

test "shell execute reports timeout and clamps large timeout" {
    const cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"sleep 1\",\"timeout_ms\":1}}", .{cwd});
    defer std.testing.allocator.free(args);
    var result = try execute("call", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.indexOf(u8, result.getDetailsJson().?, "Timeout") != null);
    const huge_args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"echo ok\",\"timeout_ms\":\"18446744073709551615\"}}", .{cwd});
    defer std.testing.allocator.free(huge_args);
    var huge = try execute("call", huge_args, null, null, null, std.testing.allocator);
    defer huge.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.indexOf(u8, huge.content.slice()[0].text.text, "ok") != null);
}

fn backgroundArgs(case: *const Case, command: []const u8) ![]u8 {
    return std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"{s}\",\"run_in_background\":true}}", .{ case.root, command });
}

fn startJob(jobs: *Jobs, case: *const Case, command: []const u8, call_id: []const u8) !void {
    const args = try backgroundArgs(case, command);
    defer std.testing.allocator.free(args);
    const tools = sessionTools(jobs);
    var result = try tools[0].runtime_execute.?(tools[0].runtime_ctx, call_id, args, null, null, null, std.testing.allocator);
    result.deinit(std.testing.allocator);
}

fn waitForOutput(case: *const Case, path: []const u8, needle: []const u8) !void {
    const by = common.nowMs() + 5_000;
    while (common.nowMs() < by) {
        const data = case.tmp.dir.readFileAlloc(common.defaultIo(), path, std.testing.allocator, .limited(1 << 20)) catch "";
        defer if (data.len > 0) std.testing.allocator.free(data);
        if (std.mem.indexOf(u8, data, needle) != null) return;
        compat.time.sleepMs(20);
    }
    return error.TestUnexpectedResult;
}

const ticking = "i=0; while true; do echo tick $i; i=$((i+1)); sleep 0.05; done";

test "a background job outlives its call, its output is readable while it runs, and it ends when the session's jobs end" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    var open = true;
    defer if (open) jobs.deinit();
    const tools = sessionTools(&jobs);
    const args = try backgroundArgs(&case, ticking);
    defer std.testing.allocator.free(args);

    var result = try tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-bg", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    const output_path = try std.testing.allocator.dupe(u8, jobs.list.items[0].output);
    defer std.testing.allocator.free(output_path);
    try std.testing.expect(std.mem.indexOf(u8, result.getDetailsJson().?, output_path) != null);

    try waitForOutput(&case, output_path, "tick 3");
    const running = try jobs.report(output_path, false);
    try std.testing.expectEqualStrings("running", running.status);
    try std.testing.expect(running.bytes > 0);

    jobs.deinit();
    open = false;
    const stopped = try case.tmp.dir.readFileAlloc(common.defaultIo(), output_path, std.testing.allocator, .limited(1 << 20));
    defer std.testing.allocator.free(stopped);
    compat.time.sleepMs(300);
    const later = try case.tmp.dir.readFileAlloc(common.defaultIo(), output_path, std.testing.allocator, .limited(1 << 20));
    defer std.testing.allocator.free(later);
    try std.testing.expectEqual(stopped.len, later.len);
}

test "ShellJob reports a finished job's exit code and ends a running one on kill" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    const tools = sessionTools(&jobs);

    const quick = try backgroundArgs(&case, "echo done; exit 3");
    defer std.testing.allocator.free(quick);
    var first = try tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-quick", quick, null, null, null, std.testing.allocator);
    defer first.deinit(std.testing.allocator);
    const slow = try backgroundArgs(&case, ticking);
    defer std.testing.allocator.free(slow);
    var second = try tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-slow", slow, null, null, null, std.testing.allocator);
    defer second.deinit(std.testing.allocator);

    const quick_path = jobs.list.items[0].output;
    const by = common.nowMs() + 5_000;
    var reported = try jobs.report(quick_path, false);
    while (std.mem.eql(u8, reported.status, "running") and common.nowMs() < by) : (reported = try jobs.report(quick_path, false)) compat.time.sleepMs(20);
    try std.testing.expectEqualStrings("exited", reported.status);
    try std.testing.expectEqual(@as(?u8, 3), reported.exit_code);

    const kill_args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"path\":\"{s}\",\"kill\":true}}", .{ case.root, jobs.list.items[1].output });
    defer std.testing.allocator.free(kill_args);
    var killed = try tools[1].runtime_execute.?(tools[1].runtime_ctx, "call-kill", kill_args, null, null, null, std.testing.allocator);
    defer killed.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.indexOf(u8, killed.getDetailsJson().?, "\"status\":\"killed\"") != null);
}

const TaskSnapshotAllocationCase = struct {
    fn run(allocator: std.mem.Allocator, jobs: *Jobs) !void {
        const tasks = try jobs.tasks(allocator);
        defer {
            for (tasks) |task| {
                allocator.free(task.task_id);
                allocator.free(task.tool_call_id);
            }
            allocator.free(tasks);
        }
        try std.testing.expectEqual(@as(usize, 2), tasks.len);
        try std.testing.expectEqualStrings("call-2", tasks[1].tool_call_id);
    }
};

test "task snapshot allocation failures release every copied field" {
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.list.deinit(std.testing.allocator);
    var first = Jobs.Job{ .id = @constCast("task-1"), .output = @constCast("one"), .workspace_root = @constCast("/"), .tool_call_id = @constCast("call-1"), .leader = 0 };
    var second = Jobs.Job{ .id = @constCast("task-2"), .output = @constCast("two"), .workspace_root = @constCast("/"), .tool_call_id = @constCast("call-2"), .leader = 0 };
    try jobs.list.append(std.testing.allocator, &first);
    try jobs.list.append(std.testing.allocator, &second);
    try std.testing.checkAllAllocationFailures(std.testing.allocator, TaskSnapshotAllocationCase.run, .{&jobs});
}

test "background task snapshots retain call identity and host cancellation cause" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const tools = sessionTools(&jobs);
    const args = try backgroundArgs(&case, ticking);
    defer std.testing.allocator.free(args);
    var started = try tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-1", args, null, null, null, std.testing.allocator);
    defer started.deinit(std.testing.allocator);
    const before = try jobs.tasks(arena.allocator());
    try std.testing.expectEqual(@as(usize, 1), before.len);
    try std.testing.expectEqualStrings("call-1", before[0].tool_call_id);
    try std.testing.expectEqual(Jobs.Status.running, before[0].status);
    try std.testing.expectEqual(@as(?Jobs.Cause, null), before[0].cause);
    try std.testing.expect(try jobs.cancelTask(before[0].task_id));
    try std.testing.expect(!(try jobs.cancelTask(before[0].task_id)));
    try std.testing.expectError(error.UnknownJob, jobs.cancelTask("missing"));
    const after = try jobs.tasks(arena.allocator());
    try std.testing.expectEqual(Jobs.Status.cancelled, after[0].status);
    try std.testing.expectEqual(@as(?Jobs.Cause, .cancelled_by_host), after[0].cause);
    try std.testing.expect(!jobs.noticeWaiting());
}

test "ending the session records why a running task ended" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const tools = sessionTools(&jobs);
    const args = try backgroundArgs(&case, ticking);
    defer std.testing.allocator.free(args);
    var result = try tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-close", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    jobs.endAll();
    const tasks = try jobs.tasks(arena.allocator());
    try std.testing.expectEqual(Jobs.Status.cancelled, tasks[0].status);
    try std.testing.expectEqual(@as(?Jobs.Cause, .session_closed), tasks[0].cause);
    try std.testing.expect(!jobs.noticeWaiting());
}

test "background task snapshots distinguish successful, failed and model-cancelled jobs" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const tools = sessionTools(&jobs);
    for ([_][]const u8{ "exit 0", "exit 3", ticking }, 0..) |command, index| {
        const args = try backgroundArgs(&case, command);
        defer std.testing.allocator.free(args);
        const call_id = try std.fmt.allocPrint(arena.allocator(), "call-{d}", .{index});
        var result = try tools[0].runtime_execute.?(tools[0].runtime_ctx, call_id, args, null, null, null, std.testing.allocator);
        result.deinit(std.testing.allocator);
    }
    const by = common.nowMs() + 5_000;
    while (common.nowMs() < by) {
        const a = try jobs.report(jobs.list.items[0].output, false);
        const b = try jobs.report(jobs.list.items[1].output, false);
        if (!std.mem.eql(u8, a.status, "running") and !std.mem.eql(u8, b.status, "running")) break;
        compat.time.sleepMs(10);
    }
    _ = try jobs.report(jobs.list.items[2].output, true);
    const tasks = try jobs.tasks(arena.allocator());
    try std.testing.expectEqual(Jobs.Status.completed, tasks[0].status);
    try std.testing.expectEqual(Jobs.Status.failed, tasks[1].status);
    try std.testing.expectEqual(Jobs.Status.cancelled, tasks[2].status);
    try std.testing.expectEqual(@as(?Jobs.Cause, .finished), tasks[0].cause);
    try std.testing.expectEqual(@as(?Jobs.Cause, .finished), tasks[1].cause);
    try std.testing.expectEqual(@as(?Jobs.Cause, .cancelled_by_model), tasks[2].cause);
}

test "a notice whose tail starts inside a character is still text" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();

    const tools = sessionTools(&jobs);
    const wide = try backgroundArgs(&case, "i=0; while [ $i -lt 1500 ]; do printf '\u{e9}'; i=$((i+1)); done; printf x");
    defer std.testing.allocator.free(wide);
    var started = try tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-wide", wide, null, null, null, std.testing.allocator);
    defer started.deinit(std.testing.allocator);

    const by = common.nowMs() + 5_000;
    while (!jobs.noticeWaiting() and common.nowMs() < by) {}
    var notice = (try jobs.takeNotice(std.testing.allocator)) orelse return error.TestUnexpectedResult;
    defer notice.deinit(std.testing.allocator);

    try std.testing.expect(std.mem.indexOf(u8, notice.text, "(not text)") == null);
    try std.testing.expect(std.mem.endsWith(u8, std.mem.trimEnd(u8, notice.text, "\n"), "\u{e9}\u{e9}x"));
}

test "a notice that cannot be built stays owed" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();

    const tools = sessionTools(&jobs);
    const quick = try backgroundArgs(&case, "echo finished");
    defer std.testing.allocator.free(quick);
    var started = try tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-quick", quick, null, null, null, std.testing.allocator);
    defer started.deinit(std.testing.allocator);
    const by = common.nowMs() + 5_000;
    while (!jobs.noticeWaiting() and common.nowMs() < by) {}
    try std.testing.expect(jobs.noticeWaiting());

    var fail_index: usize = 0;
    while (true) : (fail_index += 1) {
        var failing = std.testing.FailingAllocator.init(std.testing.allocator, .{ .fail_index = fail_index });
        const taken = jobs.takeNotice(failing.allocator()) catch |err| {
            try std.testing.expectEqual(error.OutOfMemory, err);
            try std.testing.expect(jobs.noticeWaiting());
            continue;
        };
        var notice = taken orelse return error.TestUnexpectedResult;
        defer notice.deinit(failing.allocator());
        try std.testing.expect(fail_index > 1);
        try std.testing.expect(!jobs.noticeWaiting());
        break;
    }
}

test "anyRunning counts a job until it ends" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    try std.testing.expect(!jobs.anyRunning());

    const tools = sessionTools(&jobs);
    const slow = try backgroundArgs(&case, ticking);
    defer std.testing.allocator.free(slow);
    var started = try tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-slow", slow, null, null, null, std.testing.allocator);
    defer started.deinit(std.testing.allocator);
    try std.testing.expect(jobs.anyRunning());

    _ = try jobs.report(jobs.list.items[0].output, true);
    const by = common.nowMs() + 5_000;
    while (jobs.anyRunning() and common.nowMs() < by) {}
    try std.testing.expect(!jobs.anyRunning());
}

test "a job whose command exits ends what it left running at once, without being polled" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    const tools = sessionTools(&jobs);
    const args = try backgroundArgs(&case, "(while true; do echo tick; sleep 0.05; done) & sleep 0.3; exit 0");
    defer std.testing.allocator.free(args);
    var result = try tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-leaves", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    const output_path = try std.testing.allocator.dupe(u8, jobs.list.items[0].output);
    defer std.testing.allocator.free(output_path);

    try waitForOutput(&case, output_path, "tick");
    compat.time.sleepMs(800);
    const settled = try case.tmp.dir.readFileAlloc(common.defaultIo(), output_path, std.testing.allocator, .limited(1 << 20));
    defer std.testing.allocator.free(settled);
    compat.time.sleepMs(300);
    const later = try case.tmp.dir.readFileAlloc(common.defaultIo(), output_path, std.testing.allocator, .limited(1 << 20));
    defer std.testing.allocator.free(later);
    try std.testing.expectEqual(settled.len, later.len);
}

test "the shell tool outside a session refuses a background job rather than running it in the foreground" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    const args = try backgroundArgs(&case, "sleep 30");
    defer std.testing.allocator.free(args);
    try std.testing.expectError(error.BackgroundJobsUnavailable, execute("call-static", args, null, null, null, std.testing.allocator));
}

test "a finished job's thread is joined by the next job call, and its status and output stay readable" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    const tools = sessionTools(&jobs);
    const quick = try backgroundArgs(&case, "echo done; exit 3");
    defer std.testing.allocator.free(quick);
    var first = try tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-quick", quick, null, null, null, std.testing.allocator);
    defer first.deinit(std.testing.allocator);
    const quick_path = jobs.list.items[0].output;
    const by = common.nowMs() + 5_000;
    while (std.mem.eql(u8, (try jobs.report(quick_path, false)).status, "running") and common.nowMs() < by) compat.time.sleepMs(20);

    var second = try tools[0].runtime_execute.?(tools[0].runtime_ctx, "call-next", quick, null, null, null, std.testing.allocator);
    defer second.deinit(std.testing.allocator);
    try std.testing.expect(jobs.list.items[0].watcher == null);

    const reported = try jobs.report(quick_path, false);
    try std.testing.expectEqualStrings("exited", reported.status);
    try std.testing.expectEqual(@as(?u8, 3), reported.exit_code);
    const output = try case.tmp.dir.readFileAlloc(common.defaultIo(), quick_path, std.testing.allocator, .limited(1 << 20));
    defer std.testing.allocator.free(output);
    try std.testing.expectEqualStrings("done\n", output);
}

test "job completion wakes an attached observer and respects detach" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const Observer = struct {
        fn notify(ctx: *anyopaque) void {
            const ready: *std.Io.Event = @ptrCast(@alignCast(ctx));
            ready.set(std.testing.io);
        }
    };
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    var ready: std.Io.Event = .unset;
    jobs.setReady(.{ .ctx = &ready, .notify = Observer.notify });
    try startJob(&jobs, &case, "while [ ! -e finish-one ]; do sleep 0.05; done", "call-one");
    try case.tmp.dir.writeFile(std.testing.io, .{ .sub_path = "finish-one", .data = "" });
    const deadline = compat.time.nowMillis() + 10_000;
    while (!ready.isSet() and compat.time.nowMillis() < deadline) compat.time.sleepMs(5);
    try std.testing.expect(ready.isSet());
    jobs.setReady(null);
    ready.reset();
    try startJob(&jobs, &case, "true", "call-two");
    while (jobs.anyRunning() and compat.time.nowMillis() < deadline) compat.time.sleepMs(5);
    try std.testing.expect(!jobs.anyRunning());
    try std.testing.expect(!ready.isSet());
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const selected = try jobs.tasksForCall(arena.allocator(), "call-two");
    try std.testing.expectEqual(@as(usize, 1), selected.len);
    try std.testing.expectEqualStrings("call-two", selected[0].tool_call_id);
    try std.testing.expectEqual(@as(usize, 0), (try jobs.tasksForCall(arena.allocator(), "missing")).len);
    jobs.seal();
    try std.testing.expectError(error.SessionClosed, startJob(&jobs, &case, "sleep 30", "call-late"));
    try std.testing.expectEqual(@as(usize, 0), (try jobs.tasksForCall(arena.allocator(), "call-late")).len);
}

test "task output keeps raw offsets across invalid and split UTF-8" {
    const a = std.testing.allocator;
    const first = try Jobs.decodeOutput(a, "A\xc3", false, 32);
    defer a.free(first.text);
    try std.testing.expectEqualStrings("A", first.text);
    try std.testing.expectEqual(@as(usize, 1), first.consumed);
    const second = try Jobs.decodeOutput(a, "\xc3\xa9\xffZ", false, 32);
    defer a.free(second.text);
    try std.testing.expectEqualStrings("é�Z", second.text);
    try std.testing.expectEqual(@as(usize, 4), second.consumed);
    const last = try Jobs.decodeOutput(a, "\xe2\x82", true, 32);
    defer a.free(last.text);
    try std.testing.expectEqualStrings("�", last.text);
    try std.testing.expectEqual(@as(usize, 2), last.consumed);
    const bound = try Jobs.decodeOutput(a, "aé!", false, 2);
    defer a.free(bound.text);
    try std.testing.expectEqualStrings("a", bound.text);
    try std.testing.expectEqual(@as(usize, 1), bound.consumed);
}

test "task file reads hold split characters until complete or ended" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    try startJob(&jobs, &case, "printf '\\\\303'; sleep 30", "call");
    const job = jobs.list.items[0];
    try waitForOutput(&case, job.output, "\xc3");
    const a = std.testing.allocator;
    const first = try jobs.outputTask(a, job.id, 0);
    defer a.free(first.text);
    try std.testing.expectEqualStrings("", first.text);
    try std.testing.expectEqual(@as(u64, 0), first.next);
    try std.testing.expect(!first.ended);
    const file = try case.tmp.dir.createFile(common.defaultIo(), job.output, .{ .truncate = false });
    defer file.close(common.defaultIo());
    try file.writePositionalAll(common.defaultIo(), "\xa9\xff\xe2\x82", 1);
    const second = try jobs.outputTask(a, job.id, first.next);
    defer a.free(second.text);
    try std.testing.expectEqualStrings("é�", second.text);
    try std.testing.expectEqual(@as(u64, 3), second.next);
    try std.testing.expectError(error.InvalidOffset, jobs.outputTask(a, job.id, 1));
    try std.testing.expect(try jobs.cancelTask(job.id));
    const last = try jobs.outputTask(a, job.id, second.next);
    defer a.free(last.text);
    try std.testing.expectEqualStrings("�", last.text);
    try std.testing.expectEqual(@as(u64, 5), last.next);
    try std.testing.expect(last.ended);
}

test "finished task output ends only after the last bounded page" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    try startJob(&jobs, &case, "head -c 40000 /dev/zero | tr '\\\\000' x", "call");
    const job = jobs.list.items[0];
    const deadline = common.nowMs() + 5000;
    while (jobs.anyRunning() and common.nowMs() < deadline) compat.time.sleepMs(10);
    try std.testing.expect(!jobs.anyRunning());
    const first = try jobs.outputTask(std.testing.allocator, job.id, 0);
    defer std.testing.allocator.free(first.text);
    try std.testing.expectEqual(@as(usize, 32768), first.text.len);
    try std.testing.expectEqual(@as(u64, 32768), first.next);
    try std.testing.expect(!first.ended);
    const last = try jobs.outputTask(std.testing.allocator, job.id, first.next);
    defer std.testing.allocator.free(last.text);
    try std.testing.expectEqual(@as(usize, 7232), last.text.len);
    try std.testing.expectEqual(@as(u64, 40000), last.next);
    try std.testing.expect(last.ended);
    try std.testing.expectError(error.InvalidOffset, jobs.outputTask(std.testing.allocator, job.id, 40001));
}

const TaskOutputAllocationCase = struct {
    fn decode(allocator: std.mem.Allocator) !void {
        const result = try Jobs.decodeOutput(allocator, "a\xffé\xe2\x82", true, 32);
        defer allocator.free(result.text);
        try std.testing.expectEqualStrings("a�é�", result.text);
    }

    fn read(allocator: std.mem.Allocator, jobs: *Jobs, id: []const u8) !void {
        const result = try jobs.outputTask(allocator, id, 0);
        defer allocator.free(result.text);
        try std.testing.expectEqualStrings("ready", result.text);
    }
};

test "task output ownership survives every allocation failure" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, TaskOutputAllocationCase.decode, .{});
    var case = try Case.init();
    defer case.deinit();
    var jobs = Jobs.init(std.testing.allocator);
    defer jobs.deinit();
    try startJob(&jobs, &case, "printf ready; sleep 30", "call");
    const job = jobs.list.items[0];
    try waitForOutput(&case, job.output, "ready");
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, TaskOutputAllocationCase.read, .{ &jobs, job.id });
}
