package i18n

// The server's own words (internal/server): its command line and console.
func init() {
	for k, v := range map[string]string{
		"First setup: open {url}":                          "Πρώτη ρύθμιση: άνοιξε {url}",
		"(valid for one hour)":                             "(ισχύει μία ώρα)",
		"Everysaid: {origin} (listening on {host}:{port})": "Everysaid: {origin} (ακούει στο {host}:{port})",
		"{n} passkeys":                                     "{n} passkeys",
		"no user yet: `everysaid serve` prints the link of the first setup": "κανένας χρήστης ακόμα: `everysaid serve` τυπώνει τον σύνδεσμο πρώτης ρύθμισης",
		"(one use, valid {minutes} minutes)":                                "(μίας χρήσης, ισχύει {minutes} λεπτά)",
		"(one use, valid {minutes} minutes · a new user)":                   "(μίας χρήσης, ισχύει {minutes} λεπτά · νέος χρήστης)",
		"no user":    "κανένας χρήστης",
		"— {what} —": "— {what} —",
	} {
		EL[k] = v
	}
}
