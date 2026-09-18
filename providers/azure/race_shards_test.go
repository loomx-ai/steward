package azure

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The race detector makes this package too slow for one CI job, so the race
// workflow shards it by test-name prefix. Every test must fall in exactly one
// shard, or it would silently stop being race-checked.
var raceShards = []string{
	"^TestA[A-Z]", "^TestA[a-z]", "^Test[B-C]", "^TestD", "^TestE", "^Test[F-P]", "^Test[R-W]",
}

func TestRaceShardsCoverEveryTest(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatal("no test files", err)
	}
	patterns := make([]*regexp.Regexp, 0, len(raceShards))
	for _, shard := range raceShards {
		patterns = append(patterns, regexp.MustCompile(shard))
	}
	declaration := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(`)
	total := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range declaration.FindAllStringSubmatch(string(raw), -1) {
			total++
			matched := 0
			for _, pattern := range patterns {
				if pattern.MatchString(match[1]) {
					matched++
				}
			}
			if matched != 1 {
				t.Fatalf("%s matches %d race shards; update raceShards and .github/workflows/test.yml", match[1], matched)
			}
		}
	}
	if total == 0 {
		t.Fatal("no tests found")
	}
}
