package telegram

// GIFs found by Telegram's own @gif bot (an inline query: nothing is sent), for any service to send.

import (
	"bytes"
	"context"
	"strconv"
	"sync"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"

	"everysaid/internal/plugins"
)

const (
	gifBot      = "gif"
	gifNotFound = "That GIF is no longer in the results: search again"
	noGIFBot    = "Telegram did not give its @gif bot"
	gifsKept    = 500      // the documents of the last results kept, to fetch one of them
	gifBytes    = 64 << 20 // the files kept in memory, for the results shown again
)

// gifs are the last results' documents and files: a GIF found is asked for by its id, shown then sent.
var gifs = struct {
	sync.Mutex
	bot   *tg.InputUser
	docs  map[string]*tg.Document
	order []string
	files map[string][]byte
	held  []string
	size  int
}{docs: map[string]*tg.Document{}, files: map[string][]byte{}}

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
			m, ok := r.(*tg.BotInlineMediaResult)
			if !ok {
				continue
			}
			doc, ok := m.Document.(*tg.Document)
			if !ok || doc.MimeType != "video/mp4" {
				continue
			}
			g := plugins.GIF{ID: strconv.FormatInt(doc.ID, 10)}
			for _, a := range doc.Attributes {
				if v, ok := a.(*tg.DocumentAttributeVideo); ok {
					g.Width, g.Height = v.W, v.H
				}
			}
			if _, ok := gifs.docs[g.ID]; !ok {
				gifs.order = append(gifs.order, g.ID)
			}
			gifs.docs[g.ID] = doc
			out = append(out, g)
		}
		for len(gifs.order) > gifsKept {
			delete(gifs.docs, gifs.order[0])
			gifs.order = gifs.order[1:]
		}
		return nil
	})
	return out, next, err
}

func (Plugin) GIF(ctx context.Context, c *plugins.Context, id string) (*plugins.File, error) {
	gifs.Lock()
	doc, data := gifs.docs[id], gifs.files[id]
	gifs.Unlock()
	if doc == nil {
		return nil, pluginErr(gifNotFound)
	}
	if data == nil {
		err := withConn(ctx, func(ctx context.Context, cn *conn) error {
			var b bytes.Buffer
			loc := &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference}
			if _, err := downloader.NewDownloader().Download(cn.api, loc).Stream(ctx, &b); err != nil {
				return err
			}
			data = b.Bytes()
			return nil
		})
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
