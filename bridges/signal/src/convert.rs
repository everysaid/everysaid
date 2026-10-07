//! What Signal sends, as the protocol's events: plain JSON that Everysaid reads without knowing
//! Signal's protobufs. Nothing here touches the network or the store, so it is tested on messages
//! made by hand.

use base64::prelude::*;
use presage::libsignal_service::content::ContentBody;
use presage::libsignal_service::prelude::{GroupMasterKey, GroupSecretParams, Uuid};
use presage::libsignal_service::protocol::ServiceId;
use presage::proto::{
    body_range, call_message, data_message, receipt_message, sync_message, AttachmentPointer,
    BodyRange, CallMessage, DataMessage, EditMessage, GroupContextV2, ReceiptMessage, SyncMessage,
};
use serde_json::{json, Map, Value};

use crate::protocol::event;

/// Who sent a message (to whom), from which device, and when (Unix ms).
pub struct Meta {
    pub sender: String,
    pub destination: String,
    pub sender_device: u32,
    pub ts: u64,
    pub server_ts: u64,
}

/// An event, with the attachments still to fetch (in the order of its `attachments` list) and the
/// group it is in.
pub struct Converted {
    pub event: Value,
    pub attachments: Vec<AttachmentPointer>,
    pub group: Option<[u8; 32]>,
}

impl Converted {
    fn plain(event: Value) -> Self {
        Converted { event, attachments: vec![], group: None }
    }
}

/// A group's id as Signal's apps show it (base64 of the identifier derived from its master key);
/// the master key itself, which lets one read the group, never leaves the helper.
pub fn group_id(master_key: &[u8; 32]) -> String {
    let params = GroupSecretParams::derive_from_master_key(GroupMasterKey::new(*master_key));
    BASE64_STANDARD.encode(params.get_group_identifier())
}

fn master_key(group: &Option<GroupContextV2>) -> Option<[u8; 32]> {
    group.as_ref()?.master_key.as_ref()?.as_slice().try_into().ok()
}

/// An ACI written as text, from the string or the 16 bytes a message carries.
fn aci(text: Option<&String>, binary: Option<&Vec<u8>>) -> Option<String> {
    if let Some(b) = binary {
        if let Ok(u) = Uuid::from_slice(b) {
            return Some(u.to_string());
        }
    }
    text.map(|t| t.to_lowercase())
}

/// A service id (an ACI, or "PNI:<uuid>") written as text.
fn service_id(text: Option<&String>, binary: Option<&Vec<u8>>) -> Option<String> {
    if let Some(b) = binary {
        if let Some(s) = ServiceId::parse_from_service_id_binary(b) {
            return Some(s.service_id_string());
        }
    }
    let t = text?;
    Some(ServiceId::parse_from_service_id_string(t).map(|s| s.service_id_string()).unwrap_or_else(|| t.clone()))
}

fn contact_chat(id: &str) -> Value {
    json!({"kind": "contact", "id": id})
}

fn group_chat(key: &[u8; 32]) -> Value {
    json!({"kind": "group", "id": group_id(key)})
}

/// The events of one received content. `own` is the account's ACI.
pub fn convert(meta: &Meta, body: &ContentBody, own: &str) -> Vec<Converted> {
    // a person's chat is the other person's: the sender's, or (what this device sent, as the store
    // keeps it) the destination's
    let peer = if meta.sender == own && !meta.destination.is_empty() { &meta.destination } else { &meta.sender };
    match body {
        ContentBody::DataMessage(dm) => {
            let (chat, group) = match master_key(&dm.group_v2) {
                Some(k) => (group_chat(&k), Some(k)),
                None => (contact_chat(peer), None),
            };
            let ts = dm.timestamp.unwrap_or(meta.ts);
            data_events(meta, dm, &meta.sender, meta.sender == own, chat, group, ts)
        }
        ContentBody::EditMessage(EditMessage { target_sent_timestamp: Some(target), data_message: Some(dm) }) => {
            let (chat, group) = match master_key(&dm.group_v2) {
                Some(k) => (group_chat(&k), Some(k)),
                None => (contact_chat(peer), None),
            };
            edit_event(meta, dm, &meta.sender, meta.sender == own, chat, group, *target)
        }
        ContentBody::SynchronizeMessage(sm) => sync_events(meta, sm, own),
        ContentBody::CallMessage(cm) => call_events(meta, cm, own),
        ContentBody::ReceiptMessage(rm) => receipt_event(meta, rm),
        // typing, stories, null messages (padding, or a session reset) and failures: nothing to keep
        _ => vec![],
    }
}

