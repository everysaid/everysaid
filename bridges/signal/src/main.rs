//! everysaid-signal: Signal for Everysaid, as a linked device (like Signal Desktop) built on presage.
//!
//! A separate program because libsignal and presage are AGPL-3.0: Everysaid starts it and talks to
//! it over stdin/stdout in JSON lines (protocol.rs, README.md), so no AGPL code is linked into it.
//! It never registers a primary device.

mod convert;
mod protocol;

use std::cell::RefCell;
use std::collections::{BTreeMap, HashMap, HashSet};
use std::panic::AssertUnwindSafe;
use std::path::{Path, PathBuf};
use std::rc::Rc;
use std::time::{SystemTime, UNIX_EPOCH};

use futures::{channel::oneshot, future, pin_mut, FutureExt, StreamExt};
use presage::libsignal_service::configuration::SignalServers;
use presage::libsignal_service::content::{Content, ContentBody, Metadata};
use presage::libsignal_service::prelude::{phonenumber, AttachmentPointer, ServiceError, Uuid};
use presage::libsignal_service::protocol::ServiceId;
use presage::libsignal_service::sender::AttachmentSpec;
use presage::manager::Registered;
use presage::model::identity::OnNewIdentity;
use presage::model::messages::Received;
use presage::proto::{
    body_range, data_message, receipt_message, sync_message, BodyRange, DataMessage, EditMessage,
    GroupContextV2, ReceiptMessage, SyncMessage,
};
use presage::store::{ContentsStore, StateStore, Thread};
use presage::Manager;
use presage_store_sqlite::SqliteStore;
use serde_json::{json, Value};
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::sync::mpsc;
use tokio::task::{spawn_local, LocalSet};

use convert::{admin_delete_allowed, attachment_name, contact_names, convert, group_id, Converted, Meta};
use protocol::{event, fail, ok, Command, Request};

type Signal = Manager<SqliteStore, Registered>;

/// What the helper holds: the store once opened, the account once linked.
#[derive(Default)]
struct State {
    store: Option<SqliteStore>,
    manager: Option<Signal>,
    attachments: PathBuf,
    receiving: bool,
    revisions: HashMap<[u8; 32], u32>, // groups described to Everysaid, at their revision
    described: HashSet<String>,        // people described to Everysaid (or being), by ACI
}

type Shared = Rc<RefCell<State>>;

/// The lines going out, one writer for all (answers and events never interleave).
#[derive(Clone)]
struct Out(mpsc::UnboundedSender<Option<Value>>);

impl Out {
    fn send(&self, v: Value) {
        let _ = self.0.send(Some(v));
    }
}

/// A failure, as the answer says it.
struct Fail {
    code: &'static str,
    msg: String,
}

fn failed(e: impl std::fmt::Display) -> Fail {
    Fail { code: "failed", msg: e.to_string() }
}

fn fail_with(code: &'static str, msg: &str) -> Fail {
    Fail { code, msg: msg.to_string() }
}

fn now_ms() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_millis() as u64).unwrap_or(0)
}

fn main() {
    if std::env::args().any(|a| a == "--version") {
        println!("everysaid-signal {} (presage 33dd149, libsignal v0.99.0)", env!("CARGO_PKG_VERSION"));
        return;
    }
    tracing_subscriber::fmt()
        .with_writer(std::io::stderr)
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_env("EVERYSAID_SIGNAL_LOG")
                .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("warn")),
        )
        .init();
    let rt = tokio::runtime::Builder::new_current_thread().enable_all().build().expect("runtime");
    LocalSet::new().block_on(&rt, run());
}

