package loginstub

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rs/zerolog"
	"net/http"
	"regexp"
	"strings"
)

type RespError struct {
	ErrCode       string
	Err           string
	StatusCode    int
	InternalError string
}

func (re RespError) Error() string { return re.Err }
func (re RespError) Is(err error) bool {
	var e2 RespError
	return errors.As(err, &e2) && e2.Err == re.Err
}
func (re RespError) WithInternalError(err error) RespError { re.InternalError = err.Error(); return re }
func (re RespError) WithMessage(msg string, args ...any) RespError {
	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	re.Err = msg
	return re
}
func (re RespError) AppendMessage(append string, args ...any) RespError {
	re.Err += fmt.Sprintf(append, args...)
	return re
}

var ErrLoginStepCancelled = RespError{ErrCode: "FI.MAU.LOGIN_STEP_CANCELLED", Err: "Login step was cancelled", StatusCode: http.StatusConflict}
var _ = zerolog.Nop

type LoginStepType string

const (
	LoginStepTypeUserInput      LoginStepType = "user_input"
	LoginStepTypeCookies        LoginStepType = "cookies"
	LoginStepTypeClientHTTP     LoginStepType = "client_http"
	LoginStepTypeDisplayAndWait LoginStepType = "display_and_wait"
	LoginStepTypeWebAuthn       LoginStepType = "webauthn"
	LoginStepTypeComplete       LoginStepType = "complete"
)

type LoginDisplayType string

const (
	LoginDisplayTypeQR      LoginDisplayType = "qr"
	LoginDisplayTypeEmoji   LoginDisplayType = "emoji"
	LoginDisplayTypeCode    LoginDisplayType = "code"
	LoginDisplayTypeNothing LoginDisplayType = "nothing"
)

type LoginStep struct {
	// The type of login step
	Type LoginStepType `json:"type"`
	// A unique ID for this step. The ID should be same for every login using the same flow,
	// but it should be different for different bridges and step types.
	//
	// For example, Telegram's QR scan followed by a 2-factor password
	// might use the IDs `fi.mau.telegram.qr` and `fi.mau.telegram.2fa_password`.
	StepID string `json:"step_id"`
	// A unique ID for submitting the step. This is randomly generated for every step.
	// It's used to allow safely retrying requests.
	TxnID string `json:"txn_id"`
	// Instructions contains human-readable instructions for completing the login step.
	Instructions string `json:"instructions"`

	// Exactly one of the following structs must be filled depending on the step type.

	DisplayAndWaitParams *LoginDisplayAndWaitParams `json:"display_and_wait,omitempty"`
	CookiesParams        *LoginCookiesParams        `json:"cookies,omitempty"`
	ClientHTTPParams     *LoginClientHTTPParams     `json:"client_http,omitempty"`
	UserInputParams      *LoginUserInputParams      `json:"user_input,omitempty"`
	WebAuthnParams       *LoginWebAuthnParams       `json:"webauthn,omitempty"`
	CompleteParams       *LoginCompleteParams       `json:"complete,omitempty"`
}

func (ls *LoginStep) MarshalZerologObject(e *zerolog.Event) {
	e.Str("type", string(ls.Type)).
		Str("step_id", ls.StepID).
		Str("txn_id", ls.TxnID).
		Str("instructions", ls.Instructions)
	if ls.DisplayAndWaitParams != nil {
		e.Any("display_and_wait", ls.DisplayAndWaitParams)
	}
	if ls.CookiesParams != nil {
		e.Any("cookies", ls.CookiesParams)
	}
	if ls.ClientHTTPParams != nil {
		e.Object("client_http", ls.ClientHTTPParams)
	}
	if ls.UserInputParams != nil {
		e.Any("user_input", ls.UserInputParams)
	}
	if ls.WebAuthnParams != nil {
		e.Any("webauthn", ls.WebAuthnParams)
	}
	if ls.CompleteParams != nil {
		e.Any("complete", ls.CompleteParams)
	}
}

type LoginClientHTTPParams struct {
	RequestID string      `json:"request_id"`
	Method    string      `json:"method"`
	URL       string      `json:"url"`
	Headers   http.Header `json:"headers,omitempty"`
	Body      []byte      `json:"body,omitempty"`
}

func (lchp *LoginClientHTTPParams) MarshalZerologObject(e *zerolog.Event) {
	e.Str("request_id", lchp.RequestID).
		Str("method", lchp.Method).
		Str("url", lchp.URL).
		Int("header_count", len(lchp.Headers)).
		Int("body_length", len(lchp.Body))
}

