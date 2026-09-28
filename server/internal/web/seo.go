package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"papertok/server/internal/store"
)

// Search engines and link previews cannot run the SPA (and could not pass Turnstile if they
// did), so paper pages are rendered on the server: /p/W123 is index.html with the paper's
// title, description, Open Graph / Twitter tags, JSON-LD and a readable copy of the abstract.
// robots.txt and a sitemap (split into files of 50k URLs, the protocol limit) cover the index.

const sitemapChunk = 50_000

// baseURL is the public origin used in canonical links and sitemaps.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || strings.Contains(r.Header.Get("CF-Visitor"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// indexHTML caches web/dist/index.html, reloading it when the file changes (a new build).
type indexHTML struct {
	mu    sync.Mutex
	mtime time.Time
	body  []byte
}

func (h *indexHTML) get(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !st.ModTime().Equal(h.mtime) {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		h.body, h.mtime = b, st.ModTime()
	}
	return h.body, nil
}

var paperHead = template.Must(template.New("head").Parse(`<title>{{.Title}} · PaperTik</title>
    <meta name="description" content="{{.Description}}" />
    <link rel="canonical" href="{{.URL}}" />
    <meta property="og:type" content="article" />
    <meta property="og:site_name" content="PaperTik" />
    <meta property="og:title" content="{{.Title}}" />
    <meta property="og:description" content="{{.Description}}" />
    <meta property="og:url" content="{{.URL}}" />
    <meta property="og:image" content="{{.Base}}/og-image.png" />
    <meta property="og:image:width" content="1200" />
    <meta property="og:image:height" content="630" />
    {{if .FBAppID}}<meta property="fb:app_id" content="{{.FBAppID}}" />
    {{end}}<meta name="twitter:card" content="summary_large_image" />
    <meta name="twitter:title" content="{{.Title}}" />
    <meta name="twitter:description" content="{{.Description}}" />
    <meta name="citation_title" content="{{.Title}}" />
    {{range .Authors}}<meta name="citation_author" content="{{.}}" />
    {{end}}{{if .Year}}<meta name="citation_publication_date" content="{{.Year}}" />
    {{end}}{{if .DOI}}<meta name="citation_doi" content="{{.DOI}}" />
    {{end}}<script type="application/ld+json">{{.JSONLD}}</script>`))

var paperBody = template.Must(template.New("body").Parse(`<article id="seo" class="seo">
      <h1>{{.Title}}</h1>
      <p>{{.Byline}}</p>
      <p>{{.Abstract}}</p>
      <p><a href="{{.Link}}">Read the paper</a> · <a href="/">More papers on PaperTik</a></p>
    </article>`))

