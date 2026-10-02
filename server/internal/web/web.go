// Package web is the HTTP layer: JSON API, bot protection and the static SPA.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"papertok/server/internal/feed"
	"papertok/server/internal/shm"
	"papertok/server/internal/store"
)

type Config struct {
	StaticDir        string
	TurnstileSiteKey string
	TurnstileSecret  string // empty disables bot checks (dev)
	SessionKey       []byte
	OpenAlexMailto   string // optional, for the OpenAlex polite pool
	OpenAlexAPIKey   string // optional, raises OpenAlex rate limits
	PendingPath      string // where to log papers to ingest later (empty = don't log)
	IndexPath        string // index.bin; watched, and reloaded when a new build replaces it
	TaxonomyPath     string // ingest/taxonomy.json: OpenAlex topic -> PaperTik category (papers fetched live)
	PapersPath       string // papers.jsonl (append-only; the index points into it)
	FacebookAppID    string // optional fb:app_id for Facebook link previews / Insights
	PublicURL        string // e.g. https://papertik.app, for canonical links and sitemaps (default: from the request)
}

type Server struct {
	cfg      Config
	cur      atomic.Pointer[snapshot]
	db       *shm.Client
	rec      *feed.Recommender
	sessions sessions
	search   *searcher // OpenAlex client (single-paper lookups)
	fts      *ftsIndex // local full-text search
	pending  *pendingLog
	index    indexHTML
	metrics  metrics
	vecStats vecdbSampler
}

// snapshot is everything derived from one build of the index. Requests take one snapshot at
// the start and use it throughout, so a reload in the middle of a request cannot mix row
// numbers from two builds.
type snapshot struct {
	st      *store.Store
	explore *explorer
	stamp   fileStamp
}

func (s *Server) snap() *snapshot { return s.cur.Load() }

// Store returns the index currently being served.
func (s *Server) Store() *store.Store { return s.snap().st }

func New(cfg Config, db *shm.Client, rec *feed.Recommender) (*Server, error) {
	pending, err := openPending(cfg.PendingPath)
	if err != nil {
		return nil, err
	}
	srv := &Server{
		cfg: cfg, db: db, rec: rec,
		sessions: sessions{key: cfg.SessionKey},
		search:   newSearcher(cfg.OpenAlexMailto, cfg.OpenAlexAPIKey, loadTaxonomy(cfg.TaxonomyPath)),
		pending:  pending,
		fts:      &ftsIndex{path: filepath.Join(filepath.Dir(cfg.PapersPath), "search.db"), cache: map[string]ftsResult{}},
	}
	sn, err := srv.load()
	if err != nil {
		return nil, err
	}
	srv.cur.Store(sn)
	go sn.explore.build(cfg.PapersPath, sn.st.Row) // serve right away; Discover waits for it
	srv.metrics.started = time.Now()
	go srv.watch()
	go srv.sampleVecdb()
	return srv, nil
}

type fileStamp struct {
	size  int64
	mtime time.Time
}

func stampOf(path string) (fileStamp, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{fi.Size(), fi.ModTime()}, nil
}

func (s *Server) load() (*snapshot, error) {
	stamp, err := stampOf(s.cfg.IndexPath)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(s.cfg.IndexPath, s.cfg.PapersPath)
	if err != nil {
		return nil, err
	}
	return &snapshot{st: st, explore: &explorer{}, stamp: stamp}, nil
}

// watch reloads the index when a new build replaces index.bin (the ingest service writes a
// new file and renames it into place). The new snapshot is fully prepared, Discover lists
// included, before it is swapped in; the old one is unmapped once in-flight requests are done.
func (s *Server) watch() {
	for range time.Tick(5 * time.Second) {
		old := s.snap()
		stamp, err := stampOf(s.cfg.IndexPath)
		if err != nil || stamp == old.stamp {
			continue
		}
		sn, err := s.load()
		if err != nil {
			log.Printf("reload: %v (keeping the current index)", err)
			continue
		}
		if sn.st.BuildID == old.st.BuildID {
			old.stamp = sn.stamp // touched, not rebuilt
			sn.st.Close()
			continue
		}
		sn.explore.build(s.cfg.PapersPath, sn.st.Row)
		// Switch once vecdb serves the new build too (it checks every 2 s), so feed requests
		// never ask it for a build it has not loaded. vecdb keeps the previous build loaded for
		// a while, so requests still in flight on the old snapshot are answered as well.
		for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
			if vs, ok := s.db.Stats(); !ok || vs.BuildID == sn.st.BuildID {
				break
			}
		}
		s.cur.Store(sn)
		log.Printf("reload: now serving %d papers (was %d)", sn.st.N, old.st.N)
		time.AfterFunc(time.Minute, func() { old.st.Close() })
	}
}

