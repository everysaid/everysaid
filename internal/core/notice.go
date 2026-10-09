package core

import (
	"database/sql"
	"encoding/json"
)

// What a notice says (the archive's `notice` table): its code and values, a person in them given
// their name and person as the interface shows them. Whatever source wrote it, the interface and
// the MCP read it the same way; the codes are in docs/design.md.

// noticeOf is a notice for the interface: {"code", "args"}, each person in args ({"address": id})
// given "name", "person_id" and "me".
func noticeOf(ppl *People, code string, args sql.NullString) M {
	var v any = M{}
	if args.Valid {
		var m map[string]any // only an object: anything else (null) is no values
		if json.Unmarshal([]byte(args.String), &m) == nil && m != nil {
			v = m
		}
	}
	return M{"code": code, "args": namePeople(ppl, v)}
}

func namePeople(ppl *People, v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := M{}
		for k, e := range x {
			out[k] = namePeople(ppl, e)
		}
		if id, ok := x["address"].(float64); ok {
			aid := int64(id)
			out["address"] = aid
			out["name"] = ppl.NameOfAddress(aid)
			out["me"] = ppl.OwnAddresses[aid]
			if pid, ok := ppl.PersonOf[aid]; ok {
				out["person_id"] = pid
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = namePeople(ppl, e)
		}
		return out
	}
	return v
}
