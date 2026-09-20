const std = @import("std");
const schema_bytes = @import("schema_bytes");

pub const Unsupported = error{
    UnsupportedKeyword,
    UnsupportedPattern,
    UnresolvableRef,
};

pub const Error = Unsupported || std.mem.Allocator.Error || error{InvalidSchema};

pub const Alternative = struct {
    declared_type: []const u8,
    ref: []const u8,
};

pub const Failure = struct {
    pointer: []const u8,
    keyword: []const u8,
};

const supported_keywords = [_][]const u8{
    "$ref",       "$defs",   "$id",          "$schema",   "$comment",
    "title",      "description",
    "type",       "properties", "required",  "additionalProperties",
    "items",      "const",   "enum",         "allOf",     "anyOf",
    "oneOf",      "not",     "if",           "then",      "else",
    "contains",   "minimum", "minItems",     "maxItems",  "minLength",
    "uniqueItems", "pattern",
};

fn keywordSupported(name: []const u8) bool {
    for (supported_keywords) |known| {
        if (std.mem.eql(u8, known, name)) return true;
    }
    return false;
}

pub fn pointerTokenEql(token: []const u8, key: []const u8) bool {
    var at: usize = 0;
    var into: usize = 0;
    while (at < token.len) {
        var decoded = token[at];
        if (decoded == '~' and at + 1 < token.len and (token[at + 1] == '0' or token[at + 1] == '1')) {
            decoded = if (token[at + 1] == '0') '~' else '/';
            at += 2;
        } else {
            at += 1;
        }
        if (into >= key.len or key[into] != decoded) return false;
        into += 1;
    }
    return into == key.len;
}

pub fn pointerMember(object: std.json.ObjectMap, token: []const u8) ?std.json.Value {
    var it = object.iterator();
    while (it.next()) |entry| {
        if (pointerTokenEql(token, entry.key_ptr.*)) return entry.value_ptr.*;
    }
    return null;
}

pub fn resolvesLocally(root: std.json.Value, reference: []const u8) bool {
    if (std.mem.eql(u8, reference, "#")) return true;
    if (!std.mem.startsWith(u8, reference, "#/")) return false;
    var node = root;
    var parts = std.mem.splitScalar(u8, reference["#/".len..], '/');
    while (parts.next()) |token| {
        switch (node) {
            .object => |object| node = pointerMember(object, token) orelse return false,
            .array => |items| {
                const at = std.fmt.parseUnsigned(usize, token, 10) catch return false;
                if (at >= items.items.len) return false;
                node = items.items[at];
            },
            else => return false,
        }
    }
    return true;
}

pub fn unsupportedKeyword(schema: std.json.Value) ?[]const u8 {
    if (schema != .object) return null;
    var it = schema.object.iterator();
    while (it.next()) |entry| {
        if (!keywordSupported(entry.key_ptr.*)) return entry.key_ptr.*;
    }
    if (schema.object.get("pattern")) |expression| {
        if (expression != .string) return "pattern";
        if (!std.mem.eql(u8, expression.string, dotted_lowercase_label)) return "pattern";
    }
    if (schema.object.get("items")) |elements| {
        if (elements == .array) return "items";
    }
    for ([_][]const u8{ "items", "not", "if", "then", "else", "contains", "additionalProperties" }) |name| {
        const child = schema.object.get(name) orelse continue;
        if (unsupportedKeyword(child)) |found| return found;
    }
    for ([_][]const u8{ "allOf", "anyOf", "oneOf" }) |name| {
        const children = schema.object.get(name) orelse continue;
        if (children != .array) continue;
        for (children.array.items) |child| {
            if (unsupportedKeyword(child)) |found| return found;
        }
    }
    for ([_][]const u8{ "properties", "$defs" }) |name| {
        const children = schema.object.get(name) orelse continue;
        if (children != .object) continue;
        var kids = children.object.iterator();
        while (kids.next()) |kid| {
            if (unsupportedKeyword(kid.value_ptr.*)) |found| return found;
        }
    }
    return null;
}

