const std = @import("std");

pub fn main(init: std.process.Init) !void {
    const allocator = init.arena.allocator();
    const args = try init.minimal.args.toSlice(allocator);
    const source = try std.Io.Dir.cwd().readFileAlloc(init.io, args[1], allocator, .limited(1 << 20));
    var digest: [32]u8 = undefined;
    std.crypto.hash.sha2.Sha256.hash(source, &digest, .{});
    const hex = std.fmt.bytesToHex(digest, .lower);
    if (!std.mem.eql(u8, &hex, "dc88cb35d6edab886d08761431b6955a52822e5f166756ec4d0f5c2ad70be2db")) return error.UnsupportedHttpClientSource;
    const imported = try std.mem.replaceOwned(u8, allocator, source, "@import(\"../std.zig\")", "@import(\"std\")");
    const configured = try std.mem.replaceOwned(u8, allocator, imported, "\nallocator: Allocator,\n", "\nallocator: Allocator,\nno_proxy: []const u8 = \"\",\nhttp_proxy_invalid: bool = false,\nhttps_proxy_invalid: bool = false,\n");
    const init_start = std.mem.find(u8, configured, "pub fn initDefaultProxies(") orelse return error.MissingProxyInitializer;
    const init_end = std.mem.find(u8, configured, "fn createProxyFromEnvVar(") orelse return error.MissingProxyParser;
    const without_init = try std.fmt.allocPrint(allocator, "{s}{s}", .{ configured[0..init_start], configured[init_end..] });
    const start = std.mem.find(u8, without_init, "pub fn connect(") orelse return error.MissingConnectImplementation;
    const end = std.mem.find(u8, without_init, "pub const RequestError =") orelse return error.MissingRequestErrors;
    const patched = try std.fmt.allocPrint(allocator, "{s}{s}\n{s}", .{ without_init[0..start], @embedFile("http_connect.zig"), without_init[end..] });
    const with_proxy_error = try std.mem.replaceOwned(u8, allocator, patched, "pub const RequestError = ConnectTcpError || error{", "pub const RequestError = ConnectTcpError || error{\n    UnsupportedProxyProtocol,");
    try std.Io.Dir.cwd().writeFile(init.io, .{ .sub_path = args[2], .data = with_proxy_error });
}
