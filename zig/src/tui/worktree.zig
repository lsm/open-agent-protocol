const std = @import("std");
const compat = @import("compat");
const json_writer = @import("json/writer");
const tools_common = @import("tools/common");
const process_runner = @import("tools/process_runner");

pub const git_timeout_ms: u64 = 15_000;

pub const GitResult = struct {
    ok: bool,
    stdout: []u8,
    stderr: []u8,

    pub fn deinit(self: *GitResult, allocator: std.mem.Allocator) void {
        allocator.free(self.stdout);
        allocator.free(self.stderr);
        self.* = undefined;
    }
};

pub const Runner = struct {
    ctx: *anyopaque,
    runFn: *const fn (ctx: *anyopaque, allocator: std.mem.Allocator, argv: []const []const u8, cwd: []const u8) anyerror!GitResult,

    pub fn run(self: Runner, allocator: std.mem.Allocator, argv: []const []const u8, cwd: []const u8) !GitResult {
        return self.runFn(self.ctx, allocator, argv, cwd);
    }
};

pub fn processRunner() Runner {
    return .{ .ctx = undefined, .runFn = processRun };
}

fn processRun(_: *anyopaque, allocator: std.mem.Allocator, argv: []const []const u8, cwd: []const u8) !GitResult {
    var dir = try tools_common.openWorkspace(cwd, false);
    defer dir.close(tools_common.defaultIo());
    const result = try process_runner.run(allocator, argv, .{ .dir = dir }, git_timeout_ms, null);
    return .{
        .ok = switch (result.term) {
            .exited => |code| code == 0,
            else => false,
        },
        .stdout = result.stdout,
        .stderr = result.stderr,
    };
}

pub const WorktreeInfo = struct {
    path: []u8,
    branch: []u8,
    repo_root: []u8,
    prefix: []u8,

    pub fn deinit(self: *WorktreeInfo, allocator: std.mem.Allocator) void {
        allocator.free(self.path);
        allocator.free(self.branch);
        allocator.free(self.repo_root);
        allocator.free(self.prefix);
        self.* = undefined;
    }

    pub fn workingDir(self: *const WorktreeInfo, allocator: std.mem.Allocator) ![]u8 {
        if (self.prefix.len == 0) return allocator.dupe(u8, self.path);
        const prefix = std.mem.trimEnd(u8, self.prefix, &.{std.fs.path.sep});
        if (prefix.len == 0) return allocator.dupe(u8, self.path);
        const joined = try std.fs.path.join(allocator, &.{ self.path, prefix });
        if (!pathExists(joined)) {
            allocator.free(joined);
            return allocator.dupe(u8, self.path);
        }
        return joined;
    }
};

pub const Created = struct {
    info: WorktreeInfo,
    uncommitted: usize,
};

pub const CreateOutcome = union(enum) {
    created: Created,
    not_a_repo,
    failed: []u8,

    pub fn deinit(self: *CreateOutcome, allocator: std.mem.Allocator) void {
        switch (self.*) {
            .created => |*created| created.info.deinit(allocator),
            .not_a_repo => {},
            .failed => |message| if (message.len > 0) allocator.free(message),
        }
        self.* = .not_a_repo;
    }
};

pub fn isUnderBase(path: []const u8, base: []const u8) bool {
    if (base.len == 0) return false;
    if (!std.mem.startsWith(u8, path, base)) return false;
    return path.len == base.len or path[base.len] == std.fs.path.sep;
}

pub fn pathExists(path: []const u8) bool {
    var dir = compat.fs.getCwd().openDir(io(), path, .{}) catch return false;
    dir.close(io());
    return true;
}

pub fn branchName(allocator: std.mem.Allocator, session_id: []const u8) ![]u8 {
    return std.fmt.allocPrint(allocator, "tui/{s}", .{session_id});
}

pub fn worktreePath(allocator: std.mem.Allocator, base: []const u8, repo_root: []const u8, session_id: []const u8) ![]u8 {
    const name = try std.fmt.allocPrint(allocator, "{s}-{s}", .{ std.fs.path.basename(repo_root), session_id });
    defer allocator.free(name);
    return std.fs.path.join(allocator, &.{ base, name });
}

pub fn create(allocator: std.mem.Allocator, runner: Runner, dir: []const u8, base: []const u8, session_id: []const u8) !CreateOutcome {
    const repo_root = try gitTrimmed(allocator, runner, dir, &.{ "rev-parse", "--show-toplevel" }) orelse return .not_a_repo;
    defer allocator.free(repo_root);
    const prefix = (try gitTrimmed(allocator, runner, dir, &.{ "rev-parse", "--show-prefix" })) orelse try allocator.dupe(u8, "");
    defer allocator.free(prefix);
    const uncommitted = try countUncommitted(allocator, runner, repo_root);

    const path = try worktreePath(allocator, base, repo_root, session_id);
    errdefer allocator.free(path);
    const branch = try branchName(allocator, session_id);
    errdefer allocator.free(branch);

    try compat.fs.createDir(compat.fs.getCwd(), base);
    var add = try runner.run(allocator, &.{ "git", "-C", repo_root, "worktree", "add", path, "-b", branch }, repo_root);
    defer add.deinit(allocator);
    if (!add.ok) {
        const message = try failureMessage(allocator, "git worktree add", &add);
        allocator.free(path);
        allocator.free(branch);
        return .{ .failed = message };
    }

    const owned_root = try allocator.dupe(u8, repo_root);
    errdefer allocator.free(owned_root);
    const owned_prefix = try allocator.dupe(u8, prefix);
    errdefer allocator.free(owned_prefix);
    return .{ .created = .{
        .info = .{
            .path = path,
            .branch = branch,
            .repo_root = owned_root,
            .prefix = owned_prefix,
        },
        .uncommitted = uncommitted,
    } };
}