fn base(meta: &Meta, sender: &str, outgoing: bool, chat: Value, ts: u64) -> Map<String, Value> {
    let mut m = Map::new();
    m.insert("chat".into(), chat);
    m.insert("sender".into(), json!(sender));
    m.insert("sender_device".into(), json!(meta.sender_device));
    m.insert("outgoing".into(), json!(outgoing));
    m.insert("ts".into(), json!(ts));
    m.insert("server_ts".into(), json!(meta.server_ts));
    m
}

fn data_events(
    meta: &Meta,
    dm: &DataMessage,
    sender: &str,
    outgoing: bool,
    chat: Value,
    group: Option<[u8; 32]>,
    ts: u64,
) -> Vec<Converted> {
    let mut m = base(meta, sender, outgoing, chat, ts);
    if let Some(r) = &dm.reaction {
        m.insert("emoji".into(), json!(r.emoji));
        m.insert("remove".into(), json!(r.remove.unwrap_or(false)));
        m.insert("target_author".into(), json!(aci(r.target_author_aci.as_ref(), r.target_author_aci_binary.as_ref())));
        m.insert("target_ts".into(), json!(r.target_sent_timestamp));
        return vec![Converted { event: event("reaction", Value::Object(m)), attachments: vec![], group }];
    }
    if let Some(data_message::Delete { target_sent_timestamp: Some(target) }) = &dm.delete {
        m.insert("target_author".into(), json!(sender));
        m.insert("target_ts".into(), json!(target));
        return vec![Converted { event: event("delete", Value::Object(m)), attachments: vec![], group }];
    }
    if let Some(d) = &dm.admin_delete {
        m.insert("target_author".into(), json!(aci(None, d.target_author_aci_binary.as_ref())));
        m.insert("target_ts".into(), json!(d.target_sent_timestamp));
        m.insert("admin".into(), json!(true)); // honoured only from a group's admin (main.rs)
        return vec![Converted { event: event("delete", Value::Object(m)), attachments: vec![], group }];
    }
    let attachments = message_fields(&mut m, dm);
    if !has_content(&m) {
        return vec![]; // a profile key or a poll vote: nothing to show
    }
    vec![Converted { event: event("message", Value::Object(m)), attachments, group }]
}

fn edit_event(
    meta: &Meta,
    dm: &DataMessage,
    sender: &str,
    outgoing: bool,
    chat: Value,
    group: Option<[u8; 32]>,
    target: u64,
) -> Vec<Converted> {
    let ts = dm.timestamp.unwrap_or(meta.ts);
    let mut m = base(meta, sender, outgoing, chat, ts);
    m.insert("target_ts".into(), json!(target));
    let attachments = message_fields(&mut m, dm);
    vec![Converted { event: event("edit", Value::Object(m)), attachments, group }]
}

/// Whether a message event carries anything beyond who and when.
fn has_content(m: &Map<String, Value>) -> bool {
    const FIELDS: [&str; 12] = [
        "text", "attachments", "contacts", "poll", "group_change", "group_call", "expire_timer_update",
        "pin", "unpin", "payment", "gift", "quote",
    ];
    FIELDS.iter().any(|f| match m.get(*f) {
        None | Some(Value::Null) | Some(Value::Bool(false)) => false,
        Some(Value::Array(a)) => !a.is_empty(),
        Some(Value::String(s)) => !s.is_empty(),
        _ => true,
    })
}

fn mentions(ranges: &[BodyRange]) -> Vec<Value> {
    ranges
        .iter()
        .filter_map(|r| {
            let who = match &r.associated_value {
                Some(body_range::AssociatedValue::MentionAci(s)) => aci(Some(s), None),
                Some(body_range::AssociatedValue::MentionAciBinary(b)) => aci(None, Some(b)),
                _ => None,
            }?;
            Some(json!({"start": r.start.unwrap_or(0), "length": r.length.unwrap_or(0), "aci": who}))
        })
        .collect()
}

/// An attachment as the protocol has it (the file is filled in once fetched).
pub fn attachment_json(a: &AttachmentPointer, sticker: bool) -> Value {
    let flags = a.flags.unwrap_or(0);
    json!({
        "content_type": a.content_type, "filename": a.file_name, "size": a.size,
        "width": a.width, "height": a.height, "caption": a.caption,
        "voice": flags & 1 != 0, "borderless": flags & 2 != 0, "gif": flags & 8 != 0,
        "sticker": sticker, "file": null,
    })
}

