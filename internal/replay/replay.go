// Package replay runs an archived series of savegames through the differ and
// measures what the rules would have done.
//
// # Why a harness at all
//
// The product's budget is fewer than one false red per week. No amount of unit
// testing establishes that, because a unit test asserts what a rule does on
// input someone invented. The only way to learn what a rule does on real play
// is to run it over real play — 200 saves covering nine days of one
// playthrough — and count.
//
// # What this can and cannot measure
//
// It CAN measure, exactly:
//
//   - fire rate per rule, in fires per in-game hour and per real day;
//   - corroboration rate: how often two independent signal groups agreed;
//   - falsification: a "lost" ship that turns up alive in a later save is a
//     definite false positive, and no labelling is needed to know it;
//   - attribution: how often a logbook sentence could be joined to an entity
//     at all.
//
// It CANNOT measure precision in the general case, because the corpus carries
// no ground truth. There is no record of what the player MEANT, so "this ship
// was sold, not lost" is not answerable from the file for every case. Where
// that is so, the honest output is a fire rate, a falsification count and an
// explicit "unvalidated" — never a confidence number invented to fill a
// column. See docs/s7-rules.md for which rules ended up in which category.
//
// And 200 saves of one playthrough is a lot of data about one player and none
// about anyone else. Every rate here is that player's.
package replay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pequalsnp/x4mcp/internal/diff"
	"github.com/pequalsnp/x4mcp/internal/logbook"
	"github.com/pequalsnp/x4mcp/internal/rules"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// Options configures a run.
type Options struct {
	// Dir holds the archived saves. It is opened READ-ONLY and nothing in this
	// package ever writes to it.
	Dir string

	// Diff is the differ's configuration, thresholds included.
	Diff diff.Options

	// Rules is the kill-switch state the alerts are measured under. The zero
	// value takes every rule's default position, which is what the shipped
	// table would do.
	Rules rules.Config

	// Limit caps how many saves are read (0 = all), for a fast smoke run.
	Limit int

	// Progress, if set, is called before each save is loaded.
	Progress func(stage string, i, n int, name string)
}

// SaveID is one archived save's identity and a lightweight index of it.
//
// The index exists so that the second pass can answer "did this ship ever come
// back?" without holding 200 snapshots in memory — a snapshot is tens of
// megabytes and 200 of them is not a thing to keep around for a set lookup.
type SaveID struct {
	Path      string  `json:"-"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"` // autosave / quicksave / save
	Play      int     `json:"play"` // playthrough index, 1-based; the GUID is never printed
	SaveDate  int64   `json:"save_date"`
	GameTimeS float64 `json:"game_time_s"`
	ParseErr  string  `json:"parse_err,omitempty"`

	// Ships and ShipCode are what the falsification audit looks a fire up in.
	// Both are kept because a Fire's subject is a CODE when the entity had one
	// and the component id when it did not.
	Ships    map[string]bool `json:"-"`
	ShipCode map[string]bool `json:"-"`

	LogEntries int `json:"log_entries"`
	ShipCount  int `json:"ships"`
}

// Fire is one change the differ produced during the replay, with enough
// context to audit it afterwards.
type Fire struct {
	Kind         diff.Kind `json:"kind"`
	Play         int       `json:"play"`
	PairIndex    int       `json:"pair"` // index into the playthrough's ordered saves (the NEW save)
	SaveName     string    `json:"save"` // the archive file the change was observed in
	GameTime     float64   `json:"game_time"`
	Subject      string    `json:"subject,omitempty"` // code, else id
	SubjectName  string    `json:"subject_name,omitempty"`
	Class        string    `json:"class,omitempty"`
	Corroborated bool      `json:"corroborated"`
	Groups       []string  `json:"groups"`
	Refs         []string  `json:"refs,omitempty"`
	Detail       string    `json:"detail,omitempty"`

	// Falsified is set in the audit pass when the corpus itself contradicts
	// the change — a lost ship that is alive again later.
	Falsified string `json:"falsified,omitempty"`
}

