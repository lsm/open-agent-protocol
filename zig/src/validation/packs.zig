const std = @import("std");
const jsonschema = @import("jsonschema");
const schema_bundle = @import("schema_bytes");

pub const pack_base_uri = "https://open-agent-protocol.local/ext/";

pub const Branch = jsonschema.Alternative;

pub const pack_unprefixed_name = "pack_unprefixed_name";
pub const pack_foreign_prefix = "pack_foreign_prefix";
pub const pack_id_collision = "pack_id_collision";
pub const pack_branch_unpinned = "pack_branch_unpinned";
pub const pack_branch_undeclared_type = "pack_branch_undeclared_type";

pub const Refusal = struct {
    code: []const u8,
    pack: []const u8,
    detail: []const u8 = "",
};

const Document = struct {
    name: []const u8,
    value: std.json.Value,
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

    pub fn empty(child: std.mem.Allocator) Loaded {
        return .{
            .arena = std.heap.ArenaAllocator.init(child),
            .branches = &.{},
            .members = &.{},
            .types = &.{},
        };
    }
};

fn beneath(root: []const u8, path: []const u8) bool {
    var base = root;
    while (base.len > 1 and base[base.len - 1] == std.Io.Dir.path.sep) base = base[0 .. base.len - 1];
    if (std.mem.eql(u8, base, path)) return true;
    if (!std.mem.startsWith(u8, path, base)) return false;
    if (base.len == 1 and base[0] == std.Io.Dir.path.sep) return true;
    return path[base.len] == std.Io.Dir.path.sep;
}

fn cleanRelative(allocator: std.mem.Allocator, entry: []const u8) !?[]const u8 {
    var parts: std.ArrayList([]const u8) = .empty;
    defer {
        for (parts.items) |part| allocator.free(part);
        parts.deinit(allocator);
    }
    var start: usize = 0;
    var index: usize = 0;
    while (index <= entry.len) : (index += 1) {
        if (index < entry.len and !std.Io.Dir.path.isSep(entry[index])) continue;
        const part = entry[start..index];
        start = index + 1;
        if (part.len == 0 or std.mem.eql(u8, part, ".")) continue;
        if (std.mem.eql(u8, part, "..")) {
            if (parts.items.len == 0) return null;
            allocator.free(parts.pop().?);
            continue;
        }
        const held = try allocator.dupe(u8, part);
        errdefer allocator.free(held);
        try parts.append(allocator, held);
    }
    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(allocator);
    for (parts.items, 0..) |part, position| {
        if (position != 0) try out.append(allocator, std.Io.Dir.path.sep);
        try out.appendSlice(allocator, part);
    }
    return try out.toOwnedSlice(allocator);
}

fn lexicalRelative(allocator: std.mem.Allocator, entry: []const u8) ![]const u8 {
    if (entry.len == 0 or std.Io.Dir.path.isAbsolute(entry)) return error.InvalidPackDescriptor;
    return (try cleanRelative(allocator, entry)) orelse error.InvalidPackDescriptor;
}

fn toSlash(allocator: std.mem.Allocator, name: []const u8) ![]const u8 {
    if (std.mem.indexOfScalar(u8, name, std.Io.Dir.path.sep) == null) return name;
    const slashed = try allocator.dupe(u8, name);
    for (slashed) |*byte| {
        if (byte.* == std.Io.Dir.path.sep) byte.* = '/';
    }
    return slashed;
}

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

fn unescapeToken(allocator: std.mem.Allocator, token: []const u8) ![]const u8 {
    if (std.mem.indexOfScalar(u8, token, '~') == null) return token;
    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(allocator);
    var index: usize = 0;
    while (index < token.len) : (index += 1) {
        if (token[index] == '~' and index + 1 < token.len) {
            if (token[index + 1] == '1') {
                try out.append(allocator, '/');
                index += 1;
                continue;
            }
            if (token[index + 1] == '0') {
                try out.append(allocator, '~');
                index += 1;
                continue;
            }
        }
        try out.append(allocator, token[index]);
    }
    return try out.toOwnedSlice(allocator);
}

fn pointerAt(allocator: std.mem.Allocator, root: std.json.Value, pointer: []const u8) !?std.json.Value {
    if (pointer.len == 0 or std.mem.eql(u8, pointer, "#")) return root;
    if (!std.mem.startsWith(u8, pointer, "#/")) return null;
    var current = root;
    var rest = pointer[2..];
    while (true) {
        const slash = std.mem.indexOfScalar(u8, rest, '/');
        const raw = if (slash) |at| rest[0..at] else rest;
        rest = if (slash) |at| rest[at + 1 ..] else "";
        const token = try unescapeToken(allocator, raw);
        if (current != .object) return null;
        current = current.object.get(token) orelse return null;
        if (rest.len == 0) return current;
    }
}

