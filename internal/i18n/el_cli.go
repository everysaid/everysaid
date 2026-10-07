package i18n

func init() {
	for k, v := range map[string]string{
		"Fill the archive from the sources. Importers: {names}; default: config [import] importers, else all.": "Γέμισμα του αρχείου από τις πηγές. Importers: {names}· προεπιλογή: config [import] importers, αλλιώς όλοι.",
		"unknown importers: {names}": "άγνωστοι importers: {names}",
	} {
		EL[k] = v
	}
}
