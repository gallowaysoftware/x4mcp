package main

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A miniature savegame with the shapes that actually matter: nesting deep enough
// that a path is ambiguous by name alone, an attribute that only some elements
// carry, and an owner="player" subtree with a same-named element outside it.
const mini = `<savegame>
  <universe>
    <component class="sector" id="[0x1]">
      <connections>
        <connection>
          <component class="station" id="[0x2]" owner="player">
            <cargo><ware ware="hullparts" amount="312"/><ware ware="energycells"/></cargo>
            <build state="waitingforresources" step="1" steps="15"/>
            <hull value="7816"/>
          </component>
        </connection>
        <connection>
          <component class="station" id="[0x3]" owner="argon">
            <cargo><ware ware="silicon" amount="9"/></cargo>
            <build state="active" step="4" steps="4"/>
          </component>
        </connection>
      </connections>
    </component>
  </universe>
</savegame>`

func run(t *testing.T, fn func(out *bytes.Buffer) error) string {
	t.Helper()
	var out bytes.Buffer
	if err := fn(&out); err != nil {
		t.Fatalf("mode returned: %v", err)
	}
	return out.String()
}

func TestPathMatchIsASuffixUnlessAnchored(t *testing.T) {
	stack := []frame{{name: "savegame"}, {name: "universe"}, {name: "component"}, {name: "cargo"}}
	cases := []struct {
		pat  string
		want bool
		why  string
	}{
		{"cargo", true, "a bare name matches at any depth"},
		{"component/cargo", true, "a suffix matches its parent chain"},
		{"universe/cargo", false, "the suffix must be contiguous, not a subsequence"},
		{"*/cargo", true, "a wildcard matches exactly one segment"},
		{"/savegame/universe/component/cargo", true, "a leading slash anchors at the root"},
		{"/universe/component/cargo", false, "an anchored path must match the whole stack"},
	}
	for _, c := range cases {
		if got := mustPath(c.pat).match(stack); got != c.want {
			t.Errorf("%q matched=%v, want %v — %s", c.pat, got, c.want, c.why)
		}
	}
}

func TestAncestorScopeSelectsOneSubtreeNotBothStations(t *testing.T) {
	anc, err := parsePred("owner=player")
	if err != nil {
		t.Fatal(err)
	}
	got := run(t, func(out *bytes.Buffer) error {
		return runValues(strings.NewReader(mini), out, mustPath("cargo/ware"), "ware", anc, pred{})
	})
	if !strings.Contains(got, "hullparts") || !strings.Contains(got, "energycells") {
		t.Errorf("player station's wares missing from:\n%s", got)
	}
	// The argon station also has a <cargo>/<ware>. Leaking it would silently turn
	// every "your stations hold X" answer into "the galaxy holds X".
	if strings.Contains(got, "silicon") {
		t.Errorf("an argon-owned ware escaped the owner=player scope:\n%s", got)
	}
}

// The value histogram has to have a bucket for "this element did not carry the
// attribute at all". Folding absence into an empty string would make the whole
// tool useless for the one question it exists to answer.
func TestAbsentAttributeGetsItsOwnBucket(t *testing.T) {
	got := run(t, func(out *bytes.Buffer) error {
		return runValues(strings.NewReader(mini), out, mustPath("cargo/ware"), "amount", pred{}, pred{})
	})
	if !strings.Contains(got, "attribute absent") {
		t.Errorf("no absence bucket; energycells carries no amount=:\n%s", got)
	}
}

