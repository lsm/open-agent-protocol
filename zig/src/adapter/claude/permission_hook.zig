const std = @import("std");
const compat = @import("compat");

pub const answer_limit: usize = 64 * 1024;

pub fn decisionOutput(arena: std.mem.Allocator, answer: []const u8) !?[]const u8 {
    const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, answer, .{}) catch return null;
    if (parsed != .object) return null;
    const behavior = parsed.object.get("behavior") orelse return null;
    if (behavior != .string) return null;
    const allow = std.mem.eql(u8, behavior.string, "allow");
    if (!allow and !std.mem.eql(u8, behavior.string, "deny")) return null;

    var decision: std.json.ObjectMap = .empty;
    try decision.put(arena, "behavior", .{ .string = behavior.string });
    if (!allow) {
        if (parsed.object.get("message")) |message| {
            if (message != .string) return null;
            try decision.put(arena, "message", message);
        }
    }
    var specific: std.json.ObjectMap = .empty;
    try specific.put(arena, "hookEventName", .{ .string = "PermissionRequest" });
    try specific.put(arena, "decision", .{ .object = decision });
    var output: std.json.ObjectMap = .empty;
    try output.put(arena, "hookSpecificOutput", .{ .object = specific });
    return try std.json.Stringify.valueAlloc(arena, std.json.Value{ .object = output }, .{});
}

pub const answer_timeout_ms: u64 = 50_000;

pub fn ask(arena: std.mem.Allocator, endpoint: []const u8, request: []const u8) ?[]const u8 {
    return askWithin(arena, endpoint, request, answer_timeout_ms);
}

pub fn askWithin(arena: std.mem.Allocator, endpoint: []const u8, request: []const u8, timeout_ms: u64) ?[]const u8 {
    const headers = [_]std.http.Header{.{ .name = "content-type", .value = "application/json" }};
    const answered = compat.http.fetch(arena, endpoint, .{
        .method = .POST,
        .body = request,
        .extra_headers = &headers,
        .max_response_bytes = answer_limit,
        .timeout_ms = timeout_ms,
    }) catch return null;
    if (answered.status != 200) return null;
    return decisionOutput(arena, answered.body) catch null;
}

test "an allow answer becomes the hook's allow decision" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const output = (try decisionOutput(arena.allocator(), "{\"behavior\":\"allow\",\"message\":\"ignored\"}")).?;
    try std.testing.expectEqualStrings("{\"hookSpecificOutput\":{\"hookEventName\":\"PermissionRequest\",\"decision\":{\"behavior\":\"allow\"}}}", output);
}

test "a deny answer carries its message" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const output = (try decisionOutput(arena.allocator(), "{\"behavior\":\"deny\",\"message\":\"not here\"}")).?;
    try std.testing.expectEqualStrings("{\"hookSpecificOutput\":{\"hookEventName\":\"PermissionRequest\",\"decision\":{\"behavior\":\"deny\",\"message\":\"not here\"}}}", output);
}

test "anything but allow or deny leaves the prompt to the session's own host" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    for ([_][]const u8{ "", "null", "[]", "{}", "{\"behavior\":\"ask\"}", "{\"behavior\":true}", "{\"behavior\":\"deny\",\"message\":3}", "not json" }) |answer| {
        try std.testing.expectEqual(@as(?[]const u8, null), try decisionOutput(arena.allocator(), answer));
    }
}

test "an unreachable endpoint leaves the prompt to the session's own host" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    try std.testing.expectEqual(@as(?[]const u8, null), ask(arena.allocator(), "http://127.0.0.1:1/claude/permission", "{}"));
    try std.testing.expectEqual(@as(?[]const u8, null), ask(arena.allocator(), "not a url", "{}"));
}

test "an endpoint that never answers is given up on within the bound" {
    if (!compat.net.supports_unix_channels) return error.SkipZigTest;
    const address = try compat.net.Address.parse("127.0.0.1", 0);
    var server = try address.listen(std.testing.io, .{});
    defer server.deinit(std.testing.io);
    const url = try std.fmt.allocPrint(std.testing.allocator, "http://127.0.0.1:{d}/claude/permission", .{server.socket.address.getPort()});
    defer std.testing.allocator.free(url);
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const started = try compat.time.monotonicMillis();
    try std.testing.expectEqual(@as(?[]const u8, null), askWithin(arena.allocator(), url, "{}", 200));
    try std.testing.expect(try compat.time.monotonicMillis() - started < 5_000);
}
