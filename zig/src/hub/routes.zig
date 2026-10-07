const std = @import("std");

pub const Verb = enum {
    adapters,
    capabilities,
    open,
    sessions,
    history,
    state,
    tools,
    models,
    submit,
    resolve,
    cancel,
    settings,
    close,
    events,
    work_list,
    work_status,
    work_start,
    work_send,
    work_stop,
    work_read,
    work_capabilities,
};

pub const Route = union(Verb) {
    adapters: void,
    capabilities: []const u8,
    open: []const u8,
    sessions: void,
    history: void,
    state: []const u8,
    tools: []const u8,
    models: []const u8,
    submit: []const u8,
    resolve: []const u8,
    cancel: []const u8,
    settings: []const u8,
    close: []const u8,
    events: []const u8,
    work_list: void,
    work_status: []const u8,
    work_start: []const u8,
    work_send: []const u8,
    work_stop: []const u8,
    work_read: []const u8,
    work_capabilities: void,

    pub fn verb(self: Route) Verb {
        return std.meta.activeTag(self);
    }

    pub fn parameterOf(self: Route) []const u8 {
        return switch (self) {
            inline .adapters, .sessions, .history, .work_list, .work_capabilities => "",
            inline else => |carried| carried,
        };
    }
};

pub const Match = union(enum) {
    route: Route,
    method_not_allowed: []const u8,
    not_found,
};

pub const Failure = error{
    BadEscape,
} || std.mem.Allocator.Error;

const Entry = struct {
    method: []const u8,
    pattern: []const u8,
    verb: Verb,
};

pub const table = [_]Entry{
    .{ .method = "GET", .pattern = "/adapters", .verb = .adapters },
    .{ .method = "GET", .pattern = "/adapters/{name}/capabilities", .verb = .capabilities },
    .{ .method = "POST", .pattern = "/adapters/{name}/sessions", .verb = .open },
    .{ .method = "GET", .pattern = "/sessions", .verb = .sessions },
    .{ .method = "GET", .pattern = "/sessions/history", .verb = .history },
    .{ .method = "GET", .pattern = "/sessions/{id}/state", .verb = .state },
    .{ .method = "GET", .pattern = "/sessions/{id}/tools", .verb = .tools },
    .{ .method = "GET", .pattern = "/sessions/{id}/models", .verb = .models },
    .{ .method = "POST", .pattern = "/sessions/{id}/submit", .verb = .submit },
    .{ .method = "POST", .pattern = "/sessions/{id}/resolve", .verb = .resolve },
    .{ .method = "POST", .pattern = "/sessions/{id}/cancel", .verb = .cancel },
    .{ .method = "POST", .pattern = "/sessions/{id}/settings", .verb = .settings },
    .{ .method = "POST", .pattern = "/sessions/{id}/close", .verb = .close },
    .{ .method = "GET", .pattern = "/sessions/{id}/events", .verb = .events },
    .{ .method = "GET", .pattern = "/work", .verb = .work_list },
    .{ .method = "GET", .pattern = "/work/sessions/{id}", .verb = .work_status },
    .{ .method = "POST", .pattern = "/adapters/{name}/work", .verb = .work_start },
    .{ .method = "POST", .pattern = "/work/sessions/{id}/send", .verb = .work_send },
    .{ .method = "POST", .pattern = "/work/sessions/{id}/stop", .verb = .work_stop },
    .{ .method = "GET", .pattern = "/work/sessions/{id}/read", .verb = .work_read },
    .{ .method = "GET", .pattern = "/work/capabilities", .verb = .work_capabilities },
};

const ShapeFailure = error{
    BadEscape,
    NotAbsolute,
    EmptySegment,
} || std.mem.Allocator.Error;

fn segmentsOf(arena: std.mem.Allocator, path: []const u8, into: *std.ArrayList([]const u8)) ShapeFailure!void {
    if (path.len == 0 or path[0] != '/') return error.NotAbsolute;
    var rest = path[1..];
    while (true) {
        const at = std.mem.indexOfScalar(u8, rest, '/') orelse {
            if (rest.len == 0) return error.EmptySegment;
            try into.append(arena, try unescape(arena, rest));
            return;
        };
        if (at == 0) return error.EmptySegment;
        try into.append(arena, try unescape(arena, rest[0..at]));
        rest = rest[at + 1 ..];
    }
}

fn unescape(arena: std.mem.Allocator, raw: []const u8) ShapeFailure![]const u8 {
    const at = std.mem.indexOfScalar(u8, raw, '%') orelse return raw;
    var out = std.ArrayList(u8).empty;
    try out.appendSlice(arena, raw[0..at]);
    var rest = raw[at..];
    while (rest.len > 0) {
        if (rest[0] != '%') {
            try out.append(arena, rest[0]);
            rest = rest[1..];
            continue;
        }
        if (rest.len < 3) return error.BadEscape;
        const high = std.fmt.charToDigit(rest[1], 16) catch return error.BadEscape;
        const low = std.fmt.charToDigit(rest[2], 16) catch return error.BadEscape;
        try out.append(arena, high * 16 + low);
        rest = rest[3..];
    }
    return out.items;
}

