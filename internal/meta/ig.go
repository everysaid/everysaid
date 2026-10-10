package meta

// Instagram live, through mautrix-meta's instameow client (Instagram's web API: the inbox, then its
// changes as they come). The reference is mautrix-meta's pkg/igconnector (client.go, chatsync.go,
// handlemeta.go, backfill.go, handlematrix.go) and pkg/msgconv/igconv. A thread is kept by its
// Facebook key (thread_key), as the bridge keeps it; Instagram's changes name it by its short id,
// which the thread's information maps to its key (meta_thread.ig_id), and its long id is what read
// receipts and unsends need. People are their messaging FBIDs, with their Instagram user names.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"

	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/dgw"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	tps "go.mau.fi/mautrix-meta/pkg/messagix/types"

	"github.com/coder/websocket"

	"everysaid/internal/archive"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
)

// igClient is what the source asks of Instagram.
type igClient interface {
	LoadIndex(ctx context.Context) (*tps.PolarisViewer, *slidetypes.Mailbox, error)
	GetOwnFBID() int64
	Connect(ctx context.Context)
	Disconnect()
	SetEventHandler(h func(context.Context, slidetypes.ClientEvent) error)
	LoadState(state json.RawMessage) error
	DumpState() (json.RawMessage, error)
	SetSeqID(seqID int64, ts time.Time)
	GetCookies() *cookies.Cookies
	GetThread(ctx context.Context, req *slidetypes.GetThreadInfoRequest) (*slidetypes.ThreadInfoResponse, error)
	FetchThreadID(ctx context.Context, threadFBID int64) (*instameow.ThreadIGIDs, error)
	PaginateMessages(ctx context.Context, req *slidetypes.PaginateMessagesRequest) (*slidetypes.PaginateMessagesResponse, error)
	PaginateMailbox(ctx context.Context, req *slidetypes.PaginateMailboxRequest) (*slidetypes.PaginateMailboxResponse, error)
	SendMessage(ctx context.Context, req *slidetypes.SendTextRequest) (*slidetypes.SendTextResponse, error)
	SendMedia(ctx context.Context, req *slidetypes.SendMediaRequest) (*slidetypes.SendMediaResponse, error)
	EditMessage(ctx context.Context, req *slidetypes.EditMessageRequest) (*slidetypes.EditMessageResponse, error)
	UnsendMessage(ctx context.Context, req *slidetypes.UnsendMessageRequest) (*slidetypes.UnsendMessageResponse, error)
	SendReaction(ctx context.Context, req *slidetypes.CreateReactionRequest) (*slidetypes.SendReactionResponse, error)
	MarkRead(ctx context.Context, req *slidetypes.MarkReadRequest) (*slidetypes.MarkReadResponse, error)
	MarkReadValidation(ctx context.Context, req *slidetypes.MarkReadRequest) (*slidetypes.MarkReadValidationResponse, error)
	// Upload sends a file to a thread's storage; its id, for a message.
	Upload(ctx context.Context, thread int64, name, mime string, data []byte) (int64, error)
}

// instameowClient is instameow's client.
type instameowClient struct{ *instameow.Client }

func (m instameowClient) Upload(ctx context.Context, thread int64, name, mime string, data []byte) (int64, error) {
	resp, err := m.GetHTTP().SendMercuryUploadRequest(ctx, thread, &httpclient.MercuryUploadMedia{Filename: name,
		MimeType: mime, MediaData: data})
	if err != nil {
		return 0, err
	}
	id := resp.Payload.RealMetadata.GetFbId()
	if id == 0 {
		return 0, errors.New("Meta gave no id for the file")
	}
	return id, nil
}

// newIGClient makes an Instagram client of the cookies.
var newIGClient = func(c *cookies.Cookies, log zerolog.Logger) igClient {
	return instameowClient{instameow.NewClient(instameow.ClientParams{Cookies: c, Log: log,
		Settings: exhttp.SensibleClientSettings, DisableTyping: true})}
}

// --- reading -------------------------------------------------------------------------------------

func igUser(u *slidetypes.User) importers.MetaUser {
	if u == nil || u.InteropMessagingUserFBID == 0 {
		return importers.MetaUser{}
	}
	return importers.MetaUser{ID: strconv.FormatInt(u.InteropMessagingUserFBID, 10), Name: u.FullName, Username: u.Username}
}

