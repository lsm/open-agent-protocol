const std = @import("std");

pub const frame_limit_default: usize = 8 << 20;

pub const Error = error{
    FrameTooLarge,
    InvalidMessage,
    InvalidID,
};

pub const Kind = enum { request, notification, response, failure };

pub const Message = struct {
    kind: Kind,
    method: []const u8 = "",
    raw: []const u8,
};

const allowed_members = [_][]const u8{ "jsonrpc", "id", "method", "params", "result", "error" };
const allowed_error_members = [_][]const u8{ "code", "message", "data" };

fn allowed(names: []const []const u8, candidate: []const u8) bool {
    for (names) |name| {
        if (std.mem.eql(u8, name, candidate)) return true;
    }
    return false;
}

pub const Decoder = struct {
    source: []const u8,
    at: usize = 0,
    limit: usize = frame_limit_default,

    pub fn next(self: *Decoder, arena: std.mem.Allocator) !?Message {
        const line = try self.readFrame() orelse return null;
        return try parseMessage(arena, line);
    }

    fn readFrame(self: *Decoder) !?[]const u8 {
        if (self.at >= self.source.len) return null;
        const rest = self.source[self.at..];
        const break_at = std.mem.indexOfScalar(u8, rest, '\n') orelse {
            if (rest.len > self.limit) return Error.FrameTooLarge;
            return Error.InvalidMessage;
        };
        if (break_at > self.limit) return Error.FrameTooLarge;
        const line = rest[0..break_at];
        self.at += break_at + 1;
        if (line.len == 0) return Error.InvalidMessage;
        if (std.mem.indexOfScalar(u8, line, '\r') != null) return Error.InvalidMessage;
        if (!std.unicode.utf8ValidateSlice(line)) return Error.InvalidMessage;
        return line;
    }
};

pub fn parseMessage(arena: std.mem.Allocator, line: []const u8) !Message {
    if (!std.unicode.utf8ValidateSlice(line)) return Error.InvalidMessage;
    if (line.len == 0 or line[0] != '{' or line[line.len - 1] != '}') return Error.InvalidMessage;

    const document = std.json.parseFromSliceLeaky(std.json.Value, arena, line, .{}) catch return Error.InvalidMessage;
    if (document != .object) return Error.InvalidMessage;
    const object = document.object;

    var members = object.iterator();
    while (members.next()) |entry| {
        if (!allowed(&allowed_members, entry.key_ptr.*)) return Error.InvalidMessage;
    }

    const version = object.get("jsonrpc") orelse return Error.InvalidMessage;
    if (version != .string or !std.mem.eql(u8, version.string, "2.0")) return Error.InvalidMessage;

    const identity = object.get("id");
    const method = object.get("method");
    const params = object.get("params");
    const result = object.get("result");
    const failure = object.get("error");

    if (result != null and failure != null) return Error.InvalidMessage;
    if (method != null and (result != null or failure != null)) return Error.InvalidMessage;
    if (method == null and params != null) return Error.InvalidMessage;

    if (identity) |value| {
        if (value != .string and value != .integer) return Error.InvalidID;
    }

    var kind: Kind = undefined;
    if (method != null and identity != null) {
        kind = .request;
    } else if (method != null) {
        kind = .notification;
    } else if (identity != null and result != null) {
        kind = .response;
    } else if (identity != null and failure != null) {
        kind = .failure;
    } else {
        return Error.InvalidMessage;
    }

    var named: []const u8 = "";
    if (method) |value| {
        if (value != .string or value.string.len == 0) return Error.InvalidMessage;
        named = value.string;
        if (params) |carried| {
            if (carried != .object and carried != .null) return Error.InvalidMessage;
        }
    }
    if (failure) |value| {
        if (value != .object) return Error.InvalidMessage;
        var fields = value.object.iterator();
        while (fields.next()) |entry| {
            if (!allowed(&allowed_error_members, entry.key_ptr.*)) return Error.InvalidMessage;
        }
        const code = value.object.get("code") orelse return Error.InvalidMessage;
        if (code != .integer) return Error.InvalidMessage;
        const detail = value.object.get("message") orelse return Error.InvalidMessage;
        if (detail != .string or detail.string.len == 0) return Error.InvalidMessage;
    }
    return .{ .kind = kind, .method = named, .raw = line };
}
