// Package httpapi serves the web application and the device-to-device API.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"

	"github.com/Nerow75/fastr/internal/localnet"
	"sync"
	"time"
)

// Server owns the listeners.
//
// FR-001: nothing listens until the user starts it. The zero value holds no
// socket, Start binds, and Stop releases. There is no "listen on construction"
// path, because a server that starts itself is a server that listens on a café
// network before anyone decided to.
type Server struct {
	log    *slog.Logger
	router http.Handler

	mu        sync.Mutex
	listeners []net.Listener
	srv       *http.Server
	running   bool
	addrs     []string
}

// Options configures a server.
type Options struct {
	Logger *slog.Logger
	Bundle fs.FS
	Router http.Handler
}

// New builds a server. It binds nothing.
func New(opts Options) *Server {
	return &Server{log: opts.Logger, router: opts.Router}
}

// Timeouts. Read and idle are bounded; write is not, because a write timeout
// would kill a legitimate multi-hour transfer of a large file, which is the
// whole point of the product.
const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 120 * time.Second
	shutdownGrace     = 5 * time.Second
)

// Start binds to the requested interfaces and begins serving.
//
// An empty interfaces list means every non-loopback address on the local
// network, plus loopback so the desktop view works. Loopback is always included
// because the desktop interface is served there, and a browser treats it as a
// secure context, unlike a LAN address.
func (s *Server) Start(interfaces []string, port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return errors.New("server is already running")
	}

	addrs, err := bindAddresses(interfaces)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Handler:           s.router,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          nil,
	}

	var listeners []net.Listener
	var bound []string

	for _, addr := range addrs {
		hostPort := net.JoinHostPort(addr, fmt.Sprint(port))
		ln, err := net.Listen("tcp", hostPort)
		if err != nil {
			// Release whatever succeeded, so a partial bind never leaves the
			// machine listening on some interfaces and not others.
			for _, open := range listeners {
				_ = open.Close()
			}
			return fmt.Errorf("listen on %s: %w", hostPort, err)
		}
		// Port 0 asks the operating system to choose; every later listener must
		// use the same one, or the QR code would only be right for one address.
		if port == 0 {
			port = ln.Addr().(*net.TCPAddr).Port
		}
		listeners = append(listeners, ln)
		bound = append(bound, ln.Addr().String())
	}

	if len(listeners) == 0 {
		return errors.New("no usable network interface found")
	}

	s.srv = srv
	s.listeners = listeners
	s.addrs = bound
	s.running = true

	for _, ln := range listeners {
		go func(ln net.Listener) {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.log.Error("listener stopped", "addr", ln.Addr().String(), "error", err)
			}
		}(ln)
	}

	s.log.Info("listening", "addresses", bound)
	return nil
}

// Stop shuts the server down, waiting briefly for in-flight requests.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	srv, running := s.srv, s.running
	s.srv, s.listeners, s.addrs, s.running = nil, nil, nil, false
	s.mu.Unlock()

	if !running {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, shutdownGrace)
	defer cancel()

	err := srv.Shutdown(ctx)
	s.log.Info("stopped listening")
	return err
}

// Running reports whether the server currently holds a socket. FR-050 requires
// the user to always be able to tell.
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Addresses returns the bound addresses, for display and for the QR code.
func (s *Server) Addresses() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.addrs...)
}

// bindAddresses resolves the interfaces to bind to.
//
// Principle I is about outbound traffic, but the inbound side matters too: a
// global bind on a machine with a public address would expose the server to
// the internet. Only loopback and private addresses are ever used.
func bindAddresses(interfaces []string) ([]string, error) {
	if len(interfaces) > 0 {
		for _, addr := range interfaces {
			ip := net.ParseIP(addr)
			if ip == nil {
				return nil, fmt.Errorf("not an IP address: %q", addr)
			}
			if !localnet.IsIP(ip) {
				return nil, fmt.Errorf(
					"refusing to bind %s: it is not a local network address", addr)
			}
		}
		return interfaces, nil
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("enumerate interfaces: %w", err)
	}

	out := []string{"127.0.0.1"}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		ip := ipnet.IP
		if ip.To4() == nil {
			continue // IPv4 only for now; the QR code has to stay short
		}
		if !localnet.IsIP(ip) {
			continue
		}
		out = append(out, ip.String())
	}
	return out, nil
}
