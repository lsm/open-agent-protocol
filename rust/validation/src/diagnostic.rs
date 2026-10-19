use std::fmt;

use serde::{Deserialize, Serialize};

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Phase {
    Decode,
    Schema,
    Semantic,
    Load,
}

impl Phase {
    pub fn as_str(self) -> &'static str {
        match self {
            Phase::Decode => "decode",
            Phase::Schema => "schema",
            Phase::Semantic => "semantic",
            Phase::Load => "load",
        }
    }
}

impl fmt::Display for Phase {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Diagnostic {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub fixture: String,
    pub phase: Phase,
    pub code: String,
    #[serde(default, skip_serializing_if = "is_zero")]
    pub index: usize,
    #[serde(default, skip_serializing_if = "is_zero")]
    pub line: usize,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub envelope_id: String,
    #[serde(default, rename = "type", skip_serializing_if = "String::is_empty")]
    pub envelope_type: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub pointer: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub related_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub expected: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub actual: String,
    pub message: String,
}

fn is_zero(value: &usize) -> bool {
    *value == 0
}

impl Diagnostic {
    pub fn new(fixture: &str, phase: Phase, code: &str, message: impl Into<String>) -> Self {
        Self {
            fixture: fixture.to_owned(),
            phase,
            code: code.to_owned(),
            index: 0,
            line: 0,
            envelope_id: String::new(),
            envelope_type: String::new(),
            pointer: String::new(),
            related_ids: Vec::new(),
            expected: String::new(),
            actual: String::new(),
            message: message.into(),
        }
    }

    pub fn at(mut self, index: usize, line: usize) -> Self {
        self.index = index;
        self.line = line;
        self
    }

    fn sort_key(&self) -> impl Ord + '_ {
        (
            self.index,
            self.line,
            &self.code,
            &self.pointer,
            &self.envelope_id,
            &self.envelope_type,
            &self.expected,
            &self.actual,
            &self.message,
            &self.related_ids,
        )
    }
}

impl fmt::Display for Diagnostic {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.fixture)?;
        if self.line > 0 {
            write!(f, ":{}", self.line)?;
        } else {
            write!(f, "[{}]", self.index)?;
        }
        write!(f, "{}: {}: {}", self.pointer, self.code, self.message)
    }
}

pub(crate) fn sort_diagnostics(diagnostics: &mut [Diagnostic]) {
    diagnostics.sort_by(|a, b| a.sort_key().cmp(&b.sort_key()));
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ValidationResult {
    pub diagnostics: Vec<Diagnostic>,
}

impl ValidationResult {
    pub fn valid(&self) -> bool {
        self.diagnostics.is_empty()
    }

    pub fn primary_phase(&self) -> Option<Phase> {
        self.diagnostics.first().map(|diagnostic| diagnostic.phase)
    }

    pub fn has_code(&self, code: &str) -> bool {
        self.diagnostics
            .iter()
            .any(|diagnostic| diagnostic.code == code)
    }
}
