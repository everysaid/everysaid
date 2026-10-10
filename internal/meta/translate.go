package meta

// What a Lightspeed table (messagix's table.LSTable: Meta's database changes, as its web client
// gets them) says, as the archive takes it. The reference is mautrix-meta's bridge
// (pkg/connector/handlemeta.go, backfill.go, chatinfo.go; pkg/msgconv/from-meta.go): which tables
// carry threads, messages and their files, reactions, edits, unsends, receipts, mutes, names and
// members, and how an end-to-end encrypted thread's Facebook key maps to its own.

import (
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"everysaid/internal/archive"
	"everysaid/internal/importers"
)

// The thumbs-up "stickers" Messenger sends for its like button (from-meta.go): a 👍.
var thumbsUp = map[int64]bool{369239263222822: true, 369239343222814: true, 369239383222810: true}

// ops are what a table says, in the order the archive takes them.
type ops struct {
	contacts    []importers.MetaUser
	threads     []*threadOp
	messages    []importers.MetaMessage
	files       []pendingFile
	reactions   []reactOp
	edits       []editOp
	deletes     []msgRef
	reads       []readOp
	ownReads    []stateOp
	mutes       []stateOp
	titles      []titleOp
	members     []memberOp
	ranges      []rangeOp
	hybrid      []hybridOp // an encrypted thread's Facebook key and its own
	ownReadKeys []keysOp
	readKeys    []readKeysOp
	igThreads   []igThreadOp
	igHistory   []igHistoryOp
	igReactions []igReactionOp
	igUnreacts  []igUnreactOp
}

type threadOp struct {
	importers.MetaThread
	Type  table.ThreadType
	Muted *int64 // muted until (Unix ms, -1 for ever, 0 not muted); nil: not said
	Read  int64  // the owner read up to (Unix ms); 0: not said
}

type reactOp struct {
	Thread, Message string
	By              importers.MetaUser
	Mine            bool
	Emoji           string
}

type editOp struct{ Message, Text string }

type msgRef struct{ Thread, Message string }

type readOp struct {
	Thread   string
	By       importers.MetaUser
	UpTo, At int64
}

type stateOp struct {
	Thread string
	Value  int64
}

type titleOp struct{ Thread, Title string }

type memberOp struct {
	Thread string
	Who    importers.MetaUser
}

// rangeOp is a page of a thread's history: its oldest message, and whether there are older.
type rangeOp struct {
	Thread        string
	FBKey         int64
	MinTS         int64
	MinID         string
	HasMoreBefore bool
	IDs           []string
}

type hybridOp struct {
	FBKey, Key string
	Type       table.ThreadType
}

// translator reads tables for one account; who uses it holds its lock (its maps change as it reads,
// and as sending learns a thread's ids).
type translator struct {
	sync.Mutex

	self   string            // the owner's id
	fbToWA map[string]string // an encrypted group's Facebook thread key to its own
	igKeys map[string]string // an Instagram thread's short id to its key
}

func (t *translator) key(fb int64) string {
	k := strconv.FormatInt(fb, 10)
	if wa, ok := t.fbToWA[k]; ok {
		return wa
	}
	return k
}

func (t *translator) user(id int64) importers.MetaUser {
	if id == 0 {
		return importers.MetaUser{}
	}
	return importers.MetaUser{ID: strconv.FormatInt(id, 10)}
}

