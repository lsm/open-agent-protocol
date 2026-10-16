const std = @import("std");
const builtin = @import("builtin");
const harness_pins = @import("harness_pins");
const compat = @import("compat");
const client = @import("client");
const httpapi = @import("httpapi");

pub const username = "opencode";

pub const Launch = struct {
    executable: []const u8,
    environment: []const []const u8 = &.{},
    working_directory: ?[]const u8 = null,
    log_path: []const u8,
    port: u16 = 0,
    ready_timeout_ns: u64 = 30 * std.time.ns_per_s,
    exit_grace_ns: u64 = 5 * std.time.ns_per_s,
};

pub fn admitted(version: []const u8) bool {
    for (harness_pins.opencode_admits) |admit| {
        if (std.mem.eql(u8, admit, version)) return true;
    }
    return false;
}

fn monotonic() u64 {
    return compat.time.monotonicNanos() catch 0;
}

pub const Server = struct {
    allocator: std.mem.Allocator,
    threaded: std.Io.Threaded,
    child: std.process.Child = undefined,
    watcher: ?std.Io.Future(void) = null,
    gone: std.atomic.Value(bool) = .init(false),
    status: u32 = 0,
    grace: u64,
    endpoint: []u8 = &.{},
    password: [32]u8 = undefined,

    pub fn start(allocator: std.mem.Allocator, arena: std.mem.Allocator, launch: Launch, why: *[]const u8) !*Server {
        const self = try allocator.create(Server);
        errdefer allocator.destroy(self);
        self.* = .{ .allocator = allocator, .threaded = .init(allocator, .{}), .grace = launch.exit_grace_ns };
        errdefer self.threaded.deinit();
        var secret: [16]u8 = undefined;
        compat.random.fillSecureBytes(&secret);
        self.password = std.fmt.bytesToHex(secret, .lower);
        const port = if (launch.port != 0) launch.port else try freePort();
        self.endpoint = try std.fmt.allocPrint(allocator, "http://127.0.0.1:{d}", .{port});
        errdefer allocator.free(self.endpoint);
        try self.spawn(arena, launch, port, why);
        errdefer self.stop();
        try self.ready(arena, launch, why);
        return self;
    }

    pub fn deinit(self: *Server) void {
        self.stop();
        self.allocator.free(self.endpoint);
        self.threaded.deinit();
        const allocator = self.allocator;
        self.* = undefined;
        allocator.destroy(self);
    }

    fn io(self: *Server) std.Io {
        return self.threaded.io();
    }

    fn freePort() !u16 {
        const address = try compat.net.resolveAddress(std.heap.page_allocator, "127.0.0.1", 0);
        var listener = try compat.net.tcpListen(address, .{});
        defer compat.net.closeServer(&listener);
        return compat.net.listenAddress(&listener).getPort();
    }

    fn spawn(self: *Server, arena: std.mem.Allocator, launch: Launch, port: u16, why: *[]const u8) !void {
        var environment = std.process.Environ.Map.init(arena);
        for (launch.environment) |entry| {
            const split = std.mem.indexOfScalar(u8, entry, '=') orelse continue;
            try environment.put(entry[0..split], entry[split + 1 ..]);
        }
        try environment.put("OPENCODE_PASSWORD", &self.password);
        if (std.fs.path.dirname(launch.log_path)) |directory| compat.fs.createDir(compat.fs.getCwd(), directory) catch {};
        const log = compat.fs.getCwd().createFile(self.io(), launch.log_path, .{ .truncate = true, .permissions = compat.fs.default_file_mode }) catch |err| {
            why.* = try std.fmt.allocPrint(arena, "could not open the OpenCode server log {s}: {s}", .{ launch.log_path, @errorName(err) });
            return error.ServerRefused;
        };
        defer log.close(self.io());
        const port_text = try std.fmt.allocPrint(arena, "{d}", .{port});
        self.child = std.process.spawn(self.io(), .{
            .argv = &.{ launch.executable, "serve", "--hostname", "127.0.0.1", "--port", port_text },
            .environ_map = &environment,
            .cwd = if (launch.working_directory) |path| .{ .path = path } else .inherit,
            .stdin = .ignore,
            .stdout = .{ .file = log },
            .stderr = .{ .file = log },
            .create_no_window = true,
        }) catch |err| {
            why.* = try std.fmt.allocPrint(arena, "could not start {s} serve: {s}", .{ launch.executable, @errorName(err) });
            return error.ServerRefused;
        };
        self.watcher = self.io().concurrent(watch, .{self}) catch |err| {
            self.child.kill(self.io());
            return err;
        };
    }

    fn watch(self: *Server) void {
        const term = self.child.wait(self.io()) catch {
            self.gone.store(true, .release);
            return;
        };
        self.status = switch (term) {
            .exited => |code| code,
            .signal, .stopped => |signal| @intFromEnum(signal),
            .unknown => |code| code,
        };
        self.gone.store(true, .release);
    }

    fn ready(self: *Server, arena: std.mem.Allocator, launch: Launch, why: *[]const u8) !void {
        const target = try client.parseEndpoint(arena, self.endpoint);
        const endpoint = httpapi.Endpoint{ .base_path = target.base_path, .username = username, .password = &self.password };
        const started = monotonic();
        while (monotonic() -| started < launch.ready_timeout_ns) {
            if (self.gone.load(.acquire)) {
                why.* = try std.fmt.allocPrint(arena, "{s} serve exited with status {d} before it answered; its log is {s}", .{ launch.executable, self.status, launch.log_path });
                return error.ServerRefused;
            }
            const response = client.roundTrip(self.allocator, arena, target, try httpapi.serverInfo(arena, endpoint), httpapi.default_frame_limit, std.time.ns_per_s) catch |err| {
                if (err == error.OutOfMemory) return error.OutOfMemory;
                self.io().sleep(.fromMilliseconds(100), .awake) catch {};
                continue;
            };
            if (response.status == 503) {
                self.io().sleep(.fromMilliseconds(100), .awake) catch {};
                continue;
            }
            if (response.status != 200) {
                why.* = try std.fmt.allocPrint(arena, "{s} serve answered /api/info with status {d}; its log is {s}", .{ launch.executable, response.status, launch.log_path });
                return error.ServerRefused;
            }
            const info = std.json.parseFromSliceLeaky(struct { version: []const u8 = "" }, arena, response.body, .{ .ignore_unknown_fields = true }) catch |err| {
                if (err == error.OutOfMemory) return error.OutOfMemory;
                why.* = try std.fmt.allocPrint(arena, "{s} serve answered /api/info with a body oapx cannot read; its log is {s}", .{ launch.executable, launch.log_path });
                return error.ServerRefused;
            };
            if (!admitted(info.version)) {
                why.* = try std.fmt.allocPrint(arena, "{s} is OpenCode {s}, which oapx has not been tested with; it runs {s}, which oapx opencode install fetches", .{ launch.executable, info.version, harness_pins.opencode_admits[0] });
                return error.ServerRefused;
            }
            return;
        }
        why.* = try std.fmt.allocPrint(arena, "{s} serve did not answer within {d}s; its log is {s}", .{ launch.executable, launch.ready_timeout_ns / std.time.ns_per_s, launch.log_path });
        return error.ServerRefused;
    }

    fn stop(self: *Server) void {
        var watcher = self.watcher orelse return;
        self.watcher = null;
        if (!self.gone.load(.acquire)) {
            const id = self.child.id.?;
            if (builtin.os.tag == .windows) {
                _ = std.os.windows.ntdll.NtTerminateProcess(id, .SUCCESS);
            } else {
                std.posix.kill(id, std.posix.SIG.TERM) catch {};
                const started = monotonic();
                while (!self.gone.load(.acquire) and monotonic() -| started < self.grace) self.io().sleep(.fromMilliseconds(20), .awake) catch {};
                if (!self.gone.load(.acquire)) std.posix.kill(id, std.posix.SIG.KILL) catch {};
            }
        }
        watcher.await(self.io());
    }
};

