package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const firstID int64 = 100000

// IDs keeps the numbers players know stable across syncs, so favorites and resume points survive.
type IDs struct {
	mu       sync.Mutex
	path     string
	dirty    bool
	Next     int64            `json:"next"`
	Keys     map[string]int64 `json:"keys"`
	Aliases  map[int64]int64  `json:"aliases"`
	Episodes map[int64]int64  `json:"episodes"`
}

func LoadIDs(path string) (*IDs, error) {
	ids := &IDs{path: path, Next: firstID, Keys: map[string]int64{}, Aliases: map[int64]int64{}, Episodes: map[int64]int64{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ids, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, ids); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return ids, nil
}

func (s *IDs) Assign(key string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.assignLocked(key)
}

func (s *IDs) assignLocked(key string) int64 {
	if id, found := s.Keys[key]; found {
		return id
	}
	s.Next++
	s.Keys[key] = s.Next
	s.dirty = true
	return s.Next
}

func (s *IDs) AssignEpisode(series int64, season, episode int) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.assignLocked(fmt.Sprintf("episode|%d|%d|%d", series, season, episode))
	if s.Episodes[id] != series {
		s.Episodes[id] = series
		s.dirty = true
	}
	return id
}

func (s *IDs) EpisodeSeries(id int64) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	series, found := s.Episodes[id]
	return series, found
}

// Resolve follows aliases left behind when titles merged. The bound protects against a corrupted cycle.
func (s *IDs) Resolve(id int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for range len(s.Aliases) {
		next, found := s.Aliases[id]
		if !found {
			return id
		}
		id = next
	}
	return id
}

// GroupID returns the ID for a group of keys. An ID absorbed into another stays as an alias so old favorites still resolve.
func (s *IDs) GroupID(keys []string, used map[int64]bool) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var chosen int64
	for _, key := range keys {
		if id, found := s.Keys[key]; found && !used[id] && (chosen == 0 || id < chosen) {
			chosen = id
		}
	}
	if chosen == 0 {
		s.Next++
		chosen = s.Next
		s.dirty = true
	}
	for _, key := range keys {
		previous, found := s.Keys[key]
		if found && previous == chosen {
			continue
		}
		if found && !used[previous] {
			s.Aliases[previous] = chosen
		}
		s.Keys[key] = chosen
		s.dirty = true
	}
	used[chosen] = true
	return chosen
}

func (s *IDs) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := writeAtomic(s.path, raw); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

func writeAtomic(path string, data []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