func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/config", s.handleConfig)
	api.HandleFunc("POST /api/session", s.handleSession)
	api.Handle("POST /api/feed", s.requireSession(http.HandlerFunc(s.handleFeed)))
	api.Handle("GET /api/search", s.requireSession(http.HandlerFunc(s.handleSearch)))
	api.Handle("GET /api/explore", s.requireSession(http.HandlerFunc(s.handleExplore)))
	api.Handle("GET /api/explore/papers", s.requireSession(http.HandlerFunc(s.handleExplorePapers)))
	api.Handle("GET /api/paper/{id}", s.requireSession(http.HandlerFunc(s.handlePaper)))
	api.Handle("POST /api/pending", s.requireSession(http.HandlerFunc(s.handlePending)))
	api.Handle("POST /api/vectors", s.requireSession(http.HandlerFunc(s.handleVectors)))
	api.Handle("GET /api/filters/options", s.requireSession(http.HandlerFunc(s.handleFilterOptions)))
	api.Handle("POST /api/filters/count", s.requireSession(http.HandlerFunc(s.handleFilterCount)))
	api.HandleFunc("GET /api/healthz", s.handleHealth)
	api.HandleFunc("GET /api/status", s.handleStatus)
	limited := newLimiter(5, 20).wrap(api)

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHeaders(limited))
	mux.HandleFunc("GET /p/{id}", s.handlePaperPage)
	mux.HandleFunc("GET /robots.txt", s.handleRobots)
	mux.HandleFunc("GET /sitemap.xml", s.handleSitemapIndex)
	mux.HandleFunc("GET /sitemaps/{file}", s.handleSitemap)
	mux.Handle("/", s.static())
	return s.metrics.wrap(s.canonicalHost(securityHeaders(mux)))
}

// ---- handlers ----

func (s *Server) config() map[string]any {
	return map[string]any{"turnstileSiteKey": s.turnstileSiteKey(), "dim": s.snap().st.Dim}
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.config())
}

