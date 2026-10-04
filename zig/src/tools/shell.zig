const std = @import("std");
const ai_types = @import("ai_types");
const agent = @import("agent");
const compat = @import("compat");
const common = @import("tools/common");
const process_runner = @import("tools/process_runner");

pub const schema_execute =
    \\{"type":"object","properties":{"description":{"type":"string","description":"Why this tool call is needed and what information or change it is intended to produce."},"workspace_root":{"type":"string"},"command":{"type":"string"},"timeout_ms":{"type":"integer","minimum":1}},"required":["description","workspace_root","command"],"additionalProperties":false}
;

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

pub const execute_tool = agent.AgentTool{
    .label = "Shell",
    .name = "Shell",
    .description = "Run a shell command in the workspace and return stdout, stderr, exit status, duration, and byte counts. Large output is stored as a retrievable artifact.",
    .short_description = "Run shell command; past 20 KiB, shows head and tail and saves the rest.",
    .parameters_schema_json = schema_execute,
    .execute = execute, .operation = .shell,
};

pub fn execute(
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
        .signal => |sig| @intFromEnum(sig),
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
