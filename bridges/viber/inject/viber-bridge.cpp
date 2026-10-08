// Everysaid Viber bridge — the resident side.
//
// An LD_PRELOAD library that drives the running Viber Desktop (Linux, Qt 6.10) from inside, the way
// GammaRay inspects a Qt app: it installs Qt's own QObject create/destroy hooks (qtHookData) to keep a
// live registry of Viber's objects, and calls their methods by name through the meta-object system
// (QMetaObject::metacall) — the same entry points Viber's own QML UI uses, so nothing depends on a
// binary offset. See docs/viber-bridge.md for how each mechanism was found and verified.
//
// It opens a line-based Unix socket (VIBER_BRIDGE_SOCK, default $XDG_RUNTIME_DIR/viber-bridge.sock,
// mode 600) whose commands run on Viber's main thread. Only named operations are exposed — no generic
// SQL/method surface. Reads come from the SQLite connections Viber has already unlocked (no
// decryption). Actions that send are refused unless VIBER_ALLOW_SEND=1 (the equivalent of the
// WhatsApp bridge's -send).
//
// Commands (one per line; TEXT/PATH is the rest of the line, may contain spaces):
//   ping                      -> "pong"
//   chats                     -> id \t name \t flags \t token \t lastReadToken \t timestampMs
//   events SINCE [LIMIT]      -> for each Event with id>SINCE: id,chat,contact,dir,type,ts,token,read,body,info
//   message EVENTID           -> one row: id,chat,dir,type,ts,token,body,info
//   subscribe                 -> stream new events live (same columns as `events`) until disconnect
//   send CHATID TEXT          -> send plain text (\n, \t, \\ escaped)              [needs VIBER_ALLOW_SEND]
//   file CHATID PATH          -> send a local file/image                          [needs VIBER_ALLOW_SEND]
//   reply CHATID TARGET TEXT  -> quoted reply to TARGET event                     [needs VIBER_ALLOW_SEND]
//   react TARGET CODE         -> CODE a quick reaction (1-5: heart..angry), "like", or any emoji
//                                                                                 [needs VIBER_ALLOW_SEND]
//   unreact TARGET            -> remove own reaction                              [needs VIBER_ALLOW_SEND]
//   read CHATID               -> mark chat read                                   [needs VIBER_ALLOW_SEND]
//   compose JSON              -> {"chat":ID,"reply":EVENT,"edit":EVENT,"parts":[{"text":"…"},{"mention":CONTACTID},…]}
//                                typed into the chat's input and sent: a reply, an edit of the user's
//                                own message, mentions (any of reply/edit 0 or left out)  [needs VIBER_ALLOW_SEND]
//   delete TARGET             -> delete the user's own message for everyone      [needs VIBER_ALLOW_SEND]
//   snapshot PATH             -> a plain (decrypted) copy of viber.db at PATH (mode 600), for the importer
//   input                     -> the text in the focused input (to check what compose typed)
//   check                     -> "version V", then per capability "ok NAME [note]" or "missing NAME WHAT":
//                                whether this Viber still has what each command calls (nothing done)
//   quit                      -> Viber quits cleanly
//
// Reaction emoji codes follow the archive's convention: 1-5 = heart, laugh, wow, sad, angry.
//
// Body/Info are tab- and newline-escaped (\t, \n, \\) so one event is always one line.

#include <QCoreApplication>
#include <QEvent>
#include <QGuiApplication>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QElapsedTimer>
#include <QFile>
#include <QKeyEvent>
#include <QMetaMethod>
#include <QMetaObject>
#include <QMetaType>
#include <QObject>
#include <QString>
#include <QStringList>
#include <QThread>
#include <QUrl>
#include <QVariant>
#include <QWindow>
#include <QTextDocumentFragment>
#include <QtSql/QSqlDatabase>
#include <QtSql/QSqlError>
#include <QtSql/QSqlQuery>
#include <QtSql/QSqlRecord>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fcntl.h>
#include <atomic>
#include <map>
#include <mutex>
#include <set>
#include <string>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <thread>
#include <unistd.h>
#include <vector>

#include "watch.h"

// ---- object registry via Qt hooks -------------------------------------------------------------

extern "C" Q_DECL_IMPORT quintptr qtHookData[];
enum { AddQObject = 3, RemoveQObject = 4 };
using Hook = void (*)(QObject *);

static std::mutex g_mu;
static std::set<QObject *> *g_objs = new std::set<QObject *>;
static Hook g_prevAdd, g_prevRemove;
static thread_local bool g_inHook = false;

static QList<QObject *> objectsOf(const char *cls) {
    QByteArray c(cls);
    QList<QObject *> out;
    std::lock_guard<std::mutex> g(g_mu);
    for (QObject *o : *g_objs)
        if (c == o->metaObject()->className()) out << o;
    return out;
}

// Every class seen, by name: meta-objects are static, so they stay valid when their objects are gone
// (a chat's controller and input exist only while a chat is open). Kept by `check` and compose.
static std::map<std::string, const QMetaObject *> g_classes;

