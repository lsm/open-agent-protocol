const std = @import("std");

const fields = [_][]const u8{ "input_tokens", "output_tokens", "total_tokens", "cache_read_input_tokens", "cache_write_input_tokens", "reasoning_tokens" };
pub const maximum = "18446744073709551615";

pub fn counts(value: std.json.Value) ?std.json.ObjectMap {
    if (value != .object) return null;
    const kind = value.object.get("type") orelse return null;
    if (kind != .string) return null;
    var eligible = false;
    for ([_][]const u8{ "content.completed", "run.completed", "run.failed", "run.cancelled", "inference.completed", "inference.failed" }) |site| {
        if (std.mem.eql(u8, kind.string, site)) eligible = true;
    }
    if (!eligible) return null;
    const payload = value.object.get("payload") orelse return null;
    if (payload != .object) return null;
    const usage = payload.object.get("usage") orelse return null;
    if (usage != .object) return null;
    return usage.object;
}

pub fn noncanonical(value: std.json.Value) bool {
    const usage = counts(value) orelse return false;
    for (fields) |field| {
        const number = usage.get(field) orelse continue;
        if (number != .number_string) continue;
        const token = number.number_string;
        if (std.mem.eql(u8, token, "0")) continue;
        if (token.len == 0 or token[0] < '1' or token[0] > '9') return true;
        for (token) |digit| {
            if (digit < '0' or digit > '9') return true;
        }
    }
    return false;
}

pub fn inRange(token: []const u8) bool {
    if (token.len == 0) return false;
    const negative = token[0] == '-';
    const start: usize = if (negative) 1 else 0;
    const end = std.mem.indexOfAny(u8, token, "eE") orelse token.len;
    const mantissa = token[start..end];
    const dot = std.mem.indexOfScalar(u8, mantissa, '.');
    const fraction = if (dot) |at| mantissa.len - at - 1 else 0;
    var digits: usize = 0;
    var leading: usize = 0;
    var trailing: usize = 0;
    var nonzero = false;
    for (mantissa) |digit| {
        if (digit == '.') continue;
        if (digit < '0' or digit > '9') return false;
        digits += 1;
        if (!nonzero and digit == '0') leading += 1;
        if (digit == '0') {
            trailing += 1;
        } else {
            nonzero = true;
            trailing = 0;
        }
    }
    if (!nonzero) return digits != 0;
    if (negative) return false;
    const exponent = if (end < token.len) std.fmt.parseInt(i64, token[end + 1 ..], 10) catch return false else 0;
    const shift = std.math.sub(i64, exponent, @intCast(fraction)) catch return false;
    const scale = std.math.add(i64, shift, @intCast(trailing)) catch return false;
    if (scale < 0 or scale > 20) return false;
    const significant = digits - leading - trailing;
    const length = significant + @as(usize, @intCast(scale));
    if (length > maximum.len) return false;
    if (length < maximum.len) return true;
    var index: usize = 0;
    var seen: usize = 0;
    for (mantissa) |digit| {
        if (digit == '.') continue;
        defer seen += 1;
        if (seen < leading or seen >= digits - trailing) continue;
        if (digit < maximum[index]) return true;
        if (digit > maximum[index]) return false;
        index += 1;
    }
    while (index < maximum.len) : (index += 1) {
        if ('0' < maximum[index]) return true;
    }
    return true;
}
