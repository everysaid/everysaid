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
		"Viber Desktop does not have this message":                                "Το Viber Desktop δεν έχει αυτό το μήνυμα",
		"This person is not among Viber Desktop's contacts":                       "Αυτό το πρόσωπο δεν είναι στις επαφές του Viber Desktop",
		"Viber Desktop's bridge was started without sending (VIBER_ALLOW_SEND=1)": "Η γέφυρα του Viber Desktop ξεκίνησε χωρίς αποστολή (VIBER_ALLOW_SEND=1)",
		"Viber Desktop did not take the text as written: nothing was sent":        "Το Viber Desktop δεν δέχτηκε το κείμενο όπως γράφτηκε: δεν στάλθηκε τίποτα",
		"Viber does not let this message be edited":                               "Το Viber δεν επιτρέπει να αλλάξει αυτό το μήνυμα",
		"Viber Desktop could not do it: {what}":                                   "Το Viber Desktop δεν μπόρεσε να το κάνει: {what}",
	} {
		EL[k] = v
	}
}
