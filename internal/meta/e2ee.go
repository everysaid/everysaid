package meta

// Messenger's end-to-end encrypted chats arrive through whatsmeow (messagix connects it with the
// account's own encryption device): their messages, edits, reactions, unsends and receipts, as the
// archive takes them. The reference is mautrix-meta's pkg/connector/handlewhatsapp.go and events.go,
// and pkg/msgconv/from-whatsapp.go. A one-to-one chat's key is the other person's id, as the
// thread's is; a group's is its own id (its JID's user), which the Facebook thread of the group
// maps to (translator.fbToWA).

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waArmadilloApplication"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMediaTransport"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"everysaid/internal/archive"
	"everysaid/internal/importers"
)

// waFile is an encrypted chat's file as kept to download it: whatsmeow's media type and the
// transport's integral part (its address and keys).
type waFile struct {
	Type     whatsmeow.MediaType `json:"type"`
	Integral []byte              `json:"integral"`
}

// waKey is a chat's key in the archive.
func waKey(chat types.JID) string { return chat.User }

// waMessage reads an encrypted message into o.
func (t *translator) waMessage(o *ops, evt *events.FBMessage) {
	chat := evt.Info.Chat
	key := waKey(chat)
	group := chat.Server == types.GroupServer
	sender := evt.Info.Sender.User
	mine := evt.Info.IsFromMe || sender == t.self
	th := &threadOp{MetaThread: importers.MetaThread{Key: key, Group: group}, Type: table.ENCRYPTED_OVER_WA_ONE_TO_ONE}
	if group {
		th.Type = table.ENCRYPTED_OVER_WA_GROUP
		if !mine {
			th.Members = []importers.MetaUser{{ID: sender}}
		}
	} else if key != t.self {
		th.Members = []importers.MetaUser{{ID: key}}
	}
	o.threads = append(o.threads, th)
	msg := importers.MetaMessage{Thread: key, ID: evt.Info.ID, TS: evt.Info.Timestamp.UnixMilli(), Outgoing: mine, Kind: "text"}
	if !mine {
		msg.From = importers.MetaUser{ID: sender}
	}
	if q := evt.FBApplication.GetMetadata().GetQuotedMessage(); q != nil {
		msg.ReplyTo = q.GetStanzaID()
	}
	n := 0
	file := func(kind string, tr *waMediaTransport.WAMediaTransport, mt whatsmeow.MediaType, name string) {
		if msg.Kind == "text" {
			msg.Kind = kind
		}
		if tr.GetIntegral().GetDirectPath() == "" {
			return
		}
		integral, err := proto.Marshal(tr.GetIntegral())
		if err != nil {
			return
		}
		blob, _ := json.Marshal(waFile{Type: mt, Integral: integral})
		o.files = append(o.files, pendingFile{Thread: key, Message: msg.ID, N: n, WA: blob, Name: name,
			Mime: tr.GetAncillary().GetMimetype(), At: time.Now().Unix()})
		n++
	}
	keep := true
	switch m := evt.Message.(type) {
	case *waConsumerApplication.ConsumerApplication:
		if revoke := m.GetPayload().GetApplicationData().GetRevoke(); revoke != nil {
			o.deletes = append(o.deletes, msgRef{key, revoke.GetKey().GetID()})
			return
		}
		keep = t.waConsumer(o, &msg, m.GetPayload().GetContent(), file)
	case *waArmadilloApplication.Armadillo:
		keep = t.waArmadillo(&msg, m.GetPayload().GetContent(), file)
	default:
		if evt.Message == nil && evt.FBApplication.GetMetadata().GetChatEphemeralSetting() != nil {
			s := int64(evt.FBApplication.GetMetadata().GetChatEphemeralSetting().GetEphemeralExpiration())
			msg.Kind, msg.Subtype, msg.Code = "system", "notice", "meta:ephemeral"
			msg.Notice = &archive.Notice{Code: "timer", Args: map[string]any{"seconds": s}}
		} else {
			msg.Kind, msg.Subtype, msg.Code = "system", "notice", "meta:unsupported"
			msg.Notice = &archive.Notice{Code: "unsupported", Args: map[string]any{}}
		}
	}
	if keep {
		o.messages = append(o.messages, msg)
	}
}

type waFileFunc func(kind string, tr *waMediaTransport.WAMediaTransport, mt whatsmeow.MediaType, name string)

