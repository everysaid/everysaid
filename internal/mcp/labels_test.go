package mcp

import (
	"reflect"
	"testing"

	"everysaid/internal/core"
	"everysaid/internal/db"
)

// A person's labels reach the assistant only when the user allows it (the setting mcp_labels).
func TestTheAssistantSeesLabelsOnlyWhenAllowed(t *testing.T) {
	f := build(t)
	var friend int64
	for _, x := range core.Labels(f.store, "") {
		if x["key"] == "friend" {
			friend = x["id"].(int64)
		}
	}
	if friend == 0 {
		t.Fatal("no label friend")
	}
	person := db.Int(f.store.Read(), "SELECT min(id) FROM person WHERE name IS NOT NULL")
	if err := core.SetPersonLabel(f.store, person, friend, "yes"); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, Env{Store: f.store})
	if _, ok := obj(call(t, cs, "get_person", map[string]any{"person_id": person}))["labels"]; ok {
		t.Fatal("labels without the user's leave")
	}
	if err := core.SetSetting(f.store, "mcp_labels", true); err != nil {
		t.Fatal(err)
	}
	got := obj(call(t, cs, "get_person", map[string]any{"person_id": person}))["labels"]
	want := []any{map[string]any{"kind": "relation", "label": "friend", "by": "user"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("labels %v", got)
	}
}
