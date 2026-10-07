// Ports everysaid/imlogs.py (and the logic of scripts/imlogs-import.py): the logs of the old
// multi-protocol messengers, Adium (macOS) and Pidgin or Gaim (libpurple). Both kept one file per
// conversation session, under the account and the contact:
//
//	Adium:  <Logs>/<Service>.<account>/<contact>/<contact> (<date>).chatlog/<same>.xml
//	        (and, from 2006, <contact> (<day>).AdiumHTMLLog), the chatlog folder also holding
//	        the pictures the messages showed
//	Pidgin: <.purple>/logs/<protocol>/<account>/<contact>/<date>.txt (or .html), a group chat
//	        being a contact folder ending in `.chat`; `blist.xml` has the names the owner gave
//	        to buddies, `accounts.xml` the accounts
//
// The services are those of their time: msn, icq, aim, yahoo, jabber (Google Talk included),
// skype, irc; Facebook chat (over XMPP, or Adium's own plugin) is `messenger`, where Facebook's ids
// meet a later import of Messenger. A handle is an email (MSN, Jabber: shared with every service),
// an id within the service, or a phone (Adium's WhatsApp plugin).
//
// Messages have no ids: a message's key in the archive is its fingerprint, and its origin the file
// and its index in it. The two programs were used by turns, so the same message is not expected in
// both; a message whose fingerprint its conversation already has is skipped and counted. Status
// lines are not messages and are left out.
//
// Pidgin writes who said what by display name: a message is the owner's when its sender is one of
// the account's names (the account, its alias, and any name that speaks in three or more of the
// account's conversations, which only the owner does). Adium writes handles, and the display names
// as `alias`, which go to handle_name (`chat`); Pidgin's buddy list gives the owner's own names for
// people (`book`). Both programs kept the owner's grouping of handles into one person, and the
// people of those handles are merged, as the owner had merged them then.
//
// Python's semantics are kept where they decide what goes into the archive: its Unicode \d, \s and
// \w, html.unescape, urllib.parse.unquote, os.path's joining of paths, text-mode reading (any line
// end becomes \n), and datetime's wall-clock arithmetic.
package importers

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"howett.net/plist"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/i18n"
	"everysaid/internal/text"
)

var imDevices = map[string]string{"adium": "adium", "pidgin": "pidgin"}

// the programs' names for the services -> ours
var imServices = map[string]string{"msn": "msn", "icq": "icq", "aim": "aim", "yahoo": "yahoo", "yahoo!": "yahoo",
	"jabber": "jabber", "gtalk": "jabber", "xmpp": "jabber", "skype": "skype", "irc": "irc", "facebook": "messenger",
	"whatsapp": "whatsapp", "mac": "aim", "bonjour": "bonjour", "novell": "novell", "sametime": "sametime"}

var imProtocols = map[string]string{"prpl-msn": "msn", "prpl-icq": "icq", "prpl-aim": "aim", "prpl-oscar": "aim",
	"prpl-yahoo": "yahoo", "prpl-jabber": "jabber", "prpl-bigbrownchunx-skype": "skype", "prpl-skype": "skype",
	"prpl-skypeweb": "skype", "prpl-irc": "irc", "prpl-facebook": "messenger"}

// Python's \w (str.isalnum or "_"), for the classes below.
const pyW = `\p{L}\p{N}_`

var (
	// Python's `$` also matches before a final newline
	imFacebook = pyRE(`^-?(\d+)@chat\.facebook\.com\n?$`)
	imChatN    = pyRE(`^chat\d+$`)
	imTag      = regexp.MustCompile(`<[^>]+>`)
	imBR       = pyRE(`(?i)<br\s*/?>|</div>|</p>|</pre>`)
	// <img\b[^>]*\bsrc="...": Python's \b is Unicode, so the boundaries are written out
	imImg = regexp.MustCompile(`(?i)<img[^>` + pyW + `](?:[^>]*[^>` + pyW + `])?src="([^"]+)"`)
	// Adium's chatlog: a message, its attributes and its body (self-closing ones are empty);
	// <message\b([^>]*?) with the boundary written out
	imMessage  = regexp.MustCompile(`(?s)<message((?:[^>` + pyW + `][^>]*?)??)(?:/>|>(.*?)</message>)`)
	imAttr     = regexp.MustCompile(`([` + pyW + `]+)="([^"]*)"`)
	imHTMLLine = regexp.MustCompile(`(?s)<div class="(send|receive)"><span class="timestamp">([^<]*)</span> ?<span class="sender">` +
		`([^<]*?):? ?</span> ?<pre class="message">(.*?)</pre></div>`)
	imHTMLStamp = pyRE(`^(\d{1,2}):(\d\d):(\d\d) ?([AP]M)?`)
	imHTMLDay   = pyRE(`\((\d{4})-(\d\d)-(\d\d)\)`)
	imHTMLOld   = pyRE(` on (\d\d)-(\d\d)-(\d\d)\.`) // the oldest: "on 06-10-24"
	imOffset    = pyRE(`([+-]\d\d)(\d\d)$`)
	// Pidgin: the file's date, and a line of a conversation (AM/PM, or their Greek abbreviations)
	pidginFileRE = pyRE(`(\d{4})-(\d\d)-(\d\d)\.(\d{6})([+-]\d{4})?`)
	pidginLineRE = pyRE(`^\((\d{1,2}):(\d\d):(\d\d)(?: ?([AP]M|\x{3c0}\x{3bc}|\x{3bc}\x{3bc}))?\) (.*)`)
	pidginSaidRE = regexp.MustCompile(`(?s)^([^:]+?): (.*)`)
	pidginTitle  = regexp.MustCompile(`(?s)<title>.*?</title>`)
	imCharref    = regexp.MustCompile(`&(#[0-9]+;?|#[xX][0-9a-fA-F]+;?|[^\t\n\f <&#;]{1,32};?)`)
)

// lines Pidgin writes as "(time) <phrase>: <detail>", which are not a message (in English and in
// Greek, as the program wrote them)
var pidginNotice = []string{"Unable to send message",
	"\u0391\u03b4\u03c5\u03bd\u03b1\u03bc\u03af\u03b1 \u03b1\u03c0\u03bf\u03c3\u03c4\u03bf\u03bb\u03ae\u03c2 \u03bc\u03b7\u03bd\u03cd\u03bc\u03b1\u03c4\u03bf\u03c2",
	"The privacy status of the current conversation", "OTR Error",
	"\u03a4\u03bf \u03bc\u03ae\u03bd\u03c5\u03bc\u03b1 \u03b4\u03b5\u03bd \u03ae\u03c4\u03b1\u03bd \u03b4\u03c5\u03bd\u03b1\u03c4\u03cc \u03bd\u03b1 \u03c3\u03c4\u03b1\u03bb\u03b5\u03af",
	"Message could not be sent", "Error", "\u03a3\u03c6\u03ac\u03bb\u03bc\u03b1", "Unverified", "Private conversation",
	"\u039c\u03b7 \u03b5\u03c0\u03b9\u03b2\u03b5\u03b2\u03b1\u03b9\u03c9\u03bc\u03ad\u03bd\u03b7",
	"\u0399\u03b4\u03b9\u03c9\u03c4\u03b9\u03ba\u03ae \u03c3\u03c5\u03bd\u03bf\u03bc\u03b9\u03bb\u03af\u03b1"}

