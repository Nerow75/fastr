package app

import (
	"errors"
	"fmt"
	"net/http"
)

// The error catalogue. Every failure the user can see has an entry here, and
// every entry carries a translation key whose message names a cause *and* a
// corrective action, per FR-038 and SC-014. No user ever sees a bare code.
//
// The wire format is defined in contracts/http-api.md:
//
//	{ "error": "...", "detail_key": "...", "params": { ... } }
//
// Messages are never assembled here. The server does not know the client's
// language, and FR-039a forbids hard-coded user-facing text, so the client
// renders detail_key against its own catalogue with params filled in.

// Code is a stable, machine-readable failure identifier.
type Code string

const (
	// Authorization. Deliberately distinct, because the corrective action
	// differs: re-pair, versus ask the owner to approve again.
	CodeUnauthorized    Code = "unauthorized"
	CodePairingRevoked  Code = "pairing_revoked"
	CodePairingExpired  Code = "pairing_expired"
	CodeTrustedRequired Code = "trusted_mode_required"

	// Pairing.
	CodeInvalidCode   Code = "invalid_pairing_code"
	CodeCodeExpired   Code = "pairing_code_expired"
	CodeCodeExhausted Code = "pairing_code_exhausted"
	CodeRateLimited   Code = "rate_limited"
	CodeReplay        Code = "replay_detected"

	// Transfers.
	CodeInsufficientSpace Code = "insufficient_space"
	CodeChecksumMismatch  Code = "checksum_mismatch"
	CodeOffsetMismatch    Code = "offset_mismatch"
	CodeQueueBusy         Code = "queue_busy"
	CodeTransferNotFound  Code = "transfer_not_found"
	CodeDeclined          Code = "transfer_declined"
	CodeAcceptanceTimeout Code = "acceptance_timeout"
	CodeRelayUnavailable  Code = "relay_unavailable"

	// Requests.
	CodeInvalidPath    Code = "invalid_path"
	CodeInvalidRequest Code = "invalid_request"
	CodeNotFound       Code = "not_found"
	CodeInternal       Code = "internal_error"
)

// entry is what the catalogue knows about a code.
type entry struct {
	status int
	// detailKey indexes the client's translation catalogue.
	detailKey string
	// logLevel hints how noisy this is. A wrong pairing code is routine; an
	// internal error is not.
	severe bool
}

var catalogue = map[Code]entry{
	CodeUnauthorized:    {http.StatusUnauthorized, "error.unauthorized", false},
	CodePairingRevoked:  {http.StatusUnauthorized, "error.pairing_revoked", false},
	CodePairingExpired:  {http.StatusUnauthorized, "error.pairing_expired", false},
	CodeTrustedRequired: {http.StatusUpgradeRequired, "error.trusted_mode_required", false},

	CodeInvalidCode:   {http.StatusUnauthorized, "error.invalid_pairing_code", false},
	CodeCodeExpired:   {http.StatusGone, "error.pairing_code_expired", false},
	CodeCodeExhausted: {http.StatusGone, "error.pairing_code_exhausted", false},
	CodeRateLimited:   {http.StatusTooManyRequests, "error.rate_limited", false},
	CodeReplay:        {http.StatusBadRequest, "error.replay_detected", true},

	CodeInsufficientSpace: {http.StatusConflict, "error.insufficient_space", false},
	CodeChecksumMismatch:  {http.StatusUnprocessableEntity, "error.checksum_mismatch", true},
	CodeOffsetMismatch:    {http.StatusConflict, "error.offset_mismatch", false},
	CodeQueueBusy:         {http.StatusConflict, "error.queue_busy", false},
	CodeTransferNotFound:  {http.StatusNotFound, "error.transfer_not_found", false},
	CodeDeclined:          {http.StatusForbidden, "error.transfer_declined", false},
	CodeAcceptanceTimeout: {http.StatusRequestTimeout, "error.acceptance_timeout", false},
	CodeRelayUnavailable:  {http.StatusServiceUnavailable, "error.relay_unavailable", false},

	CodeInvalidPath:    {http.StatusBadRequest, "error.invalid_path", true},
	CodeInvalidRequest: {http.StatusBadRequest, "error.invalid_request", false},
	CodeNotFound:       {http.StatusNotFound, "error.not_found", false},
	CodeInternal:       {http.StatusInternalServerError, "error.internal", true},
}

// Error is a failure with a stable code and translation parameters.
type Error struct {
	Code   Code
	Params map[string]any
	// cause is for the log only. It never reaches the client, because a
	// message assembled here would be in the wrong language and might carry a
	// path the requester has no business learning.
	cause error
}

// Errorf builds a catalogue error.
func Errorf(code Code, cause error) *Error {
	return &Error{Code: code, cause: cause}
}

// New builds a catalogue error with no underlying cause.
func New(code Code) *Error { return &Error{Code: code} }

// WithParam adds a value for the translated message to interpolate.
//
// Params are rendered into user-facing text, so they must never carry a secret
// or an absolute path. Sizes, counts, and names are the intended use.
func (e *Error) WithParam(key string, value any) *Error {
	if e.Params == nil {
		e.Params = make(map[string]any, 2)
	}
	e.Params[key] = value
	return e
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.cause)
	}
	return string(e.Code)
}

func (e *Error) Unwrap() error { return e.cause }

// Status is the HTTP status for this error.
func (e *Error) Status() int {
	if entry, ok := catalogue[e.Code]; ok {
		return entry.status
	}
	return http.StatusInternalServerError
}

// DetailKey indexes the client's translation catalogue.
func (e *Error) DetailKey() string {
	if entry, ok := catalogue[e.Code]; ok {
		return entry.detailKey
	}
	return "error.internal"
}

// Severe reports whether this warrants a loud log line. A mistyped pairing code
// is routine; a path traversal attempt is not.
func (e *Error) Severe() bool {
	if entry, ok := catalogue[e.Code]; ok {
		return entry.severe
	}
	return true
}

// Body is the response payload, matching contracts/http-api.md.
func (e *Error) Body() map[string]any {
	out := map[string]any{
		"error":      string(e.Code),
		"detail_key": e.DetailKey(),
	}
	if len(e.Params) > 0 {
		out["params"] = e.Params
	}
	return out
}

// AsError extracts a catalogue error from err, or wraps it as an internal one.
// Anything that is not in the catalogue becomes CodeInternal, so an unexpected
// failure never reaches the client as an unstructured string.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Errorf(CodeInternal, err)
}

// CatalogueKeys returns every translation key the catalogue references.
//
// The i18n coverage test uses this to assert that no code can produce a message
// the catalogues cannot render, which is how SC-022 stays true as codes are
// added.
func CatalogueKeys() []string {
	keys := make([]string, 0, len(catalogue))
	for _, e := range catalogue {
		keys = append(keys, e.detailKey)
	}
	return keys
}

// Codes returns every code in the catalogue.
func Codes() []Code {
	out := make([]Code, 0, len(catalogue))
	for c := range catalogue {
		out = append(out, c)
	}
	return out
}
