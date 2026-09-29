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

fn branches(flag: bool, value: *u8) void {
    if (flag) {
        defer release(value);
    } else {
        other();
    }
    consume(value);
    _ = flag;
}

fn captured(found: ?u8, value: *u8) void {
    if (found) |byte| {
        defer release(value);
        use(byte);
    }
    consume(value);
    _ = found;
}
