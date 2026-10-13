package plugins

import "sync"

// GIFsFound keeps what a GIFSearcher found, where each GIF is by its id (T: the searcher's own), and
// the files fetched: a GIF found is asked for by its id, shown then sent. The last Keep found are
// kept, and files up to Bytes in all.
type GIFsFound[T any] struct {
	Keep, Bytes int

	mu    sync.Mutex
	at    map[string]T
	order []string
	files map[string][]byte
	held  []string
	size  int
}

// Put keeps where a GIF found is.
func (g *GIFsFound[T]) Put(id string, at T) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.at == nil {
		g.at, g.files = map[string]T{}, map[string][]byte{}
	}
	if _, ok := g.at[id]; !ok {
		g.order = append(g.order, id)
	}
	g.at[id] = at
	for len(g.order) > g.Keep {
		delete(g.at, g.order[0])
		g.order = g.order[1:]
	}
}

// File is the GIF's file, fetched where it is not kept; false for a GIF not found (or no longer).
func (g *GIFsFound[T]) File(id string, fetch func(at T) ([]byte, error)) ([]byte, bool, error) {
	g.mu.Lock()
	at, found := g.at[id]
	data := g.files[id]
	g.mu.Unlock()
	if !found {
		return nil, false, nil
	}
	if data != nil {
		return data, true, nil
	}
	data, err := fetch(at)
	if err != nil {
		return nil, true, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.files[id]; !ok {
		g.files[id], g.held, g.size = data, append(g.held, id), g.size+len(data)
		for g.size > g.Bytes && len(g.held) > 1 {
			g.size -= len(g.files[g.held[0]])
			delete(g.files, g.held[0])
			g.held = g.held[1:]
		}
	}
	return data, true, nil
}

// At is where a GIF found is.
func (g *GIFsFound[T]) At(id string) (T, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	at, ok := g.at[id]
	return at, ok
}
