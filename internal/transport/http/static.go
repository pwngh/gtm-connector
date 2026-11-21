package httptransport

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// basePolicy is the Content-Security-Policy of every response: scripts,
// styles, fonts and requests come from this origin only.
// Inline style attributes stay allowed: the app's animation library sets them.
const basePolicy = "default-src 'self'; script-src 'self'%s; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
	"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

var inlineScript = regexp.MustCompile(`(?is)<script(\s[^>]*)?>(.*?)</script>`)

// policyFor allows each inline script of the build's index.html by its
// SHA-256, so the shell's theme script runs and no other inline script does.
func policyFor(web fs.FS) (string, error) {
	file, err := web.Open("index.html")
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf(basePolicy, ""), nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	page, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		return "", err
	}
	var hashes strings.Builder
	for _, match := range inlineScript.FindAllSubmatch(page, -1) {
		if strings.Contains(strings.ToLower(string(match[1])), "src=") || len(match[2]) == 0 {
			continue
		}
		sum := sha256.Sum256(match[2])
		hashes.WriteString(" 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'")
	}
	return fmt.Sprintf(basePolicy, hashes.String()), nil
}

// contentTypes is the fixed table of what the build serves. Anything else is
// a download: application/octet-stream, never sniffed.
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".webp":        "image/webp",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".txt":         "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json",
}

// reserved paths have their own handlers, or none; they never get the app.
var reserved = []string{"/api", "/mcp", "/.well-known", "/healthz"}

// underReserved reports whether a path is a reserved prefix or below one.
func underReserved(p string) bool {
	for _, prefix := range reserved {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// serveApp serves a file of the web app's build, or index.html for a client
// route. Only "/" and the client route prefixes fall back to index.html; a
// path with a file extension, anything under /assets/, and any reserved
// prefix (routed elsewhere) never does. Any other path without an extension
// gets the build's 404.html, when it has one.
//
// A browser keeps a file under assets/ for a year without asking again: the
// build names each by its content's hash. It asks about every other file on
// each use, index.html and so every client route among them: the page names
// the build's assets, and a file such as robots.txt keeps its name from one
// build to the next. Those are served as no-cache with an ETag of their
// bytes, and a request whose If-None-Match names them gets 304.
func (s *Server) serveApp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET.")
		return
	}
	clean := path.Clean("/" + r.URL.Path)
	if underReserved(clean) {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	if s.web == nil {
		http.Error(w, "The web app is not built.", http.StatusNotFound)
		return
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
	if !s.clientRoute(clean) {
		asset := clean == "/assets" || strings.HasPrefix(clean, "/assets/")
		if asset || path.Ext(clean) != "" || !s.serveNotFoundPage(w, r) {
			http.Error(w, "Not found.", http.StatusNotFound)
		}
		return
	}
	if !s.serveFile(w, r, "index.html") {
		w.Header().Set("Cache-Control", "no-cache")
		http.Error(w, "The web app is not built.", http.StatusNotFound)
	}
}

// serveNotFoundPage answers with the build's 404.html and status 404. It
// reports whether the build has that page.
func (s *Server) serveNotFoundPage(w http.ResponseWriter, r *http.Request) bool {
	page, err := fs.ReadFile(s.web, "404.html")
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", contentTypes[".html"])
	w.Header().Set("Content-Length", strconv.Itoa(len(page)))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		w.Write(page)
	}
	return true
}

func (s *Server) clientRoute(clean string) bool {
	if clean == "/" || clean == "/index.html" {
		return true
	}
	for _, prefix := range s.cfg.ClientRoutes {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return path.Ext(clean) == ""
		}
	}
	return false
}

// serveFile serves one regular file from the build, with a content type
// from the fixed table. A folder is read through os.Root, so no path or link
// leads outside it. It reports whether it served the file.
//
// A file outside assets/ is read once into memory, and its tag and its body
// both come from those bytes. A folder that is built again while a request
// is served cannot send one build's bytes under another's tag.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name string) bool {
	file, err := s.web.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	content, seekable := file.(io.ReadSeeker)
	if err != nil || !info.Mode().IsRegular() || !seekable {
		return false
	}
	kind, ok := contentTypes[strings.ToLower(path.Ext(name))]
	if !ok {
		kind = "application/octet-stream"
		w.Header().Set("Content-Disposition", "attachment")
	}
	w.Header().Set("Content-Type", kind)
	if strings.HasPrefix(name, "assets/") {
		// Vite names assets by their content's hash.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeContent(w, r, info.Name(), info.ModTime(), content)
		return true
	}
	body, err := io.ReadAll(content)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(body)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", `"`+hex.EncodeToString(digest[:])+`"`)
	// ServeContent answers If-None-Match from the tag. The file's time is left out: the tag alone says
	// whether a browser's copy is these bytes.
	http.ServeContent(w, r, info.Name(), time.Time{}, bytes.NewReader(body))
	return true
}
