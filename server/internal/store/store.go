// Package store gives read access to index.bin (mmapped; the same pages vecdb uses)
// and to paper metadata in papers.jsonl.
package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const indexMagic = "PTKIDX01"

type Paper struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Abstract string   `json:"abstract"`
	Authors  []string `json:"authors"`
	Year     int      `json:"year"`
	Venue    string   `json:"venue"`
	Field    string   `json:"field,omitempty"`
	DOI      string   `json:"doi,omitempty"`
	URL      string   `json:"url,omitempty"`
	PDFURL   string   `json:"pdf_url,omitempty"`
	CitedBy  int      `json:"cited_by"`
}

type Store struct {
	Dim, N  int
	mem     []byte
	ids     []uint64
	metaOff []uint64
	scales  []float32
	vecs    []int8
	papers  *os.File
	byID    map[uint64]uint32
}

func Open(indexPath, papersPath string) (*Store, error) {
	f, err := os.Open(indexPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	mem, err := syscall.Mmap(int(f.Fd()), 0, int(st.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmap %s: %w", indexPath, err)
	}
	if len(mem) < 64 || string(mem[:8]) != indexMagic {
		return nil, fmt.Errorf("%s: not a papertok index", indexPath)
	}
	le := binary.LittleEndian
	if v := le.Uint32(mem[8:]); v != 1 {
		return nil, fmt.Errorf("%s: unsupported version %d", indexPath, v)
	}
	s := &Store{mem: mem, Dim: int(le.Uint32(mem[12:])), N: int(le.Uint64(mem[16:]))}
	offIDs, offMeta := int(le.Uint64(mem[24:])), int(le.Uint64(mem[32:]))
	offScales, offVecs := int(le.Uint64(mem[40:])), int(le.Uint64(mem[48:]))
	if offVecs+s.N*s.Dim > len(mem) {
		return nil, fmt.Errorf("%s: truncated", indexPath)
	}
	s.ids = unsafe.Slice((*uint64)(unsafe.Pointer(&mem[offIDs])), s.N)
	s.metaOff = unsafe.Slice((*uint64)(unsafe.Pointer(&mem[offMeta])), s.N+1)
	s.scales = unsafe.Slice((*float32)(unsafe.Pointer(&mem[offScales])), s.N)
	s.vecs = unsafe.Slice((*int8)(unsafe.Pointer(&mem[offVecs])), s.N*s.Dim)

	s.byID = make(map[uint64]uint32, s.N)
	for i, id := range s.ids {
		s.byID[id] = uint32(i)
	}
	if s.papers, err = os.Open(papersPath); err != nil {
		return nil, err
	}
	return s, nil
}

// ParseID turns "W123", "https://openalex.org/W123" or "123" into 123.
func ParseID(s string) (uint64, bool) {
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimPrefix(s, "W")
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

// Row maps an OpenAlex id to its row.
func (s *Store) Row(id string) (uint32, bool) {
	n, ok := ParseID(id)
	if !ok {
		return 0, false
	}
	r, ok := s.byID[n]
	return r, ok
}

// ID returns the numeric OpenAlex id of a row (W123 -> 123).
func (s *Store) ID(row uint32) uint64 { return s.ids[row] }

// Paper reads the metadata for a row.
func (s *Store) Paper(row uint32) (Paper, error) {
	var p Paper
	start, end := s.metaOff[row], s.metaOff[row+1]
	buf := make([]byte, end-start)
	if _, err := s.papers.ReadAt(buf, int64(start)); err != nil {
		return p, err
	}
	err := json.Unmarshal(bytes.TrimSpace(buf), &p)
	return p, err
}

// Vector returns the quantised vector of a row: v ≈ scale * q.
func (s *Store) Vector(row uint32) (scale float32, q []int8) {
	i := int(row) * s.Dim
	return s.scales[row], s.vecs[i : i+s.Dim]
}