// igThread reads a thread's information into o, with the messages it carries where they are whole
// (a thread asked for by itself; an inbox's are stubs, as mautrix-meta's bridge says: there its
// first page of messages is asked for instead).
func (t *translator) igThread(o *ops, th *slidetypes.ThreadInfo, withMessages bool) {
	if th == nil || th.ThreadKey == 0 {
		return
	}
	key := strconv.FormatInt(th.ThreadKey, 10)
	if th.ID != "" {
		t.igKeys[th.ID] = key
	}
	typ := table.ONE_TO_ONE
	if th.IsGroup {
		typ = table.GROUP_THREAD
	}
	op := &threadOp{MetaThread: importers.MetaThread{Key: key, Group: th.IsGroup, Title: th.ThreadTitle}, Type: typ}
	for _, u := range th.Users {
		if mu := igUser(u); mu.ID != "" && mu.ID != t.self {
			op.Members = append(op.Members, mu)
			o.contacts = append(o.contacts, mu)
		}
	}
	if th.IsMuted != nil {
		v := int64(0)
		if *th.IsMuted {
			v = -1
		}
		op.Muted = &v
	}
	o.threads = append(o.threads, op)
	o.igThreads = append(o.igThreads, igThreadOp{Key: key, IGID: th.ID, IGThread: th.ThreadID})
	for _, rr := range th.SlideReadReceipts {
		id := strconv.FormatInt(rr.ParticipantFBID, 10)
		if id == t.self {
			if !th.MarkedAsUnread {
				o.ownReads = append(o.ownReads, stateOp{key, rr.WatermarkTimestampMS.UnixMilli()})
			}
		} else {
			o.reads = append(o.reads, readOp{key, importers.MetaUser{ID: id}, rr.WatermarkTimestampMS.UnixMilli(), 0})
		}
	}
	if withMessages && th.SlideMessages != nil {
		t.igPage(o, key, th.ID, th.SlideMessages)
	} else if th.ID != "" {
		o.igHistory = append(o.igHistory, igHistoryOp{Key: key, IGID: th.ID, First: true})
	}
}

type igThreadOp struct{ Key, IGID, IGThread string }

// igHistoryOp is a page of a thread's messages: where the next older page starts.
type igHistoryOp struct {
	Key, IGID, Cursor string
	HasMore           bool
	First             bool // the thread's newest page, still to ask for
	IDs               []string
}

// igPage reads a page of a thread's messages (newest first) into o.
func (t *translator) igPage(o *ops, key, igID string, page *slidetypes.SlideMessages) {
	var ids []string
	for _, e := range page.Edges {
		if m, ok := t.igMessage(o, key, e.Node); ok {
			o.messages = append(o.messages, m)
			ids = append(ids, m.ID)
		}
	}
	o.igHistory = append(o.igHistory, igHistoryOp{Key: key, IGID: igID, Cursor: page.PageInfo.EndCursor,
		HasMore: page.PageInfo.HasNextPage, IDs: ids})
}

