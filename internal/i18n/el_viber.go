package i18n

// The words of the Viber Desktop source (internal/viber) that el.go does not have.
func init() {
	for k, v := range map[string]string{
		"Viber through the Viber Desktop running on this computer, with Everysaid's bridge (bridges/viber): its history, what arrives, and sending. Linux only: there is no official way.": "Το Viber μέσω του Viber Desktop που τρέχει σε αυτόν τον υπολογιστή, με τη γέφυρα του Everysaid (bridges/viber): το ιστορικό του, ό,τι έρχεται, και αποστολή. Μόνο σε Linux: επίσημος δρόμος δεν υπάρχει.",
		"Everysaid's Viber bridge (bridges/viber)":                             "η γέφυρα Viber του Everysaid (bridges/viber)",
		"The bridge's socket":                                                  "Το socket της γέφυρας",
		"Also needs the bridge started with VIBER_ALLOW_SEND=1":                "Χρειάζεται επίσης η γέφυρα να έχει ξεκινήσει με VIBER_ALLOW_SEND=1",
		"Viber Desktop is not running with Everysaid's bridge: waiting for it": "Το Viber Desktop δεν τρέχει με τη γέφυρα του Everysaid: αναμονή",
		"Viber Desktop is not running with Everysaid's bridge":                 "Το Viber Desktop δεν τρέχει με τη γέφυρα του Everysaid",
		"running":                           "τρέχει",
		"not running":                       "δεν τρέχει",
		"This chat is not in Viber Desktop": "Αυτή η συνομιλία δεν υπάρχει στο Viber Desktop",
		"Viber Desktop does not have this message":                                                  "Το Viber Desktop δεν έχει αυτό το μήνυμα",
		"This person is not among Viber Desktop's contacts":                                         "Αυτό το πρόσωπο δεν είναι στις επαφές του Viber Desktop",
		"Viber Desktop's bridge was started without sending (VIBER_ALLOW_SEND=1)":                   "Η γέφυρα του Viber Desktop ξεκίνησε χωρίς αποστολή (VIBER_ALLOW_SEND=1)",
		"Viber Desktop did not take the text as written: nothing was sent":                          "Το Viber Desktop δεν δέχτηκε το κείμενο όπως γράφτηκε: δεν στάλθηκε τίποτα",
		"Viber does not let this message be edited":                                                 "Το Viber δεν επιτρέπει να αλλάξει αυτό το μήνυμα",
		"Viber Desktop could not do it: {what}":                                                     "Το Viber Desktop δεν μπόρεσε να το κάνει: {what}",
		"This version of Viber Desktop cannot":                                                      "Αυτή η έκδοση του Viber Desktop δεν υποστηρίζει",
		"Ready, but this version of Viber Desktop cannot":                                           "Έτοιμο, αλλά αυτή η έκδοση του Viber Desktop δεν υποστηρίζει",
		"unknown (a bridge older than its check: make -C bridges/viber/inject, then restart Viber)": "άγνωστη (η γέφυρα είναι παλαιότερη από τον έλεγχό της: make -C bridges/viber/inject και επανεκκίνηση του Viber)",
		"Viber Desktop version":                                                                     "Έκδοση Viber Desktop",
		"Missing in this version":                                                                   "Λείπουν σε αυτή την έκδοση",
		"reading":                                                                                   "ανάγνωση",
		"receiving live":                                                                            "ζωντανή λήψη",
		"sending":                                                                                   "αποστολή",
		"replies and edits":                                                                         "απαντήσεις και διορθώσεις",
		"reactions":                                                                                 "αντιδράσεις",
		"deleting for everyone":                                                                     "διαγραφή για όλους",
		"read receipts":                                                                             "επιβεβαιώσεις ανάγνωσης",
		"Viber Desktop {from} → {to}: everything the bridge uses is there":                          "Viber Desktop {from} → {to}: υπάρχουν όλα όσα χρησιμοποιεί η γέφυρα",
		"Viber Desktop {from} → {to}, missing: {what}":                                              "Viber Desktop {from} → {to}, λείπουν: {what}",
		"Viber Desktop {version}: everything the bridge uses is there":                              "Viber Desktop {version}: υπάρχουν όλα όσα χρησιμοποιεί η γέφυρα",
		"Viber Desktop {version}, missing: {what}":                                                  "Viber Desktop {version}, λείπουν: {what}",
		"Receiving live from Viber Desktop does not work: new messages come only with the check every {every} seconds": "Η ζωντανή λήψη από το Viber Desktop δεν λειτουργεί: τα νέα μηνύματα έρχονται μόνο με τον έλεγχο κάθε {every} δευτερόλεπτα",
		"Receiving live from Viber Desktop works again":                                                                "Η ζωντανή λήψη από το Viber Desktop λειτουργεί ξανά",
	} {
		EL[k] = v
	}
}
