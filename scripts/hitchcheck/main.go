// Command hitchcheck measures what a parse costs the game beside it.
//
// This is the "parse-while-gaming hitch check" the implementation plan gates S3
// and S6 on, made into something that can be RUN rather than felt. The claim
// under test is PRD §11's second-worst risk — "x4cue degrades the game it
// serves" — and the only honest form of it is a number: how much of a busy
// core does the game still get while a save is being parsed on it?
//
// Two roles, one binary:
//
//	go build -o /tmp/hitchcheck ./scripts/hitchcheck
//
//	# the "game": one thread that never stops, logging how much it got done
//	taskset -c 12 /tmp/hitchcheck -game > /tmp/proxy.log &
//
//	# the parse, through the real watcher pipeline, on the same core
//	X4MCP_PARSE_NICE=off taskset -c 12 /tmp/hitchcheck -save fixture.xml.gz -proxy /tmp/proxy.log
//	taskset -c 12 /tmp/hitchcheck -save fixture.xml.gz -proxy /tmp/proxy.log
//
// The game proxy is a separate PROCESS on purpose. Two threads inside one Go
// process are arbitrated by the Go scheduler, not by the kernel — and pinning
// that process to one core sets GOMAXPROCS=1, at which point the runtime
// round-robins them 50/50 and the nice value never gets a say. Measured that
// way, nicing appears to do nothing at all. The game is a separate process in
// real life too.
//
// Numbers from a run of this live in docs/parse-baseline.md §6.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pequalsnp/x4mcp/internal/watch"
	"github.com/pequalsnp/x4mcp/internal/wire"
)

func main() {
	game := flag.Bool("game", false, "be the game: spin and log progress to stdout")
	threads := flag.Int("threads", 1, "with -game, how many threads to spin (a whole busy machine, not just one core)")
	save := flag.String("save", "", "savegame to parse (the committed distilled fixture is a fine one)")
	proxy := flag.String("proxy", "", "the -game run's log, to correlate the parses against")
	rounds := flag.Int("rounds", 8, "parses to measure; the first is discarded as a warm-up")
	flag.Parse()

	if *game {
		beTheGame(*threads)
		return
	}
	if *save == "" {
		fmt.Fprintln(os.Stderr, "hitchcheck: -save is required (or -game)")
		os.Exit(2)
	}
	if err := measure(*save, *proxy, *rounds); err != nil {
		fmt.Fprintln(os.Stderr, "hitchcheck:", err)
		os.Exit(1)
	}
}

// beTheGame is a stand-in for X4: threads that always have work to do,
// reporting how much of it they have finished every 100 ms. The throughput of
// those loops while a parse runs, against their throughput with the machine to
// themselves, is the hitch.
//
// One process however many threads: the log is one monotonic series, and two
// -game processes writing to the same file would produce a counter that goes
// backwards.
func beTheGame(threads int) {
	if threads < 1 {
		threads = 1
	}
	counters := make([]*atomic.Uint64, threads)
	for i := range counters {
		c := &atomic.Uint64{}
		counters[i] = c
		go func() {
			runtime.LockOSThread()
			var local, x uint64
			for {
				for range 100_000 {
					x = x*6364136223846793005 + 1442695040888963407
					local++
				}
				c.Store(local + x%1) // x is read, so nothing folds away
			}
		}()
	}
	out := bufio.NewWriter(os.Stdout)
	for {
		time.Sleep(100 * time.Millisecond)
		var total uint64
		for _, c := range counters {
			total += c.Load()
		}
		fmt.Fprintf(out, "%d %d\n", time.Now().UnixNano(), total)
		out.Flush()
	}
}