async fn run() {
    let (tx, mut rx) = mpsc::unbounded_channel::<Option<Value>>();
    let out = Out(tx);
    let (done_tx, done_rx) = oneshot::channel::<()>();
    spawn_local(async move {
        let mut stdout = tokio::io::stdout();
        while let Some(Some(v)) = rx.recv().await {
            let line = format!("{v}\n");
            if stdout.write_all(line.as_bytes()).await.is_err() || stdout.flush().await.is_err() {
                break; // Everysaid is gone
            }
        }
        let _ = done_tx.send(());
    });
    let state: Shared = Rc::new(RefCell::new(State::default()));
    let busy = Rc::new(std::cell::Cell::new(0usize)); // commands under way (not a link: it may wait for ever)
    let mut lines = BufReader::new(tokio::io::stdin()).lines();
    while let Ok(Some(line)) = lines.next_line().await {
        if line.trim().is_empty() {
            continue;
        }
        let Request { id, cmd } = match protocol::parse(&line) {
            Ok(r) => r,
            Err((id, e)) => {
                out.send(fail(id, "bad_request", &e));
                continue;
            }
        };
        match cmd {
            Command::Quit => {
                out.send(ok(id, json!({})));
                break;
            }
            // opened before anything else is read, so that what follows finds it
            Command::Open { store, attachments, passphrase } => {
                let r = open(&state, &store, &attachments, passphrase.as_deref()).await;
                answer(&out, id, r);
            }
            cmd => {
                let counted = !matches!(cmd, Command::Link { .. });
                if counted {
                    busy.set(busy.get() + 1);
                }
                let (state, out, busy) = (state.clone(), out.clone(), busy.clone());
                spawn_local(async move {
                    let r = guarded(handle(&state, &out, cmd)).await.unwrap_or_else(|e| Err(failed(e)));
                    answer(&out, id, r);
                    if counted {
                        busy.set(busy.get() - 1);
                    }
                });
            }
        }
    }
    // stdin closed or quit: the commands under way end (for half a minute at most), what was written
    // goes out, then the helper ends
    let until = tokio::time::Instant::now() + std::time::Duration::from_secs(30);
    while busy.get() > 0 && tokio::time::Instant::now() < until {
        tokio::time::sleep(std::time::Duration::from_millis(20)).await;
    }
    let _ = out.0.send(None);
    let _ = done_rx.await;
}

/// A future run with a panic in it (presage's or libsignal's) caught and said, so that a request is
/// still answered and the receiving still says it ended.
async fn guarded<T>(f: impl std::future::Future<Output = T>) -> Result<T, String> {
    AssertUnwindSafe(f).catch_unwind().await.map_err(|p| {
        let why = p.downcast_ref::<&str>().map(|s| s.to_string()).or_else(|| p.downcast_ref::<String>().cloned());
        format!("the helper failed: {}", why.unwrap_or_else(|| "panic".into()))
    })
}

fn answer(out: &Out, id: u64, r: Result<Value, Fail>) {
    out.send(match r {
        Ok(v) => ok(id, v),
        Err(f) => fail(Some(id), f.code, &f.msg),
    });
}

async fn open(state: &Shared, store: &str, attachments: &str, passphrase: Option<&str>) -> Result<Value, Fail> {
    if state.borrow().store.is_some() {
        return Err(fail_with("bad_request", "already open"));
    }
    std::fs::create_dir_all(store).map_err(failed)?;
    std::fs::create_dir_all(attachments).map_err(failed)?;
    restrict(Path::new(store));
    let url = format!("sqlite://{}", Path::new(store).join("presage.db").display());
    let db = SqliteStore::open_with_passphrase(&url, passphrase.filter(|p| !p.is_empty()), OnNewIdentity::Trust)
        .await
        .map_err(|e| {
            let msg = e.to_string();
            if msg.contains("not a database") {
                fail_with("locked", "the store cannot be opened with this passphrase")
            } else {
                failed(msg)
            }
        })?;
    let manager = if db.is_registered().await {
        Some(Manager::load_registered(db.clone()).await.map_err(failed)?)
    } else {
        None
    };
    {
        let mut s = state.borrow_mut();
        s.store = Some(db);
        s.manager = manager;
        s.attachments = PathBuf::from(attachments);
    }
    Ok(status(state))
}

/// The store's folder is for its owner alone (it holds the device's keys).
fn restrict(dir: &Path) {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700));
    }
    #[cfg(not(unix))]
    let _ = dir;
}

fn e164(p: &phonenumber::PhoneNumber) -> String {
    p.format().mode(phonenumber::Mode::E164).to_string()
}

fn status(state: &Shared) -> Value {
    let s = state.borrow();
    match &s.manager {
        Some(m) => {
            let d = m.registration_data();
            json!({
                "open": s.store.is_some(), "linked": true, "receiving": s.receiving,
                "aci": d.service_ids.aci().service_id_string(),
                "pni": d.service_ids.pni().service_id_string(),
                "phone": e164(&d.phone_number), "device_id": d.device_id, "device_name": d.device_name(),
            })
        }
        None => json!({"open": s.store.is_some(), "linked": false, "receiving": false}),
    }
}

