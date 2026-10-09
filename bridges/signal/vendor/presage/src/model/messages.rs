use libsignal_service::{prelude::Content, protocol::ServiceId};

#[derive(Debug)]
pub enum Received {
    /// when the receive loop is empty, happens when opening the websocket for the first time
    /// once you're done synchronizing all pending messages for this registered client.
    QueueEmpty,

    /// Got contacts (only applies if linked to a primary device
    /// Contacts can be later queried in the store.
    Contacts,

    /// The specified service ID could not decrypt a message we sent (they sent a
    /// `DecryptionErrorMessage`).
    DecryptionError(ServiceId),

    /// A message from a known sender could not be decrypted. When allowed, a retry request was
    /// sent so that the sender's client sends it again, with the same timestamp.
    DecryptionFailed {
        sender: ServiceId,
        device: u32,
        /// The message's timestamp.
        timestamp: u64,
        group_id: Option<Vec<u8>>,
        /// What the sender said may be done when it cannot be read.
        content_hint: ContentHint,
        retry_requested: bool,
    },

    /// Incoming decrypted message with metadata and content
    Content(Box<Content>),
}

/// What a sealed sender message says may be done when it cannot be read (identified ones are
/// `Default`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ContentHint {
    /// Shown as an error.
    Default,
    /// The sender will send it again: a placeholder until then.
    Resendable,
    /// Nothing to show (typing, receipts...).
    Implicit,
}