pub const Shape = struct {
    parameter: ?usize,
};

fn shapeMatches(pattern: []const u8, path_segments: []const []const u8) ?Shape {
    var pattern_rest = pattern[1..];
    var parameter: ?usize = null;
    var count: usize = 0;
    for (path_segments, 0..) |segment, index| {
        const at = std.mem.indexOfScalar(u8, pattern_rest, '/');
        const piece = if (at) |found| pattern_rest[0..found] else pattern_rest;
        if (piece.len == 0) return null;
        if (piece[0] == '{') {
            if (piece[piece.len - 1] != '}') return null;
            if (parameter != null) return null;
            parameter = index;
        } else if (!std.mem.eql(u8, piece, segment)) {
            return null;
        }
        count += 1;
        if (at == null) return if (count == path_segments.len) Shape{ .parameter = parameter } else null;
        pattern_rest = pattern_rest[at.? + 1 ..];
    }
    return if (pattern_rest.len == 0) Shape{ .parameter = parameter } else null;
}

fn build(verb: Verb, parameter: ?[]const u8) Route {
    return switch (verb) {
        .adapters => .{ .adapters = {} },
        .sessions => .{ .sessions = {} },
        .history => .{ .history = {} },
        .capabilities => .{ .capabilities = parameter.? },
        .open => .{ .open = parameter.? },
        .state => .{ .state = parameter.? },
        .tools => .{ .tools = parameter.? },
        .models => .{ .models = parameter.? },
        .submit => .{ .submit = parameter.? },
        .resolve => .{ .resolve = parameter.? },
        .cancel => .{ .cancel = parameter.? },
        .settings => .{ .settings = parameter.? },
        .close => .{ .close = parameter.? },
        .events => .{ .events = parameter.? },
        .work_list => .{ .work_list = {} },
        .work_status => .{ .work_status = parameter.? },
        .work_start => .{ .work_start = parameter.? },
        .work_send => .{ .work_send = parameter.? },
        .work_stop => .{ .work_stop = parameter.? },
        .work_read => .{ .work_read = parameter.? },
        .work_capabilities => .{ .work_capabilities = {} },
    };
}

pub fn route(arena: std.mem.Allocator, method: []const u8, path: []const u8) Failure!Match {
    var path_segments = std.ArrayList([]const u8).empty;
    defer path_segments.deinit(arena);
    segmentsOf(arena, path, &path_segments) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        error.BadEscape => return error.BadEscape,
        error.NotAbsolute, error.EmptySegment => return .not_found,
    };
    var allowed: ?[]const u8 = null;
    for (table) |entry| {
        const shape = shapeMatches(entry.pattern, path_segments.items) orelse continue;
        allowed = entry.method;
        if (!std.mem.eql(u8, entry.method, method)) continue;
        return .{ .route = build(entry.verb, if (shape.parameter) |at| path_segments.items[at] else null) };
    }
    return if (allowed) |named| .{ .method_not_allowed = named } else .not_found;
}

const testing = std.testing;

fn matched(arena: std.mem.Allocator, method: []const u8, path: []const u8) !Match {
    return route(arena, method, path);
}

test "the table is the draft's twenty-one routes, one line each" {
    try testing.expectEqual(@as(usize, 21), table.len);
    const paths = [_][]const u8{
        "/adapters",
        "/adapters/{name}/capabilities",
        "/adapters/{name}/sessions",
        "/sessions",
        "/sessions/history",
        "/sessions/{id}/state",
        "/sessions/{id}/tools",
        "/sessions/{id}/models",
        "/sessions/{id}/submit",
        "/sessions/{id}/resolve",
        "/sessions/{id}/cancel",
        "/sessions/{id}/settings",
        "/sessions/{id}/close",
        "/sessions/{id}/events",
        "/work",
        "/work/sessions/{id}",
        "/adapters/{name}/work",
        "/work/sessions/{id}/send",
        "/work/sessions/{id}/stop",
        "/work/sessions/{id}/read",
        "/work/capabilities",
    };
    for (paths, 0..) |path, index| {
        try testing.expectEqualStrings(path, table[index].pattern);
    }
    for (std.enums.values(Verb)) |verb| {
        var seen: usize = 0;
        for (table) |entry| {
            if (entry.verb == verb) seen += 1;
        }
        try testing.expectEqual(@as(usize, 1), seen);
    }
}