fn pinnedTypeOf(branch: std.json.Value) ?[]const u8 {
    if (branch != .object) return null;
    const properties = branch.object.get("properties") orelse return null;
    if (properties != .object) return null;
    const type_schema = properties.object.get("type") orelse return null;
    if (type_schema != .object) return null;
    const held = type_schema.object.get("const") orelse return null;
    return if (held == .string) held.string else null;
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
    var named = std.StringHashMap(void).init(allocator);
    var load_refusals = std.ArrayList(Refusal).empty;
    var ids = std.ArrayList([]const u8).empty;
    var roots: ?std.StringHashMap(void) = null;

    for (pack_dirs) |dir| {
        const descriptor_path = try std.fs.path.join(allocator, &.{ dir, "pack.json" });
        const descriptor_bytes = try readAll(io, allocator, descriptor_path);
        const descriptor = try std.json.parseFromSliceLeaky(std.json.Value, allocator, descriptor_bytes, .{});
        if (descriptor != .object) return error.InvalidPackDescriptor;
        const pack_id = stringField(descriptor, "id") orelse return error.InvalidPackDescriptor;
        const version = stringField(descriptor, "version") orelse return error.InvalidPackDescriptor;
        const canonical = std.Io.Dir.cwd().realPathFileAlloc(io, dir, allocator) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => dir,
        };
        if (named.contains(canonical)) continue;
        try named.put(canonical, {});
        try ids.append(allocator, pack_id);

        if (field(descriptor, "capability_keys")) |keys| {
            if (keys == .array) {
                for (keys.array.items) |key| {
                    if (key != .string) return error.InvalidPackDescriptor;
                    if (try namespaceRefusal(&roots, allocator, pack_id, key.string)) |code| {
                        try load_refusals.append(allocator, .{ .code = code, .pack = pack_id, .detail = key.string });
                    }
                }
            }
        }

        if (field(descriptor, "error_codes")) |codes| {
            if (codes == .array) {
                for (codes.array.items) |code| {
                    if (code != .string) return error.InvalidPackDescriptor;
                    if (try namespaceRefusal(&roots, allocator, pack_id, code.string)) |refused| {
                        try load_refusals.append(allocator, .{ .code = refused, .pack = pack_id, .detail = code.string });
                    }
                }
            }
        }

            const declared_schemas = field(descriptor, "schemas");
            if (declared_schemas) |held| {
                if (held != .array) return error.InvalidPackDescriptor;
            }
            var pack_documents: std.ArrayList(Document) = .empty;
            if (declared_schemas) |held| {
                const schemas = held.array;
                const pack_root = std.Io.Dir.cwd().realPathFileAlloc(io, dir, allocator) catch |err| switch (err) {
                    error.OutOfMemory => return error.OutOfMemory,
                    else => return error.InvalidPackDescriptor,
                };
                const entries = schemas.items;
                const names = try allocator.alloc([]const u8, entries.len);
                const paths = try allocator.alloc([]const u8, entries.len);
                for (entries, 0..) |schema_name, index| {
                    if (schema_name != .string) return error.InvalidPackDescriptor;
                    const file = schema_name.string;
                    if (file.len == 0 or std.Io.Dir.path.isAbsolute(file)) return error.InvalidPackDescriptor;
                    const relative = try lexicalRelative(allocator, file);
                    const schema_path = try std.fs.path.join(allocator, &.{ dir, relative });
                    const resolved = std.Io.Dir.cwd().realPathFileAlloc(io, schema_path, allocator) catch |err| switch (err) {
                        error.OutOfMemory => return error.OutOfMemory,
                        else => return error.InvalidPackDescriptor,
                    };
                    if (!beneath(pack_root, resolved)) return error.InvalidPackDescriptor;
                    names[index] = relative;
                    paths[index] = resolved;
                }
                for (0..entries.len) |index| {
                    const schema_bytes = try readAll(io, allocator, paths[index]);
                    const normalized = try toSlash(allocator, names[index]);
                    const key = try std.fmt.allocPrint(allocator, "{s}{s}/{s}/{s}", .{ pack_base_uri, pack_id, version, normalized });
                    try pack_documents.append(allocator, .{
                        .name = normalized,
                        .value = try std.json.parseFromSliceLeaky(std.json.Value, allocator, schema_bytes, .{}),
                    });
                    if (registry) |target| {
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
            if (try namespaceRefusal(&roots, allocator, pack_id, declared_type)) |code| {
                try load_refusals.append(allocator, .{ .code = code, .pack = pack_id, .detail = declared_type });
                continue;
            }
            const schema_ref = stringField(entry, "schema") orelse continue;
            const hash = std.mem.indexOfScalar(u8, schema_ref, '#');
            const spelled = if (hash) |at| schema_ref[0..at] else schema_ref;
            const cleaned = (try cleanRelative(allocator, spelled)) orelse spelled;
            const cited = try toSlash(allocator, cleaned);
            const pointer: []const u8 = if (hash) |at| schema_ref[at..] else "";
            var resolved: ?std.json.Value = null;
            for (pack_documents.items) |document| {
                if (!std.mem.eql(u8, document.name, cited)) continue;
                resolved = try pointerAt(allocator, document.value, pointer);
                break;
            }
            if (resolved == null or resolved.? != .object) {
                try load_refusals.append(allocator, .{ .code = "", .pack = pack_id, .detail = declared_type });
                continue;
            }
            const pinned = pinnedTypeOf(resolved.?);
            if (pinned == null) {
                try load_refusals.append(allocator, .{ .code = pack_branch_unpinned, .pack = pack_id, .detail = declared_type });
                continue;
            }
            if (!std.mem.eql(u8, pinned.?, declared_type)) {
                try load_refusals.append(allocator, .{ .code = pack_branch_undeclared_type, .pack = pack_id, .detail = declared_type });
                continue;
            }
            const ref = try std.fmt.allocPrint(allocator, "{s}{s}/{s}/{s}{s}", .{ pack_base_uri, pack_id, version, cited, pointer });
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

test "a loaded pack's schemas are registered under keys its own refs resolve" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    for ([_][]const u8{ "staying", "aliased", "cleaned", "nested", "literal" }) |name| {
        try tmp.dir.createDir(std.testing.io, name, .default_dir);
    }
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "staying/note.schema.json", .data =
            \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type", "session_id"], "properties": {"type": {"const": "com.example.ok.thing"}, "session_id": {"type": "string"}}}}}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "staying/pack.json", .data =
            \\{"id": "com.example.ok", "version": "1.0.0", "schemas": ["note.schema.json"], "envelope_types": [{"type": "com.example.ok.thing", "role": "event", "schema": "note.schema.json#/$defs/thing"}]}
        ,
    });
    try tmp.dir.symLink(std.testing.io, "note.schema.json", "aliased/alias.schema.json", .{ .is_directory = false });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "aliased/note.schema.json", .data =
            \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type", "session_id"], "properties": {"type": {"const": "com.example.alias.thing"}, "session_id": {"type": "string"}}}}}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "aliased/pack.json", .data =
            \\{"id": "com.example.alias", "version": "1.0.0", "schemas": ["alias.schema.json"], "envelope_types": [{"type": "com.example.alias.thing", "role": "event", "schema": "alias.schema.json#/$defs/thing"}]}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "cleaned/note.schema.json", .data =
            \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type", "session_id"], "properties": {"type": {"const": "com.example.clean.thing"}, "session_id": {"type": "string"}}}}}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "cleaned/pack.json", .data =
            \\{"id": "com.example.clean", "version": "1.0.0", "schemas": ["sub/../note.schema.json"], "envelope_types": [{"type": "com.example.clean.thing", "role": "event", "schema": "sub/../note.schema.json#/$defs/thing"}]}
        ,
    });

    try tmp.dir.createDir(std.testing.io, "nested/sub", .default_dir);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "nested/sub/note.schema.json", .data =
            \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type", "session_id"], "properties": {"type": {"const": "com.example.nested.thing"}, "session_id": {"type": "string"}}}}}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "nested/pack.json", .data =
            \\{"id": "com.example.nested", "version": "1.0.0", "schemas": ["sub/note.schema.json"], "envelope_types": [{"type": "com.example.nested.thing", "role": "event", "schema": "sub/note.schema.json#/$defs/thing"}]}
        ,
    });

    if (comptime std.Io.Dir.path.sep == '/') {
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "literal/back\\slash.schema.json", .data =
                \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type", "session_id"], "properties": {"type": {"const": "com.example.literal.thing"}, "session_id": {"type": "string"}}}}}
            ,
        });
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "literal/pack.json", .data =
                \\{"id": "com.example.literal", "version": "1.0.0", "schemas": ["back\\slash.schema.json"], "envelope_types": [{"type": "com.example.literal.thing", "role": "event", "schema": "back\\slash.schema.json#/$defs/thing"}]}
            ,
        });
    }

    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();

    const Case = struct { dir: []const u8, declared: []const u8 };
    const portable = [_]Case{
        .{ .dir = "staying", .declared = "com.example.ok.thing" },
        .{ .dir = "aliased", .declared = "com.example.alias.thing" },
        .{ .dir = "cleaned", .declared = "com.example.clean.thing" },
        .{ .dir = "nested", .declared = "com.example.nested.thing" },
    };
    const with_literal = portable ++ [_]Case{
        .{ .dir = "literal", .declared = "com.example.literal.thing" },
    };
    const cases = if (comptime std.Io.Dir.path.sep == '/') with_literal else portable;

    for (cases) |case| {
        const root = try tmp.dir.realPathFileAlloc(std.testing.io, case.dir, allocator);
        defer allocator.free(root);
        const one = [_][]const u8{root};
        var read = try load(std.testing.io, allocator, &registry, &one);
        defer read.deinit();
        try std.testing.expectEqual(@as(usize, 1), read.branches.len);

        const complete = try std.fmt.allocPrint(allocator, "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"{s}\",\"id\":\"e1\",\"session_id\":\"s\",\"payload\":{{}}}}", .{case.declared});
        defer allocator.free(complete);
        const short = try std.fmt.allocPrint(allocator, "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"{s}\",\"id\":\"e1\",\"payload\":{{}}}}", .{case.declared});
        defer allocator.free(short);

        try std.testing.expect(try judgesAsAccepted(allocator, &registry, read.branches, complete));
        try std.testing.expect(!try judgesAsAccepted(allocator, &registry, read.branches, short));
    }

    if (comptime std.Io.Dir.path.sep == '/') {
        try std.testing.expect(registry.root("https://open-agent-protocol.local/ext/com.example.literal/1.0.0/back\\slash.schema.json") != null);
    }
    try std.testing.expect(registry.root("https://open-agent-protocol.local/ext/com.example.nested/1.0.0/sub/note.schema.json") != null);
    try std.testing.expect(registry.root("https://open-agent-protocol.local/ext/com.example.nested/1.0.0/sub\\note.schema.json") == null);
}

