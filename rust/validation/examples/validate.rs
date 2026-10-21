use std::error::Error;
use std::process::ExitCode;

use oap_validation::{ProviderValidator, ValidationResult, Validator};

type Judge = Box<dyn Fn(&[u8], &str) -> ValidationResult>;

fn main() -> Result<ExitCode, Box<dyn Error>> {
    let mut paths: Vec<_> = std::env::args_os().skip(1).collect();
    let provider = paths.first().is_some_and(|flag| flag == "--provider");
    if provider {
        paths.remove(0);
    }
    if paths.is_empty() {
        return Err("usage: validate [--provider] <trace>...".into());
    }
    let validate: Judge = if provider {
        let validator = ProviderValidator::new()?;
        Box::new(move |data, fixture| validator.validate(data, fixture))
    } else {
        let validator = Validator::new()?;
        Box::new(move |data, fixture| validator.validate(data, fixture))
    };
    let mut valid = true;
    for path in paths {
        let result = validate(&std::fs::read(&path)?, &path.to_string_lossy());
        println!("{}", serde_json::to_string(&result)?);
        valid &= result.valid();
    }
    Ok(if valid {
        ExitCode::SUCCESS
    } else {
        ExitCode::FAILURE
    })
}
