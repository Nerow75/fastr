package integration

import (
	"sort"
	"strings"
	"testing"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/i18n"
	"github.com/Nerow75/fastr/web"
)

func loadBundle(t *testing.T) *i18n.Bundle {
	t.Helper()
	fsys, err := web.Locales()
	if err != nil {
		t.Fatalf("locales: %v", err)
	}
	b, err := i18n.Load(fsys)
	if err != nil {
		t.Fatalf("load catalogues: %v", err)
	}
	return b
}

// SC-022: every user-facing string exists in both languages, with no
// untranslated string and no raw identifier shown. The catalogues are compared
// here rather than by eye, so adding a key to English without translating it
// fails the build.
func TestEveryLanguageCoversEveryKey(t *testing.T) {
	b := loadBundle(t)

	languages := b.Languages()
	if len(languages) < 2 {
		t.Fatalf("expected at least English and French, got %v", languages)
	}

	reference := b.Keys()
	for _, lang := range languages {
		c := b.Catalogue(lang)
		if c.Lang != lang {
			t.Fatalf("catalogue for %q reported itself as %q, so it fell back", lang, c.Lang)
		}

		var missing []string
		for _, key := range reference {
			if _, ok := c.T(key, nil); !ok {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Errorf("%s is missing %d keys: %v", lang, len(missing), missing)
		}
	}
}

// Every error the catalogue can produce must be renderable. Otherwise a user
// meets a raw key at exactly the moment something has already gone wrong.
func TestEveryErrorCodeHasATranslation(t *testing.T) {
	b := loadBundle(t)

	for _, lang := range b.Languages() {
		c := b.Catalogue(lang)
		for _, key := range app.CatalogueKeys() {
			msg, ok := c.T(key, nil)
			if !ok {
				t.Errorf("%s: no translation for %s", lang, key)
				continue
			}
			if strings.TrimSpace(msg) == "" {
				t.Errorf("%s: %s renders empty", lang, key)
			}
		}
	}
}

// FR-038 and SC-014: an error message names a cause *and* a corrective action.
// A message with no verb telling the user what to do is a code with extra
// words. This checks the shape rather than the prose: at least two sentences,
// or one long enough to carry both.
func TestErrorMessagesCarryACorrectiveAction(t *testing.T) {
	b := loadBundle(t)

	for _, lang := range b.Languages() {
		c := b.Catalogue(lang)
		for _, key := range app.CatalogueKeys() {
			msg, ok := c.T(key, nil)
			if !ok {
				continue
			}
			// "Not found." is the one entry that is a bare statement, and it
			// is only reachable for a resource the user did not ask for.
			if key == "error.not_found" {
				continue
			}
			if len(msg) < 30 {
				t.Errorf("%s/%s is too terse to name a corrective action: %q", lang, key, msg)
			}
		}
	}
}

// FR-039d: a missing key falls back to English rather than rendering blank.
func TestMissingKeyFallsBackToEnglish(t *testing.T) {
	b := loadBundle(t)

	// A key that exists only in the fallback, simulated by asking a real key in
	// a language that does not exist at all.
	got := b.T("kl", "pairing.title", nil)
	want := b.T("en", "pairing.title", nil)
	if got != want {
		t.Errorf("unknown language rendered %q, want the English %q", got, want)
	}
	if got == "" || got == "pairing.title" {
		t.Errorf("fallback produced a blank or a raw key: %q", got)
	}
}

// A key absent everywhere renders as itself, so the gap is visible in
// development instead of producing a sentence with a hole in it.
func TestUnknownKeyRendersAsItself(t *testing.T) {
	b := loadBundle(t)
	if got := b.T("en", "no.such.key", nil); got != "no.such.key" {
		t.Errorf("got %q, want the key itself", got)
	}
}

func TestPlaceholderInterpolation(t *testing.T) {
	b := loadBundle(t)

	got := b.T("en", "error.insufficient_space", map[string]any{
		"needed":    "2.0 GB",
		"available": "1.0 GB",
	})
	for _, want := range []string{"2.0 GB", "1.0 GB"} {
		if !strings.Contains(got, want) {
			t.Errorf("interpolation dropped %q: %s", want, got)
		}
	}
	if strings.Contains(got, "{") {
		t.Errorf("an unfilled placeholder survived: %s", got)
	}
}

// Placeholders must match across languages. A translator who renames {needed}
// produces a sentence with a literal brace in it, which no test of English
// alone would catch.
func TestPlaceholdersMatchAcrossLanguages(t *testing.T) {
	b := loadBundle(t)

	for _, key := range b.Keys() {
		reference := placeholders(b.T("en", key, nil))
		for _, lang := range b.Languages() {
			if lang == "en" {
				continue
			}
			c := b.Catalogue(lang)
			msg, ok := c.T(key, nil)
			if !ok {
				continue
			}
			got := placeholders(msg)
			if !sameSet(reference, got) {
				t.Errorf("%s/%s has placeholders %v, English has %v", lang, key, got, reference)
			}
		}
	}
}

func TestLanguageNegotiation(t *testing.T) {
	b := loadBundle(t)

	cases := []struct {
		header   string
		override string
		want     string
	}{
		{"fr-FR,fr;q=0.9,en;q=0.8", "", "fr"},
		{"en-GB,en;q=0.9", "", "en"},
		{"fr-CA", "", "fr"},                        // regional tag reduces to the base
		{"de,es;q=0.9", "", "en"},                  // nothing available, fall back
		{"", "", "en"},                             // no header at all
		{"fr", "en", "en"},                         // a stated preference outranks the header
		{"en", "fr", "fr"},                         // and in the other direction
		{"fr;q=0, en;q=0.5", "", "en"},             // q=0 means explicitly not French
		{"*", "", "en"},                            // wildcard is the fallback
		{"de", "kl", "en"},                         // an override for a language we lack is ignored
		{"es;q=0.2, fr;q=0.9, en;q=0.8", "", "fr"}, // highest quality wins, not first listed
	}

	for _, tc := range cases {
		if got := b.Negotiate(tc.header, tc.override); got != tc.want {
			t.Errorf("Negotiate(%q, %q) = %q, want %q", tc.header, tc.override, got, tc.want)
		}
	}
}

func placeholders(msg string) []string {
	var out []string
	for {
		open := strings.IndexByte(msg, '{')
		if open < 0 {
			break
		}
		close := strings.IndexByte(msg[open:], '}')
		if close < 0 {
			break
		}
		out = append(out, msg[open+1:open+close])
		msg = msg[open+close+1:]
	}
	sort.Strings(out)
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
