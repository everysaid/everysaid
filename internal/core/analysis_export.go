package core

// What the analysis plugin (internal/plugins/analysis) needs of labels.go.

// SoundKey is one word as it sounds, the ending's s off (Python's labels._key).
func SoundKey(w string) string { return soundKey(w) }

// KnownFirsts is the first names of the people the archive names: {sound: usual spelling}
// (Python's labels._known_names()[0]).
func KnownFirsts(s *Store) map[string]string { return known(s).firsts }
