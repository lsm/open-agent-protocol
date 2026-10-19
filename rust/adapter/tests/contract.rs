use futures::executor::block_on;
use futures::StreamExt;
use oap_adapter::Error;
use oap_adapter::*;
use oap_protocol::*;

struct Echo;

struct EchoSession;

impl Adapter for Echo {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        Box::pin(async {
            Ok(Descriptor {
                capability_revision: "echo-v1".into(),
                ..Descriptor::default()
            })
        })
    }

    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        Box::pin(async move {
            if request.participant.id.as_str().is_empty() {
                return Err(Error::InvalidParticipant);
            }
            Ok(Box::new(EchoSession) as Box<dyn Session>)
        })
    }
}

impl Session for EchoSession {
    fn submit(
        &self,
        request: SubmitRequest,
    ) -> BoxFuture<'_, (MessageSubmitResponse, EventStream)> {
        Box::pin(async move {
            let response = MessageSubmitResponse {
                session_id: request.request.session_id.clone(),
                accepted: true,
                run_id: Some(RunId::from("r1")),
                ..MessageSubmitResponse::default()
            };
            let started = Envelope::new(EnvelopeType::RUN_STARTED, "e1", &serde_json::json!({}))
                .map_err(|_| Error::InvalidSubmission)?;
            let events: EventStream = Box::pin(futures::stream::iter([
                Ok(started),
                Err(Error::EventStreamOverflow),
            ]));
            Ok((response, events))
        })
    }

    fn state(&self) -> BoxFuture<'_, SessionState> {
        Box::pin(async { Ok(SessionState::default()) })
    }

    fn resolve(&self, _: InteractionResolution) -> BoxFuture<'_, ()> {
        Box::pin(async { Err(Error::InteractionNotFound) })
    }

    fn cancel(&self, _: RunId) -> BoxFuture<'_, RunCancelResponse> {
        Box::pin(async { Err(Error::RunNotFound) })
    }

    fn resume(&self, _: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)> {
        Box::pin(async { Err(Error::ReplayCursorFuture) })
    }

    fn close(&self) -> BoxFuture<'_, ()> {
        Box::pin(async { Ok(()) })
    }
}

#[test]
fn an_adapter_behind_dyn_opens_a_session_whose_submission_streams_its_events() {
    let adapter: Box<dyn Adapter> = Box::new(Echo);
    block_on(async {
        assert_eq!(
            adapter.probe().await.unwrap().capability_revision,
            "echo-v1"
        );
        assert_eq!(
            adapter.open(OpenRequest::default()).await.err(),
            Some(Error::InvalidParticipant)
        );
        let request = OpenRequest {
            participant: Participant {
                id: "control".into(),
                ..Participant::default()
            },
            ..OpenRequest::default()
        };
        let session = adapter.open(request).await.unwrap();
        let submit = SubmitRequest {
            request: MessageSubmitRequest {
                session_id: "s1".into(),
                ..MessageSubmitRequest::default()
            },
            ..SubmitRequest::default()
        };
        let (response, events) = session.submit(submit).await.unwrap();
        assert_eq!(response.run_id.as_ref().map(RunId::as_str), Some("r1"));
        let results: Vec<_> = events.collect().await;
        assert_eq!(
            results[0].as_ref().unwrap().envelope_type,
            EnvelopeType::RUN_STARTED
        );
        assert_eq!(results[1].as_ref().err(), Some(&Error::EventStreamOverflow));
        assert!(session.close().await.is_ok());
    });
}

#[test]
fn an_adapter_offers_no_optional_ability_it_does_not_implement() {
    let adapter = Echo;
    assert!(adapter.admitter().is_none());
    assert!(adapter.native_lister().is_none());
    assert!(adapter.transcript_loader().is_none());
    let session = EchoSession;
    assert!(session.model_lister().is_none());
    assert!(session.compactor().is_none());
    assert!(session.call_resolver().is_none());
    assert!(session.native_session_id().is_none());
}
