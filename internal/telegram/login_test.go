package telegram

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/gotd/td/telegram/auth"

	"everysaid/internal/errs"
	"everysaid/internal/plugins"
)

// answering answers what the instance asks, as the interface would, each key with its answer
// (give up: the user gave it up); it gives the keys asked, in order, once the login is over.
func answering(t *testing.T, iid int64, answers map[string]string, giveUp string) func() []string {
	stop := make(chan struct{})
	done := make(chan []string)
	go func() {
		var asked []string
		for {
			select {
			case <-stop:
				done <- asked
				return
			case <-time.After(5 * time.Millisecond):
			}
			q := plugins.Asking(iid, "en")
			if q == nil {
				continue
			}
			key := q["key"].(string)
			asked = append(asked, key)
			plugins.Answer(iid, key, answers[key], key == giveUp)
		}
	}()
	return func() []string { close(stop); return <-done }
}

// The login from the interface asks the api_id and api_hash where they are not kept (and keeps
// them), then what Telegram's login asks: the phone, the code, the password; given up, it ends.
func TestLoginFromTheInterface(t *testing.T) {
	values := fakeSecrets(t, map[string]string{})
	in := newInstance(t, M{})
	old := loginStart
	t.Cleanup(func() { loginStart = old })
	loginStart = func(ctx context.Context, c *plugins.Context, flow auth.Flow) (string, error) {
		phone, err := flow.Auth.Phone(ctx)
		if err != nil {
			return "", err
		}
		code, err := flow.Auth.Code(ctx, nil)
		if err != nil {
			return "", err
		}
		pw, err := flow.Auth.Password(ctx)
		if err != nil {
			return "", err
		}
		if phone != "+15550001" || code != "12345" || pw != "secret" {
			t.Errorf("given %q %q %q", phone, code, pw)
		}
		return "Anna", nil
	}
	answers := map[string]string{"api_id": "123", "api_hash": "abc", "phone": "+15550001", "code": " 12345 ", "password": "secret"}
	asked := answering(t, in.iid, answers, "")
	if err := login(context.Background(), in.ctx()); err != nil {
		t.Fatal(err)
	}
	if got := asked(); !slices.Equal(got, []string{"api_id", "api_hash", "phone", "code", "password"}) {
		t.Fatalf("asked %v", got)
	}
	if values[SecretAPIID] != "123" || values[SecretAPIHash] != "abc" {
		t.Fatalf("kept %v", values)
	}
	if plugins.Asking(in.iid, "en") != nil {
		t.Fatal("still asking")
	}

	// the credentials kept: not asked again; the code given up ends the login
	asked = answering(t, in.iid, answers, "code")
	err := login(context.Background(), in.ctx())
	var ue *errs.UserError
	if !errors.As(err, &ue) || ue.Text != "The login was given up" {
		t.Fatalf("given up: %v", err)
	}
	if got := asked(); !slices.Equal(got, []string{"phone", "code"}) {
		t.Fatalf("asked %v", got)
	}
}
