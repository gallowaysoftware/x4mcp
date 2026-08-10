// Command distill-save turns a real X4 savegame into a small, committable test
// fixture: it streams the save, keeps only the sections the parser reads, and
// scrubs every value that identifies the player or the playthrough.
//
// This repository is public. A raw savegame is not: it carries the player's
// name, the playthrough GUID and seed, and whatever free text the player typed
// into their ship names. So the tool does two jobs that must both succeed —
// prune (so ~800 MB of XML becomes ~2 MB) and scrub (so what is left names
// nobody) — and it refuses to leave an output file behind if the scrub check
// finds a single hit.
//
// Usage:
//
//	go run ./scripts/distill-save -in ~/.config/EgoSoft/X4/<profile>/save/quicksave.xml.gz \
//	    -out internal/x4save/testdata/real/distilled.xml.gz
//	go run ./scripts/distill-save -in <save> -plan          # report only, write nothing
//
//	# re-verify a committed fixture. Prefer the -in form: it derives the full
//	# term list from the save itself, so nothing identifying is typed on a
//	# command line (and into shell history), and nothing is left unchecked.
//	go run ./scripts/distill-save -verify <fixture.xml.gz> -in <save>
//	go run ./scripts/distill-save -verify <fixture.xml.gz> -scrub "Some Name,…"
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	var (
		in         = flag.String("in", "", "path to a real savegame (*.xml.gz)")
		out        = flag.String("out", "", "path to write the distilled fixture (*.xml.gz)")
		plan       = flag.Bool("plan", false, "scan only: report what would be kept, write nothing")
		verify     = flag.String("verify", "", "verify an existing fixture instead of distilling")
		npcMax     = flag.Int("npc-stations", 3, "how many knownto=player NPC stations to keep")
		threatMax  = flag.Int("threats", 10, "how many Kha'ak and how many Xenon components to keep (each)")
		claimMax   = flag.Int("claimable", 12, "how many ownerless ships to keep")
		fieldMax   = flag.Int("fields", 6, "how many minable fields to keep per sector")
		scriptMax  = flag.Int64("max-script-bytes", 400_000, "keep a tracked MD story script only if smaller than this")
		extraScrub = flag.String("scrub", "", "comma-separated extra strings to scrub/verify")
	)
	flag.Parse()

	extra := splitList(*extraScrub)

	if *verify != "" {
		terms, derived, err := verifyTerms(*in, extra, *scriptMax)
		if err != nil {
			fatal(err)
		}
		hits, err := verifyFixture(*verify, terms)
		if err != nil {
			fatal(err)
		}
		reportVerify(*verify, terms, hits, !derived)
		if len(hits) > 0 {
			os.Exit(1)
		}
		return
	}

	if *in == "" {
		fatal(fmt.Errorf("-in is required"))
	}

	scan, err := scanSave(*in, *scriptMax)
	if err != nil {
		fatal(err)
	}
	scan.report(os.Stdout)
	if *plan {
		return
	}
	if *out == "" {
		fatal(fmt.Errorf("-out is required (or pass -plan)"))
	}

	sc, err := newScrubber(scan, append(machineTerms(), extra...))
	if err != nil {
		fatal(err)
	}
	cfg := distillConfig{
		npcStations: *npcMax,
		threats:     *threatMax,
		claimable:   *claimMax,
		fieldsPer:   *fieldMax,
		keepScripts: scan.keepScripts,
	}
	stats, err := distill(*in, *out, sc, cfg)
	if err != nil {
		fatal(err)
	}
	stats.report(os.Stdout, *in, *out)

	// The scrub is the part that must not be taken on trust: verify the file we
	// just wrote and delete it if anything identifying survived, so a dirty
	// fixture can never be the thing sitting in the working tree at commit time.
	hits, err := verifyFixture(*out, sc.terms())
	if err != nil {
		fatal(err)
	}
	reportVerify(*out, sc.terms(), hits, true)
	if len(hits) > 0 {
		_ = os.Remove(*out)
		fatal(fmt.Errorf("scrub verification failed; %s removed", *out))
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "distill-save:", err)
	os.Exit(1)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- pass A: scan ----

