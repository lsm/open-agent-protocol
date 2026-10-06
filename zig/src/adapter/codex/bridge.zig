const std = @import("std");
const compat = @import("compat");

pub const Error = error{
    HandshakeRefused,
    HandshakeTooLarge,
    FrameTooLarge,
    InvalidFrame,
    UnsupportedPlatform,
};

pub const frame_limit: usize = 64 << 20;
const head_limit: usize = 16 * 1024;
const chunk_bytes: usize = 64 * 1024;

pub const Opcode = enum(u4) {
    continuation = 0,
    text = 1,
    binary = 2,
    close = 8,
    ping = 9,
    pong = 10,
    _,
};

pub fn handshakeRequest(arena: std.mem.Allocator, key: []const u8) ![]u8 {
    return std.fmt.allocPrint(arena, "GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {s}\r\nSec-WebSocket-Version: 13\r\n\r\n", .{key});
}

pub fn headEnd(bytes: []const u8) ?usize {
    const at = std.mem.indexOf(u8, bytes, "\r\n\r\n") orelse return null;
    return at + 4;
}

pub fn acceptsUpgrade(head: []const u8) bool {
    const line_end = std.mem.indexOf(u8, head, "\r\n") orelse head.len;
    var parts = std.mem.tokenizeScalar(u8, head[0..line_end], ' ');
    _ = parts.next() orelse return false;
    const status = parts.next() orelse return false;
    return std.mem.eql(u8, status, "101");
}

pub fn encodeFrame(arena: std.mem.Allocator, opcode: Opcode, payload: []const u8, mask: [4]u8) ![]u8 {
    const extended: usize = if (payload.len < 126) 0 else if (payload.len <= std.math.maxInt(u16)) 2 else 8;
    const out = try arena.alloc(u8, 2 + extended + 4 + payload.len);
    out[0] = 0x80 | @as(u8, @intFromEnum(opcode));
    switch (extended) {
        0 => out[1] = 0x80 | @as(u8, @intCast(payload.len)),
        2 => {
            out[1] = 0x80 | 126;
            std.mem.writeInt(u16, out[2..4], @intCast(payload.len), .big);
        },
        else => {
            out[1] = 0x80 | 127;
            std.mem.writeInt(u64, out[2..10], payload.len, .big);
        },
    }
    const key_at = 2 + extended;
    @memcpy(out[key_at .. key_at + 4], &mask);
    for (payload, out[key_at + 4 ..], 0..) |byte, *slot, index| slot.* = byte ^ mask[index % 4];
    return out;
}

pub const Frame = struct {
    opcode: Opcode,
    payload: []const u8,
};

pub const Decoder = struct {
    gpa: std.mem.Allocator,
    pending: std.ArrayList(u8) = .empty,
    message: std.ArrayList(u8) = .empty,
    message_opcode: Opcode = .continuation,
    taken: usize = 0,

    pub fn deinit(self: *Decoder) void {
        self.pending.deinit(self.gpa);
        self.message.deinit(self.gpa);
    }

    pub fn feed(self: *Decoder, bytes: []const u8) !void {
        if (self.taken > 0) {
            self.pending.replaceRangeAssumeCapacity(0, self.taken, &.{});
            self.taken = 0;
        }
        try self.pending.appendSlice(self.gpa, bytes);
    }

    pub fn next(self: *Decoder) !?Frame {
        while (true) {
            const rest = self.pending.items[self.taken..];
            if (rest.len < 2) return null;
            const fin = rest[0] & 0x80 != 0;
            const opcode: Opcode = @enumFromInt(@as(u4, @truncate(rest[0])));
            const masked = rest[1] & 0x80 != 0;
            var length: u64 = rest[1] & 0x7f;
            var at: usize = 2;
            if (length == 126) {
                if (rest.len < 4) return null;
                length = std.mem.readInt(u16, rest[2..4], .big);
                at = 4;
            } else if (length == 127) {
                if (rest.len < 10) return null;
                length = std.mem.readInt(u64, rest[2..10], .big);
                at = 10;
            }
            if (length > frame_limit) return Error.FrameTooLarge;
            var mask: [4]u8 = .{ 0, 0, 0, 0 };
            if (masked) {
                if (rest.len < at + 4) return null;
                @memcpy(&mask, rest[at .. at + 4]);
                at += 4;
            }
            const size: usize = @intCast(length);
            if (rest.len < at + size) return null;
            const payload = rest[at .. at + size];
            if (masked) for (payload, 0..) |*byte, index| {
                byte.* ^= mask[index % 4];
            };
            self.taken += at + size;

            switch (opcode) {
                .close, .ping, .pong => return .{ .opcode = opcode, .payload = payload },
                .text, .binary => {
                    if (self.message_opcode != .continuation) return Error.InvalidFrame;
                    if (fin) return .{ .opcode = opcode, .payload = payload };
                    self.message_opcode = opcode;
                    try self.message.appendSlice(self.gpa, payload);
                },
                .continuation => {
                    if (self.message_opcode == .continuation) return Error.InvalidFrame;
                    if (self.message.items.len + payload.len > frame_limit) return Error.FrameTooLarge;
                    try self.message.appendSlice(self.gpa, payload);
                    if (fin) {
                        const whole = self.message.items;
                        const kind = self.message_opcode;
                        self.message_opcode = .continuation;
                        self.message.items.len = 0;
                        return .{ .opcode = kind, .payload = whole };
                    }
                },
                _ => return Error.InvalidFrame,
            }
        }
    }
};

