package gcp

import (
	"slices"
	"strings"
	"testing"
)

func TestRoutePolicyCELNamedSetReferences(t *testing.T) {
	tests := []struct {
		expression string
		want       []string
	}{
		{`destination.inAnyRange(prefixSets('local'))`, []string{"local"}},
		{`communities.matchesEvery(communitySets("regional"))`, []string{"regional"}},
		{`destination.inAnyRange(prefixSets('\x6cocal')) || destination.inAnyRange(prefixSets("local"))`, []string{"local"}},
		{`prefixSets(r'local')`, []string{"local"}},
		{`prefixSets('''local''')`, []string{"local"}},
		{`.prefixSets('local')`, []string{"local"}},
		{`prefixSets // a misleading communitySets('wrong') comment
     ('local')`, []string{"local"}},
		{`"prefixSets('not-a-reference')"`, nil},
		{`{'text': "communitySets('wrong')", 'value': prefixSets('actual')}`, []string{"actual"}},
		{`[1,2].exists(x, x > 0 && prefixSets('inside') != [])`, []string{"inside"}},
		{`false ? prefixSets('one') : prefixSets('two')`, []string{"one", "two"}},
		{`destination == '10.0.0.0/8'`, nil},
		{`accept()`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.expression, func(t *testing.T) {
			data := routePolicyFixture("policy-a")
			object(array(data["terms"])[0])["match"] = map[string]any{"expression": tt.expression}
			refs, err := routePolicySetReferences(data)
			if err != nil || !slices.Equal(refs, tt.want) {
				t.Fatal(refs, tt.want, err)
			}
		})
	}
	data := routePolicyFixture("policy-a")
	object(array(data["terms"])[0])["actions"] = []any{map[string]any{"expression": `communities.add(communitySets('actions'))`}}
	refs, err := routePolicySetReferences(data)
	if err != nil || !slices.Equal(refs, []string{"actions"}) {
		t.Fatal("action expression reference lost", refs, err)
	}
}

func TestRoutePolicyCELUnresolvedReferencesAndInvalidSyntax(t *testing.T) {
	for _, expression := range []string{`prefixSets(name)`, `prefixSets('a'+'b')`, `prefixSets(true ? 'a' : 'b')`, `prefixSets()`, `prefixSets('a','b')`, `prefixSets(b'local')`, `prefixSets(123)`, `prefixSets('../other')`, `object.prefixSets('local')`, `prefixSets('unterminated)`, strings.Repeat("(", 300) + "true" + strings.Repeat(")", 300), strings.Repeat("a", 100001)} {
		data := routePolicyFixture("policy-a")
		object(array(data["terms"])[0])["match"] = map[string]any{"expression": expression}
		refs, err := routePolicySetReferences(data)
		if err == nil || len(refs) != 0 {
			t.Fatal("unresolved/invalid dependency accepted", expression[:min(len(expression), 80)], refs, err)
		}
		if strings.Contains(err.Error(), "unterminated") {
			t.Fatal("raw expression leaked in validation error")
		}
	}
}
