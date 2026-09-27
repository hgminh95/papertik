package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Network errors must not leak the API key (it travels in the query string) into logs.
func TestSearcherErrorsHideAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()
	s := newSearcher("", "secret-key-123")
	s.client.Timeout = 20 * time.Millisecond
	err := s.get(context.Background(), srv.URL+"/works?api_key=secret-key-123", &struct{}{})
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if strings.Contains(err.Error(), "secret-key-123") {
		t.Fatalf("error leaks the key: %v", err)
	}
}