type LoginClientHTTPResponse struct {
	StatusCode int         `json:"status_code,omitzero"`
	FinalURL   string      `json:"final_url,omitempty"`
	Headers    http.Header `json:"headers,omitempty"`
	Body       []byte      `json:"body,omitempty"`
	Error      string      `json:"error,omitempty"`
}

func (lchr *LoginClientHTTPResponse) IsValid() bool {
	if lchr.Error != "" {
		return true
	}
	return lchr.StatusCode >= 100 && lchr.StatusCode <= 599
}

type LoginWebAuthnParams struct {
	// The origin URL where the credential should be extracted from.
	URL string `json:"url,omitempty"`

	// Standard parameters for https://developer.mozilla.org/en-US/docs/Web/API/CredentialsContainer/get
	// Only publicKey seems to be used in practice, so the other options aren't allowed yet.
	PublicKey json.RawMessage `json:"publicKey,omitempty"`
}

type LoginDisplayAndWaitParams struct {
	// The type of thing to display (QR, emoji or text code)
	Type LoginDisplayType `json:"type"`
	// The thing to display (raw data for QR, unicode emoji for emoji, plain string for code, omitted for nothing)
	Data string `json:"data,omitempty"`
	// An image containing the thing to display. If present, this is recommended over using data directly.
	// For emojis, the URL to the canonical image representation of the emoji
	ImageURL string `json:"image_url,omitempty"`
	// If set, the client may allow the user to go back from this step. The
	// context passed to Wait will be cancelled with ErrLoginStepCancelled.
	CanCancel bool `json:"can_cancel,omitzero"`
}

type LoginCookieFieldSourceType string

const (
	LoginCookieTypeCookie        LoginCookieFieldSourceType = "cookie"
	LoginCookieTypeLocalStorage  LoginCookieFieldSourceType = "local_storage"
	LoginCookieTypeRequestHeader LoginCookieFieldSourceType = "request_header"
	LoginCookieTypeRequestBody   LoginCookieFieldSourceType = "request_body"
	LoginCookieTypeSpecial       LoginCookieFieldSourceType = "special"
)

type LoginCookieFieldSource struct {
	// The type of source.
	Type LoginCookieFieldSourceType `json:"type"`
	// The name of the field. The exact meaning depends on the type of source.
	// Cookie:         cookie name
	// Local storage:  key in local storage
	// Request header: header name
	// Request body:   field name inside body after it's parsed (as JSON or multipart form data)
	// Special:        a namespaced identifier that clients can implement special handling for
	Name string `json:"name"`

	// For request header & body types, a regex matching request URLs where the value can be extracted from.
	RequestURLRegex string `json:"request_url_regex,omitempty"`
	// For cookie types, the domain the cookie is present on.
	CookieDomain string `json:"cookie_domain,omitempty"`
}

type LoginCookieField struct {
	// The key in the map that is submitted to the connector.
	ID       string `json:"id"`
	Required bool   `json:"required"`
	// The sources that can be used to acquire the field value. Only one of these needs to be used.
	Sources []LoginCookieFieldSource `json:"sources"`
	// A regex pattern that the client can use to validate value client-side.
	Pattern string `json:"pattern,omitempty"`
}

type LoginCookiesParams struct {
	URL       string `json:"url"`
	UserAgent string `json:"user_agent,omitempty"`

	// The fields that are needed for this cookie login.
	Fields []LoginCookieField `json:"fields"`
	// A JavaScript snippet that can extract some or all of the fields.
	// The snippet will evaluate to a promise that resolves when the relevant fields are found.
	// Fields that are not present in the promise result must be extracted another way.
	ExtractJS string `json:"extract_js,omitempty"`
	// A regex pattern that the URL should match before the client closes the webview.
	//
	// The client may submit the login if the user closes the webview after all cookies are collected
	// even if this URL is not reached, but it should only automatically close the webview after
	// both cookies and the URL match.
	WaitForURLPattern string `json:"wait_for_url_pattern,omitempty"`
	// If set, the client should load the URL and run ExtractJS in a webview that is not shown to the
	// user.
	Hidden bool `json:"hidden,omitzero"`
}

type LoginInputFieldType string

