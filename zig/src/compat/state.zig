const std = @import("std");
const fs = @import("fs.zig");

pub const dir_name = ".oxap";
pub const legacy_dir_name = ".oapx";

pub fn nameIn(dir: std.Io.Dir) []const u8 {
    if (fs.fileKind(dir, dir_name) == .absent and fs.fileKind(dir, legacy_dir_name) == .directory) return legacy_dir_name;
    return dir_name;
}

pub fn nameUnder(allocator: std.mem.Allocator, base: []const u8) ![]const u8 {
    const fresh = try std.fs.path.join(allocator, &.{ base, dir_name });
    defer allocator.free(fresh);
    if (fs.fileKind(fs.getCwd(), fresh) != .absent) return dir_name;
    const legacy = try std.fs.path.join(allocator, &.{ base, legacy_dir_name });
    defer allocator.free(legacy);
    return if (fs.fileKind(fs.getCwd(), legacy) == .directory) legacy_dir_name else dir_name;
}

test "a legacy name that is a file, not a directory, is not taken as state" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = legacy_dir_name, .data = "" });
    try std.testing.expectEqualStrings(dir_name, nameIn(tmp.dir));
}
