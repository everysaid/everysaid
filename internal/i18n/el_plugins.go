package i18n

// The words of the source, library, contacts and analysis plugins (internal/plugins/...) that
// el.go does not have. Names of services and tools stay as they are.
func init() {
	for k, v := range map[string]string{
		"contacts changed: {n}, gone: {gone}, addresses found in the archive: {linked}": "επαφές που άλλαξαν: {n}, που αφαιρέθηκαν: {gone}, διευθύνσεις που βρέθηκαν στο αρχείο: {linked}",
		"people merged as one contact lists them: {n}":                                  "πρόσωπα που ενώθηκαν γιατί μία επαφή τα περιέχει: {n}",
		"Backup":                            "Backup",
		"UDID":                              "UDID",
		"SMS, iMessage":                     "SMS, iMessage",
		"SMS, MMS":                          "SMS, MMS",
		"Viber":                             "Viber",
		"WhatsApp":                          "WhatsApp",
		"Viber Desktop":                     "Viber Desktop",
		"adb":                               "adb",
		"libimobiledevice (idevicebackup2)": "libimobiledevice (idevicebackup2)",
		"API key":                           "API key",
		"History":                           "Ιστορικό",
		"The GIFs already in the archive, sent or received before: each once, however many copies the services made of it. Nothing leaves this computer.": "Τα GIF που υπάρχουν ήδη στο αρχείο, όσα έστειλες ή έλαβες: το καθένα μία φορά, όσα αντίγραφα κι αν έφτιαξαν οι υπηρεσίες. Τίποτα δεν φεύγει από αυτόν τον υπολογιστή.",
		"Telegram (@gif)": "Telegram (@gif)",
		"GIFs found by Telegram's @gif bot, through the Telegram account linked here: an inline query, nothing is sent. Telegram sees the words searched for, as in its own apps.": "GIF που βρίσκει το bot @gif του Telegram, μέσω του λογαριασμού Telegram που είναι συνδεδεμένος εδώ: μια αναζήτηση inline, δεν στέλνεται τίποτα. Το Telegram βλέπει τις λέξεις που ψάχνεις, όπως και στις δικές του εφαρμογές.",
		"The Telegram account linked (its source)": "Ο λογαριασμός Telegram συνδεδεμένος (η πηγή του)",
		"Klipy": "Klipy",
		"GIFs from Klipy, to send through any service. Klipy sees the words searched for, from this server's address.": "GIF από το Klipy, για αποστολή από οποιαδήποτε υπηρεσία. Το Klipy βλέπει τις λέξεις που ψάχνεις, από τη διεύθυνση αυτού του server.",
		"An API key from partner.klipy.com": "Ένα API key από το partner.klipy.com",
		"partner.klipy.com → API Keys":      "Στο partner.klipy.com, στα API Keys",
		"the API key":                       "το API key",
		"Klipy needs its API key":           "Το Klipy θέλει το API key του",
		"Klipy did not answer":              "Το Klipy δεν απάντησε",
		"Klipy refused the API key":         "Το Klipy δεν δέχτηκε το API key",
		"Klipy's limit of searches for now is reached: try again later": "Έφτασε το όριο αναζητήσεων του Klipy για τώρα: δοκίμασε ξανά αργότερα",
		"immich":            "immich",
		"Unknown action":    "Άγνωστη ενέργεια",
		"no model answered": "κανένα μοντέλο δεν απάντησε",
	} {
		EL[k] = v
	}
}