fn newMask() [4]u8 {
    var mask: [4]u8 = undefined;
    compat.random.fillSecureBytes(&mask);
    return mask;
}

fn writeAllFd(handle: std.posix.fd_t, bytes: []const u8) !void {
    var rest = bytes;
    while (rest.len > 0) {
        const wrote = std.posix.system.write(handle, rest.ptr, rest.len);
        switch (std.posix.errno(wrote)) {
            .SUCCESS => rest = rest[@intCast(wrote)..],
            .INTR, .AGAIN => continue,
            else => return error.BrokenPipe,
        }
    }
}

fn readFd(handle: std.posix.fd_t, into: []u8) !usize {
    while (true) {
        const got = std.posix.system.read(handle, into.ptr, into.len);
        switch (std.posix.errno(got)) {
            .SUCCESS => return @intCast(got),
            .INTR, .AGAIN => continue,
            else => return error.InputOutput,
        }
    }
}

pub fn run(gpa: std.mem.Allocator, socket_path: []const u8, input: std.posix.fd_t, output: std.posix.fd_t) !void {
    if (!compat.net.supports_unix_channels) return Error.UnsupportedPlatform;
    var stream = try compat.net.unixConnect(socket_path);
    defer stream.close();
    const socket = compat.net.streamHandle(&stream);

    var key_bytes: [16]u8 = undefined;
    compat.random.fillSecureBytes(&key_bytes);
    var key: [24]u8 = undefined;
    _ = std.base64.standard.Encoder.encode(&key, &key_bytes);

    var scratch = std.heap.ArenaAllocator.init(gpa);
    defer scratch.deinit();
    try stream.writeAll(try handshakeRequest(scratch.allocator(), &key));

    var decoder = Decoder{ .gpa = gpa };
    defer decoder.deinit();
    var head: std.ArrayList(u8) = .empty;
    defer head.deinit(gpa);
    var chunk: [chunk_bytes]u8 = undefined;
    while (headEnd(head.items) == null) {
        if (head.items.len > head_limit) return Error.HandshakeTooLarge;
        const got = try stream.readSome(&chunk);
        if (got == 0) return Error.HandshakeRefused;
        try head.appendSlice(gpa, chunk[0..got]);
    }
    const end = headEnd(head.items).?;
    if (!acceptsUpgrade(head.items[0..end])) return Error.HandshakeRefused;
    try decoder.feed(head.items[end..]);

    var line: std.ArrayList(u8) = .empty;
    defer line.deinit(gpa);
    var input_open = true;
    while (true) {
        while (try decoder.next()) |frame| switch (frame.opcode) {
            .text, .binary => {
                try writeAllFd(output, frame.payload);
                try writeAllFd(output, "\n");
            },
            .ping => {
                _ = scratch.reset(.retain_capacity);
                try stream.writeAll(try encodeFrame(scratch.allocator(), .pong, frame.payload, newMask()));
            },
            .close => return,
            else => {},
        };

        var fds = [_]std.posix.pollfd{
            .{ .fd = socket, .events = std.posix.POLL.IN, .revents = 0 },
            .{ .fd = input, .events = if (input_open) std.posix.POLL.IN else 0, .revents = 0 },
        };
        _ = try std.posix.poll(&fds, -1);

        if (fds[0].revents != 0) {
            const got = try stream.readSome(&chunk);
            if (got == 0) return;
            try decoder.feed(chunk[0..got]);
        }
        if (input_open and fds[1].revents != 0) {
            const got = try readFd(input, &chunk);
            if (got == 0) {
                input_open = false;
                _ = scratch.reset(.retain_capacity);
                try stream.writeAll(try encodeFrame(scratch.allocator(), .close, &.{}, newMask()));
                continue;
            }
            try line.appendSlice(gpa, chunk[0..got]);
            while (std.mem.indexOfScalar(u8, line.items, '\n')) |break_at| {
                if (break_at > 0) {
                    _ = scratch.reset(.retain_capacity);
                    try stream.writeAll(try encodeFrame(scratch.allocator(), .text, line.items[0..break_at], newMask()));
                }
                line.replaceRangeAssumeCapacity(0, break_at + 1, &.{});
            }
            if (line.items.len > frame_limit) return Error.FrameTooLarge;
        }
    }
}