// igMessage is an Instagram message as the archive takes it, its files queued in o.
func (t *translator) igMessage(o *ops, key string, m *slidetypes.Message) (importers.MetaMessage, bool) {
	if m == nil || m.ID == "" {
		return importers.MetaMessage{}, false
	}
	sender := strconv.FormatInt(m.SenderFBID, 10)
	msg := importers.MetaMessage{Thread: key, ID: m.ID, TS: m.TimestampMS.UnixMilli(), Outgoing: sender == t.self,
		Kind: "text", ReplyTo: m.RepliedToMessageID, Forwarded: m.IGDIsForwarded, Edited: len(m.SlideEditHistory) > 0}
	if !msg.Outgoing {
		msg.From = importers.MetaUser{ID: sender}
		if m.Sender != nil {
			if u := igUser(&m.Sender.UserDict); u.ID == sender {
				msg.From = u
				o.contacts = append(o.contacts, u)
			}
		}
	}
	if m.RepliedToMessage != nil {
		msg.Reply = m.RepliedToMessage.TextBody
	}
	for _, r := range m.Reactions {
		if r == nil || r.Reaction == "" {
			continue
		}
		by := strconv.FormatInt(r.SenderFBID, 10)
		msg.Reactions = append(msg.Reactions, importers.MetaReaction{Emoji: r.Reaction, By: importers.MetaUser{ID: by},
			Mine: by == t.self})
		o.igReactions = append(o.igReactions, igReactionOp{LogID: r.LogMessageID, Thread: key, Message: m.ID, Sender: by})
	}
	n := 0
	file := func(kind, u, name, mime string) {
		if msg.Kind == "text" {
			msg.Kind = kind
		}
		if u == "" {
			return
		}
		o.files = append(o.files, pendingFile{Thread: key, Message: m.ID, N: n, URL: u, Name: name, Mime: mime, At: time.Now().Unix()})
		n++
	}
	attachment := func(kind string, a *slidetypes.Attachment) {
		if a == nil {
			file(kind, "", "", "")
			return
		}
		u := a.AttachmentCDNURL
		if u == "" {
			u = a.PreviewCDNURL
		}
		file(kind, u, "", "")
	}
	text := m.TextBody
	switch c := m.Content.Content.(type) {
	case *slidetypes.MessageContentText:
		text = c.TextBody
	case *slidetypes.MessageContentAdminText:
		msg.Kind, msg.Subtype, msg.Code = "system", "notice", "instagram:"+m.ContentType
		var b strings.Builder
		for _, f := range c.TextFragments {
			b.WriteString(f.Plaintext)
		}
		if text = b.String(); text == "" {
			text = m.IGDSnippet
		}
		if c.IsReactionActionLog {
			return msg, false // a reaction, said again as a message
		}
	case *slidetypes.MessageContentImage:
		for _, a := range c.Attachments {
			attachment("image", a)
		}
	case *slidetypes.MessageContentVideo:
		for _, a := range c.Videos {
			attachment("video", a)
		}
	case *slidetypes.MessageContentMultiMedia:
		for _, a := range c.Attachments {
			kind := "image"
			if a != nil && a.DashManifest != "" {
				kind = "video"
			}
			attachment(kind, a)
		}
	case *slidetypes.MessageContentAudio:
		for _, a := range c.AudioAttachments {
			if a != nil {
				file("voice", a.AttachmentCDNURL, "", "")
			}
		}
	case *slidetypes.MessageContentAnimatedMedia:
		for _, a := range c.AnimatedMedia {
			if a == nil {
				continue
			}
			switch {
			case a.AttachmentWebpURL != "" && (a.IsSticker || a.AttachmentMP4URL == ""):
				file("sticker", a.AttachmentWebpURL, "", "image/webp")
			case a.AttachmentMP4URL != "":
				msg.Subtype = "gif"
				file("video", a.AttachmentMP4URL, "", "video/mp4")
			default:
				file("image", a.PreviewCDNURL, "", "")
			}
		}
	case *slidetypes.MessageContentRavenImage:
		if c.ViewMode.ViewType() != "" || c.Attachment == nil {
			msg.Notice = &archive.Notice{Code: "view_once", Args: map[string]any{}}
			msg.Kind = "image"
		} else {
			attachment("image", c.Attachment)
		}
	case *slidetypes.MessageContentRavenVideo:
		if c.ViewMode.ViewType() != "" || c.Attachment == nil {
			msg.Notice = &archive.Notice{Code: "view_once", Args: map[string]any{}}
			msg.Kind = "video"
		} else {
			attachment("video", c.Attachment)
		}
	case *slidetypes.MessageContentSticker:
		file("sticker", c.PreviewURL, "", "")
		if text == "" {
			text = c.AltText
		}
	case *slidetypes.MessageContentMusicSticker:
		file("voice", c.AudioTrack.Web30SPreviewDownloadURL, "", "")
		text = strings.TrimSpace(c.AudioTrack.Title + " " + c.AudioTrack.DisplayArtist)
	case *slidetypes.MessageContentXMA:
		text = c.XMATextBody
		if x := c.XMA; x != nil {
			link := x.TargetURL
			if strings.Contains(link, "l.facebook.com") || strings.Contains(link, "l.instagram.com") {
				link = removeLPHP(link)
			}
			if link != "" {
				msg.Subtype = "link"
				if !strings.Contains(text, link) {
					text = strings.TrimSpace(strings.TrimSpace(text+"\n"+x.TitleText) + "\n" + link)
				}
			} else if text == "" {
				text = strings.TrimSpace(x.TitleText + "\n" + x.SubtitleText)
			}
		}
	case *slidetypes.MessageContentAIRichResponse:
		text = c.UnifiedResponse
	case *slidetypes.MessageContentAISearchResponse:
		text = c.MessageTextBody
	default:
		msg.Kind, msg.Subtype, msg.Code = "system", "notice", "instagram:unsupported"
		msg.Notice = &archive.Notice{Code: "unsupported", Args: map[string]any{}}
	}
	msg.Text = text
	msg.Mentions = mentions(text, m.Mentions.ToSocket(), t)
	return msg, true
}

