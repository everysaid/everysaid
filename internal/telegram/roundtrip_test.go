package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"

	"everysaid/internal/db"

	"github.com/gotd/td/tg"
)

// TestRoundTrip reads a telegram.db that Telethon wrote (read only), turns each stored object into
// gotd's by Load, writes it again by Dump and compares the two: structurally, and byte for byte.
// Only counts by class and the path of a difference are said, never a value. It runs only when
// EVERYSAID_TELEGRAM_DB names such a file.
func TestRoundTrip(t *testing.T) {
	path := os.Getenv("EVERYSAID_TELEGRAM_DB")
	if path == "" {
		t.Skip("EVERYSAID_TELEGRAM_DB not set")
	}
	d, err := db.ReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	type count struct{ equal, meaning, same, different, unread int }
	counts := map[string]*count{}
	diffs := map[string]int{}
	for _, table := range []string{"message", "chat", "entity"} {
		db.Each(d, "SELECT json FROM "+table, nil, func(scan func(...any)) {
			var js string
			scan(&js)
			var head struct {
				Class string `json:"_"`
			}
			json.Unmarshal([]byte(js), &head)
			key := table + " " + head.Class
			c := counts[key]
			if c == nil {
				c = &count{}
				counts[key] = c
			}
			orig := decode(js)
			norm, changed := telethonQuirks(orig)
			if changed {
				b, _ := json.Marshal(norm)
				js = string(b)
			}
			obj, err := Load([]byte(js))
			if err != nil {
				c.unread++
				diffs[key+": "+err.Error()]++
				return
			}
			back := Dump(obj)
			if back == js && !changed {
				c.same++
			}
			if where := differ(norm, decode(back), ""); where == "" && !changed {
				c.equal++
			} else if where == "" {
				c.meaning++
			} else {
				c.different++
				diffs[key+": "+where]++
			}
		})
	}
	keys := make([]string, 0, len(counts))
	total := count{}
	for k, c := range counts {
		keys = append(keys, k)
		total.equal += c.equal
		total.meaning += c.meaning
		total.same += c.same
		total.different += c.different
		total.unread += c.unread
	}
	sort.Strings(keys)
	for _, k := range keys {
		c := counts[k]
		t.Logf("%-32s equal %7d (byte for byte %7d)  equal in meaning %4d  different %4d  unreadable %4d", k, c.equal, c.same, c.meaning, c.different, c.unread)
	}
	t.Logf("%-32s equal %7d (byte for byte %7d)  equal in meaning %4d  different %4d  unreadable %4d", "all", total.equal, total.same, total.meaning, total.different, total.unread)
	for k, n := range diffs {
		t.Logf("%6d  %s", n, k)
	}
	if total.different+total.unread > 0 {
		t.Fail()
	}
}

// telethonQuirks undoes what only Telethon's own objects have, never a server's: a message it built
// itself (from a short update, or from what sending answered) leaves the flags it was not given as
// None (null), where any message read from Telegram says false; and a message it sent keeps the
// request's InputReplyToMessage as its reply_to. Both mean the same to the importer (a null flag is
// false, and only reply_to_msg_id, reply_to_peer_id and quote_text are read), and gotd's messages
// say false and MessageReplyHeader. It returns the value as gotd's would be, and whether it changed.
func telethonQuirks(v any) (any, bool) {
	changed := false
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			class, _ := x["_"].(string)
			if class == "InputReplyToMessage" {
				r := &tg.MessageReplyHeader{}
				if n, ok := x["reply_to_msg_id"].(json.Number); ok {
					id, _ := n.Int64()
					r.SetReplyToMsgID(int(id))
				}
				changed = true
				return decode(Dump(r))
			}
			flags := map[string]bool{}
			for _, t := range classes()[class] {
				for _, f := range meta(t).fields {
					if f.trueFlag {
						flags[f.key] = true
					}
				}
			}
			for k, e := range x {
				if e == nil && flags[k] {
					x[k] = false
					changed = true
				} else {
					x[k] = walk(e)
				}
			}
			return x
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		}
		return v
	}
	return walk(v), changed
}

func decode(s string) any {
	d := json.NewDecoder(bytes.NewReader([]byte(s)))
	d.UseNumber()
	var v any
	d.Decode(&v)
	return v
}

// differ is the path where two decoded JSON values first differ ("" when equal); only keys and
// classes are named in it.
func differ(a, b any, at string) string {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return at + " (an object, not)"
		}
		if c, _ := x["_"].(string); c != "" {
			at += "<" + c + ">"
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok {
				return at + "." + k + " (missing)"
			}
			if p := differ(v, w, at+"."+k); p != "" {
				return p
			}
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				return at + "." + k + " (extra)"
			}
		}
		return ""
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return at + " (list length)"
		}
		for i := range x {
			if p := differ(x[i], y[i], fmt.Sprintf("%s[%d]", at, i)); p != "" {
				return p
			}
		}
		return ""
	}
	if fmt.Sprintf("%T:%v", a, a) != fmt.Sprintf("%T:%v", b, b) {
		return at + " (value)"
	}
	return ""
}
