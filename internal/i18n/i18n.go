// Package i18n holds the words the server says by itself (logs, statuses, notifications, the
// plugins' words and errors) in other languages. They are written in English in the code and said
// in the user's language through Tr. A text without a translation stays in English.
package i18n

import (
	"fmt"
	"os"
	"strings"
)

var tables = map[string]map[string]string{"el": EL}

// Tr is the text in the language, else as it is.
func Tr(text, lang string) string {
	if text == "" || lang == "en" || lang == "" {
		return text
	}
	table := tables[lang]
	if v, ok := table[text]; ok {
		return v
	}
	if head, rest, ok := strings.Cut(text, ": "); ok { // "missing: X, Y" and the like
		if h, ok := table[head]; ok {
			parts := strings.Split(rest, ", ")
			for i, p := range parts {
				if v, ok := table[p]; ok {
					parts[i] = v
				}
			}
			return h + ": " + strings.Join(parts, ", ")
		}
	}
	return text
}

// Format puts params into {name} places, as Python's str.format(**params) does.
func Format(text string, params map[string]any) string {
	if len(params) == 0 {
		return text
	}
	pairs := make([]string, 0, 2*len(params))
	for k, v := range params {
		pairs = append(pairs, "{"+k+"}", fmt.Sprint(v))
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// T is Tr then Format.
func T(text, lang string, params map[string]any) string { return Format(Tr(text, lang), params) }

// CLILang is the language of the command line: Greek where the system's locale is.
func CLILang() string {
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if s := os.Getenv(v); s != "" {
			if strings.HasPrefix(s, "el") {
				return "el"
			}
			return "en"
		}
	}
	return "en"
}

// Say is a line of the command line, in its language.
func Say(text string, params map[string]any) string { return T(text, CLILang(), params) }