type igReactionOp struct{ LogID, Thread, Message, Sender string }

// igDelta reads a change; false when its thread is not known yet (its information is to be asked
// for first).
func (t *translator) igDelta(o *ops, d *slidetypes.Delta) bool {
	igID := d.ThreadIGID
	if ev, ok := d.Data.(*slidetypes.NewMessageEvent); ok && igID == "" && ev.Message != nil {
		igID = ev.Message.ThreadFBID
	}
	key, known := t.igKeys[igID]
	if !known {
		switch d.Data.(type) {
		case *slidetypes.DeleteThreadEvent, *slidetypes.DeleteMessageEvent, *slidetypes.DeleteReactionEvent,
			*slidetypes.ParticipantLeaveEvent, slidetypes.UnknownEvent, slidetypes.IgnoredEvent:
			return true // nothing to bring for a thread the archive does not have
		}
		return false
	}
	message := func(m *slidetypes.Message) {
		if msg, ok := t.igMessage(o, key, m); ok {
			o.messages = append(o.messages, msg)
		}
	}
	switch e := d.Data.(type) {
	case *slidetypes.NewMessageEvent:
		message(e.Message)
	case *slidetypes.AdminMessageEvent:
		message(e.Message)
	case *slidetypes.EditMessageEvent:
		o.edits = append(o.edits, editOp{e.MessageID, e.TextBody})
	case *slidetypes.CreateReactionEvent:
		by := strconv.FormatInt(e.Reaction.SenderFBID, 10)
		o.reactions = append(o.reactions, reactOp{Thread: key, Message: e.MessageID, By: importers.MetaUser{ID: by},
			Mine: by == t.self, Emoji: e.Reaction.Reaction})
		o.igReactions = append(o.igReactions, igReactionOp{LogID: e.Reaction.LogMessageID, Thread: key, Message: e.MessageID, Sender: by})
	case *slidetypes.DeleteReactionEvent:
		o.igUnreacts = append(o.igUnreacts, igUnreactOp{Thread: key, Message: e.MessageID, Sender: e.Reaction.SenderFBID,
			LogID: e.Reaction.LogMessageID})
	case *slidetypes.DeleteMessageEvent:
		o.deletes = append(o.deletes, msgRef{key, e.MessageID})
	case *slidetypes.MarkReadEvent:
		o.ownReads = append(o.ownReads, stateOp{key, e.ReadTimestampMS.UnixMilli()})
	case *slidetypes.ReadReceiptEvent:
		by := strconv.FormatInt(e.ReadReceipt.ParticipantFBID, 10)
		if by == t.self {
			o.ownReads = append(o.ownReads, stateOp{key, e.ReadReceipt.WatermarkTimestampMS.UnixMilli()})
		} else {
			o.reads = append(o.reads, readOp{key, importers.MetaUser{ID: by}, e.ReadReceipt.WatermarkTimestampMS.UnixMilli(), 0})
		}
	case *slidetypes.UpdateThreadNameEvent:
		o.titles = append(o.titles, titleOp{key, e.ThreadName})
		message(e.Message)
	case *slidetypes.UpdateThreadImageEvent:
		message(e.Message)
	case *slidetypes.ParticipantJoinEvent:
		if th := e.Thread.AsIGDirectThread; th != nil {
			for _, u := range th.Users {
				if mu := igUser(u); mu.ID != "" && mu.ID != t.self {
					o.members = append(o.members, memberOp{key, mu})
					o.contacts = append(o.contacts, mu)
				}
			}
		}
		message(e.Message)
	case *slidetypes.ParticipantLeaveEvent:
		message(e.Message)
	case *slidetypes.MuteThreadEvent:
		v := int64(0)
		if e.IsMutedNow {
			v = -1
		}
		o.mutes = append(o.mutes, stateOp{key, v})
	}
	return true
}

type igUnreactOp struct {
	Thread, Message, LogID string
	Sender                 int64
}

// --- the connection ------------------------------------------------------------------------------

