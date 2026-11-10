const std = @import("std");
const Client = @import("http_client");

fn workerEnviron() std.process.Environ {
    const builtin = @import("builtin");
    if (builtin.is_test) return std.testing.environ;

    const Block = std.process.Environ.Block;
    if (@hasField(Block, "use_global")) return .{ .block = .global };
    if (!builtin.link_libc) return .empty;

    const c_environ = std.c.environ;
    var env_count: usize = 0;
    while (c_environ[env_count] != null) : (env_count += 1) {}
    return .{ .block = .{ .slice = @ptrCast(c_environ[0..env_count :null]) } };
}

fn defaultIo() std.Io {
    return if (@import("builtin").is_test)
        std.testing.io
    else
        std.Io.Threaded.global_single_threaded.io();
}

pub const Method = std.http.Method;
pub const Headers = Client.Request.Headers;
pub const Request = Client.Request;
pub const Response = Client.Response;
pub const RequestOptions = struct {
    extra_headers: []const std.http.Header = &.{},
    keep_alive: bool = true,
    accept_encoding: ?[]const u8 = null,
    user_agent: ?[]const u8 = null,
};

pub const HttpClient = struct {
    client: Client,

    pub fn init(allocator: std.mem.Allocator) HttpClient {
        return .{ .client = .{ .allocator = allocator, .io = defaultIo() } };
    }

    pub fn deinit(self: *HttpClient) void {
        self.client.deinit();
        self.* = undefined;
    }

    pub fn openRequest(self: *HttpClient, method: Method, uri: std.Uri, options: RequestOptions) !Request {
        return self.client.request(method, uri, .{
            .extra_headers = options.extra_headers,
            .keep_alive = options.keep_alive,
            .headers = .{
                .accept_encoding = acceptEncoding(options.accept_encoding),
                .user_agent = if (options.user_agent) |value| .{ .override = value } else .default,
            },
        });
    }

    pub fn initDefaultProxies(self: *HttpClient, allocator: std.mem.Allocator, environ_map: *std.process.Environ.Map) !void {
        try self.client.initDefaultProxies(allocator, environ_map);
    }
};

const shared_idle_connections = 1024;
const stale_sweep_interval_ms = 1000;

var shared_lock: std.atomic.Mutex = .unlocked;
var shared_client: ?HttpClient = null;
var last_stale_sweep_ms: i64 = 0;

pub fn sharedClient() *HttpClient {
    while (!shared_lock.tryLock()) std.atomic.spinLoopHint();
    defer shared_lock.unlock();
    if (shared_client == null) {
        shared_client = HttpClient.init(std.heap.smp_allocator);
        shared_client.?.client.connection_pool.free_size = shared_idle_connections;
    }
    const client = &shared_client.?;
    const now = std.Io.Timestamp.now(defaultIo(), .boot).toMilliseconds();
    if (now - last_stale_sweep_ms >= stale_sweep_interval_ms) {
        last_stale_sweep_ms = now;
        dropClosedConnections(client);
    }
    return client;
}

fn dropClosedConnections(client: *HttpClient) void {
    if (@import("builtin").os.tag == .windows) return;
    const io = defaultIo();
    const pool = &client.client.connection_pool;
    pool.mutex.lockUncancelable(io);
    defer pool.mutex.unlock(io);
    var next = pool.free.first;
    while (next) |node| {
        next = node.next;
        const connection: *Client.Connection = @alignCast(@fieldParentPtr("pool_node", node));
        var watched = [_]std.posix.pollfd{.{ .fd = connection.stream_reader.stream.socket.handle, .events = std.posix.POLL.IN, .revents = 0 }};
        const ready = std.posix.poll(&watched, 0) catch 1;
        if (ready == 0) continue;
        pool.free.remove(node);
        pool.free_len -= 1;
        connection.destroy(io);
    }
}

pub fn sendRequest(request: *Request, body: []const u8) !void {
    request.transfer_encoding = .{ .content_length = body.len };
    var sent = try request.sendBodyUnflushed(&.{});
    try writeBody(&sent, body);
    try request.connection.?.flush();
}

fn writeBody(sent: *std.http.BodyWriter, body: []const u8) std.http.BodyWriter.Error!void {
    try sent.writer.writeAll(body);
    try sent.end();
}

