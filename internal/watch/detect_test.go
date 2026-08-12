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
