// Ports everysaid/mcp_server.py: the tools.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/plugins"
)

// prop is a tool's argument: its JSON type, default (nil: none, the argument may be null) and words.
type prop struct {
	name, typ string
	def       any
	desc      string
}

func arg(name, typ string, def any) prop { return prop{name: name, typ: typ, def: def} }

// object is a tool's input schema; required arguments first, as named.
func object(required []string, props ...prop) *jsonschema.Schema {
	s := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}, Required: required}
	need := map[string]bool{}
	for _, r := range required {
		need[r] = true
	}
	for _, p := range props {
		ps := &jsonschema.Schema{Description: p.desc}
		switch {
		case need[p.name]:
			ps.Type = p.typ
		case p.def != nil:
			ps.Type = p.typ
			ps.Default, _ = json.Marshal(p.def)
		default:
			ps.Types = []string{p.typ, "null"}
		}
		s.Properties[p.name] = ps
	}
	return s
}

var (
	objectOut = &jsonschema.Schema{Type: "object"}
	listOut   = &jsonschema.Schema{Type: "object", Required: []string{"result"},
		Properties: map[string]*jsonschema.Schema{"result": {Type: "array"}}}
	no = false
)

// withContent is a tool's answer with more content than its JSON (a file itself).
type withContent struct {
	value any
	extra []sdk.Content
}

// tool adds a tool: its answer is JSON (a list as {"result": [...]}, as the Python SDK gives it),
// a failure a tool error. A failing statement (a panic of the core) is a tool error too, not the
// end of the server.
func tool[In any](srv *sdk.Server, name, desc string, in *jsonschema.Schema, list, writes bool,
	fn func(ctx context.Context, in In) (any, error)) {
	ann := &sdk.ToolAnnotations{ReadOnlyHint: !writes, OpenWorldHint: &no}
	if writes {
		ann.DestructiveHint = &no
		ann.IdempotentHint = true
	}
	out := objectOut
	if list {
		out = listOut
	}
	sdk.AddTool(srv, &sdk.Tool{Name: name, Description: desc, InputSchema: in, OutputSchema: out, Annotations: ann},
		func(ctx context.Context, _ *sdk.CallToolRequest, args In) (res *sdk.CallToolResult, structured any, err error) {
			defer func() {
				if r := recover(); r != nil {
					res, structured = nil, nil
					if e, ok := r.(error); ok {
						err = e
					} else {
						err = fmt.Errorf("%v", r)
					}
				}
			}()
			v, err := fn(ctx, args)
			if err != nil {
				return nil, nil, err
			}
			var extra []sdk.Content
			if wc, ok := v.(withContent); ok {
				v, extra = wc.value, wc.extra
			}
			text, err := json.Marshal(v)
			if err != nil {
				return nil, nil, err
			}
			structured = v
			if list {
				structured = core.M{"result": v}
			}
			return &sdk.CallToolResult{Content: append([]sdk.Content{&sdk.TextContent{Text: string(text)}}, extra...)},
				structured, nil
		})
}

