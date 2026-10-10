package i18n

// Messenger's and Instagram's words: the importer of Meta's export (internal/importers) and the
// live source (internal/meta).
func init() {
	for k, v := range map[string]string{
		"Messenger's export (Download your information)":                                    "Η εξαγωγή του Messenger (Λήψη των πληροφοριών σου)",
		"Instagram's export (Download your information)":                                    "Η εξαγωγή του Instagram (Λήψη των πληροφοριών σου)",
		"{service}: no export (Download your information) found":                            "{service}: δεν βρέθηκε εξαγωγή (Λήψη των πληροφοριών σου)",
		"{service}: no chats in the export {dir}":                                           "{service}: καμία συνομιλία στην εξαγωγή {dir}",
		"{service}: {n} new messages ({calls} calls) from {threads} chats of the export":    "{service}: {n} νέα μηνύματα ({calls} κλήσεις) από {threads} συνομιλίες της εξαγωγής",
		"{service}: no export set ([{service}] export in config.toml)":                      "{service}: δεν έχει οριστεί εξαγωγή ([{service}] export στο config.toml)",
		"the export does not say whose it is: set the owner's name as the export writes it": "η εξαγωγή δεν λέει τίνος είναι: όρισε το όνομα του κατόχου όπως το γράφει η εξαγωγή",
	} {
		EL[k] = v
	}
}
