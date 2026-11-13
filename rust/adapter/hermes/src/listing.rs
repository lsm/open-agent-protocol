use oap_adapter::{Error, NativeListRequest, NativeListing};

use crate::adapter::Adapter;

impl Adapter {
    pub fn native_list(&self, _request: NativeListRequest) -> Result<Vec<NativeListing>, Error> {
        Ok(Vec::new())
    }
}
