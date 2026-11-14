const std = @import("std");
const fs = @import("fs.zig");
const mod = @import("mod.zig");

pub const name = ".oxap";
pub const legacy_name = ".oapx";

fn joined(allocator: std.mem.Allocator, base: []const u8, child: []const u8) ![]u8 {
    if (base.len == 0) return allocator.dupe(u8, child);
    return std.fs.path.join(allocator, &.{ base, child });
}

fn directoryExists(path: []const u8) bool {
    return fs.fileKind(fs.getCwd(), path) == .directory;
}

pub fn stateDirName(allocator: std.mem.Allocator, base: []const u8) ![]u8 {
    const fresh = try joined(allocator, base, name);
    defer allocator.free(fresh);
    if (directoryExists(fresh)) return allocator.dupe(u8, name);
    const legacy = try joined(allocator, base, legacy_name);
    defer allocator.free(legacy);
    if (directoryExists(legacy)) return allocator.dupe(u8, legacy_name);
    return allocator.dupe(u8, name);
}

pub fn stateDirIn(allocator: std.mem.Allocator, base: []const u8) ![]u8 {
    const held = try stateDirName(allocator, base);
    defer allocator.free(held);
    return std.fs.path.join(allocator, &.{ base, held });
}

pub fn stateSubpathIn(allocator: std.mem.Allocator, base: []const u8, sub: []const u8) ![]u8 {
    const dir = try stateDirIn(allocator, base);
    defer allocator.free(dir);
    return std.fs.path.join(allocator, &.{ dir, sub });
}

pub fn homeStateDir(allocator: std.mem.Allocator) ![]u8 {
    const home = try mod.getEnvVarOwned(allocator, "HOME");
    defer allocator.free(home);
    return stateDirIn(allocator, home);
}

pub fn homeStateSubpath(allocator: std.mem.Allocator, sub: []const u8) ![]u8 {
    const home = try mod.getEnvVarOwned(allocator, "HOME");
    defer allocator.free(home);
    return stateSubpathIn(allocator, home, sub);
}

pub fn workspaceStateDir(allocator: std.mem.Allocator) ![]u8 {
    return stateDirIn(allocator, "");
}

test "a home with no state directory resolves to the new name" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmp.dir.realPathFileAlloc(std.testing.io, ".", std.testing.allocator);
    defer std.testing.allocator.free(base);
    const resolved = try stateDirIn(std.testing.allocator, base);
    defer std.testing.allocator.free(resolved);
    const expected = try std.fs.path.join(std.testing.allocator, &.{ base, name });
    defer std.testing.allocator.free(expected);
    try std.testing.expectEqualStrings(expected, resolved);
}

test "a home whose state is only under the legacy name keeps resolving to it" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.createDirPath(std.testing.io, legacy_name);
    const base = try tmp.dir.realPathFileAlloc(std.testing.io, ".", std.testing.allocator);
    defer std.testing.allocator.free(base);
    const resolved = try stateDirIn(std.testing.allocator, base);
    defer std.testing.allocator.free(resolved);
    const expected = try std.fs.path.join(std.testing.allocator, &.{ base, legacy_name });
    defer std.testing.allocator.free(expected);
    try std.testing.expectEqualStrings(expected, resolved);
}

test "a home holding both names resolves to the new one, so a store never forks across two files" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.createDirPath(std.testing.io, legacy_name);
    try tmp.dir.createDirPath(std.testing.io, name);
    const base = try tmp.dir.realPathFileAlloc(std.testing.io, ".", std.testing.allocator);
    defer std.testing.allocator.free(base);
    const resolved = try stateDirIn(std.testing.allocator, base);
    defer std.testing.allocator.free(resolved);
    const expected = try std.fs.path.join(std.testing.allocator, &.{ base, name });
    defer std.testing.allocator.free(expected);
    try std.testing.expectEqualStrings(expected, resolved);
}

test "a legacy path that is a file rather than a directory is not taken as state" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = legacy_name, .data = "" });
    const base = try tmp.dir.realPathFileAlloc(std.testing.io, ".", std.testing.allocator);
    defer std.testing.allocator.free(base);
    const resolved = try stateDirIn(std.testing.allocator, base);
    defer std.testing.allocator.free(resolved);
    const expected = try std.fs.path.join(std.testing.allocator, &.{ base, name });
    defer std.testing.allocator.free(expected);
    try std.testing.expectEqualStrings(expected, resolved);
}

test "a subpath is joined under the directory its home resolves to" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.createDirPath(std.testing.io, legacy_name);
    const base = try tmp.dir.realPathFileAlloc(std.testing.io, ".", std.testing.allocator);
    defer std.testing.allocator.free(base);
    const resolved = try stateSubpathIn(std.testing.allocator, base, "auth.json");
    defer std.testing.allocator.free(resolved);
    const expected = try std.fs.path.join(std.testing.allocator, &.{ base, legacy_name, "auth.json" });
    defer std.testing.allocator.free(expected);
    try std.testing.expectEqualStrings(expected, resolved);
}

test "a workspace state path is relative to the working directory" {
    const resolved = try workspaceStateDir(std.testing.allocator);
    defer std.testing.allocator.free(resolved);
    try std.testing.expectEqualStrings(name, resolved);
}
