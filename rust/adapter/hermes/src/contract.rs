use oap_adapter::{
    Adapter as AdapterContract, BoxFuture, Descriptor, Error, EventStream, InteractionResolution,
    NativeListRequest, NativeLister, NativeListing, OpenRequest, Recovery, ResumeRequest,
    Session as SessionContract, SettingsUpdater, SubmitRequest,
};
use oap_protocol::{
    MessageSubmitResponse, RunCancelResponse, RunId, SessionSettingsUpdateRequest,
    SessionSettingsUpdateResponse, SessionState,
};

use crate::adapter::Adapter;
use crate::session::Session;

fn background<T: Send + 'static>(
    work: impl FnOnce() -> Result<T, Error> + Send + 'static,
) -> BoxFuture<'static, T> {
    Box::pin(async move {
        let (reply, replied) = futures_channel::oneshot::channel();
        std::thread::spawn(move || {
            let _ = reply.send(work());
        });
        replied.await.unwrap_or(Err(Error::SessionClosed))
    })
}

impl AdapterContract for Adapter {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        let descriptor = self.describe();
        Box::pin(async move { Ok(descriptor) })
    }

    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn SessionContract>> {
        let adapter = self.clone();
        background(move || {
            Adapter::open(&adapter, request)
                .map(|session| Box::new(session) as Box<dyn SessionContract>)
        })
    }

    fn native_lister(&self) -> Option<&dyn NativeLister> {
        Some(self)
    }
}

impl NativeLister for Adapter {
    fn native_list(&self, request: NativeListRequest) -> BoxFuture<'_, Vec<NativeListing>> {
        let adapter = self.clone();
        background(move || Adapter::native_list(&adapter, request))
    }
}

impl SessionContract for Session {
    fn submit(
        &self,
        request: SubmitRequest,
    ) -> BoxFuture<'_, (MessageSubmitResponse, EventStream)> {
        let shared = self.shared.clone();
        background(move || shared.submit(request.request))
    }

    fn state(&self) -> BoxFuture<'_, SessionState> {
        let state = Session::state(self);
        Box::pin(async move { state })
    }

    fn resolve(&self, resolution: InteractionResolution) -> BoxFuture<'_, ()> {
        let shared = self.shared.clone();
        background(move || shared.resolve(resolution))
    }

    fn cancel(&self, run_id: RunId) -> BoxFuture<'_, RunCancelResponse> {
        let shared = self.shared.clone();
        background(move || shared.cancel(&run_id))
    }

    fn resume(&self, request: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)> {
        let resumed = Session::resume(self, request);
        Box::pin(async move { resumed })
    }

    fn close(&self) -> BoxFuture<'_, ()> {
        let shared = self.shared.clone();
        background(move || shared.close())
    }

    fn native_session_id(&self) -> Option<String> {
        Some(Session::native_session_id(self))
    }

    fn settings_updater(&self) -> Option<&dyn SettingsUpdater> {
        Some(self)
    }
}

impl SettingsUpdater for Session {
    fn update_settings(
        &self,
        request: SessionSettingsUpdateRequest,
    ) -> BoxFuture<'_, (SessionSettingsUpdateResponse, SessionState)> {
        let shared = self.shared.clone();
        background(move || shared.update_settings(request))
    }
}
