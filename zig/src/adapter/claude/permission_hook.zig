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

pub fn ask(arena: std.mem.Allocator, endpoint: []const u8, request: []const u8) ?[]const u8 {
    const uri = std.Uri.parse(endpoint) catch return null;
    var client = compat.http.HttpClient.init(arena);
    defer client.deinit();
    const headers = [_]std.http.Header{.{ .name = "content-type", .value = "application/json" }};
    var pending = client.openRequest(.POST, uri, .{ .extra_headers = &headers, .keep_alive = false }) catch return null;
    defer pending.deinit();
    compat.http.sendRequest(&pending, request) catch return null;
    var head: [4096]u8 = undefined;
    var response = compat.http.receiveResponse(&pending, &head) catch return null;
    if (response.head.status != .ok) return null;
    var transfer: [4096]u8 = undefined;
    const reader = compat.http.responseReader(&response, &transfer);
    const answer = compat.http.allocRemainingResponse(arena, reader, answer_limit) catch return null;
    return decisionOutput(arena, answer) catch null;
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