const testing = std.testing;

const FakeInfo = struct {
    listener: compat.net.Server = undefined,
    port: u16 = 0,
    body: []const u8,
    unavailable: std.atomic.Value(usize) = .init(0),
    thread: ?std.Thread = null,
    stop: std.atomic.Value(bool) = .init(false),
    authorized: std.atomic.Value(bool) = .init(false),

    fn start(self: *FakeInfo) !void {
        if (builtin.os.tag == .windows) return error.SkipZigTest;
        const address = try compat.net.resolveAddress(testing.allocator, "127.0.0.1", 0);
        self.listener = try compat.net.tcpListen(address, .{ .reuse_address = true });
        self.port = compat.net.listenAddress(&self.listener).getPort();
        self.thread = try std.Thread.spawn(.{}, serve, .{self});
    }

    fn finish(self: *FakeInfo) void {
        self.stop.store(true, .release);
        if (self.thread) |thread| thread.join();
        compat.net.closeServer(&self.listener);
    }

    fn serve(self: *FakeInfo) void {
        while (!self.stop.load(.acquire)) {
            if (!(compat.net.readableWithin(compat.net.serverHandle(&self.listener), 20) catch false)) continue;
            var stream = compat.net.acceptStream(&self.listener) catch continue;
            defer stream.close();
            var request: [4096]u8 = undefined;
            const read = stream.readSome(&request) catch continue;
            if (std.mem.indexOf(u8, request[0..read], "Authorization: Basic ") != null) self.authorized.store(true, .release);
            if (self.unavailable.load(.acquire) > 0) {
                _ = self.unavailable.fetchSub(1, .acq_rel);
                stream.writeAll("HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: close\r\n\r\n") catch {};
                continue;
            }
            var head: [128]u8 = undefined;
            const status = std.fmt.bufPrint(&head, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {d}\r\nConnection: close\r\n\r\n", .{self.body.len}) catch continue;
            stream.writeAll(status) catch continue;
            stream.writeAll(self.body) catch continue;
        }
    }
};

fn script(tmp: *testing.TmpDir, arena: std.mem.Allocator, body: []const u8) ![]const u8 {
    var file = try tmp.dir.createFile(testing.io, "opencode", .{ .permissions = @enumFromInt(0o755) });
    try file.writeStreamingAll(testing.io, body);
    file.close(testing.io);
    const directory = try tmp.dir.realPathFileAlloc(testing.io, ".", arena);
    return std.fs.path.join(arena, &.{ directory, "opencode" });
}

fn launchIn(tmp: *testing.TmpDir, arena: std.mem.Allocator, body: []const u8, port: u16) !Launch {
    const executable = try script(tmp, arena, body);
    const log_path = try std.fs.path.join(arena, &.{ std.fs.path.dirname(executable).?, "logs", "serve.log" });
    return .{ .executable = executable, .log_path = log_path, .port = port, .ready_timeout_ns = 10 * std.time.ns_per_s, .exit_grace_ns = std.time.ns_per_s };
}

test "a server that answers with a version oapx admits is ready at its endpoint, under a password of its own" {
    if (builtin.os.tag == .windows) return error.SkipZigTest;
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var fake = FakeInfo{ .body = try std.fmt.allocPrint(arena, "{{\"version\":\"{s}\"}}", .{harness_pins.opencode_admits[0]}) };
    try fake.start();
    defer fake.finish();
    var tmp = testing.tmpDir(.{});
    defer tmp.cleanup();
    const launch = try launchIn(&tmp, arena, "#!/bin/sh\n[ \"$1 $2 $3\" = \"serve --hostname 127.0.0.1\" ] || exit 7\n[ ${#OPENCODE_PASSWORD} -eq 32 ] || exit 8\nexec sleep 30\n", fake.port);
    var why: []const u8 = "";
    const server = try Server.start(testing.allocator, arena, launch, &why);
    defer server.deinit();
    try testing.expectEqualStrings(try std.fmt.allocPrint(arena, "http://127.0.0.1:{d}", .{fake.port}), server.endpoint);
    try testing.expect(fake.authorized.load(.acquire));
    try testing.expect(!server.gone.load(.acquire));
}

test "a server still starting, which answers 503, is waited for rather than refused" {
    if (builtin.os.tag == .windows) return error.SkipZigTest;
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var fake = FakeInfo{ .body = try std.fmt.allocPrint(arena, "{{\"version\":\"{s}\"}}", .{harness_pins.opencode_admits[0]}), .unavailable = .init(3) };
    try fake.start();
    defer fake.finish();
    var tmp = testing.tmpDir(.{});
    defer tmp.cleanup();
    const launch = try launchIn(&tmp, arena, "#!/bin/sh\nexec sleep 30\n", fake.port);
    var why: []const u8 = "";
    const server = try Server.start(testing.allocator, arena, launch, &why);
    defer server.deinit();
    try testing.expectEqual(@as(usize, 0), fake.unavailable.load(.acquire));
}

test "a server of a version oapx has not been tested with is refused, naming both versions" {
    if (builtin.os.tag == .windows) return error.SkipZigTest;
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var fake = FakeInfo{ .body = "{\"version\":\"0.0.1\"}" };
    try fake.start();
    defer fake.finish();
    var tmp = testing.tmpDir(.{});
    defer tmp.cleanup();
    const launch = try launchIn(&tmp, arena, "#!/bin/sh\nexec sleep 30\n", fake.port);
    var why: []const u8 = "";
    try testing.expectError(error.ServerRefused, Server.start(testing.allocator, arena, launch, &why));
    try testing.expectEqualStrings(try std.fmt.allocPrint(arena, "{s} is OpenCode 0.0.1, which oapx has not been tested with; it runs {s}, which oapx opencode install fetches", .{ launch.executable, harness_pins.opencode_admits[0] }), why);
}

test "a server that exits before it answers is refused, naming its status and the log it wrote" {
    if (builtin.os.tag == .windows) return error.SkipZigTest;
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var tmp = testing.tmpDir(.{});
    defer tmp.cleanup();
    const launch = try launchIn(&tmp, arena, "#!/bin/sh\necho no config\nexit 3\n", 0);
    var why: []const u8 = "";
    try testing.expectError(error.ServerRefused, Server.start(testing.allocator, arena, launch, &why));
    try testing.expectEqualStrings(try std.fmt.allocPrint(arena, "{s} serve exited with status 3 before it answered; its log is {s}", .{ launch.executable, launch.log_path }), why);
    const logged = try compat.fs.getCwd().readFileAlloc(testing.io, launch.log_path, arena, .limited(1024));
    try testing.expectEqualStrings("no config\n", logged);
}

test "only the versions the catalog admits are admitted" {
    try testing.expect(admitted(harness_pins.opencode_admits[0]));
    try testing.expect(!admitted(""));
    try testing.expect(!admitted("2.0.24"));
}
