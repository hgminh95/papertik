// Package web is the HTTP layer: JSON API, bot protection and the static SPA.
package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	PapersPath       string // papers.jsonl, scanned once in the background for the Discover lists
	PublicURL        string // e.g. https://papertok.example.com, for canonical links and sitemaps (default: from the request)
}

type Server struct {
	cfg      Config
	store    *store.Store
	db       *shm.Client
	rec      *feed.Recommender
	sessions sessions
	search   *searcher
	titles   titleIndex // built on first local search
	pending  *pendingLog
	explore  explorer
	index    indexHTML
}

func New(cfg Config, s *store.Store, db *shm.Client, rec *feed.Recommender) (*Server, error) {
	pending, err := openPending(cfg.PendingPath)
	if err != nil {
		return nil, err
	}
	srv := &Server{
		cfg: cfg, store: s, db: db, rec: rec,
		sessions: sessions{key: cfg.SessionKey},
		search:   newSearcher(cfg.OpenAlexMailto, cfg.OpenAlexAPIKey),
		pending:  pending,
	}
	if cfg.PapersPath != "" {
		go srv.explore.build(cfg.PapersPath, s.Row)
	}
	return srv, nil
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
	api.HandleFunc("GET /api/healthz", s.handleHealth)
	limited := newLimiter(5, 20).wrap(api)

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHeaders(limited))
	mux.HandleFunc("GET /p/{id}", s.handlePaperPage)
	mux.HandleFunc("GET /robots.txt", s.handleRobots)
	mux.HandleFunc("GET /sitemap.xml", s.handleSitemapIndex)
	mux.HandleFunc("GET /sitemaps/{file}", s.handleSitemap)
	mux.Handle("/", s.static())
	return securityHeaders(mux)
}

// ---- handlers ----

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"turnstileSiteKey": s.turnstileSiteKey(), "dim": s.store.Dim})
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
	var req feedRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "bad json")
		return
	}
	k := req.K
	if k <= 0 || k > 20 {
		k = 8
	}
	pref, ok := decodePref(req.Pref, s.store.Dim)
	if !ok {
		httpError(w, http.StatusBadRequest, "bad pref vector")
		return
	}
	seen := make([]uint32, 0, len(req.Seen))
	for _, id := range req.Seen {
		if row, ok := s.store.Row(id); ok {
			seen = append(seen, row)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var items []feed.Item
	if req.Mode == "top" {
		if pref == nil {
			s.writePapers(w, nil, "")
			return
		}
		items = s.rec.Top(ctx, pref, seen, k)
	} else {
		items = s.rec.Next(ctx, pref, seen, k)
	}

	rows := make([]rowOut, len(items))
	for i, it := range items {
		rows[i] = rowOut{it.Row, it.Score, it.Reason}
	}
	s.writePapers(w, rows, "")
}

func (s *Server) vector(row uint32) (string, float32) {
	scale, q := s.store.Vector(row)
	return base64.StdEncoding.EncodeToString(int8Bytes(q)), scale
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	alive := s.db.Alive()
	if !alive {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	writeJSON(w, map[string]any{"papers": s.store.N, "vecdb": alive, "pending": s.pending.count()})
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
			// SPA fallback. Open Graph wants absolute URLs, so fill in the origin.
			page, err := s.index.get(filepath.Join(dir, "index.html"))
			if err != nil {
				http.Error(w, "not built", http.StatusInternalServerError)
				return
			}
			base := s.baseURL(r)
			page = bytes.Replace(page, []byte(`content="/og-image.png"`), []byte(`content="`+base+`/og-image.png"`), 1)
			page = bytes.Replace(page, []byte("<!--/seo-->"), []byte(`<link rel="canonical" href="`+base+`/" />`), 1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Write(page)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			// Vite emits content-hashed names; let Cloudflare cache them forever.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
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

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' https://challenges.cloudflare.com; "+
				"frame-src https://challenges.cloudflare.com; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
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