pub fn reattach(allocator: std.mem.Allocator, runner: Runner, info: *const WorktreeInfo) !?[]u8 {
    var prune = try runner.run(allocator, &.{ "git", "-C", info.repo_root, "worktree", "prune" }, info.repo_root);
    prune.deinit(allocator);
    var add = try runner.run(allocator, &.{ "git", "-C", info.repo_root, "worktree", "add", info.path, info.branch }, info.repo_root);
    defer add.deinit(allocator);
    if (!add.ok) return try failureMessage(allocator, "git worktree add", &add);
    return null;
}

pub fn branchExists(allocator: std.mem.Allocator, runner: Runner, repo_root: []const u8, branch: []const u8) !bool {
    var result = try runner.run(allocator, &.{ "git", "-C", repo_root, "rev-parse", "--verify", "--quiet", branch }, repo_root);
    defer result.deinit(allocator);
    return result.ok;
}

pub fn remove(allocator: std.mem.Allocator, runner: Runner, info: *const WorktreeInfo) !?[]u8 {
    var rm = try runner.run(allocator, &.{ "git", "-C", info.repo_root, "worktree", "remove", info.path }, info.repo_root);
    defer rm.deinit(allocator);
    if (!rm.ok) return try failureMessage(allocator, "git worktree remove", &rm);
    var branch = try runner.run(allocator, &.{ "git", "-C", info.repo_root, "branch", "-d", info.branch }, info.repo_root);
    defer branch.deinit(allocator);
    if (!branch.ok) return try failureMessage(allocator, "git branch -d", &branch);
    return null;
}

pub fn removeForce(allocator: std.mem.Allocator, runner: Runner, info: *const WorktreeInfo) !?[]u8 {
    var rm = try runner.run(allocator, &.{ "git", "-C", info.repo_root, "worktree", "remove", "--force", info.path }, info.repo_root);
    defer rm.deinit(allocator);
    if (!rm.ok) return try failureMessage(allocator, "git worktree remove --force", &rm);
    var branch = try runner.run(allocator, &.{ "git", "-C", info.repo_root, "branch", "-D", info.branch }, info.repo_root);
    defer branch.deinit(allocator);
    if (!branch.ok) return try failureMessage(allocator, "git branch -D", &branch);
    return null;
}

fn removeOrphaned(allocator: std.mem.Allocator, runner: Runner, info: *const WorktreeInfo, force: bool) !?[]u8 {
    var prune = try runner.run(allocator, &.{ "git", "-C", info.repo_root, "worktree", "prune" }, info.repo_root);
    defer prune.deinit(allocator);
    if (!prune.ok) return try failureMessage(allocator, "git worktree prune", &prune);
    const flag = if (force) "-D" else "-d";
    var branch = try runner.run(allocator, &.{ "git", "-C", info.repo_root, "branch", flag, info.branch }, info.repo_root);
    defer branch.deinit(allocator);
    if (!branch.ok) return try failureMessage(allocator, if (force) "git branch -D" else "git branch -d", &branch);
    return null;
}

pub fn sidecarPath(allocator: std.mem.Allocator, sessions_base: []const u8, session_id: []const u8) ![]u8 {
    try validateSessionId(session_id);
    return std.fs.path.join(allocator, &.{ sessions_base, session_id, "worktree.json" });
}

pub fn writeSidecar(allocator: std.mem.Allocator, sessions_base: []const u8, session_id: []const u8, info: *const WorktreeInfo) !void {
    const dir_path = try std.fs.path.join(allocator, &.{ sessions_base, session_id });
    defer allocator.free(dir_path);
    try compat.fs.createDir(compat.fs.getCwd(), dir_path);
    const path = try sidecarPath(allocator, sessions_base, session_id);
    defer allocator.free(path);

    var buf: std.ArrayList(u8) = .empty;
    errdefer buf.deinit(allocator);
    var w = json_writer.JsonWriter.init(&buf, allocator);
    try w.beginObject();
    try w.writeStringField("path", info.path);
    try w.writeStringField("branch", info.branch);
    try w.writeStringField("repo_root", info.repo_root);
    try w.writeStringField("prefix", info.prefix);
    try w.endObject();
    const data = try buf.toOwnedSlice(allocator);
    defer allocator.free(data);
    try compat.fs.writeFile(compat.fs.getCwd(), path, data);
}

pub fn readSidecar(allocator: std.mem.Allocator, sessions_base: []const u8, session_id: []const u8) !?WorktreeInfo {
    const path = try sidecarPath(allocator, sessions_base, session_id);
    defer allocator.free(path);
    const data = compat.fs.readFileAlloc(allocator, compat.fs.getCwd(), path, 64 * 1024) catch |err| switch (err) {
        error.FileNotFound => return null,
        else => return err,
    };
    defer allocator.free(data);
    return try parseSidecar(allocator, data);
}

