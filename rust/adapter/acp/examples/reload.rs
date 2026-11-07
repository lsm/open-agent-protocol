use std::io::Read;

#[path = "../tests/support/reload.rs"]
mod support;

fn main() {
    let mut input = String::new();
    std::io::stdin().read_to_string(&mut input).unwrap();
    println!("{}", support::run(&serde_json::from_str(&input).unwrap()));
}
