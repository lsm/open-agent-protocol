const std = @import("std");
const ai_types = @import("ai_types");
const agent = @import("agent");
const common = @import("tools/common");
const process_runner = @import("tools/process_runner");

pub const schema_execute =
    \\{"type":"object","properties":{"description":{"type":"string","description":"Why this tool call is needed and what information or change it is intended to produce."},"workspace_root":{"type":"string"},"command":{"type":"string"},"timeout_ms":{"type":"integer","minimum":1}},"required":["description","workspace_root","command"],"additionalProperties":false}
;

const end_directory_script =
    \\__oap_cmd=$1
    \\set --
    \\eval "$__oap_cmd"
    \\__oap_rc=$?
    \\printf '\nOAPNONCE%s\n' "$(pwd)"
    \\exit $__oap_rc
;

fn buildEndDirectoryScript(allocator: std.mem.Allocator, nonce: [16]u8) ![]u8 {
    return std.mem.replaceOwned(u8, allocator, end_directory_script, "OAPNONCE", &nonce);
}

const EndDirectory = struct {
    before: []const u8,
    found: bool,
    directory: ?[]const u8,
    after: []const u8,
};

fn splitEndDirectory(stdout: []const u8, nonce: [16]u8) EndDirectory {
    var needle_buffer: [17]u8 = undefined;
    needle_buffer[0] = '\n';
    @memcpy(needle_buffer[1..], &nonce);
    const needle = needle_buffer[0..];
    const index = std.mem.lastIndexOf(u8, stdout, needle) orelse
        return .{ .before = stdout, .found = false, .directory = null, .after = &.{} };
    const after = stdout[index + needle.len ..];
    const newline = std.mem.indexOfScalar(u8, after, '\n');
    const line_end = newline orelse after.len;
    if (line_end == 0) return .{ .before = stdout[0..index], .found = true, .directory = null, .after = &.{} };
    const tail = if (newline) |at| after[at + 1 ..] else after[after.len..];
    return .{ .before = stdout[0..index], .found = true, .directory = after[0..line_end], .after = tail };
}

fn restoredNewline(split: EndDirectory) []const u8 {
    if (!split.found) return "";
    if (split.before.len == 0) return "";
    if (split.before[split.before.len - 1] == '\n') return "";
    return "\n";
}

fn reportDirectory(term: std.process.Child.Term, parsed: ?[]const u8, start: []const u8) []const u8 {
    if (term != .exited) return start;
    return parsed orelse start;
}

pub const execute_tool = agent.AgentTool{
    .label = "Shell Execute",
    .name = "shell_execute",
    .description = "Run a shell command in the workspace and return stdout, stderr, exit status, duration, and byte counts. Large output is stored as a retrievable artifact.",
    .short_description = "Run shell command; large output becomes artifact.",
    .parameters_schema_json = schema_execute,
    .execute = execute,
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

    const nonce = common.hash16(tool_call_id);
    const start_directory = try std.Io.Dir.path.resolve(allocator, &.{workspace_root});
    defer allocator.free(start_directory);
    const script = if (@import("builtin").os.tag == .windows)
        try allocator.dupe(u8, "")
    else
        try buildEndDirectoryScript(allocator, nonce);
    defer allocator.free(script);
    const argv = if (@import("builtin").os.tag == .windows)
        [_][]const u8{ "cmd.exe", "/C", command }
    else
        [_][]const u8{ "/bin/sh", "-c", script, "sh", command };
    const result = process_runner.run(allocator, &argv, .{ .dir = dir }, timeout_ms, cancel_token) catch |err| {
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
        });
        const text = try std.fmt.allocPrint(allocator, "shell command failed: {s}", .{@errorName(err)});
        var owned_here = true;
        defer if (owned_here) {
            allocator.free(text);
            allocator.free(details);
        };
        var failed = try common.makeTextResultOwned(allocator, text, details);
        errdefer failed.deinit(allocator);
        failed.working_directory = ai_types.OwnedSlice(u8).initOwned(owned_directory);
        owned_here = false;
        return failed;
    };
    defer allocator.free(result.stdout);
    defer allocator.free(result.stderr);

    const captured = splitEndDirectory(result.stdout, nonce);
    const end_directory = reportDirectory(result.term, captured.directory, start_directory);
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
    const restored = restoredNewline(captured);
    const stdout_bytes = captured.before.len + restored.len + captured.after.len;
    const raw_bytes = stdout_bytes + result.stderr.len;
    const details = try common.jsonString(allocator, .{
        .ok = exit_code == 0,
        .exit_code = exit_code,
        .signal = signal,
        .duration_ms = duration_ms,
        .stdout_bytes = stdout_bytes,
        .stderr_bytes = result.stderr.len,
        .raw_bytes = raw_bytes,
        .working_directory = end_directory,
    });
    defer allocator.free(details);

    const text = try std.fmt.allocPrint(allocator,
        \\stdout:
        \\{s}{s}{s}
        \\stderr:
        \\{s}
    , .{ captured.before, restored, captured.after, result.stderr });
    defer allocator.free(text);
    var made = try common.makeTextResultWithArtifact(allocator, .{ .tool_name = "shell_execute", .call_id = tool_call_id, .text = text, .details_json = details });
    defer if (made.artifact_path) |path| allocator.free(path);
    made.result.working_directory = ai_types.OwnedSlice(u8).initOwned(owned_directory);
    return made.result;
}

