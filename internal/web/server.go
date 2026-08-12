// Package web is the board's HTTP surface: the SSE hub, the REST views over
// internal/wire, and the embedded Svelte app.
//
// It is the SUPERSET mux (D10). The relay and --http serve exactly /mcp and
// /healthz, because the relay is LAN-reachable and unauthenticated by design;
// everything here — /api/*, admin, the static app — binds to loopback and is
// never reachable through it. Keeping that split as two muxes rather than one
// mux with route guards removes an entire class of exposure mistake.
package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pequalsnp/x4mcp/internal/watch"
	"github.com/pequalsnp/x4mcp/internal/wire"
	webassets "github.com/pequalsnp/x4mcp/web"
)

// AuthTokenEnv is the community-edition seam: set it and a non-loopback bind is
// allowed, with every request required to carry the token.
const AuthTokenEnv = "X4MCP_AUTH_TOKEN"

// Source is what the board reads. The watcher implements it; a test does not
// need one.
type Source interface {
	Freshness() wire.Freshness
	Health() wire.WatchHealth
	Meta() *wire.SnapshotMeta
	Published() *watch.Published
	Kick()
}

// Options configures a Server.
type Options struct {
	// Addr is the host:port the server will bind, used for the bind policy and
	// the Host-header allowlist.
	Addr   string
	Source Source
	Hub    *Hub
	// MCP is the MCP handler mounted at /mcp, so one process serves the board
	// and the tools on the same loopback port.
	MCP http.Handler
	// Static is the built app; nil means the embedded web/dist.
	Static    fs.FS
	Version   string
	BuildHash string
	// AuthToken, when set, is required as a bearer token on every request
	// except /healthz. Defaults to the environment.
	AuthToken string
	Logf      func(string, ...any)
	Now       func() time.Time
}

// Server is the web face.
type Server struct {
	opts    Options
	mux     *http.ServeMux
	started time.Time
}

// New builds the server and its routes.
func New(opts Options) (*Server, error) {
	if opts.Source == nil {
		return nil, errors.New("web: a Source is required")
	}
	if opts.Hub == nil {
		opts.Hub = NewHub()
	}
	if opts.Static == nil {
		opts.Static = webassets.Dist
	}
	if opts.AuthToken == "" {
		opts.AuthToken = os.Getenv(AuthTokenEnv)
	}
	if opts.Logf == nil {
		opts.Logf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "x4mcp web: "+format+"\n", args...)
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := CheckBind(opts.Addr, opts.AuthToken); err != nil {
		return nil, err
	}
	s := &Server{opts: opts, mux: http.NewServeMux(), started: opts.Now()}
	s.routes()
	return s, nil
}

// CheckBind refuses to put the board on a network interface without a token.
//
// Everything on this mux reads the player's savegame and can write their plan
// store; the MCP face mounted beside it includes plan-write tools. Loopback is
// the v1 posture, and the escape hatch is a token rather than a flag, because a
// flag is one careless copy-paste away from a LAN-exposed empire.
func CheckBind(addr, token string) error {
	if addr == "" || token != "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("web: cannot parse --web %q: %w", addr, err)
	}
	if loopbackBind(host) {
		return nil
	}
	return fmt.Errorf("web: refusing to bind %q: it is not loopback and %s is not set "+
		"(the board serves savegame contents and plan writes with no auth)", addr, AuthTokenEnv)
}

// loopbackBind decides whether a LISTEN address reaches only this machine.
//
// It is deliberately not isLoopbackHost. The two questions look identical and
// answer OPPOSITELY on the empty host: a request that carries no Host named no
// machine, while a bind address that carries no host is net.Listen's spelling
// of every interface. Sharing one helper is what put `--web :8484` — the
// shortest and most natural way to write it, and the one the flag tests use —
// on the LAN with the savegame and the plan-write tools and no token.
//
// Everything else follows from IsLoopback, which is already false for the
// wildcards 0.0.0.0 and :: and for every routable address.
func loopbackBind(host string) bool {
	if host == "" {
		return false
	}
	return isLoopbackHost(host)
}

// isLoopbackHost answers for a Host HEADER: a name or literal that refers to
// this machine. The empty string is not one of them — see hostAllowed.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "localhost.localdomain":
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Handler is the whole web mux, guards included.
func (s *Server) Handler() http.Handler { return s.guard(s.mux) }

