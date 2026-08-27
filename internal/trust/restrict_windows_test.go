//go:build windows

package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// The Windows half of TestTheAuthorityKeyIsNotReadableByAnyoneElse, which can
// assert nothing here: a key written 0600 reports 0666 on this platform,
// because access is decided by a list rather than by permission bits. So these
// read the list the code actually set, per T137b.

func TestTheAuthorityKeyIsReachableOnlyByThisUser(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")
	authority, err := Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Issuing writes the second key, so both are in place before the check.
	if _, err := authority.Issue([]string{"192.168.1.20"}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	for _, path := range []string{dir, filepath.Join(dir, caKeyFile), filepath.Join(dir, tlsKeyFile)} {
		flags, _ := daclOf(t, path)

		// Protected, or the entries inherited from the parent directory are
		// merged back in and the list grants more than it names. This is the
		// assertion the whole task rests on.
		if !strings.Contains(flags, "P") {
			t.Errorf("%s: access list is %q, want it protected from inheritance", path, flags)
		}
		assertReachableOnlyByThisUser(t, path)
	}
}

// A key written after the directory was restricted must not arrive with a list
// of its own.
func TestAKeyWrittenLaterIsRestrictedToo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")
	authority, err := Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Reissuing is the ordinary case, not a fallback: leaves are short and the
	// machine's address moves. Each one overwrites the key, so each one has to
	// restrict what it wrote.
	for _, address := range []string{"192.168.1.20", "10.0.0.5"} {
		if _, err := authority.Issue([]string{address}); err != nil {
			t.Fatalf("issue for %s: %v", address, err)
		}
	}

	assertReachableOnlyByThisUser(t, filepath.Join(dir, tlsKeyFile))
}

// An authority left behind by a build that set no list is repaired when trusted
// mode is set up, rather than left as it was found.
func TestAnUnrestrictedAuthorityIsRepairedOnSetup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")
	if _, err := Create(dir); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Widen it the way an unrestricted key effectively is: readable by
	// everyone. Asserted rather than assumed, because a repair test that starts
	// from an already-narrow list passes whether or not anything repairs
	// anything.
	keyPath := filepath.Join(dir, caKeyFile)
	grantEveryone(t, keyPath)
	if _, accounts := daclOf(t, keyPath); !contains(accounts, everyone) {
		t.Fatalf("this test did not manage to widen the list: %v", accounts)
	}

	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatalf("load or create: %v", err)
	}

	assertReachableOnlyByThisUser(t, keyPath)
}

// everyone is how SDDL writes the world group, the entry the repair must remove.
const everyone = "WD"

func assertReachableOnlyByThisUser(t *testing.T, path string) {
	t.Helper()

	me, err := currentUserSID()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}

	// Every entry, not one entry. A directory whose entry is inheritable comes
	// back as two: one applying to the directory itself, one marked
	// inherit-only for its children. Both name this user, which is the
	// property; counting them would be asserting how Windows chose to write the
	// list down.
	_, accounts := daclOf(t, path)
	if len(accounts) == 0 {
		t.Errorf("%s: access list is empty, want this user named on it", path)
	}
	for _, account := range accounts {
		if account != me.String() {
			t.Errorf("%s: access list names %s, want only %s", path, account, me)
		}
	}
}

// grantEveryone puts the world group on the file's list, which is what an
// unrestricted key on a shared machine amounts to.
func grantEveryone(t *testing.T, path string) {
	t.Helper()

	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatalf("world group: %v", err)
	}
	me, err := currentUserSID()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}

	list, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		entryFor(me, windows.GENERIC_ALL),
		entryFor(world, windows.GENERIC_READ),
	}, nil)
	if err != nil {
		t.Fatalf("build a widened list: %v", err)
	}

	err = windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, list, nil,
	)
	if err != nil {
		t.Fatalf("widen %s: %v", path, err)
	}
}

func entryFor(sid *windows.SID, rights uint32) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.ACCESS_MASK(rights),
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// daclOf reads a path's discretionary access list: the flags that follow "D:",
// and the account named by each entry.
//
// Read as SDDL rather than by walking the entries, because the entry count in
// the ACL structure is not exported by x/sys/windows — and because a failure
// then prints something a person can read.
func daclOf(t *testing.T, path string) (flags string, accounts []string) {
	t.Helper()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}

	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read the access list of %s: %v", path, err)
	}
	sddl := descriptor.String()

	at := strings.Index(sddl, "D:")
	if at < 0 {
		t.Fatalf("%s has no discretionary access list: %q", path, sddl)
	}
	body := sddl[at+2:]
	if end := strings.Index(body, "S:"); end >= 0 {
		body = body[:end]
	}

	if open := strings.Index(body, "("); open >= 0 {
		flags, body = body[:open], body[open:]
	} else {
		flags, body = body, ""
	}

	for {
		open := strings.Index(body, "(")
		if open < 0 {
			return flags, accounts
		}
		shut := strings.Index(body[open:], ")")
		if shut < 0 {
			t.Fatalf("%s has an unterminated entry: %q", path, sddl)
		}

		// ace_type;ace_flags;rights;object_guid;inherit_object_guid;account
		fields := strings.Split(body[open+1:open+shut], ";")
		if len(fields) < 6 {
			t.Fatalf("%s has an entry with %d fields, want at least 6: %q", path, len(fields), sddl)
		}
		accounts = append(accounts, fields[5])

		body = body[open+shut+1:]
	}
}