pub const Registry = struct {
    allocator: std.mem.Allocator,
    documents: std.StringArrayHashMapUnmanaged(std.json.Parsed(std.json.Value)) = .empty,

    pub fn initFromBundled(allocator: std.mem.Allocator) !Registry {
        var self = Registry{ .allocator = allocator };
        errdefer self.deinit();
        for (schema_bytes.all) |entry| {
            const parsed = try std.json.parseFromSlice(std.json.Value, allocator, entry.bytes, .{});
            errdefer parsed.deinit();
            try self.documents.put(allocator, entry.name, parsed);
        }
        return self;
    }

    pub fn deinit(self: *Registry) void {
        for (self.documents.values()) |parsed| parsed.deinit();
        for (self.documents.keys(), 0..) |key, index| {
            if (index >= schema_bytes.all.len) self.allocator.free(key);
        }
        self.documents.deinit(self.allocator);
    }

    pub fn definitionCarryingType(self: *const Registry, document: []const u8, declared: []const u8) ?[]const u8 {
        const root_value = self.root(document) orelse return null;
        if (root_value != .object) return null;
        const defs = root_value.object.get("$defs") orelse return null;
        if (defs != .object) return null;
        var it = defs.object.iterator();
        while (it.next()) |entry| {
            if (entry.value_ptr.* != .object) continue;
            const properties = entry.value_ptr.object.get("properties") orelse continue;
            if (properties != .object) continue;
            const type_schema = properties.object.get("type") orelse continue;
            if (type_schema != .object) continue;
            const constant = type_schema.object.get("const") orelse continue;
            if (constant != .string) continue;
            if (std.mem.eql(u8, constant.string, declared)) return entry.key_ptr.*;
        }
        return null;
    }

    pub fn addDocument(self: *Registry, name: []const u8, bytes: []const u8) !void {
        if (self.documents.get(name) != null) return;
        const parsed = try std.json.parseFromSlice(std.json.Value, self.allocator, bytes, .{});
        errdefer parsed.deinit();
        const owned = try self.allocator.dupe(u8, name);
        errdefer self.allocator.free(owned);
        try self.documents.put(self.allocator, owned, parsed);
    }

    pub fn root(self: *const Registry, name: []const u8) ?std.json.Value {
        const parsed = self.documents.get(name) orelse return null;
        return parsed.value;
    }
};

const Pointer = struct {
    buffer: std.ArrayList(u8) = .empty,
    allocator: std.mem.Allocator,

    fn deinit(self: *Pointer) void {
        self.buffer.deinit(self.allocator);
    }

    fn mark(self: *const Pointer) usize {
        return self.buffer.items.len;
    }

    fn rewind(self: *Pointer, to: usize) void {
        self.buffer.shrinkRetainingCapacity(to);
    }

    fn pushKey(self: *Pointer, key: []const u8) !void {
        try self.buffer.append(self.allocator, '/');
        for (key) |c| switch (c) {
            '~' => try self.buffer.appendSlice(self.allocator, "~0"),
            '/' => try self.buffer.appendSlice(self.allocator, "~1"),
            else => try self.buffer.append(self.allocator, c),
        };
    }

    fn pushIndex(self: *Pointer, index: usize) !void {
        try self.buffer.append(self.allocator, '/');
        try self.buffer.print(self.allocator, "{d}", .{index});
    }

    fn current(self: *const Pointer) []const u8 {
        return self.buffer.items;
    }
};

