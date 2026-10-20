use std::error::Error;
use std::process::ExitCode;

use oap_validation::Validator;

fn main() -> Result<ExitCode, Box<dyn Error>> {
    let paths: Vec<_> = std::env::args_os().skip(1).collect();
    if paths.is_empty() {
        return Err("usage: validate <trace>...".into());
    }
    let validator = Validator::new()?;
    let mut valid = true;
    for path in paths {
        let result = validator.validate(&std::fs::read(&path)?, &path.to_string_lossy());
        println!("{}", serde_json::to_string(&result)?);
        valid &= result.valid();
    }
    Ok(if valid {
        ExitCode::SUCCESS
    } else {
        ExitCode::FAILURE
    })
}
