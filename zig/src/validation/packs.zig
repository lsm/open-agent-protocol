const std = @import("std");
const jsonschema = @import("jsonschema");

pub const pack_base_uri = "https://open-agent-protocol.local/ext/";

pub const Branch = struct {
    declared_type: []const u8,
    ref: []const u8,
};

pub const Member = struct {
    payload_type: []const u8,
    name: []const u8,
    schema: std.json.Value,
};

pub const Loaded = struct {
    arena: std.heap.ArenaAllocator,
    branches: []Branch,
    members: []Member,

    pub fn deinit(self: *Loaded) void {
        self.arena.deinit();
    }
};

fn readAll(allocator: std.mem.Allocator, path: []const u8) ![]u8 {
    return std.Io.Dir.cwd().readFileAlloc(std.testing.io, path, allocator, .limited(8 * 1024 * 1024));
}

pub fn load(
    parent: std.mem.Allocator,
    registry: *jsonschema.Registry,
    pack_dirs: []const []const u8,
) !Loaded {
    var arena = std.heap.ArenaAllocator.init(parent);
    errdefer arena.deinit();
    const allocator = arena.allocator();

    var branches = std.ArrayList(Branch).empty;
    var members = std.ArrayList(Member).empty;

    for (pack_dirs) |dir| {
        const descriptor_path = try std.fs.path.join(allocator, &.{ dir, "pack.json" });
        const descriptor_bytes = try readAll(allocator, descriptor_path);
        const descriptor = try std.json.parseFromSliceLeaky(std.json.Value, allocator, descriptor_bytes, .{});
        if (descriptor != .object) return error.InvalidPackDescriptor;
        const pack_id = (descriptor.object.get("id") orelse return error.InvalidPackDescriptor).string;
        const version = (descriptor.object.get("version") orelse return error.InvalidPackDescriptor).string;

        if (descriptor.object.get("schemas")) |schemas| {
            for (schemas.array.items) |schema_name| {
                const file = schema_name.string;
                const schema_path = try std.fs.path.join(allocator, &.{ dir, file });
                const schema_bytes = try readAll(allocator, schema_path);
                const key = try std.fmt.allocPrint(allocator, "{s}{s}/{s}/{s}", .{ pack_base_uri, pack_id, version, file });
                try registry.addDocument(key, schema_bytes);
            }
        }

        if (descriptor.object.get("payload_members")) |declared_members| {
            for (declared_members.array.items) |entry| {
                try members.append(allocator, .{
                    .payload_type = (entry.object.get("payload_type") orelse continue).string,
                    .name = (entry.object.get("member") orelse continue).string,
                    .schema = entry.object.get("schema") orelse continue,
                });
            }
        }

        const declared = descriptor.object.get("envelope_types") orelse continue;
        for (declared.array.items) |entry| {
            const declared_type = (entry.object.get("type") orelse continue).string;
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

    return .{
        .arena = arena,
        .branches = try branches.toOwnedSlice(allocator),
        .members = try members.toOwnedSlice(allocator),
    };
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
