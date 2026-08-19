package x4save

import (
	"os"
	"path/filepath"
	"testing"
)

// The numbers these produce are written up in docs/parse-baseline.md. S6 adds
// whole sections to the parse (logbook, stats, missions, inventory); its gate is
// "ParseMS within +10% and RSS within +5 MB of the S2 baseline", and this is
// where that baseline comes from.
//
//	go test ./internal/x4save -run '^$' -bench . -benchtime 5x
//
// peak_rss_mb is the process high-water mark (VmHWM), not the Go heap: what
// matters is whether a parse can run while X4 has the machine.

const distilledFixture = "distilled-quicksave.xml.gz"

// BenchmarkParseDistilled runs on the committed ~1 MB distilled fixture, so it
// works in CI and on any clone.
func BenchmarkParseDistilled(b *testing.B) {
	path := filepath.Join(realDir, distilledFixture)
	if _, err := os.Stat(path); err != nil {
		b.Skipf("no distilled fixture at %s", path)
	}
	benchParse(b, path)
}

// BenchmarkParseRealSave runs on a full ~100 MB save. It is the only measurement
// that says anything about the real cost, and it needs a save this machine has.
func BenchmarkParseRealSave(b *testing.B) {
	path := os.Getenv("X4MCP_REAL_SAVE")
	if path == "" {
		b.Skip("set X4MCP_REAL_SAVE=<path to a real *.xml.gz> to benchmark a full save")
	}
	benchParse(b, path)
}

func benchParse(b *testing.B, path string) {
	fi, err := os.Stat(path)
	if err != nil {
		b.Fatal(err)
	}
	resetPeakRSS()
	b.SetBytes(fi.Size())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snap, err := ParseFile(path)
		if err != nil {
			b.Fatalf("parse %s: %v", path, err)
		}
		if len(snap.Sectors) == 0 && len(snap.Ships) == 0 {
			b.Fatalf("parse %s produced an empty snapshot; the benchmark would be measuring nothing", path)
		}
	}
	b.StopTimer()
	b.ReportMetric(rssMB(peakRSSKB()), "peak_rss_mb")
}

// BenchmarkCacheRoundTrip measures what the cache costs to write and read back,
// on the distilled fixture's snapshot (or a real one via X4MCP_REAL_SAVE).
//
// It exists because the cache stopped being a plain gob of the Snapshot in
// schema 28: gob cannot round-trip a pointer to zero, which is what several of
// the snapshot's presence flags ARE (snapshotEnvelope explains why). JSON can,
// and this is the price of that — measured, not assumed. The number to keep an
// eye on is read: a cache hit is the whole reason the cache exists, and the
// thing it is competing against is an ~12 s parse.
func BenchmarkCacheRoundTrip(b *testing.B) {
	path := os.Getenv("X4MCP_REAL_SAVE")
	if path == "" {
		path = filepath.Join(realDir, distilledFixture)
	}
	if _, err := os.Stat(path); err != nil {
		b.Skipf("no save at %s", path)
	}
	snap, err := ParseFile(path)
	if err != nil {
		b.Fatal(err)
	}
	dir := b.TempDir()
	file := filepath.Join(dir, "entry.gob")

	b.Run("write", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := writeCache(file, path, snap); err != nil {
				b.Fatal(err)
			}
		}
		if fi, err := os.Stat(file); err == nil {
			b.ReportMetric(float64(fi.Size())/(1<<20), "entry_mb")
		}
	})
	if err := writeCache(file, path, snap); err != nil {
		b.Fatal(err)
	}
	b.Run("read", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := readCache(file); err != nil {
				b.Fatal(err)
			}
		}
	})
}