func addTools(srv *sdk.Server, env Env) {
	store := env.Store

	type searchIn struct {
		Query      string `json:"query"`
		Chat       string `json:"chat"`
		Service    string `json:"service"`
		Since      string `json:"since"`
		Until      string `json:"until"`
		Limit      int    `json:"limit"`
		Offset     int    `json:"offset"`
		MatchCase  bool   `json:"match_case"`
		WholeWords bool   `json:"whole_words"`
	}
	tool(srv, "search_messages", "Messages containing every word of `query`, newest first: anywhere, inside words too, accents "+
		"and case ignored; match_case: as typed; whole_words: whole words only (a word ending in * a "+
		"prefix). chat: a chat id to search within; service: a service id (as in the instructions or "+
		"list_chats); since/until: YYYY-MM-DD, both days included.",
		object([]string{"query"}, arg("query", "string", nil), arg("chat", "string", nil), arg("service", "string", nil),
			arg("since", "string", nil), arg("until", "string", nil), arg("limit", "integer", 30), arg("offset", "integer", 0),
			arg("match_case", "boolean", false), arg("whole_words", "boolean", false)), false, false,
		func(ctx context.Context, in searchIn) (any, error) {
			since, until, err := span(in.Since, in.Until)
			if err != nil {
				return nil, err
			}
			r, err := core.Search(store, in.Query, core.SearchOptions{ChatID: in.Chat, Service: in.Service, Since: since,
				Until: until, Limit: capped(in.Limit, 30, 100), Offset: max(in.Offset, 0), Case: in.MatchCase, Whole: in.WholeWords})
			if err != nil {
				return nil, err
			}
			items := []core.M{}
			for _, it := range r["items"].([]core.M) {
				m := slim(it)
				m["chat_title"] = it["chat_title"]
				items = append(items, m)
			}
			return core.M{"total": r["total"], "items": items}, nil
		})

	type chatsIn struct {
		Query           string `json:"query"`
		Kind            string `json:"kind"`
		Limit           int    `json:"limit"`
		Offset          int    `json:"offset"`
		IncludeArchived bool   `json:"include_archived"`
		LastMessage     bool   `json:"include_last_message"`
	}
	tool(srv, "list_chats", "Chats, most recent first: people (one chat per person across services), groups, notes. "+
		"query: part of the name; kind: person, group or conversation; include_archived: the chats the user "+
		"archived too; include_last_message: each with its last message.",
		object(nil, arg("query", "string", nil), arg("kind", "string", nil), arg("limit", "integer", 50),
			arg("offset", "integer", 0), arg("include_archived", "boolean", false), arg("include_last_message", "boolean", false)),
		true, false,
		func(ctx context.Context, in chatsIn) (any, error) {
			o := core.DefaultChatsOptions()
			o.Kind, o.Q, o.Limit, o.Offset, o.IncludeArchived = in.Kind, in.Query, capped(in.Limit, 50, 500), max(in.Offset, 0), in.IncludeArchived
			out := []core.M{}
			for _, c := range core.Chats(store, o) {
				m := core.M{"id": c["id"], "title": c["title"], "type": c["type"], "services": c["services"],
					"last": when(c["last_ts"]), "unread": c["unread"]}
				if in.IncludeArchived {
					m["archived"] = c["archived"]
				}
				if last, ok := c["last"].(core.M); ok && in.LastMessage {
					m["last_message"] = slim(last)
				}
				out = append(out, m)
			}
			return out, nil
		})

	type chatIn struct {
		Chat string `json:"chat"`
	}
	tool(srv, "get_chat", "A chat: its title, type, services, the person (a person's chat) or the members (a "+
		"group's), when it was last active and through which service.",
		object([]string{"chat"}, arg("chat", "string", nil)), false, false,
		func(ctx context.Context, in chatIn) (any, error) {
			c := core.GetChat(store, in.Chat)
			if c == nil {
				return core.M{"error": "no such chat"}, nil
			}
			out := core.M{"id": c["id"], "title": c["title"], "type": c["type"], "services": c["services"],
				"last": when(c["last_ts"]), "last_service": c["last_service"], "archived": c["archived"],
				"muted": c["muted"], "pinned": c["pinned"]}
			if c["type"] == "person" {
				out["person_id"] = c["person_id"]
			}
			if ms, ok := c["members"].([]core.M); ok {
				members := []core.M{}
				for _, m := range ms {
					members = append(members, core.M{"person_id": m["person_id"], "name": m["name"], "services": m["services"]})
				}
				out["members"] = members
			}
			return out, nil
		})

	type readIn struct {
		Chat       string `json:"chat"`
		Before     string `json:"before"`
		AroundDate string `json:"around_date"`
		Limit      int    `json:"limit"`
	}
	tool(srv, "read_chat", "A page of a chat, oldest first: messages and calls. Without before/around_date: the latest. "+
		"before: the `cursor` of the oldest item seen, for the page before it; around_date: YYYY-MM-DD.",
		object([]string{"chat"}, arg("chat", "string", nil), arg("before", "string", nil), arg("around_date", "string", nil),
			arg("limit", "integer", 50)), false, false,
		func(ctx context.Context, in readIn) (any, error) {
			o := core.StreamOptions{Before: in.Before, Limit: capped(in.Limit, 50, 200)}
			if in.AroundDate != "" {
				v, err := day(in.AroundDate, 0)
				if err != nil {
					return nil, err
				}
				o.Around = &v
			}
			page, err := core.Stream(store, in.Chat, o)
			if err != nil {
				return nil, err
			}
			items := page["items"].([]core.M)
			var older any
			if len(items) > 0 {
				older = items[0]["cursor"]
			}
			return core.M{"chat": in.Chat, "items": slimAll(items), "has_older": page["has_older"], "older_cursor": older}, nil
		})

	type contextIn struct {
		MessageID int64 `json:"message_id"`
		N         int   `json:"n"`
	}
	tool(srv, "message_context", "A message with the n messages before and after it in its chat.",
		object([]string{"message_id"}, arg("message_id", "integer", nil), arg("n", "integer", 10)), false, false,
		func(ctx context.Context, in contextIn) (any, error) {
			c, err := core.Context(store, in.MessageID, capped(in.N, 10, 50))
			if err != nil {
				return nil, err
			}
			if c == nil {
				return core.M{"error": "no such message"}, nil
			}
			return core.M{"chat": c["chat_id"], "items": slimAll(c["items"].([]core.M))}, nil
		})

	type peopleIn struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	tool(srv, "find_people", "People whose name or handle (number, username, email) contains `query`; "+
		"each person's chat is p<id>.",
		object([]string{"query"}, arg("query", "string", nil), arg("limit", "integer", 20)), true, false,
		func(ctx context.Context, in peopleIn) (any, error) {
			return core.PeopleList(store, core.PeopleListOptions{Q: in.Query, Limit: capped(in.Limit, 20, 100),
				Unnamed: true, Short: true})["items"], nil
		})

	type contactIn struct {
		Contact string `json:"contact"`
	}
	tool(srv, "get_direct_chat", "The one-to-one chat with someone, by a phone number (any format: matched on its "+
		"last 10 digits), a username, an email or a name: the people it matches, each with their chat, "+
		"handles, services and last contact; the best matches first.",
		object([]string{"contact"}, arg("contact", "string", nil)), true, false,
		func(ctx context.Context, in contactIn) (any, error) { return directChat(store, in.Contact), nil })

	type personIn struct {
		PersonID int64 `json:"person_id"`
	}
	tool(srv, "get_person", "A person: names, numbers and handles per service, message and call counts, first and last "+
		"contact, the groups they are in, the user's note.",
		object([]string{"person_id"}, arg("person_id", "integer", nil)), false, false,
		func(ctx context.Context, in personIn) (any, error) {
			p := core.Person(store, in.PersonID)
			if p == nil {
				return core.M{"error": "no such person"}, nil
			}
			st := p["stats"].(core.M)
			handles := []core.M{}
			for _, h := range p["handles"].([]core.M) {
				handles = append(handles, core.M{"kind": h["kind"], "value": h["label"], "service": h["service"]})
			}
			out := core.M{"id": p["id"], "name": p["name"], "chat": fmt.Sprintf("p%d", in.PersonID), "note": p["note"],
				"handles": handles, "messages": st["messages"], "messages_by_service": st["by_service"],
				"calls": st["calls"], "first": when(st["first"]), "last": when(st["last"]), "groups": p["groups"]}
			if plugins.Truthy(store.SettingAny("mcp_labels", false)) { // only where the user allows it
				labels := []core.M{}
				for _, lb := range core.PersonLabels(store, in.PersonID, true) {
					name := lb["name"]
					if !truthy(name) {
						name = lb["key"]
					}
					by := "user"
					if lb["state"] != "yes" {
						by = fmt.Sprintf("models (%s/%s)", count(lb["votes"]), count(lb["models"]))
					}
					labels = append(labels, core.M{"kind": lb["kind"], "label": name, "by": by})
				}
				out["labels"] = labels
			}
			return out, nil
		})

	type lastIn struct {
		PersonID int64 `json:"person_id"`
	}
	tool(srv, "last_interaction", "The latest message or call with a person in their chat, and the latest message they "+
		"wrote in a group.",
		object([]string{"person_id"}, arg("person_id", "integer", nil)), false, false,
		func(ctx context.Context, in lastIn) (any, error) { return lastInteraction(store, in.PersonID) })

	type messagesIn struct {
		Chat        string `json:"chat"`
		Sender      string `json:"sender"`
		Query       string `json:"query"`
		Service     string `json:"service"`
		Kind        string `json:"kind"`
		Since       string `json:"since"`
		Until       string `json:"until"`
		Limit       int    `json:"limit"`
		Offset      int    `json:"offset"`
		OldestFirst bool   `json:"oldest_first"`
	}
	tool(srv, "list_messages", "Messages by any of: chat (a chat id), sender (\"me\", or a person as p<id> or <id>: what "+
		"they wrote, in their chat and in groups), query (every word, anywhere, accents and case ignored), "+
		"service, kind (text, image, video, voice, file, location, ...), since/until (YYYY-MM-DD, both days "+
		"included); newest first unless oldest_first. At least one of them.",
		object(nil, arg("chat", "string", nil), arg("sender", "string", nil), arg("query", "string", nil),
			arg("service", "string", nil), arg("kind", "string", nil), arg("since", "string", nil), arg("until", "string", nil),
			arg("limit", "integer", 30), arg("offset", "integer", 0), arg("oldest_first", "boolean", false)), false, false,
		func(ctx context.Context, in messagesIn) (any, error) {
			since, until, err := span(in.Since, in.Until)
			if err != nil {
				return nil, err
			}
			return listMessages(store, listFilter{chat: in.Chat, sender: in.Sender, query: in.Query, service: in.Service,
				kind: in.Kind, since: since, until: until, limit: capped(in.Limit, 30, 100), offset: max(in.Offset, 0),
				oldestFirst: in.OldestFirst})
		})

	type callsIn struct {
		Chat       string `json:"chat"`
		MissedOnly bool   `json:"missed_only"`
		Limit      int    `json:"limit"`
	}
	tool(srv, "list_calls", "Calls of every service, newest first, optionally of one chat.",
		object(nil, arg("chat", "string", nil), arg("missed_only", "boolean", false), arg("limit", "integer", 50)), true, false,
		func(ctx context.Context, in callsIn) (any, error) {
			r, err := core.Calls(store, core.CallsOptions{ChatID: in.Chat, Missed: in.MissedOnly, Limit: capped(in.Limit, 50, 200),
				Unnamed: true, Short: true})
			if err != nil {
				return nil, err
			}
			out := []core.M{}
			for _, it := range r["items"].([]core.M) {
				m := slim(it)
				m["with"], m["chat"] = it["with"], it["chat_id"]
				out = append(out, m)
			}
			return out, nil
		})

	type dayIn struct {
		Date string `json:"date"`
	}
	tool(srv, "day_timeline", "Everything of one day (YYYY-MM-DD) across all chats, in order.",
		object([]string{"date"}, arg("date", "string", nil)), false, false,
		func(ctx context.Context, in dayIn) (any, error) {
			start, err := day(in.Date, 0)
			if err != nil {
				return nil, err
			}
			end, _ := day(in.Date, 1)
			r := core.Timeline(store, start, end)
			return core.M{"items": slimAll(r["items"].([]core.M)), "truncated": r["truncated"]}, nil
		})

	tool(srv, "statistics", "Counts: messages and calls by service and year, people, groups, the most written-to people.",
		object(nil), false, false,
		func(ctx context.Context, _ struct{}) (any, error) {
			s := core.M{}
			for k, v := range core.Stats(store, true) { // a copy: the core caches its own
				s[k] = v
			}
			s["first"], s["last"] = when(s["first"]), when(s["last"])
			return s, nil
		})

	type mediaIn struct {
		Chat  string `json:"chat"`
		Kind  string `json:"kind"`
		Since string `json:"since"`
		Until string `json:"until"`
		Limit int    `json:"limit"`
	}
	tool(srv, "find_media", "Files of one chat, newest first (\"the last two pictures X sent\"): kind image, video, voice, "+
		"file or all; since/until YYYY-MM-DD. Each says whether its file is still here and whether the "+
		"photo library already holds it.",
		object([]string{"chat"}, arg("chat", "string", nil), arg("kind", "string", "image"), arg("since", "string", nil),
			arg("until", "string", nil), arg("limit", "integer", 10)), true, false,
		func(ctx context.Context, in mediaIn) (any, error) {
			since, until, err := span(in.Since, in.Until)
			if err != nil {
				return nil, err
			}
			return findMedia(store, in.Chat, in.Kind, since, until, capped(in.Limit, 10, 100))
		})

	type downloadIn struct {
		MessageID int64  `json:"message_id"`
		SHA256    string `json:"sha256"`
	}
	tool(srv, "download_media", "The file of a message (message_id) or a file by its sha256 (as find_media and read_chat "+
		"give them): where it is on the computer that keeps the archive (path), its type and size; from "+
		"the archive's media, else the photo library holding it, else (where a source at work can) the service.",
		object(nil, arg("message_id", "integer", nil), arg("sha256", "string", nil)), false, false,
		func(ctx context.Context, in downloadIn) (any, error) {
			return download(ctx, env, in.MessageID, in.SHA256)
		})

	type libraryIn struct {
		SHA256 string `json:"sha256"`
		Date   string `json:"date"`
	}
	tool(srv, "send_media_to_library", "Store one file in the default photo library, unless it is already there (checked first). "+
		"Its date: the file's own EXIF date if it has one, else `date` (YYYY-MM-DD HH:MM) if given, else "+
		"the message's. Only with the user's explicit approval of this very file.",
		object([]string{"sha256"}, arg("sha256", "string", nil), arg("date", "string", nil)), false, true,
		func(ctx context.Context, in libraryIn) (any, error) {
			var whenMs *int64
			if in.Date != "" {
				t, err := parseMinute(in.Date)
				if err != nil {
					return nil, err
				}
				whenMs = &t
			}
			r, err := env.Library.ToLibrary(in.SHA256, whenMs)
			if err != nil {
				return core.M{"error": said(err)}, nil
			}
			if err := core.DecideMedia(store, in.SHA256, "library", whenMs); err != nil {
				return nil, err
			}
			return r, nil
		})

	type noteIn struct {
		PersonID int64  `json:"person_id"`
		Note     string `json:"note"`
	}
	tool(srv, "set_person_note", "Write the user's note about a person (replaces it). Ask the user before using.",
		object([]string{"person_id", "note"}, arg("person_id", "integer", nil), arg("note", "string", nil)), false, true,
		func(ctx context.Context, in noteIn) (any, error) {
			if err := core.SetPerson(store, in.PersonID, core.Opt[string]{}, core.To(in.Note), core.Opt[string]{}); err != nil {
				return nil, noPerson(err)
			}
			return core.M{"ok": true}, nil
		})

	type renameIn struct {
		PersonID int64  `json:"person_id"`
		Name     string `json:"name"`
	}
	tool(srv, "rename_person", "Give a person the name the user wants shown (empty: back to the contact's or the service's). "+
		"Ask the user before using.",
		object([]string{"person_id", "name"}, arg("person_id", "integer", nil), arg("name", "string", nil)), false, true,
		func(ctx context.Context, in renameIn) (any, error) {
			if err := core.SetPerson(store, in.PersonID, core.To(in.Name), core.Opt[string]{}, core.Opt[string]{}); err != nil {
				return nil, noPerson(err)
			}
			return core.M{"ok": true, "name": core.Person(store, in.PersonID)["name"]}, nil
		})
}

func noPerson(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return errors.New("no such person")
	}
	return err
}

func count(v any) string {
	if v == nil {
		return "?"
	}
	return fmt.Sprint(v)
}

// parseMinute is "YYYY-MM-DD HH:MM" in the user's time zone, as Unix ms.
func parseMinute(s string) (int64, error) {
	t, err := timeIn("2006-01-02 15:04", s)
	if err != nil {
		return 0, fmt.Errorf("a date is YYYY-MM-DD HH:MM, not %q", s)
	}
	return t, nil
}

// personArg is a person given as p<id> or <id>.
func personArg(s string) (int64, bool) {
	if len(s) > 1 && s[0] == 'p' {
		s = s[1:]
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0
}

// knownServices is the archive's services as "Name (id)": named as their plugins declare where
// one is here, else by id.
func knownServices(s *core.Store) []string {
	info := plugins.Services("en")
	out := []string{}
	for _, id := range db.Strs(s.Read(), "SELECT name FROM service ORDER BY id") {
		name := id
		if m, ok := info[id]; ok {
			if n, _ := m["name"].(string); n != "" {
				name = n
			}
		}
		out = append(out, fmt.Sprintf("%s (%s)", name, id))
	}
	return out
}
