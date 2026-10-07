package i18n

// The demo (internal/demo): its command line (the sender's words are in el.go).
func init() {
	for k, v := range map[string]string{
		"demo: {messages} messages, {calls} calls, {people} people, {groups} groups, {pictures} pictures -> {path}": "demo: {messages} μηνύματα, {calls} κλήσεις, {people} πρόσωπα, {groups} ομάδες, {pictures} εικόνες -> {path}",
		"usage: everysaid demo [--dir DIR] [--seed SEED] [--serve]":                                                 "χρήση: everysaid demo [--dir DIR] [--seed SEED] [--serve]",
		"A demo archive of invented people.":                                                                        "Ένα αρχείο demo με επινοημένα πρόσωπα.",
		"the folder of the demo (default: demo)":                                                                    "ο φάκελος του demo (προεπιλογή: demo)",
		"the seed of what is invented (default: 7)":                                                                 "ο σπόρος των επινοημένων (προεπιλογή: 7)",
		"then start the app on it":                                                                                  "μετά ξεκινά την εφαρμογή πάνω του",
		"unrecognized arguments: {args}":                                                                            "μη αναγνωρισμένα ορίσματα: {args}",
	} {
		EL[k] = v
	}
}
