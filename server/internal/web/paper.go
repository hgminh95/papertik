package web

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"net/url"
	"papertok/server/internal/store"
	"strings"
	"time"
)

// GET /api/paper/{id}: one paper plus a seed vector for "more like this".
//
// Indexed papers use their own vector as the seed. For papers we have not embedded yet the
// seed is approximated by averaging the vectors of their OpenAlex related and referenced
// works that are in the index, and the paper is queued for ingestion.
func (s *Server) handlePaper(w http.ResponseWriter, r *http.Request) {
	st := s.snap().st
	id := strings.ToUpper(r.PathValue("id"))
	if !workID.MatchString(id) {
		httpError(w, http.StatusBadRequest, "bad paper id")
		return
	}
	type response struct {
		Paper     feedPaper `json:"paper"`
		Seed      string    `json:"seed,omitempty"` // base64 f32[dim]
		SeedBasis int       `json:"seedBasis"`      // 0 = exact (own vector), n = averaged from n related papers
	}

	if row, ok := st.Row(id); ok {
		p, err := st.Paper(row)
		if err != nil {
			httpError(w, http.StatusInternalServerError, "could not read paper")
			return
		}
		vec, scale := s.vector(st, row)
		writeJSON(w, response{
			Paper: feedPaper{Paper: p, Vec: vec, Scale: scale, Reason: "shared", Indexed: true},
			Seed:  encodeF32(s.dequantize(st, row)),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	work, err := s.search.work(ctx, id)
	if err != nil {
		log.Printf("paper %s: %v", id, err)
		httpError(w, http.StatusBadGateway, "could not load this paper from OpenAlex right now")
		return
	}
	s.pending.add(id, "opened")
	p, _ := work.paper(s.search.tx) // a paper someone opened directly: show it even if excluded
	res := response{Paper: feedPaper{Paper: p, Reason: "shared"}}
	var sum []float32
	for _, rel := range append(work.RelatedWorks, work.ReferencedWorks...) {
		row, ok := st.Row(rel)
		if !ok {
			continue
		}
		v := s.dequantize(st, row)
		if sum == nil {
			sum = make([]float32, len(v))
		}
		for i, x := range v {
			sum[i] += x
		}
		res.SeedBasis++
	}
	if sum != nil {
		res.Seed = encodeF32(sum) // the server normalises query vectors
	}
	writeJSON(w, res)
}

// POST /api/pending {id, reason}: the client liked or bookmarked a paper we cannot embed yet.
func (s *Server) handlePending(w http.ResponseWriter, r *http.Request) {
	st := s.snap().st
	var req struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "bad json")
		return
	}
	id := strings.ToUpper(req.ID)
	if !workID.MatchString(id) {
		httpError(w, http.StatusBadRequest, "bad paper id")
		return
	}
	reason := req.Reason
	if reason != "like" && reason != "bookmark" {
		reason = "other"
	}
	_, indexed := st.Row(id)
	queued := !indexed && s.pending.add(id, reason)
	writeJSON(w, map[string]bool{"indexed": indexed, "queued": queued})
}

// POST /api/vectors {ids}: vectors for the given ids that are indexed now. Lets the client
// apply likes it made while a paper was still pending.
func (s *Server) handleVectors(w http.ResponseWriter, r *http.Request) {
	st := s.snap().st
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || len(req.IDs) > 500 {
		httpError(w, http.StatusBadRequest, "bad request")
		return
	}
	var out []rowOut
	for _, id := range req.IDs {
		if row, ok := st.Row(id); ok {
			out = append(out, rowOut{row: row, reason: "shared"})
		}
	}
	s.writePapers(w, st, out, "")
}

func (s *Server) dequantize(st *store.Store, row uint32) []float32 {
	scale, q := st.Vector(row)
	v := make([]float32, len(q))
	for i, x := range q {
		v[i] = float32(x) * scale
	}
	return v
}

func encodeF32(v []float32) string {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// work fetches one OpenAlex work (cached like searches).
func (s *searcher) work(ctx context.Context, id string) (*openAlexWork, error) {
	key := "work\x00" + id
	s.mu.Lock()
	if c, ok := s.works[key]; ok && time.Since(c.at) < workCacheTTL {
		s.mu.Unlock()
		return c.w, nil
	}
	s.mu.Unlock()
	v := url.Values{"select": {workSelect + ",related_works,referenced_works"}}
	if s.mailto != "" {
		v.Set("mailto", s.mailto)
	}
	if s.apiKey != "" {
		v.Set("api_key", s.apiKey)
	}
	var w openAlexWork
	if err := s.get(ctx, openAlexWorks+"/"+id+"?"+v.Encode(), &w); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.works) >= workCacheN {
		clear(s.works)
	}
	s.works[key] = cachedWork{time.Now(), &w}
	s.mu.Unlock()
	return &w, nil
}
