package x4save

import (
	"os"
	"path/filepath"
	"testing"
)

// The section mask exists to answer "which section costs that" when the
// aggregate parse-cost gate goes red. That answer is worth nothing if a section
// that is switched OFF still runs, so this asserts each bit actually gates the
// thing it names — and, in the other direction, that switching one section off
// leaves the others alone.
func TestSectionMaskGatesEachSection(t *testing.T) {
	// One fixture per section, and the emptiness test for it. The fixture must
	// be one where the section is NON-empty with the mask on, or "empty when
	// off" proves nothing.
	cases := []struct {
		name    string
		bit     sectionMask
		fixture string
		empty   func(*Snapshot) bool
	}{
		{"logbook", secLogbook, "02_logbook", func(s *Snapshot) bool { return len(s.Logbook) == 0 && !s.LogbookSeen }},
		{"stats", secStats, "03_stats", func(s *Snapshot) bool { return len(s.Stats) == 0 && !s.StatsSeen }},
		{"missions", secMissions, "04_missions_war", func(s *Snapshot) bool {
			return len(s.MissionOffers) == 0 && len(s.Missions) == 0 && !s.MissionsSeen
		}},
		{"licences", secLicences, "01_info_factions_licences", func(s *Snapshot) bool {
			return len(s.Licences) == 0 && len(s.Boosters) == 0 && !s.LicencesSeen
		}},
		{"inventory", secInventory, "05_player_inventory", func(s *Snapshot) bool {
			return len(s.Inventory) == 0 && !s.InventorySeen
		}},
		{"threat", secThreat, "06_khaak_xenon", func(s *Snapshot) bool { return len(s.ThreatComponents) == 0 }},
		{"hull", secHull, "14_hull_states", func(s *Snapshot) bool {
			for _, sh := range s.Ships {
				if sh.Hull != nil || sh.Attack != nil || sh.State != "" {
					return false
				}
			}
			for _, st := range s.Stations {
				if st.ModuleHealth != nil {
					return false
				}
			}
			return true
		}},
		{"build_storage", secBuildStorage, "09_player_station", func(s *Snapshot) bool { return len(s.BuildStorages) == 0 }},
		{"resource_areas", secResourceAreas, "07_gates_sectors", func(s *Snapshot) bool {
			for _, sec := range s.Sectors {
				if len(sec.ResourceAreas) > 0 || sec.PlayerProbes > 0 {
					return false
				}
			}
			return true
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if on := parseSynthetic(t, c.fixture); c.empty(on) {
				t.Fatalf("fixture %s carries nothing for section %q even with it ENABLED; the gate test would pass vacuously", c.fixture, c.name)
			}
			var off *Snapshot
			withSections(secAll&^c.bit, func() { off = parseSynthetic(t, c.fixture) })
			if !c.empty(off) {
				t.Errorf("section %q still produced output with its bit cleared; a marginal-cost measurement taken this way measures nothing", c.name)
			}
		})
	}
}

// Turning one section off must not disturb another, or a per-section cost is
// really a cost for some unnamed combination of sections.
func TestSectionMaskIsIndependent(t *testing.T) {
	full := parseSynthetic(t, "09_player_station")
	var noLog *Snapshot
	withSections(secAll&^secLogbook, func() { noLog = parseSynthetic(t, "09_player_station") })

	if len(noLog.BuildStorages) != len(full.BuildStorages) || len(noLog.Stations) != len(full.Stations) {
		t.Errorf("clearing secLogbook changed unrelated sections: build storages %d vs %d, stations %d vs %d",
			len(noLog.BuildStorages), len(full.BuildStorages), len(noLog.Stations), len(full.Stations))
	}
	if noLog.Stations[0].ModuleHealth == nil {
		t.Error("clearing secLogbook lost the station module health")
	}
}

// Clearing every bit must leave the pre-S6 snapshot exactly as it was. This is
// the gate's own baseline: it is what "the parse without the new sections"
// means, and it is also a standing check that no S6 capture leaked into a path
// that was already there.
func TestSectionMaskOffLeavesThePreviousSectionsIntact(t *testing.T) {
	full := parseSynthetic(t, "09_player_station")
	var none *Snapshot
	withSections(0, func() { none = parseSynthetic(t, "09_player_station") })

	if len(none.Ships) != len(full.Ships) || len(none.Stations) != len(full.Stations) {
		t.Fatalf("ships/stations moved: %d/%d vs %d/%d", len(none.Ships), len(none.Stations), len(full.Ships), len(full.Stations))
	}
	st, wantSt := none.Stations[0], full.Stations[0]
	if len(st.Storage) != len(wantSt.Storage) || len(st.TradeOffers) != len(wantSt.TradeOffers) {
		t.Errorf("station contents moved with every section off: %+v", st)
	}
	// buildstorage was counted in OtherCounts before S6 and must still be,
	// whether or not the new capture runs.
	if none.OtherCounts["buildstorage"] != 1 || full.OtherCounts["buildstorage"] != 1 {
		t.Errorf("OtherCounts[buildstorage] = %d (off) / %d (on), want 1 in both — the pre-S6 count is not the new section's to change",
			none.OtherCounts["buildstorage"], full.OtherCounts["buildstorage"])
	}
	// The logbook's reputation mining predates S6 and is deliberately NOT
	// scoped to the new logbook capture.
	logFull := parseSynthetic(t, "02_logbook")
	var logNone *Snapshot
	withSections(0, func() { logNone = parseSynthetic(t, "02_logbook") })
	if len(logNone.RawReputations) != len(logFull.RawReputations) {
		t.Errorf("reputation mining = %d factions with the logbook section off, %d with it on — it must not depend on the new capture",
			len(logNone.RawReputations), len(logFull.RawReputations))
	}
}

// BenchmarkParseSectionCost is the plan's second S6 gate: what does each new
// section cost ON ITS OWN.
//
// The aggregate "+10% / +5 MB" gate says a regression happened; it does not say
// which of nine sections owns it, and a red that starts a bisect is a red that
// gets waived. So each arm parses with exactly one section cleared, and the
// section's cost is (all) − (without it).
//
//	go test ./internal/x4save -run '^$' -bench BenchmarkParseSectionCost -benchtime 10x
//	X4MCP_REAL_SAVE=<save> go test ./internal/x4save -run '^$' -bench BenchmarkParseSectionCost -benchtime 3x
//
// Two honest caveats, both recorded in docs/s6-notes.md rather than left for a
// reader to discover:
//
//   - `hull` clears RETENTION, not the decode. Its fields live on rawComp,
//     which is decoded for player ships/stations and discovered NPC stations
//     either way, so its arm is a lower bound on the true cost.
//   - the difference between two arms is a difference of two noisy numbers.
//     Anything under ~1% of the total is inside the noise on a desktop; treat
//     it as "not measurable", not as "free".
func BenchmarkParseSectionCost(b *testing.B) {
	path := os.Getenv("X4MCP_REAL_SAVE")
	if path == "" {
		path = filepath.Join(realDir, distilledFixture)
	}
	if _, err := os.Stat(path); err != nil {
		b.Skipf("no save to benchmark at %s", path)
	}

	b.Run("all", func(b *testing.B) {
		withSections(secAll, func() { benchParse(b, path) })
	})
	for _, s := range sectionNames {
		b.Run("without_"+s.name, func(b *testing.B) {
			withSections(secAll&^s.bit, func() { benchParse(b, path) })
		})
	}
	// The pre-S6 parse, for the aggregate number the +10% gate is against.
	b.Run("none", func(b *testing.B) {
		withSections(0, func() { benchParse(b, path) })
	})
}
