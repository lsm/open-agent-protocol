const std = @import("std");
const c = std.c;
const tls = std.crypto.tls;

const gpa = std.heap.smp_allocator;
const nonblock: c_int = @bitCast(c.O{ .NONBLOCK = true });

fn fullRecord(bytes: []const u8) bool {
    if (bytes.len < 5) return false;
    const size = std.mem.readInt(u16, bytes[3..5], .big);
    return bytes.len >= 5 + @as(usize, size);
}

fn sendAll(fd: c.fd_t, bytes: []const u8, would_block: *usize) !void {
    var sent: usize = 0;
    while (sent < bytes.len) {
        const n = c.write(fd, bytes[sent..].ptr, bytes.len - sent);
        if (n < 0) {
            if (std.c._errno().* != @intFromEnum(c.E.AGAIN)) return error.WriteFailed;
            would_block.* += 1;
            var pollfd = [_]c.pollfd{.{ .fd = fd, .events = c.POLL.OUT, .revents = 0 }};
            _ = c.poll(&pollfd, 1, 5000);
            continue;
        }
        sent += @intCast(n);
    }
}

fn env(name: [*:0]const u8, fallback: []const u8) []const u8 {
    return std.mem.span(c.getenv(name) orelse return fallback);
}

pub fn main() !void {
    const host = env("SPIKE_HOST", "api.openai.com");
    const path = env("SPIKE_PATH", "/v1/models");
    const feed = try std.fmt.parseInt(usize, env("SPIKE_FEED", "1"), 10);
    var threaded = std.Io.Threaded.init(gpa, .{});
    const io = threaded.io();
    const now = std.Io.Timestamp.now(io, .real);

    var bundle: std.crypto.Certificate.Bundle = .empty;
    try bundle.rescan(gpa, io, now);
    var lock: std.Io.RwLock = .init;

    const name = try std.Io.net.HostName.init(host);
    const stream = try name.connect(io, 443, .{ .mode = .stream });
    const fd = stream.socket.handle;

    const read_buffer = try gpa.alloc(u8, tls.Client.min_buffer_len * 2);
    const write_buffer = try gpa.alloc(u8, tls.Client.min_buffer_len * 2);
    const client_read = try gpa.alloc(u8, tls.Client.min_buffer_len * 2);
    const client_write = try gpa.alloc(u8, tls.Client.min_buffer_len * 2);
    var socket_reader = stream.reader(io, read_buffer);
    var socket_writer = stream.writer(io, write_buffer);
    var entropy: [tls.Client.Options.entropy_len]u8 = undefined;
    io.random(&entropy);
    var client = tls.Client.init(&socket_reader.interface, &socket_writer.interface, .{
        .host = .{ .explicit = host },
        .ca = .{ .bundle = .{ .gpa = gpa, .io = io, .lock = &lock, .bundle = &bundle } },
        .read_buffer = client_read,
        .write_buffer = client_write,
        .entropy = &entropy,
        .realtime_now = now,
    }) catch |err| {
        std.debug.print("host={s} refused={s}\n", .{ host, @errorName(err) });
        return;
    };
    try socket_writer.interface.flush();

    _ = c.fcntl(fd, c.F.SETFL, nonblock);

    const cipher_out = try gpa.alloc(u8, 65536);
    var sealed: std.Io.Writer = .fixed(cipher_out);
    client.output = &sealed;
    var would_block: usize = 0;
    const request = try std.fmt.allocPrint(gpa, "GET {s} HTTP/1.1\r\nHost: {s}\r\nUser-Agent: oap-tls-spike\r\nAccept: application/json\r\nConnection: close\r\n\r\n", .{ path, host });
    try client.writer.writeAll(request);
    try client.writer.flush();
    try sendAll(fd, sealed.buffered(), &would_block);
    _ = sealed.consumeAll();

    const raw_bytes = try gpa.alloc(u8, tls.Client.min_buffer_len * 4);
    var raw: std.Io.Reader = .fixed(raw_bytes);
    raw.end = 0;
    const carried = socket_reader.interface.buffered();
    @memcpy(raw_bytes[0..carried.len], carried);
    raw.end = carried.len;
    client.input = &raw;

    var body: std.ArrayList(u8) = .empty;
    var partial_reads: usize = 0;
    var records: usize = 0;
    var clean_end = false;
    var socket_ended = false;
    while (!clean_end) {
        while (fullRecord(raw.buffered())) {
            const before = raw.buffered().len;
            client.reader.fillMore() catch |err| switch (err) {
                error.EndOfStream => {
                    clean_end = true;
                    break;
                },
                else => {
                    std.debug.print("host={s} failed={s} records={d}\n", .{ host, @errorName(client.read_err orelse error.ReadFailed), records });
                    return;
                },
            };
            if (raw.buffered().len != before) records += 1;
            const clear = client.reader.buffered();
            try body.appendSlice(gpa, clear);
            client.reader.toss(clear.len);
            if (client.eof()) clean_end = true;
            if (sealed.buffered().len > 0) {
                try sendAll(fd, sealed.buffered(), &would_block);
                _ = sealed.consumeAll();
            }
        }
        if (clean_end or socket_ended) break;
        if (raw.seek > 0) {
            const rest = raw.buffered();
            std.mem.copyForwards(u8, raw_bytes[0..rest.len], rest);
            raw.end = rest.len;
            raw.seek = 0;
        }
        const room = raw_bytes[raw.end..];
        const n = c.read(fd, room.ptr, @min(room.len, feed));
        if (n < 0) {
            if (std.c._errno().* != @intFromEnum(c.E.AGAIN)) return error.ReadFailed;
            would_block += 1;
            var pollfd = [_]c.pollfd{.{ .fd = fd, .events = c.POLL.IN, .revents = 0 }};
            _ = c.poll(&pollfd, 1, 10000);
            continue;
        }
        if (n == 0) {
            socket_ended = true;
            continue;
        }
        raw.end += @intCast(n);
        if (!fullRecord(raw.buffered())) partial_reads += 1;
    }
    const first_line = body.items[0 .. std.mem.indexOfScalar(u8, body.items, '\r') orelse @min(body.items.len, 40)];
    std.debug.print("host={s} tls={s} verified=yes records={d} body_bytes={d} would_block={d} partial_reads={d} close_notify={} status=\"{s}\"\n", .{ host, @tagName(client.tls_version), records, body.items.len, would_block, partial_reads, clean_end, first_line });
}
