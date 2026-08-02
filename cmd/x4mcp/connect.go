package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// serveConnect dials OUT to a state relay (canon's /relay/{name}) and
// serves the MCP handler over the tunnel — the inverse of --http, for
// the common case where the gaming PC sits behind a firewall but an
// assistant elsewhere on the LAN needs its saves. No inbound port, no
// firewall change: one outbound WebSocket, yamux-multiplexed HTTP over
// it, reconnect with backoff forever (the relay treats a reconnect as
// newest-wins, so a flapping link self-heals).
func serveConnect(ctx context.Context, handler http.Handler, relayURL string) error {
	backoff := time.Second
	for {
		err := connectOnce(ctx, handler, relayURL)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Fprintf(os.Stderr, "x4mcp: relay connection lost (%v), retrying in %s\n", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff < 60*time.Second {
			backoff *= 2
		}
	}
}

func connectOnce(ctx context.Context, handler http.Handler, relayURL string) error {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	c, _, err := websocket.Dial(dialCtx, relayURL, nil)
	cancel()
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	fmt.Fprintf(os.Stderr, "x4mcp: connected to relay %s\n", relayURL)

	conn := websocket.NetConn(ctx, c, websocket.MessageBinary)
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 30 * time.Second
	cfg.LogOutput = io.Discard
	sess, err := yamux.Server(conn, cfg)
	if err != nil {
		c.Close(websocket.StatusInternalError, "yamux setup failed")
		return fmt.Errorf("yamux: %w", err)
	}
	defer sess.Close()

	srv := &http.Server{Handler: handler}
	go func() {
		<-ctx.Done()
		sess.Close()
	}()
	// Serve returns when the session dies (relay restart, link drop).
	if err := srv.Serve(sess); err != nil {
		return err
	}
	return nil
}
