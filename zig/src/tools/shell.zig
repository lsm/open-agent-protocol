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
    \\__oap_nonce=$2
    \\set --
    \\eval "$__oap_cmd"
    \\__oap_rc=$?
    \\printf '\n%s%s\n' "$__oap_nonce" "$(pwd)"
    \\exit $__oap_rc
;

const EndDirectory = struct {
    stdout: []const u8,
    directory: ?[]const u8,
};

fn splitEndDirectory(stdout: []const u8, nonce: [16]u8) EndDirectory {
    var needle_buffer: [17]u8 = undefined;
    needle_buffer[0] = '\n';
    @memcpy(needle_buffer[1..], &nonce);
    const needle = needle_buffer[0..];
    const index = std.mem.lastIndexOf(u8, stdout, needle) orelse
        return .{ .stdout = stdout, .directory = null };
    const after = stdout[index + needle.len ..];
    const line_end = std.mem.indexOfScalar(u8, after, '\n') orelse after.len;
    if (line_end == 0) return .{ .stdout = stdout[0..index], .directory = null };
    return .{ .stdout = stdout[0..index], .directory = after[0..line_end] };
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
    const argv = if (@import("builtin").os.tag == .windows)
        [_][]const u8{ "cmd.exe", "/C", command }
    else
        [_][]const u8{ "/bin/sh", "-c", end_directory_script, "sh", command, &nonce };
    const result = process_runner.run(allocator, &argv, .{ .dir = dir }, timeout_ms, cancel_token) catch |err| {
        if (err == error.Cancelled) return err;
        const duration_ms = common.durationMs(start_ms);
        const details = try common.jsonString(allocator, .{
            .ok = false,
            .err = @errorName(err),
            .duration_ms = duration_ms,
            .stdout_bytes = 0,
            .stderr_bytes = 0,
            .raw_bytes = 0,
        });
        errdefer allocator.free(details);
        const text = try std.fmt.allocPrint(allocator, "shell command failed: {s}", .{@errorName(err)});
        return common.makeTextResultOwned(allocator, text, details);
    };
    defer allocator.free(result.stdout);
    defer allocator.free(result.stderr);

    const captured = splitEndDirectory(result.stdout, nonce);
    const start_directory = try std.Io.Dir.path.resolve(allocator, &.{workspace_root});
    defer allocator.free(start_directory);
    const end_directory = reportDirectory(result.term, captured.directory, start_directory);

    const exit_code: ?u8 = switch (result.term) {
        .exited => |code| code,
        else => null,
    };
    const signal: ?u32 = switch (result.term) {
        .signal => |sig| @intFromEnum(sig),
        else => null,
    };
    const duration_ms = common.durationMs(start_ms);
    const raw_bytes = captured.stdout.len + result.stderr.len;
    const details = try common.jsonString(allocator, .{
        .ok = exit_code == 0,
        .exit_code = exit_code,
        .signal = signal,
        .duration_ms = duration_ms,
        .stdout_bytes = captured.stdout.len,
        .stderr_bytes = result.stderr.len,
        .raw_bytes = raw_bytes,
        .working_directory = end_directory,
    });
    defer allocator.free(details);

    const text = try std.fmt.allocPrint(allocator,
        \\stdout:
        \\{s}
        \\stderr:
        \\{s}
    , .{ captured.stdout, result.stderr });
    defer allocator.free(text);
    const made = try common.makeTextResultWithArtifact(allocator, .{ .tool_name = "shell_execute", .call_id = tool_call_id, .text = text, .details_json = details });
    defer if (made.artifact_path) |path| allocator.free(path);
    var with_directory = made.result;
    with_directory.working_directory = ai_types.OwnedSlice(u8).initOwned(try allocator.dupe(u8, end_directory));
    return with_directory;
}

test "shell execute reports the directory the command ended in" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const cwd = try std.process.currentPathAlloc(common.defaultIo(), std.testing.allocator);
    defer std.testing.allocator.free(cwd);
    const args = try std.fmt.allocPrint(std.testing.allocator, "{{\"workspace_root\":\"{s}\",\"command\":\"mkdir -p oap-cd-test && cd oap-cd-test && pwd\"}}", .{cwd});
    defer std.testing.allocator.free(args);
    var dir = try common.openWorkspace(cwd, false);
    defer dir.close(common.defaultIo());
    dir.deleteTree(common.defaultIo(), "oap-cd-test") catch {};
    var result = try execute("call-cd", args, null, null, null, std.testing.allocator);
    defer result.deinit(std.testing.allocator);
    const expected = try std.Io.Dir.path.resolve(std.testing.allocator, &.{ cwd, "oap-cd-test" });
    defer std.testing.allocator.free(expected);
    try std.testing.expectEqualStrings(expected, result.workingDirectory().?);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, expected) != null);
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, "oap-cwd-") == null);
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

test "a command that prints the nonce does not decide the reported directory" {
    const nonce = [_]u8{ 'a' } ** 16;
    const expected_stdout = try std.fmt.allocPrint(std.testing.allocator, "noise\n{s}/elsewhere\ntail", .{&nonce});
    defer std.testing.allocator.free(expected_stdout);
    const stdout = try std.fmt.allocPrint(std.testing.allocator, "noise\n{s}/elsewhere\ntail\n{s}/real\n", .{ &nonce, &nonce });
    defer std.testing.allocator.free(stdout);
    const split = splitEndDirectory(stdout, nonce);
    try std.testing.expectEqualStrings(expected_stdout, split.stdout);
    try std.testing.expectEqualStrings("/real", split.directory.?);
}

test "the reported directory stops at the first newline after the marker" {
    const nonce = [_]u8{ 'b' } ** 16;
    const stdout = try std.fmt.allocPrint(std.testing.allocator, "out\n{s}/dir\nwritten by a background process\n", .{&nonce});
    defer std.testing.allocator.free(stdout);
    const split = splitEndDirectory(stdout, nonce);
    try std.testing.expectEqualStrings("out", split.stdout);
    try std.testing.expectEqualStrings("/dir", split.directory.?);
}

test "a missing or empty marker reports no directory" {
    const nonce = [_]u8{ 'c' } ** 16;
    try std.testing.expect(splitEndDirectory("just output\n", nonce).directory == null);
    const empty = try std.fmt.allocPrint(std.testing.allocator, "out\n{s}\n", .{&nonce});
    defer std.testing.allocator.free(empty);
    const split = splitEndDirectory(empty, nonce);
    try std.testing.expect(split.directory == null);
    try std.testing.expectEqualStrings("out", split.stdout);
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
    try std.testing.expect(std.mem.indexOf(u8, result.content.slice()[0].text.text, "oap-cwd-") == null);
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
