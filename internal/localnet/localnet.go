// Package localnet is the local-network boundary, in one place.
//
// Principle I says no request of any kind leaves the local network. Three
// things enforce that, and this package holds two of them:
//
//   - The **address classifier**, which decides what "local" means. It is used
//     to refuse a listening address that is not local (FR-001) and to refuse a
//     destination that is not local.
//   - The **restricted client**, which is the only HTTP client the application
//     is allowed to build. Its dialer checks every address it is handed, so an
//     outbound request to somewhere else fails at the socket rather than
//     succeeding quietly.
//
// The third is `.golangci.yml`, which forbids `http.Get`, `http.DefaultClient`
// and `net.Dial` outright, so the ordinary ways of making a request that skips
// this package are rejected before review.
//
// It lives in its own package rather than inside internal/httpapi because both
// the server and the discovery code need it, and discovery must not depend on
// the HTTP layer it is discovered through.
package localnet

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ErrNotLocal reports a destination outside the local network.
type ErrNotLocal struct{ Address string }

func (e *ErrNotLocal) Error() string {
	return fmt.Sprintf("%s is not on the local network, and fastr never leaves it", e.Address)
}

// IsIP reports whether an address belongs to the local network.
func IsIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// IsAddr reports whether a host:port or bare host is on the local network.
func IsAddr(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A name rather than an address. "localhost" is fine; anything else
		// would need resolution, which is itself a network call.
		return host == "localhost"
	}
	return IsIP(ip)
}

// Client returns an HTTP client that can only reach the local network.
//
// The check is in the dialer rather than in the callers, so it holds for every
// request the client will ever make, including one that followed a redirect to
// somewhere else. Redirects are refused outright for the same reason: a peer
// that answers a probe with a redirect to a public host would otherwise turn
// this client into the outbound request Principle I forbids.
//
// timeout bounds the whole request. Everything this client is used for is a
// small exchange with a machine two metres away, so a long timeout only means
// waiting longer to learn that a device is gone.
func Client(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}

	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return &ErrNotLocal{Address: req.URL.Host}
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if !IsAddr(addr) {
					return nil, &ErrNotLocal{Address: addr}
				}
				return dialer.DialContext(ctx, network, addr)
			},
			// No proxy, ever. A proxy is by definition a third party, and an
			// environment variable is not a reason to send someone's device
			// names through one.
			Proxy:                 nil,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   timeout,
			ExpectContinueTimeout: time.Second,
		},
	}
}
