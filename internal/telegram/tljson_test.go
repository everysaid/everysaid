package telegram

import (
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/gotd/td/tdp"
	"github.com/gotd/td/tg"
)

// testdata/telethon-types.json is what Telethon 1.45 (layer 229) says of every type: its class
// name, the keys of its to_dict() in order, and which of them are lists. Made from Telethon's own
// classes (each built with every argument None), with no data of anyone's.
type telethonType struct {
	Name    string   `json:"name"`
	Keys    []string `json:"keys"`
	Vectors []string `json:"vectors"`
}

func TestTypesAsTelethon(t *testing.T) {
	raw, err := os.ReadFile("testdata/telethon-types.json")
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		Layer int                     `json:"layer"`
		Types map[string]telethonType `json:"types"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.Layer != tg.Layer {
		t.Logf("Telethon's layer %d, gotd's %d", ref.Layer, tg.Layer)
	}
	checked, missing := 0, 0
	for id, make := range tg.TypesConstructorMap() {
		obj := make()
		if _, ok := obj.(tdp.Object); !ok {
			continue
		}
		want, ok := ref.Types[strconv.FormatUint(uint64(id), 10)]
		if !ok {
			missing++
			continue
		}
		checked++
		m := meta(reflect.TypeOf(obj))
		if m.name != want.Name {
			t.Errorf("%08x: name %s, Telethon %s", id, m.name, want.Name)
		}
		var keys, vectors []string
		for _, f := range m.fields {
			keys = append(keys, f.key)
			ft := reflect.TypeOf(obj).Elem().FieldByIndex(f.index).Type
			if ft.Kind() == reflect.Slice && !isBytes(ft) {
				vectors = append(vectors, f.key)
			}
		}
		if !reflect.DeepEqual(keys, want.Keys) && !(len(keys) == 0 && len(want.Keys) == 0) {
			t.Errorf("%s: keys %v, Telethon %v", want.Name, keys, want.Keys)
		}
		if !reflect.DeepEqual(vectors, want.Vectors) && !(len(vectors) == 0 && len(want.Vectors) == 0) {
			t.Errorf("%s: lists %v, Telethon %v", want.Name, vectors, want.Vectors)
		}
	}
	t.Logf("%d types checked, %d of gotd's not in Telethon", checked, missing)
}