func TestAttrsReportsPartialPresence(t *testing.T) {
	got := run(t, func(out *bytes.Buffer) error {
		return runAttrs(strings.NewReader(mini), out, mustPath("ware"), pred{}, pred{}, 3)
	})
	// ware= is on all three, amount= on two. The percentage column is how a
	// reader spots an attribute the game omits, so it must not read 100%.
	if !strings.Contains(got, "3 elements matched") {
		t.Errorf("wrong match count:\n%s", got)
	}
	for _, want := range []string{"ware ", "amount "} {
		if !strings.Contains(got, want) {
			t.Errorf("attribute %q missing:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "66.7%") {
		t.Errorf("amount= is on 2 of 3 elements; want a 66.7%% row:\n%s", got)
	}
}

func TestDumpEmitsTheWholeSubtree(t *testing.T) {
	anc, _ := parsePred("owner=player")
	got := run(t, func(out *bytes.Buffer) error {
		return runDump(strings.NewReader(mini), out, mustPath("cargo"), anc, pred{}, 0)
	})
	if !strings.Contains(got, `ware="hullparts"`) || !strings.Contains(got, `amount="312"`) {
		t.Errorf("subtree contents missing:\n%s", got)
	}
	if strings.Contains(got, "silicon") {
		t.Errorf("dumped a subtree outside the ancestor scope:\n%s", got)
	}
}

func TestDumpMaxStops(t *testing.T) {
	got := run(t, func(out *bytes.Buffer) error {
		return runDump(strings.NewReader(mini), out, mustPath("cargo"), pred{}, pred{}, 1)
	})
	if n := strings.Count(got, "<!-- match"); n != 1 {
		t.Errorf("emitted %d matches, want -max 1 to stop after the first:\n%s", n, got)
	}
}

// The regression that matters most. -dump reads a subtree to its end, so that
// element's EndElement never reaches the walk loop. If the loop does not pop the
// frame itself, every path reported for the REST of the file is one level too
// deep — and because a save is 15 million lines, the corruption is invisible in
// the first screenful and wrong everywhere after it.
//
// The control is the same document walked with a visit that consumes nothing:
// the paths recorded after the consumed element must be identical either way.
func TestConsumedSubtreeDoesNotCorruptLaterPaths(t *testing.T) {
	record := func(consume bool) []string {
		var paths []string
		err := walk(strings.NewReader(mini), pred{}, func(dec *xml.Decoder, stack []frame) (bool, error) {
			paths = append(paths, pathOf(stack))
			if consume && stack[len(stack)-1].name == "cargo" {
				var sink bytes.Buffer
				enc := xml.NewEncoder(&sink)
				start := xml.StartElement{Name: xml.Name{Local: "cargo"}}
				if err := encodeSubtree(dec, enc, start); err != nil {
					return true, err
				}
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		return paths
	}

	control := record(false)
	consumed := record(true)

	// Consuming <cargo> legitimately removes its descendants from the walk.
	var want []string
	for _, p := range control {
		if strings.Contains(p, "/cargo/") {
			continue
		}
		want = append(want, p)
	}
	if len(consumed) != len(want) {
		t.Fatalf("saw %d paths, want %d\n got: %v\nwant: %v", len(consumed), len(want), consumed, want)
	}
	for i := range want {
		if consumed[i] != want[i] {
			t.Fatalf("path %d after a consumed subtree = %q, want %q\n(a stack left unpopped shifts every later path deeper)", i, consumed[i], want[i])
		}
	}

	// And the specific symptom, asserted directly: <build> follows <cargo>, so it
	// is the first element that a leaked frame would misreport.
	var buildPath string
	for _, p := range consumed {
		if strings.HasSuffix(p, "/build") {
			buildPath = p
			break
		}
	}
	if want := "/savegame/universe/component/connections/connection/component/build"; buildPath != want {
		t.Errorf("build path after a consumed <cargo> = %q, want %q", buildPath, want)
	}
}

func TestOpenReadsGzipAndPlainAlike(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "save.xml")
	if err := os.WriteFile(plain, []byte(mini), 0o644); err != nil {
		t.Fatal(err)
	}
	gzPath := filepath.Join(dir, "save.xml.gz")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(mini)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gzPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{plain, gzPath} {
		r, closeAll, err := open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		var out bytes.Buffer
		if err := runValues(r, &out, mustPath("cargo/ware"), "ware", pred{}, pred{}); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		closeAll()
		if !strings.Contains(out.String(), "hullparts") {
			t.Errorf("%s: gzip and plain must read alike, got:\n%s", path, out.String())
		}
	}
}

func TestPredicateWildcardIsSubstring(t *testing.T) {
	p, err := parsePred("class=*station*")
	if err != nil {
		t.Fatal(err)
	}
	if !p.matchAttrs([]xml.Attr{{Name: xml.Name{Local: "class"}, Value: "station_l"}}) {
		t.Error("*station* should match station_l")
	}
	exact, _ := parsePred("class=station")
	if exact.matchAttrs([]xml.Attr{{Name: xml.Name{Local: "class"}, Value: "station_l"}}) {
		t.Error("without a wildcard the match must be exact, or -where silently over-selects")
	}
}

func TestEmptyPredicateMatchesEverything(t *testing.T) {
	var p pred
	if !p.matchAttrs(nil) {
		t.Error("an unset -where must not filter anything out")
	}
}
