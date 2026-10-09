package i18n

// The words of WhatsApp inside the process (internal/whatsapp) that el.go does not have.
func init() {
	for k, v := range map[string]string{
		"WhatsApp as it arrives, through a whatsmeow client inside Everysaid, linked as a device (like WhatsApp Web). Unofficial: WhatsApp may block accounts that use one; sending raises that risk.": "Το WhatsApp όπως έρχεται, μέσω ενός client whatsmeow μέσα στο Everysaid, συνδεδεμένου ως συσκευή (όπως το WhatsApp Web). Ανεπίσημο: το WhatsApp μπορεί να μπλοκάρει λογαριασμούς που τον χρησιμοποιούν· η αποστολή μεγαλώνει το ρίσκο.",
		"a link from the phone (a QR code)": "σύνδεση από το κινητό (ένας κωδικός QR)",
		"A risk for the account; needs [whatsapp] send = true in config.toml. Turned off by itself when WhatsApp warns the account": "Ρίσκο για τον λογαριασμό· χρειάζεται [whatsapp] send = true στο config.toml. Κλείνει μόνη της όταν το WhatsApp προειδοποιήσει τον λογαριασμό",
		"Link a device (QR code)":              "Σύνδεση συσκευής (κωδικός QR)",
		"Allow sending again":                  "Να επιτρέπεται ξανά η αποστολή",
		"off in config.toml ([whatsapp] send)": "κλειστή στο config.toml ([whatsapp] send)",
		"not linked yet (Link a device)":       "δεν έχει συνδεθεί ακόμα (Σύνδεση συσκευής)",
		"The standalone WhatsApp bridge is running: stop it first (both on one device would end its session)": "Η ξεχωριστή γέφυρα WhatsApp τρέχει: σταμάτησέ τη πρώτα (και οι δύο στην ίδια συσκευή θα τερμάτιζαν τη σύνδεσή της)",
		"Scan this QR code in WhatsApp on the phone (Linked devices):":                                        "Σάρωσε αυτόν τον κωδικό QR στο WhatsApp του κινητού (Συνδεδεμένες συσκευές):",
		"Not linked to WhatsApp yet: use “Link a device” and scan the QR code with the phone":                 "Δεν έχει γίνει ακόμα σύνδεση στο WhatsApp: πάτα «Σύνδεση συσκευής» και σάρωσε τον κωδικό QR με το κινητό",
		"Turn the live connection on first: linking goes through it":                                          "Άνοιξε πρώτα τη ζωντανή σύνδεση: η σύνδεση συσκευής γίνεται μέσα από αυτήν",
		"Linked to WhatsApp":                                       "Συνδέθηκε στο WhatsApp",
		"Already linked to WhatsApp":                               "Είναι ήδη συνδεδεμένη στο WhatsApp",
		"No QR code was scanned in time":                           "Κανένας κωδικός QR δεν σαρώθηκε εγκαίρως",
		"Sending allowed again at the bridge (blocked for: {why})": "Η αποστολή επιτρέπεται ξανά στη γέφυρα (ήταν μπλοκαρισμένη για: {why})",
		"Sending limits":                                           "Όρια αποστολής",
		"{minute} a minute, {hour} an hour, {day} a day; the same text into {same} chats an hour; {new} new chats a day": "{minute} το λεπτό, {hour} την ώρα, {day} τη μέρα· το ίδιο κείμενο σε {same} συνομιλίες την ώρα· {new} νέες συνομιλίες τη μέρα",
		"New chats started from here at most a day":                                                            "Μέγιστες νέες συνομιλίες από εδώ τη μέρα",
		"A first message to someone found on WhatsApp, only to people in your contacts or in a group with you": "Πρώτο μήνυμα σε κάποιον που βρέθηκε στο WhatsApp, μόνο σε άτομα από τις επαφές σου ή από κοινή ομάδα",
		"A new WhatsApp chat starts from here only with someone in your contacts or in a group with you":       "Νέα συνομιλία στο WhatsApp ξεκινά από εδώ μόνο με κάποιον από τις επαφές σου ή από κοινή ομάδα",
		"Messages sent at most a minute":                                                                       "Μέγιστα μηνύματα το λεπτό",
		"Bulk sending is what WhatsApp blocks accounts for; 0 in any of these, no limit":                       "Το WhatsApp μπλοκάρει λογαριασμούς για μαζικές αποστολές· 0 σε οποιοδήποτε από αυτά σημαίνει χωρίς όριο",
		"Messages sent at most an hour":                                                                        "Μέγιστα μηνύματα την ώρα",
		"Messages sent at most a day":                                                                          "Μέγιστα μηνύματα τη μέρα",
		"The same longer text into at most so many chats an hour":                                              "Το ίδιο μεγάλο κείμενο σε τόσες συνομιλίες το πολύ την ώρα",
		"unknown action": "άγνωστη ενέργεια",
	} {
		EL[k] = v
	}
}
