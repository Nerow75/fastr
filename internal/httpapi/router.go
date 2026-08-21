package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/pairing"
	"github.com/Nerow75/fastr/internal/store"
)

// Deps is everything the handlers need.
type Deps struct {
	Log        *slog.Logger
	Store      *store.Store
	Sessions   *pairing.Sessions
	Codes      *pairing.Codes
	Handshakes *pairing.Handshakes
	Pendings   *pairing.Pendings
	Bundle     fs.FS
	Events     *Events
	Tickets    *Tickets
	Transfers  *app.Transfers
	// DeviceName and DeviceID identify this instance to peers.
	DeviceName string
	DeviceID   string
	// Addresses reports where this instance is reachable. It is a function
	// because the listeners are bound after the router is built, and because
	// an interface can come and go while the process runs.
	Addresses func() []string
	// Trusted reports whether a request arrived on the trusted listener. It is
	// a function so the server can answer per connection rather than globally.
	Trusted func(*http.Request) bool
}

// NewRouter builds the request multiplexer.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated by design. /connect carries no user data: a name, a
	// protocol version, and an identifier, all of which the mDNS record already
	// broadcasts to anyone on the network.
	mux.HandleFunc("GET /connect", d.handleConnect)
	mux.HandleFunc("GET /qr", d.handleQR)

	// Pairing. These cannot require a session, because their purpose is to
	// create one. They are protected by the code and the handshake instead.
	mux.HandleFunc("POST /api/pair/init", d.handlePairInit)
	mux.HandleFunc("POST /api/pair/confirm", d.handlePairConfirm)
	mux.HandleFunc("GET /api/pair/status", d.handlePairStatus)

	// Approving a device is the host deciding, so these are restricted to
	// loopback: reaching them means holding a session on this machine, which is
	// the same trust boundary the operating system already enforces and exactly
	// what "a human on the receiving device" means. They deliberately do not
	// require a pairing, because on first run the host's own page has none.
	// The invitation carries a live pairing code, which is the one secret that
	// turns a stranger on the same network into a paired device. Loopback only.
	// The host's own page, which is the machine rather than a peer asking to be
	// let in. Loopback is the same boundary approval already rests on.
	mux.Handle("POST /api/pair/host", loopbackOnly(d, d.handleHostSession))
	mux.Handle("GET /api/pair/invitation", loopbackOnly(d, d.handleInvitation))
	mux.Handle("GET /api/pair/pending", loopbackOnly(d, d.handlePendingList))
	mux.Handle("POST /api/pair/pending/{id}/approve", loopbackOnly(d, d.handlePendingApprove))
	mux.Handle("POST /api/pair/pending/{id}/reject", loopbackOnly(d, d.handlePendingReject))

	// Everything else requires an active pairing. FR-011.
	mux.Handle("GET /api/devices", d.authenticated(d.handleDevices))
	mux.Handle("GET /api/pairings", d.authenticated(d.handlePairings))
	mux.Handle("DELETE /api/pairings/{id}", d.authenticated(d.handleRevoke))
	mux.Handle("PATCH /api/pairings/{id}", d.authenticated(d.handlePairingUpdate))
	mux.Handle("POST /api/events/ticket", d.authenticated(d.handleEventTicket))

	mux.Handle("POST /api/transfers", d.authenticated(d.handleDeclareTransfer))
	mux.Handle("GET /api/transfers/{id}", d.authenticated(d.handleGetTransfer))
	mux.Handle("POST /api/transfers/{id}/cancel", d.authenticated(d.handleCancelTransfer))
	mux.Handle("GET /api/transfers/{id}/waiting", d.authenticated(d.handlePendingSupply))
	mux.Handle("GET /api/transfers/{id}/items/{index}/offset", d.authenticated(d.handleItemOffset))
	mux.Handle("POST /api/transfers/{id}/items/{index}/complete", d.authenticated(d.handleCompleteItem))

	// Bulk content. Not sealed in simple mode: constitution v2.0.1 is explicit
	// that file content travels in the clear there, and the interface says so.
	// These stream, so they must not pass through the envelope middleware,
	// which reads a whole body into memory.
	mux.Handle("POST /api/transfers/{id}/items/{index}/ticket", d.authenticated(d.handleContentTicket))
	// The only route that accepts a scoped ticket instead of a header: a
	// download is performed by the browser's own manager, which cannot set one.
	mux.HandleFunc("GET /api/transfers/{id}/items/{index}/content", d.handleFetchContentTicketed)
	mux.Handle("POST /api/transfers/{id}/items/{index}/supply", d.streaming(d.handleSupplyContent))
	// The phone pushing a file to this computer. Same path as the download, a
	// different method: one streams out of the pipe, the other into a file.
	mux.Handle("POST /api/transfers/{id}/items/{index}/content", d.streaming(d.handleUploadContent))
	// The stream authenticates with a single-use ticket rather than a bearer
	// header, because EventSource cannot set one. See tickets.go.
	mux.HandleFunc("GET /api/events", d.handleEvents)

	// The web application. Served last so it catches everything unmatched.
	mux.Handle("/", assetHandler(d.Bundle))

	return securityWrapper(mux)
}

