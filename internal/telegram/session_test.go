package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net"
	"testing"

	"github.com/gotd/td/session"

	"everysaid/internal/i18n"
)

// fakeSecrets puts a map in place of the keyring for a test.
func fakeSecrets(t *testing.T, values map[string]string) map[string]string {
	old := secrets
	t.Cleanup(func() { secrets = old })
	secrets.get = func(name string) string { return values[name] }
	secrets.save = func(name, value string) (string, error) {
		values[name] = value
		return "keyring", nil
	}
	return values
}

// telethonString is a StringSession as Telethon writes it: '1' + urlsafe base64 of
// (dc id, IPv4, port, auth key), packed '>B4sH256s'.
func telethonString(dc byte, ip net.IP, port uint16, key []byte) string {
	var b bytes.Buffer
	b.WriteByte(dc)
	b.Write(ip.To4())
	binary.Write(&b, binary.BigEndian, port)
	b.Write(key)
	return "1" + base64.URLEncoding.EncodeToString(b.Bytes())
}

func TestFromTelethon(t *testing.T) {
	key := make([]byte, 256)
	for i := range key {
		key[i] = byte(i * 7)
	}
	values := fakeSecrets(t, map[string]string{SecretTelethon: telethonString(2, net.IPv4(149, 154, 167, 51), 443, key)})
	store := &keyringSession{}
	raw, err := store.LoadSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Version int
		Data    session.Data
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 || v.Data.DC != 2 || v.Data.Addr != "149.154.167.51:443" || !bytes.Equal(v.Data.AuthKey, key) || len(v.Data.AuthKeyID) != 8 {
		t.Fatalf("%+v", v)
	}
	// gotd's loader takes it as its own
	data, err := (&session.Loader{Storage: store}).Load(context.Background())
	if err != nil || data.DC != 2 {
		t.Fatal(err)
	}
	// stored apart: Telethon's stays as it was
	before := values[SecretTelethon]
	if err := (&session.Loader{Storage: store}).Save(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	if values[SecretTelethon] != before || values[SecretSession] == "" || store.where() != "keyring" {
		t.Fatal("stored where it should not be")
	}
	if got, _ := store.LoadSession(context.Background()); string(got) != values[SecretSession] {
		t.Fatal("gotd's own is read first")
	}
}

func TestNoSession(t *testing.T) {
	fakeSecrets(t, map[string]string{})
	if _, err := (&keyringSession{}).LoadSession(context.Background()); err != session.ErrNotFound {
		t.Fatal(err)
	}
	if hasSession() {
		t.Fatal("no session")
	}
	if _, err := FromTelethon("2abc"); err == nil {
		t.Fatal("a wrong version is refused")
	}
}

func TestCheck(t *testing.T) {
	values := fakeSecrets(t, map[string]string{})
	var p Plugin
	if ok, why := p.Check(nil); ok || why != "missing: api_id and api_hash (everysaid telegram-sync --save-credentials)" {
		t.Fatal(why)
	}
	values[SecretAPIID], values[SecretAPIHash] = "123", "abc"
	if ok, why := p.Check(nil); ok || why != "missing: a login (everysaid telegram-sync --login)" {
		t.Fatal(why)
	} else if el := i18n.Tr(why, "el"); el != "λείπει: η σύνδεση (everysaid telegram-sync --login)" {
		t.Fatal(el)
	}
	if el := i18n.Tr(notSignedIn, "el"); el == notSignedIn || i18n.Tr(expired, "el") == expired {
		t.Fatal("untranslated")
	}
	values[SecretTelethon] = "1x"
	if ok, why := p.Check(nil); !ok || why != "ready" {
		t.Fatal(why)
	}
}