fn store_of(state: &Shared) -> Result<SqliteStore, Fail> {
    state.borrow().store.clone().ok_or_else(|| fail_with("not_open", "the store is not open"))
}

fn manager_of(state: &Shared) -> Result<Signal, Fail> {
    let s = state.borrow();
    if s.store.is_none() {
        return Err(fail_with("not_open", "the store is not open"));
    }
    s.manager.clone().ok_or_else(|| fail_with("not_linked", "not linked to a Signal account"))
}

fn own_aci(m: &Signal) -> String {
    m.registration_data().service_ids.aci().service_id_string()
}

async fn handle(state: &Shared, out: &Out, cmd: Command) -> Result<Value, Fail> {
    match cmd {
        Command::Status => Ok(status(state)),
        Command::Link { device_name } => link(state, out, device_name).await,
        Command::Sync => {
            let mut m = manager_of(state)?;
            m.request_contacts().await.map_err(failed)?;
            Ok(json!({"requested": true}))
        }
        Command::Contacts => Ok(json!({"contacts": contacts(&store_of(state)?).await?})),
        Command::Groups => Ok(json!({"groups": groups(&store_of(state)?).await?})),
        Command::Receive { download } => {
            manager_of(state)?;
            if state.borrow().receiving {
                return Ok(json!({"started": false}));
            }
            state.borrow_mut().receiving = true;
            let (state, out) = (state.clone(), out.clone());
            spawn_local(async move {
                if let Err(e) = guarded(receive(state.clone(), out.clone(), download)).await {
                    state.borrow_mut().receiving = false;
                    out.send(event("receive_ended", json!({"error": e})));
                }
            });
            Ok(json!({"started": true}))
        }
        Command::Send(req) => send(state, out, req).await,
        Command::React { chat, target, emoji, remove } => react(state, out, chat, target, emoji, remove).await,
        Command::Edit { chat, target_ts, original_ts, text } => {
            edit(state, out, chat, target_ts, original_ts.unwrap_or(target_ts), text).await
        }
        Command::Delete { chat, target_ts } => delete(state, out, chat, target_ts).await,
        Command::MarkRead { messages, receipts } => mark_read(state, messages, receipts).await,
        Command::Fetch { messages } => fetch_again(state, out, messages).await,
        Command::History { since } => history(state, since).await,
        Command::Open { .. } | Command::Quit => Err(fail_with("bad_request", "not here")),
    }
}

async fn link(state: &Shared, out: &Out, device_name: String) -> Result<Value, Fail> {
    let store = store_of(state)?;
    if state.borrow().manager.is_some() {
        return Err(fail_with("already_linked", "already linked"));
    }
    let (tx, rx) = oneshot::channel();
    let o = out.clone();
    let (linked, _) = future::join(
        Manager::link_secondary_device(store, SignalServers::Production, device_name, tx),
        async move {
            if let Ok(url) = rx.await {
                o.send(event("link_url", json!({"url": url.to_string()})));
            }
        },
    )
    .await;
    let manager = linked.map_err(failed)?;
    state.borrow_mut().manager = Some(manager);
    Ok(status(state))
}

fn profile_name(p: &presage::libsignal_service::Profile) -> Option<String> {
    let n = p.name.as_ref()?;
    let full = match &n.family_name {
        Some(f) if !f.is_empty() => format!("{} {}", n.given_name, f),
        _ => n.given_name.clone(),
    };
    Some(full.trim().to_string()).filter(|s| !s.is_empty())
}

async fn contact_json(store: &SqliteStore, c: &presage::model::contacts::Contact) -> Value {
    let sid = ServiceId::Aci(c.uuid.into());
    let mut profile = None;
    if let Ok(Some(key)) = store.profile_key(&sid).await {
        if let Ok(Some(p)) = store.profile(c.uuid, key).await {
            profile = profile_name(&p);
        }
    }
    let (name, profile) = contact_names(c.phone_number.is_some(), &c.name, profile);
    json!({"aci": c.uuid.to_string(), "phone": c.phone_number.as_ref().map(e164), "name": name, "profile_name": profile})
}

