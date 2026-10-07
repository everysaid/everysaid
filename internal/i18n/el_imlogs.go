package i18n

// The Adium and Pidgin logs importer (internal/importers/imlogs.go): its report.
func init() {
	for k, v := range map[string]string{
		"no source: neither [imlogs] adium nor [imlogs] pidgin":              "καμία πηγή: ούτε [imlogs] adium ούτε [imlogs] pidgin",
		"no Adium folder: {path}":                                            "δεν βρέθηκε φάκελος του Adium: {path}",
		"no Pidgin folder: {path}":                                           "δεν βρέθηκε φάκελος του Pidgin: {path}",
		"source                  new    sent  already  dupes  chats  period": "πηγή                    νέα σταλμένα      ήδη  διπλά συνομιλίες  περίοδος",
		"total              {new} {sent} {already} {dupes} {chats}":          "σύνολο             {new} {sent} {already} {dupes} {chats}",
		"pictures: {n} {key}":                                                "εικόνες:  {n} {key}",
		"names:    {n} {device} ({kind})":                                    "ονόματα:  {n} {device} ({kind})",
		"merged:   {n} people into others, as grouped in {device}":           "συγχωνεύτηκαν: {n} πρόσωπα σε άλλα, όπως ήταν ομαδοποιημένα στο {device}",
		"not read: {n} {what}":                                               "δεν διαβάστηκαν: {n} {what}",
		"files without a date":                                               "αρχεία χωρίς ημερομηνία",
		"lines not read":                                                     "γραμμές που δεν διαβάστηκαν",
		"(dry run: nothing written)":                                         "(δοκιμαστική εκτέλεση: δεν γράφτηκε τίποτα)",
	} {
		EL[k] = v
	}
}
