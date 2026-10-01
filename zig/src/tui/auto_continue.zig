const std = @import("std");
const overflow = @import("overflow");

pub const continue_text = "continue";
pub const default_delay_ms: u64 = 3_000;

pub const Skip = enum {
    authentication,
    payment,
    context_overflow,
    streak_exhausted,
};

pub const Decision = union(enum) {
    send_after: u64,
    skip: Skip,
};

const auth_markers = [_][]const u8{
    "http 401",
    "http 403",
    "status 401",
    "status 403",
    "status code 401",
    "status code 403",
    "api key",
    "api_key",
    "x-api-key",
    "authentication",
    "unauthorized",
    "permission denied",
    "permission_error",
    "credential",
    "invalid token",
    "expired token",
};

const payment_markers = [_][]const u8{
    "http 402",
    "status 402",
    "status code 402",
    "payment required",
    "insufficient balance",
    "insufficient_balance",
    "insufficient funds",
    "insufficient_quota",
};

pub fn isAuthFailure(error_text: []const u8) bool {
    return containsAny(error_text, &auth_markers);
}

pub fn isPaymentFailure(error_text: []const u8) bool {
    return containsAny(error_text, &payment_markers);
}

pub fn isOverflowFailure(error_text: []const u8) bool {
    return overflow.isContextOverflowText(error_text);
}

pub fn classify(error_text: []const u8, already_continued: bool) Decision {
    if (already_continued) return .{ .skip = .streak_exhausted };
    if (isAuthFailure(error_text)) return .{ .skip = .authentication };
    if (isPaymentFailure(error_text)) return .{ .skip = .payment };
    if (isOverflowFailure(error_text)) return .{ .skip = .context_overflow };
    return .{ .send_after = default_delay_ms };
}

pub const Streak = struct {
    continued: bool = false,
    due_ms: ?i64 = null,

    pub fn reset(self: *Streak) void {
        self.continued = false;
        self.due_ms = null;
    }

    pub fn pending(self: *const Streak) bool {
        return self.due_ms != null;
    }

    pub fn onRunEndedInError(self: *Streak, error_text: []const u8, now_ms: i64) Decision {
        switch (classify(error_text, self.continued)) {
            .skip => |reason| {
                self.due_ms = null;
                return .{ .skip = reason };
            },
            .send_after => |delay| {
                self.continued = true;
                self.due_ms = now_ms + @as(i64, @intCast(delay));
                return .{ .send_after = delay };
            },
        }
    }

    pub fn onUserTurn(self: *Streak) void {
        self.reset();
    }

    pub fn due(self: *Streak, now_ms: i64) bool {
        const deadline = self.due_ms orelse return false;
        return now_ms >= deadline;
    }

    pub fn take(self: *Streak) void {
        self.due_ms = null;
    }
};

fn containsAny(haystack: []const u8, needles: []const []const u8) bool {
    for (needles) |needle| {
        if (indexOfCaseInsensitive(haystack, needle) != null) return true;
    }
    return false;
}

fn indexOfCaseInsensitive(haystack: []const u8, needle: []const u8) ?usize {
    if (needle.len > haystack.len) return null;
    var i: usize = 0;
    while (i <= haystack.len - needle.len) : (i += 1) {
        var matched = true;
        for (needle, 0..) |c, j| {
            if (std.ascii.toLower(haystack[i + j]) != std.ascii.toLower(c)) {
                matched = false;
                break;
            }
        }
        if (matched) return i;
    }
    return null;
}

test "classify sends a continue for a bare 400 the retries did not cover" {
    const decision = classify("anthropic request failed: HTTP 400 invalid_request_error", false);
    try std.testing.expectEqual(default_delay_ms, decision.send_after);
}

test "classify skips an auth failure" {
    try std.testing.expectEqual(Skip.authentication, classify("anthropic request failed: HTTP 401 (check ANTHROPIC_API_KEY is valid) (authentication_error: invalid x-api-key)", false).skip);
    try std.testing.expectEqual(Skip.authentication, classify("azure request failed: HTTP 403", false).skip);
    try std.testing.expectEqual(Skip.authentication, classify("{\"error\":{\"type\":\"invalid_api_key\"}}", false).skip);
    try std.testing.expectEqual(Skip.authentication, classify("invalid authentication credentials", false).skip);
}

