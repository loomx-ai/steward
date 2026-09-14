package gcp

import (
	"strings"
	"testing"
)

const uptimeMetricFilter = `metric.type="monitoring.googleapis.com/uptime_check/check_passed"`

func TestMonitoringFilterNativeDependencyGrammar(t *testing.T) {
	tests := []struct {
		name, filter string
		want         monitoringReference
	}{
		{"native sample", uptimeMetricFilter + ` AND metric.label.check_id="public-check" AND resource.type="uptime_url"`, monitoringHasReference},
		{"plural quoted key", uptimeMetricFilter + ` metric.labels."check_id"="public-check"`, monitoringHasReference},
		{"other check", uptimeMetricFilter + ` AND metric.labels.check_id="another"`, monitoringNoReference},
		{"OR precedence", uptimeMetricFilter + ` AND metric.labels.check_id="another" OR metric.labels.check_id="public-check" AND metric.labels.check_id="third"`, monitoringNoReference},
		{"parentheses", uptimeMetricFilter + ` AND (metric.labels.check_id="another" OR metric.labels.check_id="public-check")`, monitoringHasReference},
		{"implicit AND", uptimeMetricFilter + ` (metric.labels.check_id="public-check")`, monitoringHasReference},
		{"contradictory metric types", uptimeMetricFilter + ` AND metric.type="monitoring.googleapis.com/uptime_check/request_latency"`, monitoringNoReference},
		{"foreign metric", `metric.type="custom.googleapis.com/check_passed" AND metric.labels.check_id="public-check"`, monitoringNoReference},
		{"literal trap", `metric.type="custom.googleapis.com/other" AND metric.labels.note="metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\" metric.label.check_id=\"public-check\""`, monitoringNoReference},
		{"one of", uptimeMetricFilter + ` AND metric.labels.check_id=one_of("another","public-check")`, monitoringHasReference},
		{"not one of", uptimeMetricFilter + ` AND metric.labels.check_id!=one_of("another","public-check")`, monitoringNoReference},
		{"prefix", uptimeMetricFilter + ` AND metric.labels.check_id=starts_with("public-")`, monitoringHasReference},
		{"suffix", uptimeMetricFilter + ` AND metric.labels.check_id=ends_with("-check")`, monitoringHasReference},
		{"substring", uptimeMetricFilter + ` AND metric.labels.check_id: "lic-che"`, monitoringHasReference},
		{"case sensitive", uptimeMetricFilter + ` AND metric.labels.check_id=has_substring("PUBLIC")`, monitoringNoReference},
		{"ignore case", uptimeMetricFilter + ` AND metric.labels.check_id=has_substring("PUBLIC",true)`, monitoringHasReference},
		{"regex", uptimeMetricFilter + ` AND metric.labels.check_id=monitoring.regex.full_match("public-(check|other)")`, monitoringHasReference},
		{"regex full match", uptimeMetricFilter + ` AND metric.labels.check_id=monitoring.regex.full_match("public")`, monitoringNoReference},
		{"regex flags", uptimeMetricFilter + ` AND metric.labels.check_id=monitoring.regex.full_match("(?i)PUBLIC-CHECK")`, monitoringHasReference},
		{"bounded type alternatives", `metric.type=one_of("compute.googleapis.com/usage","monitoring.googleapis.com/uptime_check/check_passed") AND metric.labels.check_id="public-check"`, monitoringHasReference},
		{"dynamic metric envelope", `metric.type=starts_with("monitoring.googleapis.com/") AND metric.labels.check_id="public-check"`, monitoringUnresolvedReference},
		{"excluded dynamic metric", `metric.type=starts_with("monitoring.googleapis.com/") AND metric.labels.check_id="other"`, monitoringNoReference},
		{"broad policy", uptimeMetricFilter, monitoringHasReference},
		{"volatile resource selector", uptimeMetricFilter + ` AND resource.labels.zone="unknown-zone"`, monitoringHasReference},
		{"other string boolean number", uptimeMetricFilter + ` AND metadata.user_labels."a.b"="x" AND metric.labels.flag=true AND metric.labels.count>=-2.5`, monitoringHasReference},
		{"numeric check", uptimeMetricFilter + ` AND metric.labels.check_id=123`, monitoringUnresolvedReference},
		{"missing metric selector", `metric.labels.check_id="public-check"`, monitoringUnresolvedReference},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := monitoringFilterReference(test.filter, "public-check"); got != test.want {
				t.Fatalf("got %v want %v", got, test.want)
			}
		})
	}
}
func TestMonitoringFilterInvalidAndUnsupportedNeverProveAbsence(t *testing.T) {
	for _, filter := range []string{
		"", `metric.type='foreign'`, `metric.type="\x66oreign"`, `metric.type="\146oreign"`, `metric.type="foreign" # comment`, `metric.type="foreign" // comment`, `metric.type="foreign" /* comment */`,
		`metric.type=="foreign"`, `metric.type="foreign" AND`, `metric.type="foreign" OR`, `metric.type="foreign")`, `(metric.type="foreign"`,
		`NOT metric.type="foreign"`, `metric.type="foreign" and metric.labels.check_id="other"`, `metric.type="foreign" || metric.labels.check_id="other"`,
		`metric.type="foreign" AND metric.labels.check_id=future("other")`, `metric.type="foreign" AND metric.labels.check_id=one_of()`,
		`metric.type="foreign" AND metric.labels.check_id=one_of("other",)`, `metric.type="foreign" AND metric.labels.check_id=one_of(true)`,
		`metric.type="foreign" AND metric.labels.check_id=starts_with("x","y")`, `metric.type="foreign" AND metric.labels.check_id=has_substring("x",2)`,
		`metric.type="foreign" AND metric.labels.check_id=monitoring.regex.full_match("[")`, `metric.type="foreign" AND metric.labels.check_id=monitoring.regex.full_match("(?=x)")`,
		`metric.type="foreign" AND metric.labels.check_id="bad\q"`, strings.Repeat("(", 70) + uptimeMetricFilter + strings.Repeat(")", 70),
		strings.Repeat(uptimeMetricFilter+" AND ", 600) + uptimeMetricFilter, strings.Repeat(" ", 65537),
	} {
		if _, ok := parseMonitoringFilter(filter); ok {
			t.Fatalf("accepted invalid filter of length %d", len(filter))
		}
		if monitoringFilterReference(filter, "public-check") != monitoringUnresolvedReference {
			t.Fatal("invalid filter proved absence")
		}
	}
}
func TestMonitoringPolicyConditionReferences(t *testing.T) {
	for _, key := range []string{"conditionThreshold", "conditionAbsent", "conditionMatchedLog", "conditionMonitoringQueryLanguage", "conditionPrometheusQueryLanguage", "conditionSql"} {
		t.Run(key, func(t *testing.T) {
			policy := map[string]any{"conditions": []any{map[string]any{key: map[string]any{"filter": uptimeMetricFilter + ` metric.labels.check_id="public-check"`, "query": "PRIVATE_QUERY"}}}}
			want := monitoringUnresolvedReference
			if key == "conditionThreshold" || key == "conditionAbsent" {
				want = monitoringHasReference
			}
			if got := alertPolicyUptimeReference(policy, "public-check"); got != want {
				t.Fatal(got, want)
			}
		})
	}
	policy := map[string]any{"conditions": []any{map[string]any{"conditionThreshold": map[string]any{"filter": `metric.type="compute.googleapis.com/usage"`, "denominatorFilter": uptimeMetricFilter + ` metric.labels.check_id="public-check"`}}}}
	if alertPolicyUptimeReference(policy, "public-check") != monitoringHasReference {
		t.Fatal("denominator ignored")
	}
}

func FuzzMonitoringFilter(f *testing.F) {
	for _, value := range []string{uptimeMetricFilter, `metric.labels.check_id="public-check"`, `metric.type=one_of("x","y") AND metric.labels.check_id=monitoring.regex.full_match("a.*")`, "((("} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) { _ = monitoringFilterReference(value, "public-check") })
}

func TestMonitoringFilterUnicodeCaseFolding(t *testing.T) {
	if monitoringFilterReference(uptimeMetricFilter+` AND metric.labels.check_id=has_substring("ſTAT",true)`, "status") != monitoringHasReference {
		t.Fatal("Unicode simple case folding lost a reference")
	}
}

func TestMonitoringLogCheckIDDispatch(t *testing.T) {
	policy := map[string]any{"conditions": []any{map[string]any{"conditionMatchedLog": map[string]any{"filter": `labels.check_id="public-check"`}}}}
	if alertPolicyUptimeReference(policy, "public-check") != monitoringHasReference {
		t.Fatal("Logging reference was not recognized")
	}
}