// liveInstagram is Instagram's connection, until ctx ends or it fails.
func liveInstagram(ctx context.Context, c *plugins.Context) error {
	sess, err := loadSession(c.ID)
	if err != nil {
		return err
	}
	if !loggedIn(sess) {
		return errLoggedOutIG
	}
	threads, err := loadThreads(c.ID)
	if err != nil {
		return err
	}
	logger, closer := libraryLog(c.ID)
	defer closer.Close()
	s := newSession(c, "instagram", sess, threads)
	s.ig = newIGClient(cookiesOf(sess), logger)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.ig.SetEventHandler(func(ectx context.Context, evt slidetypes.ClientEvent) error { return s.onIG(ctx, evt) })
	var mailbox *slidetypes.Mailbox
	if sess.Seq != 0 && len(sess.State) > 0 && s.ig.LoadState(sess.State) == nil {
		s.ig.SetSeqID(sess.Seq, time.UnixMilli(sess.SeqAt)) // Instagram gives what changed since
	} else {
		viewer, mb, err := s.ig.LoadIndex(ctx)
		if err != nil {
			if errors.Is(err, httpclient.ErrTokenInvalidated) {
				s.logOut()
				return errLoggedOutIG
			}
			return connectError(err, "instagram", sess, c.ID)
		}
		if own := s.ig.GetOwnFBID(); own != 0 && strconv.FormatInt(own, 10) != sess.UserID {
			return errAnotherAccount
		}
		s.selfName = viewer.GetName()
		s.selfUsername = viewer.GetUsername()
		mailbox = mb
	}
	register(s)
	defer func() {
		cancel()
		unregister(s)
		s.ig.SetEventHandler(nil)
		s.ig.Disconnect()
		s.save(true)
	}()
	if mailbox != nil {
		o := &ops{}
		s.tr.Lock()
		for _, e := range mailbox.ThreadsByFolder.Edges {
			s.tr.igThread(o, e.Node.AsIGDirectThread, false)
		}
		s.tr.Unlock()
		if err := s.handle(ctx, item{ops: o}); err != nil {
			return err
		}
		s.setSeq(mailbox.UQSeqID)
		if !mailbox.ThreadsByFolder.PageInfo.HasNextPage {
			c.SaveState(plugins.M{"threads_listed": true})
		} else {
			s.igCursor = mailbox.ThreadsByFolder.PageInfo.EndCursor
		}
	}
	go s.ig.Connect(ctx)
	c.Log("{service}: connected as {name}", map[string]any{"service": s.label(), "name": s.selfName})
	go s.downloader(ctx)
	go s.igHistoryLoop(ctx)
	s.wakeDownloader()
	return s.loop(ctx)
}

