const std = @import("std");
const compat = @import("compat");
const policy = @import("oap_provider_http_policy");
const sse_parser = @import("sse_parser");

pub const MAX_RESPONSE_BYTES: usize = 1024 * 1024;

fn hasMediaType(actual: []const u8, expected: []const u8) bool {
    return std.mem.eql(u8, actual, expected) or
        (actual.len > expected.len and std.mem.startsWith(u8, actual, expected) and actual[expected.len] == ';');
}

pub const Client = struct {
    allocator: std.mem.Allocator,
    base_url: []u8,
    security: policy.Security,
    request_timeout_ms: u64 = 30_000,
    stream_idle_timeout_ms: u64 = 120_000,

    pub fn init(allocator: std.mem.Allocator, base_url: []const u8, security: policy.Security) !Client {
        _ = try policy.validateBaseUrl(base_url, security);
        return .{
            .allocator = allocator,
            .base_url = try allocator.dupe(u8, base_url),
            .security = security,
        };
    }

    pub fn deinit(self: *Client) void {
        self.allocator.free(self.base_url);
        self.* = undefined;
    }

    pub fn postUnary(self: *Client, body: []const u8) ![]u8 {
        const job = try TimedJob.create(self, body, .unary);
        defer job.release();
        try job.start();
        try job.waitUnary(self.request_timeout_ms);
        const answer = job.answer orelse return error.ProviderServiceIncompleteResponse;
        return self.allocator.dupe(u8, answer);
    }

    fn postUnaryBlocking(self: *Client, body: []const u8, job: *TimedJob) ![]u8 {
        _ = try policy.validateBaseUrl(self.base_url, self.security);
        const trimmed = std.mem.trimEnd(u8, self.base_url, "/");
        const endpoint = try std.fmt.allocPrint(self.allocator, "{s}{s}", .{ trimmed, policy.ENDPOINT_PATH });
        defer self.allocator.free(endpoint);
        const uri = std.Uri.parse(endpoint) catch return error.InvalidProviderServiceUrl;

        var http = compat.http.HttpClient.init(self.allocator);
        defer http.deinit();
        var request = try http.openRequest(.POST, uri, .{
            .extra_headers = &.{
                .{ .name = "Content-Type", .value = "application/json" },
                .{ .name = "Accept", .value = "application/json" },
            },
            .keep_alive = false,
        });
        job.setSocket(request.connection.?.stream_reader.stream);
        defer {
            job.clearSocket();
            request.deinit();
        }
        try compat.http.sendRequest(&request, body);
        var redirect_buffer: [4096]u8 = undefined;
        var response = try compat.http.receiveResponse(&request, &redirect_buffer);
        if (response.head.status != .ok) return error.ProviderServiceHttpFailure;
        if (response.head.content_type == null or
            !hasMediaType(response.head.content_type.?, "application/json"))
        {
            return error.UnexpectedProviderServiceContentType;
        }
        var transfer_buffer: [4096]u8 = undefined;
        const reader = compat.http.responseReader(&response, &transfer_buffer);
        return compat.http.allocRemainingResponse(self.allocator, reader, MAX_RESPONSE_BYTES);
    }

    pub fn postStream(
        self: *Client,
        body: []const u8,
        context: ?*anyopaque,
        on_envelope: *const fn (?*anyopaque, []const u8) anyerror!void,
    ) !void {
        const job = try TimedJob.create(self, body, .stream);
        defer job.release();
        try job.start();
        return job.waitStream(self.request_timeout_ms, self.stream_idle_timeout_ms, context, on_envelope);
    }

    fn postStreamBlocking(
        self: *Client,
        body: []const u8,
        job: *TimedJob,
    ) !void {
        _ = try policy.validateBaseUrl(self.base_url, self.security);
        const trimmed = std.mem.trimEnd(u8, self.base_url, "/");
        const endpoint = try std.fmt.allocPrint(self.allocator, "{s}{s}", .{ trimmed, policy.ENDPOINT_PATH });
        defer self.allocator.free(endpoint);
        const uri = std.Uri.parse(endpoint) catch return error.InvalidProviderServiceUrl;

        var http = compat.http.HttpClient.init(self.allocator);
        defer http.deinit();
        var request = try http.openRequest(.POST, uri, .{
            .extra_headers = &.{
                .{ .name = "Content-Type", .value = "application/json" },
                .{ .name = "Accept", .value = "text/event-stream, application/json" },
            },
            .keep_alive = false,
        });
        job.setSocket(request.connection.?.stream_reader.stream);
        defer {
            job.clearSocket();
            request.deinit();
        }
        try compat.http.sendRequest(&request, body);
        var redirect_buffer: [4096]u8 = undefined;
        var response = try compat.http.receiveResponse(&request, &redirect_buffer);
        if (response.head.status != .ok) return error.ProviderServiceHttpFailure;
        const content_type = response.head.content_type orelse return error.UnexpectedProviderServiceContentType;
        const is_stream = hasMediaType(content_type, "text/event-stream");
        if (!is_stream and !hasMediaType(content_type, "application/json")) {
            return error.UnexpectedProviderServiceContentType;
        }
        var transfer_buffer: [4096]u8 = undefined;
        const reader = compat.http.responseReader(&response, &transfer_buffer);
        if (!is_stream) {
            const answer = try compat.http.allocRemainingResponse(self.allocator, reader, MAX_RESPONSE_BYTES);
            defer self.allocator.free(answer);
            try TimedJob.collect(job, answer);
            return;
        }

        var parser = sse_parser.SSEParser.initWithLimits(self.allocator, .{
            .line_bytes = MAX_RESPONSE_BYTES,
            .event_bytes = MAX_RESPONSE_BYTES,
        });
        defer parser.deinit();
        var buffer: [4096]u8 = undefined;
        while (true) {
            const n = try compat.http.readResponse(reader, &buffer);
            if (n == 0) break;
            job.noteProgress();
            const events = try parser.feed(buffer[0..n]);
            for (events) |event| try TimedJob.collect(job, event.data);
        }
    }
};