const TricklingOutput = struct {
    received: std.ArrayList(u8) = .empty,
    writer: std.Io.Writer = .{ .buffer = &.{}, .vtable = &.{ .drain = drain } },

    const most_per_write = 7;

    fn drain(w: *std.Io.Writer, data: []const []const u8, splat: usize) std.Io.Writer.Error!usize {
        const self: *TricklingOutput = @alignCast(@fieldParentPtr("writer", w));
        for (data, 0..) |chunk, index| {
            if (index == data.len - 1 and splat == 0) break;
            if (chunk.len == 0) continue;
            const n = @min(chunk.len, most_per_write);
            self.received.appendSlice(std.testing.allocator, chunk[0..n]) catch return error.WriteFailed;
            return n;
        }
        return 0;
    }

    fn bodyWriter(self: *TricklingOutput, buffer: []u8, length: usize) std.http.BodyWriter {
        return .{
            .http_protocol_output = &self.writer,
            .state = .{ .content_length = length },
            .writer = .{ .buffer = buffer, .vtable = &.{ .drain = std.http.BodyWriter.contentLengthDrain, .sendFile = std.http.BodyWriter.contentLengthSendFile } },
        };
    }
};

test "a body sent a few bytes at a time stays intact for a retry, which the body as its own send buffer does not" {
    const original = "{\"model\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"hello there\"}]}";
    const body = try std.testing.allocator.dupe(u8, original);
    defer std.testing.allocator.free(body);

    var output: TricklingOutput = .{};
    defer output.received.deinit(std.testing.allocator);
    var sent = output.bodyWriter(&.{}, body.len);
    try writeBody(&sent, body);
    try std.testing.expectEqualStrings(original, output.received.items);
    try std.testing.expectEqualStrings(original, body);

    var buffered: TricklingOutput = .{};
    defer buffered.received.deinit(std.testing.allocator);
    var in_place = buffered.bodyWriter(body, body.len);
    in_place.writer.end = body.len;
    try in_place.end();
    try std.testing.expectEqualStrings(original, buffered.received.items);
    try std.testing.expect(!std.mem.eql(u8, original, body));
}

pub fn sendBodilessRequest(request: *Request) !void {
    try request.sendBodiless();
}

pub fn receiveResponse(request: *Request, redirect_buffer: []u8) !Response {
    return request.receiveHead(redirect_buffer);
}

pub const ResponseReader = opaque {};

pub fn readResponse(reader: *ResponseReader, buffer: []u8) !usize {
    const inner: *std.Io.Reader = @ptrCast(@alignCast(reader));
    var slices = [_][]u8{buffer};
    while (true) {
        const read = inner.readVec(&slices) catch |err| switch (err) {
            error.EndOfStream => return 0,
            else => |failure| return failure,
        };
        if (read > 0 or buffer.len == 0) return read;
    }
}

pub fn readAllResponse(reader: *ResponseReader, buffer: []u8) !void {
    const inner: *std.Io.Reader = @ptrCast(@alignCast(reader));
    try inner.readSliceAll(buffer);
}

pub fn allocRemainingResponse(allocator: std.mem.Allocator, reader: *ResponseReader, max_bytes: usize) ![]u8 {
    const inner: *std.Io.Reader = @ptrCast(@alignCast(reader));
    return inner.allocRemaining(allocator, std.Io.Limit.limited(max_bytes));
}

pub fn responseReader(response: *Response, transfer_buf: []u8) *ResponseReader {
    return @ptrCast(@alignCast(response.reader(transfer_buf)));
}

pub fn headerPresent(headers: []const std.http.Header, name: []const u8) bool {
    for (headers) |header| {
        if (std.ascii.eqlIgnoreCase(header.name, name)) return true;
    }
    return false;
}

test "compat http header presence ignores case and reports absence" {
    const headers = [_]std.http.Header{
        .{ .name = "Authorization", .value = "Bearer x" },
        .{ .name = "content-type", .value = "application/json" },
    };
    try std.testing.expect(headerPresent(&headers, "authorization"));
    try std.testing.expect(headerPresent(&headers, "CONTENT-TYPE"));
    try std.testing.expect(!headerPresent(&headers, "x-tenant"));
    try std.testing.expect(!headerPresent(&.{}, "authorization"));
}

