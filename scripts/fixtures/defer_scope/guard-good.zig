fn shared(flag: bool, value: *u8) void {
    if (flag) {
        defer release(value);
        use(value);
    }
    consume(value);
    _ = flag;
}

fn whole(value: *u8) void {
    defer release(value);
    consume(value);
}

fn captured(found: ?u8, value: *u8) void {
    if (found) |byte| {
        defer release(value);
        use(byte);
    }
    consume(value);
    _ = found;
}

fn branch_with_else(flag: bool, value: *u8) void {
    if (flag) {
        defer release(value);
        use(value);
    } else {
        other();
    }
    consume(value);
    _ = flag;
}

fn shares_a_line(value: *u8) void {
    if (true) { defer release(value); noop(); }
    consume(value);
}

fn body_shares_the_block(flag: bool, value: *u8) void {
    if (flag) { defer release(value); noop(); }
    consume(value);
    _ = flag;
}

fn loop_body_is_scoped_to_the_pass(lines: []const []const u8, value: *u8) void {
    for (lines) |line| {
        defer release(value);
        consume(line);
    }
}

fn loop_frees_its_own_capture(found: ?[]const u8, value: *u8) void {
    while (found) |line| {
        defer release(line);
        consume(value);
    }
}
