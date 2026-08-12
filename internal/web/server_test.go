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
	for _, key := range []string{`"credits"`, `"credits_delta"`, `"credits_series"`, `"wars"`} {
		if strings.Contains(body, key) {
			t.Errorf("%s is present before the first parse: %s", key, body)
		}
	}

	// With a snapshot AND a predecessor, both the balance and the delta are
	// real numbers.
	src.published = &watch.Published{
		Snapshot: &x4save.Snapshot{
			Money: 12_405_882, MoneySeen: true,
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
	if v.Counts.Fleet != 2 || v.Counts.Stations != 1 || v.Counts.Idle != 1 {
		t.Errorf("counts = %+v, want 2 ships (1 idle) and 1 station", v.Counts)
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

	var sawRetry, sawHeartbeat bool
	var id, event, data string
	deadline := time.Now().Add(10 * time.Second)
	for (id == "" || !sawHeartbeat) && time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "retry:"):
			sawRetry = true
		case strings.HasPrefix(line, ": "):
			sawHeartbeat = true
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	if !sawRetry {
		t.Error("no retry: hint — EventSource should be told how soon to reconnect")
	}
	if !sawHeartbeat {
		t.Error("no heartbeat comment arrived; the client cannot tell silence from death")
	}
	if id != "1" || event != string(wire.EventTypeSaveDetected) {
		t.Errorf("framing: id=%q event=%q, want 1 and save.detected", id, event)
	}
	var env wire.Envelope
	if err := json.Unmarshal([]byte(data), &env); err != nil {
		t.Fatalf("data is not an envelope: %v (%s)", err, data)
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
		line, err := r2.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if strings.HasPrefix(line, "event: ") {
			replayed = append(replayed, strings.TrimSpace(strings.TrimPrefix(line, "event: ")))
		}
	}
	want := []string{string(wire.EventTypeSaveParsing), string(wire.EventTypeSnapshotReady)}
	if len(replayed) != 2 || replayed[0] != want[0] || replayed[1] != want[1] {
		t.Errorf("replayed %v, want %v", replayed, want)
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
