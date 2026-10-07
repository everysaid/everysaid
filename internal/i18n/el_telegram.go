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
		"[telegram] media = false in config: no media are downloaded": "[telegram] media = false στις ρυθμίσεις: δεν κατεβαίνουν αρχεία",
		"Telegram live":                                               "Telegram ζωντανά",
		"Not signed in to Telegram yet (everysaid telegram-sync --save-credentials, --login)": "Δεν έχει γίνει ακόμα σύνδεση στο Telegram (everysaid telegram-sync --save-credentials, --login)",
		"The Telegram sign-in has expired: everysaid telegram-sync --login":                   "Η σύνδεση στο Telegram έληξε: everysaid telegram-sync --login",
		"api_id and api_hash (everysaid telegram-sync --save-credentials)":                    "api_id και api_hash (everysaid telegram-sync --save-credentials)",
		"a login (everysaid telegram-sync --login)":                                           "η σύνδεση (everysaid telegram-sync --login)",
	} {
		EL[k] = v
	}
}