// Hub is the event hub this server serves; the watcher publishes into it.
func (s *Server) Hub() *Hub { return s.opts.Hub }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	s.mux.HandleFunc("GET /api/state", s.handleState)
	s.mux.HandleFunc("GET /api/views/vitals", s.handleVitals)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	s.mux.HandleFunc("POST /api/admin/refresh-save", s.handleRefresh)
	if s.opts.MCP != nil {
		s.mux.Handle("/mcp", s.opts.MCP)
	}
	s.mux.Handle("/", s.static())
}

// guard is the whole-mux security policy: a Host allowlist (DNS rebinding), a
// custom header on state-changing calls (CSRF), and the bearer token when one
// is configured. No CORS headers are ever sent — a browser on another origin
// has no business here, and silence is the correct answer.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			if !s.hostAllowed(r.Host) {
				http.Error(w, "bad host", http.StatusForbidden)
				return
			}
			if s.opts.AuthToken != "" && !s.authorized(r) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// A form POST from a hostile page cannot set a custom header, and
			// this one is required only on /api/* writes: /mcp is called by MCP
			// clients that send no headers of ours and is covered by the Host
			// check and the loopback bind.
			if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/") &&
				r.Header.Get("X-X4Cue") == "" {
				http.Error(w, "missing X-X4Cue header", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(hostHeader string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	if host == "" {
		// No Host at all. Every browser sends one, so this is a hand-written
		// HTTP/1.0 request — and it used to be the way past the allowlist
		// entirely, since "" read as loopback here.
		return false
	}
	if isLoopbackHost(host) {
		return true
	}
	// Whatever we were asked to bind is legitimate too: a LAN bind is only
	// reachable at all when a token is set.
	if s.opts.Addr != "" {
		if bound, _, err := net.SplitHostPort(s.opts.Addr); err == nil && bound != "" &&
			!strings.EqualFold(bound, "0.0.0.0") && strings.EqualFold(bound, host) {
			return true
		}
	}
	return s.opts.AuthToken != "" // a token holder may arrive under any name
}

func (s *Server) authorized(r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.AuthToken)) == 1
}

// ---- handlers ----

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, s.State())
}

func (s *Server) handleVitals(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, s.Vitals())
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	// Kick, do not wait: a parse is 5–16 s and an HTTP request is not the place
	// to hold that. The stream says what happened — save.detected, save.parsing,
	// snapshot.ready — which is the same path a save the player just wrote
	// takes, so the client needs no special case for the button.
	s.opts.Source.Kick()
	writeJSONStatus(w, http.StatusAccepted, map[string]any{
		"kicked": true, "last_event_seq": s.opts.Hub.LastSeq(),
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	after := lastEventID(r)
	c, replay := s.opts.Hub.subscribe(after)
	defer s.opts.Hub.unsubscribe(c)
	s.opts.Hub.stream(w, r, c, replay)
}

// lastEventID reads the reconnect cursor. EventSource sends it as a header;
// the query parameter is for curl and for tests.
func lastEventID(r *http.Request) int64 {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("last_event_id")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func writeJSON(w http.ResponseWriter, _ *http.Request, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

// writeJSONStatus sets the headers BEFORE the status line, because after
// WriteHeader they are silently dropped — which is how a 202 ends up
// content-type-less and a client parses it as text.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status is already written; nothing useful is left to say to the
		// client, so this is for the operator.
		_ = err
	}
}

// static serves the built app with an SPA fallback: an unknown path with no
// extension is a client route, not a 404.
func (s *Server) static() http.Handler {
	files := http.FileServerFS(s.opts.Static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Assets and the binary ship together (D6), so a hashed asset is
		// immutable and index.html must never be cached — a stale shell is how
		// a tab ends up talking to a build it was not compiled against.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob:; font-src 'self' data:; "+
				"style-src 'self' 'unsafe-inline'; connect-src 'self'; object-src 'none'; "+
				"base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")

		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(s.opts.Static, clean); err != nil {
			if path.Ext(clean) != "" {
				http.NotFound(w, r)
				return
			}
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			clean = "index.html"
		}
		if clean == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(clean, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

// ListenAndServe binds and serves until ctx is done, then drains for up to 5 s.
//
// It does not return until that drain is over. Shutdown makes ListenAndServe
// return immediately, so a caller that treats "it returned" as "it is finished"
// leaves the process free to exit through the middle of the drain — which is
// the whole point of having one.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.opts.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: /api/events is a stream that is supposed to stay
		// open for a whole play session.
		IdleTimeout: 120 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	s.opts.Logf("board on http://%s (SSE at /api/events)", s.opts.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		// Never bound: there is nothing to drain, and the goroutine above is
		// released when ctx ends.
		return err
	}
	<-drained
	return nil
}
