// Package web carries the built web application, compiled into the binary.
//
// Principle I: every byte the browser loads comes from here. Nothing is fetched
// from a CDN, a font service, or an icon host at runtime.
package web

import (
	"embed"
	"errors"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// FS returns the built bundle rooted at its top level.
//
// It fails rather than serving an empty tree when the bundle is absent, so a
// binary built without `make web` never reaches a user.
func FS() (fs.FS, error) {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, errors.New("web bundle is missing index.html: run `make web` before building")
	}
	return sub, nil
}
