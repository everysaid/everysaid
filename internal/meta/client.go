package meta

// The client: messagix (mautrix-meta's Messenger client, third_party/mautrix-meta), behind an
// interface of what the source asks of it, so that the tests put a fake in its place. Its types
// never leave this package.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"

	"everysaid/internal/config"
)

// client is what the source asks of Meta.
type client interface {
	LoadMessagesPage(ctx context.Context) (types.UserInfo, *table.LSTable, error)
	Connect(ctx context.Context) error
	Disconnect()
	SetEventHandler(h messagix.EventHandler)
	ExecuteTasks(ctx context.Context, tasks ...socket.Task) (*table.LSTable, error)
	WaitUntilCanSendMessages(ctx context.Context, timeout time.Duration) error
	DumpState() (json.RawMessage, error)
	LoadState(state json.RawMessage) error
	GetCookies() *cookies.Cookies
	PostHandlePublishResponse(tbl *table.LSTable)
	GetCursor(db int64) string
	FetchMoreThreads(ctx context.Context, syncGroup int64) (*socket.KeyStoreData, *table.LSTable, error)
	// Upload sends a file to a thread's storage; its id, for a message.
	Upload(ctx context.Context, thread int64, name, mime string, data []byte) (int64, error)
	// E2EE is the encrypted chats' client on a device (registered first where new).
	E2EE(ctx context.Context, dev *store.Device, fbid int64, register bool) (*whatsmeow.Client, error)
}

// messagixClient is messagix's client.
type messagixClient struct{ *messagix.Client }

func (m messagixClient) Upload(ctx context.Context, thread int64, name, mime string, data []byte) (int64, error) {
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

func (m messagixClient) E2EE(ctx context.Context, dev *store.Device, fbid int64, register bool) (*whatsmeow.Client, error) {
	if register && dev.FacebookUUID == uuid.Nil {
		if s := m.MessengerLite.GetSuggestedDeviceID(); s != uuid.Nil {
			dev.FacebookUUID = s
		}
	}
	m.SetDevice(dev)
	if register {
		if err := m.RegisterE2EE(ctx, fbid); err != nil {
			return nil, err
		}
	}
	return m.PrepareE2EEClient()
}

// newClient makes a client of the cookies; log takes its library's lines.
var newClient = func(c *cookies.Cookies, log zerolog.Logger) client {
	return messagixClient{messagix.NewClient(c, log, &messagix.Config{ClientSettings: exhttp.SensibleClientSettings})}
}

// libraryLog is where messagix and whatsmeow write what they say (warnings and worse): a file of
// the instance's logs, not its panel.
func libraryLog(iid int64) (zerolog.Logger, io.Closer) {
	dir := filepath.Join(config.Logs, fmt.Sprintf("plugin-%d", iid))
	os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, time.Now().Format("20060102")+"-messagix.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return zerolog.Nop(), io.NopCloser(nil)
	}
	return zerolog.New(f).Level(zerolog.WarnLevel).With().Timestamp().Logger(), f
}
