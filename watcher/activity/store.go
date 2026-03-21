package activity

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const activityDir = "watcher-activity"
const activityFile = "activity.jsonl"

// Entry is a single activity record from Alchemy webhook.
type Entry struct {
	Timestamp   time.Time `json:"timestamp"`
	Network     string    `json:"network"`
	Hash        string    `json:"hash"`
	FromAddress string    `json:"from_address"`
	ToAddress   string    `json:"to_address"`
	Asset       string    `json:"asset"`
	Value       float64   `json:"value"`
	Category    string    `json:"category"` // external, internal, erc20, erc721, erc1155, token
}

// Store persists activity from Alchemy webhooks.
type Store struct {
	mu   sync.RWMutex
	dir  string
	path string
}

// NewStore creates an activity store. dir is the data root; if empty, uses activityDir.
func NewStore(dir string) *Store {
	if dir == "" {
		dir = activityDir
	} else {
		dir = filepath.Join(dir, activityDir)
	}
	return &Store{
		dir:  dir,
		path: filepath.Join(dir, activityFile),
	}
}

// Add appends an entry.
func (s *Store) Add(e *Entry) error {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(e); err != nil {
		return err
	}
	return f.Sync()
}

// List returns the last N entries, newest first.
func (s *Store) List(limit int) ([]Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var entries []Entry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}
