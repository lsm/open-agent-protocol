#[path = "../tests/journal.rs"]
mod journal;

fn main() {
    let mode = std::env::args().nth(1).expect("journal mode");
    println!("{}", journal::exercise(&mode));
}
