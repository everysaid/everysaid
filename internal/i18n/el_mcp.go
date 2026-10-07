package i18n

// The words of the MCP server's command line (internal/mcp) that el.go does not have.
func init() {
	for k, v := range map[string]string{
		"the archive (default: {path})":                          "το αρχείο (αν δεν δοθεί: {path})",
		"the same as --db":                                       "το ίδιο με το --db",
		"The archive as an MCP server for an assistant (stdio).": "Το αρχείο ως διακομιστής MCP για έναν βοηθό (stdio).",
		"cannot open the archive: {error}":                       "δεν ανοίγει το αρχείο: {error}",
	} {
		EL[k] = v
	}
}
