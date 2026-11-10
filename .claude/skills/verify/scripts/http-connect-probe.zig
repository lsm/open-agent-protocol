const std = @import("std");
const compat = @import("compat");

pub fn main(init: std.process.Init) void {
    perform(init) catch |err| {
        std.debug.print("FAIL {s}\n", .{@errorName(err)});
        std.process.exit(1);
    };
}

fn perform(init: std.process.Init) !void {
    var client = compat.http.HttpClient.init(init.gpa);
    defer client.deinit();
    client.client.io = init.io;
    try client.initDefaultProxies(init.arena.allocator(), init.environ_map);
    const now = std.Io.Clock.real.now(init.io);
    try client.client.ca_bundle.addCertsFromFilePathAbsolute(init.gpa, init.io, now, init.environ_map.get("TEST_CA_FILE").?);
    client.client.now = now;
    const uri = try std.Uri.parse(init.environ_map.get("TEST_URL").?);
    var request = try client.openRequest(.GET, uri, .{ .keep_alive = false });
    defer request.deinit();
    try compat.http.sendBodilessRequest(&request);
    var head: [4096]u8 = undefined;
    var response = try compat.http.receiveResponse(&request, &head);
    if (response.head.status != .ok) return error.UnexpectedStatus;
    var transfer: [4096]u8 = undefined;
    const body = try compat.http.allocRemainingResponse(init.gpa, compat.http.responseReader(&response, &transfer), 4096);
    defer init.gpa.free(body);
    if (!std.mem.eql(u8, body, "through tunnel")) return error.UnexpectedBody;
    std.debug.print("SUCCESS\n", .{});
}
