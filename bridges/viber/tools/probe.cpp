// Dev tool, not part of the bridge: dump Viber's meta-object graph so method/signal/property
// signatures can be re-checked after a Viber update (that is how every entry in viber-bridge.cpp was
// found). Reads nothing of the user's data and calls nothing on Viber's objects.
//
// Build:  make           (in this directory)
// Use:    PROBE_OUT=/tmp/viber-classes.txt LD_PRELOAD=./probe.so /opt/viber/Viber
//         then, once the UI has loaded:  touch /tmp/viber-probe-go
//         the dump is written to $PROBE_OUT (default /tmp/viber-classes.txt).
#include <QCoreApplication>
#include <QEvent>
#include <QMetaMethod>
#include <QMetaObject>
#include <QMetaProperty>
#include <QObject>
#include <QThread>
#include <atomic>
#include <cctype>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <mutex>
#include <set>
#include <string>
#include <sys/stat.h>
#include <thread>
#include <unistd.h>

extern "C" Q_DECL_IMPORT quintptr qtHookData[];
enum { AddQObject = 3, RemoveQObject = 4 };
using Hook = void (*)(QObject *);

static std::mutex mu;
static std::set<QObject *> *objs = new std::set<QObject *>;
static Hook prevAdd, prevRemove;
static thread_local bool inHook = false;

static const char *outPath() {
    const char *p = getenv("PROBE_OUT");
    return p ? p : "/tmp/viber-classes.txt";
}

static void dump() {
    umask(077);
    FILE *f = fopen(outPath(), "w");
    if (!f) return;
    std::set<std::string> seen;
    std::lock_guard<std::mutex> g(mu);
    fprintf(f, "objects: %zu\n", objs->size());
    for (QObject *o : *objs) {
        for (const QMetaObject *mo = o->metaObject(); mo; mo = mo->superClass()) {
            std::string cn = mo->className();
            if (cn.size() > 1 && cn[0] == 'Q' && std::isupper((unsigned char)cn[1])) break;
            if (!seen.insert(cn).second) continue;
            fprintf(f, "\nclass %s : %s\n", cn.c_str(),
                    mo->superClass() ? mo->superClass()->className() : "-");
            for (int i = mo->methodOffset(); i < mo->methodCount(); ++i) {
                QMetaMethod m = mo->method(i);
                const char *kind = m.methodType() == QMetaMethod::Signal ? "signal"
                                 : m.methodType() == QMetaMethod::Slot   ? "slot"
                                 : m.methodType() == QMetaMethod::Method ? "invokable" : "ctor";
                fprintf(f, "  %-9s %s %s\n", kind, m.typeName(), m.methodSignature().constData());
            }
            for (int i = mo->propertyOffset(); i < mo->propertyCount(); ++i) {
                QMetaProperty p = mo->property(i);
                fprintf(f, "  property  %s %s\n", p.typeName(), p.name());
            }
        }
    }
    fclose(f);
}

struct Runner : QObject {
    bool event(QEvent *e) override {
        if (e->type() == QEvent::User) { dump(); return true; }
        return QObject::event(e);
    }
};
static Runner *runner;

static void watch() {
    while (access("/tmp/viber-probe-go", F_OK) != 0) std::this_thread::sleep_for(std::chrono::seconds(1));
    QCoreApplication::postEvent(runner, new QEvent(QEvent::User));
}

static void onAdd(QObject *o) {
    if (prevAdd) prevAdd(o);
    if (inHook) return;
    inHook = true;
    QCoreApplication *app = QCoreApplication::instance();
    if (!runner && app && QThread::currentThread() == app->thread()) {
        runner = new Runner;
        std::thread(watch).detach();
    }
    {
        std::lock_guard<std::mutex> g(mu);
        objs->insert(o);
    }
    inHook = false;
}

static void onRemove(QObject *o) {
    if (prevRemove) prevRemove(o);
    std::lock_guard<std::mutex> g(mu);
    objs->erase(o);
}

__attribute__((constructor)) static void init() {
    char exe[256] = {0};
    if (readlink("/proc/self/exe", exe, sizeof exe - 1) <= 0 || std::string(exe) != "/opt/viber/Viber")
        return;
    prevAdd = reinterpret_cast<Hook>(qtHookData[AddQObject]);
    prevRemove = reinterpret_cast<Hook>(qtHookData[RemoveQObject]);
    qtHookData[AddQObject] = reinterpret_cast<quintptr>(&onAdd);
    qtHookData[RemoveQObject] = reinterpret_cast<quintptr>(&onRemove);
}