pub const Validator = struct {
    registry: *const Registry,
    allocator: std.mem.Allocator,
    failures: std.ArrayList(Failure) = .empty,
    retained_pointer: ?[]u8 = null,
    overrides: std.StringArrayHashMapUnmanaged(std.json.Value) = .empty,

    pub fn init(allocator: std.mem.Allocator, registry: *const Registry) Validator {
        return .{ .registry = registry, .allocator = allocator };
    }

    pub fn deinit(self: *Validator) void {
        for (self.failures.items) |failure| self.allocator.free(failure.pointer);
        self.failures.deinit(self.allocator);
        self.overrides.deinit(self.allocator);
        if (self.retained_pointer) |owned| self.allocator.free(owned);
    }

    pub fn validate(self: *Validator, document: []const u8, instance: std.json.Value) Error!?Failure {
        return self.validateWithBranches(document, instance, &.{});
    }

    pub fn validateWithBranches(
        self: *Validator,
        document: []const u8,
        instance: std.json.Value,
        extra_branches: []const Alternative,
    ) Error!?Failure {
        const schema = self.overrides.get(document) orelse
            self.registry.root(document) orelse
            return Unsupported.UnresolvableRef;
        if (extra_branches.len == 0) return self.validateSchema(schema, document, instance);

        var arena = std.heap.ArenaAllocator.init(self.allocator);
        defer arena.deinit();
        const composed = try composeAlternatives(arena.allocator(), schema, extra_branches);
        const failure = try self.validateSchema(composed, document, instance) orelse return null;

        const carried = try self.allocator.dupe(u8, failure.pointer);
        errdefer self.allocator.free(carried);
        if (self.retained_pointer) |previous| self.allocator.free(previous);
        self.retained_pointer = carried;
        return .{ .pointer = carried, .keyword = failure.keyword };
    }

    pub fn validateSchema(self: *Validator, schema: std.json.Value, document: []const u8, instance: std.json.Value) Error!?Failure {
        var pointer = Pointer{ .allocator = self.allocator };
        defer pointer.deinit();
        for (self.failures.items) |failure| self.allocator.free(failure.pointer);
        self.failures.clearRetainingCapacity();
        try self.check(schema, document, instance, &pointer);
        if (self.failures.items.len == 0) return null;
        var best = self.failures.items[0];
        for (self.failures.items[1..]) |candidate| {
            if (std.mem.order(u8, candidate.pointer, best.pointer) == .lt) best = candidate;
        }
        return best;
    }

    fn record(self: *Validator, pointer: *const Pointer, keyword: []const u8) !void {
        const owned = try self.allocator.dupe(u8, pointer.current());
        errdefer self.allocator.free(owned);
        try self.failures.append(self.allocator, .{ .pointer = owned, .keyword = keyword });
    }

    fn passes(self: *Validator, schema: std.json.Value, document: []const u8, instance: std.json.Value, pointer: *Pointer) Error!bool {
        const before = self.failures.items.len;
        try self.check(schema, document, instance, pointer);
        const failed = self.failures.items.len > before;
        while (self.failures.items.len > before) {
            const dropped = self.failures.pop().?;
            self.allocator.free(dropped.pointer);
        }
        return !failed;
    }

    fn check(self: *Validator, schema: std.json.Value, document: []const u8, instance: std.json.Value, pointer: *Pointer) Error!void {
        switch (schema) {
            .bool => |always| {
                if (!always) try self.record(pointer, "false");
                return;
            },
            .object => {},
            else => return error.InvalidSchema,
        }
        const object = schema.object;

        var it = object.iterator();
        while (it.next()) |entry| {
            if (!keywordSupported(entry.key_ptr.*)) return Unsupported.UnsupportedKeyword;
        }

        if (object.get("$ref")) |ref| {
            if (ref != .string) return error.InvalidSchema;
            const target = try self.resolve(ref.string, document);
            try self.check(target.schema, target.document, instance, pointer);
        }
        if (object.get("type")) |want| try self.checkType(want, instance, pointer);
        if (object.get("const")) |want| {
            if (!valueEql(want, instance)) try self.record(pointer, "const");
        }
        if (object.get("enum")) |want| {
            if (want != .array) return error.InvalidSchema;
            var matched = false;
            for (want.array.items) |candidate| {
                if (valueEql(candidate, instance)) matched = true;
            }
            if (!matched) try self.record(pointer, "enum");
        }
        if (object.get("allOf")) |branches| {
            if (branches != .array) return error.InvalidSchema;
            for (branches.array.items) |branch| try self.check(branch, document, instance, pointer);
        }
        if (object.get("anyOf")) |branches| {
            if (branches != .array) return error.InvalidSchema;
            var matched = false;
            for (branches.array.items) |branch| {
                if (try self.passes(branch, document, instance, pointer)) matched = true;
            }
            if (!matched) try self.record(pointer, "anyOf");
        }
        if (object.get("oneOf")) |branches| {
            if (branches != .array) return error.InvalidSchema;
            var matches: usize = 0;
            for (branches.array.items) |branch| {
                if (try self.passes(branch, document, instance, pointer)) matches += 1;
            }
            if (matches > 1) {
                try self.record(pointer, "oneOf");
            } else if (matches == 0) {
                if (try self.discriminatedBranch(branches.array.items, document, instance)) |chosen| {
                    try self.check(chosen, document, instance, pointer);
                } else {
                    try self.record(pointer, "oneOf");
                }
            }
        }
        if (object.get("not")) |branch| {
            if (try self.passes(branch, document, instance, pointer)) try self.record(pointer, "not");
        }
        if (object.get("if")) |condition| {
            const held = try self.passes(condition, document, instance, pointer);
            if (held) {
                if (object.get("then")) |branch| try self.check(branch, document, instance, pointer);
            } else {
                if (object.get("else")) |branch| try self.check(branch, document, instance, pointer);
            }
        }
        switch (instance) {
            .object => try self.checkObject(object, document, instance, pointer),
            .array => try self.checkArray(object, document, instance, pointer),
            .string => |text| try self.checkString(object, text, pointer),
            .integer, .float, .number_string => try self.checkNumber(object, instance, pointer),
            else => {},
        }
    }

    fn checkObject(self: *Validator, object: std.json.ObjectMap, document: []const u8, instance: std.json.Value, pointer: *Pointer) Error!void {
        const members = instance.object;
        if (object.get("required")) |required| {
            if (required != .array) return error.InvalidSchema;
            for (required.array.items) |name| {
                if (name != .string) return error.InvalidSchema;
                if (members.get(name.string) == null) try self.record(pointer, "required");
            }
        }
        const properties = object.get("properties");
        if (properties) |declared| {
            if (declared != .object) return error.InvalidSchema;
            var it = declared.object.iterator();
            while (it.next()) |entry| {
                const present = members.get(entry.key_ptr.*) orelse continue;
                const mark = pointer.mark();
                try pointer.pushKey(entry.key_ptr.*);
                defer pointer.rewind(mark);
                try self.check(entry.value_ptr.*, document, present, pointer);
            }
        }
        if (object.get("additionalProperties")) |extra| {
            var it = members.iterator();
            while (it.next()) |entry| {
                if (properties) |declared| {
                    if (declared.object.get(entry.key_ptr.*) != null) continue;
                }
                switch (extra) {
                    .bool => |allowed| if (!allowed) try self.record(pointer, "additionalProperties"),
                    else => {
                        const mark = pointer.mark();
                        try pointer.pushKey(entry.key_ptr.*);
                        defer pointer.rewind(mark);
                        try self.check(extra, document, entry.value_ptr.*, pointer);
                    },
                }
            }
        }
    }

    fn checkArray(self: *Validator, object: std.json.ObjectMap, document: []const u8, instance: std.json.Value, pointer: *Pointer) Error!void {
        const elements = instance.array.items;
        if (object.get("minItems")) |limit| {
            if (elements.len < try countOf(limit)) try self.record(pointer, "minItems");
        }
        if (object.get("maxItems")) |limit| {
            if (elements.len > try countOf(limit)) try self.record(pointer, "maxItems");
        }
        if (object.get("uniqueItems")) |unique| {
            if (unique == .bool and unique.bool) {
                for (elements, 0..) |left, i| {
                    for (elements[i + 1 ..]) |right| {
                        if (valueEql(left, right)) try self.record(pointer, "uniqueItems");
                    }
                }
            }
        }
        if (object.get("items")) |element_schema| {
            for (elements, 0..) |element, index| {
                const mark = pointer.mark();
                try pointer.pushIndex(index);
                defer pointer.rewind(mark);
                try self.check(element_schema, document, element, pointer);
            }
        }
        if (object.get("contains")) |element_schema| {
            var matched = false;
            for (elements) |element| {
                if (try self.passes(element_schema, document, element, pointer)) matched = true;
            }
            if (!matched) try self.record(pointer, "contains");
        }
    }

    fn checkString(self: *Validator, object: std.json.ObjectMap, text: []const u8, pointer: *Pointer) Error!void {
        if (object.get("minLength")) |limit| {
            if (text.len < try countOf(limit)) try self.record(pointer, "minLength");
        }
        if (object.get("pattern")) |expression| {
            if (expression != .string) return error.InvalidSchema;
            if (!try matchesKnownPattern(expression.string, text)) try self.record(pointer, "pattern");
        }
    }

    fn checkNumber(self: *Validator, object: std.json.ObjectMap, instance: std.json.Value, pointer: *Pointer) Error!void {
        if (object.get("minimum")) |limit| {
            const bound = try numberOf(limit);
            const actual = try numberOf(instance);
            if (actual < bound) try self.record(pointer, "minimum");
        }
    }

    fn checkType(self: *Validator, want: std.json.Value, instance: std.json.Value, pointer: *Pointer) Error!void {
        switch (want) {
            .string => |name| {
                if (!typeMatches(name, instance)) try self.record(pointer, "type");
            },
            .array => |names| {
                var matched = false;
                for (names.items) |name| {
                    if (name != .string) return error.InvalidSchema;
                    if (typeMatches(name.string, instance)) matched = true;
                }
                if (!matched) try self.record(pointer, "type");
            },
            else => return error.InvalidSchema,
        }
    }

    fn discriminatedBranch(self: *Validator, branches: []const std.json.Value, document: []const u8, instance: std.json.Value) Error!?std.json.Value {
        if (instance != .object) return null;
        const declared = instance.object.get("type") orelse return null;
        if (declared != .string) return null;
        for (branches) |branch| {
            const resolved = try self.flatten(branch, document);
            const properties = resolved.object.get("properties") orelse continue;
            if (properties != .object) continue;
            const type_schema = properties.object.get("type") orelse continue;
            if (type_schema != .object) continue;
            const constant = type_schema.object.get("const") orelse continue;
            if (constant != .string) continue;
            if (std.mem.eql(u8, constant.string, declared.string)) return branch;
        }
        return null;
    }

    fn flatten(self: *Validator, schema: std.json.Value, document: []const u8) Error!std.json.Value {
        if (schema != .object) return schema;
        if (schema.object.get("$ref")) |ref| {
            if (ref != .string) return schema;
            const target = try self.resolve(ref.string, document);
            return self.flatten(target.schema, target.document);
        }
        if (schema.object.get("allOf")) |parts| {
            if (parts == .array) {
                for (parts.array.items) |part| {
                    const inner = try self.flatten(part, document);
                    if (inner == .object and inner.object.get("properties") != null) return inner;
                }
            }
        }
        return schema;
    }

    const Resolved = struct {
        schema: std.json.Value,
        document: []const u8,
    };

    fn resolve(self: *Validator, ref: []const u8, document: []const u8) Error!Resolved {
        const hash = std.mem.indexOfScalar(u8, ref, '#');
        const file = if (hash) |at| ref[0..at] else ref;
        const fragment = if (hash) |at| ref[at + 1 ..] else "";
        const target_document = if (file.len == 0) document else stripSchemaBase(file);
        var node = self.overrides.get(target_document) orelse
            self.registry.root(target_document) orelse
            return Unsupported.UnresolvableRef;
        var parts = std.mem.splitScalar(u8, fragment, '/');
        while (parts.next()) |part| {
            if (part.len == 0) continue;
            if (node != .object) return Unsupported.UnresolvableRef;
            node = pointerMember(node.object, part) orelse return Unsupported.UnresolvableRef;
        }
        return .{ .schema = node, .document = target_document };
    }
};

