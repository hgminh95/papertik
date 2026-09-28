package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// TODO: OpenAlex is too expensive to rely on for search (anonymous search is throttled, and at
// scale it needs a paid API key); find an alternative, e.g. our own full-text index (Tantivy,
// Meilisearch, SQLite FTS5) over titles + abstracts, or hybrid keyword + vector search in vecdb.
//
// Discover search is delegated to OpenAlex (restricted to computer science). Results that
// are in our index come back with their vector so they can be liked; the rest are
// shown read-only.

const (
	openAlexWorks = "https://api.openalex.org/works"
	searchTTL     = 10 * time.Minute
	searchCacheN  = 512
	searchPerPage = 20
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

type searchResult struct {
	Papers []feedPaper `json:"papers"`
	Total  int         `json:"total"`
	Page   int         `json:"page"`
	Source string      `json:"source"` // "openalex", or "local" when OpenAlex is unavailable
}

type cached struct {
	at  time.Time
	res searchResult
}

type cachedWork struct {
	at time.Time
	w  *openAlexWork
}

type searcher struct {
	tx     *taxonomy
	mailto string
	apiKey string
	client *http.Client
	mu     sync.Mutex
	cache  map[string]cached
	works  map[string]cachedWork
}

func newSearcher(mailto, apiKey string, tx *taxonomy) *searcher {
	return &searcher{tx: tx, mailto: mailto, apiKey: apiKey, client: &http.Client{Timeout: 8 * time.Second}, cache: map[string]cached{}, works: map[string]cachedWork{}}
}

// searchSorts maps our sort names to OpenAlex's; "" is relevance.
var searchSorts = map[string]string{"": "", "cited": "cited_by_count:desc", "recent": "publication_date:desc"}

func (s *searcher) search(ctx context.Context, q, sortBy string, page int) (searchResult, error) {
	key := fmt.Sprintf("%d\x00%s\x00%s", page, sortBy, strings.ToLower(q))
	s.mu.Lock()
	if c, ok := s.cache[key]; ok && time.Since(c.at) < searchTTL {
		s.mu.Unlock()
		return c.res, nil
	}
	s.mu.Unlock()

	v := url.Values{
		"search":   {q},
		"filter":   {"primary_topic.field.id:17,is_retracted:false"},
		"per-page": {strconv.Itoa(searchPerPage)},
		"page":     {strconv.Itoa(page)},
		"select":   {workSelect},
	}
	if o := searchSorts[sortBy]; o != "" {
		v.Set("sort", o)
	}
	if s.mailto != "" {
		v.Set("mailto", s.mailto)
	}
	if s.apiKey != "" {
		v.Set("api_key", s.apiKey)
	}
	var body struct {
		Meta struct {
			Count int `json:"count"`
		} `json:"meta"`
		Results []openAlexWork `json:"results"`
	}
	if err := s.get(ctx, openAlexWorks+"?"+v.Encode(), &body); err != nil {
		return searchResult{}, err
	}
	res := searchResult{Source: "openalex", Total: body.Meta.Count, Page: page, Papers: make([]feedPaper, 0, len(body.Results))}
	for i := range body.Results {
		p, excluded := body.Results[i].paper(s.tx)
		if p.Title == "" || excluded {
			continue
		}
		res.Papers = append(res.Papers, feedPaper{Paper: p, Reason: "search"})
	}

	s.mu.Lock()
	if len(s.cache) >= searchCacheN {
		for k, c := range s.cache {
			if time.Since(c.at) >= searchTTL || len(s.cache) >= searchCacheN {
				delete(s.cache, k)
			}
		}
	}
	s.cache[key] = cached{time.Now(), res}
	s.mu.Unlock()
	return res, nil
}

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

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	sn := s.snap()
	st := sn.st
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 200 {
		httpError(w, http.StatusBadRequest, "query must be 1-200 characters")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = min(max(page, 1), 50)
	sortBy := r.URL.Query().Get("sort")
	if _, ok := searchSorts[sortBy]; !ok {
		sortBy = ""
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second) // under the server's write timeout
	defer cancel()
	res, err := s.search.search(ctx, q, sortBy, page)
	if err != nil {
		if st.N > maxLocalSearch {
			log.Printf("search %q: %v", q, err)
			httpError(w, http.StatusBadGateway, "Search is busy right now. Try again in a minute.")
			return
		}
		log.Printf("search %q: %v; falling back to the local index", q, err)
		res = s.localSearch(sn, q, sortBy, page)
	}
	// Attach vectors (and our cleaner metadata) for papers we have indexed. Copy first:
	// the cached slice is shared between requests.
	out := searchResult{Source: res.Source, Total: res.Total, Page: res.Page, Papers: make([]feedPaper, len(res.Papers))}
	for i, p := range res.Papers {
		if row, ok := st.Row(p.ID); ok {
			if meta, err := st.Paper(row); err == nil {
				p.Paper = meta
			}
			p.Vec, p.Scale = s.vector(st, row)
			p.Indexed = true
		}
		out.Papers[i] = p
	}
	writeJSON(w, out)
}

// ---- local fallback: keyword match over the titles in our own index ----

// The fallback scans every title (and loads them all on first use), which is fine for a few
// million papers but not for the full corpus; beyond this, fail instead. A real inverted index
// (or an OpenAlex API key) is the fix at that scale.
const maxLocalSearch = 3_000_000

type titleIndex struct {
	once   sync.Once
	titles []string // lower-cased, by row
	cited  []int32
	years  []int16
}

func (s *Server) localSearch(sn *snapshot, q, sortBy string, page int) searchResult {
	st := sn.st
	sn.titles.once.Do(func() {
		t := make([]string, st.N)
		c, y := make([]int32, st.N), make([]int16, st.N)
		for row := range t {
			if p, err := st.Paper(uint32(row)); err == nil {
				t[row] = strings.ToLower(p.Title + " " + p.Field)
				c[row], y[row] = int32(p.CitedBy), int16(p.Year)
			}
		}
		sn.titles.titles, sn.titles.cited, sn.titles.years = t, c, y
	})
	terms := strings.Fields(strings.ToLower(q))
	type match struct {
		row   uint32
		score int
	}
	var matches []match
	dup := map[string]bool{} // OpenAlex has duplicate records of the same paper
	for row, title := range sn.titles.titles {
		score := 0
		for _, t := range terms {
			if strings.Contains(title, t) {
				score++
			}
		}
		// Require most of the terms, so long queries still find something.
		if (score > 0 && score*2 >= len(terms)+1 || score == len(terms)) && !dup[title] {
			dup[title] = true
			matches = append(matches, match{uint32(row), score})
		}
	}
	// Best match first (ties keep index order, which ingest puts roughly by citations);
	// with a sort, among the rows that match best.
	ti := sn.titles
	sort.SliceStable(matches, func(a, b int) bool {
		ma, mb := matches[a], matches[b]
		if ma.score != mb.score {
			return ma.score > mb.score
		}
		switch sortBy {
		case "cited":
			return ti.cited[ma.row] > ti.cited[mb.row]
		case "recent":
			return ti.years[ma.row] > ti.years[mb.row]
		}
		return false
	})
	res := searchResult{Source: "local", Total: len(matches), Page: page}
	for _, m := range matches[min((page-1)*searchPerPage, len(matches)):min(page*searchPerPage, len(matches))] {
		if p, err := st.Paper(m.row); err == nil {
			res.Papers = append(res.Papers, feedPaper{Paper: p, Reason: "search"})
		}
	}
	return res
}
