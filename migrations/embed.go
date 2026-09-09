package migrations

import "embed"

// Files contains the schema shipped with this version of Steward.
//
//go:embed *.sql
var Files embed.FS
