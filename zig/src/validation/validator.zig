const std = @import("std");
const jsonschema = @import("jsonschema");
const packs_mod = @import("packs");
const semantic = @import("semantic");
const tolerate = @import("tolerate");
const build_options = @import("build_options");

pub const Mode = enum { strict, tolerant };

pub const PackRefusal = packs_mod.Refusal;

pub const Options = struct {
    mode: Mode = .strict,
    pack_dirs: []const []const u8 = &.{},
    io: std.Io,
    codes: ?*std.ArrayList(u8) = null,
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
    loaded: packs_mod.Loaded,
    vocabulary: semantic.Packs = .{},

    pub fn init(allocator: std.mem.Allocator, options: Options) !Validator {
        var self: Validator = .{
            .allocator = allocator,
            .registry = try jsonschema.Registry.initFromBundled(allocator),
            .mode = options.mode,
            .documents = std.heap.ArenaAllocator.init(allocator),
            .loaded = packs_mod.Loaded.empty(allocator),
        };
        errdefer self.deinit();
        self.loaded = try packs_mod.load(options.io, allocator, &self.registry, options.pack_dirs);
        if (self.loaded.refusals.len != 0) {
            if (options.codes) |sink| {
                for (self.loaded.refusals) |refusal| {
                    const rendered = try std.fmt.allocPrint(allocator, " {s} in {s}", .{
                        if (refusal.code.len == 0) "unresolved-schema-reference" else refusal.code,
                        refusal.pack,
                    });
                    defer allocator.free(rendered);
                    try sink.appendSlice(allocator, rendered);
                }
            }
            return error.PackLoadRefused;
        }
        self.vocabulary = try self.vocabularyFrom(self.loaded);
        if (self.mode == .tolerant) try self.widen();
        return self;
    }

    pub fn deinit(self: *Validator) void {
        self.loaded.deinit();
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
        for (self.widened.keys(), self.widened.values()) |name, held| {
            try compiled.overrides.put(self.allocator, name, held);
        }
        return compiled;
    }

    pub fn branchesFor(self: *const Validator, document: []const u8) []const jsonschema.Alternative {
        if (!appliesTo(document)) return &.{};
        return self.loaded.branches;
    }

    pub fn semanticPacks(self: *const Validator) semantic.Packs {
        return self.vocabulary;
    }

    fn vocabularyFrom(self: *Validator, loaded: packs_mod.Loaded) !semantic.Packs {
        const allocator = self.documents.allocator();
        const types = try allocator.alloc(semantic.PackedType, loaded.types.len);
        for (types, loaded.types) |*held, source| {
            held.* = .{
                .name = source.name,
                .role = source.role,
                .capability = source.capability,
                .response = source.response,
                .refusals = source.refusals,
            };
        }
        const members = try allocator.alloc(semantic.PackedMember, loaded.members.len);
        for (members, loaded.members) |*held, source| {
            held.* = .{
                .payload_type = source.payload_type,
                .name = source.name,
                .capability = source.capability,
            };
        }
        return .{ .types = types, .members = members };
    }
};

const envelope_document = "envelope.schema.json";
const provider_document = "provider-envelope.schema.json";

fn appliesTo(document: []const u8) bool {
    return std.mem.eql(u8, document, envelope_document);
}

const packed_type_request =
    \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"com.example.storage.objects.read","id":"read1","session_id":"s1","payload":{"session_id":"s1","bucket":"reports"}}
;

const packed_type_declared =
    "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"com.example.storage.objects.read\",\"capability_revision\":\"v1\",\"id\":\"read1\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\",\"bucket\":\"reports\",\"key\":\"q3.csv\"}}"
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
    var judge = try Validator.init(allocator, .{ .mode = mode, .io = std.testing.io });
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
    try std.testing.expectEqual(Mode.strict, (Options{ .io = std.testing.io }).mode);
}