test "classify skips a payment failure, which a replay would only repeat" {
    try std.testing.expectEqual(Skip.payment, classify("opencode-go request failed: HTTP 402 {\"error\":{\"message\":\"Insufficient balance\"}}", false).skip);
    try std.testing.expectEqual(Skip.payment, classify("request failed with status code 402", false).skip);
    try std.testing.expectEqual(Skip.payment, classify("{\"error\":{\"code\":\"insufficient_quota\"}}", false).skip);
    try std.testing.expectEqual(Skip.payment, classify("Payment Required", false).skip);
}

test "isPaymentFailure ignores a status that is not a payment status" {
    try std.testing.expect(!isPaymentFailure("openai request failed: HTTP 400 invalid_request_error"));
    try std.testing.expect(!isPaymentFailure("openai request failed: HTTP 429 rate limited"));
    try std.testing.expect(!isPaymentFailure(""));
}

test "classify skips a context overflow that compaction handles" {
    try std.testing.expectEqual(Skip.context_overflow, classify("prompt is too long: 210000 tokens > 200000 maximum", false).skip);
    try std.testing.expectEqual(Skip.context_overflow, classify("openai request failed: HTTP 400 error code 400 - request body (no body)", false).skip);
}

test "classify skips the second failure of one streak" {
    try std.testing.expectEqual(Skip.streak_exhausted, classify("anthropic request failed: HTTP 400", true).skip);
}

test "classify still sends for a 400 whose body does not read as an overflow" {
    const decision = classify("anthropic request failed: HTTP 400 {\"type\":\"invalid_request_error\"}", false);
    try std.testing.expectEqual(default_delay_ms, decision.send_after);
}

test "isAuthFailure ignores a status that is not an auth status" {
    try std.testing.expect(!isAuthFailure("anthropic request failed: HTTP 500 internal error"));
    try std.testing.expect(!isAuthFailure("openai request failed: HTTP 404 model not found"));
    try std.testing.expect(!isAuthFailure(""));
}

test "a streak sends once and then holds" {
    var streak = Streak{};
    const first = streak.onRunEndedInError("anthropic request failed: HTTP 400", 1_000);
    try std.testing.expectEqual(default_delay_ms, first.send_after);
    try std.testing.expect(streak.pending());
    try std.testing.expect(!streak.due(1_000 + @as(i64, @intCast(default_delay_ms)) - 1));
    try std.testing.expect(streak.due(1_000 + @as(i64, @intCast(default_delay_ms))));

    streak.take();
    const second = streak.onRunEndedInError("anthropic request failed: HTTP 400", 9_000);
    try std.testing.expectEqual(Skip.streak_exhausted, second.skip);
    try std.testing.expect(!streak.pending());
}

test "a user turn clears the streak so a later failure can nudge again" {
    var streak = Streak{};
    _ = streak.onRunEndedInError("anthropic request failed: HTTP 400", 0);
    streak.onUserTurn();
    try std.testing.expect(!streak.pending());
    const decision = streak.onRunEndedInError("anthropic request failed: HTTP 400", 1_000);
    try std.testing.expectEqual(default_delay_ms, decision.send_after);
}

test "a skipped failure leaves no pending nudge" {
    var streak = Streak{};
    _ = streak.onRunEndedInError("anthropic request failed: HTTP 401", 0);
    try std.testing.expect(!streak.pending());
    try std.testing.expect(!streak.due(std.math.maxInt(i64)));
}

test "streak take clears the deadline but keeps the spent marker" {
    var streak = Streak{};
    _ = streak.onRunEndedInError("anthropic request failed: HTTP 400", 0);
    streak.take();
    try std.testing.expect(!streak.pending());
    try std.testing.expectEqual(Skip.streak_exhausted, classify("anthropic request failed: HTTP 400", streak.continued).skip);
}
