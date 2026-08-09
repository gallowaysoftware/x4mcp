package x4save

import (
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSave gzips xml to a temp file and returns its path.
func writeSave(t *testing.T, xml string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "quicksave.xml.gz")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(xml)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const saveWithBlueprints = `<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info><player name="Emry Sloan" money="3054068"/></info>
<blueprints>
<blueprint ware="module_gen_prod_energycells_01"/>
<blueprint ware="module_arg_prod_hullparts_01"/>
</blueprints>
</savegame>`

// A save that simply has no <blueprints> element — indistinguishable, before
// this fix, from a player who owns none.
const saveWithoutBlueprints = `<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info><player name="Emry Sloan" money="3054068"/></info>
</savegame>`

func TestBlueprintsSeenDistinguishesEmptyFromMissing(t *testing.T) {
	cases := []struct {
		name      string
		xml       string
		wantSeen  bool
		wantCount int
	}{
		{"blueprints present", saveWithBlueprints, true, 2},
		{"blueprints section absent", saveWithoutBlueprints, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap, err := ParseFile(writeSave(t, tc.xml))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if snap.BlueprintsSeen != tc.wantSeen {
				t.Errorf("BlueprintsSeen = %v, want %v", snap.BlueprintsSeen, tc.wantSeen)
			}
			if len(snap.Blueprints) != tc.wantCount {
				t.Errorf("blueprints = %d, want %d", len(snap.Blueprints), tc.wantCount)
			}
		})
	}
}

// bulkySave returns a save big enough that parsing takes long enough to touch
// mid-read reliably, so the guard can be tested without racing.
func bulkySave() string {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<savegame>\n")
	b.WriteString(`<info><player name="Emry Sloan" money="3054068"/></info>` + "\n")
	b.WriteString("<known>\n")
	for i := 0; i < 400000; i++ {
		b.WriteString(`<entry id="filler" read="0"/>` + "\n")
	}
	b.WriteString("</known>\n<blueprints>\n<blueprint ware=\"module_gen_prod_energycells_01\"/>\n</blueprints>\n</savegame>\n")
	return b.String()
}

// The bug this whole change exists for: X4 rewrites a save in place while the
// player keeps playing, and a ~10s parse can straddle that write. Serving the
// result silently is what reported "you own no blueprints" to a player with 117.
func TestParseRefusesASaveChangedMidRead(t *testing.T) {
	p := writeSave(t, bulkySave())

	// Baseline: left alone, this file must parse cleanly and keep its blueprints.
	snap, err := ParseFile(p)
	if err != nil {
		t.Fatalf("a stable file must parse cleanly, got %v", err)
	}
	if !snap.BlueprintsSeen {
		t.Fatal("stable parse lost the blueprint section")
	}

	// Now land a write while the parse is in flight, exactly as X4 does.
	touched := make(chan struct{})
	go func() {
		time.Sleep(15 * time.Millisecond)
		future := time.Now().Add(3 * time.Second)
		_ = os.Chtimes(p, future, future)
		close(touched)
	}()

	_, err = ParseFile(p)
	<-touched
	if !errors.Is(err, ErrSaveChanged) {
		t.Fatalf("parse of a save rewritten mid-read returned %v; want ErrSaveChanged so the caller retries instead of trusting a half-read snapshot", err)
	}
}
