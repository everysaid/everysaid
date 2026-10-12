package meta

// What a table (or an encrypted chat's event) says, written into the archive in one import.

import (
	"fmt"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"everysaid/internal/archive"
	"everysaid/internal/importers"
	"everysaid/internal/keys"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

// sourceName is the archive's source of an instance.
func sourceName(service string, iid int64) string { return fmt.Sprintf("%s/%d", service, iid) }

// writer is the instance's writer into an archive, its source tied to the instance.
func writer(a *archive.Archive, c *plugins.Context, service string) *importers.MetaWriter {
	w := importers.NewMetaWriter(a, service, sourceName(service, c.ID), keys.Path(), mediaDir(c.ID))
	a.Exec("UPDATE source SET instance_id = ? WHERE id = ? AND instance_id IS NULL", c.ID, w.Source)
	return w
}

// applied is what writing ops found: which messages were new to the archive; the files to
// download, of the messages new to it (or still without them), and the preview pictures, of the
// messages new to it (or whose preview has none yet).
type applied struct {
	isNew map[string]bool
	files []pendingFile
}

// allNew says whether all of a page's messages were new (none: not so).
func (a applied) allNew(ids []string) bool {
	for _, id := range ids {
		if !a.isNew[id] {
			return false
		}
	}
	return len(ids) > 0
}

// apply writes ops into the archive.
func (s *liveSession) apply(o *ops) (out applied, err error) {
	if o.empty() {
		return out, nil
	}
	for _, h := range o.hybrid {
		s.noteThread(h.Key, threadInfo{Type: int64(h.Type), FBKey: h.FBKey})
	}
	for _, t := range o.threads {
		if t.Type != table.UNKNOWN_THREAD_TYPE {
			s.noteThread(t.Key, threadInfo{Type: int64(t.Type)})
		}
	}
	for _, t := range o.igThreads {
		s.noteThread(t.Key, threadInfo{IGID: t.IGID, IGThread: t.IGThread})
	}
	for _, r := range o.igReactions {
		if err := noteReaction(s.c.ID, r.LogID, r.Thread, r.Message, r.Sender); err != nil {
			s.c.Logf("keys.db: %v", err)
		}
	}
	for _, u := range o.igUnreacts { // a reaction taken away, by its id alone where it names no one
		if u.Sender != 0 {
			o.reactions = append(o.reactions, s.tr.reaction(u.Thread, u.Message, u.Sender, ""))
		} else if thread, message, sender := reactionOf(s.c.ID, u.LogID); message != "" {
			o.reactions = append(o.reactions, s.tr.reaction(thread, message, fbid(sender), ""))
		}
	}
	_, _, err = sourcekit.RunImporters(s.c, []sourcekit.Step{{Label: liveLabel(s.service), Run: func(a *archive.Archive, _ func(string)) error {
		w := writer(a, s.c, s.service)
		if s.self != "" {
			w.Self(s.selfUser())
		}
		for k, v := range s.names {
			w.Names[k] = v
		}
		for _, u := range o.contacts {
			w.Known(u)
			s.names[u.ID] = u
		}
		now := time.Now().UnixMilli()
		for _, t := range o.threads {
			w.Conv(t.MetaThread)
			if t.Muted != nil {
				w.State(t.Key, "muted", *t.Muted, now)
			}
			if t.Read > 0 {
				w.State(t.Key, "read_until", t.Read, now)
			}
		}
		for _, m := range o.members {
			w.Member(m.Thread, m.Who)
		}
		isNew := map[string]bool{}
		out.isNew = isNew
		for _, m := range o.messages {
			mid, added := w.Add(m)
			isNew[m.ID] = added
			if added || !hasFile(a, mid) {
				isNew[m.ID+"\x00file"] = true
			}
			if m.Preview != nil && (added || !hasPreviewImage(a, mid)) {
				isNew[m.ID+"\x00preview"] = true
			}
		}
		for _, f := range o.files {
			if f.N == previewN && isNew[f.Message+"\x00preview"] || f.N != previewN && isNew[f.Message+"\x00file"] {
				out.files = append(out.files, f)
			}
		}
		for _, r := range o.reactions {
			w.React(r.Thread, r.Message, r.By, r.Mine, r.Emoji)
		}
		for _, e := range o.edits {
			w.Edit("", e.Message, e.Text)
		}
		for _, d := range o.deletes {
			w.Delete(d.Thread, d.Message)
		}
		for _, r := range o.reads {
			w.Read(r.Thread, r.By, r.UpTo, r.At)
		}
		for _, r := range o.readKeys {
			w.ReadKeys(r.Thread, r.By, r.IDs, r.At, r.Delivered)
		}
		for _, r := range o.ownReads {
			w.State(r.Thread, "read_until", r.Value, now)
		}
		for _, r := range o.ownReadKeys {
			var last int64
			for _, id := range r.IDs {
				if mid := w.Message(r.Thread, id); mid != 0 {
					last = max(last, a.Int("SELECT ts FROM message WHERE id = ?", mid))
				}
			}
			if last > 0 {
				w.State(r.Thread, "read_until", last, now)
			}
		}
		for _, m := range o.mutes {
			w.State(m.Thread, "muted", m.Value, now)
		}
		for _, t := range o.titles {
			w.Title(t.Thread, t.Title)
		}
		a.Resolve()
		return nil
	}}})
	return out, err
}

func hasFile(a *archive.Archive, mid int64) bool {
	return a.Exists("SELECT 1 FROM attachment WHERE message_id = ?", mid)
}

func hasPreviewImage(a *archive.Archive, mid int64) bool {
	return a.Exists("SELECT 1 FROM link_preview WHERE message_id = ? AND image IS NOT NULL", mid)
}

func (o *ops) empty() bool {
	return len(o.contacts)+len(o.threads)+len(o.messages)+len(o.reactions)+len(o.edits)+len(o.deletes)+len(o.reads)+
		len(o.ownReads)+len(o.mutes)+len(o.titles)+len(o.members)+len(o.hybrid)+len(o.readKeys)+len(o.ownReadKeys)+
		len(o.igThreads)+len(o.igReactions)+len(o.igUnreacts) == 0
}