async fn contacts(store: &SqliteStore) -> Result<Vec<Value>, Fail> {
    let mut out = vec![];
    let list: Vec<_> = store.contacts().await.map_err(failed)?.filter_map(Result::ok).collect();
    for c in list {
        out.push(contact_json(store, &c).await);
    }
    Ok(out)
}

/// Someone whose message came, described once presage has fetched their profile (it does so in the
/// background, from the profile key the message carries): a `contact` event, so that a group's
/// members have their names before the phone next sends its contacts.
async fn describe_later(state: Shared, out: Out, store: SqliteStore, aci: String) {
    tokio::time::sleep(std::time::Duration::from_secs(5)).await;
    let found = match ServiceId::parse_from_service_id_string(&aci) {
        Some(sid @ ServiceId::Aci(_)) => store.contact_by_id(&sid).await.ok().flatten(),
        _ => None,
    };
    match found {
        Some(c) if !c.name.trim().is_empty() => out.send(event("contact", contact_json(&store, &c).await)),
        _ => {
            state.borrow_mut().described.remove(&aci); // tried again at their next message
        }
    }
}

fn group_json(key: &[u8; 32], g: &presage::model::groups::Group) -> Value {
    json!({
        "id": group_id(key), "title": g.title, "description": g.description, "revision": g.revision,
        "members": g.members.iter().map(|m| m.aci.service_id_string()).collect::<Vec<_>>(),
        "pending": g.pending_members.iter().map(|m| m.uuid.to_string()).collect::<Vec<_>>(),
    })
}

async fn groups(store: &SqliteStore) -> Result<Vec<Value>, Fail> {
    Ok(store
        .groups()
        .await
        .map_err(failed)?
        .filter_map(Result::ok)
        .map(|(key, g)| group_json(&key, &g))
        .collect())
}

fn meta_of(m: &Metadata) -> Meta {
    Meta {
        sender: m.sender.service_id_string(),
        destination: m.destination.service_id_string(),
        sender_device: u32::from(m.sender_device),
        ts: m.client_timestamp.timestamp_millis().max(0) as u64,
        server_ts: m.server_timestamp.timestamp_millis().max(0) as u64,
    }
}

/// A presage error as text, with what a websocket error hides (the HTTP status of a refused
/// connection is only in its sources).
fn error_text<E: std::error::Error>(e: &presage::Error<E>) -> String {
    let mut text = e.to_string();
    if let presage::Error::ServiceError(ServiceError::WsError(b)) = e {
        text.push_str(&sources_text(b.as_ref()));
    }
    text
}

fn sources_text(e: &dyn std::error::Error) -> String {
    let mut text = String::new();
    let mut at = e.source();
    while let Some(s) = at {
        text.push_str(": ");
        text.push_str(&s.to_string());
        at = s.source();
    }
    text
}

/// Whether Signal's server refused this device's own connection: it was removed from the phone's
/// linked devices (or its account is gone), and only a new link brings it back.
fn is_unlinked(text: &str) -> bool {
    ["Authorization failed", "unexpected status code: 401", "unexpected status code: 403"].iter().any(|p| text.contains(p))
}

async fn receive(state: Shared, out: Out, download: bool) {
    let ended = |state: &Shared, out: &Out, error: Option<String>| {
        state.borrow_mut().receiving = false;
        out.send(event("receive_ended", json!({"error": error})));
    };
    let Ok(mut manager) = manager_of(&state) else {
        return ended(&state, &out, Some("not linked".into()));
    };
    let own = own_aci(&manager);
    let messages = match manager.receive_messages().await {
        Ok(s) => s,
        Err(e) => {
            let text = error_text(&e);
            let code = is_unlinked(&text).then_some("unlinked");
            state.borrow_mut().receiving = false;
            return out.send(event("receive_ended", json!({"error": text, "code": code})));
        }
    };
    pin_mut!(messages);
    while let Some(item) = messages.next().await {
        match item {
            Received::QueueEmpty => out.send(event("queue_empty", json!({}))),
            Received::Contacts => match contacts(manager.store()).await {
                Ok(list) => {
                    let mut s = state.borrow_mut();
                    s.described.extend(list.iter().filter_map(|c| c["aci"].as_str().map(str::to_string)));
                    out.send(event("contacts", json!({"contacts": list})))
                }
                Err(f) => tracing::warn!(error = f.msg, "contacts"),
            },
            Received::DecryptionError(sender) => {
                out.send(event("decryption_error", json!({"sender": sender.service_id_string()})))
            }
            Received::Content(content) => {
                let meta = meta_of(&content.metadata);
                for c in convert(&meta, &content.body, &own) {
                    deliver(&state, &out, &manager, c, download).await;
                }
                if meta.sender != own && state.borrow_mut().described.insert(meta.sender.clone()) {
                    let (state, out, store) = (state.clone(), out.clone(), manager.store().clone());
                    spawn_local(describe_later(state, out, store, meta.sender.clone()));
                }
            }
        }
    }
    ended(&state, &out, None);
}

