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
pub const pack_type_duplicate = "pack_type_duplicate";
pub const pack_role_undeclared = "pack_role_undeclared";
pub const pack_reply_target_unknown = "pack_reply_target_unknown";
pub const pack_reply_target_ambiguous = "pack_reply_target_ambiguous";
pub const pack_refusal_undeclared = "pack_refusal_undeclared";
pub const pack_response_gated = "pack_response_gated";
pub const pack_ungated_type = "pack_ungated_type";
pub const pack_member_duplicate = "pack_member_duplicate";
pub const pack_member_target_unknown = "pack_member_target_unknown";
pub const pack_restates_core_member = "pack_restates_core_member";
pub const pack_schema_path_escape = "pack_schema_path_escape";
pub const pack_dependency_missing = "pack_dependency_missing";
pub const pack_external_ref = "pack_external_ref";
pub const pack_fixture_claims_core_unit = "pack_fixture_claims_core_unit";
pub const ext_claim_without_pack = "ext_claim_without_pack";

pub const Refusal = struct {
    code: []const u8,
    pack: []const u8,
    detail: []const u8 = "",
};

const Document = struct {
    name: []const u8,
    uri: []const u8,
    bytes: []const u8,
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

const Contained = struct {
    relative: []const u8,
    resolved: []const u8,
};

fn containedPath(io: std.Io, allocator: std.mem.Allocator, root: []const u8, dir: []const u8, entry: []const u8) !?Contained {
    if (entry.len == 0 or std.Io.Dir.path.isAbsolute(entry)) return null;
    const relative = (try cleanRelative(allocator, entry)) orelse return null;
    const joined = try std.fs.path.join(allocator, &.{ dir, relative });
    const resolved = std.Io.Dir.cwd().realPathFileAlloc(io, joined, allocator) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return null,
    };
    if (!beneath(root, resolved)) return null;
    return .{ .relative = relative, .resolved = resolved };
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

const Core = struct {
    roots: std.StringHashMap(void),
    members: std.StringHashMap(std.StringHashMap(void)),
};

fn coreVocabulary(allocator: std.mem.Allocator) !Core {
    var documents = std.StringHashMap(std.json.Value).init(allocator);
    for (schema_bundle.all) |entry| {
        const parsed = std.json.parseFromSliceLeaky(std.json.Value, allocator, entry.bytes, .{}) catch continue;
        try documents.put(entry.name, parsed);
    }
    var core: Core = .{ .roots = .init(allocator), .members = .init(allocator) };
    const envelope = documents.get("envelope.schema.json") orelse return core;
    const defs = field(envelope, "$defs") orelse return core;
    if (defs != .object) return core;
    var branches = defs.object.iterator();
    while (branches.next()) |branch| {
        const name = pinnedTypeOf(branch.value_ptr.*) orelse continue;
        var members = std.StringHashMap(void).init(allocator);
        const properties = field(branch.value_ptr.*, "properties") orelse continue;
        if (field(properties, "payload")) |payload| {
            try collectMembers(allocator, documents, "envelope.schema.json", payload, &members, 0);
        }
        try core.members.put(name, members);
        if (rootOf(name)) |root| try core.roots.put(root, {});
    }
    return core;
}

fn collectMembers(
    allocator: std.mem.Allocator,
    documents: std.StringHashMap(std.json.Value),
    file: []const u8,
    node: std.json.Value,
    out: *std.StringHashMap(void),
    depth: usize,
) !void {
    if (depth > 8 or node != .object) return;
    if (stringField(node, "$ref")) |ref| {
        const hash = std.mem.indexOfScalar(u8, ref, '#');
        const target = if (hash) |at| (if (at > 0) ref[0..at] else file) else file;
        const pointer = if (hash) |at| ref[at..] else ref;
        if (documents.get(target)) |document| {
            if (std.mem.startsWith(u8, pointer, "#/")) {
                if (try pointerAt(allocator, document, pointer)) |resolved| {
                    try collectMembers(allocator, documents, target, resolved, out, depth + 1);
                }
            }
        }
    }
    if (field(node, "properties")) |properties| {
        if (properties == .object) {
            for (properties.object.keys()) |name| try out.put(name, {});
        }
    }
    for (items(node, "allOf")) |member| {
        try collectMembers(allocator, documents, file, member, out, depth + 1);
    }
}

fn coreOf(core: *?Core, allocator: std.mem.Allocator) !*Core {
    if (core.* == null) core.* = try coreVocabulary(allocator);
    return &core.*.?;
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

fn namespaceRefusal(core: *?Core, allocator: std.mem.Allocator, id: []const u8, name: []const u8) !?[]const u8 {
    if (name.len == 0) return pack_unprefixed_name;
    if (underPrefix(name, id)) return null;
    if (specNamespace((try coreOf(core, allocator)).roots, name)) return pack_unprefixed_name;
    return pack_foreign_prefix;
}

fn items(value: std.json.Value, key: []const u8) []const std.json.Value {
    const held = arrayField(value, key) orelse return &.{};
    return held.items;
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

const Descriptor = struct {
    dir: []const u8,
    value: std.json.Value,
    id: []const u8,
    version: []const u8,
    base: []const u8 = "",
    documents: []const Document = &.{},
    manifest: ?[]const u8 = null,
};

fn unitOf(allocator: std.mem.Allocator, pack: Descriptor) ![]const u8 {
    return std.fmt.allocPrint(allocator, "ext:{s}/{s}", .{ pack.id, pack.version });
}

fn checkCorpus(io: std.Io, allocator: std.mem.Allocator, descriptors: []const Descriptor, pack: Descriptor, out: *std.ArrayList(Refusal)) !void {
    const keys = items(pack.value, "capability_keys");
    const path = pack.manifest orelse {
        if (keys.len != 0) try out.append(allocator, .{ .code = "", .pack = pack.id, .detail = "capability keys and no fixture manifest" });
        return;
    };
    const bytes = readAll(io, allocator, path) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return out.append(allocator, .{ .code = "", .pack = pack.id, .detail = path }),
    };
    const manifest = std.json.parseFromSliceLeaky(std.json.Value, allocator, bytes, .{}) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return out.append(allocator, .{ .code = "", .pack = pack.id, .detail = path }),
    };
    const own = try unitOf(allocator, pack);
    for (items(manifest, "fixtures")) |entry| {
        const units = items(entry, "units");
        if (units.len == 0) return out.append(allocator, .{ .code = "", .pack = pack.id, .detail = stringField(entry, "id") orelse "" });
        for (units) |listed| {
            const unit = asString(listed) orelse "";
            if (!std.mem.startsWith(u8, unit, "ext:")) return out.append(allocator, .{ .code = pack_fixture_claims_core_unit, .pack = pack.id, .detail = unit });
            var loaded = false;
            for (descriptors) |other| {
                if (std.mem.eql(u8, try unitOf(allocator, other), unit)) loaded = true;
            }
            if (!loaded) return out.append(allocator, .{ .code = ext_claim_without_pack, .pack = pack.id, .detail = unit });
            if (!std.mem.eql(u8, unit, own)) return out.append(allocator, .{ .code = "", .pack = pack.id, .detail = unit });
        }
    }
    for (keys) |key| {
        const name = asString(key) orelse continue;
        for ([_][]const u8{ "gate", "honour" }) |aspect| {
            if (!coveredUnder(manifest, name, aspect, own)) return out.append(allocator, .{ .code = "", .pack = pack.id, .detail = name });
        }
    }
}

fn coveredUnder(manifest: std.json.Value, capability: []const u8, aspect: []const u8, unit: []const u8) bool {
    for (items(manifest, "fixtures")) |entry| {
        if (!std.mem.eql(u8, stringField(entry, "kind") orelse "", "semantic-invalid")) continue;
        var claims = false;
        for (items(entry, "units")) |listed| {
            if (std.mem.eql(u8, asString(listed) orelse "", unit)) claims = true;
        }
        if (!claims) continue;
        for (items(entry, "covers")) |cover| {
            if (std.mem.eql(u8, stringField(cover, "capability") orelse "", capability) and
                std.mem.eql(u8, stringField(cover, "aspect") orelse "", aspect)) return true;
        }
    }
    return false;
}

fn dependencyOf(descriptors: []const Descriptor, dependency: std.json.Value) ?Descriptor {
    const id = stringField(dependency, "id") orelse return null;
    const version = stringField(dependency, "version") orelse return null;
    for (descriptors) |pack| {
        if (std.mem.eql(u8, pack.id, id) and std.mem.eql(u8, pack.version, version)) return pack;
    }
    return null;
}

const Ref = struct {
    absolute: []const u8 = "",
    identifier: bool,
    valid: bool = true,
};

fn admit(allocator: std.mem.Allocator, owner: Descriptor, allowed: *std.StringHashMap(void)) !void {
    for (owner.documents) |document| {
        try allowed.put(document.uri, {});
        try admitIdentifiers(allocator, owner, document.uri, document.value, allowed);
    }
    for (items(owner.value, "payload_members"), 0..) |member, index| {
        const uri = try memberUri(allocator, owner, index);
        try allowed.put(uri, {});
        if (field(member, "schema")) |schema| try admitIdentifiers(allocator, owner, uri, schema, allowed);
    }
}

fn admitIdentifiers(allocator: std.mem.Allocator, owner: Descriptor, base: []const u8, document: std.json.Value, allowed: *std.StringHashMap(void)) !void {
    var refs: std.ArrayList(Ref) = .empty;
    try collectRefs(allocator, document, base, &refs);
    for (refs.items) |ref| {
        if (ref.identifier and ref.valid and std.mem.startsWith(u8, ref.absolute, owner.base)) try allowed.put(ref.absolute, {});
    }
}

fn memberUri(allocator: std.mem.Allocator, owner: Descriptor, index: usize) ![]const u8 {
    return std.fmt.allocPrint(allocator, "{s}member-{d}.json", .{ owner.base, index });
}

fn checkReferences(allocator: std.mem.Allocator, descriptors: []const Descriptor, pack: Descriptor, out: *std.ArrayList(Refusal)) !void {
    var allowed = std.StringHashMap(void).init(allocator);
    try admit(allocator, pack, &allowed);
    for (items(pack.value, "depends_on")) |dependency| {
        if (dependencyOf(descriptors, dependency)) |other| try admit(allocator, other, &allowed);
    }
    for (pack.documents) |document| try checkDocumentReferences(allocator, pack, document.uri, document.value, allowed, out);
    for (items(pack.value, "payload_members"), 0..) |member, index| {
        const schema = field(member, "schema") orelse continue;
        try checkDocumentReferences(allocator, pack, try memberUri(allocator, pack, index), schema, allowed, out);
    }
}

fn checkDocumentReferences(allocator: std.mem.Allocator, pack: Descriptor, base: []const u8, document: std.json.Value, allowed: std.StringHashMap(void), out: *std.ArrayList(Refusal)) !void {
    var refs: std.ArrayList(Ref) = .empty;
    try collectRefs(allocator, document, base, &refs);
    for (refs.items) |ref| {
        const outside = if (!ref.valid)
            true
        else if (ref.identifier)
            !std.mem.startsWith(u8, ref.absolute, pack.base)
        else
            !allowed.contains(ref.absolute) and !isCoreResource(ref.absolute);
        if (outside) try out.append(allocator, .{ .code = pack_external_ref, .pack = pack.id, .detail = ref.absolute });
    }
}

fn isCoreResource(absolute: []const u8) bool {
    if (!std.mem.startsWith(u8, absolute, jsonschema.schema_base)) return false;
    const name = absolute[jsonschema.schema_base.len..];
    return name.len > 0 and std.mem.indexOfScalar(u8, name, '/') == null;
}

fn collectRefs(allocator: std.mem.Allocator, node: std.json.Value, base: []const u8, out: *std.ArrayList(Ref)) !void {
    switch (node) {
        .object => |object| {
            var current = base;
            if (object.get("$id")) |id| if (id == .string) {
                const ref = try resolveRef(allocator, current, id.string, true);
                try out.append(allocator, ref);
                if (ref.valid) current = ref.absolute;
            };
            var entries = object.iterator();
            while (entries.next()) |entry| {
                const key = entry.key_ptr.*;
                if (std.mem.eql(u8, key, "$id")) continue;
                if ((std.mem.eql(u8, key, "$ref") or std.mem.eql(u8, key, "$dynamicRef")) and entry.value_ptr.* == .string) {
                    try out.append(allocator, try resolveRef(allocator, current, entry.value_ptr.string, false));
                    continue;
                }
                try collectRefs(allocator, entry.value_ptr.*, current, out);
            }
        },
        .array => |array| for (array.items) |item| try collectRefs(allocator, item, base, out),
        else => {},
    }
}

fn parsesAsReference(raw: []const u8) bool {
    var index: usize = 0;
    while (index < raw.len) : (index += 1) {
        const c = raw[index];
        if (c < 0x20 or c == 0x7f) return false;
        if (c != '%') continue;
        if (index + 2 >= raw.len or !std.ascii.isHex(raw[index + 1]) or !std.ascii.isHex(raw[index + 2])) return false;
    }
    return true;
}

fn schemeEnd(text: []const u8) ?usize {
    for (text, 0..) |c, index| {
        if (c == ':') return if (index > 0 and std.ascii.isAlphabetic(text[0])) index else null;
        if (!(std.ascii.isAlphanumeric(c) or c == '+' or c == '-' or c == '.')) return null;
    }
    return null;
}

fn resolveRef(allocator: std.mem.Allocator, base: []const u8, raw: []const u8, identifier: bool) !Ref {
    if (!parsesAsReference(raw)) return .{ .identifier = identifier, .valid = false };
    const reference = raw[0 .. std.mem.indexOfScalar(u8, raw, '#') orelse raw.len];
    return .{ .absolute = try resolveAgainst(allocator, base, reference), .identifier = identifier };
}

fn resolveAgainst(allocator: std.mem.Allocator, base: []const u8, reference: []const u8) ![]const u8 {
    if (schemeEnd(reference)) |colon| return withPath(allocator, reference[0 .. colon + 1], reference[colon + 1 ..]);
    const colon = schemeEnd(base) orelse return reference;
    const scheme = base[0 .. colon + 1];
    const after = base[colon + 1 ..];
    if (std.mem.startsWith(u8, reference, "//")) return withPath(allocator, scheme, reference);
    const authority_end = if (std.mem.startsWith(u8, after, "//")) (std.mem.indexOfScalarPos(u8, after, 2, '/') orelse after.len) else 0;
    const origin = base[0 .. colon + 1 + authority_end];
    const base_rest = after[authority_end..];
    const base_path = base_rest[0 .. std.mem.indexOfScalar(u8, base_rest, '?') orelse base_rest.len];
    if (reference.len == 0) return allocator.dupe(u8, base);
    if (reference[0] == '?') return std.mem.concat(allocator, u8, &.{ origin, base_path, reference });
    if (reference[0] == '/') return withPath(allocator, origin, reference);
    const directory = base_path[0 .. if (std.mem.lastIndexOfScalar(u8, base_path, '/')) |slash| slash + 1 else 0];
    const merged = if (directory.len == 0 and authority_end > 0) try std.mem.concat(allocator, u8, &.{ "/", reference }) else try std.mem.concat(allocator, u8, &.{ directory, reference });
    return withPath(allocator, origin, merged);
}

fn withPath(allocator: std.mem.Allocator, prefix: []const u8, rest: []const u8) ![]const u8 {
    var path_start: usize = 0;
    if (std.mem.startsWith(u8, rest, "//")) path_start = std.mem.indexOfScalarPos(u8, rest, 2, '/') orelse rest.len;
    const query = std.mem.indexOfScalarPos(u8, rest, path_start, '?') orelse rest.len;
    const path = try removeDots(allocator, rest[path_start..query]);
    return std.mem.concat(allocator, u8, &.{ prefix, rest[0..path_start], path, rest[query..] });
}

fn removeDots(allocator: std.mem.Allocator, path: []const u8) ![]const u8 {
    if (std.mem.indexOf(u8, path, "./") == null and !std.mem.endsWith(u8, path, "/.") and !std.mem.endsWith(u8, path, "/..")) return path;
    var kept: std.ArrayList([]const u8) = .empty;
    var segments = std.mem.splitScalar(u8, path, '/');
    var last: []const u8 = "";
    while (segments.next()) |segment| {
        last = segment;
        if (std.mem.eql(u8, segment, ".")) continue;
        if (std.mem.eql(u8, segment, "..")) {
            if (kept.items.len > 1 or (kept.items.len == 1 and kept.items[0].len > 0)) _ = kept.pop();
            continue;
        }
        try kept.append(allocator, segment);
    }
    if (std.mem.eql(u8, last, ".") or std.mem.eql(u8, last, "..")) try kept.append(allocator, "");
    return std.mem.join(allocator, "/", kept.items);
}

fn refusedWith(arena: *std.heap.ArenaAllocator, refusals: *std.ArrayList(Refusal)) !Loaded {
    const owned = try refusals.toOwnedSlice(arena.allocator());
    return .{ .arena = arena.*, .branches = &.{}, .members = &.{}, .types = &.{}, .refusals = owned };
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

    var descriptors = std.ArrayList(Descriptor).empty;
    var named = std.StringHashMap(void).init(allocator);
    var load_refusals = std.ArrayList(Refusal).empty;
    var core: ?Core = null;

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
        try descriptors.append(allocator, .{ .dir = dir, .value = descriptor, .id = pack_id, .version = version });
    }

    for (descriptors.items, 0..) |first, index| {
        for (descriptors.items[index + 1 ..]) |second| {
            if (std.mem.eql(u8, first.id, second.id) or
                underPrefix(first.id, second.id) or
                underPrefix(second.id, first.id))
            {
                try load_refusals.append(allocator, .{ .code = pack_id_collision, .pack = first.id, .detail = second.id });
            }
        }
    }
    if (load_refusals.items.len != 0) return refusedWith(&arena, &load_refusals);

    for (descriptors.items) |descriptor| {
        try checkDeclarations(.{ .allocator = allocator, .out = &load_refusals, .pack = descriptor.id }, &core, descriptor.value);
    }
    if (load_refusals.items.len != 0) return refusedWith(&arena, &load_refusals);

    for (descriptors.items) |*pack| {
        pack.base = try std.fmt.allocPrint(allocator, "{s}{s}/{s}/", .{ pack_base_uri, pack.id, pack.version });
        const pack_root = std.Io.Dir.cwd().realPathFileAlloc(io, pack.dir, allocator) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return error.InvalidPackDescriptor,
        };
        if (stringField(pack.value, "fixtures")) |manifest| {
            if (try containedPath(io, allocator, pack_root, pack.dir, manifest)) |contained| {
                pack.manifest = contained.resolved;
            } else {
                try load_refusals.append(allocator, .{ .code = pack_schema_path_escape, .pack = pack.id, .detail = manifest });
            }
        }
        const declared_schemas = field(pack.value, "schemas") orelse continue;
        if (declared_schemas != .array) return error.InvalidPackDescriptor;
        var documents: std.ArrayList(Document) = .empty;
        for (declared_schemas.array.items) |schema_name| {
            if (schema_name != .string) return error.InvalidPackDescriptor;
            const contained = (try containedPath(io, allocator, pack_root, pack.dir, schema_name.string)) orelse {
                try load_refusals.append(allocator, .{ .code = pack_schema_path_escape, .pack = pack.id, .detail = schema_name.string });
                continue;
            };
            const schema_bytes = try readAll(io, allocator, contained.resolved);
            const normalized = try toSlash(allocator, contained.relative);
            const uri = try std.mem.concat(allocator, u8, &.{ pack.base, normalized });
            const value = try std.json.parseFromSliceLeaky(std.json.Value, allocator, schema_bytes, .{});
            try documents.append(allocator, .{ .name = normalized, .uri = uri, .bytes = schema_bytes, .value = value });
        }
        pack.documents = documents.items;
    }
    if (load_refusals.items.len != 0) return refusedWith(&arena, &load_refusals);

    for (descriptors.items) |pack| {
        for (items(pack.value, "depends_on")) |dependency| {
            if (dependencyOf(descriptors.items, dependency) == null) {
                try load_refusals.append(allocator, .{ .code = pack_dependency_missing, .pack = pack.id, .detail = stringField(dependency, "id") orelse "" });
            }
        }
    }
    if (load_refusals.items.len != 0) return refusedWith(&arena, &load_refusals);

    for (descriptors.items) |pack| try checkReferences(allocator, descriptors.items, pack, &load_refusals);
    if (load_refusals.items.len != 0) return refusedWith(&arena, &load_refusals);

    var branches = std.ArrayList(Branch).empty;
    var members = std.ArrayList(Member).empty;
    var types = std.ArrayList(Declared).empty;

    for (descriptors.items) |pack| {
        const descriptor = pack.value;
        const pack_id = pack.id;
        const version = pack.version;
        const pack_documents = pack.documents;
        if (registry) |target| {
            for (pack_documents) |document| try target.addDocument(document.uri, document.bytes);
        }

        const gates = field(descriptor, "gates");

        for (items(descriptor, "payload_members")) |entry| {
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

        const declared = arrayField(descriptor, "envelope_types") orelse continue;
        for (declared.items) |entry| {
            const name = stringField(entry, "type") orelse continue;
            var refusals = std.ArrayList([]const u8).empty;
            for (items(entry, "refusals")) |code| {
                if (asString(code)) |held| try refusals.append(allocator, held);
            }
            try types.append(allocator, .{
                .name = name,
                .role = stringField(entry, "role") orelse "",
                .capability = typeCapability(gates, name),
                .response = responseFor(.{ .array = declared }, name),
                .refusals = try refusals.toOwnedSlice(allocator),
            });
        }

        for (declared.items) |entry| {
            const declared_type = stringField(entry, "type") orelse continue;
            const schema_ref = stringField(entry, "schema") orelse continue;
            const hash = std.mem.indexOfScalar(u8, schema_ref, '#');
            const spelled = if (hash) |at| schema_ref[0..at] else schema_ref;
            const cleaned = (try cleanRelative(allocator, spelled)) orelse spelled;
            const cited = try toSlash(allocator, cleaned);
            const pointer: []const u8 = if (hash) |at| schema_ref[at..] else "";
            var resolved: ?std.json.Value = null;
            for (pack_documents) |document| {
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
    if (load_refusals.items.len != 0) return refusedWith(&arena, &load_refusals);

    for (descriptors.items) |pack| try checkCorpus(io, allocator, descriptors.items, pack, &load_refusals);
    if (load_refusals.items.len != 0) return refusedWith(&arena, &load_refusals);

    std.mem.sort(Branch, branches.items, {}, struct {
        fn lessThan(_: void, a: Branch, b: Branch) bool {
            return std.mem.order(u8, a.ref, b.ref) == .lt;
        }
    }.lessThan);

    const owned_branches = try branches.toOwnedSlice(allocator);
    const owned_members = try members.toOwnedSlice(allocator);
    const owned_types = try types.toOwnedSlice(allocator);
    return .{
        .arena = arena,
        .branches = owned_branches,
        .members = owned_members,
        .types = owned_types,
        .refusals = &.{},
    };
}

const Refuser = struct {
    allocator: std.mem.Allocator,
    out: *std.ArrayList(Refusal),
    pack: []const u8,

    fn add(self: Refuser, code: []const u8, detail: []const u8) !void {
        try self.out.append(self.allocator, .{ .code = code, .pack = self.pack, .detail = detail });
    }

    fn contains(self: Refuser, core: *?Core, name: []const u8) !bool {
        const code = (try namespaceRefusal(core, self.allocator, self.pack, name)) orelse return true;
        try self.add(code, name);
        return false;
    }
};

fn roleOf(entry: std.json.Value) []const u8 {
    return stringField(entry, "role") orelse "";
}

fn memberKey(allocator: std.mem.Allocator, payload_type: []const u8, member: []const u8) ![]const u8 {
    return std.fmt.allocPrint(allocator, "{s}#{s}", .{ payload_type, member });
}

fn checkDeclarations(refuse: Refuser, core: *?Core, descriptor: std.json.Value) !void {
    const allocator = refuse.allocator;
    const before = refuse.out.items.len;
    var keys = std.StringHashMap(void).init(allocator);
    for (items(descriptor, "capability_keys")) |key| {
        if (key != .string) return error.InvalidPackDescriptor;
        if (try refuse.contains(core, key.string)) try keys.put(key.string, {});
    }
    var codes = std.StringHashMap(void).init(allocator);
    for (items(descriptor, "error_codes")) |code| {
        if (code != .string) return error.InvalidPackDescriptor;
        if (try refuse.contains(core, code.string)) try codes.put(code.string, {});
    }
    const types = items(descriptor, "envelope_types");
    var declared = std.StringHashMap(std.json.Value).init(allocator);
    for (types) |entry| {
        const name = stringField(entry, "type") orelse "";
        if (!try refuse.contains(core, name)) continue;
        if (declared.contains(name)) {
            try refuse.add(pack_type_duplicate, name);
            continue;
        }
        try declared.put(name, entry);
    }
    if (refuse.out.items.len != before) return;

    var answered = std.StringHashMap(void).init(allocator);
    for (types) |entry| {
        const name = stringField(entry, "type") orelse "";
        const role = roleOf(entry);
        if (!std.mem.eql(u8, role, "request") and !std.mem.eql(u8, role, "response") and !std.mem.eql(u8, role, "event")) {
            try refuse.add(pack_role_undeclared, name);
            continue;
        }
        const schema_ref: []const u8 = stringField(entry, "schema") orelse "";
        if (schema_ref.len == 0) try refuse.add("", name);
        if (std.mem.eql(u8, role, "response")) {
            const replies = stringField(entry, "replies_to") orelse "";
            const target = declared.get(replies);
            if (target == null or !std.mem.eql(u8, roleOf(target.?), "request")) {
                try refuse.add(pack_reply_target_unknown, name);
            } else if (answered.contains(replies)) {
                try refuse.add(pack_reply_target_ambiguous, name);
            } else {
                try answered.put(replies, {});
            }
        }
        for (items(entry, "refusals")) |code| {
            if (!codes.contains(asString(code) orelse "")) try refuse.add(pack_refusal_undeclared, name);
        }
    }

    const gates = field(descriptor, "gates");
    var gated = std.StringHashMap(void).init(allocator);
    for (items(descriptor, "gates")) |gate| {
        const gate_type = gateString(gate, "type");
        var name = gate_type;
        if (gate_type.len == 0) {
            const payload_type = gateString(gate, "payload_type");
            const member = gateString(gate, "member");
            if (payload_type.len == 0 or member.len == 0) {
                try refuse.add("", "");
                continue;
            }
            name = try memberKey(allocator, payload_type, member);
        }
        const capability = gateString(gate, "capability");
        const ungated = if (field(gate, "ungated")) |held| held == .bool and held.bool else false;
        if (ungated == (capability.len != 0)) {
            try refuse.add("", name);
            continue;
        }
        if (capability.len != 0 and !keys.contains(capability)) {
            try refuse.add((try namespaceRefusal(core, allocator, refuse.pack, capability)) orelse "", capability);
            continue;
        }
        if (gate_type.len != 0) {
            const target = declared.get(gate_type) orelse {
                try refuse.add("", gate_type);
                continue;
            };
            if (std.mem.eql(u8, roleOf(target), "response")) {
                try refuse.add(pack_response_gated, gate_type);
                continue;
            }
        }
        if (gated.contains(name)) {
            try refuse.add("", name);
            continue;
        }
        try gated.put(name, {});
    }
    for (types) |entry| {
        if (std.mem.eql(u8, roleOf(entry), "response")) continue;
        const name = stringField(entry, "type") orelse "";
        if (!gated.contains(name)) try refuse.add(pack_ungated_type, name);
    }

    var listed = std.StringHashMap(void).init(allocator);
    for (items(descriptor, "payload_members")) |entry| {
        const payload_type = stringField(entry, "payload_type") orelse "";
        const member = stringField(entry, "member") orelse "";
        const key = try memberKey(allocator, payload_type, member);
        if (listed.contains(key)) {
            try refuse.add(pack_member_duplicate, key);
            continue;
        }
        try listed.put(key, {});
        const core_members = (try coreOf(core, allocator)).members.get(payload_type) orelse {
            try refuse.add(pack_member_target_unknown, key);
            continue;
        };
        if (std.mem.eql(u8, payload_type, "capabilities.updated") and memberCapability(gates, payload_type, member).len != 0) {
            try refuse.add(pack_member_target_unknown, key);
            continue;
        }
        if (core_members.contains(member)) {
            try refuse.add(pack_restates_core_member, key);
            continue;
        }
        if (!try refuse.contains(core, member)) continue;
        if (field(entry, "schema") == null) {
            try refuse.add("", key);
            continue;
        }
        if (!gated.contains(key)) try refuse.add(pack_ungated_type, key);
    }
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
            \\{"id": "com.example.ok", "version": "1.0.0", "schemas": ["note.schema.json"], "envelope_types": [{"type": "com.example.ok.thing", "role": "event", "schema": "note.schema.json#/$defs/thing"}], "gates": [{"type": "com.example.ok.thing", "ungated": true}]}
        ,
    });
    try tmp.dir.symLink(std.testing.io, "note.schema.json", "aliased/alias.schema.json", .{ .is_directory = false });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "aliased/note.schema.json", .data =
            \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type", "session_id"], "properties": {"type": {"const": "com.example.alias.thing"}, "session_id": {"type": "string"}}}}}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "aliased/pack.json", .data =
            \\{"id": "com.example.alias", "version": "1.0.0", "schemas": ["alias.schema.json"], "envelope_types": [{"type": "com.example.alias.thing", "role": "event", "schema": "alias.schema.json#/$defs/thing"}], "gates": [{"type": "com.example.alias.thing", "ungated": true}]}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "cleaned/note.schema.json", .data =
            \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type", "session_id"], "properties": {"type": {"const": "com.example.clean.thing"}, "session_id": {"type": "string"}}}}}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "cleaned/pack.json", .data =
            \\{"id": "com.example.clean", "version": "1.0.0", "schemas": ["sub/../note.schema.json"], "envelope_types": [{"type": "com.example.clean.thing", "role": "event", "schema": "sub/../note.schema.json#/$defs/thing"}], "gates": [{"type": "com.example.clean.thing", "ungated": true}]}
        ,
    });

    try tmp.dir.createDir(std.testing.io, "nested/sub", .default_dir);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "nested/sub/note.schema.json", .data =
            \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type", "session_id"], "properties": {"type": {"const": "com.example.nested.thing"}, "session_id": {"type": "string"}}}}}
        ,
    });
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "nested/pack.json", .data =
            \\{"id": "com.example.nested", "version": "1.0.0", "schemas": ["sub/note.schema.json"], "envelope_types": [{"type": "com.example.nested.thing", "role": "event", "schema": "sub/note.schema.json#/$defs/thing"}], "gates": [{"type": "com.example.nested.thing", "ungated": true}]}
        ,
    });

    if (comptime std.Io.Dir.path.sep == '/') {
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "literal/back\\slash.schema.json", .data =
                \\{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"thing": {"type": "object", "required": ["type", "session_id"], "properties": {"type": {"const": "com.example.literal.thing"}, "session_id": {"type": "string"}}}}}
            ,
        });
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "literal/pack.json", .data =
                \\{"id": "com.example.literal", "version": "1.0.0", "schemas": ["back\\slash.schema.json"], "envelope_types": [{"type": "com.example.literal.thing", "role": "event", "schema": "back\\slash.schema.json#/$defs/thing"}], "gates": [{"type": "com.example.literal.thing", "ungated": true}]}
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
        var refused = try load(std.testing.io, allocator, &registry, &escaping);
        defer refused.deinit();
        try std.testing.expect(carriesCode(refused.refusals, pack_schema_path_escape));
        try std.testing.expectEqual(@as(usize, 0), refused.branches.len);
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
            \\{{"id": "com.example.ref", "version": "1.0.0", "schemas": ["note.schema.json"], "envelope_types": [{{"type": "com.example.ref.thing", "role": "event", "schema": "{s}"}}], "gates": [{{"type": "com.example.ref.thing", "ungated": true}}]}}
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
            \\{{"id": "com.example.ptr", "version": "1.0.0", "schemas": ["types.schema.json"], "envelope_types": [{{"type": "com.example.ptr.thing", "role": "event", "schema": "{s}"}}], "gates": [{{"type": "com.example.ptr.thing", "ungated": true}}]}}
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
        \\{{"id": "com.example.escape", "version": "1.0.0", "schemas": ["./note.schema.json"], "envelope_types": [{{"type": "com.example.escape.thing", "role": "event", "schema": "./note.schema.json#/$defs/odd~1name~0here"}}], "gates": [{{"type": "com.example.escape.thing", "ungated": true}}]}}
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
    const cases = [_]struct { dir: []const u8, types: []const u8, gates: []const u8, refused: bool }{
        .{ .dir = "mixed", .types =
        \\[
        \\  {"type": "com.example.mixed.good", "role": "event", "schema": "types.schema.json#/$defs/good"},
        \\  {"type": "com.example.mixed.bad", "role": "event", "schema": "types.schema.json#/$defs/plain"}
        \\]
        , .gates = "{\"type\": \"com.example.mixed.bad\", \"ungated\": true}", .refused = true },
        .{ .dir = "both-pinned", .types =
        \\[
        \\  {"type": "com.example.mixed.good", "role": "event", "schema": "types.schema.json#/$defs/good"},
        \\  {"type": "com.example.mixed.other", "role": "event", "schema": "types.schema.json#/$defs/other"}
        \\]
        , .gates = "{\"type\": \"com.example.mixed.other\", \"ungated\": true}", .refused = false },
    };
    const members =
        \\[{"payload_type": "run.completed", "member": "com.example.mixed.note", "schema": {"type": "string"}}]
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
            \\{{"id": "com.example.mixed", "version": "1.0.0", "schemas": ["types.schema.json"], "envelope_types": {s}, "payload_members": {s}, "gates": [{{"type": "com.example.mixed.good", "ungated": true}}, {{"payload_type": "run.completed", "member": "com.example.mixed.note", "ungated": true}}, {s}]}}
        , .{ c.types, members, c.gates });
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
            try std.testing.expectEqualStrings("com.example.mixed.note", read.members[0].name);
            try std.testing.expectEqualStrings("run.completed", read.members[0].payload_type);
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
        .{ .dir = "own-missing", .name = "com.example.nsgap.thing", .schema = "", .code = "", .accepted = false },
        .{ .dir = "own-pinned", .name = "com.example.nsgap.thing", .schema = ", \"schema\": \"note.schema.json#/$defs/thing\"", .code = "", .accepted = true },
    };
    for (cases) |c| {
        try tmp.dir.createDir(std.testing.io, c.dir, .default_dir);
        const descriptor = try std.fmt.allocPrint(allocator,
            \\{{"id": "com.example.nsgap", "version": "1.0.0", "schemas": ["note.schema.json"], "envelope_types": [{{"type": "{s}", "role": "event"{s}}}], "gates": [{{"type": "{s}", "ungated": true}}]}}
        , .{ c.name, c.schema, c.name });
        defer allocator.free(descriptor);
        const pack = try std.fmt.allocPrint(allocator, "{s}/pack.json", .{c.dir});
        defer allocator.free(pack);
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = pack, .data = descriptor });
        const schema_path = try std.fmt.allocPrint(allocator, "{s}/note.schema.json", .{c.dir});
        defer allocator.free(schema_path);
        const schema = try std.fmt.allocPrint(allocator,
            \\{{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {{"thing": {{"type": "object", "required": ["type", "id", "session_id"], "properties": {{"type": {{"const": "{s}"}}, "id": {{"type": "string"}}, "session_id": {{"type": "string"}}}}}}}}}}
        , .{c.name});
        defer allocator.free(schema);
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = schema_path, .data = schema });
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
            try std.testing.expectEqual(@as(?[]const u8, null), try cleanRelative(allocator, "../outside/away.schema.json"));
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

