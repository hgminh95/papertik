package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"papertok/server/internal/store"
)

// The OpenAlex API client: single-paper lookups for papers that are not in the index.

const (
	openAlexWorks = "https://api.openalex.org/works"
	workCacheTTL  = 10 * time.Minute
	workCacheN    = 512
	workSelect    = "id,title,abstract_inverted_index,authorships,publication_year,primary_location,best_oa_location,doi,cited_by_count,primary_topic"
)

var (
	htmlTag   = regexp.MustCompile(`<[^>]+>`)
	multSpace = regexp.MustCompile(`\s+`)
)

type openAlexWork struct {
	ID                    string           `json:"id"`
	Title                 string           `json:"title"`
	AbstractInvertedIndex map[string][]int `json:"abstract_inverted_index"`
	Authorships           []struct {
		Author struct {
			DisplayName string `json:"display_name"`
		} `json:"author"`
	} `json:"authorships"`
	PublicationYear int    `json:"publication_year"`
	DOI             string `json:"doi"`
	CitedByCount    int    `json:"cited_by_count"`
	PrimaryLocation *struct {
		LandingPageURL string `json:"landing_page_url"`
		Source         *struct {
			DisplayName string `json:"display_name"`
		} `json:"source"`
	} `json:"primary_location"`
	BestOALocation *struct {
		PDFURL string `json:"pdf_url"`
	} `json:"best_oa_location"`
	PrimaryTopic *struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		Subfield    struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"subfield"`
	} `json:"primary_topic"`
	RelatedWorks    []string `json:"related_works"`
	ReferencedWorks []string `json:"referenced_works"`
}

func cleanText(s string) string {
	s = strings.ReplaceAll(s, `\n`, " ")
	return strings.TrimSpace(multSpace.ReplaceAllString(htmlTag.ReplaceAllString(s, ""), " "))
}

// paper converts an OpenAlex work; excluded reports a topic outside computer science.
func (w *openAlexWork) paper(tx *taxonomy) (p store.Paper, excluded bool) {
	p = store.Paper{
		ID:      w.ID[strings.LastIndexByte(w.ID, '/')+1:],
		Title:   cleanText(w.Title),
		Year:    w.PublicationYear,
		DOI:     w.DOI,
		CitedBy: w.CitedByCount,
	}
	for _, a := range w.Authorships {
		if a.Author.DisplayName != "" && len(p.Authors) < 20 {
			p.Authors = append(p.Authors, a.Author.DisplayName)
		}
	}
	if l := w.PrimaryLocation; l != nil {
		p.URL = l.LandingPageURL
		if l.Source != nil {
			p.Venue = l.Source.DisplayName
		}
	}
	if w.BestOALocation != nil {
		p.PDFURL = w.BestOALocation.PDFURL
	}
	if t := w.PrimaryTopic; t != nil {
		p.Field, excluded = tx.classify(t.ID, t.Subfield.ID, t.Subfield.DisplayName)
		p.Topic = t.DisplayName
	}
	if inv := w.AbstractInvertedIndex; len(inv) > 0 {
		type tok struct {
			pos  int
			word string
		}
		var toks []tok
		for word, positions := range inv {
			for _, i := range positions {
				toks = append(toks, tok{i, word})
			}
		}
		sort.Slice(toks, func(a, b int) bool { return toks[a].pos < toks[b].pos })
		words := make([]string, len(toks))
		for i, t := range toks {
			words[i] = t.word
		}
		p.Abstract = cleanText(strings.Join(words, " "))
	}
	return p, excluded
}

type cachedWork struct {
	at time.Time
	w  *openAlexWork
}

// searcher is the OpenAlex client, used to look up single papers that are not in the index
// (shared links, requested papers). Search itself is local (fts.go).
type searcher struct {
	tx     *taxonomy
	mailto string
	apiKey string
	client *http.Client
	mu     sync.Mutex
	works  map[string]cachedWork
}

func newSearcher(mailto, apiKey string, tx *taxonomy) *searcher {
	return &searcher{tx: tx, mailto: mailto, apiKey: apiKey, client: &http.Client{Timeout: 8 * time.Second}, works: map[string]cachedWork{}}
}

// searchSorts maps our sort names to OpenAlex's; "" is relevance.
// get fetches JSON from OpenAlex, retrying rate limits (429) and transient 5xx with back-off.
func (s *searcher) get(ctx context.Context, u string, out any) error {
	var last error
	for attempt, wait := 0, 400*time.Millisecond; attempt < 4; attempt, wait = attempt+1, wait*2 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := s.client.Do(req)
		if err != nil {
			// *url.Error prints the full URL, which carries api_key: keep only the cause.
			var ue *url.Error
			if errors.As(err, &ue) {
				err = fmt.Errorf("openalex: %s: %w", ue.Op, ue.Err)
			}
			last = err
			continue
		}
		if resp.StatusCode == http.StatusOK {
			err = json.NewDecoder(resp.Body).Decode(out)
			resp.Body.Close()
			return err
		}
		resp.Body.Close()
		last = fmt.Errorf("openalex: %s", resp.Status)
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return last
		}
		// A long Retry-After (anonymous search is throttled under load) is not worth waiting for.
		if ra, _ := strconv.Atoi(resp.Header.Get("Retry-After")); ra > 2 {
			return fmt.Errorf("%w (retry after %ds)", last, ra)
		}
	}
	return last
}
