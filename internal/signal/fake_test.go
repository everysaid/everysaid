package signal

// A fake helper, speaking the helper's protocol without Signal: the test binary itself, started with
// EVERYSAID_FAKE_SIGNAL=1. In the store folder it is given it keeps `linked` (once linked), and reads
// `script.jsonl` (the events a receive brings); it writes `sent.jsonl` and `read.jsonl` (the sends
// and read receipts asked of it), for the tests to look at. It never touches the network.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	fakeOwn   = "11111111-1111-1111-1111-111111111111"
	fakePhone = "+306900000000"
)

func fakeHelper() {
	var mu sync.Mutex
	out := bufio.NewWriter(os.Stdout)
	write := func(v any) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	store := ""
	linked := func() bool {
		_, err := os.Stat(filepath.Join(store, "linked"))
		return store != "" && err == nil
	}
	status := func() map[string]any {
		if !linked() {
			return map[string]any{"open": store != "", "linked": false, "receiving": false}
		}
		return map[string]any{"open": true, "linked": true, "receiving": false, "aci": fakeOwn,
			"pni": "PNI:44444444-4444-4444-4444-444444444444", "phone": fakePhone, "device_id": 2, "device_name": "Everysaid"}
	}
	appendTo := func(name string, v any) {
		f, _ := os.OpenFile(filepath.Join(store, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		b, _ := json.Marshal(v)
		f.Write(append(b, '\n'))
		f.Close()
	}
	sent := 0
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 16<<20)
	for in.Scan() {
		var req map[string]any
		if json.Unmarshal(in.Bytes(), &req) != nil {
			write(map[string]any{"id": nil, "ok": false, "code": "bad_request", "error": "not JSON"})
			continue
		}
		id := req["id"]
		ok := func(r any) { write(map[string]any{"id": id, "ok": true, "result": r}) }
		fail := func(code, msg string) { write(map[string]any{"id": id, "ok": false, "code": code, "error": msg}) }
		cmd, _ := req["cmd"].(string)
		if cmd != "open" && cmd != "quit" && cmd != "status" && store == "" {
			fail("not_open", "the store is not open")
			continue
		}
		needsLink := map[string]bool{"sync": true, "receive": true, "send": true, "mark_read": true, "history": true}
		if needsLink[cmd] && !linked() {
			fail("not_linked", "not linked to a Signal account")
			continue
		}
		switch cmd {
		case "open":
			store, _ = req["store"].(string)
			if p, _ := req["passphrase"].(string); len(p) < 32 {
				fail("locked", "no passphrase")
				store = ""
				continue
			}
			os.MkdirAll(store, 0o700)
			os.MkdirAll(req["attachments"].(string), 0o700)
			ok(status())
		case "status":
			ok(status())
		case "link":
			if linked() {
				fail("already_linked", "already linked")
				continue
			}
			write(map[string]any{"event": "link_url", "url": "sgnl://linkdevice?uuid=fake&pub_key=fake"})
			if _, err := os.Stat(filepath.Join(store, "no-scan")); err == nil {
				continue // the phone never scans it: no answer
			}
			os.WriteFile(filepath.Join(store, "linked"), nil, 0o600)
			ok(status())
		case "sync":
			appendTo("sync.jsonl", map[string]any{})
			ok(map[string]any{"requested": true})
		case "receive":
			ok(map[string]any{"started": true})
			if b, err := os.ReadFile(filepath.Join(store, "script.jsonl")); err == nil {
				for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
					if strings.TrimSpace(line) != "" {
						mu.Lock()
						out.WriteString(line + "\n")
						out.Flush()
						mu.Unlock()
					}
				}
			}
			write(map[string]any{"event": "queue_empty"})
		case "send":
			sent++
			ts := 9_000_000 + sent
			ev := map[string]any{"event": "message", "chat": req["chat"], "sender": fakeOwn, "sender_device": 2,
				"outgoing": true, "ts": ts, "server_ts": ts}
			for _, k := range []string{"text", "mentions", "quote"} {
				if v, has := req[k]; has {
					ev[k] = v
				}
			}
			if atts, has := req["attachments"].([]any); has {
				var list []any
				for _, a := range atts {
					m := a.(map[string]any)
					data, _ := os.ReadFile(m["path"].(string))
					m["bytes"] = string(data) // what it was given, while the file is there
					list = append(list, map[string]any{"content_type": m["content_type"], "filename": m["filename"], "file": nil})
				}
				ev["attachments"] = list
			}
			appendTo("sent.jsonl", req)
			write(ev)
			ok(map[string]any{"ts": ts})
		case "mark_read":
			appendTo("read.jsonl", req["messages"])
			n := 0
			if ms, has := req["messages"].([]any); has {
				n = len(ms)
			}
			ok(map[string]any{"marked": n})
		case "history":
			ok(map[string]any{"events": []any{}})
		case "contacts", "groups":
			ok(map[string]any{cmd: []any{}})
		case "quit":
			ok(map[string]any{})
			return
		default:
			fail("bad_request", "unknown command "+cmd)
		}
	}
}
