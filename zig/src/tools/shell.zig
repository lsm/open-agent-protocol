const std = @import("std");
const ai_types = @import("ai_types");
const agent = @import("agent");
const compat = @import("compat");
const common = @import("tools/common");
const process_runner = @import("tools/process_runner");

pub const schema_execute =
    \\{"type":"object","properties":{"description":{"type":"string","description":"Why this tool call is needed and what information or change it is intended to produce."},"workspace_root":{"type":"string"},"command":{"type":"string"},"timeout_ms":{"type":"integer","minimum":1},"run_in_background":{"type":"boolean","description":"Start the command as a background job that outlives this call and ends when the session closes. Returns task_id and output_path at once; read output_path with Read and check or stop the job with ShellJob."}},"required":["description","workspace_root","command"],"additionalProperties":false}
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
    list: std.ArrayList(*Job) = .empty,

    const Job = struct {
        id: []u8,
        output: []u8,
        workspace_root: []u8,
        leader: std.posix.pid_t,
        term: ?std.process.Child.Term = null,
        killed: bool = false,
        watcher: ?std.Thread = null,
    };

    pub const Started = struct { task_id: []const u8, output_path: []const u8 };

    pub const Report = struct {
        task_id: []const u8,
        status: []const u8,
        exit_code: ?u8 = null,
        signal: ?u32 = null,
        bytes: u64,
        output_path: []const u8,
    };

    pub fn init(allocator: std.mem.Allocator) Jobs {
        return .{ .allocator = allocator };
    }

    pub fn deinit(self: *Jobs) void {
        self.endAll();
        for (self.list.items) |job| {
            self.allocator.free(job.id);
            self.allocator.free(job.output);
            self.allocator.free(job.workspace_root);
            self.allocator.destroy(job);
        }
        self.list.deinit(self.allocator);
        self.* = undefined;
    }

    fn lock(self: *Jobs) void {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
    }

    pub fn start(self: *Jobs, scratch: std.mem.Allocator, workspace_root: []const u8, command: []const u8) !Started {
        var dir = try common.openWorkspace(workspace_root, false);
        defer dir.close(common.defaultIo());
        try dir.createDirPath(common.defaultIo(), jobs_dir);
        self.lock();
        defer self.mutex.unlock();
        try self.list.ensureUnusedCapacity(self.allocator, 1);
        self.next += 1;
        var entropy: [6]u8 = undefined;
        compat.random.fillSecureBytes(&entropy);
        const id = try std.fmt.allocPrint(self.allocator, "job-{d}-{x}", .{ self.next, entropy });
        errdefer self.allocator.free(id);
        const output = try std.fmt.allocPrint(self.allocator, jobs_dir ++ "/{s}.log", .{id});
        errdefer self.allocator.free(output);
        const root = try self.allocator.dupe(u8, workspace_root);
        errdefer self.allocator.free(root);
        const job = try self.allocator.create(Job);
        errdefer self.allocator.destroy(job);
        const argv = [_][]const u8{ "/bin/sh", "-c", "exec >\"$0\" 2>&1 </dev/null; exec /bin/sh -c \"$1\"", output, command };
        const leader = try process_runner.spawnGroup(scratch, &argv, .{ .dir = dir });
        job.* = .{ .id = id, .output = output, .workspace_root = root, .leader = leader };
        job.watcher = std.Thread.spawn(.{}, watch, .{ self, job }) catch |err| {
            process_runner.endGroup(leader);
            _ = process_runner.waitLeader(leader) catch {};
            return err;
        };
        self.list.appendAssumeCapacity(job);
        return .{ .task_id = id, .output_path = output };
    }

    fn watch(self: *Jobs, job: *Job) void {
        const term = process_runner.waitLeader(job.leader) catch std.process.Child.Term{ .unknown = 0 };
        self.lock();
        job.term = term;
        self.mutex.unlock();
        process_runner.endGroup(job.leader);
    }

    pub fn report(self: *Jobs, output_path: []const u8, kill: bool) !Report {
        self.lock();
        const job = self.find(output_path) orelse {
            self.mutex.unlock();
            return error.UnknownJob;
        };
        if (kill and job.term == null) job.killed = true;
        self.mutex.unlock();
        if (kill) self.end(job);
        self.lock();
        defer self.mutex.unlock();
        return .{
            .task_id = job.id,
            .status = if (job.term == null) "running" else if (job.killed) "killed" else switch (job.term.?) {
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
            .bytes = outputBytes(job.*),
            .output_path = job.output,
        };
    }

    pub fn endAll(self: *Jobs) void {
        self.lock();
        const jobs = self.list.items;
        self.mutex.unlock();
        for (jobs) |job| self.end(job);
    }

    fn find(self: *Jobs, output_path: []const u8) ?*Job {
        for (self.list.items) |job| if (std.mem.eql(u8, job.output, output_path)) return job;
        return null;
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

    fn outputBytes(job: Job) u64 {
        var dir = common.openWorkspace(job.workspace_root, false) catch return 0;
        defer dir.close(common.defaultIo());
        const file = dir.openFile(common.defaultIo(), job.output, .{}) catch return 0;
        defer file.close(common.defaultIo());
        const st = file.stat(common.defaultIo()) catch return 0;
        return st.size;
    }
};

const end_directory_script =
    \\__oap_cmd=$1
    \\__oap_path=$2
    \\set --
    \\eval "$__oap_cmd"
    \\__oap_rc=$?
    \\set +x
    \\pwd > "$__oap_path"
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
    if (!common.optionalBool(obj, "run_in_background", false)) return runForeground(tool_call_id, args_json, cancel_token, on_update_ctx, on_update, allocator);
    if (common.isCancelled(cancel_token)) return error.Cancelled;
    const jobs: *Jobs = @ptrCast(@alignCast(ctx orelse return error.BackgroundJobsUnavailable));
    const started = try jobs.start(allocator, try common.requiredString(obj, "workspace_root"), try common.requiredString(obj, "command"));
    const details = try common.jsonString(allocator, .{ .ok = true, .task_id = started.task_id, .output_path = started.output_path });
    errdefer allocator.free(details);
    const text = try std.fmt.allocPrint(allocator, "started background job {s}; its stdout and stderr go to {s}. Read that file for output, and pass its path to ShellJob to check or stop the job. It ends when the session closes.", .{ started.task_id, started.output_path });
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

fn runForeground(
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
    const timeout_ms = @min(common.optionalU64(obj, "timeout_ms", 30_000), @as(u64, std.math.maxInt(i64)));

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

    const result = process_runner.run(allocator, argv, .{ .dir = dir }, timeout_ms, cancel_token) catch |err| {
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
