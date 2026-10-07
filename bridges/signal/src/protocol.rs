//! The protocol with Everysaid: JSON lines. Each request has an `id` and a `cmd`; each answer
//! carries the request's `id` with `ok` and a `result` or an `error` (and a `code`). Events, which
//! come without being asked (a link code, a message, the queue drained), have an `event` instead
//! of an `id`. One JSON object per line, UTF-8, on stdin and stdout; the helper's own log goes to
//! stderr. The README describes every command and event.

use serde::Deserialize;
use serde_json::{json, Value};

/// A request, as read from one line.
#[derive(Debug, PartialEq)]
pub struct Request {
    pub id: u64,
    pub cmd: Command,
}

#[derive(Debug, Deserialize, PartialEq)]
#[serde(tag = "cmd", rename_all = "snake_case")]
pub enum Command {
    /// Opens (making it if new) the store in a folder, encrypted with the passphrase where one is
    /// given; attachments are saved into `attachments`.
    Open {
        store: String,
        attachments: String,
        #[serde(default)]
        passphrase: Option<String>,
    },
    Status,
    /// Links as a secondary device: a `link_url` event (for a QR code), then the answer once the
    /// phone scanned it.
    Link {
        #[serde(default = "default_device_name")]
        device_name: String,
    },
    /// Asks the phone for its contacts (they come later, as a `contacts` event).
    Sync,
    Contacts,
    Groups,
    /// Starts receiving: events until the helper stops.
    Receive {
        #[serde(default = "yes")]
        download: bool,
    },
    Send(SendRequest),
    /// Marks the others' messages read: always on the account's other devices (the phone), and with
    /// read receipts to their authors where `receipts`.
    MarkRead {
        messages: Vec<MessageRef>,
        #[serde(default = "yes")]
        receipts: bool,
    },
    /// Fetches again the files of messages the store holds, where they failed before.
    Fetch {
        messages: Vec<MessageRef>,
    },
    /// What the store holds (what this device received since it was linked), sent after `since`.
    History {
        #[serde(default)]
        since: u64,
    },
    Quit,
}

fn default_device_name() -> String {
    "Everysaid".into()
}

fn yes() -> bool {
    true
}

/// A chat: a person's (`contact`, their ACI) or a group's (`group`, its id in base64).
#[derive(Debug, Deserialize, PartialEq, Clone)]
pub struct Chat {
    pub kind: String,
    pub id: String,
}

/// A message by its author and sent timestamp, the way Signal names one.
#[derive(Debug, Deserialize, PartialEq, Clone)]
pub struct MessageRef {
    pub author: String,
    pub ts: u64,
}

#[derive(Debug, Deserialize, PartialEq)]
pub struct SendRequest {
    pub chat: Chat,
    #[serde(default)]
    pub text: String,
    #[serde(default)]
    pub quote: Option<Quote>,
    /// Where the text names people: in UTF-16 units, each over an U+FFFC as Signal's apps write it.
    #[serde(default)]
    pub mentions: Vec<Mention>,
    #[serde(default)]
    pub attachments: Vec<OutAttachment>,
}

#[derive(Debug, Deserialize, PartialEq)]
pub struct Quote {
    pub ts: u64,
    pub author: String,
    #[serde(default)]
    pub text: Option<String>,
}

#[derive(Debug, Deserialize, PartialEq, Clone)]
pub struct Mention {
    pub start: u32,
    pub length: u32,
    pub aci: String,
}

#[derive(Debug, Deserialize, PartialEq)]
pub struct OutAttachment {
    pub path: String,
    #[serde(default)]
    pub content_type: Option<String>,
    #[serde(default)]
    pub filename: Option<String>,
    #[serde(default)]
    pub voice: bool,
}

/// A request from a line; an error says what is wrong with it (and the id, where it was there).
pub fn parse(line: &str) -> Result<Request, (Option<u64>, String)> {
    let value: Value = serde_json::from_str(line).map_err(|e| (None, format!("not JSON: {e}")))?;
    let id = value.get("id").and_then(Value::as_u64).ok_or((None, "no id".to_string()))?;
    let cmd = serde_json::from_value(value).map_err(|e| (Some(id), e.to_string()))?;
    Ok(Request { id, cmd })
}

