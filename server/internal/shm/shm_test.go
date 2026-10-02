package shm_test

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"papertok/server/internal/shm"
	"papertok/server/internal/store"
)

// Integration test against the real vecdb binary (built with `make vecdb`).
func TestSearchAgainstVecdb(t *testing.T) {
	bin, _ := filepath.Abs("../../../vecdb/target/release/vecdb")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("vecdb binary not built")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
			t.Fatalf("vecdb %v: %v\n%s", args, err, out)
		}
	}
	run("synth", "--n", "5000", "--dim", "64", "--topics", "8", "--out", dir)
	run("build", "--papers", dir+"/papers.jsonl", "--embeddings", dir+"/embeddings.f32", "--dim", "64", "--nlist", "0", "--out", dir+"/index.bin")

	shmPath := filepath.Join(dir, "shm")
	cmd := exec.Command(bin, "serve", "--index", dir+"/index.bin", "--shm", shmPath, "--slots", "8", "--max-k", "32", "--max-exclude", "16")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Signal(os.Interrupt); cmd.Wait() }()

	st, err := store.Open(dir+"/index.bin", dir+"/papers.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	c := shm.NewClient(shmPath)
	for i := 0; !c.Alive(); i++ {
		if i > 100 {
			t.Fatal("vecdb did not come up")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Query with paper 42's own vector: it must come back first unless excluded.
	scale, q8 := st.Vector(42)
	q := make([]float32, st.Dim)
	for i, x := range q8 {
		q[i] = float32(x) * scale
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hits, err := c.Search(ctx, st.BuildID, q, 10, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 10 || hits[0].Row != 42 || math.Abs(float64(hits[0].Score)-1) > 0.02 {
		t.Fatalf("self-query: got %+v", hits[:min(3, len(hits))])
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].Score > hits[i-1].Score {
			t.Fatalf("not sorted: %+v", hits)
		}
	}
	hits, err = c.Search(ctx, st.BuildID, q, 10, []uint32{42}, nil)
	if err != nil || hits[0].Row == 42 {
		t.Fatalf("exclude ignored: %v %+v", err, hits)
	}
	// k is clamped to the server's max_k.
	if hits, _ = c.Search(ctx, st.BuildID, q, 1000, nil, nil); len(hits) != 32 {
		t.Fatalf("want 32 hits (max_k), got %d", len(hits))
	}

	// Feed filters: only matching rows come back, and Go and vecdb agree on what matches
	// (same string hashes, same rules).
	if !st.HasAttrs() {
		t.Fatal("a fresh index should have filter attributes")
	}
	for _, f := range []*store.Filter{
		{Venues: []uint32{store.Hash("PLDI")}, YearMin: 2015},
		{Fields: []uint32{store.Hash("Databases")}, Topics: []uint16{store.TopicNumber("T10003")}, CitedMin: 250},
		{YearMin: 2001, YearMax: 2002},
		{Venues: []uint32{store.Hash("No Such Venue")}},
	} {
		want := 0
		for row := 0; row < st.N; row++ {
			if f.Matches(st.Attr(uint32(row))) {
				want++
			}
		}
		hits, err := c.Search(ctx, st.BuildID, q, 32, nil, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != min(want, 32) {
			t.Fatalf("filter %+v: %d hits, %d rows match", f, len(hits), want)
		}
		for _, h := range hits {
			if !f.Matches(st.Attr(h.Row)) {
				t.Fatalf("filter %+v: row %d does not match", f, h.Row)
			}
		}
	}
	// A filter in one request doesn't leak into the next request using the same slot.
	for i := 0; i < 16; i++ {
		if hits, _ := c.Search(ctx, st.BuildID, q, 10, nil, nil); len(hits) != 10 || hits[0].Row != 42 {
			t.Fatalf("unfiltered search after filtered ones: %+v", hits)
		}
	}

	// More concurrent requests than slots.
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			scale, q8 := st.Vector(uint32(g))
			q := make([]float32, st.Dim)
			for i, x := range q8 {
				q[i] = float32(x) * scale
			}
			hits, err := c.Search(ctx, st.BuildID, q, 5, nil, nil)
			if err == nil && hits[0].Row != uint32(g) {
				err = os.ErrInvalid
			}
			errs <- err
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent search: %v", err)
		}
	}

	// A request made against a different build of the index is refused, not answered with
	// row numbers that mean something else.
	if _, err := c.Search(ctx, st.BuildID+1, q, 5, nil, nil); !errors.Is(err, shm.ErrStale) {
		t.Fatalf("request for another index build: got %v, want ErrStale", err)
	}

	// Stopping vecdb is noticed immediately.
	cmd.Process.Signal(os.Interrupt)
	cmd.Wait()
	if c.Alive() {
		t.Fatal("client still thinks vecdb is alive")
	}
	if _, err := c.Search(ctx, st.BuildID, q, 5, nil, nil); err == nil {
		t.Fatal("search succeeded with vecdb down")
	}
}