pub fn acceptEncoding(override: ?[]const u8) Headers.Value {
    return .{ .override = override orelse "identity" };
}

test "compat http requests identity encoding unless a caller overrides it" {
    switch (acceptEncoding(null)) {
        .override => |value| try std.testing.expectEqualStrings("identity", value),
        else => return error.TestUnexpectedResult,
    }
    switch (acceptEncoding("gzip")) {
        .override => |value| try std.testing.expectEqualStrings("gzip", value),
        else => return error.TestUnexpectedResult,
    }
}

const KeepAliveServer = struct {
    listener: std.Io.net.Server,
    accepted: std.atomic.Value(u32) = std.atomic.Value(u32).init(0),
    close_after_reply: bool = false,

    fn port(self: *const KeepAliveServer) u16 {
        return self.listener.socket.address.getPort();
    }

    fn serve(self: *KeepAliveServer, requests: usize) void {
        const io = std.testing.io;
        var served: usize = 0;
        while (served < requests) {
            var stream = self.listener.accept(io) catch return;
            defer stream.close(io);
            _ = self.accepted.fetchAdd(1, .acq_rel);
            var read_buffer: [4096]u8 = undefined;
            var reader = stream.reader(io, &read_buffer);
            var write_buffer: [256]u8 = undefined;
            var writer = stream.writer(io, &write_buffer);
            while (served < requests) {
                _ = reader.interface.takeDelimiterInclusive('\n') catch break;
                while (true) {
                    const line = reader.interface.takeDelimiterInclusive('\n') catch return;
                    if (std.mem.eql(u8, line, "\r\n")) break;
                }
                writer.interface.writeAll("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok") catch return;
                writer.interface.flush() catch return;
                served += 1;
                if (self.close_after_reply) break;
            }
        }
    }
};

fn releaseServer(port: u16) void {
    const address = std.Io.net.IpAddress.parse("127.0.0.1", port) catch return;
    var stream = address.connect(std.testing.io, .{ .mode = .stream }) catch return;
    defer stream.close(std.testing.io);
    var buffer: [64]u8 = undefined;
    var writer = stream.writer(std.testing.io, &buffer);
    writer.interface.writeAll("GET / HTTP/1.1\r\n\r\n") catch return;
    writer.interface.flush() catch return;
}

fn fetchThroughShared(port: u16) !void {
    var url_buffer: [64]u8 = undefined;
    const url = try std.fmt.bufPrint(&url_buffer, "http://127.0.0.1:{d}/", .{port});
    var request = try sharedClient().openRequest(.GET, try std.Uri.parse(url), .{});
    defer request.deinit();
    try sendBodilessRequest(&request);
    var redirect_buffer: [256]u8 = undefined;
    var response = try receiveResponse(&request, &redirect_buffer);
    var transfer_buffer: [64]u8 = undefined;
    const reader = responseReader(&response, &transfer_buffer);
    var body: [8]u8 = undefined;
    while (try readResponse(reader, &body) > 0) {}
}

test "the shared client reuses one connection across requests" {
    const address = try std.Io.net.IpAddress.parse("127.0.0.1", 0);
    var server = KeepAliveServer{ .listener = try address.listen(std.testing.io, .{ .reuse_address = true }) };
    defer server.listener.deinit(std.testing.io);
    const thread = try std.Thread.spawn(.{}, KeepAliveServer.serve, .{ &server, 3 });
    defer thread.join();
    for (0..3) |_| try fetchThroughShared(server.port());
    try std.testing.expectEqual(@as(u32, 1), server.accepted.load(.acquire));
}

test "the shared client drops an idle connection the server closed" {
    const address = try std.Io.net.IpAddress.parse("127.0.0.1", 0);
    var server = KeepAliveServer{ .listener = try address.listen(std.testing.io, .{ .reuse_address = true }), .close_after_reply = true };
    defer server.listener.deinit(std.testing.io);
    const thread = try std.Thread.spawn(.{}, KeepAliveServer.serve, .{ &server, 2 });
    defer thread.join();
    errdefer releaseServer(server.port());
    try fetchThroughShared(server.port());
    std.Io.sleep(std.testing.io, .fromMilliseconds(50), .boot) catch {};
    last_stale_sweep_ms = 0;
    try fetchThroughShared(server.port());
    try std.testing.expectEqual(@as(u32, 2), server.accepted.load(.acquire));
}

