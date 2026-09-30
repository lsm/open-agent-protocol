const std = @import("std");
const jsonschema = @import("jsonschema");
const schema_bundle = @import("schema_bytes");

pub const pack_base_uri = "https://open-agent-protocol.local/ext/";

pub const Branch = jsonschema.Alternative;

pub const pack_unprefixed_name = "pack_unprefixed_name";
pub const pack_foreign_prefix = "pack_foreign_prefix";
pub const pack_id_collision = "pack_id_collision";

pub const Refusal = struct {
    code: []const u8,
    pack: []const u8,
    detail: []const u8 = "",
};

pub const Member = struct {
    payload_type: []const u8,
    name: []const u8,
    schema: std.json.Value,
    capability: []const u8 = "",
};

pub const Declared = struct {
    name: []const u8,
    role: []const u8,
    capability: []const u8 = "",
    response: []const u8 = "",
    refusals: []const []const u8 = &.{},
};

pub const Loaded = struct {
    arena: std.heap.ArenaAllocator,
    branches: []Branch,
    members: []Member,
    types: []Declared = &.{},
    refusals: []Refusal = &.{},

    pub fn deinit(self: *Loaded) void {
        self.arena.deinit();
    }
};

fn underPrefix(name: []const u8, prefix: []const u8) bool {
    if (!std.mem.startsWith(u8, name, prefix)) return false;
    return name.len > prefix.len and name[prefix.len] == '.';
}

fn rootOf(name: []const u8) ?[]const u8 {
    const first = std.mem.indexOfScalar(u8, name, '.') orelse return null;
    const rest = name[first + 1 ..];
    const second = std.mem.indexOfScalar(u8, rest, '.') orelse return name;
    return name[0 .. first + 1 + second];
}

fn coreRoots(allocator: std.mem.Allocator) !std.StringHashMap(void) {
    var roots: std.StringHashMap(void) = .init(allocator);
    for (schema_bundle.all) |entry| {
        if (!std.mem.eql(u8, entry.name, "envelope.schema.json")) continue;
        const parsed = std.json.parseFromSliceLeaky(std.json.Value, allocator, entry.bytes, .{}) catch continue;
        if (parsed != .object) continue;
        const defs = parsed.object.get("$defs") orelse continue;
        if (defs != .object) continue;
        var branches = defs.object.iterator();
        while (branches.next()) |branch| {
            if (branch.value_ptr.* != .object) continue;
            const properties = branch.value_ptr.object.get("properties") orelse continue;
            if (properties != .object) continue;
            const type_schema = properties.object.get("type") orelse continue;
            if (type_schema != .object) continue;
            const held = type_schema.object.get("const") orelse continue;
            if (held != .string) continue;
            const root = rootOf(held.string) orelse continue;
            try roots.put(root, {});
        }
    }
    return roots;
}

fn labelCount(name: []const u8) usize {
    var labels: usize = 1;
    for (name) |c| {
        if (c == '.') labels += 1;
    }
    return labels;
}

fn specNamespace(roots: std.StringHashMap(void), name: []const u8) bool {
    if (labelCount(name) < 3) return true;
    const root = rootOf(name) orelse return true;
    return roots.contains(root);
}

fn namespaceRefusal(roots: *?std.StringHashMap(void), allocator: std.mem.Allocator, id: []const u8, name: []const u8) !?[]const u8 {
    if (name.len == 0) return pack_unprefixed_name;
    if (underPrefix(name, id)) return null;
    if (roots.* == null) roots.* = try coreRoots(allocator);
    if (specNamespace(roots.*.?, name)) return pack_unprefixed_name;
    return pack_foreign_prefix;
}

fn readAll(io: std.Io, allocator: std.mem.Allocator, path: []const u8) ![]u8 {
    return std.Io.Dir.cwd().readFileAlloc(io, path, allocator, .limited(8 * 1024 * 1024));
}

