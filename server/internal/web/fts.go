package web

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-sqlite3" // C SQLite via cgo, built with -tags sqlite_fts5 (see Makefile)
)

// The C driver is ~2.5x faster than a pure-Go SQLite on FTS5 ranking, which is the whole cost
// of a search. Connections are read-only and use memory-mapped I/O, so they share the OS page
// cache instead of each copying pages into its own small cache.
func init() {
	sql.Register("sqlite3_search", &sqlite3.SQLiteDriver{
		ConnectHook: func(c *sqlite3.SQLiteConn) error {
			for _, p := range []string{"PRAGMA query_only = 1", "PRAGMA mmap_size = 17179869184", "PRAGMA cache_size = -65536"} {
				if _, err := c.Exec(p, nil); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

// Search is local: SQLite FTS5 over title, authors and abstract of every indexed paper
// (data/search.db, written by the ingest service; see ingest/searchindex.py). No external
// service, no rate limits, a few GB of disk at full scale.

const (
	searchPerPage = 20
	countCap      = 1000 // "1,000+ results": counting every match of a common word is wasted work
	resultTTL     = 5 * time.Minute
)

var wordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

// Very common words are left out of the index (ingest/searchindex.py, keep in sync) and so
// out of queries.
var stopWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a about above after again against all also am an and any are as at be because been
	before being below between both but by can could did do does doing down during each few for from further had has have
	having he her here hers herself him himself his how i if in into is it its itself just me more most my myself no nor
	not now of off on once only or other our ours ourselves out over own same she should so some such than that the their
	theirs them themselves then there these they this those through to too under until up very was we were what when
	where which while who whom why will with would you your yours yourself yourselves via using based use used new paper
	study approach show shows propose proposed`) {
		m[w] = true
	}
	return m
}()

// ftsQuery turns what the user typed into an FTS5 query: every word must match (each quoted,
// so no FTS syntax can be injected), or with any=true, any word. With several words the exact
// phrase is added as an alternative: it matches no extra papers, but papers containing the
// phrase score higher.
func ftsQuery(q string, any bool) string {
	var words []string
	for _, w := range wordRe.FindAllString(strings.ToLower(q), 24) {
		if !stopWords[w] && len(words) < 12 {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return ""
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = `"` + w + `"`
	}
	if any {
		return strings.Join(quoted, " OR ")
	}
	if len(words) == 1 {
		return quoted[0]
	}
	return "(" + strings.Join(quoted, " ") + `) OR "` + strings.Join(words, " ") + `"`
}

// Ranking: title matches come first (tfts, a small title-only index: fast even for common
// words), then papers that match only in the abstract or authors (fts). BM25 weights title 10x,
// authors 3x, abstract 1x; pop = ln(1 + citations) nudges well-cited papers up.
var orderSQL = map[string]string{
	"":       "bm25(%s) - 0.15 * m.pop",
	"cited":  "m.cited_by DESC",
	"recent": "m.year DESC, m.cited_by DESC",
}

func rankedSQL(table, sortBy, extra string) string {
	order := orderSQL[sortBy]
	if sortBy == "" {
		weights := table
		if table == "fts" {
			weights = "fts, 10.0, 3.0, 1.0"
		}
		order = fmt.Sprintf(order, weights)
	}
	return fmt.Sprintf(`SELECT m.id FROM %[1]s JOIN meta m ON m.id = %[1]s.rowid WHERE %[1]s MATCH ?%s ORDER BY %s LIMIT ? OFFSET ?`,
		table, extra, order)
}

type ftsResult struct {
	ids    []int64
	total  int
	capped bool
	at     time.Time
}

type ftsIndex struct {
	path  string
	mu    sync.Mutex
	db    *sql.DB
	cache map[string]ftsResult
}

// conn opens search.db on first use (the ingest service creates it after the first build).
func (f *ftsIndex) conn() (*sql.DB, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.db != nil {
		return f.db, nil
	}
	if _, err := os.Stat(f.path); err != nil {
		return nil, err
	}
	// Read-only use; WAL lets the ingest service write at the same time.
	db, err := sql.Open("sqlite3_search", "file:"+f.path+"?_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	f.db = db
	return db, nil
}

func count(db *sql.DB, table, match string) (int, error) {
	var n int
	err := db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM (SELECT rowid FROM %[1]s WHERE %[1]s MATCH ? LIMIT ?)`, table),
		match, countCap+1).Scan(&n)
	return n, err
}

func ids(db *sql.DB, query string, args ...any) ([]int64, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

func (f *ftsIndex) search(q, sortBy string, page int) (ftsResult, error) {
	key := fmt.Sprintf("%s\x00%d\x00%s", sortBy, page, strings.ToLower(strings.TrimSpace(q)))
	f.mu.Lock()
	if r, ok := f.cache[key]; ok && time.Since(r.at) < resultTTL {
		f.mu.Unlock()
		return r, nil
	}
	f.mu.Unlock()

	db, err := f.conn()
	if err != nil {
		return ftsResult{}, err
	}
	res := ftsResult{at: time.Now()}
	offset := (page - 1) * searchPerPage
	for _, any := range []bool{false, true} { // all words first; any word if nothing matches all
		match := ftsQuery(q, any)
		if match == "" {
			break
		}
		if res.total, err = count(db, "fts", match); err != nil {
			return ftsResult{}, err
		}
		if res.total == 0 {
			continue
		}
		res.capped = res.total > countCap
		res.total = min(res.total, countCap)

		// 1. Papers with the words in the title.
		titled, err := count(db, "tfts", match)
		if err != nil {
			return ftsResult{}, err
		}
		if res.ids, err = ids(db, rankedSQL("tfts", sortBy, ""), match, searchPerPage, offset); err != nil {
			return ftsResult{}, err
		}
		// 2. Once those run out (only for rarer words, where this is cheap): the rest. The phrase
		// boost matters most here (it costs ~30% more, worth it: without it a paper whose abstract
		// says "functional programming" ranks below one that merely mentions both words).
		if need := searchPerPage - len(res.ids); need > 0 && titled <= countCap {
			more, err := ids(db, rankedSQL("fts", sortBy, " AND m.id NOT IN (SELECT rowid FROM tfts WHERE tfts MATCH ?)"),
				match, match, need, max(0, offset-titled))
			if err != nil {
				return ftsResult{}, err
			}
			res.ids = append(res.ids, more...)
		}
		break
	}
	f.mu.Lock()
	if len(f.cache) > 2000 {
		clear(f.cache)
	}
	f.cache[key] = res
	f.mu.Unlock()
	return res, nil
}

// GET /api/search?q=&sort=|cited|recent&page=
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	st := s.snap().st
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 200 {
		httpError(w, http.StatusBadRequest, "query must be 1-200 characters")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = min(max(page, 1), 50)
	sortBy := r.URL.Query().Get("sort")
	if _, ok := orderSQL[sortBy]; !ok {
		sortBy = ""
	}
	res, err := s.fts.search(q, sortBy, page)
	if err != nil {
		if os.IsNotExist(err) {
			httpError(w, http.StatusServiceUnavailable, "Search is being set up. Try again in a few minutes.")
			return
		}
		log.Printf("search %q: %v", q, err)
		httpError(w, http.StatusInternalServerError, "search failed")
		return
	}
	out := make([]rowOut, 0, len(res.ids))
	for _, id := range res.ids {
		if row, ok := st.Row("W" + strconv.FormatInt(id, 10)); ok { // skip anything not in this index build
			out = append(out, rowOut{row: row, reason: "search"})
		}
	}
	s.writePapers(w, st, out, fmt.Sprintf(`,"total":%d,"totalCapped":%t,"page":%d,"source":"local"`, res.total, res.capped, page))
}