test "a reference resolves against its base as RFC 3986 section 5.4 resolves it, fragment dropped" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const base = "http://a/b/c/d;p?q";
    const cases = [_][2][]const u8{
        .{ "g:h", "g:h" },
        .{ "g", "http://a/b/c/g" },
        .{ "./g", "http://a/b/c/g" },
        .{ "g/", "http://a/b/c/g/" },
        .{ "/g", "http://a/g" },
        .{ "//g", "http://g" },
        .{ "?y", "http://a/b/c/d;p?y" },
        .{ "g?y", "http://a/b/c/g?y" },
        .{ "#s", "http://a/b/c/d;p?q" },
        .{ "g#s", "http://a/b/c/g" },
        .{ "", "http://a/b/c/d;p?q" },
        .{ ".", "http://a/b/c/" },
        .{ "./", "http://a/b/c/" },
        .{ "..", "http://a/b/" },
        .{ "../", "http://a/b/" },
        .{ "../g", "http://a/b/g" },
        .{ "../..", "http://a/" },
        .{ "../../g", "http://a/g" },
        .{ "../../../g", "http://a/g" },
        .{ "/./g", "http://a/g" },
        .{ "/../g", "http://a/g" },
        .{ "g/./h", "http://a/b/c/g/h" },
        .{ "g/../h", "http://a/b/c/h" },
    };
    for (cases) |case| {
        const ref = try resolveRef(arena.allocator(), base, case[0], false);
        try std.testing.expect(ref.valid);
        try std.testing.expectEqualStrings(case[1], ref.absolute);
    }
    try std.testing.expect(!(try resolveRef(arena.allocator(), base, "%zz", false)).valid);
}

