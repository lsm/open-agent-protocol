use oap_adapter::{NativeReadRequest, NativeTurn, TranscriptRequest};
use oap_protocol::{SessionId, TranscriptLoadResponse};

use crate::{Hub, HubError};

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct NativeRef {
    pub adapter: String,
    pub native_id: String,
    pub directory: String,
}

impl Hub {
    pub fn held_native(&self, id: &SessionId) -> Option<NativeRef> {
        let entry = self.session(id).ok()?;
        let record = entry.record();
        Some(NativeRef {
            adapter: entry.adapter().to_owned(),
            native_id: record.native_session_id.clone(),
            directory: record.directory.clone(),
        })
    }

    pub fn recorded_native(&self, id: &SessionId) -> Result<Option<NativeRef>, HubError> {
        let Some(bindings) = self.bindings() else {
            return Ok(None);
        };
        let latest = oap_binding::sessions(&bindings.sessions()?);
        Ok(latest
            .into_iter()
            .find(|entry| entry.record.session_id == id.as_str())
            .map(|entry| NativeRef {
                adapter: entry.record.adapter,
                native_id: entry.record.native_session_id,
                directory: entry.record.directory,
            }))
    }

    pub async fn native_transcript(
        &self,
        native: &NativeRef,
        max_turns: usize,
    ) -> Option<Result<Vec<NativeTurn>, HubError>> {
        if native.native_id.is_empty() {
            return None;
        }
        let implementation = self.registry().lookup(&native.adapter)?;
        let reader = implementation.native_reader()?;
        let directory = if native.directory.is_empty() {
            self.registry()
                .working_directory(&native.adapter)
                .to_owned()
        } else {
            native.directory.clone()
        };
        Some(
            reader
                .native_read(NativeReadRequest {
                    native_id: native.native_id.clone(),
                    directory,
                    max_turns,
                })
                .await
                .map_err(HubError::from),
        )
    }

    pub async fn transcript_page(
        &self,
        native: &NativeRef,
        cursor: &str,
        limit: usize,
    ) -> Result<Option<TranscriptLoadResponse>, HubError> {
        if native.native_id.is_empty() {
            return Ok(None);
        }
        let Some(implementation) = self.registry().lookup(&native.adapter) else {
            return Ok(None);
        };
        let Some(loader) = implementation.transcript_loader() else {
            return Ok(None);
        };
        let directory = if native.directory.is_empty() {
            self.registry()
                .working_directory(&native.adapter)
                .to_owned()
        } else {
            native.directory.clone()
        };
        let page = loader
            .transcript_load(TranscriptRequest {
                native_id: native.native_id.clone(),
                directory,
                cursor: cursor.to_owned(),
                limit,
            })
            .await?;
        Ok(Some(page))
    }
}