pub const ManagementKind = enum { reattach, remove, remove_force };
pub const DirtyReport = struct {
    summary: []u8,
    modified: usize,
    untracked: usize,

    pub fn deinit(self: *DirtyReport, allocator: std.mem.Allocator) void {
        allocator.free(self.summary);
        self.* = undefined;
    }
};

pub const ManagementOutcome = union(enum) {
    reattached: ?[]u8,
    removed: ?[]u8,
    missing_branch: void,
    dirty: DirtyReport,
    unverified: []u8,
    failed: []u8,

    pub fn deinit(self: *ManagementOutcome, allocator: std.mem.Allocator) void {
        switch (self.*) {
            .reattached, .removed => |message| if (message) |value| allocator.free(value),
            .failed, .unverified => |message| allocator.free(message),
            .dirty => |*report| report.deinit(allocator),
            .missing_branch => {},
        }
        self.* = undefined;
    }
};

pub const ManagementJob = struct {
    allocator: std.mem.Allocator,
    runner: Runner,
    info: WorktreeInfo,
    kind: ManagementKind,
    thread: std.Thread = undefined,
    joined: bool = false,
    mutex: std.atomic.Mutex = .unlocked,
    finished: bool = false,
    outcome: ?ManagementOutcome = null,

    pub fn start(allocator: std.mem.Allocator, runner: Runner, info: *const WorktreeInfo, kind: ManagementKind) !*ManagementJob {
        const self = try allocator.create(ManagementJob);
        errdefer allocator.destroy(self);
        self.* = .{ .allocator = allocator, .runner = runner, .info = try cloneInfo(allocator, info), .kind = kind };
        errdefer self.info.deinit(allocator);
        self.thread = try std.Thread.spawn(.{}, runManagement, .{self});
        return self;
    }

    pub fn poll(self: *ManagementJob) ?ManagementOutcome {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.mutex.unlock();
        if (!self.finished) return null;
        const outcome = self.outcome;
        self.outcome = null;
        return outcome;
    }

    pub fn wait(self: *ManagementJob) void {
        if (!self.joined) {
            self.thread.join();
            self.joined = true;
        }
    }

    pub fn deinit(self: *ManagementJob) void {
        self.wait();
        if (self.outcome) |*value| value.deinit(self.allocator);
        self.info.deinit(self.allocator);
        self.allocator.destroy(self);
    }
};

pub fn cloneInfo(allocator: std.mem.Allocator, info: *const WorktreeInfo) !WorktreeInfo {
    const path = try allocator.dupe(u8, info.path);
    errdefer allocator.free(path);
    const branch = try allocator.dupe(u8, info.branch);
    errdefer allocator.free(branch);
    const repo_root = try allocator.dupe(u8, info.repo_root);
    errdefer allocator.free(repo_root);
    const prefix = try allocator.dupe(u8, info.prefix);
    errdefer allocator.free(prefix);
    return .{ .path = path, .branch = branch, .repo_root = repo_root, .prefix = prefix };
}

fn runManagement(self: *ManagementJob) void {
    const outcome: ManagementOutcome = managementOperation(self.allocator, self.runner, &self.info, self.kind) catch |err| .{ .failed = self.allocator.dupe(u8, @errorName(err)) catch @constCast("") };
    while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
    self.outcome = outcome;
    self.finished = true;
    self.mutex.unlock();
}

fn managementOperation(allocator: std.mem.Allocator, runner: Runner, info: *const WorktreeInfo, kind: ManagementKind) !ManagementOutcome {
    if (kind == .reattach) {
        if (!try branchExists(allocator, runner, info.repo_root, info.branch)) return .{ .missing_branch = {} };
        return .{ .reattached = try reattach(allocator, runner, info) };
    }
    if (!pathExists(info.repo_root)) return .{ .removed = null };
    if (!pathExists(info.path)) return .{ .removed = try removeOrphaned(allocator, runner, info, kind == .remove_force) };
    if (kind == .remove_force) return .{ .removed = try removeForce(allocator, runner, info) };
    const probe = probeStatus(allocator, runner, info.path) catch |err| return .{ .failed = allocator.dupe(u8, @errorName(err)) catch @constCast("") };
    switch (probe) {
        .clean => {},
        .dirty => return .{ .dirty = probe.dirty },
        .failed => return .{ .unverified = probe.failed },
    }
    return .{ .removed = try remove(allocator, runner, info) };
}

