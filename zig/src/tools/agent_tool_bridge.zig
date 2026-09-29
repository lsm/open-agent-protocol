const std = @import("std");
const agent_types = @import("agent_types");

pub const SessionId = agent_types.SessionId;
pub const Ulid = agent_types.Ulid;

pub const Request = struct {
    session_id: SessionId,
    generation: u64,
    tool_call_id: []u8,
    tool_name: []u8,
    args_json: []u8,

    pub fn deinit(self: *Request, allocator: std.mem.Allocator) void {
        allocator.free(self.tool_call_id);
        allocator.free(self.tool_name);
        allocator.free(self.args_json);
        self.* = undefined;
    }
};

pub const Key = struct {
    session_id: SessionId,
    tool_call_id: []u8,
    request_message_id: Ulid,
    generation: u64,

    pub fn deinit(self: *Key, allocator: std.mem.Allocator) void {
        allocator.free(self.tool_call_id);
        self.* = undefined;
    }
};

pub const Result = struct {
    session_id: SessionId,
    tool_call_id: []u8,
    in_reply_to: ?Ulid,
    result_json: []u8,
    details_json: []u8,
    is_error: bool,

    pub fn deinit(self: *Result, allocator: std.mem.Allocator) void {
        allocator.free(self.tool_call_id);
        allocator.free(self.result_json);
        allocator.free(self.details_json);
        self.* = undefined;
    }
};