/// The fields of a message (text, mentions, quote, attachments, ...) into m; the attachments to fetch.
fn message_fields(m: &mut Map<String, Value>, dm: &DataMessage) -> Vec<AttachmentPointer> {
    if let Some(body) = &dm.body {
        m.insert("text".into(), json!(body));
    }
    let ms = mentions(&dm.body_ranges);
    if !ms.is_empty() {
        m.insert("mentions".into(), Value::Array(ms));
    }
    if let Some(q) = &dm.quote {
        m.insert(
            "quote".into(),
            json!({"ts": q.id, "author": aci(q.author_aci.as_ref(), q.author_aci_binary.as_ref()), "text": q.text}),
        );
    }
    let mut pointers: Vec<AttachmentPointer> = dm.attachments.clone();
    let mut list: Vec<Value> = dm.attachments.iter().map(|a| attachment_json(a, false)).collect();
    if let Some(s) = &dm.sticker {
        if let Some(data) = &s.data {
            let mut a = attachment_json(data, true);
            a["emoji"] = json!(s.emoji);
            list.push(a);
            pointers.push(data.clone());
        }
    }
    if !list.is_empty() {
        m.insert("attachments".into(), Value::Array(list));
    }
    if !dm.contact.is_empty() {
        let shared: Vec<Value> = dm
            .contact
            .iter()
            .map(|c| {
                let name = c.name.as_ref().map(|n| {
                    [&n.prefix, &n.given_name, &n.middle_name, &n.family_name, &n.suffix]
                        .iter()
                        .filter_map(|p| p.as_deref())
                        .filter(|p| !p.is_empty())
                        .collect::<Vec<_>>()
                        .join(" ")
                });
                json!({
                    "name": name.filter(|n| !n.is_empty()).or_else(|| c.name.as_ref().and_then(|n| n.nickname.clone())),
                    "phones": c.number.iter().filter_map(|p| p.value.clone()).collect::<Vec<_>>(),
                    "emails": c.email.iter().filter_map(|e| e.value.clone()).collect::<Vec<_>>(),
                    "organization": c.organization,
                })
            })
            .collect();
        m.insert("contacts".into(), Value::Array(shared));
    }
    if !dm.preview.is_empty() {
        let previews: Vec<Value> = dm.preview.iter().map(|p| json!({"url": p.url, "title": p.title})).collect();
        m.insert("previews".into(), Value::Array(previews));
    }
    if let Some(p) = &dm.poll_create {
        m.insert(
            "poll".into(),
            json!({"question": p.question, "options": p.options, "multiple": p.allow_multiple.unwrap_or(false)}),
        );
    }
    if dm.group_v2.as_ref().is_some_and(|g| g.group_change.is_some()) {
        m.insert("group_change".into(), json!(true));
    }
    if let Some(g) = &dm.group_v2 {
        if let Some(r) = g.revision {
            m.insert("group_revision".into(), json!(r));
        }
    }
    if dm.group_call_update.is_some() {
        m.insert("group_call".into(), json!(true));
    }
    let flags = dm.flags.unwrap_or(0);
    if flags & data_message::Flags::ExpirationTimerUpdate as u32 != 0 {
        m.insert("expire_timer_update".into(), json!(true));
    }
    if let Some(t) = dm.expire_timer.filter(|t| *t > 0) {
        m.insert("expire_timer".into(), json!(t));
    }
    if flags & data_message::Flags::Forward as u32 != 0 {
        m.insert("forwarded".into(), json!(true));
    }
    if dm.is_view_once == Some(true) {
        m.insert("view_once".into(), json!(true));
    }
    if let Some(p) = &dm.pin_message {
        m.insert(
            "pin".into(),
            json!({"target_author": aci(None, p.target_author_aci_binary.as_ref()), "target_ts": p.target_sent_timestamp}),
        );
    }
    if let Some(p) = &dm.unpin_message {
        m.insert(
            "unpin".into(),
            json!({"target_author": aci(None, p.target_author_aci_binary.as_ref()), "target_ts": p.target_sent_timestamp}),
        );
    }
    if dm.payment.is_some() {
        m.insert("payment".into(), json!(true));
    }
    if dm.gift_badge.is_some() {
        m.insert("gift".into(), json!(true));
    }
    if dm.story_context.is_some() {
        m.insert("story_reply".into(), json!(true));
    }
    pointers
}