// translate reads a table.
func (t *translator) translate(tbl *table.LSTable) *ops {
	o := &ops{}
	if tbl == nil {
		return o
	}
	for _, h := range tbl.LSUpdateThreadAuthorityAndMappingWithOTIDFromJID {
		t.mapThread(o, h.ThreadKey, h.ThreadJID, 0)
	}
	for _, h := range tbl.LSVerifyHybridThreadExists {
		t.mapThread(o, h.ThreadKey, h.ThreadJID, h.ThreadType)
	}
	for _, c := range tbl.LSDeleteThenInsertContact {
		o.contacts = append(o.contacts, importers.MetaUser{ID: strconv.FormatInt(c.GetFBID(), 10), Name: c.GetName(),
			Username: c.GetUsername()})
	}
	for _, c := range tbl.LSVerifyContactRowExists {
		o.contacts = append(o.contacts, importers.MetaUser{ID: strconv.FormatInt(c.GetFBID(), 10), Name: c.GetName(),
			Username: c.GetUsername()})
	}
	threads := map[string]*threadOp{}
	thread := func(fb int64, typ table.ThreadType, name, folder string) *threadOp {
		k := t.key(fb)
		if th, ok := threads[k]; ok {
			return th
		}
		if folder == "spam" || typ == table.FOLDER || typ == table.COMMUNITY_FOLDER {
			return nil
		}
		if folder == "e2ee_cutover" && typ == table.ONE_TO_ONE { // moved to the encrypted chats
			typ = table.ENCRYPTED_OVER_WA_ONE_TO_ONE
		}
		th := &threadOp{MetaThread: importers.MetaThread{Key: k, Group: !typ.IsOneToOne() && typ != table.UNKNOWN_THREAD_TYPE},
			Type: typ}
		if th.Group {
			th.Title = name
		} else if k != t.self && typ != table.UNKNOWN_THREAD_TYPE {
			th.Members = []importers.MetaUser{t.user(fb)}
		}
		threads[k] = th
		o.threads = append(o.threads, th)
		return th
	}
	for _, th := range tbl.LSDeleteThenInsertThread {
		op := thread(th.ThreadKey, th.ThreadType, th.ThreadName, th.FolderName)
		if op == nil {
			continue
		}
		muted := th.MuteExpireTimeMs
		if muted < 0 {
			muted = -1
		}
		op.Muted = &muted
		op.Read = th.LastReadWatermarkTimestampMs
	}
	for _, th := range tbl.LSUpdateOrInsertThread {
		if op := thread(th.ThreadKey, th.ThreadType, th.ThreadName, th.FolderName); op != nil {
			op.Read = max(op.Read, th.LastReadWatermarkTimestampMs)
		}
	}
	for _, v := range tbl.LSVerifyThreadExists {
		thread(v.ThreadKey, v.ThreadType, "", v.FolderName)
	}
	for _, p := range tbl.LSAddParticipantIdToGroupThread {
		k := t.key(p.ThreadKey)
		if strconv.FormatInt(p.ContactId, 10) == t.self {
			continue
		}
		if th, ok := threads[k]; ok && th.Group {
			th.Members = append(th.Members, t.user(p.ContactId))
		} else {
			o.members = append(o.members, memberOp{k, t.user(p.ContactId)})
		}
		if p.ReadWatermarkTimestampMs > 0 {
			o.reads = append(o.reads, readOp{k, t.user(p.ContactId), p.ReadWatermarkTimestampMs, p.ReadActionTimestampMs})
		}
	}
	for _, m := range tbl.LSMoveThreadToE2EECutoverFolder {
		k := t.key(m.ThreadKey)
		if th, ok := threads[k]; ok && th.Type == table.ONE_TO_ONE {
			th.Type = table.ENCRYPTED_OVER_WA_ONE_TO_ONE
		} else if !ok {
			o.threads = append(o.threads, &threadOp{MetaThread: importers.MetaThread{Key: k}, Type: table.ENCRYPTED_OVER_WA_ONE_TO_ONE})
		}
	}
	for _, m := range tbl.LSUpdateThreadMuteSetting {
		v := m.MuteExpireTimeMS
		if v < 0 {
			v = -1
		}
		o.mutes = append(o.mutes, stateOp{t.key(m.ThreadKey), v})
	}
	upsert, insert := tbl.WrapMessages()
	for _, u := range upsert {
		var ids []string
		for _, m := range u.Messages {
			if msg, ok := t.message(o, m); ok {
				o.messages = append(o.messages, msg)
				ids = append(ids, msg.ID)
			}
		}
		if u.Range != nil {
			o.ranges = append(o.ranges, rangeOp{Thread: t.key(u.Range.ThreadKey), FBKey: u.Range.ThreadKey,
				MinTS: u.Range.MinTimestampMs, MinID: u.Range.MinMessageId, HasMoreBefore: u.Range.HasMoreBefore, IDs: ids})
		}
	}
	for _, m := range insert {
		if msg, ok := t.message(o, m); ok {
			o.messages = append(o.messages, msg)
		}
	}
	for _, e := range tbl.LSEditMessage {
		o.edits = append(o.edits, editOp{e.MessageID, e.Text})
	}
	for _, d := range tbl.LSDeleteMessage {
		o.deletes = append(o.deletes, msgRef{t.key(d.ThreadKey), d.MessageId})
	}
	for _, d := range tbl.LSDeleteThenInsertMessage {
		if d.IsUnsent {
			o.deletes = append(o.deletes, msgRef{t.key(d.ThreadKey), d.MessageId})
		}
	}
	for _, r := range tbl.LSUpsertReaction {
		o.reactions = append(o.reactions, t.reaction(t.key(r.ThreadKey), r.MessageId, r.ActorId, r.Reaction))
	}
	for _, r := range tbl.LSDeleteReaction {
		o.reactions = append(o.reactions, t.reaction(t.key(r.ThreadKey), r.MessageId, r.ActorId, ""))
	}
	for _, r := range tbl.LSUpdateReadReceipt {
		if strconv.FormatInt(r.ContactId, 10) == t.self {
			o.ownReads = append(o.ownReads, stateOp{t.key(r.ThreadKey), r.ReadWatermarkTimestampMs})
		} else {
			o.reads = append(o.reads, readOp{t.key(r.ThreadKey), t.user(r.ContactId), r.ReadWatermarkTimestampMs, r.ReadActionTimestampMs})
		}
	}
	for _, r := range tbl.LSMarkThreadReadV2 {
		o.ownReads = append(o.ownReads, stateOp{t.key(r.ThreadKey), r.LastReadWatermarkTimestampMs})
	}
	for _, r := range tbl.LSMarkThreadRead {
		o.ownReads = append(o.ownReads, stateOp{t.key(r.ThreadKey), r.LastReadWatermarkTimestampMs})
	}
	for _, n := range tbl.LSSyncUpdateThreadName {
		o.titles = append(o.titles, titleOp{t.key(n.ThreadKey), n.ThreadName})
	}
	return o
}