const TimedJob = struct {
    const Kind = enum { unary, stream };
    const arena = std.heap.page_allocator;

    refs: std.atomic.Value(u32) = std.atomic.Value(u32).init(1),
    done: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    progress_ms: std.atomic.Value(i64) = std.atomic.Value(i64).init(0),
    mutex: std.Io.Mutex = .init,
    cancelled: bool = false,
    socket: ?std.Io.net.Stream = null,
    pending: std.ArrayList([]u8) = .empty,
    answer: ?[]u8 = null,
    failure: ?anyerror = null,
    base_url: []u8,
    body: []u8,
    security: policy.Security,
    kind: Kind,

    fn io() std.Io {
        return if (@import("builtin").is_test) std.testing.io else std.Io.Threaded.global_single_threaded.io();
    }

    fn create(client: *const Client, body: []const u8, kind: Kind) !*TimedJob {
        const job = try arena.create(TimedJob);
        errdefer arena.destroy(job);
        const url = try arena.dupe(u8, client.base_url);
        errdefer arena.free(url);
        const request = try arena.dupe(u8, body);
        job.* = .{ .base_url = url, .body = request, .security = client.security, .kind = kind };
        return job;
    }

    fn release(self: *TimedJob) void {
        if (self.refs.fetchSub(1, .acq_rel) != 1) return;
        for (self.pending.items) |line| arena.free(line);
        self.pending.deinit(arena);
        if (self.answer) |answer| arena.free(answer);
        arena.free(self.base_url);
        arena.free(self.body);
        arena.destroy(self);
    }

    fn setSocket(self: *TimedJob, socket: std.Io.net.Stream) void {
        self.mutex.lockUncancelable(io());
        defer self.mutex.unlock(io());
        self.socket = socket;
        if (self.cancelled) socket.shutdown(io(), .both) catch {};
    }

    fn clearSocket(self: *TimedJob) void {
        self.mutex.lockUncancelable(io());
        self.socket = null;
        self.mutex.unlock(io());
    }

    fn cancel(self: *TimedJob) void {
        self.mutex.lockUncancelable(io());
        defer self.mutex.unlock(io());
        self.cancelled = true;
        if (self.socket) |socket| socket.shutdown(io(), .both) catch {};
    }

    fn noteProgress(self: *TimedJob) void {
        const now = compat.time.monotonicMillis() catch return;
        self.progress_ms.store(now, .release);
    }

    fn collect(context: ?*anyopaque, line: []const u8) !void {
        const self: *TimedJob = @ptrCast(@alignCast(context));
        const copy = try arena.dupe(u8, line);
        errdefer arena.free(copy);
        while (true) {
            self.mutex.lockUncancelable(io());
            if (self.cancelled) {
                self.mutex.unlock(io());
                return error.ProviderServiceCancelled;
            }
            if (self.pending.items.len < 128) {
                defer self.mutex.unlock(io());
                try self.pending.append(arena, copy);
                return;
            }
            self.mutex.unlock(io());
            compat.time.sleepMs(1);
        }
    }

    fn run(self: *TimedJob) void {
        defer self.release();
        var client = Client.init(arena, self.base_url, self.security) catch |err| {
            self.failure = err;
            self.done.store(true, .release);
            return;
        };
        defer client.deinit();
        const result: anyerror!void = switch (self.kind) {
            .unary => blk: {
                self.answer = client.postUnaryBlocking(self.body, self) catch |err| break :blk err;
                break :blk {};
            },
            .stream => client.postStreamBlocking(self.body, self),
        };
        if (result) |_| {} else |err| self.failure = err;
        self.done.store(true, .release);
    }

    fn start(self: *TimedJob) !void {
        _ = self.refs.fetchAdd(1, .acq_rel);
        const thread = std.Thread.spawn(.{}, run, .{self}) catch |err| {
            self.release();
            return err;
        };
        thread.detach();
    }

    fn waitUnary(self: *TimedJob, timeout_ms: u64) !void {
        const start_ms = try compat.time.monotonicMillis();
        while (!self.done.load(.acquire)) {
            if (try compat.time.monotonicMillis() - start_ms >= timeout_ms) {
                self.cancel();
                return error.ProviderServiceTimeout;
            }
            compat.time.sleepMs(1);
        }
        if (self.failure) |err| return err;
    }

    fn waitStream(
        self: *TimedJob,
        request_timeout_ms: u64,
        idle_timeout_ms: u64,
        context: ?*anyopaque,
        on_envelope: *const fn (?*anyopaque, []const u8) anyerror!void,
    ) !void {
        var last_progress_ms = try compat.time.monotonicMillis();
        var saw_envelope = false;
        while (true) {
            self.mutex.lockUncancelable(io());
            const line = if (self.pending.items.len > 0) self.pending.orderedRemove(0) else null;
            self.mutex.unlock(io());
            if (line) |owned| {
                defer arena.free(owned);
                on_envelope(context, owned) catch |err| {
                    self.cancel();
                    return err;
                };
                saw_envelope = true;
                last_progress_ms = try compat.time.monotonicMillis();
                continue;
            }
            if (self.done.load(.acquire)) {
                if (self.failure) |err| return err;
                return;
            }
            last_progress_ms = @max(last_progress_ms, self.progress_ms.load(.acquire));
            const limit = if (saw_envelope) idle_timeout_ms else request_timeout_ms;
            if (try compat.time.monotonicMillis() - last_progress_ms >= limit) {
                self.cancel();
                return error.ProviderServiceTimeout;
            }
            compat.time.sleepMs(1);
        }
    }
};

