package importers

import (
	"reflect"
	"testing"
	"time"
)

// The Python semantics the importers depend on, against what Python 3.14 gives.
func TestPythonSemantics(t *testing.T) {
	saved := time.Local
	time.Local = time.UTC // a naive time is the system's: here UTC
	defer func() { time.Local = saved }()
	for s, want := range map[string]int64{
		"2026-10-06 10:01:00+03:00":           1791270060000,
		"2026-10-06 10:01:00.123456789+03:00": 1791270060123,
		"2026-10-06T10:01:00Z":                1791280860000,
		"20261006T100100":                     1791280860000,
		"2026-10-06":                          1791244800000,
		"2026-10-06 10:01":                    1791280860000,
		"2026-10-06 10:01:00.5-02:30":         1791289860500,
		"2026-10-06 24:00:00":                 1791331200000,
	} {
		got, ok := fromISO(s)
		if !ok || tsMS(got) != want {
			t.Errorf("fromISO(%q) = %d %v, want %d", s, tsMS(got), ok, want)
		}
	}
	if _, ok := fromISO("2026-13-01"); ok {
		t.Error("month 13 read")
	}
	for in, want := range map[string]string{"ok": "ok", "a\xffb": "a�b", "\xe2\x82x": "�x", "\xf0\x9f\x98": "�",
		"\xed\xa0\x80z": "���z", "\xc0\xaf": "��"} {
		if got := decodeReplace([]byte(in)); got != want {
			t.Errorf("decodeReplace(%q) = %q, want %q", in, got, want)
		}
	}
	for f, want := range map[float64]string{1.0: "1.0", 0.1: "0.1", 1e16: "1e+16", 1.5e-5: "1.5e-05", 123456.789: "123456.789",
		1 << 60: "1.152921504606847e+18"} {
		if got := pyFloat(f); got != want {
			t.Errorf("pyFloat(%v) = %q, want %q", f, got, want)
		}
	}
	if got := splitLines("a\r\nb\rc\u0085d e\n"); !reflect.DeepEqual(got, []string{"a", "b", "c", "d", "e"}) {
		t.Errorf("splitLines: %q", got)
	}
	if got := upper("ßtel ᾳ"); got != "SSTEL ΑΙ" {
		t.Errorf("upper: %q", got)
	}
	if b, e := splitext("/a/b.c/.hidden"); b != "/a/b.c/.hidden" || e != "" {
		t.Errorf("splitext: %q %q", b, e)
	}
	if got := guessType("/x/y.JPG"); got != "image/jpeg" {
		t.Errorf("guessType: %q", got)
	}
}

// A protobuf message's fields and readable strings, as whatsapp.py reads them.
func TestProtobuf(t *testing.T) {
	// field 1: varint 150; field 2: "hi there"; field 3: nested {1: "inner text"}; field 4: 0xff bytes
	msg := []byte{0x08, 0x96, 0x01, 0x12, 0x08, 'h', 'i', ' ', 't', 'h', 'e', 'r', 'e', 0x1a, 0x0c, 0x0a, 0x0a,
		'i', 'n', 'n', 'e', 'r', ' ', 't', 'e', 'x', 't', 0x22, 0x01, 0xff}
	f := protobufFields(msg)
	if f[1][0] != uint64(150) || string(f[2][0].([]byte)) != "hi there" || len(f[3]) != 1 {
		t.Fatalf("fields %v", f)
	}
	s, ok := protobufStrings(msg, 0)
	if !ok || !reflect.DeepEqual(s, []string{"hi there", "inner text"}) {
		t.Fatalf("strings %q %v", s, ok)
	}
	if got := metadataTextOf(msg); got != "hi there\ninner text" {
		t.Fatalf("metadata text %q", got)
	}
	if _, ok := protobufStrings([]byte{0x0f}, 0); ok { // wire type 7: not a message
		t.Fatal("read a bad message")
	}
}
