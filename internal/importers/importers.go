// Package importers fills the archive from the sources. This file ports everysaid/importers.py:
// the importers, in the order they run (each needs the ones before it): sms, calls, viber,
// whatsapp, telegram, voip (WhatsApp/Viber calls, carrier notices), media. Every source is
// optional: an importer whose sources are not there says so and does nothing. Each can be run
// again: rows already in the archive are skipped.
//
// What they say goes to `out`, one line at a time, already in the language of Lang (the system's
// locale by default, as on the command line; a server sets it to its user's): the lines are
// written in English here, with their Greek in internal/i18n/el_importers.go.
//
// A failing statement panics (with *db.Error) as a Python exception stops the script; every
// exported function turns that back into an error.
package importers

import (
	"fmt"
	"sort"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/i18n"
)

// Lang gives the language the importers' lines are said in.
var Lang = i18n.CLILang

// say is a line of the importers, in the language of Lang.
func say(out func(string), text string, params map[string]any) {
	if out != nil {
		out(i18n.T(text, Lang(), params))
	}
}

// titled is Archive.Conversation of a conversation with a key, its title as the source has it (a
// value or nil): a title "" is kept as "", as the Python keeps it (Archive.Conversation takes ""
// for none), on a conversation new to the archive.
func titled(a *archive.Archive, service string, members []archive.Handle, key string, title any) int64 {
	if title == nil {
		return a.Conversation(service, members, key, "")
	}
	if t := pyStr(title); t != "" {
		return a.Conversation(service, members, key, t)
	}
	_, existed := a.FindConversation(service, key)
	cid := a.Conversation(service, members, key, "")
	if !existed {
		a.Exec("UPDATE conversation SET title = '' WHERE id = ? AND title IS NULL", cid)
	}
	return cid
}

// Importer is one of the importers, by name, with what it brings.
type Importer struct {
	Name, Description string
	run               func(a *archive.Archive, out func(string)) error
}

// Names are the importers in the order they run: media finds the messages by key and origin,
// voip reads the SMS and the WhatsApp chats, and comes after calls.
var Names = []Importer{
	{"sms", "SMS, MMS, iMessage, RCS", func(a *archive.Archive, out func(string)) error { return SMS(a, out, SMSOptions{}) }},
	{"calls", "the phones' call logs", func(a *archive.Archive, out func(string)) error { return Calls(a, out, CallsOptions{}) }},
	{"viber", "Viber messages", func(a *archive.Archive, out func(string)) error { return Viber(a, out, ViberOptions{}) }},
	{"whatsapp", "WhatsApp messages", func(a *archive.Archive, out func(string)) error {
		_, err := WhatsApp(a, out, WhatsAppOptions{})
		return err
	}},
	{"telegram", "Telegram messages and calls", func(a *archive.Archive, out func(string)) error {
		return Telegram(a, out, TelegramOptions{})
	}},
	{"voip", "WhatsApp and Viber calls, carrier notices", func(a *archive.Archive, out func(string)) error {
		return VoIP(a, out, VoIPOptions{})
	}},
	{"media", "the files of messages", func(a *archive.Archive, out func(string)) error { return Media(a, out) }},
}

func index(name string) int {
	for i, imp := range Names {
		if imp.Name == name {
			return i
		}
	}
	return -1
}

// Known says whether there is an importer of this name.
func Known(name string) bool { return index(name) >= 0 }

// Chosen are the importers to run: those named, else config [import] importers, else all.
func Chosen(names []string) []string {
	if len(names) > 0 {
		return names
	}
	if c := config.Strings("import", "importers"); len(c) > 0 {
		return c
	}
	out := make([]string, len(Names))
	for i, imp := range Names {
		out[i] = imp.Name
	}
	return out
}

// Run runs the importers named, in their order.
func Run(a *archive.Archive, names []string, out func(string)) (err error) {
	defer archive.Recover(&err)
	order := make([]int, len(names))
	for k, n := range names {
		if order[k] = index(n); order[k] < 0 {
			return fmt.Errorf("unknown importer: %s", n)
		}
	}
	sort.SliceStable(order, func(x, y int) bool { return order[x] < order[y] })
	for _, i := range order {
		imp := Names[i]
		say(out, "== {name}: {what}", map[string]any{"name": imp.Name, "what": i18n.Tr(imp.Description, Lang())})
		if err := imp.run(a, out); err != nil {
			return err
		}
	}
	return nil
}