pub fn load(
    io: std.Io,
    parent: std.mem.Allocator,
    registry: *jsonschema.Registry,
    pack_dirs: []const []const u8,
) !Loaded {
    return gather(io, parent, registry, pack_dirs);
}

pub fn describe(io: std.Io, parent: std.mem.Allocator, pack_dirs: []const []const u8) !Loaded {
    return gather(io, parent, null, pack_dirs);
}

fn gather(
    io: std.Io,
    parent: std.mem.Allocator,
    registry: ?*jsonschema.Registry,
    pack_dirs: []const []const u8,
) !Loaded {
    var arena = std.heap.ArenaAllocator.init(parent);
    errdefer arena.deinit();
    const allocator = arena.allocator();

    var branches = std.ArrayList(Branch).empty;
    var members = std.ArrayList(Member).empty;
    var types = std.ArrayList(Declared).empty;
    var load_refusals = std.ArrayList(Refusal).empty;
    var ids = std.ArrayList([]const u8).empty;
    var roots: ?std.StringHashMap(void) = null;

    for (pack_dirs) |dir| {
        const descriptor_path = try std.fs.path.join(allocator, &.{ dir, "pack.json" });
        const descriptor_bytes = try readAll(io, allocator, descriptor_path);
        const descriptor = try std.json.parseFromSliceLeaky(std.json.Value, allocator, descriptor_bytes, .{});
        if (descriptor != .object) return error.InvalidPackDescriptor;
        const pack_id = (descriptor.object.get("id") orelse return error.InvalidPackDescriptor).string;
        const version = (descriptor.object.get("version") orelse return error.InvalidPackDescriptor).string;
        try ids.append(allocator, pack_id);

        if (descriptor.object.get("capability_keys")) |keys| {
            if (keys == .array) {
                for (keys.array.items) |key| {
                    if (key != .string) return error.InvalidPackDescriptor;
                    if (try namespaceRefusal(&roots, allocator, pack_id, key.string)) |code| {
                        try load_refusals.append(allocator, .{ .code = code, .pack = pack_id, .detail = key.string });
                    }
                }
            }
        }

        if (descriptor.object.get("error_codes")) |codes| {
            if (codes == .array) {
                for (codes.array.items) |code| {
                    if (code != .string) return error.InvalidPackDescriptor;
                    if (try namespaceRefusal(&roots, allocator, pack_id, code.string)) |refused| {
                        try load_refusals.append(allocator, .{ .code = refused, .pack = pack_id, .detail = code.string });
                    }
                }
            }
        }

        if (registry) |target| {
            if (descriptor.object.get("schemas")) |schemas| {
                for (schemas.array.items) |schema_name| {
                    const file = schema_name.string;
                    const schema_path = try std.fs.path.join(allocator, &.{ dir, file });
                    const schema_bytes = try readAll(io, allocator, schema_path);
                    const key = try std.fmt.allocPrint(allocator, "{s}{s}/{s}/{s}", .{ pack_base_uri, pack_id, version, file });
                    try target.addDocument(key, schema_bytes);
                }
            }
        }

        const gates = descriptor.object.get("gates");

        if (descriptor.object.get("payload_members")) |declared_members| {
            for (declared_members.array.items) |entry| {
                const payload_type = (entry.object.get("payload_type") orelse continue).string;
                const name = (entry.object.get("member") orelse continue).string;
                try members.append(allocator, .{
                    .payload_type = payload_type,
                    .name = name,
                    .schema = entry.object.get("schema") orelse continue,
                    .capability = memberCapability(gates, payload_type, name),
                });
            }
        }

        if (descriptor.object.get("envelope_types")) |declared_types| {
            for (declared_types.array.items) |entry| {
                const name = (entry.object.get("type") orelse continue).string;
                var refusals = std.ArrayList([]const u8).empty;
                if (entry.object.get("refusals")) |listed| {
                    if (listed == .array) {
                        for (listed.array.items) |code| {
                            if (code == .string) try refusals.append(allocator, code.string);
                        }
                    }
                }
                try types.append(allocator, .{
                    .name = name,
                    .role = if (entry.object.get("role")) |role| role.string else "",
                    .capability = typeCapability(gates, name),
                    .response = responseFor(declared_types, name),
                    .refusals = try refusals.toOwnedSlice(allocator),
                });
            }
        }

        const declared = descriptor.object.get("envelope_types") orelse continue;
        for (declared.array.items) |entry| {
            const declared_type = (entry.object.get("type") orelse continue).string;
            if (try namespaceRefusal(&roots, allocator, pack_id, declared_type)) |code| {
                try load_refusals.append(allocator, .{ .code = code, .pack = pack_id, .detail = declared_type });
                continue;
            }
            const schema_ref = (entry.object.get("schema") orelse continue).string;
            const hash = std.mem.indexOfScalar(u8, schema_ref, '#') orelse continue;
            const ref = try std.fmt.allocPrint(allocator, "{s}{s}/{s}/{s}", .{ pack_base_uri, pack_id, version, schema_ref });
            _ = hash;
            try branches.append(allocator, .{
                .declared_type = try allocator.dupe(u8, declared_type),
                .ref = ref,
            });
        }
    }

    std.mem.sort(Branch, branches.items, {}, struct {
        fn lessThan(_: void, a: Branch, b: Branch) bool {
            return std.mem.order(u8, a.ref, b.ref) == .lt;
        }
    }.lessThan);

    for (ids.items, 0..) |first, index| {
        for (ids.items[index + 1 ..]) |second| {
            if (std.mem.eql(u8, first, second) or
                underPrefix(first, second) or
                underPrefix(second, first))
            {
                try load_refusals.append(allocator, .{ .code = pack_id_collision, .pack = first, .detail = second });
            }
        }
    }

    if (load_refusals.items.len != 0) {
        return .{
            .arena = arena,
            .branches = &.{},
            .members = &.{},
            .types = &.{},
            .refusals = try load_refusals.toOwnedSlice(allocator),
        };
    }

    return .{
        .arena = arena,
        .branches = try branches.toOwnedSlice(allocator),
        .members = try members.toOwnedSlice(allocator),
        .types = try types.toOwnedSlice(allocator),
        .refusals = &.{},
    };
}

