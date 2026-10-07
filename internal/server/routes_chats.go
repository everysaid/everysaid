// The routes of chats, messages and search (app.py, "chats and messages").
package server

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"everysaid/internal/core"
	"everysaid/internal/errs"
	"everysaid/internal/i18n"
	"everysaid/internal/plugins"
)

// split is a comma-separated list, without empty parts.
func split(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// setting is a shared setting's truth (as Python's bool(store.setting(k, def))).
func (s *Server) setting(key string, def bool) bool { return truthy(s.Store.SettingAny(key, def)) }

// labelled is the people with a label (theirs by the user, or suggested where the user shows those).
func (s *Server) labelled(label int64) map[int64]bool {
	tagged := core.ByPerson(s.Store, s.setting("show_tone", false))
	only := map[int64]bool{}
	for pid, ls := range tagged {
		for _, x := range ls {
			if id, _ := asInt(x["id"]); id == label {
				only[pid] = true
				break
			}
		}
	}
	return only
}

func clampLimit(n, hi int64) int {
	return int(max(1, min(n, hi)))
}

func (s *Server) chatRoutes() {
	h := s.handle

	// unnamed: with the people without a name (else as the setting says); min_messages, max_messages;
	// services, no_services: comma-separated, the chats with each of these and none of those; label:
	// only the chats of the people with it.
	h("GET /api/chats", bodyNone, func(q *req) (any, error) {
		archived, err := q.boolQ("archived", false)
		if err != nil {
			return nil, err
		}
		limit, err := q.optInt("limit")
		if err != nil {
			return nil, err
		}
		offset, err := q.intQ("offset", 0)
		if err != nil {
			return nil, err
		}
		unnamed, err := q.optBool("unnamed")
		if err != nil {
			return nil, err
		}
		minMsgs, err := q.intQ("min_messages", 0)
		if err != nil {
			return nil, err
		}
		maxMsgs, err := q.optInt("max_messages")
		if err != nil {
			return nil, err
		}
		label, err := q.intQ("label", 0)
		if err != nil {
			return nil, err
		}
		o := core.ChatsOptions{IncludeArchived: archived, Kind: q.str("kind"), Q: q.str("q"), Offset: int(max(0, offset)),
			MinMessages: max(0, minMsgs), WithServices: split(q.str("services")), WithoutServices: split(q.str("no_services")),
			EmptyGroups: !s.setting("hide_empty_groups", true), Short: s.setting("show_short_numbers", false)}
		if limit != nil {
			o.Limit = int(max(0, *limit)) // 0: all, as None
		}
		if unnamed != nil {
			o.Unnamed = *unnamed
		} else {
			o.Unnamed = s.setting("show_unnamed", false)
		}
		if maxMsgs != nil {
			o.MaxMessages, o.HasMax = *maxMsgs, true
		}
		if label != 0 {
			o.PeopleOnly = s.labelled(label)
		}
		return M{"items": core.Chats(s.Store, o)}, nil
	})

	h("GET /api/chats/{chat_id}", bodyNone, func(q *req) (any, error) {
		c := core.GetChat(s.Store, q.r.PathValue("chat_id"))
		if c == nil {
			return nil, notFound("")
		}
		can := s.Host.Able("")
		answer := s.Host.Able("can_reply")
		mention := s.Host.Able("can_mention")
		files := s.Host.Able("can_send_files")
		lang := q.xlang()
		services, _ := c["services"].([]string)
		sendable, replyable, mentionable, fileable := []string{}, []string{}, []string{}, []string{}
		for _, x := range services {
			if can[x] {
				sendable = append(sendable, x)
			}
			if answer[x] {
				replyable = append(replyable, x)
			}
			if mention[x] {
				mentionable = append(mentionable, x)
			}
			if files[x] {
				fileable = append(fileable, x)
			}
		}
		unsendable := M{}
		for svc, why := range s.Host.Unsendable() {
			if contains(services, svc) && !can[svc] {
				unsendable[svc] = i18n.Tr(why, lang)
			}
		}
		c["sendable"], c["replyable"], c["mentionable"], c["fileable"], c["unsendable"] = sendable, replyable, mentionable, fileable, unsendable
		// what can be done to a message, by service: the emoji (null: any), whether any other emoji
		// may be put too, and how long after sending an edit or a deletion for everyone is allowed
		react, free, edit, del := s.Host.Reactable()
		reactions, freeReactions, editable, deletable := M{}, M{}, M{}, M{}
		for _, x := range services {
			if r, ok := react[x]; ok {
				reactions[x], freeReactions[x] = r, free[x]
			}
			if e, ok := edit[x]; ok {
				editable[x] = e
			}
			if d, ok := del[x]; ok {
				deletable[x] = d
			}
		}
		c["reactions"], c["free_reactions"], c["editable"], c["deletable"] = reactions, freeReactions, editable, deletable
		return c, nil
	})

	h("POST /api/chats/{chat_id}/merge", bodyRequired, func(q *req) (any, error) {
		other := ""
		if truthy(q.get("other")) {
			other = pyStr(q.get("other"))
		}
		id, err := core.MergeGroups(s.Store, q.r.PathValue("chat_id"), other)
		if err != nil {
			return nil, is404(err, nil)
		}
		return M{"id": id}, nil
	})

	h("POST /api/chats/{chat_id}/split", bodyRequired, func(q *req) (any, error) {
		var conv int64
		if truthy(q.get("conversation")) {
			n, err := pyInt(q.get("conversation"))
			if err != nil {
				return nil, failed(400, err.Error())
			}
			conv = n
		}
		id, err := core.SplitGroup(s.Store, q.r.PathValue("chat_id"), conv)
		if err != nil {
			return nil, is404(err, nil)
		}
		return M{"id": id}, nil
	})

	h("GET /api/groups/suggestions", bodyNone, func(q *req) (any, error) {
		return M{"items": core.GroupSuggestions(s.Store, q.str("chat"), 50)}, nil
	})

	h("POST /api/groups/suggestions/dismiss", bodyRequired, func(q *req) (any, error) {
		var chats []string
		if list, ok := q.get("chats").([]any); ok {
			for _, c := range list {
				chats = append(chats, pyStr(c))
			}
		}
		if err := core.DismissGroupMerge(s.Store, chats); err != nil {
			return nil, is404(err, nil)
		}
		return M{"ok": true}, nil
	})

	h("GET /api/chats/{chat_id}/stream", bodyNone, func(q *req) (any, error) {
		around, err := q.optInt("around")
		if err != nil {
			return nil, err
		}
		limit, err := q.intQ("limit", 60)
		if err != nil {
			return nil, err
		}
		o := core.StreamOptions{Before: q.str("before"), After: q.str("after"), Around: around, Limit: clampLimit(limit, 200)}
		if hide := q.str("hide"); hide != "" {
			o.Hidden = strings.Split(hide, ",")
		}
		page, err := core.Stream(s.Store, q.r.PathValue("chat_id"), o)
		if err != nil {
			return nil, is404(err, func(e error) error { return failed(400, e.Error()) })
		}
		return page, nil
	})

	h("PATCH /api/chats/{chat_id}", bodyRequired, func(q *req) (any, error) {
		fields := map[string]core.StateValue{}
		for _, k := range []string{"pinned", "muted", "archived", "read_until"} {
			if v, ok := q.body[k]; ok {
				fields[k] = v
			}
		}
		if err := core.SetChatState(s.Store, q.r.PathValue("chat_id"), truthy(q.get("always")), fields); err != nil {
			return nil, is404(err, nil)
		}
		return M{"ok": true}, nil
	})

	h("POST /api/chats/{chat_id}/read", bodyNone, func(q *req) (any, error) {
		chat := q.r.PathValue("chat_id")
		if err := core.SetChatState(s.Store, chat, false, map[string]core.StateValue{"read_until": "now"}); err != nil {
			return nil, is404(err, nil)
		}
		// the services' read receipts, where the user allows them: on their own, not waited for
		s.Host.MarkReadSoon(chat, time.Now().UnixMilli())
		return M{"ok": true}, nil
	})

	// A JSON body {text, conversation_id, service, reply_to, mentions}, or a form with the same
	// fields (mentions as JSON) and a `file`, sent with the text as its caption.
	h("POST /api/chats/{chat_id}/send", bodyNone, func(q *req) (any, error) {
		var file *plugins.File
		if strings.HasPrefix(q.r.Header.Get("Content-Type"), "multipart/form-data") {
			if q.r.ContentLength > MaxUpload+1_000_000 {
				return nil, errs.New("file_too_large", 413, M{"mb": MaxUpload / 1_000_000})
			}
			body, f, err := readForm(q)
			if err != nil {
				return nil, err
			}
			q.body, file = body, f
		} else {
			b, err := io.ReadAll(http.MaxBytesReader(q.w, q.r.Body, 10<<20))
			if err != nil {
				return nil, failed(400, "body")
			}
			v, err := decodeJSON(b)
			m, ok := v.(map[string]any)
			if err != nil || !ok {
				return nil, failed(400, "body")
			}
			q.body = m
		}
		text := strings.TrimFunc(q.text("text"), isPySpace)
		if text == "" && file == nil {
			return nil, errs.New("empty_message", 400, nil)
		}
		mentions, err := checkMentions(text, q.get("mentions"))
		if err != nil {
			return nil, err
		}
		r := SendRequest{Text: text, Service: q.text("service"), Mentions: mentions, File: file}
		if truthy(q.get("conversation_id")) {
			if r.ConversationID, err = pyInt(q.get("conversation_id")); err != nil {
				return nil, failed(409, err.Error())
			}
		}
		if truthy(q.get("reply_to")) {
			if r.ReplyTo, err = pyInt(q.get("reply_to")); err != nil {
				return nil, failed(409, err.Error())
			}
		}
		out, err := s.Host.Send(q.r.Context(), q.r.PathValue("chat_id"), r)
		if err != nil {
			return nil, is404(err, func(e error) error { return failed(409, e.Error()) })
		}
		return out, nil
	})

	act := func(q *req, a MessageAction) (any, error) {
		mid, err := q.pathInt("mid")
		if err != nil {
			return nil, err
		}
		out, err := s.Host.Act(q.r.Context(), mid, a)
		if err != nil {
			return nil, is404(err, func(e error) error { return failed(409, e.Error()) })
		}
		return out, nil
	}

	// the user's reaction on a message: an emoji puts it (in place of theirs), "" takes it back
	h("POST /api/messages/{mid}/reaction", bodyRequired, func(q *req) (any, error) {
		emoji := strings.TrimFunc(q.text("emoji"), isPySpace)
		if len([]rune(emoji)) > 16 {
			return nil, failed(400, "emoji")
		}
		return act(q, MessageAction{Kind: "react", Emoji: emoji})
	})

	h("POST /api/messages/{mid}/edit", bodyRequired, func(q *req) (any, error) {
		text := strings.TrimFunc(q.text("text"), isPySpace)
		if text == "" {
			return nil, errs.New("empty_message", 400, nil)
		}
		return act(q, MessageAction{Kind: "edit", Text: text})
	})

	// deleted for everyone in the chat, through its service
	h("POST /api/messages/{mid}/delete", bodyRequired, func(q *req) (any, error) {
		return act(q, MessageAction{Kind: "delete"})
	})

	h("GET /api/messages/{mid}/receipts", bodyNone, func(q *req) (any, error) {
		mid, err := q.pathInt("mid")
		if err != nil {
			return nil, err
		}
		items := core.Receipts(s.Store, mid)
		if items == nil {
			return nil, notFound("")
		}
		return M{"items": items}, nil
	})

	h("GET /api/messages/{mid}", bodyNone, func(q *req) (any, error) {
		mid, err := q.pathInt("mid")
		if err != nil {
			return nil, err
		}
		m := core.GetMessage(s.Store, mid)
		if m == nil {
			return nil, notFound("")
		}
		return m, nil
	})

	h("GET /api/messages/{mid}/context", bodyNone, func(q *req) (any, error) {
		mid, err := q.pathInt("mid")
		if err != nil {
			return nil, err
		}
		n, err := q.intQ("n", 15)
		if err != nil {
			return nil, err
		}
		out, err := core.Context(s.Store, mid, int(max(0, min(n, 100))))
		if err != nil {
			return nil, is404(err, nil)
		}
		if out == nil {
			return nil, notFound("")
		}
		return out, nil
	})

	h("GET /api/search", bodyNone, func(q *req) (any, error) {
		o := core.SearchOptions{ChatID: q.str("chat"), Service: q.str("service"), Kind: q.str("kind")}
		var err error
		if o.Since, err = q.optInt("since"); err != nil {
			return nil, err
		}
		if o.Until, err = q.optInt("until"); err != nil {
			return nil, err
		}
		if o.Outgoing, err = q.optBool("outgoing"); err != nil {
			return nil, err
		}
		limit, err := q.intQ("limit", 50)
		if err != nil {
			return nil, err
		}
		offset, err := q.intQ("offset", 0)
		if err != nil {
			return nil, err
		}
		o.Limit, o.Offset = clampLimit(limit, 200), int(max(0, offset))
		if o.Case, err = q.boolQ("case", false); err != nil {
			return nil, err
		}
		if o.Whole, err = q.boolQ("whole", false); err != nil {
			return nil, err
		}
		if o.Archived, err = q.optBool("archived"); err != nil {
			return nil, err
		}
		out, err := core.Search(s.Store, q.str("q"), o)
		if err != nil {
			return nil, is404(err, func(e error) error { return failed(400, e.Error()) })
		}
		return out, nil
	})
}

// isPySpace is Python's str.isspace() of a character (what strip() takes away).
func isPySpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// readForm reads a form with at most one file and ten fields; the text's line breaks as one (a
// form may send them as CRLF: the mentions count them as one).
func readForm(q *req) (map[string]any, *plugins.File, error) {
	q.r.Body = http.MaxBytesReader(q.w, q.r.Body, MaxUpload+1_000_000)
	mr, err := q.r.MultipartReader()
	if err != nil {
		return nil, nil, failed(400, "body")
	}
	body := map[string]any{}
	var file *plugins.File
	fields := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, tooLarge(err)
		}
		if part.FileName() != "" {
			if file != nil {
				part.Close()
				return nil, nil, failed(400, "Too many files. Maximum number of files is 1.")
			}
			data, err := io.ReadAll(io.LimitReader(part, MaxUpload+1))
			if err != nil {
				return nil, nil, tooLarge(err)
			}
			if len(data) > MaxUpload {
				return nil, nil, errs.New("file_too_large", 413, M{"mb": MaxUpload / 1_000_000})
			}
			if part.FormName() == "file" {
				name := filepath.Base(strings.ReplaceAll(part.FileName(), `\`, "/"))
				if name == "." || name == "/" || name == "" {
					name = "file"
				}
				typ := part.Header.Get("Content-Type")
				if typ == "" {
					typ = guessType(part.FileName())
				}
				file = &plugins.File{Data: data, Filename: name, MimeType: typ}
			}
			continue
		}
		fields++
		if fields > 10 {
			return nil, nil, failed(400, "Too many fields. Maximum number of fields is 10.")
		}
		b, err := io.ReadAll(io.LimitReader(part, 1<<20))
		if err != nil {
			return nil, nil, tooLarge(err)
		}
		body[part.FormName()] = string(b) // the last of a name, as Starlette's form gives it
	}
	body["text"] = strings.ReplaceAll(stringOf(body["text"]), "\r\n", "\n")
	if m := stringOf(body["mentions"]); m != "" {
		v, err := decodeJSON([]byte(m))
		if err != nil {
			return nil, nil, failed(400, "mentions")
		}
		body["mentions"] = v
	} else {
		body["mentions"] = nil
	}
	return body, file, nil
}

func stringOf(v any) string { s, _ := v.(string); return s }

func tooLarge(err error) error {
	var mb *http.MaxBytesError
	if errors.As(err, &mb) {
		return errs.New("file_too_large", 413, M{"mb": MaxUpload / 1_000_000})
	}
	if errors.Is(err, multipart.ErrMessageTooLarge) {
		return errs.New("file_too_large", 413, M{"mb": MaxUpload / 1_000_000})
	}
	return failed(400, "body")
}

// checkMentions is [{start, length, address_id}] as given, each a "@..." within the text (in
// characters), none over another; else the request is refused.
func checkMentions(text string, given any) ([]plugins.Mention, error) {
	bad := failed(400, "mentions")
	if given == nil {
		return nil, nil
	}
	var list []any
	switch g := given.(type) {
	case []any:
		list = g
	case string: // Python iterates a string's characters: none of them is a mention
		if g == "" {
			return nil, nil
		}
		return nil, bad
	case map[string]any:
		if len(g) == 0 {
			return nil, nil
		}
		return nil, bad
	default:
		if !truthy(g) {
			return nil, nil
		}
		return nil, bad
	}
	var out []plugins.Mention
	for _, x := range list {
		m, ok := x.(map[string]any)
		if !ok {
			return nil, bad
		}
		start, e1 := pyInt(m["start"])
		length, e2 := pyInt(m["length"])
		aid, e3 := pyInt(m["address_id"])
		if e1 != nil || e2 != nil || e3 != nil {
			return nil, bad
		}
		out = append(out, plugins.Mention{Start: int(start), Length: int(length), AddressID: aid})
	}
	sortMentions(out)
	runes := []rune(text)
	end := 0
	for _, m := range out {
		// as written, never m.Start+m.Length: huge numbers would wrap around
		if m.Start < end || m.Start < 0 || m.Length < 2 || m.Start > len(runes) || m.Length > len(runes)-m.Start ||
			runes[m.Start] != '@' {
			return nil, bad
		}
		end = m.Start + m.Length
	}
	return out, nil
}

func sortMentions(ms []plugins.Mention) {
	for i := 1; i < len(ms); i++ { // stable, by start
		for j := i; j > 0 && ms[j].Start < ms[j-1].Start; j-- {
			ms[j], ms[j-1] = ms[j-1], ms[j]
		}
	}
}