/// An event out, its group described first where that changed, its attachments fetched where wanted.
async fn deliver(state: &Shared, out: &Out, manager: &Signal, mut c: Converted, download: bool) {
    if c.event["event"] == "delete" && c.event["admin"] == true {
        let sender = c.event["sender"].as_str().unwrap_or("").to_string();
        let mut admins = None;
        if let Some(key) = c.group {
            if let Ok(Some(g)) = manager.store().group(key).await {
                admins = Some(
                    g.members
                        .iter()
                        .filter(|m| m.role == presage::libsignal_service::groups_v2::Role::Administrator)
                        .map(|m| m.aci.service_id_string())
                        .collect::<Vec<_>>(),
                );
            }
        }
        if !admin_delete_allowed(&sender, c.event["target_author"].as_str().unwrap_or(""), admins.as_deref()) {
            tracing::warn!(sender, "a deletion of another's message by someone not an admin of the group: ignored");
            return;
        }
    }
    if let Some(key) = c.group {
        if let Ok(Some(g)) = manager.store().group(key).await {
            let known = state.borrow().revisions.get(&key).copied();
            if known != Some(g.revision) {
                state.borrow_mut().revisions.insert(key, g.revision);
                out.send(event("group", group_json(&key, &g)));
            }
        }
    }
    let dir = state.borrow().attachments.clone();
    let ts = c.event["ts"].as_u64().unwrap_or(0);
    let author = c.event["sender"].as_str().unwrap_or("").to_string();
    for (i, ptr) in c.attachments.iter().enumerate() {
        let name = attachment_name(ts, &author, i, ptr.content_type.as_deref(), ptr.file_name.as_deref());
        let path = dir.join(&name);
        let slot = &mut c.event["attachments"][i];
        if path.exists() {
            slot["file"] = json!(name);
        } else if download {
            match fetch(manager, ptr, &path).await {
                Ok(()) => slot["file"] = json!(name),
                Err(e) => slot["error"] = json!(e),
            }
        }
    }
    out.send(c.event);
}

async fn fetch(manager: &Signal, ptr: &AttachmentPointer, path: &Path) -> Result<(), String> {
    let data = manager.get_attachment(ptr).await.map_err(|e| e.to_string())?;
    let part = path.with_extension("part");
    tokio::fs::write(&part, &data).await.map_err(|e| e.to_string())?;
    tokio::fs::rename(&part, path).await.map_err(|e| e.to_string())
}

/// The files of messages the store holds fetched again (those that failed before): each message comes
/// again as its event, its files in it where they came now.
async fn fetch_again(state: &Shared, out: &Out, refs: Vec<protocol::MessageRef>) -> Result<Value, Fail> {
    let m = manager_of(state)?;
    let own = own_aci(&m);
    let store = m.store().clone();
    let mut found = 0;
    for r in refs {
        let Ok(sid) = parse_sid(&r.author) else { continue };
        let Ok(Some(thread)) = store.thread_for_sender_and_timestamp(&sid, r.ts).await else { continue };
        let Ok(Some(content)) = store.message(&thread, r.ts).await else { continue };
        for c in convert(&meta_of(&content.metadata), &content.body, &own) {
            if c.event["event"] == "message" && c.event["ts"].as_u64() == Some(r.ts) && !c.attachments.is_empty() {
                deliver(state, out, &m, c, true).await;
                found += 1;
            }
        }
    }
    Ok(json!({"found": found}))
}

