package main

import (
	"testing"

	"github.com/gallowaysoftware/x4mcp/internal/x4save"
)

// The real family that broke it: "Hatikvah's Choice I" is a literal prefix of
// "Hatikvah's Choice III", so a substring-only match answered with the wrong one.
func resolveFixture() *x4save.Snapshot {
	return &x4save.Snapshot{Sectors: []x4save.Sector{
		{Macro: "cluster_29_sector002_macro", Name: "Hatikvah's Choice III"},
		{Macro: "cluster_29_sector001_macro", Name: "Hatikvah's Choice I"},
		{Macro: "cluster_706_sector001_macro", Name: "Hatikvah's Faith"},
		{Macro: "cluster_14_sector001_macro", Name: "Argon Prime"},
		{Macro: "cluster_101_sector001_macro", Name: "Silent Witness I"},
		{Macro: "cluster_101_sector011_macro", Name: "Silent Witness XI"},
	}}
}

func TestResolveSector(t *testing.T) {
	snap := resolveFixture()
	cases := []struct{ query, wantName string }{
		// The regression: III is listed first and contains the query as a prefix.
		{"Hatikvah's Choice I", "Hatikvah's Choice I"},
		{"hatikvah's choice i", "Hatikvah's Choice I"},
		{"Hatikvah's Choice III", "Hatikvah's Choice III"},
		{"Silent Witness I", "Silent Witness I"},
		{"Silent Witness XI", "Silent Witness XI"},
		{"Faith", "Hatikvah's Faith"},
		{"cluster_14_sector001_macro", "Argon Prime"},
		{"argon", "Argon Prime"},
		{"", ""},
		{"Nowhere At All", ""},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			_, name := resolveSector(snap, tc.query)
			if name != tc.wantName {
				t.Errorf("resolveSector(%q) = %q, want %q", tc.query, name, tc.wantName)
			}
		})
	}
}
