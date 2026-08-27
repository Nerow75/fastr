//go:build windows

package trust

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Restricting the trust material on Windows, per T137b.
//
// **Windows does not implement POSIX permission bits.** A key written with 0600
// reports 0666, because access here is decided by an access control list that
// os.WriteFile never sets. What the key actually gets is the list it inherits
// from %LOCALAPPDATA%, which is user-only on a default installation — but that
// is the operating system's arrangement rather than a promise fastr makes, and
// this is the most sensitive artefact in the product: anything holding the
// authority's private key can impersonate any site to every phone that
// installed the authority. So it gets a list of its own.
//
// **The list is protected, and that is the part that does the work.** Without
// PROTECTED_DACL_SECURITY_INFORMATION the entries inherited from the parent
// directory are merged back in, and the result grants more than it names. With
// it, the inherited entries are dropped and only the one below survives.
//
// This does not put the key beyond an administrator, and nothing on Windows
// can: an account holding SeTakeOwnershipPrivilege can rewrite any list on the
// machine. What it does remove is every *other* account that happened to be
// named on the parent directory.

// restrictToOwner rewrites path's access list so it names this user and nobody
// else.
func restrictToOwner(path string) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}

	// A directory hands its entry down, so anything written into the trust
	// directory later starts out restricted rather than depending on whoever
	// writes it to remember. A file inherits to nothing.
	inheritance := uint32(windows.NO_INHERITANCE)
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}

	list, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("build the access list for %s: %w", path, err)
	}

	err = windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, list, nil,
	)
	if err != nil {
		return fmt.Errorf("restrict %s to its owner: %w", path, err)
	}
	return nil
}

// currentUserSID is the account this process runs as.
//
// The token here is a pseudo-handle, so it is never closed: closing it would
// close the process token itself.
func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read the account this process runs as: %w", err)
	}
	return user.User.Sid, nil
}