// mapThread notes an encrypted thread's Facebook key and its own (its JID's user).
func (t *translator) mapThread(o *ops, fb, jid int64, typ table.ThreadType) {
	if jid == 0 || fb == jid {
		return
	}
	k, j := strconv.FormatInt(fb, 10), strconv.FormatInt(jid, 10)
	t.fbToWA[k] = j
	o.hybrid = append(o.hybrid, hybridOp{FBKey: k, Key: j, Type: typ})
}

func (t *translator) reaction(thread, message string, actor int64, emoji string) reactOp {
	id := strconv.FormatInt(actor, 10)
	return reactOp{Thread: thread, Message: message, By: t.user(actor), Mine: id == t.self, Emoji: emoji}
}

// message is a message of a table, its files queued in o; false for one not kept.
func (t *translator) message(o *ops, m *table.WrappedMessage) (importers.MetaMessage, bool) {
	if m == nil || m.MessageId == "" {
		return importers.MetaMessage{}, false
	}
	thread := t.key(m.ThreadKey)
	sender := strconv.FormatInt(m.SenderId, 10)
	msg := importers.MetaMessage{Thread: thread, ID: m.MessageId, TS: m.TimestampMs, Outgoing: sender == t.self,
		Text: m.Text, Kind: "text", ReplyTo: m.ReplySourceId, Forwarded: m.IsForwarded, Deleted: m.IsUnsent,
		Edited: m.EditCount > 0}
	if !msg.Outgoing {
		msg.From = t.user(m.SenderId)
	}
	if m.ReplySourceId != "" && m.ReplyMessageText != "" {
		msg.Reply = m.ReplyMessageText
	}
	if thumbsUp[m.StickerId] && len(m.Stickers) <= 1 {
		msg.Text, m.Stickers = "👍", nil
	}
	if m.IsAdminMessage {
		msg.Kind, msg.Subtype, msg.Code = "system", "notice", "meta:admin"
	}
	for _, r := range m.Reactions {
		msg.Reactions = append(msg.Reactions, importers.MetaReaction{Emoji: r.Reaction, By: t.user(r.ActorId),
			Mine: strconv.FormatInt(r.ActorId, 10) == t.self})
	}
	if ms, err := (&socket.MentionData{MentionIDs: m.MentionIds, MentionOffsets: m.MentionOffsets,
		MentionLengths: m.MentionLengths, MentionTypes: m.MentionTypes}).Parse(); err == nil {
		msg.Mentions = mentions(m.Text, ms, t)
	}
	n := 0
	file := func(kind, u, name, mime string) {
		if msg.Kind == "text" {
			msg.Kind = kind
		}
		if u == "" {
			return
		}
		o.files = append(o.files, pendingFile{Thread: thread, Message: m.MessageId, N: n, URL: u, Name: name, Mime: mime,
			At: time.Now().Unix()})
		n++
	}
	seen := map[string]bool{}
	for _, a := range m.BlobAttachments {
		if a.AttachmentFbid != "" && seen[a.AttachmentFbid] {
			continue // Meta sends some twice
		}
		seen[a.AttachmentFbid] = true
		u, mime := a.PlayableUrl, a.PlayableUrlMimeType
		if mime == "" {
			mime = a.AttachmentMimeType
		}
		if u == "" {
			u, mime = a.PreviewUrl, a.PreviewUrlMimeType
		}
		kind, sub := attachmentKind(a.AttachmentType, mime)
		if sub != "" {
			msg.Subtype = sub
		}
		file(kind, u, a.Filename, mime)
	}
	for _, a := range m.Attachments {
		if a.EphemeralMediaViewMode == table.EphemeralMediaViewOnce || a.EphemeralMediaViewMode == table.EphemeralMediaReplayable ||
			a.AttachmentType == table.AttachmentTypeEphemeralImage || a.AttachmentType == table.AttachmentTypeEphemeralVideo {
			msg.Notice = &archive.Notice{Code: "view_once", Args: map[string]any{}}
			kind, _ := attachmentKind(a.AttachmentType, a.AttachmentMimeType)
			file(kind, "", "", "")
			continue
		}
		u, mime := a.PlayableUrl, a.PlayableUrlMimeType
		if mime == "" {
			mime = a.AttachmentMimeType
		}
		if u == "" {
			u, mime = a.PreviewUrl, a.PreviewUrlMimeType
		}
		kind, sub := attachmentKind(a.AttachmentType, mime)
		if sub != "" {
			msg.Subtype = sub
		}
		file(kind, u, a.Filename, mime)
	}
	for _, x := range m.XMAAttachments {
		t.xma(&msg, x, file)
	}
	for _, s := range m.Stickers {
		u, mime := s.PlayableUrl, s.PlayableUrlMimeType
		if u == "" {
			u, mime = s.PreviewUrl, s.PreviewUrlMimeType
		}
		file("sticker", u, "", mime)
		if msg.Text == "" && s.AccessibilitySummaryText != "" {
			msg.Text = s.AccessibilitySummaryText
		}
	}
	return msg, true
}

