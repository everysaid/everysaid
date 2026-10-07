package sources

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/core"
	"everysaid/internal/db"
	"everysaid/internal/errs"
	"everysaid/internal/i18n"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "everysaid-sources-test")
	for k, v := range map[string]string{"EVERYSAID_DATA": "data", "EVERYSAID_CACHE": "cache",
		"EVERYSAID_CONFIG": "config", "EVERYSAID_STATE": "state"} {
		os.Setenv(k, filepath.Join(dir, v))
	}
	os.Setenv("EVERYSAID_KEYRING", "everysaid-test-sources")
	os.Setenv("PATH", "") // no adb, no idevicebackup2: nothing reaches a phone
	config.Load()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type host struct {
	store  *core.Store
	mu     sync.Mutex
	events []M
}

func (h *host) Store() *core.Store { return h.store }
func (h *host) Emit(e M)           { h.mu.Lock(); h.events = append(h.events, e); h.mu.Unlock() }
func (h *host) Alert(_, _ string)  {}

func instance(t *testing.T, plugin string, settings M) (*host, *plugins.Context) {
	path := filepath.Join(t.TempDir(), "archive.db")
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	s, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	iid, err := plugins.Create(s, plugin, "Source", settings)
	if err != nil {
		t.Fatal(err)
	}
	h := &host{store: s}
	return h, plugins.NewContext(h, *plugins.GetInstance(s, iid))
}

func TestManifests(t *testing.T) {
	m := plugins.Manifest(plugins.Get("iphone-backup"), "el")
	if m["name"] != "iPhone (κρυπτογραφημένο backup)" || m["kind"] != "source" {
		t.Fatal(m["name"], m["kind"])
	}
	settings := m["settings"].([]M)
	if settings[3]["key"] != "password" || settings[3]["keeps"].(M)["keyring"].(M)["key"] != "backup_password" {
		t.Fatal(settings[3])
	}
	if !(plugins.Setting{Pattern: plugins.Get("iphone-backup").Info().Settings[2].Pattern}).Valid("00008030-001A2B3C4D5E6F70") {
		t.Fatal("a UDID")
	}
	services := plugins.Services("en")
	if services["phone"]["messages"] != false || services["phone"]["short"] != "☎" || services["sms"]["messages"] != true ||
		services["msn"]["icon"] != sourcekit.Icons["msn"] || services["viber"]["color"] != "#7360f2" {
		t.Fatal(services["phone"], services["sms"])
	}
	w := map[string]int{}
	for _, x := range plugins.NameWeights() {
		w[x.Key] = x.Weight
	}
	if w["whatsapp/book"] != 80 || w["msn/book"] != 70 || w["skype/chat"] != 20 {
		t.Fatal(w)
	}
	if plugins.StateWeight("iphone-backup", "muted") != 60 {
		t.Fatal("muted")
	}
	for _, id := range []string{"android-adb", "carrier-notices", "im-logs"} {
		if plugins.Get(id) == nil {
			t.Fatal(id)
		}
	}
	// every word the user reads of these plugins has its Greek
	for _, id := range []string{"iphone-backup", "android-adb", "carrier-notices", "im-logs"} {
		info := plugins.Get(id).Info()
		words := append([]string{info.Name, info.Description}, info.Needs...)
		for _, s := range info.Settings {
			words = append(words, s.Label, s.Help)
			for _, o := range s.Options {
				words = append(words, o.Label)
			}
		}
		for _, w := range words {
			if w != "" && i18n.Tr(w, "el") == w && !sameInGreek[w] {
				t.Errorf("%s: no Greek for %q", id, w)
			}
		}
	}
}

// sameInGreek: words that are the same in Greek.
var sameInGreek = map[string]bool{"UDID": true, "adb": true, "Viber Desktop": true, "Android (adb)": true,
	"libimobiledevice (idevicebackup2)": true}