// loopbackOnly restricts a handler to requests arriving on the loopback
// interface, which is the host itself.
func loopbackOnly(d Deps, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			d.writeError(w, r, app.New(app.CodeUnauthorized))
			return
		}
		h(w, r)
	})
}

// securityWrapper applies headers that hold for every response, including the
// API. The asset handler adds its own content policy on top.
func securityWrapper(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// The API is reached from the page it is served with, and nothing else.
		// No CORS headers means no cross-origin caller, which is the correct
		// posture for a server that only ever talks to its own bundle.
		next.ServeHTTP(w, r)
	})
}

// streaming wraps a handler that reads or writes bulk content.
//
// It authorizes the caller but does not touch the body: the envelope path reads
// a whole payload into memory, which is exactly what a 10 GB transfer must not
// do.
func (d Deps) streaming(h func(*Session, http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trusted := d.Trusted != nil && d.Trusted(r)

		ps, err := d.Sessions.Resolve(r, trusted)
		if err != nil {
			d.writeError(w, r, authError(err))
			return
		}
		h(&Session{Session: ps, deps: d}, w, r)
	})
}

// authenticated wraps a handler that exchanges sealed JSON payloads.
func (d Deps) authenticated(h func(*Session, http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := d.resolve(w, r)
		if !ok {
			return
		}
		h(s, w, r)
	})
}

// Session pairs the resolved identity with the request's sealed body.
type Session struct {
	*pairing.Session
	// Body is the decrypted request payload, empty when there was none.
	Body []byte
	deps Deps
}

// resolve authorizes the request and decrypts its body.
func (d Deps) resolve(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	trusted := d.Trusted != nil && d.Trusted(r)

	ps, err := d.Sessions.Resolve(r, trusted)
	if err != nil {
		d.writeError(w, r, authError(err))
		return nil, false
	}

	body, err := d.openBody(ps, r)
	if err != nil {
		d.writeError(w, r, err)
		return nil, false
	}

	return &Session{Session: ps, Body: body, deps: d}, true
}

// maxControlBody bounds a control-plane payload. Bulk content never travels
// through this path, so a megabyte is generous.
const maxControlBody = 1 << 20

// openBody decrypts a sealed request body.
func (d Deps) openBody(s *pairing.Session, r *http.Request) ([]byte, error) {
	if r.ContentLength == 0 || r.Body == nil {
		return nil, nil
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxControlBody))
	if err != nil {
		return nil, app.Errorf(app.CodeInvalidRequest, err)
	}
	if len(raw) == 0 {
		return nil, nil
	}

	sealed, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(raw)))
	if err != nil {
		return nil, app.Errorf(app.CodeInvalidRequest, err)
	}

	plain, err := s.Envelope.Open(r.Method, r.URL.Path, pairing.ProtocolVersion, sealed)
	if err != nil {
		if errors.Is(err, pairing.ErrReplay) {
			return nil, app.Errorf(app.CodeReplay, err)
		}
		return nil, app.Errorf(app.CodeInvalidRequest, err)
	}
	return plain, nil
}

// writeJSON seals a response payload and writes it.
func (s *Session) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	plain, err := json.Marshal(v)
	if err != nil {
		s.deps.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	sealed, err := s.Envelope.Seal(r.Method, r.URL.Path, pairing.ProtocolVersion, plain)
	if err != nil {
		s.deps.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, base64.StdEncoding.EncodeToString(sealed))
}

// writeError renders a catalogue error.
//
// The body carries a stable code, a translation key, and parameters, never an
// assembled message: the server does not know the reader's language, and
// FR-039a forbids hard-coded user-facing text.
//
// It is written in the clear rather than sealed, because an authorization
// failure has no session to seal with, and a client that cannot decrypt an
// error learns nothing about why it failed.
func (d Deps) writeError(w http.ResponseWriter, r *http.Request, err error) {
	e := app.AsError(err)

	if e.Severe() {
		// The path is included for a severe error because it is where an
		// operator starts looking. The offending value is not: a traversal
		// attempt's payload has no business in a log line.
		d.Log.Warn("request refused",
			"code", string(e.Code), "method", r.Method, "path", r.URL.Path, "error", e)
	} else {
		d.Log.Debug("request refused", "code", string(e.Code), "path", r.URL.Path)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(e.Status())
	_ = json.NewEncoder(w).Encode(e.Body())
}

// writePlainJSON writes an unsealed payload, for endpoints that have no session
// by definition.
func (d Deps) writePlainJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// authError maps an authorization failure to its catalogue code.
//
// The distinctions matter: FR-038 requires a corrective action, and "pair the
// device again" is different advice from "ask the owner to approve it".
func authError(err error) error {
	switch {
	case errors.Is(err, pairing.ErrTrustedOnly):
		return app.Errorf(app.CodeTrustedRequired, err)
	case errors.Is(err, store.ErrPairingRevoked):
		return app.Errorf(app.CodePairingRevoked, err)
	case errors.Is(err, store.ErrPairingExpired):
		return app.Errorf(app.CodePairingExpired, err)
	case errors.Is(err, pairing.ErrNoCredential), errors.Is(err, pairing.ErrBadCredential):
		return app.Errorf(app.CodeUnauthorized, err)
	default:
		return app.Errorf(app.CodeUnauthorized, err)
	}
}