fn sync_events(meta: &Meta, sm: &SyncMessage, own: &str) -> Vec<Converted> {
    let mut out = vec![];
    if let Some(sync_message::Content::Sent(sent)) = &sm.content {
        let dest = service_id(sent.destination_service_id.as_ref(), sent.destination_service_id_binary.as_ref());
        if let Some(dm) = &sent.message {
            let ts = dm.timestamp.or(sent.timestamp).unwrap_or(meta.ts);
            match (master_key(&dm.group_v2), dest) {
                (Some(k), _) => out.extend(data_events(meta, dm, own, true, group_chat(&k), Some(k), ts)),
                (None, Some(d)) => out.extend(data_events(meta, dm, own, true, contact_chat(&d), None, ts)),
                (None, None) => {}
            }
        }
        if let Some(EditMessage { target_sent_timestamp: Some(target), data_message: Some(dm) }) = &sent.edit_message {
            match (master_key(&dm.group_v2), service_id(sent.destination_service_id.as_ref(), sent.destination_service_id_binary.as_ref())) {
                (Some(k), _) => out.extend(edit_event(meta, dm, own, true, group_chat(&k), Some(k), *target)),
                (None, Some(d)) => out.extend(edit_event(meta, dm, own, true, contact_chat(&d), None, *target)),
                (None, None) => {}
            }
        }
    }
    if !sm.read.is_empty() {
        let items: Vec<Value> = sm
            .read
            .iter()
            .map(|r| json!({"author": aci(r.sender_aci.as_ref(), r.sender_aci_binary.as_ref()), "ts": r.timestamp}))
            .collect();
        out.push(Converted::plain(event("read", json!({"messages": items, "ts": meta.ts}))));
    }
    if let Some(sync_message::Content::CallEvent(c)) = &sm.content {
        use sync_message::call_event::{Direction, Event, Type};
        let kind = match c.r#type() {
            Type::AudioCall => "audio",
            Type::VideoCall => "video",
            Type::GroupCall => "group",
            Type::AdHocCall => "ad_hoc",
            Type::UnknownType => "unknown",
        };
        let chat = match (kind, c.conversation_id.as_deref()) {
            (_, None) => Value::Null,
            ("group", Some(id)) => json!({"kind": "group", "id": BASE64_STANDARD.encode(id)}),
            (_, Some(id)) => match ServiceId::parse_from_service_id_binary(id) {
                Some(s) => contact_chat(&s.service_id_string()),
                None => Value::Null,
            },
        };
        let result = match c.event() {
            Event::Accepted => "accepted",
            Event::NotAccepted => "not_accepted",
            Event::Delete => "delete",
            Event::Observed => "observed",
            Event::UnknownEvent => "unknown",
        };
        let direction = match c.direction() {
            Direction::Incoming => "incoming",
            Direction::Outgoing => "outgoing",
            Direction::UnknownDirection => "unknown",
        };
        out.push(Converted::plain(event(
            "call",
            json!({"source": "sync", "id": c.call_id.map(|i| i.to_string()), "ts": c.timestamp.unwrap_or(meta.ts),
                   "type": kind, "direction": direction, "result": result, "chat": chat}),
        )));
    }
    out
}

fn call_events(meta: &Meta, cm: &CallMessage, own: &str) -> Vec<Converted> {
    let mut out = vec![];
    let mut push = |id: Option<u64>, action: &str, extra: Value| {
        let mut m = base(meta, &meta.sender, meta.sender == own, contact_chat(&meta.sender), meta.ts);
        m.insert("source".into(), json!("message"));
        m.insert("id".into(), json!(id.map(|i| i.to_string())));
        m.insert("action".into(), json!(action));
        if let Value::Object(x) = extra {
            m.extend(x);
        }
        out.push(Converted::plain(event("call", Value::Object(m))));
    };
    if let Some(o) = &cm.offer {
        let video = o.r#type() == call_message::offer::Type::OfferVideoCall;
        push(o.id, "offer", json!({"video": video}));
    }
    if let Some(a) = &cm.answer {
        push(a.id, "answer", json!({}));
    }
    if let Some(b) = &cm.busy {
        push(b.id, "busy", json!({}));
    }
    if let Some(h) = &cm.hangup {
        use call_message::hangup::Type;
        let how = match h.r#type() {
            Type::HangupNormal => "normal",
            Type::HangupAccepted => "accepted",
            Type::HangupDeclined => "declined",
            Type::HangupBusy => "busy",
            Type::HangupNeedPermission => "need_permission",
        };
        push(h.id, "hangup", json!({"hangup": how}));
    }
    out
}

