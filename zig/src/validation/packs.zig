const std = @import("std");
const jsonschema = @import("jsonschema");

pub const pack_base_uri = "https://open-agent-protocol.local/ext/";

pub const Branch = jsonschema.Alternative;

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
};

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
    if (!std.mem.startsWith(u8, pointer, "#/")) return null;
    var current = root;
    var rest = pointer[2..];
    while (true) {
        const slash = std.mem.indexOfScalar(u8, rest, '/');
        const raw = if (slash) |at| rest[0..at] else rest;
        rest = if (slash) |at| rest[at + 1 ..] else "";
        const token = try unescapeToken(allocator, raw);
        switch (current) {
            .object => |members| current = members.get(token) orelse return null,
            .array => |list| {
                const index = std.fmt.parseInt(usize, token, 10) catch return null;
                if (index >= list.items.len) return null;
                current = list.items[index];
            },
            else => return null,
        }
        if (rest.len == 0) return current;
    }
}

fn cleanCited(allocator: std.mem.Allocator, name: []const u8) ![]const u8 {
    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(allocator);
    var parts: std.ArrayList([]const u8) = .empty;
    defer parts.deinit(allocator);
    var start: usize = 0;
    var index: usize = 0;
    while (index <= name.len) : (index += 1) {
        if (index < name.len and name[index] != '/') continue;
        const part = name[start..index];
        start = index + 1;
        if (part.len == 0 or std.mem.eql(u8, part, ".")) continue;
        if (std.mem.eql(u8, part, "..")) {
            if (parts.items.len == 0) {
                for (parts.items) |held| allocator.free(held);
                return try out.toOwnedSlice(allocator);
            }
            allocator.free(parts.pop().?);
            continue;
        }
        try parts.append(allocator, try allocator.dupe(u8, part));
    }
    for (parts.items, 0..) |part, position| {
        if (position != 0) try out.append(allocator, '/');
        try out.appendSlice(allocator, part);
    }
    for (parts.items) |held| allocator.free(held);
    return try out.toOwnedSlice(allocator);
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
    var load_refusals = std.ArrayList(Refusal).empty;

    for (pack_dirs) |dir| {
        const descriptor_path = try std.fs.path.join(allocator, &.{ dir, "pack.json" });
        const descriptor_bytes = try readAll(io, allocator, descriptor_path);
        const descriptor = try std.json.parseFromSliceLeaky(std.json.Value, allocator, descriptor_bytes, .{});
        if (descriptor != .object) return error.InvalidPackDescriptor;
        const pack_id = (descriptor.object.get("id") orelse return error.InvalidPackDescriptor).string;
        const version = (descriptor.object.get("version") orelse return error.InvalidPackDescriptor).string;

        var pack_documents: std.ArrayList(Document) = .empty;
        if (descriptor.object.get("schemas")) |schemas| {
            if (schemas != .array) return error.InvalidPackDescriptor;
            for (schemas.array.items) |schema_name| {
                if (schema_name != .string) return error.InvalidPackDescriptor;
                const file = schema_name.string;
                const schema_path = try std.fs.path.join(allocator, &.{ dir, file });
                const schema_bytes = try readAll(io, allocator, schema_path);
                const key = try std.fmt.allocPrint(allocator, "{s}{s}/{s}/{s}", .{ pack_base_uri, pack_id, version, file });
                try pack_documents.append(allocator, .{
                    .name = try cleanCited(allocator, file),
                    .value = try std.json.parseFromSliceLeaky(std.json.Value, allocator, schema_bytes, .{}),
                });
                if (registry) |target| try target.addDocument(key, schema_bytes);
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
            const schema_ref = (entry.object.get("schema") orelse continue).string;
            const hash = std.mem.indexOfScalar(u8, schema_ref, '#');
            const cited = try cleanCited(allocator, if (hash) |at| schema_ref[0..at] else schema_ref);
            const pointer: []const u8 = if (hash) |at| schema_ref[at..] else "";
            var resolved: ?std.json.Value = null;
            for (pack_documents.items) |document| {
                if (!std.mem.eql(u8, document.name, cited)) continue;
                resolved = if (pointer.len == 0 or std.mem.eql(u8, pointer, "#"))
                    document.value
                else
                    try pointerAt(allocator, document.value, pointer);
                break;
            }
            if (resolved == null) {
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

fn writePack(allocator: std.mem.Allocator, tmp: *std.testing.TmpDir, dir: []const u8, schema: []const u8, descriptor: []const u8) ![:0]u8 {
    try tmp.dir.createDir(std.testing.io, dir, .default_dir);
    const schema_path = try std.fmt.allocPrint(allocator, "{s}/types.schema.json", .{dir});
    defer allocator.free(schema_path);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = schema_path, .data = schema });
    const descriptor_path = try std.fmt.allocPrint(allocator, "{s}/pack.json", .{dir});
    defer allocator.free(descriptor_path);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = descriptor_path, .data = descriptor });
    return tmp.dir.realPathFileAlloc(std.testing.io, dir, allocator);
}

test "a contributed branch must pin type to a const naming its own declared type" {
    const allocator = std.testing.allocator;
    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();

    const fixtures = [_]struct { dir: []const u8, code: []const u8 }{
        .{ .dir = "fixtures/packs/bad-branch-unpinned", .code = pack_branch_unpinned },
        .{ .dir = "fixtures/packs/bad-branch-undeclared-type", .code = pack_branch_undeclared_type },
    };

    for (fixtures) |fixture| {
        const one = [_][]const u8{fixture.dir};
        var refused = try load(std.testing.io, allocator, &registry, &one);
        defer refused.deinit();
        try std.testing.expect(carriesCode(refused.refusals, fixture.code));
        try std.testing.expectEqual(@as(usize, 0), refused.branches.len);
    }

    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    const pinned: [:0]u8 = try writePack(allocator, &tmp, "pinned",
        \\{"$defs": {"ping": {"type": "object", "required": ["type", "session_id"], "properties": {"type": {"const": "com.example.pinned.ping"}, "session_id": {"type": "string"}}}}}
    ,
        \\{"id": "com.example.pinned", "version": "1.0.0", "schemas": ["types.schema.json"], "envelope_types": [{"type": "com.example.pinned.ping", "role": "event", "schema": "types.schema.json#/$defs/ping"}]}
    ,
    );
    defer allocator.free(pinned);
    const pinned_dirs = [_][]const u8{pinned};
    var good = try load(std.testing.io, allocator, &registry, &pinned_dirs);
    defer good.deinit();
    try std.testing.expectEqual(@as(usize, 0), good.refusals.len);
    try std.testing.expectEqual(@as(usize, 1), good.branches.len);

    const complete =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core",
        \\"type":"com.example.pinned.ping","id":"e1","session_id":"s","payload":{}}
    ;
    const short =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core",
        \\"type":"com.example.pinned.ping","id":"e1","payload":{}}
    ;
    try std.testing.expect(try judgesAsAccepted(allocator, &registry, good.branches, complete));
    try std.testing.expect(!try judgesAsAccepted(allocator, &registry, good.branches, short));

    const mixed: [:0]u8 = try writePack(allocator, &tmp, "mixed",
        \\{"$defs": {"good": {"type": "object", "properties": {"type": {"const": "com.example.mixed.good"}}}, "bad": {"type": "object", "properties": {"type": {"type": "string"}}}}}
    ,
        \\{"id": "com.example.mixed", "version": "1.0.0", "schemas": ["types.schema.json"], "envelope_types": [{"type": "com.example.mixed.good", "role": "event", "schema": "types.schema.json#/$defs/good"}, {"type": "com.example.mixed.bad", "role": "event", "schema": "types.schema.json#/$defs/bad"}]}
    ,
    );
    defer allocator.free(mixed);
    const mixed_dirs = [_][]const u8{mixed};
    var partial = try load(std.testing.io, allocator, &registry, &mixed_dirs);
    defer partial.deinit();
    try std.testing.expectEqual(@as(usize, 1), partial.refusals.len);
    try std.testing.expectEqualStrings(pack_branch_unpinned, partial.refusals[0].code);
    try std.testing.expectEqualStrings("com.example.mixed.bad", partial.refusals[0].detail);
    try std.testing.expectEqual(@as(usize, 0), partial.branches.len);

    const escaped: [:0]u8 = try writePack(allocator, &tmp, "escaped",
        \\{"$defs": {"a/b": {"type": "object", "properties": {"type": {"const": "com.example.escaped.slash"}}}, "c~d": {"type": "object", "properties": {"type": {"const": "com.example.escaped.tilde"}}}}}
    ,
        \\{"id": "com.example.escaped", "version": "1.0.0", "schemas": ["types.schema.json"], "envelope_types": [{"type": "com.example.escaped.slash", "role": "event", "schema": "types.schema.json#/$defs/a~1b"}, {"type": "com.example.escaped.tilde", "role": "event", "schema": "types.schema.json#/$defs/c~0d"}]}
    ,
    );
    defer allocator.free(escaped);
    const escaped_dirs = [_][]const u8{escaped};
    var unescaped = try load(std.testing.io, allocator, &registry, &escaped_dirs);
    defer unescaped.deinit();
    try std.testing.expectEqual(@as(usize, 0), unescaped.refusals.len);
    try std.testing.expectEqual(@as(usize, 2), unescaped.branches.len);

    const cleaned: [:0]u8 = try writePack(allocator, &tmp, "cleaned",
        \\{"$defs": {"ping": {"type": "object", "properties": {"type": {"const": "com.example.cleaned.ping"}}}}}
    ,
        \\{"id": "com.example.cleaned", "version": "1.0.0", "schemas": ["sub/../types.schema.json"], "envelope_types": [{"type": "com.example.cleaned.ping", "role": "event", "schema": "sub/../types.schema.json#/$defs/ping"}]}
    ,
    );
    defer allocator.free(cleaned);
    try tmp.dir.createDir(std.testing.io, "cleaned/sub", .default_dir);
    const cleaned_dirs = [_][]const u8{cleaned};
    var normalized = try load(std.testing.io, allocator, &registry, &cleaned_dirs);
    defer normalized.deinit();
    try std.testing.expectEqual(@as(usize, 0), normalized.refusals.len);
    try std.testing.expectEqual(@as(usize, 1), normalized.branches.len);

    const whole_good: [:0]u8 = try writePack(allocator, &tmp, "wholegood",
        \\{"type": "object", "properties": {"type": {"const": "com.example.wholegood.ping"}}}
    ,
        \\{"id": "com.example.wholegood", "version": "1.0.0", "schemas": ["types.schema.json"], "envelope_types": [{"type": "com.example.wholegood.ping", "role": "event", "schema": "types.schema.json"}]}
    ,
    );
    defer allocator.free(whole_good);
    const whole_good_dirs = [_][]const u8{whole_good};
    var whole = try load(std.testing.io, allocator, &registry, &whole_good_dirs);
    defer whole.deinit();
    try std.testing.expectEqual(@as(usize, 0), whole.refusals.len);
    try std.testing.expectEqual(@as(usize, 1), whole.branches.len);

    const whole_bad: [:0]u8 = try writePack(allocator, &tmp, "wholebad",
        \\{"type": "object", "properties": {"type": {"type": "string"}}}
    ,
        \\{"id": "com.example.wholebad", "version": "1.0.0", "schemas": ["types.schema.json"], "envelope_types": [{"type": "com.example.wholebad.ping", "role": "event", "schema": "types.schema.json"}]}
    ,
    );
    defer allocator.free(whole_bad);
    const whole_bad_dirs = [_][]const u8{whole_bad};
    var bypass = try load(std.testing.io, allocator, &registry, &whole_bad_dirs);
    defer bypass.deinit();
    try std.testing.expectEqual(@as(usize, 1), bypass.refusals.len);
    try std.testing.expectEqualStrings(pack_branch_unpinned, bypass.refusals[0].code);
    try std.testing.expectEqual(@as(usize, 0), bypass.branches.len);

    const dangling: [:0]u8 = try writePack(allocator, &tmp, "dangling",
        \\{"$defs": {"ping": {"type": "object", "properties": {"type": {"const": "com.example.dangling.ping"}}}}}
    ,
        \\{"id": "com.example.dangling", "version": "1.0.0", "schemas": ["types.schema.json"], "envelope_types": [{"type": "com.example.dangling.ping", "role": "event", "schema": "types.schema.json#/$defs/absent"}]}
    ,
    );
    defer allocator.free(dangling);
    const dangling_dirs = [_][]const u8{dangling};
    var unresolved = try load(std.testing.io, allocator, &registry, &dangling_dirs);
    defer unresolved.deinit();
    try std.testing.expectEqual(@as(usize, 1), unresolved.refusals.len);
    try std.testing.expectEqualStrings("", unresolved.refusals[0].code);
    try std.testing.expectEqual(@as(usize, 0), unresolved.branches.len);
}