test "a descriptor schema path is read only when it lands beneath the pack root" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    for ([_][]const u8{ "upward", "absolute", "absoluteinside", "linked", "backlink", "preflight" }) |name| {
        try tmp.dir.createDir(std.testing.io, name, .default_dir);
    }
    try tmp.dir.createDir(std.testing.io, "outside", .default_dir);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "outside/away.schema.json", .data =
            \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type"], "properties": {"type": {"const": "com.example.ok.thing"}}}}}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "upward/pack.json", .data =
            \\{"id": "com.example.up", "version": "1.0.0", "schemas": ["../outside/away.schema.json"], "envelope_types": []}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "absolute/pack.json", .data =
            \\{"id": "com.example.abs", "version": "1.0.0", "schemas": ["/etc/hosts"], "envelope_types": []}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "absoluteinside/note.schema.json", .data =
            \\{"$schema": "https://json-schema.org/draft/2020-12/schema"}
        ,
    });
    const absolute_root = try tmp.dir.realPathFileAlloc(std.testing.io, "absoluteinside", allocator);
    defer allocator.free(absolute_root);
    const absolute_inside = try std.fmt.allocPrint(allocator, "{{\"id\":\"com.example.absin\",\"version\":\"1.0.0\",\"schemas\":[\"{s}/note.schema.json\"],\"envelope_types\":[]}}", .{absolute_root});
    defer allocator.free(absolute_inside);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "absoluteinside/pack.json", .data = absolute_inside });
    try tmp.dir.symLink(std.testing.io, "../outside/away.schema.json", "linked/away.schema.json", .{ .is_directory = false });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "linked/pack.json", .data =
            \\{"id": "com.example.out", "version": "1.0.0", "schemas": ["away.schema.json"], "envelope_types": []}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "backlink/note.schema.json", .data =
            \\{"$schema": "https://json-schema.org/draft/2020-12/schema"}
        ,
    });
    try tmp.dir.symLink(std.testing.io, "backlink", "sym", .{ .is_directory = true });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "backlink/pack.json", .data =
            \\{"id": "com.example.back", "version": "1.0.0", "schemas": ["../sym/note.schema.json"], "envelope_types": []}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "preflight/note.schema.json", .data =
            \\{"$schema": "https://json-schema.org/draft/2020-12/schema"}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "preflight/pack.json", .data =
            \\{"id": "com.example.pre", "version": "1.0.0", "schemas": ["note.schema.json", "../outside/away.schema.json"], "envelope_types": []}
        ,
    });

    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();

    for ([_][]const u8{ "upward", "absolute", "absoluteinside", "linked", "backlink", "preflight" }) |name| {
        const dir = try tmp.dir.realPathFileAlloc(std.testing.io, name, allocator);
        defer allocator.free(dir);
        const escaping = [_][]const u8{dir};
        try std.testing.expectError(error.InvalidPackDescriptor, load(std.testing.io, allocator, &registry, &escaping));
    }

    try std.testing.expect(registry.root("https://open-agent-protocol.local/ext/com.example.pre/1.0.0/note.schema.json") == null);

    try tmp.dir.createDir(std.testing.io, "malformed", .default_dir);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "malformed/pack.json", .data =
            \\{"id": "com.example.malformed", "version": "1.0.0", "schemas": "note.schema.json", "envelope_types": []}
        ,
    });
    const malformed_root = try tmp.dir.realPathFileAlloc(std.testing.io, "malformed", allocator);
    defer allocator.free(malformed_root);
    const malformed = [_][]const u8{malformed_root};
    try std.testing.expectError(error.InvalidPackDescriptor, load(std.testing.io, allocator, &registry, &malformed));
}

