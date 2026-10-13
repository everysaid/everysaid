package telegram

// GIFs found by Telegram's own @gif bot (an inline query: nothing is sent), for any service to send.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"

	"everysaid/internal/netsafe"
	"everysaid/internal/plugins"
)

const (
	gifBot      = "gif"
	gifNotFound = "That GIF is no longer in the results: search again"
	noGIFBot    = "Telegram did not give its @gif bot"
	gifsKept    = 500      // the documents of the last results kept, to fetch one of them
	gifBytes    = 64 << 20 // the files kept in memory, for the results shown again
)

// gifs are the last results' files where they are and those fetched: a GIF found is asked for by
// its id, shown then sent.
var gifs = struct {
	sync.Mutex
	bot   *tg.InputUser
	found map[string]gifAt
	order []string
	files map[string][]byte
	held  []string
	size  int
}{found: map[string]gifAt{}, files: map[string][]byte{}}

// gifAt is where a GIF found is: a file on Telegram (one sent before), else on the web, which
// Telegram fetches (proxied: accessHash) or which is fetched from it.
type gifAt struct {
	doc        *tg.Document
	url        string
	accessHash int64
	proxied    bool
}

// gifOf is a result as a GIF and where it is; false for one that is no video.
func gifOf(r tg.BotInlineResultClass) (plugins.GIF, gifAt, bool) {
	var g plugins.GIF
	var at gifAt
	var attrs []tg.DocumentAttributeClass
	switch r := r.(type) {
	case *tg.BotInlineMediaResult: // a file already on Telegram
		doc, ok := r.Document.(*tg.Document)
		if !ok || doc.MimeType != "video/mp4" {
			return g, at, false
		}
		g.ID, at.doc, attrs = strconv.FormatInt(doc.ID, 10), doc, doc.Attributes
	case *tg.BotInlineResult: // one on the web, as for words rarely asked
		switch d := r.Content.(type) {
		case *tg.WebDocument:
			if d.MimeType != "video/mp4" {
				return g, at, false
			}
			at.url, at.accessHash, at.proxied, attrs = d.URL, d.AccessHash, true, d.Attributes
		case *tg.WebDocumentNoProxy:
			if d.MimeType != "video/mp4" {
				return g, at, false
			}
			at.url, attrs = d.URL, d.Attributes
		default:
			return g, at, false
		}
		sum := sha256.Sum256([]byte(at.url))
		g.ID = "w" + hex.EncodeToString(sum[:12])
	default:
		return g, at, false
	}
	for _, a := range attrs {
		if v, ok := a.(*tg.DocumentAttributeVideo); ok {
			g.Width, g.Height = v.W, v.H
		}
	}
	return g, at, true
}

func (Plugin) NotSearching(c *plugins.Context) string {
	if _, _, ok := credentials(); !ok || !hasSession() {
		return notSignedIn
	}
	return ""
}

func (Plugin) SearchGIFs(ctx context.Context, c *plugins.Context, query, offset string) (out []plugins.GIF, next string, err error) {
	err = withConn(ctx, func(ctx context.Context, cn *conn) error {
		bot, err := gifBotOf(ctx, cn)
		if err != nil {
			return err
		}
		res, err := cn.api.MessagesGetInlineBotResults(ctx, &tg.MessagesGetInlineBotResultsRequest{
			Bot: bot, Peer: &tg.InputPeerSelf{}, Query: query, Offset: offset})
		if err != nil {
			return err
		}
		next = res.NextOffset
		gifs.Lock()
		defer gifs.Unlock()
		for _, r := range res.Results {
			g, at, ok := gifOf(r)
			if !ok {
				continue
			}
			if _, ok := gifs.found[g.ID]; !ok {
				gifs.order = append(gifs.order, g.ID)
			}
			gifs.found[g.ID] = at
			out = append(out, g)
		}
		for len(gifs.order) > gifsKept {
			delete(gifs.found, gifs.order[0])
			gifs.order = gifs.order[1:]
		}
		return nil
	})
	return out, next, err
}

func (Plugin) GIF(ctx context.Context, c *plugins.Context, id string) (*plugins.File, error) {
	gifs.Lock()
	at, found := gifs.found[id]
	data := gifs.files[id]
	gifs.Unlock()
	if !found {
		return nil, pluginErr(gifNotFound)
	}
	if data == nil {
		var err error
		if at.doc == nil && !at.proxied {
			data, err = netsafe.Video(ctx, at.url, "Everysaid")
		} else {
			err = withConn(ctx, func(ctx context.Context, cn *conn) error {
				var b bytes.Buffer
				d := downloader.NewDownloader()
				var get *downloader.Builder
				if at.doc != nil {
					get = d.Download(cn.api, &tg.InputDocumentFileLocation{ID: at.doc.ID, AccessHash: at.doc.AccessHash,
						FileReference: at.doc.FileReference})
				} else {
					get = d.Web(cn.api, &tg.InputWebFileLocation{URL: at.url, AccessHash: at.accessHash})
				}
				if _, err := get.Stream(ctx, &b); err != nil {
					return err
				}
				data = b.Bytes()
				return nil
			})
		}
		if err != nil {
			return nil, err
		}
		gifs.Lock()
		if _, ok := gifs.files[id]; !ok {
			gifs.files[id], gifs.held, gifs.size = data, append(gifs.held, id), gifs.size+len(data)
			for gifs.size > gifBytes && len(gifs.held) > 1 {
				gifs.size -= len(gifs.files[gifs.held[0]])
				delete(gifs.files, gifs.held[0])
				gifs.held = gifs.held[1:]
			}
		}
		gifs.Unlock()
	}
	return &plugins.File{Data: data, Filename: "gif-" + id + ".mp4", MimeType: "video/mp4", Animated: true}, nil
}

// gifBotOf is @gif as the account reaches it (asked once).
func gifBotOf(ctx context.Context, cn *conn) (*tg.InputUser, error) {
	gifs.Lock()
	bot := gifs.bot
	gifs.Unlock()
	if bot != nil {
		return bot, nil
	}
	r, err := cn.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: gifBot})
	if err != nil {
		return nil, err
	}
	for _, u := range r.Users {
		if u, ok := u.(*tg.User); ok && u.Bot {
			bot = &tg.InputUser{UserID: u.ID, AccessHash: u.AccessHash}
		}
	}
	if bot == nil {
		return nil, pluginErr(noGIFBot)
	}
	gifs.Lock()
	gifs.bot = bot
	gifs.Unlock()
	return bot, nil
}