static void rememberClasses() {
    std::lock_guard<std::mutex> g(g_mu);
    for (QObject *o : *g_objs) {
        const QMetaObject *mo = o->metaObject();
        g_classes.emplace(mo->className(), mo);
    }
}

static int methodByName(QObject *o, const char *name, int params = -1) {
    const QMetaObject *mo = o->metaObject();
    for (int i = 0; i < mo->methodCount(); ++i) {
        QMetaMethod m = mo->method(i);
        if (m.name() == name && (params < 0 || m.parameterCount() == params)) return i;
    }
    return -1;
}

// ---- database (Viber's own open connections) --------------------------------------------------

static QString esc(const QString &s) {
    QString o;
    o.reserve(s.size());
    for (QChar c : s) {
        if (c == '\\') o += "\\\\";
        else if (c == '\t') o += "\\t";
        else if (c == '\n') o += "\\n";
        else o += c;
    }
    return o;
}

static QSqlDatabase viberDb() { return QSqlDatabase::database("viber.db", false); }

// Fixed, parameterless-shape queries only — no caller-supplied SQL. `check` runs each with LIMIT 0.
static const char *kEventsSql =
    "SELECT e.EventID, e.ChatID, e.ContactID, e.Direction, e.Type, e.TimeStamp, e.Token, "
    "e.IsRead, m.Body, m.Info FROM Events e LEFT JOIN Messages m ON m.EventID=e.EventID "
    "WHERE e.EventID>? ORDER BY e.EventID LIMIT ?";
static const char *kChatsSql = "SELECT ChatID, Name, Flags, Token, LastReadMessageToken, TimeStamp FROM ChatInfo";
static const char *kTablesSql =
    "SELECT name, sql FROM main.sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND sql IS NOT NULL";

static QString rowsEvents(qlonglong since, int limit) {
    QSqlDatabase db = viberDb();
    if (!db.isOpen()) return "error not-open\n";
    QSqlQuery q(db);
    q.prepare(kEventsSql);
    q.addBindValue(since);
    q.addBindValue(limit);
    if (!q.exec()) return "error " + esc(q.lastError().text()) + "\n";
    QString out;
    while (q.next()) {
        QStringList r;
        for (int i = 0; i < 10; ++i) r << esc(q.value(i).toString());
        out += r.join('\t') + "\n";
    }
    return out;
}

static qlonglong maxEventId() {
    QSqlDatabase db = viberDb();
    if (!db.isOpen()) return 0;
    QSqlQuery q(db);
    if (q.exec("SELECT MAX(EventID) FROM Events") && q.next()) return q.value(0).toLongLong();
    return 0;
}

// ---- live event stream ------------------------------------------------------------------------

static std::mutex g_subMu;
static std::vector<int> g_subs;        // subscriber socket fds
static qlonglong g_watermark = 0;      // highest EventID already streamed
static EventWatcher *g_watcher = nullptr;

// false: the peer is gone (Viber ignores SIGPIPE, so a write to it fails rather than ends Viber)
static bool writeAll(int fd, const QByteArray &b) {
    for (qsizetype off = 0; off < b.size();) {
        ssize_t w = write(fd, b.constData() + off, b.size() - off);
        if (w <= 0) return false;
        off += w;
    }
    return true;
}

// Called on the main thread when EventsStorage::eventsAdded fires.
void viberOnEvents() {
    std::lock_guard<std::mutex> g(g_subMu);
    if (g_subs.empty()) { g_watermark = maxEventId(); return; }
    QString rows = rowsEvents(g_watermark, 500);
    g_watermark = maxEventId();
    if (rows.isEmpty()) return;
    QByteArray b = rows.toUtf8();
    for (auto it = g_subs.begin(); it != g_subs.end();) {
        if (writeAll(*it, b)) {
            ++it;
        } else {  // a subscriber that went away: its connection closed, not written to again
            close(*it);
            it = g_subs.erase(it);
        }
    }
}

static bool armWatcher() {
    QList<QObject *> ss = objectsOf("EventsStorage");
    if (ss.isEmpty()) return false;
    if (!g_watcher) g_watcher = new EventWatcher;
    g_watermark = maxEventId();
    int slot = g_watcher->metaObject()->indexOfSlot("onEvents()");
    bool any = false;
    for (QObject *o : ss) {
        int sig = o->metaObject()->indexOfSignal("eventsAdded(QList<EventData>)");
        if (sig < 0) continue;
        if (QObject::connect(o, o->metaObject()->method(sig), g_watcher, g_watcher->metaObject()->method(slot)))
            any = true;
    }
    return any;
}

// ---- actions ----------------------------------------------------------------------------------

static bool sendAllowed() {
    const char *e = getenv("VIBER_ALLOW_SEND");
    return e && std::strcmp(e, "1") == 0;
}

static bool invoke(QObject *o, int idx, void **argv) {
    return QMetaObject::metacall(o, QMetaObject::InvokeMetaMethod, idx, argv) < 0;
}