test "a validator keeps the mode it was built with, and judges through it" {
    const allocator = std.testing.allocator;
    var judge = try Validator.init(allocator, .{ .mode = .tolerant, .io = std.testing.io });
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

fn storagePack(allocator: std.mem.Allocator) ![]const u8 {
    return fixturePack(allocator, "storage");
}

fn fixturePack(allocator: std.mem.Allocator, name: []const u8) ![]const u8 {
    return std.fs.path.join(allocator, &.{ build_options.repository_root, "fixtures", "packs", name });
}

fn admits(allocator: std.mem.Allocator, judge: *Validator, envelope: []const u8) !bool {
    var document = try std.json.parseFromSlice(std.json.Value, allocator, envelope, .{});
    defer document.deinit();
    var compiled = try judge.schema();
    defer compiled.deinit();
    return (try compiled.validateWithBranches(envelope_document, document.value, judge.branchesFor(envelope_document))) == null;
}

test "a packed type is judged through the branch its pack declares, and refused without it" {
    const allocator = std.testing.allocator;
    const dir = try storagePack(allocator);
    defer allocator.free(dir);

    var unpacked = try Validator.init(allocator, .{ .io = std.testing.io });
    defer unpacked.deinit();
    try std.testing.expect(!try admits(allocator, &unpacked, packed_type_declared));
    try std.testing.expect(unpacked.branchesFor(envelope_document).len == 0);

    var with_pack = try Validator.init(allocator, .{ .pack_dirs = &.{dir}, .io = std.testing.io });
    defer with_pack.deinit();
    try std.testing.expect(with_pack.branchesFor(envelope_document).len > 0);
    try std.testing.expect(try admits(allocator, &with_pack, packed_type_declared));
}

test "a schema path that leaves the pack root is refused, by .. or by symlink, and one that stays is read" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    for ([_][]const u8{ "escaping", "linked", "staying", "outside" }) |name| {
        try tmp.dir.createDir(std.testing.io, name, .default_dir);
    }
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "outside/away.schema.json", .data =             \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type"], "properties": {"type": {"const": "com.example.ok.thing"}}}}}
        , });
    try tmp.dir.symLink(std.testing.io, "../outside/away.schema.json", "linked/away.schema.json", .{ .is_directory = false });
    try tmp.dir.writeFile(std.testing.io, .{
        .sub_path = "escaping/pack.json",
        .data =
            \\{"id": "com.example.esc", "version": "1.0.0", "schemas": ["../../../etc/hosts"], "envelope_types": []}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{
        .sub_path = "linked/pack.json",
        .data =
            \\{"id": "com.example.link", "version": "1.0.0", "schemas": ["away.schema.json"], "envelope_types": []}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{
        .sub_path = "staying/note.schema.json",
        .data =
            \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type"], "properties": {"type": {"const": "com.example.ok.thing"}}}}}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{
        .sub_path = "staying/pack.json",
        .data =
            \\{"id": "com.example.ok", "version": "1.0.0", "schemas": ["note.schema.json"], "envelope_types": [{"type": "com.example.ok.thing", "role": "event", "schema": "note.schema.json#/$defs/thing"}]}
        ,
    });

    var judge = try Validator.init(allocator, .{ .io = std.testing.io });
    defer judge.deinit();

    for ([_][]const u8{ "escaping", "linked" }) |name| {
        const dir = try tmp.dir.realPathFileAlloc(std.testing.io, name, allocator);
        defer allocator.free(dir);
        const one = [_][]const u8{dir};
        try std.testing.expectError(error.InvalidPackDescriptor, packs_mod.load(std.testing.io, allocator, &judge.registry, &one));
    }

    const staying = try tmp.dir.realPathFileAlloc(std.testing.io, "staying", allocator);
    defer allocator.free(staying);
    const within = [_][]const u8{staying};
    var read = try packs_mod.load(std.testing.io, allocator, &judge.registry, &within);
    defer read.deinit();
    try std.testing.expectEqual(@as(usize, 1), read.branches.len);
}

test "a refusal whose rendered line outgrows any fixed buffer still prints its code" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.createDir(std.testing.io, "long", .default_dir);

    const long_id = try std.fmt.allocPrint(allocator, "com.example.{s}", .{"x" ** 400});
    defer allocator.free(long_id);
    try std.testing.expect(long_id.len > 256);

    const descriptor = try std.fmt.allocPrint(allocator,
        \\{{"id": "{s}", "version": "1.0.0", "capability_keys": ["capabilities.request.thing"]}}
    , .{long_id});
    defer allocator.free(descriptor);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "long/pack.json", .data = descriptor });

    const dir = try tmp.dir.realPathFileAlloc(std.testing.io, "long", allocator);
    defer allocator.free(dir);
    const dirs = [_][]const u8{dir};

    var codes: std.ArrayList(u8) = .empty;
    defer codes.deinit(allocator);
    try std.testing.expectError(error.PackLoadRefused, Validator.init(allocator, .{
        .io = std.testing.io,
        .pack_dirs = &dirs,
        .codes = &codes,
    }));
    try std.testing.expect(std.mem.indexOf(u8, codes.items, "pack_unprefixed_name") != null);
    try std.testing.expect(std.mem.indexOf(u8, codes.items, long_id) != null);
    try std.testing.expect(codes.items.len > long_id.len);

    codes.clearRetainingCapacity();
    try tmp.dir.createDir(std.testing.io, "short", .default_dir);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "short/pack.json", .data =
            \\{"id": "com.example.short", "version": "1.0.0", "capability_keys": ["capabilities.request.thing"]}
        ,
    });
    const short_dir = try tmp.dir.realPathFileAlloc(std.testing.io, "short", allocator);
    defer allocator.free(short_dir);
    const short_dirs = [_][]const u8{short_dir};
    try std.testing.expectError(error.PackLoadRefused, Validator.init(allocator, .{
        .io = std.testing.io,
        .pack_dirs = &short_dirs,
        .codes = &codes,
    }));
    try std.testing.expectEqualStrings(" pack_unprefixed_name in com.example.short", codes.items);
}
test "a descriptor whose id, version or schemas is the wrong shape is refused" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const dir = try tmp.dir.realPathFileAlloc(std.testing.io, ".", allocator);
    defer allocator.free(dir);
    const dirs = [_][]const u8{dir};
    for ([_][]const u8{
        "{\"id\":7,\"version\":\"1.0.0\"}",
        "{\"id\":\"com.example.note\",\"version\":7}",
        "{\"id\":\"com.example.note\",\"version\":\"1.0.0\",\"schemas\":\"note.schema.json\"}",
    }) |descriptor| {
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "pack.json", .data = descriptor });
        var judge = try Validator.init(allocator, .{ .io = std.testing.io });
        defer judge.deinit();
        try std.testing.expectError(error.InvalidPackDescriptor, packs_mod.load(std.testing.io, allocator, &judge.registry, &dirs));
    }
}

