package web

import (
	"bufio"
	"container/heap"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

// The Discover page browses the index by field: the most cited papers overall and per field,
// and the most cited recent ones. The lists are built once at startup by streaming
// papers.jsonl, keeping only the top exploreKeep rows per list, so memory stays small even for
// the full corpus. Until the scan finishes the endpoints answer with ready=false.

const (
	exploreKeep    = 480 // per list
	explorePerPage = 24
	exploreAll     = "" // the list key for "all fields"
	recentYears    = 3  // "recent" = published in the last few years
)

type ranked struct {
	row   uint32
	cited int
}

// minHeap keeps the top-N by citations (the least cited is on top, ready to be evicted).
type minHeap []ranked

func (h minHeap) Len() int           { return len(h) }
func (h minHeap) Less(i, j int) bool { return h[i].cited < h[j].cited }
func (h minHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *minHeap) Push(x any)        { *h = append(*h, x.(ranked)) }
func (h *minHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

func (h *minHeap) offer(r ranked) {
	if h.Len() < exploreKeep {
		heap.Push(h, r)
	} else if r.cited > (*h)[0].cited {
		(*h)[0] = r
		heap.Fix(h, 0)
	}
}

// sorted returns the rows best first.
func (h minHeap) sorted() []uint32 {
	c := append(minHeap(nil), h...)
	sort.Slice(c, func(i, j int) bool { return c[i].cited > c[j].cited || c[i].cited == c[j].cited && c[i].row < c[j].row })
	rows := make([]uint32, len(c))
	for i, r := range c {
		rows[i] = r.row
	}
	return rows
}

type fieldCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type explorer struct {
	mu     sync.RWMutex
	ready  bool
	fields []fieldCount        // by paper count, descending
	cited  map[string][]uint32 // field ("" = all) -> rows, most cited first
	recent map[string][]uint32 // same, limited to recent papers
}

// build streams papers.jsonl in the background.
func (e *explorer) build(papersPath string, rowOf func(id string) (uint32, bool)) {
	start := time.Now()
	f, err := os.Open(papersPath)
	if err != nil {
		log.Printf("explore: %v", err)
		return
	}
	defer f.Close()
	cited := map[string]*minHeap{exploreAll: {}}
	recent := map[string]*minHeap{exploreAll: {}}
	counts := map[string]int{}
	minYear := time.Now().Year() - recentYears + 1

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	var row struct {
		ID      string `json:"id"`
		Field   string `json:"field"`
		Year    int    `json:"year"`
		CitedBy int    `json:"cited_by"`
	}
	for sc.Scan() {
		row.ID, row.Field, row.Year, row.CitedBy = "", "", 0, 0
		if json.Unmarshal(sc.Bytes(), &row) != nil {
			continue
		}
		// The index stores rows grouped by cluster, so line i is not row i: look the row up.
		idx, ok := rowOf(row.ID)
		if !ok {
			continue // not in the index (e.g. papers.jsonl has lines not embedded yet)
		}
		r := ranked{idx, row.CitedBy}
		cited[exploreAll].offer(r)
		if row.Year >= minYear {
			recent[exploreAll].offer(r)
		}
		if row.Field == "" {
			continue
		}
		counts[row.Field]++
		if cited[row.Field] == nil {
			cited[row.Field], recent[row.Field] = &minHeap{}, &minHeap{}
		}
		cited[row.Field].offer(r)
		if row.Year >= minYear {
			recent[row.Field].offer(r)
		}
	}
	if err := sc.Err(); err != nil {
		log.Printf("explore: reading papers: %v", err)
	}

	fields := make([]fieldCount, 0, len(counts))
	for name, c := range counts {
		fields = append(fields, fieldCount{name, c})
	}
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].Count > fields[j].Count || fields[i].Count == fields[j].Count && fields[i].Name < fields[j].Name
	})
	c, rc := map[string][]uint32{}, map[string][]uint32{}
	for k, h := range cited {
		c[k] = h.sorted()
	}
	for k, h := range recent {
		rc[k] = h.sorted()
	}

	e.mu.Lock()
	e.fields, e.cited, e.recent, e.ready = fields, c, rc, true
	e.mu.Unlock()
	log.Printf("explore: indexed %d fields in %s", len(fields), time.Since(start).Round(time.Millisecond))
}

// GET /api/explore: the field list for the chips.
func (s *Server) handleExplore(w http.ResponseWriter, r *http.Request) {
	sn := s.snap()
	sn.explore.mu.RLock()
	defer sn.explore.mu.RUnlock()
	writeJSON(w, map[string]any{"ready": sn.explore.ready, "fields": sn.explore.fields})
}

// GET /api/explore/papers?field=&sort=cited|recent&page=
func (s *Server) handleExplorePapers(w http.ResponseWriter, r *http.Request) {
	sn := s.snap()
	st := sn.st
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	sn.explore.mu.RLock()
	lists := sn.explore.cited
	if q.Get("sort") == "recent" {
		lists = sn.explore.recent
	}
	rows := lists[q.Get("field")]
	ready := sn.explore.ready
	sn.explore.mu.RUnlock()

	lo, hi := min((page-1)*explorePerPage, len(rows)), min(page*explorePerPage, len(rows))
	out := make([]rowOut, 0, hi-lo)
	for _, row := range rows[lo:hi] {
		out = append(out, rowOut{row: row, reason: "random"})
	}
	s.writePapers(w, st, out, fmt.Sprintf(`,"ready":%t,"more":%t`, ready, hi < len(rows)))
}