fn parse_sid(s: &str) -> Result<ServiceId, Fail> {
    ServiceId::parse_from_service_id_string(s).ok_or_else(|| fail_with("bad_request", "not a Signal id"))
}

/// A group's master key, by its id.
async fn group_key(store: &SqliteStore, id: &str) -> Result<([u8; 32], u32), Fail> {
    for (key, g) in store.groups().await.map_err(failed)?.filter_map(Result::ok) {
        if group_id(&key) == id {
            return Ok((key, g.revision));
        }
    }
    Err(fail_with("unknown_group", "unknown group"))
}

async fn send(state: &Shared, out: &Out, req: protocol::SendRequest) -> Result<Value, Fail> {
    let mut m = manager_of(state)?;
    let own = own_aci(&m);
    let ts = now_ms();
    let mut dm = DataMessage { timestamp: Some(ts), ..Default::default() };
    if !req.text.is_empty() {
        dm.body = Some(req.text.clone());
    }
    if let Some(q) = &req.quote {
        dm.quote = Some(data_message::Quote {
            id: Some(q.ts),
            author_aci: Some(q.author.clone()),
            text: q.text.clone(),
            r#type: Some(data_message::quote::Type::Normal as i32),
            ..Default::default()
        });
    }
    dm.body_ranges = req
        .mentions
        .iter()
        .map(|x| BodyRange {
            start: Some(x.start),
            length: Some(x.length),
            associated_value: Some(body_range::AssociatedValue::MentionAci(x.aci.clone())),
        })
        .collect();
    let mut files = vec![];
    for a in &req.attachments {
        let data = tokio::fs::read(&a.path).await.map_err(failed)?;
        let filename = a.filename.clone().or_else(|| {
            Path::new(&a.path).file_name().map(|f| f.to_string_lossy().into_owned())
        });
        let content_type = a.content_type.clone().unwrap_or_else(|| {
            mime_guess::from_path(&a.path).first_or_octet_stream().essence_str().to_string()
        });
        let spec = AttachmentSpec {
            content_type,
            length: data.len(),
            file_name: filename,
            preview: None,
            voice_note: Some(a.voice),
            borderless: None,
            width: None,
            height: None,
            caption: None,
            blur_hash: None,
        };
        files.push((spec, data));
    }
    let local: Vec<Vec<u8>> = files.iter().map(|(_, d)| d.clone()).collect();
    for r in m.upload_attachments(files).await.map_err(failed)? {
        dm.attachments.push(r.map_err(|e| failed(format!("{e:?}")))?);
    }
    let chat = json!({"kind": req.chat.kind, "id": req.chat.id});
    send_to(&mut m, &req.chat, &mut dm, None, ts).await?;
    // what was sent comes back as the others' messages do, its files kept like theirs
    let device = m.registration_data().device_id.unwrap_or(0);
    let mut c = convert::sent_event(&own, device, chat, &dm, ts);
    let dir = state.borrow().attachments.clone();
    for (i, data) in local.iter().enumerate() {
        let ptr = &c.attachments[i];
        let name = attachment_name(ts, &own, i, ptr.content_type.as_deref(), ptr.file_name.as_deref());
        if tokio::fs::write(dir.join(&name), data).await.is_ok() {
            c.event["attachments"][i]["file"] = json!(name);
        }
    }
    out.send(c.event);
    Ok(json!({"ts": ts}))
}

/// Sends into a chat: to the person, or to the group's members with the group's context in the
/// message (an edit's message, for an edit), as Signal's apps do; `dm` keeps the context put in it.
async fn send_to(
    m: &mut Signal,
    chat: &protocol::Chat,
    dm: &mut DataMessage,
    edit: Option<u64>,
    ts: u64,
) -> Result<(), Fail> {
    let group = match chat.kind.as_str() {
        "contact" => None,
        "group" => Some(group_key(m.store(), &chat.id).await?),
        _ => return Err(fail_with("bad_request", "chat kind is contact or group")),
    };
    if let Some((key, revision)) = group {
        dm.group_v2 =
            Some(GroupContextV2 { master_key: Some(key.to_vec()), revision: Some(revision), ..Default::default() });
    }
    let body: ContentBody = match edit {
        Some(target) => EditMessage { target_sent_timestamp: Some(target), data_message: Some(dm.clone()) }.into(),
        None => dm.clone().into(),
    };
    match group {
        Some((key, _)) => m.send_message_to_group(&key, body, ts).await.map_err(failed),
        None => m.send_message(parse_sid(&chat.id)?, body, ts).await.map_err(failed),
    }
}

