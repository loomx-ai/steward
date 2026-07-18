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
		if _, err := fs.Stat(sub, name); err != nil {
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
			serveIndex(w, index)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// placeholderHandler serves the SPA shell placeholder for page routes and 404s
// for missing asset requests, mirroring the embedded handler's contract.
func placeholderHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if strings.HasPrefix(name, "assets/") {
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