// The other way of esc: a TEXT on one line may say \n, \t and \\.
static QString unesc(const QString &s) {
    QString o;
    o.reserve(s.size());
    for (qsizetype i = 0; i < s.size(); ++i) {
        if (s[i] == '\\' && i + 1 < s.size()) {
            QChar n = s[++i];
            o += n == 'n' ? QChar('\n') : n == 't' ? QChar('\t') : n;
        } else {
            o += s[i];
        }
    }
    return o;
}

static QString actSend(qlonglong cid, const QString &text) {
    QList<QObject *> ns = objectsOf("TrayNotifier");
    if (ns.size() != 1) return QString("error tray-count-%1\n").arg(ns.size());
    int idx = ns[0]->metaObject()->indexOfMethod("sendMessage(ChatID,QString)");
    if (idx < 0) return "error no-sendMessage\n";
    QString s = text;
    void *av[] = {nullptr, &cid, &s};
    invoke(ns[0], idx, av);
    return "ok\n";
}

static QString actFile(qlonglong cid, const QString &path) {
    QList<QObject *> ms = objectsOf("MainWidget");
    if (ms.isEmpty()) return "error no-mainwidget\n";
    QObject *o = ms.first();
    int idx = o->metaObject()->indexOfMethod("sendFilesToChat(QList<QUrl>,ChatID)");
    if (idx < 0) idx = o->metaObject()->indexOfMethod("sendFilesToChat(QList<QUrl>,ChatID::type)");
    if (idx < 0) return "error no-sendFilesToChat\n";
    QList<QUrl> urls{QUrl::fromLocalFile(path)};
    void *av[] = {nullptr, &urls, &cid};
    invoke(o, idx, av);
    return "ok\n";
}

static void openChat(qlonglong cid) {
    QList<QObject *> ms = objectsOf("MainWidget");
    if (ms.isEmpty()) return;
    int oi = methodByName(ms.first(), "openChat", 2);
    if (oi < 0) return;
    QString origin = "bridge";
    void *ov[] = {nullptr, &cid, &origin};
    QMetaObject::metacall(ms.first(), QMetaObject::InvokeMetaMethod, oi, ov);
}

// The open chat's controller for this chat: the chat is opened, and Qt's events run until its
// controller is there (QML makes it after openChat returns), at most two seconds.
static QObject *chatObject(qlonglong cid) {
    openChat(cid);
    QElapsedTimer t;
    t.start();
    do {
        for (QObject *c : objectsOf("Chat"))
            if (c->property("chatID").toLongLong() == cid) return c;
        QCoreApplication::processEvents(QEventLoop::AllEvents, 50);
    } while (t.elapsed() < 2000);
    return nullptr;
}

static void call0(QObject *o, const char *name) {
    int i = methodByName(o, name, 0);
    if (i >= 0) { void *v[] = {nullptr}; QMetaObject::metacall(o, QMetaObject::InvokeMetaMethod, i, v); }
}

static bool callText(QObject *o, const char *name, const QString &text) {
    int i = methodByName(o, name, 1);
    if (i < 0) return false;
    QString s = text;
    void *v[] = {nullptr, &s};
    QMetaObject::metacall(o, QMetaObject::InvokeMetaMethod, i, v);
    return true;
}

// Return on the main window, which routes it to the focused text item (the input's own send-on-Enter;
// works without a window manager under Xvfb).
static bool pressReturn() {
    QWindow *w = qGuiApp ? qGuiApp->focusWindow() : nullptr;
    if (!w && qGuiApp)
        for (QWindow *c : qGuiApp->topLevelWindows())
            if (c->isVisible()) { w = c; break; }
    if (!w) return false;
    w->requestActivate();
    QKeyEvent press(QEvent::KeyPress, Qt::Key_Return, Qt::NoModifier, "\r");
    QKeyEvent release(QEvent::KeyRelease, Qt::Key_Return, Qt::NoModifier, "\r");
    QCoreApplication::sendEvent(w, &press);
    QCoreApplication::sendEvent(w, &release);
    return true;
}

// The text item that has the focus (the chat's input once it was focused).
static QObject *focusedText() {
    std::lock_guard<std::mutex> g(g_mu);
    for (QObject *o : *g_objs)
        if (o->inherits("QQuickTextEdit") && o->property("activeFocus").toBool()) return o;
    return nullptr;
}

// What the user would type, typed into the chat's input and sent with Return: a quoted reply
// (Chat::onMessageReply), an edit of the user's own message (Chat::editMessage puts it in the input),
// mentions (InputBoxArea::mentionSelected after an "@", as when one is picked from the list).
static bool g_busy = false;  // a compose or a check under way: Qt's events run inside it

static QString compose(const QString &json);

static QString actCompose(const QString &json) {
    g_busy = true;
    QString out = compose(json);
    g_busy = false;
    return out;
}

