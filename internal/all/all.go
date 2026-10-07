// Package all loads every plugin Everysaid has (each registers itself when its package loads):
// the command line and the tests that look over all of them import it.
package all

import (
	_ "everysaid/internal/plugins/analysis"
	_ "everysaid/internal/plugins/contacts"
	_ "everysaid/internal/plugins/libraries"
	_ "everysaid/internal/plugins/sources"
	_ "everysaid/internal/signal"
	_ "everysaid/internal/telegram"
	_ "everysaid/internal/viber"
	_ "everysaid/internal/whatsapp"
)

// Loaded is referred to by importers of this package, so that it is not an unused import.
const Loaded = true
