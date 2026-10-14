package telegram

// The login from the interface (the source's action "Log in"), what `telegram-sync --login` does on
// the terminal: the api_id and api_hash where they are not kept yet, then the phone, the code
// Telegram sends (to the account's other devices, or by SMS) and, where the account has one, its
// two-step verification password, each asked as the login reaches it (Context.AskNow). The
// password is kept nowhere; the session made is kept as the terminal's is.

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"

	"everysaid/internal/errs"
	"everysaid/internal/plugins"
)

// loginStart is the login's own client (tests stand in for it).
var loginStart = func(ctx context.Context, c *plugins.Context, flow auth.Flow) (string, error) {
	client, err := newClient(syncThreshold, &keyringSession{}, nil)
	if err != nil {
		return "", err
	}
	var name string
	err = client.Run(ctx, func(ctx context.Context) error {
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return err
		}
		me, err := client.Self(ctx)
		if err == nil {
			name = strings.TrimSpace(me.FirstName + " " + me.LastName)
		}
		return err
	})
	return name, err
}

func login(ctx context.Context, c *plugins.Context) error {
	if _, _, ok := credentials(); !ok {
		id, err := c.AskNow(ctx, "api_id", "api_id (from my.telegram.org, API development tools)", false)
		if err != nil {
			return err
		}
		hash, err := c.AskNow(ctx, "api_hash", "api_hash (from my.telegram.org)", true)
		if err != nil {
			return err
		}
		id, hash = strings.TrimSpace(id), strings.TrimSpace(hash)
		if id == "" || hash == "" || strings.ContainsFunc(id, func(r rune) bool { return !unicode.IsDigit(r) }) {
			return errs.Plugin("api_id must be a number and api_hash not empty", 0)
		}
		if _, err := secrets.save(SecretAPIID, id); err != nil {
			return err
		}
		if _, err := secrets.save(SecretAPIHash, hash); err != nil {
			return err
		}
	}
	// a connection open here (the live one) is a session that works; else the login's own client,
	// which asks only what the session lacks (nothing where it still works, all where it expired)
	open, done, err := one.take(ctx, true, nil)
	if errors.Is(err, errHeld) {
		return errs.Plugin(heldElsewhere, 0)
	}
	if err != nil {
		return err
	}
	defer done()
	if open != nil {
		return errs.Plugin("Already logged in to Telegram", 0)
	}
	name, err := loginStart(ctx, c, auth.NewFlow(askAuth{ctx, c}, auth.SendCodeOptions{}))
	if errors.Is(err, plugins.ErrNotAnswered) {
		return errs.Plugin("The login was given up", 0)
	}
	if err != nil {
		return err
	}
	c.Log("Logged in to Telegram as {name}", map[string]any{"name": name})
	return nil
}

// askAuth asks the interface what the login needs, as terminalAuth asks the terminal.
type askAuth struct {
	ctx context.Context
	c   *plugins.Context
}

func (a askAuth) Phone(context.Context) (string, error) {
	return a.c.AskNow(a.ctx, "phone", "Your phone number, with the country's code", false)
}

func (a askAuth) Code(context.Context, *tg.AuthSentCode) (string, error) {
	code, err := a.c.AskNow(a.ctx, "code", "The code Telegram sent (in Telegram on your other devices, or by SMS)", false)
	return strings.TrimSpace(code), err
}

func (a askAuth) Password(context.Context) (string, error) {
	return a.c.AskNow(a.ctx, "password", "Your two-step verification password (Telegram's Settings, Privacy and Security)", true)
}

func (askAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error { return nil }

func (askAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errs.Plugin("This phone number has no Telegram account", 0)
}
