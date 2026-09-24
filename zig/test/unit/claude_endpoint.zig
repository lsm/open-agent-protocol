const std = @import("std");
const endpoint = @import("endpoint");
const claude = @import("claude_adapter");
const semantic = @import("semantic");
const jsonschema = @import("jsonschema");
const compat = @import("compat");

const testing = std.testing;

const revision = claude.capability_revision;
const session_id = "e2e";

const Conversation = struct {
    fake: claude.FakeClaude,
    adapter: claude.Adapter,
    endpoint: endpoint.Endpoint,
    arena: std.heap.ArenaAllocator,
    trace: std.ArrayList(std.json.Value) = .empty,
    requests: usize = 0,

    fn init(self: *Conversation, script: []const u8) !void {
        self.fake = try claude.FakeClaude.init(testing.allocator, script);
        self.adapter = claude.Adapter.init(testing.allocator, self.fake.config());
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

    fn answer(self: *Conversation, gate: std.json.Value, option: []const u8) ![]std.json.Value {
        const asked = gate.object.get("payload").?.object;
        const run_id = asked.get("run_id").?.string;
        const scratch = self.arena.allocator();
        const payload = try std.fmt.allocPrint(scratch,
            \\{{"interaction_id":"{s}","requested_by":"{s}","responded_by":"{s}","session_id":"{s}","run_id":"{s}","answers":[{{"question_id":"decision","selected_option_ids":["{s}"]}}]}}
        , .{ asked.get("interaction_id").?.string, asked.get("requested_by").?.string, asked.get("responded_by").?.string, session_id, run_id, option });
        const scope = try std.fmt.allocPrint(scratch, ",\"session_id\":\"{s}\",\"run_id\":\"{s}\",\"capability_revision\":\"{s}\"", .{ session_id, run_id, revision });
        return self.send(try self.envelope("user.input.resolve.request", scope, payload));
    }

    fn cancel(self: *Conversation, run_id: []const u8) ![]std.json.Value {
        const scratch = self.arena.allocator();
        const payload = try std.fmt.allocPrint(scratch, "{{\"session_id\":\"{s}\",\"run_id\":\"{s}\"}}", .{ session_id, run_id });
        const scope = try std.fmt.allocPrint(scratch, ",\"session_id\":\"{s}\",\"run_id\":\"{s}\",\"capability_revision\":\"{s}\"", .{ session_id, run_id, revision });
        return self.send(try self.envelope("run.cancel.request", scope, payload));
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

test "a gated claude turn answered through the endpoint completes, and the whole exchange validates" {
    var conversation: Conversation = undefined;
    try conversation.init(claude.fake_prelude ++ claude.fake_gated_turn ++ claude.fake_idle);
    defer conversation.deinit();
    try conversation.open();

    const admitted = try conversation.submit("auto");
    try testing.expectEqualStrings("session.message.submit.response", typeOf(admitted[0]));
    try testing.expectEqualStrings("run.started", typeOf(admitted[1]));

    const gate = try conversation.pumpUntil("user.input.requested");
    try testing.expectEqualStrings("conformance", gate.object.get("payload").?.object.get("responded_by").?.string);
    const resolved = try conversation.answer(gate, "allow");
    try testing.expectEqualStrings("user.input.resolve.response", typeOf(resolved[0]));
    try testing.expectEqualStrings("user.input.resolved", typeOf(resolved[1]));

    const completed = try conversation.pumpUntil("run.completed");
    try testing.expectEqualStrings("answered allow", completed.object.get("payload").?.object.get("final_response").?.object.get("content").?.string);

    const state = try conversation.send(try conversation.envelope("session.state.request", ",\"session_id\":\"" ++ session_id ++ "\",\"capability_revision\":\"" ++ revision ++ "\"", "{\"session_id\":\"" ++ session_id ++ "\"}"));
    try testing.expectEqualStrings("idle", state[0].object.get("payload").?.object.get("status").?.string);

    const listed = try conversation.send(try conversation.envelope("action.tools.list.request", ",\"session_id\":\"" ++ session_id ++ "\",\"capability_revision\":\"" ++ revision ++ "\"", "{\"session_id\":\"" ++ session_id ++ "\",\"allow_degraded_features\":[\"action.tools.list\"]}"));
    try testing.expectEqualStrings("action.tools.list.response", typeOf(listed[0]));
    try testing.expectEqual(@as(usize, 3), listed[0].object.get("payload").?.object.get("tools").?.array.items.len);

    const stale = try conversation.probe(try conversation.envelope("session.state.request", ",\"session_id\":\"" ++ session_id ++ "\",\"capability_revision\":\"claude-code-0-stale\"", "{\"session_id\":\"" ++ session_id ++ "\"}"));
    try testing.expectEqualStrings("stale_capabilities", errorCode(stale));

    const queued = try conversation.submit("queue");
    try testing.expectEqualStrings("unsupported_feature", errorCode(queued[0]));
    try testing.expectEqualStrings("session.message.delivery.queue", queued[0].object.get("payload").?.object.get("error").?.object.get("details").?.object.get("feature").?.string);

    try conversation.close();
    try testing.expectEqual(@as(usize, 0), conversation.endpoint.sessionCount());
    try conversation.validate();
    const written = try conversation.fake.written(conversation.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "\"behavior\":\"allow\"") != null);
}

test "a claude run cancelled through the endpoint is acknowledged before it settles cancelled, and the exchange validates" {
    var conversation: Conversation = undefined;
    try conversation.init(claude.fake_prelude ++ claude.fake_interrupted_turn ++ claude.fake_idle);
    defer conversation.deinit();
    try conversation.open();

    const admitted = try conversation.submit("auto");
    const run_id = admitted[0].object.get("payload").?.object.get("run_id").?.string;
    _ = try conversation.pumpUntil("content.delta");

    const acknowledged = try conversation.cancel(run_id);
    try testing.expectEqualStrings("run.cancel.response", typeOf(acknowledged[0]));
    try testing.expectEqualStrings("cancelling", acknowledged[0].object.get("payload").?.object.get("status").?.string);
    const cancelled = if (acknowledged.len > 1 and std.mem.eql(u8, typeOf(acknowledged[acknowledged.len - 1]), "run.cancelled"))
        acknowledged[acknowledged.len - 1]
    else
        try conversation.pumpUntil("run.cancelled");
    try testing.expectEqualStrings(run_id, cancelled.object.get("run_id").?.string);

    const again = try conversation.cancel(run_id);
    try testing.expectEqualStrings("run_not_found", errorCode(again[0]));

    try conversation.close();
    try conversation.validate();
}

test "a claude child that exits mid-run fails the run it held, and the exchange validates" {
    var conversation: Conversation = undefined;
    try conversation.init(claude.fake_prelude ++
        \\take; uuid=$(field uuid)
        \\printf '{"type":"stream_event","event":{"type":"message_start"},"session_id":"n","uuid":"e1","user_message_uuid":"%s"}\n' "$uuid"
        \\exit 7
        \\
    );
    defer conversation.deinit();
    try conversation.open();

    _ = try conversation.submit("auto");
    const failed = try conversation.pumpUntil("run.failed");
    try testing.expectEqualStrings("claude_process_exit", failed.object.get("payload").?.object.get("error").?.object.get("code").?.string);

    const state = try conversation.send(try conversation.envelope("session.state.request", ",\"session_id\":\"" ++ session_id ++ "\",\"capability_revision\":\"" ++ revision ++ "\"", "{\"session_id\":\"" ++ session_id ++ "\"}"));
    try testing.expectEqualStrings("session_closed", errorCode(state[0]));

    try conversation.close();
    try conversation.validate();
}
