package feed

import (
	"testing"

	"papertok/server/internal/shm"
)

func TestSampleFavoursHighScores(t *testing.T) {
	hits := make([]shm.Hit, 100)
	for i := range hits {
		hits[i] = shm.Hit{Row: uint32(i), Score: 0.9 - float32(i)*0.005}
	}
	counts := make([]int, 100)
	for trial := 0; trial < 2000; trial++ {
		got := sample(hits, 8, 0.05)
		seen := map[uint32]bool{}
		for _, h := range got {
			if seen[h.Row] {
				t.Fatal("duplicate in sample")
			}
			seen[h.Row] = true
			counts[h.Row]++
		}
	}
	top, bottom := 0, 0
	for i := 0; i < 10; i++ {
		top += counts[i]
		bottom += counts[90+i]
	}
	if top <= 5*bottom {
		t.Fatalf("top %d vs bottom %d", top, bottom)
	}
}