const (
	pmGreek = "\u03bc\u03bc"
	amGreek = "\u03c0\u03bc"
)

func imServiceOf(name string) string { return imServices[text.Lower(name)] }

// imHandle is a handle as the programs name it: a JID without its resource, Facebook's XMPP ids as
// Messenger ids, phones for WhatsApp, else an id within the service.
func imHandle(service, raw string) archive.Handle {
	s := pyStrip(pyUnquote(raw))
	if strings.Contains(s, "/") && strings.Contains(s, "@") { // a JID's resource
		s, _, _ = strings.Cut(s, "/")
	}
	fb := imFacebook.FindStringSubmatch(text.Lower(s))
	if fb != nil || service == "messenger" {
		v := strings.TrimLeft(s, "-")
		if fb != nil {
			v = fb[1]
		}
		return archive.H("id", v, "messenger")
	}
	if service == "whatsapp" {
		k, v := archive.Address("+"+strings.TrimLeft(s, "+"), config.Region)
		return archive.H(k, v)
	}
	if service == "msn" || service == "jabber" || strings.Contains(s, "@") {
		return archive.H("email", text.Lower(s))
	}
	return archive.H("id", strings.ReplaceAll(text.Lower(s), " ", ""), service)
}

func imIsGroup(service, raw string) bool {
	s := text.Lower(pyUnquote(raw))
	if strings.HasSuffix(s, ".chat") {
		return true
	}
	if strings.Contains(s, "@groupchat.") || strings.Contains(s, "@conference.") || strings.HasSuffix(s, "@thread.skype") ||
		strings.HasPrefix(s, "-") && imFacebook.MatchString(s) {
		return true
	}
	return service == "aim" && imChatN.MatchString(s) || service == "irc" && strings.HasPrefix(s, "#")
}

// imClean is the text out of a message's HTML: line breaks kept, tags gone, entities read.
func imClean(markup string) string {
	t := imBR.ReplaceAllLiteralString(markup, "\n")
	t = imTag.ReplaceAllLiteralString(t, "")
	t = pyUnescape(t)
	t = strings.ReplaceAll(strings.ReplaceAll(t, "\u00a0", " "), "\ufeff", "")
	return pyStrip(t)
}

// --- Python's library, as these logs need it -----------------------------------------------------

// Python's html._invalid_charrefs: numeric references the standard reads as other characters.
var pyInvalidCharrefs = map[int64]rune{0x00: 0xfffd, 0x0d: '\r', 0x80: 0x20ac, 0x81: 0x81, 0x82: 0x201a, 0x83: 0x192,
	0x84: 0x201e, 0x85: 0x2026, 0x86: 0x2020, 0x87: 0x2021, 0x88: 0x2c6, 0x89: 0x2030, 0x8a: 0x160, 0x8b: 0x2039,
	0x8c: 0x152, 0x8d: 0x8d, 0x8e: 0x17d, 0x8f: 0x8f, 0x90: 0x90, 0x91: 0x2018, 0x92: 0x2019, 0x93: 0x201c,
	0x94: 0x201d, 0x95: 0x2022, 0x96: 0x2013, 0x97: 0x2014, 0x98: 0x2dc, 0x99: 0x2122, 0x9a: 0x161, 0x9b: 0x203a,
	0x9c: 0x153, 0x9d: 0x9d, 0x9e: 0x17e, 0x9f: 0x178}

// pyInvalidCodepoint is Python's html._invalid_codepoints: references that give nothing.
func pyInvalidCodepoint(n int64) bool {
	return n >= 0x1 && n <= 0x8 || n == 0xb || n >= 0xe && n <= 0x1f || n >= 0x7f && n <= 0x9f ||
		n >= 0xfdd0 && n <= 0xfdef || n <= 0x10ffff && n&0xfffe == 0xfffe
}

// pyUnescape is Python's html.unescape. Numeric references are read here, as Python reads them;
// named ones by Go's html package, which has the same HTML5 table and the same rule for names
// without a semicolon.
func pyUnescape(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	return imCharref.ReplaceAllStringFunc(s, func(m string) string {
		ref := m[1:]
		if ref[0] != '#' {
			return html.UnescapeString(m)
		}
		digits, base := strings.TrimRight(ref[1:], ";"), 10
		if ref[1] == 'x' || ref[1] == 'X' {
			digits, base = strings.TrimRight(ref[2:], ";"), 16
		}
		b, _ := new(big.Int).SetString(digits, base)
		if !b.IsInt64() || b.Int64() > 0x10ffff {
			return "\ufffd"
		}
		n := b.Int64()
		if r, ok := pyInvalidCharrefs[n]; ok {
			return string(r)
		}
		if n >= 0xd800 && n <= 0xdfff {
			return "\ufffd"
		}
		if pyInvalidCodepoint(n) {
			return ""
		}
		return string(rune(n))
	})
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// pyUnquote is Python's urllib.parse.unquote: %XX in the ASCII runs read as UTF-8 (each run on its
// own, bad bytes as U+FFFD), a % without two hex digits left as it is.
func pyUnquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var out strings.Builder
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && s[j] < 0x80 {
			j++
		}
		if j > i {
			run := s[i:j]
			var b []byte
			for k := 0; k < len(run); k++ {
				if run[k] == '%' && k+2 < len(run) && isHex(run[k+1]) && isHex(run[k+2]) {
					v, _ := strconv.ParseUint(run[k+1:k+3], 16, 8)
					b = append(b, byte(v))
					k += 2
					continue
				}
				b = append(b, run[k])
			}
			out.WriteString(decodeReplace(b))
			i = j
			continue
		}
		for j < len(s) && s[j] >= 0x80 {
			j++
		}
		out.WriteString(s[i:j])
		i = j
	}
	return out.String()
}

// pyJoin is os.path.join of two parts (no cleaning: the path as Python would store it).
func pyJoin(a, b string) string {
	if filepath.IsAbs(b) || a == "" {
		return b
	}
	if strings.HasSuffix(a, "/") || strings.HasSuffix(a, string(os.PathSeparator)) {
		return a + b
	}
	return a + string(os.PathSeparator) + b
}

