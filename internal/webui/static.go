package webui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
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
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return placeholderHandler()
	}
	// Embedded files carry no modification time, so validators come from
	// their content. Text files are gzipped once here instead of per request.
	files := loadBundle(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "." || name == "" {
			name = "index.html"
		}
		// Only regular files come from the bundle. Directories, such as the
		// bundle's assets/ folder that shares its name with the Resources
		// route, fall through to the console instead of a file listing.
		file, ok := files[name]
		if !ok && missingBundleFile(name) {
			http.NotFound(w, r)
			return
		}
		switch {
		case !ok || name == "index.html":
			// Browsers revalidate the console shell so a new build's hashed
			// bundle names are picked up on the next load.
			name, file = "index.html", files["index.html"]
			w.Header().Set("Cache-Control", "no-cache")
		case strings.HasPrefix(name, "assets/"):
			// Built bundle files are content-hashed by name.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			// Icons and brand images keep stable names across releases.
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		file.serve(w, r, name)
	})
}

type bundleFile struct {
	data, gzipped []byte
	etag          string
}

func (f bundleFile) serve(w http.ResponseWriter, r *http.Request, name string) {
	data, etag := f.data, f.etag
	if f.gzipped != nil {
		w.Header().Add("Vary", "Accept-Encoding")
		if acceptsGzip(r.Header.Get("Accept-Encoding")) {
			data, etag = f.gzipped, strings.TrimSuffix(etag, `"`)+`-gzip"`
			w.Header().Set("Content-Encoding", "gzip")
		}
	}
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = http.DetectContentType(f.data)
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", etag)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func loadBundle(sub fs.FS) map[string]bundleFile {
	files := map[string]bundleFile{}
	_ = fs.WalkDir(sub, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(sub, name)
		if err != nil {
			return nil
		}
		digest := sha256.Sum256(data)
		file := bundleFile{data: data, etag: `"` + hex.EncodeToString(digest[:16]) + `"`}
		switch path.Ext(name) {
		case ".html", ".js", ".css", ".svg", ".json", ".ico", ".txt":
			if gzipped := gzipBytes(data); len(gzipped) < len(data) {
				file.gzipped = gzipped
			}
		}
		files[name] = file
		return nil
	})
	return files
}

func gzipBytes(data []byte) []byte {
	var buf bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = writer.Write(data)
	_ = writer.Close()
	return buf.Bytes()
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip, honoring
// an explicit q=0 refusal.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		q, ok := strings.CutPrefix(strings.ReplaceAll(strings.TrimSpace(params), " ", ""), "q=")
		return !ok || strings.Trim(q, "0.") != ""
	}
	return false
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
