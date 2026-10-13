// Meta's export ("Download your information", JSON) of Messenger or Instagram: every thread's
// messages, with their files, reactions and calls, into the archive (MetaWriter).
//
// The export is read as unpacked, one folder or a folder of its parts: each thread is a folder
// (`messages/inbox/<name>_<id>/`, also archived_threads, filtered_threads, message_requests,
// e2ee_cutover; under `your_facebook_activity/` or `your_instagram_activity/` in newer exports)
// with `message_1.json`, `message_2.json`... and the files the messages name by their path in the
// export (`uri`). The end-to-end encrypted chats Messenger gives from its own settings (camelCase
// JSON: participants as names, senderName, timestamp, text, media) are read too.
//
// Meta writes its JSON's text as UTF-8 bytes each escaped as a Latin-1 character (Î± for
// α): it is read back as the UTF-8 it was. Times are milliseconds.
//
// People are known only by their names; the owner is the one the options name, else the name of the
// export's profile, else the one in the most threads. A message's key in the source is its thread,
// its time and its place among the thread's messages of that same millisecond.
package importers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"everysaid/internal/archive"
	"everysaid/internal/config"
)

// MetaExportOptions: the export's folder, its service (messenger, instagram), the archive's source
// name ("": "<service>-export"), the owner's name as the export writes it ("": found).
type MetaExportOptions struct {
	Dir, Service, Source, Owner string
	// Instance is the plugin instance that reads it (0: none), for its source.
	Instance int64
}

// ErrMetaOwner: the export does not say who the owner is.
var ErrMetaOwner = errors.New("the export does not say whose it is: set the owner's name as the export writes it")

// metaText is a text of Meta's JSON as it was meant: UTF-8 whose bytes were written as Latin-1
// characters is read back as UTF-8; a text that is not so stays as it is.
func metaText(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xff {
			return s
		}
		b = append(b, byte(r))
	}
	if !utf8.Valid(b) {
		return s
	}
	return string(b)
}

type dyiFile struct {
	URI string `json:"uri"`
}

type dyiMessage struct {
	SenderName  string    `json:"sender_name"`
	SenderName2 string    `json:"senderName"`
	TimestampMs int64     `json:"timestamp_ms"`
	Timestamp   int64     `json:"timestamp"`
	Content     *string   `json:"content"`
	Text        *string   `json:"text"`
	Photos      []dyiFile `json:"photos"`
	Videos      []dyiFile `json:"videos"`
	AudioFiles  []dyiFile `json:"audio_files"`
	Files       []dyiFile `json:"files"`
	Gifs        []dyiFile `json:"gifs"`
	Media       []dyiFile `json:"media"`
	Sticker     *dyiFile  `json:"sticker"`
	Share       *struct {
		Link      string `json:"link"`
		ShareText string `json:"share_text"`
		Owner     string `json:"original_content_owner"` // Instagram: whose post it is
	} `json:"share"`
	Reactions []struct {
		Reaction string `json:"reaction"`
		Actor    string `json:"actor"`
	} `json:"reactions"`
	CallDuration *int64 `json:"call_duration"`
	Missed       bool   `json:"missed"`
	IsUnsent     bool   `json:"is_unsent"`
	IsUnsent2    bool   `json:"isUnsent"`
	Type         string `json:"type"`
	Users        []struct {
		Name string `json:"name"`
	} `json:"users"`
}

func (m *dyiMessage) sender() string {
	if m.SenderName != "" {
		return metaText(m.SenderName)
	}
	return metaText(m.SenderName2)
}

func (m *dyiMessage) ts() int64 {
	if m.TimestampMs != 0 {
		return m.TimestampMs
	}
	if m.Timestamp > 0 && m.Timestamp < 100_000_000_000 { // seconds
		return m.Timestamp * 1000
	}
	return m.Timestamp
}

func (m *dyiMessage) text() string {
	if m.Content != nil {
		return metaText(*m.Content)
	}
	if m.Text != nil {
		return metaText(*m.Text)
	}
	return ""
}