pub const CreateJob = struct {
    allocator: std.mem.Allocator,
    runner: Runner,
    dir: []u8,
    base: []u8,
    session_id: []u8,
    thread: std.Thread = undefined,
    joined: bool = false,
    mutex: std.atomic.Mutex = .unlocked,
    finished: bool = false,
    outcome: ?CreateOutcome = null,

    pub fn start(allocator: std.mem.Allocator, runner: Runner, dir: []const u8, base: []const u8, session_id: []const u8) !*CreateJob {
        const self = try allocator.create(CreateJob);
        errdefer allocator.destroy(self);
        const owned_dir = try allocator.dupe(u8, dir);
        errdefer allocator.free(owned_dir);
        const owned_base = try allocator.dupe(u8, base);
        errdefer allocator.free(owned_base);
        const owned_id = try allocator.dupe(u8, session_id);
        errdefer allocator.free(owned_id);
        self.* = .{
            .allocator = allocator,
            .runner = runner,
            .dir = owned_dir,
            .base = owned_base,
            .session_id = owned_id,
        };
        self.thread = try std.Thread.spawn(.{}, runCreate, .{self});
        return self;
    }

    pub fn wait(self: *CreateJob) void {
        if (!self.joined) {
            self.thread.join();
            self.joined = true;
        }
    }

    pub fn poll(self: *CreateJob) ?CreateOutcome {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.mutex.unlock();
        if (!self.finished) return null;
        const outcome = self.outcome;
        self.outcome = null;
        return outcome;
    }

    pub fn deinit(self: *CreateJob) void {
        self.wait();
        if (self.outcome) |*outcome| outcome.deinit(self.allocator);
        self.allocator.free(self.dir);
        self.allocator.free(self.base);
        self.allocator.free(self.session_id);
        self.allocator.destroy(self);
    }
};

fn runCreate(self: *CreateJob) void {
    const outcome = create(self.allocator, self.runner, self.dir, self.base, self.session_id) catch |err| blk: {
        const message = self.allocator.dupe(u8, @errorName(err)) catch break :blk CreateOutcome{ .failed = @constCast("") };
        break :blk CreateOutcome{ .failed = message };
    };
    while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
    self.outcome = outcome;
    self.finished = true;
    self.mutex.unlock();
}

fn io() std.Io {
    return if (@import("builtin").is_test)
        std.testing.io
    else
        std.Io.Threaded.global_single_threaded.io();
}

fn gitTrimmed(allocator: std.mem.Allocator, runner: Runner, cwd: []const u8, args: []const []const u8) !?[]u8 {
    var argv: std.ArrayList([]const u8) = .empty;
    defer argv.deinit(allocator);
    try argv.append(allocator, "git");
    try argv.append(allocator, "-C");
    try argv.append(allocator, cwd);
    try argv.appendSlice(allocator, args);
    var result = try runner.run(allocator, argv.items, cwd);
    defer result.deinit(allocator);
    if (!result.ok) return null;
    return try allocator.dupe(u8, std.mem.trim(u8, result.stdout, " \t\r\n"));
}

pub const StatusProbe = union(enum) {
    clean: void,
    dirty: DirtyReport,
    failed: []u8,

    pub fn deinit(self: *StatusProbe, allocator: std.mem.Allocator) void {
        switch (self.*) {
            .clean => {},
            .dirty => |*report| report.deinit(allocator),
            .failed => |message| allocator.free(message),
        }
        self.* = undefined;
    }
};

pub fn countUncommitted(allocator: std.mem.Allocator, runner: Runner, repo_root: []const u8) !usize {
    var probe = try probeStatus(allocator, runner, repo_root);
    defer probe.deinit(allocator);
    return switch (probe) {
        .dirty => |report| report.modified + report.untracked,
        else => 0,
    };
}
pub fn probeStatus(allocator: std.mem.Allocator, runner: Runner, repo_root: []const u8) !StatusProbe {
    var result = try runner.run(allocator, &.{ "git", "-C", repo_root, "status", "--porcelain" }, repo_root);
    defer result.deinit(allocator);
    if (!result.ok) return .{ .failed = try failureMessage(allocator, "git status", &result) };

    var modified: usize = 0;
    var untracked: usize = 0;
    var sample: std.ArrayList([]const u8) = .empty;
    defer sample.deinit(allocator);
    var lines = std.mem.splitScalar(u8, result.stdout, '\n');
    while (lines.next()) |line| {
        const entry = std.mem.trim(u8, line, " \t\r");
        if (entry.len == 0) continue;
        if (std.mem.startsWith(u8, entry, "??")) {
            untracked += 1;
        } else {
            modified += 1;
        }
        if (sample.items.len < 5) try sample.append(allocator, entry);
    }

    const total = modified + untracked;
    if (total == 0) return .clean;

    const headline = try std.fmt.allocPrint(allocator, "{d} modified, {d} untracked", .{ modified, untracked });
    defer allocator.free(headline);
    var detail: std.ArrayList(u8) = .empty;
    errdefer detail.deinit(allocator);
    try detail.appendSlice(allocator, headline);
    for (sample.items) |entry| {
        try detail.appendSlice(allocator, "\n  ");
        try detail.appendSlice(allocator, entry);
    }
    if (total > sample.items.len) {
        const rest = try std.fmt.allocPrint(allocator, "\n  ...and {d} more", .{total - sample.items.len});
        defer allocator.free(rest);
        try detail.appendSlice(allocator, rest);
    }
    return .{ .dirty = .{ .summary = try detail.toOwnedSlice(allocator), .modified = modified, .untracked = untracked } };
}

fn failureMessage(allocator: std.mem.Allocator, op: []const u8, result: *const GitResult) ![]u8 {
    const detail = std.mem.trim(u8, if (result.stderr.len > 0) result.stderr else result.stdout, " \t\r\n");
    if (detail.len == 0) return std.fmt.allocPrint(allocator, "{s} failed", .{op});
    return std.fmt.allocPrint(allocator, "{s}: {s}", .{ op, detail });
}

