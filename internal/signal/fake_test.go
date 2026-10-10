package signal

// A fake client, answering the client's commands and saying its events without Signal, in this
// process. In the instance's folder (StoreDir) it keeps `linked` (once linked), and reads
// `script.jsonl` (the events a receive brings; a fetch again gives their files); it writes
// `sent.jsonl`, `acted.jsonl`, `read.jsonl` and `fetch.jsonl` (the sends, reactions, edits and
// deletions, marks read and fetches asked of it), for the tests to look at; `discover.json` is who
// has an account ({number: {aci, pni}}). `unlinked` there: Signal refuses the device (it was removed
// from the phone); `link-fails`: a link fails; `no-scan`: the phone never scans the code. It never
// touches the network.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"everysaid/internal/config"
	"everysaid/internal/plugins"
)

const (
	fakeOwn   = "11111111-1111-1111-1111-111111111111"
	fakePhone = "+306900000000"
)

// StoreDir is the fake's folder for an instance: <data>/signal/<instance>.
func StoreDir(c *plugins.Context) string { return fakeDir(c.ID) }

func fakeDir(instance int64) string {
	return filepath.Join(config.Data, "signal", fmt.Sprint(instance))
}

type fake struct {
	store, attachments string
	ev                 *eventQueue

	mu      sync.Mutex
	sent    int
	stopped chan struct{}
	once    sync.Once
}

func startFake(cfg clientConfig) (transport, error) {
	return &fake{store: fakeDir(cfg.Instance), attachments: cfg.Media, ev: newEventQueue(cfg.Handle), stopped: make(chan struct{})}, nil
}

// dropFake: the fake's device goes with its folder.
func dropFake(instance int64) error { return os.RemoveAll(fakeDir(instance)) }

func (f *fake) has(name string) bool {
	_, err := os.Stat(filepath.Join(f.store, name))
	return err == nil
}

func (f *fake) status() map[string]any {
	if !f.has("linked") {
		return map[string]any{"open": true, "linked": false, "receiving": false}
	}
	return map[string]any{"open": true, "linked": true, "receiving": false, "aci": fakeOwn,
		"pni": "PNI:44444444-4444-4444-4444-444444444444", "phone": fakePhone, "device_id": 2, "device_name": "Everysaid"}
}