test "a client frame is masked and its length takes the shortest form" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const mask = [4]u8{ 1, 2, 3, 4 };
    const short = try encodeFrame(arena.allocator(), .text, "hi", mask);
    try std.testing.expectEqualSlices(u8, &.{ 0x81, 0x82, 1, 2, 3, 4, 'h' ^ 1, 'i' ^ 2 }, short);

    const medium = try encodeFrame(arena.allocator(), .text, &([_]u8{'a'} ** 200), mask);
    try std.testing.expectEqual(@as(u8, 0x80 | 126), medium[1]);
    try std.testing.expectEqual(@as(u16, 200), std.mem.readInt(u16, medium[2..4], .big));
    try std.testing.expectEqual(@as(usize, 2 + 2 + 4 + 200), medium.len);
}

test "the decoder joins a fragmented message and passes control frames between its fragments" {
    var decoder = Decoder{ .gpa = std.testing.allocator };
    defer decoder.deinit();
    try decoder.feed(&.{ 0x01, 3, 'a', 'b', 'c' });
    try std.testing.expectEqual(@as(?Frame, null), try decoder.next());
    try decoder.feed(&.{ 0x89, 1, 'p' });
    const ping = (try decoder.next()).?;
    try std.testing.expectEqual(Opcode.ping, ping.opcode);
    try std.testing.expectEqualStrings("p", ping.payload);
    try decoder.feed(&.{ 0x80, 2, 'd', 'e' });
    const whole = (try decoder.next()).?;
    try std.testing.expectEqual(Opcode.text, whole.opcode);
    try std.testing.expectEqualStrings("abcde", whole.payload);
}

test "the decoder waits for a frame split across reads" {
    var decoder = Decoder{ .gpa = std.testing.allocator };
    defer decoder.deinit();
    const payload = [_]u8{'x'} ** 300;
    var framed: [4 + payload.len]u8 = undefined;
    framed[0] = 0x81;
    framed[1] = 126;
    std.mem.writeInt(u16, framed[2..4], payload.len, .big);
    @memcpy(framed[4..], &payload);
    try decoder.feed(framed[0..3]);
    try std.testing.expectEqual(@as(?Frame, null), try decoder.next());
    try decoder.feed(framed[3..]);
    const frame = (try decoder.next()).?;
    try std.testing.expectEqual(payload.len, frame.payload.len);
}

test "only a 101 status accepts the upgrade" {
    try std.testing.expect(acceptsUpgrade("HTTP/1.1 101 Switching Protocols\r\n\r\n"));
    try std.testing.expect(!acceptsUpgrade("HTTP/1.1 400 Bad Request\r\n\r\n"));
    try std.testing.expect(!acceptsUpgrade("garbage"));
}
