pub const packages = struct {
    pub const @"vendor/zigzag" = struct {
        pub const build_root = "zig/vendor/zigzag";
        pub const build_zig = @import("vendor/zigzag");
        pub const deps: []const struct { []const u8, []const u8 } = &.{
        };
    };
};

pub const root_deps: []const struct { []const u8, []const u8 } = &.{
    .{ "zigzag", "vendor/zigzag" },
};