static QString compose(const QString &json) {
    QJsonParseError pe;
    QJsonObject o = QJsonDocument::fromJson(json.toUtf8(), &pe).object();
    if (pe.error != QJsonParseError::NoError) return "error bad-json\n";
    qlonglong cid = o.value("chat").toInteger(), reply = o.value("reply").toInteger(), edit = o.value("edit").toInteger();
    QJsonArray parts = o.value("parts").toArray();
    if (!cid || parts.isEmpty()) return "error bad-compose\n";
    QObject *chat = chatObject(cid);
    if (!chat) return "error chat-not-open\n";
    if (edit) {
        int ei = methodByName(chat, "editMessage", 1);
        if (ei < 0) return "error no-editMessage\n";
        bool ok = false;
        void *ev[] = {&ok, &edit};
        QMetaObject::metacall(chat, QMetaObject::InvokeMetaMethod, ei, ev);
        if (!ok) return "error not-editable\n";
    } else if (reply) {
        int ri = methodByName(chat, "onMessageReply", 2);
        if (ri < 0) return "error no-onMessageReply\n";
        bool priv = false;
        void *rv[] = {nullptr, &reply, &priv};
        QMetaObject::metacall(chat, QMetaObject::InvokeMetaMethod, ri, rv);
    }
    rememberClasses();
    QList<QObject *> is = objectsOf("InputBoxArea");
    if (is.isEmpty()) return "error no-inputbox\n";
    QObject *in = is.first();
    call0(in, "forceActiveFocus");
    if (!callText(in, "replaceTextWith", "")) return "error no-replaceTextWith\n";
    // Typed from the end to the start, each piece at the input's beginning: mentionSelected replaces
    // all the text before the cursor (it takes it for what was typed after the "@"), so a mention
    // goes in where nothing is before it yet. The mention brings its own space after it.
    QObject *field = focusedText();
    if (!field) return "error no-focus\n";
    // What is sent is what was asked: each piece of text in the input, and an "@" for each mention
    // (a chat still opening loses pieces, and its members come a moment later), else typed again,
    // else nothing is sent.
    auto typed = [&]() {
        QString have = QTextDocumentFragment::fromHtml(field->property("text").toString()).toPlainText();
        qsizetype ats = 0;
        for (const QJsonValue &v : parts) {
            QJsonObject p = v.toObject();
            QString t = p.value("text").toString();
            ats += p.contains("mention") ? 1 : t.count('@');
            if (!p.contains("mention") && !have.contains(t.trimmed())) return false;
        }
        return have.count('@') == ats;
    };
    const int attempts = 3;
    for (int attempt = 0; attempt < attempts; ++attempt) {
        if (attempt) {
            callText(in, "replaceTextWith", "");
            QElapsedTimer wait;
            wait.start();
            while (wait.elapsed() < 1000) QCoreApplication::processEvents(QEventLoop::AllEvents, 50);
            field->setProperty("cursorPosition", 0);
        }
        for (qsizetype i = parts.size() - 1; i >= 0; --i) {
            QJsonObject p = parts[i].toObject();
            field->setProperty("cursorPosition", 0);
            if (p.contains("mention")) {
                qlonglong contact = p.value("mention").toInteger();
                int mi = methodByName(in, "mentionSelected", 1);
                if (mi < 0) return "error no-mentionSelected\n";
                callText(in, "insertEmoticon", "@");
                void *mv[] = {nullptr, &contact};
                QMetaObject::metacall(in, QMetaObject::InvokeMetaMethod, mi, mv);
            } else {
                QString t = p.value("text").toString();
                if (i > 0 && parts[i - 1].toObject().contains("mention") && t.startsWith(' ')) t.remove(0, 1);
                if (!t.isEmpty() && !callText(in, "insertEmoticon", t)) return "error no-insertEmoticon\n";
            }
            QCoreApplication::processEvents(QEventLoop::AllEvents, 20);
        }
        if (typed()) break;
        if (attempt == attempts - 1) {
            callText(in, "replaceTextWith", "");
            return "error compose-mismatch\n";
        }
    }
    if (getenv("VIBER_BRIDGE_DRY")) return "ok dry\n";  // typed, not sent: `input` shows it
    // InputBoxArea::accept() is what the input's Return calls: sent without a key event, so it does
    // not depend on which window has the focus (on a desktop in use, not Viber's)
    if (methodByName(in, "accept", 0) >= 0) call0(in, "accept");
    else if (!pressReturn()) return "error no-window\n";
    return "ok\n";
}

static QString actDelete(qlonglong target) {
    QList<QObject *> ms = objectsOf("MessageActions");
    if (ms.isEmpty()) return "error no-messageactions\n";
    int di = ms.first()->metaObject()->indexOfMethod("deleteMessageForAll(EventID::type)");
    if (di < 0) di = ms.first()->metaObject()->indexOfMethod("deleteMessageForAll(EventID)");
    if (di < 0) return "error no-deleteMessageForAll\n";
    void *dv[] = {nullptr, &target};
    QMetaObject::metacall(ms.first(), QMetaObject::InvokeMetaMethod, di, dv);
    return "ok\n";
}

