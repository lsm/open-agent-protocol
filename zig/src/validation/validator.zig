const std = @import("std");
const jsonschema = @import("jsonschema");
const packs_mod = @import("packs");
const semantic = @import("semantic");
const tolerate = @import("tolerate");
const build_options = @import("build_options");

pub const Mode = enum { strict, tolerant };

pub const Options = struct {
    mode: Mode = .strict,
    pack_dirs: []const []const u8 = &.{},
    io: std.Io,
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
    opened: std.StringArrayHashMapUnmanaged(std.json.Value) = .empty,
    opened_for: std.StringHashMap(void),
    vocabulary: semantic.Packs = .{},

    pub fn init(allocator: std.mem.Allocator, options: Options) !Validator {
        var self: Validator = .{
            .allocator = allocator,
            .registry = try jsonschema.Registry.initFromBundled(allocator),
            .mode = options.mode,
            .documents = std.heap.ArenaAllocator.init(allocator),
            .loaded = packs_mod.Loaded.empty(allocator),
            .opened_for = std.StringHashMap(void).init(allocator),
        };
        errdefer self.deinit();
        self.loaded = try packs_mod.load(options.io, allocator, &self.registry, options.pack_dirs);
        self.vocabulary = try self.vocabularyFrom(self.loaded);
        if (self.mode == .tolerant) try self.widen();
        return self;
    }

    pub fn deinit(self: *Validator) void {
        var opened_for = self.opened_for.iterator();
        while (opened_for.next()) |entry| self.allocator.free(entry.key_ptr.*);
        self.opened_for.deinit();
        self.opened.deinit(self.allocator);
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

    pub fn schema(self: *Validator, document: []const u8) !jsonschema.Validator {
        var compiled = jsonschema.Validator.init(self.allocator, &self.registry);
        errdefer compiled.deinit();
        for (self.widened.keys(), self.widened.values()) |name, held| {
            try compiled.overrides.put(self.allocator, name, held);
        }
        try self.openMembers(document);
        for (self.opened.keys(), self.opened.values()) |name, held| {
            try compiled.overrides.put(self.allocator, name, held);
        }
        return compiled;
    }

    fn openMembers(self: *Validator, document: []const u8) !void {
        if (!appliesTo(document)) return;
        if (self.opened_for.contains(document)) return;
        const owned = try self.allocator.dupe(u8, document);
        self.opened_for.put(owned, {}) catch |err| {
            self.allocator.free(owned);
            return err;
        };
        const opened = self.documents.allocator();
        for (self.loaded.members) |member| {
            const target = packs_mod.payloadTarget(&self.registry, document, member.payload_type) orelse continue;
            const current = self.opened.get(target.document) orelse self.widened.get(target.document) orelse self.registry.root(target.document) orelse continue;
            const member_schema = if (self.mode == .tolerant)
                try tolerate.document(opened, member.schema)
            else
                member.schema;
            try self.opened.put(self.allocator, target.document, try jsonschema.withMember(
                opened,
                current,
                target.definition,
                member.name,
                member_schema,
            ));
        }
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

const packed_member_typed =
    "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.message.submit.request\",\"capability_revision\":\"v1\",\"id\":\"req1\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\",\"delivery\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"go\"}],\"com.example.storage.workspace\":{\"bucket\":\"reports\"}}}"
;

const packed_member_mistyped =
    "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.message.submit.request\",\"capability_revision\":\"v1\",\"id\":\"req1\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\",\"delivery\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"go\"}],\"com.example.storage.workspace\":{\"bucket\":42}}}"
;

fn accepts(allocator: std.mem.Allocator, mode: Mode, envelope: []const u8) !bool {
    var judge = try Validator.init(allocator, .{ .mode = mode, .io = std.testing.io });
    defer judge.deinit();
    var document = try std.json.parseFromSlice(std.json.Value, allocator, envelope, .{});
    defer document.deinit();
    var compiled = try judge.schema(envelope_document);
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
    var first = try judge.schema(envelope_document);
    defer first.deinit();
    try std.testing.expect(try first.validate(envelope_document, admitted.value) == null);

    var refused = try std.json.parseFromSlice(std.json.Value, allocator, packed_type_without_id, .{});
    defer refused.deinit();
    var second = try judge.schema(envelope_document);
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
    return std.fs.path.join(allocator, &.{ build_options.repository_root, "fixtures", "packs", "storage" });
}

fn admits(allocator: std.mem.Allocator, judge: *Validator, envelope: []const u8) !bool {
    var document = try std.json.parseFromSlice(std.json.Value, allocator, envelope, .{});
    defer document.deinit();
    var compiled = try judge.schema(envelope_document);
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

test "a packed member is admitted by its own schema and held to it, and by no pack at all" {
    const allocator = std.testing.allocator;
    const dir = try storagePack(allocator);
    defer allocator.free(dir);

    var unpacked = try Validator.init(allocator, .{ .io = std.testing.io });
    defer unpacked.deinit();
    try std.testing.expect(!try admits(allocator, &unpacked, packed_member_typed));

    var with_pack = try Validator.init(allocator, .{ .pack_dirs = &.{dir}, .io = std.testing.io });
    defer with_pack.deinit();
    try std.testing.expect(try admits(allocator, &with_pack, packed_member_typed));
    try std.testing.expect(!try admits(allocator, &with_pack, packed_member_mistyped));
}

test "a descriptor whose fields are the wrong shape is refused, not read past" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "pack.json", .data = "{\"id\":7,\"version\":\"1.0.0\"}" });
    const dir = try tmp.dir.realPathFileAlloc(std.testing.io, ".", allocator);
    defer allocator.free(dir);
    const dirs = [_][]const u8{dir};
    var judge = try Validator.init(allocator, .{ .io = std.testing.io });
    defer judge.deinit();
    try std.testing.expectError(error.InvalidPackDescriptor, packs_mod.load(std.testing.io, allocator, &judge.registry, &dirs));
}

test "a pack's members widen the core payload only, and no other document" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.writeFile(std.testing.io, .{
        .sub_path = "pack.json",
        .data = "{\"id\":\"com.example.note\",\"version\":\"1.0.0\",\"payload_members\":[{\"payload_type\":\"inference.create.request\",\"member\":\"com.example.note.extra\",\"schema\":{\"type\":\"object\",\"properties\":{\"why\":{\"type\":\"string\"}}}}]}",
    });
    const dir = try tmp.dir.realPathFileAlloc(std.testing.io, ".", allocator);
    defer allocator.free(dir);

    const provider =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.create.request","id":"i1","payload":{"model_ref":"m","messages":[],"com.example.note.extra":{"why":"because"}}}
    ;
    var carrying = try Validator.init(allocator, .{ .pack_dirs = &.{dir}, .io = std.testing.io });
    defer carrying.deinit();

    var widened = try std.json.parseFromSlice(std.json.Value, allocator, provider, .{});
    defer widened.deinit();
    var on_provider = try carrying.schema(provider_document);
    defer on_provider.deinit();
    try std.testing.expect(try on_provider.validate(provider_document, widened.value) != null);

    var on_core = try carrying.schema(envelope_document);
    defer on_core.deinit();
    try std.testing.expect(try on_core.validate(envelope_document, widened.value) != null);
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
    try std.testing.expectEqual(@as(usize, 0), judge.branchesFor("provider-envelope.schema.json").len);
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
            var compiled = try judge.schema(envelope_document);
            defer compiled.deinit();
        }
    }.run, .{});
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            const dir = try storagePack(allocator);
            defer allocator.free(dir);
            var judge = try Validator.init(allocator, .{ .pack_dirs = &.{dir}, .io = std.testing.io });
            defer judge.deinit();
            var compiled = try judge.schema(envelope_document);
            defer compiled.deinit();
        }
    }.run, .{});
}

test "a validator frees itself exactly once, on every allocation failure" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            var judge = try Validator.init(allocator, .{ .mode = .tolerant, .io = std.testing.io });
            defer judge.deinit();
            var compiled = try judge.schema(envelope_document);
            defer compiled.deinit();
        }
    }.run, .{});
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            var judge = try Validator.init(allocator, .{ .io = std.testing.io });
            defer judge.deinit();
            var compiled = try judge.schema(envelope_document);
            defer compiled.deinit();
        }
    }.run, .{});
}
