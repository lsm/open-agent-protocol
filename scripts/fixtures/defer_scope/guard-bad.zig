fn plain(flag: bool, value: *u8) void {
    if (flag) {
        defer release(value);
    }
    consume(value);
    _ = flag;
}