func TestIphoneBackup(t *testing.T) {
	root := t.TempDir()
	_, c := instance(t, "iphone-backup", M{"backup_root": root})
	p := IphoneBackup{}
	if ok, _ := plugins.Check(p, c); !ok {
		t.Fatal("asking needs nothing kept")
	}
	if asks := p.Asks(c); len(asks) != 1 || asks[0].Key != "backup_password" {
		t.Fatal(asks)
	}
	facts := p.InfoFacts(c)
	if facts[0].Value != root || facts[1].Value != "none yet" {
		t.Fatal(facts)
	}
	// a single phone's backup is found
	dir := filepath.Join(root, "00008030-001A2B3C4D5E6F70")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "Manifest.plist"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(dir, "Manifest.db"), make([]byte, 2_500_000), 0o600)
	stamp := time.Date(2026, 10, 5, 21, 30, 0, 0, time.Local)
	os.Chtimes(filepath.Join(dir, "Manifest.db"), stamp, stamp)
	facts = p.InfoFacts(c)
	if facts[0].Value != dir || facts[1].Value != "2026-10-05 21:30" || facts[2].Value != "3 MB" {
		t.Fatal(facts)
	}
	// no password: said before anything is tried
	var ue *errs.UserError
	if err := p.RunImport(c); !errors.As(err, &ue) || ue.Text != "The backup password is needed" {
		t.Fatal(err)
	}
	// kept in the keyring: ready only with it, and not asked
	_, c = instance(t, "iphone-backup", M{"backup_root": root, "password": "keyring"})
	if ok, why := plugins.Check(p, c); ok || why != "missing: The backup password" {
		t.Fatal(why)
	}
	if len(p.Asks(c)) != 0 {
		t.Fatal("asked")
	}
	// with a password and no backup tool: the backup fails, said as the extraction says it
	c.Given["backup_password"] = "secret"
	if err := p.RunImport(c); err == nil {
		t.Fatal("no backup tool, yet no failure")
	}
}

func TestRunImportersTiesTheSourcesToTheInstance(t *testing.T) {
	h, c := instance(t, "carrier-notices", nil)
	var said []string
	_, _, err := sourcekit.RunImporters(c, []sourcekit.Step{{Label: "calls", Run: func(a *archive.Archive, out func(string)) error {
		src := a.Source("test", "/x", "", "")
		a.Imported(src)
		conv := a.Conversation("sms", []archive.Handle{archive.H("phone", "+306940000001")}, "", "")
		a.AddMessage(src, "1", archive.Message{Service: "sms", ConversationID: conv, TS: 1, Kind: "text", Text: "hi",
			SenderID: a.Address(archive.H("phone", "+306940000001"))})
		out("line one\nline two")
		said = append(said, "ran")
		return nil
	}}})
	if err != nil || len(said) != 1 {
		t.Fatal(err)
	}
	if n := db.Int(h.store.Read(), "SELECT count(*) FROM source WHERE instance_id = ?", c.ID); n != 1 {
		t.Fatal(n)
	}
	lines := strings.Join(c.LastLines(10), "|")
	if lines != "== calls|line one|line two|new messages: 1, new calls: 0" {
		t.Fatal(lines)
	}
	if e := h.events[len(h.events)-2]; e["type"] != "new" || e["messages"].([]int64)[1] != 1 {
		t.Fatal(h.events)
	}
	// a step that fails stops the run
	_, _, err = sourcekit.RunImporters(c, []sourcekit.Step{{Label: "calls", Run: func(a *archive.Archive, out func(string)) error {
		return errors.New("broken")
	}}})
	if err == nil || err.Error() != "broken" {
		t.Fatal(err)
	}
}

func TestTheOthers(t *testing.T) {
	_, c := instance(t, "im-logs", nil)
	if ok, why := plugins.Check(ImLogs{}, c); ok || why != "missing: a folder of Adium or of Pidgin" {
		t.Fatal(why)
	}
	// a folder with nothing of either: nothing, and no failure
	_, c = instance(t, "im-logs", M{"pidgin": t.TempDir()})
	if err := (ImLogs{}).RunImport(c); err != nil {
		t.Fatal(err)
	}
	_, c = instance(t, "android-adb", nil)
	var ue *errs.UserError
	if err := (AndroidAdb{}).RunImport(c); !errors.As(err, &ue) || !strings.Contains(ue.Text, "not found in PATH") {
		t.Fatal(err)
	}
	// carrier notices on an archive without SMS: nothing, and no failure
	_, c = instance(t, "carrier-notices", M{"carriers": " gr , "})
	if err := (CarrierNotices{}).RunImport(c); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.Join(c.LastLines(1), ""), "new messages: 0, new calls: 0") {
		t.Fatal(c.LastLines(5))
	}
	_, c = instance(t, "carrier-notices", M{"carriers": "xx"})
	if err := (CarrierNotices{}).RunImport(c); err == nil {
		t.Fatal("an unknown carrier")
	}
}
