use crossbeam_channel::select;

use crate::rpc::Inbound;
use crate::session::Shared;

impl Shared {
    pub(crate) fn dispatch(&self) {
        let (inbound, stop) = (self.client.inbound(), &self.stopped);
        loop {
            select! {
                recv(inbound) -> message => match message {
                    Ok(Inbound::Barrier(ack)) => drop(ack),
                    Ok(_) => {}
                    Err(_) => return,
                },
                recv(stop) -> _ => return,
            }
        }
    }
}
