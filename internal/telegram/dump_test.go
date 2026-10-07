package telegram

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tdp"
	"github.com/gotd/td/tg"
)

// testdata/telethon-dumps.json: invented objects as Telethon sends them (their TL bytes) and as
// telegram_store.dump() writes them once read back (made with scripts of Telethon 1.45).
func TestDumpAsTelethon(t *testing.T) {
	raw, err := os.ReadFile("testdata/telethon-dumps.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ TL, JSON string }
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	ctors := tg.TypesConstructorMap()
	for i, c := range cases {
		data, _ := hex.DecodeString(c.TL)
		b := &bin.Buffer{Buf: data}
		id, err := b.PeekID()
		if err != nil {
			t.Fatal(err)
		}
		obj := ctors[id]()
		if err := obj.Decode(b); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if got := Dump(obj.(tdp.Object)); got != c.JSON {
			t.Errorf("%d: Dump\n got %s\nwant %s", i, got, c.JSON)
		}
		back, err := Load([]byte(c.JSON))
		if err != nil {
			t.Fatalf("%d: Load: %v", i, err)
		}
		if got := Dump(back); got != c.JSON {
			t.Errorf("%d: Load then Dump\n got %s\nwant %s", i, got, c.JSON)
		}
	}
}

func TestPyFloat(t *testing.T) {
	for f, want := range map[float64]string{
		5: "5.0", 37.98: "37.98", 23.7275: "23.7275", 1e-05: "1e-05", 0.0001: "0.0001", 1e16: "1e+16",
		1234567890123456: "1234567890123456.0", -0.5: "-0.5", 0: "0.0", 1.5e300: "1.5e+300", 123.456e-10: "1.23456e-08",
	} {
		if got := pyFloat(f); got != want {
			t.Errorf("pyFloat(%v) = %s, want %s", f, got, want)
		}
	}
}

func TestNames(t *testing.T) {
	for tl, want := range map[string]string{"messageMediaPhoto": "MessageMediaPhoto", "messages.dialogsSlice": "DialogsSlice",
		"inputPeerSelf": "InputPeerSelf"} {
		if got := telethonName(tl); got != want {
			t.Errorf("%s: %s", tl, got)
		}
	}
	u := &tg.User{ID: 7}
	u.SetSelf(true)
	if s := Dump(u); !json.Valid([]byte(s)) || !contains(s, `"is_self":true`) {
		t.Errorf("self: %s", s)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
