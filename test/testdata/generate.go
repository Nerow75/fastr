//go:build ignore

// Command generate writes a large, incompressible fixture for transfer tests.
//
// Fixtures are never committed. `make fixture SIZE=10G` produces one locally,
// and test/testdata/.gitignore keeps it out of the repository.
package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	size := flag.String("size", "1G", "fixture size, for example 512M or 10G")
	out := flag.String("out", "test/testdata/large", "output directory")
	flag.Parse()

	bytes, err := parseSize(*size)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "generate: %v\n", err)
		os.Exit(1)
	}

	path := filepath.Join(*out, strings.ToUpper(*size)+".bin")
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	// Random content, so a capture test cannot pass by accident on a run of
	// zeroes and so compression never flatters the throughput measurement.
	if _, err := io.CopyN(f, rand.Reader, bytes); err != nil {
		fmt.Fprintf(os.Stderr, "generate: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("wrote %s (%d bytes)\n", path, bytes)
}

func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}

	multiplier := int64(1)
	switch {
	case strings.HasSuffix(s, "G"):
		multiplier, s = 1<<30, strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		multiplier, s = 1<<20, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "K"):
		multiplier, s = 1<<10, strings.TrimSuffix(s, "K")
	}

	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad size %q", s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("size must be positive")
	}
	return n * multiplier, nil
}
