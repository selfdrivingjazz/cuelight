package webnode

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// NewAppHandler serves the built website-node app from dir. Unknown paths
// fall back to index.html; hashed build assets are cached immutably.
func NewAppHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(clean)))
		if clean == "/" || err != nil || info.IsDir() {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		if strings.HasPrefix(clean, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
