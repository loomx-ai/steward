package asset

import "testing"

func TestConnectionRegionEffectiveName(t *testing.T) {
	tests := []struct {
		name   string
		region ConnectionRegion
		want   string
	}{
		{name: "override", region: ConnectionRegion{NameOverride: "  杭州  ", DiscoveredName: "华东 1", RegionID: "cn-hangzhou"}, want: "杭州"},
		{name: "discovered", region: ConnectionRegion{NameOverride: "  ", DiscoveredName: " 华东 1 ", RegionID: "cn-hangzhou"}, want: "华东 1"},
		{name: "region id", region: ConnectionRegion{DiscoveredName: " ", RegionID: "cn-hangzhou"}, want: "cn-hangzhou"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.region.EffectiveName(); got != test.want {
				t.Fatalf("EffectiveName() = %q, want %q", got, test.want)
			}
		})
	}
}
