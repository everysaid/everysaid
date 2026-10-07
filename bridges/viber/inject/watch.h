// A real QObject slot (needs moc) so EventsStorage::eventsAdded can be connected live. The slot takes
// no argument: Qt allows connecting a signal to a shorter slot, so it matches
// eventsAdded(QList<EventData>) without naming Viber's EventData type. On each fire it calls
// viberOnEvents() (defined in viber-bridge.cpp), which reads the new rows and pushes them to any
// subscribers.
#pragma once
#include <QObject>

void viberOnEvents();

class EventWatcher : public QObject {
    Q_OBJECT
public slots:
    void onEvents() { viberOnEvents(); }
};
