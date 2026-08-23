package integration

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/Nerow75/fastr/internal/app"
)

// SC-014 and FR-038: every failure names a cause and a concrete corrective
// action, never a bare code.
//
// The rule is easy to agree with and easy to erode. Nobody adds an error
// message that says "internal error 47"; what happens is that somebody adds a
// *code* in a hurry, means to write the message later, and the interface starts
// rendering the identifier — which is what `t()` does with a key it does not
// have. So the checks here run in both directions:
//
//   - every code the server can return has a message, in every language;
//   - every message tells the person something to **do**, not only what
//     happened;
//   - no message leaks the code, an identifier, or a path.
//
// The corrective-action check is a heuristic and says so. It looks for an
// imperative, which is what a corrective action is in both languages this ships
// in. A message can pass it and still be unhelpful; nothing mechanical can rule
// that out, and the alternative — no check at all — lets "something went wrong"
// through without comment.

// Imperatives that make a sentence an instruction rather than a description.
//
// Matched at the **start of a sentence**, which is where an imperative lives in
// both languages this ships in. Matching anywhere was tried first and passed
// two messages that instruct nobody: "It may have finished or been cleared"
// contains "finish", and "It will start on its own" contains "start". A false
// pass is worse than a false failure here, because nobody looks at it again.
//
// Maintained by hand, which is the cost. A message written in a form none of
// these cover fails, and the fix is either to rewrite it or to add the verb
// having decided it really is an instruction.
var correctiveVerbs = map[string][]string{
	"en": {
		"check", "try", "free", "open", "send", "ask", "reload", "start",
		"connect", "pair", "wait", "choose", "finish", "make", "close",
		"restart", "remove", "type", "enter", "go", "rename", "set", "install",
		"turn", "give",
	},
	"fr": {
		"vérifiez", "réessayez", "libérez", "ouvrez", "renvoyez", "renommez",
		"demandez", "rechargez", "recommencez", "connectez", "appairez",
		"patientez", "choisissez", "terminez", "assurez-vous", "fermez",
		"redémarrez", "retirez", "saisissez", "allez", "envoyez", "reconfigurez",
		"attendez", "installez", "activez", "regardez", "consultez",
	},
}

