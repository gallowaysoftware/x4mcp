package watch

import (
	"testing"
	"time"
)

// The settle gate is the one piece of this package that has to be right in
// every sequence, not just the happy one: X4 spends 20–60 s writing a late-game
// save, so "the file exists" and "the file is finished" are minutes apart, and
// parsing the wrong one of those yields a snapshot stitched from two game
// states that looks entirely plausible.
func TestDetectorSettleGate(t *testing.T) {
	t0 := time.Date(2026, 8, 10, 20, 0, 0, 0, time.UTC)
	qs := func(size int64, mod time.Duration) candidate {
		return candidate{path: "/saves/quicksave.xml.gz", size: size, modTime: t0.Add(mod)}
	}
	auto := func(size int64, mod time.Duration) candidate {
		return candidate{path: "/saves/autosave_01.xml.gz", size: size, modTime: t0.Add(mod)}
	}

	cases := []struct {
		name string
		seq  []candidate
		want []bool // parse-now, per sighting
	}{
		{
			name: "one sighting is never enough",
			seq:  []candidate{qs(100, 0)},
			want: []bool{false},
		},
		{
			name: "two identical sightings settle",
			seq:  []candidate{qs(100, 0), qs(100, 0)},
			want: []bool{false, true},
		},
		{
			name: "a file still being written never settles",
			seq:  []candidate{qs(10, 0), qs(200, time.Second), qs(9000, 2*time.Second), qs(90000, 3*time.Second)},
			want: []bool{false, false, false, false},
		},
		{
			name: "it settles once the writing stops",
			seq:  []candidate{qs(10, 0), qs(9000, 2*time.Second), qs(9000, 2*time.Second), qs(9000, 2*time.Second)},
			want: []bool{false, false, true, false},
		},
		{
			name: "already dispatched: no re-parse while it sits there",
			seq:  []candidate{qs(100, 0), qs(100, 0), qs(100, 0), qs(100, 0)},
			want: []bool{false, true, false, false},
		},
		{
			name: "the same save rewritten settles again",
			seq:  []candidate{qs(100, 0), qs(100, 0), qs(140, time.Minute), qs(140, time.Minute)},
			want: []bool{false, true, false, true},
		},
		{
			name: "rotation: quicksave then a newer autosave",
			seq:  []candidate{qs(100, 0), qs(100, 0), auto(80, time.Minute), auto(80, time.Minute)},
			want: []bool{false, true, false, true},
		},
		{
			name: "rotation mid-settle restarts the gate",
			seq:  []candidate{qs(100, 0), auto(80, time.Minute), auto(80, time.Minute)},
			want: []bool{false, false, true},
		},
		{
			name: "no saves at all",
			seq:  []candidate{{}, {}},
			want: []bool{false, false},
		},
		{
			name: "a save appearing after an empty dir still needs two sightings",
			seq:  []candidate{{}, qs(100, 0), qs(100, 0)},
			want: []bool{false, false, true},
		},
		{
			name: "the dir emptying forgets the half-settled candidate",
			seq:  []candidate{qs(100, 0), {}, qs(100, 0), qs(100, 0)},
			want: []bool{false, false, false, true},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newDetector(DefaultSettleTicks)
			for i, cand := range c.seq {
				if got := d.observe(cand, sourcePoll); got != c.want[i] {
					t.Errorf("sighting %d (%s size=%d): parse=%v, want %v",
						i, cand.path, cand.size, got, c.want[i])
				}
			}
		})
	}
}

// Attribution has to survive the settle gate: whatever saw the save FIRST gets
// the credit, even when the sighting that finally settles it came from the
// other one. A kick that lands on a save the ticker had already spotted is the
// ticker's find, not the button's.
func TestDetectorAttributesFirstSighting(t *testing.T) {
	t0 := time.Date(2026, 8, 10, 20, 0, 0, 0, time.UTC)
	c := candidate{path: "/saves/quicksave.xml.gz", size: 100, modTime: t0}

	cases := []struct {
		name        string
		first, then source
		want        source
	}{
		{name: "poll first", first: sourcePoll, then: sourceManual, want: sourcePoll},
		{name: "manual kick first", first: sourceManual, then: sourcePoll, want: sourceManual},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDetector(DefaultSettleTicks)
			d.observe(c, tc.first)
			if !d.observe(c, tc.then) {
				t.Fatal("second identical sighting should settle")
			}
			if d.firstSource != tc.want {
				t.Errorf("firstSource = %s, want %s", d.firstSource, tc.want)
			}
		})
	}
}

