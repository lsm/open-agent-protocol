const std = @import("std");
const jsonschema = @import("jsonschema");

pub const Branch = struct {
    declared_type: []const u8,
    ref: []const u8,
};

pub const Loaded = struct {
    arena: std.heap.ArenaAllocator,
    branches: []Branch,

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

    for (pack_dirs) |dir| {
        const descriptor_path = try std.fs.path.join(allocator, &.{ dir, "pack.json" });
        const descriptor_bytes = try readAll(allocator, descriptor_path);
        const descriptor = try std.json.parseFromSliceLeaky(std.json.Value, allocator, descriptor_bytes, .{});
        if (descriptor != .object) return error.InvalidPackDescriptor;
        const pack_id = (descriptor.object.get("id") orelse return error.InvalidPackDescriptor).string;

        if (descriptor.object.get("schemas")) |schemas| {
            for (schemas.array.items) |schema_name| {
                const file = schema_name.string;
                const schema_path = try std.fs.path.join(allocator, &.{ dir, file });
                const schema_bytes = try readAll(allocator, schema_path);
                const key = try std.fmt.allocPrint(allocator, "{s}/{s}", .{ pack_id, file });
                try registry.addDocument(key, schema_bytes);
            }
        }

        const declared = descriptor.object.get("envelope_types") orelse continue;
        for (declared.array.items) |entry| {
            const declared_type = (entry.object.get("type") orelse continue).string;
            const schema_ref = (entry.object.get("schema") orelse continue).string;
            const hash = std.mem.indexOfScalar(u8, schema_ref, '#') orelse continue;
            const ref = try std.fmt.allocPrint(allocator, "{s}/{s}", .{ pack_id, schema_ref });
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

    return .{ .arena = arena, .branches = try branches.toOwnedSlice(allocator) };
}