fn validateSessionId(session_id: []const u8) !void {
    if (session_id.len == 0) return error.InvalidSessionId;
    for (session_id) |c| {
        if (c == '/' or c == '\\' or c == 0) return error.InvalidSessionId;
    }
    if (std.mem.indexOf(u8, session_id, "..") != null) return error.InvalidSessionId;
}

fn parseSidecar(allocator: std.mem.Allocator, data: []const u8) !?WorktreeInfo {
    const parsed = std.json.parseFromSlice(std.json.Value, allocator, data, .{}) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return null,
    };
    defer parsed.deinit();
    if (parsed.value != .object) return null;
    const obj = parsed.value.object;
    const path = jsonString(obj, "path") orelse return null;
    const branch = jsonString(obj, "branch") orelse return null;
    const repo_root = jsonString(obj, "repo_root") orelse return null;
    const prefix = jsonString(obj, "prefix") orelse "";
    if (path.len == 0 or branch.len == 0 or repo_root.len == 0) return null;
    const owned_path = try allocator.dupe(u8, path);
    errdefer allocator.free(owned_path);
    const owned_branch = try allocator.dupe(u8, branch);
    errdefer allocator.free(owned_branch);
    const owned_root = try allocator.dupe(u8, repo_root);
    errdefer allocator.free(owned_root);
    const owned_prefix = try allocator.dupe(u8, prefix);
    errdefer allocator.free(owned_prefix);
    return .{
        .path = owned_path,
        .branch = owned_branch,
        .repo_root = owned_root,
        .prefix = owned_prefix,
    };
}

fn jsonString(obj: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = obj.get(key) orelse return null;
    if (value != .string) return null;
    return value.string;
}

const FakeGit = struct {
    repo_root: []const u8 = "/repo",
    prefix: []const u8 = "",
    uncommitted_lines: usize = 0,
    is_repo: bool = true,
    fail_worktree_add: bool = false,
    fail_status: bool = false,
    untracked_status: bool = false,
    branch_exists: bool = true,
    calls: std.ArrayList([]const u8) = .empty,

    fn run(ctx: *anyopaque, allocator: std.mem.Allocator, argv: []const []const u8, cwd: []const u8) anyerror!GitResult {
        const self: *FakeGit = @ptrCast(@alignCast(ctx));
        _ = cwd;
        const joined = try joinArgs(allocator, argv);
        try self.calls.append(allocator, joined);
        const is_toplevel = std.mem.indexOf(u8, joined, "--show-toplevel") != null;
        const is_add = std.mem.indexOf(u8, joined, "worktree add") != null;
        const is_branch_lookup = std.mem.indexOf(u8, joined, "rev-parse --verify") != null;
        const is_status = std.mem.indexOf(u8, joined, "status") != null;
        const ok = if (is_toplevel) self.is_repo else if (is_add) !self.fail_worktree_add else if (is_status) !self.fail_status else if (is_branch_lookup) self.branch_exists else true;
        const stdout: []const u8 = if (std.mem.indexOf(u8, joined, "--show-toplevel") != null)
            self.repo_root
        else if (std.mem.indexOf(u8, joined, "--show-prefix") != null)
            self.prefix
        else if (is_status)
            if (self.untracked_status) "?? scratch.md\n" else if (self.uncommitted_lines > 0) " M changed.zig\n" else ""
        else
            "";
        const out_owned = try allocator.dupe(u8, stdout);
        errdefer allocator.free(out_owned);
        const err_owned = try allocator.dupe(u8, if (ok) "" else "fatal: refused");
        return .{ .ok = ok, .stdout = out_owned, .stderr = err_owned };
    }

    fn runner(self: *FakeGit) Runner {
        return .{ .ctx = self, .runFn = run };
    }

    fn deinit(self: *FakeGit, allocator: std.mem.Allocator) void {
        for (self.calls.items) |call| allocator.free(call);
        self.calls.deinit(allocator);
    }
};

fn joinArgs(allocator: std.mem.Allocator, argv: []const []const u8) ![]u8 {
    var buf: std.ArrayList(u8) = .empty;
    errdefer buf.deinit(allocator);
    for (argv, 0..) |arg, i| {
        if (i > 0) try buf.append(allocator, ' ');
        try buf.appendSlice(allocator, arg);
    }
    return buf.toOwnedSlice(allocator);
}

test "isUnderBase matches on a path component boundary" {
    try std.testing.expect(isUnderBase("/home/u/.oapx/worktrees/repo-x", "/home/u/.oapx/worktrees"));
    try std.testing.expect(isUnderBase("/home/u/.oapx/worktrees", "/home/u/.oapx/worktrees"));
    try std.testing.expect(!isUnderBase("/home/u/.oapx/worktrees2/repo", "/home/u/.oapx/worktrees"));
    try std.testing.expect(!isUnderBase("/repo", "/home/u/.oapx/worktrees"));
    try std.testing.expect(!isUnderBase("/repo", ""));
}

test "worktreePath joins the base with the repo name and session id" {
    const path = try worktreePath(std.testing.allocator, "/home/u/.oapx/worktrees", "/repo/project", "abc");
    defer std.testing.allocator.free(path);
    try std.testing.expectEqualStrings("/home/u/.oapx/worktrees/project-abc", path);
}

