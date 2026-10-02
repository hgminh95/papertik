package feed

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"papertok/server/internal/shm"
	"papertok/server/internal/store"
)

// Random picks (cold start, and the fallback when vecdb is down) honour the filter, for
// narrow filters (picked from the pool of matching rows) and broad ones (sampled and checked).
func TestRandomPicksMatchFilter(t *testing.T) {
	bin, _ := filepath.Abs("../../../vecdb/target/release/vecdb")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("vecdb binary not built")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"synth", "--n", "5000", "--dim", "32", "--topics", "8", "--out", dir},
		{"build", "--papers", dir + "/papers.jsonl", "--embeddings", dir + "/embeddings.f32", "--dim", "32", "--nlist", "0", "--out", dir + "/index.bin"},
	} {
		if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
			t.Fatalf("vecdb %v: %v\n%s", args, err, out)
		}
	}
	st, err := store.Open(dir+"/index.bin", dir+"/papers.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	r := New(DefaultConfig, shm.NewClient(filepath.Join(dir, "no-vecdb")))
	pref := make([]float32, st.Dim)
	pref[0] = 1
	for _, tc := range []struct {
		name   string
		f      *store.Filter
		narrow bool
	}{
		{"narrow", &store.Filter{Venues: []uint32{store.Hash("PLDI")}, YearMin: 2020}, true},
		{"broad", &store.Filter{YearMin: 2003}, false},
		{"subject", &store.Filter{Fields: []uint32{store.Hash("Databases")}, Topics: []uint16{10_001}}, false},
	} {
		pool := r.Matching(st, tc.f)
		if pool.Count == 0 || (pool.Rows != nil) != tc.narrow {
			t.Fatalf("%s: pool count %d, rows kept %v", tc.name, pool.Count, pool.Rows != nil)
		}
		for _, p := range [][]float32{nil, pref} { // cold start; vecdb down
			items := r.Next(context.Background(), st, p, nil, 8, tc.f)
			if len(items) != min(8, pool.Count) {
				t.Fatalf("%s: %d items, %d rows match", tc.name, len(items), pool.Count)
			}
			for _, it := range items {
				if !tc.f.Matches(st.Attr(it.Row)) {
					t.Fatalf("%s: row %d does not match", tc.name, it.Row)
				}
			}
		}
	}
	if items := r.Next(context.Background(), st, nil, nil, 8, &store.Filter{Venues: []uint32{store.Hash("nowhere")}}); len(items) != 0 {
		t.Fatalf("impossible filter returned %d items", len(items))
	}
	if r.Matching(st, nil).Count != st.N {
		t.Fatal("no filter should match every row")
	}
}