// Which save the gate should be WATCHING is a separate decision from whether it
// has settled, and it used to be "the newest mtime" with no alternative. That
// answer cannot see a save restored from a backup: cp -p, rsync -a and this
// repo's own archiver all preserve the original timestamp, so the restored file
// lands older than the one already there and is never looked at again.
func TestDetectorChoosesWhatChanged(t *testing.T) {
	t0 := time.Date(2026, 8, 10, 20, 0, 0, 0, time.UTC)
	c := func(name string, size int64, mod time.Duration) candidate {
		return candidate{path: "/saves/" + name, size: size, modTime: t0.Add(mod)}
	}
	// Listings arrive newest first (x4save.ListSaves sorts them).
	quick := c("quicksave.xml.gz", 100, 0)
	older := c("save_007.xml.gz", 90, -2*time.Hour)
	restored := c("restored.xml.gz", 80, -3*time.Hour)

	cases := []struct {
		name     string
		listings [][]candidate
		want     []candidate
	}{
		{
			name:     "start-up has observed nothing, so the newest is the best guess",
			listings: [][]candidate{{quick, older}},
			want:     []candidate{quick},
		},
		{
			name:     "an empty directory chooses nothing",
			listings: [][]candidate{{}},
			want:     []candidate{{}},
		},
		{
			name:     "a restored save is chosen although it is the oldest file there",
			listings: [][]candidate{{quick, older}, {quick, older, restored}},
			want:     []candidate{quick, restored},
		},
		{
			name: "and it is not abandoned on the next quiet tick",
			listings: [][]candidate{
				{quick, older}, {quick, older, restored}, {quick, older, restored}, {quick, older, restored},
			},
			want: []candidate{quick, restored, restored, restored},
		},
		{
			name:     "a file that grew is chosen over an untouched newer one",
			listings: [][]candidate{{quick, older}, {quick, c("save_007.xml.gz", 9000, -2*time.Hour)}},
			want:     []candidate{quick, c("save_007.xml.gz", 9000, -2*time.Hour)},
		},
		{
			name:     "the watched file being deleted falls back to the newest left",
			listings: [][]candidate{{restored}, {quick, older}},
			want:     []candidate{restored, quick},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDetector(2)
			for i, listing := range tc.listings {
				got := d.choose(listing)
				if !got.same(tc.want[i]) {
					t.Fatalf("listing %d: chose %q (%d bytes), want %q (%d bytes)",
						i, got.path, got.size, tc.want[i].path, tc.want[i].size)
				}
				// The gate sees it too, so `cur` tracks the real sequence.
				d.observe(got, sourcePoll)
			}
		})
	}
}

// Two saves restored in ONE tick, which is what restoring a backup actually
// looks like: `cp -p backup/*.xml.gz saves/`, an unpacked tarball, an rsync of a
// directory. Both files change between two listings, only one can be chosen
// first — and the one that lost the tie-break used to be recorded into `d.seen`
// in the very same pass. "Changed" is judged against that record, so from that
// moment it had never changed and never would: not on the next poll, not on the
// refresh button, not on refresh_save. Permanently invisible.
//
// Both must be detected, newest first, each getting the settle gate to itself.
func TestDetectorSeesEverySaveRestoredInOneTick(t *testing.T) {
	t0 := time.Date(2026, 8, 10, 20, 0, 0, 0, time.UTC)
	c := func(name string, size int64, mod time.Duration) candidate {
		return candidate{path: "/saves/" + name, size: size, modTime: t0.Add(mod)}
	}
	// Newest first, as x4save.ListSaves returns them. The two restored files
	// keep their original timestamps, so they land OLDER than what is there.
	live := c("quicksave.xml.gz", 100, 0)
	newer := c("save_002.xml.gz", 200, -2*time.Hour)
	older := c("save_001.xml.gz", 300, -3*time.Hour)

	cases := []struct {
		name     string
		restored []candidate
		want     []candidate // dispatched, in order
	}{
		{
			// The control, which passed all along: one file, one settle.
			name:     "one restore",
			restored: []candidate{newer},
			want:     []candidate{newer},
		},
		{
			name:     "two restores in one tick",
			restored: []candidate{newer, older},
			want:     []candidate{newer, older},
		},
		{
			name:     "three, in newest-first order",
			restored: []candidate{newer, older, c("save_000.xml.gz", 400, -4*time.Hour)},
			want:     []candidate{newer, older, c("save_000.xml.gz", 400, -4*time.Hour)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDetector(DefaultSettleTicks)
			// Start-up: one save on disk, seen and settled, nothing outstanding.
			for range DefaultSettleTicks {
				d.observe(d.choose([]candidate{live}), sourcePoll)
			}

			listing := append([]candidate{live}, tc.restored...)
			var dispatched []candidate
			// Ten quiet ticks after the restore. Nothing else changes on disk —
			// exactly the case where "what is newest?" has no answer left and
			// "what changed?" has to keep the ones it has not dealt with.
			for range 10 {
				cand := d.choose(listing)
				if d.observe(cand, sourcePoll) {
					dispatched = append(dispatched, cand)
				}
			}

			if len(dispatched) != len(tc.want) {
				t.Fatalf("dispatched %s, want %s", paths(dispatched), paths(tc.want))
			}
			for i, want := range tc.want {
				if !dispatched[i].same(want) {
					t.Errorf("dispatch %d = %q, want %q", i, dispatched[i].path, want.path)
				}
			}
		})
	}
}

// And the other half of the same rule: a file the gate has already dealt with
// must not come back round. Without it the queue above never drains and every
// save on disk is re-parsed forever.
func TestDetectorDoesNotReDispatchWhatItHasSeen(t *testing.T) {
	t0 := time.Date(2026, 8, 10, 20, 0, 0, 0, time.UTC)
	quick := candidate{path: "/saves/quicksave.xml.gz", size: 100, modTime: t0}
	restored := candidate{path: "/saves/save_001.xml.gz", size: 300, modTime: t0.Add(-3 * time.Hour)}

	d := newDetector(DefaultSettleTicks)
	dispatches := 0
	listing := []candidate{quick}
	// Fifty ticks is a hundred seconds of a quiet save directory, which is the
	// ordinary state of one. Two dispatches, and then nothing at all.
	for i := range 50 {
		if i == 4 {
			listing = []candidate{quick, restored} // the restore lands
		}
		if d.observe(d.choose(listing), sourcePoll) {
			dispatches++
		}
	}
	if dispatches != 2 {
		t.Errorf("dispatched %d times over 50 quiet ticks, want exactly 2 (the save that was there, and the one restored)", dispatches)
	}
}

func paths(cs []candidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.path)
	}
	return out
}
