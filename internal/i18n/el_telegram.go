package i18n

// The Telegram source's words (internal/telegram): the sync's lines and the plugin's.
func init() {
	for k, v := range map[string]string{
		"unchanged":                                                   "αμετάβλητη",
		"logged in as {name} (session: {where})":                      "σύνδεση ως {name} (συνεδρία: {where})",
		"not logged in: run with --login first":                       "χωρίς σύνδεση: τρέξε πρώτα με --login",
		"no credentials: run with --save-credentials first":           "δεν υπάρχουν στοιχεία API: τρέξε πρώτα με --save-credentials",
		"api_hash (hidden): ":                                         "api_hash (κρυφό): ",
		"api_id must be a number and api_hash not empty":              "το api_id πρέπει να είναι αριθμός και το api_hash όχι κενό",
		"saved in {where}":                                            "αποθηκεύτηκε στο {where}",
		"Please enter your phone: ":                                   "Γράψε το τηλέφωνό σου: ",
		"Please enter the code you received: ":                        "Γράψε τον κωδικό που έλαβες: ",
		"Please enter your password: ":                                "Γράψε τον κωδικό πρόσβασής σου: ",
		"{n} chats":                                                   "{n} συνομιλίες",
		"{kind} {chats} chats {messages} messages":                    "{kind} {chats} συνομιλίες {messages} μηνύματα",
		"details: {path}":                                             "λεπτομέρειες: {path}",
		"new":                                                         "νέα",
		"{n} new messages in {db}":                                    "{n} νέα μηνύματα στο {db}",
		"{n} files, {gb} GB, in {chats} chats":                        "{n} αρχεία, {gb} GB, σε {chats} συνομιλίες",
		"{n} downloaded into {folder}":                                "{n} κατέβηκαν στο {folder}",
		"{file}: not downloaded ({e})":                                "{file}: δεν κατέβηκε ({e})",
		"{chat}: not caught up ({e})":                                 "{chat}: δεν ενημερώθηκε ({e})",
		"[telegram] media = false in config: no media are downloaded": "[telegram] media = false στις ρυθμίσεις: δεν κατεβαίνουν αρχεία",
		"Telegram is connected by another Everysaid (the server, or telegram-sync run by hand): stop it, or wait until it finishes": "Το Telegram είναι συνδεδεμένο από άλλο Everysaid (τον server ή το telegram-sync που έτρεξε με το χέρι): σταμάτησέ το ή περίμενε να τελειώσει",
		"waiting: Telegram is connected by another Everysaid (telegram-sync run by hand?)":                                          "αναμονή: το Telegram είναι συνδεδεμένο από άλλο Everysaid (το telegram-sync με το χέρι;)",
		"Telegram live": "Telegram ζωντανά",
		"Of the messages that arrive, and of those of the last week when it connects. A chat's whole history: its Media column in the chats": "Των μηνυμάτων που φτάνουν, και της τελευταίας εβδομάδας όταν συνδέεται. Όλο το ιστορικό μιας συνομιλίας: η στήλη «Πολυμέσα» στις συνομιλίες",
		"Not signed in to Telegram yet (everysaid telegram-sync --save-credentials, --login)":                                                "Δεν έχει γίνει ακόμα σύνδεση στο Telegram (everysaid telegram-sync --save-credentials, --login)",
		"The Telegram sign-in has expired: everysaid telegram-sync --login":                                                                  "Η σύνδεση στο Telegram έληξε: everysaid telegram-sync --login",
		"api_id and api_hash (everysaid telegram-sync --save-credentials)":                                                                   "api_id και api_hash (everysaid telegram-sync --save-credentials)",
		"a login (everysaid telegram-sync --login)":                                                                                          "η σύνδεση (everysaid telegram-sync --login)",
		"Telegram does not allow this reaction in this chat":                                                                                 "Το Telegram δεν επιτρέπει αυτή την αντίδραση σε αυτή τη συνομιλία",
		"Telegram no longer allows this message to be edited":                                                                                "Το Telegram δεν επιτρέπει πια την επεξεργασία αυτού του μηνύματος",
		"Telegram does not allow this message to be deleted for everyone":                                                                    "Το Telegram δεν επιτρέπει να διαγραφεί αυτό το μήνυμα για όλους",
		"The message is no longer on Telegram":                                                                                               "Το μήνυμα δεν υπάρχει πια στο Telegram",
		"{chat}: Telegram did not give its members ({e})":                                                                                    "{chat}: το Telegram δεν έδωσε τα μέλη της ομάδας ({e})",
		"{chat}: Telegram gave {n} of its {count} members":                                                                                   "{chat}: το Telegram έδωσε {n} από τα {count} μέλη της ομάδας",
		"Only a chat with one person can be reported as spam":                                                                                "Μόνο μια συνομιλία με ένα πρόσωπο μπορεί να αναφερθεί ως spam",
		"Telegram does not know this person any more":                                                                                        "Το Telegram δεν γνωρίζει πια αυτό το πρόσωπο",
		"the blocked people could not be read: {e}":                                                                                          "δεν διαβάστηκαν οι αποκλεισμένοι: {e}",
	} {
		EL[k] = v
	}
}
