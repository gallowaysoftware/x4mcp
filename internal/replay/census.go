package replay

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pequalsnp/x4mcp/internal/logbook"
	"github.com/pequalsnp/x4mcp/internal/x4data"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// The census answers one question: how many DISTINCT KINDS of thing does a
// logbook contain?
//
// The tech design's estimate was that ~4,415 uncategorized entries would
// collapse to "dozens" of templates. That is the number to check, not to
// repeat, and the two halves of the check are different measurements:
//
//   - Rows vs events. X4's <log> is cumulative, so the same event appears in
//     every save written after it. 200 saves × ~17,000 rows is ~3.4 M ROWS and
//     far fewer EVENTS; a census over rows measures the archive's sampling
//     cadence, not the game. Distinct (time, title, text) is the event.
//   - Templates vs signatures. A template is a {page,id} recovered from the
//     game's own text database. A signature is a hash of a masked sentence and
//     is a fallback, useful for counting and unusable as a rule key.

// LogRow is one distinct logbook event, deduplicated across the archive.
type LogRow struct {
	Time     float64
	Category string
	Title    string
	Text     string
	Faction  string // the raw {page,id} the parser already captures
	Entity   string
	Money    *int64

	Rows int // how many archive saves carried it
	Play int // playthrough index
}

// Census is the measured template population.
type Census struct {
	SavesScanned int `json:"saves_scanned"`
	TotalRows    int `json:"total_rows"`
	DistinctLog  int `json:"distinct_events"`

	Uncategorized int            `json:"uncategorized_events"`
	ByCategory    map[string]int `json:"by_category"`

	CatalogTemplates int `json:"catalog_templates"`
	CatalogSkipped   int `json:"catalog_skipped"`

	Matched       int            `json:"matched_events"`
	MatchedRefs   int            `json:"distinct_refs"`
	AmbiguousHits int            `json:"ambiguous_matches"`
	ByRef         map[string]int `json:"by_ref"`
	ByPage        map[string]int `json:"by_page"`

	// RuleClassified is the subset the ORDERED RuleRefs table recognised —
	// the only number that describes what a rule would actually key on.
	RuleClassified int            `json:"rule_classified_events"`
	ByEventKind    map[string]int `json:"by_event_kind"`

	Unmatched          int `json:"unmatched_events"`
	UnmatchedSignature int `json:"unmatched_signatures"`

	// Top rows for the report.
	TopRefs       []CensusRow `json:"top_refs"`
	TopSignatures []CensusRow `json:"top_signatures"`

	VocabularySize int `json:"masker_vocabulary"`
}

// CensusRow is one template or signature with its weight and an example.
type CensusRow struct {
	Key      string `json:"key"`
	Template string `json:"template,omitempty"`
	Events   int    `json:"events"`
	Rows     int    `json:"rows"`
	Example  string `json:"example"`
	Rivals   string `json:"rivals,omitempty"`
}

// CensusOptions configures a census run.
type CensusOptions struct {
	Dir      string
	Limit    int
	Top      int
	Progress func(stage string, i, n int, name string)

	// SectorNames enriches the masker's vocabulary.
	//
	// It has to be passed in because Sector.Name is resolved from the GAME
	// INSTALL after load and is deliberately not part of the cached snapshot —
	// so a census reading snapshots out of the cache sees every sector name as
	// "". Without this the masker's whole vocabulary is the handful of assets
	// the player renamed, and the residue keeps every sector name in it.
	SectorNames []string

	// TSV, when non-nil, receives every distinct event as a tab-separated row.
	// This is the tech design's "logbook TSV dump", and it deliberately writes
	// the DISTINCT events rather than the rows: 3.4 M lines of the same 17,000
	// sentences is not a dump, it is a sampling artefact.
	TSV io.Writer
}

