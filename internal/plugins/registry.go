package plugins

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/i18n"
)

var (
	regMu    sync.RWMutex
	registry = map[string]Plugin{}
	order    []string
)

// Register adds a plugin to those Everysaid knows (from the plugins' own packages, at init).
func Register(p Plugin) {
	regMu.Lock()
	defer regMu.Unlock()
	id := p.Info().ID
	if _, ok := registry[id]; !ok {
		order = append(order, id)
	}
	registry[id] = p
}

// Get is a plugin by id, or nil.
func Get(id string) Plugin {
	regMu.RLock()
	defer regMu.RUnlock()
	return registry[id]
}

// Order is the order plugins are listed in (the sources, the libraries, the address books, the
// analysis); one not in it comes after them, in the order it was registered. Between equal name
// weights, this order decides.
var Order = []string{"iphone-backup", "android-adb", "viber-desktop", "whatsapp-bridge", "telegram", "signal",
	"carrier-notices", "im-logs", "folder", "immich", "carddav", "vcard-file", "ollama"}

// All is every plugin, in their Order.
func All() []Plugin {
	regMu.RLock()
	defer regMu.RUnlock()
	rank := func(id string) int {
		for i, x := range Order {
			if x == id {
				return i
			}
		}
		return len(Order)
	}
	ids := append([]string(nil), order...)
	sort.SliceStable(ids, func(i, j int) bool { return rank(ids[i]) < rank(ids[j]) })
	out := make([]Plugin, 0, len(ids))
	for _, id := range ids {
		out = append(out, registry[id])
	}
	return out
}

func Catalog(lang string) []M {
	out := []M{}
	for _, p := range All() {
		out = append(out, Manifest(p, lang))
	}
	return out
}

// Services is how each service looks, as the plugins that bring it declare: {service: {name,
// color, short, icon, messages}}.
func Services(lang string) map[string]M {
	out := map[string]M{}
	for _, p := range All() {
		for sid, info := range p.Info().ServiceInfo {
			if _, ok := out[sid]; ok {
				continue
			}
			messages := true
			if info.Messages != nil {
				messages = *info.Messages
			}
			m := M{"messages": messages, "name": i18n.Tr(info.Name, lang)}
			if info.Color != "" {
				m["color"] = info.Color
			}
			if info.Short != "" {
				m["short"] = info.Short
			}
			if info.Icon != "" {
				m["icon"] = info.Icon
			}
			out[sid] = m
		}
	}
	return out
}

// NameWeights is the sources of names with their weight: "contacts" (address books) or a
// service's, the highest any plugin declares, in the order first declared; the core orders names
// by these unless the user set an order.
func NameWeights() []Weight {
	var out []Weight
	at := map[string]int{}
	for _, p := range All() {
		for _, w := range p.Info().NameWeights {
			if i, ok := at[w.Key]; ok {
				if w.Weight > out[i].Weight {
					out[i].Weight = w.Weight
				}
				continue
			}
			at[w.Key] = len(out)
			out = append(out, w)
		}
	}
	return out
}

func weightMap() map[string]int {
	m := map[string]int{}
	for _, w := range NameWeights() {
		m[w.Key] = w.Weight
	}
	return m
}

var nameKinds = map[string]string{"book": "address book copy", "chat": "chat name", "profile": "chosen by them"}

// NameLabel is 'contacts' or '<service>/<kind>' in words.
func NameLabel(source, lang string) string {
	if source == "contacts" {
		return i18n.Tr("Address book", lang)
	}
	service, kind, _ := strings.Cut(source, "/")
	name := service
	if s, ok := Services(lang)[service]; ok {
		if n, ok := s["name"].(string); ok {
			name = n
		}
	}
	if k, ok := nameKinds[kind]; ok {
		return name + " (" + i18n.Tr(k, lang) + ")"
	}
	return name
}

// NameSources are the sources of names in the default order, for the UI: [{id, label, weight}].
func NameSources(lang string) []M {
	w := weightMap()
	var keys []string
	for _, x := range NameWeights() {
		keys = append(keys, x.Key)
	}
	sort.SliceStable(keys, func(i, j int) bool { return w[keys[i]] > w[keys[j]] })
	out := []M{}
	for _, k := range keys {
		out = append(out, M{"id": k, "weight": w[k], "label": NameLabel(k, lang)})
	}
	return out
}

// StateWeight is how much a plugin's report of a chat's state counts (0: not applied).
func StateWeight(pluginID, field string) int {
	if p := Get(pluginID); p != nil {
		return p.Info().StateWeights[field]
	}
	return 0
}

func init() {
	core.StateWeight = StateWeight
	core.NameWeights = func() []core.Weight {
		var out []core.Weight
		for _, w := range NameWeights() {
			out = append(out, core.Weight{Key: w.Key, Weight: w.Weight})
		}
		return out
	}
	core.NameLabel = NameLabel
}

// Instance is a plugin_instance row.
type Instance struct {
	ID         int64
	Plugin     string
	Kind       string
	Label      string
	Settings   string
	State      string
	Enabled    bool
	DeviceID   *int64
	IsDefault  bool
	CreatedAt  int64
	LastRun    *int64
	LastStatus *string
}

const instanceCols = "id, plugin, kind, label, settings, state, enabled, device_id, is_default, created_at, last_run, last_status"

