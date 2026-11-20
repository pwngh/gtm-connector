package httptransport

import (
	"net/http"
	"path"
	"strings"
)

// The app's HTML may load only its own scripts and styles. Inline style
// attributes stay allowed: the app's animation library sets them.
const appPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// reserved paths have their own handlers, or none; they never get the app.
var reserved = []string{"/api", "/mcp", "/.well-known", "/healthz"}

// serveApp serves a file of the web app's build, or index.html for a client
// route. A path with a file extension, anything under /assets/, and any
// reserved prefix (routed elsewhere) never falls back to index.html.
func (s *Server) serveApp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET.")
		return
	}
	if s.web == nil {
		http.Error(w, "The web app is not built.", http.StatusNotFound)
		return
	}
	clean := path.Clean("/" + r.URL.Path)
	for _, prefix := range reserved {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			writeError(w, http.StatusNotFound, "not_found", "Not found.")
			return
		}
	}
	name := strings.TrimPrefix(clean, "/")
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") {
			http.Error(w, "Not found.", http.StatusNotFound)
			return
		}
	}
	if name != "" && name != "index.html" && s.serveFile(w, r, name) {
		return
	}
	if clean == "/assets" || strings.HasPrefix(clean, "/assets/") || path.Ext(clean) != "" && clean != "/index.html" {
		http.Error(w, "Not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Security-Policy", appPolicy)
	w.Header().Set("Cache-Control", "no-cache")
	if !s.serveFile(w, r, "index.html") {
		http.Error(w, "The web app is not built.", http.StatusNotFound)
	}
}

// serveFile serves one regular file from the build, through os.Root so no
// path or link leads outside it. It reports whether it served the file.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name string) bool {
	file, err := s.web.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if strings.HasPrefix(name, "assets/") {
		// Vite names assets by their content's hash.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	return true
}