fn gateString(gate: std.json.Value, key: []const u8) []const u8 {
    if (gate != .object) return "";
    const held = gate.object.get(key) orelse return "";
    return if (held == .string) held.string else "";
}

fn typeCapability(gates: ?std.json.Value, name: []const u8) []const u8 {
    const declared = gates orelse return "";
    if (declared != .array) return "";
    for (declared.array.items) |gate| {
        if (std.mem.eql(u8, gateString(gate, "type"), name)) return gateString(gate, "capability");
    }
    return "";
}

fn memberCapability(gates: ?std.json.Value, payload_type: []const u8, name: []const u8) []const u8 {
    const declared = gates orelse return "";
    if (declared != .array) return "";
    for (declared.array.items) |gate| {
        if (std.mem.eql(u8, gateString(gate, "payload_type"), payload_type) and
            std.mem.eql(u8, gateString(gate, "member"), name)) return gateString(gate, "capability");
    }
    return "";
}

fn responseFor(declared_types: std.json.Value, name: []const u8) []const u8 {
    for (declared_types.array.items) |entry| {
        if (entry != .object) continue;
        const role = entry.object.get("role") orelse continue;
        if (role != .string or !std.mem.eql(u8, role.string, "response")) continue;
        const replies = entry.object.get("replies_to") orelse continue;
        if (replies == .string and std.mem.eql(u8, replies.string, name)) {
            const answer = entry.object.get("type") orelse continue;
            if (answer == .string) return answer.string;
        }
    }
    return "";
}

pub const PayloadTarget = struct {
    document: []const u8,
    definition: []const u8,
};