test "a schema ref that resolves to nothing is refused, and the same ref made to resolve is accepted" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const cases = [_]struct { dir: []const u8, ref: []const u8, code: []const u8 }{
        .{ .dir = "resolves", .ref = "note.schema.json#/$defs/thing", .code = "" },
        .{ .dir = "absent-file", .ref = "absent.schema.json#/$defs/thing", .code = "" },
        .{ .dir = "absent-pointer", .ref = "note.schema.json#/$defs/absent", .code = "" },
        .{ .dir = "no-fragment", .ref = "note.schema.json", .code = pack_branch_unpinned },
    };
    for (cases) |c| {
        try tmp.dir.createDir(std.testing.io, c.dir, .default_dir);
        const name = try std.fmt.allocPrint(allocator, "{s}/note.schema.json", .{c.dir});
        defer allocator.free(name);
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = name, .data =
                \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type", "id", "session_id"], "properties": {"type": {"const": "com.example.ref.thing"}, "id": {"type": "string"}, "session_id": {"type": "string"}}}}}
            ,
        });
        const descriptor = try std.fmt.allocPrint(allocator,
            \\{{"id": "com.example.ref", "version": "1.0.0", "schemas": ["note.schema.json"], "envelope_types": [{{"type": "com.example.ref.thing", "role": "event", "schema": "{s}"}}]}}
        , .{c.ref});
        defer allocator.free(descriptor);
        const pack = try std.fmt.allocPrint(allocator, "{s}/pack.json", .{c.dir});
        defer allocator.free(pack);
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = pack, .data = descriptor });
    }
    for (cases) |c| {
        var registry = try jsonschema.Registry.initFromBundled(allocator);
        defer registry.deinit();
        const root = try tmp.dir.realPathFileAlloc(std.testing.io, c.dir, allocator);
        defer allocator.free(root);
        const one = [_][]const u8{root};
        var read = try load(std.testing.io, allocator, &registry, &one);
        defer read.deinit();
        if (c.code.len == 0 and c.dir[0] == 'r') {
            try std.testing.expectEqual(@as(usize, 0), read.refusals.len);
            try std.testing.expectEqual(@as(usize, 1), read.branches.len);
            const key = try std.fmt.allocPrint(allocator, "{s}{s}/{s}/{s}", .{ pack_base_uri, "com.example.ref", "1.0.0", "note.schema.json" });
            defer allocator.free(key);
            const envelope =
                \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"com.example.ref.thing","id":"e1","session_id":"s","payload":{}}
            ;
            const line = try std.fmt.allocPrint(allocator, "{s}", .{read.branches[0].ref});
            defer allocator.free(line);
            try std.testing.expect(std.mem.startsWith(u8, line, key));
            try std.testing.expect(try judgesAsAccepted(allocator, &registry, read.branches, envelope));
        } else {
            try std.testing.expectEqual(@as(usize, 1), read.refusals.len);
            try std.testing.expectEqualStrings(c.code, read.refusals[0].code);
            try std.testing.expectEqualStrings("com.example.ref", read.refusals[0].pack);
            try std.testing.expectEqual(@as(usize, 0), read.branches.len);
        }
    }
}

