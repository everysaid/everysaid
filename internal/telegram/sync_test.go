package telegram

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	"everysaid/internal/db"
)

func freshCache(t *testing.T) {
	os.RemoveAll(Folder())
	t.Cleanup(func() { os.RemoveAll(Folder()) })
}

func en(out *bytes.Buffer) *printer { return &printer{out, "en"} }

func TestDialogsPages(t *testing.T) {
	f := many(230)
	ds, err := f.conn().dialogs(newTestCtx())
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 230 || ds[0].Name != "P0" || ds[229].Name != "P229" {
		t.Fatalf("%d dialogs", len(ds))
	}
	n := 0
	for _, c := range f.calls {
		if c == "getDialogs" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("%d pages", n)
	}
}

func TestHistoryAfter(t *testing.T) {
	f := account()
	c := f.conn()
	var got []int
	err := c.history(newTestCtx(), &tg.InputPeerUser{UserID: 2, AccessHash: 22}, 237, func(chunk []sent) error {
		for _, s := range chunk {
			got = append(got, s.Message.GetID())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 13 || got[0] != 238 || got[12] != 250 {
		t.Fatalf("%v", got)
	}
	got = nil
	c.history(newTestCtx(), &tg.InputPeerUser{UserID: 2, AccessHash: 22}, 0, func(chunk []sent) error {
		for _, s := range chunk {
			got = append(got, s.Message.GetID())
		}
		return nil
	})
	for i, id := range got {
		if id != i+1 {
			t.Fatalf("not oldest first: %v", got[:10])
		}
	}
	if len(got) != 250 {
		t.Fatalf("%d", len(got))
	}
}

func TestSyncRun(t *testing.T) {
	freshCache(t)
	f := account()
	var out bytes.Buffer
	if err := syncRun(newTestCtx(), f.conn(), en(&out)); err != nil {
		t.Fatal(err)
	}
	d, _ := db.ReadOnly(DBPath())
	defer d.Close()
	kinds := map[int64]string{}
	db.Each(d, "SELECT id, kind FROM chat", nil, func(scan func(...any)) {
		var id int64
		var k string
		scan(&id, &k)
		kinds[id] = k
	})
	if !reflect.DeepEqual(kinds, map[int64]string{2: "user", -10: "group", 1: "saved"}) {
		t.Fatalf("%v", kinds)
	}
	if n := db.Int(d, "SELECT count(*) FROM message WHERE chat_id = 2"); n != 250 {
		t.Fatalf("%d", n)
	}
	if n := db.Int(d, "SELECT archived FROM chat WHERE id = -10"); n != 1 {
		t.Fatal("archived")
	}
	// the group's sender is kept; the private chat's messages carry no sender of their own
	if ids := db.Ints(d, "SELECT id FROM entity ORDER BY id"); !reflect.DeepEqual(ids, []int64{1, 2}) {
		t.Fatalf("entities %v", ids)
	}
	var in, outbox int64
	db.Row(d, "SELECT inbox, outbox FROM chat_read WHERE chat_id = 2", nil, &in, &outbox)
	if in != 240 || outbox != 245 {
		t.Fatal("reads")
	}
	var js string
	db.Row(d, "SELECT json FROM message WHERE chat_id = 2 AND id = 2", nil, &js)
	var m map[string]any
	json.Unmarshal([]byte(js), &m)
	if m["_"] != "Message" || m["out"] != true || m["message"] != "m2" || m["entities"] == nil {
		t.Fatalf("%s", js)
	}
	if !strings.Contains(out.String(), "256 new messages in "+DBPath()) {
		t.Fatalf("%q", out.String())
	}
	// again: only what came after
	bob := f.chats[0]
	bob.messages = append(bob.messages, text(251, &tg.PeerUser{UserID: 2}, 0, 1700000000, "new", false))
	out.Reset()
	if err := syncRun(newTestCtx(), f.conn(), en(&out)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 new messages in") {
		t.Fatalf("%q", out.String())
	}
}

func TestSurvey(t *testing.T) {
	freshCache(t)
	f := account()
	var out bytes.Buffer
	if err := survey(newTestCtx(), f.conn(), en(&out)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(Folder(), "survey.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\r\n")
	if lines[0] != "kind\tid\ttitle\tarchived\tmessages\tphotos_videos\tvoice_round\tfirst\tlast" ||
		lines[1] != "user\t2\tBob\t0\t250\t0\t0\t2020-09-13\t2020-09-13" || !strings.HasPrefix(lines[2], "group\t-10\tFriends\t1\t5") {
		t.Fatalf("%q", lines[:3])
	}
	if !strings.Contains(out.String(), "user            1 chats       250 messages") {
		t.Fatalf("%q", out.String())
	}
}

func TestSyncMainFlags(t *testing.T) {
	freshCache(t)
	values := fakeSecrets(t, map[string]string{})
	var out bytes.Buffer
	if err := SyncMain([]string{"--survey"}, &out); err == nil || !strings.Contains(err.Error(), "--save-credentials") {
		t.Fatal(err)
	}
	if err := SyncMain([]string{"--bogus"}, &out); err == nil {
		t.Fatal("unknown flag")
	}
	// --chats takes negative ids, as argparse does; --dry-run needs no connection
	values[SecretAPIID] = "1"
	if err := SyncMain([]string{"--media", "--dry-run", "--chats", "-10", "2"}, &out); err != nil {
		t.Fatal(err)
	}
	old := stdin
	t.Cleanup(func() { stdin, input = old, nil })
	stdin, input = strings.NewReader("12345\nabcdef\n"), nil
	if err := SyncMain([]string{"--save-credentials"}, &out); err != nil {
		t.Fatal(err)
	}
	if values[SecretAPIID] != "12345" || values[SecretAPIHash] != "abcdef" {
		t.Fatal("not saved")
	}
	stdin, input = strings.NewReader("12a\nabcdef\n"), nil
	if err := SyncMain([]string{"--save-credentials"}, &out); err == nil {
		t.Fatal("api_id must be a number")
	}
}