test "shell execute reports the directory the command ended in" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const root = try std.Io.Dir.path.join(std.testing.allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..] });
    defer std.testing.allocator.free(root);
    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"mkdir -p sub && cd sub && pwd\"}}", .{root});
    defer std.testing.allocator.free(args);
    var result = try execute("call-cd", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    const expected = try std.Io.Dir.path.resolve(std.testing.allocator, &.{ root, "sub" });
    defer std.testing.allocator.free(expected);
    try std.testing.expectEqualStrings(expected, result.workingDirectory().?);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, expected) != null);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, &common.hash16("call-cd")) == null);
}

test "shell execute reports the start directory when the command does not move" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const expected = try std.Io.Dir.path.resolve(std.testing.allocator, &.{cwd});
    defer std.testing.allocator.free(expected);
    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"true\"}}", .{cwd});
    defer std.testing.allocator.free(args);
    var result = try execute("call-still", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings(expected, result.workingDirectory().?);
}

test "shell execute reports the start directory when the command replaces the shell" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const expected = try std.Io.Dir.path.resolve(std.testing.allocator, &.{cwd});
    defer std.testing.allocator.free(expected);
    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"exec pwd\"}}", .{cwd});
    defer std.testing.allocator.free(args);
    var result = try execute("call-exec", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings(expected, result.workingDirectory().?);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, expected) != null);
}

fn reassembled(split: EndDirectory) ![]u8 {
    return std.fmt.allocPrint(std.testing.allocator, "{s}{s}{s}", .{ split.before, restoredNewline(split), split.after });
}

test "a command that prints the nonce does not decide the reported directory" {
    const nonce = [_]u8{ 'a' } ** 16;
    const stdout = try std.fmt.allocPrint(std.testing.allocator, "noise\n{s}/elsewhere\ntail\n{s}/real\n", .{ &nonce, &nonce });
    defer std.testing.allocator.free(stdout);
    const split = splitEndDirectory(stdout, nonce);
    try std.testing.expectEqualStrings("/real", split.directory.?);
    const joined = try reassembled(split);
    defer std.testing.allocator.free(joined);
    const expected = try std.fmt.allocPrint(std.testing.allocator, "noise\n{s}/elsewhere\ntail\n", .{&nonce});
    defer std.testing.allocator.free(expected);
    try std.testing.expectEqualStrings(expected, joined);
}

test "output written after the marker line is kept, not dropped" {
    const nonce = [_]u8{ 'b' } ** 16;
    const stdout = try std.fmt.allocPrint(std.testing.allocator, "out\n{s}/dir\nwritten by a background process\n", .{&nonce});
    defer std.testing.allocator.free(stdout);
    const split = splitEndDirectory(stdout, nonce);
    try std.testing.expectEqualStrings("/dir", split.directory.?);
    try std.testing.expectEqualStrings("written by a background process\n", split.after);
    const joined = try reassembled(split);
    defer std.testing.allocator.free(joined);
    try std.testing.expectEqualStrings("out\nwritten by a background process\n", joined);
}

test "an exit trap's output survives, because it runs after the marker" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"trap 'echo from-the-trap' EXIT\"}}", .{cwd});
    defer std.testing.allocator.free(args);
    const expected = try std.Io.Dir.path.resolve(std.testing.allocator, &.{cwd});
    defer std.testing.allocator.free(expected);
    var result = try execute("call-trap", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings(expected, result.workingDirectory().?);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, "from-the-trap") != null);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, &common.hash16("call-trap")) == null);
}

