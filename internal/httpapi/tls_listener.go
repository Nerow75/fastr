package httpapi

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
)

// The trusted-mode listener, per FR-047a and T130.
//
// It runs **alongside** the plain one rather than replacing it, and that is the
// whole shape of trusted mode: it is opt-in, per device, and abandoning it must
// leave everything that already worked still working (FR-047d).
//
// A phone that has installed the authority reaches the HTTPS port and gets a
// secure context, `crypto.subtle`, a service worker, and streamed decryption. A
// phone that has not reaches the plain port exactly as before. Neither knows or
// cares about the other, and a computer where nobody has ever set trusted mode
// up serves no TLS at all.
//
// The two ports are separate rather than one port sniffing the first bytes.
// Sniffing works, and it is one more thing that can be wrong on a protocol
// whose whole job is to be trustworthy; two listeners is boring and legible.

// TrustedPortOffset is how far the TLS port sits from the plain one.
//
// Derived rather than configured, so the address a phone is shown for trusted
// mode follows from the one it already knows. A second setting would be a
// second thing to get wrong, and a second thing to explain.
const TrustedPortOffset = 1

// StartTrusted binds the TLS listeners beside the plain ones.
//
// Called only when an authority exists and a certificate has been issued.
// Failing here does not stop the server: trusted mode is an addition, and a
// machine that cannot bind the second port still transfers files exactly as it
// did before. The caller logs and carries on.
func (s *Server) StartTrusted(certificate tls.Certificate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return errors.New("the server is not listening")
	}
	if s.trustedSrv != nil {
		return errors.New("trusted mode is already listening")
	}

	srv := &http.Server{
		Handler:           s.router,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{certificate},
			// TLS 1.2 is the floor a browser on an older phone can reach; below
			// it there is nothing worth serving. iOS and Android have both
			// supported 1.3 for years, and Go negotiates it when it can.
			MinVersion: tls.VersionTLS12,
		},
	}

	var listeners []net.Listener
	var bound []string

	for _, plain := range s.addrs {
		host, port, err := net.SplitHostPort(plain)
		if err != nil {
			continue
		}
		number, err := trustedPort(port)
		if err != nil {
			continue
		}

		hostPort := net.JoinHostPort(host, fmt.Sprint(number))
		ln, err := tls.Listen("tcp", hostPort, srv.TLSConfig)
		if err != nil {
			for _, open := range listeners {
				_ = open.Close()
			}
			return fmt.Errorf("listen on %s: %w", hostPort, err)
		}
		listeners = append(listeners, ln)
		bound = append(bound, ln.Addr().String())
	}

	if len(listeners) == 0 {
		return errors.New("no address to serve trusted mode on")
	}

	s.trustedSrv = srv
	s.trustedListeners = listeners
	s.trustedAddrs = bound

	for _, ln := range listeners {
		go func(ln net.Listener) {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.log.Error("trusted listener stopped", "addr", ln.Addr().String(), "error", err)
			}
		}(ln)
	}

	s.log.Info("trusted mode listening", "addresses", bound)
	return nil
}

// TrustedAddresses returns where trusted mode answers, or nothing when it is
// not set up. The interface shows these during setup and nowhere else.
func (s *Server) TrustedAddresses() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.trustedAddrs...)
}

// TrustedRequest reports whether a request arrived over the TLS listener.
//
// The one question everything about trusted mode rests on, and it is answered
// from the connection rather than from anything the client said: a header a
// phone could set must never be able to claim a protection it does not have.
func TrustedRequest(r *http.Request) bool { return r.TLS != nil }

// trustedPort derives the TLS port from the plain one.
func trustedPort(plain string) (int, error) {
	number, err := net.LookupPort("tcp", plain)
	if err != nil {
		return 0, err
	}
	trusted := number + TrustedPortOffset
	if trusted > 65535 {
		return 0, fmt.Errorf("no port available above %d", number)
	}
	return trusted, nil
}