// GET /p/{id}: a shareable, indexable page for one paper; the SPA takes over once loaded.
func (s *Server) handlePaperPage(w http.ResponseWriter, r *http.Request) {
	st := s.snap().st
	page, err := s.index.get(filepath.Join(s.cfg.StaticDir, "index.html"))
	if err != nil {
		http.Error(w, "not built", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")

	id := strings.ToUpper(r.PathValue("id"))
	row, ok := st.Row(id)
	var p store.Paper
	if ok {
		p, err = st.Paper(row)
		ok = err == nil
	}
	if !ok {
		// Not in the index (yet): the SPA can still show it from OpenAlex, but don't index it.
		out := bytes.Replace(page, []byte("</head>"), []byte(`<meta name="robots" content="noindex" />
  </head>`), 1)
		w.Write(out)
		return
	}

	base := s.baseURL(r)
	url := base + "/p/" + p.ID
	desc := p.Abstract
	if len(desc) > 300 {
		cut := strings.LastIndexByte(desc[:300], ' ')
		desc = desc[:max(cut, 200)] + "…"
	}
	link := p.PDFURL
	if link == "" {
		link = p.URL
	}
	if link == "" && p.DOI != "" {
		link = p.DOI
	}
	if link == "" {
		link = "https://openalex.org/" + p.ID
	}
	parts := []string{strings.Join(p.Authors, ", "), p.Venue}
	if p.Year != 0 {
		parts = append(parts, strconv.Itoa(p.Year))
	}
	var nonEmpty []string
	for _, x := range parts {
		if x != "" {
			nonEmpty = append(nonEmpty, x)
		}
	}
	byline := strings.Join(nonEmpty, " · ")

	ld := map[string]any{
		"@context":            "https://schema.org",
		"@type":               "ScholarlyArticle",
		"headline":            p.Title,
		"name":                p.Title,
		"abstract":            p.Abstract,
		"url":                 url,
		"sameAs":              []string{"https://openalex.org/" + p.ID},
		"isAccessibleForFree": p.PDFURL != "",
	}
	if p.Year != 0 {
		ld["datePublished"] = strconv.Itoa(p.Year)
	}
	if p.DOI != "" {
		ld["identifier"] = p.DOI
	}
	if p.Venue != "" {
		ld["isPartOf"] = map[string]string{"@type": "Periodical", "name": p.Venue}
	}
	if p.Field != "" {
		ld["about"] = p.Field
	}
	var authors []map[string]string
	for _, a := range p.Authors {
		authors = append(authors, map[string]string{"@type": "Person", "name": a})
	}
	if authors != nil {
		ld["author"] = authors
	}
	ldJSON, _ := json.Marshal(ld)

	data := map[string]any{
		"Title": p.Title, "Description": desc, "URL": url, "Base": base, "FBAppID": s.cfg.FacebookAppID,
		"Authors": p.Authors, "Year": p.Year, "DOI": p.DOI,
		"JSONLD": template.JS(ldJSON), // json.Marshal escapes <, > and & so this cannot close the script tag
		"Byline": byline, "Abstract": p.Abstract, "Link": link,
	}
	var head, body bytes.Buffer
	if paperHead.Execute(&head, data) != nil || paperBody.Execute(&body, data) != nil {
		w.Write(page)
		return
	}
	out := page
	// Replace the default <title> … </title> and the generic description/OG tags.
	if i, j := bytes.Index(out, []byte("<!--seo-->")), bytes.Index(out, []byte("<!--/seo-->")); i >= 0 && j > i {
		out = append(append(append([]byte{}, out[:i]...), head.Bytes()...), out[j+len("<!--/seo-->"):]...)
	}
	out = bytes.Replace(out, []byte(`<div id="app"></div>`), append([]byte("<div id=\"app\"></div>\n    "), body.Bytes()...), 1)
	w.Write(out)
}

// GET /robots.txt
func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "User-agent: *\nAllow: /\nDisallow: /api/\n\nSitemap: %s/sitemap.xml\n", s.baseURL(r))
}

// GET /sitemap.xml: a sitemap index pointing at /sitemaps/{n}.xml.
func (s *Server) handleSitemapIndex(w http.ResponseWriter, r *http.Request) {
	st := s.snap().st
	base := s.baseURL(r)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+`<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`+"\n")
	for i := 0; i*sitemapChunk < st.N; i++ {
		fmt.Fprintf(w, "  <sitemap><loc>%s/sitemaps/%d.xml</loc></sitemap>\n", base, i)
	}
	fmt.Fprint(w, "</sitemapindex>\n")
}

// GET /sitemaps/{n}.xml: up to 50k paper URLs.
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	st := s.snap().st
	n, err := strconv.Atoi(strings.TrimSuffix(r.PathValue("file"), ".xml"))
	if err != nil || n < 0 || n*sitemapChunk >= st.N {
		http.NotFound(w, r)
		return
	}
	base := s.baseURL(r)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`+"\n")
	if n == 0 {
		fmt.Fprintf(w, "  <url><loc>%s/</loc><changefreq>daily</changefreq></url>\n", base)
	}
	for row := n * sitemapChunk; row < min((n+1)*sitemapChunk, st.N); row++ {
		fmt.Fprintf(w, "  <url><loc>%s/p/W%d</loc></url>\n", base, st.ID(uint32(row)))
	}
	fmt.Fprint(w, "</urlset>\n")
}
