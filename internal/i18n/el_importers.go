package i18n

// The importers' lines (internal/importers) in Greek.
func init() {
	for k, v := range map[string]string{
		"== {name}: {what}":                           "== {name}: {what}",
		"SMS, MMS, iMessage, RCS":                     "SMS, MMS, iMessage, RCS",
		"the phones' call logs":                       "τα αρχεία κλήσεων των κινητών",
		"Viber messages":                              "μηνύματα Viber",
		"WhatsApp messages":                           "μηνύματα WhatsApp",
		"Telegram messages and calls":                 "μηνύματα και κλήσεις Telegram",
		"WhatsApp and Viber calls, carrier notices":   "κλήσεις WhatsApp και Viber, ειδοποιήσεις παρόχου",
		"the files of messages":                       "τα αρχεία των μηνυμάτων",
		"unknown importer":                            "άγνωστος εισαγωγέας",
		"unknown carrier in [import] carrier_notices": "άγνωστος πάροχος στο [import] carrier_notices",

		"no source: neither {db} nor an Android export": "καμία πηγή: ούτε {db} ούτε export Android",
		"no source: neither {a} nor {b}":                "καμία πηγή: ούτε {a} ούτε {b}",
		"no source: {db} (telegram-sync)":               "καμία πηγή: {db} (telegram-sync)",

		"iPhone: {n} rows ({dup} repeats)":                             "iPhone: {n} γραμμές ({dup} επαναλήψεις)",
		"Android ({devices}): {n} rows of SMS and MMS ({dup} repeats)": "Android ({devices}): {n} γραμμές SMS και MMS ({dup} επαναλήψεις)",
		"pairs:  {n} (from Android in its time: {m})":                  "ζεύγη:  {n} (από το Android στην εποχή του: {m})",
		"new:    {n} {source} {service}":                               "νέα:    {n} {source} {service}",
		"new:    none":                                                 "νέα:    κανένα",
		"iPhone: {n} calls":                                            "iPhone: {n} κλήσεις",
		"Android ({devices}): {n} calls":                               "Android ({devices}): {n} κλήσεις",
		"new:   {n} {source} {service}":                                "νέες:   {n} {source} {service}",
		"new:   {n} {service}":                                         "νέες:   {n} {service}",
		"new:   none":                                                  "νέες:   καμία",
		"iPhone:  {n} messages, Desktop (Android): {events}":           "iPhone:  {n} μηνύματα, Desktop (Android): {events}",
		"new:     {n} {source}":                                        "νέα:     {n} {source}",
		"new:     {n} {source} ({skipped} were already there from another source)": "νέα:     {n} {source} ({skipped} υπήρχαν ήδη από άλλη πηγή)",
		"changes to what was there: {list}":                                        "αλλαγές σε όσα υπήρχαν: {list}",
		"new:     {n} {kind}":                                                      "νέα:     {n} {kind}",
		"calls: {n}":                                                               "κλήσεις: {n}",
		"new:    {n} {source}":                                                     "νέα:    {n} {source}",
		"without: {n} {source} ({why})":                                            "χωρίς:  {n} {source} ({why})",
		"no message":                                                               "χωρίς μήνυμα",
		"no file":                                                                  "χωρίς αρχείο",
		"total: {count} files, {size} GB, {links} links to messages": "σύνολο: {count} αρχεία, {size} GB, {links} συνδέσεις με μηνύματα",
	} {
		EL[k] = v
	}
}