pub fn withMember(
    allocator: std.mem.Allocator,
    document: std.json.Value,
    definition: []const u8,
    member: []const u8,
    member_schema: std.json.Value,
) !std.json.Value {
    if (document != .object) return document;
    const defs = document.object.get("$defs") orelse return document;
    if (defs != .object) return document;
    const target = defs.object.get(definition) orelse return document;
    if (target != .object) return document;
    const properties = target.object.get("properties") orelse return document;
    if (properties != .object) return document;

    var widened: std.json.ObjectMap = .empty;
    var property = properties.object.iterator();
    while (property.next()) |entry| try widened.put(allocator, entry.key_ptr.*, entry.value_ptr.*);
    try widened.put(allocator, member, member_schema);

    var rebuilt: std.json.ObjectMap = .empty;
    var field = target.object.iterator();
    while (field.next()) |entry| try rebuilt.put(allocator, entry.key_ptr.*, entry.value_ptr.*);
    try rebuilt.put(allocator, "properties", .{ .object = widened });

    var rebuilt_defs: std.json.ObjectMap = .empty;
    var def = defs.object.iterator();
    while (def.next()) |entry| try rebuilt_defs.put(allocator, entry.key_ptr.*, entry.value_ptr.*);
    try rebuilt_defs.put(allocator, definition, .{ .object = rebuilt });

    var rebuilt_document: std.json.ObjectMap = .empty;
    var top = document.object.iterator();
    while (top.next()) |entry| try rebuilt_document.put(allocator, entry.key_ptr.*, entry.value_ptr.*);
    try rebuilt_document.put(allocator, "$defs", .{ .object = rebuilt_defs });
    return .{ .object = rebuilt_document };
}

