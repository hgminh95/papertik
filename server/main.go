// Command server is the PaperTok HTTP server.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"papertok/server/internal/feed"
	"papertok/server/internal/shm"
	"papertok/server/internal/web"
)

func defaultShmPath() string {
	if runtime.GOOS == "linux" {
		return "/dev/shm/papertok"
	}
	return "/tmp/papertok.shm"
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address (keep on loopback behind cloudflared)")
	index := flag.String("index", "data/index.bin", "index built by `vecdb build`")
	papers := flag.String("papers", "data/papers.jsonl", "paper metadata")
	shmPath := flag.String("shm", defaultShmPath(), "vecdb shared-memory file")
	static := flag.String("static", "web/dist", "built SPA")
	pprofAddr := flag.String("pprof", "", "serve net/http/pprof on this address (e.g. 127.0.0.1:6060); off by default")
	pending := flag.String("pending", "", "log of papers to ingest later (default: pending.jsonl next to -papers)")
	flag.Parse()

	sessionKey, err := hex.DecodeString(os.Getenv("SESSION_KEY"))
	if err != nil || len(sessionKey) < 16 {
		sessionKey = make([]byte, 32)
		rand.Read(sessionKey)
		if os.Getenv("TURNSTILE_SECRET") != "" {
			log.Printf("SESSION_KEY unset or invalid: using a random key (sessions reset on restart)")
		}
	}
	if *pending == "" {
		*pending = filepath.Join(filepath.Dir(*papers), "pending.jsonl")
	}
	cfg := web.Config{
		StaticDir:        *static,
		TurnstileSiteKey: os.Getenv("TURNSTILE_SITEKEY"),
		TurnstileSecret:  os.Getenv("TURNSTILE_SECRET"),
		SessionKey:       sessionKey,
		OpenAlexMailto:   os.Getenv("OPENALEX_MAILTO"),
		OpenAlexAPIKey:   os.Getenv("OPENALEX_API_KEY"),
		PendingPath:      *pending,
		IndexPath:        *index,
		PapersPath:       *papers,
		PublicURL:        os.Getenv("PUBLIC_URL"),
	}
	if cfg.TurnstileSecret == "" {
		log.Printf("TURNSTILE_SECRET unset: bot verification disabled")
	}

	db := shm.NewClient(*shmPath)
	if !db.Alive() {
		log.Printf("vecdb not reachable at %s yet; serving random feed until it is", *shmPath)
	}
	// On a fresh install the ingest service builds the first index a few minutes after it starts.
	for waited := false; ; waited = true {
		if _, err := os.Stat(*index); err == nil {
			break
		}
		if !waited {
			log.Printf("waiting for %s (the ingest service builds it)", *index)
		}
		time.Sleep(5 * time.Second)
	}
	rec := feed.New(feed.DefaultConfig, db)
	app, err := web.New(cfg, db, rec)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	log.Printf("loaded %d papers (dim %d)", app.Store().N, app.Store().Dim)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	if *pprofAddr != "" {
		go func() { log.Println(http.ListenAndServe(*pprofAddr, nil)) }() // DefaultServeMux has the pprof handlers
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("listening on http://%s", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}