// trackedScripts are the MD story scripts the parser reads plot cues from (the
// knownPlots map in internal/x4save). Kept in sync by hand; a script missing
// here only means its cues are absent from the fixture.
var trackedScripts = map[string]bool{
	"Story_Thefan": true, "Story_Buccaneers": true, "Story_Pirate_Prelude": true,
	"Story_Criminal": true, "Story_Research_Erlking": true, "DataVaultLogbook": true,
	"Story_HQ_Discovery": true, "Story_Boron": true, "Story_Split": true,
	"Story_Terran_Core": true, "Story_Paranid": true, "Story_Terraforming": true,
	"Story_Yaki": true, "Story_Hyperion": true, "Story_Covert_Operations": true,
	"Story_Diplomacy_Intro": true, "Story_Unbihexium": true,
	"X4Ep1_Mentor_Subscriptions": true,
}

// scanResult is what the first pass learns: the identifying strings the second
// pass has to scrub, and which story scripts are small enough to keep. Both are
// needed before the first byte of output is written, which is why there are two
// passes at all — <info> is cheap, but a script's size is only known after it.
type scanResult struct {
	playerName  string
	guid        string
	seed        string
	gameCode    string
	saveName    string
	saveDate    string
	startType   string
	scriptBytes map[string]int64
	keepScripts map[string]bool
}

func scanSave(path string, scriptMax int64) (*scanResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	res := &scanResult{scriptBytes: map[string]int64{}, keepScripts: map[string]bool{}}
	dec := xml.NewDecoder(gz)
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case depth == 2 && t.Name.Local == "info":
				if err := res.readInfo(dec, &t); err != nil {
					return nil, err
				}
				depth--
			case depth == 2 && t.Name.Local == "md":
				// descend; its <script> children are what we are measuring
			case depth == 3 && t.Name.Local == "script":
				name := attr(t, "name")
				off := dec.InputOffset()
				if err := dec.Skip(); err != nil {
					return nil, err
				}
				if trackedScripts[name] {
					res.scriptBytes[name] = dec.InputOffset() - off
				}
				depth--
			case depth >= 2:
				if err := dec.Skip(); err != nil {
					return nil, err
				}
				depth--
			}
		case xml.EndElement:
			depth--
		}
	}
	for name, n := range res.scriptBytes {
		if n <= scriptMax {
			res.keepScripts[name] = true
		}
	}
	return res, nil
}

func (r *scanResult) readInfo(dec *xml.Decoder, start *xml.StartElement) error {
	var info struct {
		Save struct {
			Name string `xml:"name,attr"`
			Date string `xml:"date,attr"`
		} `xml:"save"`
		Game struct {
			GUID  string `xml:"guid,attr"`
			Seed  string `xml:"seed,attr"`
			Code  string `xml:"code,attr"`
			Start string `xml:"start,attr"`
		} `xml:"game"`
		Player struct {
			Name string `xml:"name,attr"`
		} `xml:"player"`
	}
	if err := dec.DecodeElement(&info, start); err != nil {
		return fmt.Errorf("scan info: %w", err)
	}
	r.playerName, r.guid, r.seed = info.Player.Name, info.Game.GUID, info.Game.Seed
	r.gameCode, r.saveName, r.startType = info.Game.Code, info.Save.Name, info.Game.Start
	r.saveDate = info.Save.Date
	return nil
}

func (r *scanResult) report(w io.Writer) {
	fmt.Fprintf(w, "scan: player=%q guid=%q seed=%q code=%q save=%q date=%q start=%q\n",
		r.playerName, r.guid, r.seed, r.gameCode, r.saveName, r.saveDate, r.startType)
	names := make([]string, 0, len(r.scriptBytes))
	for n := range r.scriptBytes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		verdict := "drop (too big)"
		if r.keepScripts[n] {
			verdict = "keep"
		}
		fmt.Fprintf(w, "scan: md script %-30s %9d B  %s\n", n, r.scriptBytes[n], verdict)
	}
}

