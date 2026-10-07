// Media on demand: finding a chat's files, a file itself, storing one in the photo library. The
// library part ports everysaid/server/host.py (to_library, fetch) for when no server lends its own.
package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/plugins"
)

// findMedia is a chat's files, newest first, each with whether it is here and in which library.
func findMedia(s *core.Store, chat, kind string, since, until *int64, limit int) (any, error) {
	var before int64
	if until != nil {
		before = *until
	}
	r, err := core.Media(s, core.MediaOptions{ChatID: chat, Kind: kind, Before: before, Limit: limit * 2})
	if err != nil {
		return nil, err
	}
	out := []core.M{}
	for _, m := range r["items"].([]core.M) {
		if since != nil && m["ts"].(int64) < *since {
			break
		}
		var library any
		var label string
		if db.Row(s.Read(), "SELECT library FROM library_link WHERE sha256 = ?", []any{m["sha256"]}, &label) {
			library = label
		}
		out = append(out, core.M{"sha256": m["sha256"], "time": when(m["ts"]), "mime": m["mime"], "size": m["size"],
			"file_here": m["available"] == "local", "in_library": library, "message_id": m["message_id"]})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// inlineMost is the largest file download_media also gives itself (over HTTP).
const inlineMost = 8 << 20

type file struct {
	sha256, mime string
	size         int64
	path         string // relative to the media root
}

// download is a message's files, or one file: where each is on this computer.
func download(ctx context.Context, env Env, messageID int64, sha string) (any, error) {
	s := env.Store
	q := s.Read()
	var files []file
	scan := func(sc func(...any)) {
		var f file
		var mt sql.NullString
		sc(&f.sha256, &mt, &f.size, &f.path)
		f.mime = mt.String
		files = append(files, f)
	}
	switch {
	case sha != "":
		db.Each(q, "SELECT sha256, mime, size, path FROM media WHERE sha256 = ?", []any{sha}, scan)
		if len(files) == 0 {
			return core.M{"error": "no such file"}, nil
		}
		if messageID == 0 { // a message that had it: for asking its service
			messageID, _ = db.IntOK(q, "SELECT message_id FROM attachment WHERE sha256 = ? ORDER BY message_id DESC LIMIT 1", sha)
		}
	case messageID != 0:
		if !db.Exists(q, "SELECT 1 FROM message WHERE id = ?", messageID) {
			return core.M{"error": "no such message"}, nil
		}
		db.Each(q, "SELECT DISTINCT md.sha256, md.mime, md.size, md.path FROM attachment a JOIN media md ON md.sha256 = a.sha256 "+
			"WHERE a.message_id = ? ORDER BY a.id", []any{messageID}, scan)
	default:
		return nil, errors.New("give message_id or sha256")
	}
	out := []core.M{}
	var extra []sdk.Content
	give := func(m core.M, p, mt string) {
		if !env.Inline || p == "" {
			return
		}
		if st, err := os.Stat(p); err != nil || st.Size() > inlineMost {
			m["inline"] = false
			return
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return
		}
		m["inline"] = true
		switch {
		case len(mt) > 6 && mt[:6] == "image/":
			extra = append(extra, &sdk.ImageContent{Data: data, MIMEType: mt})
		case len(mt) > 6 && mt[:6] == "audio/":
			extra = append(extra, &sdk.AudioContent{Data: data, MIMEType: mt})
		default:
			extra = append(extra, &sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
				URI: "everysaid:media/" + fmt.Sprint(m["sha256"]), MIMEType: mt, Blob: data}})
		}
	}
	fetched := false
	for _, f := range files {
		m := core.M{"sha256": f.sha256, "mime": nullable(f.mime), "size": f.size, "path": nil, "from": nil}
		if p := filepath.Join(archive.MediaRoot(), f.path); exists(p) {
			m["path"], m["from"] = p, "archive"
		} else if p := fromLibrary(s, f.sha256, f.mime); p != "" {
			m["path"], m["from"] = p, "library"
		} else if p, err := fetch(ctx, env, messageID); p != "" {
			m["path"], m["from"] = p, "service"
			fetched = true
		} else {
			m["error"] = err
		}
		if p, ok := m["path"].(string); ok {
			give(m, p, f.mime)
		}
		out = append(out, m)
	}
	if len(files) == 0 && !fetched { // a file the archive never had: its service may still give it
		p, err := fetch(ctx, env, messageID)
		if p == "" {
			return core.M{"error": err}, nil
		}
		mt := mime.TypeByExtension(filepath.Ext(p))
		var size any
		if st, err := os.Stat(p); err == nil {
			size = st.Size()
		}
		m := core.M{"sha256": nil, "mime": nullable(mt), "size": size, "path": p, "from": "service"}
		give(m, p, mt)
		out = append(out, m)
	}
	return withContent{core.M{"message_id": nullID(messageID), "files": out}, extra}, nil
}

// fetch asks the source at work for a message's file: its path, or why not.
func fetch(ctx context.Context, env Env, messageID int64) (string, string) {
	if env.Fetcher == nil || messageID == 0 {
		return "", "the archive no longer holds this file"
	}
	p, err := env.Fetcher.FetchMedia(ctx, env.Store, messageID)
	if err != nil || p == "" {
		if err == nil || errors.Is(err, ErrNoFetch) {
			return "", "the archive no longer holds this file, and no source at work can bring it"
		}
		return "", "the service could not give the file: " + err.Error()
	}
	return p, ""
}

