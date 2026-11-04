const std = @import("std");
const c = std.c;
const tls = std.crypto.tls;

const gpa = std.heap.smp_allocator;

fn fullRecord(bytes: []const u8) bool {
    if (bytes.len < 5) return false;
    const size = std.mem.readInt(u16, bytes[3..5], .big);
    return bytes.len >= 5 + @as(usize, size);
}

pub fn main() !void {
    const port = try std.fmt.parseInt(u16, std.mem.span(c.getenv("SPIKE_PORT").?), 10);
    var threaded = std.Io.Threaded.init(gpa, .{});
    const io = threaded.io();
    const address = try std.Io.net.IpAddress.parse("127.0.0.1", port);
    const stream = try address.connect(io, .{ .mode = .stream });
    const fd = stream.socket.handle;

    const read_buffer = try gpa.alloc(u8, tls.Client.min_buffer_len * 2);
    const write_buffer = try gpa.alloc(u8, tls.Client.min_buffer_len * 2);
    const client_read = try gpa.alloc(u8, tls.Client.min_buffer_len * 2);
    const client_write = try gpa.alloc(u8, tls.Client.min_buffer_len * 2);
    var socket_reader = stream.reader(io, read_buffer);
    var socket_writer = stream.writer(io, write_buffer);
    var entropy: [tls.Client.Options.entropy_len]u8 = undefined;
    io.random(&entropy);
    var client = try tls.Client.init(&socket_reader.interface, &socket_writer.interface, .{
        .host = .no_verification,
        .ca = .no_verification,
        .read_buffer = client_read,
        .write_buffer = client_write,
        .entropy = &entropy,
        .realtime_now = std.Io.Timestamp.now(io, .real),
    });
    try client.writer.writeAll("GET / HTTP/1.0\r\nHost: localhost\r\n\r\n");
    try client.writer.flush();
    try socket_writer.interface.flush();

    _ = c.fcntl(fd, c.F.SETFL, @as(c_int, 0x0004));

    const raw_bytes = try gpa.alloc(u8, tls.Client.min_buffer_len * 2);
    var raw: std.Io.Reader = .fixed(raw_bytes);
    raw.end = 0;
    const carried = socket_reader.interface.buffered();
    @memcpy(raw_bytes[0..carried.len], carried);
    raw.end = carried.len;
    client.input = &raw;

    var body: std.ArrayList(u8) = .empty;
    var would_block: usize = 0;
    var partial_reads: usize = 0;
    var records: usize = 0;
    var done = false;
    while (!done) {
        while (fullRecord(raw.buffered())) {
            const before = raw.buffered().len;
            client.reader.fillMore() catch |err| switch (err) {
                error.EndOfStream => {
                    done = true;
                    break;
                },
                else => return err,
            };
            if (raw.buffered().len != before) records += 1;
            const clear = client.reader.buffered();
            try body.appendSlice(gpa, clear);
            client.reader.toss(clear.len);
        }
        if (done) break;
        if (raw.seek > 0) {
            const rest = raw.buffered();
            std.mem.copyForwards(u8, raw_bytes[0..rest.len], rest);
            raw.end = rest.len;
            raw.seek = 0;
        }
        var one: [1]u8 = undefined;
        const n = c.read(fd, &one, 1);
        if (n < 0) {
            if (std.c._errno().* != @intFromEnum(c.E.AGAIN)) return error.ReadFailed;
            would_block += 1;
            var pollfd = [_]c.pollfd{.{ .fd = fd, .events = c.POLL.IN, .revents = 0 }};
            _ = c.poll(&pollfd, 1, 2000);
            continue;
        }
        if (n == 0) break;
        raw_bytes[raw.end] = one[0];
        raw.end += 1;
        if (!fullRecord(raw.buffered())) partial_reads += 1;
    }
    const version = @tagName(client.tls_version);
    std.debug.print("tls={s} records={d} body_bytes={d} would_block={d} partial_reads={d} http_ok={}\n", .{ version, records, body.items.len, would_block, partial_reads, std.mem.startsWith(u8, body.items, "HTTP/1.0 200") });
}
