package web

import (
	"encoding/base64"
	"log"
	"net/http"
	"strconv"
	"sync"
)

// The feed, Discover and vector endpoints return lists of indexed papers. Their JSON is built by
// splicing each paper's papers.jsonl line (already JSON, same field names) with the extra fields,
// instead of decoding into a struct and encoding it again: that round trip was most of the CPU
// time of a request.

type rowOut struct {
	row    uint32
	score  float32
	reason string
}

var bufPool = sync.Pool{New: func() any { b := make([]byte, 0, 64<<10); return &b }}

// writePapers writes {"papers":[...]<extra>} where extra is a raw JSON fragment such as `,"more":true`.
func (s *Server) writePapers(w http.ResponseWriter, rows []rowOut, extra string) {
	bp := bufPool.Get().(*[]byte)
	buf := append((*bp)[:0], `{"papers":[`...)
	first := true
	for _, it := range rows {
		mark := len(buf)
		if !first {
			buf = append(buf, ',')
		}
		var err error
		start := len(buf)
		buf, err = s.store.Raw(buf, it.row)
		if err != nil || len(buf) == start || buf[len(buf)-1] != '}' {
			log.Printf("papers.jsonl row %d: unreadable (%v)", it.row, err)
			buf = buf[:mark]
			continue
		}
		buf = buf[:len(buf)-1] // reopen the object
		scale, q := s.store.Vector(it.row)
		buf = append(buf, `,"vec":"`...)
		buf = base64.StdEncoding.AppendEncode(buf, int8Bytes(q))
		buf = append(buf, `","scale":`...)
		buf = strconv.AppendFloat(buf, float64(scale), 'g', -1, 32)
		if it.score != 0 {
			buf = append(buf, `,"score":`...)
			buf = strconv.AppendFloat(buf, float64(it.score), 'g', -1, 32)
		}
		buf = append(buf, `,"reason":"`...)
		buf = append(buf, it.reason...) // fixed identifiers, no escaping needed
		buf = append(buf, `","indexed":true}`...)
		first = false
	}
	buf = append(buf, ']')
	buf = append(buf, extra...)
	buf = append(buf, "}\n"...)
	w.Header().Set("Content-Type", "application/json")
	w.Write(buf)
	*bp = buf
	bufPool.Put(bp)
}
