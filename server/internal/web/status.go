package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// The System Status page: request rates and latency (recorded here), vecdb load (from the
// counters vecdb publishes in shared memory) and ingest progress (from the status file the
// ingest service writes next to papers.jsonl).

const historyMinutes = 60

type second struct {
	unix     int64
	requests uint32
	feed     uint32
	errors   uint32 // 5xx
	limited  uint32 // 429
	feedUS   uint64 // total feed latency, microseconds
}

// metrics keeps one bucket per second for the last hour.
type metrics struct {
	mu      sync.Mutex
	started time.Time
	ring    [historyMinutes * 60]second
}

func (m *metrics) bucket(now int64) *second {
	b := &m.ring[now%int64(len(m.ring))]
	if b.unix != now {
		*b = second{unix: now}
	}
	return b
}

func (m *metrics) record(path string, status int, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.bucket(time.Now().Unix())
	b.requests++
	if path == "/api/feed" {
		b.feed++
		b.feedUS += uint64(d.Microseconds())
	}
	if status >= 500 {
		b.errors++
	} else if status == http.StatusTooManyRequests {
		b.limited++
	}
}

type window struct {
	Requests, Feed, Errors, Limited uint64
	FeedUS                          uint64
}

// sum adds up the buckets for the `secs` seconds before now (the current second is partial).
func (m *metrics) sum(now int64, secs int) window {
	m.mu.Lock()
	defer m.mu.Unlock()
	var w window
	for t := now - int64(secs); t < now; t++ {
		b := &m.ring[t%int64(len(m.ring))]
		if b.unix != t {
			continue
		}
		w.Requests += uint64(b.requests)
		w.Feed += uint64(b.feed)
		w.Errors += uint64(b.errors)
		w.Limited += uint64(b.limited)
		w.FeedUS += b.feedUS
	}
	return w
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (m *metrics) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/api/status" { // the status page polling itself is not traffic
			m.record(r.URL.Path, sw.status, time.Since(start))
		}
	})
}

// vecdbSampler turns vecdb's running totals into per-minute rates.
type vecdbSampler struct {
	mu      sync.Mutex
	samples []vecSample // one per 10 s, last hour
}

type vecSample struct {
	at      time.Time
	queries uint64
	busy    time.Duration
}

func (s *Server) sampleVecdb() {
	for range time.Tick(10 * time.Second) {
		st, ok := s.db.Stats()
		if !ok {
			continue
		}
		v := &s.vecStats
		v.mu.Lock()
		// vecdb restarted: counters reset, so start a new series.
		if n := len(v.samples); n > 0 && st.Queries < v.samples[n-1].queries {
			v.samples = v.samples[:0]
		}
		v.samples = append(v.samples, vecSample{time.Now(), st.Queries, st.Busy})
		if len(v.samples) > historyMinutes*6+1 {
			v.samples = v.samples[1:]
		}
		v.mu.Unlock()
	}
}

// rate over roughly the last `d`: queries/s and the share of time vecdb was scanning.
func (v *vecdbSampler) rate(d time.Duration) (qps, busy float64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := len(v.samples)
	if n < 2 {
		return 0, 0
	}
	last := v.samples[n-1]
	first := v.samples[0]
	for i := n - 2; i >= 0; i-- {
		first = v.samples[i]
		if last.at.Sub(first.at) >= d {
			break
		}
	}
	dt := last.at.Sub(first.at).Seconds()
	if dt <= 0 {
		return 0, 0
	}
	return float64(last.queries-first.queries) / dt, (last.busy - first.busy).Seconds() / dt
}

// GET /api/status
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	sn := s.snap()
	m := &s.metrics
	minute := m.sum(now.Unix(), 60)

	// Per-minute series for the chart, oldest first.
	type point struct {
		T        int64   `json:"t"` // unix seconds, start of the minute
		Requests float64 `json:"requests"`
		Feed     float64 `json:"feed"`
	}
	series := make([]point, 0, historyMinutes)
	end := now.Unix() - now.Unix()%60
	for i := historyMinutes; i >= 1; i-- {
		t := end - int64(i*60)
		wd := m.sum(t+60, 60)
		series = append(series, point{T: t, Requests: float64(wd.Requests) / 60, Feed: float64(wd.Feed) / 60})
	}

	avgFeed := 0.0
	if minute.Feed > 0 {
		avgFeed = float64(minute.FeedUS) / float64(minute.Feed) / 1000
	}
	vs, alive := s.db.Stats()
	qps, busy := s.vecStats.rate(time.Minute)

	indexInfo := map[string]any{"papers": sn.st.N, "buildId": fmt.Sprintf("%016x", sn.st.BuildID)}
	if fi, err := os.Stat(s.cfg.IndexPath); err == nil {
		indexInfo["builtAt"] = fi.ModTime().UTC().Format(time.RFC3339)
		indexInfo["bytes"] = fi.Size()
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	out := map[string]any{
		"now":    now.UTC().Format(time.RFC3339),
		"uptime": int(now.Sub(m.started).Seconds()),
		"traffic": map[string]any{
			"requestsPerSec": float64(minute.Requests) / 60,
			"feedPerSec":     float64(minute.Feed) / 60,
			"feedLatencyMs":  avgFeed,
			"errorsPerMin":   minute.Errors,
			"limitedPerMin":  minute.Limited,
			"series":         series,
		},
		"index": indexInfo,
		"vecdb": map[string]any{
			"alive":          alive,
			"queriesPerSec":  qps,
			"busy":           busy,
			"totalQueries":   vs.Queries,
			"nprobe":         vs.NProbe,
			"clusters":       vs.NList,
			"papers":         vs.IndexN,
			"sameBuild":      alive && vs.BuildID == sn.st.BuildID,
			"startedAt":      vs.Started.UTC().Format(time.RFC3339),
			"secondsRunning": int(now.Sub(vs.Started).Seconds()),
		},
		"server":  map[string]any{"memoryMB": ms.Sys >> 20, "goroutines": runtime.NumGoroutine()},
		"pending": s.pending.count(),
		"ingest":  s.ingestStatus(),
	}
	writeJSON(w, out)
}

// ingestStatus is the JSON the ingest service writes to ingest-status.json, or nil.
func (s *Server) ingestStatus() any {
	path := filepath.Join(filepath.Dir(s.cfg.PapersPath), "ingest-status.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var v map[string]any
	if json.NewDecoder(strings.NewReader(string(b))).Decode(&v) != nil {
		return nil
	}
	return v
}