fn composeAlternatives(
    allocator: std.mem.Allocator,
    envelope: std.json.Value,
    alternatives: []const Alternative,
) !std.json.Value {
    if (envelope != .object) return envelope;
    const key = if (envelope.object.get("oneOf") != null) "oneOf" else "anyOf";
    const existing = envelope.object.get(key) orelse return envelope;
    if (existing != .array) return envelope;

    var members = std.json.Array.init(allocator);
    for (existing.array.items) |member| {
        try members.append(try excludeFromFallback(allocator, member, alternatives));
    }
    for (alternatives) |alternative| {
        var node: std.json.ObjectMap = .empty;
        try node.put(allocator, "$ref", .{ .string = alternative.ref });
        try members.append(.{ .object = node });
    }

    var composed: std.json.ObjectMap = .empty;
    var it = envelope.object.iterator();
    while (it.next()) |entry| try composed.put(allocator, entry.key_ptr.*, entry.value_ptr.*);
    try composed.put(allocator, key, .{ .array = members });
    return .{ .object = composed };
}

fn excludeFromFallback(
    allocator: std.mem.Allocator,
    member: std.json.Value,
    alternatives: []const Alternative,
) !std.json.Value {
    if (member != .object) return member;
    const properties = member.object.get("properties") orelse return member;
    if (properties != .object) return member;
    const discriminator = properties.object.get("type") orelse return member;
    if (discriminator != .object) return member;
    const negation = discriminator.object.get("not") orelse return member;
    if (negation != .object) return member;
    const known = negation.object.get("enum") orelse return member;
    if (known != .array) return member;

    var widened = std.json.Array.init(allocator);
    for (known.array.items) |value| try widened.append(value);
    for (alternatives) |alternative| try widened.append(.{ .string = alternative.declared_type });

    var rebuilt_negation: std.json.ObjectMap = .empty;
    try rebuilt_negation.put(allocator, "enum", .{ .array = widened });

    var rebuilt_discriminator: std.json.ObjectMap = .empty;
    try rebuilt_discriminator.put(allocator, "type", .{ .string = "string" });
    try rebuilt_discriminator.put(allocator, "not", .{ .object = rebuilt_negation });

    var rebuilt_properties: std.json.ObjectMap = .empty;
    try rebuilt_properties.put(allocator, "type", .{ .object = rebuilt_discriminator });

    var rebuilt: std.json.ObjectMap = .empty;
    if (member.object.get("type")) |declared| try rebuilt.put(allocator, "type", declared);
    if (member.object.get("required")) |required| try rebuilt.put(allocator, "required", required);
    try rebuilt.put(allocator, "properties", .{ .object = rebuilt_properties });
    return .{ .object = rebuilt };
}