// measure drives the REAL watcher — settle gate, parse worker, priority and all
// — over a temp save dir, and reports each parse against the proxy's log.
func measure(save, proxy string, rounds int) error {
	dir, err := os.MkdirTemp("", "hitch-saves-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	cache, err := os.MkdirTemp("", "hitch-cache-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(cache)
	os.Setenv("X4MCP_CACHE_DIR", cache)

	raw, err := os.ReadFile(save)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, "quicksave.xml.gz")

	type parse struct {
		start, end time.Time
		ms         int64
	}
	var parses []parse
	var start time.Time
	done := make(chan parse, 8)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := watch.New(watch.Options{
		Roots: []string{dir},
		Logf:  func(string, ...any) {},
		Emit: func(t wire.EventType, data any) {
			switch t {
			case wire.EventTypeSaveParsing:
				start = time.Now()
			case wire.EventTypeSnapshotReady:
				done <- parse{start, time.Now(), data.(wire.SnapshotMeta).ParseMS}
			}
		},
	})
	w.Start(ctx)

	for i := range rounds {
		if err := os.WriteFile(target, raw, 0o644); err != nil {
			return err
		}
		// A fresh mtime is a fresh cache key: every round has to be a parse,
		// not a gob-cache hit.
		mt := time.Now().Add(-time.Duration(i) * time.Second)
		if err := os.Chtimes(target, mt, mt); err != nil {
			return err
		}
		select {
		case p := <-done:
			parses = append(parses, p)
		case <-time.After(10 * time.Minute):
			return fmt.Errorf("no parse finished within 10 minutes")
		}
	}

	h := w.Health().Parse
	arm := os.Getenv("X4MCP_PARSE_NICE")
	if arm == "" {
		arm = "default"
	}
	var ms []int64
	for _, p := range parses[1:] { // the first warms the page cache
		ms = append(ms, p.ms)
	}
	fmt.Printf("X4MCP_PARSE_NICE=%-8s parse %d ms (median of %d)  thread: nice %d, io %s, applied=%v %s\n",
		arm, medianI(ms), len(ms), h.Nice, h.IOClass, h.Applied, h.Detail)

	if proxy == "" {
		return nil
	}
	samples, err := readProxy(proxy)
	if err != nil {
		return err
	}
	if len(samples) < 2 {
		return fmt.Errorf("%s has %d samples; was -game running?", proxy, len(samples))
	}
	// Baseline: the proxy before the first parse started, with the core to
	// itself. Anything after that is contended by definition.
	base := rateBetween(samples, samples[0].at, parses[0].start.Add(-200*time.Millisecond))
	if base <= 0 {
		return fmt.Errorf("no clean baseline window in %s (start the proxy a few seconds earlier)", proxy)
	}
	var lost []float64
	for _, p := range parses[1:] {
		if r := rateBetween(samples, p.start, p.end); r > 0 {
			lost = append(lost, 100*(1-r/base))
		}
	}
	fmt.Printf("                          game throughput lost while parsing: median %.1f%% of %.3g spins/s (%s)\n",
		median(lost), base, joinPct(lost))
	return nil
}

type sample struct {
	at    time.Time
	spins uint64
}

func readProxy(path string) ([]sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []sample
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		at, spins, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok {
			continue
		}
		ns, err1 := strconv.ParseInt(at, 10, 64)
		n, err2 := strconv.ParseUint(spins, 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, sample{at: time.Unix(0, ns), spins: n})
	}
	return out, sc.Err()
}

// rateBetween is the proxy's spins per second over [a, b], from the samples
// that fall inside it. 0 when the window holds fewer than two.
func rateBetween(samples []sample, a, b time.Time) float64 {
	var in []sample
	for _, s := range samples {
		if !s.at.Before(a) && !s.at.After(b) {
			in = append(in, s)
		}
	}
	if len(in) < 2 {
		return 0
	}
	d := in[len(in)-1].at.Sub(in[0].at).Seconds()
	if d <= 0 {
		return 0
	}
	return float64(in[len(in)-1].spins-in[0].spins) / d
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func medianI(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func joinPct(xs []float64) string {
	parts := make([]string, 0, len(xs))
	for _, x := range xs {
		parts = append(parts, fmt.Sprintf("%.0f%%", x))
	}
	return strings.Join(parts, " ")
}