type dyiThread struct {
	Participants json.RawMessage `json:"participants"`
	Messages     []dyiMessage    `json:"messages"`
	Title        string          `json:"title"`
	ThreadName   string          `json:"threadName"`
	ThreadType   string          `json:"thread_type"`
	ThreadPath   string          `json:"thread_path"`
}

// names are the participants, as objects with a name or as names.
func (t *dyiThread) names() []string {
	var objs []struct {
		Name string `json:"name"`
	}
	var out []string
	if json.Unmarshal(t.Participants, &objs) == nil {
		for _, o := range objs {
			out = append(out, metaText(o.Name))
		}
		return out
	}
	var names []string
	json.Unmarshal(t.Participants, &names)
	for _, n := range names {
		out = append(out, metaText(n))
	}
	return out
}

// thread is one thread of the export, from all its files.
type thread struct {
	key, title string
	group      bool
	names      []string
	dir        string // where its files are
	messages   []dyiMessage
}

// isMessagesFile says whether a JSON file of the export may be a thread's (under messages/).
func isMessagesFile(path string) bool {
	slash := filepath.ToSlash(path)
	return strings.HasSuffix(slash, ".json") && strings.Contains(slash, "messages/")
}

// metaThreads reads the threads of an export, and the folders its files' paths may be relative to.
func metaThreads(dir string) (map[string]*thread, []string, error) {
	threads := map[string]*thread{}
	roots := map[string]bool{dir: true}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "messages", "your_facebook_activity", "your_instagram_activity":
				roots[filepath.Dir(p)] = true
			}
			return nil
		}
		if !isMessagesFile(p) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var t dyiThread
		if json.Unmarshal(b, &t) != nil || t.Participants == nil || t.Messages == nil {
			return nil // not a thread's file
		}
		key := filepath.Base(filepath.Dir(p))
		if t.ThreadPath != "" {
			key = filepath.Base(filepath.FromSlash(t.ThreadPath))
		} else if !strings.HasPrefix(filepath.Base(p), "message_") {
			key = strings.TrimSuffix(filepath.Base(p), ".json") // one file a thread (the encrypted chats)
		}
		th := threads[key]
		if th == nil {
			th = &thread{key: "dyi:" + key, dir: filepath.Dir(p)}
			threads[key] = th
		}
		names := t.names()
		if len(names) > len(th.names) {
			th.names = names
		}
		title := t.Title
		if title == "" {
			title = t.ThreadName
		}
		if title != "" {
			th.title = metaText(title)
		}
		th.group = th.group || t.ThreadType == "RegularGroup" || len(names) > 2
		th.messages = append(th.messages, t.Messages...)
		return nil
	})
	var rs []string
	for r := range roots {
		rs = append(rs, r)
	}
	sort.Strings(rs)
	return threads, rs, err
}

// metaOwner is the owner's name: of the export's profile, else the one in the most threads.
func metaOwner(dir string, threads map[string]*thread) string {
	var owner string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || owner != "" {
			return nil
		}
		switch d.Name() {
		case "profile_information.json": // Facebook
			var v struct {
				Profile struct {
					Name struct {
						FullName string `json:"full_name"`
					} `json:"name"`
				} `json:"profile_v2"`
			}
			if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &v) == nil {
				owner = metaText(v.Profile.Name.FullName)
			}
		case "personal_information.json": // Instagram
			var v struct {
				User []struct {
					Data map[string]struct {
						Value string `json:"value"`
					} `json:"string_map_data"`
				} `json:"profile_user"`
			}
			if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &v) == nil && len(v.User) > 0 {
				owner = metaText(v.User[0].Data["Name"].Value)
			}
		}
		return nil
	})
	if owner != "" {
		return owner
	}
	count := map[string]int{}
	for _, t := range threads {
		seen := map[string]bool{}
		for _, n := range t.names {
			if !seen[n] {
				seen[n] = true
				count[n]++
			}
		}
	}
	best, second := "", 0
	for n, c := range count {
		if c > count[best] || (c == count[best] && n < best) {
			if best != "" {
				second = max(second, count[best])
			}
			best = n
		} else {
			second = max(second, c)
		}
	}
	if best == "" || count[best] == second {
		return ""
	}
	return best
}

