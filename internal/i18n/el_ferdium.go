package i18n

func init() {
	for k, v := range map[string]string{
		"the app's address, as the devices use it":                                                                      "η διεύθυνση της εφαρμογής, όπως τη χρησιμοποιούν οι συσκευές",
		"a folder of development recipes (default: those of Ferdium, Ferdi and Franz found here)":                       "ένας φάκελος development recipes (προεπιλογή: όσοι των Ferdium, Ferdi και Franz βρεθούν εδώ)",
		"Everysaid as a service of Ferdium (or Franz), with the unread chats on its icon: installed here, or as a zip.": "Το Everysaid ως υπηρεσία του Ferdium (ή του Franz), με τις αδιάβαστες συνομιλίες στο εικονίδιό του: εγκατεστημένο εδώ, ή ως zip.",
		"no Ferdium, Ferdi or Franz here: --dir their folder of development recipes, or a zip (everysaid ferdium zip)":  "δεν βρέθηκε εδώ Ferdium, Ferdi ή Franz: --dir ο φάκελος development recipes τους, ή ένα zip (everysaid ferdium zip)",
		"installed: {dir}": "εγκαταστάθηκε: {dir}",
		"In the app: restart it, then add the service Everysaid (under Development).": "Στην εφαρμογή: επανεκκίνηση, και μετά πρόσθεσε την υπηρεσία Everysaid (στην ενότητα Development).",
		"written: {file} (unpack it into the app's recipes/dev folder)":               "γράφτηκε: {file} (αποσυμπίεσέ το στον φάκελο recipes/dev της εφαρμογής)",
	} {
		EL[k] = v
	}
}