test "a branch pointer resolves through objects only, as goap's loader resolves it" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const Outcome = enum { accepted, uncoded, coded };
    const cases = [_]struct { dir: []const u8, ref: []const u8, outcome: Outcome, code: []const u8 }{
        .{ .dir = "object", .ref = "types.schema.json#/$defs/thing", .outcome = .accepted, .code = "" },
        .{ .dir = "array-index", .ref = "types.schema.json#/$defs/arr/0", .outcome = .uncoded, .code = "" },
        .{ .dir = "array-escaped", .ref = "types.schema.json#/$defs/deep~1arr/0", .outcome = .uncoded, .code = "" },
        .{ .dir = "array-parent", .ref = "types.schema.json#/$defs/list/0", .outcome = .uncoded, .code = "" },
        .{ .dir = "string-node", .ref = "types.schema.json#/$schema", .outcome = .uncoded, .code = "" },
        .{ .dir = "const-node", .ref = "types.schema.json#/$defs/thing/properties/type/const", .outcome = .uncoded, .code = "" },
        .{ .dir = "absent-node", .ref = "types.schema.json#/$defs/absent", .outcome = .uncoded, .code = "" },
        .{ .dir = "whole-document", .ref = "types.schema.json", .outcome = .coded, .code = pack_branch_unpinned },
        .{ .dir = "object-no-const", .ref = "types.schema.json#/$defs/plain", .outcome = .coded, .code = pack_branch_unpinned },
    };
    const document =
        \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type", "id", "session_id"], "properties": {"type": {"const": "com.example.ptr.thing"}, "id": {"type": "string"}, "session_id": {"type": "string"}}}, "plain": {"type": "object", "required": ["type"], "properties": {"type": {"type": "string"}}}, "arr": [{"type": "object", "required": ["type", "id", "session_id"], "properties": {"type": {"const": "com.example.ptr.thing"}, "id": {"type": "string"}, "session_id": {"type": "string"}}}], "deep/arr": [{"type": "object", "required": ["type"], "properties": {"type": {"const": "com.example.ptr.thing"}}}], "list": {"type": "array", "items": {"$ref": "#/$defs/thing"}}}}
    ;
    for (cases) |c| {
        try tmp.dir.createDir(std.testing.io, c.dir, .default_dir);
        const document_path = try std.fmt.allocPrint(allocator, "{s}/types.schema.json", .{c.dir});
        defer allocator.free(document_path);
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = document_path, .data = document });
        const descriptor = try std.fmt.allocPrint(allocator,
            \\{{"id": "com.example.ptr", "version": "1.0.0", "schemas": ["types.schema.json"], "envelope_types": [{{"type": "com.example.ptr.thing", "role": "event", "schema": "{s}"}}]}}
        , .{c.ref});
        defer allocator.free(descriptor);
        const pack = try std.fmt.allocPrint(allocator, "{s}/pack.json", .{c.dir});
        defer allocator.free(pack);
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = pack, .data = descriptor });
    }
    for (cases) |c| {
        var registry = try jsonschema.Registry.initFromBundled(allocator);
        defer registry.deinit();
        const root = try tmp.dir.realPathFileAlloc(std.testing.io, c.dir, allocator);
        defer allocator.free(root);
        const one = [_][]const u8{root};
        var read = try load(std.testing.io, allocator, &registry, &one);
        defer read.deinit();
        switch (c.outcome) {
            .accepted => {
                try std.testing.expectEqual(@as(usize, 0), read.refusals.len);
                try std.testing.expectEqual(@as(usize, 1), read.branches.len);
            },
            .uncoded => {
                try std.testing.expectEqual(@as(usize, 1), read.refusals.len);
                try std.testing.expectEqualStrings("", read.refusals[0].code);
                try std.testing.expectEqual(@as(usize, 0), read.branches.len);
            },
            .coded => {
                try std.testing.expectEqual(@as(usize, 1), read.refusals.len);
                try std.testing.expectEqualStrings(c.code, read.refusals[0].code);
                try std.testing.expectEqual(@as(usize, 0), read.branches.len);
            },
        }
    }
}
test "a cited name is cleaned and its pointer unescaped, so the ref matches the key it registered" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.createDir(std.testing.io, "sub", .default_dir);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "sub/note.schema.json", .data =
            \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"odd/name~here": {"type": "object", "required": ["type", "id", "session_id"], "properties": {"type": {"const": "com.example.escape.thing"}, "id": {"type": "string"}, "session_id": {"type": "string"}}}}}
        ,
    });
    const descriptor = try std.fmt.allocPrint(allocator,
        \\{{"id": "com.example.escape", "version": "1.0.0", "schemas": ["./note.schema.json"], "envelope_types": [{{"type": "com.example.escape.thing", "role": "event", "schema": "./note.schema.json#/$defs/odd~1name~0here"}}]}}
    , .{});
    defer allocator.free(descriptor);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "sub/pack.json", .data = descriptor });

    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();
    const root = try tmp.dir.realPathFileAlloc(std.testing.io, "sub", allocator);
    defer allocator.free(root);
    const one = [_][]const u8{root};
    var read = try load(std.testing.io, allocator, &registry, &one);
    defer read.deinit();
    try std.testing.expectEqual(@as(usize, 0), read.refusals.len);
    try std.testing.expectEqual(@as(usize, 1), read.branches.len);
    const key = try std.fmt.allocPrint(allocator, "{s}{s}/{s}/{s}", .{ pack_base_uri, "com.example.escape", "1.0.0", "note.schema.json" });
    defer allocator.free(key);
    try std.testing.expect(std.mem.startsWith(u8, read.branches[0].ref, key));
    try std.testing.expect(registry.root(key) != null);
    const envelope =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"com.example.escape.thing","id":"e1","session_id":"s","payload":{}}
    ;
    try std.testing.expect(try judgesAsAccepted(allocator, &registry, read.branches, envelope));
}