// xma is a shared item (a link with its preview, a post, a place): its link in the text, a place as
// a location; the preview's picture is not kept.
func (t *translator) xma(msg *importers.MetaMessage, x *table.WrappedXMA, file func(kind, u, name, mime string)) {
	if x.CTA != nil && strings.HasPrefix(x.CTA.Type_, "xma_poll_") {
		msg.Subtype = "poll"
		return
	}
	if x.CTA != nil && x.CTA.Type_ == "xma_live_location_sharing" {
		if lat, lon, ok := latLon(x.CTA.NativeUrl); ok {
			msg.Kind, msg.Lat, msg.Lon = "location", &lat, &lon
			msg.Place = strings.TrimSpace(x.TitleText + "\n" + x.SubtitleText)
			return
		}
	}
	link := ""
	if x.CTA != nil && x.CTA.ActionUrl != "" {
		link = removeLPHP(x.CTA.ActionUrl)
	} else if x.ActionUrl != "" {
		link = removeLPHP(x.ActionUrl)
	}
	if link != "" && strings.HasPrefix(link, "http") {
		if msg.Subtype == "" {
			msg.Subtype = "link"
		}
		if !strings.Contains(msg.Text, link) {
			msg.Text = strings.TrimSpace(strings.TrimSpace(msg.Text+"\n"+x.TitleText) + "\n" + link)
		}
		return
	}
	// media shared as an XMA (Instagram's reels and posts, a GIF)
	u, mime := x.PlayableUrl, x.PlayableUrlMimeType
	if u == "" {
		u, mime = x.PreviewUrl, x.PreviewUrlMimeType
	}
	typ := x.AttachmentType
	if x.ShouldAutoplayVideo {
		typ = table.AttachmentTypeAnimatedImage
	}
	kind, sub := attachmentKind(typ, mime)
	if sub != "" {
		msg.Subtype = sub
	}
	if u != "" {
		file(kind, u, x.Filename, mime)
	} else if x.TitleText != "" && msg.Text == "" {
		msg.Text = x.TitleText
	}
}

