package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"papertok/server/internal/store"
)

// The spliced JSON must decode to exactly what the struct path produced.
func TestWritePapersMatchesStructs(t *testing.T) {
	bin, _ := filepath.Abs("../../../vecdb/target/release/vecdb")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("vecdb binary not built")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"synth", "--n", "300", "--dim", "32", "--out", dir},
		{"build", "--papers", dir + "/papers.jsonl", "--embeddings", dir + "/embeddings.f32", "--dim", "32", "--nlist", "0", "--out", dir + "/index.bin"},
	} {
		if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	st, err := store.Open(dir+"/index.bin", dir+"/papers.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	rec := httptest.NewRecorder()
	s.writePapers(rec, st, []rowOut{{row: 3, score: 0.5, reason: "for-you"}, {row: 7, reason: "random"}}, `,"more":true`)

	var got struct {
		Papers []map[string]any `json:"papers"`
		More   bool             `json:"more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, rec.Body.String())
	}
	if len(got.Papers) != 2 || !got.More {
		t.Fatalf("got %d papers, more=%v", len(got.Papers), got.More)
	}
	for i, row := range []uint32{3, 7} {
		p, _ := st.Paper(row)
		want, _ := json.Marshal(feedPaper{Paper: p})
		var wantMap map[string]any
		json.Unmarshal(want, &wantMap)
		g := got.Papers[i]
		for _, k := range []string{"id", "title", "abstract", "authors", "year", "venue", "field", "cited_by"} {
			if !reflect.DeepEqual(g[k], wantMap[k]) {
				t.Errorf("row %d field %s: got %v want %v", row, k, g[k], wantMap[k])
			}
		}
		scale, q := st.Vector(row)
		vec, _ := base64.StdEncoding.DecodeString(g["vec"].(string))
		if len(vec) != len(q) || float32(g["scale"].(float64)) != scale || g["indexed"] != true {
			t.Errorf("row %d: vector fields wrong: %v", row, g)
		}
	}
	if got.Papers[0]["score"].(float64) != 0.5 || got.Papers[0]["reason"] != "for-you" {
		t.Errorf("score/reason wrong: %v", got.Papers[0])
	}
}
