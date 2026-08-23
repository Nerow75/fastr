package discovery

import (
	"io"
	"log"
	"log/slog"
	"strings"
)

// Routing the mDNS library's log output into ours.
//
// hashicorp/mdns writes to the standard library logger, and it has things to
// say on an ordinary machine: a host with IPv6 disabled produces "Failed to
// listen to both unicast and multicast on IPv6" on every start. Left alone that
// lands on stderr unformatted, outside the structured logger and outside the
// scrubber that keeps secrets out of it (FR-019).
//
// Nothing it prints is a fastr secret, so this is about discipline rather than
// exposure: one logger, one format, one place where redaction happens. Its
// messages arrive at debug level, because every one of them describes a network
// condition the manual fallback already covers.

// bridge turns lines written to a log.Logger into slog records.
type bridge struct{ log *slog.Logger }

func (b bridge) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	if line != "" {
		b.log.Debug("mdns", "message", line)
	}
	return len(p), nil
}

// libraryLogger adapts a slog logger for the mDNS library, or discards its
// output when there is nowhere to put it.
func libraryLogger(l *slog.Logger) *log.Logger {
	if l == nil {
		return log.New(io.Discard, "", 0)
	}
	return log.New(bridge{log: l}, "", 0)
}
