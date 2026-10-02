package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"papertok/server/internal/shm"
	"papertok/server/internal/store"
)

// Feed filters: the reader limits For You to papers by year, citations, subject (PaperTik
// categories or OpenAlex topics) and venue. The filter lives in the browser and comes with
// each feed request; vecdb applies it during its nearest-neighbour scan (see search::Filter),
// and the server to its random picks. Search and Discover are not filtered.

type filterRequest struct {
	YearMin  int      `json:"yearMin"`
	YearMax  int      `json:"yearMax"`
	CitedMin int      `json:"citedMin"`
	Fields   []string `json:"fields"` // PaperTik category names
	Topics   []string `json:"topics"` // OpenAlex topic ids (T10036)
	Venues   []string `json:"venues"` // venue names, exactly as stored
}

// toFilter turns the request into store.Filter (nil = no filter). Lists are cut to what one
// vecdb request carries.
func (f *filterRequest) toFilter() *store.Filter {
	if f == nil {
		return nil
	}
	year := func(y int) uint16 { return uint16(min(max(y, 0), 9999)) }
	out := &store.Filter{YearMin: year(f.YearMin), YearMax: year(f.YearMax), CitedMin: uint32(min(max(f.CitedMin, 0), 1<<31))}
	for _, name := range f.Fields[:min(len(f.Fields), shm.MaxFilterValues)] {
		out.Fields = append(out.Fields, store.Hash(name))
	}
	for _, id := range f.Topics[:min(len(f.Topics), shm.MaxFilterValues)] {
		if t := store.TopicNumber(id); t != 0 {
			out.Topics = append(out.Topics, t)
		}
	}
	for _, name := range f.Venues[:min(len(f.Venues), shm.MaxFilterValues)] {
		out.Venues = append(out.Venues, store.Hash(name))
	}
	if out.Empty() {
		return nil
	}
	return out
}

const facetResults = 20

// GET /api/filters/options?kind=topic|venue&q=: the topics or venues whose name contains q
// (all of them for an empty q), most papers first.
func (s *Server) handleFilterOptions(w http.ResponseWriter, r *http.Request) {
	sn := s.snap()
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	sn.explore.mu.RLock()
	list, ready := sn.explore.venues, sn.explore.ready
	if r.URL.Query().Get("kind") == "topic" {
		list = sn.explore.topics
	}
	out := make([]facet, 0, facetResults)
	for i := 0; i < len(list) && len(out) < facetResults; i++ {
		if strings.Contains(list[i].lower, q) {
			out = append(out, list[i])
		}
	}
	sn.explore.mu.RUnlock()
	writeJSON(w, map[string]any{"ready": ready, "options": out})
}

// POST /api/filters/count {filter}: how many papers in the index match.
func (s *Server) handleFilterCount(w http.ResponseWriter, r *http.Request) {
	st := s.snap().st
	var req filterRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "bad json")
		return
	}
	writeJSON(w, map[string]any{
		"count":     s.rec.Matching(st, req.toFilter()).Count,
		"total":     st.N,
		"supported": st.HasAttrs(), // false until the index is rebuilt by this version
	})
}