test "remote provider client validates the operator URL before network access" {
    try std.testing.expectError(error.UnprotectedProviderService, Client.init(std.testing.allocator, "http://provider.svc.cluster.local", .loopback));
    try std.testing.expectError(error.InvalidProviderServiceUrl, Client.init(std.testing.allocator, "https://user:pass@provider.test", .tls));
    var client = try Client.init(std.testing.allocator, "https://provider.test", .tls);
    client.deinit();
}

const MockUnaryServer = struct {
    server: compat.net.Server,
    thread: ?std.Thread = null,
    saw_provider_path: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    response_type: []const u8 = "application/json",
    response_body: []const u8 = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.model-provider-core\",\"type\":\"provider.describe.response\",\"id\":\"answer\",\"in_reply_to\":\"request\",\"capability_revision\":\"r1\",\"payload\":{\"providers\":[]}}",
    response_delay_ms: u64 = 0,
    middle_delay_ms: u64 = 0,
    middle_body: []const u8 = "",
    tail_delay_ms: u64 = 0,
    tail_body: []const u8 = "",
    hold_open_ms: u64 = 0,

    fn start(self: *MockUnaryServer) !void {
        self.thread = try std.Thread.spawn(.{}, serve, .{self});
    }

    fn stop(self: *MockUnaryServer) void {
        if (self.thread) |thread| thread.join();
        compat.net.closeServer(&self.server);
    }

    fn serve(self: *MockUnaryServer) void {
        var conn = compat.net.accept(&self.server) catch return;
        defer conn.stream.close();
        var request: [8192]u8 = undefined;
        var filled: usize = 0;
        while (filled < request.len) {
            const n = conn.stream.read(request[filled .. filled + 1]) catch return;
            if (n == 0) return;
            filled += n;
            if (filled >= 4 and std.mem.eql(u8, request[filled - 4 .. filled], "\r\n\r\n")) break;
        }
        self.saw_provider_path.store(std.mem.startsWith(u8, request[0..filled], "POST /oap/v0.1/provider HTTP/1.1"), .release);
        var lines = std.mem.splitSequence(u8, request[0..filled], "\r\n");
        var content_length: usize = 0;
        while (lines.next()) |line| {
            if (!std.ascii.startsWithIgnoreCase(line, "content-length:")) continue;
            content_length = std.fmt.parseInt(usize, std.mem.trim(u8, line["content-length:".len..], " \t"), 10) catch return;
        }
        var remaining = content_length;
        while (remaining > 0) {
            const n = conn.stream.read(request[0..@min(remaining, request.len)]) catch return;
            if (n == 0) return;
            remaining -= n;
        }
        if (self.response_delay_ms > 0) compat.time.sleepMs(self.response_delay_ms);
        var header: [160]u8 = undefined;
        const chunked = self.hold_open_ms > 0 or self.middle_body.len > 0 or self.tail_body.len > 0;
        const response = if (chunked)
            std.fmt.bufPrint(&header, "HTTP/1.1 200 OK\r\nContent-Type: {s}\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n", .{self.response_type}) catch return
        else
            std.fmt.bufPrint(&header, "HTTP/1.1 200 OK\r\nContent-Type: {s}\r\nContent-Length: {d}\r\nConnection: close\r\n\r\n", .{ self.response_type, self.response_body.len }) catch return;
        conn.stream.writeAll(response) catch return;
        if (chunked) {
            writeChunk(&conn.stream, self.response_body) catch return;
            if (self.middle_delay_ms > 0) compat.time.sleepMs(self.middle_delay_ms);
            if (self.middle_body.len > 0) writeChunk(&conn.stream, self.middle_body) catch return;
            if (self.tail_delay_ms > 0) compat.time.sleepMs(self.tail_delay_ms);
            if (self.tail_body.len > 0) writeChunk(&conn.stream, self.tail_body) catch return;
        } else {
            conn.stream.writeAll(self.response_body) catch return;
        }
        if (self.hold_open_ms > 0) compat.time.sleepMs(self.hold_open_ms);
        if (chunked) conn.stream.writeAll("0\r\n\r\n") catch return;
    }

    fn writeChunk(stream: *compat.net.Stream, body: []const u8) !void {
        var chunk_header: [24]u8 = undefined;
        const chunk = try std.fmt.bufPrint(&chunk_header, "{x}\r\n", .{body.len});
        try stream.writeAll(chunk);
        try stream.writeAll(body);
        try stream.writeAll("\r\n");
    }
};

