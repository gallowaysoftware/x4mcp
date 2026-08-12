package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// errNoGameCommand is returned when play was given nothing to launch.
var errNoGameCommand = errors.New(
	`play needs the game command after --, e.g. "x4mcp play --relay ws://hum:8091/relay/x4 -- %command%"`)

// runPlay launches X4 with the MCP server running alongside it, and
// stops the server when the game exits.
//
// This is the companion-app shape. The alternative — a service that runs
// at login — keeps answering while the game is shut, which is useful if
// something other than the player is asking, but it also means one more
// permanently-running process phoning home that nobody remembers
// enabling. A save only changes while the game runs, so for most people
// the server's useful lifetime IS the play session.
//
// On Steam it goes in Launch Options and is then never thought about
// again:
//
//	x4mcp play --connect ws://hum:8091/relay/x4 -- %command%
//
// Steam expands %command% to the real launcher, Proton and all, so the
// game starts exactly as it would have.
func runPlay(args []string) error {
	// Shared with fs25mcp (cmd/*/cli.go): transport flags are recognised
	// before or after the subcommand, and scanning stops at "--" so the
	// game's own arguments are never mistaken for ours.
	tr, rest, err := parseTransport(args)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, transportUsage)
	}
	// The board rides along with the game: `x4mcp play --web 127.0.0.1:8484 --
	// %command%` is the whole companion-app setup, watcher included, and it
	// stops when the game does.
	webAddr, rest, err := parseWeb(rest)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, webUsage)
	}
	// parseTransport keeps the "--"; Steam's %command% expands to the
	// launcher after it.
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return errNoGameCommand
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverErr := make(chan error, 1)
	go func() { serverErr <- runServer(ctx, tr, webAddr) }()

	game := exec.CommandContext(ctx, rest[0], rest[1:]...) //nolint:gosec // the command is the player's own launcher
	game.Stdout, game.Stderr, game.Stdin = os.Stdout, os.Stderr, os.Stdin

	// Steam terminates the launcher, not us, so a signal has to be passed
	// down or the game would be orphaned and keep running.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)

	if err := game.Start(); err != nil {
		return fmt.Errorf("launch game: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- game.Wait() }()

	select {
	case err := <-done:
		fmt.Fprintln(os.Stderr, "x4mcp: game exited, stopping")
		cancel()
		return err
	case s := <-sig:
		if game.Process != nil {
			_ = game.Process.Signal(s)
		}
		<-done
		cancel()
		return nil
	case err := <-serverErr:
		// The server died on its own; the game is still up, so say so
		// rather than exiting quietly and leaving the player wondering
		// why their assistant went blind mid-session.
		fmt.Fprintf(os.Stderr, "x4mcp: server stopped while the game is running: %v\n", err)
		<-done
		return err
	}
}
