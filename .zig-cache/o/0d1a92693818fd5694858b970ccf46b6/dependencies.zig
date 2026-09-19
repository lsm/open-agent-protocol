pub const packages = struct {
    pub const @"zig/vendor/zigzag" = struct {
        pub const build_root = "/Users/lsm/focus/open-agent-protocol/.claude/worktrees/epic-burnell-f7702d/zig/vendor/zigzag";
        pub const build_zig = @import("zig/vendor/zigzag");
        pub const deps: []const struct { []const u8, []const u8 } = &.{
        };
    };
};

pub const root_deps: []const struct { []const u8, []const u8 } = &.{
    .{ "zigzag", "zig/vendor/zigzag" },
};