// RuleStats is what internal/rules would have emitted, which is a different
// number from what the differ produced: rules dedupe, downgrade and switch off.
//
// Alerts is the number that matters for the <1-false-red-per-week budget —
// a player sees one alert per DEDUPE KEY, not one per fire.
type RuleStats struct {
	RuleID     string           `json:"rule"`
	Enabled    bool             `json:"enabled"`
	Declared   rules.Severity   `json:"declared_severity"`
	Confidence rules.Confidence `json:"confidence"`

	Fires      int `json:"fires"`
	Alerts     int `json:"alerts"` // distinct dedupe keys
	Red        int `json:"red"`
	Amber      int `json:"amber"`
	Grey       int `json:"grey"`
	Conflicts  int `json:"rule_conflicts"`
	Downgrades int `json:"downgrades"`

	RedAlerts    int     `json:"red_alerts"` // distinct dedupe keys that reached red
	RedPerWeek   float64 `json:"red_alerts_per_real_week"`
	AlertsPerDay float64 `json:"alerts_per_real_day"`
}

// KindStats is one rule's measured behaviour.
type KindStats struct {
	Kind           diff.Kind `json:"kind"`
	Fires          int       `json:"fires"`
	Corroborated   int       `json:"corroborated"`
	Uncorroborated int       `json:"uncorroborated"`
	Subjects       int       `json:"distinct_subjects"`
	Falsified      int       `json:"falsified"`
	PairsWithFire  int       `json:"pairs_with_fire"`
	// GroupCombos counts how often each set of independence groups backed a
	// fire, e.g. "log+tree": 41.
	GroupCombos map[string]int `json:"group_combos"`
}

// Report is everything one replay measured.
type Report struct {
	Dir string `json:"dir"`

	Saves       []SaveID `json:"saves"`
	ParseErrors int      `json:"parse_errors"`

	Playthroughs []Playthrough `json:"playthroughs"`

	Pairs     int            `json:"pairs"`
	Diffed    int            `json:"diffed"`
	Refusals  map[string]int `json:"refusals"`
	ElapsedS  float64        `json:"in_game_seconds_covered"`
	RealDays  float64        `json:"real_days_covered"`
	Anomalies []string       `json:"anomalies,omitempty"`

	ByKind []KindStats `json:"by_kind"`
	ByRule []RuleStats `json:"by_rule"`
	Fires  []Fire      `json:"fires"`

	Logbook LogbookStats `json:"logbook"`
}

// Playthrough is one GameGUID's run of saves. The GUID itself is deliberately
// absent: this report is committed to a public repo.
type Playthrough struct {
	Play      int     `json:"play"`
	Saves     int     `json:"saves"`
	FirstTime float64 `json:"first_game_time_s"`
	LastTime  float64 `json:"last_game_time_s"`
	FirstDate int64   `json:"first_save_date"`
	LastDate  int64   `json:"last_save_date"`
}

// LogbookStats is what the classifier saw across every window.
type LogbookStats struct {
	WindowEntries    int            `json:"window_entries"`
	WindowClassified int            `json:"window_classified"`
	ByEventKind      map[string]int `json:"by_event_kind"`
	ByRef            map[string]int `json:"by_ref"`

	// DestroyUnmatched counts destroy sentences whose code named no ship in
	// the previous snapshot — the measurement that says whether the log
	// records only the player's losses.
	DestroyEntries   int `json:"destroy_entries"`
	DestroyNoCode    int `json:"destroy_no_code"`
	DestroyUnmatched int `json:"destroy_unmatched_code"`
}

