const std = @import("std");
const builtin = @import("builtin");
const harness_pins = @import("harness_pins");
const compat = @import("compat");

pub const download_base = "https://opencode.ai/files/bin/";
pub const version = harness_pins.opencode_admits[0];

pub const Archive = enum { zip, tar_gz };

pub const Artifact = struct {
    name: []const u8,
    sha256: []const u8,
    bytes: u64,
    binary_sha256: []const u8,
    archive: Archive,
};

pub const artifact: ?Artifact = pinned: {
    const os = switch (builtin.os.tag) {
        .macos => "darwin",
        .linux => "linux",
        else => break :pinned null,
    };
    const arch = switch (builtin.cpu.arch) {
        .aarch64 => "arm64",
        .x86_64 => "x64",
        else => break :pinned null,
    };
    const stem = "opencode_opencode_" ++ os ++ "_" ++ arch;
    if (!@hasDecl(harness_pins, stem ++ "_archive_sha256") or !@hasDecl(harness_pins, stem ++ "_binary_sha256")) break :pinned null;
    const name = @field(harness_pins, stem ++ "_archive_name");
    break :pinned .{
        .name = name,
        .sha256 = @field(harness_pins, stem ++ "_archive_sha256"),
        .bytes = @field(harness_pins, stem ++ "_archive_bytes"),
        .binary_sha256 = @field(harness_pins, stem ++ "_binary_sha256"),
        .archive = if (std.mem.endsWith(u8, name, ".zip")) .zip else .tar_gz,
    };
};

pub fn installedPath(arena: std.mem.Allocator, home: []const u8) ![]const u8 {
    return std.fs.path.join(arena, &.{ home, ".oapx", "opencode", version, "opencode" });
}

pub fn installed(arena: std.mem.Allocator, home: []const u8) !?[]const u8 {
    const path = try installedPath(arena, home);
    return if (compat.fs.fileKind(compat.fs.getCwd(), path) == .file) path else null;
}

fn hex(bytes: []const u8) [64]u8 {
    var digest: [32]u8 = undefined;
    std.crypto.hash.sha2.Sha256.hash(bytes, &digest, .{});
    return std.fmt.bytesToHex(digest, .lower);
}

pub fn install(allocator: std.mem.Allocator, arena: std.mem.Allocator, home: []const u8, why: *[]const u8) ![]const u8 {
    const pinned = artifact orelse {
        why.* = "oapx has not been tested with an OpenCode build for this platform";
        return error.InstallRefused;
    };
    const url = try std.mem.concat(arena, u8, &.{ download_base, version, "/", pinned.name });
    var fetched = compat.http.fetch(allocator, url, .{ .accept_encoding = "identity", .max_response_bytes = @intCast(pinned.bytes + 1), .timeout_ms = 15 * std.time.ms_per_min }) catch |err| {
        why.* = try std.fmt.allocPrint(arena, "could not download {s}: {s}", .{ url, @errorName(err) });
        return error.InstallRefused;
    };
    defer fetched.deinit(allocator);
    if (fetched.status != 200) {
        why.* = try std.fmt.allocPrint(arena, "{s} answered {d}", .{ url, fetched.status });
        return error.InstallRefused;
    }
    return place(arena, home, pinned, fetched.body, why);
}

pub fn place(arena: std.mem.Allocator, home: []const u8, pinned: Artifact, body: []const u8, why: *[]const u8) ![]const u8 {
    if (!std.mem.eql(u8, &hex(body), pinned.sha256)) {
        why.* = try std.fmt.allocPrint(arena, "{s} is not the archive the catalog pins (sha256 {s})", .{ pinned.name, pinned.sha256 });
        return error.InstallRefused;
    }
    const io = compat.fs.defaultIo();
    const cwd = compat.fs.getCwd();
    const final = try std.fs.path.join(arena, &.{ home, ".oapx", "opencode", version });
    const staging = try std.mem.concat(arena, u8, &.{ final, ".partial" });
    cwd.deleteTree(io, staging) catch {};
    try compat.fs.createDir(cwd, staging);
    errdefer cwd.deleteTree(io, staging) catch {};
    var dir = try cwd.openDir(io, staging, .{});
    defer dir.close(io);
    try unpack(arena, dir, pinned.archive, body);
    const binary = try dir.readFileAlloc(io, "opencode", arena, .limited(@intCast(pinned.bytes * 4)));
    if (!std.mem.eql(u8, &hex(binary), pinned.binary_sha256)) {
        why.* = try std.fmt.allocPrint(arena, "the opencode in {s} is not the binary the catalog pins (sha256 {s})", .{ pinned.name, pinned.binary_sha256 });
        return error.InstallRefused;
    }
    var file = try dir.openFile(io, "opencode", .{});
    defer file.close(io);
    try file.setPermissions(io, @enumFromInt(0o755));
    cwd.deleteTree(io, final) catch {};
    try cwd.rename(staging, cwd, final, io);
    return installedPath(arena, home);
}

