package asset

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

type ChangeType string

const (
	ChangeAdded    ChangeType = "added"
	ChangeRemoved  ChangeType = "removed"
	ChangeModified ChangeType = "modified"
)

func (t ChangeType) Valid() bool {
	return t == ChangeAdded || t == ChangeRemoved || t == ChangeModified
}

const (
	maxChangedFields     = 100
	maxChangeValueLength = 512
	maxChangePathDepth   = 8
)

// FieldChange is one flattened attribute that differs between two consecutive
// projections of the same asset. A nil Before means the attribute appeared; a
// nil After means it disappeared.
type FieldChange struct {
	Path   string `json:"path"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// AssetChange records what a scan changed about one asset: it appeared, it
// disappeared, or its projected attributes differ from the previous scan.
type AssetChange struct {
	ID             string         `json:"id"`
	ConnectionID   ConnectionID   `json:"connection_id"`
	ScanTaskID     ScanTaskID     `json:"scan_task_id"`
	AssetID        AssetID        `json:"asset_id"`
	Type           ChangeType     `json:"change_type"`
	ResourceKindID ResourceKindID `json:"resource_kind_id"`
	NativeType     string         `json:"native_type"`
	NativeID       string         `json:"native_id"`
	Name           string         `json:"name,omitempty"`
	Location       string         `json:"location,omitempty"`
	Fields         []FieldChange  `json:"fields,omitempty"`
	ChangedAt      time.Time      `json:"changed_at"`
}

type ChangeCounts struct {
	Added    int `json:"added"`
	Removed  int `json:"removed"`
	Modified int `json:"modified"`
}

func (c *ChangeCounts) Add(changeType ChangeType, count int) {
	switch changeType {
	case ChangeAdded:
		c.Added += count
	case ChangeRemoved:
		c.Removed += count
	case ChangeModified:
		c.Modified += count
	}
}

// NewAssetChange describes an asset as it stands; callers set Type, scan and
// time fields.
func NewAssetChange(value Asset) AssetChange {
	return AssetChange{
		ConnectionID: value.Identity.ConnectionID, AssetID: value.ID, ResourceKindID: value.ResourceKindID,
		NativeType: value.Identity.NativeType, NativeID: value.Identity.NativeID, Name: value.Name, Location: value.Location,
	}
}

// DiffAssets compares the attributes a user sees on an asset: name, state,
// location, tags and the provider-normalized configuration. Raw provider
// payloads are deliberately ignored; they carry volatile counters and
// timestamps that would turn every scan into a change.
func DiffAssets(before, after Asset) []FieldChange {
	previous := comparableAttributes(before)
	current := comparableAttributes(after)
	paths := slices.Sorted(maps.Keys(previous))
	for path := range current {
		if _, ok := previous[path]; !ok {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	var changes []FieldChange
	for _, path := range paths {
		left, hadLeft := previous[path]
		right, hadRight := current[path]
		if hadLeft && hadRight && left == right {
			continue
		}
		change := FieldChange{Path: path}
		if hadLeft {
			change.Before = boundedValue(left)
		}
		if hadRight {
			change.After = boundedValue(right)
		}
		changes = append(changes, change)
		if len(changes) == maxChangedFields {
			break
		}
	}
	return changes
}

// MergeAssetChange folds a later change for the same asset in the same scan
// into the earlier one. Several sources or retried targets can observe one
// asset during a scan; the recorded change must describe the scan as a whole.
// keep is false when the two cancel out.
func MergeAssetChange(existing, next AssetChange) (merged AssetChange, keep bool) {
	next.ID = existing.ID
	switch {
	case existing.Type == ChangeAdded && next.Type == ChangeRemoved:
		return AssetChange{}, false
	case existing.Type == ChangeAdded:
		next.Type = ChangeAdded
		next.Fields = nil
		return next, true
	case next.Type == ChangeRemoved:
		next.Fields = nil
		return next, true
	case existing.Type == ChangeRemoved:
		// The asset was seen again later in the same scan, so it never left.
		return AssetChange{}, false
	case next.Type == ChangeAdded:
		next.Fields = nil
		return next, true
	}
	byPath := make(map[string]FieldChange, len(existing.Fields)+len(next.Fields))
	for _, field := range existing.Fields {
		byPath[field.Path] = field
	}
	for _, field := range next.Fields {
		if previous, ok := byPath[field.Path]; ok {
			field.Before = previous.Before
		}
		byPath[field.Path] = field
	}
	next.Fields = next.Fields[:0:0]
	for _, path := range slices.Sorted(maps.Keys(byPath)) {
		field := byPath[path]
		if canonicalValue(field.Before) == canonicalValue(field.After) {
			continue
		}
		next.Fields = append(next.Fields, field)
	}
	if len(next.Fields) == 0 {
		return AssetChange{}, false
	}
	return next, true
}

// volatileAttribute matches bookkeeping fields that change without the
// resource changing in a way a person cares about: update times, entity tags,
// fingerprints and which inventory source reported the resource.
var volatileAttribute = regexp.MustCompile(`(?i)^(_inventory_source|etag|.*fingerprint|updated(at|time|date)?|update(d)?(time|date)|last.*(reported|modified|updated|seen|sync|synced|refresh|refreshed).*|modif(ied|y)(at|time|date)?)$`)

func isVolatile(path string) bool {
	return volatileAttribute.MatchString(path[strings.LastIndex(path, ".")+1:])
}

func comparableAttributes(value Asset) map[string]string {
	result := map[string]string{}
	flattenValue(result, "", map[string]any(value.Normalized), 0)
	for path := range result {
		if isVolatile(path) {
			delete(result, path)
		}
	}
	for path, attribute := range map[string]string{"name": value.Name, "state": value.State, "location": value.Location} {
		delete(result, path)
		if attribute != "" {
			result[path] = canonicalValue(attribute)
		}
	}
	for path := range result {
		if len(path) > len("tags.") && path[:len("tags.")] == "tags." {
			delete(result, path)
		}
	}
	for key, tag := range value.Tags {
		result["tags."+key] = canonicalValue(tag)
	}
	return result
}

func flattenValue(result map[string]string, prefix string, value any, depth int) {
	if object, ok := value.(map[string]any); ok && depth < maxChangePathDepth {
		for key, nested := range object {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			flattenValue(result, path, nested, depth+1)
		}
		return
	}
	if prefix == "" || value == nil {
		return
	}
	result[prefix] = canonicalValue(value)
}

// canonicalValue gives a stable comparison form across JSON round trips. A
// freshly scanned json.Number such as 1.50 and the float64 1.5 read back from
// storage must compare equal, including inside arrays and objects, so the
// value goes through the same decode a stored asset does.
func canonicalValue(value any) string {
	if value == nil {
		return ""
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err == nil {
		if normalized, err := json.Marshal(decoded); err == nil {
			return string(normalized)
		}
	}
	return string(payload)
}

func boundedValue(canonical string) any {
	var value any
	if err := json.Unmarshal([]byte(canonical), &value); err != nil {
		return nil
	}
	if text, ok := value.(string); ok {
		if len(text) > maxChangeValueLength {
			return text[:maxChangeValueLength] + "…"
		}
		return text
	}
	if len(canonical) > maxChangeValueLength {
		return canonical[:maxChangeValueLength] + "…"
	}
	return value
}