fn receipt_event(meta: &Meta, rm: &ReceiptMessage) -> Vec<Converted> {
    let kind = match rm.r#type() {
        receipt_message::Type::Delivery => "delivery",
        receipt_message::Type::Read => "read",
        receipt_message::Type::Viewed => "viewed",
    };
    vec![Converted::plain(event(
        "receipt",
        json!({"sender": meta.sender, "kind": kind, "timestamps": rm.timestamp, "ts": meta.ts}),
    ))]
}

/// A message this helper sent, as the event the others' messages are.
pub fn sent_event(own: &str, device: u32, chat: Value, dm: &DataMessage, ts: u64) -> Converted {
    let meta = Meta { sender: own.to_string(), destination: String::new(), sender_device: device, ts, server_ts: ts };
    let mut m = base(&meta, own, true, chat, ts);
    let attachments = message_fields(&mut m, dm);
    Converted { event: event("message", Value::Object(m)), attachments, group: None }
}

/// What this helper sent (a reaction, an edit, a deletion), as the events the others' are: to
/// `destination` (a person's ACI; for a group, its context in the message says where).
pub fn sent_events(own: &str, device: u32, destination: &str, body: &ContentBody, ts: u64) -> Vec<Converted> {
    let meta = Meta { sender: own.to_string(), destination: destination.to_string(), sender_device: device, ts, server_ts: ts };
    convert(&meta, body, own)
}

/// The message a stored content holds: what was sent or received, or its newest edit.
pub fn data_message_of(body: &ContentBody) -> Option<&DataMessage> {
    match body {
        ContentBody::DataMessage(dm) => Some(dm),
        ContentBody::EditMessage(EditMessage { data_message: Some(dm), .. }) => Some(dm),
        ContentBody::SynchronizeMessage(SyncMessage { content: Some(sync_message::Content::Sent(s)), .. }) => {
            s.message.as_ref().or_else(|| s.edit_message.as_ref().and_then(|e| e.data_message.as_ref()))
        }
        _ => None,
    }
}

/// An edit's message: an edit replaces the whole message, so the old one's files, quote and timer
/// go with it; its text is new (the mentions and link previews were of the old text).
pub fn edited(old: Option<&DataMessage>, text: &str, ts: u64) -> DataMessage {
    let mut dm = match old {
        Some(o) => DataMessage {
            attachments: o.attachments.clone(),
            quote: o.quote.clone(),
            expire_timer: o.expire_timer,
            expire_timer_version: o.expire_timer_version,
            ..Default::default()
        },
        None => DataMessage::default(),
    };
    dm.body = (!text.is_empty()).then(|| text.to_string());
    dm.timestamp = Some(ts);
    dm
}

/// Whether a group's admin's deletion is to be kept: of the sender's own message always; of another's
/// only when the sender is one of the group's admins (as the helper knows them; not known: no).
pub fn admin_delete_allowed(sender: &str, target_author: &str, admins: Option<&[String]>) -> bool {
    sender == target_author || admins.is_some_and(|a| a.iter().any(|x| x == sender))
}

/// A contact's names, (the address book's, their own). The book names only those whose number Signal
/// shows: presage names the others, and anyone it saw a message of, after their profile (dropping the
/// number), so a name without a number is the person's own.
pub fn contact_names(has_phone: bool, name: &str, profile: Option<String>) -> (Option<String>, Option<String>) {
    let name = Some(name.trim().to_string()).filter(|n| !n.is_empty());
    if has_phone {
        (name, profile)
    } else {
        (None, profile.or(name))
    }
}

