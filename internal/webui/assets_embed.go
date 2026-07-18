//go:build withassets

package webui

import (
	"embed"
	"io/fs"
)

// embedded holds the built web console. It is only compiled into the binary
// when the `withassets` build tag is set (see `make build`), so the generated
// bundle in dist/ does not need to be committed to source control.
//
//go:embed all:dist
var embedded embed.FS

func distFS() (fs.FS, bool) {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
