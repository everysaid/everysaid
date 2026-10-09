use futures::{
    channel::{
        mpsc::{self, Sender},
        oneshot,
    },
    prelude::*,
};
use prost::Message;

pub use crate::{
    configuration::ServiceCredentials,
    proto::{
        web_socket_message, Envelope, WebSocketMessage,
        WebSocketRequestMessage, WebSocketResponseMessage,
    },
};

use crate::{
    push_service::ServiceError,
    websocket::{self, SignalWebSocket},
};

#[derive(Debug)]
#[allow(clippy::large_enum_variant)]
pub enum Incoming {
    Envelope(Envelope),
    QueueEmpty,
}

/// The server's acknowledgement of a request from the message pipe, sent with [`Ack::send`].
/// Dropped unsent, the server delivers the envelope again on the next connection.
#[derive(Debug)]
pub struct Ack(
    Option<(
        oneshot::Sender<WebSocketResponseMessage>,
        WebSocketResponseMessage,
    )>,
);

impl Ack {
    pub fn send(mut self) {
        if let Some((responder, response)) = self.0.take() {
            if responder.send(response).is_err() {
                tracing::debug!(
                    "connection closed before the acknowledgement; the server will deliver again"
                );
            }
        }
    }
}

pub struct MessagePipe {
    ws: SignalWebSocket<websocket::Identified>,
}

impl MessagePipe {
    pub fn from_socket(ws: SignalWebSocket<websocket::Identified>) -> Self {
        MessagePipe { ws }
    }

    /// Return a SignalWebSocket for sending messages and other purposes beyond receiving messages.
    pub fn ws(&self) -> SignalWebSocket<websocket::Identified> {
        self.ws.clone()
    }

    /// Worker task that processes the websocket into Envelopes
    async fn run(
        self,
        mut sink: Sender<Result<(Incoming, Ack), ServiceError>>,
    ) -> Result<(), mpsc::SendError> {
        let mut ws = self.ws.clone();
        let mut stream = ws
            .take_request_stream()
            .expect("web socket request handler not in use");

        while let Some((request, responder)) = stream.next().await {
            // WebsocketConnection::onMessage(ByteString)
            if let Some(env) = Self::process_request(request, responder).transpose()
            {
                sink.send(env).await?;
            } else {
                tracing::trace!("got empty message in websocket");
            }
        }

        ws.return_request_stream(stream);

        Ok(())
    }

    /// Decodes a request. What it carries is acknowledged only when its [`Ack`] is sent, once it
    /// has been handled (Signal Desktop answers only after storing what it decrypted): an envelope
    /// acknowledged and then lost with the process is gone from the server. One that cannot be
    /// decoded is acknowledged at once, or it would come again on every connection.
    fn process_request(
        request: WebSocketRequestMessage,
        responder: oneshot::Sender<WebSocketResponseMessage>,
    ) -> Result<Option<(Incoming, Ack)>, ServiceError> {
        // Java: MessagePipe::read
        let ack = Ack(Some((responder, WebSocketResponseMessage::from_request(&request))));

        // XXX Change the signature of this method to yield an enum of Envelope and EndOfQueue
        let result = if request.is_signal_service_envelope() {
            let decoded = match request.body.as_ref() {
                Some(body) => Envelope::decode(body.as_slice())
                    .map_err(ServiceError::from),
                None => Err(ServiceError::InvalidFrame {
                    reason: "request without body.",
                }),
            };
            match decoded {
                Ok(envelope) => Some(Incoming::Envelope(envelope)),
                Err(error) => {
                    ack.send();
                    return Err(error);
                },
            }
        } else if request.is_queue_empty() {
            Some(Incoming::QueueEmpty)
        } else {
            None
        };

        match result {
            Some(incoming) => Ok(Some((incoming, ack))),
            None => {
                ack.send();
                Ok(None)
            },
        }
    }

    /// Returns the stream of `Envelope`s
    ///
    /// Each comes with its [`Ack`], to be sent once it has been handled; one never sent comes
    /// again on the next connection.
    pub fn stream(
        self,
    ) -> impl Stream<Item = Result<(Incoming, Ack), ServiceError>> {
        let (sink, stream) = mpsc::channel(1);

        let stream = stream.map(Some);
        let runner = self.run(sink).map(|e| {
            tracing::info!("sink was closed: {:?}", e);
            None
        });

        let combined = futures::stream::select(stream, runner.into_stream());
        combined.filter_map(|x| async { x })
    }
}
