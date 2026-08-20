package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/Nerow75/fastr/internal/app"
)

// FR-019 and constitution Principle V: keys, tokens, and pairing codes must
// never appear in a log line. This is quality gate 4 in the constitution, so it
// fails the build rather than being a review comment.
//
// The three layers of defence are tested separately, because each catches what
// the others cannot.

func TestSecretCannotRenderItself(t *testing.T) {
	const value = "s3cr3t-session-token-abcdef0123456789"
	s := app.NewSecret(value)

	// Every path by which a value normally reaches output.
	renders := map[string]string{
		"String":      s.String(),
		"Sprintf %s":  sprintf("%s", s),
		"Sprintf %v":  sprintf("%v", s),
		"Sprintf %+v": sprintf("%+v", s),
		"json":        mustJSON(t, s),
		"struct":      sprintf("%v", struct{ Token app.Secret }{s}),
	}

	for name, out := range renders {
		if strings.Contains(out, value) {
			t.Errorf("%s leaked the secret: %s", name, out)
		}
		if !strings.Contains(out, app.Redacted) {
			t.Errorf("%s did not render the placeholder: %s", name, out)
		}
	}

	if s.Reveal() != value {
		t.Error("Reveal must return the underlying value")
	}
}

// Layer 2: an attribute named like a secret is redacted even when the caller
// passed a bare string.
func TestSensitiveAttributeKeysAreRedacted(t *testing.T) {
	var buf bytes.Buffer
	logger, _ := app.NewLogger(&buf, slog.LevelDebug)

	const value = "0123456789abcdef0123456789abcdef"
	for _, key := range []string{
		"token", "token_hash", "session_key", "SessionKey", "api_key",
		"secret", "credential", "password", "authorization", "pairing_code",
		"nonce", "cookie", "signature",
	} {
		buf.Reset()
		logger.Info("attempt", key, value)
		if strings.Contains(buf.String(), value) {
			t.Errorf("attribute %q leaked: %s", key, buf.String())
		}
	}
}

// Layer 3: the backstop. A secret smuggled into a formatted message has no
// honest attribute name and is not wrapped, so only the scrubber can catch it.
func TestScrubberCatchesSecretsInFormattedMessages(t *testing.T) {
	var buf bytes.Buffer
	logger, scrubber := app.NewLogger(&buf, slog.LevelDebug)

	const code = "482915"
	const key = "kEyMaTeRiAl0123456789abcdefABCDEF"
	scrubber.Add(code)
	scrubber.Add(key)

	// The kind of line a hurried caller writes.
	logger.Info("paired device using code " + code)
	logger.Info("handshake complete", "detail", "derived from "+key)
	logger.Info("nested", slog.Group("pairing", "note", "code was "+code))

	out := buf.String()
	for _, secret := range []string{code, key} {
		if strings.Contains(out, secret) {
			t.Errorf("scrubber missed %q in: %s", secret, out)
		}
	}
	if !strings.Contains(out, app.Redacted) {
		t.Errorf("expected redaction markers in: %s", out)
	}
}

// A consumed pairing code stops being live. Forgetting it keeps the scrubber
// set bounded, but must not retroactively expose it in output already written.
func TestScrubberForgetsConsumedSecrets(t *testing.T) {
	var buf bytes.Buffer
	logger, scrubber := app.NewLogger(&buf, slog.LevelDebug)

	const code = "482915"
	scrubber.Add(code)
	logger.Info("issued code " + code)

	before := buf.String()
	if strings.Contains(before, code) {
		t.Fatalf("live secret leaked: %s", before)
	}

	scrubber.Remove(code)
	buf.Reset()
	logger.Info("issued code " + code)
	if !strings.Contains(buf.String(), code) {
		t.Error("Remove did not take effect, so the secret set would grow without bound")
	}
}

// Very short values are not registered: blanking every occurrence of "42"
// would make logs unreadable without protecting anything meaningful.
func TestScrubberIgnoresValuesTooShortToBeSecrets(t *testing.T) {
	scrubber := app.NewScrubber()
	scrubber.Add("42")
	if got := scrubber.Scrub("listening on port 4200"); got != "listening on port 4200" {
		t.Errorf("short value was scrubbed: %q", got)
	}
}

// Realistic key material must not survive a round trip through the logger.
func TestBinaryKeyMaterialIsRedacted(t *testing.T) {
	var buf bytes.Buffer
	logger, scrubber := app.NewLogger(&buf, slog.LevelDebug)

	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i * 7)
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	scrubber.Add(encoded)

	logger.Info("session established", "session_key", encoded, "note", "key="+encoded)

	if strings.Contains(buf.String(), encoded) {
		t.Errorf("key material leaked: %s", buf.String())
	}
}

// Every catalogue error must be renderable, and none may carry a message
// assembled server-side: FR-039a forbids hard-coded user-facing text.
func TestErrorCatalogueIsComplete(t *testing.T) {
	for _, code := range app.Codes() {
		e := app.New(code)

		if e.Status() < 400 || e.Status() > 599 {
			t.Errorf("%s: status %d is not a failure", code, e.Status())
		}
		key := e.DetailKey()
		if key == "" || !strings.HasPrefix(key, "error.") {
			t.Errorf("%s: detail key %q is malformed", code, key)
		}

		body := e.Body()
		if body["error"] != string(code) {
			t.Errorf("%s: body error field is %v", code, body["error"])
		}
		if _, ok := body["message"]; ok {
			t.Errorf("%s: body carries a server-assembled message", code)
		}
	}
}

// An unexpected failure must become a catalogue error rather than reaching the
// client as an unstructured string that might carry a path or a secret.
func TestUnknownErrorsBecomeInternal(t *testing.T) {
	e := app.AsError(errSentinel{})
	if e.Code != app.CodeInternal {
		t.Errorf("code = %s, want %s", e.Code, app.CodeInternal)
	}
	if e.Status() != 500 {
		t.Errorf("status = %d, want 500", e.Status())
	}
	if body := e.Body(); body["detail_key"] != "error.internal" {
		t.Errorf("detail_key = %v", body["detail_key"])
	}
}

type errSentinel struct{}

func (errSentinel) Error() string { return "some internal detail with /home/user/path" }

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}