// A plain copy of viber.db through Viber's own (unlocked) connection, written beside PATH and moved
// into place only when whole, so the importer never reads half a copy.
static QString snapshot(const QString &path) {
    if (path.isEmpty() || !path.startsWith('/') || path.contains('\'') || path.contains('\n'))
        return "error bad-path\n";
    QSqlDatabase db = viberDb();
    if (!db.isOpen()) return "error not-open\n";
    const QString tmp = path + ".part";
    QFile::remove(tmp);
    int fd = open(tmp.toUtf8().constData(), O_CREAT | O_EXCL | O_WRONLY, 0600);  // private from the start
    if (fd < 0) return "error cannot-create\n";
    close(fd);
    QSqlQuery q(db);
    if (!q.exec("ATTACH DATABASE '" + tmp + "' AS plain KEY ''")) {
        QFile::remove(tmp);
        return "error " + esc(q.lastError().text()) + "\n";
    }
    // Viber's SQLite has no sqlcipher_export: each table is made again from its own statement (keys
    // and types kept), and filled, in one transaction. Virtual tables (full-text indexes) and their
    // shadow tables are Viber's search, not data, and are left out.
    QStringList names, sqls, virt;
    if (q.exec(kTablesSql))
        while (q.next()) {
            names << q.value(0).toString();
            sqls << q.value(1).toString();
            if (sqls.last().startsWith("CREATE VIRTUAL", Qt::CaseInsensitive)) virt << names.last();
        }
    bool ok = !names.isEmpty() && q.exec("BEGIN");
    QString err = ok ? QString() : (names.isEmpty() ? "no-tables" : q.lastError().text());
    for (int i = 0; ok && i < names.size(); ++i) {
        const QString &n = names[i];
        bool shadow = false;
        for (const QString &v : virt) shadow = shadow || n == v || n.startsWith(v + "_");
        if (shadow) continue;
        QString quoted = "\"" + QString(n).replace("\"", "\"\"") + "\"";
        // "CREATE TABLE <name> (…)" into the plain copy: the name, however it was written, replaced
        int open = sqls[i].indexOf('(');
        QString made = open < 0 ? QString() : "CREATE TABLE plain." + quoted + " " + sqls[i].mid(open);
        if (made.isEmpty() || !q.exec(made))
            if (!q.exec("CREATE TABLE plain." + quoted + " AS SELECT * FROM main." + quoted + " WHERE 0")) {
                ok = false;
                err = q.lastError().text();
                break;
            }
        if (!q.exec("INSERT INTO plain." + quoted + " SELECT * FROM main." + quoted)) {
            ok = false;
            err = q.lastError().text();
        }
    }
    q.exec(ok ? "COMMIT" : "ROLLBACK");
    q.exec("DETACH DATABASE plain");
    if (!ok) {
        QFile::remove(tmp);
        return "error " + esc(err) + "\n";
    }
    if (std::rename(tmp.toUtf8().constData(), path.toUtf8().constData()) != 0) {
        QFile::remove(tmp);
        return "error cannot-rename\n";
    }
    return "ok\n";
}

static QString actReact(qlonglong target, const QString &code) {
    QList<QObject *> ms = objectsOf("MessageActions");
    if (ms.isEmpty()) return "error no-messageactions\n";
    QObject *ma = ms.first();
    if (code == "like") {
        int li = methodByName(ma, "like", 2);
        if (li < 0) return "error no-like\n";
        bool on = true;
        void *lv[] = {nullptr, &target, &on};
        QMetaObject::metacall(ma, QMetaObject::InvokeMetaMethod, li, lv);
        return "ok\n";
    }
    // a number is one of Viber's quick reactions; anything else an emoji (Viber takes any)
    bool isNum;
    int qtype = code.toInt(&isNum);
    if (isNum && (qtype < 1 || qtype > 9)) return "error bad-code\n";
    QList<QObject *> fs = objectsOf("ReactionsFeature");
    if (fs.isEmpty()) return "error no-reactionsfeature\n";
    QMetaType rt = QMetaType::fromName("Reaction");
    if (!rt.isValid()) return "error no-reaction-type\n";
    void *reaction = rt.create();
    void *prev = rt.create();
    int gi = methodByName(fs.first(), isNum ? "getReactionFromQuickType" : "getReactionFromEmojiCode", 1);
    int ri = methodByName(ma, "react", 3);
    QString res = "ok\n";
    QString emoji = code;
    if (gi < 0) res = "error no-getReaction\n";
    else if (ri < 0) res = "error no-react\n";
    else {
        void *gv[] = {reaction, isNum ? static_cast<void *>(&qtype) : static_cast<void *>(&emoji)};
        QMetaObject::metacall(fs.first(), QMetaObject::InvokeMetaMethod, gi, gv);
        void *rv[] = {nullptr, &target, reaction, prev};
        QMetaObject::metacall(ma, QMetaObject::InvokeMetaMethod, ri, rv);
    }
    rt.destroy(reaction);
    rt.destroy(prev);
    return res;
}

static QString actUnreact(qlonglong target) {
    QList<QObject *> ms = objectsOf("MessageActions");
    if (ms.isEmpty()) return "error no-messageactions\n";
    int li = methodByName(ms.first(), "like", 2);
    if (li < 0) return "error no-like\n";
    bool on = false;
    void *lv[] = {nullptr, &target, &on};
    QMetaObject::metacall(ms.first(), QMetaObject::InvokeMetaMethod, li, lv);
    return "ok\n";
}

