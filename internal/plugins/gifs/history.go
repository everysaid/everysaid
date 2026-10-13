package gifs

import "everysaid/internal/plugins"

func init() { plugins.Register(History{}) }

// History is the GIFs already in the archive, sent or received before, as a tab of the GIFs like
// those of the plugins that find them: there from the start (archive.seed), taken away and put back
// as they are. The app shows them itself (/api/media?gif=1): nothing leaves this computer.
type History struct{}

func (History) Info() *plugins.Info {
	return &plugins.Info{
		ID: "gif-history", Name: "History", Kind: "gifs",
		Description: "The GIFs already in the archive, sent or received before: each once, however many copies the " +
			"services made of it. Nothing leaves this computer.",
	}
}

func (History) GIFHistory() {}
