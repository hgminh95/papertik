package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// clientIP returns the visitor's address. Behind cloudflared the TCP peer is the local
// tunnel, so CF-Connecting-IP is trusted only when the peer is loopback.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
			return cf
		}
	}
	return host
}

// ---- rate limiting: per-IP token bucket ----

type bucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*bucket
}

func newLimiter(rate, burst float64) *limiter {
	l := &limiter{rate: rate, burst: burst, buckets: map[string]*bucket{}}
	go func() {
		for range time.Tick(time.Minute) {
			l.mu.Lock()
			for k, b := range l.buckets {
				if time.Since(b.last) > 10*time.Minute {
					delete(l.buckets, k)
				}
			}
			l.mu.Unlock()
		}
	}()
	return l
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *limiter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			httpError(w, http.StatusTooManyRequests, "slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- sessions: HMAC-signed expiry, issued after a Turnstile check ----

const sessionCookie = "pt_session"
const sessionTTL = 24 * time.Hour

type sessions struct{ key []byte }

func (s sessions) issue(w http.ResponseWriter) {
	exp := make([]byte, 8)
	binary.BigEndian.PutUint64(exp, uint64(time.Now().Add(sessionTTL).Unix()))
	mac := hmac.New(sha256.New, s.key)
	mac.Write(exp)
	val := base64.RawURLEncoding.EncodeToString(append(exp, mac.Sum(nil)...))
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: val, Path: "/api/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

func (s sessions) valid(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != 8+sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(raw[:8])
	if !hmac.Equal(mac.Sum(nil), raw[8:]) {
		return false
	}
	return time.Now().Unix() < int64(binary.BigEndian.Uint64(raw[:8]))
}

// ---- Cloudflare Turnstile ----

const siteverifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

func verifyTurnstile(ctx context.Context, secret, token, ip string) error {
	if token == "" || len(token) > 2048 {
		return fmt.Errorf("missing token")
	}
	form := url.Values{"secret": {secret}, "response": {token}, "remoteip": {ip}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, siteverifyURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Success    bool     `json:"success"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("turnstile rejected: %v", out.ErrorCodes)
	}
	return nil
}