// onIG is what instameow says, from its own goroutines; a change is written before it returns (and
// so before its sequence number is kept).
func (s *liveSession) onIG(ctx context.Context, evt slidetypes.ClientEvent) error {
	switch e := evt.(type) {
	case *slidetypes.Connected:
		s.connected.Store(true)
		go s.igThreadBackfill(ctx)
	case *slidetypes.Disconnected:
		s.connected.Store(false)
		if websocket.CloseStatus(e.Error) == dgw.CloseStatusUnauthorized {
			s.logOut()
			s.fail(errLoggedOutIG)
			return errLoggedOutIG // no reconnecting
		}
	case *slidetypes.AuthError:
		if errors.Is(e.Error, httpclient.ErrTokenInvalidated) {
			s.logOut()
			s.fail(errLoggedOutIG)
		} else {
			s.fail(errCheck)
		}
	case *slidetypes.ResnapshotRequired:
		s.setSeq(0) // the inbox again, at the next connection
		s.fail(errors.New("Instagram asks to load the inbox again"))
	case *slidetypes.ReconnectionStateUpdate:
		s.sessMu.Lock()
		s.sess.State = e.State
		s.sessMu.Unlock()
	case *slidetypes.SeqIDUpdate, *slidetypes.Delta:
		done := make(chan error, 1)
		s.push(ctx, item{wa: evt, done: done})
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// handleIG writes an Instagram change.
func (s *liveSession) handleIG(ctx context.Context, evt any) (*ops, error) {
	o := &ops{}
	switch e := evt.(type) {
	case *slidetypes.SeqIDUpdate: // the changes before it are written: kept as had
		s.sessMu.Lock()
		s.sess.Seq, s.sess.SeqAt = e.SeqID, e.Timestamp.UnixMilli()
		s.sessMu.Unlock()
		return o, saveSeq(s.c.ID, e.SeqID, e.Timestamp.UnixMilli())
	case *slidetypes.Delta:
		s.tr.Lock()
		known := s.tr.igDelta(o, e)
		s.tr.Unlock()
		if known {
			return o, nil
		}
		igID := e.ThreadIGID
		if ev, ok := e.Data.(*slidetypes.NewMessageEvent); ok && igID == "" && ev.Message != nil {
			igID = ev.Message.ThreadFBID
		}
		if igID == "" {
			return o, nil // a change of no thread: nothing to bring
		}
		resp, err := s.ig.GetThread(ctx, slidetypes.MakeGetThreadInfoRequest(igID))
		if err != nil || resp.ThreadInfo.AsIGDirectThread == nil {
			// left as it is (asking again and again would hold every change after it)
			s.c.Log("{service}: a chat not brought ({e})", map[string]any{"service": s.label(), "e": fmt.Sprint(err)})
			return o, nil
		}
		s.tr.Lock()
		s.tr.igThread(o, resp.ThreadInfo.AsIGDirectThread, true)
		s.tr.igDelta(o, e)
		s.tr.Unlock()
	}
	return o, nil
}

// setSeq keeps where Instagram's changes were had to (0: none, the inbox is loaded again).
func (s *liveSession) setSeq(seq int64) {
	s.sessMu.Lock()
	s.sess.Seq, s.sess.SeqAt = seq, time.Now().UnixMilli()
	if seq == 0 {
		s.sess.State = nil
	}
	s.sessMu.Unlock()
	s.save(true)
}

// igHistoryLoop asks for each chat's earlier messages, a page at a time, while each page brought
// only messages new to the archive.
func (s *liveSession) igHistoryLoop(ctx context.Context) {
	for {
		var h igHistoryOp
		select {
		case <-ctx.Done():
			return
		case h = <-s.igHistory:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(historyPause):
		}
		req := &slidetypes.PaginateMessagesRequest{ThreadID: h.IGID, FirstN: 20, InitialMessagePageCount: 20}
		if !h.First {
			cursor := h.Cursor
			req.AfterCursor = &cursor
		}
		resp, err := s.ig.PaginateMessages(ctx, req)
		if err != nil {
			s.c.Log("{service}: earlier messages not brought ({e})", map[string]any{"service": s.label(), "e": err.Error()})
			continue
		}
		if page := resp.ThreadInfo.AsIGDirectThread; page != nil && page.Messages != nil {
			key, igID, msgs := h.Key, h.IGID, page.Messages
			s.submitRead(ctx, func(o *ops) { s.tr.igPage(o, key, igID, msgs) })
		}
	}
}

// igThreadBackfill asks once for every thread past the inbox's first page.
func (s *liveSession) igThreadBackfill(ctx context.Context) {
	if !s.c.Bool("history") || s.c.State["threads_listed"] == true || s.igCursor == "" {
		return
	}
	cursor := s.igCursor
	for ctx.Err() == nil && cursor != "" {
		select {
		case <-ctx.Done():
			return
		case <-time.After(historyPause):
		}
		resp, err := s.ig.PaginateMailbox(ctx, slidetypes.MakePaginateMailboxRequest(fbid(s.self), cursor, "INBOX", nil))
		if err != nil || resp.Mailbox == nil {
			if err != nil {
				s.c.Log("{service}: the list of chats not brought ({e})", map[string]any{"service": s.label(), "e": err.Error()})
			}
			return
		}
		edges := resp.Mailbox.ThreadsByFolder.Edges
		s.submitRead(ctx, func(o *ops) {
			for _, e := range edges {
				s.tr.igThread(o, e.Node.AsIGDirectThread, false)
			}
		})
		if !resp.Mailbox.ThreadsByFolder.PageInfo.HasNextPage || resp.Mailbox.ThreadsByFolder.PageInfo.EndCursor == cursor {
			break
		}
		cursor = resp.Mailbox.ThreadsByFolder.PageInfo.EndCursor
	}
	if ctx.Err() == nil {
		s.c.SaveState(plugins.M{"threads_listed": true})
	}
}

// submitRead reads (in the loop, where the translator is) and writes, in order with the rest, from
// another goroutine.
func (s *liveSession) submitRead(ctx context.Context, read func(o *ops)) error {
	done := make(chan error, 1)
	s.push(ctx, item{read: read, done: done})
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