test "every route in the table resolves to its own verb and carries the name the path gave it" {
    const cases = [_]struct { method: []const u8, path: []const u8, want: Verb, parameter: ?[]const u8 }{
        .{ .method = "GET", .path = "/adapters", .want = .adapters, .parameter = null },
        .{ .method = "GET", .path = "/adapters/memory/capabilities", .want = .capabilities, .parameter = "memory" },
        .{ .method = "POST", .path = "/adapters/memory/sessions", .want = .open, .parameter = "memory" },
        .{ .method = "GET", .path = "/sessions", .want = .sessions, .parameter = null },
        .{ .method = "GET", .path = "/sessions/s-1/state", .want = .state, .parameter = "s-1" },
        .{ .method = "GET", .path = "/sessions/s-1/tools", .want = .tools, .parameter = "s-1" },
        .{ .method = "GET", .path = "/sessions/s-1/models", .want = .models, .parameter = "s-1" },
        .{ .method = "POST", .path = "/sessions/s-1/submit", .want = .submit, .parameter = "s-1" },
        .{ .method = "POST", .path = "/sessions/s-1/resolve", .want = .resolve, .parameter = "s-1" },
        .{ .method = "POST", .path = "/sessions/s-1/cancel", .want = .cancel, .parameter = "s-1" },
        .{ .method = "POST", .path = "/sessions/s-1/settings", .want = .settings, .parameter = "s-1" },
        .{ .method = "POST", .path = "/sessions/s-1/close", .want = .close, .parameter = "s-1" },
        .{ .method = "GET", .path = "/sessions/s-1/events", .want = .events, .parameter = "s-1" },
    };
    try testing.expectEqual(@as(usize, 13), cases.len);
    for (cases) |case| {
        var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
        defer arena_state.deinit();
        const arena = arena_state.allocator();
        const got = try matched(arena, case.method, case.path);
        try testing.expectEqual(case.want, got.route.verb());
        if (case.parameter) |wanted| {
            try testing.expectEqualStrings(wanted, got.route.parameterOf());
        }
    }
}

test "a path that is a route asked for with the wrong method is not the same as a path that is not a route" {
    const cases = [_]struct { method: []const u8, path: []const u8, allowed: []const u8 }{
        .{ .method = "POST", .path = "/adapters", .allowed = "GET" },
        .{ .method = "GET", .path = "/sessions/s-1/submit", .allowed = "POST" },
        .{ .method = "DELETE", .path = "/sessions/s-1/close", .allowed = "POST" },
        .{ .method = "get", .path = "/adapters", .allowed = "GET" },
    };
    for (cases) |case| {
        var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
        defer arena_state.deinit();
        const arena = arena_state.allocator();
        const got = try matched(arena, case.method, case.path);
        try testing.expectEqualStrings(case.allowed, got.method_not_allowed);
    }
}

test "a path no route names is not found, whatever method it is asked with" {
    const cases = [_][]const u8{
        "/",
        "/adapters/",
        "/adapters/memory",
        "/adapters/memory/capabilities/extra",
        "/sessions/",
        "/sessions/s-1",
        "/sessions/s-1/state/extra",
        "/adapters//capabilities",
        "adapters",
        "/Sessions",
        "/adapters/memory/Capabilities",
    };
    for (cases) |path| {
        var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
        defer arena_state.deinit();
        const arena = arena_state.allocator();
        try testing.expectEqual(Match.not_found, try matched(arena, "GET", path));
        try testing.expectEqual(Match.not_found, try matched(arena, "POST", path));
    }
}

test "a name is percent-decoded, so a session id may carry anything a client can encode" {
    const cases = [_]struct { path: []const u8, want: []const u8 }{
        .{ .path = "/sessions/s%2D1/state", .want = "s-1" },
        .{ .path = "/sessions/s%201/state", .want = "s 1" },
        .{ .path = "/sessions/caf%C3%A9/state", .want = "café" },
        .{ .path = "/sessions/100%25/state", .want = "100%" },
        .{ .path = "/sessions/s-1/state", .want = "s-1" },
    };
    for (cases) |case| {
        var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
        defer arena_state.deinit();
        const arena = arena_state.allocator();
        const got = try matched(arena, "GET", case.path);
        try testing.expectEqual(Verb.state, got.route.verb());
        try testing.expectEqualStrings(case.want, got.route.state);
    }
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const named = try matched(arena, "GET", "/adapters/claude%20code/capabilities");
    try testing.expectEqual(Verb.capabilities, named.route.verb());
    try testing.expectEqualStrings("claude code", named.route.capabilities);
}

test "an encoded slash is part of the name, not a separator, because an id is opaque once it is decoded" {
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const got = try matched(arena, "GET", "/sessions/a%2Fb/state");
    try testing.expectEqual(Verb.state, got.route.verb());
    try testing.expectEqualStrings("a/b", got.route.state);
    try testing.expectEqual(Match.not_found, try matched(arena, "GET", "/sessions/a%2Fb%2Fstate"));
}

test "an escape that is not one is a refusal, not a name" {
    const cases = [_][]const u8{
        "/sessions/s%2/state",
        "/sessions/s%zz/state",
        "/sessions/s%/state",
        "/sessions/s%2",
    };
    for (cases) |path| {
        var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
        defer arena_state.deinit();
        const arena = arena_state.allocator();
        try testing.expectError(error.BadEscape, route(arena, "GET", path));
    }
}

test "a request with a name to decode survives every allocation failing, and leaks nothing" {
    try testing.checkAllAllocationFailures(testing.allocator, struct {
        fn drive(allocator: std.mem.Allocator) !void {
            var arena_state = std.heap.ArenaAllocator.init(allocator);
            defer arena_state.deinit();
            _ = try route(arena_state.allocator(), "GET", "/sessions/caf%C3%A9/events");
            return;
        }
    }.drive, .{});
}
