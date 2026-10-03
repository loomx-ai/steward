package webui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
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
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "." || name == "" || name == "index.html" {
			serveIndex(w, index)
			return
		}
		// Only regular files come from the bundle. Directories, such as the
		// bundle's assets/ folder that shares its name with the Resources
		// route, fall through to the console instead of a file listing.
		if info, err := fs.Stat(sub, name); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		if missingBundleFile(name) {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, index)
	})
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
		serveIndex(w, []byte(placeholderIndex))
	})
}

func serveIndex(w http.ResponseWriter, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(index)
}