pub const schema_base = "https://open-agent-protocol.local/v0.1/";

fn stripSchemaBase(uri: []const u8) []const u8 {
    if (std.mem.startsWith(u8, uri, schema_base)) return uri[schema_base.len..];
    return uri;
}

fn typeMatches(name: []const u8, instance: std.json.Value) bool {
    if (std.mem.eql(u8, name, "object")) return instance == .object;
    if (std.mem.eql(u8, name, "array")) return instance == .array;
    if (std.mem.eql(u8, name, "string")) return instance == .string;
    if (std.mem.eql(u8, name, "boolean")) return instance == .bool;
    if (std.mem.eql(u8, name, "null")) return instance == .null;
    if (std.mem.eql(u8, name, "integer")) return switch (instance) {
        .integer => true,
        .float => |f| @floor(f) == f,
        else => false,
    };
    if (std.mem.eql(u8, name, "number")) return switch (instance) {
        .integer, .float, .number_string => true,
        else => false,
    };
    return false;
}

fn countOf(value: std.json.Value) Error!usize {
    return switch (value) {
        .integer => |n| if (n < 0) error.InvalidSchema else @intCast(n),
        else => error.InvalidSchema,
    };
}

fn numberOf(value: std.json.Value) Error!f64 {
    return switch (value) {
        .integer => |n| @floatFromInt(n),
        .float => |f| f,
        .number_string => |s| std.fmt.parseFloat(f64, s) catch error.InvalidSchema,
        else => error.InvalidSchema,
    };
}

