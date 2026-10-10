package whatsapp

// whatsmeow's device store (the session's keys) is in keys.db (internal/keys), beside the other
// services' secrets, in whatsmeow's own tables (whatsmeow_*). A store folder (a source's instance)
// names its device there: whatsapp_device, the folder and the device's JID, written once the device
// is linked.

import (
	"context"
	"database/sql"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	"everysaid/internal/keys"
)

const devicesSchema = `
CREATE TABLE IF NOT EXISTS whatsapp_device (   -- which linked device each store folder is
    store TEXT PRIMARY KEY,                     -- the store folder, its absolute path
    jid TEXT NOT NULL                           -- the device's JID (whatsmeow_device.jid)
);`

// Devices is keys.db with whatsmeow's tables brought up to date and whatsapp_device made.
func Devices(ctx context.Context) (*sql.DB, error) {
	d, err := keys.Open()
	if err != nil {
		return nil, err
	}
	if err := sqlstore.NewWithDB(d, keys.Driver, waLog.Noop).Upgrade(ctx); err != nil {
		return nil, err
	}
	if _, err := d.Exec(devicesSchema); err != nil {
		return nil, err
	}
	return d, nil
}

// OpenDevices is whatsmeow's container over keys.db. It shares keys.db's connections: never Close it.
func OpenDevices(ctx context.Context, log waLog.Logger) (*sqlstore.Container, error) {
	if log == nil {
		log = waLog.Noop
	}
	d, err := Devices(ctx)
	if err != nil {
		return nil, err
	}
	return sqlstore.NewWithDB(d, keys.Driver, log), nil
}

// DeviceJID is the JID of the device a store folder linked ("" if none yet).
func DeviceJID(d *sql.DB, dir string) string {
	var jid string
	d.QueryRow("SELECT jid FROM whatsapp_device WHERE store = ?", key(dir)).Scan(&jid)
	return jid
}

// device is the store folder's device in the container: its linked one, or a new one to link.
func device(ctx context.Context, c *sqlstore.Container, d *sql.DB, dir string) (*store.Device, error) {
	if jid := DeviceJID(d, dir); jid != "" {
		parsed, err := types.ParseJID(jid)
		if err != nil {
			return nil, err
		}
		dev, err := c.GetDevice(ctx, parsed)
		if err != nil {
			return nil, err
		}
		if dev != nil {
			return dev, nil
		}
	}
	return c.NewDevice(), nil
}

// recordDevice notes the device a store folder linked.
func recordDevice(d *sql.DB, dir string, jid types.JID) error {
	_, err := d.Exec("INSERT INTO whatsapp_device (store, jid) VALUES (?, ?) ON CONFLICT (store) DO UPDATE SET jid = excluded.jid",
		key(dir), jid.String())
	return err
}

// HasDevice says whether a store folder has a linked device (false when there is none, or keys.db
// cannot be opened).
func HasDevice(dir string) bool {
	d, err := keys.Open()
	if err != nil {
		return false
	}
	var n int
	err = d.QueryRow("SELECT count(*) FROM whatsapp_device w JOIN whatsmeow_device d ON d.jid = w.jid WHERE w.store = ?",
		key(dir)).Scan(&n)
	return err == nil && n > 0
}