// ---- scrubbing ----

// scrubber rewrites identifying strings out of every attribute value and text
// node on its way to the output. It replaces whole names AND their individual
// word tokens: the player's full name appears in <info>, but a log entry may
// only use the surname.
//
// Matching is case-INSENSITIVE. A save spells the player's name however the
// player typed it in each place — "RADA VANTAI" on a ship's code, "rada vantai"
// in a mod's log line — and a byte-exact scrub would leave those behind while
// the byte-exact verification pass reported "clean". So terms are stored folded
// to lower case and compared with strings.EqualFold; the placeholder is written
// in its own casing.
type scrubber struct {
	from []string          // folded to lower case, longest first
	to   map[string]string // folded term -> cased placeholder
}

const (
	placeholderPlayer = "Test Pilot"
	placeholderGUID   = "00000000-0000-4000-8000-000000000000"
	placeholderSeed   = "1234567890"
	placeholderCode   = "0000000"
	placeholderSave   = "Distilled Fixture"
	// The real <save date> is wall-clock: it says when the player was sitting at
	// the machine. Nothing the parser tests care about, so it is pinned.
	placeholderDate = "1700000000"
)

// minTermLen is the shortest string this tool will search-and-replace. Below it
// a term stops being an identifier and becomes a substring of everything: a
// save named "X" would rewrite code="XYZ-123" into garbage, and — because the
// term genuinely is gone afterwards — the verification pass would call the
// vandalised fixture clean. Case-insensitive matching makes that worse, not
// better ("Ore" would eat every ware="ore").
const minTermLen = 4

// tooShortError is what the scrubber returns instead of quietly skipping a term
// it cannot handle. Silently keeping an identifier is the worst of the three
// options: the operator believes the file was scrubbed and it was not.
type tooShortError struct {
	kind string
	term string
}

func (e *tooShortError) Error() string {
	return fmt.Sprintf("%s %q is only %d character(s) long: refusing to distill.\n"+
		"  A term shorter than %d characters matches unrelated text all over a savegame, so scrubbing it\n"+
		"  would corrupt the fixture while still reporting \"clean\" — and skipping it would leave the\n"+
		"  identifier in a public file. Rename it in-game and re-save, or distill a different save.",
		e.kind, e.term, len([]rune(e.term)), minTermLen)
}

func newScrubber(scan *scanResult, extra []string) (*scrubber, error) {
	s := &scrubber{to: map[string]string{}}
	add := func(kind, from, to string) error {
		if from == "" || strings.EqualFold(from, to) {
			return nil
		}
		if len([]rune(from)) < minTermLen {
			return &tooShortError{kind: kind, term: from}
		}
		key := strings.ToLower(from)
		if _, dup := s.to[key]; dup {
			return nil
		}
		s.to[key] = to
		s.from = append(s.from, key)
		return nil
	}
	if err := add("player name", scan.playerName, placeholderPlayer); err != nil {
		return nil, err
	}
	tokens := strings.Fields(scan.playerName)
	for i, tok := range tokens {
		repl := "Anon" + fmt.Sprint(i+1)
		if i == 0 {
			repl = "Test"
		} else if i == 1 {
			repl = "Pilot"
		}
		if err := add("player-name token", tok, repl); err != nil {
			return nil, err
		}
	}
	for _, t := range []struct{ kind, from, to string }{
		{"game guid", scan.guid, placeholderGUID},
		{"game seed", scan.seed, placeholderSeed},
		{"game code", scan.gameCode, placeholderCode},
		{"save name", scan.saveName, placeholderSave},
		{"save date", scan.saveDate, placeholderDate},
	} {
		if err := add(t.kind, t.from, t.to); err != nil {
			return nil, err
		}
	}
	for _, t := range extra {
		if err := add("-scrub term", t, "redacted"); err != nil {
			return nil, err
		}
	}
	// Longest first, so a full name is replaced before its own first token is.
	sort.Slice(s.from, func(i, j int) bool { return len(s.from[i]) > len(s.from[j]) })
	return s, nil
}

