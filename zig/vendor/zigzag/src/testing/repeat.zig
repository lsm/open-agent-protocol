pub fn repeat(comptime bytes: []const u8, comptime count: usize) *const [bytes.len * count:0]u8 {
    return comptime blk: {
        @setEvalBranchQuota(count * 2 + 1000);
        var result: [bytes.len * count:0]u8 = @splat(0);
        for (0..count) |i| @memcpy(result[i * bytes.len ..][0..bytes.len], bytes);
        const final = result;
        break :blk &final;
    };
}
