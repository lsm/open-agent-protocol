const std = @import("std");
const endpoint = @import("endpoint");
const deepseek = @import("deepseek_adapter");
const semantic = @import("semantic");
const jsonschema = @import("jsonschema");
const compat = @import("compat");

const testing = std.testing;

const revision = deepseek.capability_revision;
const session_id = "s1";

const Conversation = struct {
    fake: deepseek.FakeHarness,
    adapter: deepseek.Adapter,
    endpoint: endpoint.Endpoint,
    arena: std.heap.ArenaAllocator,
    trace: std.ArrayList(std.json.Value) = .empty,
    requests: usize = 0,

    fn init(self: *Conversation, script: []const u8) !void {
        self.fake = try deepseek.FakeHarness.init(testing.allocator, script);
        self.adapter = deepseek.Adapter.init(testing.allocator, self.fake.config());
        self.endpoint = endpoint.Endpoint.init(testing.allocator, self.adapter.adapter(), .{});
        self.arena = std.heap.ArenaAllocator.init(testing.allocator);
        self.trace = .empty;
        self.requests = 0;
    }

    fn deinit(self: *Conversation) void {
        self.endpoint.deinit();
        self.arena.deinit();
        self.fake.deinit(testing.allocator);
    }

    fn envelope(self: *Conversation, kind: []const u8, scope: []const u8, payload: []const u8) ![]const u8 {
        self.requests += 1;
        return std.fmt.allocPrint(self.arena.allocator(),
            \\{{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"{s}","id":"host-{d}"{s},"payload":{s}}}
        , .{ kind, self.requests, scope, payload });
    }

    fn send(self: *Conversation, line: []const u8) ![]std.json.Value {
        try self.record(line);
        try self.endpoint.handleLine(line);
        return self.collect();
    }

    fn probe(self: *Conversation, line: []const u8) !std.json.Value {
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, self.arena.allocator(), line, .{});
        const probe_id = parsed.object.get("id").?.string;
        try self.endpoint.handleLine(line);
        var answered: ?std.json.Value = null;
        while (self.endpoint.popOutbound()) |written| {
            defer testing.allocator.free(written);
            const owned = try self.arena.allocator().dupe(u8, written);
            const reply = try std.json.parseFromSliceLeaky(std.json.Value, self.arena.allocator(), owned, .{});
            const in_reply_to = reply.object.get("in_reply_to") orelse std.json.Value.null;
            if (in_reply_to == .string and std.mem.eql(u8, in_reply_to.string, probe_id)) {
                answered = reply;
                continue;
            }
            try self.trace.append(self.arena.allocator(), reply);
        }
        return answered orelse error.ProbeUnanswered;
    }

    fn record(self: *Conversation, line: []const u8) !void {
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, self.arena.allocator(), line, .{});
        try self.trace.append(self.arena.allocator(), parsed);
    }

    fn collect(self: *Conversation) ![]std.json.Value {
        const first = self.trace.items.len;
        while (self.endpoint.popOutbound()) |line| {
            defer testing.allocator.free(line);
            const owned = try self.arena.allocator().dupe(u8, line);
            try self.record(owned);
        }
        return self.trace.items[first..];
    }

    fn pumpUntil(self: *Conversation, kind: []const u8) !std.json.Value {
        var rounds: usize = 0;
        while (rounds < 2000) : (rounds += 1) {
            _ = try self.endpoint.pump(5 * std.time.ns_per_ms);
            for (try self.collect()) |written| {
                if (std.mem.eql(u8, typeOf(written), kind)) return written;
            }
        }
        return error.EventNeverArrived;
    }

    fn open(self: *Conversation) !void {
        const initialized = try self.send(try self.envelope("protocol.initialize.request", "",
            \\{"participant":{"id":"conformance","name":"OAP conformance runner"},"protocol_versions":["0.1"],"profiles":["open-agent-protocol.agent-control-core"]}
        ));
        try testing.expectEqualStrings("protocol.initialize.response", typeOf(initialized[0]));
        const described = try self.send(try self.envelope("capabilities.request", "", "{}"));
        try testing.expectEqualStrings("capabilities.response", typeOf(described[0]));
        try testing.expectEqualStrings(revision, described[0].object.get("capability_revision").?.string);
        const opened = try self.send(try self.envelope("session.open.request", ",\"session_id\":\"" ++ session_id ++ "\",\"capability_revision\":\"" ++ revision ++ "\"", "{\"session_id\":\"" ++ session_id ++ "\"}"));
        try testing.expectEqualStrings("session.open.response", typeOf(opened[0]));
    }

    fn submit(self: *Conversation, delivery: []const u8) ![]std.json.Value {
        const payload = try std.fmt.allocPrint(self.arena.allocator(),
            \\{{"session_id":"{s}","delivery":"{s}","messages":[{{"role":"user","content":"drive one scripted run"}}]}}
        , .{ session_id, delivery });
        return self.send(try self.envelope("session.message.submit.request", ",\"session_id\":\"" ++ session_id ++ "\",\"capability_revision\":\"" ++ revision ++ "\"", payload));
    }

    fn close(self: *Conversation) !void {
        try self.endpoint.finish(endpoint.default_settle_window_ns, monotonic);
        _ = try self.collect();
    }

    fn validate(self: *Conversation) !void {
        var registry = try jsonschema.Registry.initFromBundled(testing.allocator);
        defer registry.deinit();
        for (self.trace.items, 0..) |written, index| {
            var validator = jsonschema.Validator.init(testing.allocator, &registry);
            defer validator.deinit();
            if (try validator.validate("envelope.schema.json", written)) |failure| {
                std.debug.print("envelope {d} ({s}) fails the schema at {s}: {s}\n", .{ index, typeOf(written), failure.pointer, failure.keyword });
                return error.SchemaInvalid;
            }
        }
        var machine = semantic.Machine.init(testing.allocator);
        defer machine.deinit();
        for (self.trace.items, 0..) |written, index| try machine.apply(index, written);
        try machine.close();
        for (machine.diagnostics.items) |diagnostic| {
            std.debug.print("envelope {d} ({s}) draws {s}\n", .{ diagnostic.index, typeOf(self.trace.items[diagnostic.index]), diagnostic.code });
        }
        try testing.expectEqual(@as(usize, 0), machine.diagnostics.items.len);
    }
};

fn monotonic() u64 {
    return compat.time.monotonicNanos() catch 0;
}

fn typeOf(value: std.json.Value) []const u8 {
    const declared = value.object.get("type") orelse return "";
    return if (declared == .string) declared.string else "";
}

fn errorCode(value: std.json.Value) []const u8 {
    const payload = value.object.get("payload") orelse return "";
    const failure = payload.object.get("error") orelse return "";
    const code = failure.object.get("code") orelse return "";
    return code.string;
}

test "a DeepSeek turn served through the endpoint completes, and the whole exchange validates" {
    var conversation: Conversation = undefined;
    try conversation.init(deepseek.fake_prelude ++ deepseek.fake_text_turn ++ deepseek.fake_idle);
    defer conversation.deinit();
    try conversation.open();

    const admitted = try conversation.submit("auto");
    try testing.expectEqualStrings("session.message.submit.response", typeOf(admitted[0]));
    _ = try conversation.pumpUntil("run.completed");

    try conversation.close();
    try testing.expectEqual(@as(usize, 0), conversation.endpoint.sessionCount());
    try conversation.validate();
}
