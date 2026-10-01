
const std = @import("std");

pub fn returnsError(comptime Func: type) bool {
    const return_type = returnType(Func) orelse return false;
    return @typeInfo(return_type) == .error_union;
}

pub fn Result(comptime Func: type, comptime Payload: type) type {
    const return_type = returnType(Func) orelse return Payload;
    return switch (@typeInfo(return_type)) {
        .error_union => |eu| eu.error_set!Payload,
        else => Payload,
    };
}

pub fn ErrorSet(comptime Func: type) type {
    const return_type = returnType(Func) orelse return error{};
    return switch (@typeInfo(return_type)) {
        .error_union => |eu| eu.error_set,
        else => error{},
    };
}

fn returnType(comptime Func: type) ?type {
    return switch (@typeInfo(Func)) {
        .@"fn" => |f| f.return_type,
        .pointer => |p| @typeInfo(p.child).@"fn".return_type,
        else => null,
    };
}

pub fn validate(comptime Model: type, comptime role: []const u8) void {
    comptime {
        for ([_][]const u8{ "Msg", "init", "update", "view" }) |name| {
            if (!@hasDecl(Model, name)) {
                @compileError(role ++ " '" ++ @typeName(Model) ++ "' is missing '" ++
                    name ++ "'. A model needs a 'Msg' type plus 'init', 'update' and 'view'.");
            }
        }
    }
}

test "returnsError distinguishes fallible signatures" {
    const S = struct {
        fn plain() u8 {
            return 0;
        }
        fn fallible() !u8 {
            return 0;
        }
    };

    try std.testing.expect(!returnsError(@TypeOf(S.plain)));
    try std.testing.expect(returnsError(@TypeOf(S.fallible)));
    try std.testing.expect(returnsError(@TypeOf(&S.fallible)));
}

test "Result mirrors fallibility" {
    const S = struct {
        fn plain() u8 {
            return 0;
        }
        fn fallible() error{Boom}!u8 {
            return 0;
        }
    };

    try std.testing.expectEqual([]const u8, Result(@TypeOf(S.plain), []const u8));
    try std.testing.expectEqual(
        error{Boom}![]const u8,
        Result(@TypeOf(S.fallible), []const u8),
    );
}