const (
	LoginInputFieldTypeUsername    LoginInputFieldType = "username"
	LoginInputFieldTypePassword    LoginInputFieldType = "password"
	LoginInputFieldTypePhoneNumber LoginInputFieldType = "phone_number"
	LoginInputFieldTypeEmail       LoginInputFieldType = "email"
	LoginInputFieldType2FACode     LoginInputFieldType = "2fa_code"
	LoginInputFieldTypeToken       LoginInputFieldType = "token"
	LoginInputFieldTypeURL         LoginInputFieldType = "url"
	LoginInputFieldTypeDomain      LoginInputFieldType = "domain"
	LoginInputFieldTypeSelect      LoginInputFieldType = "select"
	LoginInputFieldTypeCaptchaCode LoginInputFieldType = "captcha_code"
)

type LoginInputDataField struct {
	// The type of input field as a hint for the client.
	Type LoginInputFieldType `json:"type"`
	// The ID of the field to be used as the key in the map that is submitted to the connector.
	ID string `json:"id"`
	// The name of the field shown to the user.
	Name string `json:"name"`
	// The description of the field shown to the user.
	Description string `json:"description"`
	// A default value that the client can pre-fill the field with.
	DefaultValue string `json:"default_value,omitempty"`
	// A regex pattern that the client can use to validate input client-side.
	Pattern string `json:"pattern,omitempty"`
	// Minimum and maximum input length as a hint for the client
	MinLength int `json:"min_length,omitzero"`
	MaxLength int `json:"max_length,omitzero"`
	// For fields of type select, the valid options.
	// Pattern may also be filled with a regex that matches the same options.
	Options []string `json:"options,omitempty"`
	// A function that validates the input and optionally cleans it up before it's submitted to the connector.
	Validate func(string) (string, error) `json:"-"`
}

var numberCleaner = strings.NewReplacer("-", "", " ", "", "(", "", ")", "")

func isOnlyNumbers(input string) bool {
	for _, r := range input {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func CleanNonInternationalPhoneNumber(phone string) (string, error) {
	phone = numberCleaner.Replace(phone)
	if !isOnlyNumbers(strings.TrimPrefix(phone, "+")) {
		return "", fmt.Errorf("phone number must only contain numbers")
	}
	return phone, nil
}

func CleanPhoneNumber(phone string) (string, error) {
	phone = numberCleaner.Replace(phone)
	if len(phone) < 2 {
		return "", fmt.Errorf("phone number must start with + and contain numbers")
	} else if phone[0] != '+' {
		return "", fmt.Errorf("phone number must start with +")
	} else if !isOnlyNumbers(phone[1:]) {
		return "", fmt.Errorf("phone number must only contain numbers")
	}
	return phone, nil
}

func noopValidate(input string) (string, error) {
	return input, nil
}

func (f *LoginInputDataField) FillDefaultValidate() {
	if f.Validate != nil {
		return
	}
	switch f.Type {
	case LoginInputFieldTypePhoneNumber:
		f.Validate = CleanPhoneNumber
	case LoginInputFieldTypeEmail:
		f.Validate = func(email string) (string, error) {
			if !strings.ContainsRune(email, '@') {
				return "", fmt.Errorf("invalid email")
			}
			return email, nil
		}
	default:
		if f.Pattern != "" {
			f.Validate = func(s string) (string, error) {
				match, err := regexp.MatchString(f.Pattern, s)
				if err != nil {
					return "", err
				} else if !match {
					return "", fmt.Errorf("doesn't match regex `%s`", f.Pattern)
				} else {
					return s, nil
				}
			}
		} else {
			f.Validate = noopValidate
		}
	}
}

type LoginUserInputParams struct {
	// The fields that the user needs to fill in.
	Fields []LoginInputDataField `json:"fields"`

	// Attachments to display alongside the input fields.
	Attachments []*LoginUserInputAttachment `json:"attachments"`

	// If set, the client may allow the user to go back from this step. The login
	// process must implement LoginProcessStepCancel.
	CanCancel bool `json:"can_cancel,omitzero"`
}

type LoginUserInputAttachment struct {
	Type     string                       `json:"type,omitempty"`
	FileName string                       `json:"filename,omitempty"`
	Content  []byte                       `json:"content,omitempty"`
	Info     LoginUserInputAttachmentInfo `json:"info,omitempty"`
}

type LoginUserInputAttachmentInfo struct {
	MimeType string `json:"mimetype,omitempty"`
	Width    int    `json:"w,omitzero"`
	Height   int    `json:"h,omitzero"`
	Size     int    `json:"size,omitzero"`
}

type LoginCompleteParams struct {
	UserLoginID string `json:"user_login_id"`
}

type LoginSubmit struct {
}