test "compat http client initializes and deinitializes" {
    var client = HttpClient.init(std.testing.allocator);
    client.deinit();
}

test "compat http request options default to no extra headers" {
    const options = RequestOptions{};
    try std.testing.expectEqual(@as(usize, 0), options.extra_headers.len);
    try std.testing.expect(options.keep_alive);
    try std.testing.expect(options.accept_encoding == null);
}

test "compat http request options can override accept encoding" {
    const options = RequestOptions{ .accept_encoding = "identity" };
    try std.testing.expectEqualStrings("identity", options.accept_encoding.?);
}

pub const default_fetch_timeout_ms: u64 = 30_000;

pub const FetchError = error{
    Timeout,
    RequestFailed,
    OutOfMemory,
};

pub const FetchOptions = struct {
    method: Method = .GET,
    extra_headers: []const std.http.Header = &.{},
    body: ?[]const u8 = null,
    accept_encoding: ?[]const u8 = null,
    max_response_bytes: usize = 8 * 1024 * 1024,
    timeout_ms: u64 = default_fetch_timeout_ms,
};

pub const Fetched = struct {
    status: u16,
    body: []u8,

    pub fn deinit(self: *Fetched, allocator: std.mem.Allocator) void {
        allocator.free(self.body);
        self.* = undefined;
    }
};

const FetchJob = struct {
    refs: std.atomic.Value(u32),
    event: std.Io.Event,
    succeeded: bool,
    status: u16,
    body: []u8,

    url: []u8,
    method: Method,
    headers: []std.http.Header,
    request_body: ?[]u8,
    accept_encoding: ?[]u8,
    max_response_bytes: usize,

    const arena = std.heap.page_allocator;

    fn create(url: []const u8, options: FetchOptions) error{OutOfMemory}!*FetchJob {
        const job = try arena.create(FetchJob);
        errdefer arena.destroy(job);

        const url_copy = try arena.dupe(u8, url);
        errdefer arena.free(url_copy);

        const headers = try arena.alloc(std.http.Header, options.extra_headers.len);
        var filled: usize = 0;
        errdefer {
            for (headers[0..filled]) |header| {
                arena.free(header.name);
                arena.free(header.value);
            }
            arena.free(headers);
        }
        for (options.extra_headers, 0..) |header, i| {
            const name = try arena.dupe(u8, header.name);
            errdefer arena.free(name);
            const value = try arena.dupe(u8, header.value);
            headers[i] = .{ .name = name, .value = value };
            filled = i + 1;
        }

        const request_body = if (options.body) |value| try arena.dupe(u8, value) else null;
        errdefer if (request_body) |value| arena.free(value);

        const accept_encoding = if (options.accept_encoding) |value| try arena.dupe(u8, value) else null;

        job.* = .{
            .refs = std.atomic.Value(u32).init(2),
            .event = .unset,
            .succeeded = false,
            .status = 0,
            .body = &.{},
            .url = url_copy,
            .method = options.method,
            .headers = headers,
            .request_body = request_body,
            .accept_encoding = accept_encoding,
            .max_response_bytes = options.max_response_bytes,
        };
        return job;
    }

    fn release(job: *FetchJob) void {
        if (job.refs.fetchSub(1, .acq_rel) != 1) return;
        if (job.body.len > 0) arena.free(job.body);
        arena.free(job.url);
        for (job.headers) |header| {
            arena.free(header.name);
            arena.free(header.value);
        }
        arena.free(job.headers);
        if (job.request_body) |value| arena.free(value);
        if (job.accept_encoding) |value| arena.free(value);
        arena.destroy(job);
    }

    fn run(job: *FetchJob) void {
        defer job.release();

        var status: u16 = 0;
        var body: []u8 = &.{};
        const ok = job.perform(&status, &body);

        job.succeeded = ok;
        job.status = status;
        job.body = body;
        job.event.set(defaultIo());
    }

    fn perform(job: *FetchJob, status_out: *u16, body_out: *[]u8) bool {
        const uri = std.Uri.parse(job.url) catch return false;

        var client = HttpClient.init(arena);
        defer client.deinit();

        var environ_map = std.process.Environ.createMap(workerEnviron(), arena) catch null;
        defer if (environ_map) |*map| map.deinit();
        if (environ_map) |*map| client.initDefaultProxies(arena, map) catch {};

        var request = client.openRequest(job.method, uri, .{
            .extra_headers = job.headers,
            .keep_alive = false,
            .accept_encoding = job.accept_encoding,
        }) catch return false;
        defer request.deinit();

        if (job.request_body) |value| {
            sendRequest(&request, value) catch return false;
        } else {
            sendBodilessRequest(&request) catch return false;
        }

        var redirect_buf: [8192]u8 = undefined;
        var response = receiveResponse(&request, &redirect_buf) catch return false;
        status_out.* = @intFromEnum(response.head.status);

        var transfer_buf: [8192]u8 = undefined;
        const reader = responseReader(&response, &transfer_buf);
        body_out.* = allocRemainingResponse(arena, reader, job.max_response_bytes) catch return false;
        return true;
    }
};

