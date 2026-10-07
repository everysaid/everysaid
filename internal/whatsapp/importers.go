package whatsapp

// The importers this plugin runs, of the store folder's databases only (an iPhone's are another
// instance's): whatsapp.run(a, iphone_db=None, contacts_db=None, bridge_db, store_db),
// voip.bridge_calls(a, voip.Calls(a), bridge, store) and media.run(a, [whatsapp_bridge(bridge)]).

import (
	"everysaid/internal/archive"
	"everysaid/internal/importers"
)

// importWhatsApp imports the bridge's messages; true when messages already there changed (edits,
// deletions, reactions).
func importWhatsApp(a *archive.Archive, out func(string), bridge, store string) (bool, error) {
	updated, err := importers.WhatsApp(a, out, importers.WhatsAppOptions{NoIphone: true, NoContacts: true,
		BridgeDB: bridge, StoreDB: store})
	return len(updated) > 0, err
}

func importCalls(a *archive.Archive, out func(string), bridge, store string) error {
	return importers.BridgeCalls(a, importers.NewCallSet(a), bridge, store)
}

func importMedia(a *archive.Archive, out func(string), bridge string) error {
	return importers.Media(a, out, importers.MediaWhatsAppBridge(bridge))
}