// Run executes the replay. It is read-only with respect to the archive.
func Run(ctx context.Context, opts Options) (*Report, error) {
	files, err := filepath.Glob(filepath.Join(opts.Dir, "*.xml.gz"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files) // the archiver names files by source mtime, UTC
	if opts.Limit > 0 && len(files) > opts.Limit {
		files = files[len(files)-opts.Limit:]
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no *.xml.gz in %s", opts.Dir)
	}

	rep := &Report{Dir: opts.Dir, Refusals: map[string]int{}}
	rep.Logbook.ByEventKind = map[string]int{}
	rep.Logbook.ByRef = map[string]int{}

	// ---- pass 1: identity and a light index per save ----
	guidPlay := map[string]int{}
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if opts.Progress != nil {
			opts.Progress("index", i+1, len(files), filepath.Base(f))
		}
		id := SaveID{Path: f, Name: filepath.Base(f), Kind: saveKind(f)}
		snap, err := x4save.LoadSnapshot(f, false)
		if err != nil {
			id.ParseErr = err.Error()
			rep.ParseErrors++
			rep.Saves = append(rep.Saves, id)
			continue
		}
		play, ok := guidPlay[snap.GameGUID]
		if !ok {
			play = len(guidPlay) + 1
			guidPlay[snap.GameGUID] = play
		}
		id.Play = play
		id.SaveDate = snap.SaveDate
		id.GameTimeS = snap.GameTimeS
		id.LogEntries = len(snap.Logbook)
		id.ShipCount = len(snap.Ships)
		id.Ships = map[string]bool{}
		id.ShipCode = map[string]bool{}
		for _, s := range snap.Ships {
			id.Ships[s.ID] = true
			if s.Code != "" {
				id.ShipCode[s.Code] = true
			}
		}
		rep.Saves = append(rep.Saves, id)
	}

	// ---- ordering ----
	// Group by playthrough, order by the save's OWN clock. File order is the
	// archiver's copy order and is normally the same thing, but "normally" is
	// not a property to build a differ on: a pair diffed out of order reports
	// every ship built in between as lost.
	byPlay := map[int][]int{} // play -> indices into rep.Saves
	for i, s := range rep.Saves {
		if s.ParseErr != "" {
			continue
		}
		byPlay[s.Play] = append(byPlay[s.Play], i)
	}
	plays := make([]int, 0, len(byPlay))
	for p := range byPlay {
		plays = append(plays, p)
	}
	sort.Ints(plays)
	for _, p := range plays {
		idx := byPlay[p]
		fileOrder := append([]int(nil), idx...)
		sort.SliceStable(idx, func(a, b int) bool {
			if rep.Saves[idx[a]].SaveDate != rep.Saves[idx[b]].SaveDate {
				return rep.Saves[idx[a]].SaveDate < rep.Saves[idx[b]].SaveDate
			}
			return rep.Saves[idx[a]].GameTimeS < rep.Saves[idx[b]].GameTimeS
		})
		if !sameOrder(fileOrder, idx) {
			rep.Anomalies = append(rep.Anomalies, fmt.Sprintf(
				"playthrough %d: archive file order differs from save-date order; the save-date order was used", p))
		}
		byPlay[p] = idx
		pt := Playthrough{Play: p, Saves: len(idx)}
		if len(idx) > 0 {
			pt.FirstTime = rep.Saves[idx[0]].GameTimeS
			pt.LastTime = rep.Saves[idx[len(idx)-1]].GameTimeS
			pt.FirstDate = rep.Saves[idx[0]].SaveDate
			pt.LastDate = rep.Saves[idx[len(idx)-1]].SaveDate
		}
		rep.Playthroughs = append(rep.Playthroughs, pt)
		rep.RealDays += float64(pt.LastDate-pt.FirstDate) / 86400
	}

	// ---- pass 2: diff consecutive pairs ----
	stats := map[diff.Kind]*KindStats{}
	subjects := map[diff.Kind]map[string]bool{}
	statFor := func(k diff.Kind) *KindStats {
		s := stats[k]
		if s == nil {
			s = &KindStats{Kind: k, GroupCombos: map[string]int{}}
			stats[k] = s
			subjects[k] = map[string]bool{}
		}
		return s
	}

	ruleAgg := map[string]*ruleAccum{}
	for _, r := range rules.All() {
		ruleAgg[r.ID] = &ruleAccum{
			enabled: opts.Rules.Enabled(r.ID),
			keys:    map[string]bool{}, redKeys: map[string]bool{},
		}
	}

	total := 0
	for _, p := range plays {
		total += len(byPlay[p]) - 1
	}
	done := 0
	for _, p := range plays {
		idx := byPlay[p]
		var prev *x4save.Snapshot
		for pos, si := range idx {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			next, err := x4save.LoadSnapshot(rep.Saves[si].Path, false)
			if err != nil {
				prev = nil
				continue
			}
			if prev == nil {
				prev = next
				continue
			}
			done++
			if opts.Progress != nil {
				opts.Progress("diff", done, total, rep.Saves[si].Name)
			}
			rep.Pairs++
			res := diff.Diff(prev, next, opts.Diff)
			if res.Refused != "" {
				rep.Refusals[string(res.Refused)]++
			} else {
				rep.Diffed++
				rep.ElapsedS += res.Elapsed
			}
			measureLogbook(&rep.Logbook, prev, next, opts.Diff.Catalog)

			fired := map[diff.Kind]bool{}
			for _, c := range res.Changes {
				s := statFor(c.Kind)
				s.Fires++
				if c.Corroborated() {
					s.Corroborated++
				} else {
					s.Uncorroborated++
				}
				s.GroupCombos[strings.Join(c.Groups(), "+")]++
				subj := c.Subject.Code
				if subj == "" {
					subj = c.Subject.ID
				}
				if subj != "" {
					subjects[c.Kind][subj] = true
				}
				fired[c.Kind] = true
				rep.Fires = append(rep.Fires, Fire{
					Kind: c.Kind, Play: p, PairIndex: pos, SaveName: rep.Saves[si].Name,
					GameTime: c.GameTime, Subject: subj, SubjectName: c.Subject.Name,
					Class: c.Subject.Class, Corroborated: c.Corroborated(),
					Groups: c.Groups(), Refs: refsOf(c), Detail: detailOf(c),
				})
			}
			for k := range fired {
				statFor(k).PairsWithFire++
			}
			for _, a := range rules.Evaluate(res.Changes, opts.Rules) {
				acc := ruleAgg[a.RuleID]
				if acc == nil {
					continue
				}
				acc.fires++
				acc.keys[a.DedupeKey] = true
				switch a.Severity {
				case rules.Red:
					acc.red++
					acc.redKeys[a.DedupeKey] = true
				case rules.Amber:
					acc.amber++
				default:
					acc.grey++
				}
				if a.Conflict {
					acc.conflicts++
				}
				if a.Downgraded != "" && !a.Conflict {
					acc.downgrades++
				}
			}
			prev = next
		}
	}

	// ---- audit: falsification ----
	falsified := auditFalsification(rep, byPlay)

	for k, s := range stats {
		s.Subjects = len(subjects[k])
		s.Falsified = falsified[k]
		rep.ByKind = append(rep.ByKind, *s)
	}
	sort.Slice(rep.ByKind, func(i, j int) bool { return rep.ByKind[i].Kind < rep.ByKind[j].Kind })

	for _, r := range rules.All() {
		acc := ruleAgg[r.ID]
		st := RuleStats{
			RuleID: r.ID, Enabled: acc.enabled, Declared: r.Severity, Confidence: r.Confidence,
			Fires: acc.fires, Alerts: len(acc.keys), Red: acc.red, Amber: acc.amber, Grey: acc.grey,
			Conflicts: acc.conflicts, Downgrades: acc.downgrades, RedAlerts: len(acc.redKeys),
		}
		if rep.RealDays > 0 {
			st.AlertsPerDay = float64(st.Alerts) / rep.RealDays
			st.RedPerWeek = float64(st.RedAlerts) / rep.RealDays * 7
		}
		rep.ByRule = append(rep.ByRule, st)
	}
	return rep, nil
}