static QString actRead(qlonglong cid) {
    std::lock_guard<std::mutex> g(g_mu);
    for (QObject *o : *g_objs) {
        int idx = o->metaObject()->indexOfMethod("setRead(ChatID,bool)");
        if (idx < 0) continue;
        bool on = true;
        void *av[] = {nullptr, &cid, &on};
        QMetaObject::metacall(o, QMetaObject::InvokeMetaMethod, idx, av);
        return "ok\n";
    }
    return "error no-setRead\n";
}

// ---- self-check -------------------------------------------------------------------------------

// What each command needs of this Viber, looked up on the classes seen (nothing is called): after an
// update that renamed or removed something, the source says what no longer works, before an action
// fails. A chat's controller and input are known once a chat was open: else My Notes is opened (as
// compose opens a chat), which sends nothing.
static QString check() {
    rememberClasses();
    auto cls = [](const char *name) -> const QMetaObject * {
        auto it = g_classes.find(name);
        return it == g_classes.end() ? nullptr : it->second;
    };
    if (!cls("Chat") || !cls("InputBoxArea")) {
        QSqlDatabase db = viberDb();
        QSqlQuery q(db);
        if (db.isOpen() && q.exec("SELECT ChatID FROM ChatInfo WHERE Flags & 524288 LIMIT 1") && q.next()) {
            qlonglong notes = q.value(0).toLongLong();
            openChat(notes);
            QElapsedTimer t;
            t.start();
            do {
                QCoreApplication::processEvents(QEventLoop::AllEvents, 50);
                rememberClasses();
            } while ((!cls("Chat") || !cls("InputBoxArea")) && t.elapsed() < 2000);
        }
    }
    // what is missing of one capability, "" when nothing
    struct Need {
        QStringList missing;
        void cls(const QMetaObject *mo, const char *name) { if (!mo) missing << name; }
        void method(const QMetaObject *mo, const char *cls, const char *name, int params) {
            if (!mo) return;
            for (int i = 0; i < mo->methodCount(); ++i) {
                QMetaMethod m = mo->method(i);
                if (m.name() == name && m.parameterCount() == params) return;
            }
            missing << QString("%1::%2/%3").arg(cls, name).arg(params);
        }
        void signature(const QMetaObject *mo, const char *cls, std::initializer_list<const char *> sigs) {
            if (!mo) return;
            for (const char *s : sigs)
                if (mo->indexOfMethod(QMetaObject::normalizedSignature(s)) >= 0) return;
            missing << QString("%1::%2").arg(cls, *sigs.begin());
        }
    };
    QString out = "version " + esc(QCoreApplication::applicationVersion()) + "\n";
    auto say = [&](const char *name, const Need &n, const QString &note = QString()) {
        if (n.missing.isEmpty()) out += QString("ok ") + name + (note.isEmpty() ? "" : " " + note) + "\n";
        else out += QString("missing ") + name + " " + n.missing.join(',') + "\n";
    };

    Need read;
    QSqlDatabase db = viberDb();
    if (!db.isOpen()) read.missing << "viber.db";
    else {
        QSqlQuery q(db);
        q.prepare(kEventsSql);
        q.addBindValue(0);
        q.addBindValue(0);
        if (!q.exec()) read.missing << "events";
        if (!q.exec(QString(kChatsSql) + " LIMIT 0")) read.missing << "chats";
        if (!q.exec(QString(kTablesSql) + " LIMIT 0")) read.missing << "snapshot";
    }
    say("read", read);

    Need live;
    live.cls(cls("EventsStorage"), "EventsStorage");
    live.signature(cls("EventsStorage"), "EventsStorage", {"eventsAdded(QList<EventData>)"});
    say("live", live);

    Need send;
    int trays = objectsOf("TrayNotifier").size();
    if (trays != 1) send.missing << QString("TrayNotifier(%1)").arg(trays);
    send.signature(cls("TrayNotifier"), "TrayNotifier", {"sendMessage(ChatID,QString)"});
    say("send", send);

    Need file;
    file.cls(cls("MainWidget"), "MainWidget");
    file.signature(cls("MainWidget"), "MainWidget", {"sendFilesToChat(QList<QUrl>,ChatID)", "sendFilesToChat(QList<QUrl>,ChatID::type)"});
    say("file", file);

    Need compose;
    compose.cls(cls("MainWidget"), "MainWidget");
    compose.cls(cls("Chat"), "Chat");
    compose.cls(cls("InputBoxArea"), "InputBoxArea");
    compose.method(cls("MainWidget"), "MainWidget", "openChat", 2);
    compose.method(cls("Chat"), "Chat", "editMessage", 1);
    compose.method(cls("Chat"), "Chat", "onMessageReply", 2);
    if (cls("Chat") && cls("Chat")->indexOfProperty("chatID") < 0) compose.missing << "Chat.chatID";
    for (auto [name, n] : std::initializer_list<std::pair<const char *, int>>{
             {"forceActiveFocus", 0}, {"replaceTextWith", 1}, {"insertEmoticon", 1}, {"mentionSelected", 1}})
        compose.method(cls("InputBoxArea"), "InputBoxArea", name, n);
    Need accept;
    accept.method(cls("InputBoxArea"), "InputBoxArea", "accept", 0);
    say("compose", compose, accept.missing.isEmpty() || !cls("InputBoxArea") ? QString() : "fallback-return");

    Need react;
    react.cls(cls("MessageActions"), "MessageActions");
    react.cls(cls("ReactionsFeature"), "ReactionsFeature");
    react.method(cls("MessageActions"), "MessageActions", "like", 2);
    react.method(cls("MessageActions"), "MessageActions", "react", 3);
    react.method(cls("ReactionsFeature"), "ReactionsFeature", "getReactionFromQuickType", 1);
    react.method(cls("ReactionsFeature"), "ReactionsFeature", "getReactionFromEmojiCode", 1);
    if (!QMetaType::fromName("Reaction").isValid()) react.missing << "Reaction(type)";
    say("react", react);

    Need del;
    del.cls(cls("MessageActions"), "MessageActions");
    del.signature(cls("MessageActions"), "MessageActions", {"deleteMessageForAll(EventID::type)", "deleteMessageForAll(EventID)"});
    say("delete", del);

    Need receipts;
    bool any = false;
    for (auto &[name, mo] : g_classes) any = any || mo->indexOfMethod("setRead(ChatID,bool)") >= 0;
    if (!any) receipts.missing << "setRead(ChatID,bool)";
    say("read-receipts", receipts);
    return out;
}