test "remote provider client posts an OAP envelope and receives a unary answer" {
    const address = try compat.net.resolveAddress(std.testing.allocator, "127.0.0.1", 0);
    var server = MockUnaryServer{ .server = try compat.net.tcpListen(address, .{ .reuse_address = true }) };
    try server.start();
    defer server.stop();
    const url = try std.fmt.allocPrint(std.testing.allocator, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&server.server).getPort()});
    defer std.testing.allocator.free(url);
    var client = try Client.init(std.testing.allocator, url, .loopback);
    defer client.deinit();
    const answer = try client.postUnary("{\"type\":\"provider.describe.request\"}");
    defer std.testing.allocator.free(answer);
    try std.testing.expect(std.mem.indexOf(u8, answer, "provider.describe.response") != null);
    try std.testing.expect(server.saw_provider_path.load(.acquire));
}

const FrameCollector = struct {
    count: usize = 0,
    first: ?[]u8 = null,

    fn collect(context: ?*anyopaque, line: []const u8) !void {
        const self: *FrameCollector = @ptrCast(@alignCast(context));
        self.count += 1;
        if (self.first == null) self.first = try std.testing.allocator.dupe(u8, line);
    }
};

test "remote provider client consumes SSE envelopes" {
    const address = try compat.net.resolveAddress(std.testing.allocator, "127.0.0.1", 0);
    var server = MockUnaryServer{
        .server = try compat.net.tcpListen(address, .{ .reuse_address = true }),
        .response_type = "text/event-stream",
        .response_body = "data: {\"type\":\"inference.create.response\"}\n\ndata: {\"type\":\"inference.completed\"}\n\n",
    };
    try server.start();
    defer server.stop();
    const url = try std.fmt.allocPrint(std.testing.allocator, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&server.server).getPort()});
    defer std.testing.allocator.free(url);
    var client = try Client.init(std.testing.allocator, url, .loopback);
    defer client.deinit();
    var collector = FrameCollector{};
    defer if (collector.first) |line| std.testing.allocator.free(line);
    try client.postStream("{\"type\":\"inference.create.request\"}", &collector, FrameCollector.collect);
    try std.testing.expectEqual(@as(usize, 2), collector.count);
    try std.testing.expectEqualStrings("{\"type\":\"inference.create.response\"}", collector.first.?);
}

