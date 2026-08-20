// This directory holds the web application, not Go code.
//
// It exists only as a fence. `go build ./...` walks directories rather than
// packages, so without a nested module here the Go tool would descend into
// node_modules and compile, test, and lint whatever Go code an npm dependency
// happens to ship. One such package is already present transitively.
//
// The built bundle and the translation catalogues both live under internal/,
// so nothing in this module is ever imported.
module github.com/Nerow75/fastr/web

go 1.27
