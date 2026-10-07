package i18n

// The words of the source, library, contacts and analysis plugins (internal/plugins/...) that
// el.go does not have. Names of services and tools stay as they are.
func init() {
	for k, v := range map[string]string{
		"contacts changed: {n}, gone: {gone}, addresses found in the archive: {linked}": "επαφές που άλλαξαν: {n}, που αφαιρέθηκαν: {gone}, διευθύνσεις που βρέθηκαν στο αρχείο: {linked}",
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
		"immich":                            "immich",
		"Unknown action":                    "Άγνωστη ενέργεια",
		"no model answered":                 "κανένα μοντέλο δεν απάντησε",
	} {
		EL[k] = v
	}
}