/// What this helper sent besides a message (a reaction, an edit, a deletion), as the events of the
/// others' are, so that it is kept the same way.
fn sent_back(m: &Signal, chat: &protocol::Chat, body: &ContentBody, ts: u64) -> Vec<Converted> {
    let device = m.registration_data().device_id.unwrap_or(0);
    let to = if chat.kind == "contact" { chat.id.as_str() } else { "" };
    convert::sent_events(&own_aci(m), device, to, body, ts)
}

/// The owner's reaction on a message (in place of the one there was), or taken back with `remove`.
async fn react(
    state: &Shared,
    out: &Out,
    chat: protocol::Chat,
    target: protocol::MessageRef,
    emoji: String,
    remove: bool,
) -> Result<Value, Fail> {
    if emoji.is_empty() {
        return Err(fail_with("bad_request", "no emoji"));
    }
    let mut m = manager_of(state)?;
    let ts = now_ms();
    let mut dm = DataMessage {
        reaction: Some(data_message::Reaction {
            emoji: Some(emoji),
            remove: Some(remove),
            target_author_aci: Some(target.author),
            target_sent_timestamp: Some(target.ts),
            ..Default::default()
        }),
        timestamp: Some(ts),
        ..Default::default()
    };
    send_to(&mut m, &chat, &mut dm, None, ts).await?;
    for c in sent_back(&m, &chat, &ContentBody::DataMessage(dm), ts) {
        out.send(c.event);
    }
    Ok(json!({"ts": ts}))
}

/// The owner's message with a new text. Signal's edit replaces the whole message, so it is made from
/// the one the store holds (its files and quote kept); the files of the edit's event are those
/// already fetched for the message.
async fn edit(
    state: &Shared,
    out: &Out,
    chat: protocol::Chat,
    target_ts: u64,
    original_ts: u64,
    text: String,
) -> Result<Value, Fail> {
    let mut m = manager_of(state)?;
    let own = own_aci(&m);
    let thread = match chat.kind.as_str() {
        "contact" => Thread::Contact(parse_sid(&chat.id)?),
        "group" => Thread::Group(group_key(m.store(), &chat.id).await?.0),
        _ => return Err(fail_with("bad_request", "chat kind is contact or group")),
    };
    let stored = m.store().message(&thread, target_ts).await.ok().flatten();
    let old = stored.as_ref().and_then(|c| convert::data_message_of(&c.body));
    if text.is_empty() && old.is_none_or(|o| o.attachments.is_empty()) {
        return Err(fail_with("bad_request", "an edit leaves nothing of the message"));
    }
    let ts = now_ms();
    let mut dm = convert::edited(old, &text, ts);
    send_to(&mut m, &chat, &mut dm, Some(target_ts), ts).await?;
    let body = ContentBody::EditMessage(EditMessage { target_sent_timestamp: Some(target_ts), data_message: Some(dm) });
    let dir = state.borrow().attachments.clone();
    for mut c in sent_back(&m, &chat, &body, ts) {
        for (i, ptr) in c.attachments.iter().enumerate() {
            for at in [original_ts, target_ts] {
                let name = attachment_name(at, &own, i, ptr.content_type.as_deref(), ptr.file_name.as_deref());
                if dir.join(&name).exists() {
                    c.event["attachments"][i]["file"] = json!(name);
                    break;
                }
            }
        }
        out.send(c.event);
    }
    Ok(json!({"ts": ts}))
}

/// The owner's message deleted for everyone in the chat.
async fn delete(state: &Shared, out: &Out, chat: protocol::Chat, target_ts: u64) -> Result<Value, Fail> {
    let mut m = manager_of(state)?;
    let ts = now_ms();
    let mut dm = DataMessage {
        delete: Some(data_message::Delete { target_sent_timestamp: Some(target_ts) }),
        timestamp: Some(ts),
        ..Default::default()
    };
    send_to(&mut m, &chat, &mut dm, None, ts).await?;
    for c in sent_back(&m, &chat, &ContentBody::DataMessage(dm), ts) {
        out.send(c.event);
    }
    Ok(json!({"ts": ts}))
}