func scanInstance(scan func(...any)) Instance {
	var r Instance
	var dev, last sql.NullInt64
	var status sql.NullString
	scan(&r.ID, &r.Plugin, &r.Kind, &r.Label, &r.Settings, &r.State, &r.Enabled, &dev, &r.IsDefault, &r.CreatedAt, &last, &status)
	if dev.Valid {
		r.DeviceID = &dev.Int64
	}
	if last.Valid {
		r.LastRun = &last.Int64
	}
	if status.Valid {
		r.LastStatus = &status.String
	}
	return r
}

// Instances are the instances in this archive (of a kind, or all).
func Instances(s *core.Store, kind string) []Instance {
	q, args := "SELECT "+instanceCols+" FROM plugin_instance", []any{}
	if kind != "" {
		q += " WHERE kind = ?"
		args = append(args, kind)
	}
	var out []Instance
	db.Each(s.Read(), q+" ORDER BY kind, id", args, func(scan func(...any)) { out = append(out, scanInstance(scan)) })
	return out
}

// GetInstance is an instance by id, or nil.
func GetInstance(s *core.Store, iid int64) *Instance {
	var out *Instance
	db.Each(s.Read(), "SELECT "+instanceCols+" FROM plugin_instance WHERE id = ?", []any{iid}, func(scan func(...any)) {
		r := scanInstance(scan)
		out = &r
	})
	return out
}

// Public is an instance as the UI sees it: settings without secrets, with its plugin's name.
func Public(r Instance, lang string) M {
	p := Get(r.Plugin)
	secret := map[string]bool{}
	settings := M{}
	if p != nil {
		for _, s := range p.Info().Settings {
			if s.typ() == "secret" {
				secret[s.Key] = true
			} else if s.Default != nil {
				settings[s.Key] = s.Default
			}
		}
	}
	var set M
	json.Unmarshal([]byte(r.Settings), &set)
	for k, v := range set {
		settings[k] = v
	}
	for k := range secret {
		delete(settings, k)
	}
	name := r.Plugin
	if p != nil {
		name = i18n.Tr(p.Info().Name, lang)
	}
	return M{"id": r.ID, "plugin": r.Plugin, "kind": r.Kind, "label": r.Label, "settings": settings,
		"enabled": r.Enabled, "is_default": r.IsDefault, "last_run": r.LastRun, "last_status": r.LastStatus,
		"known": p != nil, "name": name}
}

// Create adds an instance of a plugin; it returns its id.
func Create(s *core.Store, pluginID, label string, settings M) (int64, error) {
	p := Get(pluginID)
	if p == nil {
		return 0, &UnknownPlugin{pluginID}
	}
	all := M{}
	for _, st := range p.Info().Settings {
		if st.Default != nil && st.typ() != "secret" {
			all[st.Key] = st.Default
		}
	}
	for k, v := range settings {
		all[k] = v
	}
	b, _ := json.Marshal(all)
	var id int64
	err := s.Write(func(tx *sql.Tx) error {
		first := p.Info().Kind == "library" && !db.Exists(tx, "SELECT 1 FROM plugin_instance WHERE kind = 'library'")
		id = db.LastID(tx, "INSERT INTO plugin_instance (plugin, kind, label, settings, is_default, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			pluginID, p.Info().Kind, label, string(b), db.B(first), time.Now().Unix())
		return nil
	})
	return id, err
}

// UnknownPlugin: no plugin of that id.
type UnknownPlugin struct{ ID string }

func (e *UnknownPlugin) Error() string { return "unknown plugin " + e.ID }

// Update changes an instance: its label, settings (merged), enabled, whether it is the default of
// its kind. nil: left as it is.
func Update(s *core.Store, iid int64, label *string, settings M, enabled *bool, isDefault bool) error {
	return s.Write(func(tx *sql.Tx) error {
		var old, kind string
		if !db.Row(tx, "SELECT settings, kind FROM plugin_instance WHERE id = ?", []any{iid}, &old, &kind) {
			return &NoInstance{iid}
		}
		if label != nil {
			db.Exec(tx, "UPDATE plugin_instance SET label = ? WHERE id = ?", *label, iid)
		}
		if settings != nil {
			merged := M{}
			json.Unmarshal([]byte(old), &merged)
			if merged == nil {
				merged = M{}
			}
			for k, v := range settings {
				merged[k] = v
			}
			b, _ := json.Marshal(merged)
			db.Exec(tx, "UPDATE plugin_instance SET settings = ? WHERE id = ?", string(b), iid)
		}
		if enabled != nil {
			db.Exec(tx, "UPDATE plugin_instance SET enabled = ? WHERE id = ?", db.B(*enabled), iid)
		}
		if isDefault {
			db.Exec(tx, "UPDATE plugin_instance SET is_default = (id = ?) WHERE kind = ?", iid, kind)
		}
		return nil
	})
}

// NoInstance: no instance of that id.
type NoInstance struct{ ID int64 }

func (e *NoInstance) Error() string { return "no plugin instance" }

// Remove: an instance goes; what it brought stays in the archive (its sources lose their instance).
func Remove(s *core.Store, iid int64) error {
	return s.Write(func(tx *sql.Tx) error {
		db.Exec(tx, "UPDATE source SET instance_id = NULL WHERE instance_id = ?", iid)
		db.Exec(tx, "UPDATE library_link SET instance_id = NULL WHERE instance_id = ?", iid)
		for _, cid := range db.Ints(tx, "SELECT id FROM contact WHERE instance_id = ?", iid) {
			db.Exec(tx, "DELETE FROM contact_address WHERE contact_id = ?", cid)
		}
		db.Exec(tx, "DELETE FROM contact WHERE instance_id = ?", iid)
		db.Exec(tx, "DELETE FROM plugin_instance WHERE id = ?", iid)
		return nil
	})
}
