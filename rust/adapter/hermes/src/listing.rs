use oap_adapter::gojson::members;
use oap_adapter::{Error, NativeListRequest, NativeListing};
use serde_json::value::RawValue;

use crate::adapter::Adapter;

type Members = Vec<(String, Box<RawValue>)>;

impl Adapter {
    pub fn native_list(&self, request: NativeListRequest) -> Result<Vec<NativeListing>, Error> {
        let Some(answered) = self.factory.list(request.limit) else {
            return Ok(Vec::new());
        };
        let Some(rows) = sessions(answered?.get()) else {
            return Ok(Vec::new());
        };
        let mut listed = Vec::new();
        for raw in rows {
            if listed.len() >= request.limit {
                break;
            }
            let row = members(raw.get()).unwrap_or_default();
            let native_id = text(&row, "id");
            if native_id.is_empty() {
                continue;
            }
            let mut title = text(&row, "title");
            if title.is_empty() {
                title = text(&row, "preview");
            }
            listed.push(NativeListing {
                native_id,
                title,
                updated_at_ms: started_millis(member(&row, "started_at")),
                ..NativeListing::default()
            });
        }
        Ok(listed)
    }
}

fn sessions(answered: &str) -> Option<Vec<Box<RawValue>>> {
    let page = members(answered).ok()?;
    let (_, rows) = page.iter().rev().find(|(key, _)| {
        key.chars()
            .map(|c| match c {
                'ſ' => 's',
                c => c.to_ascii_lowercase(),
            })
            .eq("sessions".chars())
    })?;
    serde_json::from_str(rows.get()).ok()
}

fn member<'a>(row: &'a Members, name: &str) -> Option<&'a RawValue> {
    row.iter()
        .rev()
        .find(|(key, _)| key == name)
        .map(|(_, value)| value.as_ref())
}

fn text(row: &Members, name: &str) -> String {
    member(row, name)
        .and_then(|raw| serde_json::from_str::<Option<String>>(raw.get()).ok())
        .flatten()
        .unwrap_or_default()
}

fn started_millis(raw: Option<&RawValue>) -> i64 {
    let text = raw.map_or("", |raw| raw.get());
    if let Ok(seconds) = text.parse::<i64>() {
        return seconds.saturating_mul(1000);
    }
    match text.parse::<f64>() {
        Ok(seconds) if seconds.abs() < 1e15 => (seconds * 1000.0) as i64,
        _ => 0,
    }
}