test "remote provider unary request times out on a silent response head" {
    const address = try compat.net.resolveAddress(std.testing.allocator, "127.0.0.1", 0);
    var server = MockUnaryServer{
        .server = try compat.net.tcpListen(address, .{ .reuse_address = true }),
        .response_delay_ms = 100,
    };
    try server.start();
    defer server.stop();
    const url = try std.fmt.allocPrint(std.testing.allocator, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&server.server).getPort()});
    defer std.testing.allocator.free(url);
    var client = try Client.init(std.testing.allocator, url, .loopback);
    defer client.deinit();
    client.request_timeout_ms = 10;
    try std.testing.expectError(error.ProviderServiceTimeout, client.postUnary("{}"));
}

test "remote provider streaming request times out after SSE becomes idle" {
    const address = try compat.net.resolveAddress(std.testing.allocator, "127.0.0.1", 0);
    var server = MockUnaryServer{
        .server = try compat.net.tcpListen(address, .{ .reuse_address = true }),
        .response_type = "text/event-stream",
        .response_body = "data: {\"type\":\"inference.create.response\"}\n\n",
        .hold_open_ms = 500,
    };
    try server.start();
    defer server.stop();
    const url = try std.fmt.allocPrint(std.testing.allocator, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&server.server).getPort()});
    defer std.testing.allocator.free(url);
    var client = try Client.init(std.testing.allocator, url, .loopback);
    defer client.deinit();
    client.request_timeout_ms = 500;
    client.stream_idle_timeout_ms = 50;
    var collector = FrameCollector{};
    defer if (collector.first) |line| std.testing.allocator.free(line);
    try std.testing.expectError(error.ProviderServiceTimeout, client.postStream("{}", &collector, FrameCollector.collect));
    try std.testing.expectEqual(@as(usize, 1), collector.count);
}

test "SSE heartbeat resets the remote provider idle deadline" {
    const address = try compat.net.resolveAddress(std.testing.allocator, "127.0.0.1", 0);
    var server = MockUnaryServer{
        .server = try compat.net.tcpListen(address, .{ .reuse_address = true }),
        .response_type = "text/event-stream",
        .response_body = "data: {\"type\":\"inference.create.response\"}\n\n",
        .middle_delay_ms = 40,
        .middle_body = ": keepalive\n\n",
        .tail_delay_ms = 40,
        .tail_body = "data: {\"type\":\"inference.completed\"}\n\n",
    };
    try server.start();
    defer server.stop();
    const url = try std.fmt.allocPrint(std.testing.allocator, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&server.server).getPort()});
    defer std.testing.allocator.free(url);
    var client = try Client.init(std.testing.allocator, url, .loopback);
    defer client.deinit();
    client.request_timeout_ms = 100;
    client.stream_idle_timeout_ms = 60;
    var collector = FrameCollector{};
    defer if (collector.first) |line| std.testing.allocator.free(line);
    try client.postStream("{}", &collector, FrameCollector.collect);
    try std.testing.expectEqual(@as(usize, 2), collector.count);
}