// machineTerms are strings that identify the machine this ran on rather than
// the playthrough — a save should never contain them, and the verification pass
// proves it rather than assuming it. Short terms are dropped rather than
// refused: unlike a player name, a two-letter username is not something the
// fixture is obliged to have removed, it is an assertion that never applies.
func machineTerms() []string {
	var out []string
	add := func(s string) {
		if len([]rune(s)) >= minTermLen && s != "root" && s != "/" {
			out = append(out, s)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(home)
	}
	if u, err := user.Current(); err == nil {
		add(u.Username)
		add(u.Name)
	}
	return out
}

// clean rewrites v in ONE left-to-right pass, skipping past each replacement it
// emits. Running strings.ReplaceAll per term instead would let a later term
// match text an earlier term just wrote: a game code of "1234567" would eat the
// front of the seed placeholder "1234567890" and leave "0000000890" behind.
// A scrubber that corrupts its own placeholders is hard to notice and impossible
// to trust.
func (s *scrubber) clean(v string) string {
	if !s.touches(v) {
		return v
	}
	var b strings.Builder
	b.Grow(len(v))
	for i := 0; i < len(v); {
		hit := ""
		for _, from := range s.from { // longest first, already folded
			// EqualFold over equal byte spans: exact for the ASCII identifiers a
			// savegame carries, and it can only ever miss (never over-match) on the
			// exotic runes whose case change alters their encoded length.
			if len(from) <= len(v)-i && strings.EqualFold(v[i:i+len(from)], from) {
				hit = from
				break
			}
		}
		if hit == "" {
			b.WriteByte(v[i])
			i++
			continue
		}
		b.WriteString(s.to[hit])
		i += len(hit)
	}
	return b.String()
}

// touches is the fast path that keeps clean()'s per-byte loop off the ~775 MB
// of XML that contains nothing identifying. One fold per value, not per term.
func (s *scrubber) touches(v string) bool {
	lower := strings.ToLower(v)
	for _, from := range s.from {
		if strings.Contains(lower, from) {
			return true
		}
	}
	return false
}

func (s *scrubber) terms() []string { return append([]string(nil), s.from...) }

// ---- pass B: distill ----

type distillConfig struct {
	npcStations int
	threats     int
	claimable   int
	fieldsPer   int
	keepScripts map[string]bool
}

type distillStats struct {
	inBytes, outBytes         int64
	rawIn, rawOut             int64
	playerShips, playerStatns int
	npcStations, gates        int
	khaak, xenon, claimable   int
	fields, sectors, scripts  int
	playerOther               int
}

func (s distillStats) report(w io.Writer, in, out string) {
	fmt.Fprintf(w, "distill: %s (%.1f MB gz / %.1f MB xml) -> %s (%.2f MB gz / %.2f MB xml)\n",
		filepath.Base(in), mb(s.inBytes), mb(s.rawIn), filepath.Base(out), mb(s.outBytes), mb(s.rawOut))
	fmt.Fprintf(w, "distill: kept %d player ships, %d player stations, %d other player components, %d NPC stations, %d gates, %d sectors\n",
		s.playerShips, s.playerStatns, s.playerOther, s.npcStations, s.gates, s.sectors)
	fmt.Fprintf(w, "distill: kept %d Kha'ak, %d Xenon, %d ownerless ships, %d minable fields, %d MD scripts\n",
		s.khaak, s.xenon, s.claimable, s.fields, s.scripts)
}

func mb(n int64) float64 { return float64(n) / (1 << 20) }

type action int

const (
	actDrop action = iota
	actDescend
	// actDescendEmit descends but writes the element's own start tag no matter
	// what: sector shells are emitted even when everything inside them is
	// pruned, so the fixture's sector list still matches the real save's.
	actDescendEmit
	actKeep
	actAttrsOnly
)

// noise is dropped even inside a kept subtree: pure runtime plumbing that no
// parser field is derived from, and a large share of a player ship's bytes.
var noise = map[string]bool{
	"listeners":   true,
	"events":      true,
	"masstraffic": true,
	// The player character's "known" list is one entry per discovered object —
	// tens of thousands of them, and nothing in Snapshot reads it.
	"known": true,
}

type frame struct {
	name    string
	attrs   []xml.Attr
	written bool
	keep    bool
}

func distill(inPath, outPath string, sc *scrubber, cfg distillConfig) (distillStats, error) {
	var st distillStats
	fi, err := os.Stat(inPath)
	if err != nil {
		return st, err
	}
	st.inBytes = fi.Size()

	f, err := os.Open(inPath)
	if err != nil {
		return st, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return st, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return st, err
	}
	of, err := os.Create(outPath)
	if err != nil {
		return st, err
	}
	defer of.Close()
	zw, err := gzip.NewWriterLevel(of, gzip.BestCompression)
	if err != nil {
		return st, err
	}
	w := newXMLWriter(zw, sc)
	w.raw(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")

	dec := xml.NewDecoder(gz)
	var stack []frame
	fieldsInSector := 0

	flushAncestors := func() {
		for i := range stack {
			if !stack[i].written {
				w.start(stack[i].name, stack[i].attrs)
				stack[i].written = true
			}
		}
	}
	emit := func(t xml.StartElement, keep bool) {
		flushAncestors()
		w.start(t.Name.Local, t.Attr)
		stack = append(stack, frame{name: t.Name.Local, attrs: t.Attr, written: true, keep: keep})
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return st, fmt.Errorf("distill: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			inKeep := len(stack) > 0 && stack[len(stack)-1].keep
			if inKeep {
				if noise[t.Name.Local] {
					if err := dec.Skip(); err != nil {
						return st, err
					}
					continue
				}
				emit(t, true)
				continue
			}
			switch a := decide(stack, t, cfg, &st, &fieldsInSector); a {
			case actKeep:
				emit(t, true)
			case actDescendEmit:
				emit(t, false)
			case actDescend:
				stack = append(stack, frame{name: t.Name.Local, attrs: t.Attr})
			case actAttrsOnly:
				flushAncestors()
				w.start(t.Name.Local, t.Attr)
				w.end(t.Name.Local)
				if err := dec.Skip(); err != nil {
					return st, err
				}
			default:
				if err := dec.Skip(); err != nil {
					return st, err
				}
			}
		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			fr := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if fr.written {
				w.end(fr.name)
			}
		case xml.CharData:
			if len(stack) > 0 && stack[len(stack)-1].keep && len(strings.TrimSpace(string(t))) > 0 {
				flushAncestors()
				w.chardata(string(t))
			}
		}
	}

	if err := w.flush(); err != nil {
		return st, err
	}
	st.rawOut = w.written
	if err := zw.Close(); err != nil {
		return st, err
	}
	if err := of.Close(); err != nil {
		return st, err
	}
	if ofi, err := os.Stat(outPath); err == nil {
		st.outBytes = ofi.Size()
	}
	st.rawIn = dec.InputOffset()
	return st, nil
}

var structuralClasses = map[string]bool{
	"galaxy": true, "cluster": true, "sector": true,
	"zone": true, "region": true, "highway": true,
}

// boringPlayerClasses mirrors internal/x4save: player-owned components the
// parser refuses to surface, so there is no reason to carry them.
var boringPlayerClasses = map[string]bool{"computer": true, "npc": true}

// decide is the pruning policy, expressed as one switch so the shape of the
// fixture is readable in one place. Anything not named here is dropped.
func decide(stack []frame, t xml.StartElement, cfg distillConfig, st *distillStats, fieldsInSector *int) action {
	name := t.Name.Local
	depth := len(stack) // 0 = the <savegame> root itself

	switch depth {
	case 0:
		return actDescend // <savegame>
	case 1:
		switch name {
		case "info", "log", "stats", "missions", "operations", "notifications", "fleetmanager":
			return actKeep
		case "md", "universe":
			return actDescend
		}
		return actDrop
	case 2:
		switch stack[1].name {
		case "md":
			if name == "vars" {
				return actKeep
			}
			if name == "script" && cfg.keepScripts[attr(t, "name")] {
				st.scripts++
				return actKeep
			}
			return actDrop
		case "universe":
			switch name {
			case "factions", "blacklists", "traderules", "diplomacy":
				return actKeep
			case "component":
				return actDescend
			}
			return actDrop
		}
		return actDrop
	}

	// Inside <universe><component> — the galaxy tree.
	if name != "component" {
		if name == "field" {
			if *fieldsInSector >= cfg.fieldsPer {
				return actDrop
			}
			*fieldsInSector++
			st.fields++
			return actAttrsOnly
		}
		// Structural plumbing (<connections>, <connection>, <zones>, ...): descend,
		// but emit nothing unless a descendant turns out to be worth keeping.
		return actDescend
	}

	cls, owner := attr(t, "class"), attr(t, "owner")
	switch {
	case structuralClasses[cls]:
		if cls == "sector" {
			st.sectors++
			*fieldsInSector = 0
			return actDescendEmit
		}
		return actDescend
	case owner == "player" && (strings.HasPrefix(cls, "ship_") || cls == "station"):
		if cls == "station" {
			st.playerStatns++
		} else {
			st.playerShips++
		}
		return actKeep
	case owner == "player" && !boringPlayerClasses[cls]:
		// Satellites, nav beacons, build storages: the parser counts these in
		// OtherCounts, and a build storage's subtree is where Probe A has to
		// look for required-vs-present build wares.
		st.playerOther++
		return actKeep
	case cls == "station" && strings.Contains(attr(t, "knownto"), "player"):
		if st.npcStations >= cfg.npcStations {
			return actDrop
		}
		st.npcStations++
		return actKeep
	case cls == "gate":
		st.gates++
		return actKeep
	case owner == "ownerless" && strings.HasPrefix(cls, "ship_"):
		if st.claimable >= cfg.claimable {
			return actDrop
		}
		st.claimable++
		return actAttrsOnly
	case owner == "khaak":
		if st.khaak >= cfg.threats {
			return actDrop
		}
		st.khaak++
		return actAttrsOnly
	case owner == "xenon":
		if st.xenon >= cfg.threats {
			return actDrop
		}
		st.xenon++
		return actAttrsOnly
	}
	return actDrop
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// ---- XML writer ----

// xmlWriter emits one element per line and collapses <a></a> back to <a/>, so
// the distilled fixture keeps the line-per-element shape of a real save (which
// is what makes `zcat fixture | grep -n` a usable verification tool).
type xmlWriter struct {
	w       *bufio.Writer
	sc      *scrubber
	pending *pendingStart
	written int64
}

type pendingStart struct {
	name  string
	attrs []xml.Attr
}

func newXMLWriter(w io.Writer, sc *scrubber) *xmlWriter {
	return &xmlWriter{w: bufio.NewWriterSize(w, 1<<20), sc: sc}
}

func (x *xmlWriter) raw(s string) {
	n, _ := x.w.WriteString(s)
	x.written += int64(n)
}

func (x *xmlWriter) flushPending() {
	if x.pending == nil {
		return
	}
	p := x.pending
	x.pending = nil
	x.raw("<" + p.name)
	x.writeAttrs(p.attrs)
	x.raw(">\n")
}

func (x *xmlWriter) writeAttrs(attrs []xml.Attr) {
	for _, a := range attrs {
		if a.Name.Local == "xmlns" || a.Name.Space != "" {
			continue
		}
		x.raw(" " + a.Name.Local + `="` + escapeAttr(x.sc.clean(a.Value)) + `"`)
	}
}

func (x *xmlWriter) start(name string, attrs []xml.Attr) {
	x.flushPending()
	x.pending = &pendingStart{name: name, attrs: attrs}
}

func (x *xmlWriter) end(name string) {
	if p := x.pending; p != nil && p.name == name {
		x.pending = nil
		x.raw("<" + p.name)
		x.writeAttrs(p.attrs)
		x.raw("/>\n")
		return
	}
	x.flushPending()
	x.raw("</" + name + ">\n")
}

func (x *xmlWriter) chardata(s string) {
	x.flushPending()
	x.raw(escapeText(x.sc.clean(s)))
}

func (x *xmlWriter) flush() error {
	x.flushPending()
	return x.w.Flush()
}

var attrEscaper = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "\n", "&#10;", "\r", "&#13;", "\t", "&#9;",
)

var textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func escapeAttr(s string) string { return attrEscaper.Replace(s) }
func escapeText(s string) string { return textEscaper.Replace(s) }

// ---- verification ----

type hit struct {
	term string
	line int
	text string
}

// verifyTerms builds the list -verify searches for, and refuses to run without
// at least one term from the playthrough itself.
//
// Machine terms alone (home directory, OS username, OS full name) are a false
// assurance: a savegame never contains them, so "0 hits — clean" is guaranteed
// and proves nothing about the player's name, the GUID, the seed or the save
// date — exactly the values the fixture exists to have removed. Passing -in
// derives the whole list from the source save, which also means no identifier
// has to be typed onto a command line and into shell history.
func verifyTerms(in string, extra []string, scriptMax int64) (terms []string, derived bool, err error) {
	if in != "" {
		scan, err := scanSave(in, scriptMax)
		if err != nil {
			return nil, false, err
		}
		sc, err := newScrubber(scan, append(machineTerms(), extra...))
		if err != nil {
			return nil, false, err
		}
		return sc.terms(), true, nil
	}
	if len(extra) == 0 {
		return nil, false, fmt.Errorf("-verify needs the playthrough's own terms, or it only checks machine\n" +
			"  identifiers a savegame never contains and reports \"clean\" no matter what is in the file.\n" +
			"  Pass -in <the source save> to derive them (nothing lands in shell history), or list them\n" +
			"  explicitly with -scrub \"<player name>,<guid>,<seed>,…\".")
	}
	return append(machineTerms(), extra...), false, nil
}

// verifyFixture decompresses a fixture and searches every line for each term.
// It is deliberately a dumb substring scan over the whole decompressed stream:
// the point is to prove absence, and anything cleverer could be wrong in a way
// that looks like success. The one concession is case: matching folds, because
// the scrubber folds, and a check narrower than the scrub cannot police it.
func verifyFixture(path string, terms []string) ([]hit, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	folded := make([]string, 0, len(terms))
	for _, term := range terms {
		if term != "" {
			folded = append(folded, strings.ToLower(term))
		}
	}

	var hits []hit
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		lower := strings.ToLower(text)
		for _, term := range folded {
			if strings.Contains(lower, term) {
				snippet := text
				if len(snippet) > 160 {
					snippet = snippet[:160]
				}
				hits = append(hits, hit{term: term, line: line, text: snippet})
			}
		}
	}
	return hits, sc.Err()
}

// reportVerify names the terms only when the operator supplied them by hand —
// they are already in that shell's history. Terms derived from a save with -in
// are counted, not printed, so re-verifying a fixture does not put the player's
// name back on a terminal (or in a CI log).
func reportVerify(path string, terms []string, hits []hit, showTerms bool) {
	if showTerms {
		fmt.Printf("verify: %s against %d term(s): %v\n", filepath.Base(path), len(terms), terms)
	} else {
		fmt.Printf("verify: %s against %d term(s) derived from the source save\n", filepath.Base(path), len(terms))
	}
	if len(hits) == 0 {
		fmt.Println("verify: 0 hits — clean")
		return
	}
	for _, h := range hits {
		fmt.Printf("verify: HIT %q at line %d: %s\n", h.term, h.line, h.text)
	}
}