func (f *fake) appendTo(name string, v any) {
	fh, _ := os.OpenFile(filepath.Join(f.store, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	b, _ := json.Marshal(v)
	fh.Write(append(b, '\n'))
	fh.Close()
}

// sayLine says an event as a script line has it.
func (f *fake) sayLine(line string) {
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil {
		return
	}
	name, _ := m["event"].(string)
	delete(m, "event")
	f.ev.say(name, m)
}

func (f *fake) Call(ctx context.Context, cmd string, args map[string]any) (json.RawMessage, error) {
	select {
	case <-f.stopped:
		return nil, ErrStopped
	default:
	}
	// the fields as JSON gives them (numbers as float64), as a client reading the protocol would
	var req map[string]any
	b, _ := json.Marshal(args)
	json.Unmarshal(b, &req)
	if req == nil {
		req = map[string]any{}
	}
	result, err := f.call(ctx, cmd, req)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (f *fake) call(ctx context.Context, cmd string, req map[string]any) (any, error) {
	needsLink := map[string]bool{"sync": true, "receive": true, "send": true, "mark_read": true, "history": true,
		"react": true, "edit": true, "delete": true, "discover": true}
	if needsLink[cmd] && !f.has("linked") {
		return nil, refuse("not_linked", "not linked to a Signal account")
	}
	switch cmd {
	case "open":
		os.MkdirAll(f.store, 0o700)
		os.MkdirAll(f.attachments, 0o700)
		return f.status(), nil
	case "status":
		return f.status(), nil
	case "link":
		if f.has("linked") {
			return nil, refuse("already_linked", "already linked")
		}
		f.ev.say("link_url", map[string]any{"url": "sgnl://linkdevice?uuid=fake&pub_key=fake"})
		if f.has("link-fails") {
			return nil, refuse("failed", "provisioning socket closed")
		}
		if f.has("no-scan") { // the phone never scans it
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-f.stopped:
				return nil, ErrStopped
			}
		}
		os.WriteFile(filepath.Join(f.store, "linked"), nil, 0o600)
		return f.status(), nil
	case "sync":
		f.appendTo("sync.jsonl", map[string]any{})
		return map[string]any{"requested": true}, nil
	case "receive":
		if f.has("unlinked") {
			f.ev.say("receive_ended", map[string]any{"error": "the websocket was refused: 403 opening websocket, we are logged out",
				"code": "unlinked"})
			return map[string]any{"started": true}, nil
		}
		if b, err := os.ReadFile(filepath.Join(f.store, "script.jsonl")); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				if strings.TrimSpace(line) != "" {
					f.sayLine(line)
				}
			}
		}
		f.ev.say("queue_empty", nil)
		return map[string]any{"started": true}, nil
	case "send":
		f.mu.Lock()
		f.sent++
		ts := 9_000_000 + f.sent
		f.mu.Unlock()
		ev := map[string]any{"chat": req["chat"], "sender": fakeOwn, "sender_device": 2, "outgoing": true, "ts": ts, "server_ts": ts}
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
		f.appendTo("sent.jsonl", req)
		f.ev.say("message", ev)
		return map[string]any{"ts": ts}, nil
	case "react", "edit", "delete":
		// said back as Signal's events of the owner, as the client does
		f.mu.Lock()
		f.sent++
		ts := 9_000_000 + f.sent
		f.mu.Unlock()
		ev := map[string]any{"chat": req["chat"], "sender": fakeOwn, "sender_device": 2, "outgoing": true, "ts": ts, "server_ts": ts}
		switch cmd {
		case "react":
			target := req["target"].(map[string]any)
			ev["emoji"], ev["remove"], ev["target_author"], ev["target_ts"] = req["emoji"], req["remove"] == true, target["author"], target["ts"]
		case "edit":
			ev["target_ts"], ev["text"] = req["target_ts"], req["text"]
		case "delete":
			ev["target_author"], ev["target_ts"] = fakeOwn, req["target_ts"]
		}
		f.appendTo("acted.jsonl", req)
		f.ev.say(map[string]string{"react": "reaction", "edit": "edit", "delete": "delete"}[cmd], ev)
		return map[string]any{"ts": ts}, nil
	case "mark_read":
		f.appendTo("read.jsonl", map[string]any{"messages": req["messages"], "receipts": req["receipts"]})
		n := 0
		if ms, has := req["messages"].([]any); has {
			n = len(ms)
		}
		return map[string]any{"marked": n}, nil
	case "fetch":
		f.appendTo("fetch.jsonl", req["messages"])
		b, _ := os.ReadFile(filepath.Join(f.store, "script.jsonl"))
		for _, x := range req["messages"].([]any) {
			r := x.(map[string]any)
			for _, line := range strings.Split(string(b), "\n") {
				var ev map[string]any
				if json.Unmarshal([]byte(line), &ev) != nil || ev["event"] != "message" || ev["sender"] != r["author"] || ev["ts"] != r["ts"] {
					continue
				}
				if !f.has("fetch-fails") {
					name := fmt.Sprintf("%.0f-%s-0.jpg", r["ts"], r["author"].(string)[:8])
					os.WriteFile(filepath.Join(f.attachments, name), []byte("fetched"), 0o600)
					ev["attachments"] = []any{map[string]any{"content_type": "image/jpeg", "file": name}}
				}
				delete(ev, "event")
				f.ev.say("message", ev)
			}
		}
		return map[string]any{"found": len(req["messages"].([]any))}, nil
	case "discover":
		var accounts map[string]map[string]any
		b, _ := os.ReadFile(filepath.Join(f.store, "discover.json"))
		json.Unmarshal(b, &accounts)
		found := []any{}
		for _, x := range req["numbers"].([]any) {
			if a, has := accounts[x.(string)]; has {
				f.ev.say("ids", map[string]any{"aci": a["aci"], "pni": a["pni"], "phone": x})
				found = append(found, map[string]any{"number": x, "aci": a["aci"], "pni": a["pni"]})
			}
		}
		return map[string]any{"found": found}, nil
	case "history":
		return map[string]any{"events": []any{}}, nil
	case "contacts", "groups":
		return map[string]any{cmd: []any{}}, nil
	}
	return nil, refuse("bad_request", "unknown command "+cmd)
}

func (f *fake) Settle()               { f.ev.Settle() }
func (f *fake) Done() <-chan struct{} { return f.stopped }

func (f *fake) Close() {
	f.once.Do(func() {
		f.ev.end()
		<-f.ev.done
		close(f.stopped)
	})
}

func (f *fake) Kill() { f.Close() }