test "a command whose output hits the cap reports the start directory and the failure" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const expected = try std.Io.Dir.path.resolve(std.testing.allocator, &.{cwd});
    defer std.testing.allocator.free(expected);
    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"head -c 20000000 /dev/zero | tr '\\\\0' x\",\"timeout_ms\":60000}}", .{cwd});
    defer std.testing.allocator.free(args);
    var result = try execute("call-cap", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings(expected, result.workingDirectory().?);
    try std.testing.expect(std.mem.indexOf(u8, result.getDetailsJson().?, "StreamTooLong") != null);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, "shell command failed") != null);
}

test "the reported directory stops at the first newline after the marker" {
    const nonce = [_]u8{ 'd' } ** 16;
    const stdout = try std.fmt.allocPrint(std.testing.allocator, "out\n{s}/dir\n", .{&nonce});
    defer std.testing.allocator.free(stdout);
    const split = splitEndDirectory(stdout, nonce);
    try std.testing.expectEqualStrings("/dir", split.directory.?);
    const joined = try reassembled(split);
    defer std.testing.allocator.free(joined);
    try std.testing.expectEqualStrings("out\n", joined);
}

test "a missing or empty marker reports no directory" {
    const nonce = [_]u8{ 'c' } ** 16;
    const missing = splitEndDirectory("just output\n", nonce);
    try std.testing.expect(missing.directory == null);
    const missing_joined = try reassembled(missing);
    defer std.testing.allocator.free(missing_joined);
    try std.testing.expectEqualStrings("just output\n", missing_joined);
    const empty = try std.fmt.allocPrint(std.testing.allocator, "out\n{s}\n", .{&nonce});
    defer std.testing.allocator.free(empty);
    const split = splitEndDirectory(empty, nonce);
    try std.testing.expect(split.directory == null);
    const joined = try reassembled(split);
    defer std.testing.allocator.free(joined);
    try std.testing.expectEqualStrings("out\n", joined);
}

test "the marker is gone and the reported byte count matches the command's own output" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"echo ok\"}}", .{cwd});
    defer std.testing.allocator.free(args);
    var result = try execute("call-nl", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    const text = result.content.slice()[0].text.text;
    try std.testing.expect(std.mem.indexOf(u8, text, &common.hash16("call-nl")) == null);
    try std.testing.expect(std.mem.indexOf(u8, text, "ok") != null);
    const counted = try std.fmt.allocPrint(std.testing.allocator, "\"stdout_bytes\":3", .{});
    defer std.testing.allocator.free(counted);
    try std.testing.expect(std.mem.indexOf(u8, result.getDetailsJson().?, counted) != null);
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
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, &common.hash16("call-args")) == null);
}

test "a command that reassigns the positional parameters still reports its directory" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"set -- clobbered; pwd\"}}", .{cwd});
    defer std.testing.allocator.free(args);
    const expected = try std.Io.Dir.path.resolve(std.testing.allocator, &.{cwd});
    defer std.testing.allocator.free(expected);
    var result = try execute("call-clobber", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings(expected, result.workingDirectory().?);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, expected) != null);
}

test "a marker is only read when the wrapper exited normally" {
    const start = "/start";
    try std.testing.expectEqualStrings("/parsed", reportDirectory(.{ .exited = 0 }, "/parsed", start));
    try std.testing.expectEqualStrings("/parsed", reportDirectory(.{ .exited = 7 }, "/parsed", start));
    try std.testing.expectEqualStrings(start, reportDirectory(.{ .exited = 0 }, null, start));
    try std.testing.expectEqualStrings(start, reportDirectory(.{ .signal = .KILL }, "/parsed", start));
    try std.testing.expectEqualStrings(start, reportDirectory(.{ .stopped = .KILL }, "/parsed", start));
    try std.testing.expectEqualStrings(start, reportDirectory(.{ .unknown = 0 }, "/parsed", start));
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

test "shell execute stores only output over the limit as an artifact, whatever compact_output says" {
    var artifact_root = common.TestArtifactRoot.init();
    defer artifact_root.deinit();
    const cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const large_args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"python3 - <<'PY'\\nimport sys\\nsys.stdout.write('x' * 11000)\\nPY\"}}", .{cwd});
    defer std.testing.allocator.free(large_args);
    var large = try execute("call-large", large_args, null, null, null, std.testing.allocator);
    defer large.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.indexOf(u8, large.content.slice()[0].text.text, "output stored as artifact") != null);
    try std.testing.expect(std.mem.indexOf(u8, large.content.slice()[0].text.text, "mode \"preview\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, large.content.slice()[0].text.text, "full_for_context") != null);
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