// fromLibrary is a file from the library that holds it (its original), as a path on this computer
// (bytes a library sends go into the cache); "" when none can.
func fromLibrary(s *core.Store, sha, mt string) string {
	var out string
	type link struct {
		iid int64
		ref string
	}
	var links []link
	db.Each(s.Read(), "SELECT instance_id, asset_id FROM library_link WHERE sha256 = ? AND instance_id IS NOT NULL",
		[]any{sha}, func(scan func(...any)) {
			var l link
			scan(&l.iid, &l.ref)
			links = append(links, l)
		})
	for _, l := range links {
		row := plugins.GetInstance(s, l.iid)
		if row == nil || !row.Enabled {
			continue
		}
		lib, ok := plugins.Get(row.Plugin).(plugins.Library)
		if !ok {
			continue
		}
		got, err := lib.Fetch(plugins.NewContext(host{s}, *row), l.ref, "original")
		if err != nil || got == nil {
			continue
		}
		if got.Path != "" && exists(got.Path) {
			return got.Path
		}
		if len(got.Data) > 0 {
			ext := ""
			if exts, _ := mime.ExtensionsByType(firstOf(got.Type, mt)); len(exts) > 0 {
				ext = exts[0]
			}
			dir := filepath.Join(config.Cache, "mcp")
			if os.MkdirAll(dir, 0o700) != nil {
				continue
			}
			p := filepath.Join(dir, sha+ext)
			if os.WriteFile(p, got.Data, 0o600) == nil {
				return p
			}
		}
	}
	return out
}

// localLibrary stores files in the default library through the library plugins (host.to_library).
type localLibrary struct{ s *core.Store }

func (l localLibrary) ToLibrary(sha string, dateMs *int64) (core.M, error) {
	s := l.s
	var row *plugins.Instance
	for _, r := range plugins.Instances(s, "library") {
		if !r.Enabled {
			continue
		}
		if row == nil || (r.IsDefault && !row.IsDefault) {
			r := r
			row = &r
		}
	}
	if row == nil {
		return nil, errs.New("library.none", 409, nil)
	}
	var path string
	if rel := db.Str(s.Read(), "SELECT path FROM media WHERE sha256 = ?", sha); rel != "" {
		if p := filepath.Join(archive.MediaRoot(), rel); exists(p) {
			path = p
		}
	}
	var known string
	if db.Row(s.Read(), "SELECT asset_id FROM library_link WHERE sha256 = ? AND instance_id = ?", []any{sha, row.ID}, &known) {
		// stored there before (the stored copy may differ: a date or a make written in)
		return core.M{"already": true, "ref": known, "library": row.Label}, nil
	}
	if path == "" {
		return nil, errs.New("file_gone", 409, nil)
	}
	lib, ok := plugins.Get(row.Plugin).(plugins.Library)
	if !ok {
		return nil, errs.New("library.none", 409, nil)
	}
	ctx := plugins.NewContext(host{s}, *row)
	found, err := lib.Find(ctx, sha, path)
	if err != nil {
		return nil, err
	}
	if found != "" {
		link(s, row.ID, row.Label, sha, found, "checksum")
		return core.M{"already": true, "ref": found, "library": row.Label}, nil
	}
	var mt, service sql.NullString
	var msgTS, decided sql.NullInt64
	db.Row(s.Read(), "SELECT md.mime, min(m.ts), s.name FROM media md JOIN attachment a ON a.sha256 = md.sha256 "+
		"JOIN message m ON m.id = a.message_id JOIN service s ON s.id = m.service_id WHERE md.sha256 = ?", []any{sha},
		&mt, &msgTS, &service)
	db.Row(s.Read(), "SELECT date_ms FROM media_decision WHERE sha256 = ?", []any{sha}, &decided)
	var date any
	switch {
	case dateMs != nil && *dateMs != 0:
		date = *dateMs
	case decided.Valid && decided.Int64 != 0:
		date = decided.Int64
	case msgTS.Valid:
		date = msgTS.Int64
	}
	ref, err := lib.Store(ctx, path, core.M{"sha256": sha, "mime": nullable(mt.String), "date_ms": date,
		"service": nullable(service.String)})
	if err != nil {
		return nil, err
	}
	link(s, row.ID, row.Label, sha, ref, "upload")
	return core.M{"already": false, "ref": ref, "library": row.Label}, nil
}

// link records where a library keeps a file (libraries.link).
func link(s *core.Store, iid int64, label, sha, ref, method string) {
	s.MustWrite(func(tx *sql.Tx) {
		db.Exec(tx, "INSERT INTO library_link (sha256, library, asset_id, method, linked_at, instance_id) "+
			"VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (sha256, library) DO UPDATE SET asset_id = excluded.asset_id, "+
			"method = excluded.method, instance_id = excluded.instance_id", sha, label, ref, method, time.Now().Unix(), iid)
	})
}

// host is what a library plugin reaches of its host here: the archive; no interface to tell.
type host struct{ s *core.Store }

func (h host) Store() *core.Store   { return h.s }
func (h host) Emit(core.M)          {}
func (h host) Alert(string, string) {}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
