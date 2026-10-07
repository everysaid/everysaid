# Build the resident Viber bridge library (viber-bridge.so).
#
# Built against the system Qt 6 headers but loaded into Viber's bundled Qt 6.10 at /opt/viber/lib —
# this works because the bridge only uses the stable meta-object / SQL / GUI APIs. If a future Viber
# ships a different Qt major, rebuild against matching headers.
#
#   make            # build viber-bridge.so
#   make clean

QT      ?= /usr/include/qt6
MOC     ?= /usr/lib/qt6/moc
VIBERLIB ?= /opt/viber/lib

CXXFLAGS = -shared -fPIC -O2 -std=c++17 -DQT_NO_VERSION_TAGGING \
           -I$(QT) -I$(QT)/QtCore -I$(QT)/QtSql -I$(QT)/QtGui -I.
LDFLAGS  = -L$(VIBERLIB) -Wl,-rpath,$(VIBERLIB) -lQt6Core -lQt6Sql -lQt6Gui -pthread

# Written beside and moved into place: a running Viber has the old file mapped, and writing into it
# would bring that Viber down.
viber-bridge.so: viber-bridge.cpp moc_watch.cpp watch.h
	g++ $(CXXFLAGS) -o $@.new viber-bridge.cpp moc_watch.cpp $(LDFLAGS)
	mv -f $@.new $@

moc_watch.cpp: watch.h
	$(MOC) watch.h -o moc_watch.cpp

clean:
	rm -f viber-bridge.so moc_watch.cpp

.PHONY: clean