// auditFalsification looks for changes the corpus itself contradicts.
//
// This is the only precision measurement available without a human labelling
// pass, and it is one-sided: it can prove a fire WRONG and can never prove one
// right. A ship reported lost that appears alive in a later save of the same
// playthrough was not lost. The converse — a ship correctly reported lost —
// leaves exactly the same evidence as a ship wrongly reported lost that the
// player never rebuilt, so the count below is a floor on the error rate and
// not an estimate of it.
func auditFalsification(rep *Report, byPlay map[int][]int) map[diff.Kind]int {
	for i := range rep.Fires {
		f := &rep.Fires[i]
		if f.Kind != diff.KindShipLost && f.Kind != diff.KindShipGone {
			continue
		}
		if f.Subject == "" {
			continue
		}
		idx := byPlay[f.Play]
		for pos := f.PairIndex + 1; pos < len(idx); pos++ {
			s := rep.Saves[idx[pos]]
			if s.Ships[f.Subject] || s.ShipCode[f.Subject] {
				f.Falsified = "present again in " + s.Name
				break
			}
		}
	}
	counts := map[diff.Kind]int{}
	for _, f := range rep.Fires {
		if f.Falsified != "" {
			counts[f.Kind]++
		}
	}
	return counts
}

// ruleAccum accumulates one rule's behaviour over the whole replay.
type ruleAccum struct {
	enabled bool

	fires, red, amber, grey int
	conflicts, downgrades   int
	keys, redKeys           map[string]bool
}