fn worktreeTestBase(allocator: std.mem.Allocator, tmp: *std.testing.TmpDir) ![]u8 {
    return std.fs.path.join(allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "worktrees" });
}

test "create reports a non-repo directory without adding a worktree" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try worktreeTestBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var git: FakeGit = .{ .is_repo = false };
    defer git.deinit(std.testing.allocator);
    var outcome = try create(std.testing.allocator, git.runner(), "/not/a/repo", base, "abc");
    defer outcome.deinit(std.testing.allocator);
    try std.testing.expect(outcome == .not_a_repo);
}

test "create adds a worktree on a new branch named after the session" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try worktreeTestBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var git: FakeGit = .{};
    defer git.deinit(std.testing.allocator);
    var outcome = try create(std.testing.allocator, git.runner(), "/repo", base, "abc");
    defer outcome.deinit(std.testing.allocator);
    const expected_path = try std.fs.path.join(std.testing.allocator, &.{ base, "repo-abc" });
    defer std.testing.allocator.free(expected_path);
    switch (outcome) {
        .created => |created| {
            try std.testing.expectEqualStrings(expected_path, created.info.path);
            try std.testing.expectEqualStrings("tui/abc", created.info.branch);
            try std.testing.expectEqualStrings("/repo", created.info.repo_root);
        },
        else => return error.TestUnexpectedResult,
    }
    try std.testing.expectEqual(@as(usize, 4), git.calls.items.len);
    const add_arg = try std.fmt.allocPrint(std.testing.allocator, "worktree add {s} -b tui/abc", .{expected_path});
    defer std.testing.allocator.free(add_arg);
    try std.testing.expect(std.mem.indexOf(u8, git.calls.items[3], add_arg) != null);
}

test "create counts uncommitted changes and keeps the launch prefix" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try worktreeTestBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var git: FakeGit = .{ .prefix = "zig/", .uncommitted_lines = 1 };
    defer git.deinit(std.testing.allocator);
    var outcome = try create(std.testing.allocator, git.runner(), "/repo/zig", base, "abc");
    defer outcome.deinit(std.testing.allocator);
    const expected_dir = try std.fs.path.join(std.testing.allocator, &.{ base, "repo-abc", "zig" });
    defer std.testing.allocator.free(expected_dir);
    try compat.fs.createDir(compat.fs.getCwd(), expected_dir);
    switch (outcome) {
        .created => |created| {
            try std.testing.expectEqual(@as(usize, 1), created.uncommitted);
            try std.testing.expectEqualStrings("zig/", created.info.prefix);
            const dir = try created.info.workingDir(std.testing.allocator);
            defer std.testing.allocator.free(dir);
            try std.testing.expectEqualStrings(expected_dir, dir);
        },
        else => return error.TestUnexpectedResult,
    }
}

test "workingDir falls back to the worktree path when the launch subdirectory is missing" {
    const info = WorktreeInfo{
        .path = @constCast("/tmp/nonexistent-worktree-for-test"),
        .branch = @constCast("tui/abc"),
        .repo_root = @constCast("/repo"),
        .prefix = @constCast("zig/"),
    };
    const dir = try info.workingDir(std.testing.allocator);
    defer std.testing.allocator.free(dir);
    try std.testing.expectEqualStrings("/tmp/nonexistent-worktree-for-test", dir);
}

test "create quotes git's refusal when the add fails" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try worktreeTestBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var git: FakeGit = .{ .fail_worktree_add = true };
    defer git.deinit(std.testing.allocator);
    var outcome = try create(std.testing.allocator, git.runner(), "/repo", base, "abc");
    defer outcome.deinit(std.testing.allocator);
    switch (outcome) {
        .failed => |message| try std.testing.expect(std.mem.indexOf(u8, message, "fatal: refused") != null),
        else => return error.TestUnexpectedResult,
    }
}

test "create job publishes its outcome once the thread finishes" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try worktreeTestBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var git: FakeGit = .{};
    defer git.deinit(std.testing.allocator);
    const job = try CreateJob.start(std.testing.allocator, git.runner(), "/repo", base, "abc");
    defer job.deinit();
    var maybe: ?CreateOutcome = null;
    while (maybe == null) {
        maybe = job.poll();
        if (maybe == null) std.atomic.spinLoopHint();
    }
    var outcome = maybe.?;
    defer outcome.deinit(std.testing.allocator);
    try std.testing.expect(outcome == .created);
    try std.testing.expect(job.poll() == null);
}

test "management job reports missing branch without blocking caller" {
    var git: FakeGit = .{ .branch_exists = false };
    defer git.deinit(std.testing.allocator);
    const info = WorktreeInfo{ .path = @constCast("/tmp/worktree"), .branch = @constCast("tui/test"), .repo_root = @constCast("/repo"), .prefix = "" };
    const job = try ManagementJob.start(std.testing.allocator, git.runner(), &info, .reattach);
    defer job.deinit();
    var maybe: ?ManagementOutcome = null;
    while (maybe == null) {
        maybe = job.poll();
        if (maybe == null) std.atomic.spinLoopHint();
    }
    var outcome = maybe.?;
    defer outcome.deinit(std.testing.allocator);
    try std.testing.expect(outcome == .missing_branch);
}

