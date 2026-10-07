// Package all loads every plugin Everysaid has (each registers itself when its package loads):
// the command line and the tests that look over all of them import it.
package all

// Loaded is referred to by importers of this package, so that it is not an unused import.
const Loaded = true
