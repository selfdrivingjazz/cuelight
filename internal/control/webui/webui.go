// Package webui serves the browser apps compiled into cuelight-control:
// controller-web at / and website-node at /node/.
//
// The builds are copied into assets/ by `node scripts/embed-web.mjs` (run by
// `pnpm build:web` and scripts/build-release.sh) before `go build`. A binary
// built without them still serves the API and answers app routes with a page
// explaining how to build the web assets.
package webui

import (
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:assets
var embedded embed.FS

// App names under assets/.
const (
	ControllerWeb = "controller-web"
	WebsiteNode   = "website-node"
)

// Handler serves the named embedded app as a single-page application: existing
// files are served directly (hashed /assets/ files cached immutably) and every
// other path falls back to index.html.
func Handler(app string) http.Handler {
	sub, err := fs.Sub(embedded, path.Join("assets", app))
	if err != nil {
		panic(err)
	}
	return spaHandler(app, sub)
}

func spaHandler(app string, files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" {
			if info, err := fs.Stat(files, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				serveFile(w, r, files, name)
				return
			}
		}
		if _, err := fs.Stat(files, "index.html"); errors.Is(err, fs.ErrNotExist) {
			notBuilt(w, app)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		serveFile(w, r, files, "index.html")
	})
}

func serveFile(w http.ResponseWriter, r *http.Request, files fs.FS, name string) {
	file, err := files.Open(name)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	defer file.Close()
	content, ok := file.(io.ReadSeeker)
	if !ok {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	// Embedded files have no modification time, so no Last-Modified is sent.
	http.ServeContent(w, r, name, time.Time{}, content)
}

func notBuilt(w http.ResponseWriter, app string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = io.WriteString(w, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Cuelight web assets not built</title></head>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; line-height: 1.5">
<h1>Cuelight web assets not built</h1>
<p>This cuelight-control binary was compiled without the <code>`+app+`</code> web app.
The API is running normally.</p>
<p>From the repository root run <code>pnpm build:web</code> (builds controller-web and
website-node and copies them into <code>internal/control/webui/assets/</code>), then rebuild
cuelight-control. Release builds (<code>scripts/build-release.sh</code>) always include them.</p>
</body></html>
`)
}
