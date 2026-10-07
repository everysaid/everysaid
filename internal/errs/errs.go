// Package errs holds the failures the user is told about.
//
// UserError{Code, Status, Params}: the interface says it in the user's language from the code
// (web/src/lib/i18n.ts, errors.<code>). A plugin, whose words the interface does not know, gives an
// English Text instead, which the server says in the user's language (code "plugin").
package errs

type UserError struct {
	Code   string
	Status int
	Text   string
	Params map[string]any
}

func (e *UserError) Error() string {
	if e.Text != "" {
		return e.Text
	}
	return e.Code
}

// New is a UserError of a code, with status 400 unless given.
func New(code string, status int, params map[string]any) *UserError {
	if status == 0 {
		status = 400
	}
	return &UserError{Code: code, Status: status, Params: params}
}

// Plugin is a plugin's failure, in English (translated on the way out).
func Plugin(text string, status int) *UserError {
	if status == 0 {
		status = 409
	}
	return &UserError{Code: "plugin", Status: status, Text: text}
}