func (s *Server) turnstileSiteKey() string {
	if s.cfg.TurnstileSecret == "" {
		return ""
	}
	return s.cfg.TurnstileSiteKey
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if s.cfg.TurnstileSecret == "" {
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := verifyTurnstile(r.Context(), s.cfg.TurnstileSecret, body.Token, clientIP(r)); err != nil {
		log.Printf("turnstile: %s: %v", clientIP(r), err)
		httpError(w, http.StatusForbidden, "verification failed")
		return
	}
	s.sessions.issue(w)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.TurnstileSecret != "" && !s.sessions.valid(r) {
			httpError(w, http.StatusUnauthorized, "session required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type feedRequest struct {
	Pref string   `json:"pref"` // base64 little-endian f32[dim]; empty = cold start
	Seen []string `json:"seen"` // OpenAlex ids, oldest first
	K    int      `json:"k"`
	Mode string   `json:"mode"` // "" = feed (sampled + exploration), "top" = exact nearest neighbours
	// Feed only ("top" ignores it): which papers may be shown. See filters.go.
	Filter *filterRequest `json:"filter"`
}

type feedPaper struct {
	store.Paper
	Vec     string  `json:"vec,omitempty"`   // base64 int8[dim]
	Scale   float32 `json:"scale,omitempty"` // v ≈ scale * vec
	Score   float32 `json:"score,omitempty"`
	Reason  string  `json:"reason"`
	Indexed bool    `json:"indexed"` // has a vector, so it can be liked
}

func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	st := s.snap().st
	var req feedRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "bad json")
		return
	}
	k := req.K
	if k <= 0 || k > 20 {
		k = 8
	}
	pref, ok := decodePref(req.Pref, st.Dim)
	if !ok {
		httpError(w, http.StatusBadRequest, "bad pref vector")
		return
	}
	seen := make([]uint32, 0, len(req.Seen))
	for _, id := range req.Seen {
		if row, ok := st.Row(id); ok {
			seen = append(seen, row)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var items []feed.Item
	if req.Mode == "top" {
		if pref == nil {
			s.writePapers(w, st, nil, "")
			return
		}
		items = s.rec.Top(ctx, st, pref, seen, k)
	} else {
		items = s.rec.Next(ctx, st, pref, seen, k, req.Filter.toFilter())
	}

	rows := make([]rowOut, len(items))
	for i, it := range items {
		rows[i] = rowOut{it.Row, it.Score, it.Reason}
	}
	s.writePapers(w, st, rows, "")
}

func (s *Server) vector(st *store.Store, row uint32) (string, float32) {
	scale, q := st.Vector(row)
	return base64.StdEncoding.EncodeToString(int8Bytes(q)), scale
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	st := s.snap().st
	alive := s.db.Alive()
	if !alive {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	writeJSON(w, map[string]any{"papers": st.N, "vecdb": alive, "pending": s.pending.count()})
}

// decodePref returns the L2-normalised preference vector, nil for "none", ok=false if malformed.
func decodePref(b64 string, dim int) ([]float32, bool) {
	if b64 == "" {
		return nil, true
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != 4*dim {
		return nil, false
	}
	v := make([]float32, dim)
	var norm float64
	for i := range v {
		x := math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, false
		}
		v[i] = x
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return nil, true
	}
	inv := float32(1 / math.Sqrt(norm))
	for i := range v {
		v[i] *= inv
	}
	return v, true
}

// int8Bytes views the vector as bytes without copying (same memory, same bits).
func int8Bytes(q []int8) []byte {
	if len(q) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&q[0])), len(q))
}

// ---- static SPA ----

func (s *Server) static() http.Handler {
	dir := s.cfg.StaticDir
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() || r.URL.Path == "/index.html" {
			s.renderShell(w, r) // an app page: per-route title, canonical URL, Open Graph tags
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			// Vite emits content-hashed names; let Cloudflare cache them forever.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case strings.HasSuffix(r.URL.Path, ".webmanifest"):
			// Go doesn't know this type (it would send text/plain).
			w.Header().Set("Content-Type", "application/manifest+json")
			w.Header().Set("Cache-Control", "public, max-age=86400")
		default: // icons, og-image: stable names, so a day at most
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		files.ServeHTTP(w, r)
	})
}

// ---- helpers ----

func apiHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type nonceKey struct{}

// securityHeaders sets the CSP. Cloudflare injects two scripts into our pages: an inline bot
// detection loader (different on every response, so it can't be allowed by hash) and the Web
// Analytics beacon. Both take the nonce from this header, so each response gets a fresh one;
// 'strict-dynamic' then lets nonced scripts load what they need (the Turnstile and
// /cdn-cgi/ scripts). The host list is only a fallback for browsers without 'strict-dynamic'.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [16]byte
		rand.Read(b[:])
		nonce := base64.StdEncoding.EncodeToString(b[:])
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'nonce-"+nonce+"' 'strict-dynamic' 'self' https://challenges.cloudflare.com "+
				"https://static.cloudflareinsights.com; frame-src https://challenges.cloudflare.com; "+
				"style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' https://cloudflareinsights.com")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nonceKey{}, nonce)))
	})
}

// preparePage gives the page's own script tags this response's CSP nonce, and embeds the
// client config (saving the app a round trip to /api/config before it can start). page is not
// modified.
func (s *Server) preparePage(page []byte, r *http.Request) []byte {
	if nonce, _ := r.Context().Value(nonceKey{}).(string); nonce != "" {
		page = bytes.ReplaceAll(page, []byte(`<script type="module"`), []byte(`<script nonce="`+nonce+`" type="module"`))
	}
	cfg, _ := json.Marshal(s.config()) // escapes <, > and &, so it cannot close the script tag
	return bytes.Replace(page, []byte("</head>"),
		append(append([]byte(`<script type="application/json" id="config">`), cfg...), "</script>\n  </head>"...), 1)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
