package relational

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"gorm.io/gorm"
)

// assetSearchText is the document a keyword asset search matches: the asset's
// identifiers, display fields, tags and every scalar value of its normalized
// configuration such as IPs and related resource IDs, lowercased and one per
// line so a term never matches across two values. Keys are sorted so an
// unchanged asset rewrites the same document and its index entry stays put.
func assetSearchText(value asset.Asset) string {
	var document strings.Builder
	add := func(text string) {
		if text == "" {
			return
		}
		if document.Len() > 0 {
			document.WriteByte('\n')
		}
		// PostgreSQL text cannot hold U+0000.
		document.WriteString(strings.ReplaceAll(strings.ToLower(text), "\x00", ""))
	}
	for _, text := range []string{
		string(value.ID), value.Identity.NativeID, value.Identity.NativeType, string(value.ResourceKindID),
		value.Name, value.State, value.Location,
	} {
		add(text)
	}
	for _, key := range slices.Sorted(maps.Keys(value.Tags)) {
		add(key)
		add(value.Tags[key])
	}
	var addValues func(any)
	addValues = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for _, key := range slices.Sorted(maps.Keys(typed)) {
				addValues(typed[key])
			}
		case []any:
			for _, item := range typed {
				addValues(item)
			}
		case string:
			add(typed)
		case json.Number:
			add(typed.String())
		case float64:
			add(strconv.FormatFloat(typed, 'f', -1, 64))
		case bool:
			add(strconv.FormatBool(typed))
		}
	}
	addValues(value.Normalized)
	return document.String()
}

// whereKeywordMatches keeps the assets whose search document contains term.
// SQLite answers it from the trigram FTS5 index over search_text, PostgreSQL
// from the pg_trgm GIN index on it; both only scan when term is shorter than a
// trigram.
func whereKeywordMatches(query *gorm.DB, term string) *gorm.DB {
	term = strings.ToLower(term)
	if query.Dialector.Name() == "sqlite" {
		// FTS5 serves GLOB, not LIKE ... ESCAPE, from the index.
		return query.Where(`assets.id IN (
			SELECT asset_search_rows.asset_id
			FROM asset_search
			JOIN asset_search_rows ON asset_search_rows.id = asset_search.rowid
			WHERE asset_search.document GLOB ?
		)`, "*"+escapeGlob(term)+"*")
	}
	return query.Where("assets.search_text LIKE ? ESCAPE '\\'", "%"+escapeLike(term)+"%")
}

func escapeGlob(value string) string {
	var escaped strings.Builder
	for _, character := range value {
		switch character {
		case '*', '?', '[':
			escaped.WriteByte('[')
			escaped.WriteRune(character)
			escaped.WriteByte(']')
		default:
			escaped.WriteRune(character)
		}
	}
	return escaped.String()
}