// pyDirname is os.path.dirname: what is before the last separator, its trailing separators
// dropped (so "a/Logs/" gives "a/Logs").
func pyDirname(p string) string {
	i := strings.LastIndexAny(p, "/"+string(os.PathSeparator)) + 1
	head := p[:i]
	if head != "" && strings.Trim(head, "/"+string(os.PathSeparator)) != "" {
		head = strings.TrimRight(head, "/"+string(os.PathSeparator))
	}
	return head
}

func pyBasename(p string) string { return p[strings.LastIndexAny(p, "/"+string(os.PathSeparator))+1:] }

// pyRelpath is os.path.relpath.
func pyRelpath(p, start string) string {
	ap, err1 := filepath.Abs(p)
	as, err2 := filepath.Abs(start)
	if err1 != nil || err2 != nil {
		panic(fmt.Errorf("relpath %s: %v %v", p, err1, err2))
	}
	r, err := filepath.Rel(as, ap)
	if err != nil {
		panic(err)
	}
	return r
}

// listdir is os.listdir: the names in the folder's own order.
func listdir(p string) []string {
	f, err := os.Open(p)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		panic(err)
	}
	return names
}

func sortedListdir(p string) []string {
	n := listdir(p)
	sort.Strings(n)
	return n
}

// readText is a file read as Python's open(path, encoding="utf-8", errors="replace") reads it: bad
// bytes as U+FFFD, \r\n and \r as \n.
func readText(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	s := decodeReplace(b)
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// imZone is the zone of an offset written (+0300), else the owner's.
func imZone(offset string) *time.Location {
	if offset == "" {
		return config.Timezone
	}
	r := []rune(offset)
	d := time.Duration(atoiPy(string(r[1:3])))*time.Hour + time.Duration(atoiPy(string(r[3:5])))*time.Minute
	if d >= 24*time.Hour {
		panic(fmt.Errorf("offset must be a timedelta strictly between -timedelta(hours=24) and timedelta(hours=24): %s", offset))
	}
	if r[0] == '-' {
		d = -d
	}
	return time.FixedZone("", int(d/time.Second))
}

func imClock(h, m, s int, ampm string) (int, int, int) {
	if (ampm == "PM" || ampm == pmGreek) && h < 12 {
		h += 12
	}
	if (ampm == "AM" || ampm == amGreek) && h == 12 {
		h = 0
	}
	return h, m, s
}

// imAt is a reading of a day at a time, as datetime.replace gives it (a ValueError where there is
// no such time).
func imAt(y int, mo time.Month, d, h, mi, s int, loc *time.Location) wall {
	w, ok := newWall(y, mo, d, h, mi, s, loc)
	if !ok {
		panic(fmt.Errorf("no such date or time: %04d-%02d-%02d %02d:%02d:%02d", y, mo, d, h, mi, s))
	}
	return w
}

// imRecord is one message as a parser gives it; alias "" is none.
type imRecord struct {
	rowKey   string
	ts       int64
	outgoing bool
	sender   string
	text     string
	alias    string
	images   []string
}

// --- Adium ---------------------------------------------------------------------------------------

// findDir is the first folder under root (root itself first, then by name) for which test holds.
func findDir(root string, test func(string) bool, depth int) string {
	if test(root) {
		return root
	}
	if depth == 0 {
		return ""
	}
	for _, d := range sortedListdir(root) {
		sub := pyJoin(root, d)
		if isDir(sub) && !strings.HasPrefix(d, ".") {
			if found := findDir(sub, test, depth-1); found != "" {
				return found
			}
		}
	}
	return ""
}

// isAdiumLogs: a folder of <Service>.<uid> account folders holding contacts' logs (the Contact
// Album has the same account folders, with pictures).
func isAdiumLogs(folder string) bool {
	for _, d := range listdir(folder) {
		acc := pyJoin(folder, d)
		name, _, _ := strings.Cut(d, ".")
		if strings.Contains(d, ".") && imServiceOf(name) != "" && isDir(acc) {
			for _, contact := range listdir(acc) {
				c := pyJoin(acc, contact)
				if isDir(c) {
					for _, f := range listdir(c) {
						if strings.HasSuffix(f, ".chatlog") || strings.HasSuffix(f, ".AdiumHTMLLog") {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// adiumLogs is the Logs folder of an Adium folder given as the Logs folder itself, Users/Default or
// Adium 2.0; "" for none.
func adiumLogs(root string) string {
	if !isDir(root) {
		return ""
	}
	return findDir(root, isAdiumLogs, 4)
}

type adiumConversation struct {
	service, uid, contact string
	files                 []string
}

// adiumConversations: every contact folder, with its files.
func adiumConversations(logs string) []adiumConversation {
	var out []adiumConversation
	for _, accdir := range sortedListdir(logs) {
		name, uid, _ := strings.Cut(accdir, ".")
		service := imServiceOf(name)
		if uid == "" || service == "" || !isDir(pyJoin(logs, accdir)) {
			continue
		}
		for _, contact := range sortedListdir(pyJoin(logs, accdir)) {
			folder := pyJoin(pyJoin(logs, accdir), contact)
			if !isDir(folder) {
				continue
			}
			var files []string
			for _, entry := range sortedListdir(folder) {
				path := pyJoin(folder, entry)
				if strings.HasSuffix(entry, ".chatlog") && isDir(path) {
					for _, f := range sortedListdir(path) {
						if strings.HasSuffix(f, ".xml") {
							files = append(files, pyJoin(path, f))
						}
					}
				} else if (strings.HasSuffix(entry, ".chatlog") || strings.HasSuffix(entry, ".xml")) && isFile(path) ||
					strings.HasSuffix(entry, ".AdiumHTMLLog") {
					files = append(files, path)
				}
			}
			out = append(out, adiumConversation{service, uid, contact, files})
		}
	}
	return out
}

func handleLess(a, b archive.Handle) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Value != b.Value {
		return a.Value < b.Value
	}
	return a.Service < b.Service // a handle without a service (a shorter tuple) first
}

// handleGroup is sorted(set(group)), or nil for a group of fewer than two.
func handleGroup(group []archive.Handle) []archive.Handle {
	seen := map[archive.Handle]bool{}
	var out []archive.Handle
	for _, h := range group {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	if len(out) < 2 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return handleLess(out[i], out[j]) })
	return out
}

// adiumMetacontacts is the owner's grouping of handles into people, from Contact List.plist beside
// the Logs folder: the groups of more than one handle. (Python reads them in the file's order; the
// merge gives the same people in any order, so here they are taken by name.)
func adiumMetacontacts(logs string) [][]archive.Handle {
	path := pyJoin(pyDirname(logs), "Contact List.plist")
	if !exists(path) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var top any
	if _, err := plist.Unmarshal(data, &top); err != nil {
		return nil
	}
	m, _ := top.(map[string]any)
	owned, _ := m["MetaContact Ownership"].(map[string]any)
	keys := make([]string, 0, len(owned))
	for k := range owned {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out [][]archive.Handle
	for _, k := range keys {
		handles, _ := owned[k].([]any)
		var group []archive.Handle
		for _, x := range handles {
			h, _ := x.(map[string]any)
			sid, _ := h["ServiceID"].(string)
			uid, _ := h["UID"].(string)
			if service := imServiceOf(sid); service != "" && uid != "" {
				group = append(group, imHandle(service, uid))
			}
		}
		if g := handleGroup(group); g != nil {
			out = append(out, g)
		}
	}
	return out
}

// adiumTime is Adium's ISO time, with +0300 or +03:00, as Unix ms; false where it is not one.
func adiumTime(value string) (int64, bool) {
	v := imOffset.ReplaceAllString(pyStrip(value), "${1}:${2}")
	t, ok := fromISO(v)
	if !ok {
		return 0, false
	}
	return tsMS(t), true
}

// adiumFile is the messages of one chatlog (XML) or HTML log, in order.
func adiumFile(path, logs, uid string, problems map[string]int) []imRecord {
	rel := pyRelpath(path, logs)
	folder := pyDirname(path)
	data := readText(path)
	me := text.Lower(uid)
	var out []imRecord
	if strings.HasSuffix(path, ".AdiumHTMLLog") {
		base := pyBasename(path)
		m := imHTMLDay.FindStringSubmatch(base)
		if m == nil {
			m = imHTMLOld.FindStringSubmatch(base)
		}
		if m == nil {
			problems["files without a date"]++
			return nil
		}
		y, mo, d := atoiPy(m[1]), atoiPy(m[2]), atoiPy(m[3])
		if y <= 99 {
			y += 2000
		}
		tz := config.Timezone
		imAt(y, time.Month(mo), d, 0, 0, 0, tz)
		var prev wall
		hasPrev := false
		for n, mm := range imHTMLLine.FindAllStringSubmatch(data, -1) {
			direction, stamp, sender, body := mm[1], mm[2], mm[3], mm[4]
			t := imHTMLStamp.FindStringSubmatch(pyStrip(stamp))
			if t == nil {
				problems["lines not read"]++
				continue
			}
			h, mi, s := imClock(atoiPy(t[1]), atoiPy(t[2]), atoiPy(t[3]), t[4])
			dt := imAt(y, time.Month(mo), d, h, mi, s, tz)
			if hasPrev && dt.before(prev.add(-time.Hour)) { // past midnight
				dt = dt.add(24 * time.Hour)
			}
			prev, hasPrev = dt, true
			out = append(out, imRecord{rowKey: fmt.Sprintf("%s#%d", rel, n), ts: tsMS(dt.instant()), outgoing: direction == "send",
				sender: pyStrip(sender), text: imClean(body)})
		}
		return out
	}
	for n, mm := range imMessage.FindAllStringSubmatch(data, -1) {
		attrs := map[string]string{}
		for _, kv := range imAttr.FindAllStringSubmatch(mm[1], -1) {
			attrs[kv[1]] = kv[2]
		}
		body := mm[2]
		ts, ok := adiumTime(attrs["time"])
		sender := pyStrip(attrs["sender"])
		if !ok || sender == "" {
			problems["lines not read"]++
			continue
		}
		var images []string
		for _, src := range imImg.FindAllStringSubmatch(body, -1) {
			if p := pyJoin(folder, src[1]); isFile(p) {
				images = append(images, pyRelpath(p, logs))
			}
		}
		low := text.Lower(sender)
		out = append(out, imRecord{rowKey: fmt.Sprintf("%s#%d", rel, n), ts: ts, outgoing: low == me || strings.HasPrefix(low, me+"/"),
			sender: sender, text: imClean(body), alias: pyStrip(pyUnescape(attrs["alias"])), images: images})
	}
	return out
}

// --- Pidgin --------------------------------------------------------------------------------------

func isPidginLogs(folder string) bool {
	for _, d := range listdir(folder) {
		if isDir(pyJoin(folder, d)) && imServiceOf(d) != "" {
			return true
		}
	}
	return false
}

// pidginRoot is (.purple folder or "", its logs folder) for a .purple folder or a logs folder.
func pidginRoot(root string) (string, string) {
	if !isDir(root) {
		return "", ""
	}
	purple := findDir(root, func(d string) bool {
		return isDir(pyJoin(d, "logs")) && isPidginLogs(pyJoin(d, "logs"))
	}, 4)
	if purple != "" {
		return purple, pyJoin(purple, "logs")
	}
	return "", findDir(root, isPidginLogs, 4)
}

// xmlNode is an element as xml.etree holds it: its tag (namespaced ones never match a plain
// name), attributes, and text up to its first child.
type xmlNode struct {
	tag      string
	attrs    map[string]string
	text     *string
	children []*xmlNode
}

func parseXML(path string) *xmlNode {
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var stack []*xmlNode
	var root *xmlNode
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			panic(fmt.Errorf("%s: %w", path, err))
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xmlNode{tag: t.Name.Local, attrs: map[string]string{}}
			if t.Name.Space != "" {
				n.tag = "{" + t.Name.Space + "}" + t.Name.Local
			}
			for _, a := range t.Attr {
				if a.Name.Space == "" {
					n.attrs[a.Name.Local] = a.Value
				}
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				if len(p.children) == 0 {
					s := string(t)
					if p.text != nil {
						s = *p.text + s
					}
					p.text = &s
				}
			}
		}
	}
	if root == nil {
		panic(fmt.Errorf("%s: no element found", path))
	}
	return root
}

// iter is Element.iter(tag): the element and those under it, in document order.
func (n *xmlNode) iter(tag string, fn func(*xmlNode)) {
	if n.tag == tag {
		fn(n)
	}
	for _, c := range n.children {
		c.iter(tag, fn)
	}
}

// findtext is Element.findtext(tag): the first child's text ("" when it has none), false when
// there is no such child.
func (n *xmlNode) findtext(tag string) (string, bool) {
	for _, c := range n.children {
		if c.tag == tag {
			if c.text == nil {
				return "", true
			}
			return *c.text, true
		}
	}
	return "", false
}

// pidginAccounts is {(protocol dir name, account uid): alias} from accounts.xml, where there is one.
func pidginAccounts(purple string) map[[2]string]string {
	out := map[[2]string]string{}
	if purple == "" {
		return out
	}
	path := pyJoin(purple, "accounts.xml")
	if !exists(path) {
		return out
	}
	parseXML(path).iter("account", func(acc *xmlNode) {
		proto, _ := acc.findtext("protocol")
		name, _ := acc.findtext("name")
		alias, _ := acc.findtext("alias")
		if proto != "" && name != "" {
			uid, _, _ := strings.Cut(name, "/")
			out[[2]string{strings.TrimPrefix(proto, "prpl-"), text.Lower(uid)}] = pyStrip(alias)
		}
	})
	return out
}

type pidginBuddy struct{ service, name, alias string }

// pidginBuddies: the names the owner gave, from blist.xml.
func pidginBuddies(purple string) []pidginBuddy {
	if purple == "" || !exists(pyJoin(purple, "blist.xml")) {
		return nil
	}
	var out []pidginBuddy
	parseXML(pyJoin(purple, "blist.xml")).iter("buddy", func(b *xmlNode) {
		service := imProtocols[b.attrs["proto"]]
		name, _ := b.findtext("name")
		alias, _ := b.findtext("alias")
		if service != "" && name != "" && alias != "" && pyStrip(alias) != "" {
			out = append(out, pidginBuddy{service, pyStrip(name), pyStrip(alias)})
		}
	})
	return out
}

// pidginContacts is the owner's grouping of buddies into one contact, from blist.xml: the contacts
// of more than one handle.
func pidginContacts(purple string) [][]archive.Handle {
	if purple == "" || !exists(pyJoin(purple, "blist.xml")) {
		return nil
	}
	var out [][]archive.Handle
	parseXML(pyJoin(purple, "blist.xml")).iter("contact", func(c *xmlNode) {
		var group []archive.Handle
		c.iter("buddy", func(b *xmlNode) {
			service := imProtocols[b.attrs["proto"]]
			if name, _ := b.findtext("name"); service != "" && name != "" {
				group = append(group, imHandle(service, pyStrip(name)))
			}
		})
		if g := handleGroup(group); g != nil {
			out = append(out, g)
		}
	})
	return out
}

type pidginConversation struct {
	proto, service, uid, contact string
	files                        []string
}

func pidginConversations(logs string) []pidginConversation {
	var out []pidginConversation
	for _, proto := range sortedListdir(logs) {
		service := imServiceOf(proto)
		if service == "" || !isDir(pyJoin(logs, proto)) {
			continue
		}
		for _, acc := range sortedListdir(pyJoin(logs, proto)) {
			accdir := pyJoin(pyJoin(logs, proto), acc)
			if !isDir(accdir) {
				continue
			}
			for _, contact := range sortedListdir(accdir) {
				folder := pyJoin(accdir, contact)
				if !isDir(folder) {
					continue
				}
				var files []string
				for _, f := range sortedListdir(folder) {
					if strings.HasSuffix(f, ".txt") || strings.HasSuffix(f, ".html") {
						files = append(files, pyJoin(folder, f))
					}
				}
				uid, _, _ := strings.Cut(pyUnquote(acc), "/")
				out = append(out, pidginConversation{proto, service, uid, contact, files})
			}
		}
	}
	return out
}

// pidginLines is the log's lines as text, the HTML variant read the same way.
func pidginLines(path string) []string {
	data := readText(path)
	if strings.HasSuffix(path, ".html") {
		data = pidginTitle.ReplaceAllLiteralString(data, "")
		data = imClean(strings.ReplaceAll(imBR.ReplaceAllLiteralString(data, "\n"), "</h3>", "\n"))
	}
	return strings.Split(data, "\n")
}

func pyRstrip(s string) string { return strings.TrimRightFunc(s, archive.IsSpace) }

// pidginFile is each message of a log; a line without a time continues the one before (a message
// of several lines).
func pidginFile(path, logs string, problems map[string]int) []imRecord {
	rel := pyRelpath(path, logs)
	m := pidginFileRE.FindStringSubmatch(pyBasename(path))
	if m == nil {
		problems["files without a date"]++
		return nil
	}
	y, mo, d := atoiPy(m[1]), time.Month(atoiPy(m[2])), atoiPy(m[3])
	tz := config.Timezone
	imAt(y, mo, d, 0, 0, 0, tz)
	offset := m[5]
	var out []imRecord
	var prev wall
	hasPrev := false
	for _, line := range pidginLines(path) {
		mm := pidginLineRE.FindStringSubmatch(line)
		if mm == nil {
			if len(out) > 0 && pyStrip(line) != "" && !strings.HasPrefix(line, "Conversation with ") {
				out[len(out)-1].text += "\n" + pyRstrip(strings.ReplaceAll(line, "\ufeff", ""))
			}
			continue
		}
		said := pidginSaidRE.FindStringSubmatch(mm[5])
		if said == nil || strings.HasPrefix(said[1], "http") || hasAnyPrefix(said[1], pidginNotice) {
			continue // a status line: left out
		}
		h, mi, s := imClock(atoiPy(mm[1]), atoiPy(mm[2]), atoiPy(mm[3]), mm[4])
		dt := imAt(y, mo, d, h, mi, s, imZone(offset))
		if hasPrev && dt.before(prev.add(-time.Hour)) { // past midnight
			dt = dt.add(24 * time.Hour)
		}
		prev, hasPrev = dt, true
		out = append(out, imRecord{rowKey: fmt.Sprintf("%s#%d", rel, len(out)), ts: tsMS(dt.instant()),
			sender: pyStrip(said[1]), text: pyRstrip(strings.ReplaceAll(said[2], "\ufeff", ""))})
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// pidginOwnerNames is the names the owner's messages carry on this account: the account, its
// alias, and any name that speaks in three or more of the account's one-to-one conversations.
func pidginOwnerNames(logs, proto, uid, alias string) map[string]bool {
	at, _, _ := strings.Cut(uid, "@")
	names := map[string]bool{text.Lower(uid): true, text.Lower(at): true}
	if alias != "" {
		names[text.Lower(alias)] = true
	}
	accdir := ""
	for _, d := range listdir(pyJoin(logs, proto)) { // the folder's order, as Python's next() takes it
		u, _, _ := strings.Cut(pyUnquote(d), "/")
		if text.Lower(u) == text.Lower(uid) {
			accdir = pyJoin(pyJoin(logs, proto), d)
			break
		}
	}
	if accdir == "" {
		return names
	}
	seen := map[string]map[string]bool{}
	for _, contact := range listdir(accdir) {
		folder := pyJoin(accdir, contact)
		if !isDir(folder) || strings.HasSuffix(contact, ".chat") {
			continue
		}
		for _, f := range listdir(folder) {
			if strings.HasSuffix(f, ".txt") || strings.HasSuffix(f, ".html") {
				for _, r := range pidginFile(pyJoin(folder, f), logs, map[string]int{}) {
					s := text.Lower(r.sender)
					if seen[s] == nil {
						seen[s] = map[string]bool{}
					}
					seen[s][contact] = true
				}
			}
		}
	}
	for s, contacts := range seen {
		if len(contacts) >= 3 {
			names[s] = true
		}
	}
	return names
}

// --- import --------------------------------------------------------------------------------------

// ImlogsKey is a device and a service ("adium", "msn").
type ImlogsKey struct{ Device, Service string }

// ImlogsStats is what an import found and did.
type ImlogsStats struct {
	Added    map[ImlogsKey]int // messages
	Dupes    map[ImlogsKey]int // skipped as already there (fingerprint)
	Seen     map[ImlogsKey]int // origins already in the archive
	Chats    map[ImlogsKey]map[int64]bool
	Span     map[ImlogsKey][2]int64 // first and last ts
	Outgoing map[ImlogsKey]int
	Images   map[ImlogsKey]int
	Names    map[[2]string]int // (device, kind) -> handle names recorded
	Merged   map[string]int    // device -> people merged into another, by the owner's old groupings
	Problems map[string]int    // files/lines the parsers could not read
}

func newImlogsStats() *ImlogsStats {
	return &ImlogsStats{Added: map[ImlogsKey]int{}, Dupes: map[ImlogsKey]int{}, Seen: map[ImlogsKey]int{},
		Chats: map[ImlogsKey]map[int64]bool{}, Span: map[ImlogsKey][2]int64{}, Outgoing: map[ImlogsKey]int{},
		Images: map[ImlogsKey]int{}, Names: map[[2]string]int{}, Merged: map[string]int{}, Problems: map[string]int{}}
}

func (s *ImlogsStats) note(k ImlogsKey, ts int64, outgoing bool) {
	s.Added[k]++
	if outgoing {
		s.Outgoing[k]++
	}
	span, ok := s.Span[k]
	if !ok {
		span = [2]int64{ts, ts}
	}
	s.Span[k] = [2]int64{min(span[0], ts), max(span[1], ts)}
}

func keyLess(a, b ImlogsKey) bool {
	if a.Device != b.Device {
		return a.Device < b.Device
	}
	return a.Service < b.Service
}

func (s *ImlogsStats) report(out func(string)) {
	set := map[ImlogsKey]bool{}
	for _, m := range []map[ImlogsKey]int{s.Added, s.Dupes, s.Seen} {
		for k := range m {
			set[k] = true
		}
	}
	keys := make([]ImlogsKey, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keyLess(keys[i], keys[j]) })
	say(out, "source                  new    sent  already  dupes  chats  period", nil)
	tz := config.Timezone
	day := func(ms int64) string { return time.UnixMilli(ms).In(tz).Format("2006-01-02") }
	var added, outgoing, seen, dupes, chats int
	for _, k := range keys {
		period := ""
		if span, ok := s.Span[k]; ok && span[0] != 0 {
			period = day(span[0]) + " .. " + day(span[1])
		}
		say(out, "{source} {new} {sent} {already} {dupes} {chats}  {period}", map[string]any{
			"source": fmt.Sprintf("%-18s", k.Device+"/"+k.Service), "new": fmt.Sprintf("%8d", s.Added[k]),
			"sent": fmt.Sprintf("%7d", s.Outgoing[k]), "already": fmt.Sprintf("%8d", s.Seen[k]),
			"dupes": fmt.Sprintf("%6d", s.Dupes[k]), "chats": fmt.Sprintf("%6d", len(s.Chats[k])), "period": period})
	}
	for _, n := range s.Added {
		added += n
	}
	for _, n := range s.Outgoing {
		outgoing += n
	}
	for _, n := range s.Seen {
		seen += n
	}
	for _, n := range s.Dupes {
		dupes += n
	}
	for _, c := range s.Chats {
		chats += len(c)
	}
	say(out, "total              {new} {sent} {already} {dupes} {chats}", map[string]any{
		"new": fmt.Sprintf("%8d", added), "sent": fmt.Sprintf("%7d", outgoing), "already": fmt.Sprintf("%8d", seen),
		"dupes": fmt.Sprintf("%6d", dupes), "chats": fmt.Sprintf("%6d", chats)})
	imageKeys := make([]ImlogsKey, 0, len(s.Images))
	for k := range s.Images {
		imageKeys = append(imageKeys, k)
	}
	sort.Slice(imageKeys, func(i, j int) bool { return keyLess(imageKeys[i], imageKeys[j]) })
	for _, k := range imageKeys {
		say(out, "pictures: {n} {key}", map[string]any{"n": fmt.Sprintf("%6d", s.Images[k]),
			"key": fmt.Sprintf("('%s', '%s')", k.Device, k.Service)})
	}
	nameKeys := make([][2]string, 0, len(s.Names))
	for k := range s.Names {
		nameKeys = append(nameKeys, k)
	}
	sort.Slice(nameKeys, func(i, j int) bool {
		return nameKeys[i][0] < nameKeys[j][0] || nameKeys[i][0] == nameKeys[j][0] && nameKeys[i][1] < nameKeys[j][1]
	})
	for _, k := range nameKeys {
		say(out, "names:    {n} {device} ({kind})", map[string]any{"n": fmt.Sprintf("%6d", s.Names[k]), "device": k[0], "kind": k[1]})
	}
	for _, k := range sortedKeys(s.Merged) {
		say(out, "merged:   {n} people into others, as grouped in {device}", map[string]any{"n": fmt.Sprintf("%6d", s.Merged[k]), "device": k})
	}
	for _, k := range sortedKeys(s.Problems) {
		say(out, "not read: {n} {what}", map[string]any{"n": s.Problems[k], "what": i18n.Tr(k, Lang())})
	}
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type imAliasKey struct {
	h       archive.Handle
	service string
}

type imAlias struct {
	name, kind string
	ts         int64
}

type imImporter struct {
	a         *archive.Archive
	stats     *ImlogsStats
	media     bool
	sources   map[ImlogsKey]int64
	order     []ImlogsKey // the sources in the order they were made
	aliases   map[imAliasKey]imAlias
	aliasOrd  []imAliasKey // the latest display name seen of each, in the order first seen
	store     *Store
	out       func(string)
	moved     map[int64]bool // addresses this run's groupings moved to another person
	groupings []imGrouping   // the owner's groupings of each program, applied at the end
}

type imGrouping struct {
	device string
	groups [][]archive.Handle
}

func (imp *imImporter) source(device, service, root string) int64 {
	k := ImlogsKey{device, service}
	if id, ok := imp.sources[k]; ok {
		return id
	}
	id := imp.a.Source(device+"/"+service, root, device, root)
	imp.sources[k] = id
	imp.order = append(imp.order, k)
	return id
}

// imChat is a conversation made when its first message is added: a log with none makes no chat.
type imChat struct {
	service string
	group   bool
	key     string // the conversation's key: the group's, or the peer's value
	peer    archive.Handle
	made    int64
}

func (imp *imImporter) chat(service string, group bool, key string, peer archive.Handle) *imChat {
	c := &imChat{service: service, group: group, key: key, peer: peer}
	if !group {
		c.key = peer.Value
	}
	return c
}

func (imp *imImporter) conv(c *imChat) int64 {
	if c.made == 0 {
		a := imp.a
		if c.group {
			c.made = a.Conversation(c.service, nil, c.key, "")
			a.Exec("UPDATE conversation SET is_group = 1 WHERE id = ?", c.made)
		} else {
			c.made = a.Conversation(c.service, []archive.Handle{c.peer}, "", "")
		}
	}
	return c.made
}

// accountIn: the owner's account the logs were written by is a member of the conversation (which
// of the owner's accounts a chat was on). Also on a later run, when every message is already there.
func (imp *imImporter) accountIn(service string, c *imChat, own archive.Handle) {
	a := imp.a
	cid, ok := c.made, c.made != 0
	if !ok {
		cid, ok = a.IntOK("SELECT id FROM conversation WHERE service_id = ? AND key = ?", a.Service.ID(service), c.key)
	}
	if ok {
		a.Exec("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", cid, a.Address(own))
	}
}

// add puts one message into the archive, unless its origin or its fingerprint is already there;
// its id, 0 for none.
func (imp *imImporter) add(device, service string, src int64, c *imChat, rec imRecord, sender archive.Handle) int64 {
	a, key := imp.a, ImlogsKey{device, service}
	if a.HasOrigin(src, rec.rowKey, "") {
		imp.stats.Seen[key]++
		return 0
	}
	kind := "text"
	if len(rec.images) > 0 && rec.text == "" {
		kind = "image"
	}
	if rec.text == "" && len(rec.images) == 0 {
		return 0
	}
	conv := imp.conv(c)
	fp := archive.Fingerprint(rec.ts, rec.outgoing, kind, rec.text)
	if a.Exists("SELECT 1 FROM message WHERE conversation_id = ? AND fingerprint = ?", conv, fp) {
		imp.stats.Dupes[key]++
		return 0
	}
	var senderID int64
	if !rec.outgoing {
		senderID = a.Address(sender)
	}
	mid := a.AddMessage(src, rec.rowKey, archive.Message{Service: service, ConversationID: conv, TS: rec.ts,
		Outgoing: rec.outgoing, SenderID: senderID, Kind: kind, Text: rec.text})
	imp.stats.note(key, rec.ts, rec.outgoing)
	if imp.stats.Chats[key] == nil {
		imp.stats.Chats[key] = map[int64]bool{}
	}
	imp.stats.Chats[key][conv] = true
	if rec.alias != "" && !rec.outgoing {
		ak := imAliasKey{sender, service}
		cur, ok := imp.aliases[ak]
		if !ok || cur.ts < rec.ts {
			if !ok {
				imp.aliasOrd = append(imp.aliasOrd, ak)
			}
			imp.aliases[ak] = imAlias{rec.alias, "chat", rec.ts}
		}
	}
	return mid
}

func (imp *imImporter) picture(device, service string, src int64, root, rel string, mid int64) {
	imp.stats.Images[ImlogsKey{device, service}]++
	if !imp.media || mid == 0 {
		return
	}
	if imp.store == nil {
		imp.store = NewStore(imp.a)
	}
	imp.store.Link(device, src, pyJoin(root, rel), rel, mid)
}

// movedPerson is a private copy of core's labels.moved_person (with _move_labels by person_id):
// when the person `other` becomes part of `into`, their labels go over (the user's word winning
// over the models'), a name found for them too where `into` has none, and both are to be read
// again. Copied here because package core belongs to another part of the port.
func movedPerson(a *archive.Archive, into, other int64) {
	a.Exec("INSERT INTO person_label (person_id, label_id, state, votes, models, evidence, at) "+
		"SELECT ?, label_id, state, votes, models, evidence, at FROM person_label WHERE person_id = ? "+
		"ON CONFLICT (person_id, label_id) DO UPDATE SET state = excluded.state, votes = excluded.votes, "+
		"models = excluded.models, evidence = excluded.evidence, at = excluded.at "+
		"WHERE person_label.state = 'suggested' AND excluded.state != 'suggested'", into, other)
	a.Exec("DELETE FROM person_label WHERE person_id = ?", other)
	a.Exec("UPDATE OR IGNORE name_guess SET person_id = ? WHERE person_id = ?", into, other)
	a.Exec("DELETE FROM name_guess WHERE person_id = ?", other)
	a.Exec("DELETE FROM analysis WHERE person_id IN (?, ?)", into, other)
}

// merge: each group of handles the owner had grouped as one person, the people of those handles
// the archive has become one (the lowest id), the owner's own handles left out, and those placed by
// hand before this run (how 'manual': merged by an earlier import, or split off by the owner
// since): a later import does not undo the owner's decisions in the app.
func (imp *imImporter) merge(device string, groups [][]archive.Handle) {
	a := imp.a
	own := a.Own()
	for _, group := range groups {
		set := map[int64]bool{}
		for _, h := range group {
			if own[h] || !a.Known(h) {
				continue
			}
			aid := a.Address(h)
			var pid int64
			var how string
			a.Row("SELECT person_id, how FROM person_address WHERE address_id = ?", []any{aid}, &pid, &how)
			if how != "manual" || imp.moved[aid] {
				set[pid] = true
			}
		}
		people := make([]int64, 0, len(set))
		for p := range set {
			people = append(people, p)
		}
		sort.Slice(people, func(i, j int) bool { return people[i] < people[j] })
		for _, other := range people[min(1, len(people)):] {
			for _, aid := range a.Ints("SELECT address_id FROM person_address WHERE person_id = ?", other) {
				imp.moved[aid] = true
			}
			a.Exec("UPDATE person_address SET person_id = ?, how = 'manual' WHERE person_id = ?", people[0], other)
			movedPerson(a, people[0], other)
			a.Exec("DELETE FROM person WHERE id = ?", other)
			imp.stats.Merged[device]++
		}
	}
}

func (imp *imImporter) finishNames(device string) {
	for _, k := range imp.aliasOrd {
		v := imp.aliases[k]
		imp.a.HandleName(k.h, k.service, v.name, v.kind, floorDiv(v.ts, 1000))
		imp.stats.Names[[2]string{device, v.kind}]++
	}
	imp.aliases = map[imAliasKey]imAlias{}
	imp.aliasOrd = nil
}

func (imp *imImporter) imported(device string) {
	for _, k := range imp.order {
		if k.Device == device {
			imp.a.Imported(imp.sources[k])
		}
	}
}

func (imp *imImporter) adium(root string) {
	logs := adiumLogs(root)
	if logs == "" {
		say(imp.out, "no Adium folder: {path}", map[string]any{"path": root})
		return
	}
	a, device := imp.a, imDevices["adium"]
	for _, ac := range adiumConversations(logs) {
		service := ac.service
		own := imHandle(service, ac.uid)
		a.Account(own, service, "")
		src := imp.source(device, service, logs)
		group := imIsGroup(service, ac.contact)
		var peer archive.Handle
		if !group {
			peer = imHandle(service, ac.contact)
		}
		c := imp.chat(service, group, text.Lower(pyUnquote(ac.contact)), peer)
		for _, path := range ac.files {
			for _, rec := range adiumFile(path, logs, ac.uid, imp.stats.Problems) {
				sender := peer
				if group {
					sender = imHandle(service, rec.sender)
				} else if rec.outgoing {
					sender = own
				}
				mid := imp.add(device, service, src, c, rec, sender)
				for _, rel := range rec.images {
					imp.picture(device, service, src, logs, rel, mid)
				}
			}
		}
		imp.accountIn(service, c, own)
		a.Commit()
	}
	imp.finishNames(device)
	imp.groupings = append(imp.groupings, imGrouping{device, adiumMetacontacts(logs)})
	imp.imported(device)
	a.Commit()
}

func (imp *imImporter) pidgin(root string) {
	purple, logs := pidginRoot(root)
	if logs == "" {
		say(imp.out, "no Pidgin folder: {path}", map[string]any{"path": root})
		return
	}
	a, device := imp.a, imDevices["pidgin"]
	accounts := pidginAccounts(purple)
	owners := map[[2]string]map[string]bool{}
	for _, pc := range pidginConversations(logs) {
		service, uid := pc.service, pc.uid
		own := imHandle(service, uid)
		a.Account(own, service, "")
		src := imp.source(device, service, logs)
		ok := [2]string{pc.proto, uid}
		if _, done := owners[ok]; !done {
			owners[ok] = pidginOwnerNames(logs, pc.proto, uid, accounts[[2]string{pc.proto, text.Lower(uid)}])
		}
		names := owners[ok]
		group := imIsGroup(service, pc.contact)
		var peer archive.Handle
		if !group {
			peer = imHandle(service, pc.contact)
		}
		c := imp.chat(service, group, strings.TrimSuffix(text.Lower(pyUnquote(pc.contact)), ".chat"), peer)
		for _, path := range pc.files {
			for _, rec := range pidginFile(path, logs, imp.stats.Problems) {
				s := text.Lower(rec.sender)
				rec.outgoing = names[s] || strings.HasPrefix(s, text.Lower(uid)+"/")
				who := peer
				if rec.outgoing {
					who = own
				} else if group {
					who = archive.H("name", rec.sender, service)
				}
				imp.add(device, service, src, c, rec, who)
			}
		}
		imp.accountIn(service, c, own)
		a.Commit()
	}
	for _, b := range pidginBuddies(purple) {
		h := imHandle(b.service, b.name)
		if a.Known(h) {
			a.HandleName(h, b.service, b.alias, "book", 0)
			imp.stats.Names[[2]string{device, "book"}]++
		}
	}
	imp.groupings = append(imp.groupings, imGrouping{device, pidginContacts(purple)})
	imp.imported(device)
	a.Commit()
}

// ImlogsOptions: the folders ("" for config [imlogs] adium and pidgin), and whether to leave the
// pictures out.
type ImlogsOptions struct {
	Adium, Pidgin string
	NoMedia       bool
}

// Imlogs imports what is given: an Adium folder (Adium 2.0, Users/Default or Logs) and a .purple
// folder (or its logs). Adium first: its files are the fuller record. Nil stats when there is no
// source.
func Imlogs(a *archive.Archive, out func(string), opt ImlogsOptions) (stats *ImlogsStats, err error) {
	defer archive.Recover(&err)
	adium, pidgin := opt.Adium, opt.Pidgin
	if adium == "" {
		adium = config.String("imlogs", "adium", "")
	}
	if pidgin == "" {
		pidgin = config.String("imlogs", "pidgin", "")
	}
	if adium == "" && pidgin == "" {
		say(out, "no source: neither [imlogs] adium nor [imlogs] pidgin", nil)
		return nil, nil
	}
	stats = newImlogsStats()
	imp := &imImporter{a: a, stats: stats, media: !opt.NoMedia, sources: map[ImlogsKey]int64{},
		aliases: map[imAliasKey]imAlias{}, out: out, moved: map[int64]bool{}}
	if adium != "" {
		imp.adium(config.ExpandUser(adium))
	}
	if pidgin != "" {
		imp.pidgin(config.ExpandUser(pidgin))
	}
	// the groupings once both programs' handles are in: one of Adium's may name a handle only
	// Pidgin's logs know (applied before them, it waited for the next run)
	for _, g := range imp.groupings {
		imp.merge(g.device, g.groups)
	}
	a.Resolve()
	a.Commit()
	stats.report(out)
	return stats, nil
}

// ImlogsImport is scripts/imlogs-import.py: the import into the archive at dbPath ("" for the
// archive's own), or, with dryRun, into a copy of it in a temporary folder, without the pictures,
// the folder removed after (what it says "new" is what a real run would add). ok is false when
// there was no source.
func ImlogsImport(dbPath, adium, pidgin string, dryRun bool, out func(string)) (ok bool, err error) {
	defer archive.Recover(&err)
	if dbPath == "" {
		dbPath = archive.DB()
	}
	opt := ImlogsOptions{Adium: adium, Pidgin: pidgin}
	if !dryRun {
		a, err := archive.Open(dbPath)
		if err != nil {
			return false, err
		}
		defer a.Close()
		stats, err := Imlogs(a, out, opt)
		return stats != nil, err
	}
	tmp, err := os.MkdirTemp("", "everysaid-imlogs-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	path := filepath.Join(tmp, "archive.db")
	if exists(dbPath) {
		// copied by SQLite, not as a file: what is committed but still in its write-ahead log is in
		// the copy too, and the archive is never held in memory whole
		d := ro(dbPath)
		_, err := d.Exec("VACUUM INTO ?", path)
		d.Close()
		if err != nil {
			return false, err
		}
	}
	a, err := archive.Open(path)
	if err != nil {
		return false, err
	}
	opt.NoMedia = true
	stats, err := Imlogs(a, out, opt)
	a.Close()
	if err != nil {
		return false, err
	}
	say(out, "(dry run: nothing written)", nil)
	return stats != nil, nil
}