test "one unpinned branch in a pack takes the pack's pinned branch with it" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const cases = [_]struct { dir: []const u8, types: []const u8, refused: bool }{
        .{ .dir = "mixed", .types =
        \\[
        \\  {"type": "com.example.mixed.good", "role": "event", "schema": "types.schema.json#/$defs/good"},
        \\  {"type": "com.example.mixed.bad", "role": "event", "schema": "types.schema.json#/$defs/plain"}
        \\]
        , .refused = true },
        .{ .dir = "both-pinned", .types =
        \\[
        \\  {"type": "com.example.mixed.good", "role": "event", "schema": "types.schema.json#/$defs/good"},
        \\  {"type": "com.example.mixed.other", "role": "event", "schema": "types.schema.json#/$defs/other"}
        \\]
        , .refused = false },
    };
    const members =
        \\[{"payload_type": "com.example.mixed.good", "member": "note", "schema": {"type": "string"}}]
    ;
    const document =
        \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"good": {"type": "object", "required": ["type", "id", "session_id"], "properties": {"type": {"const": "com.example.mixed.good"}, "id": {"type": "string"}, "session_id": {"type": "string"}}}, "other": {"type": "object", "required": ["type", "id", "session_id"], "properties": {"type": {"const": "com.example.mixed.other"}, "id": {"type": "string"}, "session_id": {"type": "string"}}}, "plain": {"type": "object", "required": ["type"], "properties": {"type": {"type": "string"}}}}}
    ;
    for (cases) |c| {
        try tmp.dir.createDir(std.testing.io, c.dir, .default_dir);
        const document_path = try std.fmt.allocPrint(allocator, "{s}/types.schema.json", .{c.dir});
        defer allocator.free(document_path);
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = document_path, .data = document });
        const descriptor = try std.fmt.allocPrint(allocator,
            \\{{"id": "com.example.mixed", "version": "1.0.0", "schemas": ["types.schema.json"], "envelope_types": {s}, "payload_members": {s}}}
        , .{ c.types, members });
        defer allocator.free(descriptor);
        const pack = try std.fmt.allocPrint(allocator, "{s}/pack.json", .{c.dir});
        defer allocator.free(pack);
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = pack, .data = descriptor });
    }
    for (cases) |c| {
        var registry = try jsonschema.Registry.initFromBundled(allocator);
        defer registry.deinit();
        const root = try tmp.dir.realPathFileAlloc(std.testing.io, c.dir, allocator);
        defer allocator.free(root);
        const one = [_][]const u8{root};
        var read = try load(std.testing.io, allocator, &registry, &one);
        defer read.deinit();
        if (c.refused) {
            try std.testing.expect(carriesCode(read.refusals, pack_branch_unpinned));
            try std.testing.expectEqual(@as(usize, 0), read.branches.len);
            try std.testing.expectEqual(@as(usize, 0), read.types.len);
            try std.testing.expectEqual(@as(usize, 0), read.members.len);
        } else {
            try std.testing.expectEqual(@as(usize, 0), read.refusals.len);
            try std.testing.expectEqual(@as(usize, 2), read.branches.len);
            try std.testing.expectEqual(@as(usize, 2), read.types.len);
            try std.testing.expectEqual(@as(usize, 1), read.members.len);
            try std.testing.expectEqualStrings("note", read.members[0].name);
            try std.testing.expectEqualStrings("com.example.mixed.good", read.members[0].payload_type);
        }
    }
}
test "a branch that does not pin its own type is refused, and the manifest fixtures carry the codes" {
    const allocator = std.testing.allocator;
    const cases = [_]struct { dir: []const u8, code: []const u8 }{
        .{ .dir = "fixtures/packs/bad-branch-unpinned", .code = pack_branch_unpinned },
        .{ .dir = "fixtures/packs/bad-branch-undeclared-type", .code = pack_branch_undeclared_type },
        .{ .dir = "fixtures/packs/storage", .code = "" },
    };
    for (cases) |c| {
        var registry = try jsonschema.Registry.initFromBundled(allocator);
        defer registry.deinit();
        const one = [_][]const u8{c.dir};
        var read = try load(std.testing.io, allocator, &registry, &one);
        defer read.deinit();
        if (c.code.len == 0) {
            try std.testing.expectEqual(@as(usize, 0), read.refusals.len);
            try std.testing.expect(read.branches.len > 0);
        } else {
            try std.testing.expect(carriesCode(read.refusals, c.code));
            try std.testing.expectEqual(@as(usize, 0), read.branches.len);
        }
    }
    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();
    const one = [_][]const u8{"fixtures/packs/bad-branch-unpinned"};
    var refused = try describe(std.testing.io, allocator, &one);
    defer refused.deinit();
    try std.testing.expect(carriesCode(refused.refusals, pack_branch_unpinned));
    try std.testing.expectEqual(@as(usize, 0), refused.branches.len);
}
test "a declared name outside the pack's namespace is refused before its schema is read" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const cases = [_]struct { dir: []const u8, name: []const u8, schema: []const u8, code: []const u8, accepted: bool }{
        .{ .dir = "core-missing", .name = "capabilities.request.thing", .schema = "", .code = pack_unprefixed_name, .accepted = false },
        .{ .dir = "core-numbered", .name = "capabilities.request.thing", .schema = ", \"schema\": 7", .code = pack_unprefixed_name, .accepted = false },
        .{ .dir = "foreign-missing", .name = "com.other.billing.thing", .schema = "", .code = pack_foreign_prefix, .accepted = false },
        .{ .dir = "foreign-numbered", .name = "com.other.billing.thing", .schema = ", \"schema\": 7", .code = pack_foreign_prefix, .accepted = false },
        .{ .dir = "own-missing", .name = "com.example.nsgap.thing", .schema = "", .code = "", .accepted = true },
    };
    for (cases) |c| {
        try tmp.dir.createDir(std.testing.io, c.dir, .default_dir);
        const descriptor = try std.fmt.allocPrint(allocator,
            \\{{"id": "com.example.nsgap", "version": "1.0.0", "schemas": ["note.schema.json"], "envelope_types": [{{"type": "{s}", "role": "event"{s}}}]}}
        , .{ c.name, c.schema });
        defer allocator.free(descriptor);
        const pack = try std.fmt.allocPrint(allocator, "{s}/pack.json", .{c.dir});
        defer allocator.free(pack);
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = pack, .data = descriptor });
        const schema_path = try std.fmt.allocPrint(allocator, "{s}/note.schema.json", .{c.dir});
        defer allocator.free(schema_path);
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = schema_path, .data =
                \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type", "id", "session_id"], "properties": {"type": {"type": "string"}, "id": {"type": "string"}, "session_id": {"type": "string"}}}}}
            ,
        });
    }
    for (cases) |c| {
        const root = try tmp.dir.realPathFileAlloc(std.testing.io, c.dir, allocator);
        defer allocator.free(root);
        const one = [_][]const u8{root};
        var loaded = try describe(std.testing.io, allocator, &one);
        defer loaded.deinit();
        try std.testing.expectEqual(c.accepted, loaded.refusals.len == 0);
        if (!c.accepted) {
            try std.testing.expect(carriesCode(loaded.refusals, c.code));
            try std.testing.expectEqual(@as(usize, 0), loaded.branches.len);
            try std.testing.expectEqual(@as(usize, 0), loaded.types.len);
        }
    }
}
test "a schema path cleans without an arena, and a failed allocation does not leak" {
    const Runner = struct {
        fn run(allocator: std.mem.Allocator) !void {
            const cleaned = (try cleanRelative(allocator, "sub/../note.schema.json")).?;
            defer allocator.free(cleaned);
            try std.testing.expectEqualStrings("note.schema.json", cleaned);
            try std.testing.expectError(error.InvalidPackDescriptor, lexicalRelative(allocator, "../outside/away.schema.json"));
        }
    };
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, Runner.run, .{});
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