fn unpack(arena: std.mem.Allocator, dir: std.Io.Dir, archive: Archive, body: []const u8) !void {
    const io = compat.fs.defaultIo();
    switch (archive) {
        .zip => {
            try dir.writeFile(io, .{ .sub_path = "archive.zip", .data = body });
            defer dir.deleteFile(io, "archive.zip") catch {};
            var file = try dir.openFile(io, "archive.zip", .{});
            defer file.close(io);
            var buffer: [4096]u8 = undefined;
            var reader = file.reader(io, &buffer);
            try std.zip.extract(dir, &reader, .{});
        },
        .tar_gz => {
            var input: std.Io.Reader = .fixed(body);
            const window = try arena.alloc(u8, std.compress.flate.max_window_len);
            var inflate: std.compress.flate.Decompress = .init(&input, .gzip, window);
            try std.tar.extract(io, dir, &inflate.reader, .{});
        },
    }
}

const testing = std.testing;

fn gzipTar(arena: std.mem.Allocator, name: []const u8, content: []const u8) ![]const u8 {
    var tarred: std.Io.Writer.Allocating = .init(arena);
    var tar: std.tar.Writer = .{ .underlying_writer = &tarred.writer };
    try tar.writeFileBytes(name, content, .{ .mode = 0o644 });
    try tar.finishPedantically();
    var zipped = try std.Io.Writer.Allocating.initCapacity(arena, 4096);
    const window = try arena.alloc(u8, std.compress.flate.max_window_len);
    var deflate = try std.compress.flate.Compress.init(&zipped.writer, window, .gzip, .default);
    try deflate.writer.writeAll(tarred.written());
    try deflate.finish();
    return zipped.written();
}

test "a pinned archive is unpacked into the version's own directory as an executable opencode" {
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var tmp = testing.tmpDir(.{});
    defer tmp.cleanup();
    const home = try tmp.dir.realPathFileAlloc(testing.io, ".", arena);
    const body = try gzipTar(arena, "opencode", "#!/bin/sh\n");
    const pinned = Artifact{ .name = "opencode-linux-x64.tar.gz", .sha256 = &hex(body), .bytes = body.len, .binary_sha256 = &hex("#!/bin/sh\n"), .archive = .tar_gz };
    var why: []const u8 = "";
    const path = try place(arena, home, pinned, body, &why);
    try testing.expectEqualStrings(try installedPath(arena, home), path);
    try testing.expectEqualStrings(path, (try installed(arena, home)).?);
    const stat = try compat.fs.getCwd().statFile(testing.io, path, .{});
    try testing.expectEqual(@as(u32, 0o755), @intFromEnum(stat.permissions) & 0o777);
}

test "an archive or a binary that is not the one the catalog pins is refused and leaves nothing installed" {
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var tmp = testing.tmpDir(.{});
    defer tmp.cleanup();
    const home = try tmp.dir.realPathFileAlloc(testing.io, ".", arena);
    const body = try gzipTar(arena, "opencode", "#!/bin/sh\n");
    var why: []const u8 = "";
    const wrong_archive = Artifact{ .name = "opencode-linux-x64.tar.gz", .sha256 = &hex("other"), .bytes = body.len, .binary_sha256 = &hex("#!/bin/sh\n"), .archive = .tar_gz };
    try testing.expectError(error.InstallRefused, place(arena, home, wrong_archive, body, &why));
    try testing.expect(std.mem.indexOf(u8, why, "is not the archive the catalog pins") != null);
    const wrong_binary = Artifact{ .name = "opencode-linux-x64.tar.gz", .sha256 = &hex(body), .bytes = body.len, .binary_sha256 = &hex("other"), .archive = .tar_gz };
    try testing.expectError(error.InstallRefused, place(arena, home, wrong_binary, body, &why));
    try testing.expect(std.mem.indexOf(u8, why, "is not the binary the catalog pins") != null);
    try testing.expect(try installed(arena, home) == null);
}

test "this platform's pinned archive, when the catalog has one, is a download oapx knows how to unpack" {
    const pinned = artifact orelse return error.SkipZigTest;
    try testing.expectEqual(@as(usize, 64), pinned.sha256.len);
    try testing.expectEqual(@as(usize, 64), pinned.binary_sha256.len);
    try testing.expect(pinned.bytes > 0);
    try testing.expect(std.mem.endsWith(u8, pinned.name, ".zip") or std.mem.endsWith(u8, pinned.name, ".tar.gz"));
}