test "a dirty worktree reports what is uncommitted" {
    var git: FakeGit = .{ .uncommitted_lines = 1 };
    defer git.deinit(std.testing.allocator);
    var probe = try probeStatus(std.testing.allocator, git.runner(), "/repo");
    defer probe.deinit(std.testing.allocator);
    try std.testing.expect(probe == .dirty);
    const report = probe.dirty;
    try std.testing.expectEqual(@as(usize, 1), report.modified);
    try std.testing.expectEqual(@as(usize, 0), report.untracked);
    try std.testing.expect(std.mem.indexOf(u8, report.summary, "1 modified, 0 untracked") != null);
    try std.testing.expect(std.mem.indexOf(u8, report.summary, "changed.zig") != null);
}

test "an untracked-only worktree is counted as untracked, not modified" {
    var git: FakeGit = .{ .untracked_status = true };
    defer git.deinit(std.testing.allocator);
    var probe = try probeStatus(std.testing.allocator, git.runner(), "/repo");
    defer probe.deinit(std.testing.allocator);
    try std.testing.expect(probe == .dirty);
    const report = probe.dirty;
    try std.testing.expectEqual(@as(usize, 0), report.modified);
    try std.testing.expectEqual(@as(usize, 1), report.untracked);
    try std.testing.expect(std.mem.indexOf(u8, report.summary, "0 modified, 1 untracked") != null);
    try std.testing.expect(std.mem.indexOf(u8, report.summary, "scratch.md") != null);
}

test "a clean worktree probes clean" {
    var git: FakeGit = .{};
    defer git.deinit(std.testing.allocator);
    var probe = try probeStatus(std.testing.allocator, git.runner(), "/repo");
    defer probe.deinit(std.testing.allocator);
    try std.testing.expect(probe == .clean);
}

test "a failed git status is never reported as clean" {
    var git: FakeGit = .{ .fail_status = true };
    defer git.deinit(std.testing.allocator);
    var probe = try probeStatus(std.testing.allocator, git.runner(), "/repo");
    defer probe.deinit(std.testing.allocator);
    try std.testing.expect(probe == .failed);
    try std.testing.expect(std.mem.indexOf(u8, probe.failed, "git status") != null);
}

test "countUncommitted reports zero when git status fails" {
    var git: FakeGit = .{ .fail_status = true };
    defer git.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 0), try countUncommitted(std.testing.allocator, git.runner(), "/repo"));
    var probe = try probeStatus(std.testing.allocator, git.runner(), "/repo");
    defer probe.deinit(std.testing.allocator);
    try std.testing.expect(probe == .failed);
}

test "management job reports an unverifiable worktree separately from a dirty one" {
    var git: FakeGit = .{ .fail_status = true };
    defer git.deinit(std.testing.allocator);
    const info = WorktreeInfo{ .path = @constCast("/tmp"), .branch = @constCast("tui/test"), .repo_root = @constCast("/tmp"), .prefix = "" };
    const job = try ManagementJob.start(std.testing.allocator, git.runner(), &info, .remove);
    defer job.deinit();
    var maybe: ?ManagementOutcome = null;
    while (maybe == null) {
        maybe = job.poll();
        if (maybe == null) std.atomic.spinLoopHint();
    }
    var outcome = maybe.?;
    defer outcome.deinit(std.testing.allocator);
    try std.testing.expect(outcome == .unverified);
    var removed = false;
    for (git.calls.items) |call| {
        if (std.mem.indexOf(u8, call, "worktree remove") != null) removed = true;
    }
    try std.testing.expect(!removed);
}

test "management job refuses to delete a dirty worktree" {
    var git: FakeGit = .{ .uncommitted_lines = 1 };
    defer git.deinit(std.testing.allocator);
    const info = WorktreeInfo{ .path = @constCast("/tmp"), .branch = @constCast("tui/test"), .repo_root = @constCast("/tmp"), .prefix = "" };
    const job = try ManagementJob.start(std.testing.allocator, git.runner(), &info, .remove);
    defer job.deinit();
    var maybe: ?ManagementOutcome = null;
    while (maybe == null) {
        maybe = job.poll();
        if (maybe == null) std.atomic.spinLoopHint();
    }
    var outcome = maybe.?;
    defer outcome.deinit(std.testing.allocator);
    try std.testing.expect(outcome == .dirty);
}

test "management job force removes a dirty worktree" {
    var git: FakeGit = .{ .uncommitted_lines = 1 };
    defer git.deinit(std.testing.allocator);
    const info = WorktreeInfo{ .path = @constCast("/tmp"), .branch = @constCast("tui/test"), .repo_root = @constCast("/tmp"), .prefix = "" };
    const job = try ManagementJob.start(std.testing.allocator, git.runner(), &info, .remove_force);
    defer job.deinit();
    var maybe: ?ManagementOutcome = null;
    while (maybe == null) {
        maybe = job.poll();
        if (maybe == null) std.atomic.spinLoopHint();
    }
    var outcome = maybe.?;
    defer outcome.deinit(std.testing.allocator);
    try std.testing.expect(outcome == .removed);
    var saw_force = false;
    for (git.calls.items) |call| {
        if (std.mem.indexOf(u8, call, "worktree remove --force") != null) saw_force = true;
        if (std.mem.indexOf(u8, call, "status") != null) return error.ForceRemoveCheckedStatus;
    }
    try std.testing.expect(saw_force);
}

