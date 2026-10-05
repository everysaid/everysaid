// LD_PRELOAD: once Viber Desktop has unlocked viber.db, copy it into a plain SQLite file,
// using Viber's own connection. Runs once, right after the key pragma, before Viber's next statement.
#include <QString>
#include <QStringList>
#include <QVariant>
#include <QtSql/QSqlQuery>
#include <QtSql/QSqlError>
#include <dlfcn.h>
#include <cstdio>
#include <cstdlib>
#include <sys/stat.h>

static FILE *logf() {
    static FILE *f = [] { umask(077); return fopen("/tmp/vb/export.log", "a"); }();
    return f;
}
static void say(const QString &s) {
    fprintf(logf(), "%s\n", s.toUtf8().constData());
    fflush(logf());
}

using ExecFn = bool (*)(void *, const QString &);
static ExecFn real_exec() {
    static auto f = reinterpret_cast<ExecFn>(dlsym(RTLD_NEXT, "_ZN9QSqlQuery4execERK7QString"));
    return f;
}

static bool run(QSqlQuery *q, const QString &sql) {
    bool ok = real_exec()(q, sql);
    say(QString("%1 -> %2 %3").arg(sql.left(120), ok ? "ok" : "FAIL", ok ? "" : q->lastError().text()));
    return ok;
}

static void do_export(QSqlQuery *q) {
    const char *out = getenv("VIBER_PLAIN_OUT");
    if (!out) return;
    QString path = QString::fromUtf8(out);
    if (!run(q, "ATTACH DATABASE '" + path + "' AS plain KEY ''")) return;
    if (!run(q, "SELECT sqlcipher_export('plain')")) {
        // No sqlcipher_export: copy table by table.
        QStringList tables;
        if (real_exec()(q, "SELECT name FROM main.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"))
            while (q->next()) tables << q->value(0).toString();
        say(QString("tables: %1").arg(tables.size()));
        for (const QString &t : tables)
            run(q, QString("CREATE TABLE plain.\"%1\" AS SELECT * FROM main.\"%1\"").arg(t));
        // Keep the original schema (with types and keys) for reference.
        run(q, "CREATE TABLE plain._schema AS SELECT type, name, tbl_name, sql FROM main.sqlite_master");
    }
    run(q, "DETACH DATABASE plain");
}

extern "C" bool _ZN9QSqlQuery4execERK7QString(void *self, const QString &sql) {
    static bool armed = false, done = false;
    if (!done && armed) {
        done = true;
        do_export(static_cast<QSqlQuery *>(self));
    }
    if (!done && sql.startsWith("PRAGMA hexkey", Qt::CaseInsensitive)) armed = true;
    return real_exec()(self, sql);
}
