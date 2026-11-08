package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Kind string

const (
	Stalled      Kind = "stalled"
	Slow         Kind = "slow"
	Dropped      Kind = "dropped"
	DeadLink     Kind = "dead_link"
	Rejected     Kind = "rejected"
	Parked       Kind = "parked"
	Recovered    Kind = "recovered"
	SwitchedCopy Kind = "switched_copy"
	GaveUp       Kind = "gave_up"
)

type Event struct {
	Time       time.Time `json:"time"`
	Kind       Kind      `json:"kind"`
	User       string    `json:"user"`
	Player     string    `json:"player,omitempty"`
	Media      string    `json:"media"`
	TitleID    int64     `json:"title_id"`
	Title      string    `json:"title"`
	Account    string    `json:"account,omitempty"`
	Position   int64     `json:"position,omitempty"`
	Buffered   int64     `json:"buffered,omitempty"`
	DowntimeMS int64     `json:"downtime_ms,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

const (
	dayLayout     = "2006-01-02"
	filePrefix    = "events-"
	fileSuffix    = ".jsonl"
	filePerm      = 0o600
	directoryPerm = 0o700
)

// Journal appends playback events to one file per day and removes files past their retention.
type Journal struct {
	dir       string
	retention int

	mu   sync.Mutex
	day  string
	file *os.File
}

func Open(dir string, retentionDays int) (*Journal, error) {
	if err := os.MkdirAll(dir, directoryPerm); err != nil {
		return nil, err
	}
	return &Journal{dir: dir, retention: retentionDays}, nil
}

func (j *Journal) Record(event Event) {
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	slog.Info("playback event", "kind", event.Kind, "account", event.Account, "title", event.Title, "detail", event.Detail)
	line, err := json.Marshal(event)
	if err != nil {
		slog.Error("encoding event", "error", err)
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.rotate(event.Time); err != nil {
		slog.Error("opening event journal", "error", err)
		return
	}
	if _, err := j.file.Write(append(line, '\n')); err != nil {
		slog.Error("writing event journal", "error", err)
	}
}

func (j *Journal) rotate(now time.Time) error {
	day := now.Format(dayLayout)
	if j.file != nil && day == j.day {
		return nil
	}
	if j.file != nil {
		j.file.Close()
	}
	file, err := os.OpenFile(filepath.Join(j.dir, filePrefix+day+fileSuffix), os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		j.file = nil
		return err
	}
	j.file, j.day = file, day
	j.prune(now)
	return nil
}

func (j *Journal) prune(now time.Time) {
	oldest := now.AddDate(0, 0, -j.retention).Format(dayLayout)
	files, err := filepath.Glob(filepath.Join(j.dir, filePrefix+"*"+fileSuffix))
	if err != nil {
		return
	}
	for _, path := range files {
		if dayOf(path) < oldest {
			os.Remove(path)
		}
	}
}

func (j *Journal) Close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file != nil {
		j.file.Close()
		j.file = nil
	}
}

func dayOf(path string) string {
	return strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), filePrefix), fileSuffix)
}

// Read returns the events recorded since the given time, oldest first.
func Read(dir string, since time.Time) ([]Event, error) {
	files, err := filepath.Glob(filepath.Join(dir, filePrefix+"*"+fileSuffix))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []Event
	firstDay := since.Format(dayLayout)
	for _, path := range files {
		if dayOf(path) < firstDay {
			continue
		}
		read, err := readFile(path, since)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		out = append(out, read...)
	}
	return out, nil
}

func readFile(path string, since time.Time) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var out []Event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if !event.Time.Before(since) {
			out = append(out, event)
		}
	}
	return out, scanner.Err()
}