test "force removing an orphaned worktree deletes the branch with -D" {
    var git: FakeGit = .{};
    defer git.deinit(std.testing.allocator);
    const info = WorktreeInfo{ .path = @constCast("/nonexistent/worktree/xyz"), .branch = @constCast("tui/test"), .repo_root = @constCast("/tmp"), .prefix = "" };
    const job = try ManagementJob.start(std.testing.allocator, git.runner(), &info, .remove_force);
    defer job.deinit();
    var maybe: ?ManagementOutcome = null;
    while (maybe == null) {
        maybe = job.poll();
        if (maybe == null) std.atomic.spinLoopHint();
    }
    var outcome = maybe.?;
    defer outcome.deinit(std.testing.allocator);
    try std.testing.expect(outcome == .removed);
    var saw_force_branch = false;
    for (git.calls.items) |call| {
        if (std.mem.indexOf(u8, call, "branch -D") != null) saw_force_branch = true;
        if (std.mem.indexOf(u8, call, "branch -d") != null) return error.OrphanedRemoveUsedSafeDelete;
    }
    try std.testing.expect(saw_force_branch);
}

test "management job prunes an orphaned worktree whose directory is gone" {
    var git: FakeGit = .{};
    defer git.deinit(std.testing.allocator);
    const info = WorktreeInfo{ .path = @constCast("/nonexistent/worktree/xyz"), .branch = @constCast("tui/test"), .repo_root = @constCast("/tmp"), .prefix = "" };
    const job = try ManagementJob.start(std.testing.allocator, git.runner(), &info, .remove);
    defer job.deinit();
    var maybe: ?ManagementOutcome = null;
    while (maybe == null) {
        maybe = job.poll();
        if (maybe == null) std.atomic.spinLoopHint();
    }
    var outcome = maybe.?;
    defer outcome.deinit(std.testing.allocator);
    try std.testing.expect(outcome == .removed);
    try std.testing.expect(outcome.removed == null);
    var saw_prune = false;
    for (git.calls.items) |call| {
        if (std.mem.indexOf(u8, call, "worktree prune") != null) saw_prune = true;
    }
    try std.testing.expect(saw_prune);
}

test "management job treats a vanished repository root as removable" {
    var git: FakeGit = .{};
    defer git.deinit(std.testing.allocator);
    const info = WorktreeInfo{ .path = @constCast("/tmp"), .branch = @constCast("tui/test"), .repo_root = @constCast("/nonexistent/repo/root"), .prefix = "" };
    const job = try ManagementJob.start(std.testing.allocator, git.runner(), &info, .remove);
    defer job.deinit();
    var maybe: ?ManagementOutcome = null;
    while (maybe == null) {
        maybe = job.poll();
        if (maybe == null) std.atomic.spinLoopHint();
    }
    var outcome = maybe.?;
    defer outcome.deinit(std.testing.allocator);
    try std.testing.expect(outcome == .removed);
    try std.testing.expect(outcome.removed == null);
}

test "reattach prunes stale registration before adding" {
    var git: FakeGit = .{};
    defer git.deinit(std.testing.allocator);
    const info = WorktreeInfo{ .path = @constCast("/tmp/reattach-wt"), .branch = @constCast("tui/abc"), .repo_root = @constCast("/tmp"), .prefix = "" };
    const message = try reattach(std.testing.allocator, git.runner(), &info);
    defer if (message) |text| std.testing.allocator.free(text);
    try std.testing.expect(message == null);
    var saw_prune = false;
    var saw_add = false;
    for (git.calls.items) |call| {
        if (std.mem.indexOf(u8, call, "worktree prune") != null) saw_prune = true;
        if (std.mem.indexOf(u8, call, "worktree add") != null) saw_add = true;
    }
    try std.testing.expect(saw_prune and saw_add);
}

test "sidecar round-trips the worktree record" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    try compat.fs.createDir(compat.fs.getCwd(), base);

    const info: WorktreeInfo = .{
        .path = @constCast("/tmp/base/repo-abc"),
        .branch = @constCast("tui/abc"),
        .repo_root = @constCast("/repo"),
        .prefix = @constCast("zig/"),
    };
    try writeSidecar(std.testing.allocator, base, "abc", &info);

    var loaded = (try readSidecar(std.testing.allocator, base, "abc")).?;
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("/tmp/base/repo-abc", loaded.path);
    try std.testing.expectEqualStrings("tui/abc", loaded.branch);
    try std.testing.expectEqualStrings("/repo", loaded.repo_root);
    try std.testing.expectEqualStrings("zig/", loaded.prefix);
    try std.testing.expect((try readSidecar(std.testing.allocator, base, "missing")) == null);
}

fn parseSidecarProbe(allocator: std.mem.Allocator) error{OutOfMemory}!void {
    const data = "{\"path\":\"/tmp/wt\",\"branch\":\"tui/abc\",\"repo_root\":\"/repo\",\"prefix\":\"zig/\"}";
    const maybe_info = try parseSidecar(allocator, data);
    var info = maybe_info orelse return;
    info.deinit(allocator);
}

test "parseSidecar survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, parseSidecarProbe, .{});
}
