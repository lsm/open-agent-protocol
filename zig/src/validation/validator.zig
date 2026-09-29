const std = @import("std");
const jsonschema = @import("jsonschema");
const tolerate = @import("tolerate");

pub const Mode = enum { strict, tolerant };

pub const Options = struct {
    mode: Mode = .strict,
};

pub fn parseMode(name: []const u8) ?Mode {
    if (std.ascii.eqlIgnoreCase(name, "strict")) return .strict;
    if (std.ascii.eqlIgnoreCase(name, "tolerant")) return .tolerant;
    return null;
}

pub const Validator = struct {
    allocator: std.mem.Allocator,
    registry: jsonschema.Registry,
    mode: Mode,
    documents: std.heap.ArenaAllocator,
    widened: std.StringArrayHashMapUnmanaged(std.json.Value) = .empty,

    pub fn init(allocator: std.mem.Allocator, options: Options) !Validator {
        var self: Validator = .{
            .allocator = allocator,
            .registry = try jsonschema.Registry.initFromBundled(allocator),
            .mode = options.mode,
            .documents = std.heap.ArenaAllocator.init(allocator),
        };
        errdefer self.deinit();
        if (self.mode == .tolerant) try self.widen();
        return self;
    }

    pub fn deinit(self: *Validator) void {
        self.widened.deinit(self.allocator);
        self.documents.deinit();
        self.registry.deinit();
    }

    fn widen(self: *Validator) !void {
        const documents = self.documents.allocator();
        for (self.registry.documents.keys()) |name| {
            if (tolerate.isMetaSchema(name)) continue;
            const root_value = self.registry.root(name) orelse continue;
            try self.widened.put(self.allocator, name, try tolerate.document(documents, root_value));
        }
    }

    pub fn schema(self: *Validator) !jsonschema.Validator {
        var compiled = jsonschema.Validator.init(self.allocator, &self.registry);
        errdefer compiled.deinit();
        for (self.widened.keys(), self.widened.values()) |name, document| {
            try compiled.overrides.put(self.allocator, name, document);
        }
        return compiled;
    }
};

const envelope_document = "envelope.schema.json";

const packed_type_request =
    \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"com.example.storage.objects.read","id":"read1","session_id":"s1","payload":{"session_id":"s1","bucket":"reports"}}
;

const packed_type_without_id =
    \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"com.example.storage.objects.read","session_id":"s1","payload":{"session_id":"s1","bucket":"reports"}}
;

const core_with_id =
    "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"capabilities.request\",\"id\":\"q1\",\"payload\":{}}"
;

const core_without_id =
    \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.request","payload":{}}
;

fn accepts(allocator: std.mem.Allocator, mode: Mode, envelope: []const u8) !bool {
    var judge = try Validator.init(allocator, .{ .mode = mode });
    defer judge.deinit();
    var document = try std.json.parseFromSlice(std.json.Value, allocator, envelope, .{});
    defer document.deinit();
    var compiled = try judge.schema();
    defer compiled.deinit();
    return (try compiled.validate(envelope_document, document.value)) == null;
}

test "a mode is named by its own spelling, in either case, and nothing else parses" {
    try std.testing.expectEqual(Mode.strict, parseMode("strict").?);
    try std.testing.expectEqual(Mode.tolerant, parseMode("tolerant").?);
    try std.testing.expectEqual(Mode.strict, parseMode("STRICT").?);
    try std.testing.expectEqual(Mode.tolerant, parseMode("Tolerant").?);
    try std.testing.expect(parseMode("tolerant ") == null);
    try std.testing.expect(parseMode("lenient") == null);
    try std.testing.expect(parseMode("") == null);
    try std.testing.expectEqual(Mode.strict, (Options{}).mode);
}

test "a validator keeps the mode it was built with, and judges through it" {
    const allocator = std.testing.allocator;
    var judge = try Validator.init(allocator, .{ .mode = .tolerant });
    defer judge.deinit();
    try std.testing.expectEqual(Mode.tolerant, judge.mode);

    var admitted = try std.json.parseFromSlice(std.json.Value, allocator, packed_type_request, .{});
    defer admitted.deinit();
    var first = try judge.schema();
    defer first.deinit();
    try std.testing.expect(try first.validate(envelope_document, admitted.value) == null);

    var refused = try std.json.parseFromSlice(std.json.Value, allocator, packed_type_without_id, .{});
    defer refused.deinit();
    var second = try judge.schema();
    defer second.deinit();
    try std.testing.expect(try second.validate(envelope_document, refused.value) != null);
}

test "tolerance is what admits a type no pack claims, and the mode is what admits it" {
    const allocator = std.testing.allocator;
    try std.testing.expect(!try accepts(allocator, .strict, packed_type_request));
    try std.testing.expect(try accepts(allocator, .tolerant, packed_type_request));
}

test "tolerance is not permissiveness: the envelope skeleton is still required" {
    const allocator = std.testing.allocator;
    try std.testing.expect(!try accepts(allocator, .tolerant, packed_type_without_id));
}

test "tolerance leaves a core envelope's own requirements alone" {
    const allocator = std.testing.allocator;
    try std.testing.expect(!try accepts(allocator, .strict, core_without_id));
    try std.testing.expect(!try accepts(allocator, .tolerant, core_without_id));
    try std.testing.expect(try accepts(allocator, .tolerant, core_with_id));
}

test "a validator frees itself exactly once, on every allocation failure" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            var judge = try Validator.init(allocator, .{ .mode = .tolerant });
            defer judge.deinit();
            var compiled = try judge.schema();
            defer compiled.deinit();
        }
    }.run, .{});
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            var judge = try Validator.init(allocator, .{});
            defer judge.deinit();
            var compiled = try judge.schema();
            defer compiled.deinit();
        }
    }.run, .{});
}
