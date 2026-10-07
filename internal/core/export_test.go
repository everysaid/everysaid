package core

// CacheSize is how many values the store keeps built.
func CacheSize(s *Store) int {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	return len(s.cache)
}