// RunCensus scans the archive's logbooks and measures the template population.
func RunCensus(ctx context.Context, db *x4data.TextDB, opts CensusOptions) (*Census, error) {
	files, err := filepath.Glob(filepath.Join(opts.Dir, "*.xml.gz"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	if opts.Limit > 0 && len(files) > opts.Limit {
		files = files[len(files)-opts.Limit:]
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no *.xml.gz in %s", opts.Dir)
	}
	if opts.Top <= 0 {
		opts.Top = 40
	}

	c := &Census{
		ByCategory:  map[string]int{},
		ByRef:       map[string]int{},
		ByPage:      map[string]int{},
		ByEventKind: map[string]int{},
	}

	events := map[string]*LogRow{}
	guidPlay := map[string]int{}
	// The masking vocabulary, accumulated across the whole corpus so that one
	// masker can be applied to every distinct sentence exactly once.
	sectorNames := map[string]bool{}
	assetNames := map[string]bool{}

	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if opts.Progress != nil {
			opts.Progress("census", i+1, len(files), filepath.Base(f))
		}
		snap, err := x4save.LoadSnapshot(f, false)
		if err != nil {
			continue
		}
		c.SavesScanned++
		play, ok := guidPlay[snap.GameGUID]
		if !ok {
			play = len(guidPlay) + 1
			guidPlay[snap.GameGUID] = play
		}
		for _, s := range snap.Sectors {
			if s.Name != "" {
				sectorNames[s.Name] = true
			}
		}
		for _, s := range snap.Ships {
			if s.Name != "" {
				assetNames[s.Name] = true
			}
		}
		for _, s := range snap.Stations {
			if s.Name != "" {
				assetNames[s.Name] = true
			}
		}
		for _, s := range snap.TradeStations {
			if s.Name != "" {
				assetNames[s.Name] = true
			}
		}
		// Within ONE save the log holds repeats: 154 of ~9,000 entries in the
		// committed fixture share a (time, title, text) triple with another —
		// two reputation ticks landing on the same in-game second are one
		// sentence written twice, not one event seen twice. So the identity
		// carries the occurrence index, and two copies of one save deduplicate
		// EXACTLY rather than approximately.
		occ := map[string]int{}
		for j := range snap.Logbook {
			e := &snap.Logbook[j]
			c.TotalRows++
			triple := strconv.FormatFloat(e.Time, 'f', 3, 64) + "\x00" + e.Title + "\x00" + e.Text
			occ[triple]++
			key := triple + "\x00#" + strconv.Itoa(occ[triple])
			row, seen := events[key]
			if !seen {
				row = &LogRow{
					Time: e.Time, Category: e.Category, Title: e.Title, Text: e.Text,
					Faction: e.Faction, Entity: e.Entity, Money: e.Money,
					Play: play,
				}
				events[key] = row
			}
			row.Rows++
		}
	}

	c.DistinctLog = len(events)

	// Ordered for determinism: two runs must produce the same table.
	keys := make([]string, 0, len(events))
	for k := range events {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		row := events[k]
		if row.Category == "" {
			c.Uncategorized++
			c.ByCategory["(absent)"]++
		} else {
			c.ByCategory[row.Category]++
		}
	}

	if opts.TSV != nil {
		w := csv.NewWriter(opts.TSV)
		w.Comma = '\t'
		_ = w.Write([]string{"play", "game_time_s", "category", "faction_ref", "entity", "money", "rows", "title", "text"})
		for _, k := range keys {
			r := events[k]
			money := ""
			if r.Money != nil {
				money = strconv.FormatInt(*r.Money, 10)
			}
			_ = w.Write([]string{
				strconv.Itoa(r.Play),
				strconv.FormatFloat(r.Time, 'f', 3, 64),
				r.Category, r.Faction, r.Entity, money,
				strconv.Itoa(r.Rows),
				strings.ReplaceAll(r.Title, "\n", `\n`),
				strings.ReplaceAll(r.Text, "\n", `\n`),
			})
		}
		w.Flush()
	}

	// ---- template matching ----
	full := logbook.NewCatalog(db)
	rules := logbook.NewCatalog(db, logbook.RulePages()...)
	c.CatalogTemplates = full.Len()
	c.CatalogSkipped = full.Skipped

	for _, n := range opts.SectorNames {
		if n != "" {
			sectorNames[n] = true
		}
	}
	masker := logbook.NewMasker(sortedSet(sectorNames), sortedSet(assetNames))
	c.VocabularySize = masker.Size()

	refAgg := map[string]*censusAgg{}
	sigAgg := map[string]*censusAgg{}

	for _, k := range keys {
		row := events[k]
		title := row.Title
		if strings.TrimSpace(title) == "" {
			continue
		}
		if ev, ok := rules.Classify(title); ok {
			c.RuleClassified++
			c.ByEventKind[string(ev.Kind)]++
		}
		m, ok := full.Match(title)
		if ok {
			c.Matched++
			c.ByRef[m.Ref.String()]++
			c.ByPage[strconv.Itoa(m.Ref.Page)]++
			if m.Ambiguous {
				c.AmbiguousHits++
			}
			a := refAgg[m.Ref.String()]
			if a == nil {
				tpl := ""
				if t, ok := full.Template(m.Ref); ok {
					tpl = t.Text
				}
				a = &censusAgg{example: title, template: tpl, rivals: refsToString(m.Rivals)}
				refAgg[m.Ref.String()] = a
			}
			a.events++
			a.rows += row.Rows
			continue
		}
		c.Unmatched++
		sig := logbook.Signature(masker.Mask(title))
		a := sigAgg[sig]
		if a == nil {
			a = &censusAgg{example: title, template: masker.Mask(title)}
			sigAgg[sig] = a
		}
		a.events++
		a.rows += row.Rows
	}
	c.MatchedRefs = len(refAgg)
	c.UnmatchedSignature = len(sigAgg)

	c.TopRefs = topRows(refAgg, opts.Top)
	c.TopSignatures = topRows(sigAgg, opts.Top)
	return c, nil
}

// censusAgg accumulates one template's or one signature's weight.
type censusAgg struct {
	events, rows int
	example      string
	template     string
	rivals       string
}

func topRows(m map[string]*censusAgg, n int) []CensusRow {
	rows := make([]CensusRow, 0, len(m))
	for k, s := range m {
		rows = append(rows, CensusRow{
			Key: k, Template: s.template, Events: s.events, Rows: s.rows,
			Example: s.example, Rivals: s.rivals,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Events != rows[j].Events {
			return rows[i].Events > rows[j].Events
		}
		return rows[i].Key < rows[j].Key
	})
	if n > 0 && len(rows) > n {
		rows = rows[:n]
	}
	return rows
}

func refsToString(rs []logbook.Ref) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.String())
	}
	return strings.Join(out, " ")
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
