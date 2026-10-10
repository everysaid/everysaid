package meta

// The cookies the user pastes from a browser where they are logged in to facebook.com or
// messenger.com, as mautrix-meta's cookie login takes them: a JSON object of names and values, a
// browser extension's JSON list ({"name", "value"}), a request copied as cURL from the developer
// tools (its -b or Cookie header), or a Cookie header itself ("a=b; c=d").

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrNoCookies: what was pasted holds no cookies.
var ErrNoCookies = errors.New("no cookies found in what was pasted")

// parseCookies reads pasted cookies into names and values.
func parseCookies(text string) (map[string]string, error) {
	text = strings.TrimSpace(text)
	out := map[string]string{}
	if strings.HasPrefix(text, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(text), &m); err != nil {
			return nil, ErrNoCookies
		}
		for k, v := range m {
			if s, ok := v.(string); ok && s != "" {
				out[k] = s
			}
		}
	} else if strings.HasPrefix(text, "[") {
		var list []struct{ Name, Value string }
		if err := json.Unmarshal([]byte(text), &list); err != nil {
			return nil, ErrNoCookies
		}
		for _, c := range list {
			if c.Name != "" && c.Value != "" {
				out[c.Name] = c.Value
			}
		}
	} else if strings.HasPrefix(text, "curl ") || strings.HasPrefix(text, "curl.exe ") {
		for _, h := range curlCookies(text) {
			headerCookies(h, out)
		}
	} else {
		headerCookies(strings.TrimPrefix(strings.TrimPrefix(text, "Cookie:"), "cookie:"), out)
	}
	if len(out) == 0 {
		return nil, ErrNoCookies
	}
	return out, nil
}

// headerCookies reads a Cookie header's "a=b; c=d" into out.
func headerCookies(h string, out map[string]string) {
	for _, part := range strings.Split(h, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && k != "" && v != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
}

// curlCookies are the cookie headers of a cURL command line (as a shell would split it; its line
// ends may be gone, a pasted field being one line).
func curlCookies(cmd string) []string {
	args := shellWords(cmd)
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "-b" || a == "--cookie") && i+1 < len(args):
			out = append(out, args[i+1])
			i++
		case (a == "-H" || a == "--header") && i+1 < len(args):
			if k, v, ok := strings.Cut(args[i+1], ":"); ok && strings.EqualFold(strings.TrimSpace(k), "cookie") {
				out = append(out, v)
			}
			i++
		}
	}
	return out
}

// shellWords splits a command line as a POSIX shell (or Windows' cmd, whose ^ escapes a line end)
// would: quotes, backslashes, and line continuations.
func shellWords(s string) []string {
	var words []string
	var cur strings.Builder
	in := false
	var quote rune
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			if r == '"' {
				quote = 0
			} else if r == '\\' && i+1 < len(rs) && strings.ContainsRune(`"\$`+"`", rs[i+1]) {
				i++
				cur.WriteRune(rs[i])
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == '\\' || r == '^':
			if i+1 < len(rs) && (rs[i+1] == '\n' || rs[i+1] == '\r' || rs[i+1] == ' ') {
				i++ // a line continuation (its line end perhaps gone)
				continue
			}
			if r == '\\' && i+1 < len(rs) {
				i++
				cur.WriteRune(rs[i])
				in = true
			} else {
				cur.WriteRune(r)
				in = true
			}
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if in {
				words = append(words, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		words = append(words, cur.String())
	}
	return words
}