// attachmentKind is a message's kind by its attachment's type (and its type of file).
func attachmentKind(typ table.AttachmentType, mime string) (kind, subtype string) {
	switch typ {
	case table.AttachmentTypeImage, table.AttachmentTypeEphemeralImage:
		return "image", ""
	case table.AttachmentTypeAnimatedImage:
		return "image", "gif"
	case table.AttachmentTypeVideo, table.AttachmentTypeEphemeralVideo:
		return "video", ""
	case table.AttachmentTypeAudio, table.AttachmentTypeSoundBite:
		return "voice", ""
	case table.AttachmentTypeSticker, table.AttachmentTypeSelfieSticker, table.AttachmentTypeThirdPartySticker:
		return "sticker", ""
	}
	switch {
	case strings.HasPrefix(mime, "image/gif"):
		return "image", "gif"
	case strings.HasPrefix(mime, "image/"):
		return "image", ""
	case strings.HasPrefix(mime, "video/"):
		return "video", ""
	case strings.HasPrefix(mime, "audio/"):
		return "voice", ""
	}
	return "file", ""
}

// removeLPHP is the address a Facebook link redirect (l.php?u=) leads to.
func removeLPHP(addr string) string {
	parsed, _ := url.Parse(addr)
	if parsed != nil && parsed.Path == "/l.php" {
		if u := parsed.Query().Get("u"); u != "" {
			return u
		}
	}
	return addr
}

// latLon reads "lat,long" (a location's native address, also as messenger://location_share?lat=&long=).
func latLon(s string) (float64, float64, bool) {
	if u, err := url.Parse(s); err == nil && u.Query().Get("lat") != "" {
		lat, e1 := strconv.ParseFloat(u.Query().Get("lat"), 64)
		lon, e2 := strconv.ParseFloat(u.Query().Get("long"), 64)
		return lat, lon, e1 == nil && e2 == nil
	}
	a, b, ok := strings.Cut(s, ",")
	if !ok {
		return 0, 0, false
	}
	lat, e1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
	lon, e2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
	return lat, lon, e1 == nil && e2 == nil
}

// mentions are the people a text names (Meta gives where, in UTF-16 units).
func mentions(text string, ms socket.Mentions, t *translator) []importers.MetaMention {
	if len(ms) == 0 {
		return nil
	}
	u := utf16.Encode([]rune(text))
	var out []importers.MetaMention
	for _, m := range ms {
		if m.Type != socket.MentionTypePerson && m.Type != socket.MentionTypeSilent {
			continue
		}
		token := ""
		if m.Offset >= 0 && m.Length > 0 && m.Offset+m.Length <= len(u) {
			token = string(utf16.Decode(u[m.Offset : m.Offset+m.Length]))
		}
		out = append(out, importers.MetaMention{Who: t.user(m.ID), Token: token})
	}
	return out
}
