// Package webui serves the dashboard, which is built from web/ by
// `npm run build` into dist/ and embedded into the controller binary.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// csp allows only same-origin scripts and API calls. ECharts sets inline
// style attributes, hence 'unsafe-inline' for styles only.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"connect-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

const notBuilt = `<!doctype html><meta charset="utf-8"><title>PathLedger</title>
<body style="font-family:system-ui;max-width:40rem;margin:4rem auto;padding:0 1rem">
<h1>Dashboard not built</h1>
<p>This controller was compiled without the web dashboard. Run <code>make web</code> (or <code>npm ci &amp;&amp; npm run build</code> in <code>web/</code>) and rebuild. The API is available under <code>/api/v1</code>.</p>`

// Handler serves the dashboard's static files. Hashed assets are cached for
// a year; index.html is never cached so new releases take effect at once.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed directive guarantees dist exists
	}
	_, statErr := fs.Stat(sub, "index.html")
	built := statErr == nil
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Frame-Options", "DENY")
		if !built {
			h.Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(notBuilt))
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if strings.HasPrefix(p, "assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		if p != "" && p != "index.html" {
			if _, err := fs.Stat(sub, p); err != nil {
				// Unknown path: serve the app shell; routing is client-side.
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		files.ServeHTTP(w, r)
	})
}
