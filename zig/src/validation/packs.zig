const std = @import("std");
const jsonschema = @import("jsonschema");

pub const pack_base_uri = "https://open-agent-protocol.local/ext/";

pub const Branch = jsonschema.Alternative;

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

    pub fn deinit(self: *Loaded) void {
        self.arena.deinit();
    }

    pub fn empty(child: std.mem.Allocator) Loaded {
        return .{
            .arena = std.heap.ArenaAllocator.init(child),
            .branches = &.{},
            .members = &.{},
            .types = &.{},
        };
    }
};

fn field(value: std.json.Value, key: []const u8) ?std.json.Value {
    if (value != .object) return null;
    return value.object.get(key);
}

fn stringField(value: std.json.Value, key: []const u8) ?[]const u8 {
    const held = field(value, key) orelse return null;
    return if (held == .string) held.string else null;
}

fn arrayField(value: std.json.Value, key: []const u8) ?std.json.Array {
    const held = field(value, key) orelse return null;
    return if (held == .array) held.array else null;
}

fn asString(value: std.json.Value) ?[]const u8 {
    return if (value == .string) value.string else null;
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
    var identities = std.StringHashMap(void).init(allocator);

    for (pack_dirs) |dir| {
        const descriptor_path = try std.fs.path.join(allocator, &.{ dir, "pack.json" });
        const descriptor_bytes = try readAll(io, allocator, descriptor_path);
        const descriptor = try std.json.parseFromSliceLeaky(std.json.Value, allocator, descriptor_bytes, .{});
        if (descriptor != .object) return error.InvalidPackDescriptor;
        const pack_id = stringField(descriptor, "id") orelse return error.InvalidPackDescriptor;
        const version = stringField(descriptor, "version") orelse return error.InvalidPackDescriptor;
        const identity = try std.fmt.allocPrint(allocator, "{s}@{s}", .{ pack_id, version });
        if (identities.contains(identity)) continue;
        try identities.put(identity, {});

        if (registry) |target| {
            if (arrayField(descriptor, "schemas")) |schemas| {
                for (schemas.items) |schema_name| {
                    const file = asString(schema_name) orelse return error.InvalidPackDescriptor;
                    const schema_path = try std.fs.path.join(allocator, &.{ dir, file });
                    const schema_bytes = try readAll(io, allocator, schema_path);
                    const key = try std.fmt.allocPrint(allocator, "{s}{s}/{s}/{s}", .{ pack_base_uri, pack_id, version, file });
                    try target.addDocument(key, schema_bytes);
                }
            }
        }

        const gates = field(descriptor, "gates");

        if (arrayField(descriptor, "payload_members")) |declared_members| {
            for (declared_members.items) |entry| {
                const payload_type = stringField(entry, "payload_type") orelse continue;
                const name = stringField(entry, "member") orelse continue;
                const member_schema = field(entry, "schema") orelse continue;
                if (member_schema != .object) continue;
                try members.append(allocator, .{
                    .payload_type = payload_type,
                    .name = name,
                    .schema = member_schema,
                    .capability = memberCapability(gates, payload_type, name),
                });
            }
        }

        if (arrayField(descriptor, "envelope_types")) |declared_types| {
            var seen_types = std.StringHashMap(void).init(allocator);
            for (declared_types.items) |entry| {
                const name = stringField(entry, "type") orelse continue;
                if (seen_types.contains(name)) return error.InvalidPackDescriptor;
                try seen_types.put(name, {});
                var refusals = std.ArrayList([]const u8).empty;
                if (arrayField(entry, "refusals")) |listed| {
                    for (listed.items) |code| {
                        if (asString(code)) |held| try refusals.append(allocator, held);
                    }
                }
                try types.append(allocator, .{
                    .name = name,
                    .role = stringField(entry, "role") orelse "",
                    .capability = typeCapability(gates, name),
                    .response = responseFor(.{ .array = declared_types }, name),
                    .refusals = try refusals.toOwnedSlice(allocator),
                });
            }
        }

        const declared = arrayField(descriptor, "envelope_types") orelse continue;
        for (declared.items) |entry| {
            const declared_type = stringField(entry, "type") orelse continue;
            const schema_ref = stringField(entry, "schema") orelse continue;
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

    return .{
        .arena = arena,
        .branches = try branches.toOwnedSlice(allocator),
        .members = try members.toOwnedSlice(allocator),
        .types = try types.toOwnedSlice(allocator),
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
