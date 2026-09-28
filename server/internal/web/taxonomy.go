package web

import (
	"encoding/json"
	"os"
	"strings"
)

// taxonomy maps OpenAlex topics to PaperTik's categories (ingest/taxonomy.json, shared with the
// ingest service). The server needs it for live search results, which come straight from
// OpenAlex; papers in the index already carry their category.
type taxonomy struct {
	Subfields map[string]string `json:"subfields"`
	Topics    map[string]struct {
		Category *string `json:"category"` // nil: not computer science, excluded
	} `json:"topics"`
}

func loadTaxonomy(path string) *taxonomy {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil // labels fall back to OpenAlex's subfield names
	}
	var t taxonomy
	if json.Unmarshal(b, &t) != nil {
		return nil
	}
	return &t
}

func tail(id string) string { return id[strings.LastIndexByte(id, '/')+1:] }

// classify returns the category for an OpenAlex topic, and whether it is excluded.
func (t *taxonomy) classify(topicID, subfieldID, subfieldName string) (string, bool) {
	if t == nil {
		return subfieldName, false
	}
	if e, ok := t.Topics[tail(topicID)]; ok {
		if e.Category == nil {
			return "", true
		}
		return *e.Category, false
	}
	if c, ok := t.Subfields[tail(subfieldID)]; ok {
		return c, false
	}
	return subfieldName, false
}