static QString runCheck() {
    g_busy = true;
    QString out = check();
    g_busy = false;
    return out;
}

// Once, at the start: the check in the journal (viber-bridge.service), when Viber's objects are
// there (asked again every two seconds until then, by startupChecks).
static std::atomic<bool> g_startupChecked{false};

static void startupCheck() {
    if (g_startupChecked || g_busy || objectsOf("MainWidget").isEmpty() || objectsOf("EventsStorage").isEmpty())
        return;
    g_startupChecked = true;
    QString out = runCheck();
    fprintf(stderr, "viber-bridge check:\n%s", out.toUtf8().constData());
    fflush(stderr);
}

// ---- command dispatch (runs on the main thread) -----------------------------------------------

static QString runCommand(const QString &line, int fd, bool *keepOpen) {
    const QString cmd = line.section(' ', 0, 0);
    const QString rest = line.section(' ', 1);
    if (cmd == "ping") return "pong\n";
    if (cmd == "chats") {
        QSqlDatabase db = viberDb();
        if (!db.isOpen()) return "error not-open\n";
        QSqlQuery q(db);
        if (!q.exec(kChatsSql))
            return "error " + esc(q.lastError().text()) + "\n";
        QString out;
        while (q.next()) {
            QStringList r;
            for (int i = 0; i < 6; ++i) r << esc(q.value(i).toString());
            out += r.join('\t') + "\n";
        }
        return out;
    }
    if (cmd == "events") {
        qlonglong since = rest.section(' ', 0, 0).toLongLong();
        int limit = rest.section(' ', 1, 1).toInt();
        if (limit <= 0 || limit > 5000) limit = 1000;
        return rowsEvents(since, limit);
    }
    if (cmd == "message") {
        qlonglong id = rest.toLongLong();
        return rowsEvents(id - 1, 1);
    }
    if (cmd == "snapshot") return snapshot(rest);
    if (cmd == "check") return runCheck();
    if (cmd == "quit") {  // a clean end (the database closed), whatever signals the launcher left ignored
        QMetaObject::invokeMethod(QCoreApplication::instance(), "quit", Qt::QueuedConnection);
        return "ok\n";
    }
    if (cmd == "input") {
        QObject *t = focusedText();
        if (!t) return "error no-focus\n";
        QString edit = "-";
        for (QObject *e : objectsOf("EditMessageController")) edit = e->property("active").toBool() ? "on" : "off";
        QString plain = t->property("text").toString();
        if (QObject *d = t->property("textDocument").value<QObject *>())
            if (QObject *doc = d->property("textDocument").value<QObject *>()) Q_UNUSED(doc);
        return "edit=" + edit + "\t" + esc(plain) + "\n";
    }
    if (cmd == "subscribe") {
        armWatcher();
        std::lock_guard<std::mutex> g(g_subMu);
        g_subs.push_back(fd);
        *keepOpen = true;  // the fd now belongs to the subscriber list
        return "subscribed\n";
    }
    // --- actions ---
    if (cmd == "send" || cmd == "file" || cmd == "reply" || cmd == "react" || cmd == "unreact" || cmd == "read" ||
        cmd == "compose" || cmd == "delete") {
        if (!sendAllowed()) return "error send-disabled\n";
        if (cmd == "send") return actSend(rest.section(' ', 0, 0).toLongLong(), unesc(rest.section(' ', 1)));
        if (cmd == "file") return actFile(rest.section(' ', 0, 0).toLongLong(), rest.section(' ', 1));
        if (cmd == "reply") {
            QJsonObject o{{"chat", rest.section(' ', 0, 0).toLongLong()}, {"reply", rest.section(' ', 1, 1).toLongLong()},
                          {"parts", QJsonArray{QJsonObject{{"text", rest.section(' ', 2)}}}}};
            return actCompose(QString::fromUtf8(QJsonDocument(o).toJson(QJsonDocument::Compact)));
        }
        if (cmd == "compose") return actCompose(rest);
        if (cmd == "delete") return actDelete(rest.toLongLong());
        if (cmd == "react") return actReact(rest.section(' ', 0, 0).toLongLong(), rest.section(' ', 1, 1));
        if (cmd == "unreact") return actUnreact(rest.toLongLong());
        if (cmd == "read") return actRead(rest.toLongLong());
    }
    return "error unknown-command\n";
}

