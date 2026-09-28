package web

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// The app's pages (/, /discover, /search, /me, /status) are the same single-page app, but each
// gets its own title, description, canonical URL and Open Graph tags from the server, so search
// engines and link previews see a real page. The home page also carries a readable
// introduction with links to papers and fields (crawlers follow links in the HTML; the app
// replaces this block once it boots).

type route struct {
	title, description string
	index              bool // allow search engines to index it
}

var routes = map[string]route{
	"/": {
		title:       "PaperTik · TikTok-style feed of computer science papers",
		description: "Swipe through computer science research papers one abstract at a time. Like what you'd read and the feed learns your taste. Free, no sign-up.",
		index:       true,
	},
	"/discover": {
		title:       "Discover computer science papers · PaperTik",
		description: "Browse the most cited and most recent computer science papers by field: AI, computer vision, networks, theory, systems and more.",
		index:       true,
	},
	"/search": {title: "Search papers · PaperTik", description: "Search computer science research papers on PaperTik."},
	"/me":     {title: "Personal · PaperTik", description: "Your likes, bookmarks and taste vector, stored only in your browser."},
	"/status": {title: "System status · PaperTik", description: "Live status of PaperTik: traffic, papers indexed and ingest progress."},
}

var shellHead = template.Must(template.New("shell").Parse(`<title>{{.Title}}</title>
    <meta name="description" content="{{.Description}}" />
    <link rel="canonical" href="{{.URL}}" />
    {{if not .Index}}<meta name="robots" content="noindex" />
    {{end}}<meta property="og:type" content="website" />
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
    <script type="application/ld+json">{{.JSONLD}}</script>`))

var homeBody = template.Must(template.New("home").Parse(`<article id="seo" class="seo">
      <h1>PaperTik: a TikTok-style feed of computer science papers</h1>
      <p>PaperTik shows one research paper at a time: its title, authors and abstract. Swipe to the
      next one, and like the papers you would read. Every like tunes your feed, using SPECTER
      embeddings of {{.Count}} papers from OpenAlex. No account needed: your likes stay in your browser.</p>
      <p><a href="/">Open the feed</a> · <a href="/discover">Discover papers by field</a> · <a href="/search">Search</a></p>
      {{if .Papers}}<h2>Most cited papers</h2>
      <ul>{{range .Papers}}
        <li><a href="/p/{{.ID}}">{{.Title}}</a>{{if .Year}} ({{.Year}}){{end}}</li>{{end}}
      </ul>{{end}}
      {{if .Fields}}<h2>Browse by field</h2>
      <ul>{{range .Fields}}
        <li><a href="/discover?field={{.Query}}">{{.Name}}</a> ({{.Count}} papers)</li>{{end}}
      </ul>{{end}}
    </article>`))

// renderShell serves index.html for an app route with that route's head tags.
func (s *Server) renderShell(w http.ResponseWriter, r *http.Request) {
	page, err := s.index.get(filepath.Join(s.cfg.StaticDir, "index.html"))
	if err != nil {
		http.Error(w, "not built", http.StatusInternalServerError)
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	rt, known := routes[path]
	status := http.StatusOK
	if !known {
		// The app still loads (and shows the feed), but tell crawlers this URL is not a page.
		rt, status = routes["/"], http.StatusNotFound
		rt.index = false
	}
	base := s.baseURL(r)
	ld := `{"@context":"https://schema.org","@type":"WebSite","name":"PaperTik","url":"` + base + `/"}`
	var head bytes.Buffer
	shellHead.Execute(&head, map[string]any{
		"Title": rt.title, "Description": rt.description, "URL": base + path, "Base": base,
		"Index": rt.index, "FBAppID": s.cfg.FacebookAppID, "JSONLD": template.JS(ld),
	})
	if i, j := bytes.Index(page, []byte("<!--seo-->")), bytes.Index(page, []byte("<!--/seo-->")); i >= 0 && j > i {
		page = append(append(append([]byte{}, page[:i]...), head.Bytes()...), page[j+len("<!--/seo-->"):]...)
	}
	if path == "/" && known {
		page = bytes.Replace(page, []byte(`<div id="app"></div>`), append([]byte("<div id=\"app\"></div>\n    "), s.homeBody().Bytes()...), 1)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	w.Write(page)
}

func (s *Server) homeBody() *bytes.Buffer {
	sn := s.snap()
	type link struct {
		ID, Title string
		Year      int
	}
	type field struct {
		Name, Query string
		Count       int
	}
	var papers []link
	var fields []field
	sn.explore.mu.RLock()
	for _, row := range sn.explore.cited[exploreAll][:min(30, len(sn.explore.cited[exploreAll]))] {
		if p, err := sn.st.Paper(row); err == nil {
			papers = append(papers, link{p.ID, p.Title, p.Year})
		}
	}
	for _, f := range sn.explore.fields {
		fields = append(fields, field{f.Name, url.QueryEscape(f.Name), f.Count})
	}
	sn.explore.mu.RUnlock()
	var b bytes.Buffer
	homeBody.Execute(&b, map[string]any{"Count": formatCount(sn.st.N), "Papers": papers, "Fields": fields})
	return &b
}

// formatCount writes 331210 as "331,210".
func formatCount(n int) string {
	d := strconv.Itoa(n)
	var b strings.Builder
	for i := range d {
		if i > 0 && (len(d)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(d[i])
	}
	return b.String()
}

// canonicalHost redirects www.<public host> to the public host (301), so search engines see
// one site. Only when PUBLIC_URL is set; any other host (localhost, the tunnel) is untouched.
func (s *Server) canonicalHost(next http.Handler) http.Handler {
	u, err := url.Parse(s.cfg.PublicURL)
	if s.cfg.PublicURL == "" || err != nil || u.Host == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Host, "www."+u.Host) {
			http.Redirect(w, r, u.Scheme+"://"+u.Host+r.URL.RequestURI(), http.StatusMovedPermanently)
			return
		}
		next.ServeHTTP(w, r)
	})
}
