package web

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pequalsnp/x4mcp/internal/watch"
	"github.com/pequalsnp/x4mcp/internal/wire"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// stubSource is a Source with no watcher behind it, so the HTTP surface can be
// tested without a savegame, a filesystem or a clock.
type stubSource struct {
	mu        sync.Mutex
	freshness wire.Freshness
	health    wire.WatchHealth
	meta      *wire.SnapshotMeta
	published *watch.Published
	kicks     int
}

func (s *stubSource) Freshness() wire.Freshness {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.freshness
}

func (s *stubSource) Health() wire.WatchHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.health
}

func (s *stubSource) Meta() *wire.SnapshotMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

func (s *stubSource) Published() *watch.Published {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.published
}

func (s *stubSource) Kick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kicks++
}

func newTestServer(t *testing.T, src *stubSource) (*Server, *httptest.Server) {
	t.Helper()
	srv, err := New(Options{
		Addr:      "127.0.0.1:0",
		Source:    src,
		Hub:       NewHub(),
		Version:   "0.1.0",
		BuildHash: "deadbeef",
		Logf:      func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func get(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	res, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestStateBootstrap(t *testing.T) {
	src := &stubSource{
		freshness: wire.Freshness{State: wire.FreshnessStateStartup, WatchDirs: []string{"/saves"}},
		health:    wire.WatchHealth{Dirs: []string{"/saves"}, PollIntervalMS: 2000},
	}
	srv, ts := newTestServer(t, src)
	srv.Hub().Publish(wire.EventTypeHealthLeg, wire.LegHealth{Leg: wire.LegSave, Up: true})

	res := get(t, ts, "/api/state")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var state wire.StateView
	if err := json.NewDecoder(res.Body).Decode(&state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.Build.Version != "0.1.0" || state.Build.Hash != "deadbeef" {
		t.Errorf("build = %+v, want the version and hash it was built with", state.Build)
	}
	if state.Build.SchemaVersion != x4save.SchemaVersion {
		t.Errorf("schema version = %d, want %d", state.Build.SchemaVersion, x4save.SchemaVersion)
	}
	if state.LastEventSeq != 1 {
		t.Errorf("last_event_seq = %d, want the hub's head so a subscribe misses nothing", state.LastEventSeq)
	}
	if state.Silence != (wire.SilencePolicy{HeartbeatS: 15, StaleS: 45, LostS: 60}) {
		t.Errorf("silence = %+v, want the design §6 timing", state.Silence)
	}
	if state.Snapshot != nil {
		t.Error("no parse has happened; snapshot must be absent, not empty")
	}
	if state.Vitals.Freshness.State != wire.FreshnessStateStartup {
		t.Errorf("freshness = %q, want startup", state.Vitals.Freshness.State)
	}
}

// The honesty rule at the HTTP boundary: before a parse, the board knows
// nothing, and the JSON has to say nothing rather than zero.
func TestVitalsOmitsWhatIsNotKnownYet(t *testing.T) {
	src := &stubSource{freshness: wire.Freshness{State: wire.FreshnessStateStartup}}
	_, ts := newTestServer(t, src)

	res := get(t, ts, "/api/views/vitals")
	body := readAll(t, res)
	for _, key := range []string{
		`"credits"`, `"credits_delta"`, `"credits_series"`, `"wars"`,
		`"fleet"`, `"stations"`, `"idle"`,
	} {
		if strings.Contains(body, key) {
			t.Errorf("%s is present before the first parse: %s", key, body)
		}
	}

	// With a snapshot AND a predecessor, both the balance and the delta are
	// real numbers.
	src.published = &watch.Published{
		Snapshot: &x4save.Snapshot{
			Money: 12_405_882, MoneySeen: true, PlayerAssetsSeen: true,
			Ships: []x4save.Ship{{Order: "Mine"}, {}}, Stations: []x4save.Station{{}},
		},
		Previous: &x4save.Snapshot{Money: 12_000_000, MoneySeen: true},
		Meta:     wire.SnapshotMeta{GameGUID: "g"},
	}
	res = get(t, ts, "/api/views/vitals")
	var v wire.VitalsView
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Credits == nil || *v.Credits != 12_405_882 {
		t.Errorf("credits = %v, want the balance", v.Credits)
	}
	if v.CreditsDelta == nil || *v.CreditsDelta != 405_882 {
		t.Errorf("delta = %v, want the change against the previous snapshot", v.CreditsDelta)
	}
	if v.Counts.Fleet == nil || *v.Counts.Fleet != 2 ||
		v.Counts.Stations == nil || *v.Counts.Stations != 1 ||
		v.Counts.Idle == nil || *v.Counts.Idle != 1 {
		t.Errorf("counts = %s, want 2 ships (1 idle) and 1 station", countsJSON(t, v.Counts))
	}

	// No predecessor (first parse of a playthrough, or a baseline reset): the
	// balance is known and the delta is not.
	src.published.Previous = nil
	res = get(t, ts, "/api/views/vitals")
	body = readAll(t, res)
	if !strings.Contains(body, `"credits":12405882`) || strings.Contains(body, `"credits_delta"`) {
		t.Errorf("with no baseline the delta must be absent: %s", body)
	}
}

// PRD risk #1, driven through the real parser: a patch moves the balance
// attribute, the save still gunzips, still tokenizes, still carries its
// playthrough identity — so nothing errors, the schema-mismatch guard stays
// silent (it needs BOTH GameGUID and PlayerName gone), every section is in band
// and freshness is green. The only thing that changed is that nobody read the
// balance, and int64's zero is waiting to stand in for it in the largest cell on
// the board.
func TestVitalsWillNotInventABalanceItNeverRead(t *testing.T) {
	const withBalance = `<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info><game guid="00000000-0000-4000-8000-000000000000"/><player name="Test Pilot" money="5492825"/></info>
</savegame>`
	// The same save with `money=` renamed to `credits=`. Byte for byte the
	// difference a game patch makes.
	const balanceMoved = `<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info><game guid="00000000-0000-4000-8000-000000000000"/><player name="Test Pilot" credits="5492825"/></info>
</savegame>`

	src := &stubSource{freshness: wire.Freshness{State: wire.FreshnessStateCurrent}}
	_, ts := newTestServer(t, src)

	// The control: with the attribute where this build expects it, the board
	// gets a number and a delta.
	src.published = &watch.Published{
		Snapshot: parseSave(t, withBalance),
		Previous: parseSave(t, withBalance),
		Meta:     wire.SnapshotMeta{GameGUID: "g"},
	}
	body := readAll(t, get(t, ts, "/api/views/vitals"))
	if !strings.Contains(body, `"credits":5492825`) {
		t.Fatalf("a balance that WAS read must be sent: %s", body)
	}

	// And with it moved: absent, not zero. The board draws its ∅ box from this.
	moved := parseSave(t, balanceMoved)
	if moved.PlayerName == "" || moved.GameGUID == "" {
		t.Fatal("the fixture is meant to keep its playthrough identity, so the schema-mismatch guard cannot cover for this")
	}
	src.published = &watch.Published{
		Snapshot: moved,
		Previous: parseSave(t, withBalance),
		Meta:     wire.SnapshotMeta{GameGUID: "g"},
	}
	body = readAll(t, get(t, ts, "/api/views/vitals"))
	if strings.Contains(body, `"credits"`) {
		t.Errorf("a balance nobody parsed was published as a number: %s", body)
	}
	// And no delta either: −5 492 825 against a number nobody read is a loss the
	// player never took, printed beside the value that "caused" it.
	if strings.Contains(body, `"credits_delta"`) {
		t.Errorf("a delta was computed against a balance nobody read: %s", body)
	}

	// The mirror image: the CURRENT balance is real and the predecessor's was
	// never read. The balance is still a fact; only the subtraction is not.
	src.published = &watch.Published{
		Snapshot: parseSave(t, withBalance),
		Previous: parseSave(t, balanceMoved),
		Meta:     wire.SnapshotMeta{GameGUID: "g"},
	}
	body = readAll(t, get(t, ts, "/api/views/vitals"))
	if !strings.Contains(body, `"credits":5492825`) || strings.Contains(body, `"credits_delta"`) {
		t.Errorf("want the balance and no delta: %s", body)
	}
}

// The same failure as the balance one, on the three cells beside it, and the
// one the board draws largest after CREDITS.
//
// Fleet, stations and idle used to be `len()` of a slice. A length has no way to
// say "the parser never found the player's property", so renaming the attribute
// a save marks ownership with — the playthrough identity untouched, so the
// schema-mismatch guard stays quiet — published fleet=0 stations=0 idle=0 with a
// green stamp, and the board printed FLEET 0 STN 0 IDLE 0 about an empire that
// is all still there. The section band caught it, but only for whoever opened
// the health drawer: no amber, no beacon change, the save leg still up.
func TestVitalsWillNotInventCountsItNeverRead(t *testing.T) {
	// Two ships (one on an order, one idle) and a station, in the nesting a real
	// save uses: galaxy > sector > components.
	const owned = `<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info><game guid="00000000-0000-4000-8000-000000000000" time="591711.4"/><player name="Test Pilot" money="5492825"/></info>
<universe>
 <component class="galaxy" macro="xu_ep2_universe_macro"><connections>
  <connection><component class="sector" macro="cluster_01_sector001_macro"><connections>
   <connection><component class="ship_m" owner="player" macro="ship_arg_m_miner_solid_01_a_macro" code="AAA-001" id="[0x1]">
     <orders><order order="MiningRoutine"/></orders></component></connection>
   <connection><component class="ship_s" owner="player" macro="ship_arg_s_scout_01_a_macro" code="AAA-002" id="[0x2]"/></connection>
   <connection><component class="station" owner="player" macro="station_arg_prod_01_macro" code="STN-001" id="[0x3]"/></connection>
  </connections></component></connection>
 </connections></component>
</universe>
</savegame>`
	// The same save with `owner=` renamed. Byte for byte, the difference a game
	// patch makes — and every collection comes back empty.
	movedOwner := strings.ReplaceAll(owned, `owner="player"`, `owned_by="player"`)

	src := &stubSource{freshness: wire.Freshness{State: wire.FreshnessStateCurrent}}
	_, ts := newTestServer(t, src)

	// The control: with the attribute where this build expects it, the counts
	// are numbers — including the ZERO ones, which are real answers.
	src.published = &watch.Published{Snapshot: parseSave(t, owned), Meta: wire.SnapshotMeta{GameGUID: "g"}}
	body := readAll(t, get(t, ts, "/api/views/vitals"))
	for _, want := range []string{`"fleet":2`, `"stations":1`, `"idle":1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("counts that WERE read must be sent (%s): %s", want, body)
		}
	}

	moved := parseSave(t, movedOwner)
	if moved.PlayerName == "" || moved.GameGUID == "" {
		t.Fatal("the fixture is meant to keep its playthrough identity, so the schema-mismatch guard cannot cover for this")
	}
	if len(moved.Ships) != 0 || len(moved.Stations) != 0 {
		t.Fatalf("the fixture is meant to yield no assets at all; got %d ships and %d stations",
			len(moved.Ships), len(moved.Stations))
	}
	src.published = &watch.Published{Snapshot: moved, Meta: wire.SnapshotMeta{GameGUID: "g"}}
	body = readAll(t, get(t, ts, "/api/views/vitals"))
	for _, key := range []string{`"fleet"`, `"stations"`, `"idle"`} {
		if strings.Contains(body, key) {
			t.Errorf("a count nobody read was published as a number (%s): %s", key, body)
		}
	}

	// The other half of the doctrine: an empire that really owns no stations
	// says so. Absent is unknown; 0 is a fact about an early game.
	src.published = &watch.Published{
		Snapshot: &x4save.Snapshot{PlayerAssetsSeen: true},
		Meta:     wire.SnapshotMeta{GameGUID: "g"},
	}
	body = readAll(t, get(t, ts, "/api/views/vitals"))
	for _, want := range []string{`"fleet":0`, `"stations":0`, `"idle":0`} {
		if !strings.Contains(body, want) {
			t.Errorf("a counted zero must survive (%s): %s", want, body)
		}
	}
}

// countsJSON renders the counts the way the browser sees them, so a failure
// says `{"fleet":2}` rather than three addresses.
func countsJSON(t *testing.T, c wire.Counts) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal counts: %v", err)
	}
	return string(b)
}

// parseSave runs the shipped parser over a save written to a temp dir. Nothing
// here can see, let alone touch, a real X4 save directory.
func parseSave(t *testing.T, xml string) *x4save.Snapshot {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quicksave.xml.gz")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(xml)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := x4save.ParseFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return snap
}

func TestRefreshSaveKicksTheWatcher(t *testing.T) {
	src := &stubSource{}
	_, ts := newTestServer(t, src)

	// A cross-site form POST cannot set a custom header; this one is required.
	res, err := ts.Client().Post(ts.URL+"/api/admin/refresh-save", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status without X-X4Cue = %d, want 403", res.StatusCode)
	}
	if src.kicks != 0 {
		t.Error("a rejected request must not reach the watcher")
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/refresh-save", nil)
	req.Header.Set("X-X4Cue", "1")
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202: the parse is 5–16 s and the stream reports it", res.StatusCode)
	}
	if src.kicks != 1 {
		t.Errorf("kicks = %d, want 1", src.kicks)
	}
}

// DNS rebinding: a hostile page can resolve its own name to 127.0.0.1 and then
// read the board from the browser. The Host header is what distinguishes that
// from the player's own tab.
func TestHostAllowlist(t *testing.T) {
	_, ts := newTestServer(t, &stubSource{})
	cases := []struct {
		host string
		want int
	}{
		{host: "127.0.0.1:8484", want: http.StatusOK},
		{host: "localhost:8484", want: http.StatusOK},
		{host: "[::1]:8484", want: http.StatusOK},
		{host: "board.evil.example", want: http.StatusForbidden},
		{host: "192.168.1.50:8484", want: http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.host, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/state", nil)
			req.Host = c.host
			res, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != c.want {
				t.Errorf("Host %q -> %d, want %d", c.host, res.StatusCode, c.want)
			}
		})
	}

	// A request with NO Host at all is not a browser and must not be the way
	// past the allowlist. It cannot be sent through net/http's client, which
	// always fills one in, so this is the wire.
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET /api/state HTTP/1.0\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "403") {
		t.Errorf("a Host-less HTTP/1.0 request got %q, want 403: it named no machine", strings.TrimSpace(status))
	}

	// /healthz is exempt: it is the same endpoint the relay mux serves and it
	// says nothing about anybody's empire.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
	req.Host = "board.evil.example"
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("/healthz = %d, want 200 whatever the Host is", res.StatusCode)
	}
}

func TestCheckBind(t *testing.T) {
	cases := []struct {
		name    string
		addr    string
		token   string
		wantErr bool
	}{
		{name: "loopback", addr: "127.0.0.1:8484"},
		{name: "localhost", addr: "localhost:8484"},
		{name: "ipv6 loopback", addr: "[::1]:8484"},
		{name: "any interface without a token", addr: "0.0.0.0:8484", wantErr: true},
		{name: "lan without a token", addr: "192.168.1.50:8484", wantErr: true},
		{name: "lan with a token", addr: "192.168.1.50:8484", token: "s3cret"},
		{name: "empty means no web server", addr: ""},
		{name: "nonsense", addr: "not-an-address", wantErr: true},

		// The wildcards. net.Listen reads a missing host as EVERY interface, so
		// these are the same exposure as 0.0.0.0 written three shorter ways —
		// and ":8484" is the spelling the flag tests themselves use.
		{name: "bare port", addr: ":8484", wantErr: true},
		{name: "bare port with a token", addr: ":8484", token: "s3cret"},
		{name: "ipv6 any", addr: "[::]:8484", wantErr: true},
		{name: "any interface on any port", addr: "0.0.0.0:0", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckBind(c.addr, c.token)
			if (err != nil) != c.wantErr {
				t.Errorf("CheckBind(%q, token=%q) = %v, wantErr %v", c.addr, c.token, err, c.wantErr)
			}
		})
	}
}

func TestAuthTokenGuardsEverythingButHealthz(t *testing.T) {
	srv, err := New(Options{
		Addr: "192.168.1.50:8484", Source: &stubSource{}, AuthToken: "s3cret",
		Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	res := get(t, ts, "/api/state")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a token: %d, want 401", res.StatusCode)
	}
	if res := get(t, ts, "/healthz"); res.StatusCode != http.StatusOK {
		t.Errorf("/healthz = %d, want 200 (a liveness probe carries no token)", res.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("with the token: %d, want 200", res.StatusCode)
	}
}

// The board is embedded in the binary (D6). Until this ran, package web
// compiled and embedded dist/ and nothing imported it — a build could have
// shipped without the app in it and every gate stayed green.
func TestServesTheEmbeddedBoard(t *testing.T) {
	_, ts := newTestServer(t, &stubSource{})

	res := get(t, ts, "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d", res.StatusCode)
	}
	body := readAll(t, res)
	if !strings.Contains(strings.ToLower(body), "<!doctype html") {
		t.Errorf("GET / did not serve the app shell: %.120s", body)
	}
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP = %q, want default-src 'self'", csp)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("index.html Cache-Control = %q, want no-cache: the shell and the binary ship together", cc)
	}

	// A client route is not a 404; a missing asset is.
	if res := get(t, ts, "/health"); res.StatusCode != http.StatusOK {
		t.Errorf("SPA fallback for /health = %d, want the shell", res.StatusCode)
	}
	if res := get(t, ts, "/assets/nope.js"); res.StatusCode != http.StatusNotFound {
		t.Errorf("missing asset = %d, want 404", res.StatusCode)
	}
}

// The SSE surface end to end over real HTTP: framing, replay on reconnect, and
// the heartbeat the client's silence timers are measured against.
func TestEventStream(t *testing.T) {
	src := &stubSource{}
	srv, ts := newTestServer(t, src)
	srv.Hub().heartbeat = 20 * time.Millisecond

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/events", nil)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	r := bufio.NewReader(res.Body)

	srv.Hub().Publish(wire.EventTypeSaveDetected, wire.SaveMeta{Name: "quicksave", Kind: wire.SaveKindQuicksave})

	var sawRetry bool
	var beat, detected sseFrame
	deadline := time.Now().Add(10 * time.Second)
	for (detected.id == "" || beat.event == "") && time.Now().Before(deadline) {
		f := readFrame(t, r)
		if f.retry != "" {
			sawRetry = true
		}
		switch f.event {
		case string(wire.EventTypeHeartbeat):
			beat = f
		case string(wire.EventTypeSaveDetected):
			detected = f
		}
	}
	if !sawRetry {
		t.Error("no retry: hint — EventSource should be told how soon to reconnect")
	}
	// The heartbeat framing, which is the client's ENTIRE evidence that the
	// server is alive. It used to be an SSE comment, which fires no JS event by
	// specification, so the client counted the socket being open instead — and a
	// socket is open until somebody closes it, which a frozen process never
	// does.
	if beat.event != string(wire.EventTypeHeartbeat) {
		t.Error("no heartbeat EVENT arrived; a comment is invisible to EventSource and the client cannot tell silence from death")
	}
	if beat.data == "" {
		t.Error("the heartbeat carries no data: SSE discards an event whose data buffer is empty, so it would be exactly as invisible as the comment it replaced")
	}
	if beat.id != "" {
		t.Errorf("the heartbeat carries id=%q: it is not a point in the log, and stamping one moves the browser's Last-Event-ID onto a sequence the ring cannot replay", beat.id)
	}
	if detected.id != "1" {
		t.Errorf("framing: id=%q, want 1", detected.id)
	}
	var env wire.Envelope
	if err := json.Unmarshal([]byte(detected.data), &env); err != nil {
		t.Fatalf("data is not an envelope: %v (%s)", err, detected.data)
	}
	if env.Seq != 1 || env.Type != wire.EventTypeSaveDetected {
		t.Errorf("envelope = %+v, want seq 1 of save.detected", env)
	}

	// Reconnect with the cursor: everything after it arrives, nothing before.
	srv.Hub().Publish(wire.EventTypeSaveParsing, wire.SaveMeta{Name: "quicksave"})
	srv.Hub().Publish(wire.EventTypeSnapshotReady, wire.SnapshotMeta{GameGUID: "g"})
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/events", nil)
	req2.Header.Set("Last-Event-ID", "1")
	res2, err := ts.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	r2 := bufio.NewReader(res2.Body)
	var replayed []string
	deadline = time.Now().Add(10 * time.Second)
	for len(replayed) < 2 && time.Now().Before(deadline) {
		f := readFrame(t, r2)
		// The keep-alive is not a replayed event, and now that it has a name it
		// would otherwise be counted as one.
		if f.event == "" || f.event == string(wire.EventTypeHeartbeat) {
			continue
		}
		replayed = append(replayed, f.event)
	}
	want := []string{string(wire.EventTypeSaveParsing), string(wire.EventTypeSnapshotReady)}
	if len(replayed) != 2 || replayed[0] != want[0] || replayed[1] != want[1] {
		t.Errorf("replayed %v, want %v", replayed, want)
	}
}

// A wedged hub must not be able to hold a browser's socket OPEN.
//
// Stopping the heartbeat (hub_test.go) is only half the fix, and it is the half
// the player cannot see: net/http will not finish a response until ServeHTTP
// returns, and no FIN goes out until it does, so a teardown that parks on h.mu
// leaves the connection ESTABLISHED and silent for as long as the process lives.
// The client used to read exactly that state — an open socket — as proof of
// life, which is how five minutes, and then an hour, of a dead server rendered
// as `connection = live` with the stamp saying `quicksave · 5m ago`. It also
// wedged the server's own shutdown: one run left httptest.Server blocked in
// Close for 3m20s.
func TestEventsHandlerClosesTheSocketWhenTheHubCannotMakeProgress(t *testing.T) {
	src := &stubSource{}
	srv, ts := newTestServer(t, src)
	hub := srv.Hub()
	hub.heartbeat = 10 * time.Millisecond
	hub.liveness = 20 * time.Millisecond

	res, err := ts.Client().Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	// The `retry:` hint: the stream is up and the subscribe is done, so what
	// follows is about teardown and nothing else.
	if f := readFrame(t, bufio.NewReader(res.Body)); f.retry == "" {
		t.Fatalf("the stream never started: %+v", f)
	}

	// Wedge it exactly as a blocking send under h.mu did. Released by this
	// defer BEFORE t.Cleanup closes the test server, so a regression here fails
	// the test instead of hanging the whole binary.
	hub.mu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			hub.mu.Unlock()
		}
	}()

	drained := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, res.Body)
		drained <- err
	}()
	select {
	case <-drained:
		// EOF (or a read error): the response finished, so the handler returned
		// and the socket is closed. That is the whole assertion.
	case <-time.After(10 * time.Second):
		t.Fatal("the hub is wedged and the connection is still open: the handler is parked in its deferred unsubscribe, so net/http cannot finish the response and the tab keeps reading an ESTABLISHED socket as a live board")
	}

	// And the bookkeeping is not leaked: the entry unsubscribe could not delete
	// is collected by the next holder of the lock.
	hub.mu.Unlock()
	unlocked = true
	deadline := time.Now().Add(5 * time.Second)
	for hub.Clients() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("clients = %d after the connection closed; the gone client was never reaped", hub.Clients())
		}
		time.Sleep(time.Millisecond)
	}
}

// sseFrame is one SSE block: the fields written before the blank line that ends
// it. Reading whole frames rather than loose lines is what lets a test say
// "this event carried no id", which is a property of the frame and invisible
// line by line.
type sseFrame struct {
	id, event, data, retry string
	comment                bool
}

func readFrame(t *testing.T, r *bufio.Reader) sseFrame {
	t.Helper()
	var f sseFrame
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		if line == "" {
			return f
		}
		switch {
		case strings.HasPrefix(line, ":"):
			f.comment = true
		case strings.HasPrefix(line, "id: "):
			f.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			f.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.data = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, "retry: "):
			f.retry = strings.TrimPrefix(line, "retry: ")
		}
	}
}

func readAll(t *testing.T, res *http.Response) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String()
		}
	}
}
