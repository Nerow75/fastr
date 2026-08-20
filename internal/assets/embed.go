// Package assets carries the built web application, compiled into the binary.
//
// Principle I: every byte the browser loads comes from here. Nothing is fetched
// from a CDN, a font service, or an icon host at runtime.
//
// The built bundle lives under internal/ rather than beside the web sources
// because a go:embed directive cannot reference a parent directory, so the Go
// file has to sit at or above dist. Putting it in web/ would place a Go package
// in the same tree as node_modules, and `go build ./...` would then compile,
// test, and lint whatever Go code an npm dependency happens to ship.
package assets

import (
	"errors"
	"io/fs"

	"embed"
)

//go:embed all:dist
var bundle embed.FS

// ErrNoBundle is returned when the web application has not been built.
var ErrNoBundle = errors.New("web bundle is missing: run `make web` before starting")

// FS returns the built bundle rooted at its top level.
//
// The directory is committed with a placeholder so a bare `go build` succeeds,
// which keeps the contributor experience honest: the failure arrives at startup
// with an actionable message rather than as an obscure embed error.
func FS() (fs.FS, error) {
	sub, err := fs.Sub(bundle, "dist")
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, ErrNoBundle
	}
	return sub, nil
}
