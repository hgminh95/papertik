package web

import (
	"encoding/base64"
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDecodePref(t *testing.T) {
	raw := make([]byte, 16)
	for i, x := range []float32{3, 0, 4, 0} {
		binary.LittleEndian.PutUint32(raw[4*i:], math.Float32bits(x))
	}
	v, ok := decodePref(base64.StdEncoding.EncodeToString(raw), 4)
	if !ok || math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[2])-0.8) > 1e-6 {
		t.Fatalf("got %v %v", v, ok)
	}
	if v, ok := decodePref("", 4); !ok || v != nil {
		t.Fatal("empty pref should mean cold start")
	}
	if _, ok := decodePref(base64.StdEncoding.EncodeToString(raw[:12]), 4); ok {
		t.Fatal("wrong length accepted")
	}
	binary.LittleEndian.PutUint32(raw, math.Float32bits(float32(math.NaN())))
	if _, ok := decodePref(base64.StdEncoding.EncodeToString(raw), 4); ok {
		t.Fatal("NaN accepted")
	}
}

func TestSessions(t *testing.T) {
	s := sessions{key: []byte("0123456789abcdef")}
	rec := httptest.NewRecorder()
	s.issue(rec)
	req := httptest.NewRequest("POST", "/api/feed", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	if !s.valid(req) {
		t.Fatal("fresh session rejected")
	}
	other := sessions{key: []byte("fedcba9876543210")}
	if other.valid(req) {
		t.Fatal("session signed with another key accepted")
	}
	forged := httptest.NewRequest("POST", "/api/feed", nil)
	forged.AddCookie(&http.Cookie{Name: sessionCookie, Value: base64.RawURLEncoding.EncodeToString(make([]byte, 40))})
	if s.valid(forged) {
		t.Fatal("forged session accepted")
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("CF-Connecting-IP", "203.0.113.9")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Fatalf("behind tunnel: %s", got)
	}
	r.RemoteAddr = "198.51.100.7:5555" // direct hit: header is not trusted
	if got := clientIP(r); got != "198.51.100.7" {
		t.Fatalf("direct: %s", got)
	}
}

func TestLimiter(t *testing.T) {
	l := &limiter{rate: 1, burst: 3, buckets: map[string]*bucket{}}
	for i := 0; i < 3; i++ {
		if !l.allow("a") {
			t.Fatalf("request %d within burst denied", i)
		}
	}
	if l.allow("a") {
		t.Fatal("burst exceeded but allowed")
	}
	if !l.allow("b") {
		t.Fatal("other client limited")
	}
}

func TestSitemapChunkWithinProtocolLimit(t *testing.T) {
	if sitemapChunk+1 > 50_000 { // +1: the home page in the first file
		t.Fatalf("sitemap files would hold %d URLs; the limit is 50,000", sitemapChunk+1)
	}
}
