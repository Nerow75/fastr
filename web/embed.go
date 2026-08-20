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

// Translation catalogues are embedded from source rather than from the built
// bundle, so the Go side and the browser side read the same files. A translator
// edits one JSON file and both surfaces follow. FR-039a.
//
//go:embed src/locales/*.json
var locales embed.FS

// Locales returns the translation catalogues, rooted at the directory holding
// them.
func Locales() (fs.FS, error) {
	return fs.Sub(locales, "src/locales")
}

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
