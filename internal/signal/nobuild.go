//go:build !signal

package signal

// Without the build tag `signal` Everysaid has no Signal client (signalmeow needs Signal's library,
// libsignal, through cgo): the source says so, and nothing connects.

import "everysaid/internal/errs"

// available: a client can be made (the tests' fake makes one without the tag).
var available = false

func startClient(cfg clientConfig) (transport, error) { return nil, errs.Plugin(notBuiltText, 0) }

// dropDevice: no device was ever made without Signal built in.
func dropDevice(instance int64) error { return nil }
