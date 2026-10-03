package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// placeholderIndex is served when the binary was built without the embedded
// web assets (plain `go build` / `go install`). Production builds embed the
// real console via `make build` (which builds the web app and compiles with
// the `withassets` tag); development serves the UI from Vite.
const placeholderIndex = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Steward</title>
  </head>
  <body>
    <div id="root"></div>
    <p style="font-family: sans-serif; padding: 24px; color: #5d5d6b;">
      Web assets are not embedded in this build. Run <code>make build</code>
      to produce a binary with the console bundled in.
    </p>
  </body>
</html>
`

func Handler() http.Handler {
	sub, ok := distFS()
	if !ok {
		return placeholderHandler()
	}
	return handler(sub)
}

func handler(sub fs.FS) http.Handler {
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return placeholderHandler()
	}
	// Embedded files carry no modification time, so validators come from
	// their content instead.
	etags := contentETags(sub)
	fileServer := http.FileServer(http.FS(sub))
	return middleware.Compress(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "." || name == "" || name == "index.html" {
			serveIndex(w, r, index, etags["index.html"])
			return
		}
		// Only regular files come from the bundle. Directories, such as the
		// bundle's assets/ folder that shares its name with the Resources
		// route, fall through to the console instead of a file listing.
		if info, err := fs.Stat(sub, name); err == nil && !info.IsDir() {
			w.Header().Set("ETag", etags[name])
			if strings.HasPrefix(name, "assets/") {
				// Built bundle files are content-hashed by name.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		if missingBundleFile(name) {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r, index, etags["index.html"])
	}))
}

func contentETags(sub fs.FS) map[string]string {
	etags := map[string]string{}
	_ = fs.WalkDir(sub, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(sub, name)
		if err != nil {
			return nil
		}
		digest := sha256.Sum256(data)
		etags[name] = `"` + hex.EncodeToString(digest[:16]) + `"`
		return nil
	})
	return etags
}

// missingBundleFile tells a request for a built file that no longer exists,
// such as a stale tab asking for an old script, from a console route like
// /assets/<asset id>. Answering it with the HTML shell would make the browser
// run HTML as JavaScript.
func missingBundleFile(name string) bool {
	return strings.HasPrefix(name, "assets/") && path.Ext(name) != ""
}

// placeholderHandler serves the SPA shell placeholder for page routes and 404s
// for missing asset requests, mirroring the embedded handler's contract.
func placeholderHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if missingBundleFile(name) {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r, []byte(placeholderIndex), "")
	})
}

// serveIndex makes browsers revalidate the console shell so a new build's
// hashed bundle names are picked up on the next load.
func serveIndex(w http.ResponseWriter, r *http.Request, index []byte, etag string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
}
