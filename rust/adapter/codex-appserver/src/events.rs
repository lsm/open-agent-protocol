use crossbeam_channel::select;

use crate::rpc::Inbound;
use crate::session::{hold, Shared};

impl Shared {
    pub(crate) fn dispatch(&self) {
        let (inbound, done, stop) = (self.client.inbound(), self.client.done(), &self.stopped);
        loop {
            select! {
                recv(inbound) -> message => match message {
                    Ok(message) => self.reduce(message),
                    Err(_) => return,
                },
                recv(done) -> _ => return,
                recv(stop) -> _ => return,
            }
        }
    }

    fn reduce(&self, message: Inbound) {
        let _operating = hold(&self.operating);
        match message {
            Inbound::Barrier(ack) => drop(ack),
            Inbound::Request(request) => {
                let data =
                    serde_json::value::to_raw_value(&serde_json::json!({"method": request.method}))
                        .ok();
                let _ =
                    request.respond_error_with(-32601, "unsupported Codex server request", data);
            }
            Inbound::Notification(_) => {}
        }
    }
}