/// The file name an attachment of a message is saved under (unique by author, time and place).
pub fn attachment_name(ts: u64, author: &str, index: usize, content_type: Option<&str>, filename: Option<&str>) -> String {
    let from_name = filename
        .and_then(|f| f.rsplit_once('.'))
        .map(|(_, e)| e.to_lowercase())
        .filter(|e| !e.is_empty() && e.len() <= 8 && e.chars().all(|c| c.is_ascii_alphanumeric()));
    let ext = from_name.or_else(|| {
        let ct = content_type?;
        let known = match ct {
            "image/jpeg" => Some("jpg"),
            "audio/aac" => Some("aac"),
            "audio/mp4" => Some("m4a"),
            "video/mp4" => Some("mp4"),
            "image/webp" => Some("webp"),
            "text/x-signal-plain" => Some("txt"),
            _ => None,
        };
        known.map(str::to_string).or_else(|| {
            mime_guess::get_mime_extensions_str(ct).and_then(|e| e.first()).map(|e| e.to_string())
        })
    });
    let short: String = author.chars().filter(|c| c.is_ascii_alphanumeric()).take(8).collect();
    match ext {
        Some(e) => format!("{ts}-{short}-{index}.{e}"),
        None => format!("{ts}-{short}-{index}"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use presage::proto::sync_message::{call_event, CallEvent, Read, Sent};

    const ME: &str = "11111111-1111-1111-1111-111111111111";
    const ANNA: &str = "22222222-2222-2222-2222-222222222222";
    const BOB: &str = "33333333-3333-3333-3333-333333333333";

    fn meta(sender: &str) -> Meta {
        Meta { sender: sender.into(), destination: ME.into(), sender_device: 1, ts: 1000, server_ts: 1001 }
    }

    fn one(meta: &Meta, body: ContentBody) -> Value {
        let mut out = convert(meta, &body, ME);
        assert_eq!(out.len(), 1, "one event");
        out.remove(0).event
    }

    #[test]
    fn text_from_a_person() {
        let dm = DataMessage { body: Some("hello".into()), timestamp: Some(1000), ..Default::default() };
        let e = one(&meta(ANNA), ContentBody::DataMessage(dm));
        assert_eq!(e["event"], "message");
        assert_eq!(e["chat"], json!({"kind": "contact", "id": ANNA}));
        assert_eq!(e["sender"], ANNA);
        assert_eq!(e["outgoing"], false);
        assert_eq!(e["ts"], 1000);
        assert_eq!(e["server_ts"], 1001);
        assert_eq!(e["text"], "hello");
    }

    #[test]
    fn group_message_with_mention_quote_and_attachment() {
        let key = [7u8; 32];
        let dm = DataMessage {
            body: Some("hi \u{fffc}".into()),
            timestamp: Some(2000),
            group_v2: Some(GroupContextV2 { master_key: Some(key.to_vec()), revision: Some(3), group_change: None }),
            body_ranges: vec![BodyRange {
                start: Some(3),
                length: Some(1),
                associated_value: Some(body_range::AssociatedValue::MentionAci(BOB.into())),
            }],
            quote: Some(data_message::Quote { id: Some(1500), author_aci: Some(ME.into()), text: Some("q".into()), ..Default::default() }),
            attachments: vec![AttachmentPointer {
                content_type: Some("image/jpeg".into()),
                size: Some(10),
                flags: Some(1),
                ..Default::default()
            }],
            ..Default::default()
        };
        let mut out = convert(&meta(ANNA), &ContentBody::DataMessage(dm), ME);
        let c = out.remove(0);
        assert_eq!(c.group, Some(key));
        assert_eq!(c.attachments.len(), 1);
        let e = c.event;
        assert_eq!(e["chat"]["kind"], "group");
        assert_eq!(e["chat"]["id"], group_id(&key));
        assert_ne!(e["chat"]["id"], BASE64_STANDARD.encode(key), "the id, not the master key");
        assert_eq!(e["mentions"], json!([{"start": 3, "length": 1, "aci": BOB}]));
        assert_eq!(e["quote"], json!({"ts": 1500, "author": ME, "text": "q"}));
        assert_eq!(e["attachments"][0]["voice"], true);
        assert_eq!(e["attachments"][0]["file"], Value::Null);
        assert_eq!(e["group_revision"], 3);
    }

    #[test]
    fn reaction_delete_and_edit() {
        let r = DataMessage {
            reaction: Some(data_message::Reaction {
                emoji: Some("👍".into()),
                remove: Some(false),
                target_author_aci: Some(ME.into()),
                target_sent_timestamp: Some(900),
                ..Default::default()
            }),
            ..Default::default()
        };
        let e = one(&meta(ANNA), ContentBody::DataMessage(r));
        assert_eq!((e["event"].as_str(), e["emoji"].as_str(), e["target_ts"].as_u64()), (Some("reaction"), Some("👍"), Some(900)));
        assert_eq!(e["target_author"], ME);

        let d = DataMessage { delete: Some(data_message::Delete { target_sent_timestamp: Some(800) }), ..Default::default() };
        let e = one(&meta(ANNA), ContentBody::DataMessage(d));
        assert_eq!((e["event"].as_str(), e["target_ts"].as_u64(), e["target_author"].as_str()), (Some("delete"), Some(800), Some(ANNA)));

        let edit = EditMessage {
            target_sent_timestamp: Some(700),
            data_message: Some(DataMessage { body: Some("fixed".into()), timestamp: Some(1100), ..Default::default() }),
        };
        let e = one(&meta(ANNA), ContentBody::EditMessage(edit));
        assert_eq!((e["event"].as_str(), e["target_ts"].as_u64(), e["text"].as_str(), e["ts"].as_u64()), (Some("edit"), Some(700), Some("fixed"), Some(1100)));
    }

    #[test]
    fn sent_here_as_the_store_keeps_it() {
        // presage keeps what this device sent as a plain message from the owner, to its destination:
        // replayed by `history`, it is in the chat of whom it went to, not the owner's notes
        let dm = DataMessage { body: Some("sent from here".into()), timestamp: Some(4000), ..Default::default() };
        let m = Meta { destination: ANNA.into(), ..meta(ME) };
        let e = one(&m, ContentBody::DataMessage(dm));
        assert_eq!(e["chat"], json!({"kind": "contact", "id": ANNA}));
        assert_eq!((e["sender"].as_str(), e["outgoing"].as_bool()), (Some(ME), Some(true)));
    }

    #[test]
    fn sent_from_the_phone() {
        let sent = Sent {
            destination_service_id: Some(ANNA.into()),
            timestamp: Some(3000),
            message: Some(DataMessage { body: Some("from my phone".into()), timestamp: Some(3000), ..Default::default() }),
            ..Default::default()
        };
        let sm = SyncMessage { content: Some(sync_message::Content::Sent(sent)), ..Default::default() };
        let e = one(&meta(ME), ContentBody::SynchronizeMessage(sm));
        assert_eq!(e["outgoing"], true);
        assert_eq!(e["sender"], ME);
        assert_eq!(e["chat"], json!({"kind": "contact", "id": ANNA}));
        assert_eq!(e["ts"], 3000);
    }

    #[test]
    fn read_on_the_phone_and_receipts() {
        let sm = SyncMessage {
            read: vec![Read { sender_aci: Some(ANNA.into()), timestamp: Some(500), ..Default::default() }],
            ..Default::default()
        };
        let e = one(&meta(ME), ContentBody::SynchronizeMessage(sm));
        assert_eq!(e["event"], "read");
        assert_eq!(e["messages"], json!([{"author": ANNA, "ts": 500}]));

        let rm = ReceiptMessage { r#type: Some(receipt_message::Type::Read as i32), timestamp: vec![1, 2] };
        let e = one(&meta(ANNA), ContentBody::ReceiptMessage(rm));
        assert_eq!((e["event"].as_str(), e["kind"].as_str()), (Some("receipt"), Some("read")));
        assert_eq!(e["timestamps"], json!([1, 2]));
    }

    #[test]
    fn calls() {
        let cm = CallMessage {
            offer: Some(call_message::Offer {
                id: Some(42),
                r#type: Some(call_message::offer::Type::OfferVideoCall as i32),
                opaque: None,
            }),
            ..Default::default()
        };
        let e = one(&meta(ANNA), ContentBody::CallMessage(cm));
        assert_eq!((e["action"].as_str(), e["id"].as_str(), e["video"].as_bool()), (Some("offer"), Some("42"), Some(true)));

        let uuid = Uuid::parse_str(ANNA).unwrap();
        let ev = CallEvent {
            conversation_id: Some(uuid.as_bytes().to_vec()),
            call_id: Some(42),
            timestamp: Some(1234),
            r#type: Some(call_event::Type::AudioCall as i32),
            direction: Some(call_event::Direction::Incoming as i32),
            event: Some(call_event::Event::Accepted as i32),
        };
        let sm = SyncMessage { content: Some(sync_message::Content::CallEvent(ev)), ..Default::default() };
        let e = one(&meta(ME), ContentBody::SynchronizeMessage(sm));
        assert_eq!(e["chat"], json!({"kind": "contact", "id": ANNA}));
        assert_eq!((e["result"].as_str(), e["direction"].as_str(), e["ts"].as_u64()), (Some("accepted"), Some("incoming"), Some(1234)));
    }

    #[test]
    fn nothing_to_keep() {
        let pk = DataMessage { profile_key: Some(vec![1; 32]), flags: Some(4), ..Default::default() };
        assert!(convert(&meta(ANNA), &ContentBody::DataMessage(pk), ME).is_empty());
        let typing = presage::proto::TypingMessage::default();
        assert!(convert(&meta(ANNA), &ContentBody::TypingMessage(typing), ME).is_empty());
    }

    #[test]
    fn sent_here_reaction_edit_and_delete() {
        // what this helper sent comes back in the chat it went to, as the owner's
        let r = DataMessage {
            reaction: Some(data_message::Reaction {
                emoji: Some("❤️".into()),
                remove: Some(false),
                target_author_aci: Some(ANNA.into()),
                target_sent_timestamp: Some(900),
                ..Default::default()
            }),
            timestamp: Some(5000),
            ..Default::default()
        };
        let e = sent_events(ME, 2, ANNA, &ContentBody::DataMessage(r), 5000).remove(0).event;
        assert_eq!(e["event"], "reaction");
        assert_eq!(e["chat"], json!({"kind": "contact", "id": ANNA}));
        assert_eq!((e["sender"].as_str(), e["outgoing"].as_bool(), e["target_author"].as_str()), (Some(ME), Some(true), Some(ANNA)));

        let d = DataMessage { delete: Some(data_message::Delete { target_sent_timestamp: Some(4000) }), ..Default::default() };
        let e = sent_events(ME, 2, ANNA, &ContentBody::DataMessage(d), 5100).remove(0).event;
        assert_eq!((e["event"].as_str(), e["target_author"].as_str(), e["target_ts"].as_u64()), (Some("delete"), Some(ME), Some(4000)));

        let photo = AttachmentPointer { content_type: Some("image/jpeg".into()), ..Default::default() };
        let old = DataMessage {
            body: Some("old \u{fffc}".into()),
            body_ranges: vec![BodyRange { start: Some(4), length: Some(1), associated_value: None }],
            attachments: vec![photo],
            quote: Some(data_message::Quote { id: Some(10), ..Default::default() }),
            ..Default::default()
        };
        let stored = ContentBody::DataMessage(old);
        let dm = edited(data_message_of(&stored), "new", 5200);
        assert_eq!((dm.body.as_deref(), dm.timestamp, dm.attachments.len()), (Some("new"), Some(5200), 1));
        assert!(dm.body_ranges.is_empty() && dm.quote.is_some());
        let body = ContentBody::EditMessage(EditMessage { target_sent_timestamp: Some(4000), data_message: Some(dm) });
        assert!(data_message_of(&body).is_some());
        let c = sent_events(ME, 2, ANNA, &body, 5200).remove(0);
        assert_eq!((c.event["event"].as_str(), c.event["target_ts"].as_u64(), c.event["ts"].as_u64()), (Some("edit"), Some(4000), Some(5200)));
        assert_eq!((c.event["text"].as_str(), c.event["outgoing"].as_bool(), c.attachments.len()), (Some("new"), Some(true), 1));
    }

    #[test]
    fn admin_deletes() {
        let admins = vec![ANNA.to_string()];
        assert!(admin_delete_allowed(ANNA, BOB, Some(&admins)));
        assert!(!admin_delete_allowed(BOB, ANNA, Some(&admins)), "not an admin");
        assert!(!admin_delete_allowed(BOB, ANNA, None), "admins not known");
        assert!(admin_delete_allowed(BOB, BOB, None), "one's own message");
        let d = DataMessage {
            admin_delete: Some(data_message::AdminDelete {
                target_author_aci_binary: Some(Uuid::parse_str(BOB).unwrap().as_bytes().to_vec()),
                target_sent_timestamp: Some(800),
            }),
            ..Default::default()
        };
        let e = one(&meta(ANNA), ContentBody::DataMessage(d));
        assert_eq!((e["event"].as_str(), e["target_author"].as_str(), e["admin"].as_bool()), (Some("delete"), Some(BOB), Some(true)));
    }

    #[test]
    fn names_of_contacts() {
        assert_eq!(contact_names(true, " Anna Rita ", None), (Some("Anna Rita".into()), None));
        assert_eq!(contact_names(true, "Anna Rita", Some("Anna".into())), (Some("Anna Rita".into()), Some("Anna".into())));
        // seen in a message: presage's contact, named after the profile, without the number
        assert_eq!(contact_names(false, "Bob P", None), (None, Some("Bob P".into())));
        assert_eq!(contact_names(false, "", None), (None, None));
    }

    #[test]
    fn attachment_names() {
        assert_eq!(attachment_name(5, ANNA, 0, Some("image/jpeg"), None), "5-22222222-0.jpg");
        assert_eq!(attachment_name(5, ANNA, 1, Some("x/y"), Some("Report.PDF")), "5-22222222-1.pdf");
        assert_eq!(attachment_name(5, ANNA, 2, None, Some("../../etc")), "5-22222222-2");
        assert_eq!(attachment_name(5, "PNI:abc", 0, None, None), "5-PNIabc-0");
    }
}
