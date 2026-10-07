// The routes of signing in and of the ways in (app.py, "auth" and "password"), with passkeys made and
// checked by go-webauthn as the Python's py_webauthn did: discoverable (resident key required), user
// verification preferred, the same relying party (the origin's host) and origins, the credential's
// id and COSE public key stored as they come, so that passkeys made by either server sign in on both.
package server

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"everysaid/internal/errs"
)

func newWebAuthn(rpID string, origins []string) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID: rpID, RPDisplayName: "Everysaid", RPOrigins: origins,
		AttestationPreference: protocol.PreferNoAttestation,
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Timeout: 60 * time.Second, TimeoutUVD: 60 * time.Second},
			Registration: webauthn.TimeoutConfig{Timeout: 60 * time.Second, TimeoutUVD: 60 * time.Second},
		},
	})
}

// waUser is a user as go-webauthn sees one.
type waUser struct {
	handle []byte
	name   string
	creds  []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return u.handle }
func (u *waUser) WebAuthnName() string                       { return u.name }
func (u *waUser) WebAuthnDisplayName() string                { return u.name }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// changer is the user a change to the ways in is for: a valid setup link's (nil: a new user; link
// true: the link is what allows it, and is used up by the change), else a session that signed in
// within the last minutes.
func (s *Server) changer(r *http.Request, token string) (uid *int64, link bool, err error) {
	if ok, uid := s.Auth.TakeSetup(token, false); ok {
		return uid, true, nil
	}
	c := cookieOf(r)
	if uid, _, ok := s.Auth.Session(c); ok {
		if s.Auth.Recent(c, 15) {
			return &uid, false, nil
		}
		return nil, false, errs.New("auth.recent_sign_in", 403, nil)
	}
	return nil, false, errs.New("auth.bad_link", 403, nil)
}

func (s *Server) rpID() string { return s.rp }

// noPasskeys is the error of a passkey's step where the origin cannot have passkeys.
func (s *Server) noPasskeys() error {
	if s.wa == nil {
		return errs.New("auth.passkey_failed", 400, M{"reason": s.waErr.Error()})
	}
	return nil
}