// sentences splits a message the way a reader would.
func sentences(message string) []string {
	parts := strings.FieldsFunc(message, func(r rune) bool {
		return r == '.' || r == '!' || r == '?'
	})

	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// instructs reports whether any sentence begins with a corrective verb.
func instructs(message string, verbs []string) bool {
	for _, sentence := range sentences(message) {
		lower := strings.ToLower(sentence)
		for _, verb := range verbs {
			if regexp.MustCompile(`^` + regexp.QuoteMeta(verb) + `\b`).MatchString(lower) {
				return true
			}
		}
	}
	return false
}

// Messages with no instruction in them, and why each is right not to have one.
//
// An allow list rather than a threshold, so adding to it is a decision somebody
// writes down rather than a message quietly slipping under a length limit.
// Two kinds end up here, and neither is a message that gave up:
//
//   - nothing is being asked of the user because nothing can be;
//   - nothing is being asked because it is already handled, and the message
//     says so. "Try again" on a transfer that is already retrying would be
//     worse than silence.
var statementsWithoutAnAction = map[string]string{
	// Reachable only for a resource the user did not ask for, where there is
	// nothing for them to do.
	"error.not_found": "an address nobody navigated to",
	// The other device's answer, and the whole content of it. Suggesting a
	// remedy for somebody else's decision would be putting words in their mouth.
	"error.transfer_declined": "a person on the other device said no",
	// Handled: the transfer is queued and will run. Asking the user to do
	// something about a queue they are already in is noise.
	"error.queue_busy": "already queued, and the message says so",
	// Handled: the sender corrects itself from the offset in the reply. The
	// user never sees this unless they are looking at a log.
	"error.offset_mismatch": "corrected automatically, and the message says so",
	// Handled: the transfer resumes when a relay is back, which is the whole
	// of FR-057. There is nothing for either phone's owner to do about a third
	// person's laptop.
	"error.relay_unavailable": "resumes on its own when the relay returns",
	// Nothing to do: the transfer is gone, and the message says the two
	// reasons it might be. Telling somebody to retry a transfer that finished
	// would be worse than saying nothing.
	"error.transfer_not_found": "there is no transfer left to act on",
	// Waiting on a person at the other end. The sender cannot hurry them, and
	// the transfer starts by itself the moment they answer.
	"error.awaiting_acceptance": "waiting on somebody else, and it starts itself",
	// Waiting on the other phone's upload. Nobody here can do anything except
	// wait, and the message says how long it is likely to be.
	"error.awaiting_relay": "waiting on another device's upload",
}

func TestEveryErrorTellsTheUserWhatToDo(t *testing.T) {
	bundle := loadBundle(t)

	for _, lang := range bundle.Languages() {
		verbs, known := correctiveVerbs[lang]
		if !known {
			t.Fatalf("no corrective verbs listed for %q; the check cannot judge this language", lang)
		}
		catalogue := bundle.Catalogue(lang)

		for _, key := range app.CatalogueKeys() {
			message, ok := catalogue.T(key, nil)
			if !ok {
				t.Errorf("%s has no %s", lang, key)
				continue
			}
			if reason, allowed := statementsWithoutAnAction[key]; allowed {
				t.Logf("%s/%s is a bare statement: %s", lang, key, reason)
				continue
			}

			if !instructs(message, verbs) {
				t.Errorf("%s/%s says what happened but not what to do: %q", lang, key, message)
			}
		}
	}
}

// A message that describes only the remedy is as useless as one that describes
// only the failure: "try again" invites the same failure.
func TestEveryErrorSaysWhatHappened(t *testing.T) {
	bundle := loadBundle(t)

	for _, lang := range bundle.Languages() {
		catalogue := bundle.Catalogue(lang)

		for _, key := range app.CatalogueKeys() {
			message, ok := catalogue.T(key, nil)
			if !ok {
				continue
			}
			if _, allowed := statementsWithoutAnAction[key]; allowed {
				continue
			}

			// Two sentences, or one long enough to carry both halves. The
			// shortest honest message in the catalogue is around fifty
			// characters, and anything much under that is a fragment.
			if len(sentences(message)) < 2 && len([]rune(message)) < 60 {
				t.Errorf("%s/%s is one short fragment rather than a cause and a remedy: %q",
					lang, key, message)
			}
		}
	}
}

// FR-038 again, from the other end: nothing a user reads is a code, a path, or
// an identifier.
func TestNoErrorMessageLeaksACodeOrAPath(t *testing.T) {
	bundle := loadBundle(t)

	for _, lang := range bundle.Languages() {
		catalogue := bundle.Catalogue(lang)

		for _, code := range app.Codes() {
			key := "error." + string(code)
			message, ok := catalogue.T(key, nil)
			if !ok {
				continue
			}

			// The code itself. "queue_busy" in a sentence is the bare
			// identifier FR-038 forbids.
			if strings.Contains(strings.ToLower(message), string(code)) {
				t.Errorf("%s/%s contains its own code: %q", lang, key, message)
			}
			// A filesystem path, which is either this machine's business or
			// the sender's, and never something to put in front of a person as
			// an explanation.
			if strings.Contains(message, "/home/") || strings.Contains(message, `C:\`) {
				t.Errorf("%s/%s contains a path: %q", lang, key, message)
			}
			// An HTTP status, which is a fact about a protocol rather than
			// about anything the person did.
			for _, status := range []string{"400", "401", "403", "404", "409", "500"} {
				if strings.Contains(message, status) {
					t.Errorf("%s/%s quotes an HTTP status: %q", lang, key, message)
				}
			}
		}
	}
}

// Every error code the server can construct has a message.
//
// The catalogue table is the source both of the status and of the key, so a
// code added there without a translation is caught by the i18n coverage test.
// What this catches is the other slip: a code used in the source that was never
// added to the table at all, which would render as an empty detail key.
func TestEveryCodeUsedInTheSourceIsInTheCatalogue(t *testing.T) {
	known := map[string]bool{}
	for _, code := range app.Codes() {
		known[string(code)] = true
	}

	// The identifiers, as written: app.CodeQueueBusy rather than "queue_busy".
	declared := declaredCodeNames(t)

	used := map[string]token.Pos{}
	root := repoRoot(t)
	for _, dir := range []string{"internal", "cmd"} {
		collectCodeUses(t, filepath.Join(root, dir), used)
	}

	for name, pos := range used {
		if !declared[name] {
			t.Errorf("app.%s is used at %v but is not declared in the catalogue", name, pos)
		}
	}

	// And nothing is declared that the catalogue table forgot.
	for name := range declared {
		if !declared[name] {
			t.Errorf("app.%s is declared but has no catalogue entry", name)
		}
	}
	if len(known) == 0 {
		t.Fatal("the catalogue is empty")
	}
}

// declaredCodeNames reads the Code constants from the errors file.
func declaredCodeNames(t *testing.T) map[string]bool {
	t.Helper()

	path := filepath.Join(repoRoot(t), "internal", "app", "errors.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse errors.go: %v", err)
	}

	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		ident, ok := spec.Type.(*ast.Ident)
		if !ok || ident.Name != "Code" {
			return true
		}
		for _, name := range spec.Names {
			out[name.Name] = true
		}
		return true
	})
	return out
}

// collectCodeUses finds every app.CodeX referenced under a directory.
func collectCodeUses(t *testing.T, dir string, into map[string]token.Pos) {
	t.Helper()

	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "app" {
				return true
			}
			name := sel.Sel.Name
			if strings.HasPrefix(name, "Code") && len(name) > 4 && unicode.IsUpper(rune(name[4])) {
				into[name] = sel.Pos()
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

// repoRoot finds the module root from this test's own location.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("cwd: %v", err)
	}
	for range 6 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find the module root")
	return ""
}