test "a pack may refer to a declared dependency's schemas, and a dependency not loaded is refused" {
    const allocator = std.testing.allocator;
    const accepted = [_][]const u8{ "fixtures/packs/client", "fixtures/packs/index" };
    var with_dependency = try describe(std.testing.io, allocator, &accepted);
    defer with_dependency.deinit();
    try std.testing.expectEqual(@as(usize, 0), with_dependency.refusals.len);

    const alone = [_][]const u8{ "fixtures/packs/client", "fixtures/packs/storage" };
    var missing = try describe(std.testing.io, allocator, &alone);
    defer missing.deinit();
    try std.testing.expect(carriesCode(missing.refusals, pack_dependency_missing));
}

test "a fixture manifest outside the pack root is refused whether or not the pack contributes schemas" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const cases = [_]struct { dir: []const u8, descriptor: []const u8, escapes: bool }{
        .{ .dir = "schemaless-escaping", .descriptor =
        \\{"id": "com.example.noschema", "version": "1.0.0", "envelope_types": [], "fixtures": "../outside/manifest.json"}
        , .escapes = true },
        .{ .dir = "schemaless-inside", .descriptor =
        \\{"id": "com.example.noschema", "version": "1.0.0", "envelope_types": [], "fixtures": "manifest.json"}
        , .escapes = false },
    };
    try tmp.dir.createDir(std.testing.io, "outside", .default_dir);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "outside/manifest.json", .data = "{}" });
    for (cases) |c| {
        try tmp.dir.createDir(std.testing.io, c.dir, .default_dir);
        const pack = try std.fmt.allocPrint(allocator, "{s}/pack.json", .{c.dir});
        defer allocator.free(pack);
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = pack, .data = c.descriptor });
        const manifest = try std.fmt.allocPrint(allocator, "{s}/manifest.json", .{c.dir});
        defer allocator.free(manifest);
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = manifest, .data = "{}" });
        const root = try tmp.dir.realPathFileAlloc(std.testing.io, c.dir, allocator);
        defer allocator.free(root);
        const one = [_][]const u8{root};
        var loaded = try describe(std.testing.io, allocator, &one);
        defer loaded.deinit();
        try std.testing.expectEqual(c.escapes, carriesCode(loaded.refusals, pack_schema_path_escape));
    }
}

