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
