package whatsapp

// Ports WhatsappBridge.run_import of everysaid/plugins/sources.py: the importers of the store
// folder's databases only (an iPhone's are another instance's): whatsapp.run(a, iphone_db=None,
// contacts_db=None, bridge_db, store_db), voip.bridge_calls(a, voip.Calls(a), bridge, store) and
// media.run(a, [whatsapp_bridge(bridge)]).

import (
	"everysaid/internal/archive"
	"everysaid/internal/importers"
	"everysaid/internal/plugins"
	"everysaid/internal/plugins/sourcekit"
)

// runImport brings the messages, the calls and the files the bridge downloaded.
func runImport(c *plugins.Context) error {
	bridge, store := paths(c)
	changed := false
	_, _, err := sourcekit.RunImporters(c, []sourcekit.Step{
		{Label: "WhatsApp (bridge)", Run: func(a *archive.Archive, out func(string)) error {
			updated, err := importers.WhatsApp(a, out, importers.WhatsAppOptions{NoIphone: true, NoContacts: true,
				BridgeDB: bridge, StoreDB: store})
			changed = changed || len(updated) > 0
			return err
		}},
		{Label: "WhatsApp calls (bridge)", Run: func(a *archive.Archive, out func(string)) error {
			return importers.BridgeCalls(a, importers.NewCallSet(a), bridge, store)
		}},
		{Label: "files", Run: func(a *archive.Archive, out func(string)) error {
			return importers.Media(a, out, importers.MediaWhatsAppBridge(bridge))
		}},
	})
	if err != nil {
		return err
	}
	if changed { // edits, deletions, reactions on messages already shown
		c.Emit(M{"type": "changed"})
	}
	return nil
}