// waConsumer reads a message's content; false: it is not a message of its own (an edit, a reaction).
func (t *translator) waConsumer(o *ops, msg *importers.MetaMessage, c *waConsumerApplication.ConsumerApplication_Content,
	file waFileFunc) bool {
	caption := func(x interface{ GetCaption() *waCommon.MessageText }) {
		if s := x.GetCaption().GetText(); s != "" {
			msg.Text = s
		}
	}
	switch x := c.GetContent().(type) {
	case *waConsumerApplication.ConsumerApplication_Content_MessageText:
		msg.Text = x.MessageText.GetText()
	case *waConsumerApplication.ConsumerApplication_Content_ExtendedTextMessage:
		msg.Text = x.ExtendedTextMessage.GetText().GetText()
		if x.ExtendedTextMessage.GetCanonicalURL() != "" || x.ExtendedTextMessage.GetMatchedText() != "" {
			msg.Subtype = "link"
		}
	case *waConsumerApplication.ConsumerApplication_Content_ImageMessage:
		caption(x.ImageMessage)
		if tr, err := x.ImageMessage.Decode(); err == nil {
			file("image", tr.GetIntegral().GetTransport(), whatsmeow.MediaImage, "")
		} else {
			msg.Kind = "image"
		}
	case *waConsumerApplication.ConsumerApplication_Content_StickerMessage:
		if tr, err := x.StickerMessage.Decode(); err == nil {
			file("sticker", tr.GetIntegral().GetTransport(), whatsmeow.MediaImage, "")
		} else {
			msg.Kind = "sticker"
		}
	case *waConsumerApplication.ConsumerApplication_Content_VideoMessage:
		caption(x.VideoMessage)
		if tr, err := x.VideoMessage.Decode(); err == nil {
			if tr.GetAncillary().GetGifPlayback() {
				msg.Subtype = "gif"
			}
			kind := "video"
			if strings.HasPrefix(tr.GetIntegral().GetTransport().GetAncillary().GetMimetype(), "image/") {
				kind = "image" // Facebook sends pictures in video messages at times
			}
			file(kind, tr.GetIntegral().GetTransport(), whatsmeow.MediaVideo, "")
		} else {
			msg.Kind = "video"
		}
	case *waConsumerApplication.ConsumerApplication_Content_AudioMessage:
		if tr, err := x.AudioMessage.Decode(); err == nil {
			file("voice", tr.GetIntegral().GetTransport(), whatsmeow.MediaAudio, "")
		} else {
			msg.Kind = "voice"
		}
	case *waConsumerApplication.ConsumerApplication_Content_DocumentMessage:
		if tr, err := x.DocumentMessage.Decode(); err == nil {
			file("file", tr.GetIntegral().GetTransport(), whatsmeow.MediaDocument, x.DocumentMessage.GetFileName())
		} else {
			msg.Kind = "file"
		}
	case *waConsumerApplication.ConsumerApplication_Content_ViewOnceMessage:
		msg.Notice = &archive.Notice{Code: "view_once", Args: map[string]any{}}
		msg.Kind = "image"
		if x.ViewOnceMessage.GetVideoMessage() != nil {
			msg.Kind = "video"
		}
	case *waConsumerApplication.ConsumerApplication_Content_LocationMessage:
		l := x.LocationMessage.GetLocation()
		lat, lon := l.GetDegreesLatitude(), l.GetDegreesLongitude()
		msg.Kind, msg.Lat, msg.Lon = "location", &lat, &lon
		msg.Place = strings.TrimSpace(l.GetName() + "\n" + x.LocationMessage.GetAddress())
	case *waConsumerApplication.ConsumerApplication_Content_LiveLocationMessage:
		l := x.LiveLocationMessage.GetLocation()
		lat, lon := l.GetDegreesLatitude(), l.GetDegreesLongitude()
		msg.Kind, msg.Lat, msg.Lon = "location", &lat, &lon
	case *waConsumerApplication.ConsumerApplication_Content_ContactMessage:
		msg.Kind = "contact"
	case *waConsumerApplication.ConsumerApplication_Content_ContactsArrayMessage:
		msg.Kind = "contact"
		msg.Text = x.ContactsArrayMessage.GetDisplayName()
	case *waConsumerApplication.ConsumerApplication_Content_EditMessage:
		o.edits = append(o.edits, editOp{x.EditMessage.GetKey().GetID(), x.EditMessage.GetMessage().GetText()})
		return false
	case *waConsumerApplication.ConsumerApplication_Content_ReactionMessage:
		o.reactions = append(o.reactions, reactOp{Thread: msg.Thread, Message: x.ReactionMessage.GetKey().GetID(),
			By: msg.From, Mine: msg.Outgoing, Emoji: x.ReactionMessage.GetText()})
		return false
	default:
		msg.Kind, msg.Subtype, msg.Code = "system", "notice", "meta:unsupported"
		msg.Notice = &archive.Notice{Code: "unsupported", Args: map[string]any{}}
	}
	return true
}

