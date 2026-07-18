//go:build !withassets

package webui

import "io/fs"

// distFS reports no embedded assets. This is the default build (plain
// `go build` / `go install` / `go test`), which does not require the generated
// dist/ bundle to exist. Build with `-tags withassets` (via `make build`) to
// embed the console.
func distFS() (fs.FS, bool) { return nil, false }