func refsOf(c diff.Change) []string {
	var out []string
	for _, e := range c.Evidence {
		if e.Ref != nil {
			out = append(out, e.Ref.String())
		}
	}
	sort.Strings(out)
	return out
}

func detailOf(c diff.Change) string {
	var parts []string
	for _, e := range c.Evidence {
		parts = append(parts, string(e.Source)+": "+e.Detail)
	}
	return strings.Join(parts, " | ")
}

// measureLogbook counts what the classifier saw, and how much of it could be
// joined to an entity.
func measureLogbook(ls *LogbookStats, prev, next *x4save.Snapshot, cat diff.Catalog) {
	if cat == nil || next == nil || !next.LogbookSeen {
		return
	}
	prevCodes := map[string]bool{}
	for _, s := range prev.Ships {
		if s.Code != "" {
			prevCodes[s.Code] = true
		}
	}
	for _, st := range prev.Stations {
		if st.Code != "" {
			prevCodes[st.Code] = true
		}
	}
	for i := range next.Logbook {
		e := &next.Logbook[i]
		if e.Time <= prev.GameTimeS || e.Time > next.GameTimeS {
			continue
		}
		ls.WindowEntries++
		ev, ok := cat.Classify(e.Title)
		if !ok {
			continue
		}
		ls.WindowClassified++
		ls.ByEventKind[string(ev.Kind)]++
		ls.ByRef[ev.Ref.String()]++
		if ev.Kind == logbook.EventDestroyed {
			ls.DestroyEntries++
			switch {
			case ev.Code == "":
				ls.DestroyNoCode++
			case !prevCodes[ev.Code]:
				ls.DestroyUnmatched++
			}
		}
	}
}

func saveKind(path string) string {
	base := filepath.Base(path)
	switch {
	case strings.Contains(base, "autosave"):
		return "autosave"
	case strings.Contains(base, "quicksave"):
		return "quicksave"
	default:
		return "save"
	}
}

func sameOrder(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ReadOnly is a belt-and-braces assertion for the archive path: the harness
// must never be pointed at the live save directory, and must never open
// anything for writing. It checks the directory is not one of the discovered
// live save roots.
func ReadOnly(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for _, root := range x4save.DefaultSaveRoots() {
		r, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if abs == r || strings.HasPrefix(abs+string(os.PathSeparator), r+string(os.PathSeparator)) {
			return fmt.Errorf("%s is inside a live X4 save root (%s); the replay harness runs against the ARCHIVE, never the live saves", abs, r)
		}
	}
	return nil
}
