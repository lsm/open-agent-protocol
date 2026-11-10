const std = @import("std");
const compat = @import("compat");

pub fn main(init: std.process.Init) void {
    perform(init) catch |err| {
        std.debug.print("FAIL {s}\n", .{@errorName(err)});
        std.process.exit(1);
    };
}

fn perform(init: std.process.Init) !void {
    var debug_allocator: std.heap.DebugAllocator(.{}) = .init;
    defer std.debug.assert(debug_allocator.deinit() == .ok);
    const allocator = debug_allocator.allocator();
    var client = compat.http.HttpClient.init(allocator);
    defer client.deinit();
    client.client.io = init.io;
    const late_proxy = init.environ_map.get("TEST_LATE_PROXY") != null;
    if (!late_proxy) try client.initDefaultProxies(init.arena.allocator(), init.environ_map);
    const now = std.Io.Clock.real.now(init.io);
    try client.client.ca_bundle.addCertsFromFilePathAbsolute(allocator, init.io, now, init.environ_map.get("TEST_CA_FILE").?);
    client.client.now = now;
    if (init.environ_map.get("TEST_WARM_URL")) |warm_url| {
        var warm = try client.openRequest(.GET, try std.Uri.parse(warm_url), .{});
        defer warm.deinit();
        try compat.http.sendBodilessRequest(&warm);
        var head: [4096]u8 = undefined;
        var response = try compat.http.receiveResponse(&warm, &head);
        var transfer: [4096]u8 = undefined;
        const body = try compat.http.allocRemainingResponse(allocator, compat.http.responseReader(&response, &transfer), 4096);
        defer allocator.free(body);
        if (response.head.status != .ok) return error.UnexpectedWarmStatus;
    }
    if (late_proxy) try client.initDefaultProxies(init.arena.allocator(), init.environ_map);
    const uri = try std.Uri.parse(init.environ_map.get("TEST_URL").?);
    var request = try client.openRequest(.GET, uri, .{ .keep_alive = false });
    defer request.deinit();
    try compat.http.sendBodilessRequest(&request);
    var head: [4096]u8 = undefined;
    var response = try compat.http.receiveResponse(&request, &head);
    if (response.head.status != .ok) return error.UnexpectedStatus;
    var transfer: [4096]u8 = undefined;
    const body = try compat.http.allocRemainingResponse(allocator, compat.http.responseReader(&response, &transfer), 4096);
    defer allocator.free(body);
    if (!std.mem.eql(u8, body, "through tunnel")) return error.UnexpectedBody;
    std.debug.print("SUCCESS\n", .{});
}
