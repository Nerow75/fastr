// Command fastr runs the local network file transfer server and its interface.
package main

import (
	"fmt"
	"os"
)

// version is set at build time from the git description. See the Makefile.
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "fastr: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Phase 2 wires configuration, the store, the server, discovery, and the tray.
	// Until then this proves the module builds and the bundle embeds.
	if len(args) > 0 && args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	fmt.Printf("fastr %s\n", version)
	return nil
}
