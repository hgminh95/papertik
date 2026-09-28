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
	s := newSearcher("", "secret-key-123", nil)
	s.client.Timeout = 20 * time.Millisecond
	err := s.get(context.Background(), srv.URL+"/works?api_key=secret-key-123", &struct{}{})
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if strings.Contains(err.Error(), "secret-key-123") {
		t.Fatalf("error leaks the key: %v", err)
	}
}

func TestTaxonomyClassify(t *testing.T) {
	tx := loadTaxonomy("../../../ingest/taxonomy.json")
	if tx == nil {
		t.Skip("taxonomy.json not found")
	}
	cases := []struct {
		topic, subfield, name, want string
		excluded                    bool
	}{
		{"https://openalex.org/T10126", "https://openalex.org/subfields/1702", "Artificial Intelligence", "Programming Languages", false},
		{"https://openalex.org/T12157", "https://openalex.org/subfields/1702", "Artificial Intelligence", "", true}, // geochemistry
		{"https://openalex.org/T99999", "https://openalex.org/subfields/1705", "Computer Networks and Communications", "Networks", false},
		{"https://openalex.org/T99999", "https://openalex.org/subfields/2700", "Medicine", "Medicine", false},
	}
	for _, c := range cases {
		got, ex := tx.classify(c.topic, c.subfield, c.name)
		if got != c.want || ex != c.excluded {
			t.Errorf("classify(%s) = %q, %v; want %q, %v", c.topic, got, ex, c.want, c.excluded)
		}
	}
	var nilTx *taxonomy
	if got, _ := nilTx.classify("T1", "S1", "Software"); got != "Software" {
		t.Errorf("without a table: %q", got)
	}
}