const dotted_lowercase_label = "^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$";

fn matchesKnownPattern(expression: []const u8, text: []const u8) Error!bool {
    if (!std.mem.eql(u8, expression, dotted_lowercase_label)) return Unsupported.UnsupportedPattern;
    var labels = std.mem.splitScalar(u8, text, '.');
    var count: usize = 0;
    while (labels.next()) |label| {
        count += 1;
        if (label.len == 0) return false;
        for (label) |c| {
            if (!(std.ascii.isLower(c) or std.ascii.isDigit(c) or c == '-')) return false;
        }
        if (label[0] == '-' or label[label.len - 1] == '-') return false;
    }
    return count >= 2;
}

fn valueEql(a: std.json.Value, b: std.json.Value) bool {
    return switch (a) {
        .null => b == .null,
        .bool => |x| b == .bool and b.bool == x,
        .string => |x| b == .string and std.mem.eql(u8, b.string, x),
        .integer, .float, .number_string => blk: {
            const left = numberOf(a) catch break :blk false;
            const right = numberOf(b) catch break :blk false;
            break :blk left == right;
        },
        .array => |x| blk: {
            if (b != .array or b.array.items.len != x.items.len) break :blk false;
            for (x.items, b.array.items) |left, right| {
                if (!valueEql(left, right)) break :blk false;
            }
            break :blk true;
        },
        .object => |x| blk: {
            if (b != .object or b.object.count() != x.count()) break :blk false;
            var it = x.iterator();
            while (it.next()) |entry| {
                const other = b.object.get(entry.key_ptr.*) orelse break :blk false;
                if (!valueEql(entry.value_ptr.*, other)) break :blk false;
            }
            break :blk true;
        },
    };
}

test "a failure survives the branch validations that follow it" {
    const allocator = std.testing.allocator;
    var registry = try Registry.initFromBundled(allocator);
    defer registry.deinit();

    const line =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","payload":{"session_id":"s"}}
    ;
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
    defer parsed.deinit();

    var validator = Validator.init(allocator, &registry);
    defer validator.deinit();

    const branches = [_]Alternative{.{ .declared_type = "run.started", .ref = "common.schema.json#/$defs/nonEmptyString" }};
    const failure = (try validator.validateWithBranches("envelope.schema.json", parsed.value, &branches)).?;

    try std.testing.expect(failure.pointer.len == 0 or failure.pointer[0] == '/');
    for (failure.pointer) |c| try std.testing.expect(c != 0xaa);
    try std.testing.expect(failure.keyword.len > 0);
}

test "a pack branch does not excuse an envelope from the root the profile requires" {
    const allocator = std.testing.allocator;
    var registry = try Registry.initFromBundled(allocator);
    defer registry.deinit();

    const pack_schema =
        \\{"$id":"pack/storage.schema.json","$defs":{"objectsRead":{"type":"object","required":["session_id"],
        \\"properties":{"type":{"const":"com.example.storage.objects.read"},"session_id":{"type":"string"},
        \\"payload":{"type":"object"}}}}}
    ;
    try registry.addDocument("pack/storage.schema.json", pack_schema);
    const branches = [_]Alternative{.{ .declared_type = "com.example.storage.objects.read", .ref = "pack/storage.schema.json#/$defs/objectsRead" }};

    const complete =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core",
        \\"type":"com.example.storage.objects.read","id":"e1","session_id":"s","payload":{}}
    ;
    const without_protocol =
        \\{"version":"0.1","profile":"open-agent-protocol.agent-control-core",
        \\"type":"com.example.storage.objects.read","id":"e1","session_id":"s","payload":{}}
    ;

    for ([_]struct { line: []const u8, accepted: bool }{
        .{ .line = complete, .accepted = true },
        .{ .line = without_protocol, .accepted = false },
    }) |case| {
        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, case.line, .{});
        defer parsed.deinit();
        var validator = Validator.init(allocator, &registry);
        defer validator.deinit();
        const failure = try validator.validateWithBranches("envelope.schema.json", parsed.value, &branches);
        try std.testing.expectEqual(case.accepted, failure == null);
    }
}