pub fn fetch(allocator: std.mem.Allocator, url: []const u8, options: FetchOptions) FetchError!Fetched {
    const job = try FetchJob.create(url, options);

    const thread = std.Thread.spawn(.{}, FetchJob.run, .{job}) catch {
        job.release();
        job.release();
        return error.RequestFailed;
    };
    thread.detach();

    const io = defaultIo();
    const timeout: std.Io.Timeout = .{ .duration = .{
        .raw = .{ .nanoseconds = @as(i96, @intCast(options.timeout_ms)) * 1_000_000 },
        .clock = .awake,
    } };

    job.event.waitTimeout(io, timeout) catch {
        job.release();
        return error.Timeout;
    };

    defer job.release();
    if (!job.succeeded) return error.RequestFailed;
    const body = allocator.dupe(u8, job.body) catch return error.OutOfMemory;
    return .{ .status = job.status, .body = body };
}

test "fetch reports a url it cannot parse as a failed request" {
    try std.testing.expectError(
        error.RequestFailed,
        fetch(std.testing.allocator, "not a url at all", .{ .timeout_ms = 2_000 }),
    );
}

test "fetch reports a refused connection rather than hanging" {
    try std.testing.expectError(
        error.RequestFailed,
        fetch(std.testing.allocator, "http://127.0.0.1:1/nothing", .{ .timeout_ms = 5_000 }),
    );
}

test "fetch options default to a bounded timeout" {
    const options = FetchOptions{};
    try std.testing.expectEqual(default_fetch_timeout_ms, options.timeout_ms);
    try std.testing.expect(options.timeout_ms > 0);
}

const BufferFillingReader = struct {
    interface: std.Io.Reader,
    pending: []const u8,

    fn init(storage: []u8, pending: []const u8) BufferFillingReader {
        return .{
            .interface = .{
                .vtable = &.{ .stream = stream, .readVec = readVec },
                .buffer = storage,
                .seek = 0,
                .end = 0,
            },
            .pending = pending,
        };
    }

    fn stream(_: *std.Io.Reader, _: *std.Io.Writer, _: std.Io.Limit) std.Io.Reader.StreamError!usize {
        return error.EndOfStream;
    }

    fn readVec(r: *std.Io.Reader, _: [][]u8) std.Io.Reader.Error!usize {
        const self: *BufferFillingReader = @fieldParentPtr("interface", r);
        if (self.pending.len == 0) return error.EndOfStream;
        const n = @min(self.pending.len, r.buffer.len - r.end);
        @memcpy(r.buffer[r.end..][0..n], self.pending[0..n]);
        r.end += n;
        self.pending = self.pending[n..];
        return 0;
    }
};

test "readResponse keeps reading when a fill lands in the reader's buffer" {
    var storage: [16]u8 = undefined;
    var source = BufferFillingReader.init(&storage, "pong");
    const reader: *ResponseReader = @ptrCast(&source.interface);
    var out: [8]u8 = undefined;
    const read = try readResponse(reader, &out);
    try std.testing.expectEqualStrings("pong", out[0..read]);
    try std.testing.expectEqual(@as(usize, 0), try readResponse(reader, &out));
}