func (s *Server) authRoutes() {
	h := s.handle
	h("GET /api/health", bodyNone, func(q *req) (any, error) { return M{"ok": true}, nil })

	h("GET /api/auth/status", bodyNone, func(q *req) (any, error) {
		var user any
		if uid, _, ok := s.Auth.Session(cookieOf(q.r)); ok {
			if u := s.Auth.User(uid); u != nil {
				user = M{"id": u.ID, "name": u.Name}
			}
		}
		return M{"logged_in": user != nil, "user": user, "needs_setup": len(s.Auth.Users()) == 0, "rp_id": s.rpID()}, nil
	})

	// A new passkey: with a setup token (the first user, or a link from `everysaid user link`), or
	// from a signed-in session (another device).
	h("POST /api/auth/register/options", bodyRequired, func(q *req) (any, error) {
		if !s.Auth.Allow(clientIP(q.r), 20, 300) {
			return nil, errs.New("too_many", 429, nil)
		}
		token := q.text("token")
		uid, link, err := s.changer(q.r, token)
		if err != nil {
			return nil, err
		}
		var byLink any // the link that allows it, used up when the passkey is made
		if link {
			byLink = token
		}
		var user *User
		if uid != nil {
			user = s.Auth.User(*uid)
		}
		name := strings.TrimSpace(q.text("name"))
		if name == "" {
			name = "Everysaid"
			if user != nil {
				name = user.Name
			}
		}
		handle := randomBytes(32)
		if user != nil {
			handle = user.Handle
		}
		opts := []webauthn.RegistrationOption{
			webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
				ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: protocol.ResidentKeyRequired(),
				UserVerification: protocol.VerificationPreferred}),
		}
		if uid != nil {
			var ex []protocol.CredentialDescriptor
			for _, id := range s.Auth.passkeyIDs(*uid) {
				ex = append(ex, protocol.CredentialDescriptor{Type: protocol.PublicKeyCredentialType, CredentialID: id})
			}
			opts = append(opts, webauthn.WithExclusions(ex))
		}
		if err := s.noPasskeys(); err != nil {
			return nil, err
		}
		creation, session, err := s.wa.BeginRegistration(&waUser{handle: handle, name: name}, opts...)
		if err != nil {
			return nil, err
		}
		var u any
		if uid != nil {
			u = *uid
		}
		nonce := s.Auth.Challenge(session, "register", M{"uid": u, "name": name, "handle": hex.EncodeToString(handle),
			"token": byLink}, 0)
		return M{"nonce": nonce, "options": creation.Response}, nil
	})

	h("POST /api/auth/register/verify", bodyRequired, func(q *req) (any, error) {
		v, info, ok := s.Auth.TakeChallenge(q.text("nonce"), "register", false)
		if !ok {
			return nil, errs.New("auth.challenge_expired", 400, nil)
		}
		session := v.(*webauthn.SessionData)
		handle, _ := hex.DecodeString(info["handle"].(string))
		credential, _ := q.get("credential").(map[string]any)
		raw, _ := json.Marshal(credential)
		parsed, err := protocol.ParseCredentialCreationResponseBytes(raw)
		var cred *webauthn.Credential
		if err == nil {
			cred, err = s.wa.CreateCredential(&waUser{handle: handle, name: info["name"].(string)}, *session, parsed)
		}
		if err != nil {
			return nil, errs.New("auth.passkey_failed", 400, M{"reason": webauthnReason(err)})
		}
		token, _ := info["token"].(string)
		var uid int64
		var codes any
		// a link is used once: of two passkeys made with one at once, one is kept; and a new user
		// is made only through one
		u, known := info["uid"].(int64)
		if token != "" || !known {
			if ok, _ := s.Auth.TakeSetup(token, true); !ok {
				return nil, errs.New("auth.bad_link", 403, nil)
			}
		}
		if known {
			uid = u
		} else {
			uid = s.Auth.CreateUser(info["name"].(string), s.Store.Path)
			s.Auth.setHandle(uid, handle)
			codes = s.Auth.NewRecoveryCodes(uid)
		}
		var transports any
		if resp, ok := credential["response"].(map[string]any); ok {
			transports = resp["transports"]
		}
		label := q.text("label")
		if label == "" {
			label = pyCut(q.r.Header.Get("User-Agent"), 60)
		}
		s.Auth.AddPasskey(uid, cred.ID, cred.PublicKey, cred.Authenticator.SignCount, transports, label)
		via := "passkey"
		if codes != nil {
			via = "setup"
		}
		s.setCookie(q.w, s.Auth.NewSession(uid, q.r.Header.Get("User-Agent"), clientIP(q.r), via))
		return M{"ok": true, "recovery_codes": codes}, nil
	})

	h("POST /api/auth/login/options", bodyNone, func(q *req) (any, error) {
		if !s.Auth.Allow(clientIP(q.r), 20, 300) {
			return nil, errs.New("too_many", 429, nil)
		}
		if err := s.noPasskeys(); err != nil {
			return nil, err
		}
		assertion, session, err := s.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationPreferred))
		if err != nil {
			return nil, err
		}
		return M{"nonce": s.Auth.Challenge(session, "login", nil, 0), "options": assertion.Response}, nil
	})

	h("POST /api/auth/login/verify", bodyRequired, func(q *req) (any, error) {
		v, _, ok := s.Auth.TakeChallenge(q.text("nonce"), "login", false)
		if !ok {
			return nil, errs.New("auth.challenge_expired", 400, nil)
		}
		credential, _ := q.get("credential").(map[string]any)
		rawID := pyStrOr(credential["rawId"], credential["id"])
		id, err := base64URL(rawID)
		if err != nil {
			return nil, errs.New("auth.unknown_passkey", 403, nil)
		}
		found := s.Auth.passkey(id)
		if found == nil {
			return nil, errs.New("auth.unknown_passkey", 403, nil)
		}
		uid := found.uid
		user := s.Auth.User(uid)
		raw, _ := json.Marshal(credential)
		parsed, err := protocol.ParseCredentialRequestResponseBytes(raw)
		var got *webauthn.Credential
		if err == nil && user != nil {
			// As the Python checked it: the stored key and count; whether the key may be backed up
			// was not stored, so the authenticator's word on it is taken.
			stored := webauthn.Credential{ID: id, PublicKey: found.publicKey,
				Authenticator: webauthn.Authenticator{SignCount: uint32(found.signCount)},
				Flags:         webauthn.CredentialFlags{BackupEligible: parsed.Response.AuthenticatorData.Flags.HasBackupEligible()}}
			session := *(v.(*webauthn.SessionData))
			session.UserID = user.Handle
			got, err = s.wa.ValidateLogin(&waUser{handle: user.Handle, name: user.Name, creds: []webauthn.Credential{stored}}, session, parsed)
			if err == nil && got.Authenticator.CloneWarning {
				err = errors.New("the sign count did not grow: a cloned authenticator?")
			}
		} else if err == nil {
			err = errors.New("no such user")
		}
		if err != nil {
			s.Auth.Log(&uid, "login failed", pyCut(webauthnReason(err), 200))
			return nil, errs.New("auth.passkey_rejected", 403, nil)
		}
		s.Auth.usedPasskey(id, got.Authenticator.SignCount)
		s.setCookie(q.w, s.Auth.NewSession(uid, q.r.Header.Get("User-Agent"), clientIP(q.r), "passkey"))
		return M{"ok": true}, nil
	})

	// --- password with an authenticator code: the way in where a passkey cannot be made ---------

	// A new authenticator secret for setting a password: with a setup link (a new user, or a way
	// back in) or from a signed-in session. The secret is kept only until the code confirms it.
	h("POST /api/auth/password/options", bodyRequired, func(q *req) (any, error) {
		if !s.Auth.Allow(clientIP(q.r), 20, 300) {
			return nil, errs.New("too_many", 429, nil)
		}
		token := q.text("token")
		uid, link, err := s.changer(q.r, token)
		if err != nil {
			return nil, err
		}
		name := ""
		if uid != nil {
			if u := s.Auth.User(*uid); u != nil {
				name = u.Name
			}
		} else {
			name = strings.TrimSpace(q.text("name"))
		}
		if name == "" {
			name = "Everysaid"
		}
		secret := TOTPSecret()
		// long enough to scan the code and choose a password (mistakes do not use it up), and never
		// longer than the setup link it came with
		left := s.Auth.SetupLeft(token)
		ttl := float64(1800)
		var keepToken any
		if link && left > 0 {
			ttl = float64(min(1800, left))
			keepToken = token
		}
		var u any
		if uid != nil {
			u = *uid
		}
		nonce := s.Auth.Challenge(secret, "password", M{"uid": u, "name": name, "token": keepToken}, ttl)
		return M{"nonce": nonce, "secret": secret, "uri": TOTPURI(secret, name), "name": name}, nil
	})

	h("POST /api/auth/password/set", bodyRequired, func(q *req) (any, error) {
		nonce := q.text("nonce")
		v, info, ok := s.Auth.TakeChallenge(nonce, "password", true)
		if !ok {
			return nil, errs.New("auth.setup_expired", 410, nil)
		}
		secret := v.(string)
		password := q.text("password")
		if len([]rune(password)) < 12 {
			return nil, errs.New("auth.password_short", 400, nil)
		}
		step, ok := TOTPCheck(secret, q.text("code"), nil)
		if !ok {
			return nil, errs.New("auth.code_mismatch", 400, nil)
		}
		token, _ := info["token"].(string)
		if token != "" && s.Auth.SetupLeft(token) == 0 {
			return nil, errs.New("auth.link_expired", 403, nil)
		}
		if _, _, ok := s.Auth.TakeChallenge(nonce, "password", false); !ok { // one of two at once gets it
			return nil, errs.New("auth.setup_expired", 410, nil)
		}
		var uid int64
		var codes any
		u, known := info["uid"].(int64)
		if token != "" || !known { // used once, as a passkey's
			if ok, _ := s.Auth.TakeSetup(token, true); !ok {
				return nil, errs.New("auth.bad_link", 403, nil)
			}
		}
		if known {
			uid = u
		} else {
			uid = s.Auth.CreateUser(info["name"].(string), s.Store.Path)
			codes = s.Auth.NewRecoveryCodes(uid)
		}
		s.Auth.SetPassword(uid, password, secret, &step)
		var name any
		if u := s.Auth.User(uid); u != nil {
			name = u.Name
		}
		if _, _, ok := s.Auth.Session(cookieOf(q.r)); !ok {
			s.setCookie(q.w, s.Auth.NewSession(uid, q.r.Header.Get("User-Agent"), clientIP(q.r), "password"))
		}
		return M{"ok": true, "recovery_codes": codes, "name": name}, nil
	})

	h("POST /api/auth/password/login", bodyRequired, func(q *req) (any, error) {
		if !s.Auth.Allow(clientIP(q.r), 10, 300) {
			return nil, errs.New("too_many", 429, nil)
		}
		uid := s.Auth.CheckPassword(q.text("name"), q.text("password"), q.text("code"), clientIP(q.r))
		if uid == 0 {
			return nil, errs.New("auth.bad_login", 403, nil)
		}
		s.setCookie(q.w, s.Auth.NewSession(uid, q.r.Header.Get("User-Agent"), clientIP(q.r), "password"))
		return M{"ok": true}, nil
	})

	h("DELETE /api/auth/password", bodyNone, func(q *req) (any, error) {
		if _, _, err := s.changer(q.r, ""); err != nil {
			return nil, err
		}
		if err := s.Auth.ClearPassword(q.uid); err != nil {
			return nil, err
		}
		return M{"ok": true}, nil
	})

	h("POST /api/auth/recover", bodyRequired, func(q *req) (any, error) {
		if !s.Auth.Allow(clientIP(q.r), 5, 300) {
			return nil, errs.New("too_many", 429, nil)
		}
		uid := s.Auth.UseRecoveryCode(q.text("code"))
		if uid == 0 {
			return nil, errs.New("auth.bad_recovery", 403, nil)
		}
		s.setCookie(q.w, s.Auth.NewSession(uid, q.r.Header.Get("User-Agent"), clientIP(q.r), "recovery"))
		return M{"ok": true, "left": s.Auth.RecoveryLeft(uid)}, nil
	})

	h("POST /api/auth/logout", bodyNone, func(q *req) (any, error) {
		s.Auth.EndSessionHash(q.session)
		http.SetCookie(q.w, &http.Cookie{Name: Cookie, Value: "", MaxAge: -1, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteStrictMode, Secure: s.https})
		return M{"ok": true}, nil
	})

	h("GET /api/auth/account", bodyNone, func(q *req) (any, error) {
		u := s.Auth.User(q.uid)
		if u == nil {
			return nil, notFound("")
		}
		sessions := s.Auth.Sessions(q.uid)
		for _, x := range sessions {
			x["current"] = strings.HasPrefix(q.session, x["id"].(string))
		}
		return M{"user": M{"id": u.ID, "name": u.Name, "archive": u.Archive}, "passkeys": s.Auth.Passkeys(q.uid),
			"sessions": sessions, "recovery_left": s.Auth.RecoveryLeft(q.uid), "audit": s.Auth.Audit(q.uid, 50),
			"has_password": s.Auth.HasPassword(q.uid), "mcp_tokens": s.Auth.MCPTokens(q.uid)}, nil
	})

	h("DELETE /api/auth/sessions/{sid}", bodyNone, func(q *req) (any, error) {
		s.Auth.EndSession(q.uid, q.r.PathValue("sid"))
		return M{"ok": true}, nil
	})

	h("DELETE /api/auth/passkeys/{pid}", bodyNone, func(q *req) (any, error) {
		if _, _, err := s.changer(q.r, ""); err != nil {
			return nil, err
		}
		err := s.Auth.RemovePasskey(q.uid, q.r.PathValue("pid"))
		if err != nil {
			return nil, passUser(err, func(e error) error { return failed(400, e.Error()) })
		}
		return M{"ok": true}, nil
	})

	h("PATCH /api/auth/passkeys/{pid}", bodyRequired, func(q *req) (any, error) {
		if err := s.Auth.RenamePasskey(q.uid, q.r.PathValue("pid"), pyCut(q.text("name"), 80)); err != nil {
			return nil, failed(400, err.Error())
		}
		return M{"ok": true}, nil
	})

	h("POST /api/auth/recovery-codes", bodyNone, func(q *req) (any, error) {
		if _, _, err := s.changer(q.r, ""); err != nil { // codes are a way in
			return nil, err
		}
		return M{"codes": s.Auth.NewRecoveryCodes(q.uid)}, nil
	})

	h("POST /api/auth/mcp-token", bodyRequired, func(q *req) (any, error) {
		if _, _, err := s.changer(q.r, ""); err != nil { // a lasting key to the archive
			return nil, err
		}
		label := q.text("label")
		if label == "" {
			label = "MCP"
		}
		return M{"token": s.Auth.NewMCPToken(q.uid, pyCut(label, 60))}, nil
	})

	h("DELETE /api/auth/mcp-tokens/{tid}", bodyNone, func(q *req) (any, error) {
		if _, _, err := s.changer(q.r, ""); err != nil { // as making one: a stolen session cannot cut the user's own off
			return nil, err
		}
		if !s.Auth.RevokeMCPToken(q.uid, q.r.PathValue("tid")) {
			return nil, notFound("")
		}
		return M{"ok": true}, nil
	})
}

// webauthnReason is what a passkey's failure says, for the log and the error's reason.
func webauthnReason(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		if pe.DevInfo != "" {
			return pe.Details + ": " + pe.DevInfo
		}
		return pe.Details
	}
	return err.Error()
}

// base64URL reads base64url with or without padding.
func base64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// pyStrOr is (a or b) of two JSON values, as text.
func pyStrOr(a, b any) string {
	if truthy(a) {
		if s, ok := a.(string); ok {
			return s
		}
	}
	s, _ := b.(string)
	return s
}
