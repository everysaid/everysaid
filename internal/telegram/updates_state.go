// Where the live connection's updates stood (gotd's updates.StateStorage), kept in a small file
// beside the store, so that after a restart Telegram is asked for what happened meanwhile (edits,
// reads, new chats, as well as new messages) rather than starting again from now.
//
// The account's common sequence is kept (private chats and small groups), and the sequences of the
// supergroups telegram.db keeps: each is asked for on every start (one request each), for what
// changed there meanwhile (edits, deletions, reactions), as Telegram Desktop does. Broadcast
// channels' stay in memory (they are not kept).
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
)

// StatePath is the file of the updates' state.
func StatePath() string { return filepath.Join(Folder(), "updates-state.json") }

type savedState struct {
	User     int64         `json:"user"`
	State    updates.State `json:"state"`
	Channels map[int64]int `json:"channels,omitempty"` // the kept supergroups' pts, by channel id
}

// fileState keeps the common state in path, the channels' in memory.
type fileState struct {
	path     string
	mu       sync.Mutex
	state    *savedState
	channels map[int64]int
	kept     map[int64]bool // channels telegram.db keeps (asked once each)
}

var _ updates.StateStorage = (*fileState)(nil)

func newFileState(path string) *fileState {
	s := &fileState{path: path, channels: map[int64]int{}, kept: map[int64]bool{}}
	if b, err := os.ReadFile(path); err == nil {
		var saved savedState
		if json.Unmarshal(b, &saved) == nil && saved.User != 0 {
			s.state = &saved
		}
	}
	return s
}

var errNoState = errors.New("no updates state")

// save writes the state through a temporary file, on the disk before it takes the old one's place,
// so that a crash or a power cut never leaves half of it (or none: the updates while away would be
// lost, the state taken anew from the server).
func (s *fileState) save() error {
	b, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// change applies fn to the account's state and saves it; an error when there is none (gotd's rule).
func (s *fileState) change(user int64, fn func(*updates.State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil || s.state.User != user {
		return errNoState
	}
	fn(&s.state.State)
	return s.save()
}

func (s *fileState) GetState(_ context.Context, user int64) (updates.State, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil || s.state.User != user { // another account's, after a new login
		return updates.State{}, false, nil
	}
	return s.state.State, true, nil
}

func (s *fileState) SetState(_ context.Context, user int64, state updates.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var chans map[int64]int // the same account's kept supergroups stay (gotd sets it after each difference)
	if s.state != nil && s.state.User == user {
		chans = s.state.Channels
	} else {
		s.channels = map[int64]int{}
	}
	s.state = &savedState{User: user, State: state, Channels: chans}
	return s.save()
}

func (s *fileState) SetPts(_ context.Context, user int64, pts int) error {
	return s.change(user, func(st *updates.State) { st.Pts = pts })
}

func (s *fileState) SetQts(_ context.Context, user int64, qts int) error {
	return s.change(user, func(st *updates.State) { st.Qts = qts })
}

func (s *fileState) SetDate(_ context.Context, user int64, date int) error {
	return s.change(user, func(st *updates.State) { st.Date = date })
}

func (s *fileState) SetSeq(_ context.Context, user int64, seq int) error {
	return s.change(user, func(st *updates.State) { st.Seq = seq })
}

func (s *fileState) SetDateSeq(_ context.Context, user int64, date, seq int) error {
	return s.change(user, func(st *updates.State) { st.Date, st.Seq = date, seq })
}

func (s *fileState) GetChannelPts(_ context.Context, user, channel int64) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pts, ok := s.channels[channel]; ok {
		return pts, true, nil
	}
	if s.state != nil && s.state.User == user {
		pts, ok := s.state.Channels[channel]
		return pts, ok, nil
	}
	return 0, false, nil
}

func (s *fileState) SetChannelPts(_ context.Context, user, channel int64, pts int) error {
	s.mu.Lock()
	kept, known := s.kept[channel]
	s.mu.Unlock()
	if !known {
		kept = keptChat(-channelMark - channel) // asked once, outside the lock: it reads telegram.db
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kept[channel] = kept
	s.channels[channel] = pts
	if !kept || s.state == nil || s.state.User != user {
		return nil
	}
	if s.state.Channels == nil {
		s.state.Channels = map[int64]int{}
	}
	s.state.Channels[channel] = pts
	return s.save()
}

// ForEachChannels gives the kept supergroups' sequences (see above).
func (s *fileState) ForEachChannels(ctx context.Context, user int64, f func(context.Context, int64, int) error) error {
	s.mu.Lock()
	var chans map[int64]int
	if s.state != nil && s.state.User == user {
		chans = maps.Clone(s.state.Channels)
	}
	s.mu.Unlock()
	for id, pts := range chans {
		if err := f(ctx, id, pts); err != nil {
			return err
		}
	}
	return nil
}

// storedHashes are the channels' access hashes as telegram.db keeps them (gotd's own are in memory:
// after a start, a kept supergroup's sequence could not be asked for without one).
type storedHashes struct {
	mu     sync.Mutex
	hashes map[int64]int64
}

var _ updates.ChannelAccessHasher = (*storedHashes)(nil)

func (h *storedHashes) SetChannelAccessHash(_ context.Context, _, channel, hash int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hashes == nil {
		h.hashes = map[int64]int64{}
	}
	h.hashes[channel] = hash
	return nil
}

func (h *storedHashes) GetChannelAccessHash(_ context.Context, _, channel int64) (int64, bool, error) {
	h.mu.Lock()
	hash, ok := h.hashes[channel]
	h.mu.Unlock()
	if ok {
		return hash, true, nil
	}
	if p, ok := storedPeer(-channelMark - channel); ok {
		if c, ok := p.(*tg.InputPeerChannel); ok {
			return c.AccessHash, true, nil
		}
	}
	return 0, false, nil
}