// waArmadillo reads Messenger's own kinds of encrypted message (shared items, galleries, view
// once).
func (t *translator) waArmadillo(msg *importers.MetaMessage, c *waArmadilloApplication.Armadillo_Content, file waFileFunc) bool {
	switch x := c.GetContent().(type) {
	case *waArmadilloApplication.Armadillo_Content_ExtendedContentMessage:
		e := x.ExtendedContentMessage
		msg.Text = e.GetMessageText()
		for _, cta := range e.GetCtas() {
			u := cta.GetNativeURL()
			if strings.HasPrefix(u, "messenger://location_share") {
				if lat, lon, ok := latLon(u); ok {
					msg.Kind, msg.Lat, msg.Lon = "location", &lat, &lon
					msg.Place = strings.TrimSpace(e.GetTitleText() + "\n" + e.GetSubtitleText())
				}
				return true
			}
			if strings.HasPrefix(u, "https://") && !strings.Contains(msg.Text, u) {
				msg.Subtype = "link"
				msg.Text = strings.TrimSpace(msg.Text + "\n" + u)
			}
		}
		if msg.Text == "" {
			msg.Text = strings.TrimSpace(e.GetTitleText() + "\n" + e.GetSubtitleText())
		}
	case *waArmadilloApplication.Armadillo_Content_ImageGalleryMessage_:
		images, err := x.ImageGalleryMessage.Decode()
		msg.Kind = "image"
		if err == nil {
			for _, img := range images {
				file("image", img.GetIntegral().GetTransport(), whatsmeow.MediaImage, "")
			}
		}
	case *waArmadilloApplication.Armadillo_Content_RavenMessage_, *waArmadilloApplication.Armadillo_Content_RavenMessageMsgr:
		msg.Kind = "image"
		msg.Notice = &archive.Notice{Code: "view_once", Args: map[string]any{}}
	case *waArmadilloApplication.Armadillo_Content_BumpExistingMessage_:
		msg.ReplyTo = x.BumpExistingMessage.GetKey().GetID()
	default:
		msg.Kind, msg.Subtype, msg.Code = "system", "notice", "meta:unsupported"
		msg.Notice = &archive.Notice{Code: "unsupported", Args: map[string]any{}}
	}
	return true
}

// waReceipt reads a receipt: the others' read or got the owner's messages; the owner's own read
// them elsewhere.
func (t *translator) waReceipt(o *ops, evt *events.Receipt) {
	key := waKey(evt.Chat)
	ids := make([]string, len(evt.MessageIDs))
	copy(ids, evt.MessageIDs)
	switch evt.Type {
	case types.ReceiptTypeReadSelf:
		o.ownReadKeys = append(o.ownReadKeys, keysOp{Thread: key, IDs: ids})
	case types.ReceiptTypeRead, types.ReceiptTypeDelivered:
		if evt.IsFromMe || evt.Sender.User == t.self {
			return
		}
		o.readKeys = append(o.readKeys, readKeysOp{Thread: key, By: importers.MetaUser{ID: evt.Sender.User}, IDs: ids,
			At: evt.Timestamp.UnixMilli(), Delivered: evt.Type == types.ReceiptTypeDelivered})
	}
}

type keysOp struct {
	Thread string
	IDs    []string
}

type readKeysOp struct {
	Thread    string
	By        importers.MetaUser
	IDs       []string
	At        int64
	Delivered bool
}

// waJID is the JID of a thread's encrypted chat: a group's, else the person's (Messenger's server).
func waJID(key string, group bool) types.JID {
	if group {
		return types.NewJID(key, types.GroupServer)
	}
	return types.NewJID(key, types.MessengerServer)
}

// fbid is an id as a number (0: not one).
func fbid(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