test "a pack's corpus proves its own capability keys under its own unit, and nothing else" {
    const allocator = std.testing.allocator;
    const alone = [_][]const u8{"fixtures/packs/storage"};
    var proven = try describe(std.testing.io, allocator, &alone);
    defer proven.deinit();
    try std.testing.expectEqual(@as(usize, 0), proven.refusals.len);

    const sibling = [_][]const u8{ "fixtures/packs/bad-fixture-claims-sibling", "fixtures/packs/storage" };
    var claimed = try describe(std.testing.io, allocator, &sibling);
    defer claimed.deinit();
    try std.testing.expectEqual(@as(usize, 1), claimed.refusals.len);
    try std.testing.expectEqualStrings("", claimed.refusals[0].code);
    try std.testing.expectEqualStrings("ext:com.example.storage/1.0.0", claimed.refusals[0].detail);

    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const keyed =
        \\{"id": "com.example.keyed", "version": "1.0.0", "capability_keys": ["com.example.keyed.thing"], "envelope_types": []
    ;
    const gate_only =
        \\{"version": 1, "fixtures": [{"id": "keyed-gate", "path": "t.json", "kind": "semantic-invalid", "valid": false, "phase": "semantic", "codes": ["unavailable_capability"], "units": ["ext:com.example.keyed/1.0.0"], "covers": [{"capability": "com.example.keyed.thing", "aspect": "gate"}], "provenance": []}]}
    ;
    const cases = [_]struct { dir: []const u8, descriptor: []const u8, manifest: ?[]const u8 }{
        .{ .dir = "keys-no-manifest", .descriptor = keyed ++ "}", .manifest = null },
        .{ .dir = "keys-gate-only", .descriptor = keyed ++ ", \"fixtures\": \"manifest.json\"}", .manifest = gate_only },
    };
    for (cases) |c| {
        try tmp.dir.createDir(std.testing.io, c.dir, .default_dir);
        const pack = try std.fmt.allocPrint(allocator, "{s}/pack.json", .{c.dir});
        defer allocator.free(pack);
        try tmp.dir.writeFile(std.testing.io, .{ .sub_path = pack, .data = c.descriptor });
        if (c.manifest) |manifest| {
            const path = try std.fmt.allocPrint(allocator, "{s}/manifest.json", .{c.dir});
            defer allocator.free(path);
            try tmp.dir.writeFile(std.testing.io, .{ .sub_path = path, .data = manifest });
        }
        const root = try tmp.dir.realPathFileAlloc(std.testing.io, c.dir, allocator);
        defer allocator.free(root);
        const one = [_][]const u8{root};
        var loaded = try describe(std.testing.io, allocator, &one);
        defer loaded.deinit();
        try std.testing.expectEqual(@as(usize, 1), loaded.refusals.len);
        try std.testing.expectEqualStrings("", loaded.refusals[0].code);
    }
}
