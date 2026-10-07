# Build the meta-object probe (dev tool; see probe.cpp).
QT       ?= /usr/include/qt6
VIBERLIB ?= /opt/viber/lib

probe.so: probe.cpp
	g++ -shared -fPIC -O1 -std=c++17 -DQT_NO_VERSION_TAGGING \
	    -I$(QT) -I$(QT)/QtCore -o $@ probe.cpp \
	    -L$(VIBERLIB) -Wl,-rpath,$(VIBERLIB) -lQt6Core -pthread

clean:
	rm -f probe.so

.PHONY: clean
