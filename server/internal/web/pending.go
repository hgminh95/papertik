package web

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"sync"
	"time"
)

// pendingLog records papers people interacted with that are not in the index yet
// (liked, bookmarked, opened), so the ingest pipeline can fetch and embed them later:
//
//	uv run ingest/fetch.py pending data/pending.jsonl
//
// One JSON object per line; each id is written once.
type pendingLog struct {
	mu   sync.Mutex
	path string
	seen map[string]bool
	f    *os.File
}

const maxPending = 200_000

var workID = regexp.MustCompile(`^W\d{1,12}$`)

func openPending(path string) (*pendingLog, error) {
	p := &pendingLog{path: path, seen: map[string]bool{}}
	if path == "" {
		return p, nil
	}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var e struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID != "" {
				p.seen[e.ID] = true
			}
		}
		f.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	p.f = f
	return p, nil
}

// add records id (an OpenAlex work id like "W123") once. It reports whether it was new.
func (p *pendingLog) add(id, reason string) bool {
	if !workID.MatchString(id) {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen[id] || len(p.seen) >= maxPending || p.f == nil {
		return false
	}
	p.seen[id] = true
	line, _ := json.Marshal(map[string]any{"id": id, "reason": reason, "ts": time.Now().UTC().Format(time.RFC3339)})
	p.f.Write(append(line, '\n'))
	return true
}

func (p *pendingLog) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.seen)
}
