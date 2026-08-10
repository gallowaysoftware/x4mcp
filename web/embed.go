// Package web carries the compiled x4cue board (Svelte 5 + Vite) into the Go
// binary. dist/ is committed on purpose: `go build ./...` from a fresh clone
// with nothing but a Go toolchain produces the complete product, and a broken
// Node toolchain can then cost the ability to *change* the UI but never the
// ability to build and run it.
//
// After editing anything under web/src, run `npm run build` in web/ and commit
// the result — CI rebuilds and fails on `git diff -- web/dist`.
package web

import (
	"embed"
	"io/fs"
)

// The all: prefix keeps files that start with "." or "_" (Vite emits none
// today, but a stray one silently missing from the binary is a bad surprise).
//
//go:embed all:dist
var dist embed.FS

// Dist is the built app rooted at its index.html, ready for http.FileServerFS.
var Dist = mustSub("dist")

func mustSub(dir string) fs.FS {
	sub, err := fs.Sub(dist, dir)
	if err != nil {
		// Unreachable: dir is a compile-time constant that go:embed validated.
		panic("web: embedded " + dir + " unreadable: " + err.Error())
	}
	return sub
}
