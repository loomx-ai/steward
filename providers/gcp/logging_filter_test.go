package gcp

import (
	"strings"
	"testing"
)

func TestLoggingFilterNativeDependencyGrammar(t *testing.T) {
	for _, tt := range []struct {
		name, filter, check string
		want                monitoringReference
	}{
		{"equals", `labels.check_id="public-check"`, "public-check", monitoringHasReference},
		{"excluded", `labels.check_id="other"`, "public-check", monitoringNoReference},
		{"unicode", `LABELS."check_id"="ＰＵＢＬＩＣ-ＣＨＥＣＫ"`, "public-check", monitoringHasReference},
		{"map keys case sensitive", `labels.CHECK_ID="public-check"`, "public-check", monitoringUnresolvedReference},
		{"quoted dotted key", `labels."check_id.other"="public-check"`, "public-check", monitoringUnresolvedReference},
		{"whole quoted identifier is global", `"labels.check_id"`, "public-check", monitoringUnresolvedReference},
		{"unknown payload", `labels.check_id="public-check" jsonPayload.state="down"`, "public-check", monitoringHasReference},
		{"unknown negation", `NOT (labels.check_id="public-check" AND jsonPayload.state="down")`, "public-check", monitoringHasReference},
		{"OR under NOT", `NOT (labels.check_id="public-check" OR jsonPayload.state="down")`, "public-check", monitoringNoReference},
		{"inequality", `labels.check_id!="public-check"`, "public-check", monitoringNoReference},
		{"minus", `-labels.check_id="other"`, "public-check", monitoringHasReference},
		{"double NOT", `NOT NOT labels.check_id="public-check"`, "public-check", monitoringHasReference},
		{"OR before AND", `labels.check_id="other" OR labels.check_id="public-check" AND labels.check_id="third"`, "public-check", monitoringNoReference},
		{"group", `labels.check_id="other" OR (labels.check_id="public-check" AND labels.check_id="third")`, "other", monitoringHasReference},
		{"value alternatives", `labels.check_id=("other" OR "public-check")`, "public-check", monitoringHasReference},
		{"value substrings", `labels.check_id:("PUBLIC" AND "check")`, "public-check", monitoringHasReference},
		{"value negation", `labels.check_id=(NOT "other" AND "public-check")`, "public-check", monitoringHasReference},
		{"existence", `labels.check_id:*`, "public-check", monitoringHasReference},
		{"quoted wildcard literal", `labels.check_id:"*"`, "public-check", monitoringNoReference},
		{"comment", "labels.check_id=\"public-check\" -- AND labels.check_id=\"other\"\n", "public-check", monitoringHasReference},
		{"comment literal", `labels.check_id="--"`, "--", monitoringHasReference},
		{"unquoted", `labels.check_id=public-check`, "public-check", monitoringHasReference},
		{"number to string", `labels.check_id=123`, "123", monitoringHasReference},
		{"lexicographic", `labels.check_id < "2"`, "10", monitoringHasReference},
		{"regex search", `labels.check_id =~ "lic-ch"`, "public-check", monitoringHasReference},
		{"regex case sensitive", `labels.check_id =~ "PUBLIC"`, "public-check", monitoringNoReference},
		{"regex flag", `labels.check_id =~ "(?i)PUBLIC"`, "public-check", monitoringHasReference},
		{"regex not normalized", `labels.check_id =~ "ＰＵＢＬＩＣ"`, "public-check", monitoringNoReference},
		{"regex negative", `labels.check_id !~ "public"`, "public-check", monitoringNoReference},
		{"global hit", `"PUBLIC"`, "public-check", monitoringHasReference},
		{"global miss not exclusion", `"PRIVATE_QUERY"`, "public-check", monitoringUnresolvedReference},
		{"global under NOT", `NOT "PUBLIC"`, "public-check", monitoringNoReference},
		{"lowercase logical term", `labels.check_id="public-check" and`, "public-check", monitoringHasReference},
		{"missing label inequality", `NOT labels.missing!="foo"`, "public-check", monitoringUnresolvedReference},
		{"unknown function selector", `labels.check_id="public-check" log_id("PRIVATE_LOG")`, "public-check", monitoringHasReference},
		{"sampling", `sample(labels.check_id,0.5)`, "public-check", monitoringHasReference},
		{"sampling all", `NOT sample(labels.check_id,1)`, "public-check", monitoringNoReference},
		{"SEARCH conservative", `NOT SEARCH(labels.check_id,"public")`, "public-check", monitoringHasReference},
		{"int cast", `CAST(labels.check_id,INT64)>2`, "10", monitoringHasReference},
		{"int cast negative", `CAST(labels.check_id,INT64)>2`, "1", monitoringNoReference},
		{"int cast unknown conversion", `CAST(labels.check_id,INT64)>2`, "not-a-number", monitoringHasReference},
		{"float cast", `CAST(labels.check_id,FLOAT64)<0.5`, "0.1", monitoringHasReference},
		{"float Infinity", `CAST(labels.check_id,FLOAT64)=infinity`, "Infinity", monitoringHasReference},
		{"float NaN", `CAST(labels.check_id,FLOAT64)=nan`, "NaN", monitoringHasReference},
		{"bool cast", `CAST(labels.check_id,BOOL)=TRUE`, "true", monitoringHasReference},
		{"bool cast order", `CAST(labels.check_id,BOOL)<true`, "false", monitoringHasReference},
		{"string cast", `CAST(labels.check_id,STRING)="PUBLIC-CHECK"`, "public-check", monitoringHasReference},
		{"nested numeric cast uncertain", `CAST(CAST(labels.check_id,FLOAT64),INT64)=1`, "1.5", monitoringHasReference},
		{"extract", `REGEXP_EXTRACT(labels.check_id,"public-(.*)")="CHECK"`, "public-check", monitoringHasReference},
		{"extract excludes", `REGEXP_EXTRACT(labels.check_id,"public-(.*)")="other"`, "public-check", monitoringNoReference},
		{"extract absent uncertain", `REGEXP_EXTRACT(labels.check_id,"other-(.*)")=""`, "public-check", monitoringHasReference},
		{"extract optional uncertain", `REGEXP_EXTRACT(labels.check_id,"(other)?public-check")=""`, "public-check", monitoringHasReference},
		{"nested extract cast", `CAST(REGEXP_EXTRACT(labels.check_id,"check-([0-9]+)"),INT64)>5`, "check-10", monitoringHasReference},
		{"ip", `ip_in_net(labels.check_id,"10.0.0.0/8")`, "10.1.2.3", monitoringHasReference},
		{"ip excludes", `ip_in_net(labels.check_id,"10.0.0.0/8")`, "192.168.0.1", monitoringNoReference},
		{"ip invalid", `ip_in_net(labels.check_id,"10.0.0.0/8")`, "public-check", monitoringNoReference},
		{"mapped ipv6 uncertain", `ip_in_net(labels.check_id,"10.0.0.0/8")`, "::ffff:10.1.2.3", monitoringHasReference},
		{"ipv6", `ip_in_net(labels.check_id,"2001:db8::/32")`, "2001:db8::1", monitoringHasReference},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := loggingFilterReference(tt.filter, tt.check); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestLoggingFilterInvalidNeverProvesAbsence(t *testing.T) {
	for _, filter := range []string{
		"", `labels.check_id=="other"`, `labels.check_id="other" AND`, `labels.check_id="other" OR`, `labels.check_id="other")`, `(labels.check_id="other"`,
		`labels.check_id="other" future()`, `labels.check_id="other" AND labels.check_id=~"["`, `labels.check_id="other" AND labels.check_id=~"(?=x)"`,
		`labels.check_id="other" AND labels.check_id=~unquoted`, `labels.check_id="other" AND REGEXP_EXTRACT(labels.check_id,"x")="x"`,
		`REGEXP_EXTRACT(labels.check_id,"(a)(b)")="other"`, `sample(labels.check_id,0)`, `sample(labels.check_id,2)`, `CAST()="other"`, `ip_in_net(labels.check_id,"garbage")`,
		`SEARCH(labels.check_id,unquoted)`, `labels.check_id="bad\q"`, `labels.check_id="\377"`, `labels.check_id="other" --only comment` + "\nAND", `labels.check_id=bare--comment`,
		strings.Repeat("NOT ", 70) + `labels.check_id="other"`, strings.Repeat("(", 70) + `labels.check_id="other"` + strings.Repeat(")", 70), strings.Repeat(`labels.check_id="other" AND `, 600) + `labels.check_id="other"`, strings.Repeat(" ", 20001), "\xff",
	} {
		if got := loggingFilterReference(filter, "public-check"); got != monitoringUnresolvedReference {
			t.Fatalf("invalid filter of length %d returned %v: %.100s", len(filter), got, filter)
		}
	}
}

func TestLoggingLongCombiningSequenceDoesNotProveAbsence(t *testing.T) {
	check := "a" + strings.Repeat("\u0301", 40)
	if _, ok := loggingFold(check); ok {
		t.Fatal("accepted stream-safe normalization modification")
	}
	if got := loggingFilterReference(`labels.check_id="other"`, check); got != monitoringHasReference {
		t.Fatal(got)
	}
}

func FuzzLoggingFilter(f *testing.F) {
	for _, seed := range []string{`labels.check_id="public-check"`, `NOT (labels.check_id="other" AND jsonPayload.x="y")`, `labels.check_id:("PUBLIC" OR "x")`, `REGEXP_EXTRACT(labels.check_id,"(.*)")="x"`, "((("} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) { _ = loggingFilterReference(input, "public-check") })
}