// ---- socket server ----------------------------------------------------------------------------

struct Command : QEvent {
    QString line;
    int fd;
    Command(QString l, int f) : QEvent(QEvent::User), line(std::move(l)), fd(f) {}
};

static const QEvent::Type StartupCheck = QEvent::Type(QEvent::User + 1);

struct Runner;
static Runner *g_runner = nullptr;

struct Runner : QObject {
    bool event(QEvent *e) override {
        if (e->type() == StartupCheck) {
            startupCheck();
            return true;
        }
        if (e->type() != QEvent::User) return QObject::event(e);
        auto *c = static_cast<Command *>(e);
        // inside a compose or a check (their Qt events run commands waiting): one that acts, or
        // copies the database, waits for it to end
        static const QStringList waits{"send", "file", "reply", "compose", "react", "unreact", "delete", "read",
                                       "snapshot", "check"};
        if (g_busy && waits.contains(c->line.section(' ', 0, 0))) {
            QCoreApplication::postEvent(g_runner, new Command(c->line, c->fd));
            return true;
        }
        bool keepOpen = false;
        QByteArray out = runCommand(c->line, c->fd, &keepOpen).toUtf8();
        writeAll(c->fd, out);
        if (!keepOpen) close(c->fd);
        return true;
    }
};

static std::string sockPath() {
    if (const char *p = getenv("VIBER_BRIDGE_SOCK")) return p;
    if (const char *r = getenv("XDG_RUNTIME_DIR")) return std::string(r) + "/viber-bridge.sock";
    return "/tmp/viber-bridge.sock";
}

static void serve() {
    const std::string path = sockPath();
    int s = socket(AF_UNIX, SOCK_STREAM, 0);
    sockaddr_un a{};
    a.sun_family = AF_UNIX;
    strncpy(a.sun_path, path.c_str(), sizeof a.sun_path - 1);
    unlink(path.c_str());
    mode_t old = umask(077);
    if (bind(s, reinterpret_cast<sockaddr *>(&a), sizeof a) != 0 || listen(s, 8) != 0) return;
    umask(old);
    for (;;) {
        int c = accept(s, nullptr, nullptr);
        if (c < 0) continue;
        std::string line;
        char ch;
        while (read(c, &ch, 1) == 1 && ch != '\n') line += ch;
        QCoreApplication::postEvent(g_runner, new Command(QString::fromStdString(line), c));
    }
}

static void startupChecks() {
    for (int i = 0; i < 120 && !g_startupChecked; ++i) {
        sleep(2);
        QCoreApplication::postEvent(g_runner, new QEvent(StartupCheck));
    }
}

// ---- hooks + init -----------------------------------------------------------------------------

static void onAdd(QObject *o) {
    if (g_prevAdd) g_prevAdd(o);
    if (g_inHook) return;
    g_inHook = true;
    QCoreApplication *app = QCoreApplication::instance();
    if (!g_runner && app && QThread::currentThread() == app->thread()) {
        g_runner = new Runner;  // re-enters the hook; g_inHook keeps the Runner out of the registry
        std::thread(serve).detach();
        std::thread(startupChecks).detach();
    }
    {
        std::lock_guard<std::mutex> g(g_mu);
        g_objs->insert(o);
    }
    g_inHook = false;
}

static void onRemove(QObject *o) {
    if (g_prevRemove) g_prevRemove(o);
    std::lock_guard<std::mutex> g(g_mu);
    g_objs->erase(o);
}

__attribute__((constructor)) static void init() {
    // Only hook the Viber process itself, not any child (QtWebEngine helpers, etc.).
    char exe[256] = {0};
    if (readlink("/proc/self/exe", exe, sizeof exe - 1) <= 0 || std::string(exe) != "/opt/viber/Viber")
        return;
    g_prevAdd = reinterpret_cast<Hook>(qtHookData[AddQObject]);
    g_prevRemove = reinterpret_cast<Hook>(qtHookData[RemoveQObject]);
    qtHookData[AddQObject] = reinterpret_cast<quintptr>(&onAdd);
    qtHookData[RemoveQObject] = reinterpret_cast<quintptr>(&onRemove);
}
