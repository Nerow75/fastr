package httpapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// assetHandler serves the embedded bundle and falls back to index.html for
// client-side routes, so a phone landing on /pair gets the application rather
// than a 404.
func assetHandler(bundle fs.FS) http.Handler {
	files := http.FileServer(http.FS(bundle))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "" || clean == "." {
			serveIndex(w, bundle)
			return
		}

		if _, err := fs.Stat(bundle, clean); err != nil {
			// Unknown path: hand it to the client-side router.
			serveIndex(w, bundle)
			return
		}

		// Hashed asset names make the content immutable, so it can be cached hard.
		// index.html must not be, or an update never reaches an open phone.
		if strings.HasPrefix(clean, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		securityHeaders(w)
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, bundle fs.FS) {
	index, err := fs.ReadFile(bundle, "index.html")
	if err != nil {
		http.Error(w, "web bundle unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	securityHeaders(w)
	_, _ = w.Write(index)
}

// securityHeaders states the local-only guarantee in a form the browser
// enforces, rather than leaving it as an intention. `connect-src 'self'` in
// particular means a compromised bundle still cannot exfiltrate anything.
func securityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; connect-src 'self'; font-src 'self'; "+
			"base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}
