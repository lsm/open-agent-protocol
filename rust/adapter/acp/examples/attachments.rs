use std::io::Read;

#[path = "../tests/support/attachments.rs"]
mod support;

fn main() {
    let mut input = String::new();
    std::io::stdin().read_to_string(&mut input).unwrap();
    let input: serde_json::Value = serde_json::from_str(&input).unwrap();
    println!("{}", support::run(&input));
}
