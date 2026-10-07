package sources

// Ports AndroidAdb, ViberDesktop, CarrierNotices and ImLogs of everysaid/plugins/sources.py.

import (
	"strings"

	"everysaid/internal/android"
	"everysaid/internal/archive"
	"everysaid/internal/config"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

// AndroidAdb is the `android-adb` source. Where the Python ran scripts/android-export.py, the
// export runs here, in the process (internal/android).
type AndroidAdb struct{}

var androidServices = []string{"sms", "mms", "phone"}

func (AndroidAdb) Info() *plugins.Info {
	return &plugins.Info{
		ID: "android-adb", Name: "Android (adb)", Kind: "source",
		Services:    androidServices,
		ServiceInfo: sourcekit.Looks(androidServices...),
		Description: "SMS, MMS, calls and blocked numbers of an Android phone, read over adb (USB debugging on).",
		Needs:       []string{"the phone on a USB cable", "adb", "USB debugging enabled"},
		Settings:    []plugins.Setting{{Key: "serial", Label: "Serial", Help: "Only when adb sees more than one phone"}},
	}
}

func (AndroidAdb) RunImport(c *plugins.Context) error {
	if err := android.Export(android.ExportOptions{Serial: c.Str("serial"), Say: sourcekit.Say(c)}); err != nil {
		return sourcekit.UserError(err)
	}
	_, _, err := sourcekit.RunImporters(c, []sourcekit.Step{
		{Label: "SMS, MMS", Run: func(a *archive.Archive, out func(string)) error {
			return importers.SMS(a, out, importers.SMSOptions{})
		}},
		{Label: "calls", Run: func(a *archive.Archive, out func(string)) error {
			return importers.Calls(a, out, importers.CallsOptions{})
		}},
		{Label: "files", Run: func(a *archive.Archive, out func(string)) error {
			return importers.Media(a, out, importers.Phones...)
		}},
	})
	return err
}

// ViberDesktop is the `viber-desktop` source.
type ViberDesktop struct{}

func (ViberDesktop) Info() *plugins.Info {
	return &plugins.Info{
		ID: "viber-desktop", Name: "Viber Desktop export", Kind: "source",
		Services:    []string{"viber"},
		ServiceInfo: sourcekit.Looks("viber"),
		Description: "The history Viber Desktop holds (synced from the phone it is linked to), decrypted with " +
			"scripts/viber-desktop-export.cpp. Linux only: there is no official way.",
		Platforms: []string{"linux"},
		Needs:     []string{"Viber Desktop", "a decrypted export (viber-desktop-export.cpp)"},
		Settings: []plugins.Setting{{Key: "export", Label: "Decrypted database", Type: "path", Required: true,
			Default: nilIfEmpty(config.ViberDesktop)}},
	}
}

// nilIfEmpty: a setting whose default config.toml does not give has none (Python's None).
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (ViberDesktop) RunImport(c *plugins.Context) error {
	path := config.ExpandUser(c.Str("export"))
	_, _, err := sourcekit.RunImporters(c, []sourcekit.Step{
		{Label: "Viber Desktop", Run: func(a *archive.Archive, out func(string)) error {
			return importers.Viber(a, out, importers.ViberOptions{DesktopDB: path})
		}},
	})
	return err
}

// CarrierNotices is the `carrier-notices` source.
type CarrierNotices struct{}

func (CarrierNotices) Info() *plugins.Info {
	return &plugins.Info{
		ID: "carrier-notices", Name: "Carrier missed-call notices", Kind: "source",
		Services:    []string{"phone"},
		ServiceInfo: sourcekit.Looks("phone"),
		Description: "Calls known only from the carrier's SMS notices (\"you have a missed call from ...\"), " +
			"read from the archive's own SMS, by a parser per carrier or country.",
		Settings: []plugins.Setting{{Key: "carriers", Label: "Carriers", Type: "text",
			Default: strings.Join(config.Strings("import", "carrier_notices"), ","), Help: "e.g. gr"}},
	}
}

func (CarrierNotices) RunImport(c *plugins.Context) error {
	var carriers []string
	for _, s := range strings.Split(c.Str("carriers"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			carriers = append(carriers, s)
		}
	}
	// none given: config.toml's (VoIPOptions.Carriers nil). The Python set the importer's module
	// variable, which then stayed for every later import of the process; here it is this run's.
	_, _, err := sourcekit.RunImporters(c, []sourcekit.Step{
		{Label: "app and carrier calls", Run: func(a *archive.Archive, out func(string)) error {
			return importers.VoIP(a, out, importers.VoIPOptions{Carriers: carriers})
		}},
	})
	return err
}

// ImLogs is the `im-logs` source.
type ImLogs struct{}

var imServices = []string{"msn", "icq", "aim", "yahoo", "jabber", "skype", "irc", "messenger", "whatsapp"}

func (ImLogs) Info() *plugins.Info {
	return &plugins.Info{
		ID: "im-logs", Name: "Adium and Pidgin logs", Kind: "source",
		Services:    imServices,
		ServiceInfo: sourcekit.Looks(imServices...),
		NameWeights: []plugins.Weight{{Key: "msn/chat", Weight: 20}, {Key: "icq/chat", Weight: 20},
			{Key: "aim/chat", Weight: 20}, {Key: "yahoo/chat", Weight: 20}, {Key: "jabber/chat", Weight: 20},
			{Key: "skype/chat", Weight: 20}, {Key: "messenger/chat", Weight: 20}, {Key: "msn/book", Weight: 70},
			{Key: "icq/book", Weight: 70}, {Key: "aim/book", Weight: 70}, {Key: "yahoo/book", Weight: 70},
			{Key: "jabber/book", Weight: 70}, {Key: "skype/book", Weight: 70}},
		Description: "The logs of the old multi-protocol messengers, Adium (macOS) and Pidgin or Gaim: MSN, ICQ, AIM, " +
			"Yahoo, Jabber and Google Talk, Skype, IRC, Facebook chat. Read from their folders as they are.",
		Needs: []string{"Adium's folder (Adium 2.0, Users/Default or Logs), or Pidgin's .purple folder, unpacked"},
		Settings: []plugins.Setting{
			{Key: "adium", Label: "Adium folder", Type: "path", Default: config.Get("imlogs", "adium"),
				Help: "Adium 2.0, its Users/Default, or its Logs folder"},
			{Key: "pidgin", Label: "Pidgin folder", Type: "path", Default: config.Get("imlogs", "pidgin"),
				Help: "The .purple folder (with blist.xml and accounts.xml), or its logs folder"},
		},
	}
}

func (ImLogs) Check(c *plugins.Context) (bool, string) {
	if c.Str("adium") == "" && c.Str("pidgin") == "" {
		return false, "missing: a folder of Adium or of Pidgin"
	}
	return true, "ready"
}

func (ImLogs) RunImport(c *plugins.Context) error {
	adium, pidgin := c.Str("adium"), c.Str("pidgin")
	_, _, err := sourcekit.RunImporters(c, []sourcekit.Step{
		{Label: "Adium and Pidgin logs", Run: func(a *archive.Archive, out func(string)) error {
			_, err := importers.Imlogs(a, out, importers.ImlogsOptions{Adium: adium, Pidgin: pidgin})
			return err
		}},
	})
	return err
}
