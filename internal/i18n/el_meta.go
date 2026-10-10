package i18n

// Messenger's and Instagram's words: the importer of Meta's export (internal/importers) and the
// sources (internal/meta).
func init() {
	for k, v := range map[string]string{
		// the importer of the export
		"Messenger's export (Download your information)":                                    "Η εξαγωγή του Messenger (Λήψη των πληροφοριών σου)",
		"Instagram's export (Download your information)":                                    "Η εξαγωγή του Instagram (Λήψη των πληροφοριών σου)",
		"{service}: no export (Download your information) found":                            "{service}: δεν βρέθηκε εξαγωγή (Λήψη των πληροφοριών σου)",
		"{service}: no chats in the export {dir}":                                           "{service}: καμία συνομιλία στην εξαγωγή {dir}",
		"{service}: {n} new messages ({calls} calls) from {threads} chats of the export":    "{service}: {n} νέα μηνύματα ({calls} κλήσεις) από {threads} συνομιλίες της εξαγωγής",
		"{service}: no export set ([{service}] export in config.toml)":                      "{service}: δεν έχει οριστεί εξαγωγή ([{service}] export στο config.toml)",
		"the export does not say whose it is: set the owner's name as the export writes it": "η εξαγωγή δεν λέει τίνος είναι: όρισε το όνομα του κατόχου όπως το γράφει η εξαγωγή",

		// the sources: their manifests
		"Facebook Messenger with the user's own account, logged in with the browser's cookies: every chat as it changes, the encrypted ones too, with each chat's earlier history; and Meta's export (Download your information) for what is older. Unofficial: Meta may ask to check the account.": "Το Facebook Messenger με τον δικό σου λογαριασμό, συνδεδεμένο με τα cookies του φυλλομετρητή: κάθε συνομιλία καθώς αλλάζει, και οι κρυπτογραφημένες, με το παλαιότερο ιστορικό κάθε συνομιλίας· και η εξαγωγή της Meta (Λήψη των πληροφοριών σου) για τα παλαιότερα. Ανεπίσημο: η Meta μπορεί να ζητήσει έλεγχο του λογαριασμού.",
		"the cookies of facebook.com or messenger.com, where you are logged in": "τα cookies του facebook.com ή του messenger.com, όπου είσαι συνδεδεμένος",
		"or Meta's export": "ή την εξαγωγή της Meta",
		"The export (Download your information), unpacked":                                  "Η εξαγωγή (Λήψη των πληροφοριών σου), αποσυμπιεσμένη",
		"Its JSON form, one folder or a folder of its parts; imported when the source runs": "Σε μορφή JSON, ένας φάκελος ή ένας φάκελος με τα μέρη της· εισάγεται όταν τρέχει η πηγή",
		"Your name as the export writes it":                                                 "Το όνομά σου όπως το γράφει η εξαγωγή",
		"Only where the export does not say whose it is":                                    "Μόνο όπου η εξαγωγή δεν λέει τίνος είναι",
		"Cookies of facebook.com or messenger.com":                                          "Cookies του facebook.com ή του messenger.com",
		"From the browser where you are logged in: a JSON object of them, a request copied as cURL from the developer tools, or the Cookie header (c_user, xs and datr are needed). Checked with Meta and kept in keys.db; give them again when Meta logs the source out": "Από τον φυλλομετρητή όπου είσαι συνδεδεμένος: ένα αντικείμενο JSON με αυτά, ένα αίτημα αντιγραμμένο ως cURL από τα εργαλεία προγραμματιστή, ή η κεφαλίδα Cookie (χρειάζονται τα c_user, xs και datr). Ελέγχονται με τη Meta και φυλάσσονται στο keys.db· δώσε τα ξανά όταν η Meta αποσυνδέσει την πηγή",
		"The cookies are of":                                "Τα cookies είναι του",
		"Download pictures, videos and files":               "Λήψη φωτογραφιών, βίντεο και αρχείων",
		"As they arrive (Meta's addresses for them expire)": "Καθώς φτάνουν (οι διευθύνσεις της Meta για αυτά λήγουν)",
		"Bring each chat's earlier messages":                "Φέρε τα παλαιότερα μηνύματα κάθε συνομιλίας",
		"Back to what the archive has, a page at a time":    "Ως εκεί που τα έχει ήδη το αρχείο, μία σελίδα τη φορά",
		"Log out": "Αποσύνδεση",
		"The source forgets the account's cookies and its encrypted chats' device; the archive keeps everything": "Η πηγή ξεχνά τα cookies του λογαριασμού και τη συσκευή των κρυπτογραφημένων συνομιλιών του· το αρχείο τα κρατά όλα",

		"Instagram's direct messages with the user's own account, logged in with the browser's cookies: every chat as it changes, with each chat's earlier history; and Meta's export (Download your information) for what is older. Unofficial: Meta may ask to check the account.": "Τα προσωπικά μηνύματα του Instagram με τον δικό σου λογαριασμό, συνδεδεμένο με τα cookies του φυλλομετρητή: κάθε συνομιλία καθώς αλλάζει, με το παλαιότερο ιστορικό κάθε συνομιλίας· και η εξαγωγή της Meta (Λήψη των πληροφοριών σου) για τα παλαιότερα. Ανεπίσημο: η Meta μπορεί να ζητήσει έλεγχο του λογαριασμού.",
		"the cookies of instagram.com, where you are logged in": "τα cookies του instagram.com, όπου είσαι συνδεδεμένος",
		"Cookies of instagram.com":                              "Cookies του instagram.com",
		"From the browser where you are logged in: a JSON object of them, a request copied as cURL from the developer tools, or the Cookie header (sessionid, csrftoken and ds_user_id are needed). Checked with Meta and kept in keys.db; give them again when Meta logs the source out": "Από τον φυλλομετρητή όπου είσαι συνδεδεμένος: ένα αντικείμενο JSON με αυτά, ένα αίτημα αντιγραμμένο ως cURL από τα εργαλεία προγραμματιστή, ή η κεφαλίδα Cookie (χρειάζονται τα sessionid, csrftoken και ds_user_id). Ελέγχονται με τη Meta και φυλάσσονται στο keys.db· δώσε τα ξανά όταν η Meta αποσυνδέσει την πηγή",
		"Logged out of Instagram: log in again (paste the cookies)": "Έγινε αποσύνδεση από το Instagram: συνδέσου ξανά (επικόλλησε τα cookies)",
		"Instagram did not say the account's id":                    "Το Instagram δεν είπε το αναγνωριστικό του λογαριασμού",

		// their state
		"the cookies or the export":      "τα cookies ή η εξαγωγή",
		"Account":                        "Λογαριασμός",
		"not logged in":                  "χωρίς σύνδεση",
		"connected":                      "συνδεδεμένο",
		"not connected":                  "χωρίς σύνδεση",
		"Encrypted chats":                "Κρυπτογραφημένες συνομιλίες",
		"Files to download":              "Αρχεία για λήψη",
		"Messenger live":                 "Messenger ζωντανά",
		"Instagram live":                 "Instagram ζωντανά",
		"Messenger export":               "Εξαγωγή του Messenger",
		"Instagram export":               "Εξαγωγή του Instagram",
		"{service}: logged in as {name}": "{service}: σύνδεση ως {name}",
		"{service}: logged out":          "{service}: αποσύνδεση",
		"{service}: not logged in (the cookies in its settings)": "{service}: χωρίς σύνδεση (τα cookies στις ρυθμίσεις της πηγής)",
		"{service}: connected as {name}":                         "{service}: συνδέθηκε ως {name}",
		"{service}: not written into the archive ({e})":          "{service}: δεν γράφτηκε στο αρχείο ({e})",
		"{service}: earlier messages not brought ({e})":          "{service}: δεν ήρθαν τα παλαιότερα μηνύματα ({e})",
		"{service}: the list of chats not brought ({e})":         "{service}: δεν ήρθε ο κατάλογος των συνομιλιών ({e})",
		"{service}: the encrypted chats are not connected ({e})": "{service}: οι κρυπτογραφημένες συνομιλίες δεν συνδέθηκαν ({e})",
		"{service}: the encrypted chats' device was logged out":  "{service}: η συσκευή των κρυπτογραφημένων συνομιλιών αποσυνδέθηκε",
		"{service}: a chat not brought ({e})":                    "{service}: δεν ήρθε μια συνομιλία ({e})",

		// their errors
		"not connected to Messenger":                                                          "δεν υπάρχει σύνδεση με το Messenger",
		"not connected to Instagram":                                                          "δεν υπάρχει σύνδεση με το Instagram",
		"Logged out of Messenger: log in again (paste the cookies)":                           "Έγινε αποσύνδεση από το Messenger: συνδέσου ξανά (επικόλλησε τα cookies)",
		"Meta asks for a check on its site: open facebook.com in the browser, then try again": "Η Meta ζητά έλεγχο στον ιστότοπό της: άνοιξε το facebook.com στον φυλλομετρητή και δοκίμασε ξανά",
		"The cookies are of another account than this source's":                               "Τα cookies είναι άλλου λογαριασμού από αυτόν της πηγής",
		"The cookies are of another account than the one logged in":                           "Τα cookies είναι άλλου λογαριασμού από αυτόν που συνδέθηκε",
		"No cookies found in what was pasted":                                                 "Δεν βρέθηκαν cookies σε ό,τι επικολλήθηκε",
		"Missing cookies: {names}":                                                            "Λείπουν cookies: {names}",
		"This chat has no thread on the service":                                              "Αυτή η συνομιλία δεν έχει νήμα στην υπηρεσία",
		"Meta refused the message":                                                            "Η Meta αρνήθηκε το μήνυμα",
		"Meta did not say the message's id":                                                   "Η Meta δεν είπε το αναγνωριστικό του μηνύματος",
		"Meta did not take the edit":                                                          "Η Meta δεν δέχτηκε την επεξεργασία",
	} {
		EL[k] = v
	}
}