// file is a file the export names, where it is: its path from one of the export's folders, or from
// the thread's folder or one above it, else the file of that name in the thread's folder.
func (t *thread) file(roots []string, uri string) string {
	if uri == "" || strings.Contains(uri, "://") {
		return ""
	}
	rel := filepath.FromSlash(uri)
	for _, r := range roots {
		if p := filepath.Join(r, rel); isFile(p) {
			return p
		}
	}
	for d := t.dir; filepath.Dir(d) != d; d = filepath.Dir(d) {
		if p := filepath.Join(d, rel); isFile(p) {
			return p
		}
	}
	if p := filepath.Join(t.dir, filepath.Base(rel)); isFile(p) {
		return p
	}
	return ""
}

// MetaExport imports an export.
func MetaExport(a *archive.Archive, out func(string), o MetaExportOptions) (err error) {
	defer archive.Recover(&err)
	if o.Dir == "" || !exists(o.Dir) {
		say(out, "{service}: no export (Download your information) found", map[string]any{"service": o.Service})
		return nil
	}
	threads, roots, err := metaThreads(o.Dir)
	if err != nil {
		return err
	}
	if len(threads) == 0 {
		say(out, "{service}: no chats in the export {dir}", map[string]any{"service": o.Service, "dir": o.Dir})
		return nil
	}
	owner := o.Owner
	if owner == "" {
		owner = metaOwner(o.Dir, threads)
	}
	if owner == "" {
		return ErrMetaOwner
	}
	source := o.Source
	if source == "" {
		source = o.Service + "-export"
	}
	w := NewMetaWriter(a, o.Service, source, o.Dir, o.Dir)
	if o.Instance != 0 {
		a.Exec("UPDATE source SET instance_id = ? WHERE id = ? AND instance_id IS NULL", o.Instance, w.Source)
	}
	w.Store = NewStore(a)
	w.WeakPreviews = true
	self := MetaUser{Name: owner}
	w.Self(self)
	keys := make([]string, 0, len(threads))
	for k := range threads {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var added, calls int
	for _, k := range keys {
		t := threads[k]
		var members []MetaUser
		for _, n := range t.names {
			if n != owner && n != "" {
				members = append(members, MetaUser{Name: n})
			}
		}
		w.Conv(MetaThread{Key: t.key, Group: t.group, Title: t.title, Members: members})
		ms := t.messages
		sort.SliceStable(ms, func(i, j int) bool {
			if ms[i].ts() != ms[j].ts() {
				return ms[i].ts() < ms[j].ts()
			}
			if ms[i].sender() != ms[j].sender() {
				return ms[i].sender() < ms[j].sender()
			}
			return ms[i].text() < ms[j].text()
		})
		var lastTS int64 = -1
		n := 0
		for i := range ms {
			m := &ms[i]
			ts := m.ts()
			if ts == lastTS {
				n++
			} else {
				lastTS, n = ts, 0
			}
			rec := metaExportMessage(t, roots, m, owner)
			rec.Thread, rec.TS, rec.RowKey = t.key, ts, fmt.Sprintf("%s/%d/%d", t.key, ts, n)
			if _, isNew := w.Add(rec); isNew {
				added++
				if rec.Call != nil {
					calls++
				}
			}
		}
		a.Commit()
		w.Store.Release()
	}
	a.Imported(w.Source)
	a.Resolve()
	a.Commit()
	w.Store.Release()
	say(out, "{service}: {n} new messages ({calls} calls) from {threads} chats of the export", map[string]any{
		"service": o.Service, "n": added, "calls": calls, "threads": len(threads)})
	if w.Previews > 0 {
		a.Changed = true
		say(out, "changes to what was there: {list}", map[string]any{"list": fmt.Sprintf("preview %d", w.Previews)})
	}
	w.Store.report(a, out)
	return nil
}

// metaExportMessage is a message of the export as the writer takes it.
func metaExportMessage(t *thread, roots []string, m *dyiMessage, owner string) MetaMessage {
	sender := m.sender()
	rec := MetaMessage{Outgoing: sender == owner, Text: m.text(), Kind: "text",
		Deleted: m.IsUnsent || m.IsUnsent2}
	if !rec.Outgoing && sender != "" {
		rec.From = MetaUser{Name: sender}
	}
	add := func(kind string, files []dyiFile) {
		for _, f := range files {
			if p := t.file(roots, f.URI); p != "" {
				rec.Files = append(rec.Files, MetaFile{Path: p, Rel: filepath.ToSlash(f.URI)})
			} else {
				rec.Files = append(rec.Files, MetaFile{Path: filepath.Join(t.dir, filepath.FromSlash(f.URI)),
					Rel: filepath.ToSlash(f.URI)}) // not in the export: counted as without a file
			}
		}
		if len(files) > 0 && rec.Kind == "text" {
			rec.Kind = kind
		}
	}
	add("image", m.Photos)
	add("video", m.Videos)
	add("voice", m.AudioFiles)
	add("file", m.Files)
	if len(m.Gifs) > 0 {
		add("image", m.Gifs)
		rec.Subtype = "gif"
	}
	for _, f := range m.Media { // the encrypted chats' files, of whatever kind
		add(kindOfName(f.URI), []dyiFile{f})
	}
	if m.Sticker != nil {
		add("sticker", []dyiFile{*m.Sticker})
	}
	if m.Share != nil && m.Share.Link != "" {
		rec.Subtype = "link"
		if !strings.Contains(rec.Text, m.Share.Link) {
			rec.Text = strings.TrimSpace(rec.Text + "\n" + m.Share.Link)
		}
		rec.Preview = shareText(m.Share.Link, metaText(m.Share.ShareText), metaText(m.Share.Owner), rec.Text)
	}
	for _, r := range m.Reactions {
		actor := metaText(r.Actor)
		rec.Reactions = append(rec.Reactions, MetaReaction{Emoji: metaText(r.Reaction), By: MetaUser{Name: actor},
			Mine: actor == owner})
	}
	switch {
	case m.CallDuration != nil || m.Type == "Call":
		rec.Kind, rec.Subtype, rec.Code = "call", "call", "meta:Call"
		var d int64
		if m.CallDuration != nil {
			d = *m.CallDuration
		}
		rec.Call = &MetaCall{Duration: d, Missed: m.Missed}
	case m.Type == "Subscribe" || m.Type == "Unsubscribe":
		rec.Kind, rec.Subtype, rec.Code = "system", "group event", "meta:"+m.Type
	}
	return rec
}

// shareText is the preview of a shared link from what the export says of it (its text, whose post
// it is), nil where it says nothing more than the link or the message's text does.
func shareText(link, text, owner, msg string) *archive.Preview {
	text, owner = strings.TrimSpace(text), strings.TrimSpace(owner)
	if text == link || strings.Contains(msg, text) {
		text = ""
	}
	switch {
	case owner != "":
		return &archive.Preview{URL: link, Title: owner, Description: text}
	case text != "":
		return &archive.Preview{URL: link, Title: text}
	}
	return nil
}

// kindOfName is the kind of a message by its file's name.
func kindOfName(name string) string {
	t := guessType(name)
	switch {
	case strings.HasPrefix(t, "image/"):
		return "image"
	case strings.HasPrefix(t, "video/"):
		return "video"
	case strings.HasPrefix(t, "audio/"):
		return "voice"
	}
	return "file"
}

// metaExportImporter is the importer of a service's export as config.toml names it ([<service>]
// export, owner).
func metaExportImporter(service string) func(a *archive.Archive, out func(string)) error {
	return func(a *archive.Archive, out func(string)) error {
		dir := config.Path(service, "export", "")
		if dir == "" {
			say(out, "{service}: no export set ([{service}] export in config.toml)", map[string]any{"service": service})
			return nil
		}
		return MetaExport(a, out, MetaExportOptions{Dir: dir, Service: service, Owner: config.String(service, "owner", "")})
	}
}
