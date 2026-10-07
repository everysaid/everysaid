// Package contacts holds the contacts plugins: an address book whose names and photos name the
// people of the archive. Ports everysaid/plugins/contacts.py.
//
// A contact joins the people whose addresses (phone numbers, emails) it lists; it creates no one:
// a number never seen in a message or call stays out. Photos are kept in the cache
// (`avatars/<sha256>.<ext>`). Two plugins: a CardDAV address book (Nextcloud, iCloud, Fastmail,
// Radicale, ...) and a .vcf file.
package contacts

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Value is a phone number or an email of a card, with its label ("cell", "home", ...; "" for none).
type Value struct{ Value, Label string }

// Photo is a card's picture: its bytes, and the kind of image (jpeg, png, ...).
type Photo struct {
	Data []byte
	Ext  string
}

// Card is a vCard as the archive takes it.
type Card struct {
	UID, URL, Name, Org string
	Phones, Emails      []Value
	Photo               *Photo
}

var (
	folded   = regexp.MustCompile(`\r?\n[ \t]`)
	typeList = regexp.MustCompile(`TYPE=([A-Z,]+)`)
	typeWord = regexp.MustCompile(`TYPE=([A-Z]+)`)
)

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// label is the TYPE of a property's parameters (upper case), lower case; in vCard 2.1 the bare
// words (TEL;CELL;PREF), PREF left out.
func label(params []string, p string) string {
	if m := typeList.FindStringSubmatch(p); m != nil {
		return strings.ToLower(m[1])
	}
	var words []string
	for _, x := range params {
		x = strings.ToUpper(strings.TrimSpace(x))
		if x != "" && x != "PREF" && !strings.Contains(x, "=") {
			words = append(words, x)
		}
	}
	return strings.ToLower(strings.Join(words, ","))
}

// decode64 is base64 as Python's b64decode reads it: what is not of its alphabet left out.
func decode64(s string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '+' || r == '/' || r == '=') {
			return r
		}
		return -1
	}, s)
	clean = strings.TrimRight(clean, "=")
	return base64.RawStdEncoding.DecodeString(clean)
}

// ParseVcards reads the cards of a vCard text (vCard 2.1, 3.0 and 4.0).
func ParseVcards(data string) []Card {
	text := folded.ReplaceAllString(strings.ReplaceAll(data, "\r\n", "\n"), "") // unfold
	var cards []Card
	var card *Card
	var n string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if hasPrefixFold(line, "BEGIN:VCARD") {
			card, n = &Card{}, ""
			continue
		}
		if hasPrefixFold(line, "END:VCARD") {
			if card != nil {
				if card.Name == "" && n != "" {
					card.Name = nameOfN(n)
				}
				if card.UID == "" {
					h := sha1.Sum([]byte(card.Name + reprPhones(card.Phones)))
					card.UID = hex.EncodeToString(h[:])
				}
				cards = append(cards, *card)
			}
			card = nil
			continue
		}
		head, value, ok := strings.Cut(line, ":")
		if card == nil || !ok {
			continue
		}
		parts := strings.Split(head, ";")
		dotted := strings.Split(parts[0], ".")
		name := strings.ToUpper(dotted[len(dotted)-1]) // item1.TEL -> TEL
		params := parts[1:]
		p := strings.ToUpper(strings.Join(params, ";"))
		value = strings.NewReplacer(`\,`, ",", `\;`, ";", `\n`, "\n", `\N`, "\n").Replace(value)
		switch name {
		case "FN":
			card.Name = strings.TrimSpace(value)
		case "N":
			n = value
		case "UID":
			card.UID = strings.TrimSpace(value)
		case "ORG":
			org, _, _ := strings.Cut(value, ";")
			card.Org = strings.TrimSpace(org)
		case "TEL":
			card.Phones = append(card.Phones, Value{strings.TrimSpace(strings.TrimPrefix(value, "tel:")), label(params, p)})
		case "EMAIL":
			card.Emails = append(card.Emails, Value{strings.TrimSpace(value), label(params, p)})
		case "PHOTO":
			if rest, ok := strings.CutPrefix(value, "data:"); ok {
				meta, b64, ok := strings.Cut(rest, ",")
				if !ok {
					continue
				}
				kind, _, _ := strings.Cut(meta, ";")
				ext := kind[strings.LastIndex(kind, "/")+1:]
				if b, err := decode64(b64); err == nil {
					card.Photo = &Photo{b, ext}
				}
			} else if strings.Contains(p, "ENCODING=B") { // B or BASE64
				ext := "jpeg"
				if m := typeWord.FindStringSubmatch(p); m != nil {
					ext = strings.ToLower(m[1])
				}
				if b, err := decode64(value); err == nil {
					card.Photo = &Photo{b, ext}
				}
			}
		}
	}
	return cards
}

// nameOfN is the name of a card's N (family;given;additional;prefixes;suffixes): given, then family.
func nameOfN(n string) string {
	parts := strings.Split(n, ";")
	var words []string
	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		words = append(words, strings.TrimSpace(parts[1]))
	}
	if strings.TrimSpace(parts[0]) != "" {
		words = append(words, strings.TrimSpace(parts[0]))
	}
	return strings.Join(words, " ")
}

// reprPhones is Python's repr of the list of (value, label) the Python made a card's uid from,
// where the card has none: the same uid, so the contacts it made are the same contacts.
func reprPhones(phones []Value) string {
	var b strings.Builder
	b.WriteString("[")
	for i, v := range phones {
		if i > 0 {
			b.WriteString(", ")
		}
		lbl := "None"
		if v.Label != "" {
			lbl = reprStr(v.Label)
		}
		fmt.Fprintf(&b, "(%s, %s)", reprStr(v.Value), lbl)
	}
	b.WriteString("]")
	return b.String()
}

// reprStr is Python's repr of a str.
func reprStr(s string) string {
	quote := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == rune(quote) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f || pyPrintable(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// pyPrintable is Python's str.isprintable of one character: not a control, format, separator
// (but the space), surrogate, private or unassigned one.
func pyPrintable(r rune) bool {
	if r == ' ' {
		return true
	}
	return !unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zs, unicode.Zl, unicode.Zp, unicode.Cs, unicode.Co) &&
		(unicode.IsPrint(r) || unicode.IsGraphic(r))
}
