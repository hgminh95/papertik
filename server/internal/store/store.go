// Package store gives read access to index.bin (mmapped; the same pages vecdb uses)
// and to paper metadata in papers.jsonl.
package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"slices"
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
	Field    string   `json:"field,omitempty"` // PaperTik category (ingest/taxonomy.json)
	Topic    string   `json:"topic,omitempty"` // the OpenAlex topic, more specific
	DOI      string   `json:"doi,omitempty"`
	URL      string   `json:"url,omitempty"`
	PDFURL   string   `json:"pdf_url,omitempty"`
	CitedBy  int      `json:"cited_by"`
}

type Store struct {
	Dim, N  int
	BuildID uint64 // changes on every `vecdb build`; vecdb reports the id of the index it serves
	mem     []byte
	ids     []uint64
	metaOff []uint64
	scales  []float32
	vecs    []int8
	papers  []byte // papers.jsonl, memory-mapped: reading a paper is a copy, not a syscall
	byID    map[uint64]uint32
	attrs   []Attr // nil for a version 2 index (no feed filters until the next build)
}

// Attr is a row's attributes for feed filters, as vecdb stores them (vecdb/src/index.rs).
type Attr struct {
	Cited uint32
	Field uint32 // Hash of the PaperTik category
	Venue uint32 // Hash of the venue name
	Topic uint16 // OpenAlex topic number (T10036 -> 10036), 0 = none
	Year  uint16 // 0 = unknown
}

// Hash is FNV-1a (32 bit) with 0 kept for "none", as vecdb's hash32.
func Hash(s string) uint32 {
	if s == "" {
		return 0
	}
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return max(h, 1)
}

// TopicNumber turns "T10036" into 10036 (0 if it is not a topic id).
func TopicNumber(id string) uint16 {
	n, err := strconv.ParseUint(strings.TrimPrefix(id[strings.LastIndexByte(id, '/')+1:], "T"), 10, 16)
	if err != nil {
		return 0
	}
	return uint16(n)
}

// Filter limits which papers a feed may show; every set condition must hold. vecdb applies
// the same rules (search::Filter) to its nearest-neighbour scan.
type Filter struct {
	YearMin, YearMax uint16 // 0 = open
	CitedMin         uint32
	Fields           []uint32 // subject: any of these categories (Hash) ...
	Topics           []uint16 // ... or OpenAlex topics
	Venues           []uint32 // Hash of venue names
}

func (f *Filter) Empty() bool {
	return f == nil || f.YearMin == 0 && f.YearMax == 0 && f.CitedMin == 0 && len(f.Fields) == 0 && len(f.Topics) == 0 && len(f.Venues) == 0
}

func (f *Filter) Matches(a *Attr) bool {
	if f.YearMin != 0 && a.Year < f.YearMin || f.YearMax != 0 && (a.Year == 0 || a.Year > f.YearMax) || a.Cited < f.CitedMin {
		return false
	}
	if len(f.Fields) > 0 || len(f.Topics) > 0 {
		if !slices.Contains(f.Fields, a.Field) && !slices.Contains(f.Topics, a.Topic) {
			return false
		}
	}
	return len(f.Venues) == 0 || slices.Contains(f.Venues, a.Venue)
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
	// Layout: vecdb/src/index.rs.
	version := le.Uint32(mem[8:])
	if version != 2 && version != 3 {
		return nil, fmt.Errorf("%s: index version %d, need 3 (rebuild with `vecdb build`)", indexPath, version)
	}
	s := &Store{mem: mem, Dim: int(le.Uint32(mem[12:])), N: int(le.Uint64(mem[16:])), BuildID: le.Uint64(mem[80:])}
	offIDs, offMeta := int(le.Uint64(mem[24:])), int(le.Uint64(mem[32:]))
	offScales, offVecs := int(le.Uint64(mem[40:])), int(le.Uint64(mem[48:]))
	if offVecs+s.N*s.Dim > len(mem) {
		return nil, fmt.Errorf("%s: truncated", indexPath)
	}
	s.ids = unsafe.Slice((*uint64)(unsafe.Pointer(&mem[offIDs])), s.N)
	s.metaOff = unsafe.Slice((*uint64)(unsafe.Pointer(&mem[offMeta])), 2*s.N) // (start, end) per row
	s.scales = unsafe.Slice((*float32)(unsafe.Pointer(&mem[offScales])), s.N)
	s.vecs = unsafe.Slice((*int8)(unsafe.Pointer(&mem[offVecs])), s.N*s.Dim)
	if version >= 3 {
		offAttrs := int(le.Uint64(mem[88:]))
		if offAttrs+s.N*int(unsafe.Sizeof(Attr{})) > len(mem) {
			return nil, fmt.Errorf("%s: truncated", indexPath)
		}
		s.attrs = unsafe.Slice((*Attr)(unsafe.Pointer(&mem[offAttrs])), s.N)
	}

	s.byID = make(map[uint64]uint32, s.N)
	for i, id := range s.ids {
		s.byID[id] = uint32(i)
	}
	pf, err := os.Open(papersPath)
	if err != nil {
		return nil, err
	}
	defer pf.Close()
	pst, err := pf.Stat()
	if err != nil {
		return nil, err
	}
	// Lines appended later (new papers not in this index yet) are outside the mapping, which is
	// fine: every row's range was written by `vecdb build` against the file as it was then.
	if s.papers, err = syscall.Mmap(int(pf.Fd()), 0, int(pst.Size()), syscall.PROT_READ, syscall.MAP_SHARED); err != nil {
		return nil, fmt.Errorf("mmap %s: %w", papersPath, err)
	}
	for row := 0; row < s.N; row++ {
		if s.metaOff[2*row+1] > uint64(len(s.papers)) {
			return nil, fmt.Errorf("%s is shorter than the index expects (was it replaced?)", papersPath)
		}
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

// Close unmaps the index and metadata. Only call it once no request can still be using the store.
func (s *Store) Close() {
	syscall.Munmap(s.mem)
	syscall.Munmap(s.papers)
}

// ID returns the numeric OpenAlex id of a row (W123 -> 123).
func (s *Store) ID(row uint32) uint64 { return s.ids[row] }

// Raw returns the row's line of papers.jsonl (one JSON object, no trailing newline), appended
// to buf. The API returns papers with the same field names, so hot paths splice this in
// instead of decoding and re-encoding it.
func (s *Store) Raw(buf []byte, row uint32) ([]byte, error) {
	start, end := s.metaOff[2*row], s.metaOff[2*row+1]
	n := len(buf)
	buf = append(buf, s.papers[start:end]...)
	for len(buf) > n && (buf[len(buf)-1] == '\n' || buf[len(buf)-1] == '\r' || buf[len(buf)-1] == ' ') {
		buf = buf[:len(buf)-1]
	}
	return buf, nil
}

// Paper reads the metadata for a row.
func (s *Store) Paper(row uint32) (Paper, error) {
	var p Paper
	start, end := s.metaOff[2*row], s.metaOff[2*row+1]
	err := json.Unmarshal(bytes.TrimSpace(s.papers[start:end]), &p)
	return p, err
}

// HasAttrs reports whether this index supports feed filters (built by vecdb 3+).
func (s *Store) HasAttrs() bool { return s.attrs != nil }

// Attr returns a row's filter attributes (HasAttrs must be true).
func (s *Store) Attr(row uint32) *Attr { return &s.attrs[row] }

// Vector returns the quantised vector of a row: v ≈ scale * q.
func (s *Store) Vector(row uint32) (scale float32, q []int8) {
	i := int(row) * s.Dim
	return s.scales[row], s.vecs[i : i+s.Dim]
}