pub fn payloadTarget(
    registry: *const jsonschema.Registry,
    envelope_document: []const u8,
    payload_type: []const u8,
) ?PayloadTarget {
    const definition = registry.definitionCarryingType(envelope_document, payload_type) orelse return null;
    const root = registry.root(envelope_document) orelse return null;
    const target = root.object.get("$defs").?.object.get(definition).?;
    const properties = target.object.get("properties") orelse return null;
    const payload = properties.object.get("payload") orelse return null;
    if (payload != .object) return null;
    const ref = payload.object.get("$ref") orelse return null;
    if (ref != .string) return null;
    const hash = std.mem.indexOfScalar(u8, ref.string, '#') orelse return null;
    const file = ref.string[0..hash];
    const fragment = ref.string[hash + 1 ..];
    const marker = "/$defs/";
    if (!std.mem.startsWith(u8, fragment, marker)) return null;
    return .{
        .document = if (file.len == 0) envelope_document else file,
        .definition = fragment[marker.len..],
    };
}

fn judgesAsAccepted(allocator: std.mem.Allocator, registry: *jsonschema.Registry, branches: []const Branch, line: []const u8) !bool {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
    defer parsed.deinit();
    var validator = jsonschema.Validator.init(allocator, registry);
    defer validator.deinit();
    const failure = try validator.validateWithBranches("envelope.schema.json", parsed.value, branches);
    return failure == null;
}

fn carriesCode(refusals: []const Refusal, code: []const u8) bool {
    for (refusals) |refusal| {
        if (std.mem.eql(u8, refusal.code, code)) return true;
    }
    return false;
}

test "a pack may only declare names inside its own namespace, and the set is prefix-free" {
    const allocator = std.testing.allocator;

    const cases = [_]struct { dirs: []const []const u8, code: []const u8 }{
        .{ .dirs = &.{"fixtures/packs/bad-unprefixed-name"}, .code = pack_unprefixed_name },
        .{ .dirs = &.{"fixtures/packs/bad-foreign-prefix"}, .code = pack_foreign_prefix },
        .{ .dirs = &.{ "fixtures/packs/nested-parent", "fixtures/packs/nested-child" }, .code = pack_id_collision },
        .{ .dirs = &.{ "fixtures/packs/duplicate-a", "fixtures/packs/duplicate-b" }, .code = pack_id_collision },
    };

    for (cases) |case| {
        var loaded = try describe(std.testing.io, allocator, case.dirs);
        defer loaded.deinit();
        try std.testing.expect(carriesCode(loaded.refusals, case.code));
        try std.testing.expectEqual(@as(usize, 0), loaded.branches.len);
        try std.testing.expectEqual(@as(usize, 0), loaded.types.len);
    }

    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.createDir(std.testing.io, "rootcase", .default_dir);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "rootcase/pack.json", .data =
            \\{"id": "com.example.rootcase", "version": "1.0.0", "capability_keys": ["capabilities.request.anything"], "envelope_types": []}
        ,
    });
    const rooted = try tmp.dir.realPathFileAlloc(std.testing.io, "rootcase", allocator);
    defer allocator.free(rooted);
    const one = [_][]const u8{rooted};
    var constructed = try describe(std.testing.io, allocator, &one);
    defer constructed.deinit();
    try std.testing.expect(carriesCode(constructed.refusals, pack_unprefixed_name));

    const storage = [_][]const u8{"fixtures/packs/storage"};
    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();
    var well_formed = try load(std.testing.io, allocator, &registry, &storage);
    defer well_formed.deinit();
    try std.testing.expectEqual(@as(usize, 0), well_formed.refusals.len);

    const complete =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core",
        \\"type":"com.example.storage.objects.read","id":"e1","session_id":"s",
        \\"payload":{"session_id":"s","bucket":"b","key":"k"}}
    ;
    const short =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core",
        \\"type":"com.example.storage.objects.read","id":"e1","session_id":"s",
        \\"payload":{"session_id":"s","key":"k"}}
    ;
    try std.testing.expect(try judgesAsAccepted(allocator, &registry, well_formed.branches, complete));
    try std.testing.expect(!try judgesAsAccepted(allocator, &registry, well_formed.branches, short));
}
