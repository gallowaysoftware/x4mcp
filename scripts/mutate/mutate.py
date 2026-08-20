#!/usr/bin/env python3
"""Mutation harness for the deterministic lane.

A passing test suite proves nothing about whether the tests can FAIL. This
applies one deliberate source mutation at a time to a throwaway copy of the
tree and runs the lane's packages: a mutation the suite does not fail on
SURVIVED, and a survivor is a guard that cannot fail.

It is not decoration. Run against the S7 lane as first written it found
46 survivors in 88 mutations, including all three log-window bounds, every
attack dedupe key, and a `subjectKey` fallback that reintroduced the
component-id restart bug one layer above the differ built to avoid it.

    python3 scripts/mutate/mutate.py            # every mutation
    python3 scripts/mutate/mutate.py D11 R16    # just these

Exit status is 0 only when every mutation is either caught or a DECLARED
survivor. Two verdicts are neither caught nor survived and both fail the run:

  BADPATCH  the mutation's source text no longer appears exactly once, so
            nothing was tested. This is what a mutation list does when the
            code moves under it, and it MUST NOT read as a pass — a rotted
            harness reporting "all caught" is the exact failure this tool
            exists to find.
  NOBUILD   the mutant did not compile, so the suite's red says nothing
            about the suite.

Mutations are keyed on exact source text, so they rot by design. Fix or
delete a BADPATCH; do not ignore it.
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile

SRC = subprocess.run(["git", "rev-parse", "--show-toplevel"],
                     capture_output=True, text=True, check=True).stdout.strip()
PKGS = ["./internal/diff", "./internal/rules", "./internal/logbook",
        "./internal/replay", "./internal/x4data"]

# Survivors that are CORRECT, each with the reason it cannot be killed. Anything
# surviving that is not in here fails the run. Keeping the list explicit is the
# point: "3 survived" means nothing, "these 3 and only these" means something.
EXPECTED_SURVIVORS = {
    "W3": "the no-op control — it must survive, and that it does is what "
          "proves the harness can tell the verdicts apart at all",
    "W6": "provably equivalent: byCodeFirst's empty-code guard duplicates the "
          "indexing guard, which already refuses codeless events",
    "D17": "provably equivalent: `IntentionalTime <= 0` is subsumed by "
           "`<= prevT` for any non-negative clock",
}

# The files under mutation.
D = "internal/diff/diff.go"
W = "internal/diff/logwindow.go"
R = "internal/rules/rules.go"
LT = "internal/logbook/template.go"
LR = "internal/logbook/rules.go"
LN = "internal/logbook/normalize.go"
LG = "internal/logbook/language.go"
TX = "internal/x4data/text.go"

# (id, file, old, new, note)
MUTS = [
 # ---- the log window: the three bounds the review named ----
 ("D11", W, "lo, hi := prev.GameTimeS, next.GameTimeS\n\tfor i := range next.Logbook {\n\t\te := &next.Logbook[i]\n\t\tif e.Time <= lo || e.Time > hi {", "lo, hi := prev.GameTimeS, next.GameTimeS\n\t_ = lo\n\tfor i := range next.Logbook {\n\t\te := &next.Logbook[i]\n\t\tif e.Time > hi {", "drop the log window's LOWER bound"),
 ("D12", W, "if e.Time <= lo || e.Time > hi {", "if e.Time < lo || e.Time > hi {", "lower bound half-open -> closed"),
 ("D13", W, "if e.Time <= lo || e.Time > hi {", "if e.Time <= lo || e.Time >= hi {", "upper bound closed -> half-open"),
 ("W1",  W, "lo, hi := prev.GameTimeS, next.GameTimeS", "lo, hi := 0.0, next.GameTimeS", "window starts at time zero"),
 ("W2",  W, "\t\t\tbest = i\n\t\t}\n\t}\n\treturn w.events[best], true", "\t\t\tbest = i\n\t\t}\n\t}\n\tfor _, i := range idx[1:] {\n\t\tif w.events[i].time > w.events[best].time {\n\t\t\tbest = i\n\t\t}\n\t}\n\treturn w.events[best], true", "byCodeFirst returns the LATEST, not the earliest"),
 ("W3",  W, "\tidx := w.byCode[string(kind)+\"|\"+code]", "\tidx := w.byCode[string(kind)+\"|\"+code]\n\t_ = code", "no-op control (must SURVIVE)"),
 ("W4",  W, "if n := nameCount[key]; n != 1 {", "if n := nameCount[key]; n < 1 {", "byNameUnique's ambiguity guard collapsed"),
 ("W5",  W, "\tkey := strings.ToLower(name)", "\tkey := name", "byNameUnique stops case-folding"),
 ("W6",  W, "if code == \"\" {\n\t\treturn windowEvent{}, false\n\t}", "if false {\n\t\treturn windowEvent{}, false\n\t}", "byCodeFirst accepts an empty code"),
 ("W7",  W, "if ev.Code != \"\" {", "if true {", "index events with an empty code"),
 ("W8",  W, "\t\tw.Classified++", "", "the classification-rate health metric stops counting"),
 ("W9",  W, "\t\tw.Total++", "", "the window's Total stops counting"),
 ("W10", W, "func shipNameCounts(ships []x4save.Ship) map[string]int {\n\tm := map[string]int{}\n\tfor _, s := range ships {\n\t\tif s.Name != \"\" {\n\t\t\tm[strings.ToLower(s.Name)]++", "func shipNameCounts(ships []x4save.Ship) map[string]int {\n\tm := map[string]int{}\n\tfor _, s := range ships {\n\t\tif s.Name != \"\" {\n\t\t\tm[s.Name]++", "shipNameCounts stops case-folding"),
 ("W11", W, "for _, i := range idx[1:] {\n\t\tif w.events[i].time < w.events[best].time {\n\t\t\tbest = i\n\t\t}\n\t}\n\treturn w.events[best], 1, true", "for _, i := range idx[1:] {\n\t\tif w.events[i].time > w.events[best].time {\n\t\t\tbest = i\n\t\t}\n\t}\n\treturn w.events[best], 1, true", "byNameUnique returns the LATEST"),

 # ---- the differ ----
 ("D7",  D, "\t\treturn nowVal < wasVal", "\t\treturn nowVal <= wasVal", "hullDropped: an unchanged hull reads as a fall"),
 ("D24", D, "if now.Attack.IntentionalTime <= prevT {", "if now.Attack.IntentionalTime < prevT {", "attack clock: a frozen clock re-triggers forever"),
 ("D31", D, "if st.Name == \"\" || d.stationNames[strings.ToLower(st.Name)] != 1 {", "if st.Name == \"\" || d.stationNames[strings.ToLower(st.Name)] < 1 {", "accounts: station-name ambiguity guard collapsed"),
 ("D33", D, "return b.Code + \"|\" + b.Macro + \"|\" + b.Sector", "return b.Code", "BuildKey drops |macro|sector"),
 ("D35", D, "func StationKey(s x4save.Station) string {\n\tif s.Code != \"\" {\n\t\treturn s.Code\n\t}\n\treturn \"id:\" + s.ID\n}", "func StationKey(s x4save.Station) string {\n\treturn \"id:\" + s.ID\n}", "StationKey keys on the id, which a restart renumbers"),
 ("D1",  D, "func ShipKey(s x4save.Ship) string {\n\tif s.Code != \"\" {\n\t\treturn s.Code\n\t}\n\treturn \"id:\" + s.ID\n}", "func ShipKey(s x4save.Ship) string {\n\treturn \"id:\" + s.ID\n}", "ShipKey keys on the id (the restart bug)"),
 ("D2",  D, "\tif a.SpawnTime == 0 || b.SpawnTime == 0 {\n\t\treturn true\n\t}\n\treturn a.SpawnTime == b.SpawnTime", "\treturn true", "sameShip's anti-reuse guard removed"),
 ("D3",  D, "\treturn a.SpawnTime == b.SpawnTime", "\treturn a.SpawnTime != b.SpawnTime", "sameShip inverted"),
 ("D4",  D, "if prev.GameGUID != next.GameGUID {", "if false {", "a different playthrough is diffed anyway"),
 ("D5",  D, "if next.GameTimeS < prev.GameTimeS {", "if false {", "a reload is diffed anyway"),
 ("D6",  D, "if next.GameTimeS == prev.GameTimeS {\n\t\treturn Result{Refused: RefuseSameObservation}\n\t}", "", "the same observation is diffed against itself"),
 ("D8",  D, "if !prev.PlayerAssetsSeen || !next.PlayerAssetsSeen {", "if false {", "a snapshot with no asset walk is diffed"),
 ("D9",  D, "if !prev.GameTimeSeen || !next.GameTimeSeen {", "if false {", "an unread clock is treated as zero"),
 ("D10", D, "statsCovers := haveShipsOwned && shipsOwnedDelta < 0 && len(missing) <= int(-shipsOwnedDelta)", "statsCovers := haveShipsOwned && shipsOwnedDelta < 0", "the fleet stat corroborates every loss regardless of count"),
 ("D14", D, "\t\t\tif stillHere && now.HullState() != x4save.HullDestroyed {", "\t\t\tif stillHere && now.HullState() == x4save.HullDestroyed {", "the wreck test inverted"),
 ("D15", D, "case hasSource(ev, SrcLogbook):\n\t\t\tkind, disposition = KindShipLost, \"absent\"", "case false:\n\t\t\tkind, disposition = KindShipLost, \"absent\"", "a destroy sentence no longer promotes gone -> lost"),
 ("D16", D, "\t\tcase g.stillHere:\n\t\t\tkind, disposition = KindShipLost, \"wreck\"", "\t\tcase false:\n\t\t\tkind, disposition = KindShipLost, \"wreck\"", "a wreck no longer produces ShipLost"),
 ("D17", D, "if now.Attack == nil || now.Attack.IntentionalTime <= 0 {", "if now.Attack == nil {", "a zero attack clock counts as an attack"),
 ("D18", D, "hullFell := existed && hullDropped(was, now)", "hullFell := existed", "every attack claims a hull drop"),
 ("D19", D, "\t\tfired[now.Code] = true", "", "the log-only pass re-fires ships the tree already fired"),
 ("D20", D, "if was, had := prevDamage[StationKey(*st)]; had && st.ModuleHealth.Damaged > was {", "if was, had := prevDamage[StationKey(*st)]; had && st.ModuleHealth.Damaged >= was {", "station module damage: unchanged reads as rising"),
 ("D21", D, "if frac >= d.opts.AccountClearFraction {", "if frac > d.opts.AccountClearFraction {", "account hysteresis clear boundary"),
 ("D22", D, "float64(was)/float64(ev.Amount) < d.opts.AccountFireFraction {", "float64(was)/float64(ev.Amount) <= d.opts.AccountFireFraction {", "account hysteresis fire boundary"),
 ("D23", D, "if ev.Kind != logbook.EventAccountDropped || !ev.HaveAmount || ev.Amount <= 0 {", "if ev.Kind != logbook.EventAccountDropped || !ev.HaveAmount {", "a zero threshold divides"),
 ("D25", D, "if delta > -d.opts.MoneyDeltaFloor && delta < d.opts.MoneyDeltaFloor {", "if delta > -d.opts.MoneyDeltaFloor && delta <= d.opts.MoneyDeltaFloor {", "money floor boundary"),
 ("D26", D, "\tif a == 0 || b == 0 {\n\t\treturn false\n\t}\n\treturn (a > 0) == (b > 0)", "\treturn (a > 0) == (b > 0)", "a stat that did not move corroborates again"),
 ("D27", D, "if !ok || stalledFor < d.opts.StallMinSeconds {", "if !ok || stalledFor <= d.opts.StallMinSeconds {", "stall threshold boundary"),
 ("D28", D, "if was.Start != now.Start {\n\t\t\tcontinue // a different step; the previous one finished\n\t\t}", "", "a new build step counts as the same stall"),
 ("D29", D, "if !stillHere || !now.Stalled() || !was.Stalled() {", "if !stillHere || !now.Stalled() {", "one observation is enough for a stall"),
 ("D30", D, "if was.JobSeen && (!stillHere || !now.JobSeen) {", "if was.JobSeen {", "a running job reports as completed"),
 ("D32", D, "if t.Knownto != \"player\" {", "if t.Knownto == \"player\" {", "undiscovered hostiles are reported"),
 ("D34", D, "if hops < 0 || hops > d.opts.MaxHostileHops {", "if hops < 0 || hops >= d.opts.MaxHostileHops {", "hostile hop boundary"),
 ("D36", D, "\tif t.Code != \"\" {\n\t\treturn \"code:\" + t.Code\n\t}", "\tif false {\n\t\treturn \"code:\" + t.Code\n\t}", "threatKey ignores the code"),
 ("D37", D, "\tcase SrcLogbook:\n\t\treturn GroupLog", "\tcase SrcLogbook:\n\t\treturn GroupTree", "the logbook stops being an independent group"),
 ("D38", D, "\treturn len(groups) >= 2", "\treturn len(groups) >= 1", "one signal counts as corroboration"),
 ("D39", D, "\tif !prev.StatsSeen || !next.StatsSeen {\n\t\treturn 0, false\n\t}", "", "a missing stats block reads as zero"),
 ("D40", D, "\t\tif d.isIdle(was) {", "\t\tif !d.isIdle(was) {", "newly-idle inverted"),
 ("D41", D, "if now.LastOrderError != \"\" && was.LastOrderError == now.LastOrderError {", "if now.LastOrderError != \"\" {", "a DIFFERENT failed order escalates"),
 ("D42", D, "\tif s.Order == \"\" {\n\t\treturn true\n\t}", "\tif s.Order == \"\" {\n\t\treturn false\n\t}", "no order stops meaning idle"),
 ("D43", D, "\t\tID: s.ID, Code: s.Code, Name: s.Name, Class: s.Class, Size: s.Size,", "\t\tID: s.ID, Code: \"\", Name: s.Name, Class: s.Class, Size: s.Size,", "a ship subject loses its code"),
 ("D44", D, "\tcase !nowKnown:\n\t\treturn false // at maximum\n\tcase !wasKnown:\n\t\treturn true // was at maximum, now carries a value", "\tcase !nowKnown:\n\t\treturn true\n\tcase !wasKnown:\n\t\treturn false", "hullDropped's absent-means-maximum rule inverted"),

 # ---- the rules layer ----
 ("R9",  R, "\t\t\tMatch:    func(c diff.Change) bool { return c.Subject.Class == \"station\" },\n\t\t\tSeverity: Amber, Confidence: Corroborated,\n\t\t\tDedupe:         func(c diff.Change) string { return \"attack|\" + subjectKey(c) },", "\t\t\tMatch:    func(c diff.Change) bool { return c.Subject.Class == \"station\" },\n\t\t\tSeverity: Amber, Confidence: Corroborated,\n\t\t\tDedupe:         func(c diff.Change) string { return \"attack\" },", "station_under_attack dedupes every station into one row"),
 ("R10", R, "\t\t\tSeverity: Red, Confidence: Corroborated,\n\t\t\tDedupe:         func(c diff.Change) string { return \"attack|\" + subjectKey(c) },\n\t\t\tDefaultEnabled: true,\n\t\t\tTrigger:        \"an L or XL ship's attack clock advanced AND its hull value fell\",", "\t\t\tSeverity: Red, Confidence: Corroborated,\n\t\t\tDedupe:         func(c diff.Change) string { return \"attack\" },\n\t\t\tDefaultEnabled: true,\n\t\t\tTrigger:        \"an L or XL ship's attack clock advanced AND its hull value fell\",", "capital_taking_damage dedupes six dying capitals into one row"),
 ("R11", R, "\t\t\tDedupe:         func(c diff.Change) string { return \"attack|\" + subjectKey(c) },\n\t\t\tDefaultEnabled: true,\n\t\t\tTrigger:        \"an L or XL ship's intentionalattacktime advanced\",", "\t\t\tDedupe:         func(c diff.Change) string { return \"attack\" },\n\t\t\tDefaultEnabled: true,\n\t\t\tTrigger:        \"an L or XL ship's intentionalattacktime advanced\",", "capital_under_attack dedupes every capital into one row"),
 ("R12", R, "\t\t\tSeverity: Amber, Confidence: Corroborated,\n\t\t\tDedupe:         func(c diff.Change) string { return \"attack|\" + subjectKey(c) },\n\t\t\tDefaultEnabled: true,\n\t\t\tTrigger:        \"an S or M ship's intentionalattacktime advanced\",", "\t\t\tSeverity: Amber, Confidence: Corroborated,\n\t\t\tDedupe:         func(c diff.Change) string { return \"attack\" },\n\t\t\tDefaultEnabled: true,\n\t\t\tTrigger:        \"an S or M ship's intentionalattacktime advanced\",", "ship_under_attack dedupes every ship into one row"),
 ("R16", R, "func subjectKey(c diff.Change) string {\n\tif c.Subject.Code != \"\" {\n\t\treturn c.Subject.Code\n\t}", "func subjectKey(c diff.Change) string {\n\tif c.Subject.ID != \"\" {\n\t\treturn c.Subject.ID\n\t}", "subjectKey prefers the id — the restart bug, one layer up"),
 ("R21", R, "\treturn unstableKeyPrefix + c.Subject.ID\n}", "\treturn c.Subject.ID\n}", "a key built on a renumbered id stops declaring itself"),
 ("R1",  R, "\t\t\tDedupe:         func(c diff.Change) string { return \"ship_lost|\" + subjectKey(c) },", "\t\t\tDedupe:         func(c diff.Change) string { return \"ship_lost\" },", "every ship loss dedupes to one row"),
 ("R2",  R, "\t\t\tDedupe:         func(c diff.Change) string { return \"ship_gone|\" + subjectKey(c) },", "\t\t\tDedupe:         func(c diff.Change) string { return \"ship_gone\" },", "every vanished ship dedupes to one row"),
 ("R3",  R, "\t\t\t\treturn fmt.Sprintf(\"build_stalled|%s|%d\", subjectKey(c), c.Detail.Step)", "\t\t\t\treturn fmt.Sprintf(\"build_stalled|%s\", subjectKey(c))", "build_stalled drops the step from its key"),
 ("R4",  R, "\t\t\tDedupe:              func(c diff.Change) string { return \"account|\" + subjectKey(c) },", "\t\t\tDedupe:              func(c diff.Change) string { return \"account\" },", "every station's account alarm dedupes to one row"),
 ("R5",  R, "\t\t\t\treturn fmt.Sprintf(\"hostile|%s|%s|%d\", c.Subject.Sector, c.Subject.Class, int(c.GameTime)/3600)", "\t\t\t\treturn \"hostile\"", "every sighting dedupes to one row"),
 ("R6",  R, "\t\t\tDedupe:         func(c diff.Change) string { return \"build_done|\" + subjectKey(c) },", "\t\t\tDedupe:         func(c diff.Change) string { return \"build_done\" },", "every completed build dedupes to one row"),
 ("R7",  R, "\t\t\t\treturn fmt.Sprintf(\"money|%d\", int(c.GameTime)/3600)", "\t\t\t\treturn \"money\"", "every money move dedupes to one row"),
 ("R8",  R, "\t\t\tDedupe:         func(c diff.Change) string { return \"idle|\" + subjectKey(c) },", "\t\t\tDedupe:         func(c diff.Change) string { return \"idle\" },", "every idle ship dedupes to one row"),
 ("R13", R, "\tcase \"L\", \"XL\":\n\t\treturn true", "\tcase \"XL\":\n\t\treturn true", "isBig stops counting L as capital"),
 ("R14", R, "\t\t\t\treturn isBig(c.Subject.Size, c.Subject.Class) && c.Detail.HullFell", "\t\t\t\treturn isBig(c.Subject.Size, c.Subject.Class)", "capital_taking_damage drops the hull-fell clause"),
 ("R15", R, "\t\t\tMatch:    func(c diff.Change) bool { return c.Subject.Class == \"station\" },", "\t\t\tMatch:    func(c diff.Change) bool { return c.Subject.Class != \"station\" },", "station_under_attack matches everything but stations"),
 ("R17", R, "} else if r.Severity == Red && !c.Corroborated() && !r.SingleSource {", "} else if r.Severity == Red && false {", "the corroboration policy stops downgrading"),
 ("R18", R, "\t\t\t\tbreak // matched, but switched off: no later rule may claim it", "\t\t\t\tcontinue", "a switched-off rule hands the change to the next one"),
 ("R19", R, "\tif v, ok := c.On[id]; ok {\n\t\treturn v\n\t}", "", "the kill switch stops being read"),
 ("R20", R, "\t\t\tSeverity: Red, Confidence: Falsifiable,\n\t\t\tSingleSource: true,", "\t\t\tSeverity: Red, Confidence: Falsifiable,\n\t\t\tSingleSource: false,", "ship_destroyed loses its single-source whitelist"),

 # ---- the logbook ----
 ("L8",  LR, "\tloc := codeRe.FindStringIndex(subject)\n\tif loc == nil {\n\t\treturn \"\", subject\n\t}", "\tlocs := codeRe.FindAllStringIndex(subject, -1)\n\tif locs == nil {\n\t\treturn \"\", subject\n\t}\n\tloc := locs[len(locs)-1]", "splitCode takes the LAST code — the attacker's — not the first"),
 ("L1",  LR, "\t\t\tif v, ok := m.Span(rr.Subject); ok {", "\t\t\tif v, ok := m.Value(rr.Subject); ok {", "Classify uses Value, not Span, across a fused pair"),
 ("L2",  LR, "\tif lo > 0 && hi < len(subject) && subject[lo-1] == '(' && subject[hi] == ')' {\n\t\tlo, hi = lo-1, hi+1\n\t}", "", "splitCode leaves the empty brackets behind"),
 ("L3",  LT, "const edgeSlotClass = `[^.:;!?\\n\\r]`", "const edgeSlotClass = `[^\\n\\r]`", "the edge-slot class stops excluding sentence structure"),
 ("L4",  LT, "\tif places[0][0] == 0 {\n\t\tfor i := 0; i < len(places); i++ {\n\t\t\tedges[i] = true\n\t\t\tif i+1 >= len(places) || !fusedTo(i, i+1) {\n\t\t\t\tbreak\n\t\t\t}\n\t\t}\n\t}", "\tif places[0][0] == 0 {\n\t\tedges[0] = true\n\t}", "only the FIRST slot of a leading run is constrained"),
 ("L5",  LT, "\tpat.WriteString(`\\A`)", "\tpat.WriteString(``)", "the pattern stops being anchored at the front"),
 ("L6",  LT, "\tpat.WriteString(`\\z`)", "\tpat.WriteString(``)", "the pattern stops being anchored at the end"),
 ("L7",  LT, "const minLiterals = 6", "const minLiterals = 0", "pure-wildcard templates enter the catalog"),
 ("L9",  LT, "\t\tcase m.Literals > best.Literals,", "\t\tcase m.Literals < best.Literals,", "Match prefers the LEAST specific template"),
 ("L10", LT, "\t\tbest.Ambiguous = true", "\t\tbest.Ambiguous = false", "an ambiguous match stops saying so"),
 ("L11", LN, "\ts = codeRe.ReplaceAllString(s, PlaceCode)\n\ts = numRe.ReplaceAllString(s, PlaceNum)", "\ts = numRe.ReplaceAllString(s, PlaceNum)\n\ts = codeRe.ReplaceAllString(s, PlaceCode)", "Normalize masks numbers before codes"),
 ("L12", LN, "\t\tif len(m.entries[i].text) != len(m.entries[j].text) {\n\t\t\treturn len(m.entries[i].text) > len(m.entries[j].text)\n\t\t}", "\t\tif len(m.entries[i].text) != len(m.entries[j].text) {\n\t\t\treturn len(m.entries[i].text) < len(m.entries[j].text)\n\t\t}", "the masker consumes the SHORTEST name first"),
 ("L13", LN, "\treturn hex.EncodeToString(sum[:])[:24]", "\treturn hex.EncodeToString(sum[:])[:2]", "the signature shrinks to one byte"),
 # ---- F10: duplicate registration codes ----
 ("P1", D, "\t\t\tif seen[s.Code] {\n\t\t\t\tout[s.Code] = true\n\t\t\t}", "\t\t\tif false {\n\t\t\t\tout[s.Code] = true\n\t\t\t}", "duplicate codes stop being detected at all"),
 ("P2", D, "\tfor _, ships := range [][]x4save.Ship{prev, next} {", "\tfor _, ships := range [][]x4save.Ship{prev} {", "the duplicate set is computed per-snapshot, not over the pair"),
 ("P3", D, "\treturn k + \"@\" + strconv.FormatFloat(s.SpawnTime, 'f', -1, 64)", "\treturn k + \"@\" + s.ID + strconv.FormatFloat(0, 'f', -1, 64)[:0]", "duplicates are separated by the RENUMBERED component id"),
 ("P4", D, "\t\tunattributed := len(wasList) > 1 || len(nowList) > 1", "\t\tunattributed := false", "an unattributable loss stops saying so"),
 ("P5", D, "\t\tfor i, was := range wasList {", "\t\tfor i, was := range wasList[:1] {", "only the first ship under a key can be lost"),
 ("P6", D, "\tv := ix[key]\n\tif len(v) != 1 {", "\tv := ix[key]\n\tif len(v) < 1 {", "lookupShip hands back one of several candidates"),
 ("P7", D, "\t\t\tsort.Slice(v, func(i, j int) bool { return v[i].ID < v[j].ID })", "", "the duplicate pairing stops being deterministic"),

 # ---- F1: language detection ----
 ("G1", LG, "\t\tif float64(n) >= decisiveFraction*float64(len(sample)) {\n\t\t\tbreak\n\t\t}", "\t\tbreak", "the search stops after the first candidate whatever it scored"),
 ("G2", LG, "\tcase best <= 0:\n\t\ta.State = StateUnavailable", "\tcase best < 0:\n\t\ta.State = StateUnavailable", "a catalog that reads nothing reports itself ok"),
 ("G3", LG, "\tcase len(sample) == 0:\n\t\ta.State = StateUnverified", "\tcase len(sample) == 0:\n\t\ta.State = StateOK", "an unchecked choice reports itself verified"),
 ("G4", LG, "\tif haveHint {\n\t\tadd(hint)\n\t}", "", "the configured language stops being tried first"),
 ("G5", LG, "\t\tif n > best {", "\t\tif n >= best {", "the last candidate wins ties instead of the first"),
 ("G6", LG, "\tstride := float64(len(nonEmpty)) / float64(maxSample)", "\tstride := 1.0", "the sample becomes the log's first 500 rows"),
 ("G7", LG, "func (a Availability) OK() bool { return a.State == StateOK }", "func (a Availability) OK() bool { return a.State != StateUnavailable }", "an unverified catalog reports OK"),
 ("G8", LG, "\tif len(in.Languages) == 0 || in.Load == nil {", "\tif in.Load == nil {", "an install with no localisations is not reported"),
 ("G9", D, "func (h LogHealth) Readable() bool { return h.Entries == 0 || h.Classified > 0 }", "func (h LogHealth) Readable() bool { return true }", "the lane always calls itself readable"),
 ("G10", D, "\t\tLog:     LogHealth{Entries: d.log.Total, Classified: d.log.Classified},", "\t\tLog:     LogHealth{Entries: d.log.Total, Classified: d.log.Total},", "the classification rate reports 100% always"),

 # ---- x4data language plumbing ----
 ("T1", TX, "\tupper := \"t/0001-\" + strings.ToUpper(lang[:1]) + lang[1:] + \".xml\"", "\tupper := \"t/0001-\" + lang + \".xml\"", "the uppercase t-file spelling stops being read"),
 ("T2", TX, "\treturn []string{NeutralTextFile, upper, lower}", "\treturn []string{upper, lower}", "the language-neutral t-file stops being read"),
 ("T3", TX, "\treturn []string{NeutralTextFile, upper, lower}", "\treturn []string{upper, lower, NeutralTextFile}", "the neutral file wins over the localised one"),
 ("T4", TX, "\tif lang == \"\" {\n\t\tlang = DefaultTextLanguage\n\t}", "", "an empty language builds t/0001-.xml"),
 ("T5", TX, "var languageFileRe = regexp.MustCompile(`^t/0001-[lL]([0-9]{3})\\.xml$`)", "var languageFileRe = regexp.MustCompile(`t/0001-[lL]([0-9]{3})`)", "the language scan stops being anchored"),
 ("T6", TX, "\tif v := strings.TrimSpace(os.Getenv(\"X4MCP_GAME_LANG\")); v != \"\" {", "\tif v := \"\"; v != \"\" {", "the X4MCP_GAME_LANG override is ignored"),
 ("T7", TX, "\t\tif id, ok := steamLanguageIDs[strings.ToLower(m[1])]; ok {", "\t\tif id := m[1]; true {", "an unmappable Steam language is passed through as an id"),
]


def worktree():
    """A throwaway copy of the TRACKED working tree.

    Tracked, not HEAD: the point is to mutate what is about to be committed,
    including edits not yet committed. Never mutates SRC itself — a crash
    mid-run must not leave the real tree holding a deliberate bug.
    """
    out = subprocess.run(["git", "-C", SRC, "ls-files", "-z"],
                         capture_output=True, check=True).stdout
    work = tempfile.mkdtemp(prefix="x4mcp-mutate-")
    for rel in out.decode().split("\0"):
        if not rel:
            continue
        dst = os.path.join(work, rel)
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        try:
            shutil.copy2(os.path.join(SRC, rel), dst)
        except FileNotFoundError:
            pass  # deleted-but-tracked; the build will say so if it matters
    return work


def run():
    only = sys.argv[1:] or None
    work = worktree()
    results = []
    try:
        for mid, path, old, new, note in MUTS:
            if only and mid not in only:
                continue
            pristine = os.path.join(SRC, path)
            full = os.path.join(work, path)
            src = open(pristine).read()
            if src.count(old) != 1:
                results.append((mid, "BADPATCH", note))
                print(f"{mid:5s} BADPATCH ({src.count(old)} occurrences) {note}", flush=True)
                continue
            open(full, "w").write(src.replace(old, new, 1))
            p = subprocess.run(["go", "test", "-count=1"] + PKGS, cwd=work,
                               capture_output=True, text=True)
            open(full, "w").write(src)
            broke = ("[build failed]" in p.stdout or "build failed" in p.stdout
                     or "undefined:" in p.stdout or "cannot use" in p.stderr)
            if broke:
                verdict = "NOBUILD"
            elif p.returncode == 0:
                verdict = "SURVIVED"
            else:
                verdict = "caught"
            results.append((mid, verdict, note))
            print(f"{mid:5s} {verdict:9s} {note}", flush=True)
    finally:
        shutil.rmtree(work, ignore_errors=True)

    caught = [r for r in results if r[1] == "caught"]
    survived = [r for r in results if r[1] == "SURVIVED"]
    problem = [r for r in results if r[1] in ("BADPATCH", "NOBUILD")]
    unexpected = [r for r in survived if r[0] not in EXPECTED_SURVIVORS]
    declared = [r for r in survived if r[0] in EXPECTED_SURVIVORS]

    print(f"\ncaught {len(caught)} · declared survivors {len(declared)} · "
          f"UNEXPECTED survivors {len(unexpected)} · problems {len(problem)}")
    for mid, _, note in unexpected:
        print(f"  SURVIVED {mid}: {note}")
        print("           a guard that cannot fail — write the test that kills it")
    for mid, verdict, note in problem:
        print(f"  {verdict} {mid}: {note}")

    json.dump([{"id": m, "verdict": v, "note": n} for m, v, n in results],
              open(os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                "last-run.json"), "w"), indent=1)
    return 1 if unexpected or problem else 0


if __name__ == "__main__":
    sys.exit(run())