test "one pack named twice is one pack, and contributes one branch" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.createDir(std.testing.io, "a", .default_dir);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "a/note.schema.json",
        .data =
        \\{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"ping":{"type":"object","required":["type","id","session_id"],"properties":{"type":{"const":"com.example.note.ping"},"id":{"type":"string"},"session_id":{"type":"string"}}}}}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "a/pack.json",
        .data =
        \\{"id":"com.example.note","version":"1.0.0","schemas":["note.schema.json"],"envelope_types":[{"type":"com.example.note.ping","role":"event","schema":"note.schema.json#/$defs/ping"}]}
        ,
    });
    const first = try tmp.dir.realPathFileAlloc(std.testing.io, "a", allocator);
    defer allocator.free(first);
    const dirs = [_][]const u8{ first, first };

    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();
    var judge = try Validator.init(allocator, .{ .io = std.testing.io });
    defer judge.deinit();

    var once = try packs_mod.load(std.testing.io, allocator, &judge.registry, &.{first});
    defer once.deinit();
    var twice = try packs_mod.load(std.testing.io, allocator, &judge.registry, &dirs);
    defer twice.deinit();
    try std.testing.expect(once.branches.len > 0);
    try std.testing.expectEqual(once.branches.len, twice.branches.len);
    try std.testing.expectEqual(once.types.len, twice.types.len);
    try std.testing.expectEqual(once.members.len, twice.members.len);
}