/// The others' messages read: said to the account's other devices (the phone) always, as Signal
/// Desktop does, and to each author with a read receipt where `receipts` (the user's choice).
async fn mark_read(state: &Shared, messages: Vec<protocol::MessageRef>, receipts: bool) -> Result<Value, Fail> {
    let mut m = manager_of(state)?;
    let own = own_aci(&m);
    let mut by_author: BTreeMap<String, Vec<u64>> = BTreeMap::new();
    for x in messages.iter().filter(|x| x.author != own) {
        by_author.entry(x.author.clone()).or_default().push(x.ts);
    }
    let ts = now_ms();
    let mut marked = 0;
    let mut reads = vec![];
    for (author, stamps) in &by_author {
        let to = parse_sid(author)?;
        if receipts {
            let receipt = ReceiptMessage { r#type: Some(receipt_message::Type::Read as i32), timestamp: stamps.clone() };
            m.send_message(to, receipt, ts).await.map_err(failed)?;
        }
        marked += stamps.len();
        for t in stamps {
            reads.push(sync_message::Read { sender_aci: Some(author.clone()), timestamp: Some(*t), ..Default::default() });
        }
    }
    if !reads.is_empty() {
        let sync = SyncMessage { read: reads, ..Default::default() };
        let me = m.registration_data().service_ids.aci();
        m.send_message(me, sync, ts).await.map_err(failed)?;
    }
    Ok(json!({"marked": marked}))
}

/// The events of what the store holds, sent after `since` (attachments as files already fetched).
async fn history(state: &Shared, since: u64) -> Result<Value, Fail> {
    let m = manager_of(state)?;
    let own = own_aci(&m);
    let store = m.store();
    let mut threads = vec![Thread::Contact(m.registration_data().service_ids.aci().into())];
    for c in store.contacts().await.map_err(failed)?.filter_map(Result::ok) {
        if c.uuid != Uuid::nil() {
            threads.push(Thread::Contact(ServiceId::Aci(c.uuid.into())));
        }
    }
    for (key, _) in store.groups().await.map_err(failed)?.filter_map(Result::ok) {
        threads.push(Thread::Group(key));
    }
    let dir = state.borrow().attachments.clone();
    let mut events = vec![];
    for t in threads {
        let contents: Vec<Content> = store.messages(&t, since + 1..).await.map_err(failed)?.filter_map(Result::ok).collect();
        for content in contents {
            if matches!(content.body, ContentBody::NullMessage(_)) {
                continue; // deleted: presage keeps an empty message in its place
            }
            for mut c in convert(&meta_of(&content.metadata), &content.body, &own) {
                let ts = c.event["ts"].as_u64().unwrap_or(0);
                let author = c.event["sender"].as_str().unwrap_or("").to_string();
                for (i, ptr) in c.attachments.iter().enumerate() {
                    let name = attachment_name(ts, &author, i, ptr.content_type.as_deref(), ptr.file_name.as_deref());
                    if dir.join(&name).exists() {
                        c.event["attachments"][i]["file"] = json!(name);
                    }
                }
                events.push(c.event);
            }
        }
    }
    Ok(json!({"events": events}))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[derive(Debug)]
    struct E(&'static str, Option<Box<E>>);
    impl std::fmt::Display for E {
        fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
            f.write_str(self.0)
        }
    }
    impl std::error::Error for E {
        fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
            self.1.as_deref().map(|e| e as _)
        }
    }

    #[test]
    fn a_device_removed_from_the_phone() {
        // the websocket's refusal says its status only in its sources
        let ws = E("websocket upgrade failed", Some(Box::new(E("unexpected status code: 403 Forbidden", None))));
        let text = format!("Websocket error: {ws}{}", sources_text(&ws));
        assert!(is_unlinked(&text), "{text}");
        assert!(is_unlinked("Authorization failed"));
        assert!(!is_unlinked("Websocket error: websocket upgrade failed: error sending request"));
    }

    #[tokio::test]
    async fn a_panic_is_an_answer() {
        assert_eq!(guarded(async { 7 }).await, Ok(7));
        let r = guarded(async {
            if now_ms() > 0 {
                panic!("in presage");
            }
        })
        .await;
        assert_eq!(r, Err("the helper failed: in presage".to_string()));
    }
}
