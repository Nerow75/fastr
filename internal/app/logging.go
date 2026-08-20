// Package app holds the orchestration layer: logging, the error catalogue, and
// the services that coordinate the store, the server, and discovery.
package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// FR-019 and constitution Principle V: secrets must never appear in logs, error
// messages, or the interface. "Must never" is not achievable by remembering, so
// this file makes it structural in three layers, each catching what the others
// miss:
//
//  1. Secret, a type that cannot render its own value. Anything held in one is
//     safe wherever it goes, including fmt verbs and JSON.
//  2. Key-based redaction, so an attribute named "token" is redacted even when
//     someone passed a bare string.
//  3. A scrubber holding the live secret values, which replaces any occurrence
//     in a rendered message. This is the layer that catches a secret smuggled
//     into a formatted string, which the other two cannot see.

// Redacted is what a secret renders as, everywhere.
const Redacted = "[redacted]"

// Secret wraps a value that must never be rendered.
//
// It implements Stringer, LogValuer, and json.Marshaler, so there is no
// remaining path by which the underlying value reaches output. Reveal is the
// only way out, and it is deliberately noisy to read at a call site.
type Secret struct {
	value string
}

// NewSecret wraps a sensitive value.
func NewSecret(v string) Secret { return Secret{value: v} }

// NewSecretBytes wraps sensitive key material.
func NewSecretBytes(v []byte) Secret { return Secret{value: string(v)} }

// String renders the placeholder. This is what makes %s and %v safe.
func (s Secret) String() string { return Redacted }

// LogValue renders the placeholder for slog.
func (s Secret) LogValue() slog.Value { return slog.StringValue(Redacted) }

// MarshalJSON renders the placeholder, so a Secret inside a response body or a
// stored record cannot leak either.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + Redacted + `"`), nil }

// Reveal returns the underlying value. Every call site should be obvious.
func (s Secret) Reveal() string { return s.value }

// Empty reports whether there is no value.
func (s Secret) Empty() bool { return s.value == "" }

// sensitiveKeys are attribute names whose values are redacted regardless of
// type. Matching is case-insensitive and by substring, so "session_key" and
// "TokenHash" are both caught.
var sensitiveKeys = []string{
	"token", "key", "secret", "credential", "password",
	"authorization", "auth", "code", "nonce", "cookie", "signature",
}

func isSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range sensitiveKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// Scrubber holds the live secret values so they can be removed from any
// rendered output, whatever path put them there.
//
// It is the backstop: layers 1 and 2 rely on a value being wrapped or an
// attribute being named honestly, and neither survives a caller who writes
// fmt.Sprintf("pairing with %s", code).
type Scrubber struct {
	mu      sync.RWMutex
	secrets map[string]struct{}
}

// NewScrubber returns an empty scrubber.
func NewScrubber() *Scrubber {
	return &Scrubber{secrets: make(map[string]struct{})}
}

// Add registers a live secret. Short values are ignored: registering a 3-digit
// string would blank out unrelated numbers everywhere and make logs useless.
func (s *Scrubber) Add(secret string) {
	if len(secret) < 4 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[secret] = struct{}{}
}

// Remove forgets a secret that is no longer live, such as a consumed pairing
// code, so the set does not grow without bound.
func (s *Scrubber) Remove(secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.secrets, secret)
}

// Scrub replaces every registered secret in the text.
func (s *Scrubber) Scrub(text string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.secrets) == 0 {
		return text
	}
	for secret := range s.secrets {
		if strings.Contains(text, secret) {
			text = strings.ReplaceAll(text, secret, Redacted)
		}
	}
	return text
}

// redactingHandler applies key-based redaction and scrubbing to every record.
type redactingHandler struct {
	inner    slog.Handler
	scrubber *Scrubber
}

// NewLogger returns a logger that cannot emit a registered secret, and its
// scrubber for registering live values.
//
// Callers register a secret the moment it exists and forget it the moment it
// stops being live: see pairing code issuance and session creation.
func NewLogger(w io.Writer, level slog.Level) (*slog.Logger, *Scrubber) {
	scrubber := NewScrubber()
	base := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(&redactingHandler{inner: base, scrubber: scrubber}), scrubber
}

func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, h.scrubber.Scrub(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(h.redact(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

// redact rewrites one attribute, recursing into groups.
func (h *redactingHandler) redact(a slog.Attr) slog.Attr {
	if isSensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}

	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		attrs := v.Group()
		out := make([]any, 0, len(attrs))
		for _, sub := range attrs {
			out = append(out, h.redact(sub))
		}
		return slog.Group(a.Key, out...)
	case slog.KindString:
		return slog.String(a.Key, h.scrubber.Scrub(v.String()))
	default:
		// Anything else is rendered, scrubbed, and compared: a secret reaching
		// a log through a custom Stringer is caught here.
		text := v.String()
		if cleaned := h.scrubber.Scrub(text); cleaned != text {
			return slog.String(a.Key, cleaned)
		}
		return slog.Attr{Key: a.Key, Value: v}
	}
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		redacted = append(redacted, h.redact(a))
	}
	return &redactingHandler{inner: h.inner.WithAttrs(redacted), scrubber: h.scrubber}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name), scrubber: h.scrubber}
}