test "a descriptor declaring one type twice is refused, not loaded once" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.writeFile(std.testing.io, .{
        .sub_path = "pack.json",
        .data = "{\"id\":\"com.example.twice\",\"version\":\"1.0.0\",\"envelope_types\":[{\"type\":\"com.example.twice.ping\",\"role\":\"event\",\"schema\":\"note.schema.json#/$defs/thing\"},{\"type\":\"com.example.twice.ping\",\"role\":\"event\",\"schema\":\"note.schema.json#/$defs/thing\"}]}",
    });
    const dir = try tmp.dir.realPathFileAlloc(std.testing.io, ".", allocator);
    defer allocator.free(dir);
    const dirs = [_][]const u8{dir};
    var judge = try Validator.init(allocator, .{ .io = std.testing.io });
    defer judge.deinit();
    try std.testing.expectError(error.InvalidPackDescriptor, packs_mod.load(std.testing.io, allocator, &judge.registry, &dirs));
}

test "a pack's branches belong to the core envelope, and to no other document" {
    const allocator = std.testing.allocator;
    const dir = try storagePack(allocator);
    defer allocator.free(dir);
    var judge = try Validator.init(allocator, .{ .pack_dirs = &.{dir}, .io = std.testing.io });
    defer judge.deinit();
    try std.testing.expect(judge.branchesFor(envelope_document).len > 0);
    try std.testing.expectEqual(@as(usize, 0), judge.branchesFor(provider_document).len);
    try std.testing.expectEqual(@as(usize, 0), judge.branchesFor("common.schema.json").len);
}

test "the pack's own vocabulary is what the semantic machine is handed" {
    const allocator = std.testing.allocator;
    const dir = try storagePack(allocator);
    defer allocator.free(dir);

    var unpacked = try Validator.init(allocator, .{ .io = std.testing.io });
    defer unpacked.deinit();
    try std.testing.expect(unpacked.semanticPacks().types.len == 0);
    try std.testing.expect(unpacked.semanticPacks().members.len == 0);

    var with_pack = try Validator.init(allocator, .{ .pack_dirs = &.{dir}, .io = std.testing.io });
    defer with_pack.deinit();
    const vocabulary = with_pack.semanticPacks();
    var named = false;
    for (vocabulary.types) |held| {
        if (std.mem.eql(u8, held.name, "com.example.storage.objects.read")) named = true;
    }
    try std.testing.expect(named);
    var declared = false;
    for (vocabulary.members) |held| {
        if (std.mem.eql(u8, held.name, "com.example.storage.workspace")) declared = true;
    }
    try std.testing.expect(declared);
}

test "a validator frees itself exactly once when a pack cannot be loaded" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            var judge = try Validator.init(allocator, .{ .io = std.testing.io });
            defer judge.deinit();
            var compiled = try judge.schema();
            defer compiled.deinit();
        }
    }.run, .{});
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            const dir = try storagePack(allocator);
            defer allocator.free(dir);
            var judge = try Validator.init(allocator, .{ .pack_dirs = &.{dir}, .io = std.testing.io });
            defer judge.deinit();
            var compiled = try judge.schema();
            defer compiled.deinit();
        }
    }.run, .{});
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            const dir = try fixturePack(allocator, "bad-type-duplicate");
            defer allocator.free(dir);
            if (Validator.init(allocator, .{ .pack_dirs = &.{dir}, .io = std.testing.io })) |judge| {
                var loaded = judge;
                defer loaded.deinit();
                return error.TestExpectedError;
            } else |err| switch (err) {
                error.InvalidPackDescriptor => {},
                else => return err,
            }
        }
    }.run, .{});
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            const dir = try fixturePack(allocator, "bad-unprefixed-name");
            defer allocator.free(dir);
            if (Validator.init(allocator, .{ .pack_dirs = &.{dir}, .io = std.testing.io })) |judge| {
                var refused = judge;
                defer refused.deinit();
                return error.TestExpectedError;
            } else |err| switch (err) {
                error.PackLoadRefused => {},
                else => return err,
            }
        }
    }.run, .{});
}

test "a validator frees itself exactly once, on every allocation failure" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            var judge = try Validator.init(allocator, .{ .mode = .tolerant, .io = std.testing.io });
            defer judge.deinit();
            var compiled = try judge.schema();
            defer compiled.deinit();
        }
    }.run, .{});
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            var judge = try Validator.init(allocator, .{ .io = std.testing.io });
            defer judge.deinit();
            var compiled = try judge.schema();
            defer compiled.deinit();
        }
    }.run, .{});
}
