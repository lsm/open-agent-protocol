pub fn repeat(comptime bytes: []const u8, comptime count: usize) *const [bytes.len * count:0]u8 {
    return comptime blk: {
        @setEvalBranchQuota(count * 2 + 1000);
        var result: [bytes.len * count:0]u8 = @splat(0);
        for (0..count) |i| @memcpy(result[i * bytes.len ..][0..bytes.len], bytes);
        const final = result;
        break :blk &final;
    };
}

test "repeat preserves UTF-8 byte sequences and a trailing sentinel" {
    const std = @import("std");
    try std.testing.expectEqualStrings("éé", repeat("é", 2));
    try std.testing.expectEqualStrings("漢漢", repeat("漢", 2));
    try std.testing.expectEqualStrings("😀😀", repeat("😀", 2));
    try std.testing.expectEqualStrings("ababab", repeat("ab", 3));
    try std.testing.expectEqualStrings("", repeat("ab", 0));
    try std.testing.expectEqual(@as(u8, 0), repeat("é", 2)[4]);
}
