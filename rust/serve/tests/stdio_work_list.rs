mod support;

use std::sync::Arc;

use futures::executor::block_on;
use oap_adapter::{
    Adapter, BoxFuture, Descriptor, NativeListRequest, NativeLister, NativeListing,
    NativeSearchRequest, NativeSearcher, OpenRequest, Session,
};
use oap_binding::MemoryStore;
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, HubOptions, Registry};
use serde_json::{json, Value};

struct Listing(Arc<dyn Adapter>);

fn native(id: &str, at: i64) -> NativeListing {
    NativeListing {
        native_id: id.into(),
        title: format!("title {id}"),
        directory: "/work".into(),
        updated_at_ms: at,
        ..NativeListing::default()
    }
}

impl Adapter for Listing {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        self.0.probe()
    }
    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        self.0.open(request)
    }
    fn native_lister(&self) -> Option<&dyn NativeLister> {
        Some(self)
    }
    fn native_searcher(&self) -> Option<&dyn NativeSearcher> {
        Some(self)
    }
}

impl NativeLister for Listing {
    fn native_list(&self, _: NativeListRequest) -> BoxFuture<'_, Vec<NativeListing>> {
        Box::pin(async { Ok(vec![native("n1", 30), native("n2", 20)]) })
    }
}

impl NativeSearcher for Listing {
    fn native_search(&self, request: NativeSearchRequest) -> BoxFuture<'_, Vec<NativeListing>> {
        Box::pin(async move {
            Ok(vec![native("n1", 30)]
                .into_iter()
                .filter(|listing| listing.title.contains(&request.term))
                .collect())
        })
    }
}

fn hub_with(registry: Registry) -> Hub {
    Hub::with_options(
        registry,
        HubOptions {
            bindings: Some(Arc::new(MemoryStore::default())),
            ..HubOptions::default()
        },
    )
}

fn open(hub: &Hub, adapter: &str, id: &str) {
    block_on(hub.open(
        adapter,
        OpenRequest {
            session_id: id.into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
}

fn list(hub: &Hub, request: &str) -> Value {
    let line = format!("{{\"id\":1,\"op\":\"work.list\",\"request\":{request}}}\n");
    let output = support::Captured::default();
    serve(
        hub,
        std::io::Cursor::new(line.into_bytes()),
        output.clone(),
        &StdioOptions::default(),
    )
    .unwrap();
    serde_json::from_slice(&output.bytes()).unwrap()
}

fn ids(answer: &Value) -> Vec<String> {
    answer["result"]["groups"]
        .as_array()
        .unwrap()
        .iter()
        .flat_map(|group| group["work"].as_array().unwrap().clone())
        .map(|work| {
            let reference = &work["ref"];
            reference["session_id"]
                .as_str()
                .or(reference["native_id"].as_str())
                .unwrap()
                .to_owned()
        })
        .collect()
}

#[test]
fn an_empty_hub_lists_no_groups_and_a_directory_filter_names_its_empty_group() {
    let hub = Hub::new(Registry::default_registry());
    assert_eq!(list(&hub, "null")["result"], json!({"groups": []}));
    assert_eq!(
        list(
            &hub,
            r#"{"directory":"/nowhere","adapters":["memory"],"limit":5}"#
        )["result"],
        json!({"groups": [{"directory": "/nowhere", "last_activity_ms": 0, "work": []}]})
    );
}

#[test]
fn work_list_refuses_a_malformed_request_a_limit_out_of_range_and_a_foreign_cursor() {
    let hub = Hub::new(Registry::default_registry());
    for (request, message) in [
        (r#"{"limit":0}"#, "limit is 1 to 100"),
        (r#"{"limit":101}"#, "limit is 1 to 100"),
        (r#"{"cursor":"nope"}"#, "cursor is not one this list issued"),
        (
            r#"{"limit":"ten"}"#,
            "request is an object of directory, adapters, include_closed, include_native, limit, cursor and search",
        ),
    ] {
        let answer = list(&hub, request);
        assert_eq!(answer["error"]["code"], "invalid_request", "{request}: {answer}");
        assert_eq!(answer["error"]["message"], message);
    }
}

#[test]
fn held_and_recorded_sessions_list_newest_first_and_page_by_cursor() {
    let hub = hub_with(Registry::default_registry());
    open(&hub, "memory", "a");
    open(&hub, "memory", "b");
    open(&hub, "memory", "gone");
    block_on(hub.session(&"gone".into()).unwrap().close()).unwrap();
    let held = list(&hub, "{}");
    let mut listed = ids(&held);
    listed.sort();
    assert_eq!(listed, ["a", "b"], "{held}");
    let all = list(&hub, r#"{"include_closed":true}"#);
    assert_eq!(ids(&all).len(), 3, "{all}");
    let first = list(&hub, r#"{"include_closed":true,"limit":2}"#);
    let cursor = first["result"]["next_cursor"].as_str().unwrap().to_owned();
    let rest = list(
        &hub,
        &format!(r#"{{"include_closed":true,"limit":2,"cursor":"{cursor}"}}"#),
    );
    let mut paged = ids(&first);
    paged.extend(ids(&rest));
    paged.sort();
    assert_eq!(paged, ["a", "b", "gone"]);
    assert!(rest["result"].get("next_cursor").is_none());
}

#[test]
fn include_native_adds_the_harness_sessions_and_search_finds_through_the_harness() {
    let memory = Registry::default_registry().lookup("memory").unwrap();
    let mut registry = Registry::new();
    registry
        .register("listing", Arc::new(Listing(memory)))
        .unwrap();
    let hub = hub_with(registry);
    let natives = list(&hub, r#"{"include_native":true}"#);
    assert_eq!(ids(&natives), ["n1", "n2"], "{natives}");
    let work = &natives["result"]["groups"][0]["work"][0];
    assert_eq!(work["held"], false);
    assert_eq!(work["native"], true);
    assert_eq!(work["state"], "idle");
    let found = list(&hub, r#"{"search":"n1"}"#);
    assert_eq!(ids(&found), ["n1"], "{found}");
}
