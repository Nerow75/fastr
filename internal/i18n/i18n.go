// Package i18n loads translation catalogues and negotiates a language.
//
// The HTTP API never returns an assembled message: it returns a translation key
// and parameters, and the browser renders them (contracts/http-api.md). This
// package exists for the surfaces the browser cannot reach, which are native and
// must still be translated: the tray menu, desktop notifications, and messages
// printed before any browser is connected.
//
// The catalogues are the same JSON files the web application uses, embedded so
// that a translator edits one file and both surfaces follow. FR-039a.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// The catalogues are authored here and imported by the web application from
// this directory, so there is exactly one copy of each translation. A
// translator edits one file and both the native surfaces and the browser
// follow. FR-039a.
//
//go:embed locales/*.json
var catalogues embed.FS

// DefaultLanguage is the fallback. A missing translation renders in English
// rather than showing a blank or a raw key, per FR-039d.
const DefaultLanguage = "en"

// Catalogue is one language's messages.
type Catalogue struct {
	Lang     string
	messages map[string]string
}

// Bundle holds every available catalogue.
type Bundle struct {
	mu       sync.RWMutex
	byLang   map[string]*Catalogue
	fallback *Catalogue
}

// Load reads every embedded catalogue.
func Load() (*Bundle, error) {
	fsys, err := fs.Sub(catalogues, "locales")
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read locales: %w", err)
	}

	b := &Bundle{byLang: make(map[string]*Catalogue, len(entries))}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		lang := strings.TrimSuffix(e.Name(), ".json")

		data, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var messages map[string]string
		if err := json.Unmarshal(data, &messages); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		b.byLang[lang] = &Catalogue{Lang: lang, messages: messages}
	}

	fallback, ok := b.byLang[DefaultLanguage]
	if !ok {
		return nil, fmt.Errorf("missing the %s catalogue, which is the fallback", DefaultLanguage)
	}
	b.fallback = fallback

	return b, nil
}

// Languages returns the available language tags, sorted.
func (b *Bundle) Languages() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([]string, 0, len(b.byLang))
	for lang := range b.byLang {
		out = append(out, lang)
	}
	sort.Strings(out)
	return out
}

// Keys returns every key defined in the fallback catalogue. The coverage test
// compares the others against it.
func (b *Bundle) Keys() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([]string, 0, len(b.fallback.messages))
	for k := range b.fallback.messages {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Catalogue returns the catalogue for a language tag, falling back to English.
func (b *Bundle) Catalogue(lang string) *Catalogue {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if c, ok := b.byLang[normalize(lang)]; ok {
		return c
	}
	return b.fallback
}

// Negotiate picks a language from an Accept-Language header, honouring quality
// values, and falls back to English when nothing matches.
//
// An explicit user preference overrides the header entirely: FR-039b gives the
// user a manual override, and a header cannot outrank a stated choice.
func (b *Bundle) Negotiate(header, override string) string {
	if override != "" {
		if _, ok := b.byLang[normalize(override)]; ok {
			return normalize(override)
		}
	}

	type candidate struct {
		lang string
		q    float64
	}
	var candidates []candidate

	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lang, q := part, 1.0
		if i := strings.Index(part, ";"); i >= 0 {
			lang = strings.TrimSpace(part[:i])
			for _, param := range strings.Split(part[i+1:], ";") {
				param = strings.TrimSpace(param)
				if v, ok := strings.CutPrefix(param, "q="); ok {
					if parsed, err := strconv.ParseFloat(v, 64); err == nil {
						q = parsed
					}
				}
			}
		}
		// "*" means "anything", which the fallback already covers.
		if lang == "*" {
			continue
		}
		candidates = append(candidates, candidate{normalize(lang), q})
	}

	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].q > candidates[j].q })

	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, c := range candidates {
		if c.q <= 0 {
			continue // q=0 means "explicitly not this one"
		}
		if _, ok := b.byLang[c.lang]; ok {
			return c.lang
		}
	}
	return DefaultLanguage
}

// normalize reduces "fr-CA" to "fr". Regional catalogues can be added later by
// keying on the full tag; until then a French speaker in Canada gets French
// rather than English.
func normalize(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		return tag[:i]
	}
	return tag
}

// T renders a message, interpolating {name} placeholders.
//
// A key absent from this catalogue is *not* rendered as a blank or as the raw
// key: the caller resolves it against the fallback. See Bundle.T.
func (c *Catalogue) T(key string, params map[string]any) (string, bool) {
	msg, ok := c.messages[key]
	if !ok {
		return "", false
	}
	return interpolate(msg, params), true
}

// T renders a message in the requested language, falling back to English, and
// finally to the key itself so a missing translation is visible in development
// rather than silently blank. FR-039d.
func (b *Bundle) T(lang, key string, params map[string]any) string {
	if msg, ok := b.Catalogue(lang).T(key, params); ok {
		return msg
	}
	b.mu.RLock()
	fallback := b.fallback
	b.mu.RUnlock()

	if msg, ok := fallback.T(key, params); ok {
		return msg
	}
	return key
}

// interpolate replaces {name} with the matching parameter. An unmatched
// placeholder is left alone rather than blanked, so a translator's typo is
// visible instead of producing a sentence with a hole in it.
func interpolate(msg string, params map[string]any) string {
	if len(params) == 0 || !strings.ContainsRune(msg, '{') {
		return msg
	}
	for name, value := range params {
		msg = strings.ReplaceAll(msg, "{"+name+"}", fmt.Sprint(value))
	}
	return msg
}
