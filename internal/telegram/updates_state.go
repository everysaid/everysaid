// Where the live connection's updates stood (gotd's updates.StateStorage), kept in a small file
// beside the store, so that after a restart Telegram is asked for what happened meanwhile (edits,
// reads, new chats, as well as new messages) rather than starting again from now.
//
// Only the account's common sequence is kept (private chats and small groups). The channels' own
// sequences, supergroups' included, stay in memory: kept, each would be asked for on every start,
// one request per channel, broadcast channels too; what a supergroup missed comes from the catch-up.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/gotd/td/telegram/updates"
)

// StatePath is the file of the updates' state.
func StatePath() string { return filepath.Join(Folder(), "updates-state.json") }

type savedState struct {
	User  int64         `json:"user"`
	State updates.State `json:"state"`
}

// fileState keeps the common state in path, the channels' in memory.
type fileState struct {
	path     string
	mu       sync.Mutex
	state    *savedState
	channels map[int64]int
}

var _ updates.StateStorage = (*fileState)(nil)

func newFileState(path string) *fileState {
	s := &fileState{path: path, channels: map[int64]int{}}
	if b, err := os.ReadFile(path); err == nil {
		var saved savedState
		if json.Unmarshal(b, &saved) == nil && saved.User != 0 {
			s.state = &saved
		}
	}
	return s
}

var errNoState = errors.New("no updates state")

// save writes the state through a temporary file, so that a crash never leaves half of it.
func (s *fileState) save() error {
	b, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
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
	s.state = &savedState{User: user, State: state}
	s.channels = map[int64]int{}
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
	pts, ok := s.channels[channel]
	return pts, ok, nil
}

func (s *fileState) SetChannelPts(_ context.Context, user, channel int64, pts int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels[channel] = pts
	return nil
}

// ForEachChannels gives none: the channels' sequences are not kept across starts (see above).
func (s *fileState) ForEachChannels(context.Context, int64, func(context.Context, int64, int) error) error {
	return nil
}