pub fn ok(id: u64, result: Value) -> Value {
    json!({"id": id, "ok": true, "result": result})
}

/// A failure: `code` is a word Everysaid acts on (not_open, not_linked, bad_request, unknown_group,
/// failed), `error` what happened.
pub fn fail(id: Option<u64>, code: &str, error: &str) -> Value {
    json!({"id": id, "ok": false, "code": code, "error": error})
}

pub fn event(name: &str, mut fields: Value) -> Value {
    if let Value::Object(m) = &mut fields {
        m.insert("event".into(), Value::String(name.into()));
        return fields;
    }
    json!({"event": name})
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_commands() {
        let r = parse(r#"{"id":1,"cmd":"open","store":"/s","attachments":"/a","passphrase":"p"}"#).unwrap();
        assert_eq!(r.id, 1);
        assert_eq!(
            r.cmd,
            Command::Open { store: "/s".into(), attachments: "/a".into(), passphrase: Some("p".into()) }
        );
        let r = parse(r#"{"id":2,"cmd":"link"}"#).unwrap();
        assert_eq!(r.cmd, Command::Link { device_name: "Everysaid".into() });
        assert_eq!(parse(r#"{"id":3,"cmd":"receive"}"#).unwrap().cmd, Command::Receive { download: true });
        assert_eq!(parse(r#"{"id":4,"cmd":"history"}"#).unwrap().cmd, Command::History { since: 0 });
    }

    #[test]
    fn parses_send() {
        let r = parse(
            r#"{"id":7,"cmd":"send","chat":{"kind":"group","id":"abc="},"text":"hi \ufffc",
               "quote":{"ts":5,"author":"u"},"mentions":[{"start":3,"length":1,"aci":"m"}],
               "attachments":[{"path":"/f.jpg"}]}"#,
        )
        .unwrap();
        let Command::Send(s) = r.cmd else { panic!("not send") };
        assert_eq!(s.chat, Chat { kind: "group".into(), id: "abc=".into() });
        assert_eq!(s.text, "hi \u{fffc}");
        assert_eq!(s.quote, Some(Quote { ts: 5, author: "u".into(), text: None }));
        assert_eq!(s.mentions, vec![Mention { start: 3, length: 1, aci: "m".into() }]);
        assert_eq!(s.attachments[0].path, "/f.jpg");
        assert!(!s.attachments[0].voice);
    }

    #[test]
    fn mark_read() {
        let r = parse(r#"{"id":8,"cmd":"mark_read","messages":[{"author":"a","ts":9}]}"#).unwrap();
        assert_eq!(r.cmd, Command::MarkRead { messages: vec![MessageRef { author: "a".into(), ts: 9 }], receipts: true });
        let r = parse(r#"{"id":9,"cmd":"mark_read","messages":[],"receipts":false}"#).unwrap();
        assert_eq!(r.cmd, Command::MarkRead { messages: vec![], receipts: false });
        let r = parse(r#"{"id":10,"cmd":"fetch","messages":[{"author":"a","ts":9}]}"#).unwrap();
        assert_eq!(r.cmd, Command::Fetch { messages: vec![MessageRef { author: "a".into(), ts: 9 }] });
    }

    #[test]
    fn bad_requests() {
        assert_eq!(parse("nope").unwrap_err().0, None);
        assert_eq!(parse(r#"{"cmd":"status"}"#).unwrap_err().0, None);
        assert_eq!(parse(r#"{"id":5,"cmd":"fly"}"#).unwrap_err().0, Some(5));
        assert_eq!(parse(r#"{"id":6,"cmd":"open"}"#).unwrap_err().0, Some(6));
    }

    #[test]
    fn answers() {
        assert_eq!(ok(1, json!({"a": 1})), json!({"id": 1, "ok": true, "result": {"a": 1}}));
        assert_eq!(
            fail(Some(2), "not_linked", "x"),
            json!({"id": 2, "ok": false, "code": "not_linked", "error": "x"})
        );
        assert_eq!(event("queue_empty", json!({})), json!({"event": "queue_empty"}));
        assert_eq!(event("x", json!({"a": 1})), json!({"event": "x", "a": 1}));
    }
}
