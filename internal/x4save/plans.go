package x4save

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Construction plans — the station layouts the player designs in the build
// menu. They live next to the savegames as user data, in two places:
//
//	<profile>/constructionplans.xml        every plan saved in-game
//	<profile>/constructionplan/<Name>.xml  one file per "Export" from the UI
//
// Both use the same <plans><plan><entry macro=.../></plan></plans> shape, so
// one parser covers them. A plan is a flat list of module macros plus the
// geometry that snaps them together; for ratio analysis only the macros
// matter, and their multiplicity IS the design.

// ConstructionPlan is one station layout: its identity and the module macros
// it places, in build order.
type ConstructionPlan struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	File        string   `json:"file"`   // where it was read from
	Macros      []string `json:"macros"` // one entry per placed module (duplicates kept)
}

// rawPlans mirrors the on-disk XML.
type rawPlans struct {
	Plans []struct {
		ID          string `xml:"id,attr"`
		Name        string `xml:"name,attr"`
		Description string `xml:"description,attr"`
		Entries     []struct {
			Macro string `xml:"macro,attr"`
		} `xml:"entry"`
	} `xml:"plan"`
}

// DefaultPlanFiles returns every construction-plan file found under the given
// roots (or the default save roots when none are given). Exported per-plan
// files and the in-game collection are both included.
func DefaultPlanFiles(roots ...string) []string {
	if len(roots) == 0 {
		roots = DefaultSaveRoots()
	}
	var out []string
	for _, root := range roots {
		profiles, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, p := range profiles {
			if !p.IsDir() {
				continue
			}
			dir := filepath.Join(root, p.Name())

			// The in-game collection.
			all := filepath.Join(dir, "constructionplans.xml")
			if _, err := os.Stat(all); err == nil {
				out = append(out, all)
			}
			// Exported single plans.
			entries, err := os.ReadDir(filepath.Join(dir, "constructionplan"))
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".xml") {
					out = append(out, filepath.Join(dir, "constructionplan", e.Name()))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// LoadPlans parses the given plan files (or every default one). Files that
// cannot be read or parsed are skipped rather than fatal: a single malformed
// export must not hide the other 38 plans.
func LoadPlans(files ...string) ([]ConstructionPlan, error) {
	if len(files) == 0 {
		files = DefaultPlanFiles()
	}
	var out []ConstructionPlan
	for _, f := range files {
		plans, err := parsePlanFile(f)
		if err != nil {
			continue
		}
		out = append(out, plans...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no construction plans found (looked in %d file(s))", len(files))
	}
	return out, nil
}

func parsePlanFile(path string) ([]ConstructionPlan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rp rawPlans
	if err := xml.Unmarshal(b, &rp); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	out := make([]ConstructionPlan, 0, len(rp.Plans))
	for _, p := range rp.Plans {
		cp := ConstructionPlan{ID: p.ID, Name: p.Name, Description: p.Description, File: path}
		for _, e := range p.Entries {
			if e.Macro != "" {
				cp.Macros = append(cp.Macros, strings.ToLower(e.Macro))
			}
		}
		out = append(out, cp)
	}
	return out, nil
}

// FindPlan resolves a plan by exact name, then case-insensitive exact, then
// unique substring. An ambiguous substring is an error naming the candidates —
// silently analysing the wrong station is worse than asking again.
func FindPlan(query string, plans []ConstructionPlan) (ConstructionPlan, error) {
	if strings.TrimSpace(query) == "" {
		return ConstructionPlan{}, fmt.Errorf("no plan name given")
	}
	for _, p := range plans {
		if p.Name == query {
			return p, nil
		}
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var hits []ConstructionPlan
	for _, p := range plans {
		if strings.ToLower(p.Name) == q {
			return p, nil
		}
	}
	for _, p := range plans {
		if strings.Contains(strings.ToLower(p.Name), q) {
			hits = append(hits, p)
		}
	}
	switch len(hits) {
	case 0:
		return ConstructionPlan{}, fmt.Errorf("no construction plan matching %q", query)
	case 1:
		return hits[0], nil
	default:
		names := make([]string, 0, len(hits))
		for _, h := range hits {
			names = append(names, h.Name)
		}
		sort.Strings(names)
		return ConstructionPlan{}, fmt.Errorf("%q matches %d plans: %s", query, len(hits), strings.Join(names, ", "))
	}
}
